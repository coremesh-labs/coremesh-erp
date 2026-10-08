package bank

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Regeln: Wer von Hand zuordnet, erzeugt einen Regelvorschlag (Gegenseite,
// IBAN, Richtung → Ziel). Er wirkt erst nach „Bestätigen“. Von Hand angelegte
// Regeln gelten sofort. Bei mehreren passenden Regeln gewinnt die genaueste
// (mehr Bedingungen), bei Gleichstand die kleinere Regelnummer.

const (
	ruleProposed  = "PROPOSED"
	ruleConfirmed = "CONFIRMED"
	ruleRejected  = "REJECTED"
)

var ruleStatusOptions = []metamodel.Option{
	{Value: ruleProposed, Label: "Vorschlag (bestätigen)"}, {Value: ruleConfirmed, Label: "bestätigt"}, {Value: ruleRejected, Label: "abgelehnt"}}

func (m *Module) rule() *crud.Entity {
	return &crud.Entity{
		Object: ruleObject, Title: "Zuordnungsregeln", Icon: "icon-branch", Table: "bank__rule", Section: "Bank",
		Keys: []string{"company_code", "rule_no"}, Order: "company_code, rule_no", TitleField: "counterparty_name",
		Filters: []string{"company_code", "status", "account_id", "target_type"}, Search: []string{"counterparty_name", "purpose_contains"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "rule_no", Label: "Regel", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: ruleStatusOptions},
			{Key: "account_id", Label: "Bankkonto (leer = alle)", Type: tText, Listable: true, Group: "Wenn",
				Lookup: &metamodel.Lookup{Object: accountObject, ValueField: "account_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "direction", Label: "Richtung", Type: tSel, Listable: true, Options: directionOptions, Group: "Wenn"},
			{Key: "counterparty_name", Label: "Name der Gegenseite (genau, ohne Groß-/Kleinschreibung)", Type: tText, Listable: true, Group: "Wenn"},
			{Key: "counterparty_iban", Label: "IBAN der Gegenseite", Type: tText, Group: "Wenn"},
			{Key: "purpose_contains", Label: "Verwendungszweck enthält", Type: tText, Listable: true, Group: "Wenn"},
			{Key: "amount", Label: "Betrag (genau, optional)", Type: tText, Group: "Wenn"},
			{Key: "target_type", Label: "Zuordnung zu", Type: tSel, Required: true, Listable: true, Options: targetTypeOptions, Group: "Dann"},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: lookupContract, Group: "Dann", ShowIf: showContract},
			{Key: "partner_id", Label: "Geschäftspartner", Type: tText, Listable: true, Lookup: lookupPartner, Group: "Dann", ShowIf: showPartner},
			{Key: "role_code", Label: "Rolle des Partners", Type: tText, Lookup: lookupRole, Group: "Dann", ShowIf: showPartner},
			{Key: "gl_account", Label: "Sachkonto", Type: tText, Lookup: lookupAccount, Group: "Dann", ShowIf: showAccount},
			{Key: "learned_from", Label: "Gelernt aus Umsatz", Type: tText, ReadOnly: true},
			{Key: "hits", Label: "Treffer", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "confirmed_at", Label: "Bestätigt am", Type: tText, ReadOnly: true},
			{Key: "confirmed_by", Label: "Bestätigt von", Type: tText, ReadOnly: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
		},
		Access: &crud.Access{Object: accountObject, Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			n, err := m.nextRule(ctx, crud.Str(rec["company_code"]))
			rec["rule_no"] = n
			return err
		},
		Validate: m.checkRule,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			if rec["amount"] != nil && crud.Str(rec["amount"]) != "" {
				rec["amount"] = formatAmount(toInt(rec["amount"]), 2)
			}
			return nil
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "confirm", Label: "Bestätigen", Record: true,
				Confirm: "Regel bestätigen? Sie ordnet passende Umsätze künftig maschinell zu."}, Handle: m.ruleStatus(ruleConfirmed)},
			{ActionConfig: metamodel.ActionConfig{Name: "reject", Label: "Ablehnen", Record: true,
				Confirm: "Regel ablehnen? Sie wirkt dann nicht."}, Handle: m.ruleStatus(ruleRejected)},
		},
	}
}

func (m *Module) nextRule(ctx context.Context, cc string) (int64, error) {
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(rule_no), 0) + 10 FROM bank__rule WHERE company_code = ?`, cc)
	if err != nil {
		return 0, err
	}
	return toInt(res.Rows[0][0]), nil
}

func (m *Module) checkRule(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		// von Hand angelegt: gilt sofort (gelernte Regeln entstehen als Vorschlag)
		rec["status"], rec["hits"] = ruleConfirmed, 0
		rec["confirmed_at"], rec["confirmed_by"] = now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	}
	defaults(rec, map[string]any{"direction": "ANY"})
	rec["account_id"] = nilIfEmpty(trimUpper(rec["account_id"]))
	rec["counterparty_name"] = nilIfEmpty(normName(crud.Str(rec["counterparty_name"])))
	rec["counterparty_iban"] = nilIfEmpty(strings.ReplaceAll(trimUpper(rec["counterparty_iban"]), " ", ""))
	rec["purpose_contains"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["purpose_contains"])))
	if v := strings.TrimSpace(crud.Str(rec["amount"])); v != "" {
		a, err := parseDecimal(v, ".", 2)
		if err != nil {
			if a, err = parseDecimal(v, ",", 2); err != nil {
				return crud.Invalid("Betrag: %v", err)
			}
		}
		rec["amount"] = a
	} else {
		rec["amount"] = nil
	}
	if rec["counterparty_name"] == nil && rec["counterparty_iban"] == nil && rec["purpose_contains"] == nil {
		return crud.Invalid("Regel braucht mindestens eine Bedingung: Name, IBAN oder Verwendungszweck")
	}
	t, err := m.resolve(ctx, cc, targetOf(rec))
	if err != nil {
		return err
	}
	if t.Type == tgInvoice {
		return crud.Invalid("Eingangsrechnungen werden je Zahlung zugeordnet, nicht per Regel")
	}
	rec["target_type"], rec["contract_id"], rec["partner_id"], rec["role_code"], rec["gl_account"] =
		t.Type, nilIfEmpty(t.Contract), nilIfEmpty(t.Partner), nilIfEmpty(t.Role), nilIfEmpty(t.Account)
	return nil
}

func (m *Module) ruleStatus(to string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in struct {
			ID string `json:"id"`
		}
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		key, err := m.set.Entity(ruleObject).ParseID(in.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		cc := crud.Str(key["company_code"])
		if err := requireWrite(ctx, ruleObject, map[string]string{ruleConfirmed: "confirm", ruleRejected: "reject"}[to], cc); err != nil {
			return sdk.Response{}, err
		}
		res, err := m.db.Exec(ctx, `UPDATE bank__rule SET status = ?, confirmed_at = ?, confirmed_by = ? WHERE company_code = ? AND rule_no = ?`,
			to, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), cc, toInt(key["rule_no"]))
		if err != nil {
			return sdk.Response{}, err
		}
		if res.RowsAffected == 0 {
			return sdk.Response{}, fmt.Errorf("%w: Regel %v", sdk.ErrNotFound, key["rule_no"])
		}
		msg := "Regel abgelehnt"
		if to == ruleConfirmed {
			msg = "Regel bestätigt – „Zuordnen“ am Bankkonto wendet sie auf offene Umsätze an"
		}
		return sdk.Response{Payload: map[string]any{"message": msg}}, nil
	}
}

// learnRule: Regelvorschlag aus einer Zuordnung von Hand (Gegenseite bzw.
// IBAN, Richtung, Bankkonto → Ziel). Rechnungen und Umsätze ohne Gegenseite
// lernen nichts; eine gleiche Regel gibt es nur einmal.
func (m *Module) learnRule(ctx context.Context, k txnKey, t target) (int64, error) {
	if t.Type == tgInvoice {
		return 0, nil
	}
	res, err := m.db.Query(ctx, `SELECT counterparty_name, counterparty_iban, amount FROM bank__transaction WHERE company_code = ? AND account_id = ?
		AND txn_no = ?`, k.CC, k.Account, k.No)
	if err != nil || len(res.Rows) == 0 {
		return 0, err
	}
	name, iban := normName(crud.Str(res.Rows[0][0])), crud.Str(res.Rows[0][1])
	if name == "" && iban == "" {
		return 0, nil
	}
	dir := "IN"
	if toInt(res.Rows[0][2]) < 0 {
		dir = "OUT"
	}
	dup, err := m.db.Query(ctx, `SELECT rule_no FROM bank__rule WHERE company_code = ? AND COALESCE(account_id, '') IN ('', ?) AND direction IN ('ANY', ?)
		AND COALESCE(counterparty_name, '') = ? AND COALESCE(counterparty_iban, '') = ? AND purpose_contains IS NULL AND status <> ?
		AND target_type = ? AND COALESCE(contract_id, '') = ? AND COALESCE(partner_id, '') = ? AND COALESCE(gl_account, '') = ?`,
		k.CC, k.Account, dir, name, iban, ruleRejected, t.Type, t.Contract, t.Partner, t.Account)
	if err != nil || len(dup.Rows) > 0 {
		return 0, err
	}
	no, err := m.nextRule(ctx, k.CC)
	if err != nil {
		return 0, err
	}
	_, err = m.db.Exec(ctx, `INSERT INTO bank__rule (company_code, rule_no, account_id, status, direction, counterparty_name, counterparty_iban,
		target_type, contract_id, partner_id, role_code, gl_account, learned_from, hits) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		k.CC, no, k.Account, ruleProposed, dir, nilIfEmpty(name), nilIfEmpty(iban), t.Type, nilIfEmpty(t.Contract), nilIfEmpty(t.Partner),
		nilIfEmpty(t.Role), nilIfEmpty(t.Account), fmt.Sprintf("%s/%d", k.Account, k.No))
	return no, err
}

// ruleDef ist eine bestätigte Regel.
type ruleDef struct {
	No                                  int64
	Account, Direction, Name, IBAN, Has string
	Amount                              *int64
	Target                              target
}

func (r ruleDef) conditions() int {
	n := 0
	for _, s := range []string{r.Account, r.Name, r.IBAN, r.Has} {
		if s != "" {
			n++
		}
	}
	if r.Amount != nil {
		n++
	}
	if r.Direction != "ANY" {
		n++
	}
	return n
}

func (r ruleDef) matches(t *txnRow) bool {
	switch {
	case r.Account != "" && r.Account != t.Account,
		r.Direction == "IN" && t.Amount < 0, r.Direction == "OUT" && t.Amount > 0,
		r.Name != "" && r.Name != normName(t.Counterparty),
		r.IBAN != "" && r.IBAN != t.IBAN,
		r.Has != "" && !strings.Contains(strings.ToLower(t.Purpose+" "+t.BookingText), strings.ToLower(r.Has)),
		r.Amount != nil && *r.Amount != t.Amount:
		return false
	}
	return true
}

func (m *Module) confirmedRules(ctx context.Context, cc string) ([]ruleDef, error) {
	res, err := m.db.Query(ctx, `SELECT rule_no, account_id, direction, counterparty_name, counterparty_iban, purpose_contains, amount, target_type,
		contract_id, partner_id, role_code, gl_account FROM bank__rule WHERE company_code = ? AND status = ? ORDER BY rule_no`, cc, ruleConfirmed)
	if err != nil {
		return nil, err
	}
	var out []ruleDef
	for _, r := range res.Rows {
		d := ruleDef{No: toInt(r[0]), Account: crud.Str(r[1]), Direction: crud.Str(r[2]), Name: crud.Str(r[3]), IBAN: crud.Str(r[4]), Has: crud.Str(r[5]),
			Target: target{Type: crud.Str(r[7]), Contract: crud.Str(r[8]), Partner: crud.Str(r[9]), Role: crud.Str(r[10]), Account: crud.Str(r[11])}}
		if r[6] != nil && crud.Str(r[6]) != "" {
			a := toInt(r[6])
			d.Amount = &a
		}
		out = append(out, d)
	}
	return out, nil
}

// normName: Name zum Vergleichen – klein, Umlaute ausgeschrieben, ohne Satzzeichen.
func normName(s string) string {
	s = strings.ToLower(s)
	s = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss").Replace(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
