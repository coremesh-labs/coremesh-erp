// Package process verwaltet Vorgänge – laufende Prozesse mit Fristen und
// nächsten Schritten, damit nichts untergeht:
//
//   - Vorgang (Process) mit Vorgangsart aus dem Katalog je Buchungskreis
//     (Wiedervermietung, Schadensmeldung, Mieter- und Eigentümeranfrage,
//     Eigentümerversammlung aus Sicht der Verwaltung bzw. des Eigentümers …);
//     die Art legt Kürzel, Perspektive, Fristen und die Schrittvorlage fest,
//   - jeder offene Vorgang hat einen Verantwortlichen und eine Wiedervorlage;
//     „wartet auf …“ braucht ein Erinnerungsdatum, der Abschluss eine Erledigungsart,
//   - Schritte mit Frist ab Eröffnung, ab dem vorigen Schritt oder zum Stichtag,
//   - Partner in Rollen, Bezug zu Vertrag, Mietobjekt, Wirtschaftseinheit oder Objekt,
//     Dokumente aus dem DMS, Verlauf (Notiz, Anruf, Mail, Brief, Systemeintrag),
//   - Sichten als Katalog (Filter der Vorgangsliste, z. B. „Meine – heute fällig“),
//   - Zeitgeber: fällige und überschrittene Fristen, Eskalation, ohne Bewegung →
//     Hinweise in der App und Ereignisse,
//   - Ereignisjournal mit laufender Nummer (ProcessEvent, Filter since) für andere
//     Systeme; der Zeitgeber meldet es nach dem Commit an den Event-Dispatcher.
//
// Keine Fachregel steht im Code: Arten, Schritte, Fristen, Status, Rollen und
// Sichten sind Kataloge mit Vorschlagswerten (setup-company).
package process

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/process, console process:…).
const Name = "process"

// setupObject: Service für Einrichten und Zeitgeber.
const setupObject = "ProcessSetup"

// settings: Takt des Zeitgebers (configs/62-process.yaml).
type settings struct {
	CheckMinutes int `json:"check_minutes"` // Fristen prüfen (Standard 60)
	PushSeconds  int `json:"push_seconds"`  // Journal melden (Standard 15)
}

// Module verwaltet die Vorgänge.
type Module struct {
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
	cfg      settings
	now      func() time.Time // Uhr (Tests)

	stop chan struct{}
	wg   sync.WaitGroup
	mu   sync.Mutex // ein Lauf des Zeitgebers bzw. der Meldung zugleich
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
	_ module.Migrator       = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{now: time.Now}
	m.set = crud.NewSet(m.entities()...)
	return m
}

func (m *Module) entities() []*crud.Entity {
	es := []*crud.Entity{m.process(), m.step(), m.partner(), m.entry(), m.notice(), m.event(), m.typeStep(), m.view()}
	for _, e := range es {
		m.withCatalogLabels(e)
	}
	return append(es, m.catalogEntities()...)
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Vorgänge", Icon: "icon-inbox",
		Description: "Wiedervermietung, Schaden, Anfragen, Eigentümerversammlung … mit Fristen, Wiedervorlage und Hinweisen"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Vorgänge")
	r.Object(setupObject).Customizing().Handle("setupCompany", m.setupCompanyAction).Handle("checkDue", m.checkDueAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kataloge der Vorgänge eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
	r.Command(metamodel.CommandDefinition{Name: "check-due", Object: setupObject, Action: "checkDue",
		Description: "Fristen und Wiedervorlagen jetzt prüfen (sonst stündlich): Hinweise und Ereignisse"})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	m.set.Bind(env.DB)
	m.set.Events(env.Services, Name)
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
)

var lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

// catalogLookup: Auswahl aus einem Katalog des Buchungskreises.
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
	lookupEntity = &metamodel.Lookup{Object: "BusinessEntity", ValueField: "entity_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupAsset = &metamodel.Lookup{Object: "Asset", ValueField: "asset_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupProcess = &metamodel.Lookup{Object: processObject, ValueField: "process_id", LabelFields: []string{"short_name", "subject"},
		Filters: map[string]string{"company_code": "company_code"}}
	// Ablagen und Dokumentarten des DMS (Dokumente übernehmen)
	lookupFileAccount = &metamodel.Lookup{Object: "DmsFileAccountCompany", ValueField: "number", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupDocType = &metamodel.Lookup{Object: "DmsDocType", ValueField: "code", LabelFields: []string{"name"}}
)

// access: Lesen je Buchungskreis (Recht Process.read).
func access() *crud.Access {
	return &crud.Access{Object: processObject, Records: true, CompanyCode: "company_code"}
}

// requireWrite: Ändern im Buchungskreis (object.action); Lesen prüft crud.
func requireWrite(ctx context.Context, object, action, cc string) error {
	if action == "get" || action == "list" {
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

// match: Abschnitt mit Unter-Objekten desselben Buchungskreises.
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
		var i int
		fmt.Sscan(strings.TrimSpace(n), &i)
		return i
	}
	return 0
}

// intOrNil: leere Eingabe bleibt leer (nicht 0).
func intOrNil(v any) any {
	if v == nil || strings.TrimSpace(crud.Str(v)) == "" {
		return nil
	}
	return toInt(v)
}

// dateOrNil: Datum (JJJJ-MM-TT) oder nil.
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

// dateStr: Datum aus der Datenbank als JJJJ-MM-TT ("" = leer).
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

// addDays: JJJJ-MM-TT + n Tage.
func addDays(date string, n int) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 0, n).Format(time.DateOnly)
}

// today: heutiges Datum der Uhr des Moduls.
func (m *Module) today() string { return m.now().Format(time.DateOnly) }

func (m *Module) stamp() string { return m.now().UTC().Format(time.RFC3339) }

func userOf(ctx context.Context) string { return sdk.CallFromContext(ctx).UserID }

func splitCodes(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToUpper(text), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

func setLabel(rec crud.Record, key, label string) {
	labels, _ := rec["_labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
		rec["_labels"] = labels
	}
	labels[key] = label
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
