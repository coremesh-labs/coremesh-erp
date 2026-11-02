// Package banktransfer liefert das Fenster „Überweisung …“ der Fachmodule (Eingangsrechnung,
// Vertrag): Empfänger, IBAN, BIC, Betrag und Verwendungszweck als Kopierfelder des
// WebServers (ab 0.53.0, {"copy": …}) und in Euro einen GiroCode (EPC-QR, {"qr": …}).
package banktransfer

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Caller: Aufruf anderer Module (module.Services).
type Caller interface {
	Call(ctx context.Context, object, action string, payload any) (sdk.Response, error)
}

// Bank: Bankverbindung des Empfängers.
type Bank struct{ IBAN, BIC, Holder string }

// PayeeName: Name des Partners wie auf der Überweisung (Person: Vorname Nachname).
func PayeeName(ctx context.Context, c Caller, id string) string {
	resp, err := c.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
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

// PartnerBank: gültige Bankverbindung des Partners zum Stichtag – die Standardverbindung zuerst.
func PartnerBank(ctx context.Context, c Caller, partner, day, role string) (Bank, error) {
	resp, err := c.Call(ctx, "PartnerBankDetail", "list", map[string]any{"query": map[string]any{"bp_id": partner}})
	if err != nil {
		return Bank{}, fmt.Errorf("Bankverbindung des %ss: %w", role, err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &list); err != nil {
		return Bank{}, err
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
		return Bank{}, crud.Invalid("%s %s hat keine gültige Bankverbindung (Geschäftspartner → Bankverbindungen)", role, partner)
	}
	iban := strings.ToUpper(strings.ReplaceAll(crud.Str(best["iban"]), " ", ""))
	return Bank{IBAN: iban, BIC: strings.ToUpper(strings.TrimSpace(crud.Str(best["bic"]))), Holder: strings.TrimSpace(crud.Str(best["account_holder"]))}, nil
}

// Purpose: Platzhalter {name} ersetzen; ein [Abschnitt] entfällt, wenn ein Platzhalter darin
// leer ist (sonst bleibt er ohne Klammern stehen). Leerraum wird zusammengefasst, höchstens
// 140 Zeichen (SEPA).
func Purpose(tpl string, values map[string]string) string {
	s := strings.Join(strings.Fields(Fill(tpl, values)), " ")
	if r := []rune(s); len(r) > 140 {
		s = string(r[:140])
	}
	return s
}

// Fill: wie Purpose, ohne Leerraum zu ändern.
func Fill(tpl string, values map[string]string) string {
	fill := func(part string) (string, bool) {
		ok := true
		var b strings.Builder
		for {
			i := strings.IndexByte(part, '{')
			j := strings.IndexByte(part[i+1:], '}')
			if i < 0 || j < 0 {
				b.WriteString(part)
				return b.String(), ok
			}
			b.WriteString(part[:i])
			name := part[i+1 : i+1+j]
			if v, known := values[name]; known {
				if strings.TrimSpace(v) == "" {
					ok = false
				}
				b.WriteString(v)
			} else {
				b.WriteString(part[i : i+2+j])
			}
			part = part[i+2+j:]
		}
	}
	var out strings.Builder
	for {
		i := strings.IndexByte(tpl, '[')
		j := strings.IndexByte(tpl[i+1:], ']')
		if i < 0 || j < 0 {
			s, _ := fill(tpl)
			out.WriteString(s)
			return out.String()
		}
		s, _ := fill(tpl[:i])
		out.WriteString(s)
		if sec, ok := fill(tpl[i+1 : i+1+j]); ok {
			out.WriteString(sec)
		}
		tpl = tpl[i+2+j:]
	}
}

// Field: zusätzliches Kopierfeld (z. B. Rhythmus, erste Ausführung).
type Field struct{ Label, Value, Copy string }

// Payload: Ergebnis des Fensters. amount mit Dezimalpunkt ("1234.50"), currency z. B. EUR;
// extra steht nach dem Verwendungszweck.
func Payload(payee string, bank Bank, amount, currency, purpose, message string, extra ...Field) map[string]any {
	fields := []map[string]any{
		{"label": "An (Empfänger)", "value": payee},
		{"label": "IBAN", "value": GroupIBAN(bank.IBAN), "copy": bank.IBAN},
		{"label": "BIC", "value": bank.BIC},
		{"label": "Betrag", "value": pricing.FormatDE(amount) + " " + currency, "copy": strings.ReplaceAll(amount, ".", ",")},
		{"label": "Verwendungszweck", "value": purpose},
	}
	for _, f := range extra {
		m := map[string]any{"label": f.Label, "value": f.Value}
		if f.Copy != "" {
			m["copy"] = f.Copy
		}
		fields = append(fields, m)
	}
	out := map[string]any{"copy": fields, "message": message}
	if currency == "EUR" {
		out["qr"] = []map[string]any{{"text": GiroCode(payee, bank.IBAN, bank.BIC, amount, purpose), "caption": "GiroCode (Banking-App)", "level": "M"}}
	}
	return out
}

// GroupIBAN: IBAN in Vierergruppen (Anzeige).
func GroupIBAN(iban string) string {
	var b strings.Builder
	for i, r := range iban {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// GiroCode: Inhalt des EPC-QR-Codes (EPC069-12, Version 002, UTF-8, SEPA-Überweisung).
func GiroCode(name, iban, bic, amount, purpose string) string {
	cut := func(s string, n int) string {
		if r := []rune(strings.TrimSpace(s)); len(r) > n {
			return string(r[:n])
		}
		return strings.TrimSpace(s)
	}
	return strings.Join([]string{"BCD", "002", "1", "SCT", bic, cut(name, 70), iban, "EUR" + amount, "", "", cut(purpose, 140)}, "\n")
}

// LastPayment: Bankverbindung der letzten Zahlung (Ausgang mit IBAN) laut Kontoauszug, die
// zur Abfrage passt (z. B. {"company_code", "contract_id"} oder {"company_code", "partner_id"}) –
// Rückfall, wenn am Partner keine Bankverbindung hinterlegt ist. day = Buchungstag der Zahlung.
func LastPayment(ctx context.Context, c Caller, query map[string]any) (bank Bank, name, day string, ok bool) {
	resp, err := c.Call(ctx, "BankTransaction", "list", map[string]any{"query": query})
	if err != nil {
		return Bank{}, "", "", false
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &list) != nil {
		return Bank{}, "", "", false
	}
	for _, it := range list.Items {
		iban := strings.ToUpper(strings.ReplaceAll(crud.Str(it["counterparty_iban"]), " ", ""))
		d, _ := crud.ParseDate(it["booking_date"])
		if iban == "" || !strings.HasPrefix(strings.TrimSpace(crud.Str(it["amount"])), "-") || d <= day {
			continue
		}
		bank, name, day, ok = Bank{IBAN: iban, BIC: strings.ToUpper(strings.TrimSpace(crud.Str(it["counterparty_bic"])))},
			strings.TrimSpace(crud.Str(it["counterparty_name"])), d, true
	}
	return bank, name, day, ok
}

// Find: Bankverbindung des Partners zum Stichtag, sonst die der letzten Zahlung laut
// Kontoauszug (query wie bei LastPayment; nil = kein Rückfall). note nennt den Rückfall.
func Find(ctx context.Context, c Caller, partner, day, role string, query map[string]any) (bank Bank, note string, err error) {
	bank, err = PartnerBank(ctx, c, partner, day, role)
	if err == nil || query == nil {
		return bank, "", err
	}
	b, name, d, ok := LastPayment(ctx, c, query)
	if !ok {
		return Bank{}, "", err
	}
	b.Holder = name
	return b, fmt.Sprintf("Bankverbindung aus der letzten Zahlung vom %s (Kontoauszug) – am Geschäftspartner %s ist keine hinterlegt.", germanDate(d), partner), nil
}

func germanDate(iso string) string {
	if len(iso) < 10 {
		return iso
	}
	return iso[8:10] + "." + iso[5:7] + "." + iso[:4]
}
