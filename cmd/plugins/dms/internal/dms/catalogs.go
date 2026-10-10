package dms

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge und Einstellungen: Ablagefristen, Dokumentarten, Archive,
// Einstellungen je Buchungskreis, Prüfregeln.

const secSettings = "Einstellungen"

var (
	refRetention = &crud.Ref{Table: "dms__retention", Column: "code", Label: "Ablagefrist", Object: retentionObject, LabelFields: []string{"name"}, ActiveField: "is_active"}
	refDocType   = &crud.Ref{Table: "dms__doc_type", Column: "code", Label: "Dokumentart", Object: docTypeObject, LabelFields: []string{"name"}, ActiveField: "is_active"}
	refCategory  = &crud.Ref{Table: "dms__doc_category", Column: "code", Label: "Dokumentkategorie", Object: categoryObject, LabelFields: []string{"name"}, ActiveField: "is_active"}
	refArchive   = &crud.Ref{Table: "dms__archive", Column: "code", Label: "Archiv", Object: archiveObject, LabelFields: []string{"name"}, ActiveField: "is_active"}
	refPlan      = &crud.Ref{Table: "dms__plan", Column: "code", Label: "Ablageplan", Object: planObject, LabelFields: []string{"name"}, ActiveField: "is_active"}
	refWorkflow  = &crud.Ref{Table: "dms__workflow", Column: "code", Label: "Workflow", Object: workflowObject, LabelFields: []string{"name"}, ActiveField: "is_active"}

	startOptions = []metamodel.Option{
		{Value: "END_OF_YEAR", Label: "ab Ende des Kalenderjahres des Dokuments"},
		{Value: "DOC_DATE", Label: "ab Dokumentdatum"},
		{Value: "FOREVER", Label: "dauerhaft (kein Löschen)"},
	}
	archiveKinds = []metamodel.Option{{Value: "FILE", Label: "Verzeichnisbaum (Dateisystem)"}}
	eventOptions = []metamodel.Option{
		{Value: "ACTIVATE", Label: "Vertrag aktivieren"},
		{Value: "CONDITION", Label: "Neue Kondition (Zeitscheibe)"},
	}
	codeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
)

// defaultPattern: Muster des Dateinamens (Vorschlag), z. B. 3.0.1:VV:ROBad1_20250110_NOTICE:Stadt-Goerlitz.pdf.
const defaultPattern = "{account}:{company}:{object}_{date}_{doctype}:{partner}"

func checkCode(rec crud.Record, key, what string) error {
	rec[key] = trimUpper(rec[key])
	if !codeRe.MatchString(crud.Str(rec[key])) {
		return crud.Invalid("%s %q: Großbuchstaben, Ziffern, _ und - (höchstens 20 Zeichen)", what, crud.Str(rec[key]))
	}
	return nil
}

func (m *Module) retention() *crud.Entity {
	return &crud.Entity{
		Object: retentionObject, Title: "Ablagefristen", Icon: "icon-clock", Table: "dms__retention", Section: secSettings,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "start", Label: "Beginn der Frist", Type: tSel, Required: true, Listable: true, Options: startOptions},
			{Key: "years", Label: "Jahre", Type: tNum, Listable: true},
			{Key: "months", Label: "Monate", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			if err := checkCode(rec, "code", "Schlüssel"); err != nil {
				return err
			}
			defaults(rec, map[string]any{"years": int64(0), "months": int64(0)})
			if toInt(rec["years"]) < 0 || toInt(rec["months"]) < 0 {
				return crud.Invalid("Jahre und Monate dürfen nicht negativ sein")
			}
			if rec["start"] != "FOREVER" && toInt(rec["years"]) == 0 && toInt(rec["months"]) == 0 {
				return crud.Invalid("Frist ohne Jahre und Monate – für „dauerhaft“ den Beginn „dauerhaft“ wählen")
			}
			return nil
		},
	}
}

func (m *Module) docType() *crud.Entity {
	return &crud.Entity{
		Object: docTypeObject, Title: "Dokumentarten", Icon: "icon-tag", Table: "dms__doc_type", Section: secSettings,
		Keys: []string{"code"}, Order: "sort_order, code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Filters: []string{"category"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "category", Label: "Dokumentkategorie", Type: tText, Required: true, Listable: true, Ref: refCategory},
			{Key: "retention", Label: "Ablagefrist (Vorschlag, wenn die Ablage keine hat)", Type: tText, Listable: true, Ref: refRetention},
			{Key: "ref_kind", Label: "Dokumentart der Verweise an Verträgen, Objekten … (Plugin document)", Type: tText, Lookup: lookupRefKind},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["ref_kind"] = nilIfEmpty(trimUpper(rec["ref_kind"]))
			rec["category"] = trimUpper(rec["category"])
			return checkCode(rec, "code", "Schlüssel")
		},
	}
}

// category: Dokumentkategorie – zu welchem kaufmännischen Objekt ein Dokument
// gehört (Vertrag, Rechnung/Gutschrift, Gebäude/Mietobjekt …); darunter die
// Dokumentarten, mit denen es am Objekt gespeichert wird.
func (m *Module) category() *crud.Entity {
	return &crud.Entity{
		Object: categoryObject, Title: "Dokumentkategorien", Icon: "icon-tags", Table: "dms__doc_category", Section: secSettings,
		Keys: []string{"code"}, Order: "sort_order, code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "arten", Title: "Dokumentarten", Relation: &metamodel.Relation{Object: docTypeObject, ForeignKey: "category",
				Columns: []string{"code", "name", "ref_kind", "retention", "is_active"}}},
			{Key: "objekte", Title: "Kaufmännische Objekte", Relation: &metamodel.Relation{Object: docTypeObjObject, ForeignKey: "category",
				Columns: []string{"object", "label", "partner_field", "date_field", "account_object", "can_create", "is_active"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error { return checkCode(rec, "code", "Schlüssel") },
	}
}

// docTypeObj: kaufmännische Objekte je Dokumentkategorie – Auswahl im Workflow
// (Partner zuerst, wenn partner_field gesetzt), Anlegen im neuen Fenster,
// Übernahme von Partner, Datum, Titel und Ablage (über das Sachkonto).
func (m *Module) docTypeObj() *crud.Entity {
	return &crud.Entity{
		Object: docTypeObjObject, Title: "Kaufmännische Objekte je Dokumentkategorie", Icon: "icon-link", Table: "dms__category_object", Section: secSettings,
		Keys: []string{"category", "object"}, Order: "category, sort_order, object", StatusField: "is_active", Filters: []string{"category"},
		Fields: []crud.Field{
			{Key: "category", Label: "Dokumentkategorie", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refCategory},
			{Key: "object", Label: "Object (z. B. SupplierInvoice, Contract, Building)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "label", Label: "Bezeichnung in der Auswahl", Type: tText, Required: true, Listable: true},
			{Key: "list_action", Label: "Action der Auswahl (Standard list)", Type: tText},
			{Key: "filters", Label: "Weitere Filter der Auswahl (feld=wert; feld=wert)", Type: tText},
			{Key: "partner_field", Label: "Feld mit dem Geschäftspartner (Auswahl nach Partner, Übernahme)", Type: tText, Listable: true, Group: "Übernahme"},
			{Key: "date_field", Label: "Feld mit dem Datum (Übernahme ins Dokumentdatum)", Type: tText, Listable: true, Group: "Übernahme"},
			{Key: "title_field", Label: "Feld mit dem Titel", Type: tText, Group: "Übernahme"},
			{Key: "account_object", Label: "Object mit den Sachkonten (z. B. SupplierInvoiceItem)", Type: tText, Listable: true, Group: "Ablage über Sachkonto"},
			{Key: "account_link", Label: "Verbindungsfeld (z. B. invoice_id)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "account_field", Label: "Feld mit dem Sachkonto (z. B. account_number)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "can_create", Label: "Im Workflow neu anlegen (Link)", Type: tBool, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			for _, k := range []string{"object", "label", "list_action", "filters", "partner_field", "date_field", "title_field", "account_object", "account_link", "account_field"} {
				rec[k] = strings.TrimSpace(crud.Str(rec[k]))
			}
			defaults(rec, map[string]any{"list_action": "list", "can_create": false, "sort_order": int64(0)})
			if (rec["account_object"] == "") != (rec["account_field"] == "") || (rec["account_object"] != "" && rec["account_link"] == "") {
				return crud.Invalid("Sachkonto: Object, Verbindungsfeld und Feld zusammen angeben (oder keins)")
			}
			return nil
		},
	}
}

func (m *Module) archive() *crud.Entity {
	return &crud.Entity{
		Object: archiveObject, Title: "Archive", Icon: "icon-archive", Table: "dms__archive", Section: secSettings,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: archiveKinds},
			{Key: "root", Label: "Wurzelverzeichnis (absoluter Pfad auf dem Server)", Type: tText, Required: true, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			if err := checkCode(rec, "code", "Schlüssel"); err != nil {
				return err
			}
			root := filepath.Clean(strings.TrimSpace(crud.Str(rec["root"])))
			if !filepath.IsAbs(root) {
				return crud.Invalid("Wurzelverzeichnis %q: absoluten Pfad angeben", crud.Str(rec["root"]))
			}
			rec["root"] = root
			a, err := openArchive(crud.Str(rec["kind"]), root)
			if err != nil {
				return err
			}
			return a.Check()
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "check", Label: "Prüfen", Record: true,
				Confirm: "Archiv prüfen (Verzeichnis vorhanden und beschreibbar)?"}, Handle: m.checkArchiveAction},
		},
	}
}

func (m *Module) checkArchiveAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	a, err := m.archiveByCode(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := a.Check(); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Archiv " + in.ID + " ist bereit: " + a.Describe()}}, nil
}

func (m *Module) postingSource() *crud.Entity {
	return &crud.Entity{
		Object: postSourceObject, Title: "Bezugsquellen der Vorerfassung", Icon: "icon-link", Table: "dms__posting_source", Section: secSettings,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "object", Label: "Object mit Verweis auf die Vorerfassung (z. B. SupplierInvoice)", Type: tText, Required: true, Listable: true},
			{Key: "filter_field", Label: "Feld mit der Vorerfassung (z. B. draft_id)", Type: tText, Required: true, Listable: true},
			{Key: "ref_object", Label: "Bezug: Object (z. B. Contract)", Type: tText, Required: true, Listable: true},
			{Key: "key_template", Label: "Bezug: Schlüssel aus Feldern, z. B. {company_code}|{contract_id}", Type: tText, Required: true, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			for _, k := range []string{"object", "filter_field", "ref_object", "key_template"} {
				rec[k] = strings.TrimSpace(crud.Str(rec[k]))
			}
			return checkCode(rec, "code", "Schlüssel")
		},
	}
}

func (m *Module) company() *crud.Entity {
	match := map[string]string{"company_code": "company_code"}
	return &crud.Entity{
		Object: companyObject, Title: "Buchungskreise", Icon: "icon-building", Table: "dms__company", Section: secSettings,
		Keys: []string{"company_code"}, Order: "company_code", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "plan", Label: "Ablageplan", Type: tText, Required: true, Listable: true, Ref: refPlan},
			{Key: "archive", Label: "Archiv", Type: tText, Required: true, Listable: true, Ref: refArchive},
			{Key: "short_name", Label: "Kürzel im Dateinamen ({company}), z. B. VV", Type: tText, Listable: true},
			{Key: "name_pattern", Label: "Muster des Dateinamens", Type: tText, Group: "Dateiname"},
			{Key: "check_postings", Label: "Buchen nur mit abgelegtem Dokument (Prüfregeln Buchung)", Type: tBool, Listable: true, Group: "Prüfungen"},
			{Key: "check_contracts", Label: "Verträge und Konditionen nur mit Dokument (Prüfregeln Vertrag)", Type: tBool, Listable: true, Group: "Prüfungen"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "ablagen", Title: "Ablagen im Buchungskreis", Collapsed: true, Relation: &metamodel.Relation{Object: accountCCObject, ForeignKey: "company_code", Match: match,
				Columns: []string{"number", "retention", "archive", "is_blocked"}}},
			{Key: "eingang", Title: "Eingangsverzeichnisse", Relation: &metamodel.Relation{Object: inboxObject, ForeignKey: "company_code", Match: match,
				Columns: []string{"code", "name", "path", "workflow", "is_active"}}},
			{Key: "buchungsregeln", Title: "Prüfregeln Buchung", Collapsed: true, Relation: &metamodel.Relation{Object: postRuleObject, ForeignKey: "company_code", Match: match,
				Columns: []string{"rule_no", "document_type", "file_accounts", "is_active"}}},
			{Key: "vertragsregeln", Title: "Prüfregeln Vertrag", Collapsed: true, Relation: &metamodel.Relation{Object: contractRuleObj, ForeignKey: "company_code", Match: match,
				Columns: []string{"rule_no", "event", "contract_type", "condition_types", "file_accounts", "is_active"}}},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkCompany,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "activateAccounts", Label: "Ablagen übernehmen", Record: true,
				Confirm: "Alle Ablagen des Ablageplans im Buchungskreis aktivieren (fehlende anlegen)?"}, Handle: m.activateAccountsAction},
		},
	}
}

func (m *Module) checkCompany(_ context.Context, rec, _ crud.Record) error {
	rec["short_name"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["short_name"])))
	p := strings.TrimSpace(crud.Str(rec["name_pattern"]))
	if p == "" {
		p = defaultPattern
	}
	if err := checkPattern(p); err != nil {
		return err
	}
	rec["name_pattern"] = p
	defaults(rec, map[string]any{"check_postings": false, "check_contracts": false})
	return nil
}

func (m *Module) postingRule() *crud.Entity {
	return &crud.Entity{
		Object: postRuleObject, Title: "Prüfregeln Buchung", Icon: "icon-check", Table: "dms__posting_rule", Section: secSettings,
		Keys: []string{"company_code", "rule_no"}, Order: "company_code, rule_no", StatusField: "is_active", Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "rule_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "document_type", Label: "Belegart (leer = alle)", Type: tText, Listable: true, Lookup: lookupLedgerDocType},
			{Key: "file_accounts", Label: "Ablagen des Dokuments (Nummern oder Anfänge, durch Komma; leer = jede)", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["document_type"] = trimUpper(rec["document_type"])
			rec["file_accounts"] = strings.Join(splitList(crud.Str(rec["file_accounts"])), ", ")
			return nil
		},
	}
}

func (m *Module) contractRule() *crud.Entity {
	return &crud.Entity{
		Object: contractRuleObj, Title: "Prüfregeln Vertrag", Icon: "icon-check", Table: "dms__contract_rule", Section: secSettings,
		Keys: []string{"company_code", "rule_no"}, Order: "company_code, rule_no", StatusField: "is_active", Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "rule_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "event", Label: "Ereignis", Type: tSel, Required: true, Listable: true, Options: eventOptions},
			{Key: "contract_type", Label: "Vertragsart (leer = alle)", Type: tText, Listable: true, Lookup: lookupContractType},
			{Key: "condition_types", Label: "Konditionsarten (durch Komma; leer = alle)", Type: tText, Listable: true},
			{Key: "file_accounts", Label: "Ablagen des Dokuments (Nummern oder Anfänge, durch Komma; leer = jede)", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["contract_type"] = trimUpper(rec["contract_type"])
			rec["condition_types"] = strings.ToUpper(strings.Join(splitList(crud.Str(rec["condition_types"])), ", "))
			rec["file_accounts"] = strings.Join(splitList(crud.Str(rec["file_accounts"])), ", ")
			return nil
		},
	}
}

// companyConfig sind die Einstellungen eines Buchungskreises.
type companyConfig struct {
	CC, Plan, Archive, ShortName, Pattern string
	CheckPostings, CheckContracts         bool
}

func (m *Module) companyConfig(ctx context.Context, cc string) (*companyConfig, error) {
	res, err := m.db.Query(ctx, `SELECT plan, archive, short_name, name_pattern, check_postings, check_contracts FROM dms__company
		WHERE company_code = ? AND is_active = ?`, cc, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Buchungskreis %s ist im Dokumentenmanagement nicht eingerichtet (Einstellungen → Buchungskreise)", cc)
	}
	r := res.Rows[0]
	return &companyConfig{CC: cc, Plan: crud.Str(r[0]), Archive: crud.Str(r[1]), ShortName: crud.Str(r[2]), Pattern: crud.Str(r[3]),
		CheckPostings: crud.AsBool(r[4]), CheckContracts: crud.AsBool(r[5])}, nil
}
