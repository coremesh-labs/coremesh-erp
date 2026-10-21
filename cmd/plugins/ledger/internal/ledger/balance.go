package ledger

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
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
//
// Seit 0.30.1: "without_documents_on": "9000-9099" lässt Belege mit einer Zeile auf
// diesen Konten weg (z. B. Eröffnungsbuchungen in der Kapitalflussrechnung).

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
		ok, err := sdk.Authorize(ctx, entryObject, metamodel.ActionRead, sdk.Attrs{sdk.AttrCompanyCode: cc})
		if err != nil {
			return sdk.Response{}, err
		}
		if !ok {
			return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung für Salden im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
		}
		add("i.company_code_id = ?", cc)
	}
	// Salden nur aus Buchungskreisen mit Leserecht JournalEntry.read.
	g, err := sdk.Grants(ctx, entryObject, metamodel.ActionRead)
	if err != nil {
		return sdk.Response{}, err
	}
	sw, sargs := g.SQL(map[string]string{sdk.AttrCompanyCode: "i.company_code_id"})
	where, args = append(where, sw), append(args, sargs...)
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
	if r := strings.Join(strings.Fields(crud.Str(q["without_documents_on"])), ""); r != "" {
		cond, cargs, err := accountRangesSQL("j.account_number", r)
		if err != nil {
			return sdk.Response{}, err
		}
		where = append(where, `NOT EXISTS (SELECT 1 FROM ledger__journal_item j WHERE j.company_code_id = i.company_code_id
			AND j.fiscal_year = i.fiscal_year AND j.document_number = i.document_number AND j.ledger = i.ledger AND (`+cond+`))`)
		args = append(args, cargs...)
	}
	for k, v := range q {
		if slices.Contains(dimColumns, k) && crud.Str(v) != "" {
			add("i."+k+" = ?", crud.Str(v))
		}
	}
	res, err := m.db.Query(ctx, `SELECT i.company_code_id, i.ledger, i.account_number, a.name, a.account_type, i.local_currency,
			SUM(CASE WHEN i.amount_local_curr > 0 THEN i.amount_local_curr ELSE 0 END),
			SUM(CASE WHEN i.amount_local_curr < 0 THEN -i.amount_local_curr ELSE 0 END)
		FROM ledger__journal_item i
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

// accountRangesSQL: Bedingung für Kontenbereiche wie im Bilanzschema ("9000-9099, 1376";
// ein Konto gehört zum Bereich, wenn seine ersten Stellen darin liegen).
func accountRangesSQL(col, ranges string) (string, []any, error) {
	if !fsAccountsRe.MatchString(ranges) {
		return "", nil, crud.Invalid("Konten %q: Bereiche und Nummern mit Komma, z. B. 9000-9099, 1376", ranges)
	}
	var ors []string
	var args []any
	for _, r := range strings.Split(ranges, ",") {
		lo, hi, ok := strings.Cut(r, "-")
		if !ok {
			hi = lo
		}
		if len(lo) != len(hi) {
			return "", nil, crud.Invalid("Konten %q: von und bis gleich lang", r)
		}
		ors = append(ors, "(LENGTH("+col+") >= ? AND SUBSTR("+col+", 1, ?) BETWEEN ? AND ?)")
		args = append(args, len(lo), len(lo), lo, hi)
	}
	return strings.Join(ors, " OR "), args, nil
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
	date := crud.KeyDate(ctx)
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
