package bank

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Buchen in der Reihenfolge der Zahlungen (Buchungstag, Nr.): Je Umsatz eine
// eigene Transaktion – Vorerfassung, Positionen, Prüfen, Buchen, Vermerk am
// Umsatz. Der erste nicht zugeordnete Umsatz hält
// die Buchung an (außer „überspringen“ am Bankkonto); ignorierte zählen nicht.
//
//	Eingang:  Soll Bank            Haben Partnerkonto bzw. Sachkonto
//	Ausgang:  Soll Partnerkonto    Haben Bank
//
// Belegart: Debitorenzahlung (Mieter, Kunden – auch Rückzahlungen),
// Kreditorenzahlung (Lieferanten – auch Gutschriften), Sachkontenbeleg.
//
// Das Partnerkonto ist das Abstimmkonto des Partners in seiner Rolle (Vertrag:
// Rolle der Vertragsart; Rechnung: Lieferantenrolle der Rechnungsart).
// Kontierungen (Vertrag, Kunde bzw. Lieferant) nur, wo der Feldstatus sie zulässt.

type postRow struct {
	No, Amount                                   int64
	Date, ValueDate, Currency, Counterparty, E2E string
	Purpose, Status                              string
	Target                                       target
}

func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	a, _, err := m.accountOfRequest(ctx, req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, accountObject, "post", a.CC); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT txn_no, amount, booking_date, value_date, currency, counterparty_name, end_to_end_ref, purpose, status,
		target_type, contract_id, partner_id, role_code, invoice_id, gl_account FROM bank__transaction
		WHERE company_code = ? AND account_id = ? AND status IN (?, ?, ?) ORDER BY booking_date, txn_no`, a.CC, a.ID, stOpen, stProposed, stMatched)
	if err != nil {
		return sdk.Response{}, err
	}
	fields := &fieldStatus{m: m, cc: a.CC, groups: map[string]map[string]string{}}
	decimals := m.currencyDecimals(ctx, a.Currency)
	posted, skipped := 0, 0
	stop := ""
	for _, r := range res.Rows {
		p := postRow{No: toInt(r[0]), Amount: toInt(r[1]), Currency: crud.Str(r[4]), Counterparty: crud.Str(r[5]), E2E: crud.Str(r[6]),
			Purpose: crud.Str(r[7]), Status: crud.Str(r[8]),
			Target: target{Type: crud.Str(r[9]), Contract: crud.Str(r[10]), Partner: crud.Str(r[11]), Role: crud.Str(r[12]), Invoice: crud.Str(r[13]),
				Account: crud.Str(r[14])}}
		p.Date, _ = crud.ParseDate(r[2])
		p.ValueDate, _ = crud.ParseDate(r[3])
		if p.Status != stMatched {
			if a.OutOfOrder {
				skipped++
				continue
			}
			stop = fmt.Sprintf("angehalten bei Umsatz %d vom %s (%s) – zuerst zuordnen oder ignorieren", p.No, germanDate(p.Date), statusText(p.Status))
			break
		}
		if err := m.postOne(ctx, a, p, fields, decimals); err != nil {
			stop = fmt.Sprintf("angehalten bei Umsatz %d vom %s: %v", p.No, germanDate(p.Date), err)
			break
		}
		posted++
	}
	msg := fmt.Sprintf("%d Umsätze gebucht", posted)
	if skipped > 0 {
		msg += fmt.Sprintf(", %d nicht zugeordnete übersprungen", skipped)
	}
	if stop != "" {
		msg += "; " + stop
	}
	return sdk.Response{Payload: map[string]any{"posted": posted, "skipped": skipped, "message": msg}}, nil
}

func (m *Module) postOne(ctx context.Context, a *accountRow, p postRow, fields *fieldStatus, decimals int) error {
	t, err := m.resolve(ctx, a.CC, p.Target)
	if err != nil {
		return err
	}
	// Belegart nach Art des Partners (Debitor/Kreditor), nicht nach Richtung:
	// auch die Rückzahlung an einen Mieter ist eine Debitorenzahlung.
	counter, kont, docType := "", map[string]string{}, a.DocIn
	if t.Type == tgAccount {
		counter, docType = t.Account, a.DocGL
	} else {
		if counter, err = m.partnerAccount(ctx, a.CC, t.Partner, t.Role); err != nil {
			return err
		}
		debitor, err := m.isDebitorRole(ctx, t.Role)
		if err != nil {
			return err
		}
		if debitor {
			kont["sd_customer_id"] = t.Partner
		} else {
			kont["supplier_id"], docType = t.Partner, a.DocOut
		}
		if t.Contract != "" {
			kont["rent_contract_id"] = t.Contract
		}
	}
	amount := p.Amount
	bankSide, counterSide := "S", "H"
	if amount < 0 {
		amount, bankSide, counterSide = -amount, "H", "S"
	}
	text := strings.TrimSpace(p.Counterparty + " " + p.Purpose)
	if len([]rune(text)) > 50 {
		text = string([]rune(text)[:50])
	}
	ref := p.E2E
	if ref == "" || strings.EqualFold(ref, "NOTPROVIDED") {
		ref = fmt.Sprintf("%s/%d", a.ID, p.No)
	}
	docDate := p.ValueDate
	if docDate == "" {
		docDate = p.Date
	}
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		d, err := m.services.Call(ctx, "JournalDraft", "create", map[string]any{"data": map[string]any{
			"company_code_id": a.CC, "document_type": docType, "posting_date": p.Date, "document_date": docDate, "currency": p.Currency,
			"header_text": text, "reference": ref}})
		if err != nil {
			return err
		}
		draftID := field(d.Payload, "id")
		line := func(acc, side string, k map[string]string) error {
			data := map[string]any{"draft_id": draftID, "account_number": acc, "shkzg": side, "amount": formatAmount(amount, decimals), "item_text": text}
			allowed, err := fields.allowed(ctx, acc)
			if err != nil {
				return err
			}
			for f, v := range k {
				if allowed[f] && v != "" {
					data[f] = v
				}
			}
			_, err = m.services.Call(ctx, "JournalDraftItem", "create", map[string]any{"data": data})
			return err
		}
		if err := line(a.GLAccount, bankSide, nil); err != nil {
			return err
		}
		if err := line(counter, counterSide, kont); err != nil {
			return err
		}
		if _, err := m.services.Call(ctx, "JournalDraft", "simulate", map[string]any{"id": draftID}); err != nil {
			return err
		}
		res, err := m.services.Call(ctx, "JournalDraft", "post", map[string]any{"id": draftID})
		if err != nil {
			return err
		}
		docNo := field(res.Payload, "document_number")
		trace := fmt.Sprintf("%s %s %s / %s %s %s", bankSide, a.GLAccount, formatAmount(amount, decimals), counterSide, counter, formatAmount(amount, decimals))
		if who := targetText(t); who != "" {
			trace += " (" + who + ")"
		}
		trace = "Beleg " + docNo + " " + docType + ": " + trace
		_, err = m.db.Exec(ctx, `UPDATE bank__transaction SET status = ?, draft_id = ?, document_number = ?, posting_trace = ?, posted_at = ?,
			changed_at = ?, changed_by = ? WHERE company_code = ? AND account_id = ? AND txn_no = ?`, stPosted, draftID, nilIfEmpty(docNo), trace,
			time.Now().UTC().Format(time.RFC3339), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), a.CC, a.ID, p.No)
		return err
	})
}

func targetText(t target) string {
	var parts []string
	if t.Contract != "" {
		parts = append(parts, "Vertrag "+t.Contract)
	}
	if t.Invoice != "" {
		parts = append(parts, "Rechnung "+t.Invoice)
	}
	if t.Partner != "" {
		parts = append(parts, "Partner "+t.Partner+" "+t.Role)
	}
	return strings.Join(parts, ", ")
}

// partnerAccount: Abstimmkonto des Partners in der Rolle (Buchungskreisdaten).
func (m *Module) partnerAccount(ctx context.Context, cc, partner, role string) (string, error) {
	resp, err := m.services.Call(ctx, "PartnerCompanyCode", "list", map[string]any{"query": map[string]any{"bp_id": partner, "company_code": cc, "role_code": role}})
	if err != nil {
		return "", unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			Account string `json:"reconciliation_account"`
			Block   bool   `json:"posting_block"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	if len(out.Items) == 0 || out.Items[0].Account == "" || out.Items[0].Block {
		return "", crud.Invalid("Partner %s: kein Partnerkonto in der Rolle %s im Buchungskreis %s oder Buchungssperre", partner, role, cc)
	}
	return out.Items[0].Account, nil
}

func (m *Module) isDebitorRole(ctx context.Context, role string) (bool, error) {
	resp, err := m.services.Call(ctx, "PartnerRoleType", "get", map[string]any{"id": role})
	if err != nil {
		return false, crud.Invalid("Rolle %s: %v", role, err)
	}
	var rt struct {
		Debitor bool `json:"is_debitor"`
	}
	if err := sdk.Decode(resp.Payload, &rt); err != nil {
		return false, err
	}
	return rt.Debitor, nil
}

func germanDate(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[8:10] + "." + s[5:7] + "." + s[0:4]
}

// fieldStatus: Kontierungen je Konto, die der Feldstatus zulässt.
type fieldStatus struct {
	m      *Module
	cc     string
	groups map[string]map[string]string
	byAcc  map[string]string
}

func (f *fieldStatus) allowed(ctx context.Context, account string) (map[string]bool, error) {
	if f.byAcc == nil {
		f.byAcc = map[string]string{}
	}
	group, ok := f.byAcc[account]
	if !ok {
		resp, err := f.m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": f.cc, "account_number": account}})
		if err != nil {
			return nil, err
		}
		var acc struct {
			Items []struct {
				Group string `json:"field_status_group"`
			} `json:"items"`
		}
		_ = sdk.Decode(resp.Payload, &acc)
		group = "STD"
		if len(acc.Items) > 0 && acc.Items[0].Group != "" {
			group = acc.Items[0].Group
		}
		f.byAcc[account] = group
	}
	st, ok := f.groups[group]
	if !ok {
		resp, err := f.m.services.Call(ctx, "FieldStatus", "list", map[string]any{"query": map[string]any{"group_id": group}})
		if err != nil {
			return nil, err
		}
		var out struct {
			Items []struct {
				Field  string `json:"field_name"`
				Status string `json:"status"`
			} `json:"items"`
		}
		_ = sdk.Decode(resp.Payload, &out)
		st = map[string]string{}
		for _, it := range out.Items {
			st[it.Field] = it.Status
		}
		f.groups[group] = st
	}
	out := map[string]bool{}
	for k, v := range st {
		out[k] = v == "OPTIONAL" || v == "REQUIRED"
	}
	return out, nil
}
