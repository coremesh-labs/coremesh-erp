package accounting

import (
	"context"
	"strings"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
)

// balanceObject ist der Saldo-Service (ohne Oberfläche, nur JSON-API):
//
//	POST /api/v1/accounting/AccountBalance/list
//	{"company_code": "1000", "date_from": "2026-01-01", "date_to": "2026-12-31"}
//
// Ergebnis ist die Summen- und Saldenliste je Konto und Währung: Summe Soll,
// Summe Haben und Saldo (Soll − Haben; positiv = Soll-Saldo). totals enthält
// je Währung die Gesamtsummen – bei korrekter Buchführung ist Soll = Haben.
const balanceObject = "AccountBalance"

type balanceRow struct {
	Account     string `json:"account_code"`
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	Currency    string `json:"currency"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	Balance     string `json:"balance"`
	BalanceMin  int64  `json:"balance_minor"`
}

type balanceTotal struct {
	Currency string `json:"currency"`
	Debit    string `json:"debit"`
	Credit   string `json:"credit"`
}

func (m *Module) balances(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	if inner, ok := q["query"].(map[string]any); ok {
		q = inner
	}
	where, args := []string{"1 = 1"}, []any{}
	if cc := strings.TrimSpace(crud.Str(q["company_code"])); cc != "" {
		if err := requireCompanyCode(ctx, "list", cc); err != nil {
			return sdk.Response{}, err
		}
		where, args = append(where, "e.company_code = ?"), append(args, cc)
	} else {
		scope, sargs, none, err := companyScope(ctx)
		if err != nil {
			return sdk.Response{}, err
		}
		if none {
			return sdk.Response{Payload: map[string]any{"items": []balanceRow{}, "totals": []balanceTotal{}}}, nil
		}
		if scope != "" {
			where, args = append(where, "e."+scope), append(args, sargs...)
		}
	}
	for key, op := range map[string]string{"date_from": ">=", "date_to": "<="} {
		if v := crud.Str(q[key]); v != "" {
			d, err := crud.ParseDate(v)
			if err != nil {
				return sdk.Response{}, err
			}
			where, args = append(where, "e.posting_date "+op+" ?"), append(args, d)
		}
	}
	res, err := m.db.Query(ctx, `SELECT l.account_code, a.name, a.account_type, e.currency,
			SUM(CASE WHEN l.side = 'D' THEN l.amount_minor ELSE 0 END),
			SUM(CASE WHEN l.side = 'C' THEN l.amount_minor ELSE 0 END)
		FROM ledger__journal_lines l
		JOIN ledger__journal_entries e ON e.id = l.entry_id
		JOIN ledger__accounts a ON a.code = l.account_code
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY l.account_code, a.name, a.account_type, e.currency
		ORDER BY l.account_code, e.currency`, args...)
	if err != nil {
		return sdk.Response{}, err
	}
	items := []balanceRow{}
	sums := map[string][2]int64{}
	var currencies []string
	for _, r := range res.Rows {
		dr, cr := toInt(r[4]), toInt(r[5])
		cur := crud.Str(r[3])
		items = append(items, balanceRow{Account: crud.Str(r[0]), Name: crud.Str(r[1]), AccountType: crud.Str(r[2]), Currency: cur,
			Debit: formatAmount(dr), Credit: formatAmount(cr), Balance: formatAmount(dr - cr), BalanceMin: dr - cr})
		if _, ok := sums[cur]; !ok {
			currencies = append(currencies, cur)
		}
		s := sums[cur]
		sums[cur] = [2]int64{s[0] + dr, s[1] + cr}
	}
	totals := []balanceTotal{}
	for _, cur := range currencies {
		totals = append(totals, balanceTotal{Currency: cur, Debit: formatAmount(sums[cur][0]), Credit: formatAmount(sums[cur][1])})
	}
	return sdk.Response{Payload: map[string]any{"items": items, "totals": totals}}, nil
}
