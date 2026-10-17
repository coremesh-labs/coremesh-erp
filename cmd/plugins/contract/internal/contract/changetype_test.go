package contract

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestChangeType: Vertragsart DL → DLHZ; Konditionen bekommen das Konto der neuen
// Kontenfindung, fehlt eine, ändert sich nichts. Gegenstände nach Vertragsart gefiltert.
func TestChangeType(t *testing.T) {
	e := setup(t)
	e.basics()
	for _, q := range []string{
		`CREATE TEMP TABLE ct AS SELECT * FROM contract__contract_type WHERE company_code = '1000' AND code = 'DL'`,
		`UPDATE ct SET code = 'DLHZ', name = 'Wartung Heizung'`,
		`INSERT INTO contract__contract_type SELECT * FROM ct`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.h.accounts["1000|6450"] = "Reparaturen"
	e.create("ContractAccount", map[string]any{"company_code": "1000", "contract_type": "DLHZ", "condition_type": "EN", "account_number": "6450", "is_default": true})
	id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "DL", "designation": "Heizung",
		"partner_id": "INS", "valid_from": "2026-01-01"})["contract_id"].(string)
	for _, typ := range []string{"EN", "MA"} {
		e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": typ, "calc_method": "FIXED",
			"amount": "10,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	}
	change := func(to string) error {
		_, err := e.call("Contract", "changeType", map[string]any{"id": "1000|" + id, "data": map[string]any{"new_contract_type": to}})
		return err
	}
	err := change("DLHZ")
	expect(t, err, sdk.ErrInvalidArgument, "MA ohne Kontenfindung")
	if !strings.Contains(err.Error(), "MA") {
		t.Fatalf("Meldung nennt MA nicht: %v", err)
	}
	expect(t, change("MV"), sdk.ErrInvalidArgument, "andere Richtung")
	var n int
	_ = e.h.db.QueryRow(`SELECT COUNT(*) FROM contract__condition WHERE contract_id = ? AND contract_type = 'DL' AND account_number = '4999'`, id).Scan(&n)
	if n != 2 {
		t.Fatalf("unverändert erwartet, %d Konditionen DL/4999", n)
	}
	// Filter: in DLHZ ist bisher nur EN vorgesehen
	got := items(e.must("ConditionType", "list", map[string]any{"query": map[string]any{"company_code": "1000", "contract_type": "DLHZ"}}))
	if len(got) != 1 || got[0]["code"] != "EN" {
		t.Fatalf("Gegenstände DLHZ: %v", got)
	}
	e.create("ContractAccount", map[string]any{"company_code": "1000", "contract_type": "DLHZ", "condition_type": "MA", "account_number": "4999", "is_default": true})
	if err := change("DLHZ"); err != nil {
		t.Fatal(err)
	}
	rows, err := e.h.db.Query(`SELECT condition_type, contract_type, account_number FROM contract__condition WHERE contract_id = ? ORDER BY condition_type`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a, b, c string
		_ = rows.Scan(&a, &b, &c)
		out = append(out, a+"/"+b+"/"+c)
	}
	var ct string
	_ = e.h.db.QueryRow(`SELECT contract_type FROM contract__contract WHERE contract_id = ?`, id).Scan(&ct)
	if ct != "DLHZ" || strings.Join(out, " ") != "EN/DLHZ/6450 MA/DLHZ/4999" {
		t.Fatalf("nach Wechsel: %s %v", ct, out)
	}
}
