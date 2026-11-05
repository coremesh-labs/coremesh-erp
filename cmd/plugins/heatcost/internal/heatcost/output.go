package heatcost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Ausgabe (seit 0.1.3) ------------------------------------------------------------
//
// „Einzelabrechnungen drucken …“: ein Druck für mehrere Nutzer (Vorlage mit der
// Verwendung HEATCOST_STATEMENT, Datenformat heatcost.statement/1, doppelseitig);
// wahlweise je Nutzer ein PDF im DMS. Eine Korrektur zeigt bisher/neu/Differenz
// je Position und die geänderten Kosten der Liegenschaft.
// „An Nebenkostenabrechnung übergeben …“: Ergebnis je Nutzer als Eingaben in den
// Lauf der Nebenkostenabrechnung (opcost) – Heizkosten ohne CO2-Gutschrift und
// den Vermieteranteil der CO2-Kosten als negativen Betrag.

const (
	usageStatement  = "HEATCOST_STATEMENT"
	statementFormat = "heatcost.statement/2" // seit 0.1.6 als Brief: letters = statements
)

// outputFields: Einstellungen der Einheit für Druck und Übergabe.
func outputFields() []crud.Field {
	g := "Druck und Nebenkosten"
	return []crud.Field{
		{Key: "template", Label: "Vorlage der Einzelabrechnung (Code|Sprache, Verwendung HEATCOST_STATEMENT; leer = erste der Verwendung)", Short: "Vorlage", Type: tText, Group: g},
		{Key: "doc_type", Label: "Dokumentart im DMS (z. B. STATEMENT; leer = nicht ablegen)", Short: "Dokumentart", Type: tText, Group: g},
		{Key: "nk_definition", Label: "Regelwerk der Nebenkostenabrechnung (Übergabe)", Short: "Regelwerk NK", Type: tText, Group: g,
			Lookup: &metamodel.Lookup{Object: "SettlementDefinition", ValueField: "code", LabelFields: []string{"name"}, Filters: ccFilter}},
		{Key: "nk_heat_account", Label: "Abrechnungskonto für die Heiz- und Wasserkosten (z. B. HEIZ)", Short: "Konto Heizkosten", Type: tText, Group: g},
		{Key: "sender_roles", Label: "Absender der Einzelabrechnung: Rollen am Mietobjekt (durch Komma, erste vorhandene; sonst Partner des Buchungskreises)", Short: "Rollen Absender", Type: tText, Group: g},
		{Key: "tenant_roles", Label: "Empfänger: Rollen der Mieter am Mietobjekt (durch Komma; ohne Mieter = Bezeichnung des Nutzers)", Short: "Rollen Mieter", Type: tText, Group: g},
		{Key: "nk_co2_account", Label: "Abrechnungskonto für den Vermieteranteil der CO2-Kosten (z. B. CO2)", Short: "Konto CO2", Type: tText, Group: g},
	}
}

var outputKeys = []string{"template", "doc_type", "nk_definition", "nk_heat_account", "nk_co2_account", "sender_roles", "tenant_roles"}

type runInfo struct {
	cc, id, unit, title, from, to, status, predecessor string
	unitName, externalNo, provider, partner            string
	template, docType, nkDef, nkHeat, nkCO2            string
	co2Mode, co2Scale, co2Stage                        string
	co2KgM2, co2Percent, co2Landlord, total            float64
	hasCO2                                             bool
}

func (m *Module) loadRunInfo(ctx context.Context, cc, id string) (*runInfo, error) {
	res, err := m.db.Query(ctx, `SELECT r.unit_id, COALESCE(r.title, ''), r.period_from, r.period_to, r.status, COALESCE(r.predecessor, ''),
		u.designation, COALESCE(u.external_no, ''), COALESCE(u.partner_id, ''), COALESCE(u.template, ''), COALESCE(u.doc_type, ''),
		COALESCE(u.nk_definition, ''), COALESCE(u.nk_heat_account, ''), COALESCE(u.nk_co2_account, ''),
		COALESCE(r.co2_mode, ''), COALESCE(r.co2_scale, ''), COALESCE(r.co2_stage, ''), r.co2_kg_m2, r.co2_percent, r.co2_landlord, r.total_cost
		FROM heatcost__run r JOIN heatcost__unit u ON u.company_code = r.company_code AND u.unit_id = r.unit_id
		WHERE r.company_code = ? AND r.run_id = ?`, cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Abrechnung %s|%s", sdk.ErrNotFound, cc, id)
	}
	x := res.Rows[0]
	s := func(i int) string { return crud.Str(x[i]) }
	r := &runInfo{cc: cc, id: id, unit: s(0), title: s(1), from: date(x[2]), to: date(x[3]), status: s(4), predecessor: s(5),
		unitName: s(6), externalNo: s(7), partner: s(8), template: s(9), docType: s(10), nkDef: s(11), nkHeat: s(12), nkCO2: s(13),
		co2Mode: s(14), co2Scale: s(15), co2Stage: s(16), co2KgM2: toFloat(x[17]), hasCO2: isSet(x[17]), co2Percent: toFloat(x[18]),
		co2Landlord: toFloat(x[19]), total: toFloat(x[20])}
	if r.status == runDraft {
		return nil, crud.Invalid("Abrechnung %s ist nach der letzten Änderung nicht gerechnet – zuerst „Rechnen“", id)
	}
	if r.partner != "" {
		r.provider = m.partnerName(ctx, r.partner)
	}
	return r, nil
}

type userResult struct {
	no, name, location, object, from, to string
	blocks                               []map[string]any
	texts                                map[string]float64 // Position → Betrag (für den Vergleich)
	order                                []string
	total, heat                          float64 // heat: umlagefähig, ohne CO2-Gutschrift
	other                                float64 // nicht umlagefähig (z. B. Nutzerwechselgebühr)
	co2                                  map[string]any
	landlord                             float64
	noInterim                            bool
}

// userResults: Ergebnis je Nutzer (users leer = alle mit Ergebnis).
func (m *Module) userResults(ctx context.Context, cc, id string, users []string) ([]*userResult, error) {
	keys := map[string]int{}
	if kr, err := m.db.Query(ctx, "SELECT code, decimals FROM heatcost__key WHERE company_code = ?", cc); err == nil {
		for _, k := range kr.Rows {
			keys[crud.Str(k[0])] = int(toInt(k[1]))
		}
	}
	res, err := m.db.Query(ctx, `SELECT r.user_no, COALESCE(u.designation, ''), COALESCE(u.location, ''), COALESCE(u.object_id, ''), u.valid_from, u.valid_to,
		r.block, r.text, r.price, r.value, COALESCE(r.unit, ''), r.share, r.amount, COALESCE(r.key_code, ''), COALESCE(r.apportionable, ?), r.total, r.basis, COALESCE(r.factor_text, ''), COALESCE(u.no_interim, ?)
		FROM heatcost__result r JOIN heatcost__run h ON h.company_code = r.company_code AND h.run_id = r.run_id
		LEFT JOIN heatcost__user u ON u.company_code = r.company_code AND u.unit_id = h.unit_id AND u.user_no = r.user_no
		WHERE r.company_code = ? AND r.run_id = ? ORDER BY r.user_no, r.line_no`, true, false, cc, id)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, u := range users {
		want[u] = true
	}
	var out []*userResult
	byNo := map[string]*userResult{}
	for _, r := range res.Rows {
		no := crud.Str(r[0])
		if len(want) > 0 && !want[no] {
			continue
		}
		u := byNo[no]
		if u == nil {
			u = &userResult{no: no, name: crud.Str(r[1]), location: crud.Str(r[2]), object: crud.Str(r[3]), from: date(r[4]), to: date(r[5]), texts: map[string]float64{}}
			if u.from == "1900-01-01" {
				u.from = ""
			}
			if u.to == "9999-12-31" {
				u.to = ""
			}
			byNo[no] = u
			out = append(out, u)
		}
		block, text, amount, key := crud.Str(r[6]), crud.Str(r[7]), toFloat(r[12]), crud.Str(r[13])
		if len(u.blocks) == 0 || u.blocks[len(u.blocks)-1]["name"] != block {
			u.blocks = append(u.blocks, map[string]any{"name": block, "sum": 0.0, "rows": []map[string]any{}})
		}
		b := u.blocks[len(u.blocks)-1]
		price, value := "", ""
		if key != "" {
			price = germanNumber(toFloat(r[8]), 6) + " €"
			value = strings.TrimSpace(germanNumber(toFloat(r[9]), keys[key]) + " " + crud.Str(r[10]))
		}
		label := strings.ReplaceAll(strings.TrimPrefix(text, block+" – "), ",00 %", " %")
		row := map[string]any{"text": text, "label": label, "price": price, "value": value,
			"share": germanNumber(toFloat(r[11]), 2) + " %", "amount": amount, "total": toFloat(r[15]), "factor_text": crud.Str(r[17]),
			"apportionable": crud.AsBool(r[14]), "has_key": key != ""}
		if key != "" {
			row["basis"], row["price_n"], row["value_n"], row["dec"], row["unit"] = toFloat(r[16]), toFloat(r[8]), toFloat(r[9]), keys[key], crud.Str(r[10])
			unit := crud.Str(r[10])
			row["basis_s"] = strings.TrimSpace(germanNumber(toFloat(r[16]), keys[key]) + " " + unit)
			row["value_s"] = strings.TrimSpace(germanNumber(toFloat(r[9]), keys[key]) + " " + unit)
			row["price_s"] = germanNumber(toFloat(r[8]), 6)
		}
		b["rows"] = append(b["rows"].([]map[string]any), row)
		u.noInterim = crud.AsBool(r[18])
		b["sum"] = round2(b["sum"].(float64) + amount)
		if _, ok := u.texts[text]; !ok {
			u.order = append(u.order, text)
		}
		u.texts[text] = round2(u.texts[text] + amount)
		u.total = round2(u.total + amount)
		switch {
		case block == blockCO2:
		case !crud.AsBool(r[14]):
			u.other = round2(u.other + amount)
		default:
			u.heat = round2(u.heat + amount)
		}
	}
	for _, w := range users {
		if byNo[w] == nil {
			return nil, crud.Invalid("Kein Ergebnis für Nutzer %q in %s", w, id)
		}
	}
	cr, err := m.db.Query(ctx, "SELECT user_no, share, emissions, cost, percent, landlord, tenant, deducted FROM heatcost__co2 WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return nil, err
	}
	for _, r := range cr.Rows {
		if u := byNo[crud.Str(r[0])]; u != nil {
			u.co2 = map[string]any{"share": toFloat(r[1]), "emissions": toFloat(r[2]), "cost": toFloat(r[3]), "percent": toFloat(r[4]),
				"landlord": toFloat(r[5]), "tenant": toFloat(r[6]), "deducted": crud.AsBool(r[7])}
			u.landlord = toFloat(r[5])
		}
	}
	return out, nil
}

// costChanges: geänderte Kosten der Liegenschaft gegenüber der Vorgängerabrechnung.
func (m *Module) costChanges(ctx context.Context, cc, id, pred string) ([]map[string]any, error) {
	load := func(run string) (map[int64][]any, []int64, error) {
		res, err := m.db.Query(ctx, "SELECT line_no, text, amount, COALESCE(note, '') FROM heatcost__cost WHERE company_code = ? AND run_id = ? ORDER BY sort_order, line_no", cc, run)
		if err != nil {
			return nil, nil, err
		}
		out, order := map[int64][]any{}, []int64{}
		for _, r := range res.Rows {
			out[toInt(r[0])] = r
			order = append(order, toInt(r[0]))
		}
		return out, order, nil
	}
	old, oldOrder, err := load(pred)
	if err != nil {
		return nil, err
	}
	cur, order, err := load(id)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, no := range order {
		c := cur[no]
		o, had := old[no]
		if had && toFloat(o[2]) == toFloat(c[2]) && crud.Str(o[1]) == crud.Str(c[1]) {
			continue
		}
		row := map[string]any{"text": crud.Str(c[1]), "note": crud.Str(c[3]), "has_new": true, "new": toFloat(c[2]), "has_old": had}
		if had {
			row["old"] = toFloat(o[2])
		}
		out = append(out, row)
	}
	for _, no := range oldOrder {
		if _, ok := cur[no]; !ok {
			o := old[no]
			out = append(out, map[string]any{"text": crud.Str(o[1]) + " (entfällt)", "note": crud.Str(o[3]), "has_old": true, "old": toFloat(o[2]), "has_new": false})
		}
	}
	return out, nil
}

func (m *Module) printFields() []crud.Field {
	return []crud.Field{
		{Key: "pr_users", Label: "Nutzer (Nutzernummern durch Komma; leer = alle)", Short: "Nutzer", Type: tText, ActionOnly: true},
		{Key: "pr_steps", Label: "Rechenweg der Liegenschaft als Anlage", Short: "Rechenweg", Type: tBool, ActionOnly: true},
		{Key: "pr_store", Label: "Je Nutzer im DMS ablegen (Dokumentart der Einheit)", Short: "im DMS ablegen", Type: tBool, ActionOnly: true},
	}
}

// printAction: HeatCostRun.print {id, data: {pr_users, pr_steps, pr_store}}.
func (m *Module) printAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if ok, err := sdk.CheckAccess(ctx, "HeatCostRun", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	r, err := m.loadRunInfo(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	store := crud.AsBool(in.Data["pr_store"])
	if store {
		if err := requireWrite(ctx, "HeatCostRun", "update", cc); err != nil {
			return sdk.Response{}, err
		}
		if r.docType == "" {
			return sdk.Response{}, crud.Invalid("Ablage im DMS: an der Abrechnungseinheit %s fehlt die Dokumentart (z. B. STATEMENT)", r.unit)
		}
	}
	users, err := m.userResults(ctx, cc, id, splitList(crud.Str(in.Data["pr_users"])))
	if err != nil {
		return sdk.Response{}, err
	}
	if len(users) == 0 {
		return sdk.Response{}, crud.Invalid("Abrechnung %s hat kein Ergebnis", id)
	}
	tmpl := r.template
	if tmpl == "" {
		if opts := reportapi.Templates(ctx, m.services, usageStatement); len(opts) > 0 {
			tmpl = opts[0].Value
		}
	}
	if tmpl == "" {
		return sdk.Response{}, crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → „Beispiele anlegen …“ legt HEATCOST_STATEMENT an)", usageStatement)
	}
	if !strings.Contains(tmpl, "|") {
		tmpl += "|de"
	}
	data, err := m.statementBase(ctx, r, crud.AsBool(in.Data["pr_steps"]))
	if err != nil {
		return sdk.Response{}, err
	}
	var prevUsers map[string]*userResult
	if r.predecessor != "" {
		if st, err := m.one(ctx, "SELECT status FROM heatcost__run WHERE company_code = ? AND run_id = ?", cc, r.predecessor); err != nil {
			return sdk.Response{}, err
		} else if st == runDraft {
			return sdk.Response{}, crud.Invalid("Vorgänger %s ist nicht gerechnet – dort zuerst „Rechnen“, damit bisher/neu verglichen werden kann", r.predecessor)
		}
		prev, err := m.userResults(ctx, cc, r.predecessor, nil)
		if err != nil {
			return sdk.Response{}, err
		}
		prevUsers = map[string]*userResult{}
		for _, u := range prev {
			prevUsers[u.no] = u
		}
	}
	lb, err := m.newLetterBuilder(ctx, r, data["company"].(map[string]any))
	if err != nil {
		return sdk.Response{}, err
	}
	if err := lb.base(ctx, data); err != nil {
		return sdk.Response{}, err
	}
	var statements []map[string]any
	for _, u := range users {
		st := statementOf(u, r.predecessor, prevUsers)
		if err := lb.letter(ctx, st, u); err != nil {
			return sdk.Response{}, err
		}
		statements = append(statements, st)
	}
	data["statements"] = statements
	data["letters"] = statements
	out, err := reportapi.Render(ctx, m.services, cc, tmpl, fmt.Sprintf("Einzelabrechnungen %s (%d Nutzer)", id, len(users)), data)
	if err != nil {
		return sdk.Response{}, err
	}
	if !store {
		return sdk.Response{Payload: out}, nil
	}
	var stored []string
	for i, u := range users {
		one := map[string]any{}
		for k, v := range data {
			one[k] = v
		}
		one["statements"] = []map[string]any{statements[i]}
		one["letters"] = one["statements"]
		res, err := reportapi.Render(ctx, m.services, cc, tmpl, "Einzelabrechnung "+id+" Nutzer "+u.no, one)
		if err != nil {
			return sdk.Response{}, err
		}
		file, ok := res["download"].(metamodel.Content)
		if !ok || file.Content == "" {
			return sdk.Response{}, crud.Invalid("Nutzer %s: PDF nicht fertig – %s", u.no, crud.Str(res["message"]))
		}
		refs := []any{map[string]any{"object": "HeatCostRun", "key": cc + "|" + id}}
		if u.object != "" {
			refs = append(refs, map[string]any{"object": "RentObject", "key": cc + "|" + u.object})
		}
		kind := "Einzelabrechnung"
		if r.predecessor != "" {
			kind = "Korrektur der Einzelabrechnung"
		}
		resp, err := m.services.Call(ctx, "DmsDocument", "store", map[string]any{"company_code": cc, "content": file.Content,
			"file_name": fmt.Sprintf("%s_Nutzer_%s.pdf", id, u.no), "content_type": "application/pdf", "doc_type": r.docType, "doc_date": time.Now().Format(time.DateOnly),
			"title":      fmt.Sprintf("%s Heizkosten %s–%s Nutzer %s %s (%s)", kind, germanDate(r.from), germanDate(r.to), u.no, u.name, id),
			"partner_id": nilIfEmpty(r.partner), "refs": refs})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Nutzer %s: Ablage im DMS: %v", u.no, err)
		}
		var d struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = sdk.Decode(resp.Payload, &d)
		stored = append(stored, firstNonEmpty(d.Name, d.ID))
	}
	out["message"] = crud.Str(out["message"]) + fmt.Sprintf("; %d Einzelabrechnungen im DMS abgelegt: %s", len(stored), strings.Join(stored, ", "))
	return sdk.Response{Payload: out}, nil
}

// statementBase: gemeinsame Daten aller Einzelabrechnungen eines Drucks.
func (m *Module) statementBase(ctx context.Context, r *runInfo, withSteps bool) (map[string]any, error) {
	run := map[string]any{"id": r.id, "title": r.title, "period_from": r.from, "period_to": r.to, "unit": r.unit, "unit_name": r.unitName,
		"external_no": r.externalNo, "provider": r.provider, "predecessor": r.predecessor, "total_cost": r.total}
	if r.predecessor != "" {
		t, err := m.one(ctx, "SELECT COALESCE(title, '') FROM heatcost__run WHERE company_code = ? AND run_id = ?", r.cc, r.predecessor)
		if err != nil {
			return nil, err
		}
		run["predecessor_title"] = t
	}
	if r.hasCO2 {
		scale, _ := m.one(ctx, "SELECT name FROM heatcost__co2_scale WHERE company_code = ? AND code = ?", r.cc, r.co2Scale)
		run["co2"] = map[string]any{"kg_m2": r.co2KgM2, "stage": r.co2Stage, "percent": r.co2Percent, "landlord": r.co2Landlord,
			"scale": firstNonEmpty(scale, r.co2Scale), "deducted": r.co2Mode == co2Deduct || r.co2Mode == co2Costs, "from_costs": r.co2Mode == co2Costs}
	}
	data := map[string]any{"format": statementFormat, "title": firstNonEmpty(r.title, r.id), "printed_on": time.Now().Format(time.DateOnly),
		"company": reportapi.Company(ctx, m.services, r.cc), "run": run, "with_steps": withSteps}
	if withSteps {
		res, err := m.db.Query(ctx, "SELECT block, text, amount, basis, COALESCE(unit, ''), price FROM heatcost__step WHERE company_code = ? AND run_id = ? ORDER BY step_no", r.cc, r.id)
		if err != nil {
			return nil, err
		}
		var steps []map[string]any
		for _, s := range res.Rows {
			steps = append(steps, map[string]any{"block": crud.Str(s[0]), "text": crud.Str(s[1]), "amount": toFloat(s[2]), "basis": s[3], "unit": crud.Str(s[4]), "price": s[5]})
		}
		data["steps"] = steps
	}
	if r.predecessor != "" {
		ch, err := m.costChanges(ctx, r.cc, r.id, r.predecessor)
		if err != nil {
			return nil, err
		}
		data["changes"] = ch
	}
	return data, nil
}

func statementOf(u *userResult, pred string, prev map[string]*userResult) map[string]any {
	s := map[string]any{"user_no": u.no, "designation": u.name, "location": u.location, "object_id": u.object,
		"valid_from": u.from, "valid_to": u.to, "blocks": u.blocks, "total": u.total}
	if u.co2 != nil {
		s["co2"] = u.co2
	}
	if pred == "" {
		return s
	}
	p := prev[u.no]
	if p == nil {
		p = &userResult{texts: map[string]float64{}}
	}
	var rows []map[string]any
	seen := map[string]bool{}
	add := func(text string) {
		if seen[text] {
			return
		}
		seen[text] = true
		o, n := p.texts[text], u.texts[text]
		if d := round2(n - o); d != 0 {
			rows = append(rows, map[string]any{"text": text, "old": o, "new": n, "diff": d})
		}
	}
	for _, t := range u.order {
		add(t)
	}
	for _, t := range p.order {
		add(t)
	}
	s["correction"] = map[string]any{"predecessor": pred, "previous_total": p.total, "difference": round2(u.total - p.total), "rows": rows}
	return s
}

// --- Übergabe an die Nebenkostenabrechnung --------------------------------------------

// transferAction: HeatCostRun.transfer {id} – je Nutzer mit Mietobjekt die
// Heizkosten (ohne CO2-Gutschrift) und den Vermieteranteil der CO2-Kosten
// (negativ) als Eingabe in den Lauf des Regelwerks der Einheit (Lauf ab
// Beginn des Abrechnungszeitraums). Vorhandene Eingaben gleicher Konten und
// Mietobjekte werden ersetzt.
func (m *Module) transferAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostRun", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	r, err := m.loadRunInfo(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if r.nkDef == "" || r.nkHeat == "" {
		return sdk.Response{}, crud.Invalid("Übergabe: an der Abrechnungseinheit %s Regelwerk der Nebenkostenabrechnung und Konto für die Heizkosten eintragen", r.unit)
	}
	users, err := m.userResults(ctx, cc, id, nil)
	if err != nil {
		return sdk.Response{}, err
	}
	tenants, err := m.list(ctx, "SettlementTenant", map[string]any{"company_code": cc, "definition": r.nkDef, "period_from": r.from})
	if err != nil {
		return sdk.Response{}, crud.Invalid("Nebenkostenabrechnung %s ab %s: %v", r.nkDef, germanDate(r.from), err)
	}
	var lines []string
	n := 0
	for _, u := range users {
		if u.object == "" {
			continue
		}
		contract := ""
		for _, t := range tenants {
			if crud.Str(t["rent_object_id"]) != u.object {
				continue
			}
			tf, tt := date(t["usage_from"]), date(t["usage_to"])
			if (u.to == "" || tf <= u.to) && (u.from == "" || tt == "" || tt >= u.from) {
				if contract != "" && contract != crud.Str(t["contract_id"]) {
					return sdk.Response{}, crud.Invalid("Nutzer %s: Mietobjekt %s hat im Zeitraum mehrere Mieter (%s, %s) – Nutzer je Mieter mit Nutzungszeitraum anlegen",
						u.no, u.object, contract, crud.Str(t["contract_id"]))
				}
				contract = crud.Str(t["contract_id"])
			}
		}
		if contract == "" {
			return sdk.Response{}, crud.Invalid("Nutzer %s: kein Mieter für Mietobjekt %s im Lauf %s ab %s – dort zuerst „Rechnen“", u.no, u.object, r.nkDef, germanDate(r.from))
		}
		src := fmt.Sprintf("Heizkostenabrechnung %s, Nutzer %s", id, u.no)
		if err := m.putInput(ctx, cc, r, r.nkHeat, contract, u.object, u.heat, "Heiz- und Wasserkosten Ihr Anteil, "+src); err != nil {
			return sdk.Response{}, err
		}
		line := fmt.Sprintf("%s %s %s €", u.object, r.nkHeat, money(u.heat))
		if u.other != 0 {
			line += fmt.Sprintf(" (ohne %s € nicht umlagefähig, z. B. Kosten des Nutzerwechsels – nicht übergeben)", money(u.other))
		}
		lines = append(lines, line)
		n++
		if u.co2 != nil && r.nkCO2 != "" {
			note := fmt.Sprintf("Vermieteranteil CO2 nach CO2KostAufG (%s, %s %%), %s", r.co2Stage, germanNumber(r.co2Percent, 2), src)
			if err := m.putInput(ctx, cc, r, r.nkCO2, contract, u.object, -u.landlord, note); err != nil {
				return sdk.Response{}, err
			}
			lines = append(lines, fmt.Sprintf("%s %s %s €", u.object, r.nkCO2, money(-u.landlord)))
		}
	}
	if n == 0 {
		return sdk.Response{}, crud.Invalid("Abrechnung %s: kein Nutzer mit Mietobjekt", id)
	}
	msg := fmt.Sprintf("An Nebenkostenabrechnung %s ab %s übergeben (%d Nutzer): %s – dort neu „Rechnen“", r.nkDef, germanDate(r.from), n, strings.Join(lines, "; "))
	return sdk.Response{Payload: map[string]any{"message": msg, "count": n}}, nil
}

// putInput: Eingabe des Kontos für Mietobjekt und Vertrag ersetzen oder anlegen.
func (m *Module) putInput(ctx context.Context, cc string, r *runInfo, account, contract, object string, amount float64, note string) error {
	items, err := m.list(ctx, "SettlementInput", map[string]any{"company_code": cc, "definition": r.nkDef, "period_from": r.from, "account": account, "rent_object_id": object})
	if err != nil {
		return crud.Invalid("Eingaben %s: %v", r.nkDef, err)
	}
	// Beim Nutzerwechsel hat ein Mietobjekt mehrere Verträge: nur die Eingaben dieses Vertrags ersetzen.
	mine := items[:0]
	for _, it := range items {
		if c := crud.Str(it["contract_id"]); c == "" || c == contract {
			mine = append(mine, it)
		}
	}
	items = mine
	data := map[string]any{"company_code": cc, "definition": r.nkDef, "period_from": r.from, "account": account, "contract_id": contract,
		"rent_object_id": object, "amount": fmt.Sprintf("%.2f", amount), "note": note}
	for i, it := range items {
		key := fmt.Sprintf("%s|%s|%s|%v", cc, r.nkDef, r.from, it["line_no"])
		if i == 0 {
			upd := map[string]any{"account": account, "contract_id": contract, "amount": data["amount"], "note": note}
			if _, err := m.services.Call(ctx, "SettlementInput", "update", map[string]any{"id": key, "data": upd}); err != nil {
				return crud.Invalid("Eingabe %s %s: %v", account, object, err)
			}
			continue
		}
		if _, err := m.services.Call(ctx, "SettlementInput", "remove", map[string]any{"id": key}); err != nil {
			return crud.Invalid("Eingabe %s %s entfernen: %v", account, object, err)
		}
	}
	if len(items) > 0 {
		return nil
	}
	if _, err := m.services.Call(ctx, "SettlementInput", "create", map[string]any{"data": data}); err != nil {
		return crud.Invalid("Eingabe %s %s: %v", account, object, err)
	}
	return nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// splitList: Nutzernummern durch Komma oder Leerzeichen.
func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		out = append(out, strings.TrimSpace(f))
	}
	return out
}
