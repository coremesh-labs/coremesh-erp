package bank

import (
	"context"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt gespeicherte Sachkontonummern auf die reine Nummer um
// (ledger 0.15.0: 1200 statt SKR25-1200; module.Migrator, einmal je Prozess).
func (m *Module) Migrate(ctx context.Context) error {
	if err := ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
		ledgerapi.AccountColumn{Table: "bank__account", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__transaction", Column: "gl_account"},
		ledgerapi.AccountColumn{Table: "bank__rule", Column: "gl_account"},
	); err != nil {
		return err
	}
	return m.migrateContracts(ctx)
}

// migrateContracts (0.7.0): Gebuchte Umsätze mit Vertrag – oder auf eine
// Eingangsrechnung mit Vertrag – tragen ihn an der Partnerposition nach; bis
// ledger 0.33.2 und bank 0.6.0 fehlte er dort (einmal, Merker bank__migration).
// 0.9.1: Die Migration ledger 0.34.0 lief früher und nahm den Vertrag aus dem
// damaligen, vertragsübergreifenden Ausgleich – teils den falschen; den ersetzt
// jetzt der Vertrag des Bankumsatzes (nur nachgetragene Werte, ledger 0.39.0).
func (m *Module) migrateContracts(ctx context.Context) error {
	refs, err := m.contractRefs(ctx)
	if err != nil {
		return err
	}
	// 0.7.1: neuer Name – 0.7.0 fand das Geschäftsjahr nicht (Belegliste ohne Filter Belegnummer)
	if err := ledgerapi.AddContracts(ctx, m.db, m.services, m.log, "bank__migration", "contract_refs_0.7.1",
		"Migration bank 0.7.0: Vertrag des Bankumsatzes", refs); err != nil {
		return err
	}
	return ledgerapi.CorrectContracts(ctx, m.db, m.services, m.log, "bank__migration", "contract_fix_0.9.1",
		"Migration bank 0.9.1: Vertrag des Bankumsatzes statt des Vertrags aus dem Ausgleich", refs)
}

// contractRefs: Vertrag je gebuchtem Umsatz (eigener oder der Eingangsrechnung).
func (m *Module) contractRefs(ctx context.Context) ([]ledgerapi.ContractRef, error) {
	res, err := m.db.Query(ctx, `SELECT company_code, document_number, COALESCE(contract_id, ''), COALESCE(invoice_id, ''), booking_date FROM bank__transaction
		WHERE status = ? AND COALESCE(document_number, '') <> '' AND (COALESCE(contract_id, '') <> '' OR COALESCE(invoice_id, '') <> '')`, stPosted)
	if err != nil {
		return nil, err
	}
	var refs []ledgerapi.ContractRef
	for _, r := range res.Rows {
		cc, contract, invoice := crud.Str(r[0]), crud.Str(r[2]), crud.Str(r[3])
		if contract == "" {
			inv, err := m.services.Call(ctx, "SupplierInvoice", "get", map[string]any{"id": cc + "|" + invoice})
			if err != nil {
				continue
			}
			contract = field(inv.Payload, "framework_contract")
		}
		date, _ := crud.ParseDate(r[4])
		refs = append(refs, ledgerapi.ContractRef{CompanyCode: cc, Number: crud.Str(r[1]), PostingDate: date, Contract: contract})
	}
	return refs, nil
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
