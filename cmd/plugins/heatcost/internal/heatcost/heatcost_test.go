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

// Vorgaben für Abrechnungseinheiten: leer = erste Vorgabe, gewählte Vorgabe füllt nur leere Felder.
func TestProfiles(t *testing.T) {
	e := setup(t)
	u := e.create("HeatCostUnit", map[string]any{"company_code": "1000", "designation": "Hexenküche"})
	if u["profile"] != "STANDARD" || toFloat(u["heat_base_percent"]) != 30 || u["ww_usage_key"] != "WW" || u["ww_method"] != wwMeter {
		t.Fatalf("Standardvorgabe: %v", u)
	}
	u = e.create("HeatCostUnit", map[string]any{"company_code": "1000", "designation": "Walpurgisnacht", "profile": "HALB", "ww_base_percent": 40})
	if toFloat(u["heat_base_percent"]) != 50 || toFloat(u["ww_base_percent"]) != 40 {
		t.Fatalf("Vorgabe HALB: %v", u)
	}
	if _, err := e.call("HeatCostUnit", "create", map[string]any{"data": map[string]any{"company_code": "1000", "designation": "Brocken", "profile": "GIBTSNICHT"}}); err == nil {
		t.Fatal("unbekannte Vorgabe")
	}
	if _, err := e.call("HeatCostProfile", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "HALBGAR", "name": "unvollständig", "heat_base_percent": 40}}); err == nil {
		t.Fatal("unvollständige Vorgabe")
	}
	for _, c := range []string{"STANDARD", "HALB"} {
		e.must("HeatCostProfile", "deactivate", map[string]any{"id": "1000|" + c})
	}
	if _, err := e.call("HeatCostUnit", "create", map[string]any{"data": map[string]any{"company_code": "1000", "designation": "Gretchens Stube"}}); err == nil {
		t.Fatal("ohne Vorgabe und ohne Einstellungen")
	}
}

// TestCO2: Vermieteranteil der CO2-Kosten wie in der Abrechnung (Stufe 5, § 9 halbiert = 20 %),
// erst nur ausgewiesen (WEG), dann abgezogen; Formel § 9 Abs. 2.
func TestCO2(t *testing.T) {
	e := setup(t)
	_, run := e.liegenschaft()
	id := "1000|" + run
	e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"co2_cost": 7868.30, "co2_kg": 102735, "co2_scale": "DENKMAL",
		"co2_mode": co2Show, "co2_ww_decimals": 2}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	r := e.must("HeatCostRun", "get", map[string]any{"id": id})
	if !near(toFloat(r["co2_landlord"]), 1573.66) || toFloat(r["co2_percent"]) != 20 || math.Abs(toFloat(r["co2_kg_m2"])-30.9) > 0.001 {
		t.Fatalf("CO2 Liegenschaft: %v %v %v %v", r["co2_kg_m2"], r["co2_stage"], r["co2_percent"], r["co2_landlord"])
	}
	c := e.must("HeatCostCo2Result", "get", map[string]any{"id": id + "|16"})
	if !near(toFloat(c["landlord"]), 28.84) || !near(toFloat(c["tenant"]), 115.37) || math.Abs(toFloat(c["emissions"])-1883.02) > 0.05 || c["deducted"] == true {
		t.Fatalf("CO2 Whg 16: %v", c)
	}
	if _, sum := e.userAmounts(run, "16"); !near(sum, 2433.39) {
		t.Fatalf("nur ausgewiesen: %v", sum)
	}
	st := e.must("HeatCostRun", "statement", map[string]any{"id": id, "data": map[string]any{"st_user": "16"}})
	if !strings.Contains(fmt.Sprint(st["table"]), "nicht abgezogen") {
		t.Fatalf("Einzelabrechnung ohne CO2-Hinweis: %v", st["table"])
	}
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"co2_mode": co2Deduct}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	if _, sum := e.userAmounts(run, "16"); !near(sum, 2404.55) {
		t.Fatalf("abgezogen: %v", sum)
	}
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"ww_method": wwFormula, "ww_volume": 1000}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	if r := e.must("HeatCostRun", "get", map[string]any{"id": id}); math.Abs(toFloat(r["ww_share"])-2.5*1000*50/377702*100) > 1e-9 {
		t.Fatalf("Formel § 9 Abs. 2: %v", r["ww_share"])
	}
}

// TestPrintAndTransfer: Korrektur mit bisher/neu, ein Druck für alle Nutzer und je
// Nutzer ins DMS; Übergabe an die Nebenkosten ersetzt vorhandene Eingaben.
func TestPrintAndTransfer(t *testing.T) {
	e := setup(t)
	unit, run := e.liegenschaft()
	id := "1000|" + run
	e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"co2_cost": 7868.30, "co2_kg": 102735, "co2_scale": "DENKMAL",
		"co2_mode": co2Show, "co2_ww_decimals": 2}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	k := e.must("HeatCostRun", "correct", map[string]any{"id": id, "data": map[string]any{"co_title": "Korrektur Wasserverlust"}})
	kid := "1000|" + str(k["run_id"])
	e.create("HeatCostLine", map[string]any{"company_code": "1000", "run_id": str(k["run_id"]), "cost_kind": "TW_VERBRAUCH", "amount": -507.57, "scope": "W1_2",
		"text": "Wasserverlust 160 m³ – Trinkwasser", "note": "Rest 160 m³"})
	if _, err := e.call("HeatCostRun", "print", map[string]any{"id": kid}); err == nil {
		t.Fatal("Druck ohne Rechnen")
	}
	e.must("HeatCostRun", "compute", map[string]any{"id": kid})
	if _, err := e.call("HeatCostRun", "print", map[string]any{"id": kid, "data": map[string]any{"pr_store": true}}); err == nil {
		t.Fatal("Ablage ohne Dokumentart")
	}
	e.must("HeatCostUnit", "update", map[string]any{"id": "1000|" + unit, "data": map[string]any{"doc_type": "statement", "nk_definition": "nk_hof",
		"nk_heat_account": "heiz", "nk_co2_account": "co2"}})
	out := e.must("HeatCostRun", "print", map[string]any{"id": kid, "data": map[string]any{"pr_steps": true, "pr_store": true}})
	if len(e.h.renders) != 3 || len(e.h.stored) != 2 || !strings.Contains(str(out["message"]), "2 Einzelabrechnungen im DMS") {
		t.Fatalf("Druck: %d Aufträge, %d abgelegt, %v", len(e.h.renders), len(e.h.stored), out["message"])
	}
	data := e.h.renders[0]["data"].(map[string]any)
	sts := data["statements"].([]map[string]any)
	s16 := sts[0]
	corr := s16["correction"].(map[string]any)
	if len(sts) != 2 || !near(toFloat(corr["difference"]), -66.37) || !near(toFloat(corr["previous_total"]), 2433.39) || s16["co2"] == nil ||
		len(data["changes"].([]map[string]any)) != 1 || len(data["steps"].([]map[string]any)) == 0 {
		t.Fatalf("Daten: %v", s16)
	}
	refs := fmt.Sprint(e.h.stored[0]["refs"])
	if e.h.stored[0]["doc_type"] != "STATEMENT" || !strings.Contains(refs, "RentObject") {
		t.Fatalf("DMS: %v", e.h.stored[0])
	}
	if _, err := e.call("HeatCostRun", "transfer", map[string]any{"id": kid}); err == nil {
		t.Fatal("Übergabe ohne Mieter im Lauf")
	}
	e.h.lists["SettlementTenant"] = []map[string]any{{"contract_id": "MV-1", "rent_object_id": "WE16", "usage_from": "2025-01-01", "usage_to": "2025-12-31"}}
	e.h.inputs = []map[string]any{{"line_no": 1, "account": "HEIZ", "rent_object_id": "WE16", "amount": "2433.39"}}
	tr := e.must("HeatCostRun", "transfer", map[string]any{"id": kid})
	if len(e.h.inputs) != 2 || e.h.inputs[0]["amount"] != "2367.02" || e.h.inputs[1]["account"] != "CO2" || e.h.inputs[1]["amount"] != "-28.84" ||
		e.h.inputs[1]["contract_id"] != "MV-1" {
		t.Fatalf("Übergabe: %v %v", e.h.inputs, tr["message"])
	}
}

// TestUserChange: Nutzerwechsel in Gretchens Wohnung zum 1. Juli (§ 9b HeizkostenV) –
// Grundkosten Heizung nach Gradtagszahlen VDI 2067 oder Kalendertagen, Grundkosten
// Warmwasser nach Kalendertagen, ohne Zwischenablesung auch der Verbrauch.
func TestUserChange(t *testing.T) {
	e := setup(t)
	unit, run := e.liegenschaft()
	id := "1000|" + run
	if r := e.must("HeatCostRun", "get", map[string]any{"id": id}); str(r["heat_change"]) != changeDegree || str(r["degree_table"]) != "VDI2067" {
		t.Fatalf("Vorgabe Nutzerwechsel: %v %v", r["heat_change"], r["degree_table"])
	}
	e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	if _, err := e.h.db.Exec("UPDATE heatcost__run SET heat_change = NULL"); err != nil {
		t.Fatal(err)
	}
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	e.must("HeatCostUser", "update", map[string]any{"id": "1000|" + unit + "|16", "data": map[string]any{"valid_to": "2025-06-30"}})
	e.create("HeatCostUser", map[string]any{"company_code": "1000", "unit_id": unit, "user_no": "17", "object_id": "WE16", "groups": "H1_2,W1_2",
		"valid_from": "2025-07-01", "designation": "Valentin"})
	// Abrechnungen von vor 0.1.4 ohne Einstellung: ohne Nutzerwechsel rechnen, sonst Hinweis
	if _, err := e.h.db.Exec("UPDATE heatcost__run SET heat_change = NULL"); err != nil {
		t.Fatal(err)
	}
	e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	if _, err := e.call("HeatCostRun", "compute", map[string]any{"id": id}); err == nil || !strings.Contains(err.Error(), "Nutzer 16 wechselt") {
		t.Fatalf("ohne Einstellung: %v", err)
	}
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"heat_change": changeDegree}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	got16, _ := e.userAmounts(run, "16")
	got17, _ := e.userAmounts(run, "17")
	deg := 583.333 / 1000
	tm := 181.0 / 365
	if a := got16["Heizung – 30,00 % Grundkosten (Nutzungsanteil 58,33 %)"]; math.Abs(a-271.40*deg) > 0.02 {
		t.Fatalf("Grundkosten Heizung Vornutzer: %v", got16)
	}
	if a := got17["Heizung – 30,00 % Grundkosten (Nutzungsanteil 41,67 %)"]; math.Abs(a-271.40*(1-deg)) > 0.02 {
		t.Fatalf("Grundkosten Heizung Nachnutzer: %v", got17)
	}
	if a := got16["Warmwasser Haus 1–2 – 30,00 % Grundkosten (Nutzungsanteil 49,59 %)"]; math.Abs(a-134.26*tm) > 0.02 {
		t.Fatalf("Grundkosten Warmwasser: %v", got16)
	}
	if !near(got16["Heizung – 70,00 % Verbrauchskosten"], 366.34) {
		t.Fatalf("Verbrauch nach Zwischenablesung: %v", got16)
	}
	// Kalendertage
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"heat_change": changeTime}})
	e.must("HeatCostRun", "compute", map[string]any{"id": id})
	got16, _ = e.userAmounts(run, "16")
	if a := got16["Heizung – 30,00 % Grundkosten (Nutzungsanteil 49,59 %)"]; math.Abs(a-271.40*tm) > 0.02 {
		t.Fatalf("Grundkosten Heizung zeitanteilig: %v", got16)
	}
	// ohne Zwischenablesung
	e.must("HeatCostRun", "update", map[string]any{"id": id, "data": map[string]any{"heat_change": changeDegree}})
	for _, u := range []string{"16", "17"} {
		e.must("HeatCostUser", "update", map[string]any{"id": "1000|" + unit + "|" + u, "data": map[string]any{"no_interim": true}})
	}
	e.h.usageCalls = nil
	e.must("HeatCostRun", "fetch", map[string]any{"id": id})
	if len(e.h.usageCalls) != 2 || e.h.usageCalls[0]["from"] != "2025-01-01" || e.h.usageCalls[0]["to"] != "2025-12-31" {
		t.Fatalf("Zeitraum ohne Zwischenablesung: %v", e.h.usageCalls)
	}
	vals := map[string]float64{}
	for _, v := range items(e.must("HeatCostValue", "list", map[string]any{"query": map[string]any{"company_code": "1000", "run_id": run, "user_no": "16"}})) {
		vals[str(v["key_code"])] = toFloat(v["value"])
	}
	if math.Abs(vals["VE"]-4009.276*deg) > 0.01 || math.Abs(vals["WW"]-38.232*tm) > 0.001 || !near(vals["HZF"], 52.6) {
		t.Fatalf("Werte ohne Zwischenablesung: %v", vals)
	}
	// fehlender Monat in der Tabelle
	if _, err := e.h.db.Exec("DELETE FROM heatcost__degree_day WHERE company_code = '1000' AND table_code = 'VDI2067' AND year = 2025 AND month = 3"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.call("HeatCostRun", "compute", map[string]any{"id": id}); err == nil || !strings.Contains(err.Error(), "März 2025") {
		t.Fatalf("fehlender Monat: %v", err)
	}
	if out := e.must("HeatCostDegreeTable", "copyYear", map[string]any{"id": "1000|VDI2067", "data": map[string]any{"dy_to": 2027}}); !strings.Contains(str(out["message"]), "12 Monatswerte von 2026 nach 2027") {
		t.Fatalf("Jahr anlegen: %v", out)
	}
}
