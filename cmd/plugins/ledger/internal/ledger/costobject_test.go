package ledger

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestCostObject (0.33.0): Feldstatusgruppe COST_OBJECT verlangt Objekt oder Vertrag
// (Status „eines davon Pflicht“); die Summen- und Saldenliste filtert nach Objekt.
func TestCostObject(t *testing.T) {
	e := setup(t)
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "HAUSHALT"})
	e.must(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "HAUSHALT", "currency": "EUR", "year": 2026})
	for _, c := range items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": "5600"}})) {
		e.must("GLAccountCompany", "update", map[string]any{"id": c["id"], "data": map[string]any{"field_status_group": "COST_OBJECT"}})
	}
	draft := func(dims map[string]any) string {
		d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-03-01", "currency": "EUR", "header_text": "Geschenk"}})
		id := d["id"].(string)
		gift := map[string]any{"draft_id": id, "account_number": "5600", "shkzg": "S", "amount": "30"}
		for k, v := range dims {
			gift[k] = v
		}
		e.must("JournalDraftItem", "create", map[string]any{"data": gift})
		e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": id, "account_number": "1100", "shkzg": "H", "amount": "30"}})
		return id
	}
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-03-01", "currency": "EUR", "header_text": "x"}})
	_, err := e.call("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d["id"], "account_number": "5600", "shkzg": "S", "amount": "30"}})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Kostenträger")
	e.must("JournalDraft", "post", map[string]any{"id": draft(map[string]any{"asset_id": "AL001"})})
	e.must("JournalDraft", "post", map[string]any{"id": draft(map[string]any{"rent_contract_id": "UH-2026-0001"})})

	rows := e.must(balanceObject, "list", map[string]any{"company_code": "1000", "fiscal_year": 2026, "asset_id": "AL001"})["items"].([]balanceRow)
	if len(rows) != 1 || rows[0].Account != "5600" || rows[0].Balance != "30.00" {
		t.Fatalf("Salden je Objekt (nur die kontierte Zeile): %+v", rows)
	}
}
