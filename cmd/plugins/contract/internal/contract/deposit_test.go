package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestDeposit: Mietkaution als eigener Vertrag mit Bezug auf den Mietvertrag;
// Kondition einmalig mit Monatsraten aus der Konditionsart.
func TestDeposit(t *testing.T) {
	e := setup(t)
	e.basics()
	mv := e.rentContract()
	kt := func(parent string) (map[string]any, error) {
		return e.call("Contract", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "KT",
			"designation": "Kaution Müller", "partner_id": "BP1", "valid_from": "2026-01-01", "parent_contract_id": parent}})
	}
	_, err := kt("")
	expect(t, err, sdk.ErrInvalidArgument, "Kaution ohne Mietvertrag")
	_, err = kt("GIBTSNICHT")
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Bezugsvertrag")
	c, err := kt(mv)
	if err != nil {
		t.Fatal(err)
	}
	id := c["contract_id"].(string)
	// Kaution als Bezugsvertrag einer Kaution: falsche Art
	_, err = kt(id)
	expect(t, err, sdk.ErrInvalidArgument, "Bezugsvertrag falscher Art")
	// Mietvertrag ohne Bezug erlaubt, mit Bezug nicht
	_, err = e.call("Contract", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "MV",
		"designation": "x", "partner_id": "BP1", "valid_from": "2026-01-01", "parent_contract_id": mv}})
	expect(t, err, sdk.ErrInvalidArgument, "Bezug bei Mietvertrag")

	// Kondition Kaution: einmalig, Raten aus der Konditionsart (3), überschreibbar
	k := e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "KA",
		"calc_method": "FIXED", "amount": "2550,00", "frequency": "ONCE", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	if toInt(k["installments"]) != 3 {
		t.Fatalf("Raten aus der Konditionsart: %v", k["installments"])
	}
	k2 := e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "BG",
		"calc_method": "FIXED", "amount": "50,00", "frequency": "ONCE", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01", "installments": 2})
	if toInt(k2["installments"]) != 2 {
		t.Fatalf("Raten an der Kondition: %v", k2["installments"])
	}
	// Monatlich: keine Raten
	k3 := e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "KM",
		"calc_method": "FIXED", "amount": "1,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01", "installments": 4})
	if k3["installments"] != nil {
		t.Fatalf("Raten bei monatlich: %v", k3["installments"])
	}
	// Abschnitt am Mietvertrag: zugehörige Verträge
	rel := e.must("Contract", "list", map[string]any{"query": map[string]any{"company_code": "1000", "parent_contract_id": mv}})
	if len(items(rel)) != 1 {
		t.Fatalf("zugehörige Verträge: %v", rel)
	}
}
