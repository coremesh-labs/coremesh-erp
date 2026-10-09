package bank

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
		ledgerapi.AccountColumn{Table: "bank__account", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__transaction", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__rule", Column: "gl_account"},
	)
}
