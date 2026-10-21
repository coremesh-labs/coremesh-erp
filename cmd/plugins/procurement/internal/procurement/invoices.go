package procurement

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

const (
	invoiceObject = "SupplierInvoice"
	itemObject    = "SupplierInvoiceItem"

	invoiceOpen      = "OPEN"
	invoiceDraft     = "DRAFT"
	invoicePosted    = "POSTED"
	invoiceCancelled = "CANCELLED"
	invoiceDiscarded = "DISCARDED"
)

var (
	invoiceStatusOptions = []metamodel.Option{{Value: invoiceOpen, Label: "erfasst"}, {Value: invoiceDraft, Label: "vorerfasst (Hauptbuch)"},
		{Value: invoicePosted, Label: "gebucht"}, {Value: invoiceCancelled, Label: "storniert"},
		{Value: invoiceDiscarded, Label: "verworfen"}}
	lookupInvoice = &metamodel.Lookup{Object: invoiceObject, ValueField: "invoice_id", LabelFields: []string{"supplier_reference"},
		Filters: map[string]string{"company_code": "company_code"}}
)

// invoice: Eingangsrechnung – Nummer <Rechnungsart>-<Jahr>-<n> je Buchungskreis.
func (m *Module) invoice() *crud.Entity {
	return &crud.Entity{
		Object: invoiceObject, Title: "Eingangsrechnungen", Icon: "icon-file", Table: "procurement__invoice", Section: "Beschaffung",
		Events: true, CompanyCodeField: "company_code", // seit 0.12.2: Suchtext im DMS
		Keys: []string{"company_code", "invoice_id"}, Order: "company_code, invoice_date DESC, invoice_id DESC", TitleField: "supplier_reference",
		Filters: []string{"company_code", "invoice_type", "supplier_id", "status", "quote_id", "object_id"},
		Search:  []string{"invoice_id", "supplier_reference", "header_text"},
		// Status auch als Liste (seit 0.13.1), z. B. OPEN,DRAFT,POSTED für die Auswahl im DMS.
		FilterExpr: map[string]func(v any) (string, []any){"status": statusFilter},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "invoice_id", Label: "Rechnung (Nummer)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "invoice_type", Label: "Rechnungsart", Type: tText, Required: true, Immutable: true, Lookup: catalogLookup("InvoiceType")},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "supplier_reference", Label: "Rechnungsnummer des Lieferanten", Short: "Rechnungsnr.", Type: tText, Required: true, Listable: true},
			{Key: "invoice_date", Label: "Rechnungsdatum", Type: tDate, Required: true, Listable: true},
			{Key: "posting_date", Label: "Buchungsdatum (leer = Rechnungsdatum)", Type: tDate},
			{Key: "due_date", Label: "Fällig am", Type: tDate, Listable: true},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "total", Label: "Summe der Positionen (Soll, brutto)", Short: "Summe", Type: tText, Listable: true, ReadOnly: true, Virtual: true},
			{Key: "invoice_amount", Label: "Rechnungsbetrag laut Beleg (Haben, brutto; Differenz → Position)", Type: tText, Listable: true},
			{Key: "tax_code", Label: "Steuerkennzeichen (Standard der Positionen)", Short: "StKz", Type: tText, Lookup: lookupTaxCode},
			{Key: "framework_contract", Label: "Vertrag (Rechnung zum Vertrag, Rahmenvertrag)", Type: tText, Group: "Zuordnung",
				Lookup: &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "header_text", Label: "Text", Type: tText},
			{Key: "net_amounts", Label: "Positionen netto (Umsatzsteuer als nachgelagerte Position)", Type: tBool, Group: "Positionen"},
			{Key: "deviation", Label: "Abweichung laut Rechnung (Summe der geprüften Positionen)", Short: "Abweichung", Type: tText, Virtual: true, Group: "Positionen"},
			{Key: "dependent_types", Label: "Nachgelagerte Konditionen (leer = Standard bzw. Vertrag, „-“ = keine)", Type: tSel, Multiple: true, Group: "Positionen"},
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
			// nur im Formular „Vertragskonditionen übernehmen …“
			{Key: "conditions_contract", Label: "Vertrag", Type: tText, ActionOnly: true,
				Lookup: &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"}}},
			{Key: "conditions_date", Label: "Konditionen gültig am", Type: tDate, ActionOnly: true},
			// nur im Formular „Referenz korrigieren …“ (gebuchte Rechnung)
			{Key: "new_supplier_reference", Label: "Rechnungsnummer des Lieferanten", Type: tText, ActionOnly: true},
			{Key: "new_invoice_text", Label: "Text", Type: tText, ActionOnly: true},
			{Key: "change_reason", Label: "Grund der Änderung", Type: tText, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: itemObject, ForeignKey: "invoice_id",
				Match: map[string]string{"company_code": "company_code", "invoice_id": "invoice_id"},
				Columns: []string{"line_no", "parent_line", "item_text", "quantity", "unit", "unit_price", "amount", "stated_amount", "deviation", "tax_code",
					"scheme", "cost_category", "account_number", "object_id", "allocable", "service_from", "service_to"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			if rec["net_amounts"] == nil || crud.Str(rec["net_amounts"]) == "" {
				rec["net_amounts"] = true // seit 0.8.0: neue Rechnungen netto
			}
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
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			return m.recalc(ctx, invoiceDoc, crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"]), 0)
		},
		Decorate:  m.decorateInvoice,
		FormState: m.invoiceFormState,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true,
				Confirm: "Rechnung im Hauptbuch vorerfassen und prüfen (bei automatischer Buchung: buchen)?"}, Handle: m.postAction},
			{ActionConfig: metamodel.ActionConfig{Name: "takeConditions", Label: "Vertragskonditionen übernehmen …", Record: true, FormState: true,
				Fields:  []string{"conditions_contract", "conditions_date"},
				Confirm: "Die Positionen der Rechnung durch die Konditionen des Vertrags ersetzen?"}, Handle: m.takeConditionsAction},
			{ActionConfig: metamodel.ActionConfig{Name: "takeQuote", Label: "Positionen aus dem Angebot übernehmen …", Record: true,
				Confirm: "Die Positionen der Rechnung durch die des angenommenen Angebots ersetzen?"}, Handle: m.takeQuoteAction},
			{ActionConfig: metamodel.ActionConfig{Name: "discard", Label: "Verwerfen …", Record: true,
				Confirm: "Nicht gebuchte Rechnung verwerfen (eine Vorerfassung im Hauptbuch wird mit verworfen)?"}, Handle: m.discardAction},
			{ActionConfig: metamodel.ActionConfig{Name: "cancel", Label: "Stornieren …", Record: true, Fields: []string{"reversal_date"}},
				Handle: m.cancelAction},
			{ActionConfig: metamodel.ActionConfig{Name: correctAction, Label: "Referenz korrigieren …", Record: true, FormState: true,
				Fields: []string{"new_supplier_reference", "new_invoice_text", "change_reason"}}, Handle: m.correctReferenceAction},
		},
		Routes: map[string]module.HandlerFunc{"fromTemplate": m.fromTemplateAction, "contractDraft": m.contractDraftAction},
	}
}

func (m *Module) checkInvoice(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != invoiceOpen {
		return crud.Invalid("Rechnung %s ist %s und nicht mehr änderbar", crud.Str(old["invoice_id"]), statusText(crud.Str(old["status"])))
	}
	cc := crud.Str(rec["company_code"])
	if err := m.checkInvoiceContract(ctx, cc, rec); err != nil {
		return err
	}
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
		AND status NOT IN (?, ?) AND invoice_id <> ?`, cc, supplier, ref, invoiceCancelled, invoiceDiscarded, crud.Str(rec["invoice_id"]))
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
	if err := m.checkDependentTypes(ctx, rec, old, crud.Str(rec["framework_contract"])); err != nil {
		return err
	}
	if err := m.checkTaxCode(ctx, rec); err != nil {
		return err
	}
	if err := m.parseInvoiceAmount(ctx, rec); err != nil {
		return err
	}
	if err := m.checkObject(ctx, rec); err != nil {
		return err
	}
	if old != nil {
		return m.balanceAfterAmount(ctx, rec, old)
	}
	return nil
}

// decorateInvoice: Summe der aktiven Positionen.
func (m *Module) decorateInvoice(ctx context.Context, rec crud.Record) error {
	total, err := m.docTotal(ctx, invoiceDoc, crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"]), 0)
	if err != nil {
		return err
	}
	rec["total"] = total
	if dev := m.text(ctx, `SELECT SUM(stated_amount - amount) FROM procurement__invoice_item WHERE company_code = ? AND invoice_id = ? AND is_active = ?
		AND stated_amount IS NOT NULL`, rec["company_code"], rec["invoice_id"], true); dev != "" {
		rec["deviation"] = toInt(dev)
		if err := m.decorateAmount(ctx, rec, "deviation"); err != nil {
			return err
		}
	}
	if err := m.decorateAmount(ctx, rec, "total"); err != nil {
		return err
	}
	if crud.Str(rec["invoice_amount"]) != "" {
		return m.decorateAmount(ctx, rec, "invoice_amount")
	}
	return nil
}

// statusFilter: Status oder Komma-Liste von Status.
func statusFilter(v any) (string, []any) {
	var args []any
	for _, s := range strings.Split(crud.Str(v), ",") {
		if s = strings.TrimSpace(s); s != "" {
			args = append(args, s)
		}
	}
	if len(args) == 0 {
		return "1 = 1", nil
	}
	return "status IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(args)), ", ") + ")", args
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
		Events: true, CompanyCodeField: "company_code", // seit 0.12.2: Suchtext im DMS
		Keys: []string{"company_code", "invoice_id", "line_no"}, Order: "company_code, invoice_id, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "invoice_id", "cost_category", "object_id", "allocable"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "invoice_id", Label: "Rechnung", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupInvoice},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Listable: true, Trigger: true, Lookup: catalogLookup("CostCategory")},
			{Key: "account_number", Label: "Sachkonto (leer = Kostenart)", Type: tText, Listable: true, Lookup: lookupAccount},
			{Key: "quantity", Label: "Menge (leer = 1)", Type: tText, Listable: true},
			{Key: "unit", Label: "Mengeneinheit (leer = Stück)", Short: "Einheit", Type: tText, Listable: true, Lookup: lookupUnit},
			{Key: "unit_price", Label: "Einzelpreis (netto; bei älteren Rechnungen brutto)", Type: tText, Listable: true},
			{Key: "amount", Label: "Gesamtpreis (= Menge × Einzelpreis; negativ = Gutschrift)", Type: tText, Listable: true},
			{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
			{Key: "scheme", Label: "Kalkulationsschema (abhängige Zeilen, z. B. STROM)", Short: "Schema", Type: tText, Listable: true, Lookup: lookupScheme},
			{Key: "parent_line", Label: "zu Position (abhängige Zeile)", Type: tNum, Listable: true, ReadOnly: true, Group: "Herkunft"},
			{Key: "scheme_step", Label: "Schritt des Schemas", Type: tNum, ReadOnly: true, Group: "Herkunft"},
			{Key: "price_override", Label: "Satz überschrieben", Type: tBool, ReadOnly: true, Group: "Herkunft"},
			{Key: "stated_amount", Label: "Betrag laut Rechnung (Prüfung)", Short: "lt. Rechnung", Type: tText, Listable: true, Group: "Prüfung"},
			{Key: "deviation", Label: "Abweichung (laut Rechnung − gerechnet)", Type: tText, Listable: true, Virtual: true, Group: "Prüfung"},
			{Key: "tax_code", Label: "Steuerkennzeichen (leer = Rechnung)", Short: "StKz", Type: tText, Listable: true, Lookup: lookupTaxCode},
			{Key: "object_type", Label: "Objektart (leer = Rechnung)", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt (leer = Rechnung)", Type: tText, Listable: true, Group: "Zuordnung"},
			{Key: "cost_center", Label: "Kostenstelle", Type: tText, Group: "Zuordnung"},
			{Key: "contract_id", Label: "Mietvertrag (Kosten eines Mieters, z. B. zusätzliche Anfahrt)", Type: tText, Group: "Zuordnung",
				Lookup: &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "allocable", Label: "Umlagefähig (Nebenkostenabrechnung)", Type: tBool, Listable: true, Group: "Nebenkosten"},
			{Key: "service_from", Label: "Leistungszeitraum von", Short: "Leistung von", Type: tDate, Group: "Nebenkosten"},
			{Key: "service_to", Label: "Leistungszeitraum bis", Short: "Leistung bis", Type: tDate, Group: "Nebenkosten"},
			{Key: "item_text", Label: "Text", Type: tText, Listable: true},
			{Key: "condition_type", Label: "Konditionsart (aus dem Vertrag)", Short: "Kond.-Art", Type: tText, ReadOnly: true, Group: "Herkunft"},
			{Key: "dependent_type", Label: "Nachgelagerte Konditionsart (gerechnet)", Short: "Folgeart", Type: tText, ReadOnly: true, Listable: true, Group: "Herkunft"},
			{Key: "dependent_effect", Label: "Wirkung", Type: tSel, ReadOnly: true, Options: dependentEffects, Group: "Herkunft"},
			{Key: "allocated", Label: "Auf die Basispositionen verteilt", Type: tBool, ReadOnly: true, Group: "Herkunft"},
			{Key: "cost_amount", Label: "Kostenbetrag (netto − Anteil + verteilte Zuschläge und nicht abziehbare Steuer)", Type: tText, ReadOnly: true, Group: "Herkunft"},
			{Key: "nondeductible_tax", Label: "davon nicht abziehbare Vorsteuer", Type: tText, ReadOnly: true, Group: "Herkunft"},
			{Key: "deductible_tax", Label: "Abziehbare Vorsteuer (aufgeteilt)", Type: tText, ReadOnly: true, Group: "Herkunft"},
			{Key: "tax_quota_text", Label: "Vorsteuerquote", Type: tText, ReadOnly: true, Group: "Herkunft"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare:  m.prepareItem,
		Validate: m.checkItem,
		AfterCreate: func(ctx context.Context, rec crud.Record) error {
			return m.recalc(ctx, invoiceDoc, crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"]), 0)
		},
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			return m.recalc(ctx, invoiceDoc, crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"]), 0)
		},
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"])
			if crud.Str(rec["parent_line"]) != "" {
				return nil, crud.Invalid("Position %d gehört zum Kalkulationsschema der Position %d – das Schema dort entfernen", toInt(rec["line_no"]), toInt(rec["parent_line"]))
			}
			if m.isNet(ctx, invoiceDoc, cc, id) {
				return nil, m.recalc(ctx, invoiceDoc, cc, id, toInt(rec["line_no"]))
			}
			return nil, m.balanceAfterItem(ctx, cc, id, toInt(rec["line_no"]), 0)
		},
		FormState: m.itemFormState,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["currency"] = m.text(ctx, `SELECT currency FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, rec["company_code"], rec["invoice_id"])
			if crud.Str(rec["stated_amount"]) != "" {
				rec["deviation"] = toInt(rec["stated_amount"]) - toInt(rec["amount"])
			}
			err := m.decorateAmount(ctx, rec, "amount", "cost_amount", "stated_amount", "deviation", "nondeductible_tax", "deductible_tax")
			delete(rec, "currency")
			decoratePosition(rec)
			return err
		},
	}
}

// checkTaxOverride: Eine nachgelagerte Position wird gerechnet; an einer Steuerzeile ist nur
// der Betrag laut Rechnung änderbar – bis zur Rundungsdifferenz der Rechnungsart wird er
// ihr Betrag (recalc nach dem Schreiben), darüber bleibt er als Abweichung sichtbar.
func (m *Module) checkTaxOverride(ctx context.Context, rec, old crud.Record) error {
	if !m.isTaxDependent(ctx, crud.Str(old["company_code"]), crud.Str(old["dependent_type"])) {
		return crud.Invalid("Position %d ist nachgelagert (%s) und wird gerechnet – Arten am Rechnungskopf ändern", toInt(old["line_no"]), crud.Str(old["dependent_type"]))
	}
	stated := rec["stated_amount"]
	for k := range rec {
		rec[k] = old[k]
	}
	rec["stated_amount"] = stated
	return nil
}

// isTaxDependent: nachgelagerte Art rechnet Steuer als Zuschlag.
func (m *Module) isTaxDependent(ctx context.Context, cc, code string) bool {
	defs, _ := m.dependentDefs(ctx, cc)
	for _, d := range defs {
		if d.Code == code {
			return d.Method == pricing.MethodTax && d.Effect == pricing.EffectSurcharge
		}
	}
	return false
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

func (m *Module) checkItem(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["invoice_id"])
	res, err := m.db.Query(ctx, `SELECT status, currency, object_type, object_id, invoice_type FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, cc, id)
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
	if err := m.parseStated(ctx, rec, crud.Str(h[1])); err != nil {
		return err
	}
	if old != nil && crud.Str(old["dependent_type"]) != "" {
		return m.checkTaxOverride(ctx, rec, old)
	}
	if old != nil && crud.Str(old["parent_line"]) != "" {
		// abhängige Zeile eines Schemas: nur Satz (überschreibt das Schema) und Betrag laut Rechnung
		if p := strings.TrimSpace(crud.Str(rec["unit_price"])); p != "" && p != crud.Str(old["unit_price"]) {
			if _, err := pricing.ParseDecimal(p); err != nil {
				return crud.Invalid("Satz: %v", err)
			}
			rec["price_override"] = true
		}
		return nil // Betrag rechnet recalc nach dem Schreiben
	}
	if s := trimUpper(rec["scheme"]); s != "" {
		if m.text(ctx, `SELECT code FROM procurement__scheme WHERE company_code = ? AND code = ? AND is_active = ?`, cc, s, true) == "" {
			return crud.Invalid("Kalkulationsschema %s gibt es im Buchungskreis %s nicht", s, cc)
		}
		rec["scheme"] = s
	} else {
		rec["scheme"] = nil
	}
	rec["dependent_type"], rec["dependent_effect"], rec["allocated"], rec["cost_amount"] = nil, nil, false, nil
	rec["nondeductible_tax"], rec["deductible_tax"], rec["tax_quota"], rec["tax_quota_text"], rec["tax_quota_date"] = nil, nil, nil, nil, nil
	rec["condition_type"] = nilIfEmpty(trimUpper(rec["condition_type"]))
	acc := strings.TrimSpace(crud.Str(rec["account_number"]))
	allocable := false
	if cat := trimUpper(rec["cost_category"]); cat != "" {
		catAcc, alloc, err := m.costCategoryOf(ctx, cc, cat)
		if err != nil {
			return err
		}
		rec["cost_category"] = cat
		allocable = alloc
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
	if err := m.itemPrice(ctx, rec, old, cc, m.currencyDecimals(ctx, crud.Str(h[1]))); err != nil {
		return err
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
	if allocable {
		typ, err := m.invoiceTypeOf(ctx, cc, crud.Str(h[4]))
		if err != nil {
			return err
		}
		if err := servicePeriod(typ, crud.Str(rec["cost_category"]), crud.Str(rec["service_from"]), crud.Str(rec["service_to"])); err != nil {
			return err
		}
	}
	defaults(rec, map[string]any{"allocable": false})
	rec["cost_center"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["cost_center"])))
	if crud.Str(rec["tax_code"]) == "" {
		rec["tax_code"] = nilIfEmpty(m.text(ctx, `SELECT tax_code FROM procurement__invoice WHERE company_code = ? AND invoice_id = ?`, cc, id))
	}
	if err := m.checkTaxCode(ctx, rec); err != nil {
		return err
	}
	if c := strings.TrimSpace(crud.Str(rec["contract_id"])); c != "" {
		if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + c}); err != nil {
			return crud.Invalid("Mietvertrag %s gibt es im Buchungskreis %s nicht", c, cc)
		}
		rec["contract_id"] = c
	} else {
		rec["contract_id"] = nil
	}
	// Soll geändert: Haben (Rechnungsbetrag) folgt.
	if m.isNet(ctx, invoiceDoc, cc, id) {
		return nil // nachgelagerte Positionen und Ausgleich: recalc nach dem Schreiben
	}
	return m.balanceAfterItem(ctx, cc, id, toInt(rec["line_no"]), toInt(rec["amount"]))
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

// servicePeriod: Rechnungsart mit Pflicht zum Leistungszeitraum – eine Position
// mit umlagefähiger Kostenart braucht von und bis (Zuordnung zum Abrechnungsjahr
// der Nebenkosten, unabhängig vom Buchungsdatum).
func servicePeriod(typ *typeRow, cat, from, to string) error {
	if typ.ServicePeriodRequired && (from == "" || to == "") {
		return crud.Invalid("Kostenart %s ist umlagefähig: Leistungszeitraum von und bis ist Pflicht (Rechnungsart %s)", cat, typ.Code)
	}
	return nil
}
