package ledger

import (
	"fmt"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Journal: Belege mit Positionen in der gewählten Reihenfolge, Zeitraum zum
// gewählten Datum, Summen je Monat und gesamt.
func TestJournal(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	if _, err := e.gl.Post(e.ctx, rentInvoice("SOLL-2026-10-MV-0007")); err != nil {
		t.Fatal(err)
	}
	late := rentInvoice("SOLL-2026-09-MV-0007")
	late.PostingDate, late.DocumentDate, late.HeaderText = "2026-09-30", "2026-10-05", "Sollstellung September"
	if _, err := e.gl.Post(e.ctx, late); err != nil {
		t.Fatal(err)
	}
	run := func(order, from, to string) map[string]any {
		t.Helper()
		out := e.must(journalObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "ledger": "0L",
			"period_from": from, "period_to": to, "order": order, "template": "JOURNAL|de"}})
		if fmt.Sprint(out["download"]) == "" || e.h.rendered["template"] != "JOURNAL" {
			t.Fatalf("Ausgabe: %v %v", out, e.h.rendered)
		}
		return e.h.rendered["data"].(map[string]any)
	}
	texts := func(d map[string]any) []string {
		var out []string
		for _, g := range d["groups"].([]any) {
			for _, en := range g.(map[string]any)["entries"].([]any) {
				out = append(out, fmt.Sprint(en.(map[string]any)["text"]))
			}
		}
		return out
	}
	d := run("POSTING_DATE", "2026-01-01", "2026-12-31")
	if got := texts(d); fmt.Sprint(got) != "[Sollstellung September Sollstellung Miete Oktober]" {
		t.Errorf("Buchungsdatum: %v", got)
	}
	if d["debit"] != 2500.0 || d["credit"] != 2500.0 || d["documents"] != 2 || len(d["groups"].([]any)) != 2 {
		t.Errorf("Summen: %v %v %v %d", d["debit"], d["credit"], d["documents"], len(d["groups"].([]any)))
	}
	g := d["groups"].([]any)[0].(map[string]any)
	en := g["entries"].([]any)[0].(map[string]any)
	l := en["lines"].([]any)[0].(map[string]any)
	if g["label"] != "September 2026" || l["account"] != "1200" || l["debit"] != 1250.0 || l["credit"] != 0.0 {
		t.Errorf("Gruppe/Position: %v %v", g["label"], l)
	}
	// Belegdatum: beide im Oktober, der späte zuletzt; Zeitraum zum Belegdatum
	d = run("DOCUMENT_DATE", "2026-10-01", "2026-10-31")
	if got := texts(d); fmt.Sprint(got) != "[Sollstellung Miete Oktober Sollstellung September]" || len(d["groups"].([]any)) != 1 {
		t.Errorf("Belegdatum: %v", got)
	}
	// Buchungsdatum Oktober: nur einer
	if got := texts(run("POSTING_DATE", "2026-10-01", "2026-10-31")); len(got) != 1 {
		t.Errorf("Zeitraum: %v", got)
	}
	// Erfassung und Belegnummer: beide, Belegnummer ohne Monatsgruppen
	if got := texts(run("ENTRY", "2000-01-01", "2099-12-31")); fmt.Sprint(got) != "[Sollstellung Miete Oktober Sollstellung September]" {
		t.Errorf("Erfassung: %v", got)
	}
	if d := run("NUMBER", "2026-01-01", "2026-12-31"); len(d["groups"].([]any)) != 1 || d["groups"].([]any)[0].(map[string]any)["label"] != "alle Belege" {
		t.Errorf("Belegnummer: %v", d["groups"])
	}
	// Maske: Vorlagen, Ledger, Reihenfolge
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: journalObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "run", "values": map[string]any{}}})
	var st map[string]any
	if err != nil || sdk.Decode(resp.Payload, &st) != nil {
		t.Fatal(err)
	}
	if f := fmt.Sprint(st["fields"]); !containsStr(f, "ledger:map[value:0L]") || !containsStr(f, "value:JOURNAL|de") || !containsStr(f, "order:map[value:POSTING_DATE]") {
		t.Errorf("Maske: %s", f)
	}
	if _, err := e.call(journalObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "ledger": "0L",
		"period_from": "2026-01-01", "period_to": "2026-12-31", "order": "XX", "template": "JOURNAL|de"}}); err == nil {
		t.Error("unbekannte Reihenfolge")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
