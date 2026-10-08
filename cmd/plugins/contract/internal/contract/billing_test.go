package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
)

// Sollstellungen: contract-billing vermerkt (record), das Hauptbuch meldet
// gebuchte und verworfene Vorerfassungen (onEvent).
func TestPostingRecordAndDraftEvents(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	entry := func(cond, from string) map[string]any {
		return map[string]any{"contract_id": id, "condition_type": cond, "object_id": "", "period_from": from, "period_to": from[:8] + "28",
			"due_date": from[:8] + "03", "amount": 80000, "account_number": "SKR25-6000", "rent_object_id": "LpzBrn1WG001"}
	}
	rec := func(draft, status string, entries ...map[string]any) {
		e.must(serviceObject, "record", map[string]any{"company_code": "1000", "run_id": "r1", "draft_id": draft, "status": status,
			"currency": "EUR", "entries": entries})
	}
	rec("d1", postingDraft, entry("KM", "2026-02-01"), entry("NK", "2026-02-01"))
	rec("d2", postingDraft, entry("KM", "2026-03-01"))

	status := func() map[string]string {
		out := map[string]string{}
		rows, err := e.h.db.Query(`SELECT condition_type || ' ' || period_from, status || ' ' || COALESCE(document_number, '-') FROM contract__posting`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var k, v string
			_ = rows.Scan(&k, &v)
			out[k] = v
		}
		return out
	}
	// Liste mit Betrag in der Währung
	list := e.must(postingObject, "list", map[string]any{"query": map[string]any{"contract_id": id}})
	if items := list["items"].([]any); len(items) != 3 || items[0].(map[string]any)["amount"] != "800.00" {
		t.Fatalf("Liste: %v", list)
	}

	event := func(action, draft string, data map[string]any) {
		e.must(serviceObject, events.CallbackAction, map[string]any{"event": events.Event{Object: "JournalDraft", Action: action, EntityID: draft, Data: data}})
	}
	event("post", "d1", map[string]any{"posted_document_id": "doc-1", "document_number": "1000000007"})
	event("deactivate", "d2", nil)
	got := status()
	if got["KM 2026-02-01"] != "POSTED 1000000007" || got["NK 2026-02-01"] != "POSTED 1000000007" {
		t.Fatalf("gebucht: %v", got)
	}
	if _, ok := got["KM 2026-03-01"]; ok || len(got) != 2 {
		t.Fatalf("verworfen ist wieder offen: %v", got)
	}
	// Ein gebuchter Vermerk bleibt auch bei einem (späten) deactivate.
	event("deactivate", "d1", nil)
	if len(status()) != 2 {
		t.Fatal("gebuchte Fälligkeiten dürfen nicht verschwinden")
	}
	_, err := e.call(serviceObject, "record", map[string]any{"company_code": "1000", "draft_id": "d3", "status": "OPEN", "entries": []any{entry("KM", "2026-04-01")}})
	if err == nil {
		t.Fatal("ungültiger Status muss abgelehnt werden")
	}
}
