package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestTransfer (0.52.0): „Überweisung / Dauerauftrag …“ an einem zu zahlenden Vertrag –
// Betrag je Rhythmus am Stichtag, erste Ausführung, Folgebetrag, Verwendungszweck.
func TestTransfer(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "VS", "designation": "Gebäudeversicherung",
		"partner_id": "INS", "external_number": "POL-4711", "customer_number": "K 99", "valid_from": "2026-01-01"})["contract_id"].(string)
	cond := func(typ, amount, freq, from, to string, extra map[string]any) {
		d := map[string]any{"company_code": "1000", "contract_id": id, "condition_type": typ, "calc_method": "FIXED", "amount": amount,
			"frequency": freq, "payment_mode": "IN_ADVANCE", "valid_from": from, "valid_to": to, "due_day": 15}
		for k, v := range extra {
			d[k] = v
		}
		e.create("ContractCondition", d)
	}
	cond("EN", "120,00", "MONTHLY", "2026-01-01", "2026-12-31", nil)
	cond("EN", "130,00", "MONTHLY", "2027-01-01", "9999-12-31", nil)
	cond("MA", "50,00", "YEARLY", "2026-01-01", "9999-12-31", map[string]any{"due_month": 2})

	r := e.must("Contract", transferAction, map[string]any{"id": "1000|" + id, "data": map[string]any{"transfer_date": "2026-11-20"}})
	got := map[string]string{}
	for _, f := range r["copy"].([]map[string]any) {
		got[fmt.Sprint(f["label"])] = fmt.Sprint(f["value"])
	}
	for k, want := range map[string]string{
		"An (Empfänger)":             "Allianz Versicherungs-AG",
		"IBAN":                       "DE89 3704 0044 0532 0130 00",
		"Betrag":                     "120,00 EUR",
		"Verwendungszweck":           "POL-4711 Kunde K 99",
		"Rhythmus":                   "monatlich",
		"Ausführung":                 "am 15. jedes Monats",
		"Erste Ausführung":           "15.12.2026",
		"Ab 01.01.2027":              "130,00 EUR (Dauerauftrag anpassen)",
		"Weitere Zahlung (jährlich)": "50,00 EUR, erste am 15.02.2027",
	} {
		if got[k] != want {
			t.Errorf("%s: %q, erwartet %q", k, got[k], want)
		}
	}
	if qr := r["qr"].([]map[string]any)[0]["text"].(string); !strings.Contains(qr, "\nEUR120.00\n") {
		t.Fatalf("GiroCode: %q", qr)
	}
	// Vorlage der Vertragsart; Platzhalter leer → Abschnitt entfällt
	e.must("ContractType", "update", map[string]any{"id": "1000|VS", "data": map[string]any{"transfer_purpose": "Vers.-Nr. {external}[ Mandat {mandate}] {designation}"}})
	r = e.must("Contract", transferAction, map[string]any{"id": "1000|" + id})
	if p := r["copy"].([]map[string]any)[4]["value"]; p != "Vers.-Nr. POL-4711 Gebäudeversicherung" {
		t.Fatalf("Vorlage: %v", p)
	}
	// Mietvertrag (wir bekommen Geld): abgelehnt
	_, err := e.call("Contract", transferAction, map[string]any{"id": "1000|" + e.rentContract()})
	expect(t, err, sdk.ErrInvalidArgument, "Forderungsvertrag")
}

func TestNextDue(t *testing.T) {
	for _, c := range []struct {
		g    payGroup
		day  string
		want string
	}{
		{payGroup{Frequency: "MONTHLY", DueDay: 31}, "2026-02-01", "2026-02-28"},
		{payGroup{Frequency: "QUARTERLY", DueDay: 15}, "2026-02-16", "2026-04-15"},
		{payGroup{Frequency: "QUARTERLY", DueDay: 1, Mode: "IN_ARREARS"}, "2026-01-05", "2026-03-01"},
		{payGroup{Frequency: "YEARLY", DueDay: 1, StartMonth: 10}, "2026-11-02", "2027-10-01"},
		{payGroup{Frequency: "MONTHLY", DueDay: 1, ValidFrom: "2027-01-01"}, "2026-11-02", "2027-01-01"},
	} {
		if got := nextDue(c.g, c.day); got != c.want {
			t.Errorf("%+v %s: %s, erwartet %s", c.g, c.day, got, c.want)
		}
	}
}
