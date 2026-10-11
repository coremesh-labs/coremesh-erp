package procurement

import (
	"context"
	"math/big"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

// Kalkulationsschema (seit 0.9.0): Eine Kette abhängiger Positionen wird einmal
// definiert und an Hauptpositionen gehängt (Feld Kalkulationsschema), z. B. STROM am
// Arbeitspreis: Netzentgelt, Konzessionsabgabe, Stromsteuer je kWh. Jeder Schritt
// bezieht sich auf die Hauptposition (0) oder einen früheren Schritt und rechnet
//
//   - QUANTITY: Menge des Bezugs × Satz je Einheit (z. B. 0,0205 €/kWh),
//   - AMOUNT:   Satz in Prozent vom Betrag des Bezugs.
//
// Sätze sind Zeitscheiben (gültig ab); maßgeblich ist der Leistungsbeginn der
// Hauptposition, sonst das Belegdatum. Der Satz lässt sich an der abhängigen Zeile
// überschreiben (z. B. abweichender Satz des Versorgers); „Betrag laut Rechnung“
// zeigt die Abweichung zum gerechneten Betrag. Die Umsatzsteuer bleibt die
// nachgelagerte Position auf Belegebene.

const (
	schemeObject     = "PricingScheme"
	schemeStepObject = "PricingSchemeStep"

	basisQuantity = "QUANTITY"
	basisAmount   = "AMOUNT"
)

var (
	basisOptions = []metamodel.Option{{Value: basisQuantity, Label: "Menge des Bezugs × Satz je Einheit", Short: "je Menge"},
		{Value: basisAmount, Label: "Prozent vom Betrag des Bezugs", Short: "% vom Betrag"}}
	lookupScheme = &metamodel.Lookup{Object: schemeObject, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
)

func (m *Module) scheme() *crud.Entity {
	return &crud.Entity{
		Object: schemeObject, Title: "Kalkulationsschemas", Icon: "icon-list", Table: "procurement__scheme", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "schritte", Title: "Schritte", Relation: &metamodel.Relation{Object: schemeStepObject, ForeignKey: "scheme",
				Match:   map[string]string{"company_code": "company_code", "scheme": "code"},
				Columns: []string{"step", "name", "ref_step", "basis", "rate", "unit", "tax_code", "cost_category", "valid_from", "valid_to"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
			}
			return nil
		},
	}
}

func (m *Module) schemeStep() *crud.Entity {
	return &crud.Entity{
		Object: schemeStepObject, Title: "Schritte der Kalkulationsschemas", Icon: "icon-list", Table: "procurement__scheme_step", Section: "Einstellungen",
		Keys: []string{"company_code", "scheme", "step", "valid_from"}, TimeSlice: true, Order: "company_code, scheme, step, valid_from",
		Filters: []string{"company_code", "scheme"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "scheme", Label: "Kalkulationsschema", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupScheme},
			crud.Field{Key: "step", Label: "Schritt (z. B. 10, 20 …)", Type: tNum, Required: true, Listable: true, Immutable: true},
			crud.Field{Key: "name", Label: "Bezeichnung (Text der Zeile)", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "ref_step", Label: "Bezug (0 = Hauptposition, sonst ein früherer Schritt)", Type: tNum, Listable: true},
			crud.Field{Key: "basis", Label: "Rechenregel", Type: tSel, Required: true, Listable: true, Options: basisOptions},
			crud.Field{Key: "rate", Label: "Satz (je Einheit bzw. Prozent)", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "unit", Label: "Mengeneinheit (leer = die des Bezugs)", Type: tText, Listable: true, Lookup: lookupUnit},
			crud.Field{Key: "tax_code", Label: "Steuerkennzeichen (leer = das der Hauptposition)", Type: tText, Listable: true, Lookup: lookupTaxCode},
			crud.Field{Key: "cost_category", Label: "Kostenart (leer = die der Hauptposition)", Type: tText, Listable: true, Lookup: catalogLookup("CostCategory")},
			crud.Field{Key: "account_number", Label: "Sachkonto (leer = Kostenart bzw. Hauptposition)", Type: tText, Lookup: lookupAccount},
			crud.Field{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
		),
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			rec["scheme"] = trimUpper(rec["scheme"])
			if m.text(ctx, `SELECT code FROM procurement__scheme WHERE company_code = ? AND code = ?`, cc, rec["scheme"]) == "" {
				return crud.Invalid("Kalkulationsschema %s gibt es im Buchungskreis %s nicht", crud.Str(rec["scheme"]), cc)
			}
			step, ref := toInt(rec["step"]), toInt(rec["ref_step"])
			if step <= 0 {
				return crud.Invalid("Schritt muss größer 0 sein")
			}
			if ref < 0 || ref >= step {
				return crud.Invalid("Bezug: 0 (Hauptposition) oder ein früherer Schritt (kleiner als %d)", step)
			}
			rec["ref_step"] = ref
			r, err := pricing.ParseDecimal(crud.Str(rec["rate"]))
			if err != nil {
				return crud.Invalid("Satz: %v", err)
			}
			rec["rate"] = pricing.FormatTrim(r, 8)
			for _, k := range []string{"unit", "tax_code", "cost_category", "rounding_rule"} {
				rec[k] = nilIfEmpty(trimUpper(rec[k]))
			}
			if err := m.checkTaxCode(ctx, rec); err != nil {
				return err
			}
			if cat := crud.Str(rec["cost_category"]); cat != "" {
				if _, _, err := m.costCategoryOf(ctx, cc, cat); err != nil {
					return err
				}
			}
			if acc := strings.TrimSpace(crud.Str(rec["account_number"])); acc != "" {
				nr, err := m.glAccount(ctx, cc, acc)
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

// schemeStep ist ein Schritt am Stichtag.
type schemeStepRow struct {
	Step, Ref                                        int64
	Name, Basis, Unit, TaxCode, Cost, Account, Round string
	Rate                                             *big.Rat
}

func (m *Module) schemeSteps(ctx context.Context, cc, scheme, date string) ([]schemeStepRow, error) {
	res, err := m.db.Query(ctx, `SELECT step, ref_step, name, basis, rate, unit, tax_code, cost_category, account_number, rounding_rule
		FROM procurement__scheme_step WHERE company_code = ? AND scheme = ? AND valid_from <= ? AND valid_to >= ? ORDER BY step`, cc, scheme, date, date)
	if err != nil {
		return nil, err
	}
	var out []schemeStepRow
	for _, r := range res.Rows {
		rate, err := pricing.ParseDecimal(crud.Str(r[4]))
		if err != nil {
			return nil, crud.Invalid("Schema %s Schritt %d: Satz %v", scheme, toInt(r[0]), r[4])
		}
		out = append(out, schemeStepRow{Step: toInt(r[0]), Ref: toInt(r[1]), Name: crud.Str(r[2]), Basis: crud.Str(r[3]), Rate: rate,
			Unit: crud.Str(r[5]), TaxCode: crud.Str(r[6]), Cost: crud.Str(r[7]), Account: crud.Str(r[8]), Round: crud.Str(r[9])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Step < out[j].Step })
	return out, nil
}

// expandSchemes: abhängige Zeilen der Hauptpositionen mit Kalkulationsschema neu
// rechnen (anlegen, ändern, entfernen). Läuft vor den nachgelagerten Positionen –
// die abhängigen Zeilen sind deren Basis.
func (m *Module) expandSchemes(ctx context.Context, k docKind, cc, id string, skip int64, dec int) error {
	docDate := ""
	if k.invoice {
		docDate = dateOnly(m.text(ctx, `SELECT invoice_date FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, cc, id))
	} else {
		docDate = dateOnly(m.text(ctx, `SELECT quote_date FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, cc, id))
	}
	extra := "NULL, NULL, NULL, NULL, NULL, NULL"
	if k.invoice {
		extra = "object_type, object_id, service_from, service_to, allocable, cost_center"
	}
	masters, err := m.db.Query(ctx, `SELECT line_no, scheme, quantity, unit, amount, tax_code, cost_category, account_number, `+extra+`
		FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ? AND is_active = ? AND scheme IS NOT NULL AND parent_line IS NULL
		AND dependent_type IS NULL AND line_no <> ? ORDER BY line_no`, cc, id, true, skip)
	if err != nil {
		return err
	}
	kids, err := m.db.Query(ctx, `SELECT line_no, parent_line, scheme_step, unit_price, price_override FROM `+k.items+`
		WHERE company_code = ? AND `+k.key+` = ? AND is_active = ? AND parent_line IS NOT NULL`, cc, id, true)
	if err != nil {
		return err
	}
	type kid struct {
		line     int64
		price    string
		override bool
	}
	existing := map[[2]int64]kid{}
	for _, r := range kids.Rows {
		existing[[2]int64{toInt(r[1]), toInt(r[2])}] = kid{toInt(r[0]), crud.Str(r[3]), crud.AsBool(r[4])}
	}
	kept := map[int64]bool{}
	next := int64(1)
	if r, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ?`, cc, id); err == nil {
		next = toInt(r.Rows[0][0])
	}
	for _, r := range masters.Rows {
		master := toInt(r[0])
		date := firstNonEmpty(dateOnly(crud.Str(r[10])), docDate)
		steps, err := m.schemeSteps(ctx, cc, crud.Str(r[1]), date)
		if err != nil {
			return err
		}
		qty := big.NewRat(1, 1)
		if q, err := pricing.ParseDecimal(crud.Str(r[2])); err == nil {
			qty = q
		}
		type val struct {
			qty    *big.Rat
			amount int64
			unit   string
		}
		vals := map[int64]val{0: {qty, toInt(r[4]), crud.Str(r[3])}}
		for _, st := range steps {
			ref, ok := vals[st.Ref]
			if !ok {
				return crud.Invalid("Schema %s Schritt %d: Bezug %d fehlt am %s", crud.Str(r[1]), st.Step, st.Ref, date)
			}
			price := st.Rate
			ex, has := existing[[2]int64{master, st.Step}]
			override := has && ex.override
			if override {
				if p, err := pricing.ParseDecimal(ex.price); err == nil {
					price = p
				}
			}
			round := pricing.New(m.services).Rounding(ctx, cc, st.Round, dec)
			var q any
			var amount int64
			unit := firstNonEmpty(st.Unit, ref.unit)
			if st.Basis == basisAmount {
				amount = round.Minor(new(big.Rat).Mul(pricing.FromMinor(ref.amount, dec), new(big.Rat).Quo(price, big.NewRat(100, 1))), dec)
				unit = "PCT"
			} else {
				qf, _ := ref.qty.Float64()
				q = qf
				amount = round.Minor(new(big.Rat).Mul(ref.qty, price), dec)
			}
			vals[st.Step] = val{ref.qty, amount, unit}
			cat := firstNonEmpty(st.Cost, crud.Str(r[6]))
			acc := st.Account
			allocable := crud.AsBool(r[12])
			if st.Cost != "" {
				catAcc, alloc, err := m.costCategoryOf(ctx, cc, st.Cost)
				if err != nil {
					return err
				}
				allocable = alloc
				if acc == "" {
					acc = catAcc
				}
			}
			acc = firstNonEmpty(acc, crud.Str(r[7]))
			tax := firstNonEmpty(st.TaxCode, crud.Str(r[5]))
			priceText := pricing.FormatTrim(price, 8)
			if has {
				kept[ex.line] = true
				if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET item_text = ?, quantity = ?, unit = ?, unit_price = ?, amount = ?, tax_code = ?,
					cost_category = ?, account_number = ?, rounding_rule = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
					st.Name, q, nilIfEmpty(unit), priceText, amount, nilIfEmpty(tax), nilIfEmpty(cat), nilIfEmpty(acc), nilIfEmpty(st.Round),
					cc, id, ex.line); err != nil {
					return err
				}
				if k.invoice {
					if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET object_type = ?, object_id = ?, service_from = ?, service_to = ?,
						allocable = ?, cost_center = ? WHERE company_code = ? AND invoice_id = ? AND line_no = ?`,
						r[8], r[9], r[10], r[11], allocable, r[13], cc, id, ex.line); err != nil {
						return err
					}
				}
				continue
			}
			if k.invoice {
				if acc == "" {
					return crud.Invalid("Schema %s Schritt %d: Sachkonto fehlt (Schritt, Kostenart oder Hauptposition)", crud.Str(r[1]), st.Step)
				}
				_, err = m.db.Exec(ctx, `INSERT INTO procurement__invoice_item (company_code, invoice_id, line_no, item_text, quantity, unit, unit_price,
					amount, tax_code, cost_category, account_number, rounding_rule, object_type, object_id, service_from, service_to, allocable, cost_center,
					parent_line, scheme_step, price_override, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					cc, id, next, st.Name, q, nilIfEmpty(unit), priceText, amount, nilIfEmpty(tax), nilIfEmpty(cat), acc, nilIfEmpty(st.Round),
					r[8], r[9], r[10], r[11], allocable, r[13], master, st.Step, false, true)
			} else {
				_, err = m.db.Exec(ctx, `INSERT INTO procurement__quote_item (company_code, quote_id, line_no, item_text, quantity, unit, unit_price,
					amount, tax_code, cost_category, account_number, rounding_rule, parent_line, scheme_step, price_override, is_active)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					cc, id, next, st.Name, q, nilIfEmpty(unit), priceText, amount, nilIfEmpty(tax), nilIfEmpty(cat), nilIfEmpty(acc), nilIfEmpty(st.Round),
					master, st.Step, false, true)
			}
			if err != nil {
				return err
			}
			kept[next] = true
			next++
		}
	}
	for _, ex := range existing {
		if !kept[ex.line] {
			if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET is_active = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
				false, cc, id, ex.line); err != nil {
				return err
			}
		}
	}
	return nil
}

// dateOnly: JJJJ-MM-TT aus einem Datum oder Zeitstempel.
func dateOnly(s string) string {
	if d, err := crud.ParseDate(s); err == nil {
		return d
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// parseStated: Betrag laut Rechnung (Prüfung) in der kleinsten Einheit.
func (m *Module) parseStated(ctx context.Context, rec crud.Record, currency string) error {
	v := rec["stated_amount"]
	if v == nil || strings.TrimSpace(crud.Str(v)) == "" {
		rec["stated_amount"] = nil
		return nil
	}
	if isMinor(v) {
		return nil
	}
	minor, err := parseAmount(crud.Str(v), m.currencyDecimals(ctx, currency))
	if err != nil {
		return crud.Invalid("Betrag laut Rechnung: %v", err)
	}
	rec["stated_amount"] = minor
	return nil
}
