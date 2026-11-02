package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Barauslagen (seit 0.36.0): privat bar bezahlte Belege – Fahrkarte, Porto,
// Bürobedarf. Es gibt keine Geschäftskasse; die Ausgabe ist eine Einlage
// (z. B. Aufwand an Privateinlage Barauslagen) bzw. im Haushalt ein Abgang aus
// dem Bargeld. Was gebucht wird, steht im Katalog Barauslagearten
// (CashExpenseType, je Buchungskreis): Aufwandskonto, Gegenkonto und Belegart –
// eine eigene Belegart mit eigenem Intervall (Belegnummernvergabe) gibt den
// Barbelegen eine eigene laufende Nummer. Der Begriff (phrase) der Art dient dem
// DMS als Erkennungsregel (Fahrkarte, LVB, Deutsche Post …).
//
// Erfassen: Art, Belegdatum, Betrag, Gebäude (Kontierung Gebäude, Freie Dimension 1),
// Mietobjekt bzw. Objekt (Kostenträger), Text;
// „Buchen“ legt eine Vorerfassung mit zwei Positionen an und bucht sie – Aufwand
// mit Kontierung im Soll, Gegenkonto im Haben. Scheitert die Buchung (z. B.
// Periode geschlossen), wird die Vorerfassung verworfen und die Barauslage bleibt
// offen. Im DMS (Workflow ERP, Dokumentkategorie Beleg) erfasst die KI die Maske
// aus dem Scan; das Dokument hängt dann an Barauslage und Beleg.

const (
	cashTypeObject    = "CashExpenseType"
	cashExpenseObject = "CashExpense"
	cashOpen          = "OPEN"
	cashPosted        = "POSTED"
)

var cashStatus = []metamodel.Option{{Value: cashOpen, Label: "Offen"}, {Value: cashPosted, Label: "Gebucht"}}

// accountLookup: Konto des Buchungskreises (field = Feld mit dem Buchungskreis).
func accountLookup(field string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
		Filters: map[string]string{"company_code_id": field, "is_blocked": "=false"}}
}

// cashExpenseType: Katalog Barauslagearten.
func (m *Module) cashExpenseType() *crud.Entity {
	return &crud.Entity{
		Object: cashTypeObject, Customizing: true, Title: "Barauslagearten", Icon: "icon-tag", Table: "ledger__cash_expense_type", Section: "Einstellungen",
		Keys: []string{"company_code_id", "code"}, Order: "company_code_id, code", StatusField: "is_active", TitleField: "name",
		Search: []string{"code", "name", "phrase"}, Filters: []string{"company_code_id", "account_number"},
		Fields: []crud.Field{
			{Key: "company_code_id", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung (Positionstext, z. B. Fahrkarte LVB)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "phrase", Label: "Begriff auf dem Beleg (Erkennen im DMS, z. B. LVB)", Short: "Begriff", Type: tText, Listable: true},
			{Key: "account_number", Label: "Aufwandskonto", Short: "Konto", Type: tText, Required: true, Listable: true, Lookup: accountLookup("company_code_id")},
			{Key: "offset_account", Label: "Gegenkonto (z. B. Privateinlage Barauslagen, Bargeld)", Short: "Gegenkonto", Type: tText, Required: true, Listable: true,
				Lookup: accountLookup("company_code_id")},
			{Key: "document_type", Label: "Belegart (leer = SA; eigene Belegart = eigene laufende Nummer)", Short: "Belegart", Type: tText, Listable: true, Ref: refDocType},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: readByCompany(entryObject),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			if action == "get" || action == "list" {
				return nil
			}
			return requireWrite(ctx, cashTypeObject, action, crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: Großbuchstaben, Ziffern, _ - (höchstens 20)", crud.Str(rec["code"]))
				}
			}
			cc := crud.Str(rec["company_code_id"])
			if _, err := m.config(ctx, cc); err != nil {
				return err
			}
			for _, f := range [][2]string{{"account_number", "Aufwandskonto"}, {"offset_account", "Gegenkonto"}} {
				acc := strings.TrimSpace(crud.Str(rec[f[0]]))
				rec[f[0]] = acc
				if m.text(ctx, `SELECT account_number FROM ledger__account_company WHERE company_code_id = ? AND account_number = ?`, cc, acc) == "" {
					return crud.Invalid("%s %s ist im Buchungskreis %s nicht angelegt", f[1], acc, cc)
				}
			}
			if rec["account_number"] == rec["offset_account"] {
				return crud.Invalid("Aufwandskonto und Gegenkonto müssen verschieden sein")
			}
			rec["document_type"] = nilIfEmpty(strings.ToUpper(strings.TrimSpace(crud.Str(rec["document_type"]))))
			rec["phrase"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["phrase"])))
			return nil
		},
	}
}

// cashExpense: Barauslagen – erfassen, buchen.
func (m *Module) cashExpense() *crud.Entity {
	dims := dimFields(false)
	var rentObject, asset crud.Field
	for _, f := range dims {
		switch f.Key {
		case "rent_object_id":
			rentObject = f
		case "asset_id":
			asset = f
		}
	}
	rentObject.Group, asset.Group = "", ""
	rentObject.Label, asset.Label = "Mietobjekt (Kostenträger, z. B. Fahrt zur Besichtigung)", "Objekt (Kostenträger, Plugin asset)"
	rentObject.Listable = true
	return &crud.Entity{
		Object: cashExpenseObject, Title: "Barauslagen", Icon: "icon-money", Table: "ledger__cash_expense", Section: "Belege",
		Keys: []string{"id"}, Surrogate: true, Order: "receipt_date DESC, changed_at DESC", Events: true,
		Search: []string{"item_text", "reference"}, Filters: []string{"id", "company_code_id", "expense_type", "status", "building_id", "rent_object_id", "asset_id"},
		TitleField: "item_text",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "expense_type", Label: "Barauslageart", Short: "Art", Type: tText, Required: true, Listable: true,
				Lookup: &metamodel.Lookup{Object: cashTypeObject, ValueField: "code", LabelFields: []string{"name", "account_number"},
					Filters: map[string]string{"company_code_id": "company_code_id"}}},
			{Key: "receipt_date", Label: "Belegdatum (Datum auf der Quittung)", Short: "Datum", Type: tDate, Required: true, Listable: true},
			{Key: "posting_date", Label: "Buchungsdatum (leer = Belegdatum)", Type: tDate},
			{Key: "amount", Label: "Betrag (brutto, wie bezahlt)", Short: "Betrag", Type: tText, Required: true, Listable: true},
			{Key: "currency", Label: "Währung (leer = Hauswährung)", Short: "Währ.", Type: tText, Listable: true},
			{Key: "item_text", Label: "Text (leer = Bezeichnung der Art)", Short: "Text", Type: tText, Listable: true},
			{Key: "reference", Label: "Referenz (Nummer der Quittung bzw. Fahrkarte)", Short: "Referenz", Type: tText},
			{Key: "building_id", Label: "Gebäude (Kostenträger, z. B. Fahrt zum Objekt; Kontierung Gebäude)", Short: "Gebäude", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}}},
			rentObject, asset,
			{Key: "account_number", Label: "Aufwandskonto", Short: "Konto", Type: tText, ReadOnly: true, Listable: true},
			{Key: "offset_account", Label: "Gegenkonto", Type: tText, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, ReadOnly: true, Listable: true, Options: cashStatus},
			{Key: "posted_document_id", Label: "Beleg", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Lookup: lookupEntry},
			{Key: "posted_fiscal_year", Label: "Beleg (Jahr)", Type: tNum, ReadOnly: true},
			{Key: "posted_document_number", Label: "Beleg (Nummer)", Type: tText, ReadOnly: true},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Lookup: lookupDraft},
			// Für das DMS: Objekt des Dokuments (Objekt-Ebene im Archiv, Dokumentname).
			{Key: "object_type", Label: "Objektart", Type: tText, Virtual: true, ReadOnly: true},
			{Key: "object_id", Label: "Objekt", Type: tText, Virtual: true, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Barauslage", Fields: []string{"company_code_id", "expense_type", "receipt_date", "amount", "currency", "item_text", "reference",
				"building_id", "rent_object_id", "asset_id", "posting_date"}},
			{Key: "buchung", Title: "Buchung", Fields: []string{"status", "account_number", "offset_account", "posted_document_id", "draft_id"}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
			{Key: "protokoll", Title: "Protokoll", Collapsed: true, Fields: []string{"changed_by", "changed_at", "id"}},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen", Record: true,
			Confirm: "Barauslage jetzt buchen? Danach ist sie nicht mehr änderbar."}, Handle: m.beforeAction(m.cashPostAction)}},
		Access: readByCompany(entryObject),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			if action == "get" || action == "list" {
				return nil
			}
			if crud.Str(rec["status"]) == cashPosted {
				return crud.Invalid("Barauslage ist gebucht und nicht mehr änderbar")
			}
			return requireCompanyCode(ctx, entryObject, "post", crud.Str(rec["company_code_id"]))
		},
		Validate: m.checkCashExpense,
		Decorate: func(_ context.Context, rec crud.Record) error {
			if rec["posted_document_number"] != nil {
				rec["posted_document_id"] = docKey{CompanyCode: crud.Str(rec["company_code_id"]), FiscalYear: int(toInt(rec["posted_fiscal_year"])),
					Number: crud.Str(rec["posted_document_number"])}.id()
			}
			switch {
			case crud.Str(rec["rent_object_id"]) != "":
				rec["object_type"], rec["object_id"] = "RentObject", rec["rent_object_id"]
			case crud.Str(rec["building_id"]) != "":
				rec["object_type"], rec["object_id"] = "Building", rec["building_id"]
			case crud.Str(rec["asset_id"]) != "":
				rec["object_type"], rec["object_id"] = "Asset", rec["asset_id"]
			}
			rec["_locked"] = crud.Str(rec["status"]) == cashPosted
			return nil
		},
	}
}

type cashType struct{ Name, Account, Offset, DocType string }

func (m *Module) cashTypeOf(ctx context.Context, cc, code string) (*cashType, error) {
	res, err := m.db.Query(ctx, `SELECT name, account_number, offset_account, document_type, is_active FROM ledger__cash_expense_type
		WHERE company_code_id = ? AND code = ?`, cc, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 || !crud.AsBool(res.Rows[0][4]) {
		return nil, crud.Invalid("Barauslageart %q gibt es im Buchungskreis %s nicht (Einstellungen → Barauslagearten)", code, cc)
	}
	r := res.Rows[0]
	return &cashType{Name: crud.Str(r[0]), Account: crud.Str(r[1]), Offset: crud.Str(r[2]), DocType: firstNonEmpty(crud.Str(r[3]), "SA")}, nil
}

func (m *Module) checkCashExpense(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code_id"])
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return err
	}
	if old == nil {
		rec["status"] = cashOpen
		if err := requireCompanyCode(ctx, entryObject, "post", cc); err != nil {
			return err
		}
	}
	rec["expense_type"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["expense_type"])))
	t, err := m.cashTypeOf(ctx, cc, crud.Str(rec["expense_type"]))
	if err != nil {
		return err
	}
	rec["currency"] = strings.ToUpper(strings.TrimSpace(firstNonEmpty(crud.Str(rec["currency"]), cfg.Currency)))
	d, err := m.cur.decimals(ctx, crud.Str(rec["currency"]))
	if err != nil {
		return err
	}
	n, err := parseAmount(crud.Str(rec["amount"]), d)
	if err != nil {
		return crud.Invalid("%v", err)
	}
	rec["amount"] = formatAmount(n, d)
	rec["account_number"], rec["offset_account"] = t.Account, t.Offset
	for _, k := range []string{"item_text", "reference", "building_id", "rent_object_id", "asset_id", "posting_date"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	rec["changed_at"], rec["changed_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	return nil
}

// cashPostAction: „Buchen“ – Vorerfassung anlegen und buchen (Aufwand an Gegenkonto).
func (m *Module) cashPostAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	id := crud.Str(p["id"])
	res, err := m.db.Query(ctx, `SELECT company_code_id, expense_type, receipt_date, posting_date, amount, currency, item_text, reference,
		rent_object_id, asset_id, status, building_id FROM ledger__cash_expense WHERE id = ?`, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Barauslage %s", sdk.ErrNotFound, id)
	}
	r := res.Rows[0]
	cc := crud.Str(r[0])
	if crud.Str(r[10]) != cashOpen {
		return sdk.Response{}, crud.Invalid("Barauslage ist schon gebucht")
	}
	if err := requireCompanyCode(ctx, entryObject, "post", cc); err != nil {
		return sdk.Response{}, err
	}
	t, err := m.cashTypeOf(ctx, cc, crud.Str(r[1]))
	if err != nil {
		return sdk.Response{}, err
	}
	receipt, _ := crud.ParseDate(r[2])
	posting := receipt
	if r[3] != nil {
		posting, _ = crud.ParseDate(r[3])
	}
	text := strings.TrimSpace(firstNonEmpty(crud.Str(r[6]), t.Name))
	var draftID string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		head := map[string]any{"company_code_id": cc, "document_type": t.DocType, "posting_date": posting, "document_date": receipt,
			"currency": crud.Str(r[5]), "header_text": "Barauslage: " + text, "reference": nilIfEmpty(crud.Str(r[7]))}
		resp, err := m.set.Entity("JournalDraft").Create(ctx, map[string]any{"data": head})
		if err != nil {
			return err
		}
		rec, _ := resp.Payload.(crud.Record)
		draftID = crud.Str(rec["id"])
		expense := map[string]any{"draft_id": draftID, "account_number": t.Account, "shkzg": sideDebit, "amount": crud.Str(r[4]), "item_text": text,
			"rent_object_id": nilIfEmpty(crud.Str(r[8])), "asset_id": nilIfEmpty(crud.Str(r[9])), "dimension_custom_1": nilIfEmpty(crud.Str(r[11]))}
		offset := map[string]any{"draft_id": draftID, "account_number": t.Offset, "shkzg": sideCredit, "amount": crud.Str(r[4]), "item_text": text}
		for i, data := range []map[string]any{expense, offset} {
			if _, err := m.set.Entity("JournalDraftItem").Create(ctx, map[string]any{"data": data}); err != nil {
				return fmt.Errorf("Position %d (%s): %w", i+1, data["account_number"], err)
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	posted, err := m.draftPostAction(ctx, sdk.Request{Payload: map[string]any{"id": draftID}})
	if err != nil {
		// Vorerfassung verwerfen, die Barauslage bleibt offen (z. B. Periode öffnen, erneut buchen).
		if _, derr := m.db.Exec(ctx, `UPDATE ledger__draft_header SET status = ? WHERE id = ? AND status = ?`, draftDiscarded, draftID, draftOpen); derr != nil {
			err = errors.Join(err, derr)
		}
		return sdk.Response{}, err
	}
	out, _ := posted.Payload.(map[string]any)
	if _, err := m.db.Exec(ctx, `UPDATE ledger__cash_expense SET status = ?, draft_id = ?, posted_fiscal_year = ?, posted_document_number = ?,
		changed_at = ?, changed_by = ? WHERE id = ?`, cashPosted, draftID, out["fiscal_year"], out["document_number"],
		time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID), id); err != nil {
		return sdk.Response{}, err
	}
	m.emit(ctx, events.Event{Object: cashExpenseObject, Action: "post", CompanyCode: cc, EntityID: id, Source: Name,
		Data: map[string]any{"posted_document_id": out["id"], "document_number": out["document_number"]}})
	return sdk.Response{Payload: map[string]any{"id": id, "entry_id": out["id"], "draft_id": draftID, "document_number": out["document_number"],
		"message": fmt.Sprintf("Barauslage gebucht: Beleg %v (%s an %s, %s %s)", out["document_number"], t.Account, t.Offset, crud.Str(r[4]), crud.Str(r[5]))}}, nil
}
