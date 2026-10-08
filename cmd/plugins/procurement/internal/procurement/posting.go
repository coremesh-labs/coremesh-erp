package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Buchen einer Eingangsrechnung über die Vorerfassung des Hauptbuchs – in
// einer Transaktion (Vorerfassung, Positionen, Prüfung, ggf. Buchung, Status):
//
//   - je Position: Soll Sachkonto (Aufwand), Haben bei negativem Betrag;
//   - Gegenposition: Haben Abstimmkonto des Lieferanten aus seinen
//     Buchungskreisdaten in der Rolle der Rechnungsart (Kreditor) mit dem
//     Lieferanten als Partner;
//   - Kontierungen (Objekt, Kostenstelle, Lieferant) nur, wo die
//     Feldstatusgruppe des Kontos sie zulässt;
//   - Belegart der Rechnungsart, bei negativem Saldo die Gutschrift-Belegart.

const serviceObject = "SupplierInvoiceService"

func (m *Module) registerPosting(r *module.Router) {
	r.Object(serviceObject).Handle(events.CallbackAction, m.onDraftEvent)
}

// subscribePosting: Vorerfassungen, die im Hauptbuch gebucht oder verworfen werden.
func (m *Module) subscribePosting(ctx context.Context) {
	for _, a := range []string{"post", "deactivate"} {
		if err := events.Register(ctx, m.services, events.Subscription{Object: "JournalDraft", Action: a, CompanyCode: events.All,
			Callback: serviceObject}); err != nil {
			m.log.WarnContext(ctx, "Event nicht abonniert", "object", "JournalDraft", "action", a, "err", err.Error())
		}
	}
}

// onDraftEvent: gebucht → Belegnummer übernehmen; verworfen → wieder erfasst.
func (m *Module) onDraftEvent(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	ev, err := events.Decode(req.Payload)
	if err != nil || ev.Object != "JournalDraft" || ev.EntityID == "" {
		return sdk.Response{}, err
	}
	switch ev.Action {
	case "post":
		_, err = m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, document_id = ?, document_number = ?, changed_at = ?
			WHERE draft_id = ? AND status = ?`, invoicePosted, nilIfEmpty(crud.Str(ev.Data["posted_document_id"])),
			nilIfEmpty(crud.Str(ev.Data["document_number"])), now(), ev.EntityID, invoiceDraft)
	case "deactivate":
		_, err = m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, draft_id = NULL, changed_at = ? WHERE draft_id = ? AND status = ?`,
			invoiceOpen, now(), ev.EntityID, invoiceDraft)
	}
	return sdk.Response{}, err
}

type invoiceRow struct {
	CompanyCode, ID, Type, Supplier, Reference, InvoiceDate, PostingDate, Currency, Status string
	ObjectType, ObjectID, HeaderText, DraftID, DocumentID, DocumentNumber                  string
}

func (m *Module) invoiceOf(ctx context.Context, id string) (*invoiceRow, error) {
	key, err := m.set.Entity(invoiceObject).ParseID(id)
	if err != nil {
		return nil, err
	}
	res, err := m.db.Query(ctx, `SELECT company_code, invoice_id, invoice_type, supplier_id, supplier_reference, invoice_date, posting_date,
		currency, status, object_type, object_id, header_text, draft_id, document_id, document_number FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`,
		key["company_code"], key["invoice_id"])
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Rechnung %s", sdk.ErrNotFound, id)
	}
	r := res.Rows[0]
	s := func(i int) string { return crud.Str(r[i]) }
	d := func(i int) string { v, _ := crud.ParseDate(r[i]); return v }
	return &invoiceRow{CompanyCode: s(0), ID: s(1), Type: s(2), Supplier: s(3), Reference: s(4), InvoiceDate: d(5), PostingDate: d(6),
		Currency: s(7), Status: s(8), ObjectType: s(9), ObjectID: s(10), HeaderText: s(11), DraftID: s(12), DocumentID: s(13), DocumentNumber: s(14)}, nil
}

type itemRow struct {
	Account, ObjectType, ObjectID, CostCenter, Text, Contract string
	Amount                                                    int64
}

// postAction: „Buchen …“ – Vorerfassung anlegen, prüfen, ggf. buchen.
func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, "post", inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if inv.Status != invoiceOpen {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s", inv.ID, statusText(inv.Status))
	}
	typ, err := m.invoiceTypeOf(ctx, inv.CompanyCode, inv.Type)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT account_number, object_type, object_id, cost_center, item_text, amount, contract_id FROM procurement__invoice_item
		WHERE company_code = ? AND invoice_id = ? AND is_active = ? ORDER BY line_no`, inv.CompanyCode, inv.ID, true)
	if err != nil {
		return sdk.Response{}, err
	}
	var items []itemRow
	var total int64
	for _, r := range res.Rows {
		it := itemRow{Account: crud.Str(r[0]), ObjectType: crud.Str(r[1]), ObjectID: crud.Str(r[2]), CostCenter: crud.Str(r[3]), Text: crud.Str(r[4]),
			Amount: toInt(r[5]), Contract: crud.Str(r[6])}
		items = append(items, it)
		total += it.Amount
	}
	if len(items) == 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s hat keine Positionen", inv.ID)
	}
	if total == 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s: Summe der Positionen ist 0", inv.ID)
	}
	recon, err := m.supplierAccount(ctx, inv.CompanyCode, inv.Supplier, typ.SupplierRole)
	if err != nil {
		return sdk.Response{}, err
	}
	docType := typ.DocumentType
	if total < 0 {
		docType = typ.CreditDocumentType
	}
	decimals := m.currencyDecimals(ctx, inv.Currency)
	fields := fieldStatusCache{m: m, cc: inv.CompanyCode, groups: map[string]map[string]string{}}
	header := strings.TrimSpace(inv.ID + " " + m.partnerName(ctx, inv.Supplier))
	if inv.HeaderText != "" {
		header = inv.ID + " " + inv.HeaderText
	}
	var draftID, docID, docNo string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		draft, err := m.services.Call(ctx, "JournalDraft", "create", map[string]any{"data": map[string]any{
			"company_code_id": inv.CompanyCode, "document_type": docType, "posting_date": inv.PostingDate, "document_date": inv.InvoiceDate,
			"currency": inv.Currency, "header_text": header, "reference": inv.Reference}})
		if err != nil {
			return unavailable("Hauptbuch", err)
		}
		draftID = field(draft.Payload, "id")
		if draftID == "" {
			return fmt.Errorf("Vorerfassung ohne id")
		}
		line := func(acc, side string, amount int64, text string, kontierung map[string]string) error {
			data := map[string]any{"draft_id": draftID, "account_number": acc, "shkzg": side,
				"amount": formatAmount(amount, decimals), "item_text": text}
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
		for _, it := range items {
			side, amt := "S", it.Amount
			if amt < 0 {
				side, amt = "H", -amt
			}
			text := it.Text
			if text == "" {
				text = header
			}
			k := map[string]string{"cost_center": it.CostCenter, "supplier_id": inv.Supplier, "rent_contract_id": it.Contract}
			typ, obj := it.ObjectType, it.ObjectID
			if obj == "" {
				typ, obj = inv.ObjectType, inv.ObjectID
			}
			if f := m.objectField(ctx, inv.CompanyCode, typ); f != "" && obj != "" && k[f] == "" {
				k[f] = obj
			}
			if err := line(it.Account, side, amt, text, k); err != nil {
				return err
			}
		}
		side, amt := "H", total
		if total < 0 {
			side, amt = "S", -total
		}
		k := map[string]string{"supplier_id": inv.Supplier}
		if f := m.objectField(ctx, inv.CompanyCode, inv.ObjectType); f != "" && inv.ObjectID != "" {
			k[f] = inv.ObjectID
		}
		if err := line(recon, side, amt, header, k); err != nil {
			return err
		}
		if _, err := m.services.Call(ctx, "JournalDraft", "simulate", map[string]any{"id": draftID}); err != nil {
			return err
		}
		status := invoiceDraft
		if typ.AutoPost {
			res, err := m.services.Call(ctx, "JournalDraft", "post", map[string]any{"id": draftID})
			if err != nil {
				return err
			}
			docID, docNo, status = field(res.Payload, "id"), field(res.Payload, "document_number"), invoicePosted
		}
		_, err = m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, draft_id = ?, document_id = ?, document_number = ?, changed_at = ?, changed_by = ?
			WHERE company_code = ? AND invoice_id = ?`, status, draftID, nilIfEmpty(docID), nilIfEmpty(docNo), now(),
			nilIfEmpty(sdk.CallFromContext(ctx).UserID), inv.CompanyCode, inv.ID)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Rechnung %s vorerfasst und geprüft – im Hauptbuch buchen", inv.ID)
	if docNo != "" {
		msg = fmt.Sprintf("Rechnung %s gebucht: Beleg %s", inv.ID, docNo)
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "draft_id": draftID, "document_id": docID, "document_number": docNo, "message": msg}}, nil
}

// cancelAction: „Stornieren …“ – erfasst: storniert; vorerfasst: Vorerfassung
// verwerfen; gebucht: Storno im Hauptbuch zum angegebenen Datum.
func (m *Module) cancelAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			ReversalDate string `json:"reversal_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, "cancel", inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	var reversal, msg string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		switch inv.Status {
		case invoiceOpen:
			msg = "Rechnung " + inv.ID + " storniert"
		case invoiceDraft:
			if _, err := m.services.Call(ctx, "JournalDraft", "deactivate", map[string]any{"id": inv.DraftID}); err != nil {
				return err
			}
			msg = "Rechnung " + inv.ID + " storniert, Vorerfassung verworfen"
		case invoicePosted:
			d := strings.TrimSpace(in.Data.ReversalDate)
			if d == "" {
				return crud.Invalid("„Storno zum“ ist Pflicht – die Rechnung ist gebucht (Beleg %s)", inv.DocumentNumber)
			}
			res, err := m.services.Call(ctx, "JournalEntry", "reverse", map[string]any{"id": inv.DocumentID,
				"data": map[string]any{"posting_date": d, "header_text": "Storno " + inv.ID}})
			if err != nil {
				return err
			}
			reversal = field(res.Payload, "id")
			msg = fmt.Sprintf("Rechnung %s storniert, Stornobeleg %s", inv.ID, field(res.Payload, "document_number"))
		default:
			return crud.Invalid("Rechnung %s ist bereits storniert", inv.ID)
		}
		_, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, reversal_document_id = ?, changed_at = ?, changed_by = ?
			WHERE company_code = ? AND invoice_id = ?`, invoiceCancelled, nilIfEmpty(reversal), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID),
			inv.CompanyCode, inv.ID)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "status": invoiceCancelled, "message": msg}}, nil
}

// supplierAccount: Abstimmkonto des Lieferanten in der Rolle (Buchungskreisdaten), nicht gesperrt.
func (m *Module) supplierAccount(ctx context.Context, cc, supplier, role string) (string, error) {
	resp, err := m.services.Call(ctx, "PartnerCompanyCode", "list", map[string]any{"query": map[string]any{
		"bp_id": supplier, "company_code": cc, "role_code": role}})
	if err != nil {
		return "", unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Account      string `json:"reconciliation_account"`
			PostingBlock bool   `json:"posting_block"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 || out.Items[0].Account == "" {
		return "", crud.Invalid("Lieferant %s hat in der Rolle %s keine Buchungskreisdaten im Buchungskreis %s (Abstimmkonto) – "+
			"im Partnermodul unter „Buchungskreisdaten“ ergänzen", m.partnerName(ctx, supplier), role, cc)
	}
	if out.Items[0].PostingBlock {
		return "", crud.Invalid("Lieferant %s: Buchungssperre im Buchungskreis %s", m.partnerName(ctx, supplier), cc)
	}
	return out.Items[0].Account, nil
}

// fieldStatusCache: Kontierungen je Konto, die der Feldstatus zulässt (OPTIONAL, REQUIRED).
type fieldStatusCache struct {
	m      *Module
	cc     string
	groups map[string]map[string]string // Gruppe → Feld → Status
}

func (f *fieldStatusCache) allowed(ctx context.Context, account string) (map[string]bool, error) {
	resp, err := f.m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": f.cc, "account_number": account}})
	if err != nil {
		return nil, unavailable("Hauptbuch", err)
	}
	var acc struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
			Group         string `json:"field_status_group"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &acc); err != nil {
		return nil, err
	}
	group := "STD"
	for _, it := range acc.Items {
		if sameAccount(it.AccountNumber, account) && it.Group != "" {
			group = it.Group
		}
	}
	st, ok := f.groups[group]
	if !ok {
		resp, err := f.m.services.Call(ctx, "FieldStatus", "list", map[string]any{"query": map[string]any{"group_id": group}})
		if err != nil {
			return nil, unavailable("Hauptbuch", err)
		}
		var out struct {
			Items []struct {
				Field  string `json:"field_name"`
				Status string `json:"status"`
			} `json:"items"`
		}
		if err := sdk.Decode(resp.Payload, &out); err != nil {
			return nil, err
		}
		st = map[string]string{}
		for _, it := range out.Items {
			st[it.Field] = it.Status
		}
		f.groups[group] = st
	}
	allowed := map[string]bool{}
	for k, v := range st {
		allowed[k] = v == "OPTIONAL" || v == "REQUIRED"
	}
	return allowed, nil
}

// field: Textfeld einer Antwort.
func field(payload any, key string) string {
	if m, ok := payload.(map[string]any); ok {
		return crud.Str(m[key])
	}
	var m map[string]any
	if sdk.Decode(payload, &m) == nil {
		return crud.Str(m[key])
	}
	return ""
}
