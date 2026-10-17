package contract

import "testing"

// Vorsteuerquote (seit 0.27.0): Anteil der Mietobjekte mit Vertrag, der Umsatzsteuer berechnet.
func TestTaxQuota(t *testing.T) {
	e := setup(t)
	e.basics()
	if e.h.measurements == nil {
		e.h.measurements = map[string]string{}
	}
	for id, wfl := range map[string]string{"LpzBrn1WG001": "60", "LpzBrn1WG002": "40", "LpzBrn1GE001": "100"} {
		e.rentObject(id, "UNIT", id)
		e.h.objects["RentObject|1000|"+id]["building_id"] = "LpzBrn1"
		e.h.measurements[id+"|WFL"] = wfl
	}
	e.rentObject("LpzBrn1POOL", "POOL", "Pool")
	e.h.objects["RentObject|1000|LpzBrn1POOL"]["building_id"] = "LpzBrn1"
	e.create("ContractDependentType", map[string]any{"company_code": "1000", "code": "UST19", "name": "Umsatzsteuer", "method": "TAX",
		"tax_code": "U19", "effect": "SURCHARGE", "sort_order": 90, "account_number": "1200"})
	lease := func(obj string, taxed bool) {
		c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": obj,
			"partner_id": "BP1", "valid_from": "2026-01-01"})["contract_id"].(string)
		e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c, "object_type": "RentObject",
			"object_id": obj, "is_main": true, "valid_from": "2026-01-01"})
		k := map[string]any{"company_code": "1000", "contract_id": c, "payment_mode": "IN_ADVANCE", "frequency": "MONTHLY", "valid_from": "2026-01-01"}
		with := func(m map[string]any) map[string]any {
			out := map[string]any{}
			for a, b := range k {
				out[a] = b
			}
			for a, b := range m {
				out[a] = b
			}
			return out
		}
		e.create("ContractCondition", with(map[string]any{"condition_type": "KM", "calc_method": "FIXED", "amount": "1.000,00"}))
		if taxed {
			e.create("ContractCondition", with(map[string]any{"condition_type": "UST", "calc_method": "DEPENDENT", "dependent_type": "UST19"}))
		}
		e.must("Contract", "activate", map[string]any{"id": "1000|" + c})
	}
	lease("LpzBrn1GE001", true)
	lease("LpzBrn1WG001", false)
	for _, tc := range []struct{ typ, obj, measure, want string }{
		{"Building", "LpzBrn1", "WFL", "0.50000000"},
		{"Building", "LpzBrn1", "", "0.33333333"},
		{"RentObject", "LpzBrn1GE001", "", "1.00000000"},
		{"RentObject", "LpzBrn1WG001", "WFL", "0.00000000"},
	} {
		out := e.must("ContractCondition", "taxQuota", map[string]any{"company_code": "1000", "object_type": tc.typ, "object_id": tc.obj,
			"date": "2026-06-30", "measurement_type": tc.measure})
		if out["quota"] != tc.want {
			t.Fatalf("%s %s %s: Quote %v (%v), erwartet %s", tc.typ, tc.obj, tc.measure, out["quota"], out["text"], tc.want)
		}
	}
	// vor Vertragsbeginn: nichts steuerpflichtig
	out := e.must("ContractCondition", "taxQuota", map[string]any{"company_code": "1000", "object_type": "Building", "object_id": "LpzBrn1", "date": "2025-12-31"})
	if out["quota"] != "0.00000000" {
		t.Fatalf("vor Vertragsbeginn: %v", out["quota"])
	}
	if _, err := e.call("ContractCondition", "taxQuota", map[string]any{"company_code": "1000", "object_type": "Building", "object_id": "LpzBrn1",
		"date": "2026-06-30", "measurement_type": "MEA"}); err == nil {
		t.Fatal("fehlende Bemessung MEA ohne Fehler")
	}
}
