package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
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
	bad["rate"], bad["installments"] = "2", "5"
	_, err = e.call(depreciationObject, "create", map[string]any{"data": bad})
	expect(t, err, sdk.ErrInvalidArgument, "Raten im Jahr")
	dep["installments"], dep["annual_amount"], dep["rounding_rule"] = "12", "5.990,00", "kfm"
	d := e.create(depreciationObject, dep)
	if d["base_amount"] != "300000.00" || d["condition_type"] != "AF" || d["rate"] != "2" ||
		d["annual_amount"] != "5990.00" || crud.Str(d["installments"]) != "12" || d["rounding_rule"] != "KFM" {
		t.Fatalf("Abschreibung: %v", d)
	}
	if r := e.must("Contract", "activate", map[string]any{"id": "1000|" + id}); r["status"] != statusActive {
		t.Fatalf("aktiviert: %v", r)
	}
	// KV ohne Vertragspartner (Vorschlag „Vertragspartner Pflicht“ aus); MV verlangt ihn
	if kv["partner_required"] != false {
		t.Fatalf("Vertragsart KV: Partner Pflicht %v", kv["partner_required"])
	}
	_, err = e.call("Contract", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "MV",
		"designation": "Miete ohne Mieter", "valid_from": "2025-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Vertragspartner ist Pflicht")
	c2 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "KV", "designation": "Schlossallee 1",
		"valid_from": "2020-01-01", "billing_start": "2026-01-01"})
	id2 := c2["contract_id"].(string)
	if c2["external_number"] != "KV-SCH-001" {
		t.Fatalf("ohne Partner: %v", c2)
	}
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id2, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2020-01-01"})
	dep["contract_id"], dep["valid_from"] = id2, "2020-01-01"
	e.create(depreciationObject, dep)
	if r := e.must("Contract", "activate", map[string]any{"id": "1000|" + id2}); r["status"] != statusActive {
		t.Fatalf("ohne Partner aktiviert: %v", r)
	}
	// späterer Kauf im selben Haus: Bezugsvertrag KV erlaubt, nicht Pflicht
	if kv["parent_types"] != "KV" || kv["parent_required"] != false {
		t.Fatalf("Vertragsart KV: Bezug %v %v", kv["parent_types"], kv["parent_required"])
	}
	c3 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "KV", "designation": "Kauf 2",
		"valid_from": "2024-08-01", "parent_contract_id": id2})
	if c3["parent_contract_id"] != id2 {
		t.Fatalf("Bezugsvertrag: %v", c3)
	}
	_, err = e.call("Contract", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "KV",
		"designation": "Kauf 3", "valid_from": "2024-08-01", "parent_contract_id": id}})
	if err != nil {
		t.Fatalf("Bezug auf aktiven KV: %v", err)
	}
}
