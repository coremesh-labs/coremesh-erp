package procurement

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Ausgleich Soll und Haben (Rechnungsart auto_balance): Die Positionen sind
// das Soll, der Rechnungsbetrag laut Beleg das Haben.
//
//   - Position angelegt, geändert oder entfernt → der Rechnungsbetrag folgt
//     der Summe der Positionen (Haben ergänzt).
//   - Rechnungsbetrag geändert → die Differenz wird eine weitere Position
//     (Kontierung wie die letzte Position, Text „Differenz zum Rechnungsbetrag“).
//
// Steuer je Position (Steuerkennzeichen, Plugin ledger): Beträge sind brutto;
// abziehbare Vorsteuer teilt die Buchung in netto (Aufwand) und Steuer
// (Vorsteuerkonto), nicht abziehbare bleibt brutto im Aufwand.

func (m *Module) parseInvoiceAmount(ctx context.Context, rec crud.Record) error {
	v := rec["invoice_amount"]
	if v == nil || strings.TrimSpace(crud.Str(v)) == "" {
		rec["invoice_amount"] = nil
		return nil
	}
	if isMinor(v) {
		return nil
	}
	minor, err := parseAmount(crud.Str(v), m.currencyDecimals(ctx, crud.Str(rec["currency"])))
	if err != nil {
		return crud.Invalid("Rechnungsbetrag: %v", err)
	}
	rec["invoice_amount"] = minor
	return nil
}

func (m *Module) autoBalance(ctx context.Context, cc, id string) bool {
	t := m.text(ctx, `SELECT invoice_type FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, cc, id)
	typ, err := m.invoiceTypeOf(ctx, cc, t)
	return err == nil && typ.AutoBalance
}

// balanceAfterItem: Haben = Summe der Positionen (Position line mit neuem Betrag amount; 0 = entfernt).
func (m *Module) balanceAfterItem(ctx context.Context, cc, id string, line, amount int64) error {
	if !m.autoBalance(ctx, cc, id) {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT COALESCE(SUM(amount), 0) FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ?
		AND line_no <> ?`, cc, id, true, line)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE procurement__invoice SET invoice_amount = ? WHERE company_code = ? AND invoice_id = ?`,
		toInt(res.Rows[0][0])+amount, cc, id)
	return err
}

// balanceAfterAmount: Rechnungsbetrag geändert → Differenzposition.
func (m *Module) balanceAfterAmount(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"])
	if rec["invoice_amount"] == nil || !m.autoBalance(ctx, cc, id) {
		return nil
	}
	amount := toInt(rec["invoice_amount"])
	if prev := old["invoice_amount"]; prev != nil && crud.Str(prev) != "" {
		p := toInt(prev)
		if !isMinor(prev) {
			p, _ = parseAmount(crud.Str(prev), m.currencyDecimals(ctx, crud.Str(rec["currency"])))
		}
		if p == amount {
			return nil // unverändert
		}
	}
	res, err := m.db.Query(ctx, `SELECT COALESCE(SUM(amount), 0), MAX(line_no) FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ?`,
		cc, id, true)
	if err != nil {
		return err
	}
	diff := amount - toInt(res.Rows[0][0])
	if diff == 0 || crud.Str(res.Rows[0][1]) == "" {
		return nil // ausgeglichen bzw. noch keine Position (die erste übernimmt den Betrag)
	}
	last, err := m.db.Query(ctx, `SELECT cost_category, account_number, object_type, object_id, cost_center, contract_id, allocable, tax_code
		FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND line_no = ?`, cc, id, res.Rows[0][1])
	if err != nil || len(last.Rows) == 0 {
		return err
	}
	l := last.Rows[0]
	next := toInt(res.Rows[0][1]) + 1
	_, err = m.db.Exec(ctx, `INSERT INTO procurement__invoice_item (company_code, invoice_id, line_no, cost_category, account_number, amount,
		object_type, object_id, cost_center, contract_id, allocable, tax_code, item_text, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cc, id, next, l[0], l[1], diff, l[2], l[3], l[4], l[5], crud.AsBool(l[6]), l[7], "Differenz zum Rechnungsbetrag", true)
	return err
}

// --- Steuerkennzeichen -------------------------------------------------------------------

// taxInfo ist ein Steuerkennzeichen (Plugin ledger, Object TaxCode).
type taxInfo struct {
	Code, Kind, Account string
	Rate                float64
}

func (m *Module) taxCodeOf(ctx context.Context, code string) (*taxInfo, error) {
	resp, err := m.services.Call(ctx, "TaxCode", "get", map[string]any{"id": code})
	if err != nil {
		return nil, crud.Invalid("Steuerkennzeichen %s: %v", code, err)
	}
	var t struct {
		Kind     string `json:"kind"`
		Rate     any    `json:"rate"`
		Account  string `json:"account_number"`
		IsActive any    `json:"is_active"`
	}
	if err := sdk.Decode(resp.Payload, &t); err != nil {
		return nil, err
	}
	rate, _ := strconv.ParseFloat(strings.ReplaceAll(crud.Str(t.Rate), ",", "."), 64)
	return &taxInfo{Code: code, Kind: t.Kind, Account: t.Account, Rate: rate}, nil
}

func (m *Module) checkTaxCode(ctx context.Context, rec crud.Record) error {
	code := trimUpper(rec["tax_code"])
	if code == "" {
		rec["tax_code"] = nil
		return nil
	}
	if _, err := m.taxCodeOf(ctx, code); err != nil {
		return err
	}
	rec["tax_code"] = code
	return nil
}

// splitTax: Bruttobetrag → Steueranteil (abziehbare Vorsteuer; sonst 0), kaufmännisch gerundet.
func splitTax(gross int64, t *taxInfo) int64 {
	if t == nil || t.Kind != "INPUT" || t.Rate <= 0 {
		return 0
	}
	return int64(math.Round(float64(gross) * t.Rate / (100 + t.Rate)))
}

// taxCache: Steuerkennzeichen beim Buchen (einmal je Code).
type taxCache struct {
	m     *Module
	codes map[string]*taxInfo
}

func (c *taxCache) get(ctx context.Context, code string) (*taxInfo, error) {
	if code == "" {
		return nil, nil
	}
	if t, ok := c.codes[code]; ok {
		return t, nil
	}
	t, err := c.m.taxCodeOf(ctx, code)
	if err != nil {
		return nil, err
	}
	if t.Kind == "INPUT" && t.Rate > 0 && t.Account == "" {
		return nil, fmt.Errorf("Steuerkennzeichen %s ohne Vorsteuerkonto", code)
	}
	c.codes[code] = t
	return t, nil
}
