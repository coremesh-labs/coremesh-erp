package dms

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestStoreGenerated: ein Plugin legt ein erzeugtes Dokument ab (DmsDocument.store) –
// ohne Ablage an der Dokumentart ein Fehler, dann die Ablage der Dokumentart, Verweise
// an den Bezügen, gleicher Inhalt liefert das vorhandene Dokument.
func TestStoreGenerated(t *testing.T) {
	e := setup(t)
	e.company()
	in := map[string]any{"company_code": "2000", "content": base64.StdEncoding.EncodeToString(pdf("auftrag")), "file_name": "Auftrag.pdf",
		"content_type": "application/pdf", "doc_type": "purchase_order", "doc_date": "2026-10-29", "title": "Auftrag AU-2026-1 Sanierung Bad",
		"partner_id": "P2", "refs": []any{map[string]any{"object": "PurchaseOrder", "key": "2000|AU-2026-1"}}}
	_, err := e.call(docObject, "store", in)
	expect(t, err, sdk.ErrInvalidArgument, "ohne Ablage")
	if n := e.one(`SELECT COUNT(*) FROM dms__document`); n != int64(0) {
		t.Fatalf("Fehler ohne Rückstand erwartet: %v Dokumente", n)
	}
	_, err = e.call(docObject, "store", map[string]any{"company_code": "2000", "content": "", "doc_type": "PURCHASE_ORDER", "doc_date": "2026-10-29"})
	expect(t, err, sdk.ErrInvalidArgument, "leeres Dokument")

	e.must(docTypeObject, "update", map[string]any{"id": "PURCHASE_ORDER", "data": map[string]any{"file_account": "4.2.0"}})
	r := e.must(docObject, "store", in)
	id, _ := r["id"].(string)
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["status"] != stFiled || doc["file_account"] != "4.2.0" || doc["doc_type"] != "PURCHASE_ORDER" || doc["received_via"] != viaGenerated ||
		!strings.Contains(r["message"].(string), "Abgelegt: ") {
		t.Fatalf("abgelegt: %v %v", r, doc)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__document_ref WHERE document_id = ? AND object = ? AND object_key = ?`, id, "PurchaseOrder", "2000|AU-2026-1"); n != int64(1) {
		t.Fatalf("Verweis: %v", n)
	}
	again := e.must(docObject, "store", in)
	if again["id"] != id || again["duplicate"] != true {
		t.Fatalf("gleicher Inhalt: %v", again)
	}
}

// TestFileAccountChoice: Ablagen zur Auswahl je Objekt (Vorschlag und Auswahl der
// Dokumentarten, nur vorhandene) und Ablage aus dem Objekt vor der Ablagefindung.
func TestFileAccountChoice(t *testing.T) {
	e := setup(t)
	e.company()
	e.must(docTypeObject, "update", map[string]any{"id": "QUOTE", "data": map[string]any{"file_account": "4.2.0", "file_accounts": "3.0.1,4.2.0, 9.9"}})
	r := e.must(docObject, "fileAccounts", map[string]any{"company_code": "2000", "object": "PurchaseQuote"})
	if fmt.Sprint(r["options"]) != "[map[label:4.2.0 "+e.accountName("4.2.0")+" value:4.2.0] map[label:3.0.1 "+e.accountName("3.0.1")+" value:3.0.1]]" ||
		r["default"] != "4.2.0" {
		t.Fatalf("Auswahl: %v", r)
	}
	st := e.must(docObject, "store", map[string]any{"company_code": "2000", "content": base64.StdEncoding.EncodeToString(pdf("angebot")),
		"file_name": "Angebot.pdf", "content_type": "application/pdf", "doc_type": "QUOTE", "doc_date": "2026-10-22",
		"refs": []any{map[string]any{"object": "PurchaseQuote", "key": "2000|AN-1"}}})
	if doc := e.must(docObject, "get", map[string]any{"id": st["id"]}); doc["file_account"] != "3.0.1" {
		t.Fatalf("Ablage aus dem Angebot: %v", doc)
	}
}

func (e *env) accountName(number string) string {
	return fmt.Sprint(e.one(`SELECT name FROM dms__account WHERE plan = 'IMMO' AND number = ?`, number))
}

// TestStoreAsset (0.26.0): Ablage über Objektart × Dokumentart des bezogenen Objekts
// (Bestandteil Steuern der Person), Objekt-Ebene im Archiv ist die Person; gleiche
// Regel an zwei Ablagen ist nicht erlaubt.
func TestStoreAsset(t *testing.T) {
	e := setup(t)
	e.company()
	acc := func(number string, data map[string]any) error {
		data["company_code"], data["number"] = "2000", number
		_, err := e.call(accountCCObject, "update", map[string]any{"id": "2000|" + number, "data": data})
		return err
	}
	if err := acc("3.0.1", map[string]any{"asset_types": "pers_steuer", "doc_types": "NOTICE"}); err != nil {
		t.Fatal(err)
	}
	if err := acc("3.0.0", map[string]any{"asset_types": "PERS_STEUER"}); err != nil {
		t.Fatal(err)
	}
	expect(t, acc("3.1.1", map[string]any{"asset_types": "PERS_STEUER", "doc_types": "NOTICE"}), sdk.ErrInvalidArgument, "gleiche Regel")
	expect(t, acc("3.1.1", map[string]any{"doc_types": "NOTICE"}), sdk.ErrInvalidArgument, "Dokumentart ohne Objektart")
	store := func(tag, docType string) map[string]any {
		r := e.must(docObject, "store", map[string]any{"company_code": "2000", "content": base64.StdEncoding.EncodeToString(pdf(tag)),
			"file_name": tag + ".pdf", "content_type": "application/pdf", "doc_type": docType, "doc_date": "2025-06-30",
			"refs": []any{map[string]any{"object": "Asset", "key": "2000|PS001"}}})
		return e.must(docObject, "get", map[string]any{"id": r["id"]})
	}
	if d := store("bescheid", "NOTICE"); d["file_account"] != "3.0.1" || !strings.Contains(fmt.Sprint(d["location"]), "P001 Heinrich Faust") {
		t.Fatalf("Bescheid: %v %v", d["file_account"], d["location"])
	}
	if d := store("brief", "OTHER"); d["file_account"] != "3.0.0" {
		t.Fatalf("Sonstiges: %v", d["file_account"])
	}
}
