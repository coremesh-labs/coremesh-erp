package contract

import (
	"testing"
)

// Positionen: Menge × Einzelpreis, Rundungsregel mit 4 Stellen, nachgelagerte
// Konditionen (Materialanteil als Anteil, Umsatzsteuer als Zuschlag), Route positions.
func TestConditionPositions(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Hausmeister",
		"partner_id": "BP1", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	base := func(k map[string]any) map[string]any {
		k["company_code"], k["contract_id"], k["payment_mode"], k["frequency"], k["valid_from"] = "1000", c, "IN_ADVANCE", "MONTHLY", "2026-01-01"
		return k
	}
	k := e.create("ContractCondition", base(map[string]any{"condition_type": "KM", "calc_method": "FIXED", "quantity": "3", "unit": "h", "unit_price": "33,335",
		"rounding_rule": "KFM4"}))
	if k["unit"] != "H" || k["unit_price"] != "33.335" || k["amount"] != "100.01" { // 3 × 33,335 = 100,005 → 100,01
		t.Fatalf("Menge × Einzelpreis: %v", k)
	}
	if err := e.try("ContractCondition", base(map[string]any{"condition_type": "NK", "calc_method": "FIXED", "unit_price": "1.234"})); err == nil {
		t.Fatal("3 Stellen im Einzelpreis bei Standardregel")
	}
	// ältere Aufrufer: nur Betrag → Menge 1, Stück
	k2 := e.create("ContractCondition", base(map[string]any{"condition_type": "NK", "calc_method": "FIXED", "amount": "200,00"}))
	if k2["unit"] != "STK" || k2["quantity"] != "1" || k2["unit_price"] != "200" {
		t.Fatalf("nur Betrag: %v", k2)
	}
	e.create("ContractDependentType", map[string]any{"company_code": "1000", "code": "MAT", "name": "Materialanteil", "method": "PERCENT",
		"percent": "10", "effect": "SHARE", "base_types": "km", "sort_order": 10, "account_number": "1200"})
	e.create("ContractDependentType", map[string]any{"company_code": "1000", "code": "UST", "name": "Umsatzsteuer", "method": "TAX",
		"tax_code": "U19", "effect": "SURCHARGE", "sort_order": 90, "account_number": "1200"})
	if err := e.try("ContractDependentType", map[string]any{"company_code": "1000", "code": "X", "name": "x", "method": "PERCENT", "effect": "SURCHARGE"}); err == nil {
		t.Fatal("Prozent ohne Satz")
	}
	for _, d := range []string{"MAT", "UST"} {
		e.create("ConditionType", map[string]any{"company_code": "1000", "code": d, "name": d, "claim_class": "SECONDARY"})
		e.create("ContractCondition", base(map[string]any{"condition_type": d, "calc_method": "DEPENDENT", "dependent_type": d,
			"valid_from": "2026-02-01"}))
	}
	out := e.must("ContractCondition", "positions", map[string]any{"company_code": "1000", "contract_id": c, "date": "2026-03-01"})
	items, deps := out["items"].([]any), out["dependents"].([]any)
	if len(items) != 2 || len(deps) != 2 {
		t.Fatalf("Positionen: %v", out)
	}
	got := map[string]string{}
	for _, d := range deps {
		m := d.(map[string]any)
		got[m["dependent_type"].(string)] = m["amount"].(string)
	}
	// Materialanteil 10 % von 100,01 = 10,00 (in der Basis); Umsatzsteuer 19 % von 300,01 = 57,00
	if got["MAT"] != "10.00" || got["UST"] != "57.00" {
		t.Fatalf("nachgelagert: %v", got)
	}
}
