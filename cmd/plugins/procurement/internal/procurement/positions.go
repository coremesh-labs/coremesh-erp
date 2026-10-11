package procurement

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

// Positionen (seit 0.8.0): Angebot und Rechnung führen Positionen mit Menge,
// Mengeneinheit, Einzelpreis und Gesamtpreis (gerundet nach der Rundungsregel,
// Plugin ledger) – netto. Nachgelagerte Positionen (Umsatzsteuer, Materialanteil …)
// rechnet das Modul aus den Arten des Kopfs (dependent_types):
//
//   - Arten aus dem Vertragsmodul (ContractDependentType); ohne Vertragsmodul aus der
//     eigenen Tabelle (ProcurementDependentType).
//   - Zuschlag: eigene Position. Mit eigenem Sachkonto bzw. abziehbarer Steuer
//     (Steuerkonto) wird sie so gebucht; sonst (z. B. nicht abziehbare Vorsteuer) auf
//     die Basispositionen verteilt (Kostenbetrag).
//   - Anteil: eigene Position mit eigenem Sachkonto, aus den Basispositionen
//     herausgerechnet (Kostenbetrag); die Summe der Rechnung bleibt gleich.
//
// Ältere Rechnungen (net_amounts = false) bleiben brutto: die Buchung teilt die Steuer
// wie bisher aus dem Bruttobetrag.

const (
	dependentObject = "ProcurementDependentType"
	unitDefault     = "STK"
)

// docKind: Rechnung oder Angebot mit ihren Tabellen.
type docKind struct {
	head, items, key string
	invoice          bool
}

var (
	invoiceDoc = docKind{head: "procurement__invoice", items: "procurement__invoice_item", key: "invoice_id", invoice: true}
	quoteDoc   = docKind{head: "procurement__quote", items: "procurement__quote_item", key: "quote_id"}
)

// --- eigene Tabelle der nachgelagerten Konditionsarten ---------------------------------

var (
	dependentMethods = []metamodel.Option{{Value: pricing.MethodTax, Label: "Steuer (Satz aus dem Steuerkennzeichen)"},
		{Value: pricing.MethodPercent, Label: "Prozent der Basis"}}
	dependentEffects = []metamodel.Option{{Value: pricing.EffectSurcharge, Label: "Zuschlag (kommt zur Basis hinzu, z. B. Umsatzsteuer)"},
		{Value: pricing.EffectShare, Label: "Anteil (aus der Basis herausgerechnet, z. B. Materialanteil)"}}
	lookupRounding = &metamodel.Lookup{Object: "RoundingRule", ValueField: "code", LabelFields: []string{"name"}}
	lookupUnit     = &metamodel.Lookup{Object: "Unit", ValueField: "code", LabelFields: []string{"name"}}
)

func (m *Module) dependentType() *crud.Entity {
	return &crud.Entity{
		Object: dependentObject, Title: "Nachgelagerte Konditionsarten (ohne Vertragsmodul)", Icon: "icon-tag", Table: "procurement__dependent_type",
		Section: "Einstellungen", Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "method", Label: "Berechnung", Type: tSel, Required: true, Listable: true, Options: dependentMethods, Trigger: true},
			{Key: "tax_code", Label: "Steuerkennzeichen (leer = das der Basisposition)", Type: tText, Listable: true, Lookup: lookupTaxCode,
				ShowIf: &metamodel.Condition{Field: "method", Values: []string{pricing.MethodTax}}},
			{Key: "percent", Label: "Prozentsatz (z. B. 10)", Type: tText, Listable: true,
				ShowIf: &metamodel.Condition{Field: "method", Values: []string{pricing.MethodPercent}}},
			{Key: "effect", Label: "Wirkung", Type: tSel, Required: true, Listable: true, Options: dependentEffects},
			{Key: "base_types", Label: "Basis: Kostenarten bzw. Konditionsarten (leer = alle außer Steuer)", Type: tText, Listable: true},
			{Key: "account_number", Label: "Sachkonto (leer = Steuerkonto bzw. auf die Basis verteilt)", Type: tText, Lookup: lookupAccount},
			{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
			{Key: "invoice_default", Label: "Standard für neue Rechnungen und Angebote", Type: tBool, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
			}
			defaults(rec, map[string]any{"sort_order": 0, "invoice_default": false})
			if crud.Str(rec["effect"]) == "" {
				rec["effect"] = pricing.EffectSurcharge
			}
			switch crud.Str(rec["method"]) {
			case pricing.MethodTax:
				rec["percent"] = nil
				if err := m.checkTaxCode(ctx, rec); err != nil {
					return err
				}
			case pricing.MethodPercent:
				rec["tax_code"] = nil
				p, err := pricing.ParseDecimal(crud.Str(rec["percent"]))
				if err != nil || p.Sign() < 0 {
					return crud.Invalid("Prozentsatz %q: Zahl ab 0", crud.Str(rec["percent"]))
				}
				rec["percent"] = pricing.FormatTrim(p, 6)
			default:
				return crud.Invalid("Berechnung: Steuer oder Prozent")
			}
			var base []string
			for _, b := range strings.Split(crud.Str(rec["base_types"]), ",") {
				if b = trimUpper(b); b != "" {
					base = append(base, b)
				}
			}
			rec["base_types"] = nilIfEmpty(strings.Join(base, ","))
			rec["rounding_rule"] = nilIfEmpty(trimUpper(rec["rounding_rule"]))
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
		},
	}
}

// dependentDefs: Arten des Buchungskreises – aus dem Vertragsmodul, ohne es aus der
// eigenen Tabelle; source nennt die Quelle.
func (m *Module) dependentDefs(ctx context.Context, cc string) (defs []pricing.Dependent, source string) {
	if ds, err := pricing.New(m.services).Dependents(ctx, "ContractDependentType", cc); err == nil {
		return ds, "ContractDependentType"
	}
	res, err := m.db.Query(ctx, `SELECT code, name, method, tax_code, percent, effect, base_types, account_number, rounding_rule, sort_order, invoice_default
		FROM procurement__dependent_type WHERE company_code = ? AND is_active = ?`, cc, true)
	if err != nil {
		return nil, dependentObject
	}
	for _, r := range res.Rows {
		defs = append(defs, pricing.DependentOf(map[string]any{"code": r[0], "name": r[1], "method": r[2], "tax_code": r[3], "percent": r[4],
			"effect": r[5], "base_types": r[6], "account_number": r[7], "rounding_rule": r[8], "sort_order": r[9], "invoice_default": r[10]}))
	}
	return defs, dependentObject
}

// dependentOptions: Auswahl der Arten für den Kopf (Mehrfachauswahl).
func (m *Module) dependentOptions(ctx context.Context, cc string) []metamodel.Option {
	defs, _ := m.dependentDefs(ctx, cc)
	var out []metamodel.Option
	for _, d := range defs {
		out = append(out, metamodel.Option{Value: d.Code, Label: firstNonEmpty(d.Name, d.Code)})
	}
	return out
}

// checkDependentTypes: Komma-Liste der Arten; leer beim Anlegen = Standard (Vertrag bzw.
// „Standard für neue Rechnungen“).
func (m *Module) checkDependentTypes(ctx context.Context, rec, old crud.Record, contract string) error {
	cc := crud.Str(rec["company_code"])
	defs, _ := m.dependentDefs(ctx, cc)
	known := map[string]bool{}
	for _, d := range defs {
		known[d.Code] = true
	}
	_, given := rec["dependent_types"]
	raw := strings.TrimSpace(crud.Str(rec["dependent_types"]))
	if old == nil && raw == "" && !given {
		var codes []string
		if contract != "" {
			codes = m.contractDependents(ctx, cc, contract, crud.Str(rec["invoice_date"]))
		} else {
			for _, d := range defs {
				if d.Default {
					codes = append(codes, d.Code)
				}
			}
		}
		raw = strings.Join(codes, ",")
	}
	var codes []string
	for _, c := range strings.Split(raw, ",") {
		if c = trimUpper(c); c != "" && c != "-" {
			if !known[c] {
				return crud.Invalid("Nachgelagerte Konditionsart %s gibt es im Buchungskreis %s nicht", c, cc)
			}
			codes = append(codes, c)
		}
	}
	rec["dependent_types"] = nilIfEmpty(strings.Join(codes, ","))
	return nil
}

// contractDependents: Arten der nachgelagerten Konditionen eines Vertrags am Stichtag.
func (m *Module) contractDependents(ctx context.Context, cc, contract, date string) []string {
	pos, err := m.contractPositions(ctx, cc, contract, date)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, d := range pos.Dependents {
		if !seen[d.Type] {
			seen[d.Type] = true
			out = append(out, d.Type)
		}
	}
	return out
}

// --- Gesamtpreis einer Position ----------------------------------------------------------

// itemPrice: Menge × Einzelpreis → amount (kleinste Einheit) nach der Rundungsregel;
// ohne Einzelpreis gilt der Betrag als Einzelpreis bei Menge 1 (ältere Aufrufer,
// Differenzposition). Geänderter Betrag bei unverändertem Einzelpreis: Betrag gilt.
func (m *Module) itemPrice(ctx context.Context, rec, old crud.Record, cc string, dec int) error {
	rec["rounding_rule"] = nilIfEmpty(trimUpper(rec["rounding_rule"]))
	priceText := strings.TrimSpace(crud.Str(rec["unit_price"]))
	amountText := strings.TrimSpace(crud.Str(rec["amount"]))
	if old != nil && amountText != "" && !isMinor(rec["amount"]) && priceText == strings.TrimSpace(crud.Str(old["unit_price"])) &&
		crud.Str(rec["quantity"]) == crud.Str(old["quantity"]) {
		if minor, err := parseAmount(amountText, dec); err == nil && minor != toInt(old["amount"]) {
			priceText, rec["quantity"] = amountText, nil
		}
	}
	var price *big.Rat
	switch {
	case priceText != "":
		p, err := pricing.ParseDecimal(priceText)
		if err != nil {
			return crud.Invalid("Einzelpreis: %v", err)
		}
		price = p
	case isMinor(rec["amount"]):
		price = pricing.FromMinor(toInt(rec["amount"]), dec)
	case amountText != "":
		p, err := pricing.ParseDecimal(amountText)
		if err != nil {
			return crud.Invalid("Betrag: %v", err)
		}
		price = p
	default:
		return crud.Invalid("Einzelpreis ist Pflicht")
	}
	q := big.NewRat(1, 1)
	if s := strings.TrimSpace(crud.Str(rec["quantity"])); s != "" {
		v, err := pricing.ParseDecimal(s)
		if err != nil {
			return crud.Invalid("Menge: %v", err)
		}
		if v.Sign() <= 0 {
			return crud.Invalid("Menge muss größer 0 sein (Gutschrift: Einzelpreis negativ)")
		}
		q = v
	}
	unit := trimUpper(rec["unit"])
	if unit == "" {
		unit = unitDefault
	}
	if resp, err := m.services.Call(ctx, "Unit", "get", map[string]any{"id": unit}); err == nil {
		var u struct {
			Decimals any `json:"decimals"`
		}
		if sdk.Decode(resp.Payload, &u) == nil && u.Decimals != nil && pricing.Decimals(q) > int(toInt(u.Decimals)) {
			return crud.Invalid("Menge %s: höchstens %d Nachkommastellen bei %s", pricing.FormatTrim(q, 12), toInt(u.Decimals), unit)
		}
	} else {
		return crud.Invalid("Mengeneinheit %s: %v", unit, err)
	}
	r := pricing.New(m.services).Rounding(ctx, cc, crud.Str(rec["rounding_rule"]), dec)
	total, err := r.Total(q, price, dec)
	if err != nil {
		return crud.Invalid("%v", err)
	}
	qf, _ := q.Float64()
	rec["quantity"], rec["unit"], rec["unit_price"], rec["amount"] = qf, unit, pricing.FormatTrim(price, 6), total
	return nil
}

// decoratePosition: Menge ohne Nullen am Ende.
func decoratePosition(rec crud.Record) {
	if q := crud.Str(rec["quantity"]); q != "" {
		if v, err := pricing.ParseDecimal(q); err == nil {
			rec["quantity"] = pricing.FormatTrim(v, 6)
		}
	}
}

// --- nachgelagerte Positionen rechnen ----------------------------------------------------

type baseItem struct {
	line                 int64
	kinds                []string
	tax, account         string
	amount               int64
	objectType, objectID string
}

// recalc rechnet die nachgelagerten Positionen und Kostenbeträge eines Belegs neu und
// gleicht den Kopf ab (Rechnung mit Ausgleich: Rechnungsbetrag; Angebot: Betrag).
// skip: Position, die gerade entfernt wird (0 = keine).
func (m *Module) recalc(ctx context.Context, k docKind, cc, id string, skip int64) error {
	res, err := m.db.Query(ctx, `SELECT net_amounts, dependent_types, currency FROM `+k.head+` WHERE company_code = ? AND `+k.key+` = ?`, cc, id)
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	if !crud.AsBool(res.Rows[0][0]) {
		return nil
	}
	dec := m.currencyDecimals(ctx, crud.Str(res.Rows[0][2]))
	want := map[string]bool{}
	for _, c := range strings.Split(crud.Str(res.Rows[0][1]), ",") {
		if c = strings.TrimSpace(c); c != "" {
			want[c] = true
		}
	}
	all, _ := m.dependentDefs(ctx, cc)
	var defs []pricing.Dependent
	for _, d := range all {
		if want[d.Code] {
			defs = append(defs, d)
		}
	}
	objCols := "NULL, NULL"
	if k.invoice {
		objCols = "object_type, object_id"
	}
	items, err := m.db.Query(ctx, `SELECT line_no, condition_type, cost_category, tax_code, amount, dependent_type, account_number, `+objCols+`
		FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ? AND is_active = ? AND line_no <> ? ORDER BY line_no`, cc, id, true, skip)
	if err != nil {
		return err
	}
	var base []baseItem
	existing := map[string]int64{} // Art|Steuer → Position
	for _, r := range items.Rows {
		if dt := crud.Str(r[5]); dt != "" {
			existing[dt+"|"+crud.Str(r[3])] = toInt(r[0])
			continue
		}
		base = append(base, baseItem{line: toInt(r[0]), kinds: []string{trimUpper(r[1]), trimUpper(r[2])}, tax: crud.Str(r[3]),
			account: crud.Str(r[6]), amount: toInt(r[4]), objectType: crud.Str(r[7]), objectID: crud.Str(r[8])})
	}
	var lines []pricing.Line
	for _, b := range base {
		lines = append(lines, pricing.Line{Key: fmt.Sprint(b.line), Kinds: b.kinds, TaxCode: b.tax, Amount: b.amount})
	}
	type taxRow struct {
		rate          *big.Rat
		kind, account string
	}
	taxes := map[string]taxRow{}
	taxOf := func(code string) (taxRow, error) {
		if t, ok := taxes[code]; ok {
			return t, nil
		}
		r, kind, acc, err := pricing.New(m.services).TaxRate(ctx, code)
		if err != nil {
			return taxRow{}, err
		}
		taxes[code] = taxRow{r, kind, acc}
		return taxes[code], nil
	}
	results, err := pricing.Apply(defs, lines, dec, func(code string) (*big.Rat, error) {
		t, err := taxOf(code)
		return t.rate, err
	}, func(code string) pricing.Rounding { return pricing.New(m.services).Rounding(ctx, cc, code, dec) })
	if err != nil {
		return crud.Invalid("%v", err)
	}
	cost := map[int64]int64{}
	for _, b := range base {
		cost[b.line] = b.amount
	}
	// verteilen: auf die Basispositionen mit diesem Steuerkennzeichen (und Basis der Art)
	allocate := func(d pricing.Dependent, tax string, amount int64) string {
		var idx []int
		var weights []int64
		for i, b := range base {
			if b.tax == tax && pricing.Matches(d.Base, b.kinds) {
				idx = append(idx, i)
				weights = append(weights, b.amount)
			}
		}
		if len(idx) == 0 {
			for i, b := range base {
				idx, weights = append(idx, i), append(weights, b.amount)
			}
		}
		if len(idx) == 0 {
			return ""
		}
		parts := pricing.Allocate(amount, weights)
		biggest := idx[0]
		for n, i := range idx {
			cost[base[i].line] += parts[n]
			if abs64(base[i].amount) > abs64(base[biggest].amount) {
				biggest = i
			}
		}
		return base[biggest].account
	}
	next := int64(1)
	if r, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ?`, cc, id); err == nil {
		next = toInt(r.Rows[0][0])
	}
	kept := map[int64]bool{}
	var total int64
	for _, b := range base {
		total += b.amount
	}
	for _, r := range results {
		d := r.Dependent
		acc, allocated := d.Account, false
		switch {
		case d.Effect == pricing.EffectShare:
			if acc == "" {
				allocated = true // ohne eigenes Konto bleibt der Anteil in der Basis
			} else {
				for key, s := range r.Shares {
					var line int64
					fmt.Sscan(key, &line)
					cost[line] -= s
				}
			}
		case d.Method == pricing.MethodTax:
			t, err := taxOf(r.TaxCode)
			if err != nil {
				return crud.Invalid("%v", err)
			}
			if (t.kind == "INPUT" || t.kind == "OUTPUT") && t.account != "" {
				acc = t.account
			} else {
				acc, allocated = "", true // nicht abziehbar: in den Aufwand der Basis
			}
		default:
			allocated = acc == ""
		}
		switch {
		case allocated && d.Effect == pricing.EffectShare:
			if len(base) > 0 { // Anteil ohne eigenes Konto: nur zur Information, Kosten bleiben in der Basis
				acc = base[0].account
			}
		case allocated:
			if a := allocate(d, r.TaxCode, r.Amount); acc == "" {
				acc = a
			}
		}
		if acc != "" {
			if nr, err := m.glAccount(ctx, cc, acc); err == nil {
				acc = nr
			}
		}
		if d.Effect != pricing.EffectShare {
			total += r.Amount
		}
		key := d.Code + "|" + r.TaxCode
		text := r.Text(dec)
		if line, ok := existing[key]; ok {
			kept[line] = true
			if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET amount = ?, item_text = ?, account_number = ?, dependent_effect = ?, allocated = ?,
				cost_amount = NULL WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
				r.Amount, text, nilIfEmpty(acc), d.Effect, allocated, cc, id, line); err != nil {
				return err
			}
			continue
		}
		if acc == "" && k.invoice {
			return crud.Invalid("Nachgelagerte Position %s: Sachkonto fehlt (an der Art angeben)", firstNonEmpty(d.Name, d.Code))
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO `+k.items+` (company_code, `+k.key+`, line_no, account_number, amount, tax_code, item_text,
			dependent_type, dependent_effect, allocated, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			cc, id, next, nilIfEmpty(acc), r.Amount, nilIfEmpty(r.TaxCode), text, d.Code, d.Effect, allocated, true); err != nil {
			return err
		}
		kept[next] = true
		next++
	}
	for _, line := range existing {
		if !kept[line] {
			if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET is_active = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
				false, cc, id, line); err != nil {
				return err
			}
		}
	}
	lineNos := make([]int64, 0, len(cost))
	for l := range cost {
		lineNos = append(lineNos, l)
	}
	sort.Slice(lineNos, func(i, j int) bool { return lineNos[i] < lineNos[j] })
	for _, l := range lineNos {
		if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET cost_amount = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
			cost[l], cc, id, l); err != nil {
			return err
		}
	}
	if !k.invoice {
		_, err := m.db.Exec(ctx, `UPDATE procurement__quote SET amount = ? WHERE company_code = ? AND quote_id = ?`, total, cc, id)
		return err
	}
	if m.autoBalance(ctx, cc, id) {
		_, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET invoice_amount = ? WHERE company_code = ? AND invoice_id = ?`, total, cc, id)
		return err
	}
	return nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// docTotal: Summe der Positionen ohne Anteile (die sind in der Basis enthalten).
func (m *Module) docTotal(ctx context.Context, k docKind, cc, id string, exceptLine int64) (int64, error) {
	res, err := m.db.Query(ctx, `SELECT COALESCE(SUM(amount), 0) FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ? AND is_active = ?
		AND line_no <> ? AND COALESCE(dependent_effect, '') <> ?`, cc, id, true, exceptLine, pricing.EffectShare)
	if err != nil {
		return 0, err
	}
	return toInt(res.Rows[0][0]), nil
}

// --- Positionen des Vertrags (Vertragsmodul) ------------------------------------------

type contractPos struct {
	Currency string `json:"currency"`
	Items    []struct {
		ConditionType string `json:"condition_type"`
		Name          string `json:"name"`
		Object        string `json:"object_id"`
		Quantity      string `json:"quantity"`
		Unit          string `json:"unit"`
		UnitPrice     string `json:"unit_price"`
		Amount        string `json:"amount"`
		Account       string `json:"account_number"`
		TaxCode       string `json:"tax_code"`
		Note          string `json:"note"`
		Calc          string `json:"calc_method"`
	} `json:"items"`
	Dependents []struct {
		Type string `json:"dependent_type"`
	} `json:"dependents"`
}

func (m *Module) contractPositions(ctx context.Context, cc, contract, date string) (*contractPos, error) {
	resp, err := m.services.Call(ctx, "ContractCondition", "positions", map[string]any{"company_code": cc, "contract_id": contract, "date": date})
	if err != nil {
		return nil, err
	}
	var p contractPos
	if err := sdk.Decode(resp.Payload, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// isNet: Beleg mit Nettopositionen (seit 0.8.0).
func (m *Module) isNet(ctx context.Context, k docKind, cc, id string) bool {
	return crud.AsBool(m.text(ctx, `SELECT net_amounts FROM `+k.head+` WHERE company_code = ? AND `+k.key+` = ?`, cc, id))
}

// hasItems: Beleg hat aktive Positionen.
func (m *Module) hasItems(ctx context.Context, k docKind, cc, id string) bool {
	res, err := m.db.Query(ctx, `SELECT 1 FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ? AND is_active = ?`, cc, id, true)
	return err == nil && len(res.Rows) > 0
}

// takeQuoteAction: Positionen des angenommenen Angebots (Feld Angebot der Rechnung)
// übernehmen – ersetzt die Positionen; Netto und nachgelagerte Arten wie am Angebot.
func (m *Module) takeQuoteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, "update", inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if inv.Status != invoiceOpen {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s und nicht mehr änderbar", inv.ID, statusText(inv.Status))
	}
	quote := m.text(ctx, `SELECT quote_id FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, inv.CompanyCode, inv.ID)
	if quote == "" {
		return sdk.Response{}, crud.Invalid("Rechnung %s: Angebot angeben (Feld Angebot)", inv.ID)
	}
	head, err := m.db.Query(ctx, `SELECT net_amounts, dependent_types, currency FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, inv.CompanyCode, quote)
	if err != nil || len(head.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Angebot %s gibt es nicht", quote)
	}
	items, err := m.db.Query(ctx, `SELECT item_text, quantity, unit, unit_price, amount, tax_code, cost_category, account_number, rounding_rule
		FROM procurement__quote_item WHERE company_code = ? AND quote_id = ? AND is_active = ? AND dependent_type IS NULL ORDER BY line_no`,
		inv.CompanyCode, quote, true)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(items.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Angebot %s hat keine Positionen", quote)
	}
	dec := m.currencyDecimals(ctx, crud.Str(head.Rows[0][2]))
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET net_amounts = ?, dependent_types = ? WHERE company_code = ? AND invoice_id = ?`,
			crud.AsBool(head.Rows[0][0]), head.Rows[0][1], inv.CompanyCode, inv.ID); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET is_active = ? WHERE company_code = ? AND invoice_id = ? AND is_active = ?`,
			false, inv.CompanyCode, inv.ID, true); err != nil {
			return err
		}
		for i, r := range items.Rows {
			data := map[string]any{"company_code": inv.CompanyCode, "invoice_id": inv.ID, "item_text": r[0], "quantity": r[1], "unit": r[2],
				"unit_price": firstNonEmpty(crud.Str(r[3]), formatAmount(toInt(r[4]), dec)), "tax_code": r[5], "cost_category": r[6],
				"account_number": r[7], "rounding_rule": r[8]}
			for k, v := range data {
				if v == nil || crud.Str(v) == "" {
					delete(data, k)
				}
			}
			if _, err := m.set.Entity(itemObject).Create(ctx, map[string]any{"data": data}); err != nil {
				return fmt.Errorf("Position %d: %w", i+1, err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID,
		"message": fmt.Sprintf("Rechnung %s: %d Positionen aus Angebot %s übernommen – bitte prüfen", inv.ID, len(items.Rows), quote)}}, nil
}
