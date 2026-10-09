package ledgerapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// AccountColumn: Spalte einer eigenen Tabelle mit Sachkontonummern.
type AccountColumn struct {
	Table  string
	Column string
	Where  string // zusätzliche Bedingung, z. B. "source_type = 'LEDGER'"
}

// StripChartPrefix stellt gespeicherte Sachkontonummern auf die reine Nummer
// um (ledger 0.15.0: 1200 statt SKR25-1200). Ein Präfix wird nur entfernt,
// wenn er ein Kontenplan des Hauptbuchs ist. Wiederholbar; ist das Hauptbuch
// nicht erreichbar, bleibt alles, wie es ist (Warnung, nächster Prozessstart).
//
//	func (m *Module) Migrate(ctx context.Context) error {
//		return ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
//			ledgerapi.AccountColumn{Table: "contract__condition", Column: "account_number"})
//	}
func StripChartPrefix(ctx context.Context, db module.DB, s module.Services, log *slog.Logger, cols ...AccountColumn) error {
	resp, err := s.Call(ctx, "ChartOfAccounts", "list", map[string]any{"query": map[string]any{}})
	if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable) {
		log.WarnContext(ctx, "Kontonummern nicht umgestellt – Hauptbuch nicht erreichbar", "err", err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return err
	}
	if len(out.Items) == 0 {
		return nil
	}
	changed := 0
	err = db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, c := range cols {
			for _, ch := range out.Items {
				q := fmt.Sprintf("UPDATE %s SET %s = SUBSTR(%s, ?) WHERE %s LIKE ? AND LENGTH(%s) > ?", c.Table, c.Column, c.Column, c.Column, c.Column)
				if c.Where != "" {
					q += " AND (" + c.Where + ")"
				}
				res, err := db.Exec(ctx, q, len(ch.ID)+2, ch.ID+"-%", len(ch.ID)+1)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", c.Table, c.Column, err)
				}
				changed += int(res.RowsAffected)
			}
		}
		return nil
	})
	if err == nil && changed > 0 {
		log.InfoContext(ctx, "Kontonummern ohne Kontenplan-Präfix", "rows", changed)
	}
	return err
}
