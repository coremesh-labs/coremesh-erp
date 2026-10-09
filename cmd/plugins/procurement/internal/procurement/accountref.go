package procurement

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
		ledgerapi.AccountColumn{Table: "procurement__cost_category", Column: "account_number"},
		ledgerapi.AccountColumn{Table: "procurement__invoice_item", Column: "account_number"},
	)
}
