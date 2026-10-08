package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func catalogFields(extra ...crud.Field) []crud.Field {
	fs := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	fs = append(fs, extra...)
	return append(fs,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
}

func (m *Module) catalog(object, title, table string, fields []crud.Field, check func(context.Context, crud.Record) error) *crud.Entity {
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-tag", Table: table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields:  fields,
		Access:  &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["code"] = trimUpper(rec["code"])
			defaults(rec, map[string]any{"sort_order": 0})
			if check != nil {
				return check(ctx, rec)
			}
			return nil
		},
	}
}

// invoiceType: Rechnungsart – Belegarten im Hauptbuch, Rolle des Lieferanten
// (Finanzrolle mit Abstimmkonto), Nummernkreis, automatisch buchen.
func (m *Module) invoiceType() *crud.Entity {
	docType := &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}
	e := m.catalog("InvoiceType", "Rechnungsarten", "procurement__invoice_type", catalogFields(
		crud.Field{Key: "document_type", Label: "Belegart im Hauptbuch", Type: tText, Required: true, Listable: true, Lookup: docType},
		crud.Field{Key: "credit_document_type", Label: "Belegart bei Gutschrift (negativer Saldo)", Type: tText, Required: true, Lookup: docType},
		crud.Field{Key: "supplier_role", Label: "Rolle des Lieferanten (Partnermodul, Finanzrolle)", Type: tText, Required: true, Listable: true,
			Lookup: &metamodel.Lookup{Object: "PartnerRoleType", ValueField: "code", LabelFields: []string{"description"}}},
		crud.Field{Key: "range_key", Label: "Nummernkreis (Intervallschlüssel; leer = Schlüssel)", Type: tText},
		crud.Field{Key: "auto_post", Label: "Automatisch ins Hauptbuch buchen (sonst bleibt die geprüfte Vorerfassung offen)", Type: tBool, Listable: true},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"auto_post": false})
		if crud.Str(rec["range_key"]) == "" {
			rec["range_key"] = rec["code"]
		}
		for _, k := range []string{"range_key", "document_type", "credit_document_type", "supplier_role"} {
			rec[k] = trimUpper(rec[k])
		}
		return nil
	})
	e.Actions = []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Buchungskreis einrichten …",
		Fields: []string{"company_code"}}, Handle: m.setupCompanyAction}}
	return e
}

// costCategory: Kostenart – Vorschlag Sachkonto, umlagefähig, Nr. nach BetrKV.
func (m *Module) costCategory() *crud.Entity {
	return m.catalog("CostCategory", "Kostenarten", "procurement__cost_category", catalogFields(
		crud.Field{Key: "account_number", Label: "Sachkonto (Vorschlag)", Type: tText, Listable: true, Lookup: lookupAccount},
		crud.Field{Key: "allocable", Label: "Umlagefähig (Betriebskosten)", Type: tBool, Listable: true},
		crud.Field{Key: "betrkv_no", Label: "Nr. nach § 2 BetrKV (leer = keine Betriebskosten)", Type: tText, Listable: true},
	), func(ctx context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"allocable": false})
		if acc := strings.TrimSpace(crud.Str(rec["account_number"])); acc != "" {
			nr, err := m.glAccount(ctx, crud.Str(rec["company_code"]), acc)
			if err != nil {
				return err
			}
			rec["account_number"] = nr
		} else {
			rec["account_number"] = nil
		}
		return nil
	})
}

// ledgerFieldOptions: Kontierungsfelder der Einzelposten für Objekte.
var ledgerFieldOptions = []metamodel.Option{
	{Value: "rent_object_id", Label: "Mietobjekt (rent_object_id)"}, {Value: "dimension_custom_1", Label: "Freie Dimension 1"},
	{Value: "dimension_custom_2", Label: "Freie Dimension 2"}, {Value: "profit_center", Label: "Profit-Center"},
	{Value: "cost_center", Label: "Kostenstelle"}, {Value: "segment", Label: "Segment"}}

// objectPosting: Kontierung der Objekte – Objektart → Feld im Hauptbuch.
func (m *Module) objectPosting() *crud.Entity {
	return &crud.Entity{
		Object: "ObjectPosting", Title: "Kontierung der Objekte", Icon: "icon-tag", Table: "procurement__object_posting", Section: "Einstellungen",
		Keys: []string{"company_code", "object_type"}, Order: "company_code, object_type", Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "object_type", Label: "Objektart", Type: tSel, Required: true, Listable: true, Immutable: true, Options: objectTypeOptions},
			{Key: "ledger_field", Label: "Kontierungsfeld im Hauptbuch", Type: tSel, Required: true, Listable: true, Options: ledgerFieldOptions},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
	}
}

// objectField: Kontierungsfeld der Objektart ("" = nicht kontiert).
func (m *Module) objectField(ctx context.Context, cc, typ string) string {
	return m.text(ctx, `SELECT ledger_field FROM procurement__object_posting WHERE company_code = ? AND object_type = ?`, cc, typ)
}

var defaultObjectPosting = map[string]string{"RentObject": "rent_object_id", "Building": "dimension_custom_1", "BusinessEntity": "dimension_custom_2"}

// costCategoryOf: Sachkonto und umlagefähig einer aktiven Kostenart.
func (m *Module) costCategoryOf(ctx context.Context, cc, code string) (account string, allocable bool, err error) {
	res, err := m.db.Query(ctx, `SELECT account_number, allocable FROM procurement__cost_category WHERE company_code = ? AND code = ? AND is_active = ?`,
		cc, code, true)
	if err != nil {
		return "", false, err
	}
	if len(res.Rows) == 0 {
		return "", false, crud.Invalid("Kostenart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	return crud.Str(res.Rows[0][0]), crud.AsBool(res.Rows[0][1]), nil
}

// typeRow: Rechnungsart.
type typeRow struct {
	Code, Name, DocumentType, CreditDocumentType, SupplierRole, RangeKey string
	AutoPost                                                             bool
}

func (m *Module) invoiceTypeOf(ctx context.Context, cc, code string) (*typeRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, document_type, credit_document_type, supplier_role, range_key, auto_post
		FROM procurement__invoice_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Rechnungsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	r := res.Rows[0]
	return &typeRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), DocumentType: crud.Str(r[2]), CreditDocumentType: crud.Str(r[3]),
		SupplierRole: crud.Str(r[4]), RangeKey: crud.Str(r[5]), AutoPost: crud.AsBool(r[6])}, nil
}

// glAccount: Sachkonto im Buchungskreis (Hauptbuch), nicht gesperrt – Nummer mit Kontenplan-Präfix.
func (m *Module) glAccount(ctx context.Context, cc, number string) (string, error) {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if err != nil {
		return "", unavailable("Hauptbuch", err)
	}
	var out struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
			IsBlocked     bool   `json:"is_blocked"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if sameAccount(it.AccountNumber, number) {
			if it.IsBlocked {
				return "", crud.Invalid("Sachkonto %s ist im Buchungskreis %s gesperrt", number, cc)
			}
			return it.AccountNumber, nil
		}
	}
	return "", crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht", number, cc)
}

// sameAccount: gleiche Nummer, mit oder ohne Kontenplan-Präfix (SKR25-6000 = 6000).
func sameAccount(a, b string) bool {
	a, b = strings.ToUpper(strings.TrimSpace(a)), strings.ToUpper(strings.TrimSpace(b))
	return a == b || strings.HasSuffix(a, "-"+b) || strings.HasSuffix(b, "-"+a)
}

// --- Vorschlagswerte ----------------------------------------------------------------

var defaultInvoiceTypes = []struct{ code, name, doc, credit, role string }{
	{"ER", "Eingangsrechnung", "KR", "KG", "CREDITOR"},
}

// defaultCostCategories: Betriebskostenarten nach § 2 BetrKV und übliche
// nicht umlagefähige Kosten. Sachkonten pflegt der Buchungskreis.
var defaultCostCategories = []struct {
	code, name, betrkv string
	allocable          bool
}{
	{"GRST", "Grundsteuer", "1", true},
	{"WASSER", "Wasserversorgung", "2", true},
	{"ABWASSER", "Entwässerung", "3", true},
	{"HEIZUNG", "Heizung", "4", true},
	{"WARMW", "Warmwasser", "5", true},
	{"AUFZUG", "Aufzug", "7", true},
	{"STRREIN", "Straßenreinigung und Müllbeseitigung", "8", true},
	{"GEBREIN", "Gebäudereinigung und Ungezieferbekämpfung", "9", true},
	{"GARTEN", "Gartenpflege", "10", true},
	{"BELEUCHT", "Beleuchtung", "11", true},
	{"SCHORNST", "Schornsteinreinigung", "12", true},
	{"VERSICH", "Sach- und Haftpflichtversicherung", "13", true},
	{"HAUSWART", "Hauswart", "14", true},
	{"ANTENNE", "Gemeinschaftsantenne / Breitbandnetz", "15", true},
	{"WAESCHE", "Einrichtungen der Wäschepflege", "16", true},
	{"SONSTBK", "Sonstige Betriebskosten", "17", true},
	{"INSTAND", "Instandhaltung / Reparaturen", "", false},
	{"VERWALT", "Verwaltungskosten", "", false},
}

// setupCompany legt fehlende Rechnungs- und Kostenarten an.
func (m *Module) setupCompany(ctx context.Context, cc string) (types, cats int, err error) {
	has := func(table, code string) (bool, error) {
		res, err := m.db.Query(ctx, "SELECT 1 FROM "+table+" WHERE company_code = ? AND code = ?", cc, code)
		return err == nil && len(res.Rows) > 0, err
	}
	for i, t := range defaultInvoiceTypes {
		if ok, err := has("procurement__invoice_type", t.code); err != nil || ok {
			if err != nil {
				return types, cats, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__invoice_type (company_code, code, name, document_type, credit_document_type, supplier_role,
			range_key, auto_post, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cc, t.code, t.name, t.doc, t.credit, t.role, t.code, false, (i+1)*10, true); err != nil {
			return types, cats, err
		}
		types++
	}
	for i, c := range defaultCostCategories {
		if ok, err := has("procurement__cost_category", c.code); err != nil || ok {
			if err != nil {
				return types, cats, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__cost_category (company_code, code, name, allocable, betrkv_no, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, c.code, c.name, c.allocable, nilIfEmpty(c.betrkv), (i+1)*10, true); err != nil {
			return types, cats, err
		}
		cats++
	}
	for typ, f := range defaultObjectPosting {
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__object_posting (company_code, object_type, ledger_field)
			SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM procurement__object_posting WHERE company_code = ? AND object_type = ?)`,
			cc, typ, f, cc, typ); err != nil {
			return types, cats, err
		}
	}
	return types, cats, nil
}

// setupCompanyAction: Konsole (company) bzw. Aktion „Buchungskreis einrichten …“.
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
		Data        struct {
			CompanyCode string `json:"company_code"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company + in.CompanyCode + in.Data.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, "InvoiceType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var types, cats int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		types, cats, err = m.setupCompany(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "invoice_types": types, "cost_categories": cats,
		"message": fmt.Sprintf("Buchungskreis %s: %d Rechnungsarten, %d Kostenarten angelegt", cc, types, cats)}}, nil
}

// --- Anzeige ------------------------------------------------------------------------

// withLabels: Kataloge, Partner (Kurzname und Name) und Objekte zeigen Texte.
func (m *Module) withLabels(e *crud.Entity) {
	catalogs := map[string]string{"invoice_type": "procurement__invoice_type", "cost_category": "procurement__cost_category"}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		if decorate != nil {
			if err := decorate(ctx, rec); err != nil {
				return err
			}
		}
		cc := crud.Str(rec["company_code"])
		l := labels(rec)
		for _, f := range e.Fields {
			v := crud.Str(rec[f.Key])
			if v == "" || f.Lookup == nil {
				continue
			}
			switch {
			case catalogs[f.Key] != "" && e.Table != catalogs[f.Key]:
				if t := m.text(ctx, "SELECT name FROM "+catalogs[f.Key]+" WHERE company_code = ? AND code = ?", cc, v); t != "" {
					l[f.Key] = t
				}
			case f.Lookup.Object == "BusinessPartner":
				if t := m.partnerName(ctx, v); t != "" {
					l[f.Key] = t
				}
			}
		}
		return nil
	}
}

func (m *Module) text(ctx context.Context, sql string, args ...any) string {
	res, err := m.db.Query(ctx, sql, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

// partnerName: „MUELLER Müller Anna“ aus dem Partnermodul ("" bei Fehler).
func (m *Module) partnerName(ctx context.Context, id string) string {
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Short string `json:"search_term"`
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(bp.Short + " " + strings.TrimSpace(bp.Name1+" "+bp.Name2))
}

// requirePartner: Geschäftspartner gibt es (Partnermodul).
func (m *Module) requirePartner(ctx context.Context, id string) error {
	if id == "" {
		return crud.Invalid("Lieferant ist Pflicht")
	}
	if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id}); err != nil {
		return crud.Invalid("Lieferant %s: %v", id, err)
	}
	return nil
}

// checkObject: Objekt der Immobilienverwaltung (Typ und Schlüssel) im Buchungskreis.
func (m *Module) checkObject(ctx context.Context, rec crud.Record) error {
	typ, id := strings.TrimSpace(crud.Str(rec["object_type"])), strings.TrimSpace(crud.Str(rec["object_id"]))
	rec["object_type"], rec["object_id"] = nilIfEmpty(typ), nilIfEmpty(id)
	switch {
	case typ == "" && id == "":
		return nil
	case typ == "" || id == "":
		return crud.Invalid("Objekt: Art und Objekt angeben (oder beides leer)")
	}
	if _, err := m.services.Call(ctx, typ, "get", map[string]any{"id": crud.Str(rec["company_code"]) + "|" + id}); err != nil {
		return crud.Invalid("%s %s gibt es im Buchungskreis %s nicht", typ, id, crud.Str(rec["company_code"]))
	}
	return nil
}
