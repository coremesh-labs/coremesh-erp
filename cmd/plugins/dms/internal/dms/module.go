// Package dms ist das Dokumentenmanagement des ERP.
//
//   - Ablageplan (DmsFilePlan, DmsFileAccount): hierarchische Ablagen wie ein
//     Kontenplan, für alle Buchungskreise; Dokumente nur in der untersten
//     Ebene. „Ablageplan einlesen …“ übernimmt eine Mindmap (Freemind-XML,
//     Knoten „1.0.0 Grundbuchauszüge“).
//   - Je Buchungskreis (DmsCompany, DmsFileAccountCompany): Ablageplan,
//     Archiv, Kürzel und Muster des Dateinamens, aktivierte Ablagen mit
//     abweichender Frist oder abweichendem Archiv, Prüfungen.
//   - Ablagefristen (DmsRetention) und Dokumentarten (DmsDocType).
//   - Archive (DmsArchive) hinter der Schnittstelle Archive; Art FILE legt je
//     Ebene des Ablageplans ein Verzeichnis „Nummer Name“ an und setzt die
//     Datei auf nur lesbar.
//   - Eingangsverzeichnisse (DmsInbox) mit Vorgaben aus Object und Schlüssel
//     (DmsInboxParam) und einem Workflow; „Einlesen“ übernimmt neue Dateien,
//     „Hochladen …“ legt eine Datei aus dem Browser hinein.
//   - Workflows (DmsWorkflow, DmsWorkflowStep, DmsStepParam, DmsStepOption):
//     für alle Buchungskreise, kopierbar; Schritte für Benutzereingaben und
//     Programme (Texterkennung, Muster, KI-Anfrage, Ablegen).
//   - Dokumente (DmsDocument, DmsDocumentRef): Kopfzeile, Aktionen, Viewer;
//     Suche auch im erkannten Text; Bezüge erscheinen als Verweis (Object
//     Documents) am Vertrag, Gebäude, an der Rechnung …
//   - Prüfungen: Hook ledger.posting (Belegart → abgelegtes Dokument nötig),
//     contract.activate und contract.condition (z. B. Hausgeld nur mit Dokument).
package dms

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

// Name ist der Namensraum des Moduls (/m/dms).
const Name = "dms"

const (
	docObject        = "DmsDocument"
	refObject        = "DmsDocumentRef"
	inboxObject      = "DmsInbox"
	inboxParamObject = "DmsInboxParam"
	planObject       = "DmsFilePlan"
	accountObject    = "DmsFileAccount"
	accountCCObject  = "DmsFileAccountCompany"
	companyObject    = "DmsCompany"
	archiveObject    = "DmsArchive"
	retentionObject  = "DmsRetention"
	docTypeObject    = "DmsDocType"
	docTypeObjObject = "DmsCategoryObject"
	categoryObject   = "DmsDocCategory"
	workflowObject   = "DmsWorkflow"
	stepObject       = "DmsWorkflowStep"
	paramObject      = "DmsStepParam"
	optionObject     = "DmsStepOption"
	postRuleObject   = "DmsPostingRule"
	postSourceObject = "DmsPostingSource"
	contractRuleObj  = "DmsContractRule"
	setupObject      = "DmsSetup"
)

// Status eines Dokuments.
const (
	stInbox     = "INBOX"     // im Eingang, Datei im Eingangsverzeichnis
	stFiled     = "FILED"     // abgelegt, Datei im Archiv (nur lesbar)
	stRemoved   = "REMOVED"   // nach Ablauf der Frist gelöscht
	stDiscarded = "DISCARDED" // verworfen (aus einem Eingang „Vorentscheidung verwerfen“), Datei gelöscht
)

// Module ist das Dokumentenmanagement.
type Module struct {
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
	// run führt externe Programme aus (Texterkennung); in Tests ersetzbar.
	run runner
	// http ruft KI-Dienste auf; in Tests ersetzbar.
	http httpDoer
	// getenv liest den API-Schlüssel eines KI-Dienstes; in Tests ersetzbar.
	getenv func(string) string
	// settings: settings.modules.dms der Plugin-Konfiguration.
	settings settings
	// Hinweise der Texterkennung je Dokument (fehlende Sprachdaten).
	langMu    sync.Mutex
	langNotes map[string]string
}

// settings: Einstellungen des Moduls (Plugin-Konfiguration, nicht in der
// Datenbank – wer Workflows pflegt, soll keine beliebigen Programme auf dem
// Server starten können).
type settings struct {
	// AllowedPrograms: Programme, die Schritte der Texterkennung aufrufen
	// dürfen (Name im Suchpfad oder absoluter Pfad). Leer = pdftotext,
	// ocrmypdf, tesseract.
	AllowedPrograms []string `json:"allowed_programs"`
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{run: execRunner{}, http: defaultHTTP(), getenv: envLookup}
	m.set = crud.NewSet(m.document(), m.documentRef(), m.documentText(), m.inbox(), m.inboxParam(),
		m.plan(), m.fileAccount(), m.fileAccountCompany(), m.company(),
		m.workflow(), m.step(), m.stepParam(), m.stepOption(),
		m.archive(), m.retention(), m.category(), m.docType(), m.docTypeObj(), m.postingRule(), m.postingSource(), m.contractRule())
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Dokumentenverwaltung", Icon: "icon-file",
		Description: "Dokumentenmanagement: Ablageplan, Archive, Eingang, Workflows"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Dokumente")
	m.registerHooks(r)
	r.Object(setupObject).Handle("importPlan", m.importPlanCommand).Handle("scan", m.scanCommand).Handle("setupCompany", m.setupCompanyCommand)
	r.Command(metamodel.CommandDefinition{Name: "import-plan", Object: setupObject, Action: "importPlan",
		Description: "Ablageplan aus einer Mindmap (Freemind-XML) einlesen – fehlende Ablagen anlegen, Namen aktualisieren",
		Params: []metamodel.CommandParam{{Name: "plan", Required: true}, {Name: "name", Description: "Bezeichnung (leer = oberster Knoten)"},
			{Name: "file", Required: true, File: true}}})
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Buchungskreis einrichten: Ablageplan und Archiv zuordnen, alle Ablagen aktivieren",
		Params: []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "plan", Required: true}, {Name: "archive", Required: true},
			{Name: "root", Description: "Wurzelverzeichnis des Archivs (legt das Archiv an, falls es fehlt)"},
			{Name: "short_name", Description: "Kürzel im Dateinamen, z. B. VV"}}})
	r.Command(metamodel.CommandDefinition{Name: "scan", Object: setupObject, Action: "scan",
		Description: "Eingangsverzeichnis einlesen (neue Dateien übernehmen, Workflow starten)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "inbox", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	if err := env.Config(&m.settings); err != nil {
		return err
	}
	m.set.Bind(env.DB)
	m.subscribeHooks(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema {
	return module.Schema{HCL: schemaHCL, Seed: []sdk.SchemaSeed{
		{Table: "dms__retention", Rows: seedRetentions},
		{Table: "dms__doc_category", Rows: seedCategories},
		{Table: "dms__doc_type", Rows: seedDocTypes},
		{Table: "dms__category_object", Rows: seedCategoryObjects},
		{Table: "dms__posting_source", Rows: seedPostingSources},
	}}
}

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
	tSel  = metamodel.TypeSelect
	tArea = metamodel.TypeTextarea
	tFile = metamodel.TypeFile
)

var (
	lookupCC            = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupPartner       = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"search_term", "name1"}}
	lookupLedgerDocType = &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}
	lookupContractType  = &metamodel.Lookup{Object: "ContractType", ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupRefKind = &metamodel.Lookup{Object: "DocumentKind", ValueField: "code", LabelFields: []string{"name"}}
)

// requireWrite: Ändern im Buchungskreis (object.action); Lesen prüft crud.
func requireWrite(ctx context.Context, object, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func today() string { return time.Now().Format(time.DateOnly) }

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var i int64
		fmt.Sscan(strings.TrimSpace(n), &i)
		return i
	}
	return 0
}

// defaults setzt leere Felder auf Standardwerte.
func defaults(rec map[string]any, values map[string]any) {
	for k, v := range values {
		if rec[k] == nil || crud.Str(rec[k]) == "" {
			rec[k] = v
		}
	}
}

// dateOf: Datum als JJJJ-MM-TT (leer bei fehlendem Wert).
func dateOf(v any) string {
	if crud.Str(v) == "" {
		return ""
	}
	d, err := crud.ParseDate(v)
	if err != nil {
		return ""
	}
	return d
}

// companyOf: Buchungskreis aus der Konsole (company als Text oder Zahl) bzw. dem Formular.
func companyOf(payload any) string {
	var in struct {
		Company     any `json:"company"`
		CompanyCode any `json:"company_code"`
		Data        struct {
			CompanyCode any `json:"company_code"`
		} `json:"data"`
	}
	_ = sdk.Decode(payload, &in)
	for _, v := range []any{in.Company, in.CompanyCode, in.Data.CompanyCode} {
		if s := strings.TrimSpace(crud.Str(v)); s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}

// splitList: "4.2, 3.0.1" → ["4.2", "3.0.1"].
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// accountMatches: Ablage number liegt in einer der Ablagen bzw. Anfänge (leer = jede).
func accountMatches(number string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if number == p || strings.HasPrefix(number, p+".") {
			return true
		}
	}
	return false
}
