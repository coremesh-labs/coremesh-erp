package procurement

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
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
	if it["line_no"] != int64(1) || it["account_number"] != "6300" || it["object_id"] != "LpzBrn1" || it["amount"] != "4850.00" {
		t.Fatalf("Position 1: %v", it)
	}
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "account_number": "6400", "amount": "-50,00",
		"object_type": "RentObject", "object_id": "LpzBrn1WG001", "allocable": true, "service_from": "2026-01-01", "service_to": "2026-12-31", "cost_center": "HV"})
	expect(t, e.try(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "amount": "1"}), sdk.ErrInvalidArgument, "ohne Konto")
	expect(t, e.try(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "amount": "1", "account_number": "6300", "contract_id": "XX"}),
		sdk.ErrInvalidArgument, "unbekannter Mietvertrag")
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
	if l := lines[0]; l["account_number"] != "6300" || l["shkzg"] != "S" || l["amount"] != "4850.00" || l["dimension_custom_1"] != "LpzBrn1" ||
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
	// Vorerfasst: nicht stornieren, sondern verwerfen (mit der Vorerfassung)
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"auto_post": false}})
	draft := mk("HW1", "R-3", "10")
	e.must(invoiceObject, "post", map[string]any{"id": draft})
	if _, err := e.call(invoiceObject, "cancel", map[string]any{"id": draft, "data": map[string]any{}}); err == nil {
		t.Fatal("nicht gebuchte Rechnung storniert")
	}
	e.must(invoiceObject, "discard", map[string]any{"id": draft})
	if c := e.h.calls[len(e.h.calls)-1]; !strings.HasPrefix(c, "deactivate ") {
		t.Fatalf("Vorerfassung verwerfen: %v", e.h.calls)
	}
	if got := e.must(invoiceObject, "get", map[string]any{"id": draft}); got["status"] != invoiceDiscarded {
		t.Fatalf("verworfen: %v", got)
	}
	if _, err := e.call(invoiceObject, "discard", map[string]any{"id": draft}); err == nil {
		t.Fatal("zweimal verworfen")
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
	if v := st.Fields["account_number"].Value; v == nil || *v != "6400" {
		t.Fatalf("Konto: %+v", st.Fields)
	}
	if v := st.Fields["allocable"].Value; v == nil || *v != "true" {
		t.Fatalf("umlagefähig: %+v", st.Fields)
	}
}

// TestServicePeriodRequired: Rechnungsart mit Pflicht zum Leistungszeitraum –
// nur für umlagefähige Kostenarten, beim Erfassen und beim Buchen geprüft.
func TestServicePeriodRequired(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "W-1",
		"invoice_date": "2026-02-10"})
	id := r["invoice_id"].(string)
	item := func(cat string, period bool) map[string]any {
		d := map[string]any{"company_code": "1000", "invoice_id": id, "cost_category": cat, "amount": "100"}
		if period {
			d["service_from"], d["service_to"] = "2025-01-01", "2025-12-31"
		}
		return d
	}
	e.create(itemObject, item("WASSER", false)) // noch keine Pflicht
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"service_period_required": true}})
	expect(t, e.try(itemObject, item("WASSER", false)), sdk.ErrInvalidArgument, "umlagefähig ohne Leistungszeitraum")
	e.create(itemObject, item("WASSER", true))
	e.create(itemObject, item("INSTAND", false)) // nicht umlagefähig: keine Pflicht
	_, err := e.call(invoiceObject, "post", map[string]any{"id": "1000|" + id})
	expect(t, err, sdk.ErrInvalidArgument, "Buchen mit alter Position ohne Leistungszeitraum")
	e.must(itemObject, "update", map[string]any{"id": "1000|" + id + "|1", "data": map[string]any{"service_from": "2025-01-01", "service_to": "2025-12-31"}})
	e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})
}

// TestBalanceTaxTemplate: Rechnung aus Vorlage eines Rahmenvertrags, Ausgleich
// Soll/Haben (Position → Rechnungsbetrag, Rechnungsbetrag → Differenzposition),
// abziehbare Vorsteuer geteilt, nicht abziehbare brutto.
func TestBalanceTaxTemplate(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	r := e.must(invoiceObject, "fromTemplate", map[string]any{"company_code": "1000", "contract_id": "RV-1", "template": "RT",
		"supplier_reference": "R-77", "invoice_date": "2026-10-01"})
	id := r["invoice_id"].(string)
	inv := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id})
	if inv["supplier_id"] != "HW1" || inv["framework_contract"] != "RV-1" || inv["invoice_amount"] != "150.00" || inv["tax_code"] != "VN19" ||
		inv["object_id"] != "LpzBrn1" {
		t.Fatalf("aus Vorlage: %v", inv)
	}
	// ältere Rechnung: brutto (Steuer aus dem Bruttobetrag geteilt)
	e.must(invoiceObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"net_amounts": false}})
	// Soll hinzu → Haben folgt.
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "account_number": "6400", "amount": "119,00", "tax_code": "V19"})
	if inv = e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); inv["invoice_amount"] != "269.00" || inv["total"] != "269.00" {
		t.Fatalf("Haben ergänzt: %v", inv)
	}
	// Tatsächlicher Betrag → Differenzposition.
	e.must(invoiceObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"invoice_amount": "300,00"}})
	l := e.must(itemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "invoice_id": id}})["items"].([]any)
	var items []map[string]any
	for _, x := range l {
		items = append(items, x.(map[string]any))
	}
	if len(items) != 3 || items[2]["amount"] != "31.00" || items[2]["item_text"] != "Differenz zum Rechnungsbetrag" || items[2]["account_number"] != "6400" {
		t.Fatalf("Differenzposition: %v", items)
	}
	if inv = e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); inv["total"] != "300.00" || inv["invoice_amount"] != "300.00" {
		t.Fatalf("ausgeglichen: %v", inv)
	}
	// Buchen: 150 (VN19 brutto) + 119 (V19: 100 netto + 19 Vorsteuer) + 31 (V19: 26.05 + 4.95), Haben 300.
	e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})
	var got []string
	for _, l := range e.h.lines["D1"] {
		got = append(got, fmt.Sprintf("%v %v %v", l["account_number"], l["shkzg"], l["amount"]))
	}
	want := "6300 S 150.00|6400 S 100.00|1406 S 19.00|6400 S 26.05|1406 S 4.95|2900 H 300.00"
	if strings.Join(got, "|") != want {
		t.Fatalf("Buchung:\n%s\nerwartet\n%s", strings.Join(got, "|"), want)
	}
	_, err := e.call(invoiceObject, "fromTemplate", map[string]any{"company_code": "1000", "contract_id": "MV-1", "template": "RT", "supplier_reference": "x"})
	expect(t, err, sdk.ErrInvalidArgument, "Vertragsart ohne Vorlagen")
}

// Entwurf zum Vertrag: Kopf mit Vertrag, Lieferant, Objekt, Steuerkennzeichen;
// je am Stichtag gültige Kondition eine Position. Der Vertrag ist an der Rechnung änderbar.
func TestContractDraft(t *testing.T) {
	e := setup(t)
	out := e.must(invoiceObject, "contractDraft", map[string]any{"company_code": "1000", "contract_id": "RV-1", "date": "2026-03-01"})
	res := out["result"].(map[string]any)
	head := res["invoice"].(map[string]any)
	if head["framework_contract"] != "RV-1" || head["supplier_id"] != "HW1" || head["tax_code"] != "VN19" || head["object_id"] != "LpzBrn1" {
		t.Fatalf("Kopf: %v", head)
	}
	items := res["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("Positionen (nur gültige): %v", items)
	}
	i0, i1 := items[0].(map[string]any), items[1].(map[string]any)
	if i0["amount"] != "1234.50" || i0["account_number"] != "6350" || i0["tax_code"] != "VN19" || i1["item_text"] != "Material" {
		t.Errorf("Positionen: %v %v", i0, i1)
	}
	if _, err := e.call(invoiceObject, "contractDraft", map[string]any{"company_code": "1000", "contract_id": "XX"}); err == nil {
		t.Error("unbekannter Vertrag")
	}
}

// TestTakeConditions: „Vertragskonditionen übernehmen …“ ersetzt die Positionen.
func TestTakeConditions(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "K-1",
		"invoice_date": "2026-03-01"})
	id := "1000|" + r["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": r["invoice_id"], "account_number": "6300", "amount": "10"})
	if _, err := e.call(invoiceObject, "takeConditions", map[string]any{"id": id, "data": map[string]any{}}); err == nil {
		t.Fatal("ohne Vertrag übernommen")
	}
	st, err := e.m.invoiceFormState(e.ctx, metamodel.FormStateRequest{Mode: "action", Action: "takeConditions", ID: id, Values: map[string]string{}})
	if err != nil || st.Fields["conditions_date"].Value == nil || *st.Fields["conditions_date"].Value != "2026-03-01" {
		t.Fatalf("Vorschlag Datum: %v %v", st, err)
	}
	e.must(invoiceObject, "takeConditions", map[string]any{"id": id, "data": map[string]any{"conditions_contract": "RV-1"}})
	got := e.must(invoiceObject, "get", map[string]any{"id": id})
	if got["framework_contract"] != "RV-1" || got["object_id"] != "LpzBrn1" {
		t.Fatalf("Kopf: %v", got)
	}
	res, _ := e.m.db.Query(e.ctx, `SELECT account_number, amount FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ? ORDER BY line_no`,
		"1000", r["invoice_id"], true)
	if len(res.Rows) != 2 || crud.Str(res.Rows[0][0]) != "6350" {
		t.Fatalf("Positionen: %v", res.Rows)
	}
}

// TestNetInvoice: Nettorechnung – Menge × Einzelpreis, nachgelagerte Positionen aus der
// eigenen Tabelle (ohne Vertragsmodul): Materialanteil 10 % (Anteil, eigenes Konto),
// Umsatzsteuer je Steuerkennzeichen (abziehbar → Vorsteuerkonto, nicht abziehbar →
// in den Aufwand verteilt), Ausgleich, Buchung; Arten aus dem Vertragsmodul.
func TestNetInvoice(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	e.create(dependentObject, map[string]any{"company_code": "1000", "code": "MAT", "name": "Materialanteil", "method": "PERCENT", "percent": "10",
		"effect": "SHARE", "base_types": "instand", "account_number": "6351", "sort_order": 10})
	// UST (Umsatzsteuer, Standard) legt die Einrichtung des Buchungskreises an
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "N-1",
		"invoice_date": "2026-03-01"})
	id := r["invoice_id"].(string)
	if r["dependent_types"] != "UST" || r["net_amounts"] != true {
		t.Fatalf("Standard: %v", r)
	}
	e.must(invoiceObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"dependent_types": "MAT,UST"}})
	it := e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "cost_category": "INSTAND", "quantity": "3", "unit": "h",
		"unit_price": "33,34", "tax_code": "V19"})
	if it["amount"] != "100.02" || it["unit"] != "H" {
		t.Fatalf("Menge × Einzelpreis: %v", it)
	}
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "account_number": "6400", "unit_price": "50", "tax_code": "VN19"})
	l := e.must(itemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "invoice_id": id}})["items"].([]any)
	got := map[string]string{}
	for _, x := range l {
		m := x.(map[string]any)
		got[fmt.Sprint(m["dependent_type"], "/", m["tax_code"])] = fmt.Sprint(m["amount"], " ", m["cost_amount"], " ", m["account_number"])
	}
	// MAT 10 % von 100,02 = 10,00 (6351, aus der Basis); USt 19 % auf 100,02 = 19,00 (1406), auf 50 = 9,50 (verteilt auf 6400)
	want := map[string]string{"<nil>/V19": "100.02 90.02 6300", "<nil>/VN19": "50.00 59.50 6400", "MAT/V19": "10.00 <nil> 6351",
		"UST/V19": "19.00 <nil> 1406", "UST/VN19": "9.50 <nil> 6400"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %q, erwartet %q (%v)", k, got[k], v, got)
		}
	}
	if inv := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); inv["total"] != "178.52" || inv["invoice_amount"] != "178.52" {
		t.Fatalf("Summe ohne Anteil: %v", inv)
	}
	e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})
	var lines []string
	var soll, haben float64
	for _, x := range e.h.lines["D1"] {
		lines = append(lines, fmt.Sprintf("%v %v %v", x["account_number"], x["shkzg"], x["amount"]))
		var v float64
		fmt.Sscan(fmt.Sprint(x["amount"]), &v)
		if x["shkzg"] == "S" {
			soll += v
		} else {
			haben += v
		}
	}
	if w := "6300 S 90.02|6351 S 10.00|1406 S 19.00|6400 S 59.50|2900 H 178.52"; strings.Join(lines, "|") != w {
		t.Fatalf("Buchung:\n%s\nerwartet\n%s", strings.Join(lines, "|"), w)
	}
	if fmt.Sprintf("%.2f", soll) != fmt.Sprintf("%.2f", haben) {
		t.Fatalf("Soll %v ≠ Haben %v", soll, haben)
	}
	// mit Vertragsmodul: dessen Arten gelten (die eigene Tabelle nicht)
	e.h.contractDeps = []any{map[string]any{"company_code": "1000", "code": "VUST", "name": "USt (Vertrag)", "method": "TAX", "tax_code": "V19",
		"effect": "SURCHARGE", "is_active": true}}
	e.h.rvDeps = []any{map[string]any{"dependent_type": "VUST"}}
	out := e.must(invoiceObject, "contractDraft", map[string]any{"company_code": "1000", "contract_id": "RV-1", "date": "2026-03-01"})
	head := out["result"].(map[string]any)["invoice"].(map[string]any)
	items := out["result"].(map[string]any)["items"].([]any)
	if head["dependent_types"] != "VUST" || items[1].(map[string]any)["quantity"] != "4" || items[1].(map[string]any)["unit_price"] != "5" {
		t.Fatalf("Entwurf: %v %v", head, items)
	}
	if _, err := e.call(invoiceObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"dependent_types": "MAT"}}); err == nil {
		t.Fatal("gebuchte Rechnung geändert")
	}
}

// TestQuotePositions: Angebot mit Positionen (Summe mit Umsatzsteuer), Rechnung
// übernimmt die Positionen des angenommenen Angebots.
func TestQuotePositions(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	q := e.create(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "Treppenhaus streichen", "quote_date": "2026-02-01"})
	id := q["quote_id"].(string)
	e.create(quoteItemObject, map[string]any{"company_code": "1000", "quote_id": id, "item_text": "Malerarbeiten", "quantity": "10", "unit": "H",
		"unit_price": "45", "tax_code": "V19", "account_number": "6300"})
	e.create(quoteItemObject, map[string]any{"company_code": "1000", "quote_id": id, "item_text": "Farbe", "unit_price": "80", "tax_code": "V19", "account_number": "6300"})
	if got := e.must(quoteObject, "get", map[string]any{"id": "1000|" + id}); got["amount"] != "630.70" { // 530 + 19 %
		t.Fatalf("Angebot: %v", got)
	}
	e.must(quoteObject, "accept", map[string]any{"id": "1000|" + id})
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "M-1",
		"invoice_date": "2026-03-01", "quote_id": id})
	e.must(invoiceObject, "takeQuote", map[string]any{"id": "1000|" + r["invoice_id"].(string)})
	if inv := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + r["invoice_id"].(string)}); inv["total"] != "630.70" {
		t.Fatalf("Rechnung aus Angebot: %v", inv)
	}
}

// TestPricingScheme: Kalkulationsschema STROM am Arbeitspreis – Netzentgelt,
// Konzessionsabgabe, Stromsteuer je kWh (Satz nach Leistungszeitraum), Zuschlag in %
// vom Netzentgelt; Satz an der Zeile überschrieben, Betrag laut Rechnung mit
// Abweichung; Umsatzsteuer auf alles.
func TestPricingScheme(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	e.create(schemeObject, map[string]any{"company_code": "1000", "code": "strom", "name": "Strom"})
	step := func(nr int, name, basis, rate string, ref int, from string) {
		e.create(schemeStepObject, map[string]any{"company_code": "1000", "scheme": "STROM", "step": nr, "name": name, "basis": basis,
			"rate": rate, "ref_step": ref, "valid_from": from, "rounding_rule": "KFM4"})
	}
	step(10, "Netzentgelt", "QUANTITY", "0,0912", 0, "2026-01-01")
	step(20, "Konzessionsabgabe", "QUANTITY", "0,0199", 0, "2026-01-01")
	step(30, "Stromsteuer", "QUANTITY", "0,0205", 0, "2025-01-01")
	step(40, "Zuschlag", "AMOUNT", "5", 10, "2026-01-01")
	if err := e.try(schemeStepObject, map[string]any{"company_code": "1000", "scheme": "STROM", "step": 50, "name": "x", "basis": "AMOUNT",
		"rate": "1", "ref_step": 60}); err == nil {
		t.Fatal("Bezug auf späteren Schritt")
	}
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "S-1",
		"invoice_date": "2026-12-31"})
	id := r["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "item_text": "Arbeitspreis", "quantity": "3500", "unit": "KWH",
		"unit_price": "0,2850", "rounding_rule": "KFM4", "tax_code": "V19", "account_number": "6400", "scheme": "STROM",
		"service_from": "2026-01-01", "service_to": "2026-12-31"})
	items := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, x := range e.must(itemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "invoice_id": id}})["items"].([]any) {
			m := x.(map[string]any)
			out[fmt.Sprint(m["item_text"])] = m
		}
		return out
	}
	got := items()
	// 3.500 × 0,2850 = 997,50; Netzentgelt 319,20; Konzession 69,65; Stromsteuer 71,75; Zuschlag 5 % von 319,20 = 15,96
	for name, want := range map[string]string{"Arbeitspreis": "997.50", "Netzentgelt": "319.20", "Konzessionsabgabe": "69.65", "Stromsteuer": "71.75", "Zuschlag": "15.96"} {
		if got[name] == nil || got[name]["amount"] != want {
			t.Errorf("%s: %v, erwartet %s", name, got[name], want)
		}
	}
	if got["Netzentgelt"]["parent_line"] != int64(1) && fmt.Sprint(got["Netzentgelt"]["parent_line"]) != "1" {
		t.Errorf("Bezug: %v", got["Netzentgelt"])
	}
	// Summe netto 1.474,06 + 19 % USt 280,07 = 1.754,13
	if inv := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); inv["total"] != "1754.13" {
		t.Fatalf("Summe: %v", inv["total"])
	}
	// Versorger rechnet die Stromsteuer mit 0,0206: Satz überschreiben, laut Rechnung eintragen
	st := got["Stromsteuer"]
	e.must(itemObject, "update", map[string]any{"id": fmt.Sprintf("1000|%s|%v", id, st["line_no"]), "data": map[string]any{"unit_price": "0,0206",
		"stated_amount": "72,10"}})
	got = items()
	if got["Stromsteuer"]["amount"] != "72.10" || got["Stromsteuer"]["deviation"] != "0.00" || got["Stromsteuer"]["price_override"] != true {
		t.Fatalf("überschrieben: %v", got["Stromsteuer"])
	}
	// Menge ändern: abhängige Zeilen folgen, der überschriebene Satz bleibt
	e.must(itemObject, "update", map[string]any{"id": "1000|" + id + "|1", "data": map[string]any{"quantity": "4000"}})
	if got = items(); got["Netzentgelt"]["amount"] != "364.80" || got["Stromsteuer"]["amount"] != "82.40" {
		t.Fatalf("Menge 4000: %v %v", got["Netzentgelt"]["amount"], got["Stromsteuer"]["amount"])
	}
	if inv := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + id}); inv["deviation"] != "-10.30" {
		t.Fatalf("Abweichung: %v", inv["deviation"])
	}
	if _, err := e.call(itemObject, "deactivate", map[string]any{"id": fmt.Sprintf("1000|%s|%v", id, got["Zuschlag"]["line_no"])}); err == nil {
		t.Fatal("abhängige Zeile einzeln entfernt")
	}
	e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})
}

// TestSchemeMatchesReadLines: gelesene Zeilen (z. B. von der KI) werden den neuen
// abhängigen Zeilen als „Betrag laut Rechnung“ zugeordnet und entfallen.
func TestSchemeMatchesReadLines(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	e.create(schemeObject, map[string]any{"company_code": "1000", "code": "STROM", "name": "Strom"})
	e.create(schemeStepObject, map[string]any{"company_code": "1000", "scheme": "STROM", "step": 10, "name": "Stromsteuer", "basis": "QUANTITY",
		"rate": "0,0205", "valid_from": "2026-01-01", "rounding_rule": "KFM4"})
	r := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "S-2",
		"invoice_date": "2026-12-31"})
	id := r["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "item_text": "Stromsteuer 2026", "unit_price": "72,00",
		"tax_code": "V19", "account_number": "6400"})
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "item_text": "Arbeitspreis", "quantity": "3500", "unit": "KWH",
		"unit_price": "0,2850", "rounding_rule": "KFM4", "tax_code": "V19", "account_number": "6400", "scheme": "STROM"})
	var texts []string
	var dev any
	for _, x := range e.must(itemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "invoice_id": id}})["items"].([]any) {
		m := x.(map[string]any)
		if m["dependent_type"] == nil {
			texts = append(texts, fmt.Sprint(m["item_text"]))
		}
		if m["item_text"] == "Stromsteuer" {
			dev = m["deviation"]
		}
	}
	if strings.Join(texts, "|") != "Arbeitspreis|Stromsteuer" || dev != "0.25" { // 72,00 laut Rechnung − 71,75 gerechnet
		t.Fatalf("zugeordnet: %v %v", texts, dev)
	}
	// andere Reihenfolge (die KI legt die Hauptposition zuerst an): nachträglich zugeordnet
	r2 := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "S-3",
		"invoice_date": "2026-12-31"})
	id2 := r2["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id2, "item_text": "Arbeitspreis", "quantity": "3500", "unit": "KWH",
		"unit_price": "0,2850", "rounding_rule": "KFM4", "tax_code": "V19", "account_number": "6400", "scheme": "STROM"})
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id2, "item_text": "Stromsteuer", "unit_price": "71,75",
		"tax_code": "V19", "account_number": "6400"})
	n := 0
	for _, x := range e.must(itemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "invoice_id": id2}})["items"].([]any) {
		if m := x.(map[string]any); m["item_text"] == "Stromsteuer" {
			n++
			if m["deviation"] != "0.00" {
				t.Fatalf("nachträglich: %v", m)
			}
		}
	}
	if n != 1 {
		t.Fatalf("Stromsteuer %d-mal", n)
	}
}
