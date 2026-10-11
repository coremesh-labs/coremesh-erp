package dms

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// KI-Anfragen des ERP-Workflows (Modul ai): Geschäftspartner, Eingangsrechnung,
// Lieferantenvertrag, Wirtschaftsplan. dms:setup-ai legt fehlende an; vorhandene
// bleiben (dort gepflegte Texte gehen vor).

const (
	aiBusinessPartner  = "BUSINESS_PARTNER"
	aiSupplierInvoice  = "SUPPLIER_INVOICE"
	aiSupplierContract = "SUPPLIER_CONTRACT"
	aiEconomicPlan     = "ECONOMIC_PLAN"
)

type aiReqSeed struct {
	Code, Name, Task, Extra, Description string
	Objects                              []map[string]any
}

var aiRequestSeeds = []aiReqSeed{
	{aiBusinessPartner, "Geschäftspartner aus einem Schreiben",
		"Ermittle aus dem Eingang den Absender des Schreibens (Firma oder Person) und trage ihn in die vorgegebene JSON-Struktur ein: " +
			"Stammdaten, Rolle, Anschrift(en), Kommunikation (Telefon, Fax, E-Mail, Internet) und Bankverbindung(en). Gib die ausgefüllte Struktur zurück.",
		"Firmen: type ORGANIZATION, Firmenname in name1, Zusatz (z. B. Abteilung) in name2. Personen: type PERSON, Nachname in name1, Vorname in name2.\n" +
			"Die Hauptanschrift hat die Adressrolle MAIN. Handwerker, Lieferanten und Dienstleister haben die Rolle CREDITOR.\n" +
			"Die Hausverwaltung als Empfänger des Schreibens ist nicht gemeint. IBAN ohne Leerzeichen.",
		"Workflow: Partnerauswahl → Anlegen (Schritt 036)",
		[]map[string]any{
			{"name": "partner", "object": "BusinessPartner", "fields": "type,salutation_code,gender,name1,name2"},
			{"name": "roles", "object": "PartnerRole", "parent": "partner", "relation": "rollen", "multiple": true, "fields": "role_code"},
			{"name": "addresses", "object": "PartnerAddress", "parent": "partner", "relation": "adressen", "multiple": true, "fields": "address_role_code,is_default"},
			{"name": "address", "object": "PartnerAddressData", "parent": "addresses", "via_field": "address_id", "fields": "street,house_no,zip_code,city,country"},
			{"name": "contacts", "object": "PartnerContact", "parent": "partner", "relation": "kommunikation", "multiple": true, "fields": "comm_type_code,value,is_default"},
			{"name": "banks", "object": "PartnerBankDetail", "parent": "partner", "relation": "bank", "multiple": true, "fields": "iban,bic,bank_name,account_holder"},
		}},
	{aiSupplierInvoice, "Eingangsrechnung (Kopf und Positionen)",
		"Erfasse die Eingangsrechnung und trage sie in die vorgegebene JSON-Struktur ein: Kopf (Rechnungsnummer des Lieferanten, Rechnungsdatum, " +
			"Fälligkeit, Rechnungsbetrag brutto, Steuerkennzeichen, Text) und Positionen (Text, Betrag brutto, Steuerkennzeichen, Kostenart, " +
			"Leistungszeitraum). Gib die ausgefüllte Struktur zurück.",
		"Beträge brutto; je Steuersatz mindestens eine Position, die Summe der Positionen ist der Rechnungsbetrag.\n" +
			"Der Lieferant (supplier_id) und der Buchungskreis sind vorgegeben. Rechnungsart ER, wenn nichts anderes passt.",
		"Workflow: Kaufmännisches Objekt → Anlegen (Schritt 042)",
		[]map[string]any{
			{"name": "invoice", "object": "SupplierInvoice", "fields": "company_code,invoice_type,supplier_id,supplier_reference,invoice_date,due_date,currency,invoice_amount,tax_code,header_text"},
			{"name": "items", "object": "SupplierInvoiceItem", "parent": "invoice", "relation": "positionen", "multiple": true,
				"fields": "item_text,amount,tax_code,cost_category,service_from,service_to,allocable"},
		}},
	{aiSupplierContract, "Lieferanten- und Dienstleistungsvertrag",
		"Erfasse den Vertrag und trage ihn in die vorgegebene JSON-Struktur ein: Bezeichnung (kurz, was geleistet wird), Vertragsnummer des Partners, " +
			"Kundennummer, Beginn, Ende (leer = unbefristet), Datum der Unterschrift, Bemerkung (Kündigungsfrist, Leistungsumfang) und die Konditionen " +
			"(Entgelt mit Betrag, Rhythmus, Zahlungsweise, Beginn). Gib die ausgefüllte Struktur zurück.",
		"Vertragsart, Partner und Buchungskreis sind vorgegeben; die Beschreibung der Vertragsart steht unten.\n" +
			"Konditionen: calc_method FIXED, ein Jahresbeitrag ist frequency YEARLY, Zahlungsweise meist IN_ADVANCE; Beträge brutto.",
		"Workflow: Kaufmännisches Objekt → Anlegen, nach der Wahl der Vertragsart (Schritte 041, 042)",
		[]map[string]any{
			{"name": "contract", "object": "Contract", "fields": "company_code,contract_type,designation,partner_id,external_number,customer_number,valid_from,valid_to,signed_date,currency,note"},
			{"name": "conditions", "object": "ContractCondition", "parent": "contract", "relation": "konditionen", "multiple": true,
				"fields": "condition_type,calc_method,amount,frequency,payment_mode,valid_from,due_day,note"},
		}},
	{aiEconomicPlan, "Wirtschaftsplan (WEG)",
		"Erfasse den Wirtschaftsplan der Eigentümergemeinschaft und trage ihn in die vorgegebene JSON-Struktur ein: Zeitraum, Datum des Plans, " +
			"Bezeichnung und je Kostenposition Beschreibung, Kostenart, Gesamtkosten, Umlageschlüssel, Summe und Anteil des Schlüssels sowie den " +
			"Betrag dieser Einheit. Gib die ausgefüllte Struktur zurück.",
		"Der Vertrag (contract_id) und der Buchungskreis sind vorgegeben. Beträge als Zahl; der Zeitraum ist meist ein Kalenderjahr.",
		"Workflow: Kaufmännisches Objekt (Kategorie Wirtschaftsplan) → Anlegen (Schritt 042)",
		[]map[string]any{
			{"name": "plan", "object": "ContractPlan", "fields": "company_code,contract_id,period_from,period_to,plan_date,reference,note"},
			{"name": "items", "object": "ContractPlanItem", "parent": "plan", "relation": "positionen", "multiple": true,
				"fields": "description,cost_category,total_cost,allocation_key,key_total,key_share,amount,note"},
		}},
}

// setupAI legt die fehlenden KI-Anfragen an.
func (m *Module) setupAI(ctx context.Context) (string, error) {
	var created, kept []string
	for _, r := range aiRequestSeeds {
		if _, err := m.services.Call(ctx, "AiRequest", "get", map[string]any{"id": r.Code}); err == nil {
			kept = append(kept, r.Code)
			continue
		} else if !errors.Is(err, sdk.ErrNotFound) {
			return "", fmt.Errorf("KI-Modul: %w", err)
		}
		if _, err := m.services.Call(ctx, "AiRequest", "create", map[string]any{"data": map[string]any{"code": r.Code, "name": r.Name, "kind": "OBJECT",
			"task_text": r.Task, "extra_text": r.Extra, "description": r.Description, "retention_days": 1}}); err != nil {
			return "", fmt.Errorf("KI-Anfrage %s: %w", r.Code, err)
		}
		for i, o := range r.Objects {
			data := map[string]any{"request": r.Code, "line_no": i + 1}
			for k, v := range o {
				data[k] = v
			}
			if _, err := m.services.Call(ctx, "AiRequestObject", "create", map[string]any{"data": data}); err != nil {
				return "", fmt.Errorf("KI-Anfrage %s, Objekt %v: %w", r.Code, o["name"], err)
			}
		}
		created = append(created, r.Code)
	}
	msg := "KI-Anfragen angelegt: " + firstOf(strings.Join(created, ", "), "keine")
	if len(kept) > 0 {
		msg += "; vorhanden: " + strings.Join(kept, ", ")
	}
	return msg, nil
}

func (m *Module) setupAICommand(ctx context.Context, _ sdk.Request) (sdk.Response, error) {
	msg, err := m.setupAI(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}
