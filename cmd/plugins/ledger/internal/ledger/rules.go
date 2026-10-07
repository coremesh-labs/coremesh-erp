package ledger

import (
	"context"
	"slices"
	"strings"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
)

// Regeln für Positionen – eine Stelle für Formular (formState), Vorerfassung und
// Buchen (PostingService), nach dem Vorbild von SAP:
//
//	Belegart (ledger__document_type)          → erlaubte Positionsarten, Referenz Pflicht
//	Positionsart (item_type, analog KOART)    → GL, CUSTOMER, SUPPLIER, TAX, ASSET – aus dem Konto abgeleitet
//	Feldstatusgruppe des Kontos (SKB1)        → je Kontierungsfeld SUPPRESS / OPTIONAL / REQUIRED
//	Kontensperre je Periode (OB52-Intervalle) → Konto in der Periode buchbar?

const (
	itemGL       = "GL"
	itemCustomer = "CUSTOMER"
	itemSupplier = "SUPPLIER"
	itemTax      = "TAX"
	itemAsset    = "ASSET"

	statusSuppress = "SUPPRESS"
	statusOptional = "OPTIONAL"
	statusRequired = "REQUIRED"

	defaultFSG = "STD"
)

// statusFields sind die Felder mit Feldstatus: Kontierungen und Positionstext.
var statusFields = append(slices.Clone(dimColumns), "item_text")

// itemRule ist das Ergebnis der Regeln für eine Position.
type itemRule struct {
	ItemType  string
	Allowed   []string          // erlaubte Positionsarten der Belegart
	Status    map[string]string // Feld → SUPPRESS/OPTIONAL/REQUIRED
	Group     string            // Feldstatusgruppe (id)
	GroupName string
	Recon     string // Abstimmkonto-Typ des Kontos
}

type docType struct {
	Code, Name        string
	Allowed           []string
	ReferenceRequired bool
}

func (m *Module) documentType(ctx context.Context, code string) (*docType, error) {
	res, err := m.db.Query(ctx, "SELECT code, name, allowed_item_types, reference_required, is_active FROM ledger__document_type WHERE code = ?", code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 || !crud.AsBool(res.Rows[0][4]) {
		return nil, crud.Invalid("Belegart %q gibt es nicht oder ist inaktiv", code)
	}
	r := res.Rows[0]
	return &docType{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Allowed: splitList(crud.Str(r[2])), ReferenceRequired: crud.AsBool(r[3])}, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		out = append(out, strings.ToUpper(p))
	}
	return out
}

// rule bestimmt Positionsart und Feldstatus einer Position. requested ist die
// gewählte Positionsart ("" = aus dem Konto ableiten).
func (m *Module) rule(ctx context.Context, cc, docTypeCode, account, requested string) (*itemRule, error) {
	dt, err := m.documentType(ctx, docTypeCode)
	if err != nil {
		return nil, err
	}
	r := &itemRule{Allowed: dt.Allowed, Status: map[string]string{}}
	res, err := m.db.Query(ctx, `SELECT c.reconciliation_type, c.tax_category, COALESCE(c.field_status_group, ?), g.name
		FROM ledger__account_company c LEFT JOIN ledger__field_status_group g ON g.id = COALESCE(c.field_status_group, ?)
		WHERE c.company_code_id = ? AND c.account_number = ?`, defaultFSG, defaultFSG, cc, account)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Konto %s ist dem Buchungskreis %s nicht zugeordnet", account, cc)
	}
	row := res.Rows[0]
	r.Recon, r.Group, r.GroupName = crud.Str(row[0]), crud.Str(row[2]), crud.Str(row[3])
	derived := itemGL
	switch {
	case r.Recon == "CUSTOMER":
		derived = itemCustomer
	case r.Recon == "SUPPLIER":
		derived = itemSupplier
	case r.Recon == "ASSET":
		derived = itemAsset
	case strings.HasSuffix(crud.Str(row[1]), "_TAX_ACCOUNT"):
		derived = itemTax
	}
	r.ItemType = derived
	if requested = strings.ToUpper(strings.TrimSpace(requested)); requested != "" && requested != derived {
		// Abstimmkonten und Steuerkonten bestimmen die Art; sonst ist GL fest.
		return nil, crud.Invalid("Konto %s ist eine Position der Art %s, nicht %s", account, derived, requested)
	}
	if r.ItemType == itemAsset {
		return nil, crud.Invalid("Konto %s ist Abstimmkonto Anlagen – nur über die Anlagenbuchhaltung bebuchbar", account)
	}
	if !slices.Contains(dt.Allowed, r.ItemType) {
		return nil, crud.Invalid("Belegart %s erlaubt keine Positionen der Art %s (erlaubt: %s)", dt.Code, r.ItemType, strings.Join(dt.Allowed, ", "))
	}
	fs, err := m.db.Query(ctx, "SELECT field_name, status FROM ledger__field_status WHERE group_id = ?", r.Group)
	if err != nil {
		return nil, err
	}
	for _, f := range statusFields {
		r.Status[f] = statusOptional
	}
	for _, x := range fs.Rows {
		if s := crud.Str(x[1]); s == statusSuppress || s == statusOptional || s == statusRequired {
			r.Status[crud.Str(x[0])] = s
		}
	}
	return r, nil
}

// check prüft die Kontierungen einer Position gegen die Regel.
func (r *itemRule) check(account string, values map[string]string) error {
	for _, f := range statusFields {
		v := strings.TrimSpace(values[f])
		switch r.Status[f] {
		case statusSuppress:
			if v != "" {
				return crud.Invalid("Feld %s ist für Konto %s ausgeblendet (Feldstatusgruppe %s)", f, account, r.Group)
			}
		case statusRequired:
			if v == "" {
				return crud.Invalid("Feld %s ist für Konto %s Pflicht (Feldstatusgruppe %s)", f, account, r.Group)
			}
		}
	}
	// Debitoren-Positionen brauchen einen Partner: Kunde oder Mietvertrag.
	if r.ItemType == itemCustomer && strings.TrimSpace(values["sd_customer_id"]) == "" && strings.TrimSpace(values["rent_contract_id"]) == "" {
		return crud.Invalid("Konto %s ist Abstimmkonto Debitoren – Kunde (sd_customer_id) oder Mietvertrag (rent_contract_id) nötig", account)
	}
	if r.ItemType == itemSupplier && strings.TrimSpace(values["supplier_id"]) == "" {
		return crud.Invalid("Konto %s ist Abstimmkonto Kreditoren – Lieferant (supplier_id) nötig", account)
	}
	return nil
}

// accountOpen: Ist das Konto in der Periode buchbar? Grundlage ist der Status
// der Periode; aktive Kontensperren für Kontenbereiche gehen vor. Widersprechen
// sich Sperren, gilt CLOSED.
func (m *Module) accountOpen(ctx context.Context, cc, ledger string, year, period int, account string) error {
	open, err := m.periodOpen(ctx, cc, ledger, year, period)
	if err != nil {
		return err
	}
	locks, err := m.db.Query(ctx, `SELECT status, reason, account_from, account_to FROM ledger__period_account_lock
		WHERE company_code_id = ? AND ledger = ? AND fiscal_year = ? AND period_from <= ? AND period_to >= ?
		AND account_from <= ? AND account_to >= ? AND is_active = ?`, cc, ledger, year, period, period, account, account, true)
	if err != nil {
		return err
	}
	var closedBy, openedBy []any
	for _, l := range locks.Rows {
		if crud.Str(l[0]) == "CLOSED" {
			closedBy = l
		} else {
			openedBy = l
		}
	}
	switch {
	case closedBy != nil:
		reason := ""
		if r := crud.Str(closedBy[1]); r != "" {
			reason = " – " + r
		}
		return crud.Invalid("Konto %s ist in Periode %d/%d gesperrt (Kontensperre %s–%s%s)", account, period, year, crud.Str(closedBy[2]), crud.Str(closedBy[3]), reason)
	case openedBy != nil, open:
		return nil
	}
	return crud.Invalid("Periode %d/%d ist im Buchungskreis %s (Ledger %s) nicht offen (console ledger:periods)", period, year, cc, ledger)
}

// --- Seeds ---------------------------------------------------------------------------

func fsg(id, name string, status map[string]string) (map[string]any, []map[string]any) {
	rows := []map[string]any{}
	for _, f := range statusFields {
		s, ok := status[f]
		switch {
		case ok:
		case f == "item_text":
			s = statusOptional
		default:
			s = statusSuppress
		}
		rows = append(rows, map[string]any{"group_id": id, "field_name": f, "status": s})
	}
	return map[string]any{"id": id, "name": name, "is_active": true}, rows
}

func all(status string) map[string]string {
	out := map[string]string{}
	for _, f := range statusFields {
		out[f] = status
	}
	return out
}

func opt(fields ...string) map[string]string {
	out := map[string]string{}
	for _, f := range fields {
		out[f] = statusOptional
	}
	return out
}

func with(m map[string]string, field, status string) map[string]string {
	m[field] = status
	return m
}

// fieldStatusSeeds: mitgelieferte Feldstatusgruppen. Nicht genannte Felder sind
// ausgeblendet, der Positionstext ist optional.
func fieldStatusSeeds() []sdk.SchemaSeed {
	groups := []map[string]any{}
	status := []map[string]any{}
	for _, g := range []struct {
		id, name string
		st       map[string]string
	}{
		{defaultFSG, "Standard (alle Kontierungen optional)", all(statusOptional)},
		{"BANK", "Bank, Kasse, Treuhand (ohne Kontierung)", nil},
		{"BALANCE", "Bestandskonto", opt("rent_object_id", "rent_contract_id", "profit_center", "segment")},
		{"CUSTOMER", "Abstimmkonto Debitoren", opt("sd_customer_id", "rent_contract_id", "rent_object_id", "sd_sales_order_id", "profit_center", "segment")},
		{"SUPPLIER", "Abstimmkonto Kreditoren", with(opt("purchase_order_id", "rent_object_id", "profit_center", "segment"), "supplier_id", statusRequired)},
		{"REVENUE", "Erlöse", opt("profit_center", "segment", "sd_customer_id", "sd_sales_org", "sd_sales_order_id", "dimension_custom_1", "dimension_custom_2")},
		{"RENT_REVENUE", "Mieterlöse (Objekt und Vertrag Pflicht)", with(with(opt("dimension_custom_1", "profit_center", "segment"), "rent_object_id", statusRequired), "rent_contract_id", statusRequired)},
		{"COST", "Aufwand mit Kostenstelle", with(opt("profit_center", "segment", "purchase_order_id", "supplier_id", "dimension_custom_1", "dimension_custom_2"), "cost_center", statusRequired)},
		{"OBJECT_COST", "Objektaufwand (Mietobjekt Pflicht)", with(opt("rent_contract_id", "cost_center", "dimension_custom_1", "supplier_id", "purchase_order_id", "profit_center"), "rent_object_id", statusRequired)},
		{"TAX", "Steuerkonto (ohne Kontierung)", nil},
	} {
		head, rows := fsg(g.id, g.name, g.st)
		groups = append(groups, head)
		status = append(status, rows...)
	}
	return []sdk.SchemaSeed{{Table: "ledger__field_status_group", Rows: groups}, {Table: "ledger__field_status", Rows: status}}
}

var documentTypeSeeds = sdk.SchemaSeed{Table: "ledger__document_type", Rows: []map[string]any{
	{"code": "SA", "name": "Sachkontenbeleg", "allowed_item_types": "GL,TAX", "reference_required": false, "is_active": true},
	{"code": "DR", "name": "Debitorenrechnung", "allowed_item_types": "CUSTOMER,GL,TAX", "reference_required": true, "is_active": true},
	{"code": "DZ", "name": "Debitorenzahlung", "allowed_item_types": "CUSTOMER,GL", "reference_required": false, "is_active": true},
	{"code": "KR", "name": "Kreditorenrechnung", "allowed_item_types": "SUPPLIER,GL,TAX", "reference_required": true, "is_active": true},
	{"code": "KZ", "name": "Kreditorenzahlung", "allowed_item_types": "SUPPLIER,GL", "reference_required": false, "is_active": true},
	{"code": "AB", "name": "Verrechnung / Storno", "allowed_item_types": "GL,CUSTOMER,SUPPLIER,TAX", "reference_required": false, "is_active": true},
}}

// defaultGroup: Feldstatusgruppe eines Kontos ohne Vorgabe aus der Kontenrahmen-Datei.
func defaultGroup(accountType, recon, tax string) string {
	switch {
	case recon == "CUSTOMER":
		return "CUSTOMER"
	case recon == "SUPPLIER":
		return "SUPPLIER"
	case strings.HasSuffix(tax, "_TAX_ACCOUNT"):
		return "TAX"
	case accountType == "REVENUE":
		return "REVENUE"
	case accountType == "PRIMARY_COST", accountType == "SECONDARY_COST":
		return "COST"
	case accountType == "BALANCE_SHEET":
		return "BALANCE"
	}
	return defaultFSG
}
