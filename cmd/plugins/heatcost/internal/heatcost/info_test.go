package heatcost

import (
	"strings"
	"testing"
)

// TestMonthlyInfo: Verbrauchsinformation Oktober 2025 für Gretchens Wohnung (§ 6a HeizkostenV).
func TestMonthlyInfo(t *testing.T) {
	e := setup(t)
	unit, run := e.liegenschaft()
	e.must("HeatCostRun", "fetch", map[string]any{"id": "1000|" + run})
	e.must("HeatCostRun", "compute", map[string]any{"id": "1000|" + run})
	e.h.objects["BusinessPartner|P-GRETCHEN"] = map[string]any{"type": "PERSON", "name1": "Margarete", "street": "Auerbachs Hof 1"}
	e.h.lists["partners:WE16"] = []map[string]any{{"role_code": "TENANT", "partner_id": "P-GRETCHEN", "contract_id": "MV-1", "valid_from": "2020-01-01", "valid_to": "9999-12-31"}}
	e.must("HeatCostUnit", "update", map[string]any{"id": "1000|" + unit, "data": map[string]any{"tenant_roles": "TENANT", "doc_type": "STATEMENT"}})
	month := func(hkv, ww float64, days int) []any {
		return []any{map[string]any{"medium": "HKV", "consumption": hkv, "covered_days": days, "period_days": 31},
			map[string]any{"medium": "WW", "consumption": ww, "covered_days": days, "period_days": 31}}
	}
	e.h.usage["WE16|2025-10-01"] = month(300, 3, 31)
	e.h.usage["WE16|2025-09-01"] = []any{map[string]any{"medium": "HKV", "consumption": 100, "covered_days": 30, "period_days": 30},
		map[string]any{"medium": "WW", "consumption": 2.5, "covered_days": 30, "period_days": 30}}
	e.h.usage["WE16|2024-10-01"] = month(250, 3.5, 20) // Vorjahr unvollständig
	if _, err := e.call("HeatCostUnit", "monthInfo", map[string]any{"id": "1000|" + unit, "data": map[string]any{"info_month": "13.2025"}}); err == nil {
		t.Fatal("Monat 13")
	}
	r := e.must("HeatCostUnit", "monthInfo", map[string]any{"id": "1000|" + unit, "data": map[string]any{"info_month": "10.2025"}})
	if r["info_id"] != "VI-2025-0001" || !strings.Contains(str(r["message"]), "bereitzustellen bis 30.11.2025") {
		t.Fatalf("Verbrauchsinformation: %v", r)
	}
	info := e.must("HeatCostInfo", "get", map[string]any{"id": "1000|VI-2025-0001"})
	heatF := (377702.0 - 112192.0) / 438341.534
	if !near(toFloat(info["heat_factor"]), round3(heatF)) || info["base_run"] != run || !strings.Contains(str(info["factor_note"]), "Abrechnung "+run) {
		t.Fatalf("Umrechnung: %v / %v", info["heat_factor"], info["factor_note"])
	}
	lines := items(e.must("HeatCostInfoLine", "list", map[string]any{"query": map[string]any{"info_id": "VI-2025-0001"}}))
	byKey := map[string]map[string]any{}
	for _, l := range lines {
		byKey[str(l["key_code"])] = l
	}
	ve := byKey["VE"]
	if ve == nil || toFloat(ve["value"]) != 300 || !near(toFloat(ve["kwh"]), round3(300*heatF)) || toFloat(ve["prev_month"]) != 100 || ve["prev_year"] != nil ||
		!near(toFloat(ve["average"]), 300) {
		t.Fatalf("Heizung: %v", ve)
	}
	if byKey["WW"] == nil || byKey["WW"]["kwh"] == nil {
		t.Fatalf("Warmwasser: %v", byKey["WW"])
	}
	// Kürzungsrisiko: 3 % der Heiz- und Warmwasserkosten des Nutzers aus der Abrechnung
	users := items(e.must("HeatCostInfoUser", "list", map[string]any{"query": map[string]any{"info_id": "VI-2025-0001"}}))
	if len(users) != 1 || !near(toFloat(users[0]["risk"]), round2(toFloat(users[0]["annual_cost"])*0.03)) || toFloat(users[0]["annual_cost"]) <= 0 {
		t.Fatalf("Nutzer: %v", users)
	}
	if !near(toFloat(info["risk"]), toFloat(users[0]["risk"])) {
		t.Fatalf("Risiko der Einheit: %v", info["risk"])
	}
	// Drucken und ablegen: Bezüge, Partner, Daten
	e.h.lists["TemplateDefinition"] = nil
	out := e.must("HeatCostInfo", "print", map[string]any{"id": "1000|VI-2025-0001", "data": map[string]any{"vi_store": true}})
	if len(e.h.stored) != 1 || !strings.Contains(str(out["message"]), "1 Verbrauchsinformationen im DMS") {
		t.Fatalf("Ablage: %v", out["message"])
	}
	st := e.h.stored[0]
	if st["partner_id"] != "P-GRETCHEN" || !strings.Contains(str(st["title"]), "Verbrauchsinformation Oktober 2025 Nutzer 16") || len(st["refs"].([]any)) != 3 {
		t.Fatalf("Dokument: %v", st)
	}
	data := e.h.renders[0]["data"].(map[string]any)
	l := data["letters"].([]map[string]any)[0]
	if data["format"] != infoFormat || l["subject"] != "Verbrauchsinformation Oktober 2025 – DG rechts" || l["rows"].([]map[string]any)[0]["diff_month"] != 200.0 {
		t.Fatalf("Daten: %v / %v", l["subject"], l["rows"])
	}
	// überfällig, dann bereitgestellt (Nachweis); danach bleiben die Werte
	if od := items(e.must("HeatCostInfo", "list", map[string]any{"query": map[string]any{"overdue": true}})); len(od) != 1 {
		t.Fatalf("überfällig: %v", od)
	}
	if _, err := e.call("HeatCostInfo", "provided", map[string]any{"id": "1000|VI-2025-0001", "data": map[string]any{"vi_provided_on": "2025-11-20"}}); err == nil {
		t.Fatal("Bereitstellung ohne Weg")
	}
	e.must("HeatCostInfo", "provided", map[string]any{"id": "1000|VI-2025-0001", "data": map[string]any{"vi_provided_on": "2025-11-20", "vi_provided_by": "E-Mail an die Mieter"}})
	if od := items(e.must("HeatCostInfo", "list", map[string]any{"query": map[string]any{"overdue": true}})); len(od) != 0 {
		t.Fatalf("noch überfällig: %v", od)
	}
	if _, err := e.call("HeatCostInfo", "recompute", map[string]any{"id": "1000|VI-2025-0001"}); err == nil {
		t.Fatal("Neu rechnen nach der Bereitstellung")
	}
	if _, err := e.call("HeatCostUnit", "monthInfo", map[string]any{"id": "1000|" + unit, "data": map[string]any{"info_month": "10.2025"}}); err == nil {
		t.Fatal("zweite Information für denselben Monat")
	}
}
