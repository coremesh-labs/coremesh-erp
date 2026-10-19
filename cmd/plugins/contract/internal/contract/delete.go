package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Löschen (seit 0.37.0): ein Vertrag, zu dem nichts gebucht ist (z. B. eine
// Fehlerfassung), verschwindet mit allen Unterobjekten. Gebucht heißt: eine
// gebuchte Sollstellung oder Abrechnung, eine offene Vorerfassung dazu, eine
// Position im Hauptbuch mit dem Vertrag (rent_contract_id) oder ein
// zugehöriger Vertrag (Bezugsvertrag). Sonst bleibt nur „Kündigen …“.

// deleteTables: Unterobjekte des Vertrags, Kinder vor den Eltern.
var deleteTables = []string{
	"contract__settlement_item", "contract__settlement", "contract__plan_item", "contract__plan",
	"contract__posting", "contract__posting_run", "contract__loan_payment", "contract__loan", "contract__depreciation",
	"contract__condition", "contract__notice_term", "contract__persons", "contract__bank_account",
	"contract__object", "contract__partner",
}

func (m *Module) deleteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "delete", c.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if reasons, err := m.bookedReasons(ctx, c); err != nil {
		return sdk.Response{}, err
	} else if len(reasons) > 0 {
		return sdk.Response{}, crud.Invalid("Vertrag %s lässt sich nicht löschen: %s – stattdessen „Kündigen …“", c.ID, strings.Join(reasons, "; "))
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range append(deleteTables, "contract__contract") {
			if _, err := m.db.Exec(ctx, "DELETE FROM "+t+" WHERE company_code = ? AND contract_id = ?", c.CompanyCode, c.ID); err != nil {
				return fmt.Errorf("%s: %w", t, err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.pushEvent(ctx, c, "delete", "DELETED")
	return sdk.Response{Payload: map[string]any{"id": in.ID,
		"message": fmt.Sprintf("Vertrag %s „%s“ gelöscht", c.ID, c.Designation)}}, nil
}

// bookedReasons: Gründe, warum der Vertrag nicht gelöscht werden darf.
func (m *Module) bookedReasons(ctx context.Context, c *contractRow) ([]string, error) {
	var out []string
	count := func(q string, args ...any) (int, error) {
		res, err := m.db.Query(ctx, q, args...)
		if err != nil || len(res.Rows) == 0 {
			return 0, err
		}
		return int(toInt(res.Rows[0][0])), nil
	}
	n, err := count(`SELECT COUNT(*) FROM contract__contract WHERE company_code = ? AND parent_contract_id = ?`, c.CompanyCode, c.ID)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		out = append(out, fmt.Sprintf("%d zugehörige Verträge", n))
	}
	for _, t := range []struct{ table, label string }{{"contract__posting", "Sollstellungen"}, {"contract__settlement", "Abrechnungen"}} {
		if n, err = count("SELECT COUNT(*) FROM "+t.table+" WHERE company_code = ? AND contract_id = ? AND (status = ? OR document_number IS NOT NULL)",
			c.CompanyCode, c.ID, postingPosted); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, fmt.Sprintf("%d gebuchte %s", n, t.label))
		}
		// Vorerfassungen: verworfene zählen nicht
		res, err := m.db.Query(ctx, "SELECT DISTINCT draft_id FROM "+t.table+" WHERE company_code = ? AND contract_id = ? AND draft_id IS NOT NULL AND document_number IS NULL",
			c.CompanyCode, c.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			id := crud.Str(r[0])
			resp, err := m.services.Call(ctx, "JournalDraft", "get", map[string]any{"id": id})
			if errors.Is(err, sdk.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, unavailable("Hauptbuch", err)
			}
			var d struct {
				Status string `json:"status"`
			}
			if err := sdk.Decode(resp.Payload, &d); err != nil {
				return nil, err
			}
			if d.Status != "DISCARDED" {
				out = append(out, fmt.Sprintf("Vorerfassung %s (%s) erst verwerfen", id, t.label))
			}
		}
	}
	resp, err := m.services.Call(ctx, "JournalEntryItem", "list", map[string]any{"query": map[string]any{
		"company_code_id": c.CompanyCode, "rent_contract_id": c.ID}, "limit": 1})
	if err != nil {
		return nil, unavailable("Hauptbuch", err)
	}
	var l struct {
		Items []map[string]any `json:"items"`
		Total int              `json:"total"`
	}
	if err := sdk.Decode(resp.Payload, &l); err != nil {
		return nil, err
	}
	if len(l.Items) > 0 {
		out = append(out, "Buchungen im Hauptbuch zum Vertrag")
	}
	return out, nil
}
