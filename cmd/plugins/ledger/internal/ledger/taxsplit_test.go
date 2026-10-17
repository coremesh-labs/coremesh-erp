package ledger

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Seit 0.26.0: aufzuteilende Vorsteuer (Steuerkonto Pflicht, Bemessungsart nur dort) und
// Konto für nicht abziehbare Vorsteuer am Sachkonto im Buchungskreis.
func TestPartialTaxAndSubAccount(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	va := e.must("TaxCode", "get", map[string]any{"id": "VA19"})
	if va["kind"] != "PARTIAL" || va["account_number"] != "1406" {
		t.Fatalf("Vorschlag VA19: %v", va)
	}
	_, err := e.call("TaxCode", "create", map[string]any{"data": map[string]any{"code": "VAX", "name": "x", "kind": "PARTIAL", "rate": "19"}})
	expect(t, err, sdk.ErrInvalidArgument, "aufzuteilen ohne Steuerkonto")
	vn := e.must("TaxCode", "create", map[string]any{"data": map[string]any{"code": "VNX", "name": "x", "kind": "NONDEDUCTIBLE", "rate": "19", "split_measurement": "wfl"}})
	if vn["split_measurement"] != nil {
		t.Fatalf("Bemessungsart bei nicht abziehbarer Vorsteuer: %v", vn["split_measurement"])
	}
	e.must("TaxCode", "update", map[string]any{"id": "VA19", "data": map[string]any{"split_measurement": "wfl"}})
	if v := e.must("TaxCode", "get", map[string]any{"id": "VA19"}); v["split_measurement"] != "WFL" {
		t.Fatalf("Bemessungsart: %v", v["split_measurement"])
	}
	acc := func(nr string) map[string]any {
		return items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": nr}}))[0]
	}
	a := acc("6000")
	_, err = e.call("GLAccountCompany", "update", map[string]any{"id": a["id"], "data": map[string]any{"nondeductible_tax_account": "4711"}})
	expect(t, err, sdk.ErrInvalidArgument, "Steuer-Unterkonto nicht im Buchungskreis")
	_, err = e.call("GLAccountCompany", "update", map[string]any{"id": a["id"], "data": map[string]any{"nondeductible_tax_account": "6000"}})
	expect(t, err, sdk.ErrInvalidArgument, "Steuer-Unterkonto = Konto")
	e.must("GLAccountCompany", "update", map[string]any{"id": a["id"], "data": map[string]any{"nondeductible_tax_account": "1200"}})
	if got := acc("6000")["nondeductible_tax_account"]; got != "1200" {
		t.Fatalf("Steuer-Unterkonto: %v", got)
	}
}
