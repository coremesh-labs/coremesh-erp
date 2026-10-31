package ledger

import (
	"testing"
)

// TestReferences (0.35.0): Rechnung, Objekt und Vertrag an der Kreditorenposition;
// die Zahlung übernimmt sie beim Ausgleich, nicht bei zwei verschiedenen Rechnungen;
// die Bankposition bleibt ohne (Feldstatus); Protokoll je Position.
func TestReferences(t *testing.T) {
	e := setup(t)
	e.rentCompany() // 1700 Bankguthaben: BANK, 2900 Kreditoren: SUPPLIER
	db := e.h.db
	head := func(no string) {
		if _, err := db.Exec(`INSERT INTO ledger__journal_header (document_number, company_code_id, fiscal_year, posting_period, document_type,
			document_date, posting_date, currency, local_currency, exchange_rate, source_module, created_at)
			VALUES (?, '1000', 2026, 1, 'KR', '2026-01-01', '2026-01-01', 'EUR', 'EUR', '1', 'MANUAL', '2026-01-01T00:00:00Z')`, no); err != nil {
			t.Fatal(err)
		}
	}
	item := func(no string, line int, account, kind string, amount int) {
		if _, err := db.Exec(`INSERT INTO ledger__journal_item (document_number, line_item_number, ledger, company_code_id, fiscal_year, posting_period,
			posting_date, chart_of_accounts_id, account_kind, account_number, shkzg, amount_document_curr, amount_local_curr, currency, local_currency, supplier_id)
			VALUES (?, ?, '0L', '1000', 2026, 1, '2026-01-01', 'SKR25', ?, ?, 'S', ?, ?, 'EUR', 'EUR', 'L1')`, no, line, kind, account, amount, amount); err != nil {
			t.Fatal(err)
		}
	}
	clear := func(debitDoc string, debitLine int, creditDoc string, creditLine, amount int) {
		if _, err := db.Exec(`INSERT INTO ledger__clearing (company_code_id, ledger, debit_fiscal_year, debit_document_number, debit_line, credit_fiscal_year,
			credit_document_number, credit_line, amount, clearing_date, method, created_at) VALUES ('1000', '0L', 2026, ?, ?, 2026, ?, ?, ?, '2026-01-02', 'AUTO', '2026-01-02T00:00:00Z')`,
			debitDoc, debitLine, creditDoc, creditLine, amount); err != nil {
			t.Fatal(err)
		}
	}
	get := func(no string, line int, field string) string {
		var c string
		db.QueryRow(`SELECT COALESCE(`+field+`, '') FROM ledger__journal_item WHERE document_number = ? AND line_item_number = ?`, no, line).Scan(&c)
		return c
	}
	// Rechnungen 1 und 2, Zahlung 3 für Rechnung 1, Sammelzahlung 4 für 1 und 2
	for _, no := range []string{"1", "2", "3", "4"} {
		head(no)
	}
	item("1", 1, "7000", "S", 100)
	item("1", 2, "2900", "K", -100)
	item("2", 1, "7000", "S", 50)
	item("2", 2, "2900", "K", -50)
	item("3", 1, "1700", "S", -60)
	item("3", 2, "2900", "K", 60)
	item("4", 1, "1700", "S", -90)
	item("4", 2, "2900", "K", 90)
	clear("3", 2, "1", 2, 60)
	clear("4", 2, "1", 2, 40)
	clear("4", 2, "2", 2, 50)

	fill := func(no string, data map[string]any) int {
		r := e.must(refObject, "fill", map[string]any{"id": "1000|2026|" + no, "data": data})
		return int(toInt(r["changed"]))
	}
	n := fill("1", map[string]any{"fields": map[string]any{"invoice_id": "ER-1", "dimension_custom_1": "HAUS1", "rent_contract_id": "WH-1"},
		"lines": map[string]any{"1": map[string]any{"charge_type": "MIETER", "charge_id": "RV-1"}}, "change_reason": "Test"})
	// Rechnung: 2 Positionen × 3 Felder + Belastung an Pos. 1 (2) = 8; Zahlung 3: 3 Felder
	if n != 11 {
		t.Fatalf("ergänzt: %d", n)
	}
	if get("1", 1, "charge_id") != "RV-1" || get("1", 2, "charge_id") != "" || get("1", 2, "invoice_id") != "ER-1" || get("1", 2, "dimension_custom_1") != "HAUS1" {
		t.Fatalf("Rechnung: %q %q %q", get("1", 1, "charge_id"), get("1", 2, "invoice_id"), get("1", 2, "dimension_custom_1"))
	}
	if get("3", 2, "invoice_id") != "ER-1" || get("3", 2, "dimension_custom_1") != "HAUS1" || get("3", 2, "rent_contract_id") != "WH-1" || get("3", 1, "invoice_id") != "" {
		t.Fatalf("Zahlung: %q %q %q / Bank %q", get("3", 2, "invoice_id"), get("3", 2, "dimension_custom_1"), get("3", 2, "rent_contract_id"), get("3", 1, "invoice_id"))
	}
	if get("4", 2, "invoice_id") != "" { // Rechnung 2 ohne Verweise: nicht eindeutig
		t.Fatalf("Sammelzahlung: %q", get("4", 2, "invoice_id"))
	}
	// Rechnung 2 mit eigener Rechnungsnummer: Sammelzahlung bleibt leer (zwei Werte), Objekt gleich → übernommen
	fill("2", map[string]any{"fields": map[string]any{"invoice_id": "ER-2", "dimension_custom_1": "HAUS1"}, "partner_only": true})
	if get("4", 2, "invoice_id") != "" || get("4", 2, "dimension_custom_1") != "HAUS1" || get("2", 1, "invoice_id") != "" {
		t.Fatalf("Sammelzahlung: %q %q / Aufwand 2: %q", get("4", 2, "invoice_id"), get("4", 2, "dimension_custom_1"), get("2", 1, "invoice_id"))
	}
	var logged int
	var reason string
	db.QueryRow(`SELECT COUNT(*) FROM ledger__journal_change WHERE document_number = '3'`).Scan(&logged)
	db.QueryRow(`SELECT reason FROM ledger__journal_change WHERE document_number = '3' AND field_name = 'invoice_id'`).Scan(&reason)
	if logged != 3 || reason != inheritReason {
		t.Fatalf("Protokoll Zahlung: %d %q", logged, reason)
	}
	// unbekanntes Feld
	if _, err := e.call(refObject, "fill", map[string]any{"id": "1000|2026|1", "data": map[string]any{"fields": map[string]any{"amount_local_curr": "1"}}}); err == nil {
		t.Fatal("Betrag ist keine Kontierung")
	}
	// Ausgleich beim Buchen (writeClearings): die neue Zahlung übernimmt die Verweise von Rechnung 2
	head("5")
	item("5", 1, "1700", "S", -10)
	item("5", 2, "2900", "K", 10)
	if err := e.m.writeClearings(e.ctx, "1000", "0L", []clearing{{Debit: &openItem{FY: 2026, Doc: "5", Line: 2, Date: "2026-01-03"},
		Credit: &openItem{FY: 2026, Doc: "2", Line: 2, Date: "2026-01-01"}, Amount: 10, Method: clearManual}}); err != nil {
		t.Fatal(err)
	}
	if get("5", 2, "invoice_id") != "ER-2" || get("5", 1, "invoice_id") != "" {
		t.Fatalf("Zahlung 5: %q / Bank %q", get("5", 2, "invoice_id"), get("5", 1, "invoice_id"))
	}
	// 0.35.1: „Vertrag nachtragen …“ an Rechnung 2 gibt an ihre Zahlungen weiter
	e.must(entryObject, addContractAction, map[string]any{"id": "1000|2026|2", "data": map[string]any{"new_contract": "WH-1"}})
	if get("5", 2, "rent_contract_id") != "WH-1" || get("4", 2, "rent_contract_id") != "WH-1" {
		t.Fatalf("Zahlungen nach Vertrag nachtragen: %q %q", get("5", 2, "rent_contract_id"), get("4", 2, "rent_contract_id"))
	}
}
