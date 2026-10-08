package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Partnerkonto: Jede Finanzrolle eines Geschäftspartners hat eigene
// Buchungskreisdaten mit Abstimmkonto (Partnermodul, PartnerCompanyCode).
// Verlangt die Vertragsart ein Partnerkonto (partner_account_required),
// braucht der Vertragspartner – und ein abweichender Zahler – in der Rolle
// der Vertragsart Buchungskreisdaten im Buchungskreis des Vertrags; das Konto
// muss im Hauptbuch ein Abstimmkonto passender Art sein (Debitor-Rolle →
// CUSTOMER, Kreditor-Rolle → SUPPLIER). Die Sollstellung bucht auf dieses Konto.

// roleKind: CUSTOMER (Debitor-Rolle), SUPPLIER (Kreditor-Rolle) oder "" (keine Finanzrolle).
func (m *Module) roleKind(ctx context.Context, role string) (string, error) {
	resp, err := m.services.Call(ctx, "PartnerRoleType", "list", map[string]any{"query": map[string]any{}})
	if err != nil {
		return "", unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Code       string `json:"code"`
			IsDebitor  bool   `json:"is_debitor"`
			IsCreditor bool   `json:"is_creditor"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if it.Code != role {
			continue
		}
		switch {
		case it.IsDebitor:
			return "CUSTOMER", nil
		case it.IsCreditor:
			return "SUPPLIER", nil
		}
		return "", nil
	}
	return "", crud.Invalid("Rolle %s gibt es im Partnermodul nicht", role)
}

// requireFinanceRole: Verlangt die Vertragsart ein Partnerkonto, muss ihre
// Rolle eine Finanzrolle sein.
func (m *Module) requireFinanceRole(ctx context.Context, role string) error {
	kind, err := m.roleKind(ctx, role)
	if err != nil {
		return err
	}
	if kind == "" {
		return crud.Invalid("Rolle %s ist keine Finanzrolle (Partnermodul → Rollentypen: Debitor bzw. Kreditor) – "+
			"sonst „Partnerkonto im Buchungskreis Pflicht“ abschalten", role)
	}
	return nil
}

// requirePartnerAccount: Buchungskreisdaten des Partners in der Rolle, mit
// Abstimmkonto passender Art im Hauptbuch. what = „Vertragspartner“, „Zahler“ …
func (m *Module) requirePartnerAccount(ctx context.Context, cc, partner, role, what string) error {
	kind, err := m.roleKind(ctx, role)
	if err != nil {
		return err
	}
	if kind == "" {
		return m.requireFinanceRole(ctx, role)
	}
	resp, err := m.services.Call(ctx, "PartnerCompanyCode", "list", map[string]any{"query": map[string]any{
		"bp_id": partner, "company_code": cc, "role_code": role}})
	if err != nil {
		return unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Account string `json:"reconciliation_account"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return err
	}
	if len(out.Items) == 0 || out.Items[0].Account == "" {
		return crud.Invalid("%s %s hat in der Rolle %s keine Buchungskreisdaten im Buchungskreis %s (Abstimmkonto) – "+
			"im Partnermodul unter „Buchungskreisdaten“ ergänzen", what, m.partnerLabel(ctx, partner), role, cc)
	}
	acc := out.Items[0].Account
	recon, err := m.reconciliationType(ctx, cc, acc)
	if err != nil {
		return err
	}
	if recon != kind {
		return crud.Invalid("%s %s: Konto %s ist im Hauptbuch kein Abstimmkonto %s (Sachkonto im Buchungskreis %s: %s)",
			what, m.partnerLabel(ctx, partner), acc, kind, cc, recon)
	}
	return nil
}

// reconciliationType: Abstimmkontoart des Sachkontos im Buchungskreis (NONE, CUSTOMER, SUPPLIER, ASSET).
func (m *Module) reconciliationType(ctx context.Context, cc, number string) (string, error) {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if err != nil {
		return "", unavailable("Hauptbuch", err)
	}
	var out struct {
		Items []struct {
			AccountNumber      string `json:"account_number"`
			ReconciliationType string `json:"reconciliation_type"`
			IsBlocked          bool   `json:"is_blocked"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if sameAccount(it.AccountNumber, number) {
			if it.IsBlocked {
				return "", crud.Invalid("Abstimmkonto %s ist im Buchungskreis %s gesperrt", number, cc)
			}
			return it.ReconciliationType, nil
		}
	}
	return "", crud.Invalid("Abstimmkonto %s gibt es im Buchungskreis %s nicht", number, cc)
}

// partnerLabel: „Müller (7cb5…)“ bzw. die ID, wenn der Name fehlt.
func (m *Module) partnerLabel(ctx context.Context, id string) string {
	if name := m.partnerName(ctx, id); name != "" && name != id {
		return name + " (" + id + ")"
	}
	return id
}
