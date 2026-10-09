package opcost

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Buchung einer freigegebenen Nebenkostenabrechnung – je Mieter ein Beleg in
// einer gemeinsamen Transaktion:
//
//	Soll  Vorauszahlungen (Konto des Regelwerks)   Vorauszahlungen des Mieters
//	Haben Erlöse abgerechnete Betriebskosten       Kosten des Mieters
//	Soll  Mieterkonto (Nachzahlung) bzw. Haben (Guthaben)
//
// Das Mieterkonto kommt aus den Buchungskreisdaten des Mieters in der Rolle
// seiner Vertragsart. Kontierungen nur, wo der Feldstatus sie zulässt.

func (m *Module) registerPosting(r *module.Router) {
	r.Object(serviceObject).Handle(events.CallbackAction, m.onDraftEvent)
}

func (m *Module) subscribePosting(ctx context.Context) {
	for _, a := range []string{"post", "deactivate"} {
		if err := events.Register(ctx, m.services, events.Subscription{Object: "JournalDraft", Action: a, CompanyCode: events.All,
			Callback: serviceObject}); err != nil {
			m.log.WarnContext(ctx, "Event nicht abonniert", "object", "JournalDraft", "action", a, "err", err.Error())
		}
	}
}

func (m *Module) onDraftEvent(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	ev, err := events.Decode(req.Payload)
	if err != nil || ev.Object != "JournalDraft" || ev.EntityID == "" {
		return sdk.Response{}, err
	}
	switch ev.Action {
	case "post":
		_, err = m.db.Exec(ctx, `UPDATE opcost__tenant SET status = 'POSTED', document_number = ? WHERE draft_id = ?`,
			nilIfEmpty(crud.Str(ev.Data["document_number"])), ev.EntityID)
	case "deactivate":
		_, err = m.db.Exec(ctx, `UPDATE opcost__tenant SET status = 'OPEN', draft_id = NULL WHERE draft_id = ? AND status = 'DRAFT'`, ev.EntityID)
	}
	return sdk.Response{}, err
}

type tenantRow struct {
	Contract, Partner, Object, From, To string
	Costs, Advances                     int64
}

// postAction: „Buchen …“ – nur freigegebene Läufe, je Mieter mit Kosten oder Vorauszahlungen.
func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, runObject, "post", k.CC); err != nil {
		return sdk.Response{}, err
	}
	status, to, err := m.runStatus(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != runReleased {
		return sdk.Response{}, crud.Invalid("Nur freigegebene Läufe werden gebucht (Status: %s)", status)
	}
	def, err := m.set.Entity(definitionObject).Load(ctx, crud.Record{"company_code": k.CC, "code": k.Definition})
	if err != nil {
		return sdk.Response{}, err
	}
	advAcc, revAcc := crud.Str(def["advance_account"]), crud.Str(def["revenue_account"])
	if advAcc == "" || revAcc == "" {
		return sdk.Response{}, crud.Invalid("Regelwerk %s: Konto der Vorauszahlungen und Erlöskonto sind Pflicht für die Buchung", k.Definition)
	}
	res, err := m.db.Query(ctx, `SELECT contract_id, partner_id, usage_from, usage_to, costs, advances, rent_object_id FROM opcost__tenant
		WHERE company_code = ? AND definition = ? AND period_from = ? AND status = 'OPEN' ORDER BY contract_id`, k.CC, k.Definition, k.From)
	if err != nil {
		return sdk.Response{}, err
	}
	var tenants []tenantRow
	for _, r := range res.Rows {
		f, _ := crud.ParseDate(r[2])
		t, _ := crud.ParseDate(r[3])
		tenants = append(tenants, tenantRow{Contract: crud.Str(r[0]), Partner: crud.Str(r[1]), From: f, To: t, Costs: toInt(r[4]), Advances: toInt(r[5]), Object: crud.Str(r[6])})
	}
	fields := &fieldStatus{m: m, cc: k.CC, groups: map[string]map[string]string{}}
	label := fmt.Sprintf("Nebenkosten %s–%s", germanDate(k.From), germanDate(to))
	auto := crud.AsBool(def["auto_post"])
	var posted, drafts int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range tenants {
			if t.Costs == 0 && t.Advances == 0 {
				continue
			}
			recon, err := m.tenantAccount(ctx, k.CC, t.Contract, t.Partner)
			if err != nil {
				return err
			}
			diff := t.Costs - t.Advances
			docType := crud.Str(def["document_type"])
			if diff < 0 {
				docType = crud.Str(def["credit_document_type"])
			}
			draft, err := m.services.Call(ctx, "JournalDraft", "create", map[string]any{"data": map[string]any{
				"company_code_id": k.CC, "document_type": docType, "posting_date": crud.KeyDate(ctx), "document_date": to,
				"currency": "EUR", "header_text": label + " " + t.Contract, "reference": t.Contract + "/" + strings.ReplaceAll(k.From, "-", "")}})
			if err != nil {
				return err
			}
			draftID := field(draft.Payload, "id")
			line := func(acc, side string, amount int64, partner bool) error {
				if amount == 0 {
					return nil
				}
				if amount < 0 {
					amount = -amount
					side = map[string]string{"S": "H", "H": "S"}[side]
				}
				data := map[string]any{"draft_id": draftID, "account_number": acc, "shkzg": side, "amount": centsText(amount), "item_text": label}
				allowed, err := fields.allowed(ctx, acc)
				if err != nil {
					return err
				}
				k := map[string]string{"rent_contract_id": t.Contract, "rent_object_id": t.Object}
				if partner {
					k["sd_customer_id"] = t.Partner
				}
				for f, v := range k {
					if allowed[f] && v != "" {
						data[f] = v
					}
				}
				_, err = m.services.Call(ctx, "JournalDraftItem", "create", map[string]any{"data": data})
				return err
			}
			if err := line(advAcc, "S", t.Advances, false); err != nil {
				return err
			}
			if err := line(revAcc, "H", t.Costs, false); err != nil {
				return err
			}
			if err := line(recon, "S", diff, true); err != nil {
				return err
			}
			if _, err := m.services.Call(ctx, "JournalDraft", "simulate", map[string]any{"id": draftID}); err != nil {
				return fmt.Errorf("Mieter %s: %w", t.Contract, err)
			}
			st, docNo := "DRAFT", ""
			if auto {
				r, err := m.services.Call(ctx, "JournalDraft", "post", map[string]any{"id": draftID})
				if err != nil {
					return err
				}
				st, docNo = "POSTED", field(r.Payload, "document_number")
				posted++
			} else {
				drafts++
			}
			if _, err := m.db.Exec(ctx, `UPDATE opcost__tenant SET status = ?, draft_id = ?, document_number = ? WHERE company_code = ? AND definition = ?
				AND period_from = ? AND contract_id = ?`, st, draftID, nilIfEmpty(docNo), k.CC, k.Definition, k.From, t.Contract); err != nil {
				return err
			}
		}
		_, err := m.db.Exec(ctx, `UPDATE opcost__run SET status = ? WHERE company_code = ? AND definition = ? AND period_from = ?`, runPosted, k.CC, k.Definition, k.From)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Abrechnung gebucht: %d Mieter gebucht, %d vorerfasst", posted, drafts)}}, nil
}

// tenantAccount: Abstimmkonto des Mieters in der Rolle seiner Vertragsart.
func (m *Module) tenantAccount(ctx context.Context, cc, contract, partner string) (string, error) {
	c, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + contract})
	if err != nil {
		return "", fmt.Errorf("Vertrag %s: %w", contract, err)
	}
	ct, err := m.services.Call(ctx, "ContractType", "get", map[string]any{"id": cc + "|" + field(c.Payload, "contract_type")})
	if err != nil {
		return "", fmt.Errorf("Vertragsart von %s: %w", contract, err)
	}
	role := field(ct.Payload, "main_role")
	resp, err := m.services.Call(ctx, "PartnerCompanyCode", "list", map[string]any{"query": map[string]any{"bp_id": partner, "company_code": cc, "role_code": role}})
	if err != nil {
		return "", err
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
		return "", crud.Invalid("Mieter %s (Vertrag %s): kein Partnerkonto in der Rolle %s oder Buchungssperre", partner, contract, role)
	}
	return out.Items[0].Account, nil
}

func centsText(c int64) string {
	sign := ""
	if c < 0 {
		sign, c = "-", -c
	}
	return fmt.Sprintf("%s%d.%02d", sign, c/100, c%100)
}

func germanDate(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[8:10] + "." + s[5:7] + "." + s[0:4]
}

func field(payload any, key string) string {
	var m map[string]any
	if sdk.Decode(payload, &m) == nil {
		return crud.Str(m[key])
	}
	return ""
}

// fieldStatus: Kontierungen je Konto, die der Feldstatus zulässt.
type fieldStatus struct {
	m      *Module
	cc     string
	groups map[string]map[string]string
}

func (f *fieldStatus) allowed(ctx context.Context, account string) (map[string]bool, error) {
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
	group := "STD"
	if len(acc.Items) > 0 && acc.Items[0].Group != "" {
		group = acc.Items[0].Group
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
