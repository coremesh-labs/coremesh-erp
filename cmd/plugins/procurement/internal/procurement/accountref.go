package procurement

import (
	"context"
	"regexp"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

var _ module.Migrator = (*Module)(nil)

// Migrate stellt gespeicherte Sachkontonummern auf die reine Nummer um
// (ledger 0.15.0: 1200 statt SKR25-1200; module.Migrator, einmal je Prozess).
func (m *Module) Migrate(ctx context.Context) error {
	m.migrateDependentTexts(ctx)
	return ledgerapi.StripChartPrefix(ctx, m.db, m.services, m.log,
		ledgerapi.AccountColumn{Table: "procurement__cost_category", Column: "account_number"},
		ledgerapi.AccountColumn{Table: "procurement__invoice_item", Column: "account_number"},
	)
}

// oldDependentText: Text nachgelagerter Positionen vor procurement 0.12.1 mit Punkt
// („Umsatzsteuer 19 % auf 201.73“).
var oldDependentText = regexp.MustCompile(`^(.* )(\d+(?:\.\d+)?) % auf (-?\d+\.\d+)$`)

// migrateDependentTexts schreibt die Texte nachgelagerter Positionen deutsch
// („Umsatzsteuer 19 % auf 201,73“, wie pricing.Result.Text seit 0.12.1).
func (m *Module) migrateDependentTexts(ctx context.Context) {
	for _, k := range []docKind{invoiceDoc, quoteDoc} {
		res, err := m.db.Query(ctx, `SELECT company_code, `+k.key+`, line_no, item_text FROM `+k.items+` WHERE item_text LIKE ?`, "% auf %.%")
		if err != nil {
			m.log.WarnContext(ctx, "Texte nachgelagerter Positionen nicht umgestellt", "table", k.items, "err", err.Error())
			continue
		}
		for _, row := range res.Rows {
			p := oldDependentText.FindStringSubmatch(crud.Str(row[3]))
			if p == nil {
				continue
			}
			text := p[1] + pricing.FormatDE(p[2]) + " % auf " + pricing.FormatDE(p[3])
			if _, err := m.db.Exec(ctx, `UPDATE `+k.items+` SET item_text = ? WHERE company_code = ? AND `+k.key+` = ? AND line_no = ?`,
				text, row[0], row[1], row[2]); err != nil {
				m.log.WarnContext(ctx, "Text nachgelagerter Position nicht umgestellt", "table", k.items, "err", err.Error())
			}
		}
	}
}

// Kontenplanwechsel des Buchungskreises (Hook ledger.chart_change): gespeicherte
// Sachkonten prüfen (check) und nach der Zuordnung umstellen (commit).
const chartChangeCallback = "ProcurementChartChange"

var chartColumns = []ledgerapi.CompanyAccountColumn{
	{Table: "procurement__cost_category", Column: "account_number"},
	{Table: "procurement__invoice_item", Column: "account_number"},
}

func (m *Module) registerChartChange(r *module.Router) {
	hook.Handle(r, chartChangeCallback, func(ctx context.Context, req hook.Request) (hook.Response, error) {
		return ledgerapi.ChartChangeHandler(m.db, m.log, "Beschaffung", chartColumns...)(ctx, req)
	})
}
