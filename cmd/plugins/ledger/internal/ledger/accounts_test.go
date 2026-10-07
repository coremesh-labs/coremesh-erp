package ledger

import (
	"strings"
	"testing"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh_erp/pkg/ledgerapi"
)

// TestAccountNumbers: Sachkonten heißen <Kontenplan>-<Nummer>; ohne Präfix wird
// ergänzt, ein fremder Kontenplan abgelehnt.
func TestAccountNumbers(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04"})

	a := e.must("GLAccount", "create", map[string]any{"data": map[string]any{"chart_of_accounts_id": "SKR25", "account_number": "4711",
		"name": "Test", "account_type": "BALANCE_SHEET"}})
	if a["account_number"] != "SKR25-4711" || a["account_kind"] != "S" {
		t.Fatalf("ergänzt: %v", a)
	}
	_, err := e.call("GLAccount", "create", map[string]any{"data": map[string]any{"chart_of_accounts_id": "SKR25", "account_number": "SKR04-4712",
		"name": "Test", "account_type": "BALANCE_SHEET"}})
	expect(t, err, sdk.ErrInvalidArgument, "fremder Kontenplan")
	if err != nil && !strings.Contains(err.Error(), "gehört zum Kontenplan SKR04") {
		t.Fatalf("Meldung: %v", err)
	}
	if a := e.must("GLAccount", "create", map[string]any{"data": map[string]any{"chart_of_accounts_id": "SKR25", "account_number": "skr25-4713",
		"name": "Test", "account_type": "BALANCE_SHEET"}}); a["account_number"] != "SKR25-4713" {
		t.Fatalf("mit Präfix: %v", a)
	}
	// Buchen mit voller und kurzer Nummer; fremder Kontenplan scheitert.
	inv := rentInvoice("N-1")
	inv.Items[0].Account = "SKR25-1200"
	res, err := e.gl.Post(e.ctx, inv)
	if err != nil {
		t.Fatal(err)
	}
	bad := rentInvoice("N-2")
	bad.Items[0].Account = "SKR04-1200"
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "SKR04 in 1000")

	// Einzelposten: volle Nummer, Kontoart, Jahr/Periode; Filter auch mit kurzer Nummer.
	lines := items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res.ID, "account_number": "1200"}}))
	if len(lines) != 1 || lines[0]["account_number"] != "SKR25-1200" || lines[0]["account_kind"] != "D" || toInt(lines[0]["fiscal_year_period"]) != 2026010 {
		t.Fatalf("Position: %v", lines)
	}
	head := e.must("JournalEntry", "get", map[string]any{"id": res.ID})
	if toInt(head["fiscal_year_period"]) != 2026010 {
		t.Fatalf("Kopf: %v", head["fiscal_year_period"])
	}
	// Abstimmkonto im Kontenplan: Kontoart D.
	if a := e.must("GLAccount", "get", map[string]any{"id": "SKR25|SKR25-1200"}); a["account_kind"] != "D" {
		t.Fatalf("Kontoart Abstimmkonto: %v", a["account_kind"])
	}
}

// TestAccountKindPeriods: "+" ist der Hauptschalter; Kontoarten mit eigener
// Periodensteuerung brauchen zusätzlich eine eigene offene Periode.
func TestAccountKindPeriods(t *testing.T) {
	e := setup(t)
	e.rentCompany() // + offen 1–12/2026
	gl := ledgerapi.PostRequest{SourceModule: "MANUAL", CompanyCode: "1000", PostingDate: "2026-10-05", Currency: "EUR", HeaderText: "Umbuchung",
		Items: []ledgerapi.Item{{Account: "2800", Side: ledgerapi.Debit, Amount: "10"}, {Account: "2850", Side: ledgerapi.Credit, Amount: "10"}}}

	// Debitoren mit eigener Periodensteuerung: ohne eigene offene Periode gesperrt.
	e.must("AccountType", "update", map[string]any{"id": "D", "data": map[string]any{"own_period_control": true}})
	_, err := e.gl.Post(e.ctx, rentInvoice("K-1"))
	expect(t, err, sdk.ErrInvalidArgument, "D ohne eigene Periode")
	if _, err := e.gl.Post(e.ctx, gl); err != nil {
		t.Fatalf("Sachkonten (S) bleiben buchbar: %v", err)
	}
	e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "to": 10, "status": "OPEN", "kind": "D"})
	if _, err := e.gl.Post(e.ctx, rentInvoice("K-1")); err != nil {
		t.Fatalf("D offen: %v", err)
	}
	// Hauptschalter zu: nichts geht mehr, auch nicht D.
	e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "to": 10, "status": "CLOSED"})
	_, err = e.gl.Post(e.ctx, rentInvoice("K-2"))
	expect(t, err, sdk.ErrInvalidArgument, "+ geschlossen")
	gl.Items[0].Text = "zweite"
	_, err = e.gl.Post(e.ctx, gl)
	expect(t, err, sdk.ErrInvalidArgument, "+ geschlossen (S)")
	// Kontoart prüfen.
	_, err = e.call(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "status": "OPEN", "kind": "X"})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Kontoart")
}

// TestPeriodDefinitionPerCompany: Jeder Buchungskreis hat seine Periodendefinition.
func TestPeriodDefinitionPerCompany(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04"})
	e.must(loaderObject, "setupCompany", map[string]any{"company": "2000", "chart": "SKR04", "currency": "EUR"})
	if n := len(items(e.must("PostingPeriod", "list", map[string]any{"query": map[string]any{"company_code_id": "2000"}}))); n != 16 {
		t.Fatalf("2000: %d Perioden", n)
	}
	e.must("PostingPeriod", "update", map[string]any{"id": "1000|14", "data": map[string]any{"name": "Halbjahr", "calendar_month": 6}})
	if p := e.must("PostingPeriod", "get", map[string]any{"id": "2000|14"}); toInt(p["calendar_month"]) != 12 {
		t.Fatalf("2000 unverändert: %v", p)
	}
}

// TestAccountMigration: Kontonummern ohne Kontenplan (vor 0.9.0) werden umgestellt.
func TestAccountMigration(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	db := e.h.db
	for _, q := range []string{
		`INSERT INTO ledger__account_master (chart_of_accounts_id, account_number, name, account_type, account_kind, is_active) VALUES ('SKR25', '9990', 'Alt', 'BALANCE_SHEET', 'S', 1)`,
		`INSERT INTO ledger__account_company (id, company_code_id, chart_of_accounts_id, account_number, currency, reconciliation_type, tax_category, is_blocked)
		 VALUES ('old1', '1000', 'SKR25', '9990', 'EUR', 'CUSTOMER', 'NONE', 0)`,
		`INSERT INTO ledger__period_account_lock (id, company_code_id, ledger, fiscal_year, period_from, period_to, account_from, account_to, status, is_active)
		 VALUES ('l1', '1000', '0L', 2026, 1, 1, '9990', '9999', 'CLOSED', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.migrateAccounts(e.ctx); err != nil {
		t.Fatal(err)
	}
	var master, company, from, to string
	db.QueryRow(`SELECT account_number FROM ledger__account_master WHERE name = 'Alt'`).Scan(&master)
	db.QueryRow(`SELECT account_number FROM ledger__account_company WHERE id = 'old1'`).Scan(&company)
	db.QueryRow(`SELECT account_from, account_to FROM ledger__period_account_lock WHERE id = 'l1'`).Scan(&from, &to)
	if master != "SKR25-9990" || company != "SKR25-9990" || from != "SKR25-9990" || to != "SKR25-9999" {
		t.Fatalf("umgestellt: %s %s %s–%s", master, company, from, to)
	}
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	var kind string
	db.QueryRow(`SELECT account_kind FROM ledger__account_master WHERE account_number = 'SKR25-9990'`).Scan(&kind)
	if kind != "D" {
		t.Fatalf("Kontoart aus Abstimmkonto: %q", kind)
	}
}
