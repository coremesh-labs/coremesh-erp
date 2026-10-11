package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Eingangsrechnung aus einer Vorlage eines Rahmenvertrags (Service
// SupplierInvoice.fromTemplate; Vertrag „Eingangsrechnung erfassen …“, DMS):
// Vorlage ist eine Kondition des Vertrags mit Konditionsart „Vorlage“ – Sachkonto,
// Objekt, Betrag (Vorschlag), Text; Steuerkennzeichen der Konditionsart bzw. der
// Vertragsart. Die Rechnung entsteht offen (nicht gebucht): Lieferant = Partner
// des Vertrags, eine Position aus der Vorlage, Rechnungsbetrag = Position.

type templateRequest struct {
	CompanyCode string `json:"company_code"`
	Contract    string `json:"contract_id"`
	Template    string `json:"template"` // Konditionsart[|Objekt] der Vorlage
	Reference   string `json:"supplier_reference"`
	Date        string `json:"invoice_date"`
	Amount      string `json:"amount"` // leer = Betrag der Vorlage
}

func (m *Module) fromTemplateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in templateRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if d, ok := req.Payload.(map[string]any)["data"]; ok {
		_ = sdk.Decode(d, &in)
	}
	cc, contract := strings.TrimSpace(in.CompanyCode), strings.TrimPrefix(strings.TrimSpace(in.Contract), strings.TrimSpace(in.CompanyCode)+"|")
	if cc == "" || contract == "" || in.Template == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis, Vertrag und Vorlage angeben")
	}
	if err := requireWrite(ctx, invoiceObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	get := func(object, id string) (map[string]any, error) {
		resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": id})
		if err != nil {
			return nil, err
		}
		var rec map[string]any
		return rec, sdk.Decode(resp.Payload, &rec)
	}
	c, err := get("Contract", cc+"|"+contract)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Vertrag %s: %v", contract, err)
	}
	ct, err := get("ContractType", cc+"|"+crud.Str(c["contract_type"]))
	if err != nil {
		return sdk.Response{}, crud.Invalid("Vertragsart %s: %v", crud.Str(c["contract_type"]), err)
	}
	if !crud.AsBool(ct["template_invoices"]) {
		return sdk.Response{}, crud.Invalid("Vertragsart %s erlaubt keine Rechnungen aus Vorlagen (an der Vertragsart einstellbar)", crud.Str(c["contract_type"]))
	}
	condType, condObject, _ := strings.Cut(in.Template, "|")
	cond, err := get("ContractCondition", cc+"|"+contract+"|"+condType+"|"+condObject)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Vorlage %s am Vertrag %s: %v", in.Template, contract, err)
	}
	cTyp, err := get("ConditionType", cc+"|"+condType)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Konditionsart %s: %v", condType, err)
	}
	if !crud.AsBool(cTyp["is_template"]) {
		return sdk.Response{}, crud.Invalid("Konditionsart %s ist keine Rechnungsvorlage", condType)
	}
	objType, objID := m.contractObject(ctx, cc, contract, crud.Str(cond["object_id"]))
	tax := firstNonEmpty(crud.Str(cTyp["tax_code"]), crud.Str(ct["default_tax_code"]))
	amount := firstNonEmpty(strings.TrimSpace(in.Amount), crud.Str(cond["amount"]))
	date := firstNonEmpty(in.Date, crud.KeyDate(ctx))
	text := firstNonEmpty(crud.Str(cond["note"]), crud.Str(cTyp["name"]), condType)
	head := map[string]any{"company_code": cc, "invoice_type": firstNonEmpty(crud.Str(ct["invoice_type"]), "ER"), "supplier_id": c["partner_id"],
		"supplier_reference": in.Reference, "invoice_date": date, "currency": c["currency"], "tax_code": nilIfEmpty(tax),
		"header_text": text, "object_type": nilIfEmpty(objType), "object_id": nilIfEmpty(objID)}
	var id string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		resp, err := m.set.Entity(invoiceObject).Create(ctx, map[string]any{"data": head})
		if err != nil {
			return err
		}
		rec, _ := resp.Payload.(crud.Record)
		id = crud.Str(rec["invoice_id"])
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET framework_contract = ? WHERE company_code = ? AND invoice_id = ?`, contract, cc, id); err != nil {
			return err
		}
		item := map[string]any{"company_code": cc, "invoice_id": id, "account_number": cond["account_number"], "amount": amount,
			"item_text": text, "tax_code": nilIfEmpty(tax), "object_type": nilIfEmpty(objType), "object_id": nilIfEmpty(objID)}
		if _, err := m.set.Entity(itemObject).Create(ctx, map[string]any{"data": item}); err != nil {
			return fmt.Errorf("Position aus Vorlage: %w", err)
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": cc + "|" + id, "invoice_id": id,
		"message": fmt.Sprintf("Eingangsrechnung %s aus Vorlage %s erfasst (offen – prüfen, ergänzen, dann buchen)", id, text)}}, nil
}

// contractObject: Objekt der Vorlage (bzw. Hauptobjekt des Vertrags) mit Art.
func (m *Module) contractObject(ctx context.Context, cc, contract, object string) (string, string) {
	resp, err := m.services.Call(ctx, "ContractObject", "list", map[string]any{"query": map[string]any{"company_code": cc, "contract_id": contract}})
	if err != nil {
		return "", ""
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return "", ""
	}
	var main [2]string
	for _, it := range l.Items {
		if crud.Str(it["contract_id"]) != contract {
			continue
		}
		t, o := crud.Str(it["object_type"]), crud.Str(it["object_id"])
		if object != "" && o == object {
			return t, o
		}
		if crud.AsBool(it["is_main"]) || main[1] == "" {
			main = [2]string{t, o}
		}
	}
	return main[0], main[1]
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// checkInvoiceContract: Vertrag der Rechnung (ohne Buchungskreis) muss es geben.
func (m *Module) checkInvoiceContract(ctx context.Context, cc string, rec crud.Record) error {
	c := strings.TrimPrefix(strings.TrimSpace(crud.Str(rec["framework_contract"])), cc+"|")
	if c == "" {
		rec["framework_contract"] = nil
		return nil
	}
	if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + c}); err != nil {
		return crud.Invalid("Vertrag %s: %v", c, err)
	}
	rec["framework_contract"] = c
	return nil
}

// contractDraftAction: Entwurf einer Eingangsrechnung zum Vertrag (nichts wird
// gespeichert) – Kopf mit Vertrag, Lieferant, Objekt und Steuerkennzeichen der
// Vertragsart, je Kondition (am Stichtag gültig) eine Position mit Text, Sachkonto,
// Objekt, Steuerkennzeichen und Betrag. Für Vorbelegungen, z. B. den KI-Schritt
// des Dokumentenmanagements: {company_code, contract_id, date} → {result: {invoice, items}}.
func (m *Module) contractDraftAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		Contract    string `json:"contract_id"`
		Date        string `json:"date"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if d, ok := req.Payload.(map[string]any)["data"]; ok {
		_ = sdk.Decode(d, &in)
	}
	cc, contract := strings.TrimSpace(in.CompanyCode), strings.TrimPrefix(strings.TrimSpace(in.Contract), strings.TrimSpace(in.CompanyCode)+"|")
	if cc == "" || contract == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Vertrag angeben")
	}
	date := firstNonEmpty(strings.TrimSpace(in.Date), crud.KeyDate(ctx))
	if len(date) > 10 {
		date = date[:10]
	}
	get := func(object, id string) map[string]any {
		resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": id})
		if err != nil {
			return nil
		}
		var rec map[string]any
		_ = sdk.Decode(resp.Payload, &rec)
		return rec
	}
	c := get("Contract", cc+"|"+contract)
	if c == nil {
		return sdk.Response{}, crud.Invalid("Vertrag %s gibt es nicht", contract)
	}
	ct := get("ContractType", cc+"|"+crud.Str(c["contract_type"]))
	objType, objID := m.contractObject(ctx, cc, contract, "")
	defTax := crud.Str(ct["default_tax_code"])
	head := map[string]any{"company_code": cc, "invoice_type": firstNonEmpty(crud.Str(ct["invoice_type"]), "ER"), "supplier_id": crud.Str(c["partner_id"]),
		"currency": crud.Str(c["currency"]), "framework_contract": contract, "tax_code": defTax, "object_type": objType, "object_id": objID,
		"header_text": crud.Str(c["designation"])}
	var items []any
	resp, err := m.services.Call(ctx, "ContractCondition", "list", map[string]any{"query": map[string]any{"company_code": cc, "contract_id": contract}})
	if err == nil {
		var l struct {
			Items []map[string]any `json:"items"`
		}
		_ = sdk.Decode(resp.Payload, &l)
		for _, k := range l.Items {
			if crud.Str(k["contract_id"]) != contract {
				continue
			}
			from, to := crud.Str(k["valid_from"]), crud.Str(k["valid_to"])
			if (from != "" && from[:min(10, len(from))] > date) || (to != "" && to[:min(10, len(to))] < date) {
				continue
			}
			typ := get("ConditionType", cc+"|"+crud.Str(k["condition_type"]))
			ot, oid := objType, objID
			if o := crud.Str(k["object_id"]); o != "" {
				ot, oid = m.contractObject(ctx, cc, contract, o)
			}
			lbl, _ := k["_labels"].(map[string]any)
			items = append(items, map[string]any{"item_text": firstNonEmpty(crud.Str(k["note"]), crud.Str(typ["name"]), crud.Str(lbl["condition_type"]), crud.Str(k["condition_type"])),
				"account_number": crud.Str(k["account_number"]), "amount": amountText(k["amount"]),
				"tax_code": firstNonEmpty(crud.Str(typ["tax_code"]), defTax), "object_type": ot, "object_id": oid})
		}
	}
	return sdk.Response{Payload: map[string]any{"result": map[string]any{"invoice": head, "items": items}}}, nil
}

// amountText: Betrag einer Kondition als Zahl mit Punkt (die Liste liefert ihn formatiert).
func amountText(v any) string {
	s := strings.TrimSpace(crud.Str(v))
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	}
	return s
}
