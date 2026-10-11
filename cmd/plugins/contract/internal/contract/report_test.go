package contract

import (
	"fmt"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Report: Objektstruktur (oberste Einheit), Vertragsarten, Konditionen mit
// Gültigkeit, Beträge je Monat abgegrenzt, Summen je oberster Einheit.
func TestContractReport(t *testing.T) {
	e := setup(t)
	e.basics()
	// Wirtschaftseinheit WE1 → Gebäude B1 → Wohnung und Stellplatz
	e.h.objects["BusinessEntity|1000|WE1"] = map[string]any{"entity_id": "WE1", "designation": "WEG Brunnenstraße"}
	e.h.objects["Building|1000|B1"] = map[string]any{"building_id": "B1", "entity_id": "WE1", "designation": "Brunnenstr. 1"}
	for _, id := range []string{"LpzBrn1WG001", "LpzBrn1SP001"} {
		e.h.objects["RentObject|1000|"+id]["building_id"] = "B1"
	}
	e.rentObject("Solo1", "UNIT", "Garage ohne Gebäude")

	// Mietvertrag Müller: Kaltmiete 850 bis Juni, ab Juli 875; Nebenkosten vierteljährlich 300
	c1 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Mietvertrag Müller",
		"partner_id": "BP1", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c1, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	cond := func(c string, k map[string]any) {
		k["company_code"], k["contract_id"], k["calc_method"], k["payment_mode"] = "1000", c, "FIXED", "IN_ADVANCE"
		e.create("ContractCondition", k)
	}
	cond(c1, map[string]any{"condition_type": "KM", "amount": "850,00", "frequency": "MONTHLY", "valid_from": "2026-01-01", "valid_to": "2026-06-30"})
	cond(c1, map[string]any{"condition_type": "KM", "amount": "875,00", "frequency": "MONTHLY", "valid_from": "2026-07-01"})
	cond(c1, map[string]any{"condition_type": "NK", "amount": "300,00", "frequency": "QUARTERLY", "valid_from": "2026-01-01"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c1})
	// Stellplatz Schulz ab 16. April (halber Monat)
	c2 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "SP", "designation": "Stellplatz Schulz",
		"partner_id": "BP2", "valid_from": "2026-04-16"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c2, "object_type": "RentObject",
		"object_id": "LpzBrn1SP001", "is_main": true, "valid_from": "2026-04-16"})
	cond(c2, map[string]any{"condition_type": "KM", "amount": "60", "frequency": "MONTHLY", "valid_from": "2026-04-16"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c2})
	// Versicherung am Gebäude (Ausgabe, jährlich 1.200)
	c3 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "VS", "designation": "Gebäudeversicherung",
		"partner_id": "INS", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c3, "object_type": "Building",
		"object_id": "B1", "is_main": true, "valid_from": "2026-01-01"})
	cond(c3, map[string]any{"condition_type": "EN", "amount": "1200", "frequency": "YEARLY", "valid_from": "2026-01-01"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c3})
	// Garage ohne Gebäude
	c4 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "SP", "designation": "Garage",
		"partner_id": "BP1", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c4, "object_type": "RentObject",
		"object_id": "Solo1", "is_main": true, "valid_from": "2026-01-01"})
	cond(c4, map[string]any{"condition_type": "KM", "amount": "40", "frequency": "MONTHLY", "valid_from": "2026-01-01"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c4})
	// Entwurf zählt nicht
	e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Entwurf", "partner_id": "BP1", "valid_from": "2026-01-01"})

	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: reportObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "run", "values": map[string]any{}}})
	var st map[string]any
	if err != nil || sdk.Decode(resp.Payload, &st) != nil {
		t.Fatalf("Maske: %v", err)
	}
	if f := fmt.Sprint(st["fields"]); !containsAll(f, "company_code:map[value:1000]", "value:VERTRAEGE|de", "period_from:map[value:", "MV – Wohnraummiete") {
		t.Errorf("Maske: %s", f)
	}

	run := func(extra map[string]any) map[string]any {
		t.Helper()
		data := map[string]any{"company_code": "1000", "period_from": "2026-01-01", "period_to": "2026-12-31", "template": "VERTRAEGE|de"}
		for k, v := range extra {
			data[k] = v
		}
		out := e.must(reportObject, "run", map[string]any{"data": data})
		if !contains(fmt.Sprint(out["download"]), "Vertragsuebersicht.pdf") {
			t.Fatalf("Download: %v", out["download"])
		}
		out["data"] = e.h.rendered["data"]
		return out
	}
	out := run(nil)
	rows := out["table"].(map[string]any)["rows"].([][]string)
	// Januar: 850 + 100 + 40 = 990 Einnahmen, 100 Ausgaben; April: + Stellplatz 15 Tage von 30 = 30
	if fmt.Sprint(rows[0]) != "[Januar 2026 3 990,00 100,00 890,00]" || fmt.Sprint(rows[3]) != "[April 2026 4 1.020,00 100,00 920,00]" ||
		fmt.Sprint(rows[6]) != "[Juli 2026 4 1.075,00 100,00 975,00]" || fmt.Sprint(rows[12]) != "[Summe Zeitraum 4 12.540,00 1.200,00 11.340,00]" {
		t.Errorf("Monate: %v", rows)
	}
	data := out["data"].(map[string]any)
	units := data["units"].([]any)
	if len(units) != 2 {
		t.Fatalf("Einheiten: %v", units)
	}
	u1 := units[0].(map[string]any)
	if u1["label"] != "Wirtschaftseinheit WE1 WEG Brunnenstraße" || u1["contracts"] != 3 || u1["period_out"] != 1200.0 {
		t.Errorf("oberste Einheit: %v %v %v", u1["label"], u1["contracts"], u1["period_out"])
	}
	types := u1["types"].([]any)
	if len(types) != 3 || types[0].(map[string]any)["code"] != "MV" || types[2].(map[string]any)["code"] != "VS" {
		t.Errorf("Arten: %v", types)
	}
	mv := types[0].(map[string]any)["contracts"].([]any)[0].(map[string]any)
	conds := mv["conditions"].([]any)
	if len(conds) != 3 || mv["object"] != "LpzBrn1WG001 Wohnung 1. OG links" {
		t.Fatalf("Vertrag: %v", mv)
	}
	k1 := conds[0].(map[string]any)
	if k1["valid_to"] != "2026-06-30" || k1["period_in"] != 5100.0 || k1["partial"] != true {
		t.Errorf("Kondition bis Juni: %v", k1)
	}
	if u2 := units[1].(map[string]any); u2["label"] != "Solo1 Garage ohne Gebäude" || u2["period_in"] != 480.0 {
		t.Errorf("Einheit ohne Gebäude: %v", u2)
	}

	// mehrere Vertragsarten
	if us := run(map[string]any{"contract_types": "SP,VS"})["data"].(map[string]any)["units"].([]any); len(us) != 2 ||
		len(us[0].(map[string]any)["types"].([]any)) != 2 {
		t.Errorf("SP,VS: %v", us)
	}
	// Gebäude: alles darunter (Wohnung, Stellplatz, Versicherung), nicht die Garage
	if d := run(map[string]any{"building_id": "B1"})["data"].(map[string]any); d["contracts"] != 3 || len(d["units"].([]any)) != 1 {
		t.Errorf("Gebäude: %v", d["contracts"])
	}
	// Mietobjekt: nur dessen Vertrag
	if d := run(map[string]any{"object_id": "LpzBrn1SP001"})["data"].(map[string]any); d["contracts"] != 1 {
		t.Errorf("Mietobjekt: %v", d["contracts"])
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
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
