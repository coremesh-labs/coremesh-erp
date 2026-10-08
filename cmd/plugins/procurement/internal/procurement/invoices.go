package procurement

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

const (
	invoiceObject = "SupplierInvoice"
	itemObject    = "SupplierInvoiceItem"

	invoiceOpen      = "OPEN"
	invoiceDraft     = "DRAFT"
	invoicePosted    = "POSTED"
	invoiceCancelled = "CANCELLED"
)

var (
	invoiceStatusOptions = []metamodel.Option{{Value: invoiceOpen, Label: "erfasst"}, {Value: invoiceDraft, Label: "vorerfasst (Hauptbuch)"},
		{Value: invoicePosted, Label: "gebucht"}, {Value: invoiceCancelled, Label: "storniert"}}
	lookupInvoice = &metamodel.Lookup{Object: invoiceObject, ValueField: "invoice_id", LabelFields: []string{"supplier_reference"},
		Filters: map[string]string{"company_code": "company_code"}}
)

// invoice: Eingangsrechnung – Nummer <Rechnungsart>-<Jahr>-<n> je Buchungskreis.
func (m *Module) invoice() *crud.Entity {
	return &crud.Entity{
		Object: invoiceObject, Title: "Eingangsrechnungen", Icon: "icon-file", Table: "procurement__invoice", Section: "Beschaffung",
		Keys: []string{"company_code", "invoice_id"}, Order: "company_code, invoice_date DESC, invoice_id DESC", TitleField: "supplier_reference",
		Filters: []string{"company_code", "invoice_type", "supplier_id", "status", "quote_id", "object_id"},
		Search:  []string{"invoice_id", "supplier_reference", "header_text"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "invoice_id", Label: "Rechnung (Nummer)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "invoice_type", Label: "Rechnungsart", Type: tText, Required: true, Immutable: true, Lookup: catalogLookup("InvoiceType")},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "supplier_reference", Label: "Rechnungsnummer des Lieferanten", Type: tText, Required: true, Listable: true},
			{Key: "invoice_date", Label: "Rechnungsdatum", Type: tDate, Required: true, Listable: true},
			{Key: "posting_date", Label: "Buchungsdatum (leer = Rechnungsdatum)", Type: tDate},
			{Key: "due_date", Label: "Fällig am", Type: tDate, Listable: true},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "total", Label: "Betrag (brutto, Summe der Positionen)", Type: tText, Listable: true, ReadOnly: true, Virtual: true},
			{Key: "header_text", Label: "Text", Type: tText},
			{Key: "quote_id", Label: "Angebot", Type: tText, Group: "Zuordnung", Lookup: lookupQuote},
			{Key: "object_type", Label: "Objektart (Standard der Positionen)", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt (Standard der Positionen)", Type: tText, Listable: true, Group: "Zuordnung"},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: invoiceStatusOptions, Group: "Buchung"},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "document_number", Label: "Beleg", Type: tText, ReadOnly: true, Listable: true, Group: "Buchung"},
			{Key: "document_id", Label: "Beleg (ID)", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "id", LabelFields: []string{"document_number"}}},
			{Key: "reversal_document_id", Label: "Stornobeleg", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "id", LabelFields: []string{"document_number"}}},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true},
			// nur im Formular „Stornieren …“
			{Key: "reversal_date", Label: "Storno zum (Buchungsdatum)", Type: tDate, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: itemObject, ForeignKey: "invoice_id",
				Match: map[string]string{"company_code": "company_code", "invoice_id": "invoice_id"},
				Columns: []string{"line_no", "cost_category", "account_number", "amount", "object_id", "allocable", "service_from", "service_to", "item_text"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			if err := m.checkInvoice(ctx, rec, nil); err != nil {
				return err
			}
			typ, err := m.invoiceTypeOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["invoice_type"]))
			if err != nil {
				return err
			}
			nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: rangeInvoice, CompanyCode: crud.Str(rec["company_code"]),
				Key: typ.RangeKey, Year: int(toInt(crud.Str(rec["posting_date"])[:4])), Reference: crud.Str(rec["supplier_reference"])})
			if err != nil {
				return unavailable("Nummernkreis", err)
			}
			rec["invoice_id"], rec["status"] = nr.Number, invoiceOpen
			return nil
		},
		Validate: m.checkInvoice,
		Decorate: m.decorateInvoice,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true,
				Confirm: "Rechnung im Hauptbuch vorerfassen und prüfen (bei automatischer Buchung: buchen)?"}, Handle: m.postAction},
			{ActionConfig: metamodel.ActionConfig{Name: "cancel", Label: "Stornieren …", Record: true, Fields: []string{"reversal_date"}},
				Handle: m.cancelAction},
		},
	}
}

func (m *Module) checkInvoice(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != invoiceOpen {
		return crud.Invalid("Rechnung %s ist %s und nicht mehr änderbar", crud.Str(old["invoice_id"]), statusText(crud.Str(old["status"])))
	}
	cc := crud.Str(rec["company_code"])
	rec["invoice_type"] = trimUpper(rec["invoice_type"])
	if _, err := m.invoiceTypeOf(ctx, cc, crud.Str(rec["invoice_type"])); err != nil {
		return err
	}
	supplier := strings.TrimSpace(crud.Str(rec["supplier_id"]))
	rec["supplier_id"] = supplier
	if err := m.requirePartner(ctx, supplier); err != nil {
		return err
	}
	ref := strings.TrimSpace(crud.Str(rec["supplier_reference"]))
	if ref == "" {
		return crud.Invalid("Rechnungsnummer des Lieferanten ist Pflicht")
	}
	rec["supplier_reference"] = ref
	// Doppelt erfasst? Gleiche Rechnungsnummer desselben Lieferanten (nicht storniert).
	dup, err := m.db.Query(ctx, `SELECT invoice_id FROM procurement__invoice WHERE company_code = ? AND supplier_id = ? AND supplier_reference = ?
		AND status <> ? AND invoice_id <> ?`, cc, supplier, ref, invoiceCancelled, crud.Str(rec["invoice_id"]))
	if err != nil {
		return err
	}
	if len(dup.Rows) > 0 {
		return crud.Invalid("Rechnung %s des Lieferanten ist bereits als %s erfasst", ref, crud.Str(dup.Rows[0][0]))
	}
	inv, err := date(rec, "invoice_date", "Rechnungsdatum")
	if err != nil {
		return err
	}
	if crud.Str(rec["posting_date"]) == "" {
		rec["posting_date"] = inv
	}
	if _, err := date(rec, "posting_date", "Buchungsdatum"); err != nil {
		return err
	}
	if err := optDate(rec, "due_date"); err != nil {
		return err
	}
	if crud.Str(rec["currency"]) == "" {
		rec["currency"] = "EUR"
	}
	rec["currency"] = trimUpper(rec["currency"])
	if q := strings.TrimSpace(crud.Str(rec["quote_id"])); q != "" {
		res, err := m.db.Query(ctx, `SELECT supplier_id, status FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, cc, q)
		if err != nil {
			return err
		}
		switch {
		case len(res.Rows) == 0:
			return crud.Invalid("Angebot %s gibt es im Buchungskreis %s nicht", q, cc)
		case crud.Str(res.Rows[0][0]) != supplier:
			return crud.Invalid("Angebot %s ist von einem anderen Lieferanten", q)
		case crud.Str(res.Rows[0][1]) != quoteAccepted:
			return crud.Invalid("Angebot %s ist nicht angenommen", q)
		}
	}
	rec["quote_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["quote_id"])))
	rec["changed_at"] = now()
	return m.checkObject(ctx, rec)
}

// decorateInvoice: Summe der aktiven Positionen.
func (m *Module) decorateInvoice(ctx context.Context, rec crud.Record) error {
	res, err := m.db.Query(ctx, `SELECT COALESCE(SUM(amount), 0) FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ?`,
		rec["company_code"], rec["invoice_id"], true)
	if err != nil {
		return err
	}
	rec["total"] = toInt(res.Rows[0][0])
	return m.decorateAmount(ctx, rec, "total")
}

func statusText(s string) string {
	for _, o := range invoiceStatusOptions {
		if o.Value == s {
			return o.Label
		}
	}
	return s
}

// invoiceItem: Position – Kostenart, Sachkonto, Betrag, Objekt, umlagefähig,
// Leistungszeitraum. Nur änderbar, solange die Rechnung erfasst ist;
// „Entfernen“ = inaktivieren.
func (m *Module) invoiceItem() *crud.Entity {
	return &crud.Entity{
		Object: itemObject, Title: "Rechnungspositionen", Icon: "icon-list", Table: "procurement__invoice_item", Section: "Beschaffung",
		Keys: []string{"company_code", "invoice_id", "line_no"}, Order: "company_code, invoice_id, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "invoice_id", "cost_category", "object_id", "allocable"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "invoice_id", Label: "Rechnung", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupInvoice},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Listable: true, Trigger: true, Lookup: catalogLookup("CostCategory")},
			{Key: "account_number", Label: "Sachkonto (leer = Kostenart)", Type: tText, Listable: true, Lookup: lookupAccount},
			{Key: "amount", Label: "Betrag (brutto; negativ = Gutschrift)", Type: tText, Required: true, Listable: true},
			{Key: "object_type", Label: "Objektart (leer = Rechnung)", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt (leer = Rechnung)", Type: tText, Listable: true, Group: "Zuordnung"},
			{Key: "cost_center", Label: "Kostenstelle", Type: tText, Group: "Zuordnung"},
			{Key: "allocable", Label: "Umlagefähig (Nebenkostenabrechnung)", Type: tBool, Listable: true, Group: "Nebenkosten"},
			{Key: "service_from", Label: "Leistungszeitraum von", Type: tDate, Group: "Nebenkosten"},
			{Key: "service_to", Label: "Leistungszeitraum bis", Type: tDate, Group: "Nebenkosten"},
			{Key: "item_text", Label: "Text", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access:    &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare:   m.prepareItem,
		Validate:  m.checkItem,
		FormState: m.itemFormState,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["currency"] = m.text(ctx, `SELECT currency FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, rec["company_code"], rec["invoice_id"])
			err := m.decorateAmount(ctx, rec, "amount")
			delete(rec, "currency")
			return err
		},
	}
}

// prepareItem: nächste Positionsnummer der Rechnung.
func (m *Module) prepareItem(ctx context.Context, rec crud.Record) error {
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ?`,
		rec["company_code"], rec["invoice_id"])
	if err != nil {
		return err
	}
	rec["line_no"] = toInt(res.Rows[0][0])
	return nil
}

func (m *Module) checkItem(ctx context.Context, rec, _ crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"])
	res, err := m.db.Query(ctx, `SELECT status, currency, object_type, object_id FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, cc, id)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Rechnung %s gibt es im Buchungskreis %s nicht", id, cc)
	}
	h := res.Rows[0]
	if crud.Str(h[0]) != invoiceOpen {
		return crud.Invalid("Rechnung %s ist %s – Positionen nicht mehr änderbar", id, statusText(crud.Str(h[0])))
	}
	acc := strings.TrimSpace(crud.Str(rec["account_number"]))
	if cat := trimUpper(rec["cost_category"]); cat != "" {
		catAcc, _, err := m.costCategoryOf(ctx, cc, cat)
		if err != nil {
			return err
		}
		rec["cost_category"] = cat
		if acc == "" {
			acc = catAcc
		}
	} else {
		rec["cost_category"] = nil
	}
	if acc == "" {
		return crud.Invalid("Sachkonto ist Pflicht (oder Kostenart mit Sachkonto)")
	}
	if rec["account_number"], err = m.glAccount(ctx, cc, acc); err != nil {
		return err
	}
	if !isMinor(rec["amount"]) {
		minor, err := parseAmount(crud.Str(rec["amount"]), m.currencyDecimals(ctx, crud.Str(h[1])))
		if err != nil {
			return crud.Invalid("%v", err)
		}
		rec["amount"] = minor
	}
	if toInt(rec["amount"]) == 0 {
		return crud.Invalid("Betrag ist Pflicht (ungleich 0)")
	}
	if crud.Str(rec["object_id"]) == "" && crud.Str(h[3]) != "" {
		rec["object_type"], rec["object_id"] = h[2], h[3]
	}
	if err := m.checkObject(ctx, rec); err != nil {
		return err
	}
	for _, k := range []string{"service_from", "service_to"} {
		if err := optDate(rec, k); err != nil {
			return err
		}
	}
	if f, t := crud.Str(rec["service_from"]), crud.Str(rec["service_to"]); f != "" && t != "" && t < f {
		return crud.Invalid("Leistungszeitraum: bis (%s) liegt vor von (%s)", t, f)
	}
	defaults(rec, map[string]any{"allocable": false})
	rec["cost_center"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["cost_center"])))
	return nil
}

// itemFormState: Kostenart gewählt → Sachkonto und „umlagefähig“ vorschlagen.
func (m *Module) itemFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	cat := strings.ToUpper(strings.TrimSpace(req.Values["cost_category"]))
	if cat == "" || req.Values["company_code"] == "" {
		return st, nil
	}
	acc, allocable, err := m.costCategoryOf(ctx, req.Values["company_code"], cat)
	if err != nil {
		return st, nil
	}
	if acc != "" && req.Values["account_number"] == "" {
		st.Fields["account_number"] = metamodel.FieldState{Value: &acc}
	}
	v := "false"
	if allocable {
		v = "true"
	}
	st.Fields["allocable"] = metamodel.FieldState{Value: &v}
	return st, nil
}
