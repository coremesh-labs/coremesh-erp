package procurement

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

const (
	quoteObject     = "PurchaseQuote"
	quoteItemObject = "PurchaseQuoteItem"

	quoteOpen     = "OPEN"
	quoteAccepted = "ACCEPTED"
	quoteRejected = "REJECTED"
)

var (
	quoteStatusOptions = []metamodel.Option{{Value: quoteOpen, Label: "offen"}, {Value: quoteAccepted, Label: "angenommen (beauftragt)"},
		{Value: quoteRejected, Label: "abgelehnt"}}
	lookupQuote = &metamodel.Lookup{Object: quoteObject, ValueField: "quote_id", LabelFields: []string{"description"},
		Filters: map[string]string{"company_code": "company_code"}}
)

// quote: Angebot eines Lieferanten – Nummer AN-<Jahr>-<n> je Buchungskreis.
func (m *Module) quote() *crud.Entity {
	return &crud.Entity{
		Object: quoteObject, Title: "Angebote", Icon: "icon-file", Table: "procurement__quote", Section: "Beschaffung",
		Events: true, CompanyCodeField: "company_code", // seit 0.12.2: Suchtext im DMS
		Keys: []string{"company_code", "quote_id"}, Order: "company_code, quote_date DESC, quote_id DESC", TitleField: "description",
		Filters: []string{"company_code", "supplier_id", "status", "object_id", "cost_category"}, Search: []string{"quote_id", "description", "supplier_reference"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "quote_id", Label: "Angebot (Nummer)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "supplier_reference", Label: "Angebotsnummer des Lieferanten", Short: "Angebotsnr.", Type: tText},
			{Key: "description", Label: "Leistung", Type: tText, Required: true, Listable: true},
			{Key: "quote_date", Label: "Angebot vom", Type: tDate, Required: true, Listable: true},
			{Key: "valid_until", Label: "Gültig bis", Type: tDate},
			{Key: "planned_from", Label: "Ausführung geplant von", Short: "Ausführung von", Type: tDate, Listable: true, Group: "Ausführung"},
			{Key: "planned_to", Label: "Ausführung geplant bis (Zahlung erwartet)", Short: "Ausführung bis", Type: tDate, Listable: true, Group: "Ausführung"},
			{Key: "amount", Label: "Betrag (Summe der Positionen; ohne Positionen: Eingabe)", Type: tText, Listable: true},
			{Key: "net_amounts", Label: "Positionen netto (Umsatzsteuer als nachgelagerte Position)", Type: tBool, Group: "Positionen"},
			{Key: "dependent_types", Label: "Nachgelagerte Konditionen (leer = Standard, „-“ = keine)", Type: tSel, Multiple: true, Group: "Positionen"},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "object_type", Label: "Objektart", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt (Immobilienverwaltung)", Type: tText, Listable: true, Group: "Zuordnung"},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Group: "Zuordnung", Lookup: catalogLookup("CostCategory")},
			{Key: "file_account", Label: "Ablage im DMS (Auswahl laut Dokumentart; leer = Ablagefindung)", Short: "Ablage", Type: tSel, Group: "Zuordnung"},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: quoteStatusOptions},
			{Key: "decided_at", Label: "Entschieden am", Type: tText, ReadOnly: true},
			{Key: "decided_by", Label: "Entschieden von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			// Eingaben von „Auftrag erteilen …“
			{Key: "ord_order", Label: "Zu Auftrag (Entwurf desselben Lieferanten; leer = neuer Auftrag)", Type: tSel, ActionOnly: true, Trigger: true},
			{Key: "ord_type", Label: "Auftragsart", Type: tSel, ActionOnly: true},
			{Key: "ord_lines", Label: "Beauftragte Positionen (alternative und Bedarfspositionen dazu wählen)", Type: tSel, Multiple: true, ActionOnly: true, Wide: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: quoteItemObject, ForeignKey: "quote_id",
				Match:   map[string]string{"company_code": "company_code", "quote_id": "quote_id"},
				Columns: []string{"line_no", "supplier_pos", "item_kind", "parent_line", "item_text", "quantity", "unit", "unit_price", "amount", "tax_code", "scheme", "cost_category", "dependent_type"}}},
			{Key: "auftraege", Title: "Aufträge", Relation: &metamodel.Relation{Object: orderQuoteObject, ForeignKey: "quote_id",
				Match:   map[string]string{"company_code": "company_code", "quote_id": "quote_id"},
				Columns: []string{"order_id"}}},
			{Key: "rechnungen", Title: "Rechnungen", Relation: &metamodel.Relation{Object: invoiceObject, ForeignKey: "quote_id",
				Match:   map[string]string{"company_code": "company_code", "quote_id": "quote_id"},
				Columns: []string{"invoice_id", "supplier_reference", "invoice_date", "total", "status"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		// Nummer vor der Transaktion ziehen (numrange vergibt in einer eigenen).
		Prepare: func(ctx context.Context, rec crud.Record) error {
			if rec["net_amounts"] == nil || crud.Str(rec["net_amounts"]) == "" {
				rec["net_amounts"] = true
			}
			if err := m.checkQuote(ctx, rec, nil); err != nil {
				return err
			}
			year := 0
			if d := crud.Str(rec["quote_date"]); len(d) >= 4 {
				year = int(toInt(d[:4]))
			}
			nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: rangeQuote, CompanyCode: crud.Str(rec["company_code"]),
				Year: year, Reference: crud.Str(rec["description"])})
			if err != nil {
				return unavailable("Nummernkreis", err)
			}
			rec["quote_id"], rec["status"] = nr.Number, quoteOpen
			return nil
		},
		Validate: m.checkQuote,
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			if m.hasItems(ctx, quoteDoc, crud.Str(rec["company_code"]), crud.Str(rec["quote_id"])) {
				return m.recalc(ctx, quoteDoc, crud.Str(rec["company_code"]), crud.Str(rec["quote_id"]), 0)
			}
			return nil
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmount(ctx, rec, "amount")
		},
		FormState: func(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
			if req.Mode == "action" && req.Action == "order" {
				return m.orderFromQuoteFormState(ctx, req)
			}
			st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
			cc := req.Values["company_code"]
			if cc == "" && req.ID != "" {
				if key, err := m.set.Entity(quoteObject).ParseID(req.ID); err == nil {
					cc = crud.Str(key["company_code"])
				}
			}
			if cc != "" {
				st.Fields["dependent_types"] = metamodel.FieldState{Options: m.dependentOptions(ctx, cc)}
				m.fileAccountState(ctx, &st, cc, quoteObject, req)
			}
			if req.Mode == "create" && req.Values["net_amounts"] == "" {
				v := "true"
				st.Fields["net_amounts"] = metamodel.FieldState{Value: &v}
			}
			return st, nil
		},
		Actions: []crud.Action{
			// seit 0.14.0: Auftrag mit Auswahl der Positionen (neu oder zu einem Entwurf desselben Lieferanten)
			{ActionConfig: metamodel.ActionConfig{Name: "order", Label: "Auftrag erteilen …", Record: true, FormState: true,
				Fields: []string{"ord_order", "ord_type", "ord_lines", "planned_from", "planned_to"}}, Handle: m.orderFromQuoteAction},
			{ActionConfig: metamodel.ActionConfig{Name: "accept", Label: "Annehmen …", Record: true,
				Fields: []string{"planned_from", "planned_to"}}, Handle: m.decideQuote(quoteAccepted)},
			{ActionConfig: metamodel.ActionConfig{Name: "reject", Label: "Ablehnen", Record: true,
				Confirm: "Angebot ablehnen?"}, Handle: m.decideQuote(quoteRejected)},
			// Auch nach dem Annehmen: Termine verschieben sich (Liquiditätsvorschau).
			{ActionConfig: metamodel.ActionConfig{Name: "plan", Label: "Ausführungszeitraum ändern …", Record: true,
				Fields: []string{"planned_from", "planned_to"}}, Handle: m.planQuote},
		},
	}
}

func (m *Module) checkQuote(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != quoteOpen {
		return crud.Invalid("Angebot %s ist entschieden und nicht mehr änderbar", crud.Str(old["quote_id"]))
	}
	if err := m.checkFileAccount(ctx, rec, quoteObject); err != nil {
		return err
	}
	if err := m.requirePartner(ctx, strings.TrimSpace(crud.Str(rec["supplier_id"]))); err != nil {
		return err
	}
	if _, err := date(rec, "quote_date", "Angebot vom"); err != nil {
		return err
	}
	if err := optDate(rec, "valid_until"); err != nil {
		return err
	}
	if err := checkPlanned(rec); err != nil {
		return err
	}
	if crud.Str(rec["currency"]) == "" {
		rec["currency"] = "EUR"
	}
	rec["currency"] = trimUpper(rec["currency"])
	if err := m.checkDependentTypes(ctx, rec, old, ""); err != nil {
		return err
	}
	if crud.Str(rec["amount"]) == "" {
		rec["amount"] = int64(0) // Summe der Positionen
	}
	if crud.Str(rec["amount"]) != "" && !isMinor(rec["amount"]) {
		minor, err := parseAmount(crud.Str(rec["amount"]), m.currencyDecimals(ctx, crud.Str(rec["currency"])))
		if err != nil {
			return crud.Invalid("%v", err)
		}
		rec["amount"] = minor
	}
	if cat := trimUpper(rec["cost_category"]); cat != "" {
		if _, _, err := m.costCategoryOf(ctx, crud.Str(rec["company_code"]), cat); err != nil {
			return err
		}
		rec["cost_category"] = cat
	} else {
		rec["cost_category"] = nil
	}
	rec["changed_at"] = now()
	return m.checkObject(ctx, rec)
}

// isMinor: Wert ist schon in der kleinsten Einheit (Prepare hat geprüft, Validate läuft erneut).
func isMinor(v any) bool {
	switch v.(type) {
	case int64, int:
		return true
	}
	return false
}

// checkPlanned: geplanter Ausführungszeitraum (optional), von ≤ bis.
func checkPlanned(rec crud.Record) error {
	for _, k := range []string{"planned_from", "planned_to"} {
		if err := optDate(rec, k); err != nil {
			return err
		}
	}
	if from, to := crud.Str(rec["planned_from"]), crud.Str(rec["planned_to"]); from != "" && to != "" && to < from {
		return crud.Invalid("Ausführung geplant bis %s liegt vor dem Beginn %s", to, from)
	}
	return nil
}

// quoteAction: Angebot und Felder des Dialogs einer Aktion; prüft das Schreibrecht.
func (m *Module) quoteAction(ctx context.Context, req sdk.Request) (cc, id string, plan crud.Record, err error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return "", "", nil, err
	}
	key, err := m.set.Entity(quoteObject).ParseID(in.ID)
	if err != nil {
		return "", "", nil, err
	}
	cc, id = crud.Str(key["company_code"]), crud.Str(key["quote_id"])
	if err := requireWrite(ctx, quoteObject, "update", cc); err != nil {
		return "", "", nil, err
	}
	plan = crud.Record{"planned_from": in.Data["planned_from"], "planned_to": in.Data["planned_to"]}
	return cc, id, plan, checkPlanned(plan)
}

// planQuote: geplanten Ausführungszeitraum eines offenen oder angenommenen Angebots ändern.
func (m *Module) planQuote(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc, id, plan, err := m.quoteAction(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Exec(ctx, `UPDATE procurement__quote SET planned_from = ?, planned_to = ?, changed_at = ?
		WHERE company_code = ? AND quote_id = ? AND status <> ?`, plan["planned_from"], plan["planned_to"], now(), cc, id, quoteRejected)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.RowsAffected == 0 {
		return sdk.Response{}, crud.Invalid("Angebot %s fehlt oder ist abgelehnt", id)
	}
	return sdk.Response{Payload: map[string]any{"message": "Angebot " + id + ": Ausführungszeitraum geändert"}}, nil
}

// decideQuote: Annehmen bzw. Ablehnen eines offenen Angebots; beim Annehmen
// optional mit geplantem Ausführungszeitraum (leer = bisheriger).
func (m *Module) decideQuote(status string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		cc, id, plan, err := m.quoteAction(ctx, req)
		if err != nil {
			return sdk.Response{}, err
		}
		res, err := m.db.Exec(ctx, `UPDATE procurement__quote SET status = ?, decided_at = ?, decided_by = ?, changed_at = ?,
			planned_from = COALESCE(?, planned_from), planned_to = COALESCE(?, planned_to)
			WHERE company_code = ? AND quote_id = ? AND status = ?`, status, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), now(),
			plan["planned_from"], plan["planned_to"], cc, id, quoteOpen)
		if err != nil {
			return sdk.Response{}, err
		}
		if res.RowsAffected == 0 {
			return sdk.Response{}, crud.Invalid("Angebot %s ist nicht offen", id)
		}
		label := map[string]string{quoteAccepted: "angenommen", quoteRejected: "abgelehnt"}[status]
		return sdk.Response{Payload: map[string]any{"id": cc + "|" + id, "status": status, "message": "Angebot " + id + " " + label}}, nil
	}
}

// decorateAmount: Beträge (kleinste Einheit) als Dezimaltext in der Währung des Datensatzes.
func (m *Module) decorateAmount(ctx context.Context, rec crud.Record, keys ...string) error {
	cur := crud.Str(rec["currency"])
	if cur == "" {
		cur = "EUR"
	}
	d := m.currencyDecimals(ctx, cur)
	for _, k := range keys {
		if rec[k] != nil && crud.Str(rec[k]) != "" {
			rec[k] = formatAmount(toInt(rec[k]), d)
			labels(rec)[k] = crud.Str(rec[k]) + " " + cur
		}
	}
	return nil
}

// quoteItem: Position eines Angebots (seit 0.8.0) – Menge, Einheit, Einzelpreis,
// Gesamtpreis (netto), Steuer, Kostenart; nachgelagerte Positionen wie an der Rechnung.
func (m *Module) quoteItem() *crud.Entity {
	recalc := func(ctx context.Context, rec crud.Record) error {
		return m.recalc(ctx, quoteDoc, crud.Str(rec["company_code"]), crud.Str(rec["quote_id"]), 0)
	}
	return &crud.Entity{
		Object: quoteItemObject, Title: "Angebotspositionen", Icon: "icon-list", Table: "procurement__quote_item", Section: "Beschaffung",
		Events: true, CompanyCodeField: "company_code", // seit 0.12.2: Suchtext im DMS
		Keys: []string{"company_code", "quote_id", "line_no"}, Order: "company_code, quote_id, line_no", StatusField: "is_active",
		Filters: []string{"company_code", "quote_id", "cost_category"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "quote_id", Label: "Angebot", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupQuote},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "supplier_pos", Label: "Position laut Angebot (z. B. 1.060)", Short: "Pos.", Type: tText, Listable: true},
			{Key: "item_kind", Label: "Positionsart (leer = normal)", Short: "Art", Type: tText, Listable: true, Lookup: catalogLookup(itemKindObject)},
			{Key: "alternative_to", Label: "Alternative zu Position (Nr.)", Short: "Alternative zu", Type: tNum},
			{Key: "alternative_pos", Label: "Alternative zu Position laut Angebot (z. B. 1.050; statt der Nr.)", Type: tText, Virtual: true, VirtualUpdate: true},
			{Key: "item_text", Label: "Text", Type: tText, Listable: true},
			{Key: "quantity", Label: "Menge (leer = 1)", Type: tText, Listable: true},
			{Key: "unit", Label: "Mengeneinheit (leer = Stück)", Short: "Einheit", Type: tText, Listable: true, Lookup: lookupUnit},
			{Key: "unit_price", Label: "Einzelpreis (netto)", Type: tText, Listable: true},
			{Key: "amount", Label: "Gesamtpreis (= Menge × Einzelpreis)", Type: tText, Listable: true},
			{Key: "tax_code", Label: "Steuerkennzeichen", Short: "StKz", Type: tText, Listable: true, Lookup: lookupTaxCode},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Listable: true, Lookup: catalogLookup("CostCategory")},
			{Key: "account_number", Label: "Sachkonto (leer = Kostenart)", Type: tText, Lookup: lookupAccount},
			{Key: "rounding_rule", Label: "Rundungsregel (leer = Buchungskreis)", Type: tText, Lookup: lookupRounding},
			{Key: "scheme", Label: "Kalkulationsschema (abhängige Zeilen, z. B. STROM)", Short: "Schema", Type: tText, Listable: true, Lookup: lookupScheme},
			{Key: "parent_line", Label: "zu Position (abhängige Zeile)", Type: tNum, Listable: true, ReadOnly: true, Group: "Herkunft"},
			{Key: "scheme_step", Label: "Schritt des Schemas", Type: tNum, ReadOnly: true, Group: "Herkunft"},
			{Key: "price_override", Label: "Satz überschrieben", Type: tBool, ReadOnly: true, Group: "Herkunft"},
			{Key: "dependent_type", Label: "Nachgelagerte Konditionsart (gerechnet)", Short: "Folgeart", Type: tText, ReadOnly: true, Listable: true},
			{Key: "dependent_effect", Label: "Wirkung", Type: tSel, ReadOnly: true, Options: dependentEffects},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM procurement__quote_item WHERE company_code = ? AND quote_id = ?`,
				rec["company_code"], rec["quote_id"])
			if err != nil {
				return err
			}
			rec["line_no"] = toInt(res.Rows[0][0])
			return nil
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["quote_id"])
			res, err := m.db.Query(ctx, `SELECT status, currency, net_amounts FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, cc, id)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				return crud.Invalid("Angebot %s gibt es im Buchungskreis %s nicht", id, cc)
			}
			if crud.Str(res.Rows[0][0]) != quoteOpen {
				return crud.Invalid("Angebot %s ist entschieden – Positionen nicht mehr änderbar", id)
			}
			return m.checkPosition(ctx, quoteDoc, rec, old, cc, m.currencyDecimals(ctx, crud.Str(res.Rows[0][1])))
		},
		AfterCreate: recalc,
		AfterUpdate: recalc,
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			if crud.Str(rec["parent_line"]) != "" {
				return nil, crud.Invalid("Position %d gehört zum Kalkulationsschema der Position %d – das Schema dort entfernen", toInt(rec["line_no"]), toInt(rec["parent_line"]))
			}
			return nil, m.recalc(ctx, quoteDoc, crud.Str(rec["company_code"]), crud.Str(rec["quote_id"]), toInt(rec["line_no"]))
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["currency"] = m.text(ctx, `SELECT currency FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, rec["company_code"], rec["quote_id"])
			err := m.decorateAmount(ctx, rec, "amount")
			delete(rec, "currency")
			decoratePosition(rec)
			return err
		},
	}
}

// checkPosition: Position eines Angebots oder Auftrags – nachgelagerte Zeilen werden
// gerechnet, abhängige Zeilen nur im Satz änderbar; Schema, Kostenart, Sachkonto,
// Steuer, Positionsart, Preis.
func (m *Module) checkPosition(ctx context.Context, k docKind, rec, old crud.Record, cc string, dec int) error {
	if old != nil && crud.Str(old["dependent_type"]) != "" {
		return crud.Invalid("Position %d ist nachgelagert und wird gerechnet", toInt(old["line_no"]))
	}
	if old != nil && crud.Str(old["parent_line"]) != "" {
		if p := strings.TrimSpace(crud.Str(rec["unit_price"])); p != "" && p != crud.Str(old["unit_price"]) {
			rec["price_override"] = true
		}
		return nil
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
	if cat := trimUpper(rec["cost_category"]); cat != "" {
		acc, _, err := m.costCategoryOf(ctx, cc, cat)
		if err != nil {
			return err
		}
		rec["cost_category"] = cat
		if strings.TrimSpace(crud.Str(rec["account_number"])) == "" {
			rec["account_number"] = nilIfEmpty(acc)
		}
	} else {
		rec["cost_category"] = nil
	}
	if acc := strings.TrimSpace(crud.Str(rec["account_number"])); acc != "" {
		nr, err := m.glAccount(ctx, cc, acc)
		if err != nil {
			return err
		}
		rec["account_number"] = nr
	}
	if err := m.checkTaxCode(ctx, rec); err != nil {
		return err
	}
	if err := m.checkItemKind(ctx, k, rec, old); err != nil {
		return err
	}
	return m.itemPrice(ctx, rec, old, cc, dec)
}
