package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func TestBalanceSheetProposal(t *testing.T) {
	e := setup(t)
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04"})
	out := e.must(fsSchemaObject, "loadProposal", map[string]any{"data": map[string]any{"chart_of_accounts_id": "SKR04"}})
	if msg := out["message"].(string); !strings.Contains(msg, "33 Posten angelegt, 0 vorhanden") {
		t.Fatalf("Vorschlag: %s", msg)
	}
	// wiederholbar
	if msg := e.must(fsSchemaObject, "loadProposal", map[string]any{"chart": "SKR04"})["message"].(string); !strings.Contains(msg, "0 Posten angelegt, 33 vorhanden") {
		t.Fatalf("zweiter Lauf: %s", msg)
	}
	it := e.must(fsItemObject, "get", map[string]any{"id": "HGB266K|P.C.2"})
	if it["accounts"] != "3150-3249" || it["parent_code"] != "P.C" || it["side"] != "LIABILITIES" {
		t.Fatalf("Posten: %v", it)
	}
	if r := e.must(fsItemObject, "get", map[string]any{"id": "HGB266K|P.A.5"}); r["source"] != "RESULT" || r["accounts"] != nil {
		t.Fatalf("Jahresergebnis: %v", r)
	}
	// Kontenbereiche: Leerzeichen werden vereinheitlicht, falsche Angaben abgewiesen
	e.must(fsItemObject, "create", map[string]any{"data": map[string]any{"schema_code": "HGB266K", "item_code": "x1", "side": "ASSETS",
		"label": "Test", "parent_code": "b", "accounts": " 1376 ,1380 - 1389"}})
	if r := e.must(fsItemObject, "get", map[string]any{"id": "HGB266K|X1"}); r["accounts"] != "1376, 1380-1389" || r["parent_code"] != "B" {
		t.Fatalf("Konten: %v", r)
	}
	for name, data := range map[string]map[string]any{
		"Text":         {"accounts": "12xx"},
		"Länge":        {"accounts": "120-1299"},
		"Reihenfolge":  {"accounts": "1299-1200"},
		"Übergeordnet": {"parent_code": "GIBTESNICHT"},
	} {
		data["schema_code"], data["item_code"], data["side"], data["label"] = "HGB266K", "X2", "ASSETS", "Test"
		_, err := e.call(fsItemObject, "create", map[string]any{"data": data})
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
	_, err := e.call(fsSchemaObject, "loadProposal", map[string]any{"chart": "SKR99"})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Vorschlag")
}
