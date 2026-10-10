package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
)

// TestPartnerBalances: Sollstellung und Teilzahlung eines Mieters – Saldo
// offen, nach der Restzahlung ausgeglichen; Tabelle der Aktion.
func TestPartnerBalances(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	post := func(ref, date string, items ...ledgerapi.Item) {
		t.Helper()
		inv := rentInvoice(ref)
		inv.PostingDate, inv.Items = date, items
		if _, err := e.gl.Post(e.ctx, inv); err != nil {
			t.Fatal(err)
		}
	}
	tenant := map[string]string{"tenant": "P1"}
	post("SOLL-1", "2026-10-01", ledgerapi.Item{Account: "1200", Side: ledgerapi.Debit, Amount: "1000.00", Assignments: tenant},
		ledgerapi.Item{Account: "6000", Side: ledgerapi.Credit, Amount: "1000.00", Assignments: map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}})
	post("ZAHL-1", "2026-10-05", ledgerapi.Item{Account: "1200", Side: ledgerapi.Credit, Amount: "600.00", Assignments: tenant},
		ledgerapi.Item{Account: "1700", Side: ledgerapi.Debit, Amount: "600.00"})
	r := e.must(partnerBalanceObject, "list", map[string]any{"company_code": "1000"})
	items := r["items"].([]map[string]any)
	if len(items) != 1 || items[0]["partner_id"] != "P1" || items[0]["balance"] != "400.00" || items[0]["debit"] != "1000.00" || items[0]["credit"] != "600.00" || items[0]["status"] != "offene Forderung" {
		t.Fatalf("offen: %v", r)
	}
	post("ZAHL-2", "2026-10-08", ledgerapi.Item{Account: "1200", Side: ledgerapi.Credit, Amount: "400.00", Assignments: tenant},
		ledgerapi.Item{Account: "1700", Side: ledgerapi.Debit, Amount: "400.00"})
	r = e.must("LedgerCompanyConfig", "partnerBalances", map[string]any{"id": "1000", "data": map[string]any{"pb_open_only": true}})
	if !strings.Contains(r["message"].(string), "0 nicht ausgeglichen") || len(r["items"].([]map[string]any)) != 0 {
		t.Fatalf("ausgeglichen: %v", r)
	}
	// Stichtag vor der Restzahlung: wieder offen
	r = e.must(partnerBalanceObject, "list", map[string]any{"company_code": "1000", "date_to": "2026-10-06"})
	if r["items"].([]map[string]any)[0]["balance"] != "400.00" {
		t.Fatalf("Stichtag: %v", r)
	}
}
