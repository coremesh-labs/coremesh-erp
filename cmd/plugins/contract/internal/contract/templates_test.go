package contract

import (
	"fmt"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Rahmenvertrag: Vorlagen sind Konditionen der Konditionsart RT; „Eingangsrechnung
// erfassen …“ reicht die gewählte Vorlage an die Beschaffung weiter.
func TestInvoiceFromTemplate(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "RV", "designation": "Rahmenvertrag Allianz",
		"partner_id": "INS", "valid_from": "2026-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "RT",
		"calc_method": "FIXED", "amount": "120,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01",
		"note": "Wartung"})
	ts, err := e.m.templates(e.ctx, "1000", id)
	if err != nil || len(ts) != 1 || ts[0].Value != "RT" {
		t.Fatalf("Vorlagen: %v %+v", err, ts)
	}
	key := "1000|" + id
	_, err = e.call("Contract", "invoiceFromTemplate", map[string]any{"id": key, "data": map[string]any{"inv_template": "XX", "inv_reference": "R1"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Vorlage")
	e.must("Contract", "invoiceFromTemplate", map[string]any{"id": key, "data": map[string]any{"inv_template": "RT", "inv_reference": " R-77 ", "inv_date": "2026-03-02"}})
	if f := e.h.fromTemplate; f["template"] != "RT" || f["supplier_reference"] != "R-77" || f["contract_id"] != id {
		t.Fatalf("fromTemplate: %v", f)
	}
}

// TestBillingMode: Abrechnungsweg der Vertragsart – Eingangsrechnung macht jede Kondition
// zur Vorlage (mehrere in einer Rechnung), „beides“ nur die Rechnungspositionen,
// Sollstellung lässt Rechnungspositionen nicht zu.
func TestBillingMode(t *testing.T) {
	e := setup(t)
	e.basics()
	dl := func() string {
		return e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "DL", "designation": "Hausmeister",
			"partner_id": "INS", "valid_from": "2026-01-01"})["contract_id"].(string)
	}
	cond := func(id, typ string, kv ...string) error {
		d := map[string]any{"company_code": "1000", "contract_id": id, "condition_type": typ, "calc_method": "FIXED", "amount": "100,00",
			"frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"}
		for i := 0; i < len(kv); i += 2 {
			d[kv[i]] = kv[i+1]
		}
		return e.try("ContractCondition", d)
	}
	id := dl()
	expect(t, cond(id, "EN", "condition_kind", "RP"), sdk.ErrInvalidArgument, "Rechnungsposition bei Sollstellung")
	if err := cond(id, "EN"); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.m.templates(e.ctx, "1000", id); len(ts) != 0 {
		t.Fatalf("Sollstellung ohne Vorlagen: %+v", ts)
	}
	setMode := func(mode string) error {
		_, err := e.call("ContractType", "update", map[string]any{"id": "1000|DL", "data": map[string]any{"billing_mode": mode}})
		return err
	}
	// Eingangsrechnung: alle Konditionen sind Vorlagen, Rechnung mit Entgelt und Material
	if err := setMode(billInvoice); err != nil {
		t.Fatal(err)
	}
	if ct := e.must("ContractType", "get", map[string]any{"id": "1000|DL"}); ct["template_invoices"] != true {
		t.Fatalf("aus Vorlagen folgt dem Abrechnungsweg: %v", ct)
	}
	if err := cond(id, "MA"); err != nil {
		t.Fatal(err)
	}
	ts, _ := e.m.templates(e.ctx, "1000", id)
	if len(ts) != 2 {
		t.Fatalf("Vorlagen: %+v", ts)
	}
	got := e.must("ContractCondition", "get", map[string]any{"id": "1000|" + id + "|EN||1"})
	if got["is_template"] != true {
		t.Fatalf("Kondition bei Eingangsrechnung: %v", got)
	}
	e.must("Contract", "invoiceFromTemplate", map[string]any{"id": "1000|" + id, "data": map[string]any{"inv_template": "EN,MA", "inv_reference": "26/199",
		"inv_amount": "238,01"}})
	if f := e.h.fromTemplate; f["template"] != "EN,MA" || f["amount"] != "238,01" {
		t.Fatalf("fromTemplate: %v", f)
	}
	// beides: nur die Rechnungsposition ist Vorlage; zurück auf Sollstellung geht dann nicht
	if err := setMode(billBoth); err != nil {
		t.Fatal(err)
	}
	if err := cond(id, "RW", "condition_kind", "RP"); err != nil {
		t.Fatal(err)
	}
	if ts, _ := e.m.templates(e.ctx, "1000", id); len(ts) != 1 || ts[0].Value != "RW" {
		t.Fatalf("beides: %+v", ts)
	}
	expect(t, setMode(billPosting), sdk.ErrInvalidArgument, "Sollstellung mit Rechnungspositionen")
}

// TestAccountByKind: ohne Kontenfindung keine Kondition; ein Eintrag zur Konditionsart geht
// dem ohne Konditionsart vor.
func TestAccountByKind(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "DL", "designation": "Hausmeister",
		"partner_id": "INS", "valid_from": "2026-01-01"})["contract_id"].(string)
	cond := func(typ, kind string) (map[string]any, error) {
		return e.call("ContractCondition", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_id": id,
			"condition_type": typ, "condition_kind": kind, "calc_method": "FIXED", "amount": "50,00", "frequency": "MONTHLY",
			"payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"}})
	}
	if _, err := e.h.db.Exec(`DELETE FROM contract__account WHERE contract_type = 'DL' AND condition_type = 'EN'`); err != nil {
		t.Fatal(err)
	}
	_, err := cond("EN", "")
	expect(t, err, sdk.ErrInvalidArgument, "ohne Kontenfindung")
	e.h.accounts["1000|6350"], e.h.accounts["1000|1510"] = "Hausmeister", "Geleistete Anzahlungen"
	e.create("ContractAccount", map[string]any{"company_code": "1000", "contract_type": "DL", "condition_type": "EN", "account_number": "6350", "is_default": true})
	e.create("ContractAccount", map[string]any{"company_code": "1000", "contract_type": "DL", "condition_type": "EN", "condition_kind": "AB",
		"account_number": "1510", "is_default": true})
	got, err := cond("EN", "")
	if err != nil || got["account_number"] != "6350" || got["condition_kind"] != "FO" {
		t.Fatalf("Forderung: %v %v", err, got)
	}
	if _, err := e.h.db.Exec(`DELETE FROM contract__condition WHERE contract_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	got, err = cond("EN", "AB")
	if err != nil || got["account_number"] != "1510" {
		t.Fatalf("Abschlag: %v %v", err, got)
	}
}

// TestMigrateKinds: Gegenstände vor 0.26.0 bekommen die Standard-Konditionsart aus ihren
// Merkmalen, Konditionen die ihres Gegenstands, Vertragsarten den Abrechnungsweg.
func TestMigrateKinds(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.rentContract()
	for _, sql := range []string{`UPDATE contract__condition_type SET default_kind = NULL`, `UPDATE contract__condition SET condition_kind = NULL`,
		`UPDATE contract__contract_type SET billing_mode = NULL`, `DELETE FROM contract__condition_kind`,
		`UPDATE contract__condition_type SET is_template = true WHERE code = 'EN'`} {
		if _, err := e.h.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if !e.m.migrateKinds(e.ctx) {
		t.Fatal("Migration")
	}
	kinds := map[string]string{}
	rows, err := e.h.db.Query(`SELECT code, default_kind FROM contract__condition_type WHERE code IN ('KM', 'NK', 'MG', 'ZI', 'KA', 'EN')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		kinds[k] = v
	}
	rows.Close()
	if fmt.Sprint(kinds) != "map[EN:RP KA:RA KM:FO MG:KO NK:VO ZI:ZS]" {
		t.Fatalf("Standard-Konditionsarten: %v", kinds)
	}
	var kind, mv, rv string
	_ = e.h.db.QueryRow(`SELECT condition_kind FROM contract__condition WHERE contract_id = ?`, c).Scan(&kind)
	_ = e.h.db.QueryRow(`SELECT billing_mode FROM contract__contract_type WHERE code = 'MV'`).Scan(&mv)
	_ = e.h.db.QueryRow(`SELECT billing_mode FROM contract__contract_type WHERE code = 'RV'`).Scan(&rv)
	if kind != "FO" || mv != billPosting || rv != billInvoice {
		t.Fatalf("Kondition %s, MV %s, RV %s", kind, mv, rv)
	}
}

// TestConditionPosition: derselbe Gegenstand zweimal im selben Zeitraum (Position 1 und 2,
// die nächste freie von selbst); eine Folgezeitscheibe behält ihre Position; Vorlagen mit Position.
func TestConditionPosition(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "DL", "designation": "Wartung",
		"partner_id": "INS", "valid_from": "2026-01-01"})["contract_id"].(string)
	en := func(amount, from, to string, pos ...string) map[string]any {
		d := map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "EN", "calc_method": "FIXED", "amount": amount,
			"frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": from}
		if to != "" {
			d["valid_to"] = to
		}
		if len(pos) > 0 {
			d["position"] = pos[0]
		}
		return e.create("ContractCondition", d)
	}
	a := en("100,00", "2026-01-01", "2026-06-30")
	b := en("50,00", "2026-01-01", "")
	c := en("110,00", "2026-07-01", "") // Folgezeitscheibe von Position 1
	if fmt.Sprint(a["position"], b["position"], c["position"]) != "1 2 1" {
		t.Fatalf("Positionen: %v %v %v", a["position"], b["position"], c["position"])
	}
	_, err := e.call("ContractCondition", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "EN",
		"calc_method": "FIXED", "amount": "1,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01", "position": "2"}})
	expect(t, err, sdk.ErrAlreadyExists, "Position 2 doppelt")
	if _, err := e.call("ContractType", "update", map[string]any{"id": "1000|DL", "data": map[string]any{"billing_mode": billInvoice}}); err != nil {
		t.Fatal(err)
	}
	ts, _ := e.m.templates(e.ctx, "1000", id)
	if len(ts) != 2 || ts[0].Value != "EN" || ts[1].Value != "EN||2" {
		t.Fatalf("Vorlagen: %+v", ts)
	}
}
