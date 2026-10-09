package bank

import (
	"context"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt gespeicherte Sachkontonummern auf die reine Nummer um
// (ledger 0.15.0: 1200 statt SKR25-1200; module.Migrator, einmal je Prozess).
func (m *Module) Migrate(ctx context.Context) error {
	return ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
		ledgerapi.AccountColumn{Table: "bank__account", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__transaction", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__rule", Column: "gl_account"},
	)
}

// Kontenplanwechsel des Buchungskreises (Hook ledger.chart_change): gespeicherte
// Sachkonten prüfen (check) und nach der Zuordnung umstellen (commit).
const chartChangeCallback = "BankChartChange"

var chartColumns = []ledgerapi.CompanyAccountColumn{
	{Table: "bank__account", Column: "gl_account"},
	{Table: "bank__transaction", Column: "gl_account"},
	{Table: "bank__rule", Column: "gl_account"},
}

func (m *Module) registerChartChange(r *module.Router) {
	hook.Handle(r, chartChangeCallback, func(ctx context.Context, req hook.Request) (hook.Response, error) {
		return ledgerapi.ChartChangeHandler(m.db, m.log, "Bank", chartColumns...)(ctx, req)
	})
}
