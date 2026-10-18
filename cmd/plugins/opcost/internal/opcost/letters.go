package opcost

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Abrechnungsschreiben (seit 0.5.0): Aktion „Abrechnungsschreiben …“ am Lauf.
// Die Daten gehen im Format opcost.settlement/1 an eine Vorlage des Moduls
// template mit der Verwendung OPCOST_SETTLEMENT – ein Serienbrief, je Mieter
// (Vertrag) ein Brief. Die Daten enthalten keine festen Texte, nur Stammdaten
// (Kontobezeichnungen, Anreden) und Zahlen (Euro); Gestaltung und Formulierung
// liegen in der Vorlage. Aufbau:
//
//	format, company, date, due_date, files [{name, content}]
//	settlement: definition {code, name, mode}, period {from, to},
//	    unit {type, id, designation, street, zip, city},
//	    accounts {Konto: {code, name, group, sort, binding, input_mode, on_statement, heating, hhs}},
//	    keys {Schlüssel: {code, name, unit}}, rules [{step, no, kind, description, sources, target, target_credit}],
//	    pool_totals {Konto: Betrag}
//	letters[]: sender (Partner, signature = Datei aus files), recipient {persons, street, zip, city, country},
//	    contract {id, type, designation, usage {from, to}, objects [{id, designation, street, zip, city}]},
//	    result {costs, advances, balance,
//	        accounts {Konto: {code, name, level, amount, objects [{id, amount}]}},
//	        sums [{code, name, level, amount, parts [{code, name, amount, objects, details}]}],
//	        balances [{code, name, level, final, cost {code, name, amount}, advance {…}, amount, result {code, name}}],
//	        total (letzter Saldo bzw. Kosten, Vorauszahlungen, Saldo des Mieters)}
//
// details: Rechenweg je Konto aus dem Rechnen – total (Kosten des Topfs),
// key, weight, weight_total, use, use_total, use_key, factor, amount.

const (
	usageSettlement = "OPCOST_SETTLEMENT"
	letterFormat    = "opcost.settlement/1"
)

func euro(cents int64) float64 { return float64(cents) / 100 }

func euroOf(v any) float64 {
	switch n := v.(type) {
	case float64:
		return math.Round(n) / 100
	case json.Number:
		f, _ := n.Float64()
		return math.Round(f) / 100
	}
	return euro(toInt(v))
}

// lettersFormState: Vorlage (Verwendung OPCOST_SETTLEMENT; Vorschlag: Vorlage
// „Abrechnung an Mieter“ des Regelwerks), Datum heute.
func (m *Module) lettersFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" || req.Action != "letters" {
		return st, nil
	}
	tmpls := reportapi.Templates(ctx, m.services, usageSettlement)
	tf := metamodel.FieldState{Options: tmpls}
	if strings.TrimSpace(req.Values["letter_template"]) == "" && len(tmpls) > 0 {
		v := tmpls[0].Value
		if k, err := m.runKeyOf(map[string]any{"id": req.ID}); err == nil {
			if pref := m.text(ctx, `SELECT template FROM opcost__template WHERE company_code = ? AND definition = ? AND usage = 'TENANT_LETTER' AND is_active = ?
				ORDER BY code`, k.CC, k.Definition, true); pref != "" {
				for _, t := range tmpls {
					if t.Value == pref || strings.HasPrefix(t.Value, strings.TrimSpace(pref)+"|") {
						v = t.Value
						break
					}
				}
			}
		}
		tf.Value = &v
	}
	st.Fields["letter_template"] = tf
	if strings.TrimSpace(req.Values["letter_date"]) == "" {
		today := crud.KeyDate(ctx)
		st.Fields["letter_date"] = metamodel.FieldState{Value: &today}
	}
	if len(tmpls) == 0 {
		st.Message = "Keine Vorlage mit der Verwendung " + usageSettlement + " (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt SETTLEMENT_LETTER an)."
	} else {
		st.Message = "Je Mieter ein Schreiben in einem PDF; Absender ist der Eigentümer bzw. Verwalter des Mietobjekts (Rollen am Regelwerk)."
	}
	return st, nil
}

func (m *Module) lettersAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Template string `json:"letter_template"`
			Date     string `json:"letter_date"`
			Due      string `json:"letter_due"`
			Contract string `json:"letter_contract"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, runObject, "letters", k.CC); err != nil {
		return sdk.Response{}, err
	}
	dates := map[string]string{}
	for key, v := range map[string]string{"date": in.Data.Date, "due_date": in.Data.Due} {
		if strings.TrimSpace(v) == "" {
			continue
		}
		d, err := crud.ParseDate(v)
		if err != nil {
			return sdk.Response{}, crud.Invalid("%s: %v", key, err)
		}
		dates[key] = d
	}
	data, n, err := m.letterData(ctx, k, strings.TrimSpace(in.Data.Contract))
	if err != nil {
		return sdk.Response{}, err
	}
	for key, v := range dates {
		data[key] = v
	}
	out, err := reportapi.Render(ctx, m.services, k.CC, in.Data.Template, fmt.Sprintf("Abrechnungsschreiben %s (%d)", k.Definition, n), data)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

// letterData: Daten opcost.settlement/1 eines gerechneten Laufs (contract = nur dieser Vertrag).
func (m *Module) letterData(ctx context.Context, k runKey, contract string) (map[string]any, int, error) {
	def, err := m.set.Entity(definitionObject).Load(ctx, crud.Record{"company_code": k.CC, "code": k.Definition})
	if err != nil {
		return nil, 0, err
	}
	run, err := m.query(ctx, `SELECT period_to, pool_totals, computed_at FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`,
		[]string{"period_to", "pool_totals", "computed_at"}, k.CC, k.Definition, k.From)
	if err != nil {
		return nil, 0, err
	}
	if len(run) == 0 || crud.Str(run[0]["computed_at"]) == "" {
		return nil, 0, crud.Invalid("Lauf ist noch nicht gerechnet")
	}
	to, _ := crud.ParseDate(run[0]["period_to"])

	accounts := map[string]any{}
	accSort := map[string]int64{}
	accName := map[string]string{}
	rows, err := m.query(ctx, `SELECT code, name, display_group, sort_order, binding, input_mode, on_statement, heating, hhs FROM opcost__account
		WHERE company_code = ? AND definition = ? AND is_active = ?`, []string{"code", "name", "group", "sort", "binding", "input_mode", "on_statement", "heating", "hhs"},
		k.CC, k.Definition, true)
	if err != nil {
		return nil, 0, err
	}
	for _, a := range rows {
		c := crud.Str(a["code"])
		a["code"], a["name"], a["group"], a["sort"] = c, crud.Str(a["name"]), crud.Str(a["group"]), toInt(a["sort"])
		for _, f := range []string{"on_statement", "heating", "hhs"} {
			a[f] = crud.AsBool(a[f])
		}
		accounts[c], accSort[c], accName[c] = a, toInt(a["sort"]), crud.Str(a["name"])
	}
	keys, keyCode := map[string]any{}, map[string]string{}
	if rows, err = m.query(ctx, `SELECT code, name, unit FROM opcost__allocation_key WHERE company_code = ?`, []string{"code", "name", "unit"}, k.CC); err != nil {
		return nil, 0, err
	}
	for _, r := range rows {
		keys[crud.Str(r["code"])] = map[string]any{"code": crud.Str(r["code"]), "name": crud.Str(r["name"]), "unit": crud.Str(r["unit"])}
		keyCode[crud.Str(r["name"])] = crud.Str(r["code"])
	}
	rules, err := m.query(ctx, `SELECT step, rule_no, kind, description, source_value, pool, to_pool FROM opcost__rule WHERE company_code = ? AND definition = ?
		AND is_active = ? AND kind IN ('SUM', 'ADVANCE', 'BALANCE') ORDER BY step, rule_no`, []string{"step", "no", "kind", "description", "sources", "target", "target_credit"},
		k.CC, k.Definition, true)
	if err != nil {
		return nil, 0, err
	}
	var finalStep int64 = -1
	for _, r := range rules {
		r["step"], r["no"], r["kind"], r["description"] = toInt(r["step"]), toInt(r["no"]), crud.Str(r["kind"]), crud.Str(r["description"])
		r["sources"] = splitCodes(crud.Str(r["sources"]))
		r["target"], r["target_credit"] = crud.Str(r["target"]), crud.Str(r["target_credit"])
		if r["kind"] == "BALANCE" && toInt(r["step"]) > finalStep {
			finalStep = toInt(r["step"])
		}
	}
	pools := map[string]any{}
	if s := crud.Str(run[0]["pool_totals"]); s != "" {
		var p map[string]int64
		if json.Unmarshal([]byte(s), &p) == nil {
			for c, v := range p {
				pools[c] = euro(v)
			}
		}
	}

	q := `SELECT contract_id, partner_id, rent_object_id, usage_from, usage_to, costs, advances, balance, details FROM opcost__tenant
		WHERE company_code = ? AND definition = ? AND period_from = ?`
	args := []any{k.CC, k.Definition, k.From}
	if contract != "" {
		q += ` AND contract_id = ?`
		args = append(args, contract)
	}
	tenants, err := m.query(ctx, q+` ORDER BY contract_id`, []string{"contract", "partner", "object", "from", "to", "costs", "advances", "balance", "details"}, args...)
	if err != nil {
		return nil, 0, err
	}
	if len(tenants) == 0 {
		return nil, 0, crud.Invalid("Keine Mieter im Lauf%s", map[bool]string{true: " für Vertrag " + contract}[contract != ""])
	}
	results, err := m.query(ctx, `SELECT contract_id, rent_object_id, account, level, amount FROM opcost__result WHERE company_code = ? AND definition = ? AND period_from = ?
		ORDER BY line_no`, []string{"contract", "object", "account", "level", "amount"}, k.CC, k.Definition, k.From)
	if err != nil {
		return nil, 0, err
	}

	lb := &letterBuilder{m: m, cc: k.CC, date: to, roles: splitCodes(crud.Str(def["sender_roles"])), sigType: crud.Str(def["signature_doc_type"]),
		partners: map[string]map[string]any{}, files: map[string]reportapi.File{}, objects: map[string]map[string]any{}}
	unitType, unitID := crud.Str(def["unit_type"]), crud.Str(def["unit_id"])
	unit := lb.place(ctx, unitType, unitID)
	unit["type"] = unitType
	company := reportapi.Company(ctx, m.services, k.CC)

	var letters []any
	for _, t := range tenants {
		c := crud.Str(t["contract"])
		res := tenantResult(c, results, accName, accSort, keyCode, rules, finalStep, crud.Str(t["details"]))
		res["costs"], res["advances"], res["balance"] = euro(toInt(t["costs"])), euro(toInt(t["advances"])), euro(toInt(t["balance"]))
		if res["total"] == nil {
			res["total"] = map[string]any{"cost": map[string]any{"amount": res["costs"]}, "advance": map[string]any{"amount": res["advances"]}, "amount": res["balance"]}
		}
		from, _ := crud.ParseDate(t["from"])
		until, _ := crud.ParseDate(t["to"])
		object := crud.Str(t["object"])
		sender := lb.sender(ctx, object, unitID, company)
		letters = append(letters, map[string]any{
			"sender":    sender,
			"recipient": lb.recipient(ctx, c, crud.Str(t["partner"]), sender),
			"contract":  lb.contract(ctx, c, object, from, until),
			"result":    res,
		})
	}
	var files []any
	for _, f := range lb.files {
		files = append(files, map[string]any{"name": f.Name, "content": f.Content})
	}
	return map[string]any{
		"format":  letterFormat,
		"company": company,
		"files":   files,
		"settlement": map[string]any{
			"definition":  map[string]any{"code": k.Definition, "name": crud.Str(def["name"]), "mode": crud.Str(def["mode"])},
			"period":      map[string]any{"from": k.From, "to": to},
			"unit":        unit,
			"accounts":    accounts,
			"keys":        keys,
			"rules":       rules,
			"pool_totals": pools,
		},
		"letters": letters,
	}, len(letters), nil
}

func splitCodes(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, c)
		}
	}
	return out
}

// tenantResult: Ergebnis eines Vertrags – Konten, Summen mit Teilen und Rechenweg, Salden.
// keyCode: Name → Code der Verteilerschlüssel (der Rechenweg nennt den Namen).
func tenantResult(contract string, results []map[string]any, accName map[string]string, accSort map[string]int64, keyCode map[string]string, rules []map[string]any,
	finalStep int64, detailsJSON string) map[string]any {
	type acc struct {
		level   int64
		amount  int64
		objects []any
	}
	accs := map[string]*acc{}
	for _, r := range results {
		if crud.Str(r["contract"]) != contract {
			continue
		}
		c := crud.Str(r["account"])
		a := accs[c]
		if a == nil {
			a = &acc{level: toInt(r["level"])}
			accs[c] = a
		}
		a.amount += toInt(r["amount"])
		if o := crud.Str(r["object"]); o != "" {
			a.objects = append(a.objects, map[string]any{"id": o, "amount": euro(toInt(r["amount"]))})
		}
	}
	details := map[string][]any{}
	var all []any
	if detailsJSON != "" {
		var ds []map[string]any
		if json.Unmarshal([]byte(detailsJSON), &ds) == nil {
			for _, d := range ds {
				d["total"], d["amount"] = euroOf(d["total"]), euroOf(d["amount"])
				if c, ok := keyCode[crud.Str(d["key"])]; ok {
					d["key"] = c
				}
				d["object"] = d["rent_object_id"]
				delete(d, "rent_object_id")
				details[crud.Str(d["account"])] = append(details[crud.Str(d["account"])], d)
				all = append(all, d)
			}
		}
	}
	accounts := map[string]any{}
	entry := func(c string) map[string]any {
		a := accs[c]
		e := map[string]any{"code": c, "name": accName[c], "level": a.level, "amount": euro(a.amount), "objects": a.objects}
		if ds := details[c]; ds != nil {
			e["details"] = ds
		}
		return e
	}
	for c := range accs {
		accounts[c] = entry(c)
	}
	ref := func(c string) map[string]any {
		amount := 0.0
		if a := accs[c]; a != nil {
			amount = euro(a.amount)
		}
		return map[string]any{"code": c, "name": accName[c], "amount": amount}
	}
	var sums, balances []any
	var total any
	for _, r := range rules {
		target, sources := crud.Str(r["target"]), r["sources"].([]string)
		switch r["kind"] {
		case "SUM":
			a := accs[target]
			if a == nil {
				continue
			}
			var parts []string
			for _, s := range sources {
				if accs[s] != nil {
					parts = append(parts, s)
				}
			}
			sort.SliceStable(parts, func(i, j int) bool { return accSort[parts[i]] < accSort[parts[j]] })
			var ps []any
			for _, p := range parts {
				ps = append(ps, entry(p))
			}
			sums = append(sums, map[string]any{"code": target, "name": accName[target], "level": a.level, "amount": euro(a.amount), "parts": ps})
		case "BALANCE":
			if len(sources) < 2 || (accs[sources[0]] == nil && accs[sources[1]] == nil) {
				continue
			}
			cost, adv := ref(sources[0]), ref(sources[1])
			amount := math.Round((cost["amount"].(float64)-adv["amount"].(float64))*100) / 100
			res := target
			if amount < 0 && crud.Str(r["target_credit"]) != "" {
				res = crud.Str(r["target_credit"])
			}
			b := map[string]any{"code": res, "name": accName[res], "level": toInt(r["step"]), "final": toInt(r["step"]) == finalStep,
				"cost": cost, "advance": adv, "amount": amount, "result": map[string]any{"code": res, "name": accName[res]}}
			balances = append(balances, b)
			if b["final"] == true {
				total = b
			}
		}
	}
	return map[string]any{"accounts": accounts, "sums": sums, "balances": balances, "total": total, "details": all}
}

// letterBuilder: Absender, Empfänger und Objekte mit Zwischenspeicher je Lauf.
type letterBuilder struct {
	m        *Module
	cc, date string
	roles    []string
	sigType  string
	partners map[string]map[string]any
	files    map[string]reportapi.File
	objects  map[string]map[string]any
}

func (b *letterBuilder) partner(ctx context.Context, id string) map[string]any {
	if p, ok := b.partners[id]; ok {
		return p
	}
	p := reportapi.Partner(ctx, b.m.services, id)
	b.partners[id] = p
	return p
}

// sender: erster Partner des Mietobjekts (bzw. der Abrechnungseinheit) in den
// Rollen des Regelwerks, sonst der Partner des Buchungskreises; mit Unterschrift.
func (b *letterBuilder) sender(ctx context.Context, object, unit string, company map[string]any) map[string]any {
	id := reportapi.ObjectPartner(ctx, b.m.services, b.cc, object, b.date, b.roles)
	if id == "" {
		id = reportapi.ObjectPartner(ctx, b.m.services, b.cc, unit, b.date, b.roles)
	}
	if id == "" {
		if p, ok := company["partner"].(map[string]any); ok {
			id = crud.Str(p["id"])
		}
	}
	p := b.partner(ctx, id)
	if p == nil {
		return map[string]any{"name": crud.Str(company["name"])}
	}
	s := map[string]any{}
	for k, v := range p {
		s[k] = v
	}
	if f, ok := b.files["sig:"+id]; ok {
		s["signature"] = f.Name
	} else if f, ok := reportapi.Signature(ctx, b.m.services, b.cc, id, b.sigType, "signature-"+id); ok {
		b.files["sig:"+id] = f
		s["signature"] = f.Name
	}
	return s
}

// recipient: Partner des Vertrags mit derselben Rolle wie der Mieter, Anschrift des Mieters.
func (b *letterBuilder) recipient(ctx context.Context, contract, partner string, sender map[string]any) map[string]any {
	ids := []string{partner}
	resp, err := b.m.services.Call(ctx, "ContractPartner", "list", map[string]any{"query": map[string]any{"company_code": b.cc, "contract_id": contract}})
	if err == nil {
		var l struct {
			Items []struct {
				Role      string `json:"role_code"`
				Partner   string `json:"partner_id"`
				ValidFrom string `json:"valid_from"`
				ValidTo   string `json:"valid_to"`
			} `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) == nil {
			role := ""
			for _, it := range l.Items {
				if it.Partner == partner {
					role = it.Role
				}
			}
			for _, it := range l.Items {
				if it.Role == role && role != "" && it.Partner != partner && (it.ValidTo == "" || it.ValidTo >= b.date) {
					ids = append(ids, it.Partner)
				}
			}
		}
	}
	r := map[string]any{}
	var persons []any
	for _, id := range ids {
		p := b.partner(ctx, id)
		if p == nil {
			continue
		}
		persons = append(persons, p)
		if len(persons) == 1 {
			for _, f := range []string{"street", "zip", "city", "country"} {
				r[f] = p[f]
			}
		}
	}
	r["persons"] = persons
	if c := crud.Str(r["country"]); c == "" || c == crud.Str(sender["country"]) {
		delete(r, "country") // Inland: ohne Land
	}
	return r
}

// contract: Vertrag mit Nutzungszeitraum und Mietobjekten (Hauptobjekt zuerst).
func (b *letterBuilder) contract(ctx context.Context, id, object, from, to string) map[string]any {
	c := map[string]any{"id": id, "usage": map[string]any{"from": from, "to": to}}
	if resp, err := b.m.services.Call(ctx, "Contract", "get", map[string]any{"id": b.cc + "|" + id}); err == nil {
		var v struct {
			Type        string `json:"contract_type"`
			Designation string `json:"designation"`
		}
		if sdk.Decode(resp.Payload, &v) == nil {
			c["type"], c["designation"] = v.Type, v.Designation
		}
	}
	ids := []string{}
	if object != "" {
		ids = append(ids, object)
	}
	if resp, err := b.m.services.Call(ctx, "ContractObject", "list", map[string]any{"query": map[string]any{"company_code": b.cc, "contract_id": id}}); err == nil {
		var l struct {
			Items []struct {
				Type    string `json:"object_type"`
				Object  string `json:"object_id"`
				Main    any    `json:"is_main"`
				ValidTo string `json:"valid_to"`
			} `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) == nil {
			sort.SliceStable(l.Items, func(i, j int) bool { return crud.AsBool(l.Items[i].Main) && !crud.AsBool(l.Items[j].Main) })
			for _, it := range l.Items {
				if it.Type == "RentObject" && it.Object != object && (it.ValidTo == "" || it.ValidTo >= from) {
					ids = append(ids, it.Object)
				}
			}
		}
	}
	var objs []any
	for _, o := range ids {
		objs = append(objs, b.place(ctx, "RentObject", o))
	}
	c["objects"] = objs
	return c
}

// place: Objekt (Mietobjekt, Gebäude, Wirtschaftseinheit) mit Bezeichnung und
// Anschrift – beim Mietobjekt die des Gebäudes bzw. der Wirtschaftseinheit.
func (b *letterBuilder) place(ctx context.Context, typ, id string) map[string]any {
	key := typ + "|" + id
	if p, ok := b.objects[key]; ok {
		return copyMap(p)
	}
	p := map[string]any{"id": id}
	var v struct {
		Designation string `json:"designation"`
		Street      string `json:"street"`
		Zip         string `json:"zip"`
		City        string `json:"city"`
		Building    string `json:"building_id"`
		Entity      string `json:"entity_id"`
	}
	if resp, err := b.m.services.Call(ctx, typ, "get", map[string]any{"id": b.cc + "|" + id}); err == nil && sdk.Decode(resp.Payload, &v) == nil {
		p["designation"], p["street"], p["zip"], p["city"] = v.Designation, v.Street, v.Zip, v.City
		if typ == "RentObject" {
			up := map[string]any{}
			if v.Building != "" {
				up = b.place(ctx, "Building", v.Building)
			}
			if crud.Str(up["street"]) == "" && v.Entity != "" {
				up = b.place(ctx, "BusinessEntity", v.Entity)
			}
			for _, f := range []string{"street", "zip", "city"} {
				p[f] = up[f]
			}
		}
	}
	b.objects[key] = p
	return copyMap(p)
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
