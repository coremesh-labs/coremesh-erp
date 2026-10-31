package bank

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

var (
	statusOptions = []metamodel.Option{
		{Value: stOpen, Label: "offen"}, {Value: stProposed, Label: "Vorschlag (prüfen)"}, {Value: stMatched, Label: "zugeordnet"},
		{Value: stPosted, Label: "gebucht"}, {Value: stIgnored, Label: "ignoriert"}}
	targetTypeOptions = []metamodel.Option{
		{Value: tgContract, Label: "Vertrag"}, {Value: tgPartner, Label: "Geschäftspartner", Short: "Partner"},
		{Value: tgInvoice, Label: "Eingangsrechnung", Short: "Rechnung"}, {Value: tgAccount, Label: "Sachkonto (z. B. Entgelte, Zinsen)"}}
	sourceOptions = []metamodel.Option{
		{Value: "RULE", Label: "Regel"}, {Value: "MANDATE", Label: "Lastschriftmandat", Short: "Mandat"}, {Value: "REFERENCE", Label: "Nummer im Verwendungszweck"}, {Value: "INVOICE", Label: "Eingangsrechnung", Short: "Rechnung"},
		{Value: "IBAN", Label: "IBAN"}, {Value: "NAME", Label: "Name"}, {Value: "MANUAL", Label: "von Hand"}}
	directionOptions = []metamodel.Option{{Value: "ANY", Label: "Eingang und Ausgang", Short: "beide"}, {Value: "IN", Label: "Eingang"}, {Value: "OUT", Label: "Ausgang"}}

	showContract = &metamodel.Condition{Field: "target_type", Values: []string{tgContract, tgAccount}}
	showPartner  = &metamodel.Condition{Field: "target_type", Values: []string{tgPartner}}
	showInvoice  = &metamodel.Condition{Field: "target_type", Values: []string{tgInvoice}}
	showAccount  = &metamodel.Condition{Field: "target_type", Values: []string{tgAccount}}
)

func (m *Module) importRun() *crud.Entity {
	return &crud.Entity{
		Object: importObject, Title: "Einlesungen", Icon: "icon-upload", Table: "bank__import", Section: "Bank",
		Keys: []string{"company_code", "import_id"}, Order: "company_code, import_id DESC", ReadOnly: true, TitleField: "import_id",
		Filters: []string{"company_code", "account_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "import_id", Label: "Einlesung", Type: tText, Listable: true},
			{Key: "account_id", Label: "Bankkonto", Type: tText, Listable: true},
			{Key: "format", Label: "Importformat", Short: "Format", Type: tText, Listable: true},
			{Key: "file_name", Label: "Datei", Type: tText, Listable: true},
			{Key: "imported_at", Label: "Eingelesen am", Type: tText, Listable: true},
			{Key: "imported_by", Label: "Eingelesen von", Type: tText, Lookup: crud.LookupUser},
			{Key: "rows_read", Label: "Gelesen", Type: tNum, Listable: true},
			{Key: "rows_new", Label: "Neu", Type: tNum, Listable: true},
			{Key: "rows_duplicate", Label: "Schon vorhanden", Short: "Vorhanden", Type: tNum, Listable: true},
			{Key: "date_from", Label: "Umsätze von", Type: tDate},
			{Key: "date_to", Label: "Umsätze bis", Type: tDate},
			{Key: "message", Label: "Ergebnis", Type: tText},
		},
		Access: &crud.Access{Object: accountObject, Records: true, CompanyCode: "company_code"},
	}
}

func (m *Module) transaction() *crud.Entity {
	return &crud.Entity{
		Object: txnObject, Title: "Umsätze", Icon: "icon-list", Table: "bank__transaction", Section: "Bank",
		Keys: []string{"company_code", "account_id", "txn_no"}, Order: "company_code, account_id, booking_date DESC, txn_no DESC", ReadOnly: true,
		TitleField: "counterparty_name",
		Filters:    []string{"company_code", "account_id", "open_only", "status", "target_type", "contract_id", "partner_id", "import_id", "counterparty_iban"},
		// „Nur offene“: offene Umsätze und Vorschläge – blendet zugeordnete, gebuchte und ignorierte aus.
		FilterExpr: map[string]func(any) (string, []any){"open_only": func(v any) (string, []any) {
			if !crud.AsBool(v) {
				return "1 = 1", nil
			}
			return "status IN (?, ?)", []any{stOpen, stProposed}
		}},
		Search: []string{"counterparty_name", "purpose", "booking_text"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "account_id", Label: "Bankkonto", Type: tText, Listable: true, ReadOnly: true},
			{Key: "txn_no", Label: "Nr.", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "booking_date", Label: "Buchungstag", Type: tDate, Listable: true, ReadOnly: true, Group: "Umsatz"},
			{Key: "value_date", Label: "Wertstellung", Type: tDate, ReadOnly: true, Group: "Umsatz"},
			{Key: "amount", Label: "Betrag", Type: tText, Listable: true, ReadOnly: true, Group: "Umsatz"},
			{Key: "currency", Label: "Währung", Type: tText, ReadOnly: true, Group: "Umsatz"},
			{Key: "counterparty_name", Label: "Gegenseite", Type: tText, Listable: true, ReadOnly: true, Group: "Umsatz"},
			{Key: "counterparty_iban", Label: "IBAN der Gegenseite", Type: tText, ReadOnly: true, Group: "Umsatz"},
			{Key: "counterparty_bic", Label: "BIC der Gegenseite", Type: tText, ReadOnly: true, Group: "Umsatz"},
			{Key: "purpose", Label: "Verwendungszweck", Type: tText, Listable: true, ReadOnly: true, Group: "Umsatz"},
			{Key: "booking_text", Label: "Buchungstext", Type: tArea, ReadOnly: true, Group: "Umsatz"},
			{Key: "transaction_type", Label: "Umsatzart", Type: tText, ReadOnly: true, Group: "Umsatz"},
			{Key: "end_to_end_ref", Label: "End-to-End-Referenz", Type: tText, ReadOnly: true, Group: "Referenzen"},
			{Key: "mandate_ref", Label: "Mandatsreferenz", Type: tText, ReadOnly: true, Group: "Referenzen"},
			{Key: "creditor_id", Label: "Gläubiger-ID", Type: tText, ReadOnly: true, Group: "Referenzen"},
			{Key: "customer_ref", Label: "Kundenreferenz", Type: tText, ReadOnly: true, Group: "Referenzen"},
			{Key: "import_id", Label: "Einlesung", Type: tText, ReadOnly: true, Group: "Referenzen"},
			{Key: "open_only", Label: "Nur offene (ohne zugeordnete, gebuchte und ignorierte)", Type: tBool, Virtual: true, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: statusOptions, Group: "Zuordnung"},
			{Key: "target_type", Label: "Zuordnung zu", Short: "Zuordnung", Type: tSel, Listable: true, Options: targetTypeOptions, Group: "Zuordnung"},
			{Key: "contract_id", Label: "Vertrag (bei Sachkonto: Bezug, z. B. Kautionsvertrag – leer = ohne)", Short: "Vertrag", Type: tText, Listable: true,
				Lookup: lookupContract, Group: "Zuordnung", ShowIf: showContract},
			{Key: "partner_id", Label: "Geschäftspartner", Short: "Partner", Type: tText, Listable: true, Lookup: lookupPartner, Group: "Zuordnung", ShowIf: showPartner},
			{Key: "role_code", Label: "Rolle des Partners (Finanzrolle; leer = einzige)", Type: tText, Lookup: lookupRole, Group: "Zuordnung", ShowIf: showPartner},
			{Key: "invoice_id", Label: "Eingangsrechnung", Type: tText, Lookup: lookupInvoice, Group: "Zuordnung", ShowIf: showInvoice},
			{Key: "gl_account", Label: "Sachkonto", Type: tText, Lookup: lookupAccount, Group: "Zuordnung", ShowIf: showAccount},
			{Key: "asset_id", Label: "Objekt (Kostenträger; leer = ohne)", Short: "Objekt", Type: tText, Listable: true, Lookup: lookupAsset, Group: "Zuordnung"},
			{Key: "match_source", Label: "Zugeordnet über", Type: tSel, ReadOnly: true, Options: sourceOptions, Group: "Zuordnung"},
			{Key: "match_note", Label: "Begründung", Type: tText, ReadOnly: true, Group: "Zuordnung"},
			{Key: "rule_no", Label: "Regel", Type: tNum, ReadOnly: true, Group: "Zuordnung"},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "document_number", LabelFields: []string{"header_text"}}},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "posting_trace", Label: "Gebucht als", Type: tArea, ReadOnly: true, Group: "Buchung"},
			{Key: "posted_at", Label: "Gebucht am", Type: tText, ReadOnly: true, Group: "Buchung"},
			{Key: "reversal_date", Label: "Storno am (leer = Buchungstag des Umsatzes)", Type: tDate, Virtual: true, Group: "Buchung"},
		},
		Access:   &crud.Access{Object: accountObject, Records: true, CompanyCode: "company_code"},
		Decorate: m.decorateTxn,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "assign", Label: "Zuordnen …", Record: true,
				Fields: []string{"target_type", "contract_id", "partner_id", "role_code", "invoice_id", "gl_account", "asset_id"}}, Handle: m.assignAction},
			{ActionConfig: metamodel.ActionConfig{Name: "accept", Label: "Vorschlag übernehmen", Record: true,
				Confirm: "Vorschlag übernehmen? Der Umsatz ist dann buchbar."}, Handle: m.statusAction(stProposed, stMatched)},
			{ActionConfig: metamodel.ActionConfig{Name: "ignore", Label: "Ignorieren", Record: true,
				Confirm: "Umsatz nicht buchen (z. B. schon anders gebucht)?"}, Handle: m.statusAction("", stIgnored)},
			{ActionConfig: metamodel.ActionConfig{Name: "reset", Label: "Zuordnung aufheben", Record: true,
				Confirm: "Zuordnung aufheben? Der Umsatz ist danach wieder offen."}, Handle: m.statusAction("", stOpen)},
			{ActionConfig: metamodel.ActionConfig{Name: "unpost", Label: "Buchung stornieren …", Record: true, Fields: []string{"reversal_date"},
				Confirm: "Beleg des Umsatzes stornieren? Der Umsatz ist danach wieder zugeordnet und lässt sich neu zuordnen und buchen."},
				Handle: m.unpostAction},
		},
	}
}

func (m *Module) decorateTxn(ctx context.Context, rec crud.Record) error {
	d := m.currencyDecimals(ctx, crud.Str(rec["currency"]))
	if rec["amount"] != nil {
		rec["amount"] = formatAmount(toInt(rec["amount"]), d)
	}
	switch st := crud.Str(rec["status"]); st {
	case stPosted, stIgnored:
		rec["_locked"] = true
	case stOpen: // nichts zu übernehmen oder aufzuheben
		rec["_hidden_actions"] = []any{"accept", "reset"}
	case stMatched:
		rec["_hidden_actions"] = []any{"accept"}
	}
	return nil
}

// txnKey aus der Record-ID.
type txnKey struct {
	CC, Account string
	No          int64
}

func (m *Module) txnKeyOf(payload any) (txnKey, map[string]any, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(payload, &in); err != nil {
		return txnKey{}, nil, err
	}
	key, err := m.set.Entity(txnObject).ParseID(in.ID)
	if err != nil {
		return txnKey{}, nil, err
	}
	return txnKey{CC: crud.Str(key["company_code"]), Account: crud.Str(key["account_id"]), No: toInt(key["txn_no"])}, in.Data, nil
}

// target ist das Ziel einer Zuordnung (Umsatz oder Regel).
type target struct {
	Type, Contract, Partner, Role, Invoice, Account string
	Asset                                           string // Objekt als Kostenträger (seit 0.6.0)
}

func targetOf(data map[string]any) target {
	s := func(k string) string { return strings.TrimSpace(crud.Str(data[k])) }
	return target{Type: strings.ToUpper(s("target_type")), Contract: s("contract_id"), Partner: s("partner_id"), Role: strings.ToUpper(s("role_code")),
		Invoice: s("invoice_id"), Account: s("gl_account"), Asset: s("asset_id")}
}

// resolve prüft das Ziel und ergänzt Partner und Rolle (Vertrag: Partner und
// Rolle der Vertragsart; Rechnung: Lieferant und Rolle der Rechnungsart).
func (m *Module) resolve(ctx context.Context, cc string, t target) (target, error) {
	out := target{Type: t.Type}
	switch t.Type {
	case tgContract:
		if t.Contract == "" {
			return out, crud.Invalid("Vertrag ist Pflicht")
		}
		c, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + t.Contract})
		if err != nil {
			return out, crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht", t.Contract, cc)
		}
		ct, err := m.services.Call(ctx, "ContractType", "get", map[string]any{"id": cc + "|" + field(c.Payload, "contract_type")})
		if err != nil {
			return out, fmt.Errorf("Vertragsart von %s: %w", t.Contract, err)
		}
		out.Contract, out.Partner, out.Role = t.Contract, field(c.Payload, "partner_id"), field(ct.Payload, "main_role")
	case tgPartner:
		if t.Partner == "" {
			return out, crud.Invalid("Geschäftspartner ist Pflicht")
		}
		if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": t.Partner}); err != nil {
			return out, crud.Invalid("Geschäftspartner %s gibt es nicht", t.Partner)
		}
		role := t.Role
		if role == "" {
			roles, err := m.financeRoles(ctx, cc, t.Partner)
			if err != nil {
				return out, err
			}
			if len(roles) != 1 {
				return out, crud.Invalid("Geschäftspartner %s hat %d Finanzrollen im Buchungskreis %s – Rolle wählen", t.Partner, len(roles), cc)
			}
			role = roles[0]
		}
		out.Partner, out.Role = t.Partner, role
	case tgInvoice:
		if t.Invoice == "" {
			return out, crud.Invalid("Eingangsrechnung ist Pflicht")
		}
		inv, err := m.services.Call(ctx, "SupplierInvoice", "get", map[string]any{"id": cc + "|" + t.Invoice})
		if err != nil {
			return out, crud.Invalid("Eingangsrechnung %s gibt es im Buchungskreis %s nicht", t.Invoice, cc)
		}
		if field(inv.Payload, "status") != "POSTED" {
			return out, crud.Invalid("Eingangsrechnung %s ist nicht gebucht", t.Invoice)
		}
		it, err := m.services.Call(ctx, "InvoiceType", "get", map[string]any{"id": cc + "|" + field(inv.Payload, "invoice_type")})
		if err != nil {
			return out, fmt.Errorf("Rechnungsart von %s: %w", t.Invoice, err)
		}
		out.Invoice, out.Partner, out.Role = t.Invoice, field(inv.Payload, "supplier_id"), field(it.Payload, "supplier_role")
		// Vertrag der Rechnung (Rechnung zum Vertrag, Rahmenvertrag) auch an der Zahlung (seit 0.6.1)
		out.Contract = field(inv.Payload, "framework_contract")
	case tgAccount:
		nr, err := m.glAccount(ctx, cc, t.Account)
		if err != nil {
			return out, err
		}
		out.Account = nr
		// Bezug auf einen Vertrag (seit 0.5.0), z. B. Kautionsvertrag auf dem Kautionskonto
		if t.Contract != "" {
			if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + t.Contract}); err != nil {
				return out, crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht", t.Contract, cc)
			}
			out.Contract = t.Contract
		}
	default:
		return out, crud.Invalid("Zuordnung zu: Vertrag, Geschäftspartner, Eingangsrechnung oder Sachkonto")
	}
	if t.Asset != "" {
		if _, err := m.services.Call(ctx, "Asset", "get", map[string]any{"id": cc + "|" + t.Asset}); err != nil {
			return out, crud.Invalid("Objekt %s gibt es im Buchungskreis %s nicht", t.Asset, cc)
		}
		out.Asset = t.Asset
	}
	return out, nil
}

// financeRoles: Rollen des Partners mit Buchungskreisdaten im Buchungskreis.
func (m *Module) financeRoles(ctx context.Context, cc, partner string) ([]string, error) {
	resp, err := m.services.Call(ctx, "PartnerCompanyCode", "list", map[string]any{"query": map[string]any{"bp_id": partner, "company_code": cc}})
	if err != nil {
		return nil, unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Role string `json:"role_code"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	var roles []string
	for _, it := range out.Items {
		roles = append(roles, it.Role)
	}
	return roles, nil
}

func field(payload any, key string) string {
	var m map[string]any
	if sdk.Decode(payload, &m) == nil {
		return crud.Str(m[key])
	}
	return ""
}

// setTarget schreibt Zuordnung, Status und Begründung.
func (m *Module) setTarget(ctx context.Context, k txnKey, t target, status, source, note string, rule any) error {
	_, err := m.db.Exec(ctx, `UPDATE bank__transaction SET status = ?, target_type = ?, contract_id = ?, partner_id = ?, role_code = ?, invoice_id = ?,
		gl_account = ?, asset_id = ?, match_source = ?, match_note = ?, rule_no = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`,
		status, nilIfEmpty(t.Type), nilIfEmpty(t.Contract), nilIfEmpty(t.Partner), nilIfEmpty(t.Role), nilIfEmpty(t.Invoice), nilIfEmpty(t.Account), nilIfEmpty(t.Asset),
		nilIfEmpty(source), nilIfEmpty(note), rule, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), k.CC, k.Account, k.No)
	return err
}

func (m *Module) txnStatus(ctx context.Context, k txnKey) (string, error) {
	res, err := m.db.Query(ctx, `SELECT status FROM bank__transaction WHERE company_code = ? AND account_id = ? AND txn_no = ?`, k.CC, k.Account, k.No)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", fmt.Errorf("%w: Umsatz %s/%d", sdk.ErrNotFound, k.Account, k.No)
	}
	return crud.Str(res.Rows[0][0]), nil
}

// assignAction: „Zuordnen …“ von Hand – Umsatz zugeordnet, Regel als Vorschlag gelernt.
func (m *Module) assignAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, data, err := m.txnKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, txnObject, "assign", k.CC); err != nil {
		return sdk.Response{}, err
	}
	st, err := m.txnStatus(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if st == stPosted {
		return sdk.Response{}, crud.Invalid("Umsatz ist gebucht")
	}
	t, err := m.resolve(ctx, k.CC, targetOf(data))
	if err != nil {
		return sdk.Response{}, err
	}
	hint, err := m.mandateHint(ctx, k, t.Contract)
	if err != nil {
		return sdk.Response{}, err
	}
	note := "von Hand zugeordnet"
	if hint != "" {
		note += " – " + hint
	}
	var learned int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.setTarget(ctx, k, t, stMatched, "MANUAL", note, nil); err != nil {
			return err
		}
		learned, err = m.learnRule(ctx, k, t)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "Umsatz zugeordnet"
	if hint != "" {
		msg += "; Achtung: " + hint
	}
	if learned > 0 {
		msg += fmt.Sprintf("; Regel %d vorgeschlagen – nach Bestätigung ordnet sie gleiche Umsätze maschinell zu", learned)
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "rule_no": learned}}, nil
}

// mandateHint: Prüfung der Lastschrift gegen das Mandat des Vertrags (leer = passt).
func (m *Module) mandateHint(ctx context.Context, k txnKey, contract string) (string, error) {
	if contract == "" {
		return "", nil
	}
	res, err := m.db.Query(ctx, `SELECT COALESCE(mandate_ref, ''), COALESCE(creditor_id, ''), amount FROM bank__transaction
		WHERE company_code = ? AND account_id = ? AND txn_no = ?`, k.CC, k.Account, k.No)
	if err != nil || len(res.Rows) == 0 || crud.Str(res.Rows[0][0]) == "" {
		return "", err
	}
	c, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": k.CC + "|" + contract})
	if err != nil {
		return "", err
	}
	uses := false
	if pm := field(c.Payload, "payment_method"); pm != "" {
		if p, err := m.services.Call(ctx, "PaymentMethod", "get", map[string]any{"id": k.CC + "|" + pm}); err == nil {
			var pmRec map[string]any
			if sdk.Decode(p.Payload, &pmRec) == nil {
				uses = crud.AsBool(pmRec["uses_mandate"])
			}
		}
	}
	ref := &refData{contracts: []contractRef{{ID: contract, UsesMandate: uses,
		Mandate: field(c.Payload, "mandate_ref"), Creditor: field(c.Payload, "creditor_id")}}}
	hint, _ := ref.checkMandate(contract, &txnRow{Mandate: crud.Str(res.Rows[0][0]), Creditor: crud.Str(res.Rows[0][1]), Amount: toInt(res.Rows[0][2])})
	return hint, nil
}

// statusAction: Status wechseln (from leer = jeder außer gebucht).
func (m *Module) statusAction(from, to string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		k, _, err := m.txnKeyOf(req.Payload)
		if err != nil {
			return sdk.Response{}, err
		}
		if err := requireWrite(ctx, txnObject, map[string]string{stMatched: "accept", stIgnored: "ignore", stOpen: "reset"}[to], k.CC); err != nil {
			return sdk.Response{}, err
		}
		st, err := m.txnStatus(ctx, k)
		if err != nil {
			return sdk.Response{}, err
		}
		switch {
		case st == stPosted:
			return sdk.Response{}, crud.Invalid("Umsatz ist gebucht")
		case from != "" && st != from:
			return sdk.Response{}, crud.Invalid("Umsatz ist nicht im Status %s", from)
		}
		if to == stMatched {
			// Vorschlag übernehmen: Ziel prüfen und ergänzen (Partner, Rolle)
			res, err := m.db.Query(ctx, `SELECT target_type, contract_id, partner_id, role_code, invoice_id, gl_account, match_source, match_note, asset_id
				FROM bank__transaction WHERE company_code = ? AND account_id = ? AND txn_no = ?`, k.CC, k.Account, k.No)
			if err != nil {
				return sdk.Response{}, err
			}
			r := res.Rows[0]
			t, err := m.resolve(ctx, k.CC, target{Type: crud.Str(r[0]), Contract: crud.Str(r[1]), Partner: crud.Str(r[2]), Role: crud.Str(r[3]),
				Invoice: crud.Str(r[4]), Account: crud.Str(r[5]), Asset: crud.Str(r[8])})
			if err != nil {
				return sdk.Response{}, crud.Invalid("Vorschlag unvollständig – „Zuordnen …“: %v", err)
			}
			// Übernommen = von Hand entschieden: der nächste Lauf bewertet ihn nicht neu (MANUAL),
			// und wie beim Zuordnen von Hand entsteht ein Regelvorschlag für gleiche Umsätze.
			var learned int64
			err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
				if err := m.setTarget(ctx, k, t, stMatched, "MANUAL", crud.Str(r[7])+" (übernommen)", nil); err != nil {
					return err
				}
				learned, err = m.learnRule(ctx, k, t)
				return err
			})
			if err != nil {
				return sdk.Response{}, err
			}
			msg := "Vorschlag übernommen – der Umsatz ist buchbar"
			if learned > 0 {
				msg += fmt.Sprintf("; Regel %d vorgeschlagen – nach Bestätigung ordnet sie gleiche Umsätze maschinell zu", learned)
			}
			return sdk.Response{Payload: map[string]any{"message": msg, "rule_proposed": learned}}, nil
		}
		q := `UPDATE bank__transaction SET status = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`
		if to == stOpen {
			q = `UPDATE bank__transaction SET status = ?, changed_at = ?, changed_by = ?, target_type = NULL, contract_id = NULL, partner_id = NULL,
				role_code = NULL, invoice_id = NULL, gl_account = NULL, asset_id = NULL, match_source = NULL, match_note = NULL, rule_no = NULL
				WHERE company_code = ? AND account_id = ? AND txn_no = ?`
		}
		if _, err := m.db.Exec(ctx, q, to, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), k.CC, k.Account, k.No); err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: map[string]any{"message": "Status: " + statusText(to)}}, nil
	}
}

func statusText(s string) string {
	for _, o := range statusOptions {
		if o.Value == s {
			return o.Label
		}
	}
	return s
}

// unpostAction: „Buchung stornieren …“ (seit 0.5.0) – storniert den Beleg eines gebuchten
// Umsatzes im Hauptbuch (Stornobeleg am Buchungstag des Umsatzes bzw. am angegebenen Tag);
// der Umsatz ist danach wieder zugeordnet und lässt sich neu zuordnen und buchen.
func (m *Module) unpostAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, data, err := m.txnKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, txnObject, "unpost", k.CC); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT status, COALESCE(document_number, ''), booking_date, COALESCE(posting_trace, '') FROM bank__transaction
		WHERE company_code = ? AND account_id = ? AND txn_no = ?`, k.CC, k.Account, k.No)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Umsatz %s/%d", sdk.ErrNotFound, k.Account, k.No)
	}
	r := res.Rows[0]
	doc, booked, trace := crud.Str(r[1]), dateOnly(r[2]), crud.Str(r[3])
	if crud.Str(r[0]) != stPosted || doc == "" {
		return sdk.Response{}, crud.Invalid("Umsatz ist nicht gebucht")
	}
	date := strings.TrimSpace(crud.Str(data["reversal_date"]))
	if date == "" {
		date = booked
	}
	// Geschäftsjahr des Belegs aus seinen Positionen (Belegnummer + Buchungstag)
	entries, err := m.services.Call(ctx, "JournalEntryItem", "list", map[string]any{"query": map[string]any{
		"company_code_id": k.CC, "document_number": doc}})
	if err != nil {
		return sdk.Response{}, unavailable("Hauptbuch", err)
	}
	var list struct {
		Items []struct {
			FiscalYear  any    `json:"fiscal_year"`
			PostingDate string `json:"posting_date"`
		} `json:"items"`
	}
	if err := sdk.Decode(entries.Payload, &list); err != nil {
		return sdk.Response{}, err
	}
	years := map[string]bool{}
	for _, it := range list.Items {
		if dateOnly(it.PostingDate) == booked {
			years[fmt.Sprint(it.FiscalYear)] = true
		}
	}
	if len(years) != 1 {
		return sdk.Response{}, crud.Invalid("Beleg %s vom %s nicht eindeutig gefunden", doc, germanDate(booked))
	}
	var year string
	for y := range years {
		year = y
	}
	rev, err := ledgerapi.New(m.services).Reverse(ctx, ledgerapi.ReverseRequest{
		ID: fmt.Sprintf("%s|%s|%s", k.CC, year, doc), PostingDate: date,
		HeaderText: fmt.Sprintf("Storno Bankumsatz %s/%d", k.Account, k.No)})
	if err != nil {
		return sdk.Response{}, err
	}
	trace = strings.TrimSpace(trace + " – storniert durch Beleg " + rev.DocumentNumber + " am " + germanDate(date))
	if _, err := m.db.Exec(ctx, `UPDATE bank__transaction SET status = ?, draft_id = NULL, document_number = NULL, posted_at = NULL, posting_trace = ?,
		changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`, stMatched, trace, now(),
		nilIfEmpty(sdk.CallFromContext(ctx).UserID), k.CC, k.Account, k.No); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Beleg %s storniert (Stornobeleg %s) – Umsatz neu zuordnen und buchen", doc, rev.DocumentNumber)}}, nil
}

// dateOnly: Datum als JJJJ-MM-TT (die Datenbank liefert Text oder Zeitpunkt).
func dateOnly(v any) string {
	if t, ok := v.(time.Time); ok {
		return t.Format(time.DateOnly)
	}
	s := crud.Str(v)
	if len(s) > 10 {
		s = s[:10]
	}
	return s
}
