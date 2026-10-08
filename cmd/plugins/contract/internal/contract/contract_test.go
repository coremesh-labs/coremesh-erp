package contract

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

// TestSetupCompany: Rollen nur, wenn das Partnermodul sie kennt; Vertragsarten
// nur mit aktivierter Rolle; beim zweiten Mal nichts mehr.
func TestSetupCompany(t *testing.T) {
	e := setup(t)
	r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	// Rollen: TENANT, LANDLORD, OWNER, CREDITOR, DEBITOR, GUARANTOR (WEGADM, JANITOR, PAYER fehlen im Partnermodul).
	// Vertragsarten: alle außer VV (WEG-Verwalter fehlt).
	if r["partner_roles"] != 6 || r["contract_types"] != len(defaultTypes)-1 || r["condition_types"] != len(defaultConditions) {
		t.Fatalf("Einrichtung: %v", r)
	}
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); r["partner_roles"] != 0 || r["contract_types"] != 0 {
		t.Fatalf("zweites Mal: %v", r)
	}
	mg := e.must("ConditionType", "get", map[string]any{"id": "1000|MG"})
	if mg["claim_class"] != claimSecond || mg["clearing_order"] != int64(10) {
		t.Fatalf("Mahngebühr: %v", mg)
	}
	_, err := e.call("ContractType", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "X",
		"name": "x", "direction": dirPayable, "main_role": "WEGADM"}})
	expect(t, err, sdk.ErrInvalidArgument, "Rolle nicht aktiviert")
	_, err = e.call("ContractType", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "X",
		"name": "x", "direction": dirPayable, "main_role": "CREDITOR", "object_types": "Auto"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Objektart")
}

// TestCreateContract: interne Nummer aus dem Nummernkreis der Vertragsart,
// externe Nummer Pflicht (sonst Vertragsart-Partner-Nummer), Partner Pflicht.
func TestCreateContract(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "mv", "designation": "Mietvertrag Müller",
		"partner_id": "BP1", "valid_from": "2026-01-01"})
	if c["contract_id"] != "MV-2026-0001" || c["external_number"] != "MV-MUE-001" || c["status"] != statusDraft ||
		c["direction"] != dirReceivable || c["valid_to"] != "9999-12-31" || c["currency"] != "EUR" {
		t.Fatalf("Vertrag: %v", c)
	}
	if l := c["_labels"].(map[string]any); l["partner_id"] != "Müller" || l["contract_type"] != "Wohnraummiete" {
		t.Fatalf("Texte: %v", l)
	}
	ps := items(e.must("ContractPartner", "list", map[string]any{"query": map[string]any{"contract_id": "MV-2026-0001"}}))
	if len(ps) != 1 || ps[0]["role_code"] != "TENANT" || ps[0]["partner_id"] != "BP1" || ps[0]["valid_to"] != "9999-12-31" {
		t.Fatalf("Vertragspartner: %v", ps)
	}
	if c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Zweiter",
		"partner_id": "BP1", "valid_from": "2026-01-01"}); c["external_number"] != "MV-MUE-002" {
		t.Fatalf("zweite externe Nummer: %v", c["external_number"])
	}
	if c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "VS", "designation": "Gebäudeversicherung",
		"partner_id": "INS", "external_number": "POL-4711", "valid_from": "2026-01-01"}); c["external_number"] != "POL-4711" || c["direction"] != dirPayable {
		t.Fatalf("Versicherung: %v", c)
	}
	expect(t, e.try("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "x",
		"partner_id": "INS", "valid_from": "2026-01-01"}), sdk.ErrInvalidArgument, "Partner ohne Rolle Mieter")
	expect(t, e.try("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "x",
		"valid_from": "2026-01-01"}), sdk.ErrInvalidArgument, "ohne Partner")
	_, err := e.call("Contract", "update", map[string]any{"id": "1000|MV-2026-0001", "data": map[string]any{"external_number": ""}})
	expect(t, err, sdk.ErrInvalidArgument, "externe Nummer leeren")
	if got := nameAbbr("Ärger & Co"); got != "AER" {
		t.Fatalf("Kürzel: %s", got)
	}
	if got := nameAbbr("Li"); got != "LIX" {
		t.Fatalf("kurzes Kürzel: %s", got)
	}
}

// TestActivate: Partner, Objekt, Konditionen nötig; Hook kann ablehnen.
func TestActivate(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Leer",
		"partner_id": "BP1", "valid_from": "2026-01-01"})
	_, err := e.call("Contract", "activate", map[string]any{"id": "1000|" + c["contract_id"].(string)})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Objekt")

	id := e.rentContract()
	e.h.hookVeto = "Mieter hat offene Posten"
	_, err = e.call("Contract", "activate", map[string]any{"id": "1000|" + id})
	if err == nil || !strings.Contains(err.Error(), "offene Posten") {
		t.Fatalf("Hook-Veto: %v", err)
	}
	e.h.hookVeto = ""
	r := e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	if r["status"] != statusActive {
		t.Fatalf("aktiviert: %v", r)
	}
	if got := e.must("Contract", "get", map[string]any{"id": "1000|" + id}); got["status"] != statusActive {
		t.Fatalf("Status: %v", got["status"])
	}
	if !strings.Contains(strings.Join(e.h.hookCalls, " "), "contract.activate/commit") {
		t.Fatalf("Hook commit: %v", e.h.hookCalls)
	}
	var activated bool
	for _, ev := range e.h.events {
		if ev["object"] == "Contract" && ev["action"] == "activate" {
			activated = true
		}
	}
	if !activated {
		t.Fatalf("Event: %v", e.h.events)
	}
	_, err = e.call("Contract", "activate", map[string]any{"id": "1000|" + id})
	expect(t, err, sdk.ErrInvalidArgument, "zweimal aktivieren")
	_, err = e.call("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"valid_to": "2026-12-31"}})
	expect(t, err, sdk.ErrInvalidArgument, "Ende nur über Kündigen")
}

// TestPartnersAndObjects: Rollen, Laufzeit, Leerstand (exklusive Vertragsarten,
// auch über Vertragsobjekte), erlaubte Objektarten.
func TestPartnersAndObjects(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	p := func(role, partner, from string) error {
		return e.try("ContractPartner", map[string]any{"company_code": "1000", "contract_id": id, "role_code": role, "partner_id": partner, "valid_from": from})
	}
	if err := p("GUARANTOR", "BP2", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	expect(t, p("GUARANTOR", "BP1", "2026-01-01"), sdk.ErrInvalidArgument, "Partner ohne Rolle Bürge")
	expect(t, p("TENANT", "BP2", "2025-01-01"), sdk.ErrInvalidArgument, "vor Vertragsbeginn")

	// Zweiter Mietvertrag für dieselbe Wohnung: Leerstand verletzt.
	c2 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Schulz",
		"partner_id": "BP2", "valid_from": "2026-06-01"})["contract_id"].(string)
	obj := func(contract, typ, id string) error {
		return e.try("ContractObject", map[string]any{"company_code": "1000", "contract_id": contract, "object_type": typ,
			"object_id": id, "valid_from": "2026-06-01"})
	}
	err := obj(c2, "RentObject", "LpzBrn1WG001")
	if err == nil || !strings.Contains(err.Error(), id) {
		t.Fatalf("Leerstand: %v", err)
	}
	// Vertragsobjekt mit dem Stellplatz: über das Vertragsobjekt gesperrt.
	e.rentObject("LpzBrn1WG900", "COMPOSITE", "Wohnung + Stellplatz")
	e.h.composite = append(e.h.composite, map[string]any{"company_code": "1000", "composite_id": "LpzBrn1WG900", "object_id": "LpzBrn1SP001",
		"valid_from": "2026-01-01", "valid_to": "9999-12-31"})
	if err := obj(c2, "RentObject", "LpzBrn1WG900"); err != nil {
		t.Fatal(err)
	}
	c3 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "SP", "designation": "Stellplatz",
		"partner_id": "BP1", "valid_from": "2026-06-01"})["contract_id"].(string)
	err = obj(c3, "RentObject", "LpzBrn1SP001")
	if err == nil || !strings.Contains(err.Error(), "über LpzBrn1WG900") {
		t.Fatalf("über Vertragsobjekt: %v", err)
	}
	// Hausgeld ist nicht exklusiv: dieselbe Wohnung darf zusätzlich vorkommen.
	e.partner("OW", "Eigner", "OWNER")
	hg := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "HG", "designation": "Hausgeld",
		"partner_id": "OW", "valid_from": "2026-06-01"})["contract_id"].(string)
	if err := obj(hg, "RentObject", "LpzBrn1WG001"); err != nil {
		t.Fatalf("Hausgeld: %v", err)
	}
	e.h.objects["Building|1000|LpzBrn1"] = map[string]any{"designation": "Vorderhaus"}
	expect(t, obj(hg, "Building", "LpzBrn1"), sdk.ErrInvalidArgument, "Objektart nicht erlaubt")
	expect(t, obj(hg, "RentObject", "Gibtsnicht"), sdk.ErrInvalidArgument, "unbekanntes Objekt")
	if l := items(e.must("ContractObject", "list", map[string]any{"query": map[string]any{"contract_id": hg}}))[0]["_labels"].(map[string]any); l["object_id"] != "LpzBrn1WG001 Wohnung 1. OG links" {
		t.Fatalf("Objekttext: %v", l)
	}
}

// TestConditions: Betrag in Cent, Kontenfindung mit Standard, Berechnung je Einheit.
func TestConditions(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	conds := items(e.must("ContractCondition", "list", map[string]any{"query": map[string]any{"contract_id": id}}))
	if len(conds) != 1 || conds[0]["amount"] != "850.00" || conds[0]["contract_type"] != "MV" || conds[0]["account_number"] != nil {
		t.Fatalf("Kaltmiete: %v", conds)
	}
	var raw int64
	_ = e.h.db.QueryRow("SELECT amount FROM contract__condition WHERE contract_id = ?", id).Scan(&raw)
	if raw != 85000 {
		t.Fatalf("gespeichert in Cent: %d", raw)
	}
	// Kontenfindung: zwei Konten für NK, eines Standard.
	e.h.accounts["1000|SKR25-6200"] = "Erlöse Umlagen"
	e.h.accounts["1000|SKR25-6210"] = "Erlöse Umlagen Gewerbe"
	e.h.accounts["1000|SKR25-9999"] = "!Gesperrt"
	acc := func(nr string, def bool) error {
		return e.try("ContractAccount", map[string]any{"company_code": "1000", "contract_type": "MV", "condition_type": "NK",
			"account_number": nr, "is_default": def})
	}
	if err := acc("SKR25-6200", true); err != nil {
		t.Fatal(err)
	}
	// ohne Kontenplan-Präfix eingegeben, gespeichert wie im Hauptbuch
	if err := acc("6210", false); err != nil {
		t.Fatal(err)
	}
	var stored string
	_ = e.h.db.QueryRow("SELECT account_number FROM contract__account WHERE account_number LIKE '%6210'").Scan(&stored)
	if stored != "SKR25-6210" {
		t.Fatalf("Kontenfindung gespeichert als %q", stored)
	}
	expect(t, acc("SKR25-9999", false), sdk.ErrInvalidArgument, "gesperrtes Konto")
	expect(t, acc("SKR25-0000", false), sdk.ErrInvalidArgument, "unbekanntes Konto")
	nk := func(kv ...any) (map[string]any, error) {
		d := map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "NK", "calc_method": "FIXED", "amount": "1.250,50",
			"frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"}
		for i := 0; i < len(kv); i += 2 {
			d[kv[i].(string)] = kv[i+1]
		}
		return e.call("ContractCondition", "create", map[string]any{"data": d})
	}
	_, err := nk("account_number", "SKR25-6300")
	expect(t, err, sdk.ErrInvalidArgument, "Konto nicht in der Kontenfindung")
	got, err := nk()
	if err != nil {
		t.Fatal(err)
	}
	if got["account_number"] != "SKR25-6200" || got["amount"] != "1250.50" || got["_labels"].(map[string]any)["account_number"] != "SKR25-6200 Erlöse Umlagen" {
		t.Fatalf("Standardkonto: %v", got)
	}
	_, err = nk("valid_from", "2026-02-01", "calc_method", "PER_UNIT", "amount", "3,10")
	expect(t, err, sdk.ErrInvalidArgument, "je Einheit ohne Objekt")
	if _, err := nk("valid_from", "2026-02-01", "object_id", "LpzBrn1WG001", "calc_method", "PER_UNIT", "amount", "3,10", "measurement_type", "wfl"); err != nil {
		t.Fatal(err)
	}
	_, err = nk("valid_from", "2026-03-01", "object_id", "LpzBrn1SP001")
	expect(t, err, sdk.ErrInvalidArgument, "Objekt nicht im Vertrag")
	_, err = nk("valid_from", "2026-03-01", "amount", "12,345")
	expect(t, err, sdk.ErrInvalidArgument, "zu viele Nachkommastellen")
	_, err = nk("valid_from", "2025-01-01")
	expect(t, err, sdk.ErrInvalidArgument, "vor Vertragsbeginn")
}

// TestTerminate: Kündigungsfrist mit Stichtag und Mindestlaufzeit; Aufhebung;
// Zeitscheiben enden mit dem Vertrag.
func TestTerminate(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	e.create("ContractNoticeTerm", map[string]any{"company_code": "1000", "contract_id": id, "notice_period_months": 3,
		"notice_deadline_day": 3, "minimum_duration_months": 0, "valid_from": "2026-01-01"})
	rid := "1000|" + id
	term := func(by, end string) (map[string]any, error) {
		return e.call("Contract", "terminate", map[string]any{"id": rid, "data": map[string]any{
			"notice_received": "2026-03-10", "terminated_by": by, "termination_reason": "Umzug", "valid_to": end}})
	}
	_, err := term("PARTNER", "")
	expect(t, err, sdk.ErrInvalidArgument, "Entwurf kündigen")
	e.must("Contract", "activate", map[string]any{"id": rid})
	// Eingang am 10. (nach dem 3.): Frist zählt ab April → Ende 31.07.
	_, err = term("PARTNER", "2026-06-30")
	if err == nil || !strings.Contains(err.Error(), "2026-07-31") {
		t.Fatalf("Frist: %v", err)
	}
	r, err := term("MUTUAL", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if r["status"] != statusTerminated || r["valid_to"] != "2026-06-30" {
		t.Fatalf("Aufhebung: %v", r)
	}
	for _, o := range []string{"ContractPartner", "ContractObject", "ContractCondition"} {
		for _, it := range items(e.must(o, "list", map[string]any{"query": map[string]any{"contract_id": id, "includeHistory": "true"}})) {
			if it["valid_to"] != "2026-06-30" {
				t.Fatalf("%s endet nicht mit dem Vertrag: %v", o, it)
			}
		}
	}
	c := e.must("Contract", "get", map[string]any{"id": rid})
	if c["terminated_by"] != "MUTUAL" || c["notice_received"] != "2026-03-10" {
		t.Fatalf("Kündigung: %v", c)
	}
}

// TestEarliestEnd: Mindestlaufzeit verschiebt das früheste Ende.
func TestEarliestEnd(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	e.create("ContractNoticeTerm", map[string]any{"company_code": "1000", "contract_id": id, "notice_period_months": 3,
		"notice_deadline_day": 3, "minimum_duration_months": 24, "valid_from": "2026-01-01"})
	c, _ := e.m.contractOf(e.ctx, "1000", id)
	if end, _ := e.m.earliestEnd(e.ctx, c, "2026-02-02"); end != "2027-12-31" {
		t.Fatalf("Mindestlaufzeit: %s", end)
	}
}

// TestVisibility: Verträge nur im Buchungskreis mit Leserecht.
func TestVisibility(t *testing.T) {
	e := setup(t)
	e.basics()
	e.rentContract()
	e.h.rules["Contract.read"] = []sdk.GrantRule{{CompanyCodes: []string{"2000"}}}
	if n := len(items(e.must("Contract", "list", nil))); n != 0 {
		t.Fatalf("anderer Buchungskreis: %d", n)
	}
	e.h.rules["Contract.read"] = []sdk.GrantRule{{CompanyCodes: []string{"1000"}, Fields: map[string][]sdk.ValueRange{"contract_type": {{Low: "VS"}}}}}
	if n := len(items(e.must("Contract", "list", nil))); n != 0 {
		t.Fatalf("nur Versicherungen: %d", n)
	}
}

// TestObjectPartnersHook: realestate.partners bekommt die Partner aktiver
// Verträge am Objekt – auch über ein Vertragsobjekt; Entwürfe zählen nicht.
func TestObjectPartnersHook(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	call := func(obj string) []map[string]any {
		t.Helper()
		resp, err := e.p.Handle(e.ctx, sdk.Request{Object: partnersCallback, Action: hook.CallbackAction, Payload: map[string]any{
			"hook": partnersHook, "action": hook.PhaseModify, "data": map[string]any{"company_code": "1000", "object_id": obj,
				"lineage": []any{obj, "LpzBrn1", "LpzBrn"}, "date": "2026-06-01",
				"partners": []any{map[string]any{"role_code": "OWNER", "partner_id": "OW"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		var d partnersData
		if err := sdk.Decode(resp.Payload.(hook.Response).ReturnData, &d); err != nil {
			t.Fatal(err)
		}
		return d.Partners
	}
	if ps := call("LpzBrn1WG001"); len(ps) != 1 {
		t.Fatalf("Entwurf zählt nicht: %v", ps)
	}
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	ps := call("LpzBrn1WG001")
	if len(ps) != 2 || ps[1]["partner_name"] != "Müller" || ps[1]["role_name"] != "Mieter" || ps[1]["contract_id"] != id {
		t.Fatalf("Mieter: %v", ps)
	}
	// Stellplatz gehört zum Vertragsobjekt WG900, das vermietet ist.
	e.rentObject("LpzBrn1WG900", "COMPOSITE", "Wohnung + Stellplatz")
	e.h.composite = append(e.h.composite, map[string]any{"company_code": "1000", "composite_id": "LpzBrn1WG900", "object_id": "LpzBrn1SP001",
		"valid_from": "2026-01-01", "valid_to": "9999-12-31"})
	c2 := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Schulz",
		"partner_id": "BP2", "valid_from": "2026-01-01"})["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": c2, "object_type": "RentObject", "object_id": "LpzBrn1WG900", "valid_from": "2026-01-01"})
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": c2, "condition_type": "KM", "calc_method": "FIXED",
		"amount": "900", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	e.must("Contract", "activate", map[string]any{"id": "1000|" + c2})
	if ps := call("LpzBrn1SP001"); len(ps) != 2 || ps[1]["partner_name"] != "Schulz" || ps[1]["from_object"] != "LpzBrn1WG900" {
		t.Fatalf("über Vertragsobjekt: %v", ps)
	}
}
