package opcost

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
		ledgerapi.AccountColumn{Table: "opcost__cost_category", Column: "account_number"},
		ledgerapi.AccountColumn{Table: "opcost__definition", Column: "advance_account"},
		ledgerapi.AccountColumn{Table: "opcost__definition", Column: "revenue_account"},
		ledgerapi.AccountColumn{Table: "opcost__journal", Column: "debit_account"},
		ledgerapi.AccountColumn{Table: "opcost__journal", Column: "credit_account"},
		ledgerapi.AccountColumn{Table: "opcost__rule", Column: "source_value", Where: "source_type = 'LEDGER'"},
	)
}

// Kontenplanwechsel des Buchungskreises (Hook ledger.chart_change): gespeicherte
// Sachkonten prüfen (check) und nach der Zuordnung umstellen (commit).
const chartChangeCallback = "OpCostChartChange"

var chartColumns = []ledgerapi.CompanyAccountColumn{
	{Table: "opcost__cost_category", Column: "account_number"},
	{Table: "opcost__definition", Column: "advance_account"},
	{Table: "opcost__definition", Column: "revenue_account"},
	{Table: "opcost__journal", Column: "debit_account"},
	{Table: "opcost__journal", Column: "credit_account"},
	{Table: "opcost__rule", Column: "source_value", Where: "source_type = 'LEDGER'"},
}

func (m *Module) registerChartChange(r *module.Router) {
	hook.Handle(r, chartChangeCallback, func(ctx context.Context, req hook.Request) (hook.Response, error) {
		return ledgerapi.ChartChangeHandler(m.db, m.log, "Betriebskosten", chartColumns...)(ctx, req)
	})
}
