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
	quoteObject = "PurchaseQuote"

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
		Keys: []string{"company_code", "quote_id"}, Order: "company_code, quote_date DESC, quote_id DESC", TitleField: "description",
		Filters: []string{"company_code", "supplier_id", "status", "object_id", "cost_category"}, Search: []string{"quote_id", "description", "supplier_reference"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "quote_id", Label: "Angebot (Nummer)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "supplier_reference", Label: "Angebotsnummer des Lieferanten", Type: tText},
			{Key: "description", Label: "Leistung", Type: tText, Required: true, Listable: true},
			{Key: "quote_date", Label: "Angebot vom", Type: tDate, Required: true, Listable: true},
			{Key: "valid_until", Label: "Gültig bis", Type: tDate},
			{Key: "amount", Label: "Betrag (brutto)", Type: tText, Required: true, Listable: true},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "object_type", Label: "Objektart", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt (Immobilienverwaltung)", Type: tText, Listable: true, Group: "Zuordnung"},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Group: "Zuordnung", Lookup: catalogLookup("CostCategory")},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: quoteStatusOptions},
			{Key: "decided_at", Label: "Entschieden am", Type: tText, ReadOnly: true},
			{Key: "decided_by", Label: "Entschieden von", Type: tText, ReadOnly: true},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "rechnungen", Title: "Rechnungen", Relation: &metamodel.Relation{Object: invoiceObject, ForeignKey: "quote_id",
				Match:   map[string]string{"company_code": "company_code", "quote_id": "quote_id"},
				Columns: []string{"invoice_id", "supplier_reference", "invoice_date", "total", "status"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		// Nummer vor der Transaktion ziehen (numrange vergibt in einer eigenen).
		Prepare: func(ctx context.Context, rec crud.Record) error {
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
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmount(ctx, rec, "amount")
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "accept", Label: "Annehmen", Record: true,
				Confirm: "Angebot annehmen (beauftragen)?"}, Handle: m.decideQuote(quoteAccepted)},
			{ActionConfig: metamodel.ActionConfig{Name: "reject", Label: "Ablehnen", Record: true,
				Confirm: "Angebot ablehnen?"}, Handle: m.decideQuote(quoteRejected)},
		},
	}
}

func (m *Module) checkQuote(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != quoteOpen {
		return crud.Invalid("Angebot %s ist entschieden und nicht mehr änderbar", crud.Str(old["quote_id"]))
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
	if crud.Str(rec["currency"]) == "" {
		rec["currency"] = "EUR"
	}
	rec["currency"] = trimUpper(rec["currency"])
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

// decideQuote: Annehmen bzw. Ablehnen eines offenen Angebots.
func (m *Module) decideQuote(status string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		key, err := m.set.Entity(quoteObject).ParseID(in.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		cc, id := crud.Str(key["company_code"]), crud.Str(key["quote_id"])
		if err := requireWrite(ctx, quoteObject, "update", cc); err != nil {
			return sdk.Response{}, err
		}
		res, err := m.db.Exec(ctx, `UPDATE procurement__quote SET status = ?, decided_at = ?, decided_by = ?, changed_at = ?
			WHERE company_code = ? AND quote_id = ? AND status = ?`, status, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), now(), cc, id, quoteOpen)
		if err != nil {
			return sdk.Response{}, err
		}
		if res.RowsAffected == 0 {
			return sdk.Response{}, crud.Invalid("Angebot %s ist nicht offen", id)
		}
		label := map[string]string{quoteAccepted: "angenommen", quoteRejected: "abgelehnt"}[status]
		return sdk.Response{Payload: map[string]any{"id": in.ID, "status": status, "message": "Angebot " + id + " " + label}}, nil
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
