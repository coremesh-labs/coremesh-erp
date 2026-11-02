package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Überweisung (seit 0.21.0): „Überweisung …“ an der Eingangsrechnung zeigt untereinander,
// was das Banking braucht – Empfänger (Kontoinhaber der Bankverbindung, sonst Name des
// Lieferanten), IBAN, BIC, Betrag (deutsch, z. B. 1.234,56 EUR) und Verwendungszweck, je
// mit Knopf „Kopieren“ (Webserver 0.53.0); in Euro dazu ein GiroCode (EPC-QR) für die
// Banking-App. Der Verwendungszweck folgt der Vorlage der Rechnungsart (transfer_purpose,
// Platzhalter {reference} Rechnungsnummer des Lieferanten, {date} Rechnungsdatum,
// {invoice} eigene Nummer, {customer} Kundennummer des Vertrags; leer = defaultPurpose).
// Gebucht wird nichts – die Zahlung kommt mit dem Kontoauszug.

const (
	transferAction = "transfer"
	defaultPurpose = "Rechnung {reference} vom {date}"
)

type bankDetail struct{ IBAN, BIC, Holder string }

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
	name := m.payeeName(ctx, inv.Supplier)
	bank, err := m.supplierBank(ctx, inv.Supplier, firstNonEmpty(inv.InvoiceDate, crud.KeyDate(ctx)))
	if err != nil {
		return sdk.Response{}, err
	}
	payee := firstNonEmpty(bank.Holder, name)
	purpose := m.transferPurpose(ctx, inv)
	cur := firstNonEmpty(inv.Currency, "EUR")
	amount := formatAmount(minor, m.currencyDecimals(ctx, cur))
	fields := []map[string]any{
		{"label": "An (Empfänger)", "value": payee},
		{"label": "IBAN", "value": groupIBAN(bank.IBAN), "copy": bank.IBAN},
		{"label": "BIC", "value": bank.BIC},
		{"label": "Betrag", "value": pricing.FormatDE(amount) + " " + cur, "copy": strings.ReplaceAll(amount, ".", ",")},
		{"label": "Verwendungszweck", "value": purpose},
	}
	out := map[string]any{"copy": fields,
		"message": fmt.Sprintf("Überweisung zur Rechnung %s (%s) – Werte einzeln kopieren oder den GiroCode mit der Banking-App scannen", inv.ID, inv.Reference)}
	if cur == "EUR" {
		out["qr"] = []map[string]any{{"text": giroCode(payee, bank.IBAN, bank.BIC, amount, purpose), "caption": "GiroCode (Banking-App)", "level": "M"}}
	}
	return sdk.Response{Payload: out}, nil
}

// payeeName: Name des Lieferanten wie auf der Überweisung (Person: Vorname Nachname).
func (m *Module) payeeName(ctx context.Context, id string) string {
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Type  string `json:"type"`
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	if bp.Type == "PERSON" {
		return strings.TrimSpace(bp.Name2 + " " + bp.Name1)
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// supplierBank: gültige Bankverbindung des Lieferanten zum Stichtag – die Standardverbindung zuerst.
func (m *Module) supplierBank(ctx context.Context, partner, day string) (bankDetail, error) {
	resp, err := m.services.Call(ctx, "PartnerBankDetail", "list", map[string]any{"query": map[string]any{"bp_id": partner}})
	if err != nil {
		return bankDetail{}, fmt.Errorf("Bankverbindung des Lieferanten: %w", err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &list); err != nil {
		return bankDetail{}, err
	}
	var best map[string]any
	for _, it := range list.Items {
		if crud.Str(it["bp_id"]) != partner || crud.Str(it["iban"]) == "" {
			continue
		}
		from, _ := crud.ParseDate(it["valid_from"])
		to, _ := crud.ParseDate(it["valid_to"])
		if (from != "" && from > day) || (to != "" && to < day) {
			continue
		}
		if best == nil || crud.AsBool(it["is_default"]) && !crud.AsBool(best["is_default"]) {
			best = it
		}
	}
	if best == nil {
		return bankDetail{}, crud.Invalid("Lieferant %s hat keine gültige Bankverbindung (Geschäftspartner → Bankverbindungen)", partner)
	}
	iban := strings.ToUpper(strings.ReplaceAll(crud.Str(best["iban"]), " ", ""))
	return bankDetail{IBAN: iban, BIC: strings.ToUpper(strings.TrimSpace(crud.Str(best["bic"]))), Holder: strings.TrimSpace(crud.Str(best["account_holder"]))}, nil
}

// transferPurpose: Verwendungszweck nach der Vorlage der Rechnungsart.
func (m *Module) transferPurpose(ctx context.Context, inv *invoiceRow) string {
	tpl := firstNonEmpty(strings.TrimSpace(m.text(ctx, `SELECT transfer_purpose FROM procurement__invoice_type WHERE company_code = ? AND code = ?`,
		inv.CompanyCode, inv.Type)), defaultPurpose)
	customer := ""
	if inv.Contract != "" && strings.Contains(tpl, "{customer}") {
		if resp, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": inv.CompanyCode + "|" + inv.Contract}); err == nil {
			var c struct {
				Customer string `json:"customer_number"`
			}
			if sdk.Decode(resp.Payload, &c) == nil {
				customer = c.Customer
			}
		}
	}
	date := inv.InvoiceDate
	if len(date) == 10 {
		date = date[8:10] + "." + date[5:7] + "." + date[0:4]
	}
	s := strings.NewReplacer("{reference}", inv.Reference, "{date}", date, "{invoice}", inv.ID, "{customer}", customer).Replace(tpl)
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 140 { // SEPA: höchstens 140 Zeichen
		s = string([]rune(s)[:140])
	}
	return s
}

// groupIBAN: IBAN in Vierergruppen (Anzeige).
func groupIBAN(iban string) string {
	var b strings.Builder
	for i, r := range iban {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// giroCode: Inhalt des EPC-QR-Codes (EPC069-12, Version 002, UTF-8, SEPA-Überweisung).
func giroCode(name, iban, bic, amount, purpose string) string {
	cut := func(s string, n int) string {
		if r := []rune(strings.TrimSpace(s)); len(r) > n {
			return string(r[:n])
		}
		return strings.TrimSpace(s)
	}
	return strings.Join([]string{"BCD", "002", "1", "SCT", bic, cut(name, 70), iban, "EUR" + amount, "", "", cut(purpose, 140)}, "\n")
}
