package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Eingangsrechnung aus Vorlagen eines Vertrags (Service SupplierInvoice.fromTemplate;
// Vertrag „Eingangsrechnung erfassen …“, DMS): Vorlagen sind Konditionen, die keine
// Sollstellung erzeugen (is_template: Abrechnungsweg der Vertragsart Eingangsrechnung,
// bei „beides“ Konditionsart Rechnungsposition) – Sachkonto, Objekt, Menge × Einzelpreis
// (netto), Text; Steuerkennzeichen des Konditionsgegenstands bzw. der Vertragsart. Die
// Rechnung entsteht offen (nicht gebucht) und netto: Lieferant = Partner des Vertrags,
// je Vorlage eine Position, die Umsatzsteuer als nachgelagerte Position (eigene Zeile).
// Ein Rechnungsbetrag laut Beleg gleicht Rundungsdifferenzen in der Steuerzeile aus.

type templateRequest struct {
	CompanyCode string `json:"company_code"`
	Contract    string `json:"contract_id"`
	Template    string `json:"template"` // Gegenstand[|Objekt] der Vorlage; mehrere durch Komma getrennt
	Reference   string `json:"supplier_reference"`
	Date        string `json:"invoice_date"`
	Amount      string `json:"amount"` // Rechnungsbetrag laut Beleg (brutto); leer = Summe der Positionen
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
		return sdk.Response{}, crud.Invalid("Vertragsart %s rechnet nicht über Eingangsrechnungen ab (Abrechnungsweg an der Vertragsart)", crud.Str(c["contract_type"]))
	}
	type tmpl struct {
		cond, typ                   map[string]any
		condType, objType, objID, text, tax string
	}
	var ts []tmpl
	for _, t := range strings.Split(in.Template, ",") {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		// Vorlage Gegenstand|Objekt|Position (Position fehlt = 1, vor contract 0.29.0)
		condType, rest, _ := strings.Cut(t, "|")
		condObject, pos, _ := strings.Cut(rest, "|")
		if pos = strings.TrimSpace(pos); pos == "" {
			pos = "1"
		}
		cond, err := get("ContractCondition", cc+"|"+contract+"|"+condType+"|"+condObject+"|"+pos)
		if err != nil {
			return sdk.Response{}, crud.Invalid("Vorlage %s am Vertrag %s: %v", t, contract, err)
		}
		if !crud.AsBool(cond["is_template"]) {
			return sdk.Response{}, crud.Invalid("Kondition %s am Vertrag %s ist keine Rechnungsvorlage (sie wird per Sollstellung abgerechnet)", condType, contract)
		}
		cTyp, err := get("ConditionType", cc+"|"+condType)
		if err != nil {
			return sdk.Response{}, crud.Invalid("Konditionsgegenstand %s: %v", condType, err)
		}
		objType, objID := m.contractObject(ctx, cc, contract, crud.Str(cond["object_id"]))
		ts = append(ts, tmpl{cond: cond, typ: cTyp, condType: condType, objType: objType, objID: objID,
			text: firstNonEmpty(crud.Str(cond["note"]), crud.Str(cTyp["name"]), condType),
			tax:  firstNonEmpty(crud.Str(cTyp["tax_code"]), crud.Str(ct["default_tax_code"]))})
	}
	if len(ts) == 0 {
		return sdk.Response{}, crud.Invalid("Vorlage wählen")
	}
	first := ts[0]
	date := firstNonEmpty(in.Date, crud.KeyDate(ctx))
	headText := first.text
	if len(ts) > 1 {
		headText = firstNonEmpty(crud.Str(c["designation"]), first.text)
	}
	head := map[string]any{"company_code": cc, "invoice_type": firstNonEmpty(crud.Str(ct["invoice_type"]), "ER"), "supplier_id": c["partner_id"],
		"supplier_reference": in.Reference, "invoice_date": date, "currency": c["currency"], "tax_code": nilIfEmpty(first.tax),
		"header_text": headText, "object_type": nilIfEmpty(first.objType), "object_id": nilIfEmpty(first.objID), "framework_contract": contract,
		"net_amounts": true, "dependent_types": "-"} // nachgelagerte Arten nach den Positionen (Steuerzeile zuletzt)
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
		for _, t := range ts {
			item := map[string]any{"company_code": cc, "invoice_id": id, "account_number": t.cond["account_number"], "amount": amountText(t.cond["amount"]),
				"item_text": t.text, "tax_code": nilIfEmpty(t.tax), "object_type": nilIfEmpty(t.objType), "object_id": nilIfEmpty(t.objID),
				"condition_type": t.condType, "unit": nilIfEmpty(crud.Str(t.cond["unit"])), "scheme": nilIfEmpty(crud.Str(t.cond["scheme"]))}
			if crud.Str(t.cond["unit_price"]) != "" {
				item["unit_price"], item["quantity"] = t.cond["unit_price"], t.cond["quantity"]
			}
			if _, err := m.set.Entity(itemObject).Create(ctx, map[string]any{"data": item}); err != nil {
				return fmt.Errorf("Position aus Vorlage %s: %w", t.condType, err)
			}
		}
		defs, _ := m.dependentDefs(ctx, cc)
		upd := map[string]any{"dependent_types": firstNonEmpty(strings.Join(m.defaultDependents(ctx, cc, contract, date, defs), ","), "-")}
		if _, err := m.set.Entity(invoiceObject).Update(ctx, map[string]any{"id": cc + "|" + id, "data": upd}); err != nil {
			return fmt.Errorf("Nachgelagerte Konditionen: %w", err)
		}
		if a := strings.TrimSpace(in.Amount); a != "" {
			if _, err := m.set.Entity(invoiceObject).Update(ctx, map[string]any{"id": cc + "|" + id, "data": map[string]any{"invoice_amount": a}}); err != nil {
				return fmt.Errorf("Rechnungsbetrag: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	text := first.text
	if len(ts) > 1 {
		text = fmt.Sprintf("%d Vorlagen", len(ts))
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
	head, items, err := m.contractDraft(ctx, in.CompanyCode, in.Contract, in.Date)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"result": map[string]any{"invoice": head, "items": items}}}, nil
}

// contractDraft: Kopf und Positionen zum Vertrag (Vertragsmodul, ContractCondition.positions):
// je am Datum gültiger Kondition eine Position mit Menge, Einheit, Einzelpreis,
// Gesamtpreis; die nachgelagerten Konditionen als Arten des Kopfs (dependent_types).
func (m *Module) contractDraft(ctx context.Context, cc, contract, date string) (map[string]any, []any, error) {
	cc = strings.TrimSpace(cc)
	contract = strings.TrimPrefix(strings.TrimSpace(contract), cc+"|")
	if cc == "" || contract == "" {
		return nil, nil, crud.Invalid("Buchungskreis und Vertrag angeben")
	}
	date = firstNonEmpty(strings.TrimSpace(date), crud.KeyDate(ctx))
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
		return nil, nil, crud.Invalid("Vertrag %s gibt es nicht", contract)
	}
	ct := get("ContractType", cc+"|"+crud.Str(c["contract_type"]))
	objType, objID := m.contractObject(ctx, cc, contract, "")
	defTax := crud.Str(ct["default_tax_code"])
	pos, err := m.contractPositions(ctx, cc, contract, date)
	if err != nil {
		return nil, nil, crud.Invalid("Positionen des Vertrags %s: %v", contract, err)
	}
	var deps []string
	seen := map[string]bool{}
	for _, d := range pos.Dependents {
		if !seen[d.Type] {
			seen[d.Type] = true
			deps = append(deps, d.Type)
		}
	}
	head := map[string]any{"company_code": cc, "invoice_type": firstNonEmpty(crud.Str(ct["invoice_type"]), "ER"), "supplier_id": crud.Str(c["partner_id"]),
		"currency": crud.Str(c["currency"]), "framework_contract": contract, "tax_code": defTax, "object_type": objType, "object_id": objID,
		"header_text": crud.Str(c["designation"]), "net_amounts": true, "dependent_types": firstNonEmpty(strings.Join(deps, ","), "-")}
	var items []any
	for _, k := range pos.Items {
		ot, oid := objType, objID
		if k.Object != "" {
			ot, oid = m.contractObject(ctx, cc, contract, k.Object)
		}
		items = append(items, map[string]any{"item_text": firstNonEmpty(k.Note, k.Name, k.ConditionType), "condition_type": k.ConditionType,
			"account_number": k.Account, "quantity": k.Quantity, "unit": k.Unit, "unit_price": k.UnitPrice, "amount": amountText(k.Amount),
			"tax_code": firstNonEmpty(k.TaxCode, defTax), "object_type": ot, "object_id": oid, "scheme": k.Scheme})
	}
	return head, items, nil
}

// invoiceFormState: „Vertragskonditionen übernehmen …“ – Vertrag und Datum der Rechnung vorschlagen.
func (m *Module) invoiceFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode == "create" || req.Mode == "edit" {
		cc := req.Values["company_code"]
		if cc == "" && req.ID != "" {
			if inv, err := m.invoiceOf(ctx, req.ID); err == nil {
				cc = inv.CompanyCode
			}
		}
		if cc != "" {
			st.Fields["dependent_types"] = metamodel.FieldState{Options: m.dependentOptions(ctx, cc)}
		}
		if req.Mode == "create" && req.Values["net_amounts"] == "" {
			v := "true"
			st.Fields["net_amounts"] = metamodel.FieldState{Value: &v}
		}
		return st, nil
	}
	if req.Mode != "action" || req.Action != "takeConditions" || req.ID == "" {
		return st, nil
	}
	inv, err := m.invoiceOf(ctx, req.ID)
	if err != nil {
		return st, nil
	}
	if req.Values["conditions_contract"] == "" {
		if c := m.text(ctx, `SELECT framework_contract FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, inv.CompanyCode, inv.ID); c != "" {
			st.Fields["conditions_contract"] = metamodel.FieldState{Value: &c}
		}
	}
	if req.Values["conditions_date"] == "" && inv.InvoiceDate != "" {
		d := inv.InvoiceDate
		st.Fields["conditions_date"] = metamodel.FieldState{Value: &d}
	}
	st.Message = "Ersetzt die Positionen durch die am Datum gültigen Konditionen des Vertrags; der Vertrag wird an der Rechnung eingetragen."
	if inv.Status != invoiceOpen {
		st.Message = fmt.Sprintf("Rechnung %s ist %s – Positionen nicht mehr änderbar.", inv.ID, statusText(inv.Status))
	}
	return st, nil
}

// takeConditionsAction: Positionen der (erfassten) Rechnung durch die Konditionen des
// Vertrags ersetzen; Vertrag, Objekt und Steuer kommen an die Rechnung, wenn dort leer.
func (m *Module) takeConditionsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Contract string `json:"conditions_contract"`
			Date     string `json:"conditions_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, "update", inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if inv.Status != invoiceOpen {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s und nicht mehr änderbar", inv.ID, statusText(inv.Status))
	}
	contract := firstNonEmpty(in.Data.Contract, m.text(ctx, `SELECT framework_contract FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, inv.CompanyCode, inv.ID))
	if contract == "" {
		return sdk.Response{}, crud.Invalid("Bitte den Vertrag angeben")
	}
	head, items, err := m.contractDraft(ctx, inv.CompanyCode, contract, firstNonEmpty(in.Data.Date, inv.InvoiceDate))
	if err != nil {
		return sdk.Response{}, err
	}
	if len(items) == 0 {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat am %s keine gültigen Konditionen", crud.Str(head["framework_contract"]), firstNonEmpty(in.Data.Date, inv.InvoiceDate))
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		deps := crud.Str(head["dependent_types"])
		if deps == "-" {
			deps = ""
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET framework_contract = ?,
			object_type = COALESCE(NULLIF(object_type, ''), ?), object_id = COALESCE(NULLIF(object_id, ''), ?),
			tax_code = COALESCE(NULLIF(tax_code, ''), ?), net_amounts = ?, dependent_types = ? WHERE company_code = ? AND invoice_id = ?`,
			head["framework_contract"], nilIfEmpty(crud.Str(head["object_type"])), nilIfEmpty(crud.Str(head["object_id"])),
			nilIfEmpty(crud.Str(head["tax_code"])), true, nilIfEmpty(deps), inv.CompanyCode, inv.ID); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET is_active = ? WHERE company_code = ? AND invoice_id = ? AND is_active = ?`,
			false, inv.CompanyCode, inv.ID, true); err != nil {
			return err
		}
		for i, it := range items {
			item, _ := it.(map[string]any)
			data := map[string]any{"company_code": inv.CompanyCode, "invoice_id": inv.ID}
			for k, v := range item {
				if s := crud.Str(v); s != "" {
					data[k] = s
				}
			}
			if _, err := m.set.Entity(itemObject).Create(ctx, map[string]any{"data": data}); err != nil {
				return fmt.Errorf("Position %d (%s): %w", i+1, crud.Str(item["item_text"]), err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID,
		"message": fmt.Sprintf("Rechnung %s: %d Positionen aus Vertrag %s übernommen – bitte Beträge prüfen", inv.ID, len(items), crud.Str(head["framework_contract"]))}}, nil
}

// amountText: Betrag einer Kondition als Zahl mit Punkt (die Liste liefert ihn formatiert).
func amountText(v any) string {
	s := strings.TrimSpace(crud.Str(v))
	if strings.Contains(s, ",") {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	}
	return s
}
