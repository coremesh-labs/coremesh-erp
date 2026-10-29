package dms

import (
	"encoding/base64"
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
