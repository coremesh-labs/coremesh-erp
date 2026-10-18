package opcost

import "testing"

// TestCostSheet: Regelwerk der Art COSTS – ohne Mieter, Schreiben und Buchung;
// Kostenaufstellung je Gruppe und Konto mit Belegen, Rechnung aus dem DMS als Anlage.
func TestCostSheet(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.create(definitionObject, map[string]any{"company_code": "1000", "code": "md", "name": "Messdienst", "unit_type": "Building",
		"unit_id": "GEB1", "tenant_contract_types": "MV", "mode": modeCosts})
	for i, a := range [][3]string{{"WASSER", "Wasserversorgung", "Wasser"}, {"ZMKWZ", "Miete Kaltwasserzähler", "Zählermiete"}, {"GAS", "Gas", "Energie"}} {
		e.create(accountObject, map[string]any{"company_code": "1000", "definition": "MD", "code": a[0], "name": a[1], "display_group": a[2],
			"sort_order": (i + 1) * 10, "on_statement": true})
	}
	e.create(runObject, map[string]any{"company_code": "1000", "definition": "MD", "period_from": "2025-01-01", "period_to": "2025-12-31"})
	id := "1000|MD|2025-01-01"
	if r := e.must(runObject, "get", map[string]any{"id": id}); len(r["_hidden_actions"].([]any)) != 2 {
		t.Fatalf("Aktionen der Kostenaufstellung: %v", r["_hidden_actions"])
	}
	e.h.billing = map[string]any{
		"summary": "Kosten 150.00", "check_result": "0 ok", "message": "gerechnet",
		"journal": []any{
			map[string]any{"step": 0, "rule": 10, "debit": "S:WASSER", "credit": "Q:LEDGER:635010", "amount": 10000 * 100000,
				"formula": "Beleg 4712 Pos. 1 (Buchungsdatum) 08.03.2025 Wasser", "source": "JE:4712/1"},
			map[string]any{"step": 0, "rule": 20, "debit": "S:ZMKWZ", "credit": "Q:INVOICE:ZMKWZ", "amount": 5000 * 100000,
				"formula": "Rechnung ER-1 Pos. 2 P9", "source": "INV:ER-1/2"},
		},
		"pool_totals": []any{map[string]any{"account": "WASSER", "amount": 10000}, map[string]any{"account": "ZMKWZ", "amount": 5000}},
	}
	e.must(runObject, "compute", map[string]any{"id": id})
	k, _ := e.m.runKeyOf(map[string]any{"id": id})
	d, err := e.m.costSheetData(e.ctx, k)
	if err != nil {
		t.Fatal(err)
	}
	groups := d["groups"].([]any)
	if len(groups) != 3 || d["total"] != 150.0 {
		t.Fatalf("Gruppen: %v, Summe %v", groups, d["total"])
	}
	w := groups[0].(map[string]any)["accounts"].([]any)[0].(map[string]any)
	wi := w["items"].([]any)[0].(map[string]any)
	if w["amount"] != 100.0 || wi["date"] != "2025-03-08" || wi["document"] != "4712" || wi["amount"] != 100.0 {
		t.Fatalf("Einzelposten: %v", w)
	}
	zi := groups[1].(map[string]any)["accounts"].([]any)[0].(map[string]any)["items"].([]any)[0].(map[string]any)
	if zi["document"] != "ER-1" || zi["text"] != "Miete Kaltwasserzähler" || zi["date"] != "2025-03-01" || zi["attachment"] != "anlage-01.pdf" {
		t.Fatalf("Rechnungsposition: %v", zi)
	}
	if a := d["attachments"].([]any); len(a) != 1 || len(d["files"].([]any)) != 1 {
		t.Fatalf("Anlagen: %v", a)
	}
	if m := d["missing"].([]any); len(m) != 1 || m[0].(map[string]any)["document"] != "Beleg 4712" {
		t.Fatalf("fehlende Belege: %v", m)
	}
	if g := groups[2].(map[string]any); g["amount"] != 0.0 {
		t.Fatalf("leere Gruppe: %v", g)
	}
}
