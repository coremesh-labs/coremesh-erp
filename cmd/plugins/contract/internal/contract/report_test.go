package contract

import (
	"fmt"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Report: Abschnitte gleicher Konditionen, Gruppen je Mietobjekt, Summen,
// Kopfdaten des Buchungskreises; Ausgabe über Template.render.
func TestContractReport(t *testing.T) {
	e := setup(t)
	e.basics()
	// Mietvertrag Müller: Kaltmiete 850 bis Juni, ab Juli 875; Nebenkosten vierteljährlich 300
	c1 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Mietvertrag Müller",
		"partner_id": "BP1", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c1, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	for _, k := range []map[string]any{
		{"condition_type": "KM", "amount": "850,00", "frequency": "MONTHLY", "valid_from": "2026-01-01", "valid_to": "2026-06-30"},
		{"condition_type": "KM", "amount": "875,00", "frequency": "MONTHLY", "valid_from": "2026-07-01"},
		{"condition_type": "NK", "amount": "300,00", "frequency": "QUARTERLY", "valid_from": "2026-01-01"},
	} {
		k["company_code"], k["contract_id"], k["calc_method"], k["payment_mode"] = "1000", c1, "FIXED", "IN_ADVANCE"
		e.create("ContractCondition", k)
	}
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c1})
	// Stellplatz Schulz ab April
	c2 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "SP", "designation": "Stellplatz Schulz",
		"partner_id": "BP2", "valid_from": "2026-04-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c2, "object_type": "RentObject",
		"object_id": "LpzBrn1SP001", "is_main": true, "valid_from": "2026-04-01"})
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": c2, "condition_type": "KM", "calc_method": "FIXED",
		"amount": "50", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-04-01"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c2})
	// Entwurf zählt nicht
	e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Entwurf", "partner_id": "BP1", "valid_from": "2026-01-01"})

	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: reportObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "run", "values": map[string]any{}}})
	var st map[string]any
	if err != nil || sdk.Decode(resp.Payload, &st) != nil {
		t.Fatalf("Maske: %v", err)
	}
	if f := fmt.Sprint(st["fields"]); !containsAll(f, "company_code:map[value:1000]", "template:map[options:[map[label:Vertragsübersicht (PDF, de) value:VERTRAEGE|de]]",
		"period_from:map[value:", "MV – Wohnraummiete") {
		t.Errorf("Maske: %s", f)
	}

	out := e.must(reportObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "period_from": "2026-01-01",
		"period_to": "2026-12-31", "template": "VERTRAEGE|de"}})
	if fmt.Sprint(out["table"]) != "map[columns:[Zeitraum Verträge Einnahmen mtl. Ausgaben mtl.] rows:[[01.01.2026 – 31.03.2026 1 950,00 0,00] "+
		"[01.04.2026 – 30.06.2026 2 1.000,00 0,00] [01.07.2026 – 31.12.2026 2 1.025,00 0,00]]]" {
		t.Errorf("Tabelle: %v", out["table"])
	}
	if d := fmt.Sprint(out["download"]); !contains(d, "Vertragsuebersicht.pdf") {
		t.Errorf("Download: %v", out["download"])
	}
	r := e.h.rendered
	if r["template"] != "VERTRAEGE" || r["language"] != "de" || r["company_code"] != "1000" {
		t.Fatalf("render: %v", r)
	}
	data := r["data"].(map[string]any)
	company := fmt.Sprint(data["company"])
	if !containsAll(company, "name:Allianz", "street:Musterweg 1", "zip:12345", "city:Musterstadt") {
		t.Errorf("Buchungskreis: %s", company)
	}
	periods := data["periods"].([]any)
	p3 := periods[2].(map[string]any)
	groups := p3["groups"].([]any)
	if len(groups) != 2 {
		t.Fatalf("Gruppen: %v", groups)
	}
	g1 := groups[0].(map[string]any)
	if g1["label"] != "LpzBrn1SP001 Stellplatz 1" || g1["monthly_in"] != 50.0 {
		t.Errorf("Gruppe 1: %v", g1)
	}
	g2 := groups[1].(map[string]any)
	c := g2["contracts"].([]any)[0].(map[string]any)
	conds := fmt.Sprint(c["conditions"])
	if g2["monthly_in"] != 975.0 || c["partner"] != "Müller" || c["open_end"] != true ||
		!containsAll(conds, "amount:875", "frequency:monatlich", "amount:300", "frequency:vierteljährlich", "monthly_in:100") {
		t.Errorf("Gruppe 2: %v %v", g2["monthly_in"], c)
	}
	// nur eine Vertragsart, nur ein Mietobjekt
	e.must(reportObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "period_from": "2026-01-01",
		"period_to": "2026-12-31", "template": "VERTRAEGE|de", "contract_type": "SP"}})
	if ps := e.h.rendered["data"].(map[string]any)["periods"].([]any); len(ps) != 1 || ps[0].(map[string]any)["from"] != "2026-04-01" {
		t.Errorf("nur SP: %v", ps)
	}
	e.must(reportObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "period_from": "2026-01-01",
		"period_to": "2026-12-31", "template": "VERTRAEGE|de", "object_id": "LpzBrn1WG001"}})
	if ps := e.h.rendered["data"].(map[string]any)["periods"].([]any); len(ps) != 2 {
		t.Errorf("nur WG001: %d Abschnitte", len(ps))
	}
	if _, err := e.call(reportObject, "run", map[string]any{"data": map[string]any{"company_code": "1000", "period_from": "2026-12-31",
		"period_to": "2026-01-01", "template": "VERTRAEGE|de"}}); err == nil {
		t.Error("Zeitraum verkehrt")
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !contains(s, p) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(sub) == 0 || len(s) >= len(sub) && (s[:len(sub)] == sub || contains(s[1:], sub))
}
