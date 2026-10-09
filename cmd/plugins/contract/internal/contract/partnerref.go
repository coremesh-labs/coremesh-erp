package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/bpref"
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
