package ledger

import (
	"testing"
)

// TestCorrectContract (0.39.0): Vertrag an der Kreditorenposition ersetzen, mit
// altem Wert im Protokoll; derived nur für nachgetragene Werte; Ausgleich neu.
func TestCorrectContract(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	db := e.h.db
	head := func(no string) {
		if _, err := db.Exec(`INSERT INTO ledger__journal_header (document_number, company_code_id, fiscal_year, posting_period, document_type,
			document_date, posting_date, currency, local_currency, exchange_rate, source_module, created_at)
			VALUES (?, '1000', 2026, 1, 'KR', '2026-01-01', '2026-01-01', 'EUR', 'EUR', '1', 'MANUAL', '2026-01-01T00:00:00Z')`, no); err != nil {
			t.Fatal(err)
		}
	}
	item := func(no string, line int, account, kind, contract string, amount int) {
		var c, sup any
		if contract != "" {
			c = contract
		}
		if kind == "K" {
			sup = "L1"
		}
		if _, err := db.Exec(`INSERT INTO ledger__journal_item (document_number, line_item_number, ledger, company_code_id, fiscal_year, posting_period,
			posting_date, chart_of_accounts_id, account_kind, account_number, shkzg, amount_document_curr, amount_local_curr, currency, local_currency, rent_contract_id, supplier_id)
			VALUES (?, ?, '0L', '1000', 2026, 1, '2026-01-01', 'SKR25', ?, ?, 'S', ?, ?, 'EUR', 'EUR', ?, ?)`, no, line, kind, account, amount, amount, c, sup); err != nil {
			t.Fatal(err)
		}
	}
	get := func(no string) string {
		var c string
		db.QueryRow(`SELECT COALESCE(rent_contract_id, '') FROM ledger__journal_item WHERE document_number = ? AND account_kind = 'K'`, no).Scan(&c)
		return c
	}
	// Sollstellungen je Vertrag
	head("1")
	item("1", 1, "7000", "S", "WH-1", 100)
	item("1", 2, "2900", "K", "WH-1", -100)
	head("2")
	item("2", 1, "7000", "S", "WH-2", 100)
	item("2", 2, "2900", "K", "WH-2", -100)
	// Zahlung für WH-1, der Vertrag WH-2 per Migration nachgetragen
	head("3")
	item("3", 1, "1700", "S", "", -100)
	item("3", 2, "2900", "K", "WH-2", 100)
	db.Exec(`INSERT INTO ledger__journal_change (company_code_id, fiscal_year, document_number, change_no, field_name, old_value, new_value, reason, changed_at)
		VALUES ('1000', 2026, '3', 1, 'rent_contract_id', NULL, 'Pos. 2: WH-2', 'Migration 0.34.0: Vertrag aus dem Ausgleich offener Posten', '2026-01-01T00:00:00Z')`)
	// Zahlung mit Vertrag aus der Buchung: derived lässt sie stehen
	head("4")
	item("4", 1, "1700", "S", "", -100)
	item("4", 2, "2900", "K", "WH-2", 100)
	if _, err := e.m.clearPartners(e.ctx, "1000", nil, "", true); err != nil {
		t.Fatal(err)
	}

	if _, err := e.call(entryObject, correctContractAction, map[string]any{"id": "1000|2026|3", "data": map[string]any{"fix_contract": "WH-1"}}); err == nil {
		t.Fatal("Grund ist Pflicht")
	}
	if _, err := e.call(entryObject, correctContractAction, map[string]any{"id": "1000|2026|3", "data": map[string]any{"fix_contract": "XX", "change_reason": "x"}}); err == nil {
		t.Fatal("unbekannter Vertrag")
	}
	r := e.must(entryObject, correctContractAction, map[string]any{"id": "1000|2026|4", "data": map[string]any{"fix_contract": "WH-1", "change_reason": "Bank", "derived": true}})
	if toInt(r["changed"]) != 0 || get("4") != "WH-2" {
		t.Fatalf("gebuchter Vertrag geändert: %v %q", r, get("4"))
	}
	r = e.must(entryObject, correctContractAction, map[string]any{"id": "1000|2026|3", "data": map[string]any{"fix_contract": "WH-1", "change_reason": "Bank", "derived": true}})
	if toInt(r["changed"]) != 1 || get("3") != "WH-1" || get("3") == "" {
		t.Fatalf("korrigiert: %v %q", r, get("3"))
	}
	var old, val string
	db.QueryRow(`SELECT old_value, new_value FROM ledger__journal_change WHERE document_number = '3' AND change_no = 2`).Scan(&old, &val)
	if old != "Pos. 2: WH-2" || val != "Pos. 2: WH-1" {
		t.Fatalf("Protokoll: %q %q", old, val)
	}
	// Ausgleich neu: Zahlung 3 gleicht Sollstellung 1 (WH-1) aus, Zahlung 4 die 2
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ledger__clearing WHERE (debit_document_number = '3' AND credit_document_number = '1')
		OR (debit_document_number = '4' AND credit_document_number = '2') OR (debit_document_number = '1' AND credit_document_number = '3')
		OR (debit_document_number = '2' AND credit_document_number = '4')`).Scan(&n)
	if n != 2 {
		var all int
		db.QueryRow(`SELECT COUNT(*) FROM ledger__clearing`).Scan(&all)
		t.Fatalf("Ausgleich: %d passend von %d", n, all)
	}
	// ohne derived: auch gebuchte Werte
	r = e.must(entryObject, correctContractAction, map[string]any{"id": "1000|2026|4", "data": map[string]any{"fix_contract": "WH-1", "change_reason": "von Hand"}})
	if toInt(r["changed"]) != 1 || get("4") != "WH-1" {
		t.Fatalf("von Hand: %v %q", r, get("4"))
	}
}
