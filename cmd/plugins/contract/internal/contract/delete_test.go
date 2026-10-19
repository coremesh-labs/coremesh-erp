package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestDeleteContract: Vertrag ohne Buchungen löschen (mit Unterobjekten);
// mit Hauptbuch-Positionen, offener Vorerfassung oder zugehörigem Vertrag nicht.
func TestDeleteContract(t *testing.T) {
	e := setup(t)
	e.basics()
	e.h.ledgerItems, e.h.drafts = map[string]bool{}, map[string]string{}
	kv := func(name, parent string) string {
		d := map[string]any{"company_code": "1000", "contract_type": "KV", "designation": name, "valid_from": "2020-01-01"}
		if parent != "" {
			d["parent_contract_id"] = parent
		}
		id := e.create("Contract", d)["contract_id"].(string)
		e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
			"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2020-01-01"})
		return id
	}
	id := kv("Fehlerfassung", "")
	child := kv("Kauf 2", id)
	_, err := e.call("Contract", "delete", map[string]any{"id": "1000|" + id})
	expect(t, err, sdk.ErrInvalidArgument, "1 zugehörige Verträge")

	e.h.ledgerItems["1000|"+child] = true
	_, err = e.call("Contract", "delete", map[string]any{"id": "1000|" + child})
	expect(t, err, sdk.ErrInvalidArgument, "Buchungen im Hauptbuch")
	e.h.ledgerItems["1000|"+child] = false

	if _, err := e.m.db.Exec(e.ctx, `INSERT INTO contract__posting (company_code, contract_id, condition_type, object_id, position, period_from, sequence,
		period_to, due_date, amount, currency, account_number, status, draft_id, recorded_at)
		VALUES ('1000', ?, 'AF', 'LpzBrn1WG001', 0, '2026-01-01', 0, '2026-01-31', '2026-01-31', 100, 'EUR', '6221', 'DRAFT', 'D1', '2026-01-01')`, child); err != nil {
		t.Fatal(err)
	}
	e.h.drafts["D1"] = "DRAFT"
	_, err = e.call("Contract", "delete", map[string]any{"id": "1000|" + child})
	expect(t, err, sdk.ErrInvalidArgument, "Vorerfassung D1")
	e.h.drafts["D1"] = "DISCARDED"
	if r := e.must("Contract", "delete", map[string]any{"id": "1000|" + child}); r["message"] == nil {
		t.Fatalf("gelöscht: %v", r)
	}
	if _, err := e.call("Contract", "get", map[string]any{"id": "1000|" + child}); err == nil {
		t.Fatal("Vertrag noch vorhanden")
	}
	for _, tbl := range []string{"contract__object", "contract__posting"} {
		res, _ := e.m.db.Query(e.ctx, "SELECT COUNT(*) FROM "+tbl+" WHERE contract_id = ?", child)
		if toInt(res.Rows[0][0]) != 0 {
			t.Fatalf("%s: Reste", tbl)
		}
	}
	e.must("Contract", "delete", map[string]any{"id": "1000|" + id})
}
