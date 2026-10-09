package opcost

import (
	"context"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
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
