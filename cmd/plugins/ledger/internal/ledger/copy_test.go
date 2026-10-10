package ledger

import (
	"fmt"
	"strings"
	"testing"
)

// TestCopyToDraft: gebuchter Beleg und Vorerfassung als neue Vorerfassung –
// neues Datum und Text, Positionen mit Kontierung, Kopie bleibt änderbar.
func TestCopyToDraft(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	res, err := e.gl.Post(e.ctx, rentInvoice("SOLL-COPY"))
	if err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("1000|%d|%s", res.FiscalYear, res.DocumentNumber)
	r := e.must(entryObject, "copy", map[string]any{"id": id, "data": map[string]any{"copy_posting_date": "2026-11-01", "copy_header_text": "Sollstellung November"}})
	draft := fmt.Sprint(r["draft_id"])
	if r["positions"] != 3 || !strings.Contains(fmt.Sprint(r["message"]), "2026-11-01") {
		t.Fatalf("Kopie: %v", r)
	}
	h := e.must("JournalDraft", "get", map[string]any{"id": draft})
	if h["posting_date"] != "2026-11-01" || h["header_text"] != "Sollstellung November" || h["status"] != draftOpen || h["document_type"] != "DR" {
		t.Fatalf("Kopf: %v", h)
	}
	its := items(e.must("JournalDraftItem", "list", map[string]any{"query": map[string]any{"draft_id": draft}}))
	if len(its) != 3 || its[0]["account_number"] != "1200" || its[0]["shkzg"] != "S" || its[0]["amount"] != "1250.00" ||
		its[1]["rent_contract_id"] != "MV-0007" || its[2]["item_text"] != "BK-Vorauszahlung" {
		t.Fatalf("Positionen: %v", its)
	}
	// Kopie der Kopie, Belegdatum eigens
	r2 := e.must("JournalDraft", "copy", map[string]any{"id": draft, "data": map[string]any{"copy_posting_date": "2026-12-01", "copy_document_date": "2026-11-28"}})
	h2 := e.must("JournalDraft", "get", map[string]any{"id": fmt.Sprint(r2["draft_id"])})
	if h2["document_date"] != "2026-11-28" || h2["header_text"] != "Sollstellung November" {
		t.Fatalf("Kopie der Vorerfassung: %v", h2)
	}
}
