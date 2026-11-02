package ledger

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestCashExpense (0.36.0): Barauslageart mit eigener Belegart und eigenem
// Intervall, Barauslage erfassen und buchen (Aufwand mit Mietobjekt an Gegenkonto),
// gebuchte Barauslage gesperrt, Buchung in geschlossener Periode lässt sie offen.
func TestCashExpense(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	// Belegart BA mit eigener laufender Nummer (Intervall außerhalb der Ledger-Intervalle)
	e.must("DocumentType", "create", map[string]any{"data": map[string]any{"code": "BA", "name": "Barauslage", "allowed_item_types": "GL"}})
	if _, err := e.h.Handle(e.ctx, sdk.Request{Object: "NumberRange", Action: "create", Payload: map[string]any{"data": map[string]any{
		"object": numberObject, "company_code": "1000", "range_key": "BA", "year": 2026, "from_number": 9000000001, "to_number": 9999999999}}}); err != nil {
		t.Fatal(err)
	}
	e.must(numberingObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "ledger": "0L", "document_type": "BA", "range_key": "BA"}})

	_, err := e.call(cashTypeObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "code": "lvb", "name": "Fahrkarte LVB",
		"account_number": "7200", "offset_account": "9999"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gegenkonto fehlt im Buchungskreis")
	ty := e.must(cashTypeObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "code": "lvb", "name": "Fahrkarte LVB",
		"phrase": "Leipziger Verkehrsbetriebe", "account_number": "7200", "offset_account": "2000", "document_type": "BA"}})
	if ty["code"] != "LVB" || ty["document_type"] != "BA" {
		t.Fatalf("Art: %v", ty)
	}

	x := e.must(cashExpenseObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "expense_type": "LVB",
		"receipt_date": "2026-10-07", "amount": "3,60", "rent_object_id": "WE-0001", "reference": "Auerbachs Keller"}})
	id := fmt.Sprint(x["id"])
	if x["status"] != cashOpen || x["amount"] != "3.60" || x["currency"] != "EUR" || x["account_number"] != "7200" || x["offset_account"] != "2000" {
		t.Fatalf("Barauslage: %v", x)
	}
	if g := e.must(cashExpenseObject, "get", map[string]any{"id": id}); g["object_type"] != "RentObject" || g["object_id"] != "WE-0001" {
		t.Fatalf("Objekt fürs DMS: %v", g)
	}
	r := e.must(cashExpenseObject, "post", map[string]any{"id": id})
	if r["document_number"] != "9000000001" || !strings.Contains(fmt.Sprint(r["message"]), "7200 an 2000") {
		t.Fatalf("Buchen: %v", r)
	}
	head := e.must(entryObject, "get", map[string]any{"id": r["entry_id"]})
	if head["document_type"] != "BA" || head["header_text"] != "Barauslage: Fahrkarte LVB" || head["reference"] != "Auerbachs Keller" || head["draft_id"] != r["draft_id"] {
		t.Fatalf("Beleg: %v", head)
	}
	lines := items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": r["entry_id"]}}))
	var debit, credit map[string]any
	for _, l := range lines {
		if l["ledger"] != "0L" {
			continue
		}
		if l["shkzg"] == "S" {
			debit = l
		} else {
			credit = l
		}
	}
	if debit == nil || credit == nil || debit["account_number"] != "7200" || debit["rent_object_id"] != "WE-0001" || credit["account_number"] != "2000" ||
		credit["rent_object_id"] != nil && credit["rent_object_id"] != "" {
		t.Fatalf("Positionen: %v", lines)
	}
	g := e.must(cashExpenseObject, "get", map[string]any{"id": id})
	if g["status"] != cashPosted || g["posted_document_id"] != r["entry_id"] {
		t.Fatalf("nach dem Buchen: %v", g)
	}
	_, err = e.call(cashExpenseObject, "update", map[string]any{"id": id, "data": map[string]any{"amount": "4"}})
	expect(t, err, sdk.ErrInvalidArgument, "gebucht ändern")
	_, err = e.call(cashExpenseObject, "post", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "erneut buchen")

	// Zweite Barauslage: nächste laufende Nummer der Belegart BA
	x2 := e.must(cashExpenseObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "expense_type": "LVB",
		"receipt_date": "2026-10-08", "amount": "3.60", "item_text": "Rückfahrt", "rent_object_id": "WE-0001"}})
	if r2 := e.must(cashExpenseObject, "post", map[string]any{"id": x2["id"]}); r2["document_number"] != "9000000002" {
		t.Fatalf("laufende Nummer: %v", r2)
	}

	// Periode 2025 nicht offen: Vorerfassung verworfen, Barauslage bleibt offen
	x3 := e.must(cashExpenseObject, "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "expense_type": "LVB",
		"receipt_date": "2025-12-30", "amount": "3.60", "rent_object_id": "WE-0001"}})
	_, err = e.call(cashExpenseObject, "post", map[string]any{"id": x3["id"]})
	if err == nil {
		t.Fatal("Buchen in geschlossener Periode")
	}
	if g := e.must(cashExpenseObject, "get", map[string]any{"id": x3["id"]}); g["status"] != cashOpen {
		t.Fatalf("offen: %v", g)
	}
	if n := len(items(e.must("JournalDraft", "list", map[string]any{"query": map[string]any{"status": draftOpen}}))); n != 0 {
		t.Fatalf("offene Vorerfassungen: %d", n)
	}
	// … mit Buchungsdatum in 2026 geht es
	e.must(cashExpenseObject, "update", map[string]any{"id": x3["id"], "data": map[string]any{"posting_date": "2026-01-02"}})
	e.must(cashExpenseObject, "post", map[string]any{"id": x3["id"]})
}

// TestAddObject (0.36.0): Mietobjekt an einen gebuchten Beleg nachtragen – nur leere
// Felder, nur die genannte Position, im Änderungsprotokoll.
func TestAddObject(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-10-05",
		"currency": "EUR", "header_text": "Barauslage: Porto"}})
	e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "1700", "shkzg": "H", "amount": "0.95"}})
	e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "2000", "shkzg": "S", "amount": "0.95"}})
	res := e.must("JournalDraft", "post", map[string]any{"id": d["id"]})
	id := res["id"]
	_, err := e.call(entryObject, addObjectAction, map[string]any{"id": id, "data": map[string]any{}})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Objekt")
	r := e.must(entryObject, addObjectAction, map[string]any{"id": id, "data": map[string]any{"new_rent_object": "WE-0002", "new_line": 2,
		"change_reason": "Fahrt zur Wohnungsübergabe"}})
	if r["changed"] != 1 {
		t.Fatalf("Nachtrag: %v", r)
	}
	for _, l := range items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": id}})) {
		want := ""
		if l["line_item_number"] == int64(2) {
			want = "WE-0002"
		}
		if got := fmt.Sprint(l["rent_object_id"]); got != want && !(want == "" && l["rent_object_id"] == nil) {
			t.Fatalf("Position %v: %v", l["line_item_number"], l["rent_object_id"])
		}
	}
	ch := items(e.must(changeObject, "list", map[string]any{"query": map[string]any{"document_number": res["document_number"]}}))
	if len(ch) != 1 || ch[0]["field_name"] != "rent_object_id" || ch[0]["reason"] != "Fahrt zur Wohnungsübergabe" {
		t.Fatalf("Protokoll: %v", ch)
	}
	if r := e.must(entryObject, addObjectAction, map[string]any{"id": id, "data": map[string]any{"new_rent_object": "WE-0003", "new_line": 2}}); r["changed"] != 0 {
		t.Fatalf("schon belegt: %v", r)
	}
}
