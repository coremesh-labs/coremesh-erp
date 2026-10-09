package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/bpref"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate (module.Migrator, einmal je Prozess) stellt Verweise auf
// Geschäftspartner von GUIDs (partner bis 0.9.0) auf BP-Nummern um und
// gespeicherte Sachkontonummern auf die reine Nummer (ledger 0.15.0: 1200
// statt SKR25-1200).
func (m *Module) Migrate(ctx context.Context) error {
	if err := bpref.Remap(ctx, m.db, m.services, m.log,
		bpref.Column{Table: "contract__contract", Column: "partner_id"},
		bpref.Column{Table: "contract__partner", Column: "partner_id"},
		bpref.Column{Table: "contract__condition", Column: "payer_id"},
	); err != nil {
		return err
	}
	acc := func(table, col string) ledgerapi.AccountColumn {
		return ledgerapi.AccountColumn{Table: table, Column: col}
	}
	return ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
		acc("contract__contract_type", "reconciliation_account"), acc("contract__account", "account_number"),
		acc("contract__condition", "account_number"), acc("contract__posting", "account_number"),
		acc("contract__loan", "loan_account"), acc("contract__loan", "interest_account"),
		acc("contract__settlement", "reserve_account"), acc("contract__settlement", "withdrawal_account"),
		acc("contract__settlement_item", "account_number"), acc("contract__bank_account", "gl_account"),
	)
}

// Kontenplanwechsel des Buchungskreises (Hook ledger.chart_change): gespeicherte
// Sachkonten prüfen (check) und nach der Zuordnung umstellen (commit).
const chartChangeCallback = "ContractChartChange"

var chartColumns = []ledgerapi.CompanyAccountColumn{
	{Table: "contract__contract_type", Column: "reconciliation_account"},
	{Table: "contract__account", Column: "account_number"},
	{Table: "contract__condition", Column: "account_number"},
	{Table: "contract__posting", Column: "account_number"},
	{Table: "contract__loan", Column: "loan_account"},
	{Table: "contract__loan", Column: "interest_account"},
	{Table: "contract__settlement", Column: "reserve_account"},
	{Table: "contract__settlement", Column: "withdrawal_account"},
	{Table: "contract__settlement_item", Column: "account_number"},
	{Table: "contract__bank_account", Column: "gl_account"},
}

func (m *Module) registerChartChange(r *module.Router) {
	hook.Handle(r, chartChangeCallback, func(ctx context.Context, req hook.Request) (hook.Response, error) {
		return ledgerapi.ChartChangeHandler(m.db, m.log, "Verträge", chartColumns...)(ctx, req)
	})
}
