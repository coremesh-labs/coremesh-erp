package ledger

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// migrate stellt Daten älterer Versionen einmal je Prozess um. Der Host erlaubt
// Datenbankzugriffe nur innerhalb einer Anfrage und die Schema-Migration läuft
// nach Initialize – daher beim ersten Zugriff (Listen, Datensätze, Buchen,
// Konsole). Alle Schritte sind wiederholbar.
//
//	0.8.0  offene Perioden aus ledger__fiscal_period_status
//	0.9.0  Kontonummern <Kontenplan>-<Nummer>, Periodendefinition je
//	       Buchungskreis, Geschäftsjahr/Periode (JJJJPPP), Kontoart je Position
//	       und je Abstimmkonto
//	0.13.0 Belege mit fachlichem Schlüssel (Buchungskreis, Jahr, Nummer),
//	       Belegnummern aus numrange (documents.go)
func (m *Module) migrate(ctx context.Context) error {
	if m.migrated.Load() {
		return nil
	}
	steps := []func(context.Context) error{m.migrateAccounts, m.migratePeriods, m.migrateData, m.migrateDocuments}
	for _, step := range steps {
		if err := step(ctx); err != nil {
			return err
		}
	}
	m.migrated.Store(true)
	return nil
}

func (m *Module) migrateData(ctx context.Context) error {
	for _, q := range []string{
		// Periodendefinition je Buchungskreis aus der Vorlage.
		`INSERT INTO ledger__period_definition (company_code_id, period, name, is_special, calendar_month)
		 SELECT c.company_code_id, p.period, p.name, p.is_special, p.calendar_month
		 FROM ledger__company_config c CROSS JOIN ledger__posting_period p
		 WHERE NOT EXISTS (SELECT 1 FROM ledger__period_definition d WHERE d.company_code_id = c.company_code_id)`,
		// Geschäftsjahr und Periode zusammen (JJJJPPP).
		`UPDATE ledger__journal_entry_header SET fiscal_year_period = fiscal_year * 1000 + posting_period WHERE fiscal_year_period IS NULL`,
		`UPDATE ledger__journal_entry_item SET fiscal_year_period = fiscal_year * 1000 + posting_period WHERE fiscal_year_period IS NULL`,
		// Kontoart je Position aus der Positionsart.
		`UPDATE ledger__journal_entry_item SET account_kind = CASE item_type WHEN 'CUSTOMER' THEN 'D' WHEN 'SUPPLIER' THEN 'K'
		 WHEN 'ASSET' THEN 'A' ELSE 'S' END WHERE account_kind IS NULL`,
		// Abstimmkonten im Kontenplan: Kontoart aus der Zuordnung im Buchungskreis.
		`UPDATE ledger__account_master SET account_kind = (SELECT CASE MAX(ac.reconciliation_type) WHEN 'CUSTOMER' THEN 'D'
		 WHEN 'SUPPLIER' THEN 'K' WHEN 'ASSET' THEN 'A' END FROM ledger__account_company ac
		 WHERE ac.chart_of_accounts_id = ledger__account_master.chart_of_accounts_id AND ac.account_number = ledger__account_master.account_number
		 AND ac.reconciliation_type <> 'NONE')
		 WHERE account_kind = 'S' AND EXISTS (SELECT 1 FROM ledger__account_company ac
		 WHERE ac.chart_of_accounts_id = ledger__account_master.chart_of_accounts_id AND ac.account_number = ledger__account_master.account_number
		 AND ac.reconciliation_type <> 'NONE')`,
	} {
		if _, err := m.db.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// beforeAction stellt vor einer Action die Daten älterer Versionen um.
func (m *Module) beforeAction(h module.HandlerFunc) module.HandlerFunc {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		if err := m.migrate(ctx); err != nil {
			return sdk.Response{}, err
		}
		return h(ctx, req)
	}
}
