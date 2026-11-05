package heatcost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// --- Einzelabrechnung als Brief (Datenformat heatcost.statement/2, seit 0.1.6) ----------
//
// Je Nutzer ein Brief (Kette heatcost-statement → letter → letter-class): Absender
// ist der Eigentümer des Mietobjekts (Rollen „Absender“ der Einheit, sonst Partner
// des Buchungskreises), Empfänger die Mieter im Nutzungszeitraum (Rollen „Mieter“,
// auch über Mieteinheiten), ohne Mieter die Bezeichnung des Nutzers an der Anschrift
// des Objekts (z. B. Leerstand). Dazu Abrechnungs- und Nutzungszeitraum mit Tagen und
// Gradtagszahlen, Kostenaufstellung, Ermittlung und Aufteilung des Warmwassers,
// Ablesewerte, Verbrauchsvergleich (§ 6a HeizkostenV) und die Gradtagszahlentabelle.

// letterBuilder: Angaben, die für alle Briefe eines Drucks gleich sind.
type letterBuilder struct {
	m                        *Module
	r                        *runInfo
	senderRoles, tenantRoles []string
	entity, building         string
	heatChange, degTable     string
	deg                      degreeTable
	heatArea, heatUsage      string
	wwArea, wwUsage          string
	keys                     map[string][3]string // Code → Name, Einheit, Stellen
	basis                    map[string]float64   // Schlüssel (ganze Liegenschaft) → Verteilungsgröße
	partners                 map[string]map[string]any
	places                   map[string]map[string]any
	prev                     map[string]map[string]float64 // Vorjahr: Mietobjekt → Schlüssel → Wert
	prevPeriod               [2]string
	company                  map[string]any
}

func (m *Module) newLetterBuilder(ctx context.Context, r *runInfo, company map[string]any) (*letterBuilder, error) {
	b := &letterBuilder{m: m, r: r, company: company, keys: map[string][3]string{}, basis: map[string]float64{},
		partners: map[string]map[string]any{}, places: map[string]map[string]any{}}
	res, err := m.db.Query(ctx, `SELECT COALESCE(u.sender_roles, ''), COALESCE(u.tenant_roles, ''), COALESCE(u.entity_id, ''), COALESCE(u.building_id, ''),
		COALESCE(r.heat_change, ''), COALESCE(r.degree_table, ''), COALESCE(r.heat_area_key, ''), COALESCE(r.heat_usage_key, ''),
		COALESCE(r.ww_area_key, ''), COALESCE(r.ww_usage_key, '')
		FROM heatcost__run r JOIN heatcost__unit u ON u.company_code = r.company_code AND u.unit_id = r.unit_id
		WHERE r.company_code = ? AND r.run_id = ?`, r.cc, r.id)
	if err != nil {
		return nil, err
	}
	x := res.Rows[0]
	s := func(i int) string { return crud.Str(x[i]) }
	b.senderRoles, b.tenantRoles = splitCodes(s(0)), splitCodes(s(1))
	b.entity, b.building, b.heatChange, b.degTable = s(2), s(3), s(4), s(5)
	b.heatArea, b.heatUsage, b.wwArea, b.wwUsage = s(6), s(7), s(8), s(9)
	if b.degTable != "" {
		if b.deg, err = m.loadDegree(ctx, r.cc, b.degTable); err != nil {
			return nil, err
		}
	}
	kr, err := m.db.Query(ctx, "SELECT code, name, COALESCE(unit, ''), decimals FROM heatcost__key WHERE company_code = ?", r.cc)
	if err != nil {
		return nil, err
	}
	for _, k := range kr.Rows {
		b.keys[crud.Str(k[0])] = [3]string{crud.Str(k[1]), crud.Str(k[2]), fmt.Sprint(toInt(k[3]))}
	}
	// Verteilungsgrößen der ganzen Liegenschaft: Eintrag, sonst Summe der Nutzer
	br, err := m.db.Query(ctx, "SELECT key_code, total FROM heatcost__basis WHERE company_code = ? AND run_id = ? AND (scope IS NULL OR scope = '' OR scope = '*')", r.cc, r.id)
	if err != nil {
		return nil, err
	}
	for _, v := range br.Rows {
		b.basis[crud.Str(v[0])] = toFloat(v[1])
	}
	vr, err := m.db.Query(ctx, "SELECT key_code, SUM(value) FROM heatcost__value WHERE company_code = ? AND run_id = ? GROUP BY key_code", r.cc, r.id)
	if err != nil {
		return nil, err
	}
	for _, v := range vr.Rows {
		if _, ok := b.basis[crud.Str(v[0])]; !ok {
			b.basis[crud.Str(v[0])] = toFloat(v[1])
		}
	}
	// Vorjahr: letzte gerechnete Abrechnung derselben Einheit vor diesem Zeitraum
	pr, err := m.db.Query(ctx, `SELECT run_id, period_from, period_to FROM heatcost__run WHERE company_code = ? AND unit_id = ? AND period_to < ? AND status <> ?
		ORDER BY period_to DESC, run_id DESC LIMIT 1`, r.cc, r.unit, r.from, runDraft)
	if err != nil {
		return nil, err
	}
	if len(pr.Rows) > 0 {
		prev := crud.Str(pr.Rows[0][0])
		b.prevPeriod = [2]string{date(pr.Rows[0][1]), date(pr.Rows[0][2])}
		b.prev = map[string]map[string]float64{}
		pv, err := m.db.Query(ctx, `SELECT u.object_id, v.key_code, SUM(v.value) FROM heatcost__value v JOIN heatcost__run h ON h.company_code = v.company_code AND h.run_id = v.run_id
			JOIN heatcost__user u ON u.company_code = v.company_code AND u.unit_id = h.unit_id AND u.user_no = v.user_no
			WHERE v.company_code = ? AND v.run_id = ? AND u.object_id IS NOT NULL GROUP BY u.object_id, v.key_code`, r.cc, prev)
		if err != nil {
			return nil, err
		}
		for _, v := range pv.Rows {
			o := crud.Str(v[0])
			if b.prev[o] == nil {
				b.prev[o] = map[string]float64{}
			}
			b.prev[o][crud.Str(v[1])] = toFloat(v[2])
		}
	}
	return b, nil
}

func (b *letterBuilder) partner(ctx context.Context, id string) map[string]any {
	if id == "" {
		return nil
	}
	if p, ok := b.partners[id]; ok {
		return p
	}
	p := reportapi.Partner(ctx, b.m.services, id)
	b.partners[id] = p
	return p
}

// place: Objekt mit Bezeichnung und Anschrift (beim Mietobjekt die des Gebäudes bzw. der Wirtschaftseinheit).
func (b *letterBuilder) place(ctx context.Context, typ, id string) map[string]any {
	key := typ + "|" + id
	if p, ok := b.places[key]; ok {
		return p
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
	if id != "" {
		if resp, err := b.m.services.Call(ctx, typ, "get", map[string]any{"id": b.r.cc + "|" + id}); err == nil && sdk.Decode(resp.Payload, &v) == nil {
			p["designation"], p["street"], p["zip"], p["city"] = v.Designation, v.Street, v.Zip, v.City
			if v.Street == "" && typ == "RentObject" {
				up := b.place(ctx, "Building", firstNonEmpty(v.Building, b.building))
				if crud.Str(up["street"]) == "" {
					up = b.place(ctx, "BusinessEntity", firstNonEmpty(v.Entity, b.entity))
				}
				for _, f := range []string{"street", "zip", "city"} {
					p[f] = up[f]
				}
			}
		}
	}
	b.places[key] = p
	return p
}

type objPartner struct {
	Role     string `json:"role_code"`
	Partner  string `json:"partner_id"`
	Contract string `json:"contract_id"`
	From     string `json:"valid_from"`
	To       string `json:"valid_to"`
}

// objectPartners: wirksame Partner eines Objekts am Stichtag (realestate, mit Vererbung und Mieteinheiten).
func (b *letterBuilder) objectPartners(ctx context.Context, object, day string) []objPartner {
	if object == "" {
		return nil
	}
	resp, err := b.m.services.Call(ctx, "RealEstateSetup", "partners", map[string]any{"company": b.r.cc, "object": object, "date": day})
	if err != nil {
		return nil
	}
	var out struct {
		Partners []objPartner `json:"partners"`
	}
	if sdk.Decode(resp.Payload, &out) != nil {
		return nil
	}
	return out.Partners
}

func hasRole(roles []string, role string) bool {
	for _, r := range roles {
		if strings.EqualFold(r, role) {
			return true
		}
	}
	return false
}

// sender: Eigentümer bzw. Verwalter des Mietobjekts (erste vorhandene Rolle), sonst Partner des Buchungskreises.
func (b *letterBuilder) sender(ctx context.Context, object, day string) map[string]any {
	id := ""
	for _, obj := range []string{object, b.building, b.entity} {
		ps := b.objectPartners(ctx, obj, day)
		for _, role := range b.senderRoles {
			for _, p := range ps {
				if id == "" && strings.EqualFold(p.Role, role) {
					id = p.Partner
				}
			}
		}
		if id != "" {
			break
		}
	}
	if id == "" {
		if p, ok := b.company["partner"].(map[string]any); ok {
			id = crud.Str(p["id"])
		}
	}
	if p := b.partner(ctx, id); p != nil {
		return p
	}
	return map[string]any{"name": crud.Str(b.company["name"])}
}

// recipient: Mieter des Mietobjekts, deren Vertrag den Nutzungszeitraum berührt.
func (b *letterBuilder) recipient(ctx context.Context, u *userResult, from, to string) (map[string]any, []string, []string) {
	var persons []any
	var names, contracts []string
	seen := map[string]bool{}
	for _, p := range b.objectPartners(ctx, u.object, to) {
		if !hasRole(b.tenantRoles, p.Role) || seen[p.Partner] {
			continue
		}
		if (p.From != "" && p.From > to) || (p.To != "" && p.To < from) {
			continue
		}
		pp := b.partner(ctx, p.Partner)
		if pp == nil {
			continue
		}
		seen[p.Partner] = true
		persons = append(persons, pp)
		names = append(names, crud.Str(pp["name"]))
		if p.Contract != "" && !contains(contracts, p.Contract) {
			contracts = append(contracts, p.Contract)
		}
	}
	obj := b.place(ctx, "RentObject", u.object)
	r := map[string]any{}
	if len(persons) == 0 {
		r["name"] = firstNonEmpty(u.name, "Nutzer "+u.no)
		for _, f := range []string{"street", "zip", "city"} {
			r[f] = obj[f]
		}
		return r, []string{crud.Str(r["name"])}, nil
	}
	first := persons[0].(map[string]any)
	for _, f := range []string{"street", "zip", "city", "country"} {
		r[f] = first[f]
	}
	if crud.Str(r["street"]) == "" {
		for _, f := range []string{"street", "zip", "city"} {
			r[f] = obj[f]
		}
	}
	r["persons"] = persons
	if c := crud.Str(r["country"]); c == "" || strings.EqualFold(c, "DE") || strings.EqualFold(c, "Deutschland") {
		delete(r, "country")
	}
	return r, names, contracts
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// period: Tage und Gradtagszahlen eines Zeitraums innerhalb der Abrechnung.
func (b *letterBuilder) period(from, to string) map[string]any {
	p := map[string]any{"from": from, "to": to, "days": int(dayCount(from, to))}
	if b.deg != nil {
		if v, err := b.deg.weightOf(b.degTable, from, to); err == nil {
			p["degree"] = v
		}
	}
	return p
}

// base: Angaben der Liegenschaft für alle Briefe.
func (b *letterBuilder) base(ctx context.Context, data map[string]any) error {
	r, m := b.r, b.m
	res, err := m.db.Query(ctx, `SELECT l.text, l.amount, COALESCE(l.note, ''), COALESCE(l.invoice_ref, ''), l.allocation, COALESCE(l.cost_group, ''),
		COALESCE(g.name, ''), COALESCE(g.sort_order, 0), COALESCE(k.co2, ?), COALESCE(k.apportionable, ?)
		FROM heatcost__cost l LEFT JOIN heatcost__cost_group g ON g.company_code = l.company_code AND g.code = l.cost_group
		LEFT JOIN heatcost__cost_kind k ON k.company_code = l.company_code AND k.code = l.cost_kind
		WHERE l.company_code = ? AND l.run_id = ? ORDER BY l.sort_order, l.line_no`, false, true, r.cc, r.id)
	if err != nil {
		return err
	}
	type grp struct {
		code, name string
		sort       int64
		plant      bool
		rows       []map[string]any
		sum        float64
	}
	var groups []*grp
	byCode := map[string]*grp{}
	co2Done := false
	var fuel []string
	for _, x := range res.Rows {
		code := crud.Str(x[5])
		g := byCode[code]
		if g == nil {
			g = &grp{code: code, name: firstNonEmpty(crud.Str(x[6]), "Weitere Kosten"), sort: toInt(x[7])}
			byCode[code] = g
			groups = append(groups, g)
		}
		alloc := crud.Str(x[4])
		if alloc == allocSplit || alloc == allocHeating || alloc == allocHotwater {
			g.plant = true
		}
		row := map[string]any{"text": crud.Str(x[0]), "amount": toFloat(x[1]), "note": crud.Str(x[2]), "invoice_ref": crud.Str(x[3]),
			"allocation": alloc, "apportionable": crud.AsBool(x[9])}
		g.rows = append(g.rows, row)
		g.sum = round2(g.sum + toFloat(x[1]))
		if crud.AsBool(x[8]) {
			fuel = append(fuel, crud.Str(x[0]))
			if !co2Done && r.co2Mode == co2Costs && r.hasCO2 && r.co2Landlord != 0 {
				co2Done = true
				g.rows = append(g.rows, map[string]any{"text": fmt.Sprintf("Abzug CO2-Kostenanteil Vermieter (%s %%)", germanNumber(r.co2Percent, 2)),
					"amount": -r.co2Landlord, "allocation": alloc, "apportionable": true})
				g.sum = round2(g.sum - r.co2Landlord)
			}
		}
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].sort < groups[j].sort })
	// Warmwasser je Kostengruppe aus dem Rechenweg
	wwOf := map[string]float64{}
	wwText := ""
	st, err := m.db.Query(ctx, "SELECT text, amount FROM heatcost__step WHERE company_code = ? AND run_id = ? AND block = ? ORDER BY step_no", r.cc, r.id, blockWW)
	if err != nil {
		return err
	}
	for _, x := range st.Rows {
		t := crud.Str(x[0])
		if strings.HasPrefix(t, "Warmwasser aus ") {
			wwOf[strings.TrimPrefix(t, "Warmwasser aus ")] = toFloat(x[1])
		} else if strings.HasPrefix(t, "Warmwasseranteil ") {
			wwText = t
		}
	}
	hb, wb, err := b.basePercents(ctx)
	if err != nil {
		return err
	}
	var costs, split []map[string]any
	plant, other := 0.0, 0.0
	sumSplit := map[string]float64{}
	for _, g := range groups {
		costs = append(costs, map[string]any{"name": g.name, "rows": g.rows, "sum": g.sum, "plant": g.plant})
		if !g.plant {
			other = round2(other + g.sum)
			continue
		}
		plant = round2(plant + g.sum)
		ww := wwOf[g.name]
		heat := round2(g.sum - ww)
		row := map[string]any{"name": g.name, "total": g.sum, "heat": heat, "heat_base": round2(heat * hb / 100), "heat_usage": round2(heat - round2(heat*hb/100)),
			"ww": ww, "ww_base": round2(ww * wb / 100), "ww_usage": round2(ww - round2(ww*wb/100))}
		for _, k := range []string{"total", "heat", "heat_base", "heat_usage", "ww", "ww_base", "ww_usage"} {
			sumSplit[k] = round2(sumSplit[k] + toFloat(row[k]))
		}
		split = append(split, row)
	}
	data["costs"] = costs
	data["split"] = split
	data["split_sum"] = sumSplit
	run := data["run"].(map[string]any)
	extra, err := m.db.Query(ctx, `SELECT heat_cost, ww_cost, ww_share, COALESCE(ww_method, ''), ww_energy, total_energy, co2_cost, co2_kg FROM heatcost__run
		WHERE company_code = ? AND run_id = ?`, r.cc, r.id)
	if err != nil {
		return err
	}
	e := extra.Rows[0]
	run["heat_cost"], run["ww_cost"], run["ww_share"], run["ww_method"] = toFloat(e[0]), toFloat(e[1]), toFloat(e[2]), crud.Str(e[3])
	run["ww_energy"], run["total_energy"], run["has_energy"] = toFloat(e[4]), toFloat(e[5]), isSet(e[5])
	run["plant_cost"], run["other_cost"], run["ww_text"] = plant, other, wwText
	run["heat_base_percent"], run["ww_base_percent"] = hb, wb
	run["fuel"] = strings.Join(fuel, ", ")
	run["days"] = int(dayCount(r.from, r.to))
	if b.deg != nil {
		if v, err := b.deg.weightOf(b.degTable, r.from, r.to); err == nil {
			run["degree"] = v
		}
	}
	run["heat_change"] = b.heatChange
	if c, ok := run["co2"].(map[string]any); ok {
		c["cost"], c["kg"] = toFloat(e[6]), toFloat(e[7])
		c["tenant_all"] = round2(toFloat(e[6]) - r.co2Landlord)
	}
	// Gradtagszahlen der Monate im Abrechnungszeitraum
	if b.deg != nil {
		var months []map[string]any
		sum := 0.0
		for d := parseDay(r.from); !d.After(parseDay(r.to)); d = d.AddDate(0, 1, 0) {
			d = time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
			v, ok := b.deg[[2]int{d.Year(), int(d.Month())}]
			if !ok {
				continue
			}
			n := daysIn(d.Year(), d.Month())
			months = append(months, map[string]any{"year": d.Year(), "month": monthNames[d.Month()], "days": n, "promille": v, "per_day": v / float64(n)})
			sum += v
		}
		name, _ := m.one(ctx, "SELECT name FROM heatcost__degree_table WHERE company_code = ? AND code = ?", r.cc, b.degTable)
		data["degree"] = map[string]any{"table": b.degTable, "name": firstNonEmpty(name, b.degTable), "months": months, "sum": sum,
			"by_degree": b.heatChange == changeDegree}
	}
	return nil
}

func (b *letterBuilder) basePercents(ctx context.Context) (float64, float64, error) {
	res, err := b.m.db.Query(ctx, "SELECT heat_base_percent, ww_base_percent FROM heatcost__run WHERE company_code = ? AND run_id = ?", b.r.cc, b.r.id)
	if err != nil {
		return 0, 0, err
	}
	return toFloat(res.Rows[0][0]), toFloat(res.Rows[0][1]), nil
}

// letter: Brief eines Nutzers (Statement um Absender, Empfänger und Angaben ergänzt).
func (b *letterBuilder) letter(ctx context.Context, s map[string]any, u *userResult) error {
	r := b.r
	uf, ut := clip(r.from, r.to, u.from, u.to)
	usage := b.period(uf, ut)
	usage["partial"] = uf != r.from || ut != r.to
	usage["no_interim"] = u.noInterim
	s["period"] = b.period(r.from, r.to)
	s["usage"] = usage
	sender := b.sender(ctx, u.object, ut)
	recipient, names, contracts := b.recipient(ctx, u, uf, ut)
	s["sender"], s["recipient"] = sender, recipient
	s["tenants"] = strings.Join(names, ", ")
	s["contracts"] = strings.Join(contracts, ", ")
	s["owner"] = crud.Str(sender["name"])
	obj := b.place(ctx, "RentObject", u.object)
	s["object"] = obj
	kind := "Heiz- und Warmwasserkostenabrechnung"
	if r.predecessor != "" {
		kind = "Korrektur der Heiz- und Warmwasserkostenabrechnung"
	}
	where := firstNonEmpty(u.location, crud.Str(obj["designation"]), u.object)
	s["subject"] = fmt.Sprintf("%s %s – %s", kind, periodText(r.from, r.to), where)
	var summary []map[string]any
	for _, bl := range u.blocks {
		summary = append(summary, map[string]any{"name": bl["name"], "sum": bl["sum"]})
	}
	s["summary"] = summary
	// Ablesewerte (Werte aus Zählern mit den Messpunkten)
	vr, err := b.m.db.Query(ctx, `SELECT v.key_code, v.value, COALESCE(v.source, ''), COALESCE(v.note, '') FROM heatcost__value v
		LEFT JOIN heatcost__key k ON k.company_code = v.company_code AND k.code = v.key_code
		WHERE v.company_code = ? AND v.run_id = ? AND v.user_no = ? ORDER BY k.sort_order, v.key_code`, r.cc, r.id, u.no)
	if err != nil {
		return err
	}
	vals := map[string]float64{}
	var readings []map[string]any
	for _, x := range vr.Rows {
		key := crud.Str(x[0])
		vals[key] = toFloat(x[1])
		k := b.keys[key]
		src := crud.Str(x[2])
		if src != "METER" && src != srcManual {
			continue
		}
		var points []string
		note := crud.Str(x[3])
		if src == "METER" {
			for _, p := range strings.Split(note, "; ") {
				if p = strings.TrimSpace(p); p != "" {
					points = append(points, p)
				}
			}
			note = ""
		}
		if src == srcManual && note == "" && k[1] != "m²" {
			note = "von Hand erfasst"
		}
		if src == srcManual && k[1] == "m²" {
			continue
		}
		readings = append(readings, map[string]any{"key": key, "name": k[0], "unit": k[1], "value": toFloat(x[1]), "dec": int(toFloat(k[2])),
			"value_s": strings.TrimSpace(germanNumber(toFloat(x[1]), int(toFloat(k[2]))) + " " + k[1]),
			"points":  points, "note": note})
	}
	s["readings"] = readings
	s["compare"] = b.compare(vals, u.object)
	return nil
}

// compare: Verbrauch je m² gegenüber dem Durchschnitt der Liegenschaft und dem Vorjahr (§ 6a Abs. 3 HeizkostenV).
func (b *letterBuilder) compare(vals map[string]float64, object string) []map[string]any {
	var out []map[string]any
	for _, c := range [][3]string{{"Heizung", b.heatUsage, b.heatArea}, {"Warmwasser", b.wwUsage, firstNonEmpty(b.wwArea, b.heatArea)}} {
		usageKey, areaKey := c[1], c[2]
		v, ok := vals[usageKey]
		area := vals[areaKey]
		if !ok || area == 0 || usageKey == "" {
			continue
		}
		row := map[string]any{"name": c[0], "value": v, "unit": b.keys[usageKey][1], "area": area, "per_m2": v / area}
		if all, ar := b.basis[usageKey], b.basis[areaKey]; ar != 0 {
			avg := all / ar
			row["avg_m2"] = avg
			if avg != 0 {
				d := (v/area - avg) / avg * 100
				row["diff_percent"], row["above"] = d, d > 0
				row["diff_abs"] = d
				if d < 0 {
					row["diff_abs"] = -d
				}
			}
		}
		if p, ok := b.prev[object]; ok {
			if pv, ok := p[usageKey]; ok {
				row["previous"] = pv
				row["previous_from"], row["previous_to"] = b.prevPeriod[0], b.prevPeriod[1]
			}
		}
		out = append(out, row)
	}
	return out
}

// periodText: Kalenderjahr als „2025“, sonst „01.01.2025 – 30.06.2025“.
func periodText(from, to string) string {
	if strings.HasSuffix(from, "-01-01") && strings.HasSuffix(to, "-12-31") && from[:4] == to[:4] {
		return from[:4]
	}
	return germanDate(from) + " – " + germanDate(to)
}
