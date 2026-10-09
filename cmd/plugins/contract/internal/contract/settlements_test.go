package contract

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
)

// TestSettlement: WEG-Jahresabrechnung erfassen – Kopf mit Beträgen laut
// Abrechnung und Rücklage, Positionen mit Kostenart, Schlüssel und Konto,
// Vorjahr übernehmen, Vermerk der Buchung, Events aus dem Hauptbuch.
func TestSettlement(t *testing.T) {
	e := setup(t)
	e.basics()
	for _, a := range []string{"7000", "7300", "1550", "7100"} {
		e.h.accounts["1000|"+a] = "Konto " + a
	}
	e.partner("WEG", "WEG Brunnenstraße", "CREDITOR")
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "WH", "designation": "Hausgeld WG001",
		"partner_id": "WEG", "valid_from": "2025-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2025-01-01"})
	head := map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01", "period_to": "2025-12-31",
		"settlement_date": "2026-05-10", "reference": "WEG-2025-07", "stated_advances": "3.600,00", "stated_result": "-120,50",
		"reserve_opening": "4.200,00", "reserve_closing": "4.800,00"}
	s := e.create(settlementObject, head)
	if s["status"] != settlementOpen || s["stated_result"] != "-120.50" || s["posting_date"] != "2026-05-10" {
		t.Fatalf("Kopf: %v", s)
	}
	bad := func(data map[string]any, what string) {
		t.Helper()
		expect(t, e.try(settlementObject, data), sdk.ErrInvalidArgument, what)
	}
	withdrawal := map[string]any{}
	for k, v := range head {
		withdrawal[k] = v
	}
	withdrawal["period_from"], withdrawal["reserve_withdrawal"] = "2024-01-01", "500"
	bad(withdrawal, "Entnahme ohne Gegenkonto")

	item := func(data map[string]any) map[string]any {
		data["company_code"], data["contract_id"], data["period_from"] = "1000", id, "2025-01-01"
		return e.create(settlementItemObject, data)
	}
	w := item(map[string]any{"cost_category": "wasser", "total_cost": "12.400,00", "allocation_key": "mea", "key_total": "1000", "key_share": "85,32",
		"amount": "1.057,97"})
	if w["line_no"] != int64(1) || w["account_number"] != "7000" || w["cost_type"] != "ALLOCABLE" || w["key_share"] != "85.32" ||
		w["object_id"] != "LpzBrn1WG001" || w["amount"] != "1057.97" || w["allocation_key"] != "MEA" {
		t.Fatalf("Position Wasser: %v", w)
	}
	item(map[string]any{"cost_category": "VERWALT", "amount": "420,00"})
	item(map[string]any{"cost_category": "RUECKL", "settlement_group": "Haus", "amount": "600,00"})
	expect(t, e.try(settlementItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"cost_category": "OHNE", "amount": "1"}), sdk.ErrInvalidArgument, "Kostenart ohne Konto")
	expect(t, e.try(settlementItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"cost_category": "GIBTSNICHT", "amount": "1"}), sdk.ErrInvalidArgument, "unbekannte Kostenart")
	expect(t, e.try(settlementItemObject, map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"cost_category": "WASSER", "amount": "1", "key_total": "0"}), sdk.ErrInvalidArgument, "Schlüssel gesamt 0")

	// Folgejahr: Positionen übernehmen, Beträge leer
	next := map[string]any{}
	for k, v := range head {
		next[k] = v
	}
	next["period_from"], next["period_to"], next["settlement_date"] = "2026-01-01", "2026-12-31", "2027-05-10"
	e.create(settlementObject, next)
	r := e.must(settlementObject, "copyPrevious", map[string]any{"id": "1000|" + id + "|2026-01-01"})
	if toInt(r["copied"]) != 3 || !strings.Contains(r["message"].(string), "01.01.2025") {
		t.Fatalf("Vorjahr übernehmen: %v", r)
	}
	copied := e.must(settlementItemObject, "list", map[string]any{"query": map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2026-01-01"}})
	its := items(copied)
	if len(its) != 3 || its[0]["amount"] != "0.00" || its[0]["key_share"] != "85.32" || its[2]["settlement_group"] != "Haus" {
		t.Fatalf("übernommen: %v", its)
	}

	// Vermerk der Buchung durch contract-billing, dann gebucht im Hauptbuch
	e.must(serviceObject, "recordSettlement", map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"status": settlementDraft, "draft_id": "D9"})
	_, err := e.call(settlementItemObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_id": id,
		"period_from": "2025-01-01", "cost_category": "WASSER", "amount": "1"}})
	expect(t, err, sdk.ErrInvalidArgument, "Position nach dem Vorerfassen")
	ev := events.Event{Object: "JournalDraft", Action: "post", EntityID: "D9", Data: map[string]any{"posted_document_id": "1000|2026|1000000077", "document_number": "1000000077"}}
	e.must(serviceObject, events.CallbackAction, map[string]any{"event": ev})
	if got := e.must(settlementObject, "get", map[string]any{"id": "1000|" + id + "|2025-01-01"}); got["status"] != settlementPosted || got["document_number"] != "1000000077" {
		t.Fatalf("gebucht: %v", got)
	}
	_, err = e.call(serviceObject, "recordSettlement", map[string]any{"company_code": "1000", "contract_id": id, "period_from": "2025-01-01",
		"status": settlementDraft, "draft_id": "D10"})
	expect(t, err, sdk.ErrInvalidArgument, "zweimal vermerken")
}
