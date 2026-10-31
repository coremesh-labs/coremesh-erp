package procurement

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
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
		if err == nil { // Vorsteuerberichtigung (seit 0.11.0)
			_, err = m.db.Exec(ctx, `UPDATE procurement__tax_correction SET status = ?, document_number = ? WHERE draft_id = ? AND status = ?`,
				correctionPosted, nilIfEmpty(crud.Str(ev.Data["document_number"])), ev.EntityID, correctionDraft)
		}
	case "deactivate":
		_, err = m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, draft_id = NULL, changed_at = ? WHERE draft_id = ? AND status = ?`,
			invoiceOpen, now(), ev.EntityID, invoiceDraft)
		if err == nil {
			_, err = m.db.Exec(ctx, `UPDATE procurement__tax_correction SET status = ? WHERE draft_id = ? AND status = ?`,
				correctionDiscarded, ev.EntityID, correctionDraft)
		}
	}
	return sdk.Response{}, err
}

type invoiceRow struct {
	CompanyCode, ID, Type, Supplier, Reference, InvoiceDate, PostingDate, Currency, Status string
	ObjectType, ObjectID, HeaderText, DraftID, DocumentID, DocumentNumber                  string
	Contract                                                                               string // Rechnung zum Vertrag, Rahmenvertrag
}

func (m *Module) invoiceOf(ctx context.Context, id string) (*invoiceRow, error) {
	key, err := m.set.Entity(invoiceObject).ParseID(id)
	if err != nil {
		return nil, err
	}
	res, err := m.db.Query(ctx, `SELECT company_code, invoice_id, invoice_type, supplier_id, supplier_reference, invoice_date, posting_date,
		currency, status, object_type, object_id, header_text, draft_id, document_id, document_number, framework_contract FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`,
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
		Currency: s(7), Status: s(8), ObjectType: s(9), ObjectID: s(10), HeaderText: s(11), DraftID: s(12), DocumentID: s(13), DocumentNumber: s(14), Contract: s(15)}, nil
}

type itemRow struct {
	Account, ObjectType, ObjectID, CostCenter, Text, Contract, TaxCode string
	Amount                                                             int64
	// seit 0.8.0 (Nettorechnung): nachgelagerte Position, Wirkung, verteilt; Kostenbetrag der Basis
	Dependent, Effect string
	Allocated         bool
	Cost              *int64
	// seit 0.11.0: Position, nicht abziehbare Steuer (Nettorechnung), Stichtag der Vorsteuerquote
	Line, NonDeductible int64
	Date                string
	// seit 0.20.0: Belastung an (Art, Kennung), umlagefähig
	Charge, ChargeID string
	Allocable        bool
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
	if typ.ServicePeriodRequired {
		res, err := m.db.Query(ctx, `SELECT line_no, cost_category FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ?
			AND cost_category IS NOT NULL AND (service_from IS NULL OR service_to IS NULL) ORDER BY line_no`, inv.CompanyCode, inv.ID, true)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, r := range res.Rows {
			cat := crud.Str(r[1])
			if _, alloc, err := m.costCategoryOf(ctx, inv.CompanyCode, cat); err == nil && alloc {
				return sdk.Response{}, crud.Invalid("Position %d: %v", toInt(r[0]), servicePeriod(typ, cat, "", ""))
			}
		}
	}
	net := m.isNet(ctx, invoiceDoc, inv.CompanyCode, inv.ID)
	if net {
		if err := m.recalc(ctx, invoiceDoc, inv.CompanyCode, inv.ID, 0); err != nil {
			return sdk.Response{}, err
		}
	}
	res, err := m.db.Query(ctx, `SELECT account_number, object_type, object_id, cost_center, item_text, amount, contract_id, tax_code,
		COALESCE(dependent_type, ''), COALESCE(dependent_effect, ''), allocated, cost_amount, line_no, nondeductible_tax, service_to, service_from,
		COALESCE(charge_type, ''), COALESCE(charge_id, ''), allocable FROM procurement__invoice_item
		WHERE company_code = ? AND invoice_id = ? AND is_active = ? ORDER BY line_no`, inv.CompanyCode, inv.ID, true)
	if err != nil {
		return sdk.Response{}, err
	}
	var items []itemRow
	var total int64
	for _, r := range res.Rows {
		it := itemRow{Account: crud.Str(r[0]), ObjectType: crud.Str(r[1]), ObjectID: crud.Str(r[2]), CostCenter: crud.Str(r[3]), Text: crud.Str(r[4]),
			Amount: toInt(r[5]), Contract: crud.Str(r[6]), TaxCode: crud.Str(r[7]), Dependent: crud.Str(r[8]), Effect: crud.Str(r[9]), Allocated: crud.AsBool(r[10]),
			Line: toInt(r[12]), NonDeductible: toInt(r[13]), Date: serviceDate(r[14], r[15], inv.InvoiceDate, inv.PostingDate),
			Charge: crud.Str(r[16]), ChargeID: crud.Str(r[17]), Allocable: crud.AsBool(r[18])}
		if crud.Str(r[11]) != "" {
			c := toInt(r[11])
			it.Cost = &c
		}
		items = append(items, it)
		if it.Effect != pricing.EffectShare {
			total += it.Amount
		}
	}
	if len(items) == 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s hat keine Positionen", inv.ID)
	}
	if total == 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s: Summe der Positionen ist 0", inv.ID)
	}
	if amt := m.text(ctx, `SELECT invoice_amount FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, inv.CompanyCode, inv.ID); amt != "" && toInt(amt) != total {
		return sdk.Response{}, crud.Invalid("Rechnung %s: Rechnungsbetrag %s ≠ Summe der Positionen %s", inv.ID,
			formatAmount(toInt(amt), m.currencyDecimals(ctx, inv.Currency)), formatAmount(total, m.currencyDecimals(ctx, inv.Currency)))
	}
	taxes := taxCache{m: m, codes: map[string]*taxInfo{}}
	quotas := newQuotaCache(m, inv.CompanyCode)
	subs := &taxAccounts{m: m, cc: inv.CompanyCode, acc: map[string]string{}}
	for _, it := range items {
		if _, err := taxes.get(ctx, it.TaxCode); err != nil && !net {
			return sdk.Response{}, err
		}
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
	header := m.ledgerHeader(ctx, inv)
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
			amt := it.Amount
			if net {
				// netto: Basis mit Kostenbetrag (Anteile heraus, verteilte Zuschläge und nicht
				// abziehbare Steuer hinein); nachgelagerte nur mit eigener Buchung (Konto, Steuerkonto)
				if it.Dependent != "" && it.Allocated {
					continue
				}
				if it.Cost != nil { // Basis: Kostenbetrag; Steuerzeile aufzuteilender Vorsteuer: abziehbarer Teil
					amt = *it.Cost
				}
			}
			nonded := int64(0) // nicht abziehbare Steuer in amt (Konto für nicht abziehbare Vorsteuer)
			if net && it.Dependent == "" {
				nonded = it.NonDeductible
			}
			side := "S"
			if amt < 0 {
				side, amt, nonded = "H", -amt, -nonded
			}
			if amt == 0 {
				continue
			}
			text := it.Text
			if text == "" {
				text = header
			}
			contract := it.Contract
			if contract == "" {
				contract = inv.Contract // Vertrag der Rechnung (seit 0.18.1)
			}
			k := map[string]string{"cost_center": it.CostCenter, "supplier_id": inv.Supplier, "rent_contract_id": contract, "invoice_id": inv.ID}
			typ, obj := it.ObjectType, it.ObjectID
			if obj == "" {
				typ, obj = inv.ObjectType, inv.ObjectID
			}
			if f := m.objectField(ctx, inv.CompanyCode, typ); f != "" && obj != "" && k[f] == "" {
				k[f] = obj
			}
			// Belastung an (seit 0.20.0); Konto der Belastungsart statt Aufwandskonto
			if it.Dependent == "" {
				ct, id, err := m.itemCharge(ctx, inv.CompanyCode, it, typ, obj)
				if err != nil {
					return err
				}
				if ct != nil {
					k["charge_type"], k["charge_id"] = ct.Code, id
					if ct.Account != "" {
						it.Account = ct.Account
					}
				}
			}
			t, _ := taxes.get(ctx, it.TaxCode)
			tax := splitTax(amt, t)
			if net {
				tax = 0 // Steuer ist eine eigene (nachgelagerte) Position
			} else if t != nil && (t.Kind == "NONDEDUCTIBLE" || t.Kind == taxPartial) && t.Rate > 0 {
				// Bruttoposition: Steueranteil herausrechnen; aufzuteilen: abziehbarer Teil nach Quote
				part := int64(math.Round(float64(amt) * t.Rate / (100 + t.Rate)))
				sign := int64(1)
				if side == "H" {
					sign = -1
				}
				var q quotaInfo
				if t.Kind == taxPartial {
					var err error
					if q, err = quotas.get(ctx, typ, obj, it.Date, m.splitMeasurement(ctx, t.Code)); err != nil {
						return crud.Invalid("Position %d: %v", it.Line, err)
					}
					tax = scaleRat(part, q.rate)
				}
				nonded = part - tax
				if err := m.setTaxParts(ctx, inv.CompanyCode, inv.ID, it.Line, sign*nonded, sign*tax, q); err != nil {
					return err
				}
				if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET cost_amount = ? WHERE company_code = ? AND invoice_id = ? AND line_no = ?`,
					sign*(amt-tax), inv.CompanyCode, inv.ID, it.Line); err != nil {
					return err
				}
			}
			main, sub := amt-tax, ""
			if nonded != 0 {
				var err error
				if sub, err = subs.of(ctx, it.Account); err != nil {
					return err
				}
				if sub != "" {
					main -= nonded
				}
			}
			if main != 0 {
				if err := line(it.Account, side, main, text, k); err != nil {
					return err
				}
			}
			if sub != "" {
				if err := line(sub, side, nonded, text+" (nicht abziehbare Vorsteuer)", k); err != nil {
					return err
				}
			}
			if tax != 0 {
				// Abziehbare Vorsteuer: Steueranteil auf das Vorsteuerkonto.
				acc, err := m.glAccount(ctx, inv.CompanyCode, t.Account)
				if err != nil {
					return err
				}
				if err := line(acc, side, tax, text+" ("+t.Code+")", map[string]string{"supplier_id": inv.Supplier, "invoice_id": inv.ID}); err != nil {
					return err
				}
			}
		}
		side, amt := "H", total
		if total < 0 {
			side, amt = "S", -total
		}
		// Kreditorenposition: Rechnung, Vertrag, Objekt der Rechnung (ohne: das eine Objekt
		// aller Positionen) – der Beleg allein ist auswertbar, die Zahlung übernimmt sie
		k := map[string]string{"supplier_id": inv.Supplier, "rent_contract_id": inv.Contract, "invoice_id": inv.ID}
		objType, objID := inv.ObjectType, inv.ObjectID
		if objID == "" {
			objType, objID = uniqueObject(items)
		}
		if f := m.objectField(ctx, inv.CompanyCode, objType); f != "" && objID != "" {
			k[f] = objID
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

// uniqueObject: das eine Objekt aller Positionen ("" bei keinem oder mehreren).
func uniqueObject(items []itemRow) (string, string) {
	typ, obj := "", ""
	for _, it := range items {
		if it.ObjectID == "" {
			continue
		}
		if obj != "" && (it.ObjectID != obj || it.ObjectType != typ) {
			return "", ""
		}
		typ, obj = it.ObjectType, it.ObjectID
	}
	return typ, obj
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
		case invoiceOpen, invoiceDraft:
			// Stornieren braucht einen Stornobeleg – nicht gebuchte Rechnungen werden verworfen.
			return crud.Invalid("Rechnung %s ist nicht gebucht – bitte „Verwerfen …“ statt Stornieren", inv.ID)
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
			return crud.Invalid("Rechnung %s ist %s", inv.ID, statusText(inv.Status))
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

// discardAction: Verwerfen einer nicht gebuchten Rechnung (erfasst oder vorerfasst) –
// ohne Stornobeleg; eine Vorerfassung im Hauptbuch wird mit verworfen.
func (m *Module) discardAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
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
	if err := requireWrite(ctx, invoiceObject, "discard", inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	msg := "Rechnung " + inv.ID + " verworfen"
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		switch inv.Status {
		case invoiceOpen:
		case invoiceDraft:
			if _, err := m.services.Call(ctx, "JournalDraft", "deactivate", map[string]any{"id": inv.DraftID}); err != nil {
				return err
			}
			msg += ", Vorerfassung verworfen"
		case invoicePosted:
			return crud.Invalid("Rechnung %s ist gebucht (Beleg %s) – bitte „Stornieren …“", inv.ID, inv.DocumentNumber)
		default:
			return crud.Invalid("Rechnung %s ist %s", inv.ID, statusText(inv.Status))
		}
		_, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ?, changed_at = ?, changed_by = ?
			WHERE company_code = ? AND invoice_id = ?`, invoiceDiscarded, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID),
			inv.CompanyCode, inv.ID)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "status": invoiceDiscarded, "message": msg}}, nil
}

// migrateDiscarded: früher hießen verworfene (nie gebuchte) Rechnungen „storniert“ –
// ohne Stornobeleg sind sie verworfen.
func (m *Module) migrateDiscarded(ctx context.Context) {
	if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET status = ? WHERE status = ? AND reversal_document_id IS NULL`,
		invoiceDiscarded, invoiceCancelled); err != nil {
		m.log.WarnContext(ctx, "Status verworfen nicht übernommen", "err", err.Error())
	}
}
