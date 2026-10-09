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
