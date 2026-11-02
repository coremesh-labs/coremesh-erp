package procurement

import (
	"fmt"
	"strings"
	"testing"
)

// TestTransfer (0.21.0): „Überweisung …“ – Empfänger, gültige Standard-IBAN, Betrag deutsch,
// Verwendungszweck nach der Vorlage der Rechnungsart, GiroCode; ohne Bankverbindung Fehler.
func TestTransfer(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	id := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "766/2026",
		"invoice_date": "2026-10-30", "object_type": "Building", "object_id": "LpzBrn1"})["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "cost_category": "INSTAND", "amount": "1234,50"})
	r := e.must(invoiceObject, transferAction, map[string]any{"id": "1000|" + id})
	got := map[string]string{}
	for _, f := range r["copy"].([]map[string]any) {
		got[f["label"].(string)] = fmt.Sprint(f["value"]) + "|" + fmt.Sprint(f["copy"])
	}
	for k, want := range map[string]string{
		"An (Empfänger)":   "Klempner Schulze|<nil>",
		"IBAN":             "DE89 3704 0044 0532 0130 00|DE89370400440532013000",
		"BIC":              "COBADEFFXXX|<nil>",
		"Betrag":           "1.234,50 EUR|1234,50",
		"Verwendungszweck": "Rechnung 766/2026 vom 30.10.2026|<nil>",
	} {
		if got[k] != want {
			t.Errorf("%s: %q, erwartet %q", k, got[k], want)
		}
	}
	qr := r["qr"].([]map[string]any)[0]["text"].(string)
	if qr != "BCD\n002\n1\nSCT\nCOBADEFFXXX\nKlempner Schulze\nDE89370400440532013000\nEUR1234.50\n\n\nRechnung 766/2026 vom 30.10.2026" {
		t.Fatalf("GiroCode: %q", qr)
	}
	// Kunden-, Projekt- und Zahlungsnummer (0.22.0); Kundennummer der letzten Rechnung des Lieferanten
	e.must(invoiceObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"customer_number": "K-4711", "project_number": "BV Brühl 1"}})
	if p := e.must(invoiceObject, transferAction, map[string]any{"id": "1000|" + id})["copy"].([]map[string]any)[4]["value"]; p != "Rechnung 766/2026 Kunde K-4711 Projekt BV Brühl 1 vom 30.10.2026" {
		t.Fatalf("mit Kunde/Projekt: %v", p)
	}
	id3 := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "801/2026",
		"invoice_date": "2026-11-01", "object_type": "Building", "object_id": "LpzBrn1", "payment_reference": "ZN 99 123"})["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id3, "cost_category": "INSTAND", "amount": "5"})
	if p := e.must(invoiceObject, transferAction, map[string]any{"id": "1000|" + id3})["copy"].([]map[string]any)[4]["value"]; p != "ZN 99 123 Rechnung 801/2026 Kunde K-4711 vom 01.11.2026" {
		t.Fatalf("Zahlungsnummer, Kunde der letzten Rechnung: %v", p)
	}
	// Vorlage der Rechnungsart
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"transfer_purpose": "RE {reference}  Beleg {invoice}"}})
	r = e.must(invoiceObject, transferAction, map[string]any{"id": "1000|" + id})
	if p := r["copy"].([]map[string]any)[4]["value"]; p != "RE 766/2026 Beleg "+id {
		t.Fatalf("Vorlage: %v", p)
	}
	// Lieferant ohne Bankverbindung
	id2 := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW2", "supplier_reference": "M-1",
		"invoice_date": "2026-10-30", "object_type": "Building", "object_id": "LpzBrn1"})["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id2, "cost_category": "INSTAND", "amount": "10"})
	_, err := e.call(invoiceObject, transferAction, map[string]any{"id": "1000|" + id2})
	if err == nil || !strings.Contains(err.Error(), "keine gültige Bankverbindung") {
		t.Fatalf("ohne Bank: %v", err)
	}
}
