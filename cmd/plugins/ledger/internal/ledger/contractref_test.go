package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestContractMigration (0.34.0): Vertrag an Kreditorenpositionen aus dem Beleg
// und über den Ausgleich; Sachkontenpositionen bleiben; Protokoll je Position.
func TestContractMigration(t *testing.T) {
	e := setup(t)
	e.rentCompany() // 1700 Bankguthaben: Feldstatusgruppe BANK, 2900 Kreditoren: SUPPLIER
	db := e.h.db
	// Stand vor 0.34.0: Vertrag an Kreditoren ausgeblendet
	db.Exec(`UPDATE ledger__field_status SET status = 'SUPPRESS' WHERE group_id = 'SUPPLIER' AND field_name = 'rent_contract_id'`)
	db.Exec(`UPDATE ledger__field_status_group SET description = NULL WHERE id = 'SUPPLIER'`)
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	head := func(no string) {
		if _, err := db.Exec(`INSERT INTO ledger__journal_header (document_number, company_code_id, fiscal_year, posting_period, document_type,
			document_date, posting_date, currency, local_currency, exchange_rate, source_module, created_at)
			VALUES (?, '1000', 2026, 1, 'KR', '2026-01-01', '2026-01-01', 'EUR', 'EUR', '1', 'MANUAL', '2026-01-01T00:00:00Z')`, no); err != nil {
			t.Fatal(err)
		}
	}
	item := func(no string, line int, account, kind, contract string, amount int) {
		var c any
		if contract != "" {
			c = contract
		}
		if _, err := db.Exec(`INSERT INTO ledger__journal_item (document_number, line_item_number, ledger, company_code_id, fiscal_year, posting_period,
			posting_date, chart_of_accounts_id, account_kind, account_number, shkzg, amount_document_curr, amount_local_curr, currency, local_currency, rent_contract_id)
			VALUES (?, ?, '0L', '1000', 2026, 1, '2026-01-01', 'SKR25', ?, ?, 'S', ?, ?, 'EUR', 'EUR', ?)`, no, line, kind, account, amount, amount, c); err != nil {
			t.Fatal(err)
		}
	}
	// Sollstellung: Aufwand mit Vertrag, Kreditor ohne
	head("1")
	item("1", 1, "7000", "S", "WH-1", 100)
	item("1", 2, "2900", "K", "", -100)
	// Zahlung: Bank (ausgeblendet) und Kreditor ohne Vertrag, ausgeglichen mit Sollstellung
	head("2")
	item("2", 1, "1700", "S", "", -100)
	item("2", 2, "2900", "K", "", 100)
	if _, err := db.Exec(`INSERT INTO ledger__clearing (company_code_id, ledger, debit_fiscal_year, debit_document_number, debit_line, credit_fiscal_year,
		credit_document_number, credit_line, amount, clearing_date, method, created_at) VALUES ('1000', '0L', 2026, '2', 2, 2026, '1', 2, 100, '2026-01-02', 'AUTO', '2026-01-02T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	// Rechnung ohne Vertrag mit Sachkontenposition ohne Vertrag: bleibt leer
	head("3")
	item("3", 1, "7000", "S", "WH-1", 50)
	item("3", 2, "7100", "S", "", 50)
	item("3", 3, "2900", "K", "", -100)

	if err := e.m.migrateContracts(e.ctx); err != nil {
		t.Fatal(err)
	}
	get := func(no string, line int) string {
		var c string
		db.QueryRow(`SELECT COALESCE(rent_contract_id, '') FROM ledger__journal_item WHERE document_number = ? AND line_item_number = ?`, no, line).Scan(&c)
		return c
	}
	if get("1", 2) != "WH-1" || get("2", 2) != "WH-1" || get("2", 1) != "" || get("3", 3) != "WH-1" || get("3", 2) != "" {
		t.Fatalf("nachgetragen: 1/2=%q 2/2=%q 2/1=%q 3/3=%q 3/2=%q", get("1", 2), get("2", 2), get("2", 1), get("3", 3), get("3", 2))
	}
	var n int
	var val, reason string
	db.QueryRow(`SELECT COUNT(*) FROM ledger__journal_change WHERE field_name = 'rent_contract_id'`).Scan(&n)
	db.QueryRow(`SELECT new_value, reason FROM ledger__journal_change WHERE document_number = '2'`).Scan(&val, &reason)
	if n != 3 || val != "Pos. 2: WH-1" || !strings.Contains(reason, "Ausgleich") {
		t.Fatalf("Protokoll: %d %q %q", n, val, reason)
	}
	// wiederholbar
	if err := e.m.migrateContracts(e.ctx); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM ledger__journal_change`).Scan(&n)
	if n != 3 {
		t.Fatalf("zweiter Lauf: %d", n)
	}

	// Aktion: alle erlaubten leeren Positionen (auch Sachkonten), Bank ausgeblendet
	head("4")
	item("4", 1, "1700", "S", "", -80)
	item("4", 2, "7100", "S", "", 80)
	_, err := e.call(entryObject, addContractAction, map[string]any{"id": "1000|2026|4", "data": map[string]any{"new_contract": "XX"}})
	if err == nil {
		t.Fatal("unbekannter Vertrag")
	}
	r := e.must(entryObject, addContractAction, map[string]any{"id": "1000|2026|4", "data": map[string]any{"new_contract": "WH-1", "change_reason": "Test"}})
	if r["changed"] != int64(1) && r["changed"] != 1 && r["changed"] != float64(1) || get("4", 2) != "WH-1" || get("4", 1) != "" {
		t.Fatalf("Aktion: %v / %q %q", r, get("4", 1), get("4", 2))
	}
	_, err = e.call(entryObject, addContractAction, map[string]any{"id": "1000|2026|99", "data": map[string]any{"new_contract": "WH-1"}})
	if err == nil || !strings.Contains(err.Error(), sdk.ErrNotFound.Error()) && !strings.Contains(err.Error(), "Beleg") {
		t.Fatalf("unbekannter Beleg: %v", err)
	}
}
