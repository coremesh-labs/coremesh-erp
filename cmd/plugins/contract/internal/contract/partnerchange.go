package contract

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Vertragspartner wechseln (seit 0.38.0): Der Vertragspartner im Kopf (Schuldner der
// Sollstellungen, Empfänger der Nebenkostenabrechnung) ist fest. Scheidet er aus – z. B.
// ein Mitmieter zieht aus –, endet seine Zeitscheibe im Abschnitt „Partner“, und ein
// anderer Partner in der Hauptrolle der Vertragsart, der am Stichtag Vertragspartner
// ist, wird Vertragspartner im Kopf. Schon gebuchte Sollstellungen bleiben beim
// bisherigen Partner (Korrektur über Storno); künftige gehen an den neuen.

const changePartnerAction = "changePartner"

func (m *Module) changePartnerAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Partner string `json:"new_partner_id"`
			Date    string `json:"partner_change_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "update", c.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	partner := strings.TrimSpace(in.Data.Partner)
	date := strings.TrimSpace(in.Data.Date)
	if date == "" {
		date = crud.KeyDate(ctx)
	}
	if date, err = crud.ParseDate(date); err != nil {
		return sdk.Response{}, crud.Invalid("Stichtag: %v", err)
	}
	switch {
	case partner == "":
		return sdk.Response{}, crud.Invalid("Neuer Vertragspartner fehlt")
	case partner == c.Partner:
		return sdk.Response{}, crud.Invalid("%s ist schon Vertragspartner von %s", m.partnerName(ctx, partner), c.ID)
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return sdk.Response{}, err
	}
	if !m.mainPartners(ctx, c, ct.MainRole, date)[partner] {
		return sdk.Response{}, crud.Invalid("%s ist am %s nicht Vertragspartner in der Rolle %s – erst im Abschnitt „Partner“ eintragen",
			m.partnerName(ctx, partner), date, ct.MainRole)
	}
	if ct.AccountRequired {
		if err := m.requirePartnerAccount(ctx, c.CompanyCode, partner, ct.MainRole, "Vertragspartner"); err != nil {
			return sdk.Response{}, err
		}
	}
	old := c.Partner
	if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET partner_id = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND contract_id = ?`,
		partner, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	c.Partner = partner
	m.pushEvent(ctx, c, "update", c.Status)
	msg := fmt.Sprintf("Vertrag %s: Vertragspartner %s statt %s", c.ID, m.partnerName(ctx, partner), m.partnerName(ctx, old))
	if !m.mainPartners(ctx, c, ct.MainRole, date)[old] {
		msg += " (bisheriger Partner am Stichtag ausgeschieden)"
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "partner_id": partner, "message": msg}}, nil
}

// mainPartners: Partner des Vertrags in der Rolle am Stichtag.
func (m *Module) mainPartners(ctx context.Context, c *contractRow, role, date string) map[string]bool {
	out := map[string]bool{}
	res, err := m.db.Query(ctx, `SELECT partner_id FROM contract__partner WHERE company_code = ? AND contract_id = ? AND role_code = ?
		AND valid_from <= ? AND valid_to >= ?`, c.CompanyCode, c.ID, role, date, date)
	if err != nil {
		return out
	}
	for _, r := range res.Rows {
		out[crud.Str(r[0])] = true
	}
	return out
}

// changePartnerFormState: Auswahl = Partner in der Hauptrolle am Stichtag (ohne den bisherigen).
func (m *Module) changePartnerFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	c, err := m.contractOfID(ctx, req.ID)
	if err != nil {
		return st, nil
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return st, nil
	}
	date := req.Values["partner_change_date"]
	if date == "" {
		date = crud.KeyDate(ctx)
		st.Fields["partner_change_date"] = metamodel.FieldState{Value: &date}
	}
	var opts []metamodel.Option
	for p := range m.mainPartners(ctx, c, ct.MainRole, date) {
		if p != c.Partner {
			opts = append(opts, metamodel.Option{Value: p, Label: m.partnerName(ctx, p)})
		}
	}
	sort.Slice(opts, func(i, j int) bool { return opts[i].Label < opts[j].Label })
	st.Fields["new_partner_id"] = metamodel.FieldState{Options: opts}
	st.Message = fmt.Sprintf("Bisher: %s. Zur Wahl stehen die Partner in der Rolle %s am Stichtag (Abschnitt „Partner“); "+
		"gebuchte Sollstellungen bleiben beim bisherigen Partner.", m.partnerName(ctx, c.Partner), ct.MainRole)
	if len(opts) == 0 {
		st.Message += " Kein anderer Partner in dieser Rolle – erst im Abschnitt „Partner“ eintragen."
	}
	return st, nil
}
