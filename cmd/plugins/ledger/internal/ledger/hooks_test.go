package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

// TestPostingHook: modify ergänzt Positionstexte, check kann abbrechen, commit
// erhält den gebuchten Beleg; Meldungen kommen im Ergebnis zurück.
func TestPostingHook(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	var calls []string
	var committed PostingHookData
	e.h.hook = func(req hook.Request) hook.Result {
		var d PostingHookData
		if err := req.DecodeData(&d); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, req.Action)
		switch req.Action {
		case hook.PhaseModify:
			for i := range d.Request.Items {
				if d.Request.Items[i].Text == "" {
					d.Request.Items[i].Text = "von Hook: " + d.Request.HeaderText
				}
			}
			return hook.Result{Data: d}
		case hook.PhaseCheck:
			if d.FiscalYear != 2026 || d.Ledger == "" {
				t.Errorf("check ohne Periode/Ledger: %+v", d)
			}
			if d.Request.Reference == "VERBOTEN" {
				return hook.Result{Data: req.Data, Messages: []hook.Message{{Type: "E", ID: "T-1", Text: "Referenz gesperrt", Source: "test/Check"}}}
			}
			return hook.Result{Data: req.Data, Messages: []hook.Message{{Type: "W", ID: "T-2", Text: "Bitte Beleg ablegen", Source: "test/Check"}}}
		case hook.PhaseCommit:
			committed = d
			return hook.Result{Data: req.Data, Messages: []hook.Message{{Type: "S", ID: "T-3", Text: "Folgebeleg angelegt"}}}
		}
		return hook.Result{Data: req.Data}
	}

	res, err := e.gl.Post(e.ctx, rentInvoice("H-1"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "modify,check,commit" {
		t.Fatalf("Phasen: %v", calls)
	}
	if committed.Result == nil || committed.Result.DocumentNumber != res.DocumentNumber || !strings.HasPrefix(committed.Request.Items[0].Text, "von Hook") {
		t.Fatalf("commit: %+v", committed)
	}
	if len(res.Messages) != 2 || res.Messages[0].ID != "T-2" || res.Messages[1].ID != "T-3" {
		t.Fatalf("Meldungen: %+v", res.Messages)
	}
	// modify wirkt auf den geschriebenen Beleg.
	for _, it := range items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res.ID}})) {
		if !strings.HasPrefix(it["item_text"].(string), "von Hook") && it["item_text"] != "BK-Vorauszahlung" {
			t.Fatalf("Positionstext: %v", it["item_text"])
		}
	}

	// check mit E: abgebrochen, nichts gebucht, kein commit.
	calls = nil
	bad := rentInvoice("H-2")
	bad.Reference = "VERBOTEN"
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "check E")
	if !strings.Contains(err.Error(), "T-1 Referenz gesperrt (test/Check)") || strings.Join(calls, ",") != "modify,check" {
		t.Fatalf("Abbruch: %v %v", err, calls)
	}
	if n := len(items(e.must("JournalEntry", "list", nil))); n != 1 {
		t.Fatalf("%d Belege", n)
	}

	// Simulation: modify und check, kein commit; Hinweis in der Meldung.
	calls = nil
	if err := e.gl.Simulate(e.ctx, rentInvoice("H-3")); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "modify,check" {
		t.Fatalf("Simulation: %v", calls)
	}
}
