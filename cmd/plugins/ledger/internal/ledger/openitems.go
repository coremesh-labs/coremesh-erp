package ledger

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Offene Posten und Ausgleich (seit 0.29.0): Auf Abstimmkonten (Debitoren,
// Kreditoren) ist jede Position ein Posten des Partners. Der Ausgleich ordnet
// Soll- und Habenposten einander zu (ledger__clearing, auch Teilbeträge) – z. B.
// die Zahlung eines Mieters den Sollstellungen eines Monats. Offen ist der
// Betrag eines Postens abzüglich seiner Ausgleiche.
//
// Automatisch nach jedem Buchen und Stornieren für die Partner des Belegs
// (Regeln ohne Fachwissen, in dieser Reihenfolge):
//  1. Storno: Position des Stornobelegs ↔ gleiche Position des Originals.
//  2. Zahlungen (Belegart mit „Zahlungsbeleg“, z. B. DZ, KZ) nach Datum: gleicher
//     Vertrag zuerst; ein Beleg, dessen offene Gegenposten genau den Betrag
//     ergeben (z. B. Kaltmiete + NK + HK eines Monats), sonst ein einzelner
//     Posten mit genau dem Betrag (auch eine Gegenzahlung), sonst die ältesten
//     Gegenposten zuerst (§ 366 Abs. 2 BGB). Andere Posten (z. B. im Vorjahr
//     bezahlte Miete im Eröffnungsbeleg) nur gegen frühere Gegenposten mit
//     genau passendem Betrag.
//
// „Neu zuordnen“ (redo) löscht die automatischen Ausgleiche und rechnet neu;
// Ausgleiche von Hand bleiben.
// Andere Zuordnungen von Hand: „Ausgleichen mit …“ bzw. „Ausgleich aufheben“
// am Einzelposten, Service OpenItem.list/clear/reset/autoClear.

const openItemObject = "OpenItem"

const (
	clearAuto     = "AUTO"
	clearManual   = "MANUAL"
	clearReversal = "REVERSAL"
)

type openItem struct {
	FY                                    int
	Doc                                   string
	Line                                  int
	Date, DocType, Account, Partner, Kind string
	Contract, Object, Text, Currency      string
	Payment                               bool
	Amount, Open                          int64
	ReversedFY                            int
	ReversedDoc                           string
}

func (o *openItem) id(cc string) string { return fmt.Sprintf("%s|%d|%s|%d", cc, o.FY, o.Doc, o.Line) }
func (o *openItem) debit() bool         { return o.Amount > 0 }

// parseItemID: <Buchungskreis>|<Jahr>|<Beleg>|<Zeile>
func parseItemID(id string) (cc string, fy int, doc string, line int, err error) {
	p := strings.Split(strings.TrimSpace(id), "|")
	if len(p) == 5 { // Schlüssel des Einzelpostens mit Ledger: cc|Jahr|Beleg|Ledger|Zeile
		p = []string{p[0], p[1], p[2], p[4]}
	}
	if len(p) != 4 {
		return "", 0, "", 0, crud.Invalid("Posten %q: <Buchungskreis>|<Jahr>|<Beleg>|<Zeile> erwartet", id)
	}
	fy, err1 := strconv.Atoi(p[1])
	line, err2 := strconv.Atoi(p[3])
	if err1 != nil || err2 != nil {
		return "", 0, "", 0, crud.Invalid("Posten %q: Jahr und Zeile sind Zahlen", id)
	}
	return p[0], fy, p[2], line, nil
}

// partnerExpr: Partner der Position je Abstimmkontoart.
const partnerExpr = `CASE WHEN c.reconciliation_type = 'SUPPLIER' THEN COALESCE(NULLIF(i.supplier_id, ''), i.sd_customer_id)
	ELSE COALESCE(NULLIF(i.sd_customer_id, ''), i.supplier_id) END`

// clearedExpr: ausgeglichener Betrag einer Position (beide Seiten).
const clearedExpr = `(SELECT COALESCE(SUM(k.amount), 0) FROM ledger__clearing k WHERE k.company_code_id = i.company_code_id AND k.ledger = i.ledger
	AND ((k.debit_fiscal_year = i.fiscal_year AND k.debit_document_number = i.document_number AND k.debit_line = i.line_item_number)
	  OR (k.credit_fiscal_year = i.fiscal_year AND k.credit_document_number = i.document_number AND k.credit_line = i.line_item_number)))`

type itemFilter struct {
	Account, Partner, Contract, DateTo string
	Doc                                *docKey // nur Partner dieses Belegs
	Item                               *openItem
}

// loadItems: Posten auf Abstimmkonten im führenden Ledger, nach Datum.
func (m *Module) loadItems(ctx context.Context, cc, ledger string, f itemFilter) ([]*openItem, error) {
	q := `SELECT i.fiscal_year, i.document_number, i.line_item_number, i.posting_date, h.document_type, COALESCE(t.is_payment, FALSE),
		i.account_number, ` + partnerExpr + `, c.reconciliation_type, COALESCE(i.rent_contract_id, ''), COALESCE(i.rent_object_id, ''),
		COALESCE(i.item_text, ''), i.local_currency, i.amount_local_curr, ` + clearedExpr + `,
		h.reversed_fiscal_year, COALESCE(h.reversed_document_number, '')
		FROM ledger__journal_item i
		JOIN ledger__journal_header h ON h.company_code_id = i.company_code_id AND h.fiscal_year = i.fiscal_year AND h.document_number = i.document_number
		JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		LEFT JOIN ledger__document_type t ON t.code = h.document_type
		WHERE i.company_code_id = ? AND i.ledger = ? AND c.reconciliation_type IN ('CUSTOMER', 'SUPPLIER')`
	args := []any{cc, ledger}
	if f.Account != "" {
		q += " AND i.account_number = ?"
		args = append(args, f.Account)
	}
	if f.Partner != "" {
		q += " AND " + partnerExpr + " = ?"
		args = append(args, f.Partner)
	}
	if f.Contract != "" {
		q += " AND i.rent_contract_id = ?"
		args = append(args, f.Contract)
	}
	if f.DateTo != "" {
		q += " AND i.posting_date <= ?"
		args = append(args, f.DateTo)
	}
	q += " ORDER BY i.posting_date, i.fiscal_year, i.document_number, i.line_item_number"
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]*openItem, 0, len(res.Rows))
	for _, r := range res.Rows {
		o := &openItem{FY: int(toInt(r[0])), Doc: crud.Str(r[1]), Line: int(toInt(r[2])), DocType: crud.Str(r[4]), Payment: isTrue(r[5]) || toInt(r[5]) == 1,
			Account: crud.Str(r[6]), Partner: crud.Str(r[7]), Kind: crud.Str(r[8]), Contract: crud.Str(r[9]), Object: crud.Str(r[10]),
			Text: crud.Str(r[11]), Currency: crud.Str(r[12]), Amount: toInt(r[13]), ReversedFY: int(toInt(r[15])), ReversedDoc: crud.Str(r[16])}
		o.Date, _ = crud.ParseDate(r[3])
		o.Open = abs(o.Amount) - toInt(r[14])
		out = append(out, o)
	}
	return out, nil
}

// clearing: ein Ausgleich zwischen Soll- und Habenposten.
type clearing struct {
	Debit, Credit *openItem
	Amount        int64
	Method        string
}

func (m *Module) writeClearings(ctx context.Context, cc, ledger string, list []clearing) error {
	if len(list) == 0 {
		return nil
	}
	user := sdk.CallFromContext(ctx).UserID
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, c := range list {
			date := c.Debit.Date
			if c.Credit.Date > date {
				date = c.Credit.Date
			}
			// derselbe Paarausgleich ein zweites Mal: Betrag erhöhen
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__clearing (company_code_id, ledger, debit_fiscal_year, debit_document_number, debit_line,
				credit_fiscal_year, credit_document_number, credit_line, amount, clearing_date, method, created_at, created_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (company_code_id, ledger, debit_fiscal_year, debit_document_number, debit_line, credit_fiscal_year, credit_document_number, credit_line)
				DO UPDATE SET amount = ledger__clearing.amount + excluded.amount`,
				cc, ledger, c.Debit.FY, c.Debit.Doc, c.Debit.Line, c.Credit.FY, c.Credit.Doc, c.Credit.Line, c.Amount, date, c.Method,
				time.Now().UTC().Format(time.RFC3339), nilIfEmpty(user)); err != nil {
				return err
			}
		}
		return nil
	})
}

// pair gleicht zwei Posten um den kleineren offenen Betrag aus.
func pair(out *[]clearing, a, b *openItem, method string) {
	amt := min(a.Open, b.Open)
	if amt <= 0 || a.debit() == b.debit() {
		return
	}
	d, c := a, b
	if !a.debit() {
		d, c = b, a
	}
	*out = append(*out, clearing{Debit: d, Credit: c, Amount: amt, Method: method})
	a.Open -= amt
	b.Open -= amt
}

// autoClear: Regeln 1 und 2 für die Posten eines Partners auf einem Konto.
func autoClear(items []*openItem) []clearing {
	var out []clearing
	byDocLine := map[string]*openItem{}
	for _, o := range items {
		byDocLine[fmt.Sprintf("%d|%s|%d", o.FY, o.Doc, o.Line)] = o
	}
	// 1. Storno ↔ Original
	for _, o := range items {
		if o.ReversedDoc == "" || o.Open == 0 {
			continue
		}
		if orig := byDocLine[fmt.Sprintf("%d|%s|%d", o.ReversedFY, o.ReversedDoc, o.Line)]; orig != nil && orig.Amount == -o.Amount {
			pair(&out, o, orig, clearReversal)
		}
	}
	// 2. Zahlungen nach Datum; andere Posten (z. B. im Vorjahr bezahlte Miete im
	// Eröffnungsbeleg, Guthaben einer Abrechnung) nur gegen frühere Gegenposten
	// mit genau passendem Betrag
	for _, p := range items {
		if p.Open == 0 {
			continue
		}
		var charges, payments []*openItem
		for _, o := range items {
			if o == p || o.Open == 0 || o.debit() == p.debit() || (!p.Payment && o.Date > p.Date) {
				continue
			}
			if o.Payment {
				payments = append(payments, o)
			} else {
				charges = append(charges, o)
			}
		}
		if p.Contract != "" { // gleicher Vertrag zuerst
			same := filterItems(charges, func(o *openItem) bool { return o.Contract == p.Contract })
			if len(same) > 0 {
				charges = same
			}
		}
		if !p.Payment {
			// der ganze Beleg des Postens (z. B. KM + NK + HK eines Monats) gegen
			// einen Gegenbeleg bzw. Gegenposten mit genau diesem Betrag
			own := filterItems(items, func(o *openItem) bool {
				return o.FY == p.FY && o.Doc == p.Doc && o.Open > 0 && o.debit() == p.debit() && o.Contract == p.Contract
			})
			var sum int64
			for _, o := range own {
				sum += o.Open
			}
			against := exactDocument(charges, sum)
			if against == nil {
				if o := exactItem(append(slicesClone(charges), payments...), sum); o != nil {
					against = []*openItem{o}
				}
			}
			for _, a := range own {
				for _, b := range against {
					pair(&out, a, b, clearAuto)
				}
			}
			continue
		}
		if g := exactDocument(charges, p.Open); g != nil {
			for _, o := range g {
				pair(&out, p, o, clearAuto)
			}
			continue
		}
		if o := exactItem(append(slicesClone(charges), payments...), p.Open); o != nil {
			pair(&out, p, o, clearAuto)
			continue
		}
		for _, o := range charges { // älteste zuerst
			if p.Open == 0 {
				break
			}
			pair(&out, p, o, clearAuto)
		}
	}
	return out
}

func filterItems(l []*openItem, keep func(*openItem) bool) []*openItem {
	var out []*openItem
	for _, o := range l {
		if keep(o) {
			out = append(out, o)
		}
	}
	return out
}

func slicesClone(l []*openItem) []*openItem { return append([]*openItem(nil), l...) }

// exactDocument: offene Posten des ältesten Belegs, die zusammen genau amount ergeben.
func exactDocument(l []*openItem, amount int64) []*openItem {
	groups, order := map[string][]*openItem{}, []string{}
	for _, o := range l {
		k := fmt.Sprintf("%d|%s", o.FY, o.Doc)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], o)
	}
	for _, k := range order {
		var sum int64
		for _, o := range groups[k] {
			sum += o.Open
		}
		if sum == amount && len(groups[k]) > 1 {
			return groups[k]
		}
	}
	return nil
}

func exactItem(l []*openItem, amount int64) *openItem {
	for _, o := range l {
		if o.Open == amount {
			return o
		}
	}
	return nil
}

// clearPartners: automatischer Ausgleich für alle Partner/Konten (oder die eines Belegs).
func (m *Module) clearPartners(ctx context.Context, cc string, doc *docKey, partner string, redo ...bool) (int, error) {
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return 0, err
	}
	q := `SELECT DISTINCT i.account_number, ` + partnerExpr + ` FROM ledger__journal_item i
		JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		WHERE i.company_code_id = ? AND i.ledger = ? AND c.reconciliation_type IN ('CUSTOMER', 'SUPPLIER')`
	args := []any{cc, cfg.Ledger}
	if doc != nil {
		q += " AND i.fiscal_year = ? AND i.document_number = ?"
		args = append(args, doc.FiscalYear, doc.Number)
	}
	if partner != "" {
		q += " AND " + partnerExpr + " = ?"
		args = append(args, partner)
	}
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res.Rows {
		if crud.Str(r[1]) == "" {
			continue
		}
		if len(redo) > 0 && redo[0] {
			if err := m.dropAutoClearings(ctx, cc, cfg.Ledger, crud.Str(r[0]), crud.Str(r[1])); err != nil {
				return n, err
			}
		}
		items, err := m.loadItems(ctx, cc, cfg.Ledger, itemFilter{Account: crud.Str(r[0]), Partner: crud.Str(r[1])})
		if err != nil {
			return n, err
		}
		list := autoClear(items)
		if err := m.writeClearings(ctx, cc, cfg.Ledger, list); err != nil {
			return n, err
		}
		n += len(list)
	}
	return n, nil
}

// dropAutoClearings: automatische Ausgleiche eines Partners auf einem Konto löschen.
func (m *Module) dropAutoClearings(ctx context.Context, cc, ledger, account, partner string) error {
	_, err := m.db.Exec(ctx, `DELETE FROM ledger__clearing WHERE company_code_id = ? AND ledger = ? AND method = ?
		AND EXISTS (SELECT 1 FROM ledger__journal_item i JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		  WHERE i.company_code_id = ledger__clearing.company_code_id AND i.ledger = ledger__clearing.ledger AND i.fiscal_year = ledger__clearing.debit_fiscal_year
		  AND i.document_number = ledger__clearing.debit_document_number AND i.line_item_number = ledger__clearing.debit_line
		  AND i.account_number = ? AND `+partnerExpr+` = ?)`, cc, ledger, clearAuto, account, partner)
	return err
}

// clearAfterPost: nach Buchen/Stornieren – Fehler nur protokollieren.
func (m *Module) clearAfterPost(ctx context.Context, docID string) {
	key, err := parseDocID(docID)
	if err != nil {
		return
	}
	// Storno: Ausgleiche des Originals lösen – seine Gegenposten sind wieder offen,
	// das Original gleicht danach der Stornobeleg aus.
	if res, err := m.db.Query(ctx, `SELECT h.reversed_fiscal_year, h.reversed_document_number, g.leading_ledger FROM ledger__journal_header h
		JOIN ledger__company_config g ON g.company_code_id = h.company_code_id
		WHERE h.company_code_id = ? AND h.fiscal_year = ? AND h.document_number = ? AND h.reversed_document_number IS NOT NULL`,
		key.CompanyCode, key.FiscalYear, key.Number); err == nil && len(res.Rows) > 0 {
		fy, doc, ledger := toInt(res.Rows[0][0]), crud.Str(res.Rows[0][1]), crud.Str(res.Rows[0][2])
		if _, err := m.db.Exec(ctx, `DELETE FROM ledger__clearing WHERE company_code_id = ? AND ledger = ? AND method <> ?
			AND ((debit_fiscal_year = ? AND debit_document_number = ?) OR (credit_fiscal_year = ? AND credit_document_number = ?))`,
			key.CompanyCode, ledger, clearReversal, fy, doc, fy, doc); err != nil {
			m.log.WarnContext(ctx, "Ausgleich des stornierten Belegs nicht gelöst", "document", docID, "err", err.Error())
		}
	}
	if _, err := m.clearPartners(ctx, key.CompanyCode, &key, ""); err != nil {
		m.log.WarnContext(ctx, "Ausgleich offener Posten fehlgeschlagen", "document", docID, "err", err.Error())
	}
}

// --- Service OpenItem -------------------------------------------------------------

func (m *Module) openItemAuthorized(ctx context.Context, cc string, write bool) error {
	if cc == "" {
		return crud.Invalid("Buchungskreis ist Pflicht")
	}
	act := metamodel.ActionRead
	if write {
		act = "post"
	}
	ok, err := sdk.Authorize(ctx, entryObject, act, sdk.Attrs{sdk.AttrCompanyCode: cc})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für offene Posten im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	return nil
}

// openItemList: OpenItem.list {company_code, partner_id?, account_number?, contract_id?,
// date_to?, open_only?, kind? (CUSTOMER, SUPPLIER)} – Posten mit offenem Betrag, Gegenkonto (Sachkonto der
// Gegenbuchung im Beleg) und den Ausgleichen (Gegenposten, Betrag, Datum der
// Gegenposition, Zahlung ja/nein).
func (m *Module) openItemList(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	if inner, ok := q["query"].(map[string]any); ok {
		q = inner
	}
	cc := strings.TrimSpace(crud.Str(q["company_code"]))
	if err := m.openItemAuthorized(ctx, cc, false); err != nil {
		return sdk.Response{}, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	f := itemFilter{Account: crud.Str(q["account_number"]), Partner: crud.Str(q["partner_id"]), Contract: crud.Str(q["contract_id"])}
	if d := strings.TrimSpace(crud.Str(q["date_to"])); d != "" {
		if f.DateTo, err = crud.ParseDate(d); err != nil {
			return sdk.Response{}, err
		}
	}
	items, err := m.loadItems(ctx, cc, cfg.Ledger, f)
	if err != nil {
		return sdk.Response{}, err
	}
	if k := strings.ToUpper(strings.TrimSpace(crud.Str(q["kind"]))); k != "" {
		items = filterItems(items, func(o *openItem) bool { return o.Kind == k })
	}
	if isTrue(q["open_only"]) {
		items = filterItems(items, func(o *openItem) bool { return o.Open != 0 })
	}
	offsets, err := m.offsetAccounts(ctx, cc, cfg.Ledger, items)
	if err != nil {
		return sdk.Response{}, err
	}
	clr, err := m.clearingsOf(ctx, cc, cfg.Ledger, items)
	if err != nil {
		return sdk.Response{}, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, o := range items {
		d, _ := m.cur.decimals(ctx, o.Currency)
		k := fmt.Sprintf("%d|%s|%d", o.FY, o.Doc, o.Line)
		out = append(out, map[string]any{"id": o.id(cc), "fiscal_year": o.FY, "document_number": o.Doc, "line_item_number": o.Line,
			"posting_date": o.Date, "document_type": o.DocType, "payment": o.Payment, "account_number": o.Account, "partner_id": o.Partner, "kind": o.Kind,
			"contract_id": o.Contract, "rent_object_id": o.Object, "item_text": o.Text, "currency": o.Currency,
			"amount_minor": o.Amount, "open_minor": o.Open, "amount": formatAmount(o.Amount, d), "open": formatAmount(o.Open, d),
			"offset_account": offsets[k], "clearings": orEmpty(clr[k])})
	}
	return sdk.Response{Payload: map[string]any{"items": out, "message": fmt.Sprintf("%d Posten", len(out))}}, nil
}

func orEmpty(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

// offsetAccounts: Sachkonto der Gegenbuchung je Posten – im selben Beleg die
// Position außerhalb der Abstimmkonten mit gleichem Text und Gegenbetrag, sonst
// mit gleichem Text, sonst die größte desselben Vertrags, sonst die einzige.
func (m *Module) offsetAccounts(ctx context.Context, cc, ledger string, items []*openItem) (map[string]string, error) {
	out := map[string]string{}
	if len(items) == 0 {
		return out, nil
	}
	type gl struct {
		Account, Text, Contract string
		Amount                  int64
	}
	docs := map[string][]gl{}
	need := map[string]bool{}
	for _, o := range items {
		need[fmt.Sprintf("%d|%s", o.FY, o.Doc)] = true
	}
	years := map[int]bool{}
	for _, o := range items {
		years[o.FY] = true
	}
	for y := range years {
		res, err := m.db.Query(ctx, `SELECT g.document_number, g.account_number, COALESCE(g.item_text, ''), g.amount_local_curr, COALESCE(g.rent_contract_id, '')
			FROM ledger__journal_item g
			LEFT JOIN ledger__account_company c ON c.company_code_id = g.company_code_id AND c.account_number = g.account_number
			WHERE g.company_code_id = ? AND g.ledger = ? AND g.fiscal_year = ? AND COALESCE(c.reconciliation_type, 'NONE') NOT IN ('CUSTOMER', 'SUPPLIER')`,
			cc, ledger, y)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			k := fmt.Sprintf("%d|%s", y, crud.Str(r[0]))
			if need[k] {
				docs[k] = append(docs[k], gl{crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[4]), toInt(r[3])})
			}
		}
	}
	for _, o := range items {
		lines := docs[fmt.Sprintf("%d|%s", o.FY, o.Doc)]
		acc := ""
		for _, g := range lines {
			if g.Text == o.Text && g.Amount == -o.Amount {
				acc = g.Account
				break
			}
		}
		if acc == "" {
			for _, g := range lines {
				if g.Text == o.Text && o.Text != "" {
					acc = g.Account
					break
				}
			}
		}
		if acc == "" && o.Contract != "" { // z. B. Abrechnung: Position desselben Vertrags mit dem größten Betrag
			var best int64
			for _, g := range lines {
				if g.Contract == o.Contract && abs(g.Amount) > best {
					acc, best = g.Account, abs(g.Amount)
				}
			}
		}
		if acc == "" && len(lines) == 1 {
			acc = lines[0].Account
		}
		out[fmt.Sprintf("%d|%s|%d", o.FY, o.Doc, o.Line)] = acc
	}
	return out, nil
}

// clearingsOf: Ausgleiche je Posten mit Gegenposten, Betrag und dessen Buchungsdatum.
func (m *Module) clearingsOf(ctx context.Context, cc, ledger string, items []*openItem) (map[string][]map[string]any, error) {
	out := map[string][]map[string]any{}
	if len(items) == 0 {
		return out, nil
	}
	known := map[string]*openItem{}
	for _, o := range items {
		known[fmt.Sprintf("%d|%s|%d", o.FY, o.Doc, o.Line)] = o
	}
	res, err := m.db.Query(ctx, `SELECT k.debit_fiscal_year, k.debit_document_number, k.debit_line, k.credit_fiscal_year, k.credit_document_number, k.credit_line,
		k.amount, k.clearing_date, k.method, dh.posting_date, ch.posting_date, COALESCE(dt.is_payment, FALSE), COALESCE(ct.is_payment, FALSE)
		FROM ledger__clearing k
		JOIN ledger__journal_header dh ON dh.company_code_id = k.company_code_id AND dh.fiscal_year = k.debit_fiscal_year AND dh.document_number = k.debit_document_number
		JOIN ledger__journal_header ch ON ch.company_code_id = k.company_code_id AND ch.fiscal_year = k.credit_fiscal_year AND ch.document_number = k.credit_document_number
		LEFT JOIN ledger__document_type dt ON dt.code = dh.document_type
		LEFT JOIN ledger__document_type ct ON ct.code = ch.document_type
		WHERE k.company_code_id = ? AND k.ledger = ?`, cc, ledger)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		dk := fmt.Sprintf("%d|%s|%d", toInt(r[0]), crud.Str(r[1]), toInt(r[2]))
		ck := fmt.Sprintf("%d|%s|%d", toInt(r[3]), crud.Str(r[4]), toInt(r[5]))
		date, _ := crud.ParseDate(r[7])
		dd, _ := crud.ParseDate(r[9])
		cd, _ := crud.ParseDate(r[10])
		dPay, cPay := isTrue(r[11]) || toInt(r[11]) == 1, isTrue(r[12]) || toInt(r[12]) == 1
		if known[dk] != nil {
			out[dk] = append(out[dk], map[string]any{"against": cc + "|" + ck, "amount_minor": toInt(r[6]), "clearing_date": date,
				"against_posting_date": cd, "against_payment": cPay, "method": crud.Str(r[8])})
		}
		if known[ck] != nil {
			out[ck] = append(out[ck], map[string]any{"against": cc + "|" + dk, "amount_minor": toInt(r[6]), "clearing_date": date,
				"against_posting_date": dd, "against_payment": dPay, "method": crud.Str(r[8])})
		}
	}
	return out, nil
}

// openItemClear: OpenItem.clear {company_code?, ids: [...]} – die genannten Posten
// (gleiches Konto, gleicher Partner) gegeneinander, älteste zuerst.
func (m *Module) openItemClear(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	ids := splitIDs(q["ids"])
	n, err := m.clearManual(ctx, ids)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%d Ausgleiche gebucht", n)}}, nil
}

func splitIDs(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			out = append(out, crud.Str(x))
		}
	case []string:
		out = t
	default:
		for _, s := range strings.Split(crud.Str(v), ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (m *Module) clearManual(ctx context.Context, ids []string) (int, error) {
	if len(ids) < 2 {
		return 0, crud.Invalid("Mindestens zwei Posten angeben")
	}
	var items []*openItem
	cc, ledger := "", ""
	for _, id := range ids {
		c, fy, doc, line, err := parseItemID(id)
		if err != nil {
			return 0, err
		}
		if cc == "" {
			cc = c
			if err := m.openItemAuthorized(ctx, cc, true); err != nil {
				return 0, err
			}
			cfg, err := m.config(ctx, cc)
			if err != nil {
				return 0, err
			}
			ledger = cfg.Ledger
		} else if c != cc {
			return 0, crud.Invalid("Posten aus verschiedenen Buchungskreisen")
		}
		o, err := m.oneItem(ctx, cc, ledger, fy, doc, line)
		if err != nil {
			return 0, err
		}
		if len(items) > 0 && (o.Account != items[0].Account || o.Partner != items[0].Partner) {
			return 0, crud.Invalid("Ausgleich nur zwischen Posten desselben Partners auf demselben Konto (%s/%s ≠ %s/%s)",
				o.Partner, o.Account, items[0].Partner, items[0].Account)
		}
		items = append(items, o)
	}
	sort.SliceStable(items, func(a, b int) bool { return items[a].Date < items[b].Date })
	var out []clearing
	for _, a := range items {
		for _, b := range items {
			if a.Open == 0 {
				break
			}
			if a.debit() && !b.debit() {
				pair(&out, a, b, clearManual)
			}
		}
	}
	if len(out) == 0 {
		return 0, crud.Invalid("Nichts auszugleichen: Soll- und Habenposten mit offenem Betrag nötig")
	}
	return len(out), m.writeClearings(ctx, cc, ledger, out)
}

func (m *Module) oneItem(ctx context.Context, cc, ledger string, fy int, doc string, line int) (*openItem, error) {
	res, err := m.db.Query(ctx, `SELECT i.account_number, `+partnerExpr+` FROM ledger__journal_item i
		JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		WHERE i.company_code_id = ? AND i.ledger = ? AND i.fiscal_year = ? AND i.document_number = ? AND i.line_item_number = ?
		AND c.reconciliation_type IN ('CUSTOMER', 'SUPPLIER')`, cc, ledger, fy, doc, line)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Posten %d/%s/%d: kein Posten auf einem Debitoren- oder Kreditorenkonto", fy, doc, line)
	}
	items, err := m.loadItems(ctx, cc, ledger, itemFilter{Account: crud.Str(res.Rows[0][0]), Partner: crud.Str(res.Rows[0][1])})
	if err != nil {
		return nil, err
	}
	for _, o := range items {
		if o.FY == fy && o.Doc == doc && o.Line == line {
			return o, nil
		}
	}
	return nil, crud.Invalid("Posten %d/%s/%d nicht gefunden", fy, doc, line)
}

// openItemReset: OpenItem.reset {ids} – Ausgleiche der Posten aufheben.
func (m *Module) openItemReset(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	n, err := m.resetItems(ctx, splitIDs(dataOf(req.Payload)["ids"]))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%d Ausgleiche aufgehoben", n)}}, nil
}

func (m *Module) resetItems(ctx context.Context, ids []string) (int, error) {
	n := 0
	for _, id := range ids {
		cc, fy, doc, line, err := parseItemID(id)
		if err != nil {
			return n, err
		}
		if err := m.openItemAuthorized(ctx, cc, true); err != nil {
			return n, err
		}
		cfg, err := m.config(ctx, cc)
		if err != nil {
			return n, err
		}
		res, err := m.db.Exec(ctx, `DELETE FROM ledger__clearing WHERE company_code_id = ? AND ledger = ?
			AND ((debit_fiscal_year = ? AND debit_document_number = ? AND debit_line = ?) OR (credit_fiscal_year = ? AND credit_document_number = ? AND credit_line = ?))`,
			cc, cfg.Ledger, fy, doc, line, fy, doc, line)
		if err != nil {
			return n, err
		}
		n += int(res.RowsAffected)
	}
	return n, nil
}

// openItemAutoClear: OpenItem.autoClear {company_code, partner_id?, redo?} – Regeln für alle Posten.
func (m *Module) openItemAutoClear(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	q := dataOf(req.Payload)
	cc := strings.TrimSpace(crud.Str(q["company_code"]))
	if err := m.openItemAuthorized(ctx, cc, true); err != nil {
		return sdk.Response{}, err
	}
	n, err := m.clearPartners(ctx, cc, nil, strings.TrimSpace(crud.Str(q["partner_id"])), isTrue(q["redo"]))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Buchungskreis %s: %d Ausgleiche gebucht", cc, n)}}, nil
}

// --- Aktionen in der Oberfläche ---------------------------------------------------

// Am Buchungskreis: „Offene Posten ausgleichen …“ (Regeln für alle Partner;
// wahlweise automatische Zuordnungen neu rechnen).
var redoField = crud.Field{Key: "clr_redo", Label: "Automatische Zuordnungen neu rechnen (von Hand zugeordnete bleiben)", Type: tBool, ActionOnly: true}

func (m *Module) autoClearAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "clearOpenItems", Label: "Offene Posten ausgleichen …", Record: true,
		Fields: []string{"clr_redo"}},
		Handle: func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
			var in struct {
				ID   string `json:"id"`
				Data struct {
					Redo any `json:"clr_redo"`
				} `json:"data"`
			}
			if err := sdk.Decode(req.Payload, &in); err != nil {
				return sdk.Response{}, err
			}
			return m.openItemAutoClear(ctx, sdk.Request{Payload: map[string]any{"company_code": in.ID, "redo": isTrue(in.Data.Redo)}})
		}}
}

var clearFields = []crud.Field{
	{Key: "clr_with", Label: "Ausgleichen mit (offene Gegenposten desselben Partners)", Type: tSel, ActionOnly: true},
	{Key: "open_amount", Label: "Offen", Type: tText, Virtual: true, ReadOnly: true},
	{Key: "cleared_with", Label: "Ausgeglichen mit", Type: tText, Virtual: true, ReadOnly: true},
}

func (m *Module) itemClearActions() []crud.Action {
	return []crud.Action{
		{ActionConfig: metamodel.ActionConfig{Name: "clearWith", Label: "Ausgleichen mit …", Record: true, FormState: true, Fields: []string{"clr_with"}},
			Handle: func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
				var in struct {
					ID   string `json:"id"`
					Data struct {
						With string `json:"clr_with"`
					} `json:"data"`
				}
				if err := sdk.Decode(req.Payload, &in); err != nil {
					return sdk.Response{}, err
				}
				if strings.TrimSpace(in.Data.With) == "" {
					return sdk.Response{}, crud.Invalid("Gegenposten wählen")
				}
				n, err := m.clearManual(ctx, []string{in.ID, in.Data.With})
				if err != nil {
					return sdk.Response{}, err
				}
				return sdk.Response{Payload: map[string]any{"id": in.ID, "message": fmt.Sprintf("%d Ausgleich gebucht", n)}}, nil
			}},
		{ActionConfig: metamodel.ActionConfig{Name: "resetClearing", Label: "Ausgleich aufheben", Record: true,
			Confirm: "Alle Ausgleiche dieses Postens aufheben? Er ist danach wieder offen."},
			Handle: func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
				var in struct {
					ID string `json:"id"`
				}
				if err := sdk.Decode(req.Payload, &in); err != nil {
					return sdk.Response{}, err
				}
				n, err := m.resetItems(ctx, []string{in.ID})
				if err != nil {
					return sdk.Response{}, err
				}
				return sdk.Response{Payload: map[string]any{"id": in.ID, "message": fmt.Sprintf("%d Ausgleiche aufgehoben", n)}}, nil
			}},
	}
}

// clearWithOptions: Auswahl der offenen Gegenposten in der Maske „Ausgleichen mit …“.
func (m *Module) clearWithOptions(ctx context.Context, id string) ([]metamodel.Option, error) {
	cc, fy, doc, line, err := parseItemID(id)
	if err != nil {
		return nil, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return nil, err
	}
	o, err := m.oneItem(ctx, cc, cfg.Ledger, fy, doc, line)
	if err != nil {
		return nil, err
	}
	items, err := m.loadItems(ctx, cc, cfg.Ledger, itemFilter{Account: o.Account, Partner: o.Partner})
	if err != nil {
		return nil, err
	}
	var out []metamodel.Option
	for _, x := range items {
		if x.Open == 0 || x.debit() == o.debit() {
			continue
		}
		d, _ := m.cur.decimals(ctx, x.Currency)
		out = append(out, metamodel.Option{Value: x.id(cc), Label: fmt.Sprintf("%s · %s/%d · %s · offen %s", germanDay(x.Date), x.Doc, x.Line, x.Text,
			formatAmount(x.Open, d))})
	}
	return out, nil
}

// decorateClearing: offener Betrag und Ausgleich am Einzelposten (nur Abstimmkonten).
func (m *Module) decorateClearing(ctx context.Context, rec crud.Record) {
	if k := crud.Str(rec["account_kind"]); k != "D" && k != "K" {
		return
	}
	cc, fy, doc, line := crud.Str(rec["company_code_id"]), toInt(rec["fiscal_year"]), crud.Str(rec["document_number"]), toInt(rec["line_item_number"])
	res, err := m.db.Query(ctx, `SELECT CASE WHEN debit_fiscal_year = ? AND debit_document_number = ? AND debit_line = ? THEN credit_document_number ELSE debit_document_number END,
		amount, clearing_date FROM ledger__clearing WHERE company_code_id = ? AND ledger = ?
		AND ((debit_fiscal_year = ? AND debit_document_number = ? AND debit_line = ?) OR (credit_fiscal_year = ? AND credit_document_number = ? AND credit_line = ?))
		ORDER BY clearing_date`, fy, doc, line, cc, rec["ledger"], fy, doc, line, fy, doc, line)
	if err != nil {
		return
	}
	d, _ := m.cur.decimals(ctx, crud.Str(rec["local_currency"]))
	var cleared int64
	var parts []string
	for _, r := range res.Rows {
		cleared += toInt(r[1])
		date, _ := crud.ParseDate(r[2])
		parts = append(parts, fmt.Sprintf("%s (%s, %s)", crud.Str(r[0]), formatAmount(toInt(r[1]), d), germanDay(date)))
	}
	rec["open_amount"] = formatAmount(abs(toInt(rec["amount_local_curr"]))-cleared, d)
	rec["cleared_with"] = strings.Join(parts, "; ")
}
