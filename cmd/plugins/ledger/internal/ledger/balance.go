package ledger

import (
	"context"
	"slices"
	"strings"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
)

type metamodelOption = metamodel.Option

// AccountBalance.list – Summen- und Saldenliste aus dem Universal Journal
// (Hauswährung), je Buchungskreis, Konto und Ledger:
//
//	{"company_code": "1000", "ledger": "0L", "fiscal_year": 2026, "period_from": 1, "period_to": 12,
//	 "date_from": "…", "date_to": "…", "rent_object_id": "WE-0001-0003", …}
//
// Jede Kontierungsspalte (dimColumns) ist als Filter erlaubt – z. B. Ergebnis je
// Mietobjekt. Saldo = Soll − Haben (positiv = Soll-Saldo).

type balanceRow struct {
	CompanyCode string `json:"company_code"`
	Ledger      string `json:"ledger"`
	Account     string `json:"account_number"`
	Name        string `json:"name"`
	AccountType string `json:"account_type"`
	Currency    string `json:"local_currency"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
	Balance     string `json:"balance"`
	BalanceMin  int64  `json:"balance_minor"`
}

func (m *Module) balances(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	if inner, ok := q["query"].(map[string]any); ok {
		q = inner
	}
	where, args := []string{"1 = 1"}, []any{}
	add := func(cond string, v any) { where, args = append(where, cond), append(args, v) }
	if cc := strings.TrimSpace(crud.Str(q["company_code"])); cc != "" {
		if err := requireCompanyCode(ctx, entryObject, "list", cc); err != nil {
			return sdk.Response{}, err
		}
		add("i.company_code_id = ?", cc)
	} else {
		sw, sargs, none, err := scope(entryObject, "list")(ctx)
		if err != nil {
			return sdk.Response{}, err
		}
		if none {
			return sdk.Response{Payload: map[string]any{"items": []balanceRow{}}}, nil
		}
		if sw != "" {
			where, args = append(where, "i."+sw), append(args, sargs...)
		}
	}
	if l := strings.ToUpper(crud.Str(q["ledger"])); l != "" {
		add("i.ledger = ?", l)
	} else {
		where = append(where, "i.ledger = (SELECT leading_ledger FROM ledger__company_config c WHERE c.company_code_id = i.company_code_id)")
	}
	for key, cond := range map[string]string{"fiscal_year": "i.fiscal_year = ?", "period_from": "i.posting_period >= ?", "period_to": "i.posting_period <= ?"} {
		if v := crud.Str(q[key]); v != "" {
			add(cond, toInt(v))
		}
	}
	for key, cond := range map[string]string{"date_from": "i.posting_date >= ?", "date_to": "i.posting_date <= ?"} {
		if v := crud.Str(q[key]); v != "" {
			d, err := crud.ParseDate(v)
			if err != nil {
				return sdk.Response{}, err
			}
			add(cond, d)
		}
	}
	for k, v := range q {
		if slices.Contains(dimColumns, k) && crud.Str(v) != "" {
			add("i."+k+" = ?", crud.Str(v))
		}
	}
	res, err := m.db.Query(ctx, `SELECT i.company_code_id, i.ledger, i.account_number, a.name, a.account_type, i.local_currency,
			SUM(CASE WHEN i.amount_local_curr > 0 THEN i.amount_local_curr ELSE 0 END),
			SUM(CASE WHEN i.amount_local_curr < 0 THEN -i.amount_local_curr ELSE 0 END)
		FROM ledger__journal_entry_item i
		JOIN ledger__account_master a ON a.chart_of_accounts_id = i.chart_of_accounts_id AND a.account_number = i.account_number
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY i.company_code_id, i.ledger, i.account_number, a.name, a.account_type, i.local_currency
		ORDER BY i.company_code_id, i.ledger, i.account_number`, args...)
	if err != nil {
		return sdk.Response{}, err
	}
	items := []balanceRow{}
	for _, r := range res.Rows {
		cur := crud.Str(r[5])
		d, _ := m.cur.decimals(ctx, cur)
		dr, cr := toInt(r[6]), toInt(r[7])
		items = append(items, balanceRow{CompanyCode: crud.Str(r[0]), Ledger: crud.Str(r[1]), Account: crud.Str(r[2]), Name: crud.Str(r[3]),
			AccountType: crud.Str(r[4]), Currency: cur, Debit: formatAmount(dr, d), Credit: formatAmount(cr, d),
			Balance: formatAmount(dr-cr, d), BalanceMin: dr - cr})
	}
	return sdk.Response{Payload: map[string]any{"items": items}}, nil
}

// CurrencyConversion.convert {amount, from, to, date?, rate_type?} – Umrechnung
// mit den gepflegten Tageskursen (wie beim Buchen).
func (m *Module) convertAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	from, to := strings.ToUpper(crud.Str(q["from"])), strings.ToUpper(crud.Str(q["to"]))
	rateType := strings.ToUpper(crud.Str(q["rate_type"]))
	if rateType == "" {
		rateType = "M"
	}
	date := crud.Today()
	if v := crud.Str(q["date"]); v != "" {
		d, err := crud.ParseDate(v)
		if err != nil {
			return sdk.Response{}, err
		}
		date = d
	}
	fd, err := m.cur.decimals(ctx, from)
	if err != nil {
		return sdk.Response{}, err
	}
	td, err := m.cur.decimals(ctx, to)
	if err != nil {
		return sdk.Response{}, err
	}
	n, err := parseAmount(crud.Str(q["amount"]), fd)
	if err != nil {
		return sdk.Response{}, crud.Invalid("%v", err)
	}
	f, err := m.factor(ctx, rateType, from, to, date)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"amount": formatAmount(n, fd), "from": from,
		"converted": formatAmount(convertMinor(n, fd, td, f), td), "to": to, "date": date, "rate_type": rateType,
		"factor": strings.TrimRight(strings.TrimRight(f.FloatString(10), "0"), ".")}}, nil
}
