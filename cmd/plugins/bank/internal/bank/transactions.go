package bank

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

var (
	statusOptions = []metamodel.Option{
		{Value: stOpen, Label: "offen"}, {Value: stProposed, Label: "Vorschlag (prüfen)"}, {Value: stMatched, Label: "zugeordnet"},
		{Value: stPosted, Label: "gebucht"}, {Value: stIgnored, Label: "ignoriert"}}
	targetTypeOptions = []metamodel.Option{
		{Value: tgContract, Label: "Vertrag"}, {Value: tgPartner, Label: "Geschäftspartner"},
		{Value: tgInvoice, Label: "Eingangsrechnung"}, {Value: tgAccount, Label: "Sachkonto (z. B. Entgelte, Zinsen)"}}
	sourceOptions = []metamodel.Option{
		{Value: "RULE", Label: "Regel"}, {Value: "REFERENCE", Label: "Nummer im Verwendungszweck"}, {Value: "INVOICE", Label: "Eingangsrechnung"},
		{Value: "IBAN", Label: "IBAN"}, {Value: "NAME", Label: "Name"}, {Value: "MANUAL", Label: "von Hand"}}
	directionOptions = []metamodel.Option{{Value: "ANY", Label: "Eingang und Ausgang"}, {Value: "IN", Label: "Eingang"}, {Value: "OUT", Label: "Ausgang"}}

	showContract = &metamodel.Condition{Field: "target_type", Values: []string{tgContract}}
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
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "import_id", Label: "Einlesung", Type: tText, Listable: true},
			{Key: "account_id", Label: "Bankkonto", Type: tText, Listable: true},
			{Key: "format", Label: "Importformat", Type: tText, Listable: true},
			{Key: "file_name", Label: "Datei", Type: tText, Listable: true},
			{Key: "imported_at", Label: "Eingelesen am", Type: tText, Listable: true},
			{Key: "imported_by", Label: "Eingelesen von", Type: tText},
			{Key: "rows_read", Label: "Gelesen", Type: tNum, Listable: true},
			{Key: "rows_new", Label: "Neu", Type: tNum, Listable: true},
			{Key: "rows_duplicate", Label: "Schon vorhanden", Type: tNum, Listable: true},
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
		Filters:    []string{"company_code", "account_id", "status", "target_type", "contract_id", "partner_id", "import_id"},
		Search:     []string{"counterparty_name", "purpose", "booking_text"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
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
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: statusOptions, Group: "Zuordnung"},
			{Key: "target_type", Label: "Zuordnung zu", Type: tSel, Listable: true, Options: targetTypeOptions, Group: "Zuordnung"},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: lookupContract, Group: "Zuordnung", ShowIf: showContract},
			{Key: "partner_id", Label: "Geschäftspartner", Type: tText, Listable: true, Lookup: lookupPartner, Group: "Zuordnung", ShowIf: showPartner},
			{Key: "role_code", Label: "Rolle des Partners (Finanzrolle; leer = einzige)", Type: tText, Lookup: lookupRole, Group: "Zuordnung", ShowIf: showPartner},
			{Key: "invoice_id", Label: "Eingangsrechnung", Type: tText, Lookup: lookupInvoice, Group: "Zuordnung", ShowIf: showInvoice},
			{Key: "gl_account", Label: "Sachkonto", Type: tText, Lookup: lookupAccount, Group: "Zuordnung", ShowIf: showAccount},
			{Key: "match_source", Label: "Zugeordnet über", Type: tSel, ReadOnly: true, Options: sourceOptions, Group: "Zuordnung"},
			{Key: "match_note", Label: "Begründung", Type: tText, ReadOnly: true, Group: "Zuordnung"},
			{Key: "rule_no", Label: "Regel", Type: tNum, ReadOnly: true, Group: "Zuordnung"},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "document_number", LabelFields: []string{"header_text"}}},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "posting_trace", Label: "Gebucht als", Type: tArea, ReadOnly: true, Group: "Buchung"},
			{Key: "posted_at", Label: "Gebucht am", Type: tText, ReadOnly: true, Group: "Buchung"},
		},
		Access:   &crud.Access{Object: accountObject, Records: true, CompanyCode: "company_code"},
		Decorate: m.decorateTxn,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "assign", Label: "Zuordnen …", Record: true,
				Fields: []string{"target_type", "contract_id", "partner_id", "role_code", "invoice_id", "gl_account"}}, Handle: m.assignAction},
			{ActionConfig: metamodel.ActionConfig{Name: "accept", Label: "Vorschlag übernehmen", Record: true,
				Confirm: "Vorschlag übernehmen? Der Umsatz ist dann buchbar."}, Handle: m.statusAction(stProposed, stMatched)},
			{ActionConfig: metamodel.ActionConfig{Name: "ignore", Label: "Ignorieren", Record: true,
				Confirm: "Umsatz nicht buchen (z. B. schon anders gebucht)?"}, Handle: m.statusAction("", stIgnored)},
			{ActionConfig: metamodel.ActionConfig{Name: "reset", Label: "Zuordnung aufheben", Record: true,
				Confirm: "Zuordnung aufheben? Der Umsatz ist danach wieder offen."}, Handle: m.statusAction("", stOpen)},
		},
	}
}

func (m *Module) decorateTxn(ctx context.Context, rec crud.Record) error {
	d := m.currencyDecimals(ctx, crud.Str(rec["currency"]))
	if rec["amount"] != nil {
		rec["amount"] = formatAmount(toInt(rec["amount"]), d)
	}
	if st := crud.Str(rec["status"]); st == stPosted || st == stIgnored {
		rec["_locked"] = true
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
}

func targetOf(data map[string]any) target {
	s := func(k string) string { return strings.TrimSpace(crud.Str(data[k])) }
	return target{Type: strings.ToUpper(s("target_type")), Contract: s("contract_id"), Partner: s("partner_id"), Role: strings.ToUpper(s("role_code")),
		Invoice: s("invoice_id"), Account: s("gl_account")}
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
	case tgAccount:
		nr, err := m.glAccount(ctx, cc, t.Account)
		if err != nil {
			return out, err
		}
		out.Account = nr
	default:
		return out, crud.Invalid("Zuordnung zu: Vertrag, Geschäftspartner, Eingangsrechnung oder Sachkonto")
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
		gl_account = ?, match_source = ?, match_note = ?, rule_no = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`,
		status, nilIfEmpty(t.Type), nilIfEmpty(t.Contract), nilIfEmpty(t.Partner), nilIfEmpty(t.Role), nilIfEmpty(t.Invoice), nilIfEmpty(t.Account),
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
	var learned int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.setTarget(ctx, k, t, stMatched, "MANUAL", "von Hand zugeordnet", nil); err != nil {
			return err
		}
		learned, err = m.learnRule(ctx, k, t)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "Umsatz zugeordnet"
	if learned > 0 {
		msg += fmt.Sprintf("; Regel %d vorgeschlagen – nach Bestätigung ordnet sie gleiche Umsätze maschinell zu", learned)
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "rule_no": learned}}, nil
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
			res, err := m.db.Query(ctx, `SELECT target_type, contract_id, partner_id, role_code, invoice_id, gl_account, match_source, match_note
				FROM bank__transaction WHERE company_code = ? AND account_id = ? AND txn_no = ?`, k.CC, k.Account, k.No)
			if err != nil {
				return sdk.Response{}, err
			}
			r := res.Rows[0]
			t, err := m.resolve(ctx, k.CC, target{Type: crud.Str(r[0]), Contract: crud.Str(r[1]), Partner: crud.Str(r[2]), Role: crud.Str(r[3]),
				Invoice: crud.Str(r[4]), Account: crud.Str(r[5])})
			if err != nil {
				return sdk.Response{}, crud.Invalid("Vorschlag unvollständig – „Zuordnen …“: %v", err)
			}
			if err := m.setTarget(ctx, k, t, stMatched, crud.Str(r[6]), crud.Str(r[7])+" (übernommen)", nil); err != nil {
				return sdk.Response{}, err
			}
			return sdk.Response{Payload: map[string]any{"message": "Vorschlag übernommen – der Umsatz ist buchbar"}}, nil
		}
		q := `UPDATE bank__transaction SET status = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`
		if to == stOpen {
			q = `UPDATE bank__transaction SET status = ?, changed_at = ?, changed_by = ?, target_type = NULL, contract_id = NULL, partner_id = NULL,
				role_code = NULL, invoice_id = NULL, gl_account = NULL, match_source = NULL, match_note = NULL, rule_no = NULL
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
