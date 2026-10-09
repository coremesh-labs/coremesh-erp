package opcost

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func TestCatalogs(t *testing.T) {
	e := setup(t)
	r := e.must(setupObject, "setupCompany", map[string]any{"company": 1000})
	if r["cost_categories"] != len(defaultCostCategories) || r["allocation_keys"] != len(defaultAllocationKeys) {
		t.Fatalf("Einrichtung: %v", r)
	}
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); r["cost_categories"] != 0 {
		t.Fatalf("zweites Mal: %v", r)
	}
	w := e.must("CostCategory", "update", map[string]any{"id": "1000|WASSER", "data": map[string]any{"account_number": "7000"}})
	if w["account_number"] != "7000" || w["allocable"] != true || w["betrkv_no"] != "2" {
		t.Fatalf("Wasser: %v", w)
	}
	if v := e.must("CostCategory", "get", map[string]any{"id": "1000|VERWALT"}); v["allocable"] != false || v["cost_type"] != costNonAllocable {
		t.Fatalf("Verwaltung: %v", v)
	}
	_, err := e.call("CostCategory", "update", map[string]any{"id": "1000|WASSER", "data": map[string]any{"account_number": "9999"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Konto")
	expect(t, e.try("AllocationKey", map[string]any{"company_code": "1000", "code": "x", "name": "x", "basis": "MEASUREMENT"}),
		sdk.ErrInvalidArgument, "Bemessung ohne Bemessungsart")
	k := e.create("AllocationKey", map[string]any{"company_code": "1000", "code": "nfl", "name": "Nutzfläche", "basis": "MEASUREMENT", "measurement_type": "nfl"})
	if k["code"] != "NFL" || k["measurement_type"] != "NFL" {
		t.Fatalf("Schlüssel: %v", k)
	}
	if d := e.create("AllocationKey", map[string]any{"company_code": "1000", "code": "D2", "name": "x", "basis": "DIRECT", "measurement_type": "MEA"}); d["measurement_type"] != nil {
		t.Fatalf("direkt ohne Bemessungsart: %v", d)
	}
}
