package procurement

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Vorsteuerberichtigung (seit 0.11.0): Die Quote der aufzuteilenden Vorsteuer
// steht beim Buchen fest (Leistungsdatum). Ändern sich danach Verträge oder
// Bemessungen rückwirkend, rechnet „Vorsteuerberichtigung vorschlagen …“ die
// Quoten aller gebuchten Rechnungspositionen eines Jahres (Buchungsdatum) neu und
// legt die Differenz als Vorerfassung im Hauptbuch an: Vorsteuerkonto gegen das
// Sachkonto der Position (bzw. dessen Konto für nicht abziehbare Vorsteuer), mit
// der Kontierung der Position. Gebuchte Berichtigungen zählen beim nächsten
// Vorschlag mit; eine noch vorerfasste muss erst gebucht oder verworfen werden.

const (
	correctionObject     = "InputTaxCorrection"
	correctionItemObject = "InputTaxCorrectionItem"
	correctionDraft      = "DRAFT"
	correctionPosted     = "POSTED"
	correctionDiscarded  = "DISCARDED"
)

var correctionStatus = []metamodel.Option{{Value: correctionDraft, Label: "vorerfasst"}, {Value: correctionPosted, Label: "gebucht"},
	{Value: correctionDiscarded, Label: "verworfen"}}

func (m *Module) taxCorrection() *crud.Entity {
	return &crud.Entity{
		Object: correctionObject, Title: "Vorsteuerberichtigungen", Icon: "icon-percent", Table: "procurement__tax_correction", Section: "Beschaffung",
		Keys: []string{"company_code", "correction_id"}, Order: "company_code, correction_id DESC", Filters: []string{"company_code", "year", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "correction_id", Label: "Berichtigung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "year", Label: "Jahr (Buchungsdatum der Rechnungen)", Type: tNum, Required: true, Listable: true},
			{Key: "posting_date", Label: "Buchungsdatum der Berichtigung (leer = 31.12. des Jahres)", Type: tDate, Listable: true},
			{Key: "amount", Label: "Berichtigung der Vorsteuer (+ mehr abziehbar)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "currency", Label: "Währung", Type: tText, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: correctionStatus},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true, ReadOnly: true},
			{Key: "created_at", Label: "Angelegt am", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Angelegt von", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: correctionItemObject, ForeignKey: "correction_id",
				Match:   map[string]string{"company_code": "company_code", "correction_id": "correction_id"},
				Columns: []string{"invoice_id", "line_no", "amount", "tax_quota_text"}}},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		ReadOnly: true,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmount(ctx, rec, "amount")
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "propose", Label: "Vorsteuerberichtigung vorschlagen …", Fields: []string{"company_code", "year", "posting_date"},
				Confirm: "Quoten der aufzuteilenden Vorsteuer des Jahres neu rechnen und die Differenz im Hauptbuch vorerfassen?"}, Handle: m.proposeCorrectionAction},
		},
	}
}

func (m *Module) taxCorrectionItem() *crud.Entity {
	return &crud.Entity{
		Object: correctionItemObject, Title: "Positionen der Vorsteuerberichtigung", Icon: "icon-list", Table: "procurement__tax_correction_item", Section: "Beschaffung",
		Keys: []string{"company_code", "correction_id", "invoice_id", "line_no"}, Order: "company_code, correction_id, invoice_id, line_no",
		Filters: []string{"company_code", "correction_id", "invoice_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, ReadOnly: true},
			{Key: "correction_id", Label: "Berichtigung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "invoice_id", Label: "Rechnung", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupInvoice},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "amount", Label: "Berichtigung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "tax_quota_text", Label: "Neue Vorsteuerquote", Type: tText, Listable: true, ReadOnly: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		ReadOnly: true,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["currency"] = m.text(ctx, `SELECT currency FROM procurement__tax_correction WHERE company_code = ? AND correction_id = ?`, rec["company_code"], rec["correction_id"])
			err := m.decorateAmount(ctx, rec, "amount")
			delete(rec, "currency")
			return err
		},
	}
}

type correctionLine struct {
	invoice, account, taxAccount, objType, objID, costCenter, contract, supplier, text string
	line, amount                                                                       int64
	quota                                                                              quotaInfo
}

func (m *Module) proposeCorrectionAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			CompanyCode string `json:"company_code"`
			Year        any    `json:"year"`
			PostingDate string `json:"posting_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Data.CompanyCode)
	year := toInt(in.Data.Year)
	if cc == "" || year < 1900 || year > 9999 {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Jahr angeben")
	}
	if err := requireWrite(ctx, invoiceObject, "post", cc); err != nil {
		return sdk.Response{}, err
	}
	posting := strings.TrimSpace(in.Data.PostingDate)
	if posting == "" {
		posting = fmt.Sprintf("%d-12-31", year)
	}
	if d, err := crud.ParseDate(posting); err != nil {
		return sdk.Response{}, crud.Invalid("Buchungsdatum %q: %v", posting, err)
	} else {
		posting = d
	}
	if id := m.text(ctx, `SELECT correction_id FROM procurement__tax_correction WHERE company_code = ? AND year = ? AND status = ?`, cc, year, correctionDraft); id != "" {
		return sdk.Response{}, crud.Invalid("Vorsteuerberichtigung %s ist noch vorerfasst – erst im Hauptbuch buchen oder dort verwerfen", id)
	}
	from, to := fmt.Sprintf("%d-01-01", year), fmt.Sprintf("%d-12-31", year)
	res, err := m.db.Query(ctx, `SELECT i.invoice_id, i.line_no, i.account_number, COALESCE(i.object_type, ''), COALESCE(i.object_id, ''),
		COALESCE(i.cost_center, ''), COALESCE(i.contract_id, ''), i.tax_code, COALESCE(i.nondeductible_tax, 0), i.deductible_tax, i.tax_quota_date,
		COALESCE(h.object_type, ''), COALESCE(h.object_id, ''), h.supplier_id, h.currency, COALESCE(i.item_text, ''),
		COALESCE((SELECT SUM(c.amount) FROM procurement__tax_correction_item c JOIN procurement__tax_correction t
			ON t.company_code = c.company_code AND t.correction_id = c.correction_id
			WHERE c.company_code = i.company_code AND c.invoice_id = i.invoice_id AND c.line_no = i.line_no AND t.status = ?), 0)
		FROM procurement__invoice_item i JOIN procurement__invoice h ON h.company_code = i.company_code AND h.invoice_id = i.invoice_id
		WHERE i.company_code = ? AND h.status = ? AND h.posting_date >= ? AND h.posting_date <= ? AND i.is_active = ? AND i.deductible_tax IS NOT NULL
		ORDER BY i.invoice_id, i.line_no`, correctionPosted, cc, invoicePosted, from, to, true)
	if err != nil {
		return sdk.Response{}, err
	}
	quotas := newQuotaCache(m, cc)
	taxes := taxCache{m: m, codes: map[string]*taxInfo{}}
	subs := &taxAccounts{m: m, cc: cc, acc: map[string]string{}}
	var lines []correctionLine
	currency := ""
	var total int64
	for _, r := range res.Rows {
		s := func(i int) string { return crud.Str(r[i]) }
		if currency != "" && s(14) != currency {
			return sdk.Response{}, crud.Invalid("Rechnungen des Jahres %d in mehreren Währungen (%s, %s) – Berichtigung je Währung nicht unterstützt", year, currency, s(14))
		}
		currency = s(14)
		t, err := taxes.get(ctx, s(7))
		if err != nil || t == nil || t.Kind != taxPartial {
			continue
		}
		objType, objID := s(3), s(4)
		if objID == "" {
			objType, objID = s(11), s(12)
		}
		q, err := quotas.get(ctx, objType, objID, s(10), m.splitMeasurement(ctx, t.Code))
		if err != nil {
			return sdk.Response{}, crud.Invalid("Rechnung %s Pos. %s: %v", s(0), s(1), err)
		}
		ded, nonded := toInt(r[9]), toInt(r[8])
		diff := scaleRat(ded+nonded, q.rate) - ded - toInt(r[16])
		if diff == 0 {
			continue
		}
		acc := s(2)
		if sub, err := subs.of(ctx, acc); err != nil {
			return sdk.Response{}, err
		} else if sub != "" {
			acc = sub
		}
		taxAcc, err := m.glAccount(ctx, cc, t.Account)
		if err != nil {
			return sdk.Response{}, err
		}
		lines = append(lines, correctionLine{invoice: s(0), line: toInt(r[1]), account: acc, taxAccount: taxAcc, objType: objType, objID: objID,
			costCenter: s(5), contract: s(6), supplier: s(13), text: s(15), amount: diff, quota: q})
		total += diff
	}
	if len(lines) == 0 {
		return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Vorsteuerberichtigung %d: keine Abweichung der Quoten – nichts vorzuerfassen", year)}}, nil
	}
	decimals := m.currencyDecimals(ctx, currency)
	n := toInt(m.text(ctx, `SELECT COUNT(*) FROM procurement__tax_correction WHERE company_code = ? AND year = ?`, cc, year))
	id := fmt.Sprintf("%d-%02d", year, n+1)
	header := fmt.Sprintf("Vorsteuerberichtigung %d (%s)", year, id)
	fields := fieldStatusCache{m: m, cc: cc, groups: map[string]map[string]string{}}
	var draftID string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		draft, err := m.services.Call(ctx, "JournalDraft", "create", map[string]any{"data": map[string]any{
			"company_code_id": cc, "posting_date": posting, "document_date": posting, "currency": currency, "header_text": header}})
		if err != nil {
			return unavailable("Hauptbuch", err)
		}
		if draftID = field(draft.Payload, "id"); draftID == "" {
			return fmt.Errorf("Vorerfassung ohne id")
		}
		post := func(acc string, amount int64, text string, kontierung map[string]string) error {
			side := "S"
			if amount < 0 {
				side, amount = "H", -amount
			}
			data := map[string]any{"draft_id": draftID, "account_number": acc, "shkzg": side, "amount": formatAmount(amount, decimals), "item_text": text}
			allowed, err := fields.allowed(ctx, acc)
			if err != nil {
				return err
			}
			for k, v := range kontierung {
				if v != "" && allowed[k] {
					data[k] = v
				}
			}
			_, err = m.services.Call(ctx, "JournalDraftItem", "create", map[string]any{"data": data})
			return err
		}
		taxTotal := map[string]int64{}
		for _, l := range lines {
			k := map[string]string{"cost_center": l.costCenter, "supplier_id": l.supplier, "rent_contract_id": l.contract}
			if f := m.objectField(ctx, cc, l.objType); f != "" && l.objID != "" {
				k[f] = l.objID
			}
			text := strings.TrimSpace(fmt.Sprintf("%s Pos. %d Vorsteuerquote %s %s", l.invoice, l.line, percent(l.quota.rate), l.text))
			if err := post(l.account, -l.amount, text, k); err != nil {
				return err
			}
			taxTotal[l.taxAccount] += l.amount
		}
		accs := make([]string, 0, len(taxTotal))
		for a := range taxTotal {
			accs = append(accs, a)
		}
		sort.Strings(accs)
		for _, a := range accs {
			if taxTotal[a] != 0 {
				if err := post(a, taxTotal[a], header, nil); err != nil {
					return err
				}
			}
		}
		if _, err := m.services.Call(ctx, "JournalDraft", "simulate", map[string]any{"id": draftID}); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__tax_correction (company_code, correction_id, year, posting_date, currency, amount, status,
			draft_id, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, id, year, posting, currency, total, correctionDraft, draftID,
			time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)); err != nil {
			return err
		}
		for _, l := range lines {
			if _, err := m.db.Exec(ctx, `INSERT INTO procurement__tax_correction_item (company_code, correction_id, invoice_id, line_no, amount, tax_quota, tax_quota_text)
				VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, id, l.invoice, l.line, l.amount, l.quota.rate.FloatString(8), l.quota.text); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": cc + "|" + id, "draft_id": draftID,
		"message": fmt.Sprintf("Vorsteuerberichtigung %s: %d Positionen, Vorsteuer %s %s – im Hauptbuch prüfen und buchen", id, len(lines),
			formatAmount(total, decimals), currency)}}, nil
}
