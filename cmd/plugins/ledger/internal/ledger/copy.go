package ledger

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kopieren (seit 0.21.0): ein gebuchter Beleg oder eine Vorerfassung wird zur
// neuen Vorerfassung – z. B. wiederkehrende Kleinbelege (Fahrkarte, Barauslage).
// Übernommen werden Belegart, Währung und die Positionen (des führenden
// Ledgers) mit Konto, Soll/Haben, Betrag, Text und Kontierung. Im Formular:
// Buchungsdatum (Vorgabe Stichtag), Belegdatum, Kopftext, Referenz (leer – sie
// ist meist eine Rechnungsnummer). Beträge, Konten und Kontierung lassen sich
// danach in der Vorerfassung ändern; gebucht wird erst mit „Buchen“.

var copyFields = []crud.Field{
	{Key: "copy_posting_date", Label: "Buchungsdatum der Kopie (leer = Stichtag)", Type: tDate, ActionOnly: true},
	{Key: "copy_document_date", Label: "Belegdatum der Kopie (leer = Buchungsdatum)", Type: tDate, ActionOnly: true},
	{Key: "copy_header_text", Label: "Belegkopftext der Kopie (leer = wie Vorlage)", Type: tText, ActionOnly: true},
	{Key: "copy_reference", Label: "Referenz der Kopie (z. B. Belegnummer der Quittung)", Type: tText, ActionOnly: true},
}

func copyAction(handle func(context.Context, sdk.Request) (sdk.Response, error)) crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "copy", Label: "Kopieren …", Record: true,
		Fields: []string{"copy_posting_date", "copy_document_date", "copy_header_text", "copy_reference"}}, Handle: handle}
}

type copyInput struct {
	ID   string `json:"id"`
	Data struct {
		PostingDate  string `json:"copy_posting_date"`
		DocumentDate string `json:"copy_document_date"`
		HeaderText   string `json:"copy_header_text"`
		Reference    string `json:"copy_reference"`
	} `json:"data"`
}

type copyLine struct {
	Account, Side, Amount, Text string
	Dims                        map[string]any
}

// copyEntryAction: gebuchter Beleg → neue Vorerfassung.
func (m *Module) copyEntryAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in copyInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	k, err := parseDocID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT document_type, currency, header_text FROM ledger__journal_header
		WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, k.CompanyCode, k.FiscalYear, k.Number)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, in.ID)
	}
	h := res.Rows[0]
	cur := crud.Str(h[1])
	d, err := m.cur.decimals(ctx, cur)
	if err != nil {
		return sdk.Response{}, err
	}
	items, err := m.db.Query(ctx, `SELECT account_number, shkzg, amount_document_curr, item_text, `+strings.Join(dimColumns, ", ")+`
		FROM ledger__journal_item WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?
		AND ledger = (SELECT leading_ledger FROM ledger__company_config WHERE company_code_id = ?) ORDER BY line_item_number`,
		k.CompanyCode, k.FiscalYear, k.Number, k.CompanyCode)
	if err != nil {
		return sdk.Response{}, err
	}
	var lines []copyLine
	for _, r := range items.Rows {
		amt := toInt(r[2])
		if amt < 0 {
			amt = -amt
		}
		lines = append(lines, copyLine{Account: crud.Str(r[0]), Side: crud.Str(r[1]), Amount: formatAmount(amt, d), Text: crud.Str(r[3]), Dims: dimsOf(r[4:])})
	}
	return m.copyToDraft(ctx, k.CompanyCode, crud.Str(h[0]), cur, crud.Str(h[2]), "Beleg "+k.Number, in, lines)
}

// copyDraftAction: Vorerfassung (auch gebuchte oder verworfene) → neue Vorerfassung.
func (m *Module) copyDraftAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in copyInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, "SELECT company_code_id, document_type, currency, header_text FROM ledger__draft_header WHERE id = ?", in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Vorerfassung %s", sdk.ErrNotFound, in.ID)
	}
	h := res.Rows[0]
	items, err := m.db.Query(ctx, `SELECT account_number, shkzg, amount, item_text, `+strings.Join(dimColumns, ", ")+`
		FROM ledger__draft_item WHERE draft_id = ? ORDER BY line_item_number`, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	var lines []copyLine
	for _, r := range items.Rows {
		lines = append(lines, copyLine{Account: crud.Str(r[0]), Side: crud.Str(r[1]), Amount: crud.Str(r[2]), Text: crud.Str(r[3]), Dims: dimsOf(r[4:])})
	}
	return m.copyToDraft(ctx, crud.Str(h[0]), crud.Str(h[1]), crud.Str(h[2]), crud.Str(h[3]), "Vorerfassung", in, lines)
}

func dimsOf(vals []any) map[string]any {
	out := map[string]any{}
	for i, c := range dimColumns {
		if v := crud.Str(vals[i]); v != "" {
			out[c] = v
		}
	}
	return out
}

// copyToDraft legt die neue Vorerfassung über die Entities an – mit allen
// Prüfungen wie bei der Erfassung von Hand (Konto im Buchungskreis, Feldstatus).
func (m *Module) copyToDraft(ctx context.Context, cc, docType, cur, text, from string, in copyInput, lines []copyLine) (sdk.Response, error) {
	if err := requireCompanyCode(ctx, entryObject, "post", cc); err != nil {
		return sdk.Response{}, err
	}
	if len(lines) == 0 {
		return sdk.Response{}, crud.Invalid("%s hat keine Positionen", from)
	}
	posting := crud.KeyDate(ctx)
	if strings.TrimSpace(in.Data.PostingDate) != "" {
		d, err := crud.ParseDate(in.Data.PostingDate)
		if err != nil {
			return sdk.Response{}, err
		}
		posting = d
	}
	docDate := posting
	if strings.TrimSpace(in.Data.DocumentDate) != "" {
		d, err := crud.ParseDate(in.Data.DocumentDate)
		if err != nil {
			return sdk.Response{}, err
		}
		docDate = d
	}
	if t := strings.TrimSpace(in.Data.HeaderText); t != "" {
		text = t
	}
	var id string
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		head := map[string]any{"company_code_id": cc, "document_type": nilIfEmpty(docType), "posting_date": posting, "document_date": docDate,
			"currency": cur, "header_text": text, "reference": nilIfEmpty(strings.TrimSpace(in.Data.Reference))}
		resp, err := m.set.Entity("JournalDraft").Create(ctx, map[string]any{"data": head})
		if err != nil {
			return err
		}
		rec, _ := resp.Payload.(crud.Record)
		id = crud.Str(rec["id"])
		for i, l := range lines {
			data := map[string]any{"draft_id": id, "account_number": l.Account, "shkzg": l.Side, "amount": l.Amount, "item_text": nilIfEmpty(l.Text)}
			for k, v := range l.Dims {
				data[k] = v
			}
			if _, err := m.set.Entity("JournalDraftItem").Create(ctx, map[string]any{"data": data}); err != nil {
				return fmt.Errorf("Position %d (%s): %w", i+1, l.Account, err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"draft_id": id, "positions": len(lines),
		"message": fmt.Sprintf("%s kopiert: neue Vorerfassung %s zum %s mit %d Positionen – Beträge und Kontierung prüfen, dann buchen", from, id, posting, len(lines))}}, nil
}
