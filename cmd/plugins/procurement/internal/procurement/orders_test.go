package procurement

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestOrderFromQuote: Angebot mit Alternativposition (zählt nicht zur Summe), Auftrag
// mit der Alternative statt der Grundposition, Texte aus den Standard-Bausteinen mit
// Platzhaltern, Kontakt als Vereinbarung, erteilen (DMS), E-Mail-Entwurf, Rechnung.
func TestOrderFromQuote(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	q := e.create(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "Sanierung Bad", "quote_date": "2026-10-22",
		"supplier_reference": "2026-0815", "object_type": "RentObject", "object_id": "LpzBrn1WG001", "file_account": "6.1.1"})
	qid := q["quote_id"].(string)
	expect(t, e.try(quoteObject, map[string]any{"company_code": "1000", "supplier_id": "HW1", "description": "x", "quote_date": "2026-10-22",
		"file_account": "9.9"}), sdk.ErrInvalidArgument, "Ablage nicht zur Auswahl")
	item := func(pos, text, price string, extra map[string]any) {
		d := map[string]any{"company_code": "1000", "quote_id": qid, "supplier_pos": pos, "item_text": text, "unit_price": price,
			"tax_code": "V19", "account_number": "6300"}
		for k, v := range extra {
			d[k] = v
		}
		e.create(quoteItemObject, d)
	}
	item("1.010", "Demontage", "100", nil)
	item("1.050", "Gleittür, Kunstglas", "50", nil)
	line := func(pos string) string { // Zeilennummer (nachgelagerte Steuerzeilen belegen eigene)
		return fmt.Sprint(e.rows(`SELECT line_no FROM procurement__quote_item WHERE quote_id = ? AND supplier_pos = ?`, qid, pos)[0][0])
	}
	// Alternative über die Positionsnummer des Lieferanten (wie beim Anlegen mit KI)
	expect(t, e.try(quoteItemObject, map[string]any{"company_code": "1000", "quote_id": qid, "item_text": "x", "unit_price": "1", "item_kind": "alt",
		"alternative_pos": "9.999"}), sdk.ErrInvalidArgument, "Alternative zu unbekannter Position")
	item("1.060", "Pendeltür, Echtglas", "60", map[string]any{"item_kind": "alt", "alternative_pos": "1.050"})
	if got := e.rows(`SELECT alternative_to FROM procurement__quote_item WHERE quote_id = ? AND supplier_pos = '1.060'`, qid); fmt.Sprint(got[0][0]) != line("1.050") {
		t.Errorf("Alternative zu: %v, erwartet Zeile %s", got, line("1.050"))
	}
	if got := e.must(quoteObject, "get", map[string]any{"id": "1000|" + qid}); got["amount"] != "178.50" { // 150 + 19 %, ohne Alternative
		t.Fatalf("Angebot ohne Alternative: %v", got)
	}
	expect(t, e.try(quoteItemObject, map[string]any{"company_code": "1000", "quote_id": qid, "item_text": "x", "unit_price": "1", "item_kind": "GIBTSNICHT"}),
		sdk.ErrInvalidArgument, "unbekannte Positionsart")

	_, err := e.call(quoteObject, "order", map[string]any{"id": "1000|" + qid, "data": map[string]any{"ord_type": "AU", "ord_lines": []any{}}})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Position")
	r := e.must(quoteObject, "order", map[string]any{"id": "1000|" + qid, "data": map[string]any{"ord_type": "AU", "ord_lines": []any{line("1.010"), line("1.060")},
		"planned_from": "2026-11-16", "planned_to": "2026-11-27"}})
	oid := r["order_id"].(string)
	if !strings.HasPrefix(oid, "AU-2026-") {
		t.Fatalf("Auftragsnummer: %v", r)
	}
	if got := e.must(quoteObject, "get", map[string]any{"id": "1000|" + qid}); got["status"] != quoteAccepted {
		t.Fatalf("Angebot nicht angenommen: %v", got)
	}
	o := e.must(orderObject, "get", map[string]any{"id": "1000|" + oid})
	if o["amount"] != "190.40" || o["file_account"] != "6.1.1" || o["status"] != orderDraft || o["supplier_id"] != "HW1" || o["planned_from"] != "2026-11-16" { // 160 + 19 %
		t.Fatalf("Auftrag mit Alternative: %v", o)
	}
	var lines []string
	for _, row := range e.rows(`SELECT supplier_pos, quote_line FROM procurement__order_item WHERE order_id = ? AND dependent_type IS NULL ORDER BY line_no`, oid) {
		lines = append(lines, fmt.Sprintf("%v/%v", row[0], row[1]))
	}
	if strings.Join(lines, " ") != "1.010/"+line("1.010")+" 1.060/"+line("1.060") {
		t.Fatalf("Positionen: %v", lines)
	}

	// Position nachträglich aus demselben Angebot in den Entwurf (ohne Preis-Eingabe);
	// schon übernommene Positionen kommen nicht doppelt
	e.must(quoteObject, "order", map[string]any{"id": "1000|" + qid, "data": map[string]any{"ord_order": oid, "ord_lines": []any{line("1.050"), line("1.060")}}})
	lines = nil
	for _, row := range e.rows(`SELECT supplier_pos FROM procurement__order_item WHERE order_id = ? AND dependent_type IS NULL AND is_active = 1 ORDER BY line_no`, oid) {
		lines = append(lines, fmt.Sprint(row[0]))
	}
	if strings.Join(lines, " ") != "1.010 1.060 1.050" {
		t.Fatalf("nachgetragen: %v", lines)
	}
	alt := e.rows(`SELECT line_no FROM procurement__order_item WHERE order_id = ? AND supplier_pos = '1.050'`, oid)[0][0]
	e.must(orderItemObject, "deactivate", map[string]any{"id": fmt.Sprintf("1000|%s|%v", oid, alt)})
	if got := e.must(orderObject, "get", map[string]any{"id": "1000|" + oid}); got["amount"] != "190.40" {
		t.Fatalf("nach Inaktivieren von 1.050: %v", got["amount"])
	}

	// Texte: Standard-Bausteine; unbekannter Platzhalter wird abgewiesen
	expect(t, e.try(orderTextObject, map[string]any{"company_code": "1000", "order_id": oid, "section": sectionAgreement, "text": "{gibtsnicht:x}"}),
		sdk.ErrInvalidArgument, "unbekannter Platzhalter")
	e.must(orderObject, "addBlocks", map[string]any{"id": "1000|" + oid, "data": map[string]any{"ord_blocks": []any{"TERMIN"}}})
	e.h.partners["HW3"] = "Hausmeister Siebel"
	e.h.mails["HW3"] = "siebel@example.org"
	e.create(orderContactObject, map[string]any{"company_code": "1000", "order_id": oid, "role": "schluessel", "partner_id": "HW3", "person": "Herr Siebel",
		"text": "Die Schlüssel sind bei {kontakt:name} ({kontakt:person}, mobil {kontakt:MOBILE}) hinterlegt."})

	e.must(orderObject, "preview", map[string]any{"id": "1000|" + oid})
	d := e.h.rendered
	if d["format"] != orderFormat || d["subject"] != "Auftragserteilung" || d["our_ref"] != oid {
		t.Fatalf("Daten: %v", d)
	}
	intro := fmt.Sprint(d["intro"])
	for _, want := range []string{"Angebot 2026-0815 vom 22.10.2026", "Vertragssumme 160,00 Euro"} {
		if !strings.Contains(intro, want) {
			t.Errorf("Einleitung ohne %q: %s", want, intro)
		}
	}
	ag := fmt.Sprint(d["agreements"])
	for _, want := range []string{"vom 16.11.2026 bis 27.11.2026", "Schlüssel sind bei Hausmeister Siebel (Herr Siebel, mobil 0171 555)", "siebel@example.org", "VOB Teile B und C"} {
		if !strings.Contains(ag, want) {
			t.Errorf("Vereinbarungen ohne %q: %s", want, ag)
		}
	}
	if i, j := strings.Index(ag, "Schlüssel"), strings.Index(ag, "VOB"); i > j { // Kontakt (510) vor VOB (900)
		t.Errorf("Reihenfolge: %s", ag)
	}
	// Summen in Euro (nicht in Cent): 160,00 netto, 30,40 Umsatzsteuer, 190,40 brutto
	if ord := d["order"].(map[string]any); fmt.Sprint(ord["net"], " ", ord["tax"], " ", ord["gross"]) != "160 30.4 190.4" {
		t.Errorf("Summen: netto %v, Steuer %v, brutto %v", ord["net"], ord["tax"], ord["gross"])
	}
	// Kopf ändern: die Summe bleibt (vorher NULL → Fehler)
	e.must(orderObject, "update", map[string]any{"id": "1000|" + oid, "data": map[string]any{"note": "Kopf geändert"}})
	if got := e.rows(`SELECT amount FROM procurement__order WHERE order_id = ?`, oid); fmt.Sprint(got[0][0]) != "19040" {
		t.Errorf("Summe nach Ändern des Kopfs: %v", got)
	}
	if it := fmt.Sprint(d["items"]); !strings.Contains(it, "unit:Stück") || strings.Index(it, "pos:1.010") > strings.Index(it, "pos:1.060") {
		t.Errorf("Einheit mit Bezeichnung: %s", it)
	}
	if obj := fmt.Sprint(d["order"].(map[string]any)["object"]); !strings.Contains(obj, "LpzBrn1WG001") {
		t.Errorf("Objekt: %s", obj)
	}

	_, err = e.call(orderObject, "mail", map[string]any{"id": "1000|" + oid})
	expect(t, err, sdk.ErrInvalidArgument, "E-Mail vor dem Erteilen")
	r = e.must(orderObject, "issue", map[string]any{"id": "1000|" + oid})
	if r["status"] != orderIssued || r["document_name"] != "AU_2026_Auftrag" {
		t.Fatalf("erteilt: %v", r)
	}
	st := e.h.stored
	if st["doc_type"] != "PURCHASE_ORDER" || st["file_account"] != "6.1.1" || st["partner_id"] != "HW1" || st["doc_date"] != fmt.Sprint(o["order_date"]) {
		t.Fatalf("Ablage: %v", st)
	}
	refs := fmt.Sprint(st["refs"])
	for _, want := range []string{"key:1000|" + oid + " object:" + orderObject, "key:1000|LpzBrn1WG001 object:RentObject", "key:1000|" + qid + " object:" + quoteObject} {
		if !strings.Contains(refs, want) {
			t.Errorf("Bezug %q fehlt: %s", want, refs)
		}
	}
	expect(t, e.try(orderTextObject, map[string]any{"company_code": "1000", "order_id": oid, "section": sectionClosing, "text": "Danke"}),
		sdk.ErrInvalidArgument, "Text am erteilten Auftrag")

	r = e.must(orderObject, "mail", map[string]any{"id": "1000|" + oid})
	var dl struct {
		Content  string `json:"content"`
		FileName string `json:"file_name"`
	}
	if err := sdk.Decode(r["download"], &dl); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(dl.Content)
	eml := string(raw)
	for _, want := range []string{"X-Unsent: 1", "To: info@schulze.example", `filename=AU_2026_Auftrag.pdf`, "multipart/mixed"} {
		if !strings.Contains(eml, want) {
			t.Errorf("E-Mail ohne %q:\n%s", want, eml)
		}
	}
	if dl.FileName != "AU_2026_Auftrag.eml" {
		t.Errorf("Dateiname: %s", dl.FileName)
	}

	// Rechnung zum Auftrag: Positionen aus dem Auftrag (mit Alternative)
	inv := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "R-1",
		"invoice_date": "2026-12-01", "quote_id": qid, "order_id": oid})
	e.must(invoiceObject, "takeQuote", map[string]any{"id": "1000|" + inv["invoice_id"].(string)})
	if got := e.must(invoiceObject, "get", map[string]any{"id": "1000|" + inv["invoice_id"].(string)}); got["total"] != "190.40" {
		t.Fatalf("Rechnung aus Auftrag: %v", got)
	}
	expect(t, e.try(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW2", "supplier_reference": "R-2",
		"invoice_date": "2026-12-01", "order_id": oid}), sdk.ErrInvalidArgument, "Auftrag eines anderen Lieferanten")
}

// TestOrderWithoutQuote: Auftrag ohne Angebot – frei erfasst, Position von Hand,
// ohne Positionen kein Erteilen.
func TestOrderWithoutQuote(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	o := e.create(orderObject, map[string]any{"company_code": "1000", "order_type": "AU", "supplier_id": "HW1", "description": "Rohrbruch beheben",
		"object_type": "RentObject", "object_id": "LpzBrn1WG001"})
	oid := o["order_id"].(string)
	if o["order_date"] == nil || o["status"] != orderDraft {
		t.Fatalf("Auftrag: %v", o)
	}
	_, err := e.call(orderObject, "issue", map[string]any{"id": "1000|" + oid})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Positionen")
	e.create(orderItemObject, map[string]any{"company_code": "1000", "order_id": oid, "item_text": "Notdienst", "unit_price": "250", "tax_code": "V19",
		"account_number": "6300"})
	e.must(orderObject, "issue", map[string]any{"id": "1000|" + oid})
	if fmt.Sprint(e.h.rendered["your_mail"]) != "<nil>" {
		t.Errorf("ohne Angebot kein „Ihr Schreiben vom“: %v", e.h.rendered["your_mail"])
	}
	e.must(orderObject, "draft", map[string]any{"id": "1000|" + oid})
	e.must(orderObject, "cancel", map[string]any{"id": "1000|" + oid})
	if got := e.must(orderObject, "get", map[string]any{"id": "1000|" + oid}); got["status"] != orderCancelled {
		t.Fatalf("storniert: %v", got)
	}
}
