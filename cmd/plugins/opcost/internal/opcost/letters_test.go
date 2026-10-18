package opcost

import "testing"

// tenantResult: Summe mit Teilen in Kontoreihenfolge und Rechenweg, Saldo je
// Ebene (Nachzahlung bzw. Guthaben), letzte Ebene als total.
func TestTenantResult(t *testing.T) {
	res := []map[string]any{
		{"contract": "MV1", "account": "MUELL", "level": int64(0), "amount": int64(21803)},
		{"contract": "MV1", "account": "HAUSWART", "level": int64(0), "amount": int64(36913)},
		{"contract": "MV1", "account": "HEIZ", "object": "W1", "level": int64(0), "amount": int64(243339)},
		{"contract": "MV1", "account": "BK", "level": int64(1), "amount": int64(58716)},
		{"contract": "MV1", "account": "HK", "object": "W1", "level": int64(1), "amount": int64(243339)},
		{"contract": "MV1", "account": "VBK", "level": int64(1), "amount": int64(60000)},
		{"contract": "MV1", "account": "VHK", "level": int64(1), "amount": int64(127200)},
		{"contract": "MV1", "account": "GBK", "level": int64(2), "amount": int64(-1284)},
		{"contract": "MV1", "account": "NHK", "level": int64(2), "amount": int64(116139)},
		{"contract": "MV2", "account": "BK", "level": int64(1), "amount": int64(1)},
	}
	names := map[string]string{"MUELL": "Müll", "HAUSWART": "Hauswart", "BK": "Betriebskosten", "HK": "Heizkosten", "VBK": "VZ BK", "VHK": "VZ HK",
		"NBK": "Nachzahlung BK", "GBK": "Guthaben BK", "NHK": "Nachzahlung HK", "GHK": "Guthaben HK"}
	sorts := map[string]int64{"HAUSWART": 10, "MUELL": 20}
	rules := []map[string]any{
		{"step": int64(1), "kind": "SUM", "sources": []string{"MUELL", "HAUSWART", "NIEDERSCHL"}, "target": "BK", "target_credit": ""},
		{"step": int64(1), "kind": "SUM", "sources": []string{"HEIZ"}, "target": "HK", "target_credit": ""},
		{"step": int64(2), "kind": "BALANCE", "sources": []string{"BK", "VBK"}, "target": "NBK", "target_credit": "GBK"},
		{"step": int64(2), "kind": "BALANCE", "sources": []string{"HK", "VHK"}, "target": "NHK", "target_credit": "GHK"},
	}
	r := tenantResult("MV1", res, names, sorts, map[string]string{"Wohnfläche": "WFL"}, rules, 2, `[{"account":"MUELL","rent_object_id":"","total":150000,"key":"Wohnfläche","weight":107.3,"weight_total":747.6,"factor":0.1435,"amount":21803}]`)
	sums := r["sums"].([]any)
	bk := sums[0].(map[string]any)
	parts := bk["parts"].([]any)
	if bk["amount"] != 587.16 || len(parts) != 2 || parts[0].(map[string]any)["code"] != "HAUSWART" {
		t.Fatalf("Summe: %v", bk)
	}
	d := parts[1].(map[string]any)["details"].([]any)[0].(map[string]any)
	if d["total"] != 1500.0 || d["amount"] != 218.03 || d["key"] != "WFL" {
		t.Errorf("Rechenweg: %v", d)
	}
	bal := r["balances"].([]any)
	b0, b1 := bal[0].(map[string]any), bal[1].(map[string]any)
	if b0["code"] != "GBK" || b0["amount"] != -12.84 || b0["name"] != "Guthaben BK" || b1["code"] != "NHK" || b1["amount"] != 1161.39 {
		t.Errorf("Salden: %v", bal)
	}
	if r["total"] == nil || r["total"].(map[string]any)["code"] != "NHK" {
		t.Errorf("total: %v", r["total"])
	}
	if hk := sums[1].(map[string]any); hk["parts"].([]any)[0].(map[string]any)["objects"].([]any)[0].(map[string]any)["id"] != "W1" {
		t.Errorf("Objekt je Konto: %v", hk)
	}
}
