package export

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Parameter einer Sicht: Schlüssel im Katalog (params, ref_param) → Feld der Sicht.
var paramFields = map[string]string{
	"period": "date_from", "building": "building_id", "object": "object_id",
	"contract": "contract_id", "partner": "partner_id", "process": "process_id",
}

var paramOptions = []metamodel.Option{
	{Value: "building", Label: "Gebäude"}, {Value: "object", Label: "Mietobjekt"}, {Value: "contract", Label: "Vertrag"},
	{Value: "partner", Label: "Partner"}, {Value: "process", Label: "Vorgang"},
}

var sortOptions = []metamodel.Option{
	{Value: "DATE", Label: "nach Belegdatum"},
	{Value: "TYPE", Label: "nach Dokumentart, dann Belegdatum"},
	{Value: "PARTNER", Label: "nach Partner, dann Belegdatum"},
	{Value: "RULE", Label: "nach Regel, dann Belegdatum"},
	{Value: "FILED", Label: "nach Ablagedatum"},
}

var targetKinds = []metamodel.Option{
	{Value: targetDir, Label: "Verzeichnis auf dem Server"},
	{Value: targetZip, Label: "zip (Download; mit Pfad als Datei dort)"},
}

const patternHelp = "Platzhalter: {lfd} {belegdatum} {jahr} {monat} {dokumentart} {kategorie} {partner} {titel} {ablage} {regel} {name}"

func catalogAccess() *crud.Access { return &crud.Access{Records: true, CompanyCode: "company_code"} }

func codeCheck(rec, old crud.Record) error {
	if old != nil {
		return nil
	}
	rec["code"] = trimUpper(rec["code"])
	if !codeRe.MatchString(crud.Str(rec["code"])) {
		return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 24 Zeichen)", crud.Str(rec["code"]))
	}
	return nil
}

// viewType: Katalog Sichtarten mit Regeln.
func (m *Module) viewType() *crud.Entity {
	return &crud.Entity{
		Object: viewTypeObject, Customizing: true, Title: "Sichtarten", Icon: "icon-layers", Table: "export__view_type", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "params", Label: "Pflichtangaben der Sicht (Komma: period = Zeitraum, building, object, contract, partner, process)", Short: "Pflichtangaben", Type: tText, Listable: true},
			{Key: "folder_pattern", Label: "Gliederung: Ordner (leer = keine Ordner; " + patternHelp + ")", Short: "Ordner", Type: tText},
			{Key: "file_pattern", Label: "Benennung: Dateiname ohne Endung (" + patternHelp + ")", Short: "Dateiname", Type: tText, Required: true},
			{Key: "sort_by", Label: "Reihenfolge (laufende Nummer)", Short: "Reihenfolge", Type: tSel, Required: true, Options: sortOptions},
			{Key: "note", Label: "Bemerkung (wofür die Sicht gedacht ist)", Short: "Bemerkung", Type: tArea},
			{Key: "key_by", Label: "Schlüssel: ein Dataset je …", Short: "Schlüssel", Type: tSel, Listable: true, Options: keyOptions, Group: "Datasets"},
			{Key: "dataset_pattern", Label: "Name des Datasets (" + datasetPatternHelp + "; leer = Sicht und Schlüssel)", Short: "Name", Type: tText, Group: "Datasets"},
			{Key: "rhythm", Label: "Rhythmus der Erneuerung (Vorschlag für neue Datasets)", Short: "Rhythmus", Type: tSel, Listable: true, Options: rhythmOptions, Group: "Datasets"},
			{Key: "lifetime_months", Label: "Lebensdauer: löschen … Monate nach dem Ende des Zeitraums bzw. dem Abschluss (leer = bleibt)", Short: "Lebensdauer", Type: tNum, Group: "Datasets"},
			{Key: "end_field", Label: "Ende: Feld am Vorgang, das den Abschluss zeigt (z. B. closed_on; nur Schlüssel je Vorgang)", Short: "Ende", Type: tText, Group: "Datasets"},
			{Key: "end_days", Label: "Nach dem Abschluss löschen nach … Tagen (leer = nach der Lebensdauer)", Short: "Tage", Type: tNum, Group: "Datasets"},
			{Key: "refresh_call", Label: "Vor dem Sammeln erzeugen (Object.Aktion mit dem Schlüssel, z. B. Process.storeSheet)", Short: "Erzeugen", Type: tText, Group: "Datasets"},
			{Key: "replace_same", Label: "Neues Dokument gleicher Art, gleichen Partners und gleicher Datei ersetzt das alte (Kennzeichen „ersetzt“)", Short: "ersetzt", Type: tBool, Group: "Datasets"},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{{Key: "regeln", Title: "Regeln", Relation: &metamodel.Relation{Object: ruleObject, ForeignKey: "view_type",
			Match: match("view_type", "code"), Columns: []string{"rule_no", "name", "doc_type", "ref_object", "ref_param", "by_partner", "in_period", "exclude", "folder"}}}},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, viewTypeObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if err := codeCheck(rec, old); err != nil {
				return err
			}
			var ps []string
			for _, p := range splitCodes(strings.ToLower(crud.Str(rec["params"]))) {
				if _, ok := paramFields[p]; !ok {
					return crud.Invalid("Pflichtangabe %q gibt es nicht (period, building, object, contract, partner, process)", p)
				}
				ps = append(ps, p)
			}
			rec["params"] = nilIfEmpty(strings.Join(ps, ", "))
			if err := checkPattern(crud.Str(rec["folder_pattern"])); err != nil {
				return err
			}
			rec["key_by"], rec["rhythm"] = trimUpper(rec["key_by"]), trimUpper(rec["rhythm"])
			if rec["key_by"] == "" {
				rec["key_by"] = keySicht
			}
			if rec["rhythm"] == "" {
				rec["rhythm"] = rhManual
			}
			if !validOption(keyOptions, crud.Str(rec["key_by"])) {
				return crud.Invalid("Schlüssel %q gibt es nicht", crud.Str(rec["key_by"]))
			}
			if !validOption(rhythmOptions, crud.Str(rec["rhythm"])) {
				return crud.Invalid("Rhythmus %q gibt es nicht", crud.Str(rec["rhythm"]))
			}
			for _, k := range []string{"dataset_pattern", "refresh_call", "end_field"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			if rc := crud.Str(rec["refresh_call"]); rc != "" && !strings.Contains(rc, ".") {
				return crud.Invalid("Erzeugen: Object.Aktion erwartet (z. B. Process.storeSheet)")
			}
			for _, k := range []string{"lifetime_months", "end_days"} {
				if n := intPtr(rec[k]); n == nil {
					rec[k] = nil
				} else if *n < 0 {
					return crud.Invalid("Lebensdauer und Tage: 0 oder mehr")
				}
			}
			if err := checkDatasetPattern(crud.Str(rec["dataset_pattern"])); err != nil {
				return err
			}
			if strings.TrimSpace(crud.Str(rec["file_pattern"])) == "" {
				return crud.Invalid("Dateiname fehlt (z. B. {lfd}_{belegdatum}_{partner}_{titel})")
			}
			return checkPattern(crud.Str(rec["file_pattern"]))
		},
	}
}

// rule: Regel einer Sichtart.
func (m *Module) rule() *crud.Entity {
	return &crud.Entity{
		Object: ruleObject, Customizing: true, Title: "Regeln der Sichtarten", Icon: "icon-filter", Table: "export__rule", Section: "Einstellungen",
		Keys: []string{"company_code", "view_type", "rule_no"}, Order: "company_code, view_type, rule_no", StatusField: "is_active",
		Filters: []string{"company_code", "view_type"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "view_type", Label: "Sichtart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup(viewTypeObject)},
			{Key: "rule_no", Label: "Nr.", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung (z. B. Belege des Jahres)", Short: "Bezeichnung", Type: tText, Listable: true},
			{Key: "doc_type", Label: "Dokumentart (leer = alle)", Short: "Dokumentart", Type: tText, Listable: true, Lookup: lookupDocType},
			{Key: "doc_category", Label: "Dokumentkategorie (leer = alle)", Short: "Kategorie", Type: tText, Lookup: lookupCategory},
			{Key: "file_account", Label: "Ablage (Nummer bzw. Anfang, z. B. 72 für alle Unterablagen; leer = alle)", Short: "Ablage", Type: tText},
			{Key: "ref_param", Label: "Bezug auf die Angabe der Sicht (Dokument verknüpft mit …)", Short: "Bezug", Type: tSel, Listable: true, Options: paramOptions},
			{Key: "ref_object", Label: "… als Object im DMS (z. B. Contract, RentObject, Building, Process)", Short: "Object", Type: tText, Listable: true},
			{Key: "by_partner", Label: "Partner des Dokuments = Partner der Sicht", Short: "Partner", Type: tBool, Listable: true},
			{Key: "in_period", Label: "Nur Belegdatum im Zeitraum der Sicht", Short: "Zeitraum", Type: tBool, Listable: true},
			{Key: "exclude", Label: "Treffer ausschließen (statt aufnehmen)", Short: "Ausschluss", Type: tBool, Listable: true},
			{Key: "folder", Label: "Ordner für diese Treffer (übersteuert die Gliederung der Sichtart; " + patternHelp + ")", Short: "Ordner", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, ruleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			for _, k := range []string{"name", "doc_type", "doc_category", "file_account", "ref_object", "folder"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			p := strings.ToLower(strings.TrimSpace(crud.Str(rec["ref_param"])))
			rec["ref_param"] = nilIfEmpty(p)
			if p != "" {
				if _, ok := paramFields[p]; !ok || p == "period" {
					return crud.Invalid("Bezug %q gibt es nicht (building, object, contract, partner, process)", p)
				}
				if rec["ref_object"] == nil {
					return crud.Invalid("Bezug auf %s: Object im DMS fehlt (z. B. Contract)", p)
				}
			}
			if toInt(rec["rule_no"]) <= 0 {
				return crud.Invalid("Nr. der Regel: 1 oder größer")
			}
			return checkPattern(crud.Str(rec["folder"]))
		},
	}
}

// target: Katalog Ziele.
func (m *Module) target() *crud.Entity {
	return &crud.Entity{
		Object: targetObject, Customizing: true, Title: "Ziele", Icon: "icon-share", Table: "export__target", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: targetKinds},
			{Key: "path", Label: "Verzeichnis auf dem Server (Pflicht bei „Verzeichnis“; je Lauf ein Unterordner bzw. eine zip-Datei)", Short: "Verzeichnis", Type: tText, Listable: true},
			{Key: "note", Label: "Bemerkung (z. B. wer das Ziel bekommt)", Short: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, targetObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if err := codeCheck(rec, old); err != nil {
				return err
			}
			rec["kind"] = trimUpper(rec["kind"])
			rec["path"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["path"])))
			switch rec["kind"] {
			case targetDir:
				if rec["path"] == nil {
					return crud.Invalid("Ziel „Verzeichnis“: Verzeichnis auf dem Server fehlt")
				}
			case targetZip:
			default:
				return crud.Invalid("Art %q gibt es nicht (DIRECTORY, ZIP)", crud.Str(rec["kind"]))
			}
			return nil
		},
	}
}

// --- Vorschlagswerte (setup-company) -----------------------------------------------------

type ruleDef struct {
	no                                 int
	name, docType, refParam, refObject string
	byPartner, inPeriod, exclude       bool
	folder                             string
}

type typeDef struct {
	code, name, params, folder, file, sort, note string
	rules                                        []ruleDef
	// Datasets (seit 0.3.0)
	keyBy, rhythm, pattern, refresh, endField string
	lifetime, endDays                         *int
	replace                                   bool
}

func intp(v int) *int { return &v }

var typeDefaults = []typeDef{
	{code: "STEUERBUERO", name: "Steuerbüro (Belege eines Zeitraums)", params: "period", folder: "{dokumentart}",
		file: "{lfd}_{belegdatum}_{partner}_{titel}", sort: "TYPE", keyBy: keyYear, rhythm: rhQuarterly, pattern: "Steuerunterlagen {jahr}",
		note: "Buchhaltungsbelege mit Belegdatum im Zeitraum (Dokumentarten des DMS-Vorschlags), je Dokumentart ein Ordner; Verträge und Schriftverkehr nur über eigene Regeln.",
		rules: []ruleDef{{no: 10, name: "Rechnungen", docType: "INVOICE", inPeriod: true}, {no: 20, name: "Rechnungen aus Verträgen", docType: "CONTRACT_INVOICE", inPeriod: true},
			{no: 30, name: "Gutschriften", docType: "CREDIT_NOTE", inPeriod: true}, {no: 40, name: "Gutschriften aus Verträgen", docType: "CONTRACT_CREDIT", inPeriod: true},
			{no: 50, name: "Barbelege", docType: "CASH_RECEIPT", inPeriod: true}, {no: 60, name: "Kontoauszüge", docType: "BANK", inPeriod: true},
			{no: 70, name: "Abrechnungen", docType: "STATEMENT", inPeriod: true}, {no: 80, name: "Bescheide", docType: "NOTICE", inPeriod: true}}},
	{code: "VERBRAUCHSINFO", name: "Energie-Monitor (Verbrauchsinformationen § 6a HeizkostenV)", params: "period", folder: "{partner}",
		file: "{belegdatum}_{titel}", sort: "PARTNER", keyBy: keyPartnerMonth, rhythm: rhMonthly, pattern: "Energie-Monitor {partner} {monat}",
		lifetime: intp(18), replace: true,
		note: "Monatliche Verbrauchsinformationen (Dokumentart Verbrauchsinformation) je Mieter und Monat; für das Jahr eine zweite Sicht mit Schlüssel „je Partner und Jahr“. " +
			"Sichtbar bis 18 Monate nach dem Monat; ein neuer Druck ersetzt den alten.",
		rules: []ruleDef{{no: 10, name: "Verbrauchsinformationen", docType: "CONSUMPTION_INFO", inPeriod: true}}},
	{code: "VERTRAG", name: "Vertrag mit Schriftverkehr", params: "contract", folder: "{dokumentart}",
		file: "{lfd}_{belegdatum}_{titel}", sort: "DATE",
		note:  "Alles, was mit dem Vertrag verknüpft ist.",
		rules: []ruleDef{{no: 10, name: "verknüpft mit dem Vertrag", refParam: "contract", refObject: "Contract"}}},
	{code: "RECHTSSTREIT", name: "Rechtsstreit (Vertrag und Partner, chronologisch)", params: "contract", folder: "",
		file: "{lfd}_{belegdatum}_{dokumentart}_{titel}", sort: "DATE",
		note: "Vertrag mit Nachträgen und Schriftverkehr, dazu die Dokumente des Partners im Zeitraum; chronologisch mit Manifest.",
		rules: []ruleDef{{no: 10, name: "verknüpft mit dem Vertrag", refParam: "contract", refObject: "Contract"},
			{no: 20, name: "Dokumente des Partners im Zeitraum", byPartner: true, inPeriod: true}}},
	{code: "MIETOBJEKT", name: "Mietobjekt (Wohnung, Stellplatz)", params: "object", folder: "{dokumentart}",
		file: "{lfd}_{belegdatum}_{titel}", sort: "DATE",
		rules: []ruleDef{{no: 10, name: "verknüpft mit dem Mietobjekt", refParam: "object", refObject: "RentObject", inPeriod: true}}},
	{code: "GEBAEUDE", name: "Gebäude (z. B. Verkauf, Datenraum)", params: "building", folder: "{dokumentart}",
		file: "{lfd}_{belegdatum}_{titel}", sort: "TYPE",
		rules: []ruleDef{{no: 10, name: "verknüpft mit dem Gebäude", refParam: "building", refObject: "Building", inPeriod: true}}},
	{code: "VORGANG", name: "Vorgang (z. B. Versicherungsfall)", params: "process", folder: "",
		file: "{lfd}_{belegdatum}_{dokumentart}_{titel}", sort: "DATE", keyBy: keyProcess, rhythm: rhChange, pattern: "Vorgang {vorgang}",
		refresh: "Process.storeSheet", endField: "closed_on", endDays: intp(0), replace: true,
		note: "Je Vorgang ein Dataset: bei jeder Änderung des Vorgangs wird er als PDF ins DMS gelegt (das alte gilt als ersetzt) und mit seinen Dokumenten gesammelt; " +
			"mit dem Abschluss des Vorgangs wird das Dataset gelöscht.",
		rules: []ruleDef{{no: 10, name: "verknüpft mit dem Vorgang", refParam: "process", refObject: "Process"}}},
}

var markDefaults = []map[string]any{
	{"code": "STORNO_ER", "name": "Eingangsrechnung storniert oder verworfen", "ref_object": "SupplierInvoice", "key_field": "invoice_id",
		"status_field": "status", "statuses": "CANCELLED,DISCARDED", "mark_label": "storniert"},
}

var targetDefaults = []map[string]any{
	{"code": "ZIP", "name": "Download (zip)", "kind": targetZip},
}

func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	n := 0
	for i, t := range typeDefaults {
		row, err := m.one(ctx, "SELECT 1 FROM export__view_type WHERE company_code = ? AND code = ?", cc, t.code)
		if err != nil {
			return 0, err
		}
		keyBy, rhythm := firstNonEmpty(t.keyBy, keySicht), firstNonEmpty(t.rhythm, rhManual)
		if row != nil {
			// seit 0.3.0: Angaben zu den Datasets nachtragen, solange sie fehlen
			res, err := m.db.Exec(ctx, `UPDATE export__view_type SET key_by = ?, rhythm = ?, dataset_pattern = ?, refresh_call = ?, end_field = ?, lifetime_months = ?,
				end_days = ?, replace_same = ?, note = COALESCE(?, note) WHERE company_code = ? AND code = ? AND key_by IS NULL`, keyBy, rhythm, nilIfEmpty(t.pattern),
				nilIfEmpty(t.refresh), nilIfEmpty(t.endField), intOrNil(t.lifetime), intOrNil(t.endDays), t.replace, nilIfEmpty(t.note), cc, t.code)
			if err != nil {
				return 0, err
			}
			n += int(res.RowsAffected)
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO export__view_type (company_code, code, name, params, folder_pattern, file_pattern, sort_by, note, sort_order, is_active,
			key_by, rhythm, dataset_pattern, refresh_call, end_field, lifetime_months, end_days, replace_same)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, t.code, t.name, nilIfEmpty(t.params), nilIfEmpty(t.folder), t.file, t.sort, nilIfEmpty(t.note), (i+1)*10, true,
			keyBy, rhythm, nilIfEmpty(t.pattern), nilIfEmpty(t.refresh), nilIfEmpty(t.endField), intOrNil(t.lifetime), intOrNil(t.endDays), t.replace); err != nil {
			return 0, err
		}
		n++
		for _, r := range t.rules {
			if _, err := m.db.Exec(ctx, `INSERT INTO export__rule (company_code, view_type, rule_no, name, doc_type, ref_param, ref_object, by_partner, in_period, exclude, folder, is_active)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, t.code, r.no, nilIfEmpty(r.name), nilIfEmpty(r.docType), nilIfEmpty(r.refParam), nilIfEmpty(r.refObject),
				r.byPartner, r.inPeriod, r.exclude, nilIfEmpty(r.folder), true); err != nil {
				return 0, err
			}
		}
	}
	for i, r := range markDefaults {
		row, err := m.one(ctx, "SELECT 1 FROM export__mark_rule WHERE company_code = ? AND code = ?", cc, r["code"])
		if err != nil {
			return 0, err
		}
		if row != nil {
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO export__mark_rule (company_code, code, name, ref_object, key_field, status_field, statuses, mark_label, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, r["code"], r["name"], r["ref_object"], r["key_field"], r["status_field"], r["statuses"], r["mark_label"], (i+1)*10, true); err != nil {
			return 0, err
		}
		n++
	}
	for i, t := range targetDefaults {
		row, err := m.one(ctx, "SELECT 1 FROM export__target WHERE company_code = ? AND code = ?", cc, t["code"])
		if err != nil {
			return 0, err
		}
		if row != nil {
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO export__target (company_code, code, name, kind, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
			cc, t["code"], t["name"], t["kind"], (i+1)*10, true); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// setupCompanyAction: ExportSetup.setupCompany {company} (Konsole export:setup-company).
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company + in.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, viewTypeObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.services.Call(ctx, "CompanyCode", "get", map[string]any{"id": cc}); err != nil && !errorsIsUnimplemented(err) {
		return sdk.Response{}, crud.Invalid("Buchungskreis %s gibt es nicht", cc)
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		n, err = m.setupCatalogs(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	err = m.migrateRuns(ctx)
	m.mu.Unlock()
	if err != nil {
		return sdk.Response{}, fmt.Errorf("Läufe in Datasets übernehmen: %w", err)
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n,
		"message": fmt.Sprintf("Buchungskreis %s: %d Sichtarten, Kennzeichenregeln bzw. Ziele angelegt oder ergänzt", cc, n)}}, nil
}

func intOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
