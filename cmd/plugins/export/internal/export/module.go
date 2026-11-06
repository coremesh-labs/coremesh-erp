// Package export ist das Modul „Sichten“: Dokumente nach festgelegten
// Kriterien zusammenstellen und an ein Ziel ausgeben.
//
//   - Sichtart (ExportViewType, Katalog je Buchungskreis): Regeln (ExportRule) über
//     die Merkmale und Bezüge im DMS, Gliederung (Ordnermuster), Benennung
//     (Dateimuster) und Reihenfolge,
//   - Sicht (ExportView): Sichtart mit Parametern (Zeitraum, Gebäude, Mietobjekt,
//     Vertrag, Partner, Vorgang), z. B. „Steuerbüro 2025“,
//   - Lauf (ExportRun): die Sicht zu einem Stichtag – Liste der Dokumente
//     (ExportItem), nachbessern (ausschließen mit Grund, Dokument hinzunehmen),
//     Ausgabe mit Inhaltsverzeichnis (CSV) und Manifest (SHA-256),
//   - Ziel (ExportTarget): Verzeichnis auf dem Server, zip oder Objektspeicher (Ablage
//     für die App: verschlüsselt im Format cm1, Teilnehmer mit Rollen, Freigaben je
//     Sicht, Startdateien; siehe store.go, keys.go, participants.go).
//
// Das Modul liest nur: Dokumente kommen über die Dienste des DMS (DmsDocument.list,
// DmsDocument.content) mit den Rechten des Benutzers – wer einen Beleg nicht sehen
// darf, kann ihn nicht exportieren. Regeln, Gliederung, Benennung und Ziele sind
// Kataloge mit Vorschlagswerten (setup-company), keine Fachregel steht im Code.
package export

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/export, console export:…).
const Name = "export"

const (
	viewTypeObject = "ExportViewType"
	ruleObject     = "ExportRule"
	targetObject   = "ExportTarget"
	viewObject     = "ExportView"
	runObject      = "ExportRun"
	itemObject     = "ExportItem"
	setupObject    = "ExportSetup"
	datasetObject  = "ExportDataset"
	markObject     = "ExportMarkRule"
)

const (
	runOpen     = "OPEN"
	runExported = "EXPORTED"
	runDeleted  = "DELETED" // Dataset gelöscht: nur noch Protokoll

	targetDir = "DIRECTORY"
	targetZip = "ZIP"
)

// Module verwaltet Sichten, Läufe und Ziele.
type Module struct {
	db        module.DB
	services  module.Services
	log       *slog.Logger
	set       *crud.Set
	now       func() time.Time // Uhr (Tests)
	cfg       settings
	mu        sync.Mutex // Erneuern (Zeitgeber und von Hand) nacheinander
	keyMu     sync.Mutex // Hauptschlüssel
	masterKey []byte
	stop      chan struct{}
	wg        sync.WaitGroup
}

// settings: Zeitgeber der Datasets (check_minutes < 0 schaltet ihn ab, z. B. in Tests).
type settings struct {
	CheckMinutes int `json:"check_minutes" yaml:"check_minutes"`
	// KeyFile: Hauptschlüssel der Ablage (Standard data/keys/export-master.key, relativ zur Installation)
	KeyFile string `json:"key_file" yaml:"key_file"`
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{now: time.Now}
	m.set = crud.NewSet(m.viewType(), m.rule(), m.markRule(), m.target(), m.view(), m.dataset(), m.run(), m.item(),
		m.role(), m.contentType(), m.participant(), m.participantRole(), m.grant(), m.access(), m.storeFile())
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Sichten", Icon: "icon-layers",
		Description: "Dokumente nach Kriterien zusammenstellen (Steuerbüro, Rechtsstreit …) und exportieren"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Sichten")
	r.Object(setupObject).Customizing().Handle("setupCompany", m.setupCompanyAction).Handle("tick", m.tickAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Sichtarten, Regeln und Ziele eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	m.set.Bind(env.DB)
	if err := env.Config(&m.cfg); err != nil {
		return err
	}
	m.startTimer()
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error {
	if m.stop != nil {
		close(m.stop)
		m.wg.Wait()
		m.stop = nil
	}
	return nil
}

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }

// --- gemeinsame Hilfsfunktionen ----------------------------------------------------

const (
	tText = metamodel.TypeText
	tNum  = metamodel.TypeNumber
	tDate = metamodel.TypeDate
	tBool = metamodel.TypeBoolean
	tArea = metamodel.TypeTextarea
	tSel  = metamodel.TypeSelect
)

var codeRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,23}$`)

var lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
}

var (
	lookupPartner  = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}
	lookupContract = &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupRentObject = &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupBuilding = &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupProcess = &metamodel.Lookup{Object: "Process", ValueField: "process_id", LabelFields: []string{"short_name", "subject"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupFileAccount = &metamodel.Lookup{Object: "DmsFileAccountCompany", ValueField: "number", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupDocType  = &metamodel.Lookup{Object: "DmsDocType", ValueField: "code", LabelFields: []string{"name"}}
	lookupCategory = &metamodel.Lookup{Object: "DmsDocCategory", ValueField: "code", LabelFields: []string{"name"}}
)

// requireWrite: Ändern im Buchungskreis (object.action); Lesen prüft crud.
func requireWrite(ctx context.Context, object, action, cc string) error {
	if action == "get" || action == "list" || userOf(ctx) == "" { // ohne Benutzer: Zeitgeber, Systemanfrage
		return nil
	}
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
	}
	return nil
}

func match(childField, masterField string) map[string]string {
	return map[string]string{"company_code": "company_code", childField: masterField}
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

func toInt(v any) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	case string:
		i, _ := strconv.Atoi(strings.TrimSpace(n))
		return i
	}
	return 0
}

func dateStr(v any) string {
	if v == nil || crud.Str(v) == "" {
		return ""
	}
	d, err := crud.ParseDate(v)
	if err != nil {
		return ""
	}
	return d
}

func dateOrNil(v any) (any, error) {
	if v == nil || strings.TrimSpace(crud.Str(v)) == "" {
		return nil, nil
	}
	d, err := crud.ParseDate(v)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (m *Module) today() string { return m.now().Format(time.DateOnly) }
func (m *Module) stamp() string { return m.now().UTC().Format(time.RFC3339) }

func userOf(ctx context.Context) string { return sdk.CallFromContext(ctx).UserID }

func splitCodes(text string) []string {
	var out []string
	for _, t := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' }) {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// one: erste Zeile einer Abfrage (nil = keine).
func (m *Module) one(ctx context.Context, q string, args ...any) ([]any, error) {
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	return res.Rows[0], nil
}

// recordInput: {id, data} einer Aktion am Datensatz.
type recordInput struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"data"`
}

// splitKey: „2000|EX-2026-0001“ → Buchungskreis, Schlüssel.
func splitKey(id, what string) (string, string, error) {
	cc, key, ok := strings.Cut(id, "|")
	if !ok || cc == "" || key == "" {
		return "", "", crud.Invalid("%s %q: Buchungskreis|Schlüssel erwartet", what, id)
	}
	return cc, key, nil
}

// germanSize: Bytes lesbar (z. B. 1,4 MB).
func germanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return strings.Replace(fmt.Sprintf("%.1f MB", float64(n)/(1<<20)), ".", ",", 1)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d Byte", n)
}
