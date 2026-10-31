package bank

import (
	"strings"
	"testing"
)

// TestMandate: Lastschrift über das Mandat zugeordnet; abweichendes Mandat
// macht aus einem Treffer einen Vorschlag; fehlendes Mandat ergibt einen Hinweis.
func TestMandate(t *testing.T) {
	m := &Module{}
	ref := &refData{directions: map[string]string{"VE": "PAYABLE"}, coPartners: map[string][]string{}, byIBAN: map[string][]string{},
		finance: map[string][]string{}, debitor: map[string]bool{}, invoiceUsed: map[string]bool{},
		contracts: []contractRef{
			{ID: "VE-2026-0001", Partner: "SW", Type: "VE", Status: "ACTIVE", From: "2026-01-01", UsesMandate: true, Mandate: "SW-4711", Creditor: "DE98ZZZ09999999999"},
			{ID: "VE-2026-0002", Partner: "SW", Type: "VE", Status: "ACTIVE", From: "2026-01-01", UsesMandate: true},
			{ID: "DA-2026-0001", Partner: "BK", Type: "VE", Status: "ACTIVE", From: "2026-01-01", Mandate: "DARL-1"},
			{ID: "DA-2026-0002", Partner: "BK", Type: "VE", Status: "ACTIVE", From: "2026-01-01", Mandate: "DARL-1"},
		}}
	txn := func(mandate, creditor, purpose string) *txnRow {
		return &txnRow{Amount: -5000, Date: "2026-02-02", Mandate: mandate, Creditor: creditor, Purpose: purpose}
	}
	for _, c := range []struct {
		t                    *txnRow
		contract, status, in string
	}{
		{txn("sw-4711", "DE98ZZZ09999999999", "Abschlag Gas"), "VE-2026-0001", stMatched, "Mandat sw-4711 des Vertrags"},
		{txn("SW-4711", "DE11ZZZ00000000001", "Abschlag Gas"), "", stOpen, ""}, // anderer Gläubiger: kein Treffer
		{txn("SW-9999", "", "Abschlag VE-2026-0001"), "VE-2026-0001", stProposed, "weicht vom Vertrag ab (SW-4711)"},
		{txn("SW-5555", "", "Abschlag VE-2026-0002"), "VE-2026-0002", stMatched, "am Vertrag nicht hinterlegt"},
		{txn("DARL-1", "", "Rate DA-2026-0002"), "DA-2026-0002", stMatched, "Vertrag DA-2026-0002 im Verwendungszweck"}, // ein Mandat, zwei Verträge
	} {
		tg, status, _, note, _ := m.matchOne(c.t, nil, ref)
		if tg.Contract != c.contract || status != c.status || !strings.Contains(note, c.in) {
			t.Errorf("%v: %s %s %q", c.t, tg.Contract, status, note)
		}
	}
}
