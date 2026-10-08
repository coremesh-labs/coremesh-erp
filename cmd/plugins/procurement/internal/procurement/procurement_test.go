package procurement

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func (e *env) setupCompany() {
	e.t.Helper()
	r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	if r["invoice_types"] != len(defaultInvoiceTypes) {
		e.t.Fatalf("Einrichtung: %v", r)
	}
}

// TestQuote: Angebot mit Nummer, annehmen/ablehnen, danach fest.
func TestQuote(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	q := e.create(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "Austausch Steigleitung",
		"quote_date": "2026-09-15", "amount": "4.850,00", "object_type": "Building", "object_id": "LpzBrn1", "cost_category": "instand"})
	if q["quote_id"] != "AN-2026-00001" || q["status"] != quoteOpen || q["amount"] != "4850.00" || q["cost_category"] != "INSTAND" {
		t.Fatalf("Angebot: %v", q)
	}
	expect(t, e.try(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "XX", "description": "x", "quote_date": "2026-09-15", "amount": "1"}),
		sdk.ErrInvalidArgument, "unbekannter Lieferant")
	expect(t, e.try(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "x", "quote_date": "2026-09-15", "amount": "1",
		"object_type": "RentObject", "object_id": "GIBTSNICHT"}), sdk.ErrInvalidArgument, "unbekanntes Objekt")
	e.must(quoteObject, "accept", map[string]any{"id": "1000|AN-2026-00001"})
	_, err := e.call(quoteObject, "reject", map[string]any{"id": "1000|AN-2026-00001"})
	expect(t, err, sdk.ErrInvalidArgument, "zweimal entscheiden")
	_, err = e.call(quoteObject, "update", map[string]any{"id": "1000|AN-2026-00001", "data": map[string]any{"description": "neu"}})
	expect(t, err, sdk.ErrInvalidArgument, "entschiedenes Angebot ändern")
}

// TestInvoicePosting: Rechnung erfassen (Doppelerfassung, Angebot, Positionen
// mit Vorschlägen der Kostenart), buchen über die Vorerfassung, Feldstatus,
// Events aus dem Hauptbuch, Storno.
func TestInvoicePosting(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	e.create(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "Steigleitung", "quote_date": "2026-09-15", "amount": "4.850,00"})
	inv := map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "R-2026-118",
		"invoice_date": "2026-10-05", "object_type": "Building", "object_id": "LpzBrn1", "quote_id": "AN-2026-00001"}
	expect(t, e.try(invoiceObject, inv), sdk.ErrInvalidArgument, "Angebot nicht angenommen")
	e.must(quoteObject, "accept", map[string]any{"id": "1000|AN-2026-00001"})
	r := e.create(invoiceObject, inv)
	id := r["invoice_id"].(string)
	if id != "ER-2026-00001" || r["posting_date"] != "2026-10-05" || r["status"] != invoiceOpen {
		t.Fatalf("Rechnung: %v", r)
	}
	expect(t, e.try(invoiceObject, inv), sdk.ErrInvalidArgument, "doppelt erfasst")

	_, err := e.call(invoiceObject, "post", map[string]any{"id": "1000|" + id})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Positionen")

	it := e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "cost_category": "INSTAND", "amount": "4.850,00",
		"item_text": "Steigleitung Vorderhaus"})
	if it["line_no"] != int64(1) || it["account_number"] != "SKR25-6300" || it["object_id"] != "LpzBrn1" || it["amount"] != "4850.00" {
		t.Fatalf("Position 1: %v", it)
	}
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "account_number": "6400", "amount": "-50,00",
		"object_type": "RentObject", "object_id": "LpzBrn1WG001", "allocable": true, "service_from": "2026-01-01", "service_to": "2026-12-31", "cost_center": "HV"})
	expect(t, e.try(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "amount": "1"}), sdk.ErrInvalidArgument, "ohne Konto")
	expect(t, e.try(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "amount": "1", "cost_category": "OHNEKONTO"}),
		sdk.ErrInvalidArgument, "Kostenart ohne Konto")
	expect(t, e.try(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "amount": "1", "cost_category": "GIBTSNICHT", "account_number": "6300"}),
		sdk.ErrInvalidArgument, "unbekannte Kostenart")
	if got := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); got["total"] != "4800.00" {
		t.Fatalf("Summe: %v", got["total"])
	}

	res := e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})
	if res["draft_id"] != "D1" || !strings.Contains(res["message"].(string), "vorerfasst") {
		t.Fatalf("Buchen: %v", res)
	}
	d := e.h.drafts["D1"]
	if d["document_type"] != "KR" || d["reference"] != "R-2026-118" || d["posting_date"] != "2026-10-05" {
		t.Fatalf("Kopf: %v", d)
	}
	lines := e.h.lines["D1"]
	if len(lines) != 3 {
		t.Fatalf("Positionen: %v", lines)
	}
	if l := lines[0]; l["account_number"] != "SKR25-6300" || l["shkzg"] != "S" || l["amount"] != "4850.00" || l["dimension_custom_1"] != "LpzBrn1" ||
		l["rent_object_id"] != nil || l["supplier_id"] != nil {
		t.Fatalf("Aufwand (Gebäude als Dimension 1, Lieferant ausgeblendet): %v", l)
	}
	if l := lines[1]; l["shkzg"] != "H" || l["amount"] != "50.00" || l["rent_object_id"] != "LpzBrn1WG001" || l["cost_center"] != "HV" {
		t.Fatalf("Gutschriftposition: %v", l)
	}
	if l := lines[2]; l["account_number"] != "2900" || l["shkzg"] != "H" || l["amount"] != "4800.00" || l["supplier_id"] != "HW1" || l["dimension_custom_1"] != nil {
		t.Fatalf("Kreditor: %v", l)
	}
	if strings.Join(e.h.calls, ",") != "create D1,simulate D1" {
		t.Fatalf("Hauptbuch: %v", e.h.calls)
	}
	_, err = e.call(itemObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "invoice_id": id, "account_number": "6300", "amount": "1"}})
	expect(t, err, sdk.ErrInvalidArgument, "Position nach dem Vorerfassen")

	// Im Hauptbuch gebucht (SystemEvent)
	ev := events.Event{Object: "JournalDraft", Action: "post", EntityID: "D1", Data: map[string]any{"posted_document_id": "1000|2026|1000000099", "document_number": "1000000099"}}
	e.must(serviceObject, events.CallbackAction, map[string]any{"event": ev})
	if got := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); got["status"] != invoicePosted || got["document_number"] != "1000000099" {
		t.Fatalf("nach Buchung: %v", got)
	}
	// Storno: Datum Pflicht, dann Storno im Hauptbuch
	_, err = e.call(invoiceObject, "cancel", map[string]any{"id": "1000|" + id, "data": map[string]any{}})
	expect(t, err, sdk.ErrInvalidArgument, "Storno ohne Datum")
	e.must(invoiceObject, "cancel", map[string]any{"id": "1000|" + id, "data": map[string]any{"reversal_date": "2026-10-20"}})
	if got := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); got["status"] != invoiceCancelled || got["reversal_document_id"] != "1000|2026|1000000100" {
		t.Fatalf("storniert: %v", got)
	}
	// Nach dem Storno darf dieselbe Rechnungsnummer neu erfasst werden
	e.create(invoiceObject, inv)
}

// TestInvoiceRules: Gutschrift-Belegart, Lieferant ohne Konto oder gesperrt,
// Prüfung im Hauptbuch scheitert (nichts geändert), automatisch buchen,
// Vorerfassung verwerfen.
func TestInvoiceRules(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	mk := func(supplier, ref string, amount string) string {
		r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": supplier, "supplier_reference": ref,
			"invoice_date": "2026-10-05"})
		e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": r["invoice_id"], "account_number": "6300", "amount": amount})
		return "1000|" + r["invoice_id"].(string)
	}
	credit := mk("HW1", "G-1", "-100,00")
	e.must(invoiceObject, "post", map[string]any{"id": credit})
	if d := e.h.drafts["D1"]; d["document_type"] != "KG" {
		t.Fatalf("Gutschrift: %v", d)
	}
	if l := e.h.lines["D1"]; l[1]["shkzg"] != "S" {
		t.Fatalf("Gutschrift Kreditor im Soll: %v", l)
	}
	// verworfen im Hauptbuch → wieder erfasst
	e.must(serviceObject, events.CallbackAction, map[string]any{"event": events.Event{Object: "JournalDraft", Action: "deactivate", EntityID: "D1"}})
	if got := e.must(invoiceObject, "get", map[string]any{"id": credit}); got["status"] != invoiceOpen || got["draft_id"] != nil {
		t.Fatalf("verworfen: %v", got)
	}

	_, err := e.call(invoiceObject, "post", map[string]any{"id": mk("HW2", "R-9", "10")})
	if err == nil || !strings.Contains(err.Error(), "Buchungssperre") {
		t.Fatalf("Buchungssperre: %v", err)
	}
	e.h.partners["HW3"] = "Ohne Konto"
	_, err = e.call(invoiceObject, "post", map[string]any{"id": mk("HW3", "R-1", "10")})
	if err == nil || !strings.Contains(err.Error(), "Buchungskreisdaten") {
		t.Fatalf("ohne Konto: %v", err)
	}
	e.h.failSim = "Periode 10/2026 gesperrt"
	failed := mk("HW1", "R-2", "10")
	_, err = e.call(invoiceObject, "post", map[string]any{"id": failed})
	expect(t, err, sdk.ErrInvalidArgument, "Prüfung scheitert")
	if got := e.must(invoiceObject, "get", map[string]any{"id": failed}); got["status"] != invoiceOpen {
		t.Fatalf("nach gescheiterter Prüfung: %v", got)
	}
	e.h.failSim = ""

	// Automatisch buchen
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"auto_post": true}})
	res := e.must(invoiceObject, "post", map[string]any{"id": failed})
	if res["document_number"] != "1000000099" {
		t.Fatalf("automatisch gebucht: %v", res)
	}
	// Vorerfasst stornieren = Vorerfassung verwerfen
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"auto_post": false}})
	draft := mk("HW1", "R-3", "10")
	e.must(invoiceObject, "post", map[string]any{"id": draft})
	e.must(invoiceObject, "cancel", map[string]any{"id": draft, "data": map[string]any{}})
	if c := e.h.calls[len(e.h.calls)-1]; !strings.HasPrefix(c, "deactivate ") {
		t.Fatalf("Vorerfassung verwerfen: %v", e.h.calls)
	}
}

// TestItemFormState: Kostenart schlägt Sachkonto und „umlagefähig“ vor.
func TestItemFormState(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	st, err := e.m.itemFormState(e.ctx, metamodel.FormStateRequest{Mode: "create",
		Values: map[string]string{"company_code": "1000", "cost_category": "WASSER"}})
	if err != nil {
		t.Fatal(err)
	}
	if v := st.Fields["account_number"].Value; v == nil || *v != "SKR25-6400" {
		t.Fatalf("Konto: %+v", st.Fields)
	}
	if v := st.Fields["allocable"].Value; v == nil || *v != "true" {
		t.Fatalf("umlagefähig: %+v", st.Fields)
	}
}
