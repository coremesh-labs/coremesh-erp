package contract

import (
	"context"
	"fmt"
	"math/big"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Konditionen aus dem Wirtschaftsplan (seit 0.45.0):
//
//   - Zahlungen laut Plan (ContractPlanPayment): Monatsbetrag je Konditionsgegenstand,
//     wie der Plan ihn nennt (Verwaltungen runden oft, z. B. Hausgeld 244,00).
//   - Ohne Zahlungen rechnet „Konditionen übernehmen …“ sie aus den Positionen: je
//     Gegenstand mit „Wirtschaftsplan: Monatsbetrag aus“ (Arten der Kostenarten) die
//     Summe dieser Positionen / Monate des Wirtschaftsjahres – und legt sie als
//     Zahlungen ab (prüfen und korrigieren, dann erneut übernehmen).
//   - Übernehmen: die zum Beginn („Zahlung ab“, leer = Wirtschaftsjahr von) gültige
//     Kondition des Gegenstands endet am Vortag, ab dem Beginn gilt der Betrag laut
//     Plan; beginnt sie genau dann, bekommt sie den neuen Betrag. Gleicher Betrag:
//     nichts zu tun. Eine Erinnerungsregel „Betrag ändert sich“ meldet dann z. B.,
//     den Dauerauftrag anzupassen.

const planPaymentObject = "ContractPlanPayment"

func (m *Module) planPayment() *crud.Entity {
	return &crud.Entity{
		Object: planPaymentObject, Title: "Zahlungen laut Wirtschaftsplan", Icon: "icon-coins", Table: "contract__plan_payment", Section: "Abrechnung",
		Keys: []string{"company_code", "contract_id", "period_from", "condition_type"}, Order: "company_code, contract_id, period_from, condition_type",
		Filters: []string{"company_code", "contract_id", "period_from"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "period_from", Label: "Wirtschaftsplan (Wirtschaftsjahr von)", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "condition_type", Label: "Konditionsgegenstand (z. B. Hausgeld, Rücklage)", Short: "Gegenstand", Type: tText, Required: true, Listable: true,
				Immutable: true, Lookup: lookupConditionTypeOfContract},
			{Key: "amount", Label: "Monatlich laut Plan", Short: "monatlich", Type: tText, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		},
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkPlanPayment,
		Decorate: func(ctx context.Context, rec crud.Record) error { return m.decorateAmounts(ctx, rec, "amount") },
	}
}

func (m *Module) checkPlanPayment(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return err
	}
	rec["period_from"] = from
	if m.text(ctx, `SELECT 1 FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, id, from) == "" {
		return crud.Invalid("Wirtschaftsplan %s ab %s gibt es nicht", id, from)
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	if old == nil {
		rec["condition_type"] = trimUpper(rec["condition_type"])
		if _, err := m.conditionTypeOf(ctx, cc, crud.Str(rec["condition_type"])); err != nil {
			return err
		}
	}
	rec["note"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["note"])))
	return minorField(rec, "amount", m.currencyDecimals(ctx, c.Currency), true)
}

// planPayments: Zahlungen laut Plan (Gegenstand → Monatsbetrag, kleinste Einheit).
func (m *Module) planPayments(ctx context.Context, cc, id, from string) (map[string]int64, error) {
	res, err := m.db.Query(ctx, `SELECT condition_type, amount FROM contract__plan_payment WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
		cc, id, from)
	if err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, r := range res.Rows {
		out[crud.Str(r[0])] = toInt(r[1])
	}
	return out, nil
}

// proposePlanPayments: Monatsbeträge aus den Positionen für die Gegenstände des
// Vertrags mit „Wirtschaftsplan: Monatsbetrag aus“.
func (m *Module) proposePlanPayments(ctx context.Context, c *contractRow, from, to string) (map[string]int64, error) {
	n := months(from, to)
	if n <= 0 {
		return nil, crud.Invalid("Wirtschaftsjahr %s bis %s ohne Monate", from, to)
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT t.code, t.plan_cost_types FROM contract__condition k JOIN contract__condition_type t
		ON t.company_code = k.company_code AND t.code = k.condition_type
		WHERE k.company_code = ? AND k.contract_id = ? AND COALESCE(t.plan_cost_types, '') <> ''`, c.CompanyCode, c.ID)
	if err != nil {
		return nil, err
	}
	sums, err := m.db.Query(ctx, `SELECT cost_type, SUM(amount) FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ?
		AND is_active = ? GROUP BY cost_type`, c.CompanyCode, c.ID, from, true)
	if err != nil {
		return nil, err
	}
	byType := map[string]int64{}
	for _, r := range sums.Rows {
		byType[crud.Str(r[0])] = toInt(r[1])
	}
	out := map[string]int64{}
	for _, r := range res.Rows {
		var sum int64
		for _, ct := range strings.Split(crud.Str(r[1]), ",") {
			sum += byType[strings.TrimSpace(ct)]
		}
		out[crud.Str(r[0])] = scale(sum, big.NewRat(1, int64(n)))
	}
	return out, nil
}

func (m *Module) applyPlanAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(planObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	from, err := crud.ParseDate(key["period_from"])
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT period_to, payment_from, COALESCE(reference, '') FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
		cc, id, from)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Wirtschaftsplan %s ab %s gibt es nicht", id, from)
	}
	to, start, ref := dateOf(res.Rows[0][0]), dateOf(res.Rows[0][1]), crud.Str(res.Rows[0][2])
	if start == "" {
		start = from
	}
	dec := m.currencyDecimals(ctx, c.Currency)
	pays, err := m.planPayments(ctx, cc, id, from)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(pays) == 0 {
		if pays, err = m.proposePlanPayments(ctx, c, from, to); err != nil {
			return sdk.Response{}, err
		}
		if len(pays) == 0 {
			return sdk.Response{}, crud.Invalid("Keine Zahlungen laut Plan und kein Konditionsgegenstand des Vertrags mit „Wirtschaftsplan: Monatsbetrag aus“ – Zahlungen am Plan eintragen")
		}
		pe := m.set.Entity(planPaymentObject)
		err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
			for typ, amt := range pays {
				if _, err := pe.Create(ctx, map[string]any{"data": map[string]any{"company_code": cc, "contract_id": id, "period_from": from,
					"condition_type": typ, "amount": amt, "note": "aus den Positionen gerechnet"}}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return sdk.Response{}, err
		}
	}
	note := "laut Wirtschaftsplan " + firstNonEmpty(ref, from)
	cond := m.set.Entity("ContractCondition")
	types := make([]string, 0, len(pays))
	for t := range pays {
		types = append(types, t)
	}
	slices.Sort(types)
	var done []string
	var sumNew, sumOld int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, typ := range types {
			msg, old, err := m.applyPlanPayment(ctx, cond, c, typ, start, pays[typ], dec, note)
			if err != nil {
				return fmt.Errorf("%s: %w", typ, err)
			}
			done = append(done, msg)
			sumNew, sumOld = sumNew+pays[typ], sumOld+old
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	if len(types) > 1 {
		done = append(done, fmt.Sprintf("Zahlung gesamt %s statt %s %s", germanAmount(formatAmount(sumNew, dec)), germanAmount(formatAmount(sumOld, dec)), c.Currency))
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Konditionen ab %s: %s", germanDate(start), strings.Join(done, "; "))}}, nil
}

// applyPlanPayment: Betrag laut Plan für den Gegenstand ab start.
func (m *Module) applyPlanPayment(ctx context.Context, cond *crud.Entity, c *contractRow, typ, start string, amount int64, dec int, note string) (string, int64, error) {
	res, err := m.db.Query(ctx, `SELECT object_id, position, valid_from, amount, calc_method, `+strings.Join(copyFields, ", ")+`
		FROM contract__condition WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND valid_to >= ? ORDER BY valid_from`,
		c.CompanyCode, c.ID, typ, start)
	if err != nil {
		return "", 0, err
	}
	if len(res.Rows) == 0 {
		return "", 0, crud.Invalid("Vertrag %s hat ab %s keine Kondition %s – zuerst anlegen", c.ID, start, typ)
	}
	r := res.Rows[0]
	for _, o := range res.Rows[1:] {
		if crud.Str(o[0]) != crud.Str(r[0]) || toInt(o[1]) != toInt(r[1]) {
			return "", 0, crud.Invalid("mehrere Konditionen %s (Objekt/Position) – Beträge einzeln anpassen", typ)
		}
		return "", 0, crud.Invalid("Kondition %s hat schon eine Zeitscheibe ab %s", typ, dateOf(o[2]))
	}
	if crud.Str(r[4]) != calcFixed {
		return "", 0, crud.Invalid("Kondition %s rechnet nicht mit festem Betrag", typ)
	}
	obj, pos, vf, old := crud.Str(r[0]), toInt(r[1]), dateOf(r[2]), toInt(r[3])
	if vf > start {
		return "", 0, crud.Invalid("Kondition %s beginnt erst %s", typ, vf)
	}
	text := formatAmount(amount, dec)
	if old == amount {
		return typ + " " + germanAmount(text) + " (unverändert)", old, nil
	}
	id := cond.RecordID(crud.Record{"company_code": c.CompanyCode, "contract_id": c.ID, "condition_type": typ, "object_id": obj, "position": pos, "valid_from": vf})
	price := map[string]any{"amount": text, "unit_price": text, "quantity": "1", "note": note}
	if vf == start {
		if _, err := cond.Update(ctx, map[string]any{"id": id, "data": price}); err != nil {
			return "", 0, err
		}
	} else {
		if _, err := cond.Expire(ctx, map[string]any{"id": id, "valid_to": dayBefore(start)}); err != nil {
			return "", 0, err
		}
		data := map[string]any{"company_code": c.CompanyCode, "contract_id": c.ID, "condition_type": typ, "position": pos, "valid_from": start}
		if obj != "" {
			data["object_id"] = obj
		}
		for i, k := range copyFields {
			if v := r[5+i]; v != nil && crud.Str(v) != "" {
				data[k] = v
			}
		}
		for k, v := range price {
			data[k] = v
		}
		if _, err := cond.Create(ctx, map[string]any{"data": data}); err != nil {
			return "", 0, err
		}
	}
	return fmt.Sprintf("%s %s statt %s", typ, germanAmount(text), germanAmount(formatAmount(old, dec))), old, nil
}
