package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestDepreciation: Kaufvertrag (KV) mit Abschreibungskondition – Vorschlag der
// Vertragsart ohne Partnerkonto, Beträge und Satz geprüft, Aktivierung ohne Konditionen.
func TestDepreciation(t *testing.T) {
	e := setup(t)
	e.basics()
	e.partner("SELL", "Verkäufer GmbH", "CREDITOR")
	e.h.accounts["1000|0230"] = "Bauten auf eigenen Grundstücken"
	e.h.accounts["1000|6221"] = "Abschreibungen auf Gebäude"
	kv := e.must("ContractType", "get", map[string]any{"id": "1000|KV"})
	if kv["partner_account_required"] != false || kv["needs_object"] != true {
		t.Fatalf("Vertragsart KV: %v", kv)
	}
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "KV", "designation": "Kauf Wohnung 1. OG",
		"partner_id": "SELL", "valid_from": "2025-07-15"})
	id := c["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2025-07-15"})
	dep := map[string]any{"company_code": "1000", "contract_id": id, "base_amount": "300.000,00", "land_amount": "60.000,00",
		"method": "LINEAR", "rate": "2", "asset_account": "0230", "expense_account": "6221", "valid_from": "2025-07-15"}
	bad := map[string]any{}
	for k, v := range dep {
		bad[k] = v
	}
	bad["rate"] = "zwei"
	_, err := e.call(depreciationObject, "create", map[string]any{"data": bad})
	expect(t, err, sdk.ErrInvalidArgument, "Satz")
	d := e.create(depreciationObject, dep)
	if d["base_amount"] != "300000.00" || d["condition_type"] != "AF" || d["rate"] != "2" {
		t.Fatalf("Abschreibung: %v", d)
	}
	if r := e.must("Contract", "activate", map[string]any{"id": "1000|" + id}); r["status"] != statusActive {
		t.Fatalf("aktiviert: %v", r)
	}
}
