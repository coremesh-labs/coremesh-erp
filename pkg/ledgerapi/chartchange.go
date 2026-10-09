package ledgerapi

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Kontenplanwechsel eines Buchungskreises (ledger 0.17.0, console ledger:change-chart).
//
// Das Hauptbuch ruft den Hook ledger.chart_change mit der Zuordnung alte →
// neue Kontonummer auf: in der Phase check meldet jedes Modul Konten, die es
// im Buchungskreis gespeichert hat und die nicht zugeordnet sind (Meldung E
// verhindert den Wechsel); in commit stellt es sie um.
//
//	hook.Handle(r, "ContractChartChange", ledgerapi.ChartChangeHandler(m.db, cols...))
//	ledgerapi.SubscribeChartChange(ctx, m.services, m.log, "ContractChartChange", "Verträge")

// HookChartChange: Name des Hooks.
const HookChartChange = "ledger.chart_change"

// ChartChange: Daten des Hooks.
type ChartChange struct {
	CompanyCode string            `json:"company_code"`
	FromChart   string            `json:"from_chart"`
	ToChart     string            `json:"to_chart"`
	Mapping     map[string]string `json:"mapping"` // alte Nummer → neue Nummer
}

// CompanyAccountColumn: Spalte mit Kontonummern in einer Tabelle mit
// Buchungskreis-Spalte (Company leer = "company_code").
type CompanyAccountColumn struct {
	Table, Column, Company, Where string
}

func (c CompanyAccountColumn) company() string {
	if c.Company == "" {
		return "company_code"
	}
	return c.Company
}

func (c CompanyAccountColumn) where() string {
	w := c.company() + " = ? AND " + c.Column + " IS NOT NULL AND " + c.Column + " <> ''"
	if c.Where != "" {
		w += " AND (" + c.Where + ")"
	}
	return w
}

// UsedAccounts: alle Kontonummern der Spalten im Buchungskreis.
func UsedAccounts(ctx context.Context, db module.DB, cc string, cols ...CompanyAccountColumn) ([]string, error) {
	var out []string
	for _, c := range cols {
		res, err := db.Query(ctx, fmt.Sprintf("SELECT DISTINCT %s FROM %s WHERE %s", c.Column, c.Table, c.where()), cc)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", c.Table, c.Column, err)
		}
		for _, r := range res.Rows {
			if v := fmt.Sprint(r[0]); !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

// RemapAccounts stellt die Spalten im Buchungskreis nach der Zuordnung um und
// liefert die Zahl der geänderten Zeilen.
func RemapAccounts(ctx context.Context, db module.DB, cc string, mapping map[string]string, cols ...CompanyAccountColumn) (int, error) {
	n := 0
	err := db.InTx(ctx, nil, func(ctx context.Context) error {
		// zweistufig über einen Platzhalter, damit Ketten (600 → 4861, 4861 → …) nicht doppelt umstellen
		const mark = "#" // kommt in Kontonummern nicht vor
		for _, c := range cols {
			for from, to := range mapping {
				res, err := db.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s = ? WHERE %s AND %s = ?", c.Table, c.Column, c.where(), c.Column),
					mark+to, cc, from)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", c.Table, c.Column, err)
				}
				n += int(res.RowsAffected)
			}
			if _, err := db.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s = SUBSTR(%s, 2) WHERE %s = ? AND %s LIKE ?", c.Table, c.Column, c.Column,
				c.company(), c.Column), cc, mark+"%"); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// ChartChangeHandler: Abonnent des Hooks für die Kontospalten eines Moduls.
func ChartChangeHandler(db module.DB, log *slog.Logger, module string, cols ...CompanyAccountColumn) hook.Handler {
	return func(ctx context.Context, req hook.Request) (hook.Response, error) {
		var in ChartChange
		if err := req.DecodeData(&in); err != nil {
			return hook.Response{}, err
		}
		used, err := UsedAccounts(ctx, db, in.CompanyCode, cols...)
		if err != nil {
			return hook.Response{}, err
		}
		var missing []string
		for _, a := range used {
			if _, ok := in.Mapping[a]; !ok {
				missing = append(missing, a)
			}
		}
		switch req.Action {
		case hook.PhaseCheck:
			if len(missing) > 0 {
				return hook.Reply(nil, hook.Error("CHART-001", fmt.Sprintf("%s: Konten ohne Zuordnung in Buchungskreis %s: %s",
					module, in.CompanyCode, strings.Join(missing, ", ")))), nil
			}
			return hook.Reply(nil), nil
		case hook.PhaseCommit:
			n, err := RemapAccounts(ctx, db, in.CompanyCode, in.Mapping, cols...)
			if err != nil {
				return hook.Response{}, err
			}
			log.InfoContext(ctx, "Kontenplanwechsel: Konten umgestellt", "company_code", in.CompanyCode, "to_chart", in.ToChart, "rows", n)
			return hook.Reply(nil, hook.Message{Type: hook.TypeSuccess, ID: "CHART-002",
				Text: fmt.Sprintf("%s: %d Einträge auf %s umgestellt", module, n, in.ToChart)}), nil
		}
		return hook.Reply(nil), nil
	}
}

// SubscribeChartChange abonniert den Hook in check und commit (beim Start).
func SubscribeChartChange(ctx context.Context, s module.Services, log *slog.Logger, callback, module string) {
	for _, phase := range []string{hook.PhaseCheck, hook.PhaseCommit} {
		if err := hook.Subscribe(ctx, s, hook.Subscription{Hook: HookChartChange, Phase: phase, Callback: callback, Priority: 50,
			Description: module + ": gespeicherte Sachkonten beim Kontenplanwechsel prüfen und umstellen"}); err != nil {
			log.WarnContext(ctx, "Hook nicht abonniert", "hook", HookChartChange, "err", err.Error())
		}
	}
}
