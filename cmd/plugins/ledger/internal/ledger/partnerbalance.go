package ledger

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Partnersalden (seit 0.20.0): Ist das Konto eines Debitors bzw. Kreditors
// ausgeglichen? Je Partner und Abstimmkonto Soll, Haben und Saldo bis zum
// Stichtag – aus dem Universal Journal (gebuchte Belege) und auf Wunsch den
// offenen Vorerfassungen (Sollstellungen, die noch nicht gebucht sind).
//
//   - Aktion „Partnersalden …“ am Buchungskreis (Einstellungen → Buchungskreise):
//     Tabelle, wahlweise nur nicht ausgeglichene Partner.
//   - Service PartnerBalance.list {company_code, date_to?, include_drafts?, open_only?}
//     z. B. console handle PartnerBalance.list --company_code=2000.
//
// Partner ist sd_customer_id (Debitoren) bzw. supplier_id (Kreditoren) der
// Positionen auf Konten mit Abstimmkonto CUSTOMER bzw. SUPPLIER. Saldo > 0:
// Soll-Saldo (Forderung an den Partner bzw. Guthaben beim Kreditor), < 0:
// Haben-Saldo (Verbindlichkeit bzw. Guthaben des Debitors).

const partnerBalanceObject = "PartnerBalance"

type partnerBalance struct {
	Partner, Name, Kind, Account, Currency string
	Debit, Credit                          int64
	Drafts                                 int64 // Anteil aus offenen Vorerfassungen (Soll − Haben)
}

var partnerBalanceFields = []crud.Field{
	{Key: "pb_date_to", Label: "Stichtag (leer = heute bzw. Stichtag des Benutzers)", Type: tDate, ActionOnly: true},
	{Key: "pb_include_drafts", Label: "Offene Vorerfassungen einbeziehen (z. B. noch nicht gebuchte Sollstellungen)", Type: tBool, ActionOnly: true},
	{Key: "pb_open_only", Label: "Nur nicht ausgeglichene Partner", Type: tBool, ActionOnly: true},
}

func (m *Module) partnerBalanceAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "partnerBalances", Label: "Partnersalden …", Record: true,
		Fields: []string{"pb_date_to", "pb_include_drafts", "pb_open_only"}}, Handle: m.partnerBalancesAction}
}

func (m *Module) partnerBalancesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			DateTo   string `json:"pb_date_to"`
			Drafts   any    `json:"pb_include_drafts"`
			OpenOnly any    `json:"pb_open_only"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	return m.partnerBalanceResponse(ctx, strings.TrimSpace(in.ID), in.Data.DateTo, isTrue(in.Data.Drafts), isTrue(in.Data.OpenOnly), true)
}

// partnerBalanceList: Service PartnerBalance.list (Konsole, andere Module).
func (m *Module) partnerBalanceList(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	if inner, ok := q["query"].(map[string]any); ok {
		q = inner
	}
	drafts := q["include_drafts"] == nil || isTrue(q["include_drafts"])
	return m.partnerBalanceResponse(ctx, strings.TrimSpace(crud.Str(q["company_code"])), crud.Str(q["date_to"]), drafts, isTrue(q["open_only"]), false)
}

func isTrue(v any) bool { return v == true || strings.EqualFold(crud.Str(v), "true") }

func (m *Module) partnerBalanceResponse(ctx context.Context, cc, dateTo string, drafts, openOnly, table bool) (sdk.Response, error) {
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis ist Pflicht")
	}
	ok, err := sdk.Authorize(ctx, entryObject, metamodel.ActionRead, sdk.Attrs{sdk.AttrCompanyCode: cc})
	if err != nil {
		return sdk.Response{}, err
	}
	if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung für Salden im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	to := crud.KeyDate(ctx)
	if strings.TrimSpace(dateTo) != "" {
		if to, err = crud.ParseDate(dateTo); err != nil {
			return sdk.Response{}, err
		}
	}
	rows, err := m.partnerBalances(ctx, cc, to, drafts)
	if err != nil {
		return sdk.Response{}, err
	}
	open, total := 0, 0
	items := []map[string]any{}
	var tbl [][]any
	for _, b := range rows {
		d, _ := m.cur.decimals(ctx, b.Currency)
		saldo := b.Debit - b.Credit
		total++
		if saldo != 0 {
			open++
		} else if openOnly {
			continue
		}
		status := balanceStatus(b.Kind, saldo)
		items = append(items, map[string]any{"partner_id": b.Partner, "name": b.Name, "kind": b.Kind, "account_number": b.Account,
			"currency": b.Currency, "debit": formatAmount(b.Debit, d), "credit": formatAmount(b.Credit, d), "balance": formatAmount(saldo, d),
			"balance_minor": saldo, "from_drafts": formatAmount(b.Drafts, d), "status": status})
		tbl = append(tbl, []any{b.Partner, b.Name, b.Kind, b.Account, formatAmount(b.Debit, d), formatAmount(b.Credit, d), formatAmount(saldo, d), status})
	}
	msg := fmt.Sprintf("Buchungskreis %s, Stichtag %s: %d Partner, davon %d nicht ausgeglichen", cc, to, total, open)
	if drafts {
		msg += " (mit offenen Vorerfassungen)"
	} else {
		msg += " (nur gebuchte Belege)"
	}
	out := map[string]any{"items": items, "message": msg, "open": open, "partners": total}
	if table {
		out["table"] = map[string]any{"columns": []string{"Partner", "Name", "Art", "Konto", "Soll", "Haben", "Saldo", "Status"}, "rows": tbl}
	}
	return sdk.Response{Payload: out}, nil
}

func balanceStatus(kind string, saldo int64) string {
	switch {
	case saldo == 0:
		return "ausgeglichen"
	case kind == "Debitor" && saldo > 0:
		return "offene Forderung"
	case kind == "Debitor":
		return "Guthaben des Debitors"
	case saldo < 0:
		return "offene Verbindlichkeit"
	}
	return "Guthaben beim Kreditor"
}

// partnerBalances: Summen je Partner und Abstimmkonto bis Stichtag to.
func (m *Module) partnerBalances(ctx context.Context, cc, to string, drafts bool) ([]partnerBalance, error) {
	acc := map[string]partnerBalance{}
	add := func(partner, kind, account, cur string, amount int64, draft bool) {
		if partner == "" {
			partner = "(ohne Partner)"
		}
		k := partner + "|" + account
		b := acc[k]
		b.Partner, b.Kind, b.Account, b.Currency = partner, kind, account, cur
		if amount > 0 {
			b.Debit += amount
		} else {
			b.Credit -= amount
		}
		if draft {
			b.Drafts += amount
		}
		acc[k] = b
	}
	kindOf := map[string]string{"CUSTOMER": "Debitor", "SUPPLIER": "Kreditor"}
	res, err := m.db.Query(ctx, `SELECT c.reconciliation_type, i.account_number, i.sd_customer_id, i.supplier_id, i.local_currency,
			SUM(CASE WHEN i.amount_local_curr > 0 THEN i.amount_local_curr ELSE 0 END), SUM(CASE WHEN i.amount_local_curr < 0 THEN i.amount_local_curr ELSE 0 END)
		FROM ledger__journal_item i
		JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		WHERE i.company_code_id = ? AND c.reconciliation_type IN ('CUSTOMER', 'SUPPLIER') AND i.posting_date <= ?
		  AND i.ledger = (SELECT leading_ledger FROM ledger__company_config g WHERE g.company_code_id = i.company_code_id)
		GROUP BY c.reconciliation_type, i.account_number, i.sd_customer_id, i.supplier_id, i.local_currency`, cc, to)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		kind := kindOf[crud.Str(r[0])]
		partner := crud.Str(r[2])
		if kind == "Kreditor" || partner == "" {
			partner = orDefault(crud.Str(r[3]), partner)
		}
		add(partner, kind, crud.Str(r[1]), crud.Str(r[4]), toInt(r[5]), false)
		add(partner, kind, crud.Str(r[1]), crud.Str(r[4]), toInt(r[6]), false)
	}
	if drafts {
		cfg, err := m.config(ctx, cc)
		if err != nil {
			return nil, err
		}
		// Betrag der Vorerfassung ist Dezimaltext in Belegwährung (hier: Hauswährung des Buchungskreises)
		res, err := m.db.Query(ctx, `SELECT c.reconciliation_type, i.account_number, i.sd_customer_id, i.supplier_id, i.shkzg, i.amount
			FROM ledger__draft_item i JOIN ledger__draft_header h ON h.id = i.draft_id
			JOIN ledger__account_company c ON c.company_code_id = h.company_code_id AND c.account_number = i.account_number
			WHERE h.company_code_id = ? AND h.status = ? AND c.reconciliation_type IN ('CUSTOMER', 'SUPPLIER') AND h.posting_date <= ?`,
			cc, draftOpen, to)
		if err != nil {
			return nil, err
		}
		dec, _ := m.cur.decimals(ctx, cfg.Currency)
		for _, r := range res.Rows {
			kind := kindOf[crud.Str(r[0])]
			partner := crud.Str(r[2])
			if kind == "Kreditor" || partner == "" {
				partner = orDefault(crud.Str(r[3]), partner)
			}
			amount, err := parseAmount(crud.Str(r[5]), dec)
			if err != nil {
				return nil, err
			}
			if crud.Str(r[4]) == "H" {
				amount = -amount
			}
			add(partner, kind, crud.Str(r[1]), cfg.Currency, amount, true)
		}
	}
	out := make([]partnerBalance, 0, len(acc))
	names := map[string]string{}
	for _, b := range acc {
		if _, ok := names[b.Partner]; !ok {
			names[b.Partner] = m.partnerName(ctx, b.Partner)
		}
		b.Name = names[b.Partner]
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name+out[i].Partner < out[j].Name+out[j].Partner
	})
	return out, nil
}

// partnerName: Name aus dem Partnermodul (leer, wenn es nicht läuft).
func (m *Module) partnerName(ctx context.Context, id string) string {
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(bp.Name2 + " " + bp.Name1))
}
