package opcost

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestSettlementRun: Regelwerk, Regeln, Lauf rechnen (Rechenkern als
// Attrappe), freigeben, je Mieter buchen.
func TestSettlementRun(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	expect(t, e.try(definitionObject, map[string]any{"company_code": "1000", "code": "nk", "name": "Nebenkosten", "unit_type": "Building",
		"unit_id": "XX", "tenant_contract_types": "MV"}), sdk.ErrInvalidArgument, "unbekannte Einheit")
	def := e.create(definitionObject, map[string]any{"company_code": "1000", "code": "nk", "name": "Nebenkosten", "unit_type": "Building",
		"unit_id": "GEB1", "tenant_contract_types": "mv, gm", "advance_types": "NK", "advance_account": "2800", "revenue_account": "6100", "auto_post": true})
	if def["code"] != "NK" || def["tenant_contract_types"] != "MV,GM" || def["advance_account"] != "2800" || str(def["scale"]) != "100000" {
		t.Fatalf("Regelwerk: %v", def)
	}
	r1 := e.create(ruleObject, map[string]any{"company_code": "1000", "definition": "NK", "description": "x", "step": 1, "kind": "COLLECT", "source_type": "INVOICE",
		"source_value": "wasser", "pool": "wasser", "cost_category": "WASSER"})
	if r1["rule_no"] == nil || str(r1["rule_no"]) != "10" || r1["source_value"] != "WASSER" || r1["pool"] != "WASSER" {
		t.Fatalf("Regel: %v", r1)
	}
	expect(t, e.try(ruleObject, map[string]any{"company_code": "1000", "definition": "NK", "description": "x", "step": 2, "kind": "TRANSFER", "pool": "WASSER", "to_pool": "WASSER",
		"share_pct": "30"}), sdk.ErrInvalidArgument, "Umbuchen in denselben Topf")
	expect(t, e.try(ruleObject, map[string]any{"company_code": "1000", "definition": "NK", "description": "x", "step": 3, "kind": "DISTRIBUTE", "pool": "WASSER",
		"allocation_key": "XX"}), sdk.ErrInvalidArgument, "unbekannter Schlüssel")
	if r := e.create(ruleObject, map[string]any{"company_code": "1000", "definition": "NK", "description": "x", "step": 3, "kind": "DISTRIBUTE", "pool": "WASSER",
		"allocation_key": "wfl"}); str(r["rule_no"]) != "20" {
		t.Fatalf("Regel 2: %v", r)
	}
	run := e.create(runObject, map[string]any{"company_code": "1000", "definition": "NK", "period_from": "2025-01-01", "period_to": "2025-12-31"})
	id := "1000|NK|2025-01-01"
	if run["status"] != runDraft {
		t.Fatalf("Lauf: %v", run)
	}
	_, err := e.call(runObject, "release", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Freigabe ungerechnet")
	_, err = e.call(runObject, "compute", map[string]any{"id": id})
	expect(t, err, sdk.ErrUnavailable, "ohne Rechenkern")

	e.h.billing = map[string]any{
		"summary": "Kosten 1000.00", "check_result": "0 ok, 0 Abweichungen, 1 Fehler", "failures": 1, "message": "gerechnet",
		"journal": []any{
			map[string]any{"step": 1, "rule": 10, "debit": "S:WASSER", "credit": "Q:INVOICE:WASSER", "amount": 100000 * 100000, "formula": "Rechnung 1", "source": "INV:1/1", "cost_category": "WASSER"},
			map[string]any{"step": 3, "rule": 20, "debit": "M:MV1:WASSER", "credit": "S:WASSER", "amount": 60000*100000 + 5, "formula": "× 60 / 100"},
		},
		"tenants": []any{
			map[string]any{"contract_id": "MV1", "partner_id": "P1", "rent_object_id": "W1", "usage_from": "2025-01-01", "usage_to": "2025-12-31", "costs": 60000, "advances": 72000, "lines": "Wasser 600.00"},
			map[string]any{"contract_id": "MV2", "partner_id": "P2", "usage_from": "2025-07-01", "usage_to": "2025-12-31", "costs": 20000, "advances": 0, "lines": "Wasser 200.00"},
			map[string]any{"contract_id": "MV3", "partner_id": "P3", "usage_from": "2025-01-01", "usage_to": "2025-01-31", "costs": 0, "advances": 0},
		},
	}
	e.must(runObject, "compute", map[string]any{"id": id})
	if rules := e.h.request["rules"].([]any); len(rules) != 2 || e.h.request["period_to"] != "2025-12-31" {
		t.Fatalf("Anfrage: %v", e.h.request)
	}
	if got := e.must(runObject, "get", map[string]any{"id": id}); got["summary"] != "Kosten 1000.00" || got["computed_at"] == nil {
		t.Fatalf("Lauf gerechnet: %v", got)
	}
	j := e.must(journalObject, "get", map[string]any{"id": id + "|2"})
	if j["amount"] != "600.0000005" || j["debit_account"] != "M:MV1:WASSER" {
		t.Fatalf("Journal (Cent × Faktor): %v", j)
	}
	if m := e.must(tenantObject, "get", map[string]any{"id": id + "|MV1"}); m["balance"] != "-120.00" || m["status"] != "OPEN" {
		t.Fatalf("Mieter: %v", m)
	}
	// neu rechnen ersetzt das Ergebnis
	e.must(runObject, "compute", map[string]any{"id": id})
	if l := items(e.must(journalObject, "list", map[string]any{"query": map[string]any{"definition": "NK"}})); len(l) != 2 {
		t.Fatalf("Journal nach erneutem Rechnen: %d Zeilen", len(l))
	}
	_, err = e.call(runObject, "post", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Buchen vor Freigabe")
	_, err = e.call(runObject, "release", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Freigabe mit Fehlern")
	e.h.billing["failures"] = 0
	e.must(runObject, "compute", map[string]any{"id": id})
	e.must(runObject, "release", map[string]any{"id": id})
	_, err = e.call(runObject, "compute", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Rechnen nach Freigabe")

	res := e.must(runObject, "post", map[string]any{"id": id})
	if !strings.Contains(str(res["message"]), "2 Mieter gebucht") || len(e.h.drafts) != 2 {
		t.Fatalf("Buchen: %v, %d Belege", res, len(e.h.drafts))
	}
	// MV1: Guthaben 120,00 – Soll Vorauszahlungen 720,00, Haben Erlöse 600,00, Haben Mieter 120,00
	if d := e.h.drafts[0]; d["document_type"] != "DG" || d["document_date"] != "2025-12-31" || d["reference"] != "MV1/20250101" {
		t.Fatalf("Beleg MV1: %v", d)
	}
	want := []string{"2800 S 720.00", "6100 H 600.00", "1200 H 120.00", "6100 H 200.00", "1200 S 200.00"}
	var got []string
	for _, it := range e.h.items {
		got = append(got, str(it["account_number"])+" "+str(it["shkzg"])+" "+str(it["amount"]))
		if it["rent_contract_id"] == nil {
			t.Errorf("Kontierung Mietvertrag fehlt: %v", it)
		}
		if (it["rent_object_id"] == "W1") != (len(got) <= 3) {
			t.Errorf("Kontierung Mietobjekt: %v", it)
		}
	}
	if strings.Join(got, ", ") != strings.Join(want, ", ") {
		t.Fatalf("Positionen:\n%v\nerwartet\n%v", got, want)
	}
	if m := e.must(tenantObject, "get", map[string]any{"id": id + "|MV2"}); m["status"] != "POSTED" || m["document_number"] != "1800000001" {
		t.Fatalf("Mieter gebucht: %v", m)
	}
	if m := e.must(tenantObject, "get", map[string]any{"id": id + "|MV3"}); m["status"] != "OPEN" {
		t.Fatalf("Mieter ohne Beträge: %v", m)
	}
	if got := e.must(runObject, "get", map[string]any{"id": id}); got["status"] != runPosted {
		t.Fatalf("Lauf gebucht: %v", got)
	}
}

func str(v any) string {
	switch n := v.(type) {
	case nil:
		return ""
	case float64:
		return strings.TrimSuffix(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", n), "0"), "."), ".")
	}
	return fmt.Sprintf("%v", v)
}

func items(m map[string]any) []any {
	l, _ := m["items"].([]any)
	return l
}

// TestReleaseWithErrors: Regelwerk erlaubt die Freigabe trotz Fehlern; Zuordnung
// der Quelle: Vorschlag je Quelle, Sachkonto ohne Leistungszeitraum.
func TestReleaseWithErrors(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.create(definitionObject, map[string]any{"company_code": "1000", "code": "NK", "name": "NK", "unit_type": "Building", "unit_id": "GEB1",
		"tenant_contract_types": "MV", "release_with_errors": true})
	e.h.accounts["6300"] = true
	rule := func(src, val, assignment string) map[string]any {
		return map[string]any{"company_code": "1000", "definition": "NK", "description": "x", "step": 1, "kind": "COLLECT", "source_type": src,
			"source_value": val, "pool": "P", "assignment": assignment}
	}
	if r := e.create(ruleObject, rule("LEDGER", "6300", "")); r["assignment"] != assignPosting {
		t.Fatalf("Sachkonto: Vorschlag Buchungsdatum: %v", r)
	}
	if r := e.create(ruleObject, rule("CONTRACT_SETTLEMENT", "WASSER", "")); r["assignment"] != assignService {
		t.Fatalf("Vertragsabrechnung: Vorschlag Leistungszeitraum: %v", r)
	}
	expect(t, e.try(ruleObject, rule("LEDGER", "6300", assignService)), sdk.ErrInvalidArgument, "Sachkonto mit Leistungszeitraum")
	if r := e.create(ruleObject, rule("INVOICE", "WASSER", assignDocument)); r["assignment"] != assignDocument {
		t.Fatalf("Rechnung nach Belegdatum: %v", r)
	}
	e.create(runObject, map[string]any{"company_code": "1000", "definition": "NK", "period_from": "2025-01-01", "period_to": "2025-12-31"})
	e.h.billing = map[string]any{"summary": "x", "check_result": "1 Fehler", "failures": 2}
	e.must(runObject, "compute", map[string]any{"id": "1000|NK|2025-01-01"})
	if rules := e.h.request["rules"].([]any); rules[0].(map[string]any)["assignment"] != assignPosting {
		t.Fatalf("Zuordnung an contract-billing: %v", rules[0])
	}
	if r := e.must(runObject, "get", map[string]any{"id": "1000|NK|2025-01-01"}); str(r["failures"]) != "2" {
		t.Fatalf("Fehler am Lauf: %v", r)
	}
	e.must(runObject, "release", map[string]any{"id": "1000|NK|2025-01-01"})
}
