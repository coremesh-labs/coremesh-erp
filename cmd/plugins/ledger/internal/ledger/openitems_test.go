package ledger

import (
	"testing"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
)

// TestOpenItems: Sollstellung Oktober (KM + NK) und Nachzahlung; die Zahlung in
// Höhe der Monatsmiete gleicht den Monatsbeleg aus, die Nachzahlung bleibt offen;
// eine Teilzahlung gleicht die Nachzahlung teilweise aus; Storno löst den Ausgleich;
// von Hand aufheben und zuordnen.
func TestOpenItems(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	a := map[string]string{"tenant": "P1", "contract": "MV-0007"}
	g := map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}
	post := func(ref, date, docType string, items ...ledgerapi.Item) string {
		t.Helper()
		req := rentInvoice(ref)
		req.PostingDate, req.DocumentType, req.Items = date, docType, items
		res, err := e.gl.Post(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		e.m.clearAfterPost(e.ctx, res.ID) // wie nach postAction (emitEntry)
		return res.DocumentNumber
	}
	// im Vorjahr bezahlte Oktobermiete im Eröffnungsbeleg (keine Zahlung): gleicht den Monat nur bei genauem Betrag aus
	post("EB", "2026-01-01", "DR", ledgerapi.Item{Account: "1200", Side: ledgerapi.Credit, Amount: "999.00", Assignments: a, Text: "bez. Vorjahr"},
		ledgerapi.Item{Account: "1700", Side: ledgerapi.Debit, Amount: "999.00"})
	post("NACH-2025", "2026-04-01", "DR", ledgerapi.Item{Account: "1200", Side: ledgerapi.Debit, Amount: "50.00", Assignments: a, Text: "Nachzahlung 2025"},
		ledgerapi.Item{Account: "6000", Side: ledgerapi.Credit, Amount: "50.00", Assignments: g, Text: "Nachzahlung 2025"})
	post("SOLL-10", "2026-10-01", "DR",
		ledgerapi.Item{Account: "1200", Side: ledgerapi.Debit, Amount: "300.00", Assignments: a, Text: "KM 10/2026"},
		ledgerapi.Item{Account: "1200", Side: ledgerapi.Debit, Amount: "100.00", Assignments: a, Text: "NK 10/2026"},
		ledgerapi.Item{Account: "6000", Side: ledgerapi.Credit, Amount: "300.00", Assignments: g, Text: "KM 10/2026"},
		ledgerapi.Item{Account: "2800", Side: ledgerapi.Credit, Amount: "100.00", Assignments: g, Text: "NK 10/2026"})
	pay := post("ZAHL-10", "2026-10-03", "DZ", ledgerapi.Item{Account: "1200", Side: ledgerapi.Credit, Amount: "400.00", Assignments: a},
		ledgerapi.Item{Account: "1700", Side: ledgerapi.Debit, Amount: "400.00"})
	open := func() map[string]map[string]any {
		r := e.must(openItemObject, "list", map[string]any{"company_code": "1000", "partner_id": "P1"})
		out := map[string]map[string]any{}
		for _, it := range r["items"].([]map[string]any) {
			out[it["item_text"].(string)] = it
		}
		return out
	}
	o := open()
	if o["KM 10/2026"]["open"] != "0.00" || o["NK 10/2026"]["open"] != "0.00" || o["Nachzahlung 2025"]["open"] != "50.00" {
		t.Fatalf("Monatsbeleg: %v", o)
	}
	if o["bez. Vorjahr"]["open"] != "999.00" {
		t.Fatalf("Eröffnung ohne passenden Betrag: %v", o["bez. Vorjahr"])
	}
	if o["KM 10/2026"]["offset_account"] != "6000" || o["NK 10/2026"]["offset_account"] != "2800" {
		t.Fatalf("Gegenkonto: %v", o)
	}
	cl := o["KM 10/2026"]["clearings"].([]map[string]any)
	if len(cl) != 1 || cl[0]["amount_minor"] != int64(30000) || cl[0]["against_posting_date"] != "2026-10-03" || cl[0]["against_payment"] != true {
		t.Fatalf("Ausgleich KM: %v", cl)
	}
	// Teilzahlung ohne passenden Beleg: älteste zuerst → Nachzahlung
	post("ZAHL-11", "2026-10-20", "DZ", ledgerapi.Item{Account: "1200", Side: ledgerapi.Credit, Amount: "30.00", Assignments: a},
		ledgerapi.Item{Account: "1700", Side: ledgerapi.Debit, Amount: "30.00"})
	if o = open(); o["Nachzahlung 2025"]["open"] != "20.00" {
		t.Fatalf("Teilzahlung: %v", o["Nachzahlung 2025"])
	}
	// Storno der Monatszahlung: Monat wieder offen, Zahlung und Storno gegeneinander
	res, err := e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: "1000|2026|" + pay, PostingDate: "2026-10-25"})
	if err != nil {
		t.Fatal(err)
	}
	e.m.clearAfterPost(e.ctx, res.ID)
	if o = open(); o["KM 10/2026"]["open"] != "300.00" || o["NK 10/2026"]["open"] != "100.00" {
		t.Fatalf("Storno: %v", o)
	}
	// von Hand: Zahlung 30 von der Nachzahlung lösen und der KM zuordnen
	var zahl11, km string
	for _, it := range e.must(openItemObject, "list", map[string]any{"company_code": "1000", "partner_id": "P1"})["items"].([]map[string]any) {
		switch {
		case it["amount"] == "-30.00":
			zahl11 = it["id"].(string)
		case it["item_text"] == "KM 10/2026":
			km = it["id"].(string)
		}
	}
	e.must(openItemObject, "autoClear", map[string]any{"company_code": "1000", "redo": true})
	if o = open(); o["Nachzahlung 2025"]["open"] != "20.00" {
		t.Fatalf("neu zuordnen: %v", o["Nachzahlung 2025"])
	}
	e.must(openItemObject, "reset", map[string]any{"ids": []any{zahl11}})
	e.must(openItemObject, "clear", map[string]any{"ids": []any{zahl11, km}})
	if o = open(); o["KM 10/2026"]["open"] != "270.00" || o["Nachzahlung 2025"]["open"] != "50.00" {
		t.Fatalf("von Hand: %v", o)
	}
}
