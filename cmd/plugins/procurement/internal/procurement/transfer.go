package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/banktransfer"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Überweisung (seit 0.21.0): „Überweisung …“ an der Eingangsrechnung zeigt untereinander,
// was das Banking braucht – Empfänger (Kontoinhaber der Bankverbindung, sonst Name des
// Lieferanten), IBAN, BIC, Betrag (deutsch, z. B. 1.234,56 EUR) und Verwendungszweck, je
// mit Knopf „Kopieren“ (Webserver 0.53.0); in Euro dazu ein GiroCode (EPC-QR) für die
// Banking-App. Der Verwendungszweck folgt der Vorlage der Rechnungsart (transfer_purpose,
// Platzhalter {reference} Rechnungsnummer des Lieferanten, {date} Rechnungsdatum,
// {invoice} eigene Nummer, {customer} Kundennummer, {project} Projektnummer, {payment}
// Zahlungsnummer; leer = defaultPurpose). Seit 0.22.0: Text in [eckigen Klammern] entfällt,
// wenn ein Platzhalter darin leer bleibt (z. B. „[ Kunde {customer}]“). Kundennummer: an der
// Rechnung, sonst Kundennummer des Vertrags, sonst die der letzten Rechnung des Lieferanten.
// Gebucht wird nichts – die Zahlung kommt mit dem Kontoauszug.

const (
	transferAction = "transfer"
	defaultPurpose = "[{payment} ]Rechnung {reference}[ Kunde {customer}][ Projekt {project}] vom {date}"
)

func (m *Module) transferAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	inv, err := m.invoiceOf(ctx, crud.Str(p["id"]))
	if err != nil {
		return sdk.Response{}, err
	}
	if inv.Status == invoiceCancelled || inv.Status == invoiceDiscarded {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s – keine Überweisung", inv.ID, statusText(inv.Status))
	}
	// invoice_amount steht in der kleinsten Einheit der Währung (Cent)
	minor := toInt(m.text(ctx, `SELECT invoice_amount FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, inv.CompanyCode, inv.ID))
	if minor <= 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s: kein positiver Rechnungsbetrag (Gutschrift?) – nichts zu überweisen", inv.ID)
	}
	bank, note, err := banktransfer.Find(ctx, m.services, inv.Supplier, firstNonEmpty(inv.InvoiceDate, crud.KeyDate(ctx)), "Lieferant",
		map[string]any{"company_code": inv.CompanyCode, "partner_id": inv.Supplier})
	if err != nil {
		return sdk.Response{}, err
	}
	payee := firstNonEmpty(bank.Holder, banktransfer.PayeeName(ctx, m.services, inv.Supplier))
	cur := firstNonEmpty(inv.Currency, "EUR")
	amount := formatAmount(minor, m.currencyDecimals(ctx, cur))
	return sdk.Response{Payload: banktransfer.Payload(payee, bank, amount, cur, m.transferPurpose(ctx, inv),
		strings.TrimSpace(fmt.Sprintf("Überweisung zur Rechnung %s (%s) – Werte einzeln kopieren oder den GiroCode mit der Banking-App scannen. %s", inv.ID, inv.Reference, note)))}, nil
}

// transferPurpose: Verwendungszweck nach der Vorlage der Rechnungsart.
func (m *Module) transferPurpose(ctx context.Context, inv *invoiceRow) string {
	tpl := firstNonEmpty(strings.TrimSpace(m.text(ctx, `SELECT transfer_purpose FROM procurement__invoice_type WHERE company_code = ? AND code = ?`,
		inv.CompanyCode, inv.Type)), defaultPurpose)
	date := inv.InvoiceDate
	if len(date) == 10 {
		date = date[8:10] + "." + date[5:7] + "." + date[0:4]
	}
	values := map[string]string{"reference": inv.Reference, "date": date, "invoice": inv.ID, "customer": "", "project": inv.ProjectNumber, "payment": inv.PaymentRef}
	if strings.Contains(tpl, "{customer}") {
		values["customer"] = m.customerNumber(ctx, inv)
	}
	return banktransfer.Purpose(tpl, values)
}

// customerNumber: Kundennummer an der Rechnung, sonst die des Vertrags, sonst die der
// letzten Rechnung desselben Lieferanten.
func (m *Module) customerNumber(ctx context.Context, inv *invoiceRow) string {
	if inv.CustomerNumber != "" {
		return inv.CustomerNumber
	}
	if inv.Contract != "" {
		if resp, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": inv.CompanyCode + "|" + inv.Contract}); err == nil {
			var c struct {
				Customer string `json:"customer_number"`
			}
			if sdk.Decode(resp.Payload, &c) == nil && strings.TrimSpace(c.Customer) != "" {
				return strings.TrimSpace(c.Customer)
			}
		}
	}
	return m.text(ctx, `SELECT customer_number FROM procurement__invoice WHERE company_code = ? AND supplier_id = ? AND customer_number IS NOT NULL
		AND customer_number <> '' AND invoice_id <> ? ORDER BY invoice_date DESC, invoice_id DESC LIMIT 1`, inv.CompanyCode, inv.Supplier, inv.ID)
}
