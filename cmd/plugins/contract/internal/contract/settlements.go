package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Vertragsabrechnung: Der Partner rechnet die Vorauszahlungen eines Zeitraums
// ab – Versorger, Grundsteuerbescheid, Jahresabrechnung der WEG.
//
//   - ContractSettlement (Kopf): Zeitraum, Abrechnungsdatum, Nummer; laut
//     Abrechnung Vorauszahlungen und Ergebnis (Nachzahlung > 0, Guthaben < 0);
//     Anteil an der Erhaltungsrücklage (Anfang, Entnahme, Ende) mit
//     Rücklagen- und Entnahmekonto.
//   - ContractSettlementItem: je Kostenart (Modul Betriebskosten) Gesamtkosten
//     (z. B. der WEG), Verteilerschlüssel mit Gesamt- und Anteilswert,
//     Einzelbetrag, Sachkonto, Objekt, Abrechnungskreis.
//   - „Prüfen“ und „Buchen …“ rechnet contract-billing (Haskell): Prüfungen
//     (Einzelbetrag aus Schlüssel, Vorauszahlungen, Ergebnis, Rücklage gegen
//     Vorjahr und Hauptbuch) und der Buchungsplan – Kosten je Position an
//     Vorauszahlungskonten (gebuchte Vorauszahlungen des Zeitraums), Differenz
//     an das Partnerkonto. Vorauszahlungen sind Konditionsarten mit
//     „Vorauszahlung (wird abgerechnet)“; Toleranz an der Vertragsart.
//   - „Positionen aus dem Vorjahr übernehmen“: Kostenarten, Schlüssel,
//     Konten, Objekte der letzten Abrechnung – nur die Beträge fehlen.

const (
	settlementObject     = "ContractSettlement"
	settlementItemObject = "ContractSettlementItem"

	settlementOpen      = "OPEN"
	settlementDraft     = "DRAFT"
	settlementPosted    = "POSTED"
	settlementCancelled = "CANCELLED"
)

var (
	settlementStatusOptions = []metamodel.Option{{Value: settlementOpen, Label: "erfasst"}, {Value: settlementDraft, Label: "vorerfasst (Hauptbuch)"},
		{Value: settlementPosted, Label: "gebucht"}, {Value: settlementCancelled, Label: "verworfen"}}
	lookupCostCategory = &metamodel.Lookup{Object: "CostCategory", ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupAllocationKey = &metamodel.Lookup{Object: "AllocationKey", ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	decimalRe = amountRe
)

func (m *Module) settlement() *crud.Entity {
	match := map[string]string{"company_code": "company_code", "contract_id": "contract_id", "period_from": "period_from"}
	return &crud.Entity{
		Object: settlementObject, Title: "Vertragsabrechnungen", Icon: "icon-calc", Table: "contract__settlement", Section: "Abrechnung",
		Keys: []string{"company_code", "contract_id", "period_from"}, Order: "company_code, contract_id, period_from DESC",
		Filters: []string{"company_code", "contract_id", "status"}, TitleField: "reference",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "period_from", Label: "Zeitraum von", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "period_to", Label: "Zeitraum bis", Type: tDate, Required: true, Listable: true},
			{Key: "settlement_date", Label: "Abrechnung vom (Belegdatum)", Type: tDate, Required: true, Listable: true},
			{Key: "posting_date", Label: "Buchungsdatum (leer = Abrechnungsdatum)", Type: tDate},
			{Key: "reference", Label: "Abrechnungs- bzw. Bescheidnummer", Type: tText, Listable: true},
			{Key: "stated_advances", Label: "Vorauszahlungen laut Abrechnung", Type: tText, Group: "Laut Abrechnung"},
			{Key: "stated_result", Label: "Ergebnis laut Abrechnung (Nachzahlung positiv, Guthaben negativ)", Type: tText, Listable: true, Group: "Laut Abrechnung"},
			{Key: "reserve_opening", Label: "Anteil Rücklage zu Beginn", Type: tText, Group: "Erhaltungsrücklage"},
			{Key: "reserve_withdrawal", Label: "Entnahme aus der Rücklage (Anteil)", Type: tText, Group: "Erhaltungsrücklage"},
			{Key: "reserve_closing", Label: "Anteil Rücklage am Ende", Type: tText, Group: "Erhaltungsrücklage"},
			{Key: "reserve_account", Label: "Rücklagenkonto (Bilanz; leer = Konto der Rücklagen-Position)", Type: tText, Group: "Erhaltungsrücklage", Lookup: glLookup},
			{Key: "withdrawal_account", Label: "Gegenkonto der Entnahme (Aufwand)", Type: tText, Group: "Erhaltungsrücklage", Lookup: glLookup},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: settlementStatusOptions, Group: "Buchung"},
			{Key: "check_result", Label: "Letzte Prüfung", Type: tText, ReadOnly: true, Group: "Buchung"},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "document_number", Label: "Beleg", Type: tText, ReadOnly: true, Group: "Buchung"},
			{Key: "document_id", Label: "Beleg (ID)", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "id", LabelFields: []string{"document_number"}}},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: settlementItemObject, ForeignKey: "contract_id", Match: match,
				Columns: []string{"line_no", "settlement_group", "cost_category", "total_cost", "allocation_key", "key_total", "key_share", "amount", "account_number"}}},
		},
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkSettlement,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "stated_advances", "stated_result", "reserve_opening", "reserve_withdrawal", "reserve_closing")
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "check", Label: "Prüfen", Record: true}, Handle: m.settlementAction("checkSettlement")},
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true,
				Confirm: "Abrechnung prüfen und im Hauptbuch vorerfassen (bei automatischer Buchung: buchen)?"}, Handle: m.settlementAction("postSettlement")},
			{ActionConfig: metamodel.ActionConfig{Name: "copyPrevious", Label: "Positionen aus dem Vorjahr übernehmen", Record: true,
				Confirm: "Kostenarten, Schlüssel, Konten und Objekte der letzten Abrechnung übernehmen (Beträge leer)?"}, Handle: m.copyPreviousAction},
		},
	}
}

func (m *Module) settlementItem() *crud.Entity {
	return &crud.Entity{
		Object: settlementItemObject, Title: "Abrechnungspositionen", Icon: "icon-list", Table: "contract__settlement_item", Section: "Abrechnung",
		Keys: []string{"company_code", "contract_id", "period_from", "line_no"}, Order: "company_code, contract_id, period_from, line_no",
		StatusField: "is_active", Filters: []string{"company_code", "contract_id", "period_from", "cost_category"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "period_from", Label: "Abrechnung (Zeitraum von)", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "settlement_group", Label: "Abrechnungskreis (z. B. Tiefgarage; leer = Haus)", Type: tText, Listable: true},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Required: true, Listable: true, Lookup: lookupCostCategory},
			{Key: "cost_type", Label: "Art der Kostenart", Type: tText, ReadOnly: true},
			{Key: "total_cost", Label: "Gesamtkosten (z. B. der WEG)", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "allocation_key", Label: "Verteilerschlüssel", Type: tText, Listable: true, Group: "Verteilung", Lookup: lookupAllocationKey},
			{Key: "key_total", Label: "Schlüssel gesamt", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "key_share", Label: "Schlüssel Anteil", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "amount", Label: "Einzelbetrag laut Abrechnung", Type: tText, Required: true, Listable: true},
			{Key: "account_number", Label: "Sachkonto (leer = Kostenart)", Type: tText, Listable: true, Lookup: glLookup},
			{Key: "object_type", Label: "Objektart (leer = Hauptobjekt des Vertrags)", Type: tSel, Options: objectTypeOptions, Group: "Zuordnung"},
			{Key: "object_id", Label: "Objekt", Type: tText, Group: "Zuordnung"},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access:  &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Prepare: m.prepareSettlementItem,
		Validate: m.checkSettlementItem,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "total_cost", "amount")
		},
	}
}

// settlementRow: Kopf einer Abrechnung (für Prüfungen der Positionen).
func (m *Module) settlementStatus(ctx context.Context, cc, contract, from string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT status FROM contract__settlement WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, contract, from)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Abrechnung %s ab %s gibt es nicht", contract, from)
	}
	return crud.Str(res.Rows[0][0]), nil
}

func (m *Module) checkSettlement(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != settlementOpen {
		return crud.Invalid("Abrechnung ist %s und nicht mehr änderbar", settlementStatusText(crud.Str(old["status"])))
	}
	cc := crud.Str(rec["company_code"])
	c, err := m.contractOf(ctx, cc, crud.Str(rec["contract_id"]))
	if err != nil {
		return err
	}
	if old == nil {
		rec["status"] = settlementOpen
	}
	for _, k := range []string{"period_from", "period_to", "settlement_date"} {
		d, err := crud.ParseDate(rec[k])
		if err != nil {
			return err
		}
		rec[k] = d
	}
	if crud.Str(rec["period_to"]) < crud.Str(rec["period_from"]) {
		return crud.Invalid("Zeitraum: bis liegt vor von")
	}
	if crud.Str(rec["posting_date"]) == "" {
		rec["posting_date"] = rec["settlement_date"]
	}
	if rec["posting_date"], err = crud.ParseDate(rec["posting_date"]); err != nil {
		return err
	}
	decimals := m.currencyDecimals(ctx, c.Currency)
	for _, k := range []string{"stated_advances", "stated_result", "reserve_opening", "reserve_withdrawal", "reserve_closing"} {
		if err := minorField(rec, k, decimals, false); err != nil {
			return err
		}
	}
	for _, k := range []string{"reserve_account", "withdrawal_account"} {
		if acc := strings.TrimSpace(crud.Str(rec[k])); acc != "" {
			nr, _, err := m.glAccount(ctx, cc, acc)
			if err != nil {
				return err
			}
			rec[k] = nr
		} else {
			rec[k] = nil
		}
	}
	if toInt(rec["reserve_withdrawal"]) != 0 && rec["withdrawal_account"] == nil {
		return crud.Invalid("Entnahme aus der Rücklage braucht das Gegenkonto (Aufwand)")
	}
	rec["changed_at"] = now()
	return nil
}

// minorField: Betrag (Dezimaltext) → kleinste Einheit; leer = NULL bzw. Pflicht.
func minorField(rec crud.Record, key string, decimals int, required bool) error {
	switch v := rec[key].(type) {
	case int64, int:
		return nil
	case nil:
		if required {
			return crud.Invalid("%s ist Pflicht", key)
		}
		return nil
	default:
		s := strings.TrimSpace(crud.Str(v))
		if s == "" {
			if required {
				return crud.Invalid("%s ist Pflicht", key)
			}
			rec[key] = nil
			return nil
		}
		n, err := parseAmount(s, decimals)
		if err != nil {
			return crud.Invalid("%v", err)
		}
		rec[key] = n
	}
	return nil
}

func settlementStatusText(s string) string {
	for _, o := range settlementStatusOptions {
		if o.Value == s {
			return o.Label
		}
	}
	return s
}

func (m *Module) prepareSettlementItem(ctx context.Context, rec crud.Record) error {
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return err
	}
	rec["period_from"] = from
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM contract__settlement_item WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
		rec["company_code"], rec["contract_id"], from)
	if err != nil {
		return err
	}
	rec["line_no"] = toInt(res.Rows[0][0])
	return nil
}

func (m *Module) checkSettlementItem(ctx context.Context, rec, _ crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return err
	}
	rec["period_from"] = from
	status, err := m.settlementStatus(ctx, cc, id, from)
	if err != nil {
		return err
	}
	if status != settlementOpen {
		return crud.Invalid("Abrechnung ist %s – Positionen nicht mehr änderbar", settlementStatusText(status))
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	cat := trimUpper(rec["cost_category"])
	acc, typ, err := m.costCategory(ctx, cc, cat)
	if err != nil {
		return err
	}
	rec["cost_category"], rec["cost_type"] = cat, typ
	if a := strings.TrimSpace(crud.Str(rec["account_number"])); a != "" {
		acc = a
	}
	if acc == "" {
		return crud.Invalid("Sachkonto ist Pflicht (oder Kostenart mit Sachkonto, Modul Betriebskosten)")
	}
	if rec["account_number"], _, err = m.glAccount(ctx, cc, acc); err != nil {
		return err
	}
	decimals := m.currencyDecimals(ctx, c.Currency)
	if err := minorField(rec, "amount", decimals, true); err != nil {
		return err
	}
	if err := minorField(rec, "total_cost", decimals, false); err != nil {
		return err
	}
	for _, k := range []string{"key_total", "key_share"} {
		v := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec[k])), ",", ".")
		if v != "" && (!decimalRe.MatchString(v) || strings.HasPrefix(v, "-")) {
			return crud.Invalid("%s: Zahl ≥ 0 erwartet (z. B. 85,32)", k)
		}
		rec[k] = nilIfEmpty(v)
	}
	if rec["key_total"] != nil && crud.Str(rec["key_total"]) != "" && strings.Trim(crud.Str(rec["key_total"]), "0.") == "" {
		return crud.Invalid("Schlüssel gesamt darf nicht 0 sein")
	}
	rec["allocation_key"] = nilIfEmpty(trimUpper(rec["allocation_key"]))
	rec["settlement_group"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["settlement_group"])))
	if crud.Str(rec["object_id"]) == "" {
		// Hauptobjekt des Vertrags im Zeitraum
		res, err := m.db.Query(ctx, `SELECT object_type, object_id FROM contract__object WHERE company_code = ? AND contract_id = ? AND is_main = ?
			ORDER BY valid_from DESC`, cc, id, true)
		if err == nil && len(res.Rows) > 0 {
			rec["object_type"], rec["object_id"] = res.Rows[0][0], res.Rows[0][1]
		}
	}
	rec["object_type"], rec["object_id"] = nilIfEmpty(crud.Str(rec["object_type"])), nilIfEmpty(crud.Str(rec["object_id"]))
	defaults(rec, map[string]any{"is_active": true})
	return nil
}

// costCategory: Sachkonto und Art einer Kostenart (Modul Betriebskosten).
func (m *Module) costCategory(ctx context.Context, cc, code string) (account, costType string, err error) {
	if code == "" {
		return "", "", crud.Invalid("Kostenart ist Pflicht")
	}
	resp, err := m.services.Call(ctx, "CostCategory", "get", map[string]any{"id": cc + "|" + code})
	if errors.Is(err, sdk.ErrNotFound) {
		return "", "", crud.Invalid("Kostenart %q gibt es im Buchungskreis %s nicht (Betriebskosten → Kostenarten)", code, cc)
	}
	if err != nil {
		return "", "", unavailable("Betriebskosten", err)
	}
	var c struct {
		Account  string `json:"account_number"`
		CostType string `json:"cost_type"`
	}
	if err := sdk.Decode(resp.Payload, &c); err != nil {
		return "", "", err
	}
	return c.Account, c.CostType, nil
}

// settlementAction: „Prüfen“ bzw. „Buchen …“ – rechnet contract-billing.
// Mitgegeben werden die Vorauszahlungs-Konditionsarten und die Toleranz.
func (m *Module) settlementAction(action string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		key, err := m.set.Entity(settlementObject).ParseID(in.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
		from, _ := crud.ParseDate(key["period_from"])
		if action == "postSettlement" {
			if err := requireWrite(ctx, "Contract", "post", cc); err != nil {
				return sdk.Response{}, err
			}
		}
		c, err := m.contractOf(ctx, cc, id)
		if err != nil {
			return sdk.Response{}, err
		}
		tol := m.text(ctx, `SELECT settlement_tolerance FROM contract__contract_type WHERE company_code = ? AND code = ?`, cc, c.Type)
		res, err := m.db.Query(ctx, `SELECT code FROM contract__condition_type WHERE company_code = ? AND is_advance = ?`, cc, true)
		if err != nil {
			return sdk.Response{}, err
		}
		advance := []string{}
		for _, r := range res.Rows {
			advance = append(advance, crud.Str(r[0]))
		}
		resp, err := m.billingCall(ctx, action, map[string]any{"company_code": cc, "contract_id": id, "period_from": from,
			"advance_types": advance, "tolerance": tol})
		if err != nil {
			return sdk.Response{}, err
		}
		if out, ok := resp.Payload.(map[string]any); ok {
			if s := crud.Str(out["check_result"]); s != "" {
				_, _ = m.db.Exec(ctx, `UPDATE contract__settlement SET check_result = ? WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
					s, cc, id, from)
			}
		}
		return resp, nil
	}
}

// recordSettlementAction: contract-billing vermerkt die Buchung einer Abrechnung
// (ContractSettlementService.record, in seiner Transaktion).
func (m *Module) recordSettlementAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode    string `json:"company_code"`
		ContractID     string `json:"contract_id"`
		PeriodFrom     string `json:"period_from"`
		Status         string `json:"status"`
		DraftID        string `json:"draft_id"`
		DocumentID     string `json:"document_id"`
		DocumentNumber string `json:"document_number"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if in.Status != settlementDraft && in.Status != settlementPosted {
		return sdk.Response{}, crud.Invalid("Status %q: DRAFT oder POSTED", in.Status)
	}
	if err := requireWrite(ctx, "Contract", "post", in.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	from, err := crud.ParseDate(in.PeriodFrom)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Exec(ctx, `UPDATE contract__settlement SET status = ?, draft_id = ?, document_id = ?, document_number = ?, changed_at = ?
		WHERE company_code = ? AND contract_id = ? AND period_from = ? AND status = ?`, in.Status, in.DraftID, nilIfEmpty(in.DocumentID),
		nilIfEmpty(in.DocumentNumber), now(), in.CompanyCode, in.ContractID, from, settlementOpen)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.RowsAffected == 0 {
		return sdk.Response{}, crud.Invalid("Abrechnung %s ab %s ist nicht erfasst", in.ContractID, from)
	}
	return sdk.Response{Payload: map[string]any{"recorded": true}}, nil
}

// copyPreviousAction: Positionen der letzten früheren Abrechnung des Vertrags
// übernehmen – ohne Beträge (Gesamtkosten und Einzelbetrag 0).
func (m *Module) copyPreviousAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(settlementObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	from, _ := crud.ParseDate(key["period_from"])
	if err := requireWrite(ctx, "Contract", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	status, err := m.settlementStatus(ctx, cc, id, from)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != settlementOpen {
		return sdk.Response{}, crud.Invalid("Abrechnung ist %s", settlementStatusText(status))
	}
	prev := m.text(ctx, `SELECT MAX(period_from) FROM contract__settlement WHERE company_code = ? AND contract_id = ? AND period_from < ?`, cc, id, from)
	if prev == "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keine frühere Abrechnung", id)
	}
	prev, _ = crud.ParseDate(prev)
	var n int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		base := toInt(m.text(ctx, `SELECT COALESCE(MAX(line_no), 0) FROM contract__settlement_item WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
			cc, id, from))
		res, err := m.db.Exec(ctx, `INSERT INTO contract__settlement_item (company_code, contract_id, period_from, line_no, settlement_group, cost_category,
			cost_type, total_cost, allocation_key, key_total, key_share, amount, account_number, object_type, object_id, note, is_active)
			SELECT company_code, contract_id, ?, line_no + ?, settlement_group, cost_category, cost_type, 0, allocation_key, key_total, key_share, 0,
			account_number, object_type, object_id, note, is_active FROM contract__settlement_item
			WHERE company_code = ? AND contract_id = ? AND period_from = ? AND is_active = ?`, from, base, cc, id, prev, true)
		if err != nil {
			return err
		}
		n = res.RowsAffected
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "copied": n,
		"message": fmt.Sprintf("%d Positionen aus der Abrechnung ab %s übernommen – Gesamtkosten und Einzelbeträge eintragen", n, germanDate(prev))}}, nil
}
