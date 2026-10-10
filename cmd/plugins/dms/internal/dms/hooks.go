package dms

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Prüfungen über Hooks – nur in Buchungskreisen, in denen sie eingeschaltet
// sind (DmsCompany.check_postings bzw. check_contracts), und nur, wenn eine
// Prüfregel passt:
//
//   - ledger.posting (check): Belegart mit Prüfregel → am Beleg muss ein
//     abgelegtes Dokument hängen, dessen Ablage zur Regel passt. Bezug ist die
//     Vorerfassung selbst oder ein Datensatz, der auf sie zeigt
//     (Bezugsquellen: Eingangsrechnung, Sollstellung eines Vertrags). Beim
//     Simulieren ist es eine Warnung (die Sollstellung kennt die Vorerfassung
//     erst danach).
//   - contract.activate (check): Vertragsart mit Regel ACTIVATE → abgelegtes
//     Dokument am Vertrag.
//   - contract.condition (check): Regel CONDITION (z. B. Hausgeld HG am WEG-
//     Vertrag) → abgelegtes Dokument am Vertrag, datiert nach dem Beginn der
//     vorigen Zeitscheibe (also neu seit der letzten Anpassung).

const (
	hookPosting   = "ledger.posting"
	hookActivate  = "contract.activate"
	hookCondition = "contract.condition"
	cbPosting     = "DmsPostingCheck"
	cbContract    = "DmsContractCheck"
)

func (m *Module) registerHooks(r *module.Router) {
	hook.Handle(r, cbPosting, m.onPosting)
	hook.Handle(r, cbContract, m.onContract)
}

func (m *Module) subscribeHooks(ctx context.Context) {
	for _, s := range []hook.Subscription{
		{Hook: hookPosting, Phase: hook.PhaseCheck, Callback: cbPosting, Priority: 200,
			Description: "DMS: Buchung nur mit abgelegtem Dokument (Prüfregeln Buchung)"},
		{Hook: hookActivate, Phase: hook.PhaseCheck, Callback: cbContract, Priority: 200,
			Description: "DMS: Vertrag aktivieren nur mit Dokument (Prüfregeln Vertrag)"},
		{Hook: hookCondition, Phase: hook.PhaseCheck, Callback: cbContract, Priority: 200,
			Description: "DMS: Kondition (z. B. Hausgeld) nur mit neuem Dokument (Prüfregeln Vertrag)"},
	} {
		if err := hook.Subscribe(ctx, m.services, s); err != nil {
			m.log.WarnContext(ctx, "Hook nicht abonniert", "hook", s.Hook, "err", err.Error())
		}
	}
}

// docKey ist ein Bezug (Object, Schlüssel).
type docKey struct{ Object, Key string }

// filedFor: abgelegte Dokumente mit einem der Bezüge – Ablage und Dokumentdatum.
func (m *Module) filedFor(ctx context.Context, cc string, keys []docKey) ([][2]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var conds []string
	args := []any{cc, stFiled, true}
	for _, k := range keys {
		conds = append(conds, "(r.object = ? AND r.object_key = ?)")
		args = append(args, k.Object, k.Key)
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT d.file_account, d.doc_date FROM dms__document d JOIN dms__document_ref r ON r.document_id = d.id
		WHERE d.company_code = ? AND d.status = ? AND r.is_active = ? AND (`+strings.Join(conds, " OR ")+`)`, args...)
	if err != nil {
		return nil, err
	}
	out := make([][2]string, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, [2]string{crud.Str(r[0]), dateOf(r[1])})
	}
	return out, nil
}

// --- Buchung ---------------------------------------------------------------------------

func (m *Module) onPosting(ctx context.Context, req hook.Request) (hook.Response, error) {
	var in struct {
		Request struct {
			CompanyCode  string `json:"company_code"`
			DocumentType string `json:"document_type"`
			HeaderText   string `json:"header_text"`
		} `json:"request"`
		DraftID  string `json:"draft_id"`
		Simulate bool   `json:"simulate"`
	}
	if err := req.DecodeData(&in); err != nil {
		return hook.Response{}, err
	}
	cc, docType := in.Request.CompanyCode, strings.ToUpper(firstOf(in.Request.DocumentType, "SA"))
	cfg, err := m.companyConfig(ctx, cc)
	if err != nil || !cfg.CheckPostings {
		return hook.Reply(nil), nil
	}
	res, err := m.db.Query(ctx, `SELECT rule_no, file_accounts FROM dms__posting_rule WHERE company_code = ? AND is_active = ?
		AND (document_type = '' OR document_type = ?) ORDER BY CASE WHEN document_type = '' THEN 1 ELSE 0 END, rule_no`, cc, true, docType)
	if err != nil || len(res.Rows) == 0 {
		return hook.Reply(nil), err
	}
	prefixes := splitList(crud.Str(res.Rows[0][1]))
	block := func(text string) (hook.Response, error) {
		if in.Simulate {
			return hook.Reply(nil, hook.Warning("DMS-1", text)), nil
		}
		return hook.Reply(nil, hook.Error("DMS-1", text)), nil
	}
	if in.DraftID == "" {
		return hook.Reply(nil, hook.Warning("DMS-1", "Beleg ohne Vorerfassung – Dokument nicht prüfbar")), nil
	}
	keys, err := m.draftRefs(ctx, cc, in.DraftID)
	if err != nil {
		return hook.Response{}, err
	}
	docs, err := m.filedFor(ctx, cc, keys)
	if err != nil {
		return hook.Response{}, err
	}
	for _, d := range docs {
		if accountMatches(d[0], prefixes) {
			return hook.Reply(nil), nil
		}
	}
	where := "an der Vorerfassung"
	if len(keys) > 1 {
		var names []string
		for _, k := range keys[1:] {
			names = append(names, k.Object+" "+k.Key)
		}
		where += " bzw. an " + strings.Join(names, ", ")
	}
	text := fmt.Sprintf("Belegart %s: kein abgelegtes Dokument %s", docType, where)
	if len(prefixes) > 0 {
		text += " in Ablage " + strings.Join(prefixes, ", ")
	}
	if len(docs) > 0 {
		text += fmt.Sprintf(" (%d Dokumente in anderen Ablagen)", len(docs))
	}
	return block(text + " – Dokument ablegen und als Bezug zuordnen")
}

// draftRefs: Bezüge einer Vorerfassung – sie selbst und die Datensätze der Bezugsquellen.
func (m *Module) draftRefs(ctx context.Context, cc, draftID string) ([]docKey, error) {
	keys := []docKey{{Object: "JournalDraft", Key: draftID}}
	res, err := m.db.Query(ctx, `SELECT object, filter_field, ref_object, key_template FROM dms__posting_source WHERE is_active = ? ORDER BY code`, true)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		object, field, refObj, tmpl := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3])
		resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": map[string]any{field: draftID, "company_code": cc}})
		if err != nil {
			continue // Modul nicht da oder ohne Recht: Quelle entfällt
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &list) != nil {
			continue
		}
		for _, it := range list.Items {
			if crud.Str(it[field]) != draftID {
				continue
			}
			if k := fillTemplate(tmpl, it); k != "" {
				keys = append(keys, docKey{Object: refObj, Key: k})
			}
		}
	}
	return keys, nil
}

// fillTemplate: {feld} aus dem Datensatz; fehlt ein Feld, ist der Schlüssel leer.
func fillTemplate(tmpl string, rec map[string]any) string {
	missing := false
	out := placeholderRe.ReplaceAllStringFunc(tmpl, func(ph string) string {
		v := crud.Str(rec[ph[1:len(ph)-1]])
		if v == "" {
			missing = true
		}
		return v
	})
	if missing {
		return ""
	}
	return out
}

// --- Vertrag ---------------------------------------------------------------------------

func (m *Module) onContract(ctx context.Context, req hook.Request) (hook.Response, error) {
	var in struct {
		CompanyCode   string `json:"company_code"`
		ContractID    string `json:"contract_id"`
		ContractType  string `json:"contract_type"`
		ConditionType string `json:"condition_type"`
		ValidFrom     string `json:"valid_from"`
		PreviousFrom  string `json:"previous_valid_from"`
	}
	if err := req.DecodeData(&in); err != nil {
		return hook.Response{}, err
	}
	cfg, err := m.companyConfig(ctx, in.CompanyCode)
	if err != nil || !cfg.CheckContracts {
		return hook.Reply(nil), nil
	}
	event := "ACTIVATE"
	if req.Hook == hookCondition {
		event = "CONDITION"
	}
	res, err := m.db.Query(ctx, `SELECT rule_no, condition_types, file_accounts FROM dms__contract_rule WHERE company_code = ? AND is_active = ?
		AND event = ? AND (contract_type = '' OR contract_type = ?) ORDER BY CASE WHEN contract_type = '' THEN 1 ELSE 0 END, rule_no`,
		in.CompanyCode, true, event, in.ContractType)
	if err != nil {
		return hook.Response{}, err
	}
	var prefixes []string
	found := false
	for _, r := range res.Rows {
		types := splitList(crud.Str(r[1]))
		if event == "CONDITION" && len(types) > 0 && !contains(types, strings.ToUpper(in.ConditionType)) {
			continue
		}
		prefixes, found = splitList(crud.Str(r[2])), true
		break
	}
	if !found {
		return hook.Reply(nil), nil
	}
	key := in.CompanyCode + "|" + in.ContractID
	docs, err := m.filedFor(ctx, in.CompanyCode, []docKey{{Object: "Contract", Key: key}})
	if err != nil {
		return hook.Response{}, err
	}
	for _, d := range docs {
		if accountMatches(d[0], prefixes) && (event == "ACTIVATE" || in.PreviousFrom == "" || d[1] > in.PreviousFrom) {
			return hook.Reply(nil), nil
		}
	}
	var text string
	if event == "ACTIVATE" {
		text = fmt.Sprintf("Vertrag %s: kein abgelegtes Dokument am Vertrag", in.ContractID)
	} else {
		text = fmt.Sprintf("Kondition %s ab %s am Vertrag %s: kein abgelegtes Dokument", in.ConditionType, in.ValidFrom, in.ContractID)
		if in.PreviousFrom != "" {
			text += " mit Dokumentdatum nach " + in.PreviousFrom + " (Beginn der vorigen Zeitscheibe)"
		}
	}
	if len(prefixes) > 0 {
		text += " in Ablage " + strings.Join(prefixes, ", ")
	}
	return hook.Reply(nil, hook.Error("DMS-2", text+" – Dokument ablegen und den Vertrag als Bezug zuordnen")), nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
