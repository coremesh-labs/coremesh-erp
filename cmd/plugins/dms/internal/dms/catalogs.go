package dms

import (
	"context"
	"errors"
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

// defaultPattern: Muster des Dateinamens (Vorschlag), z. B. 52.0:VV:ROBro1_DL-2026-0003_20260302_VR:VGS.pdf –
// Ablage, Buchungskreis, Objekt-Ebene, Bezug, Datum, Dokumentart, Partner.
const defaultPattern = "{account}:{company}:{property}_{object}_{date}_{doctype}:{partner}"

func checkCode(rec crud.Record, key, what string) error {
	rec[key] = trimUpper(rec[key])
	if !codeRe.MatchString(crud.Str(rec[key])) {
		return crud.Invalid("%s %q: Großbuchstaben, Ziffern, _ und - (höchstens 20 Zeichen)", what, crud.Str(rec[key]))
	}
	return nil
}

func (m *Module) retention() *crud.Entity {
	return &crud.Entity{
		Object: retentionObject, Customizing: true, Title: "Ablagefristen", Icon: "icon-clock", Table: "dms__retention", Section: secSettings,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "start", Label: "Beginn der Frist", Short: "Beginn", Type: tSel, Required: true, Listable: true, Options: startOptions},
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
		Object: docTypeObject, Customizing: true, Title: "Dokumentarten", Icon: "icon-tag", Table: "dms__doc_type", Section: secSettings,
		Keys: []string{"code"}, Order: "sort_order, code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Filters: []string{"category"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "category", Label: "Dokumentkategorie", Short: "Kategorie", Type: tText, Required: true, Listable: true, Ref: refCategory},
			{Key: "short", Label: "Kürzel im Dateinamen (eindeutig, z. B. RE, VR)", Short: "Kürzel", Type: tText, Listable: true},
			{Key: "retention", Label: "Ablagefrist (Vorschlag, wenn die Ablage keine hat)", Short: "Frist", Type: tText, Listable: true, Ref: refRetention},
			{Key: "ref_kind", Label: "Dokumentart der Verweise an Verträgen, Objekten … (Plugin document)", Short: "Verweisart", Type: tText, Lookup: lookupRefKind},
			{Key: "file_account", Label: "Ablage, wenn die Ablagefindung keine liefert (erzeugte Dokumente, z. B. Lieferanten-Schriftwechsel)", Short: "Ablage", Type: tText, Listable: true, Lookup: lookupFileAccount},
			{Key: "file_accounts", Label: "Ablagen zur Auswahl am Angebot bzw. Auftrag (Komma, z. B. 6.1.0, 6.1.1)", Short: "Auswahl", Type: tText, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["ref_kind"] = nilIfEmpty(trimUpper(rec["ref_kind"]))
			if err := m.checkRefKind(ctx, crud.Str(rec["ref_kind"])); err != nil {
				return err
			}
			rec["category"] = trimUpper(rec["category"])
			rec["file_account"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["file_account"])))
			rec["file_accounts"] = nilIfEmpty(strings.Join(splitList(crud.Str(rec["file_accounts"])), ", "))
			if err := checkCode(rec, "code", "Schlüssel"); err != nil {
				return err
			}
			short := trimUpper(rec["short"])
			rec["short"] = nilIfEmpty(short)
			if short == "" {
				return nil
			}
			if namePart(short) != short {
				return crud.Invalid("Kürzel %q: ohne Leerzeichen, : und _", short)
			}
			res, err := m.db.Query(ctx, `SELECT code FROM dms__doc_type WHERE short = ? AND code <> ?`, short, rec["code"])
			if err != nil {
				return err
			}
			if len(res.Rows) > 0 {
				return crud.Invalid("Kürzel %s hat schon die Dokumentart %s", short, crud.Str(res.Rows[0][0]))
			}
			return nil
		},
	}
}

// category: Dokumentkategorie – zu welchem kaufmännischen Objekt ein Dokument
// gehört (Vertrag, Rechnung/Gutschrift, Gebäude/Mietobjekt …); darunter die
// Dokumentarten, mit denen es am Objekt gespeichert wird.
// hint: Hinweise aus dem Dokument (Platzhalter {hint.<Schlüssel>}, siehe hints.go).
func (m *Module) hint() *crud.Entity {
	return &crud.Entity{
		Object: hintObject, Customizing: true, Title: "Hinweise aus dem Dokument", Icon: "icon-search", Table: "dms__hint", Section: secSettings,
		Keys: []string{"code"}, Order: "sort_order, code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel (Platzhalter {hint.<Schlüssel>}, Kleinbuchstaben)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: hintKinds},
			{Key: "pattern", Label: "Muster im Text (regulärer Ausdruck; der erste passende Treffer zählt)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintPattern}}},
			{Key: "check_rule", Label: "Prüfung des Treffers", Type: tSel, Options: hintChecks,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintPattern}}},
			{Key: "strip", Label: "Zeichen entfernen (z. B. Leerzeichen)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintPattern}}},
			{Key: "exclude", Label: "Ausschließen: Werte aus Object.Feld (z. B. BankAccount.iban – eigene Konten)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintPattern}}},
			{Key: "source", Label: "Quelle (Object, z. B. BankTransaction)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintLookup, hintMatch}}},
			{Key: "match", Label: "Suche: Feld=Wert; … (Platzhalter wie {hint.iban}, {company_code})", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintLookup, hintMatch}}},
			{Key: "value_field", Label: "Feld mit dem Wert", Type: tText, ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintLookup}}},
			{Key: "partner_field", Label: "Feld mit dem Geschäftspartner (zählt beim Erkennen; Platzhalter {hint.<Schlüssel>_partner})", Short: "Partnerfeld", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintLookup, hintMatch}}},
			{Key: "sort_field", Label: "Neuester zuerst nach Feld (z. B. booking_date)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintLookup}}},
			// Erkennen (Schritt RECOGNIZE)
			{Key: "target", Label: "Erkennen: Ziel (Feld des Dokuments, z. B. partner_id, doc_date; ref:<Object>)", Type: tText, Listable: true},
			{Key: "points", Label: "Erkennen: Punkte je Treffer", Short: "Punkte", Type: tNum, Listable: true},
			{Key: "compare_field", Label: "Vergleichen: Feld der Quelle (z. B. iban, name1)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "normalize", Label: "Vergleichen als", Type: tSel, Options: normalizeOptions,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "compare_hint", Label: "Vergleichen mit Hinweis (Schlüssel; leer = erkannter Text)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "min_length", Label: "Mindestlänge des Werts", Type: tNum,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "candidate_field", Label: "Kandidat: Feld der Quelle (z. B. bp_id; leer = id)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "rule_group", Label: "Gruppe (zählt je Kandidat einmal, z. B. contract)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "requires", Label: "Nur zusammen mit Regel (Schlüssel, z. B. m_name1)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "points_field", Label: "Punkte aus Feld der Quelle (z. B. points der Stichworte; leer = Punkte je Treffer)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "kind", Values: []string{hintMatch}}},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "sort_order", Label: "Reihenfolge (Hinweise können frühere verwenden)", Type: tNum, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: m.checkHint,
	}
}

func (m *Module) checkHint(_ context.Context, rec, _ crud.Record) error {
	rec["target"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["target"])))
	code := strings.ToLower(strings.TrimSpace(crud.Str(rec["code"])))
	if !hintCodeRe.MatchString(code) {
		return crud.Invalid("Schlüssel %q: Kleinbuchstaben, Ziffern, _", code)
	}
	rec["code"] = code
	switch crud.Str(rec["kind"]) {
	case hintPattern:
		if _, err := regexp.Compile(crud.Str(rec["pattern"])); err != nil || strings.TrimSpace(crud.Str(rec["pattern"])) == "" {
			return crud.Invalid("Muster: regulärer Ausdruck erwartet")
		}
	case hintLookup:
		if strings.TrimSpace(crud.Str(rec["source"])) == "" || strings.TrimSpace(crud.Str(rec["value_field"])) == "" {
			return crud.Invalid("Quelle und Feld mit dem Wert sind Pflicht")
		}
	case hintMatch:
		if strings.TrimSpace(crud.Str(rec["source"])) == "" || strings.TrimSpace(crud.Str(rec["compare_field"])) == "" ||
			strings.TrimSpace(crud.Str(rec["target"])) == "" {
			return crud.Invalid("Erkennungsregel: Quelle, Vergleichsfeld und Ziel sind Pflicht")
		}
		if crud.Str(rec["normalize"]) == "" {
			rec["normalize"] = normName
		}
	default:
		return crud.Invalid("Art %q unbekannt", rec["kind"])
	}
	return nil
}

func (m *Module) category() *crud.Entity {
	return &crud.Entity{
		Object: categoryObject, Customizing: true, Title: "Dokumentkategorien", Icon: "icon-tags", Table: "dms__doc_category", Section: secSettings,
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
				Columns: []string{"object", "label", "partner_field", "title_field", "account_object", "can_create", "is_active"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error { return checkCode(rec, "code", "Schlüssel") },
	}
}

// docTypeObj: kaufmännische Objekte je Dokumentkategorie – Auswahl im Workflow
// (Partner zuerst, wenn partner_field gesetzt), Anlegen im neuen Fenster,
// Übernahme von Partner, Datum, Titel und Ablage (über Sachkonto, Kostenart, umlagefähig).
func (m *Module) docTypeObj() *crud.Entity {
	return &crud.Entity{
		Object: docTypeObjObject, Customizing: true, Title: "Kaufmännische Objekte je Dokumentkategorie", Icon: "icon-link", Table: "dms__category_object", Section: secSettings,
		Keys: []string{"category", "object"}, Order: "category, sort_order, object", StatusField: "is_active", Filters: []string{"category"},
		Fields: []crud.Field{
			{Key: "category", Label: "Dokumentkategorie", Short: "Kategorie", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refCategory},
			{Key: "object", Label: "Object (z. B. SupplierInvoice, Contract, Building)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "label", Label: "Bezeichnung in der Auswahl", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "list_action", Label: "Action der Auswahl (Standard list)", Type: tText},
			{Key: "filters", Label: "Weitere Filter der Auswahl (feld=wert; feld=wert)", Type: tText},
			{Key: "partner_field", Label: "Feld mit dem Geschäftspartner (Auswahl nach Partner, Übernahme)", Short: "Partnerfeld", Type: tText, Listable: true, Group: "Übernahme"},
			{Key: "title_field", Label: "Feld mit dem Titel", Type: tText, Group: "Übernahme"},
			{Key: "account_object", Label: "Object mit den Sachkonten (z. B. SupplierInvoiceItem)", Short: "Konten-Object", Type: tText, Listable: true, Group: "Ablage über Sachkonto"},
			{Key: "account_link", Label: "Verbindungsfeld (z. B. invoice_id)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "account_field", Label: "Feld mit dem Sachkonto (z. B. account_number)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "cost_category_field", Label: "Feld mit der Kostenart (z. B. cost_category)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "allocable_field", Label: "Feld „umlagefähig“ (z. B. allocable)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "file_account_field", Label: "Feld des Objekts mit der dort gewählten Ablage (geht der Ablagefindung vor, z. B. file_account)", Type: tText, Group: "Ablage über Sachkonto"},
			{Key: "can_create", Label: "Im Workflow neu anlegen (Link)", Short: "Anlegen", Type: tBool, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			for _, k := range []string{"object", "label", "list_action", "filters", "partner_field", "title_field", "account_object", "account_link", "account_field", "cost_category_field", "allocable_field", "file_account_field"} {
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
		Object: archiveObject, Customizing: true, Title: "Archive", Icon: "icon-archive", Table: "dms__archive", Section: secSettings,
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
		Object: postSourceObject, Customizing: true, Title: "Bezugsquellen der Vorerfassung", Icon: "icon-link", Table: "dms__posting_source", Section: secSettings,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "object", Label: "Object mit Verweis auf die Vorerfassung (z. B. SupplierInvoice)", Short: "Object", Type: tText, Required: true, Listable: true},
			{Key: "filter_field", Label: "Feld mit der Vorerfassung (z. B. draft_id)", Short: "Filterfeld", Type: tText, Required: true, Listable: true},
			{Key: "ref_object", Label: "Bezug: Object (z. B. Contract)", Type: tText, Required: true, Listable: true},
			{Key: "key_template", Label: "Bezug: Schlüssel aus Feldern, z. B. {company_code}|{contract_id}", Short: "Schlüssel", Type: tText, Required: true, Listable: true},
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
		Object: companyObject, Customizing: true, Title: "Buchungskreise", Icon: "icon-building", Table: "dms__company", Section: secSettings,
		Keys: []string{"company_code"}, Order: "company_code", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "plan", Label: "Ablageplan", Short: "Plan", Type: tText, Required: true, Listable: true, Ref: refPlan},
			{Key: "archive", Label: "Archiv", Type: tText, Required: true, Listable: true, Ref: refArchive},
			{Key: "short_name", Label: "Kürzel im Dateinamen ({company}), z. B. VV", Short: "Kürzel", Type: tText, Listable: true},
			{Key: "name_pattern", Label: "Muster des Dateinamens", Type: tText, Group: "Dateiname"},
			{Key: "object_dir", Label: "Verzeichnis unter der Ablage", Type: tSel, Group: "Archiv", Options: []metamodel.Option{
				{Value: "PROPERTY", Label: "Objekt des Dokuments (Gebäude, Wirtschaftseinheit)"}, {Value: "NONE", Label: "keins"}}},
			{Key: "object_dir_fallback", Label: "Verzeichnis für Dokumente ohne Objekt", Type: tText, Group: "Archiv"},
			{Key: "name_account_levels", Label: "Ablagenummer im Dateinamen: so viele Ebenen zusammen, dann ein Punkt und der Rest zusammen (2: 5.2.3.4 → 52.34; 0 = wie im Ablageplan)", Type: tNum, Group: "Dateiname"},
			{Key: "check_postings", Label: "Buchen nur mit abgelegtem Dokument (Prüfregeln Buchung)", Short: "Prüfung Buchungen", Type: tBool, Listable: true, Group: "Prüfungen"},
			{Key: "check_contracts", Label: "Verträge und Konditionen nur mit Dokument (Prüfregeln Vertrag)", Short: "Prüfung Verträge", Type: tBool, Listable: true, Group: "Prüfungen"},
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
			{ActionConfig: metamodel.ActionConfig{Name: "refreshSearch", Label: "Suchtexte aktualisieren", Record: true,
				Confirm: "Suchtexte aller Dokumente des Buchungskreises aus den Bezügen (Rechnungen, Verträge …) und Geschäftspartnern neu aufbauen?"},
				Handle: m.refreshSearchAction},
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
	defaults(rec, map[string]any{"check_postings": false, "check_contracts": false, "name_account_levels": int64(2),
		"object_dir": "PROPERTY", "object_dir_fallback": "Allgemein"})
	if f := strings.TrimSpace(crud.Str(rec["object_dir_fallback"])); safeFileName(f) != f {
		return crud.Invalid("Verzeichnis %q: ohne / und \\", f)
	}
	if n := toInt(rec["name_account_levels"]); n < 0 || n > 5 {
		return crud.Invalid("Ebenen ohne Punkt: 0 bis 5")
	}
	return nil
}

func (m *Module) postingRule() *crud.Entity {
	return &crud.Entity{
		Object: postRuleObject, Customizing: true, Title: "Prüfregeln Buchung", Icon: "icon-check", Table: "dms__posting_rule", Section: secSettings,
		Keys: []string{"company_code", "rule_no"}, Order: "company_code, rule_no", StatusField: "is_active", Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "rule_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "document_type", Label: "Belegart (leer = alle)", Type: tText, Listable: true, Lookup: lookupLedgerDocType},
			{Key: "file_accounts", Label: "Ablagen des Dokuments (Nummern oder Anfänge, durch Komma; leer = jede)", Short: "Ablagen", Type: tText, Listable: true},
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
		Object: contractRuleObj, Customizing: true, Title: "Prüfregeln Vertrag", Icon: "icon-check", Table: "dms__contract_rule", Section: secSettings,
		Keys: []string{"company_code", "rule_no"}, Order: "company_code, rule_no", StatusField: "is_active", Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "rule_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "event", Label: "Ereignis", Type: tSel, Required: true, Listable: true, Options: eventOptions},
			{Key: "contract_type", Label: "Vertragsart (leer = alle)", Type: tText, Listable: true, Lookup: lookupContractType},
			{Key: "condition_types", Label: "Konditionsarten (durch Komma; leer = alle)", Type: tText, Listable: true},
			{Key: "file_accounts", Label: "Ablagen des Dokuments (Nummern oder Anfänge, durch Komma; leer = jede)", Short: "Ablagen", Type: tText, Listable: true},
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
	AccountLevels                         int
	ObjectDir, ObjectDirFallback          string
}

func (m *Module) companyConfig(ctx context.Context, cc string) (*companyConfig, error) {
	res, err := m.db.Query(ctx, `SELECT plan, archive, short_name, name_pattern, check_postings, check_contracts, name_account_levels, object_dir, object_dir_fallback FROM dms__company
		WHERE company_code = ? AND is_active = ?`, cc, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Buchungskreis %s ist im Dokumentenmanagement nicht eingerichtet (Einstellungen → Buchungskreise)", cc)
	}
	r := res.Rows[0]
	return &companyConfig{CC: cc, Plan: crud.Str(r[0]), Archive: crud.Str(r[1]), ShortName: crud.Str(r[2]), Pattern: crud.Str(r[3]),
		CheckPostings: crud.AsBool(r[4]), CheckContracts: crud.AsBool(r[5]), AccountLevels: int(toInt(r[6])), ObjectDir: crud.Str(r[7]), ObjectDirFallback: crud.Str(r[8])}, nil
}

// checkRefKind (0.28.1): Verweisart aus dem Katalog DocumentKind (Plugin document);
// ohne Plugin document keine Prüfung.
func (m *Module) checkRefKind(ctx context.Context, kind string) error {
	if kind == "" {
		return nil
	}
	if _, err := m.services.Call(ctx, "DocumentKind", "get", map[string]any{"id": kind}); errors.Is(err, sdk.ErrNotFound) {
		return crud.Invalid("Verweisart %s gibt es nicht (Katalog Dokumentarten der Verweise)", kind)
	}
	return nil
}
