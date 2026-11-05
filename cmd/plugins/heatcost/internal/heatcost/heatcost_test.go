package heatcost

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// liegenschaft: Liegenschaft Auerbachs Hof (vier Doppelhäuser, eine Fernwärmestation)
// mit Gretchens Wohnung (Nutzer 16) und Marthes Wohnung (Nutzer 20, fremd, ohne
// Mietobjekt). Zahlen wie in einer echten Messdienst-Abrechnung 2025.
func (e *env) liegenschaft() (unit, run string) {
	h := e.h
	h.objects["RentObject|1000|WE16"] = map[string]any{"object_id": "WE16", "designation": "Whg. Gretchen DG rechts"}
	h.lists["Measurement"] = []map[string]any{
		{"company_code": "1000", "object_id": "WE16", "measurement_type": "HZF", "value": 52.6, "valid_from": "2000-01-01", "valid_to": "9999-12-31"},
		{"company_code": "1000", "object_id": "WE16", "measurement_type": "WFL", "value": 53.0, "valid_from": "2000-01-01", "valid_to": "9999-12-31"},
	}
	h.usage["WE16"] = []any{
		map[string]any{"medium": "HKV", "consumption": 4009.276, "points": 5, "covered_days": 365, "period_days": 365},
		map[string]any{"medium": "WW", "consumption": 38.232, "points": 1, "covered_days": 365, "period_days": 365},
		map[string]any{"medium": "KW", "consumption": 65.099, "points": 2, "covered_days": 365, "period_days": 365},
	}
	u := e.create("HeatCostUnit", map[string]any{"company_code": "1000", "designation": "Liegenschaft Auerbachs Hof 1–8", "external_no": "558/018"})
	unit = str(u["unit_id"])
	for i, g := range [][2]string{{"H1_2", "Haus 1–2"}, {"H3_4", "Haus 3–4"}, {"H5_6", "Haus 5–6"}, {"H7_8", "Haus 7–8"},
		{"W1_2", "Haus 1 + 2"}, {"W3_6", "Haus 3–6"}, {"W7_8", "Haus 7 + 8"}} {
		e.create("HeatCostUserGroup", map[string]any{"company_code": "1000", "unit_id": unit, "code": g[0], "designation": g[1], "sort_order": i})
	}
	e.must("HeatCostUnit", "update", map[string]any{"id": "1000|" + unit, "data": map[string]any{"ww_groups": "H1_2,H3_4,H5_6,H7_8", "ww_group_key": "WW"}})
	e.create("HeatCostUser", map[string]any{"company_code": "1000", "unit_id": unit, "user_no": "16", "object_id": "WE16", "groups": "h1_2, w1_2", "location": "DG rechts"})
	e.create("HeatCostUser", map[string]any{"company_code": "1000", "unit_id": unit, "user_no": "20", "designation": "Marthe Schwerdtlein", "groups": "H7_8,W7_8"})
	r := e.create("HeatCostRun", map[string]any{"company_code": "1000", "unit_id": unit, "period_from": "2025-01-01", "period_to": "2025-12-31",
		"ww_energy": 112192, "total_energy": 377702})
	run = str(r["run_id"])
	if run != "HK-2025-0001" || str(r["ww_groups"]) != "H1_2,H3_4,H5_6,H7_8" || str(r["heat_area_key"]) != "HZF" || str(r["ww_usage_key"]) != "WW" {
		e.t.Fatalf("Abrechnung: %v", r)
	}
	cost := func(kind string, amount float64, extra map[string]any) {
		d := map[string]any{"company_code": "1000", "run_id": run, "cost_kind": kind, "amount": amount}
		for k, v := range extra {
			d[k] = v
		}
		e.create("HeatCostLine", d)
	}
	ww := map[string]any{"ww_percent": 29.7}
	cost("FERNWAERME", 74668.12, map[string]any{"invoice_ref": "Rechnung vom 31.12.2025, 377.702 kWh"})
	cost("STROM", 3733.41, ww)
	cost("MESSDIENST", 2790.71, ww)
	cost("FUNK", 202.30, ww)
	cost("MIETE_WMZ_WW", 1496.64, nil)
	cost("MIETE_WWZ", 954.86, nil)
	cost("MIETE_KWZ", 1909.71, nil)
	cost("MIETE_HKV", 2128.02, nil)
	cost("KW_ABR", 578.55, nil)
	for _, w := range []struct {
		scope                 string
		tw, gut, aw, twg, awg float64
	}{{"W1_2", 3879.82, -1581.25, 3152.54, 1037.21, 537.78}, {"W3_6", 3098.61, 0, 2517.78, 1677.67, 804.68}, {"W7_8", 2414.40, 0, 1961.83, 830.62, 442.25}} {
		s := map[string]any{"scope": w.scope}
		cost("TW_VERBRAUCH", w.tw, s)
		if w.gut != 0 {
			cost("WASSER_GUTSCHRIFT", w.gut, s)
		}
		cost("AW_VERBRAUCH", w.aw, s)
		cost("TW_GRUND", w.twg, s)
		cost("AW_GRUND", w.awg, s)
	}
	cost("DIREKT", 121.44, map[string]any{"user_no": "20", "text": "Nutzerdirektkosten (Zwischenablesung)"})
	basis := func(key, scope string, total float64) {
		e.create("HeatCostBasis", map[string]any{"company_code": "1000", "run_id": run, "key_code": key, "scope": scope, "total": total})
	}
	basis("HZF", "", 3326.761)
	basis("VE", "*", 438341.534)
	for g, v := range map[string][2]float64{"H1_2": {246.384, 872.728}, "H3_4": {250.928, 789.440}, "H5_6": {197.412, 782.190}, "H7_8": {188.844, 882.400}} {
		basis("WW", g, v[0])
		basis("HZF", g, v[1])
	}
	basis("STCK_KW", "", 136)
	basis("STCK_HKV", "", 311)
	for g, v := range map[string][2]float64{"W1_2": {497.815, 1163.490}, "W3_6": {910.311, 1571.630}, "W7_8": {446.158, 930.400}} {
		basis("KW", g, v[0])
		basis("HZF", g, v[1])
	}
	return unit, run
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

func (e *env) userAmounts(run, user string) (map[string]float64, float64) {
	out, sum := map[string]float64{}, 0.0
	for _, r := range items(e.must("HeatCostResult", "list", map[string]any{"query": map[string]any{"company_code": "1000", "run_id": run, "user_no": user}, "limit": 100})) {
		a := toFloat(r["amount"])
		out[str(r["text"])] += a
		sum += a
	}
	return out, round2(sum)
}

// TestComputeLikeBill: Werte aus Zählern und Flächen übernehmen, rechnen – das
// Ergebnis stimmt auf den Cent mit der Abrechnung des Messdienstes überein.
func TestComputeLikeBill(t *testing.T) {
	e := setup(t)
	_, run := e.liegenschaft()
	id := "1000|" + run
	f := e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	if f["written"] != 8 || len(e.h.usageCalls) != 1 || e.h.usageCalls[0]["from"] != "2025-01-01" || e.h.usageCalls[0]["to"] != "2025-12-31" {
		t.Fatalf("Werte übernehmen: %v %v", f, e.h.usageCalls)
	}
	vals := map[string]float64{}
	for _, v := range items(e.must("HeatCostValue", "list", map[string]any{"query": map[string]any{"company_code": "1000", "run_id": run}})) {
		vals[str(v["key_code"])] = toFloat(v["value"])
	}
	if !near(vals["HZF"], 52.6) || !near(vals["VE"], 4009.276) || vals["STCK_KW"] != 2 || vals["STCK_HKV"] != 5 || !near(vals["KW"], 65.099) || vals["WFL"] != 53 {
		t.Fatalf("Werte: %v", vals)
	}
	c := e.must("HeatCostRun", "compute", map[string]any{"id": id})
	if !near(toFloat(c["total_cost"]), 109357.70) || !near(toFloat(c["ww_cost"]), 26628.55) || !near(toFloat(c["heat_cost"]), 57217.49) {
		t.Fatalf("Rechnen: %v", c)
	}
	got, sum := e.userAmounts(run, "16")
	want := map[string]float64{
		"Heizung – 30,00 % Grundkosten": 271.40, "Heizung – 70,00 % Verbrauchskosten": 366.34,
		"Warmwasser Haus 1–2 – 30,00 % Grundkosten": 134.26, "Warmwasser Haus 1–2 – 70,00 % Verbrauchskosten": 806.55,
		"Miete Kaltwasserzähler": 28.08, "Miete Heizkostenverteiler": 34.21, "Kaltwasserabrechnung": 8.51,
		"Verbrauch Trinkwasser (Haus 1 + 2)": 507.36, "Erstattung / Gutschrift Wasser und Abwasser (Haus 1 + 2)": -206.78,
		"Verbrauch Abwasser (Haus 1 + 2)": 412.26, "Grundpreis Trinkwasser (Haus 1 + 2)": 46.89, "Grundpreis Abwasser (Haus 1 + 2)": 24.31,
	}
	for k, v := range want {
		if !near(got[k], v) {
			t.Errorf("%s: %.2f, erwartet %.2f", k, got[k], v)
		}
	}
	if len(got) != len(want) || sum != 2433.39 {
		t.Fatalf("Nutzer 16: %.2f %v", sum, got)
	}
	if _, s20 := e.userAmounts(run, "20"); s20 != 121.44 {
		t.Fatalf("Nutzer 20 (nur Direktkosten, keine Werte): %.2f", s20)
	}
	steps := map[string]float64{}
	for _, s := range items(e.must("HeatCostStep", "list", map[string]any{"query": map[string]any{"company_code": "1000", "run_id": run}, "limit": 200})) {
		steps[str(s["text"])] = toFloat(s["amount"])
	}
	if !near(steps["Warmwasser Haus 1–2"], 7425.40) || !near(steps["Warmwasser aus Brennstoffkosten"], 22179.30) ||
		!near(steps["Warmwasser aus Weitere Heizungsbetriebskosten"], 4449.25) || !near(steps["Warmwasser Haus 1–2 – 30,00 % Grundkosten"], 2227.62) {
		t.Fatalf("Rechenweg: %v", steps)
	}
	st := e.must("HeatCostRun", "statement", map[string]any{"id": id, "data": map[string]any{"st_user": "16"}})
	if !strings.Contains(str(st["message"]), "2.433,39 €") {
		t.Fatalf("Einzelabrechnung: %v", st["message"])
	}
	// Korrektur: neue Abrechnung, Gutschrift Leckwasser nach Kaltwasser in Haus 1 + 2
	k := e.must("HeatCostRun", "correct", map[string]any{"id": id, "data": map[string]any{}})
	kr := str(k["run_id"])
	if kr != "HK-2025-0002" {
		t.Fatalf("Korrektur: %v", k)
	}
	e.create("HeatCostLine", map[string]any{"company_code": "1000", "run_id": kr, "cost_kind": "WASSER_GUTSCHRIFT", "amount": -920, "scope": "W1_2",
		"text": "Gutschrift Leckwasser 160 m³ (nicht umlagefähig)"})
	e.must("HeatCostRun", "compute", map[string]any{"id": "1000|" + kr})
	if _, s := e.userAmounts(kr, "16"); s != 2313.08 {
		t.Fatalf("Korrektur Nutzer 16: %.2f", s)
	}
	if _, s := e.userAmounts(run, "16"); s != 2433.39 {
		t.Fatalf("Abrechnung geändert: %.2f", s)
	}
}

// TestRunLifecycle: Änderungen setzen auf Entwurf, festgeschrieben ist unveränderlich,
// von Hand erfasste Werte überstehen „Werte übernehmen“.
func TestRunLifecycle(t *testing.T) {
	e := setup(t)
	unit, run := e.liegenschaft()
	id := "1000|" + run
	if _, err := e.call("HeatCostRun", "finalize", map[string]any{"id": id}); err == nil {
		t.Fatal("Festschreiben ohne Rechnen")
	}
	e.create("HeatCostValue", map[string]any{"company_code": "1000", "run_id": run, "user_no": "16", "key_code": "HZF", "value": 50})
	f := e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	if f["kept"] != 1 {
		t.Fatalf("Eingabe beibehalten: %v", f)
	}
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	e.create("HeatCostLine", map[string]any{"company_code": "1000", "run_id": run, "cost_kind": "WARTUNG", "amount": 100})
	if r := e.must("HeatCostRun", "get", map[string]any{"id": id}); r["status"] != runDraft {
		t.Fatalf("nach Änderung: %v", r["status"])
	}
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	e.must("HeatCostRun", "finalize", map[string]any{"id": id})
	if _, err := e.call("HeatCostLine", "create", map[string]any{"data": map[string]any{"company_code": "1000", "run_id": run, "cost_kind": "WARTUNG", "amount": 1}}); err == nil {
		t.Fatal("Zeile in festgeschriebener Abrechnung")
	}
	if _, err := e.call("HeatCostRun", "delete", map[string]any{"id": id}); err == nil {
		t.Fatal("festgeschriebene Abrechnung gelöscht")
	}
	if _, err := e.call("HeatCostLine", "create", map[string]any{"data": map[string]any{"company_code": "1000", "run_id": "HK-2025-0009", "cost_kind": "WARTUNG", "amount": 1}}); err == nil {
		t.Fatal("Zeile ohne Abrechnung")
	}
	if _, err := e.call("HeatCostUser", "create", map[string]any{"data": map[string]any{"company_code": "1000", "unit_id": unit, "user_no": "30", "groups": "XX"}}); err == nil {
		t.Fatal("unbekannte Gruppe")
	}
	k := e.must("HeatCostRun", "correct", map[string]any{"id": id, "data": map[string]any{"co_title": "Korrektur Leckwasser"}})
	if _, err := e.call("HeatCostRun", "delete", map[string]any{"id": "1000|" + str(k["run_id"])}); err != nil {
		t.Fatalf("Korrektur löschen: %v", err)
	}
}
