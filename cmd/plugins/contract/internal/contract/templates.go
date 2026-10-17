package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Rechnungen aus Vorlagen (seit 0.19.0; seit 0.26.0 nach dem Abrechnungsweg der
// Vertragsart): Vorlagen sind Konditionen ohne Sollstellung – alle bei
// „Eingangsrechnung“, bei „beides“ die mit einer Konditionsart „Rechnungsposition“
// (templateSQL). Sachkonto, Objekt, Betrag als Vorschlag, Bemerkung als Text.
// „Eingangsrechnung erfassen …“ legt über die Beschaffung (SupplierInvoice.fromTemplate)
// eine offene Rechnung mit den gewählten Vorlagen als Positionen an.

// templateChoice ist eine Vorlage am Vertrag.
type templateChoice struct{ Value, Label string }

// templates: heute gültige Vorlagen eines Vertrags (Wert Konditionsart|Objekt|Position;
// Position 1 und leeres Objekt entfallen am Ende).
func (m *Module) templates(ctx context.Context, cc, contract string) ([]templateChoice, error) {
	today := crud.KeyDate(ctx)
	res, err := m.db.Query(ctx, `SELECT k.condition_type, COALESCE(k.object_id, ''), t.name, k.account_number, k.amount, k.note, k.position
		FROM contract__condition k JOIN contract__condition_type t ON t.company_code = k.company_code AND t.code = k.condition_type`+templateJoins+`
		WHERE k.company_code = ? AND k.contract_id = ? AND `+templateSQL+` AND k.valid_from <= ? AND k.valid_to >= ?
		ORDER BY t.sort_order, k.condition_type, k.object_id, k.position`, cc, contract, today, today)
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
		if o, p := crud.Str(r[1]), toInt(r[6]); o != "" || p > 1 {
			v += "|" + o
			if p > 1 {
				v += fmt.Sprintf("|%d", p)
			}
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
	if len(ts) > 0 && req.Values["inv_template"] == "" {
		all := make([]string, len(ts)) // Vorschlag: alle Vorlagen (z. B. Entgelt und Material)
		for i, t := range ts {
			all[i] = t.Value
		}
		v := strings.Join(all, ",")
		st.Fields["inv_template"] = metamodel.FieldState{Options: opts, Value: &v}
	}
	today := crud.KeyDate(ctx)
	st.Fields["inv_date"] = metamodel.FieldState{Value: &today}
	if len(ts) == 0 {
		st.Message = "Der Vertrag hat heute keine Rechnungsvorlagen: Abrechnungsweg der Vertragsart „Eingangsrechnung“ bzw. bei „beides“ Konditionen mit einer Konditionsart „Rechnungsposition“."
	} else {
		st.Message = "Je gewählter Vorlage eine Position (netto) und die Umsatzsteuer als eigene Zeile; der Rechnungsbetrag laut Beleg gleicht Rundungsdifferenzen dort aus. Die Rechnung entsteht offen in der Beschaffung – dort prüfen, ergänzen, buchen."
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
	if !ct.TemplateInvoices() {
		return sdk.Response{}, crud.Invalid("Vertragsart %s rechnet über Sollstellungen ab (Abrechnungsweg an der Vertragsart: „Eingangsrechnung“ oder „beides“)", ct.Code)
	}
	ts, err := m.templates(ctx, cc, contract)
	if err != nil {
		return sdk.Response{}, err
	}
	var chosen []string
	for _, v := range strings.Split(in.Data.Template, ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if !slices.ContainsFunc(ts, func(t templateChoice) bool { return t.Value == v }) {
			return sdk.Response{}, crud.Invalid("Vorlage %q gibt es am Vertrag %s nicht", v, contract)
		}
		chosen = append(chosen, v)
	}
	if len(chosen) == 0 {
		return sdk.Response{}, crud.Invalid("Mindestens eine Vorlage wählen")
	}
	resp, err := m.services.Call(ctx, "SupplierInvoice", "fromTemplate", map[string]any{"company_code": cc, "contract_id": contract,
		"template": strings.Join(chosen, ","), "supplier_reference": strings.TrimSpace(in.Data.Reference), "invoice_date": in.Data.Date, "amount": in.Data.Amount})
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
