package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// TestPlan: Wirtschaftsplan am Hausgeld-Vertrag – Positionen mit Kostenart,
// Schlüssel und Anteil, Summen nach Art, Warnung bei falschem Anteil, Vorjahr
// übernehmen, Plan und Ist an der Abrechnung.
func TestPlan(t *testing.T) {
	e := setup(t)
	e.basics()
	for _, a := range []string{"7000", "7300", "1550"} {
		e.h.accounts["1000|"+a] = "Konto " + a
	}
	e.partner("WEG", "WEG Brunnenstraße", "CREDITOR")
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "WH", "designation": "Hausgeld WG001",
		"partner_id": "WEG", "valid_from": "2025-01-01"})
	id := c["contract_id"].(string)
	plan := func(from, to string) map[string]any {
		return e.create(planObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": from, "period_to": to,
			"plan_date": "2024-11-20", "reference": "WP " + from[:4]})
	}
	plan("2025-01-01", "2025-12-31")
	expect(t, e.try(planObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2024-01-01", "period_to": "2023-12-31"}),
		sdk.ErrInvalidArgument, "bis vor von")

	item := func(from string, data map[string]any) map[string]any {
		data["company_code"], data["contract_id"], data["period_from"] = "1000", id, from
		return e.create(planItemObject, data)
	}
	w := item("2025-01-01", map[string]any{"description": "Wasser/Abwasser", "cost_category": "wasser", "total_cost": "12.000,00",
		"allocation_key": "mea", "key_total": "1000", "key_share": "85,32", "amount": "1.023,84"})
	if w["line_no"] != int64(1) || w["cost_type"] != "ALLOCABLE" || w["amount"] != "1023.84" || w["key_share"] != "85.32" || w[crud.WarningsField] != nil {
		t.Fatalf("Position Wasser: %v", w)
	}
	v := item("2025-01-01", map[string]any{"description": "Verwalter", "cost_category": "VERWALT", "total_cost": "3.000,00",
		"allocation_key": "EINH", "key_total": "10", "key_share": "1", "amount": "350,00"})
	if ws := fmt.Sprint(v[crud.WarningsField]); !strings.Contains(ws, "300.00") {
		t.Fatalf("Warnung Anteil erwartet: %v", v)
	}
	item("2025-01-01", map[string]any{"description": "Erhaltungsrücklage", "cost_category": "RUECKL", "amount": "600,00"})
	expect(t, e.try(planItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"cost_category": "GIBTSNICHT", "amount": "1"}), sdk.ErrInvalidArgument, "unbekannte Kostenart")
	expect(t, e.try(planItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2027-01-01",
		"cost_category": "WASSER", "amount": "1"}), sdk.ErrInvalidArgument, "Plan fehlt")

	p := e.must(planObject, "get", map[string]any{"id": "1000|" + id + "|2025-01-01"})
	if p["total_amount"] != "1973.84" || p["allocable_amount"] != "1023.84" || p["non_allocable_amount"] != "350.00" ||
		p["reserve_amount"] != "600.00" || p["monthly_amount"] != "164.49" {
		t.Fatalf("Summen: %v", p)
	}

	// Folgejahr: Positionen übernehmen, Beträge leer
	plan("2026-01-01", "2026-12-31")
	r := e.must(planObject, "copyPrevious", map[string]any{"id": "1000|" + id + "|2026-01-01"})
	if toInt(r["copied"]) != 3 || !strings.Contains(r["message"].(string), "01.01.2025") {
		t.Fatalf("Vorjahr übernehmen: %v", r)
	}
	its := items(e.must(planItemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2026-01-01"}}))
	if len(its) != 3 || its[0]["amount"] != "0.00" || its[0]["description"] != "Wasser/Abwasser" || its[1]["allocation_key"] != "EINH" {
		t.Fatalf("übernommen: %v", its)
	}
	expect(t, e.call2(planObject, "copyPrevious", "1000|"+id+"|2025-01-01"), sdk.ErrInvalidArgument, "kein früherer Plan")

	// Abrechnung 2025: Plan und Ist
	e.create(settlementObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01", "period_to": "2025-12-31",
		"settlement_date": "2026-05-10"})
	si := e.create(settlementItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"cost_category": "WASSER", "amount": "1.057,97", "object_type": "RentObject", "object_id": "X"})
	if si["plan_amount"] != "1023.84" || si["plan_deviation"] != "34.13" {
		t.Fatalf("Plan an der Position: %v", si)
	}
	s := e.must(settlementObject, "get", map[string]any{"id": "1000|" + id + "|2025-01-01"})
	if s["plan_total"] != "1973.84" || s["actual_total"] != "1057.97" || s["plan_deviation"] != "-915.87" {
		t.Fatalf("Plan und Ist am Kopf: %v", s)
	}
}

func (e *env) call2(object, action, id string) error {
	_, err := e.call(object, action, map[string]any{"id": id})
	return err
}

// TestPlanExportImport: Export als YAML, erneuter Import lässt Vorhandenes
// unverändert, neuer Plan über das Hauptobjekt, Prüflauf, falsches Format.
func TestPlanExportImport(t *testing.T) {
	e := setup(t)
	e.basics()
	e.partner("WEG", "WEG Brunnenstraße", "CREDITOR")
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "WH", "designation": "Hausgeld WG001",
		"partner_id": "WEG", "valid_from": "2025-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2025-01-01"})
	e.create(planObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01", "period_to": "2025-12-31",
		"reference": "WP 2025"})
	e.create(planItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01", "description": "Wasser",
		"cost_category": "WASSER", "total_cost": "12.000,00", "allocation_key": "MEA", "key_total": "1000", "key_share": "85,32", "amount": "1.023,84"})

	out := e.must(setupObject, "exportPlans", map[string]any{"company": "1000"})
	y, _ := out["file_content"].(string)
	for _, want := range []string{"format: coremesh.contract-plans", "object_id: LpzBrn1WG001", "cost_category: WASSER", `amount: "1023.84"`, `key_share: "85.32"`} {
		if !strings.Contains(y, want) {
			t.Fatalf("Export ohne %q:\n%s", want, y)
		}
	}
	r := e.must(setupObject, "importPlans", map[string]any{"company": "1000", "file": y})
	if !strings.Contains(r["message"].(string), "1 vorhanden") {
		t.Fatalf("erneuter Import: %v", r)
	}
	next := strings.NewReplacer("contract_id: "+id, "contract_id: GIBTSNICHT", "2025-", "2026-", "1023.84", "1100.00").Replace(y)
	r = e.must(setupObject, "importPlans", map[string]any{"company": "1000", "file": next, "dry_run": true})
	if !strings.Contains(r["message"].(string), "Prüfung") || !strings.Contains(r["message"].(string), "1 Pläne neu") {
		t.Fatalf("Prüflauf: %v", r)
	}
	r = e.must(setupObject, "importPlans", map[string]any{"company": "1000", "file": next})
	if !strings.Contains(r["message"].(string), "1 Pläne neu") || !strings.Contains(r["message"].(string), "1 Warnungen") {
		t.Fatalf("Import über das Hauptobjekt: %v", r)
	}
	p := e.must(planObject, "get", map[string]any{"id": "1000|" + id + "|2026-01-01"})
	if p["total_amount"] != "1100.00" {
		t.Fatalf("importierter Plan: %v", p)
	}
	_, err := e.call(setupObject, "importPlans", map[string]any{"company": "1000", "file": "format: anders\nversion: 1\n"})
	expect(t, err, sdk.ErrInvalidArgument, "falsches Format")
}

// TestNothingDueReason: „Buchen …“ ohne Fälligkeit nennt den Grund.
func TestNothingDueReason(t *testing.T) {
	e := setup(t)
	e.basics()
	e.partner("SW", "Stadtwerke", "CREDITOR")
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "VE", "designation": "Fernwärme",
		"partner_id": "SW", "valid_from": "2025-01-01", "billing_start": "2026-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "VZ", "amount": "766,00", "calc_method": "FIXED", "payment_mode": "IN_ADVANCE",
		"frequency": "MONTHLY", "due_day": 1, "valid_from": "2026-10-09"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	r := e.must("Contract", "post", map[string]any{"id": "1000|" + id, "data": map[string]any{"post_until": "2026-01-31"}})
	if msg := fmt.Sprint(r["message"]); !strings.Contains(msg, "VZ ab 09.10.2026") || !strings.Contains(msg, "Gültig ab") {
		t.Fatalf("Grund erwartet: %v", r)
	}
}

// TestPlanConditions: Zahlungen laut Plan → Konditionen ab „Zahlung ab“; ohne
// Zahlungen aus den Positionen gerechnet; Erinnerung „Betrag ändert sich“ nur
// beim Zahlweg der Regel.
func TestPlanConditions(t *testing.T) {
	e := setup(t)
	e.basics()
	e.partner("WEG", "WEG Brunnenstraße", "CREDITOR")
	id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "WH", "designation": "Hausgeld WG001",
		"partner_id": "WEG", "valid_from": "2026-01-01", "payment_method": "STANDING_ORDER"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	for typ, amt := range map[string]string{"HV": "224,00", "RZ": "51,00"} {
		e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": typ,
			"calc_method": "FIXED", "unit_price": amt, "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	}
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	e.create(reminderRuleObject, map[string]any{"company_code": "1000", "contract_type": "WH", "condition_kind": "VO", "occasion": "AMOUNT_CHANGE",
		"description": "Dauerauftrag anpassen", "lead_days": 30, "payment_methods": "STANDING_ORDER", "active_from": "2026-06-01"})

	e.create(planObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2027-01-01", "period_to": "2027-12-31",
		"reference": "WP 2027"})
	planID := "1000|" + id + "|2027-01-01"
	_, err := e.call(planObject, "applyConditions", map[string]any{"id": planID})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Zahlungen und ohne Zuordnung")

	// aus den Positionen: Gegenstände mit „Monatsbetrag aus“
	e.must("ConditionType", "update", map[string]any{"id": "1000|HV", "data": map[string]any{"plan_cost_types": "ALLOCABLE,NON_ALLOCABLE"}})
	e.must("ConditionType", "update", map[string]any{"id": "1000|RZ", "data": map[string]any{"plan_cost_types": "RESERVE"}})
	for _, it := range []map[string]any{{"cost_category": "WASSER", "amount": "1.800,00"}, {"cost_category": "VERWALT", "amount": "1.130,47"},
		{"cost_category": "RUECKL", "amount": "612,27"}} {
		it["company_code"], it["contract_id"], it["period_from"] = "1000", id, "2027-01-01"
		e.create(planItemObject, it)
	}
	r := e.must(planObject, "applyConditions", map[string]any{"id": planID})
	if msg := fmt.Sprint(r["message"]); !strings.Contains(msg, "HV 244,21 statt 224,00") || !strings.Contains(msg, "RZ 51,02 statt 51,00") {
		t.Fatalf("aus Positionen: %v", r)
	}
	// Plan nennt gerundete Zahlungen: korrigieren und erneut übernehmen (Zeitscheibe ab 2027 bekommt den Betrag)
	e.must(planPaymentObject, "update", map[string]any{"id": planID + "|HV", "data": map[string]any{"amount": "244,00"}})
	e.must(planPaymentObject, "update", map[string]any{"id": planID + "|RZ", "data": map[string]any{"amount": "51,00"}})
	r = e.must(planObject, "applyConditions", map[string]any{"id": planID})
	if msg := fmt.Sprint(r["message"]); !strings.Contains(msg, "HV 244,00 statt 244,21") || !strings.Contains(msg, "RZ 51,00 statt 51,02") ||
		!strings.Contains(msg, "Zahlung gesamt 295,00 statt 295,23 EUR") {
		t.Fatalf("laut Plan: %v", r)
	}
	res, _ := e.m.db.Query(e.ctx, `SELECT condition_type, valid_from, valid_to, amount FROM contract__condition WHERE contract_id = ? ORDER BY 1, 2`, id)
	for _, r := range res.Rows {
		r[1], r[2] = dateOf(r[1]), dateOf(r[2])
	}
	if got := fmt.Sprint(res.Rows); got != "[[HV 2026-01-01 2026-12-31 22400] [HV 2027-01-01 9999-12-31 24400] [RZ 2026-01-01 2026-12-31 5100] [RZ 2027-01-01 9999-12-31 5100]]" {
		t.Fatalf("Konditionen: %s", got)
	}

	// Erinnerung 30 Tage vorher, nur HV (RZ unverändert)
	if err := e.m.refreshRemindersTx(e.ctx, "2026-12-15"); err != nil {
		t.Fatal(err)
	}
	res, _ = e.m.db.Query(e.ctx, `SELECT condition_type, remind_date, status, detail FROM contract__reminder WHERE contract_id = ?`, id)
	for _, r := range res.Rows {
		r[1] = dateOf(r[1])
	}
	if got := fmt.Sprint(res.Rows); got != "[[HV 2026-12-02 OPEN ab 01.01.2027 244,00 statt 224,00 EUR; Zahlung gesamt 295,00 statt 275,00 EUR]]" {
		t.Fatalf("Erinnerung: %s", got)
	}
	// Lastschrift: keine Erinnerung
	e.must("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"payment_method": "DIRECT_DEBIT"}})
	if err := e.m.refreshRemindersTx(e.ctx, "2026-12-15"); err != nil {
		t.Fatal(err)
	}
	res, _ = e.m.db.Query(e.ctx, `SELECT status FROM contract__reminder WHERE contract_id = ?`, id)
	if got := fmt.Sprint(res.Rows); got != "[[OBSOLETE]]" {
		t.Fatalf("Lastschrift: %s", got)
	}
}
