package accounting

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
)

// Buchen und Stornieren.
//
//	JournalEntry.post    {data: {company_code, posting_date, document_date?, currency, text, reference?,
//	                             lines: [{account_code, debit | credit, text?}, …]}}
//	                     oder einfach: {data: {…, debit_account, credit_account, amount}}
//	JournalEntry.reverse {id, data: {posting_date?, text?}}
//	AccountBalance.list  {company_code?, date_from?, date_to?}   → Summen- und Saldenliste
//
// Rechte je Buchungskreis: JournalEntry.post bzw. JournalEntry.reverse im
// Buchungskreis des Belegs (Account.Check), Lesen mit JournalEntry.list/get.

// line ist eine geprüfte Position.
type line struct {
	Account string
	Side    string
	Amount  int64
	Text    string
}

// header sind die Kopfdaten eines Belegs.
type header struct {
	CompanyCode, PostingDate, DocumentDate, Reference, Text, Currency string
}

func dataOf(payload any) map[string]any {
	m, _ := payload.(map[string]any)
	if d, ok := m["data"].(map[string]any); ok {
		return d
	}
	if m == nil {
		return map[string]any{}
	}
	return m
}

func (m *Module) post(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d := dataOf(req.Payload)
	h := header{
		CompanyCode: strings.TrimSpace(crud.Str(d["company_code"])), Reference: strings.TrimSpace(crud.Str(d["reference"])),
		Text: strings.TrimSpace(crud.Str(d["text"])), Currency: strings.ToUpper(strings.TrimSpace(crud.Str(d["currency"]))),
	}
	var err error
	if h.PostingDate, err = dateOr(d["posting_date"], crud.Today()); err != nil {
		return sdk.Response{}, err
	}
	if h.DocumentDate, err = dateOr(d["document_date"], h.PostingDate); err != nil {
		return sdk.Response{}, err
	}
	lines, err := parseLines(d)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := m.checkHeader(ctx, h, "post"); err != nil {
		return sdk.Response{}, err
	}
	if err := m.checkLines(ctx, lines); err != nil {
		return sdk.Response{}, err
	}
	var id string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		id, err = m.writeEntry(ctx, h, lines, "")
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return m.set.Entity(entryObject).Get(ctx, map[string]any{"id": id})
}

// dateOr: Datum JJJJ-MM-TT oder der Standardwert, wenn leer.
func dateOr(v any, def string) (string, error) {
	if strings.TrimSpace(crud.Str(v)) == "" {
		return def, nil
	}
	return crud.ParseDate(v)
}

// parseLines liest lines oder die einfache Form „Soll an Haben“.
func parseLines(d map[string]any) ([]line, error) {
	raw, hasLines := d["lines"].([]any)
	simple := crud.Str(d["debit_account"]) != "" || crud.Str(d["credit_account"]) != "" || crud.Str(d["amount"]) != ""
	switch {
	case hasLines && simple:
		return nil, crud.Invalid("entweder lines oder debit_account/credit_account/amount angeben, nicht beides")
	case simple:
		amount, err := parseAmount(crud.Str(d["amount"]))
		if err != nil {
			return nil, crud.Invalid("%v", err)
		}
		dr, cr := strings.TrimSpace(crud.Str(d["debit_account"])), strings.TrimSpace(crud.Str(d["credit_account"]))
		if dr == "" || cr == "" {
			return nil, crud.Invalid("Soll-Konto und Haben-Konto sind Pflicht")
		}
		if dr == cr {
			return nil, crud.Invalid("Soll- und Haben-Konto müssen verschieden sein")
		}
		return []line{{Account: dr, Side: sideDebit, Amount: amount}, {Account: cr, Side: sideCredit, Amount: amount}}, nil
	}
	var out []line
	for i, r := range raw {
		lm, _ := r.(map[string]any)
		l := line{Account: strings.TrimSpace(crud.Str(lm["account_code"])), Text: strings.TrimSpace(crud.Str(lm["text"]))}
		dr, cr := crud.Str(lm["debit"]), crud.Str(lm["credit"])
		if (dr == "") == (cr == "") {
			return nil, crud.Invalid("Position %d: genau einer von debit (Soll) und credit (Haben) ist Pflicht", i+1)
		}
		amount, side := dr, sideDebit
		if cr != "" {
			amount, side = cr, sideCredit
		}
		n, err := parseAmount(amount)
		if err != nil {
			return nil, crud.Invalid("Position %d: %v", i+1, err)
		}
		if l.Account == "" {
			return nil, crud.Invalid("Position %d: Konto (account_code) ist Pflicht", i+1)
		}
		l.Side, l.Amount = side, n
		out = append(out, l)
	}
	if len(out) < 2 {
		return nil, crud.Invalid("ein Beleg braucht mindestens zwei Positionen (Soll und Haben)")
	}
	return out, nil
}

// checkHeader: Pflichtfelder, Währung, Buchungskreis (existiert, Recht action).
func (m *Module) checkHeader(ctx context.Context, h header, action string) error {
	if h.CompanyCode == "" {
		return crud.Invalid("Buchungskreis ist Pflicht")
	}
	if h.Text == "" {
		return crud.Invalid("Buchungstext ist Pflicht")
	}
	if !currencyRe.MatchString(h.Currency) {
		return crud.Invalid("Währung %q: ISO-4217-Code erwartet, z. B. CHF", h.Currency)
	}
	if _, err := m.services.Call(ctx, "CompanyCode", "get", map[string]any{"id": h.CompanyCode}); err != nil {
		if errors.Is(err, sdk.ErrNotFound) {
			return crud.Invalid("Buchungskreis %q gibt es nicht", h.CompanyCode)
		}
		return err
	}
	return requireCompanyCode(ctx, action, h.CompanyCode)
}

// checkLines: Konten existieren und sind aktiv, Soll = Haben.
func (m *Module) checkLines(ctx context.Context, lines []line) error {
	var debit, credit int64
	var codes []any
	for _, l := range lines {
		if l.Side == sideDebit {
			debit += l.Amount
		} else {
			credit += l.Amount
		}
		if !slices.Contains(codes, any(l.Account)) {
			codes = append(codes, l.Account)
		}
	}
	if debit != credit {
		return crud.Invalid("Soll (%s) und Haben (%s) sind nicht ausgeglichen", formatAmount(debit), formatAmount(credit))
	}
	res, err := m.db.Query(ctx, "SELECT code, status FROM ledger__accounts WHERE code IN ("+
		strings.TrimSuffix(strings.Repeat("?, ", len(codes)), ", ")+")", codes...)
	if err != nil {
		return err
	}
	status := map[string]string{}
	for _, r := range res.Rows {
		status[crud.Str(r[0])] = crud.Str(r[1])
	}
	for _, c := range codes {
		switch status[c.(string)] {
		case "":
			return crud.Invalid("Konto %s gibt es nicht", c)
		case statusLocked:
			return crud.Invalid("Konto %s ist gesperrt", c)
		}
	}
	return nil
}

// writeEntry schreibt Kopf und Positionen (in der Transaktion des Aufrufers)
// und vergibt die Belegnummer <Jahr>-<laufende Nummer> je Buchungskreis.
func (m *Module) writeEntry(ctx context.Context, h header, lines []line, reversalOf string) (string, error) {
	year := h.PostingDate[:4]
	res, err := m.db.Query(ctx, `SELECT document_no FROM ledger__journal_entries WHERE company_code = ? AND document_no LIKE ?
		ORDER BY document_no DESC LIMIT 1`, h.CompanyCode, year+"-%")
	if err != nil {
		return "", err
	}
	next := 1
	if len(res.Rows) > 0 {
		_, n, _ := strings.Cut(crud.Str(res.Rows[0][0]), "-")
		last, _ := strconv.Atoi(n)
		next = last + 1
	}
	id := crud.NewID()
	_, err = m.db.Exec(ctx, `INSERT INTO ledger__journal_entries (id, document_no, company_code, posting_date, document_date,
		reference, text, currency, reversal_of, posted_at, posted_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, fmt.Sprintf("%s-%06d", year, next), h.CompanyCode, h.PostingDate, h.DocumentDate, nilIfEmpty(h.Reference), h.Text, h.Currency,
		nilIfEmpty(reversalOf), time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID))
	if err != nil {
		return "", err
	}
	for i, l := range lines {
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__journal_lines (id, entry_id, line_no, account_code, side, amount_minor, text)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), id, i+1, l.Account, l.Side, l.Amount, nilIfEmpty(l.Text)); err != nil {
			return "", err
		}
	}
	return id, nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// reverse storniert einen Beleg: neuer Beleg mit vertauschten Seiten, beide
// verweisen aufeinander. Ein Beleg wird höchstens einmal storniert; ein
// Stornobeleg selbst wird nicht storniert (neu buchen statt Storno vom Storno).
func (m *Module) reverse(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	id := crud.Str(p["id"])
	if id == "" {
		return sdk.Response{}, crud.Invalid("id des Belegs fehlt")
	}
	d := dataOf(req.Payload)
	var newID string
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, `SELECT document_no, company_code, posting_date, currency, reversal_of, reversed_by, reference
			FROM ledger__journal_entries WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, id)
		}
		r := res.Rows[0]
		docNo, origDate := crud.Str(r[0]), r[2]
		orig, _ := crud.ParseDate(origDate)
		switch {
		case r[5] != nil:
			return crud.Invalid("Beleg %s ist bereits storniert", docNo)
		case r[4] != nil:
			return crud.Invalid("Beleg %s ist selbst ein Storno – bitte neu buchen", docNo)
		}
		h := header{CompanyCode: crud.Str(r[1]), Currency: crud.Str(r[3]), Reference: crud.Str(r[6]),
			Text: strings.TrimSpace(crud.Str(d["text"]))}
		if h.Text == "" {
			h.Text = "Storno " + docNo
		}
		if h.PostingDate, err = dateOr(d["posting_date"], crud.Today()); err != nil {
			return err
		}
		if h.PostingDate < orig {
			return crud.Invalid("Storno am %s liegt vor dem Buchungsdatum des Belegs (%s)", h.PostingDate, orig)
		}
		h.DocumentDate = h.PostingDate
		if err := requireCompanyCode(ctx, "reverse", h.CompanyCode); err != nil {
			return err
		}
		lr, err := m.db.Query(ctx, "SELECT account_code, side, amount_minor, text FROM ledger__journal_lines WHERE entry_id = ? ORDER BY line_no", id)
		if err != nil {
			return err
		}
		var lines []line
		for _, l := range lr.Rows {
			side := sideCredit
			if crud.Str(l[1]) == sideCredit {
				side = sideDebit
			}
			lines = append(lines, line{Account: crud.Str(l[0]), Side: side, Amount: toInt(l[2]), Text: crud.Str(l[3])})
		}
		if newID, err = m.writeEntry(ctx, h, lines, id); err != nil {
			return err
		}
		upd, err := m.db.Exec(ctx, "UPDATE ledger__journal_entries SET reversed_by = ? WHERE id = ? AND reversed_by IS NULL", newID, id)
		if err != nil {
			return err
		}
		if upd.RowsAffected != 1 {
			return crud.Invalid("Beleg %s wurde gleichzeitig storniert", docNo)
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return m.set.Entity(entryObject).Get(ctx, map[string]any{"id": newID})
}

func requireCompanyCode(ctx context.Context, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, entryObject, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, entryObject, action, cc)
	}
	return nil
}
