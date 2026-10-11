package contract

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

// Positionen der Konditionen (seit 0.22.0): Menge × Einzelpreis = Gesamtpreis je
// Periode, gerundet nach der Rundungsregel (Kondition, Konditionsart, Buchungskreis,
// Standard; Plugin ledger). Je Einheit der Bemessung: Einzelpreis × Bemessung des
// Objekts. Nachgelagert: aus den anderen Konditionen des Vertrags (Steuer, Prozent;
// Zuschlag oder Anteil) – gerechnet in der Sollstellung (contract-billing) und für
// Rechnungen zum Vertrag (Positionen, Route ContractCondition.positions).

// prepareCondition: nachgelagerte Konditionen brauchen keinen Rhythmus und keine Zahlungsweise.
func prepareCondition(_ context.Context, rec crud.Record) error {
	if crud.Str(rec["calc_method"]) == calcDependent {
		defaults(rec, map[string]any{"frequency": "MONTHLY", "payment_mode": "IN_ADVANCE"})
		if crud.Str(rec["frequency"]) == "" {
			rec["frequency"] = "MONTHLY"
		}
		if crud.Str(rec["payment_mode"]) == "" {
			rec["payment_mode"] = "IN_ADVANCE"
		}
	}
	return nil
}

// roundingFor: Rundungsregel der Kondition (rounding_rule), sonst der Konditionsart.
func (m *Module) roundingFor(ctx context.Context, cc, typ, own string, decimals int) pricing.Rounding {
	code := strings.TrimSpace(own)
	if code == "" {
		code = m.text(ctx, `SELECT rounding_rule FROM contract__condition_type WHERE company_code = ? AND code = ?`, cc, typ)
	}
	return pricing.New(m.services).Rounding(ctx, cc, code, decimals)
}

// conditionPrice: Menge, Einheit, Einzelpreis und Gesamtpreis bzw. die nachgelagerte Art.
func (m *Module) conditionPrice(ctx context.Context, rec, old crud.Record, c *contractRow) error {
	dec := m.currencyDecimals(ctx, c.Currency)
	cc, typ := c.CompanyCode, crud.Str(rec["condition_type"])
	rec["rounding_rule"] = nilIfEmpty(trimUpper(rec["rounding_rule"]))
	calc := crud.Str(rec["calc_method"])
	if calc == "" {
		calc = calcFixed
	}
	rec["calc_method"] = calc
	switch calc {
	case calcDependent:
		code := trimUpper(rec["dependent_type"])
		if code == "" {
			return crud.Invalid("Nachgelagerte Konditionsart ist Pflicht")
		}
		res, err := m.db.Query(ctx, `SELECT 1 FROM contract__dependent_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Nachgelagerte Konditionsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
		}
		rec["dependent_type"], rec["amount"] = code, int64(0)
		rec["quantity"], rec["unit"], rec["unit_price"], rec["measurement_type"] = nil, nil, nil, nil
		return nil
	case calcPerUnit:
		if crud.Str(rec["object_id"]) == "" {
			return crud.Invalid("Berechnung je Einheit braucht ein Objekt (dessen Bemessung zählt)")
		}
		if crud.Str(rec["measurement_type"]) == "" {
			return crud.Invalid("Berechnung je Einheit braucht die Bemessungsart, z. B. WFL")
		}
		rec["measurement_type"] = trimUpper(rec["measurement_type"])
		price, err := m.priceInput(rec, old, dec)
		if err != nil {
			return err
		}
		r := m.roundingFor(ctx, cc, typ, crud.Str(rec["rounding_rule"]), dec)
		if d := pricing.Decimals(price); d > max(r.PriceDecimals, dec) {
			return crud.Invalid("Einzelpreis: höchstens %d Nachkommastellen (Rundungsregel %s)", max(r.PriceDecimals, dec), firstNonEmpty(r.Code, "Standard"))
		}
		rec["unit_price"] = pricing.FormatTrim(price, 6)
		rec["amount"] = r.Minor(price, dec) // Sollstellung: Einzelpreis (genau: unit_price) × Bemessung
		rec["quantity"], rec["unit"], rec["dependent_type"] = nil, nil, nil
		return nil
	case calcFixed:
	default:
		return crud.Invalid("Berechnung %q unbekannt", calc)
	}
	rec["measurement_type"], rec["dependent_type"] = nil, nil
	price, err := m.priceInput(rec, old, dec)
	if err != nil {
		return err
	}
	q := big.NewRat(1, 1)
	if s := strings.TrimSpace(crud.Str(rec["quantity"])); s != "" {
		if q, err = pricing.ParseDecimal(s); err != nil {
			return crud.Invalid("Menge: %v", err)
		}
		if q.Sign() <= 0 {
			return crud.Invalid("Menge muss größer 0 sein (Gutschrift: Einzelpreis negativ)")
		}
	}
	unit := trimUpper(rec["unit"])
	if unit == "" {
		unit = defaultUnit
	}
	if resp, err := m.services.Call(ctx, "Unit", "get", map[string]any{"id": unit}); err != nil {
		return crud.Invalid("Mengeneinheit %s: %v", unit, err)
	} else {
		var u struct {
			Decimals any `json:"decimals"`
		}
		if sdk.Decode(resp.Payload, &u) == nil && u.Decimals != nil && pricing.Decimals(q) > int(toInt(u.Decimals)) {
			return crud.Invalid("Menge %s: höchstens %d Nachkommastellen bei %s", pricing.FormatTrim(q, 12), toInt(u.Decimals), unit)
		}
	}
	r := m.roundingFor(ctx, cc, typ, crud.Str(rec["rounding_rule"]), dec)
	total, err := r.Total(q, price, dec)
	if err != nil {
		return crud.Invalid("%v", err)
	}
	qf, _ := q.Float64()
	rec["quantity"], rec["unit"], rec["unit_price"], rec["amount"] = qf, unit, pricing.FormatTrim(price, 6), total
	return nil
}

// priceInput: Einzelpreis aus unit_price; ohne Einzelpreis (ältere Aufrufer) bzw. bei
// geändertem Gesamtpreis ohne neuen Einzelpreis gilt der Gesamtpreis als Einzelpreis (Menge 1).
func (m *Module) priceInput(rec, old crud.Record, dec int) (*big.Rat, error) {
	priceText := strings.TrimSpace(crud.Str(rec["unit_price"]))
	amountText := strings.TrimSpace(crud.Str(rec["amount"]))
	if old != nil && amountText != "" && priceText == strings.TrimSpace(crud.Str(old["unit_price"])) &&
		crud.Str(rec["quantity"]) == crud.Str(old["quantity"]) && crud.Str(rec["calc_method"]) == calcFixed {
		if minor, err := parseAmount(amountText, dec); err == nil && minor != toInt(old["amount"]) {
			priceText, rec["quantity"] = amountText, nil // Gesamtpreis geändert
		}
	}
	if priceText == "" {
		if amountText == "" {
			return nil, crud.Invalid("Einzelpreis ist Pflicht")
		}
		priceText = amountText
		if _, isInt := rec["amount"].(int64); isInt { // schon in der kleinsten Einheit
			return pricing.FromMinor(rec["amount"].(int64), dec), nil
		}
	}
	p, err := pricing.ParseDecimal(priceText)
	if err != nil {
		return nil, crud.Invalid("Einzelpreis: %v", err)
	}
	return p, nil
}

// dependentAccount: Sachkonto der nachgelagerten Kondition – eigenes, das der Art,
// bei Steuer mit festem Kennzeichen das Steuerkonto, sonst die Kontenfindung
// (Steuer ohne festes Kennzeichen: Steuerkonto je Kennzeichen in der Sollstellung).
func (m *Module) dependentAccount(ctx context.Context, rec crud.Record, c *contractRow) error {
	d, err := m.dependentTypeOf(ctx, c.CompanyCode, crud.Str(rec["dependent_type"]))
	if err != nil {
		return err
	}
	acc := firstNonEmpty(strings.TrimSpace(crud.Str(rec["account_number"])), d.Account)
	if acc == "" && d.Method == pricing.MethodTax {
		if d.TaxCode != "" {
			if _, _, a, err := pricing.New(m.services).TaxRate(ctx, d.TaxCode); err == nil {
				acc = a
			}
		}
		if acc == "" {
			rec["account_number"] = nil
			return nil
		}
	}
	if acc == "" {
		return m.accountFor(ctx, rec, c)
	}
	nr, _, err := m.glAccount(ctx, c.CompanyCode, acc)
	if err != nil {
		return err
	}
	rec["account_number"] = nr
	return nil
}

func (m *Module) dependentTypeOf(ctx context.Context, cc, code string) (pricing.Dependent, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, method, tax_code, percent, effect, base_types, account_number, rounding_rule, sort_order, invoice_default
		FROM contract__dependent_type WHERE company_code = ? AND code = ?`, cc, code)
	if err != nil {
		return pricing.Dependent{}, err
	}
	if len(res.Rows) == 0 {
		return pricing.Dependent{}, crud.Invalid("Nachgelagerte Konditionsart %q gibt es im Buchungskreis %s nicht", code, cc)
	}
	r := res.Rows[0]
	return pricing.DependentOf(map[string]any{"code": r[0], "name": r[1], "method": r[2], "tax_code": r[3], "percent": r[4], "effect": r[5],
		"base_types": r[6], "account_number": r[7], "rounding_rule": r[8], "sort_order": r[9], "invoice_default": r[10]}), nil
}

// --- Positionen eines Vertrags zum Stichtag ---------------------------------------------

// position ist eine Kondition als Position (Rechnung zum Vertrag, Anzeige).
type position struct {
	ConditionType, Name, Object, Unit, Account, TaxCode, Frequency, Note, Calc string
	Quantity, Price                                                            *big.Rat
	Amount                                                                     int64
}

// positions: am Stichtag gültige Konditionen (ohne Rechnungsvorlagen) und die
// nachgelagerten daraus.
func (m *Module) positions(ctx context.Context, c *contractRow, date string) ([]position, []pricing.Result, int, error) {
	dec := m.currencyDecimals(ctx, c.Currency)
	res, err := m.db.Query(ctx, `SELECT k.condition_type, t.name, k.object_id, k.calc_method, k.amount, k.quantity, k.unit, k.unit_price,
		k.measurement_type, k.account_number, COALESCE(t.tax_code, ct.default_tax_code), k.frequency, k.note, k.dependent_type, k.rounding_rule
		FROM contract__condition k JOIN contract__condition_type t ON t.company_code = k.company_code AND t.code = k.condition_type
		LEFT JOIN contract__contract_type ct ON ct.company_code = k.company_code AND ct.code = k.contract_type
		WHERE k.company_code = ? AND k.contract_id = ? AND k.valid_from <= ? AND k.valid_to >= ? AND t.is_template = ?
		ORDER BY t.sort_order, k.condition_type, k.object_id`, c.CompanyCode, c.ID, date, date, false)
	if err != nil {
		return nil, nil, dec, err
	}
	var out []position
	var deps []pricing.Dependent
	for _, r := range res.Rows {
		s := func(i int) string { return crud.Str(r[i]) }
		p := position{ConditionType: s(0), Name: s(1), Object: s(2), Calc: s(3), Unit: s(6), Account: s(9), TaxCode: s(10), Frequency: s(11), Note: s(12)}
		switch p.Calc {
		case calcDependent:
			d, err := m.dependentTypeOf(ctx, c.CompanyCode, s(13))
			if err != nil {
				return nil, nil, dec, err
			}
			if a := s(9); a != "" {
				d.Account = a
			}
			if rr := s(14); rr != "" {
				d.Rounding = rr
			}
			d.Name = firstNonEmpty(p.Note, d.Name, p.Name)
			deps = append(deps, d)
			continue
		case calcPerUnit:
			price, err := pricing.ParseDecimal(firstNonEmpty(s(7), formatAmount(toInt(r[4]), dec)))
			if err != nil {
				return nil, nil, dec, err
			}
			p.Price = price
			v, err := m.measurementOf(ctx, c.CompanyCode, p.Object, s(8), date)
			if err != nil {
				return nil, nil, dec, crud.Invalid("Kondition %s: Bemessung %s für %s fehlt am %s", p.ConditionType, s(8), p.Object, date)
			}
			p.Quantity, p.Unit = v, s(8)
			p.Amount = m.roundingFor(ctx, c.CompanyCode, p.ConditionType, s(14), dec).Minor(new(big.Rat).Mul(v, price), dec)
		default:
			p.Amount = toInt(r[4])
			p.Quantity = big.NewRat(1, 1)
			if q := s(5); q != "" {
				if v, err := pricing.ParseDecimal(q); err == nil {
					p.Quantity = v
				}
			}
			p.Price = pricing.FromMinor(p.Amount, dec)
			if pr := s(7); pr != "" {
				if v, err := pricing.ParseDecimal(pr); err == nil {
					p.Price = v
				}
			}
			p.Unit = firstNonEmpty(p.Unit, defaultUnit)
		}
		out = append(out, p)
	}
	var lines []pricing.Line
	for i, p := range out {
		lines = append(lines, pricing.Line{Key: fmt.Sprint(i), Kinds: []string{p.ConditionType}, TaxCode: p.TaxCode, Amount: p.Amount})
	}
	results, err := pricing.Apply(deps, lines, dec, func(code string) (*big.Rat, error) {
		r, _, _, err := pricing.New(m.services).TaxRate(ctx, code)
		return r, err
	}, func(code string) pricing.Rounding { return pricing.New(m.services).Rounding(ctx, c.CompanyCode, code, dec) })
	if err != nil {
		return nil, nil, dec, crud.Invalid("%v", err)
	}
	return out, results, dec, nil
}

// positionsAction: ContractCondition.positions {company_code, contract_id, date} –
// Positionen (Menge, Einheit, Einzelpreis, Gesamtpreis, Sachkonto, Steuer) und
// nachgelagerte Positionen (Art, Basis, Satz, Betrag, Sachkonto).
func (m *Module) positionsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		Contract    string `json:"contract_id"`
		Date        string `json:"date"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if d, ok := req.Payload.(map[string]any)["data"]; ok {
		_ = sdk.Decode(d, &in)
	}
	cc := strings.TrimSpace(in.CompanyCode)
	c, err := m.contractOf(ctx, cc, strings.TrimPrefix(strings.TrimSpace(in.Contract), cc+"|"))
	if err != nil {
		return sdk.Response{}, err
	}
	if ok, err := sdk.CheckAccess(ctx, "Contract", "get", cc); err != nil || !ok {
		return sdk.Response{}, fmt.Errorf("%w: Vertrag %s lesen", sdk.ErrPermissionDenied, c.ID)
	}
	date := firstNonEmpty(strings.TrimSpace(in.Date), crud.KeyDate(ctx))
	if len(date) > 10 {
		date = date[:10]
	}
	ps, deps, dec, err := m.positions(ctx, c, date)
	if err != nil {
		return sdk.Response{}, err
	}
	items := []any{}
	for _, p := range ps {
		items = append(items, map[string]any{"condition_type": p.ConditionType, "name": p.Name, "object_id": p.Object, "calc_method": p.Calc,
			"quantity": pricing.FormatTrim(p.Quantity, 6), "unit": p.Unit, "unit_price": pricing.FormatTrim(p.Price, 6),
			"amount": formatAmount(p.Amount, dec), "account_number": p.Account, "tax_code": p.TaxCode, "frequency": p.Frequency, "note": p.Note})
	}
	dl := []any{}
	for _, d := range deps {
		dl = append(dl, map[string]any{"dependent_type": d.Dependent.Code, "name": d.Dependent.Name, "method": d.Dependent.Method,
			"effect": d.Dependent.Effect, "tax_code": d.TaxCode, "rate": pricing.FormatTrim(d.Rate, 6), "base": formatAmount(d.Base, dec),
			"amount": formatAmount(d.Amount, dec), "account_number": d.Dependent.Account, "text": d.Text(dec)})
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "contract_id": c.ID, "date": date, "currency": c.Currency,
		"items": items, "dependents": dl}}, nil
}

// conditionTotal: Gesamtpreis je Periode zur Anzeige (virtuelles Feld total).
func (m *Module) conditionTotal(ctx context.Context, rec crud.Record, c *contractRow, dec int) string {
	switch crud.Str(rec["calc_method"]) {
	case calcPerUnit:
		price, err := pricing.ParseDecimal(firstNonEmpty(crud.Str(rec["unit_price"]), crud.Str(rec["amount"])))
		if err != nil {
			return ""
		}
		date := maxStr(crud.KeyDate(ctx), dateOf(rec["valid_from"]))
		v, err := m.measurementOf(ctx, c.CompanyCode, crud.Str(rec["object_id"]), crud.Str(rec["measurement_type"]), date)
		if err != nil {
			return "Bemessung " + crud.Str(rec["measurement_type"]) + " fehlt"
		}
		t := m.roundingFor(ctx, c.CompanyCode, crud.Str(rec["condition_type"]), crud.Str(rec["rounding_rule"]), dec).Minor(new(big.Rat).Mul(v, price), dec)
		return fmt.Sprintf("%s %s (%s %s × %s)", formatAmount(t, dec), c.Currency, pricing.FormatTrim(v, 4), crud.Str(rec["measurement_type"]), pricing.FormatTrim(price, 6))
	case calcDependent:
		date := maxStr(crud.KeyDate(ctx), dateOf(rec["valid_from"]))
		if date > dateOf(rec["valid_to"]) {
			date = dateOf(rec["valid_to"])
		}
		_, deps, _, err := m.positions(ctx, c, date)
		if err != nil {
			return err.Error()
		}
		var parts []string
		var sum int64
		for _, d := range deps {
			if d.Dependent.Code == crud.Str(rec["dependent_type"]) {
				sum += d.Amount
				parts = append(parts, d.Text(dec))
			}
		}
		if len(parts) == 0 {
			return "keine Basis am " + date
		}
		return fmt.Sprintf("%s %s (%s)", formatAmount(sum, dec), c.Currency, strings.Join(parts, "; "))
	}
	return crud.Str(rec["amount"]) + " " + c.Currency
}

// migratePositions: Konditionen ohne Einzelpreis (vor 0.22.0) – Menge 1, Stück,
// Einzelpreis = bisheriger Betrag (je Einheit: Preis je Einheit).
func (m *Module) migratePositions(ctx context.Context) {
	res, err := m.db.Query(ctx, `SELECT k.company_code, k.contract_id, k.condition_type, k.object_id, k.valid_from, k.calc_method, k.amount, c.currency
		FROM contract__condition k JOIN contract__contract c ON c.company_code = k.company_code AND c.contract_id = k.contract_id
		WHERE k.unit_price IS NULL AND k.calc_method IN (?, ?)`, calcFixed, calcPerUnit)
	if err != nil {
		m.log.WarnContext(ctx, "Positionen der Konditionen nicht übernommen", "err", err.Error())
		return
	}
	decs := map[string]int{}
	n := 0
	for _, r := range res.Rows {
		cur := crud.Str(r[7])
		if _, ok := decs[cur]; !ok {
			decs[cur] = m.currencyDecimals(ctx, cur)
		}
		price := formatAmount(toInt(r[6]), decs[cur])
		var q, unit any
		if crud.Str(r[5]) == calcFixed {
			q, unit = 1.0, defaultUnit
		}
		if _, err := m.db.Exec(ctx, `UPDATE contract__condition SET unit_price = ?, quantity = ?, unit = ?
			WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ? AND valid_from = ?`,
			price, q, unit, r[0], r[1], r[2], r[3], r[4]); err != nil {
			m.log.WarnContext(ctx, "Position der Kondition nicht übernommen", "contract", crud.Str(r[1]), "err", err.Error())
			continue
		}
		n++
	}
	if n > 0 {
		m.log.InfoContext(ctx, "Konditionen mit Menge und Einzelpreis ergänzt", "count", n)
	}
}
