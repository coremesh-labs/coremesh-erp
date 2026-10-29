package procurement

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// Auftrag (seit 0.14.0): an einen Lieferanten, aus einem oder mehreren Angeboten
// („Auftrag erteilen …“ am Angebot) oder frei erfasst. Positionen wie am Angebot
// (Summe, nachgelagerte Umsatzsteuer), Texte aus Textbausteinen (Markdown mit
// Platzhaltern, orderletter.go), Kontakte (Geschäftspartner in einer Rolle).
// Entwurf → „Auftrag erteilen …“ (Schreiben im DMS) → erteilt; „Zurück zum
// Entwurf“ für ein neues Schreiben, „Stornieren“.

const (
	itemKindObject     = "QuoteItemKind"
	orderTypeObject    = "PurchaseOrderType"
	textBlockObject    = "PurchaseOrderTextBlock"
	contactRoleObject  = "PurchaseOrderContactRole"
	orderObject        = "PurchaseOrder"
	orderQuoteObject   = "PurchaseOrderQuote"
	orderItemObject    = "PurchaseOrderItem"
	orderTextObject    = "PurchaseOrderText"
	orderContactObject = "PurchaseOrderContact"

	orderDraft     = "DRAFT"
	orderIssued    = "ISSUED"
	orderCancelled = "CANCELLED"

	sectionIntro     = "INTRO"
	sectionAgreement = "AGREEMENT"
	sectionClosing   = "CLOSING"
	sectionMail      = "MAIL"
)

var (
	orderStatusOptions = []metamodel.Option{{Value: orderDraft, Label: "Entwurf"}, {Value: orderIssued, Label: "erteilt"},
		{Value: orderCancelled, Label: "storniert"}}
	// Abschnitte des Schreibens (Aufbau der Vorlage)
	sectionOptions = []metamodel.Option{{Value: sectionIntro, Label: "Einleitung"}, {Value: sectionAgreement, Label: "Vereinbarung (nummeriert)"},
		{Value: sectionClosing, Label: "Schluss"}, {Value: sectionMail, Label: "Text der E-Mail"}}
	lookupOrder = &metamodel.Lookup{Object: orderObject, ValueField: "order_id", LabelFields: []string{"description"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupTemplate = &metamodel.Lookup{Object: "TemplateDefinition", ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"usage": "=" + usageOrder}}
)

// usageOrder: Verwendung der Vorlagen für das Auftragsschreiben (Modul template).
const usageOrder = "PURCHASE_ORDER"

// --- Kataloge ------------------------------------------------------------------------

// itemKind: Positionsart der Angebote – zählt zur Summe, beim Beauftragen vorausgewählt.
func (m *Module) itemKind() *crud.Entity {
	return m.catalog(itemKindObject, "Positionsarten", "procurement__item_kind", catalogFields(
		crud.Field{Key: "short", Label: "Kürzel im Schreiben (z. B. Alt.)", Short: "Kürzel", Type: tText, Listable: true},
		crud.Field{Key: "in_total", Label: "Zählt zur Summe des Angebots", Short: "Summe", Type: tBool, Listable: true},
		crud.Field{Key: "preselected", Label: "Beim Beauftragen vorausgewählt", Short: "Vorausgewählt", Type: tBool, Listable: true},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"in_total": true, "preselected": true})
		return nil
	})
}

// orderType: Auftragsart – Vorlage, Dokumentart im DMS, Absender, Unterschrift, Betreff, E-Mail.
func (m *Module) orderType() *crud.Entity {
	return m.catalog(orderTypeObject, "Auftragsarten", "procurement__order_type", catalogFields(
		crud.Field{Key: "template", Label: "Vorlage (Verwendung PURCHASE_ORDER; leer = die erste)", Short: "Vorlage", Type: tText, Listable: true, Lookup: lookupTemplate},
		crud.Field{Key: "doc_type", Label: "Dokumentart im DMS (Auftragsschreiben)", Short: "Dokumentart", Type: tText, Listable: true,
			Lookup: &metamodel.Lookup{Object: "DmsDocType", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "sender_roles", Label: "Absender: Rollen am Objekt in dieser Reihenfolge (z. B. SEADM,OWNER; sonst Partner des Buchungskreises)", Type: tText, Group: "Schreiben"},
		crud.Field{Key: "signature_doc_type", Label: "Unterschrift: Dokumentart im DMS am Absender (z. B. SIGNATURE)", Type: tText, Group: "Schreiben"},
		crud.Field{Key: "subject", Label: "Betreff (Platzhalter wie in den Textbausteinen)", Type: tText, Wide: true, Group: "Schreiben"},
		crud.Field{Key: "show_items", Label: "Beauftragte Positionen im Schreiben aufführen", Type: tBool, Group: "Schreiben"},
		crud.Field{Key: "mail_subject", Label: "Betreff der E-Mail (leer = Betreff des Schreibens)", Type: tText, Wide: true, Group: "E-Mail"},
		crud.Field{Key: "mail_block", Label: "Text der E-Mail (Textbaustein, Abschnitt E-Mail)", Type: tText, Group: "E-Mail", Lookup: catalogLookup(textBlockObject)},
		crud.Field{Key: "mail_category", Label: "E-Mail-Adresse des Lieferanten: Kommunikationskategorie (z. B. EMAIL)", Type: tText, Group: "E-Mail"},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"show_items": false})
		for _, k := range []string{"doc_type", "signature_doc_type", "mail_block", "mail_category"} {
			rec[k] = nilIfEmpty(trimUpper(rec[k]))
		}
		rec["sender_roles"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["sender_roles"])), ","))
		return nil
	})
}

// textBlock: Textbaustein in Markdown mit Platzhaltern (orderletter.go).
func (m *Module) textBlock() *crud.Entity {
	return m.catalog(textBlockObject, "Textbausteine (Aufträge)", "procurement__text_block", catalogFields(
		crud.Field{Key: "section", Label: "Abschnitt", Type: tSel, Required: true, Listable: true, Options: sectionOptions},
		crud.Field{Key: "text", Label: "Text (Markdown: **fett**, *kursiv*, Listen; Platzhalter {auftrag:feld}, {angebote}, {objekt:feld}, {lieferant:feld}, {summe:netto})",
			Short: "Text", Type: tArea, Required: true, Wide: true, Listable: true},
		crud.Field{Key: "is_default", Label: "Bei jedem neuen Auftrag übernehmen", Short: "Standard", Type: tBool, Listable: true},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"is_default": false})
		return checkPlaceholders(crud.Str(rec["text"]), false)
	})
}

// contactRole: Rolle eines Kontakts im Auftrag mit ihrem Satz.
func (m *Module) contactRole() *crud.Entity {
	return m.catalog(contactRoleObject, "Kontaktrollen (Aufträge)", "procurement__contact_role", catalogFields(
		crud.Field{Key: "text", Label: "Satz im Schreiben (Markdown; Platzhalter {kontakt:name}, {kontakt:person} und die der Textbausteine)", Short: "Satz",
			Type: tArea, Wide: true, Listable: true},
		crud.Field{Key: "categories", Label: "Kommunikationskategorien im Schreiben (Komma, z. B. PHONE,MOBILE,EMAIL; leer = alle)", Short: "Kategorien", Type: tText},
	), func(_ context.Context, rec crud.Record) error {
		rec["categories"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["categories"])), ","))
		return checkPlaceholders(crud.Str(rec["text"]), true)
	})
}

// splitCodes: Komma-Liste → Schlüssel in Großbuchstaben.
func splitCodes(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToUpper(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkItemKind: Positionsart (Katalog) und Alternative (Position desselben Belegs).
func (m *Module) checkItemKind(ctx context.Context, k docKind, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if kind := trimUpper(rec["item_kind"]); kind != "" {
		if m.text(ctx, `SELECT code FROM procurement__item_kind WHERE company_code = ? AND code = ? AND is_active = ?`, cc, kind, true) == "" {
			return crud.Invalid("Positionsart %s gibt es im Buchungskreis %s nicht", kind, cc)
		}
		rec["item_kind"] = kind
	} else {
		rec["item_kind"] = nil
	}
	rec["supplier_pos"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["supplier_pos"])))
	if !k.kinds {
		return nil
	}
	alt := toInt(rec["alternative_to"])
	if alt == 0 {
		rec["alternative_to"] = nil
		return nil
	}
	if old != nil && alt == toInt(old["line_no"]) {
		return crud.Invalid("Position kann keine Alternative zu sich selbst sein")
	}
	if m.text(ctx, `SELECT line_no FROM `+k.items+` WHERE company_code = ? AND `+k.key+` = ? AND line_no = ? AND is_active = ? AND dependent_type IS NULL`,
		cc, rec[k.key], alt, true) == "" {
		return crud.Invalid("Alternative zu Position %d: die Position gibt es nicht", alt)
	}
	rec["alternative_to"] = alt
	return nil
}

// --- Auftrag ------------------------------------------------------------------------

func (m *Module) order() *crud.Entity {
	return &crud.Entity{
		Object: orderObject, Title: "Aufträge", Icon: "icon-file", Table: "procurement__order", Section: "Beschaffung",
		Events: true, CompanyCodeField: "company_code",
		Keys: []string{"company_code", "order_id"}, Order: "company_code, order_date DESC, order_id DESC", TitleField: "description",
		Filters: []string{"company_code", "supplier_id", "status", "object_id", "order_type"}, Search: []string{"order_id", "description", "object_text"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "order_id", Label: "Auftrag (Nummer)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "order_type", Label: "Auftragsart (leer = die erste)", Short: "Art", Type: tText, Listable: true, Lookup: catalogLookup(orderTypeObject)},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "description", Label: "Leistung / Bauvorhaben", Short: "Leistung", Type: tText, Required: true, Listable: true, Wide: true},
			{Key: "order_date", Label: "Auftrag vom (Datum des Schreibens)", Short: "Datum", Type: tDate, Listable: true},
			{Key: "object_type", Label: "Objektart (Standard Mietobjekt – die einzelne Wohnung)", Type: tSel, Options: objectTypeOptions, Trigger: true, Group: "Objekt"},
			{Key: "object_id", Label: "Objekt (Immobilienverwaltung)", Short: "Objekt", Type: tText, Listable: true, Group: "Objekt"},
			{Key: "object_text", Label: "Objektangabe im Schreiben (leer = Anschrift und Bezeichnung des Objekts)", Type: tText, Wide: true, Group: "Objekt"},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Group: "Objekt", Lookup: catalogLookup("CostCategory")},
			{Key: "file_account", Label: "Ablage im DMS (Auswahl laut Dokumentart; leer = Ablagefindung)", Short: "Ablage", Type: tSel, Group: "Objekt"},
			{Key: "planned_from", Label: "Ausführung geplant von", Short: "Ausführung von", Type: tDate, Group: "Ausführung"},
			{Key: "planned_to", Label: "Ausführung geplant bis (Zahlung erwartet)", Short: "Ausführung bis", Type: tDate, Group: "Ausführung"},
			{Key: "amount", Label: "Auftragssumme (Summe der Positionen)", Short: "Summe", Type: tText, Listable: true, ReadOnly: true},
			{Key: "net_amounts", Label: "Positionen netto (Umsatzsteuer als nachgelagerte Position)", Type: tBool, Group: "Positionen"},
			{Key: "dependent_types", Label: "Nachgelagerte Konditionen (leer = Standard, „-“ = keine)", Type: tSel, Multiple: true, Group: "Positionen"},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "salutation", Label: "Anrede (leer = Briefanrede des Lieferanten, z. B. Sehr geehrter Herr Wagner)", Type: tText, Wide: true, Group: "Schreiben"},
			{Key: "subject", Label: "Betreff (leer = der Auftragsart)", Type: tText, Wide: true, Group: "Schreiben"},
			{Key: "our_reference", Label: "Unser Zeichen (leer = Auftragsnummer)", Type: tText, Group: "Schreiben"},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: orderStatusOptions},
			{Key: "issued_at", Label: "Erteilt am", Type: tText, ReadOnly: true, Group: "Erteilung"},
			{Key: "issued_by", Label: "Erteilt von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser, Group: "Erteilung"},
			{Key: "document_name", Label: "Auftragsschreiben im DMS", Short: "Schreiben", Type: tText, ReadOnly: true, Group: "Erteilung"},
			{Key: "document_id", Label: "Dokument (DMS)", Type: tText, ReadOnly: true, Group: "Erteilung"},
			{Key: "note", Label: "Bemerkung (intern)", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			// Eingaben der Aktionen
			{Key: "ord_blocks", Label: "Textbausteine", Type: tSel, Multiple: true, ActionOnly: true, Required: true, Wide: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "angebote", Title: "Angebote", Relation: &metamodel.Relation{Object: orderQuoteObject, ForeignKey: "order_id",
				Match: map[string]string{"company_code": "company_code", "order_id": "order_id"}, Columns: []string{"quote_id"}}},
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: orderItemObject, ForeignKey: "order_id",
				Match:   map[string]string{"company_code": "company_code", "order_id": "order_id"},
				Columns: []string{"line_no", "supplier_pos", "item_kind", "item_text", "quantity", "unit", "unit_price", "amount", "tax_code", "cost_category", "dependent_type"}}},
			{Key: "texte", Title: "Texte", Relation: &metamodel.Relation{Object: orderTextObject, ForeignKey: "order_id",
				Match: map[string]string{"company_code": "company_code", "order_id": "order_id"}, Columns: []string{"section", "seq", "text"}}},
			{Key: "kontakte", Title: "Kontakte", Relation: &metamodel.Relation{Object: orderContactObject, ForeignKey: "order_id",
				Match: map[string]string{"company_code": "company_code", "order_id": "order_id"}, Columns: []string{"seq", "role", "partner_id", "person", "categories"}}},
			{Key: "rechnungen", Title: "Rechnungen", Relation: &metamodel.Relation{Object: invoiceObject, ForeignKey: "order_id",
				Match:   map[string]string{"company_code": "company_code", "order_id": "order_id"},
				Columns: []string{"invoice_id", "supplier_reference", "invoice_date", "total", "status"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			defaults(rec, map[string]any{"net_amounts": true, "object_type": "RentObject", "order_date": crud.KeyDate(ctx)})
			if err := m.checkOrder(ctx, rec, nil); err != nil {
				return err
			}
			year := 0
			if d := crud.Str(rec["order_date"]); len(d) >= 4 {
				year = int(toInt(d[:4]))
			}
			nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: rangeOrder, CompanyCode: crud.Str(rec["company_code"]),
				Year: year, Reference: crud.Str(rec["description"])})
			if err != nil {
				return unavailable("Nummernkreis", err)
			}
			rec["order_id"], rec["status"] = nr.Number, orderDraft
			return nil
		},
		Validate: m.checkOrder,
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			if m.hasItems(ctx, orderDoc, crud.Str(rec["company_code"]), crud.Str(rec["order_id"])) {
				return m.recalc(ctx, orderDoc, crud.Str(rec["company_code"]), crud.Str(rec["order_id"]), 0)
			}
			return nil
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmount(ctx, rec, "amount")
		},
		FormState: m.orderFormState,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "preview", Label: "Vorschau (PDF)", Record: true}, Handle: m.orderPreviewAction},
			{ActionConfig: metamodel.ActionConfig{Name: "issue", Label: "Auftrag erteilen …", Record: true,
				Confirm: "Auftragsschreiben erzeugen, im DMS ablegen und den Auftrag als erteilt kennzeichnen?"}, Handle: m.orderIssueAction},
			{ActionConfig: metamodel.ActionConfig{Name: "pdf", Label: "Auftragsschreiben (PDF)", Record: true}, Handle: m.orderPDFAction},
			{ActionConfig: metamodel.ActionConfig{Name: "mail", Label: "E-Mail-Entwurf", Record: true}, Handle: m.orderMailAction},
			{ActionConfig: metamodel.ActionConfig{Name: "addBlocks", Label: "Textbausteine einfügen …", Record: true, FormState: true,
				Fields: []string{"ord_blocks"}}, Handle: m.orderAddBlocksAction},
			{ActionConfig: metamodel.ActionConfig{Name: "draft", Label: "Zurück zum Entwurf", Record: true,
				Confirm: "Auftrag wieder bearbeiten? Das abgelegte Schreiben bleibt im DMS; „Auftrag erteilen …“ legt ein neues ab."}, Handle: m.orderStatusAction(orderIssued, orderDraft)},
			{ActionConfig: metamodel.ActionConfig{Name: "cancel", Label: "Stornieren", Record: true,
				Confirm: "Auftrag stornieren?"}, Handle: m.orderStatusAction("", orderCancelled)},
			{ActionConfig: metamodel.ActionConfig{Name: "plan", Label: "Ausführungszeitraum ändern …", Record: true,
				Fields: []string{"planned_from", "planned_to"}}, Handle: m.orderPlanAction},
		},
	}
}

func (m *Module) checkOrder(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != orderDraft {
		return crud.Invalid("Auftrag %s ist %s – nur im Entwurf änderbar („Zurück zum Entwurf“)", crud.Str(old["order_id"]),
			optionLabel(orderStatusOptions, crud.Str(old["status"])))
	}
	if err := m.checkFileAccount(ctx, rec, orderObject); err != nil {
		return err
	}
	cc := crud.Str(rec["company_code"])
	if err := m.requirePartner(ctx, strings.TrimSpace(crud.Str(rec["supplier_id"]))); err != nil {
		return err
	}
	if _, err := date(rec, "order_date", "Auftrag vom"); err != nil {
		return err
	}
	if err := checkPlanned(rec); err != nil {
		return err
	}
	typ := trimUpper(rec["order_type"])
	if typ == "" {
		typ = m.text(ctx, `SELECT code FROM procurement__order_type WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`, cc, true)
	}
	if typ == "" || m.text(ctx, `SELECT code FROM procurement__order_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, typ, true) == "" {
		return crud.Invalid("Auftragsart %q gibt es im Buchungskreis %s nicht (Einstellungen → Auftragsarten; „Buchungskreis einrichten …“ legt AU an)", typ, cc)
	}
	rec["order_type"] = typ
	if crud.Str(rec["currency"]) == "" {
		rec["currency"] = "EUR"
	}
	rec["currency"] = trimUpper(rec["currency"])
	if err := m.checkDependentTypes(ctx, rec, old, ""); err != nil {
		return err
	}
	if cat := trimUpper(rec["cost_category"]); cat != "" {
		if _, _, err := m.costCategoryOf(ctx, cc, cat); err != nil {
			return err
		}
		rec["cost_category"] = cat
	} else {
		rec["cost_category"] = nil
	}
	for _, k := range []string{"object_text", "salutation", "subject", "our_reference"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	if err := checkPlaceholders(crud.Str(rec["subject"]), false); err != nil {
		return err
	}
	delete(rec, "amount") // Summe der Positionen
	rec["changed_at"] = now()
	return m.checkObject(ctx, rec)
}

func (m *Module) orderFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	if req.Mode == "action" && req.Action == "addBlocks" {
		return m.addBlocksFormState(ctx, req)
	}
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	cc := req.Values["company_code"]
	if cc == "" && req.ID != "" {
		if key, err := m.set.Entity(orderObject).ParseID(req.ID); err == nil {
			cc = crud.Str(key["company_code"])
		}
	}
	if cc != "" {
		st.Fields["dependent_types"] = metamodel.FieldState{Options: m.dependentOptions(ctx, cc)}
		m.fileAccountState(ctx, &st, cc, orderObject, req)
	}
	if req.Mode == "create" {
		if req.Values["net_amounts"] == "" {
			v := "true"
			st.Fields["net_amounts"] = metamodel.FieldState{Value: &v}
		}
		if req.Values["object_type"] == "" {
			v := "RentObject"
			st.Fields["object_type"] = metamodel.FieldState{Value: &v}
		}
		if req.Values["order_date"] == "" {
			v := crud.KeyDate(ctx)
			st.Fields["order_date"] = metamodel.FieldState{Value: &v}
		}
	}
	return st, nil
}

// orderOf: Kopf eines Auftrags (für Aktionen).
type orderRow struct {
	CC, ID, Type, Supplier, Status, Currency, DocumentID, DocumentName string
}

func (m *Module) loadOrder(ctx context.Context, cc, id string) (*orderRow, error) {
	res, err := m.db.Query(ctx, `SELECT order_type, supplier_id, status, currency, document_id, document_name FROM procurement__order
		WHERE company_code = ? AND order_id = ?`, cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Auftrag %s gibt es im Buchungskreis %s nicht", id, cc)
	}
	r := res.Rows[0]
	return &orderRow{CC: cc, ID: id, Type: crud.Str(r[0]), Supplier: crud.Str(r[1]), Status: crud.Str(r[2]), Currency: crud.Str(r[3]),
		DocumentID: crud.Str(r[4]), DocumentName: crud.Str(r[5])}, nil
}

// requireDraft: Teile eines Auftrags sind nur im Entwurf änderbar.
func (m *Module) requireDraft(ctx context.Context, cc, id string) (*orderRow, error) {
	o, err := m.loadOrder(ctx, cc, id)
	if err != nil {
		return nil, err
	}
	if o.Status != orderDraft {
		return nil, crud.Invalid("Auftrag %s ist %s – nur im Entwurf änderbar", id, optionLabel(orderStatusOptions, o.Status))
	}
	return o, nil
}

// orderQuote: Angebote eines Auftrags (angelegt über „Auftrag erteilen …“ am Angebot).
func (m *Module) orderQuote() *crud.Entity {
	return &crud.Entity{
		Object: orderQuoteObject, Title: "Angebote eines Auftrags", Icon: "icon-link", Table: "procurement__order_quote", Section: "Beschaffung",
		Keys: []string{"company_code", "order_id", "quote_id"}, Order: "company_code, order_id, quote_id", ReadOnly: true,
		Filters: []string{"company_code", "order_id", "quote_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "order_id", Label: "Auftrag", Type: tText, Listable: true, Lookup: lookupOrder},
			{Key: "quote_id", Label: "Angebot", Type: tText, Listable: true, Lookup: lookupQuote},
		},
		Access: &crud.Access{Object: orderObject, Records: true, CompanyCode: "company_code"},
	}
}

// orderItem: Position eines Auftrags – wie die des Angebots, mit Herkunft.
func (m *Module) orderItem() *crud.Entity {
	recalc := func(ctx context.Context, rec crud.Record) error {
		return m.recalc(ctx, orderDoc, crud.Str(rec["company_code"]), crud.Str(rec["order_id"]), 0)
	}
	return &crud.Entity{
		Object: orderItemObject, Title: "Auftragspositionen", Icon: "icon-list", Table: "procurement__order_item", Section: "Beschaffung",
		Events: true, CompanyCodeField: "company_code",
		Keys: []string{"company_code", "order_id", "line_no"}, Order: "company_code, order_id, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "order_id", "quote_id", "cost_category"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "order_id", Label: "Auftrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupOrder},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "supplier_pos", Label: "Position laut Angebot (z. B. 1.060)", Short: "Pos.", Type: tText, Listable: true},
			{Key: "item_kind", Label: "Positionsart (leer = normal)", Short: "Art", Type: tText, Listable: true, Lookup: catalogLookup(itemKindObject)},
			{Key: "item_text", Label: "Text", Type: tText, Listable: true, Wide: true},
			{Key: "quantity", Label: "Menge (leer = 1)", Type: tText, Listable: true},
			{Key: "unit", Label: "Mengeneinheit (leer = Stück)", Short: "Einheit", Type: tText, Listable: true, Lookup: lookupUnit},
			{Key: "unit_price", Label: "Einzelpreis (netto)", Type: tText, Listable: true},
			{Key: "amount", Label: "Gesamtpreis (= Menge × Einzelpreis)", Type: tText, Listable: true},
			{Key: "tax_code", Label: "Steuerkennzeichen", Short: "StKz", Type: tText, Listable: true, Lookup: lookupTaxCode},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Listable: true, Lookup: catalogLookup("CostCategory")},
			{Key: "account_number", Label: "Sachkonto (leer = Kostenart)", Type: tText, Lookup: lookupAccount},
			{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
			{Key: "scheme", Label: "Kalkulationsschema (abhängige Zeilen)", Short: "Schema", Type: tText, Lookup: lookupScheme},
			{Key: "quote_id", Label: "Aus Angebot", Type: tText, ReadOnly: true, Lookup: lookupQuote, Group: "Herkunft"},
			{Key: "quote_line", Label: "Position im Angebot", Type: tNum, ReadOnly: true, Group: "Herkunft"},
			{Key: "parent_line", Label: "zu Position (abhängige Zeile)", Type: tNum, ReadOnly: true, Group: "Herkunft"},
			{Key: "scheme_step", Label: "Schritt des Schemas", Type: tNum, ReadOnly: true, Group: "Herkunft"},
			{Key: "price_override", Label: "Satz überschrieben", Type: tBool, ReadOnly: true, Group: "Herkunft"},
			{Key: "dependent_type", Label: "Nachgelagerte Konditionsart (gerechnet)", Short: "Folgeart", Type: tText, ReadOnly: true, Listable: true},
			{Key: "dependent_effect", Label: "Wirkung", Type: tSel, ReadOnly: true, Options: dependentEffects},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			rec["line_no"] = m.nextLine(ctx, "procurement__order_item", rec)
			return nil
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["order_id"])
			o, err := m.requireDraft(ctx, cc, id)
			if err != nil {
				return err
			}
			return m.checkPosition(ctx, orderDoc, rec, old, cc, m.currencyDecimals(ctx, o.Currency))
		},
		AfterCreate: recalc,
		AfterUpdate: recalc,
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			if _, err := m.requireDraft(ctx, crud.Str(rec["company_code"]), crud.Str(rec["order_id"])); err != nil {
				return nil, err
			}
			if crud.Str(rec["parent_line"]) != "" {
				return nil, crud.Invalid("Position %d gehört zum Kalkulationsschema der Position %d – das Schema dort entfernen", toInt(rec["line_no"]), toInt(rec["parent_line"]))
			}
			return nil, m.recalc(ctx, orderDoc, crud.Str(rec["company_code"]), crud.Str(rec["order_id"]), toInt(rec["line_no"]))
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["currency"] = m.text(ctx, `SELECT currency FROM procurement__order WHERE company_code = ? AND order_id = ?`, rec["company_code"], rec["order_id"])
			err := m.decorateAmount(ctx, rec, "amount")
			delete(rec, "currency")
			decoratePosition(rec)
			return err
		},
	}
}

// nextLine: nächste Positionsnummer eines Auftrags.
func (m *Module) nextLine(ctx context.Context, table string, rec crud.Record) int64 {
	return toInt(m.text(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM `+table+` WHERE company_code = ? AND order_id = ?`, rec["company_code"], rec["order_id"]))
}

// orderText: Text eines Auftrags (Kopie eines Textbausteins oder eigener Text).
func (m *Module) orderText() *crud.Entity {
	return &crud.Entity{
		Object: orderTextObject, Title: "Texte eines Auftrags", Icon: "icon-file-text", Table: "procurement__order_text", Section: "Beschaffung",
		Keys: []string{"company_code", "order_id", "line_no"}, Order: "company_code, order_id, seq, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "order_id", "section"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "order_id", Label: "Auftrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupOrder},
			{Key: "line_no", Label: "Nr.", Type: tNum, ReadOnly: true},
			{Key: "section", Label: "Abschnitt", Type: tSel, Required: true, Listable: true, Options: sectionOptions},
			{Key: "seq", Label: "Reihenfolge (leer = am Ende)", Short: "Reihenfolge", Type: tNum, Listable: true},
			{Key: "text", Label: "Text (Markdown; Platzhalter wie in den Textbausteinen)", Short: "Text", Type: tArea, Required: true, Listable: true, Wide: true},
			{Key: "block", Label: "Aus Textbaustein", Type: tText, ReadOnly: true, Lookup: catalogLookup(textBlockObject)},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Object: orderObject, Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			rec["line_no"] = m.nextLine(ctx, "procurement__order_text", rec)
			if crud.Str(rec["seq"]) == "" || toInt(rec["seq"]) == 0 {
				rec["seq"] = toInt(rec["line_no"]) * 10
			}
			return nil
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			if _, err := m.requireDraft(ctx, crud.Str(rec["company_code"]), crud.Str(rec["order_id"])); err != nil {
				return err
			}
			if crud.Str(rec["seq"]) == "" {
				rec["seq"] = toInt(rec["line_no"]) * 10
			}
			return checkPlaceholders(crud.Str(rec["text"]), false)
		},
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			_, err := m.requireDraft(ctx, crud.Str(rec["company_code"]), crud.Str(rec["order_id"]))
			return nil, err
		},
	}
}

// orderContact: Kontakt eines Auftrags – Geschäftspartner in einer Rolle; im
// Schreiben eine Vereinbarung mit dem Satz der Rolle und dem Adressblock.
func (m *Module) orderContact() *crud.Entity {
	return &crud.Entity{
		Object: orderContactObject, Title: "Kontakte eines Auftrags", Icon: "icon-users", Table: "procurement__order_contact", Section: "Beschaffung",
		Keys: []string{"company_code", "order_id", "line_no"}, Order: "company_code, order_id, seq, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "order_id", "partner_id", "role"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "order_id", Label: "Auftrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupOrder},
			{Key: "line_no", Label: "Nr.", Type: tNum, ReadOnly: true},
			{Key: "role", Label: "Rolle", Type: tText, Required: true, Listable: true, Lookup: catalogLookup(contactRoleObject)},
			{Key: "partner_id", Label: "Geschäftspartner", Short: "Partner", Type: tText, Required: true, Listable: true, Lookup: lookupPartner},
			{Key: "person", Label: "Ansprechpartner (z. B. Frau Schwerdtlein, i.A. Siebel)", Short: "Ansprechpartner", Type: tText, Listable: true},
			{Key: "addition", Label: "Zusatz unter dem Namen (z. B. Filiale Musterstadt)", Type: tText},
			{Key: "categories", Label: "Kommunikationskategorien (Komma, z. B. PHONE,EMAIL; leer = die der Rolle)", Short: "Kategorien", Type: tText, Listable: true},
			{Key: "show_address", Label: "Anschrift aufführen", Type: tBool},
			{Key: "seq", Label: "Reihenfolge unter den Vereinbarungen (leer = am Ende)", Short: "Reihenfolge", Type: tNum, Listable: true},
			{Key: "text", Label: "Eigener Satz (leer = der Rolle)", Type: tArea, Wide: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Object: orderObject, Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			rec["line_no"] = m.nextLine(ctx, "procurement__order_contact", rec)
			defaults(rec, map[string]any{"show_address": true})
			if crud.Str(rec["seq"]) == "" || toInt(rec["seq"]) == 0 {
				rec["seq"] = 500 + toInt(rec["line_no"])*10
			}
			return nil
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if _, err := m.requireDraft(ctx, cc, crud.Str(rec["order_id"])); err != nil {
				return err
			}
			role := trimUpper(rec["role"])
			if m.text(ctx, `SELECT code FROM procurement__contact_role WHERE company_code = ? AND code = ? AND is_active = ?`, cc, role, true) == "" {
				return crud.Invalid("Kontaktrolle %s gibt es im Buchungskreis %s nicht", role, cc)
			}
			rec["role"] = role
			if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": crud.Str(rec["partner_id"])}); err != nil {
				return crud.Invalid("Geschäftspartner %s: %v", crud.Str(rec["partner_id"]), err)
			}
			rec["categories"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["categories"])), ","))
			for _, k := range []string{"person", "addition", "text"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			return checkPlaceholders(crud.Str(rec["text"]), true)
		},
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			_, err := m.requireDraft(ctx, crud.Str(rec["company_code"]), crud.Str(rec["order_id"]))
			return nil, err
		},
	}
}

// optionLabel: Text einer Auswahl (sonst der Wert).
func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}
