package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-lab/coremesh-erp/pkg/ledgerapi"
)

// eventKeys: Object.Action der gemeldeten Events in Reihenfolge.
func (e *env) eventKeys() string {
	var out []string
	for _, ev := range e.h.events {
		out = append(out, ev.Object+"."+ev.Action)
	}
	return strings.Join(out, ",")
}

// TestSystemEvents: Bewegungsdaten melden jede Änderung an den Event-Dispatcher.
func TestSystemEvents(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	e.h.events = nil

	res, err := e.gl.Post(e.ctx, rentInvoice("SOLL-EV-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.gl.Post(e.ctx, rentInvoice("SOLL-EV-1")); err != nil { // Duplikat: kein Event
		t.Fatal(err)
	}
	if e.eventKeys() != "JournalEntry.post" {
		t.Fatalf("Events: %s", e.eventKeys())
	}
	ev := e.h.events[0]
	if ev.CompanyCode != "1000" || ev.EntityID != res.ID || ev.Source != "ledger" || ev.Data["document_number"] != "1000000001" ||
		ev.Data["source_module"] != "RENT" || ev.Data["source_reference"] != "SOLL-EV-1" {
		t.Fatalf("Event: %+v", ev)
	}

	// Vorerfassung: jede Änderung, dann Buchen (Beleg und Vorerfassung).
	e.h.events = nil
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-10-05",
		"currency": "EUR", "header_text": "Einlage"}})
	i := e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "1700", "shkzg": "S", "amount": "10"}})
	e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "2000", "shkzg": "H", "amount": "10"}})
	e.must("JournalDraftItem", "update", map[string]any{"id": i["id"], "data": map[string]any{"item_text": "Bank"}})
	x := e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "1700", "shkzg": "S", "amount": "1"}})
	e.must("JournalDraftItem", "remove", map[string]any{"id": x["id"]})
	e.must("JournalDraft", "post", map[string]any{"id": d["id"]})
	want := "JournalDraft.create,JournalDraftItem.create,JournalDraftItem.create,JournalDraftItem.update,JournalDraftItem.create," +
		"JournalDraftItem.remove,JournalEntry.post,JournalDraft.post"
	if e.eventKeys() != want {
		t.Fatalf("Events Vorerfassung:\n%s\nerwartet\n%s", e.eventKeys(), want)
	}
	if ev := e.h.events[0]; ev.CompanyCode != "1000" || ev.EntityID != d["id"] {
		t.Fatalf("Event Vorerfassung: %+v", ev)
	}
	if ev := e.h.events[6]; ev.Data["draft_id"] != d["id"] || ev.Data["source_module"] != "MANUAL" {
		t.Fatalf("Event Beleg aus Vorerfassung: %+v", ev)
	}

	// Storno.
	e.h.events = nil
	rev, err := e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: res.ID, PostingDate: "2026-10-20"})
	if err != nil {
		t.Fatal(err)
	}
	if e.eventKeys() != "JournalEntry.reverse" || e.h.events[0].EntityID != rev.ID || e.h.events[0].Data["reversed_document_id"] != res.ID {
		t.Fatalf("Events Storno: %s %+v", e.eventKeys(), e.h.events)
	}
	// Fehlgeschlagene Buchung: kein Event.
	e.h.events = nil
	bad := rentInvoice("")
	bad.Items[0].Amount = "1"
	if _, err := e.gl.Post(e.ctx, bad); err == nil || len(e.h.events) != 0 {
		t.Fatalf("Fehler ohne Event erwartet: %v %s", err, e.eventKeys())
	}
}
