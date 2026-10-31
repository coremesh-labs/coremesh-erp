package procurement

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Belastung an (seit 0.20.0): An wen die Kosten einer Rechnungsposition gehen.
//   - Objekt: Haus oder Wohnung – alle Mieter dort, über die Nebenkostenabrechnung
//     (bei Mietwechsel anteilig); ohne Kennung das Objekt der Position;
//   - Vertrag: ein Mietvertrag (Verursacher), über die Nebenkostenabrechnung;
//   - Person: ein Dritter ohne Vertrag – eigene Weiterbelastung als Forderung
//     („Weiterbelasten …“ an der gebuchten Rechnung).
//
// Umlagefähige Positionen ohne Angabe gehen an ihr Objekt. Die Belastungsart kann
// ein eigenes Konto haben (z. B. weiterzubelastende Kosten statt Aufwand). Rechnung
// und Belastung an stehen an den Positionen im Hauptbuch.

const chargeTypeObject = "ChargeType"

const (
	chargeKindObject   = "OBJECT"
	chargeKindContract = "CONTRACT"
	chargeKindPerson   = "PERSON"
)

var chargeKinds = []metamodel.Option{
	{Value: chargeKindObject, Label: "Objekt (alle Mieter dort, Nebenkostenabrechnung)"},
	{Value: chargeKindContract, Label: "Vertrag (ein Mieter, Nebenkostenabrechnung)"},
	{Value: chargeKindPerson, Label: "Person (Dritter, eigene Weiterbelastung)"},
}

// chargeType: Katalog Belastungsarten je Buchungskreis.
func (m *Module) chargeType() *crud.Entity {
	return m.catalog(chargeTypeObject, "Belastungsarten", "procurement__charge_type", catalogFields(
		crud.Field{Key: "kind", Label: "Belastet wird", Type: tSel, Required: true, Listable: true, Options: chargeKinds},
		crud.Field{Key: "object_type", Label: "Objektart (bei Objekt)", Type: tSel, Listable: true, Options: objectTypeOptions},
		crud.Field{Key: "account_number", Label: "Konto statt Aufwandskonto der Position (leer = Konto der Position)", Short: "Konto", Type: tText, Listable: true, Lookup: lookupAccount},
		crud.Field{Key: "document_type", Label: "Belegart der Weiterbelastung (bei Person, z. B. DR)", Type: tText,
			Lookup: &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}},
		crud.Field{Key: "partner_role", Label: "Rolle der Person (Finanzrolle mit Abstimmkonto, z. B. DEBITOR)", Type: tText,
			Lookup: &metamodel.Lookup{Object: "PartnerRoleType", ValueField: "code", LabelFields: []string{"description"}}},
	), func(_ context.Context, rec crud.Record) error {
		kind := trimUpper(rec["kind"])
		rec["kind"] = kind
		for _, k := range []string{"document_type", "partner_role"} {
			rec[k] = trimUpper(rec[k])
		}
		switch kind {
		case chargeKindObject:
			if crud.Str(rec["object_type"]) == "" {
				return crud.Invalid("Belastungsart %s: Objektart ist Pflicht", crud.Str(rec["code"]))
			}
		case chargeKindContract:
			rec["object_type"] = nil
		case chargeKindPerson:
			rec["object_type"] = nil
			if crud.Str(rec["document_type"]) == "" || crud.Str(rec["partner_role"]) == "" {
				return crud.Invalid("Belastungsart %s: Belegart und Rolle der Person sind Pflicht (Weiterbelastung als Forderung)", crud.Str(rec["code"]))
			}
		default:
			return crud.Invalid("Belastet wird: Objekt, Vertrag oder Person")
		}
		return nil
	})
}

// defaultChargeTypes: Vorschlag beim Einrichten des Buchungskreises (änderbar).
var defaultChargeTypes = []struct{ code, name, kind, object, docType, role string }{
	{"HAUS", "Haus (alle Mieter des Gebäudes)", chargeKindObject, "Building", "", ""},
	{"WE", "Wirtschaftseinheit (alle Mieter)", chargeKindObject, "BusinessEntity", "", ""},
	{"WOHNUNG", "Wohnung (Mieter der Wohnung, bei Mietwechsel anteilig)", chargeKindObject, "RentObject", "", ""},
	{"MIETER", "Mietvertrag (Verursacher)", chargeKindContract, "", "", ""},
	{"PERSON", "Person (Dritter, eigene Weiterbelastung)", chargeKindPerson, "", "DR", "DEBITOR"},
}

func (m *Module) setupChargeTypes(ctx context.Context, cc string) error {
	for i, t := range defaultChargeTypes {
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__charge_type (company_code, code, name, kind, object_type, document_type, partner_role, sort_order, is_active)
			SELECT ?, ?, ?, ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM procurement__charge_type WHERE company_code = ? AND code = ?)`,
			cc, t.code, t.name, t.kind, nilIfEmpty(t.object), nilIfEmpty(t.docType), nilIfEmpty(t.role), (i+1)*10, true, cc, t.code); err != nil {
			return err
		}
	}
	return nil
}

// migrateChargeTypes (0.20.0, einmal): Belastungsarten für eingerichtete Buchungskreise.
func (m *Module) migrateChargeTypes(ctx context.Context) error {
	if m.text(ctx, `SELECT name FROM procurement__migration WHERE name = ?`, "charge_types") != "" {
		return nil
	}
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM procurement__invoice_type`)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			if err := m.setupChargeTypes(ctx, crud.Str(r[0])); err != nil {
				return err
			}
		}
		_, err = m.db.Exec(ctx, `INSERT INTO procurement__migration (name, done_at) VALUES (?, ?)`, "charge_types", now())
		return err
	})
}

type chargeTypeRow struct {
	Code, Name, Kind, ObjectType, Account, DocumentType, PartnerRole string
}

// chargeTypeOf: aktive Belastungsart.
func (m *Module) chargeTypeOf(ctx context.Context, cc, code string) (*chargeTypeRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, kind, COALESCE(object_type, ''), COALESCE(account_number, ''), COALESCE(document_type, ''),
		COALESCE(partner_role, '') FROM procurement__charge_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Belastungsart %q gibt es im Buchungskreis %s nicht (Einstellungen → Belastungsarten)", code, cc)
	}
	r := res.Rows[0]
	return &chargeTypeRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Kind: crud.Str(r[2]), ObjectType: crud.Str(r[3]), Account: crud.Str(r[4]),
		DocumentType: crud.Str(r[5]), PartnerRole: crud.Str(r[6])}, nil
}

// objectChargeType: erste aktive Belastungsart „Objekt“ der Objektart ("" = keine).
func (m *Module) objectChargeType(ctx context.Context, cc, objectType string) string {
	return m.text(ctx, `SELECT code FROM procurement__charge_type WHERE company_code = ? AND kind = ? AND object_type = ? AND is_active = ?
		ORDER BY sort_order, code LIMIT 1`, cc, chargeKindObject, objectType, true)
}

// checkCharge: Belastung an einer Rechnungsposition – Art aus dem Katalog; Vertrag und
// Person müssen genannt sein und existieren; Objekt ohne Kennung = Objekt der Position.
func (m *Module) checkCharge(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	code := trimUpper(rec["charge_type"])
	id := strings.TrimSpace(crud.Str(rec["charge_id"]))
	rec["charge_type"], rec["charge_id"] = nilIfEmpty(code), nilIfEmpty(id)
	if code == "" {
		if id != "" {
			return crud.Invalid("Belastung an: zuerst die Belastungsart wählen")
		}
		return nil
	}
	ct, err := m.chargeTypeOf(ctx, cc, code)
	if err != nil {
		return err
	}
	switch ct.Kind {
	case chargeKindContract:
		if id == "" {
			return crud.Invalid("Belastung an %s: Vertrag ist Pflicht", ct.Name)
		}
		if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + id}); errors.Is(err, sdk.ErrNotFound) {
			return crud.Invalid("Belastung an: Vertrag %s gibt es im Buchungskreis %s nicht", id, cc)
		} else if err != nil {
			return unavailable("Vertragsverwaltung", err)
		}
	case chargeKindPerson:
		if id == "" {
			return crud.Invalid("Belastung an %s: Partner (Person) ist Pflicht", ct.Name)
		}
		if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id}); errors.Is(err, sdk.ErrNotFound) {
			return crud.Invalid("Belastung an: Partner %s gibt es nicht", id)
		} else if err != nil {
			return unavailable("Partnermodul", err)
		}
	}
	return nil
}

// itemCharge: Belastung an einer Position beim Buchen – Angabe der Position, sonst bei
// umlagefähigen Positionen ihr Objekt. Objekt ohne Kennung: Objekt der Position, wenn
// die Objektart passt.
func (m *Module) itemCharge(ctx context.Context, cc string, it itemRow, objType, obj string) (*chargeTypeRow, string, error) {
	code, id := it.Charge, it.ChargeID
	if code == "" {
		if !it.Allocable || obj == "" {
			return nil, "", nil
		}
		if code = m.objectChargeType(ctx, cc, objType); code == "" {
			return nil, "", nil
		}
	}
	ct, err := m.chargeTypeOf(ctx, cc, code)
	if err != nil {
		return nil, "", crud.Invalid("Position %d: %v", it.Line, err)
	}
	if ct.Kind == chargeKindObject && id == "" {
		if objType != ct.ObjectType || obj == "" {
			return nil, "", crud.Invalid("Position %d: Belastung an %s – Objekt angeben (die Position nennt kein Objekt dieser Art)", it.Line, ct.Name)
		}
		id = obj
	}
	return ct, id, nil
}

// --- Weiterbelasten an Personen ---------------------------------------------------

const chargeAction = "charge"

type chargeLine struct {
	Line              int64
	Account, Text     string
	ObjectType, Obj   string
	Amount            int64
	ChargeType, Party string
	Type              *chargeTypeRow
}

// chargeOnAction: „Weiterbelasten …“ – Positionen einer gebuchten Rechnung mit
// Belastung an eine Person, die noch nicht weiterbelastet sind: je Person und
// Belastungsart eine Forderung (Belegart der Belastungsart): Soll Debitor (Abstimmkonto
// der Rolle), Haben Konto der Belastungsart bzw. Konto der Position; Rechnung, Objekt
// und Belastung an an beiden Seiten.
func (m *Module) chargeOnAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Date string `json:"charge_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, chargeAction, inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if inv.Status != invoicePosted {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s – weiterbelastet wird eine gebuchte Rechnung", inv.ID, statusText(inv.Status))
	}
	date := strings.TrimSpace(in.Data.Date)
	if date == "" {
		date = inv.PostingDate
	}
	res, err := m.db.Query(ctx, `SELECT line_no, account_number, COALESCE(item_text, ''), COALESCE(object_type, ''), COALESCE(object_id, ''),
		COALESCE(cost_amount, amount), charge_type, charge_id FROM procurement__invoice_item
		WHERE company_code = ? AND invoice_id = ? AND is_active = ? AND COALESCE(charge_type, '') <> '' AND COALESCE(charge_document, '') = ''
		AND (dependent_type IS NULL OR allocated = ?) ORDER BY line_no`, inv.CompanyCode, inv.ID, true, false)
	if err != nil {
		return sdk.Response{}, err
	}
	groups, order := map[string][]chargeLine{}, []string{}
	for _, r := range res.Rows {
		ct, err := m.chargeTypeOf(ctx, inv.CompanyCode, crud.Str(r[6]))
		if err != nil {
			return sdk.Response{}, err
		}
		if ct.Kind != chargeKindPerson {
			continue
		}
		l := chargeLine{Line: toInt(r[0]), Account: crud.Str(r[1]), Text: crud.Str(r[2]), ObjectType: crud.Str(r[3]), Obj: crud.Str(r[4]),
			Amount: toInt(r[5]), ChargeType: ct.Code, Party: crud.Str(r[7]), Type: ct}
		if l.Obj == "" {
			l.ObjectType, l.Obj = inv.ObjectType, inv.ObjectID
		}
		k := l.ChargeType + "|" + l.Party
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], l)
	}
	if len(order) == 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s: keine Position mit Belastung an eine Person, die noch nicht weiterbelastet ist", inv.ID)
	}
	decimals := m.currencyDecimals(ctx, inv.Currency)
	fields := fieldStatusCache{m: m, cc: inv.CompanyCode, groups: map[string]map[string]string{}}
	var docs []string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, k := range order {
			lines := groups[k]
			ct, party := lines[0].Type, lines[0].Party
			recon, err := m.supplierAccount(ctx, inv.CompanyCode, party, ct.PartnerRole)
			if err != nil {
				return err
			}
			header := fmt.Sprintf("Weiterbelastung %s an %s", inv.ID, m.partnerName(ctx, party))
			var total int64
			for _, l := range lines {
				total += l.Amount
			}
			if total == 0 {
				continue
			}
			draft, err := m.services.Call(ctx, "JournalDraft", "create", map[string]any{"data": map[string]any{
				"company_code_id": inv.CompanyCode, "document_type": ct.DocumentType, "posting_date": date, "document_date": date,
				"currency": inv.Currency, "header_text": header, "reference": inv.ID}})
			if err != nil {
				return unavailable("Hauptbuch", err)
			}
			draftID := field(draft.Payload, "id")
			post := func(acc, side string, amount int64, text string, k map[string]string) error {
				if amount < 0 {
					side, amount = map[string]string{"S": "H", "H": "S"}[side], -amount
				}
				data := map[string]any{"draft_id": draftID, "account_number": acc, "shkzg": side, "amount": formatAmount(amount, decimals), "item_text": text}
				allowed, err := fields.allowed(ctx, acc)
				if err != nil {
					return err
				}
				for f, v := range k {
					if v != "" && allowed[f] {
						data[f] = v
					}
				}
				_, err = m.services.Call(ctx, "JournalDraftItem", "create", map[string]any{"data": data})
				return err
			}
			refs := func(l chargeLine) map[string]string {
				k := map[string]string{"invoice_id": inv.ID, "charge_type": l.ChargeType, "charge_id": l.Party}
				if f := m.objectField(ctx, inv.CompanyCode, l.ObjectType); f != "" && l.Obj != "" {
					k[f] = l.Obj
				}
				return k
			}
			for _, l := range lines {
				acc := l.Account
				if ct.Account != "" {
					acc = ct.Account
				}
				text := l.Text
				if text == "" {
					text = header
				}
				if err := post(acc, "H", l.Amount, text, refs(l)); err != nil {
					return err
				}
			}
			k := refs(lines[0])
			if len(lines) > 1 { // Objekt nur, wenn alle Positionen dasselbe nennen
				for _, l := range lines[1:] {
					if l.Obj != lines[0].Obj {
						k = map[string]string{"invoice_id": inv.ID, "charge_type": ct.Code, "charge_id": party}
					}
				}
			}
			k["sd_customer_id"] = party
			if err := post(recon, "S", total, header, k); err != nil {
				return err
			}
			res, err := m.services.Call(ctx, "JournalDraft", "post", map[string]any{"id": draftID})
			if err != nil {
				return err
			}
			docID, docNo := field(res.Payload, "id"), field(res.Payload, "document_number")
			for _, l := range lines {
				if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET charge_document = ? WHERE company_code = ? AND invoice_id = ? AND line_no = ?`,
					docID, inv.CompanyCode, inv.ID, l.Line); err != nil {
					return err
				}
			}
			docs = append(docs, docNo)
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "documents": docs,
		"message": fmt.Sprintf("Rechnung %s weiterbelastet: Beleg %s", inv.ID, strings.Join(docs, ", "))}}, nil
}
