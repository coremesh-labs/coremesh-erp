package contract

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Rechnungen aus Vorlagen (seit 0.19.0): Vertragsart mit template_invoices
// (Vorschlag: RV Rahmenvertrag). Vorlagen sind Konditionen, deren Konditionsart
// „Rechnungsvorlage“ ist (is_template; Vorschlag RT) – Sachkonto, Objekt, Betrag
// als Vorschlag, Bemerkung als Text; sie erzeugen keine Sollstellung.
// „Eingangsrechnung erfassen …“ legt über die Beschaffung (SupplierInvoice.fromTemplate)
// eine offene Rechnung an.

// templateChoice ist eine Vorlage am Vertrag.
type templateChoice struct{ Value, Label string }

// templates: heute gültige Vorlagen eines Vertrags (Wert Konditionsart|Objekt).
func (m *Module) templates(ctx context.Context, cc, contract string) ([]templateChoice, error) {
	today := crud.KeyDate(ctx)
	res, err := m.db.Query(ctx, `SELECT c.condition_type, COALESCE(c.object_id, ''), t.name, c.account_number, c.amount, c.note
		FROM contract__condition c JOIN contract__condition_type t ON t.company_code = c.company_code AND t.code = c.condition_type
		WHERE c.company_code = ? AND c.contract_id = ? AND t.is_template = ? AND c.valid_from <= ? AND c.valid_to >= ?
		ORDER BY t.sort_order, c.condition_type, c.object_id`, cc, contract, true, today, today)
	if err != nil {
		return nil, err
	}
	co, err := m.contractOf(ctx, cc, contract)
	if err != nil {
		return nil, err
	}
	dec := m.currencyDecimals(ctx, co.Currency)
	var out []templateChoice
	for _, r := range res.Rows {
		v := crud.Str(r[0])
		if o := crud.Str(r[1]); o != "" {
			v += "|" + o
		}
		label := firstNonEmpty(crud.Str(r[5]), crud.Str(r[2]))
		if a := crud.Str(r[3]); a != "" {
			label += " – Konto " + a
		}
		if amt := toInt(r[4]); amt != 0 {
			label += " – " + formatAmount(amt, dec) + " " + co.Currency
		}
		if o := crud.Str(r[1]); o != "" {
			label += " – " + o
		}
		out = append(out, templateChoice{v, label})
	}
	return out, nil
}

func (m *Module) templateFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	key, err := m.set.Entity("Contract").ParseID(req.ID)
	if err != nil {
		return st, nil
	}
	ts, err := m.templates(ctx, crud.Str(key["company_code"]), crud.Str(key["contract_id"]))
	if err != nil {
		return st, err
	}
	opts := make([]metamodel.Option, len(ts))
	for i, t := range ts {
		opts[i] = metamodel.Option{Value: t.Value, Label: t.Label}
	}
	st.Fields["inv_template"] = metamodel.FieldState{Options: opts}
	today := crud.KeyDate(ctx)
	st.Fields["inv_date"] = metamodel.FieldState{Value: &today}
	if len(ts) == 0 {
		st.Message = "Der Vertrag hat keine Rechnungsvorlagen (Konditionen mit Konditionsart „Rechnungsvorlage“, z. B. RT)."
	} else {
		st.Message = "Die Rechnung entsteht offen in der Beschaffung – dort prüfen, Positionen ergänzen, Rechnungsbetrag eintragen, buchen."
	}
	return st, nil
}

func (m *Module) invoiceFromTemplateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Template  string `json:"inv_template"`
			Reference string `json:"inv_reference"`
			Date      string `json:"inv_date"`
			Amount    string `json:"inv_amount"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, contract := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	c, err := m.contractOf(ctx, cc, contract)
	if err != nil {
		return sdk.Response{}, err
	}
	ct, err := m.contractTypeOf(ctx, cc, c.Type)
	if err != nil {
		return sdk.Response{}, err
	}
	if !ct.TemplateInvoices {
		return sdk.Response{}, crud.Invalid("Vertragsart %s erlaubt keine Rechnungen aus Vorlagen (Vertragsart: „Eingangsrechnungen aus Vorlagen“)", ct.Code)
	}
	ts, err := m.templates(ctx, cc, contract)
	if err != nil {
		return sdk.Response{}, err
	}
	found := false
	for _, t := range ts {
		found = found || t.Value == in.Data.Template
	}
	if !found {
		return sdk.Response{}, crud.Invalid("Vorlage %q gibt es am Vertrag %s nicht", in.Data.Template, contract)
	}
	resp, err := m.services.Call(ctx, "SupplierInvoice", "fromTemplate", map[string]any{"company_code": cc, "contract_id": contract,
		"template": in.Data.Template, "supplier_reference": strings.TrimSpace(in.Data.Reference), "invoice_date": in.Data.Date, "amount": in.Data.Amount})
	if err != nil {
		return sdk.Response{}, fmt.Errorf("Beschaffung: %w", err)
	}
	return resp, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
