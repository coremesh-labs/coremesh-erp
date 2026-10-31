package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// TestPaymentMethods: Zahlwege aus dem Katalog; das Merkmal „mit Mandat“
// blendet die Mandatsfelder ein, auch für einen selbst angelegten Zahlweg.
func TestPaymentMethods(t *testing.T) {
	e := setup(t)
	e.basics()
	if n, err := e.m.seedPaymentMethods(e.ctx, "1000"); err != nil || n != 0 {
		t.Fatalf("Vorschlag schon beim Einrichten: %d %v", n, err)
	}
	e.create(paymentMethodObject, map[string]any{"company_code": "1000", "code": "FIRMENLS", "name": "Firmenlastschrift", "uses_mandate": true})
	id := e.rentContract()
	_, err := e.call("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"payment_method": "SCHECK"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Zahlweg")
	e.must("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"payment_method": "firmenls", "mandate_ref": " M-1 ", "creditor_id": "de98 zzz 09999999999"}})
	c := e.must("Contract", "get", map[string]any{"id": "1000|" + id})
	if c["payment_method"] != "FIRMENLS" || c["mandate_ref"] != "M-1" || c["creditor_id"] != "DE98ZZZ09999999999" {
		t.Fatalf("Vertrag: %v", c)
	}
	visible := func(method string) bool {
		t.Helper()
		st, err := e.m.contractFormState(e.ctx, metamodel.FormStateRequest{Mode: "edit", ID: "1000|" + id, Values: map[string]string{"payment_method": method}})
		if err != nil {
			t.Fatal(err)
		}
		return *st.Fields["mandate_ref"].Visible
	}
	if !visible("FIRMENLS") || !visible("DIRECT_DEBIT") || visible("STANDING_ORDER") || visible("") {
		t.Fatal("Mandatsfelder nach Merkmal")
	}
	_, err = e.call(reminderRuleObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "MV", "condition_kind": "FO",
		"occasion": "AMOUNT_CHANGE", "description": "x", "lead_days": 10, "payment_methods": "SCHECK"}})
	expect(t, err, sdk.ErrInvalidArgument, "Regel mit unbekanntem Zahlweg")
}
