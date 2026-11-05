package heatcost

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// --- Rechnen ------------------------------------------------------------------------
//
// 1. Kostenaufstellung: Kosten mit Aufteilung SPLIT, HEATING, HOTWATER bilden die
//    Gesamtkosten der Heizungsanlage. Der Warmwasseranteil einer SPLIT-Zeile ist
//    ihr eigener Prozentsatz, sonst der der Abrechnung (nach Wärmezähler oder fest).
//    Der Warmwasseranteil wird je Kostengruppe gerundet; Heizung = Gesamt − Warmwasser.
// 2. Heizung und Warmwasser: wahlweise zuerst nach einem Schlüssel auf Nutzergruppen
//    (z. B. Doppelhäuser), dann je Gruppe Grundkosten (% nach Fläche) und
//    Verbrauchskosten (Rest nach Verbrauch).
// 3. Kosten mit eigenem Schlüssel (Hausnebenkosten) je Zeile auf die Nutzer der
//    Liegenschaft bzw. der Gruppe; direkte Kosten an den Nutzer.
// Verteilungsgröße eines Schlüssels: Eintrag „Verteilungsgrößen“ (Summe laut
// Messdienst), sonst die Summe der erfassten Nutzer. Beträge je Nutzer und Zeile
// kaufmännisch auf Cent.

type calcRun struct {
	cc, id, unit, from, to                string
	wwEnergy, totalEnergy, wwFixed        float64
	wwMethod                              string
	heatBase, wwBase                      float64
	heatArea, heatUsage, heatGroupKey     string
	wwArea, wwUsage, wwGroupKey           string
	heatGroups, wwGroups                  []string
	hasWWEnergy, hasTotalEnergy, hasFixed bool
	// seit 0.1.2
	wwRound, groupRest                    string
	wwTemp, wwVolume, co2Cost, co2Kg      float64
	hasTemp, hasVolume, hasCO2, hasCO2Dec bool
	co2Scale, co2Mode, co2AreaKey         string
	co2Dec                                int
	heatChange, degreeTable               string
	wwDec                                 int
	timeRound                             string // EXACT (leer) oder YEAR_FIRST
	hasWWDec                              bool
}

type co2Row struct {
	user                                           string
	share, emissions, cost, percent, landlord, ten float64
	deducted                                       bool
}

type co2Head struct {
	kgM2, percent, landlord float64
	stage                   string
}

type calcUser struct {
	no, name, from, to, object string
	groups                     []string
	sh                         shares
}

type calcLine struct {
	no                                         int64
	kind, text, alloc, group, key, scope, user string
	amount, wwPercent                          float64
	hasPercent, apportionable, co2             bool
}

type calcKey struct {
	name, unit   string
	decimals     int
	timeWeighted bool
}

type stepRow struct {
	block, text, key, scope, unit string
	amount, basis, price          float64
	hasBasis                      bool
}

type resultRow struct {
	user, block, group, text, key, scope, unit string
	total, basis, price, value, share, amount  float64
	factor                                     float64 // Nutzungsanteil beim Nutzerwechsel (0 = ganz)
	factorText                                 string
	apportionable                              bool
}

type calc struct {
	m        *Module
	ctx      context.Context
	r        calcRun
	users    []calcUser
	groups   map[string]string // Gruppe → Bezeichnung
	keys     map[string]calcKey
	cgroups  map[string]string // Kostengruppe → Bezeichnung
	cgOrder  map[string]int64
	lines    []calcLine
	basis    map[[2]string]float64 // (Schlüssel, Gruppe|*) → Summe
	values   map[string]map[string]float64
	steps    []stepRow
	results  []resultRow
	warnings []string
	co2      []co2Row
	co2Head  *co2Head
}

func (m *Module) loadCalc(ctx context.Context, cc, id string) (*calc, error) {
	c := &calc{m: m, ctx: ctx, groups: map[string]string{}, keys: map[string]calcKey{}, cgroups: map[string]string{}, cgOrder: map[string]int64{},
		basis: map[[2]string]float64{}, values: map[string]map[string]float64{}}
	res, err := m.db.Query(ctx, `SELECT unit_id, period_from, period_to, ww_energy, total_energy, ww_method, ww_fixed_percent, heat_base_percent, ww_base_percent,
		COALESCE(heat_area_key, ''), COALESCE(heat_usage_key, ''), COALESCE(heat_group_key, ''), COALESCE(heat_groups, ''),
		COALESCE(ww_area_key, ''), COALESCE(ww_usage_key, ''), COALESCE(ww_group_key, ''), COALESCE(ww_groups, ''), status,
		COALESCE(ww_round, ''), COALESCE(group_rest, ''), ww_temp, ww_volume, co2_cost, co2_kg, COALESCE(co2_scale, ''), COALESCE(co2_mode, ''),
		COALESCE(co2_area_key, ''), co2_ww_decimals, COALESCE(heat_change, ''), COALESCE(degree_table, ''), ww_decimals, COALESCE(time_round, '')
		FROM heatcost__run WHERE company_code = ? AND run_id = ?`, cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Abrechnung %s|%s", sdk.ErrNotFound, cc, id)
	}
	x := res.Rows[0]
	s := func(i int) string { return crud.Str(x[i]) }
	c.r = calcRun{cc: cc, id: id, unit: s(0), from: date(x[1]), to: date(x[2]), wwEnergy: toFloat(x[3]), totalEnergy: toFloat(x[4]),
		hasWWEnergy: isSet(x[3]), hasTotalEnergy: isSet(x[4]), wwMethod: s(5), wwFixed: toFloat(x[6]), hasFixed: isSet(x[6]),
		heatBase: toFloat(x[7]), wwBase: toFloat(x[8]), heatArea: s(9), heatUsage: s(10), heatGroupKey: s(11), heatGroups: splitCodes(s(12)),
		wwArea: s(13), wwUsage: s(14), wwGroupKey: s(15), wwGroups: splitCodes(s(16)),
		wwRound: s(18), groupRest: s(19), wwTemp: toFloat(x[20]), hasTemp: isSet(x[20]), wwVolume: toFloat(x[21]), hasVolume: isSet(x[21]),
		co2Cost: toFloat(x[22]), hasCO2: isSet(x[22]), co2Kg: toFloat(x[23]), co2Scale: s(24), co2Mode: s(25), co2AreaKey: s(26),
		co2Dec: int(toInt(x[27])), hasCO2Dec: isSet(x[27]), heatChange: s(28), degreeTable: s(29), wwDec: int(toInt(x[30])), hasWWDec: isSet(x[30]), timeRound: s(31)}
	if s(17) == runFinal {
		return nil, crud.Invalid("Abrechnung %s ist festgeschrieben", id)
	}
	if res, err = m.db.Query(ctx, "SELECT code, designation FROM heatcost__user_group WHERE company_code = ? AND unit_id = ?", cc, c.r.unit); err != nil {
		return nil, err
	}
	for _, g := range res.Rows {
		c.groups[crud.Str(g[0])] = crud.Str(g[1])
	}
	if res, err = m.db.Query(ctx, `SELECT user_no, designation, COALESCE(groups, ''), valid_from, valid_to, COALESCE(object_id, '') FROM heatcost__user
		WHERE company_code = ? AND unit_id = ? AND valid_from <= ? AND valid_to >= ? ORDER BY user_no`, cc, c.r.unit, c.r.to, c.r.from); err != nil {
		return nil, err
	}
	for _, u := range res.Rows {
		c.users = append(c.users, calcUser{no: crud.Str(u[0]), name: crud.Str(u[1]), groups: splitCodes(crud.Str(u[2])), from: date(u[3]), to: date(u[4]),
			object: crud.Str(u[5]), sh: shares{time: 1, degree: 1}})
	}
	if res, err = m.db.Query(ctx, "SELECT code, name, COALESCE(unit, ''), decimals, COALESCE(time_weighted, ?) FROM heatcost__key WHERE company_code = ?", false, cc); err != nil {
		return nil, err
	}
	for _, k := range res.Rows {
		c.keys[crud.Str(k[0])] = calcKey{name: crud.Str(k[1]), unit: crud.Str(k[2]), decimals: int(toInt(k[3])), timeWeighted: crud.AsBool(k[4])}
	}
	if res, err = m.db.Query(ctx, "SELECT code, name, sort_order FROM heatcost__cost_group WHERE company_code = ?", cc); err != nil {
		return nil, err
	}
	for _, g := range res.Rows {
		c.cgroups[crud.Str(g[0])], c.cgOrder[crud.Str(g[0])] = crud.Str(g[1]), toInt(g[2])
	}
	if res, err = m.db.Query(ctx, `SELECT l.line_no, l.cost_kind, l.text, l.allocation, COALESCE(l.cost_group, ''), COALESCE(l.key_code, ''), COALESCE(l.scope, ''),
		COALESCE(l.user_no, ''), l.amount, l.ww_percent, COALESCE(k.apportionable, ?), COALESCE(k.co2, ?)
		FROM heatcost__cost l LEFT JOIN heatcost__cost_kind k ON k.company_code = l.company_code AND k.code = l.cost_kind
		WHERE l.company_code = ? AND l.run_id = ? ORDER BY l.sort_order, l.line_no`, true, false, cc, id); err != nil {
		return nil, err
	}
	for _, l := range res.Rows {
		c.lines = append(c.lines, calcLine{no: toInt(l[0]), kind: crud.Str(l[1]), text: crud.Str(l[2]), alloc: crud.Str(l[3]), group: crud.Str(l[4]),
			key: crud.Str(l[5]), scope: crud.Str(l[6]), user: crud.Str(l[7]), amount: toFloat(l[8]), wwPercent: toFloat(l[9]), hasPercent: isSet(l[9]),
			apportionable: crud.AsBool(l[10]), co2: crud.AsBool(l[11])})
	}
	if res, err = m.db.Query(ctx, "SELECT key_code, scope, total FROM heatcost__basis WHERE company_code = ? AND run_id = ?", cc, id); err != nil {
		return nil, err
	}
	for _, b := range res.Rows {
		c.basis[[2]string{crud.Str(b[0]), crud.Str(b[1])}] = toFloat(b[2])
	}
	if res, err = m.db.Query(ctx, "SELECT user_no, key_code, value FROM heatcost__value WHERE company_code = ? AND run_id = ?", cc, id); err != nil {
		return nil, err
	}
	for _, v := range res.Rows {
		u := crud.Str(v[0])
		if c.values[u] == nil {
			c.values[u] = map[string]float64{}
		}
		c.values[u][crud.Str(v[1])] = toFloat(v[2])
	}
	return c, nil
}

func (c *calc) inScope(u calcUser, scope string) bool {
	return scope == "" || scope == "*" || containsCode(u.groups, scope)
}

// total: Verteilungsgröße des Schlüssels in der Gruppe (Eintrag, sonst Summe der Nutzer).
func (c *calc) total(key, scope string) float64 { return c.totalW(key, scope, nil) }

// weight: Gewicht eines Nutzers beim Nutzerwechsel (nil = 1) und seine Darstellung
// (Gradtage „530,000/1000“ bzw. Tage „120/365“).
type weight func(u calcUser) (float64, string)

func (c *calc) totalW(key, scope string, w weight) float64 {
	if scope == "" {
		scope = "*"
	}
	if v, ok := c.basis[[2]string{key, scope}]; ok {
		return v
	}
	sum := 0.0
	for _, u := range c.users {
		if c.inScope(u, scope) {
			f := 1.0
			if w != nil {
				f, _ = w(u)
			}
			sum += c.values[u.no][key] * f
		}
	}
	return sum
}

func timeShare(u calcUser) (float64, string) {
	if !u.sh.partial {
		return 1, ""
	}
	return u.sh.time, fmt.Sprintf("%d/%d", u.sh.days, u.sh.allDays)
}

// heatShare: Grundkosten Heizung bei Nutzerwechsel nach Gradtagszahlen oder Kalendertagen.
func (c *calc) heatShare(u calcUser) (float64, string) {
	if c.r.heatChange == changeDegree && u.sh.partial {
		return u.sh.degree, germanNumber(u.sh.degPart, 3) + "/" + germanNumber(u.sh.degAll, 0)
	}
	return timeShare(u)
}

// keyWeight: Schlüssel mit „zeitanteilig“ nach Kalendertagen.
func (c *calc) keyWeight(key string) weight {
	if c.keys[key].timeWeighted {
		return timeShare
	}
	return nil
}

func (c *calc) scopeName(scope string) string {
	if scope == "" || scope == "*" {
		return ""
	}
	if n := c.groups[scope]; n != "" {
		return n
	}
	return scope
}

func (c *calc) keyUnit(key string) string { return c.keys[key].unit }

func (c *calc) step(s stepRow) { c.steps = append(c.steps, s) }

// distribute: Betrag nach Schlüssel auf die Nutzer der Gruppe.
func (c *calc) distribute(block, group, text, key, scope string, amount float64, apportionable bool, w weight) {
	basis := c.totalW(key, scope, w)
	price := 0.0
	if basis != 0 {
		price = amount / basis
	}
	c.step(stepRow{block: block, text: text, key: key, scope: scope, unit: c.keyUnit(key), amount: amount, basis: basis, price: price, hasBasis: true})
	if basis == 0 {
		if amount != 0 {
			c.warnings = append(c.warnings, fmt.Sprintf("%s: Verteilungsgröße %s ist 0 – Betrag %s € nicht verteilt", text, key, money(amount)))
		}
		return
	}
	for _, u := range c.users {
		if !c.inScope(u, scope) {
			continue
		}
		v, ok := c.values[u.no][key]
		if !ok {
			continue
		}
		f, ftext := 1.0, ""
		if w != nil {
			f, ftext = w(u)
		}
		row := resultRow{user: u.no, block: block, group: group, text: text, key: key, scope: scope, unit: c.keyUnit(key),
			total: amount, basis: basis, price: price, value: v, share: v * f / basis * 100, amount: round2(amount * v * f / basis), apportionable: apportionable}
		if f != 1 && c.r.timeRound == timeYearFirst {
			row.amount = round2(round2(amount*v/basis) * f) // erst Jahresbetrag, dann Zeitanteil
		}
		if f != 1 {
			row.factor, row.factorText = f, ftext
		}
		c.results = append(c.results, row)
	}
}

// block: Heizung bzw. Warmwasser – ggf. auf Gruppen, dann Grund- und Verbrauchskosten.
func (c *calc) block(name string, amount float64, groups []string, groupKey string, base float64, areaKey, usageKey string, w weight) {
	type pool struct {
		scope  string
		amount float64
	}
	pools := []pool{{scope: "*", amount: amount}}
	if len(groups) > 0 {
		pools = pools[:0]
		sum := 0.0
		for _, g := range groups {
			sum += c.total(groupKey, g)
		}
		if sum == 0 {
			c.warnings = append(c.warnings, fmt.Sprintf("%s: Verteilungsgrößen %s der Gruppen sind 0 – nicht verteilt", name, groupKey))
			return
		}
		c.step(stepRow{block: name, text: fmt.Sprintf("%s nach %s auf die Gruppen", name, c.keys[groupKey].name), key: groupKey,
			unit: c.keyUnit(groupKey), amount: amount, basis: sum, price: amount / sum, hasBasis: true})
		rest := amount
		for i, g := range groups {
			a := round2(amount * c.total(groupKey, g) / sum)
			if i == len(groups)-1 && c.r.groupRest != restNone {
				a = round2(rest)
			}
			rest -= a
			pools = append(pools, pool{scope: g, amount: a})
		}
	}
	for _, p := range pools {
		label := name
		if p.scope != "*" {
			label = name + " " + c.scopeName(p.scope)
			c.step(stepRow{block: name, text: label, scope: p.scope, amount: p.amount})
		}
		grund := round2(p.amount * base / 100)
		c.distribute(name, "", fmt.Sprintf("%s – %s %% Grundkosten", label, germanNumber(base, 2)), areaKey, p.scope, grund, true, w)
		c.distribute(name, "", fmt.Sprintf("%s – %s %% Verbrauchskosten", label, germanNumber(100-base, 2)), usageKey, p.scope, round2(p.amount-grund), true, nil)
	}
}

const (
	blockHeat = "Heizung"
	blockWW   = "Warmwasser"
)

func (c *calc) run() (heat, ww, total float64, err error) {
	share := 0.0
	needShare := false
	for _, l := range c.lines {
		if l.alloc == allocSplit && !l.hasPercent {
			needShare = true
		}
	}
	if c.r.wwRound == "" || c.r.groupRest == "" {
		return 0, 0, 0, crud.Invalid("Rundung fehlt an der Abrechnung – „Warmwasseranteil runden“ und „Rundungsrest“ eintragen oder an der Einheit eine Vorgabe wählen")
	}
	formula := 0.0
	switch c.r.wwMethod {
	case wwFixed:
		share = c.r.wwFixed / 100
	case wwFormula:
		if !c.r.hasVolume || !c.r.hasTemp || !c.r.hasTotalEnergy || c.r.totalEnergy == 0 {
			return 0, 0, 0, crud.Invalid("Formel § 9 Abs. 2: Warmwassermenge, Warmwassertemperatur und Gesamtenergie an der Abrechnung eintragen")
		}
		formula = 2.5 * c.r.wwVolume * (c.r.wwTemp - 10)
		share = formula / c.r.totalEnergy
	default:
		if needShare && (!c.r.hasWWEnergy || !c.r.hasTotalEnergy || c.r.totalEnergy == 0) {
			return 0, 0, 0, crud.Invalid("Warmwasseranteil nach Wärmezähler: Energie für Warmwasser und Gesamtenergie an der Abrechnung eintragen")
		}
		if c.r.totalEnergy != 0 {
			share = c.r.wwEnergy / c.r.totalEnergy
		}
	}
	if c.r.hasWWDec && c.r.wwMethod != wwFixed {
		share = round(share*100, c.r.wwDec) / 100
	}
	// CO2: Stufe vorab; „von den Kosten“ zieht den Vermieteranteil als Zeile von den Brennstoffkosten ab.
	if err := c.co2Stage(); err != nil {
		return 0, 0, 0, err
	}
	if c.co2Head != nil && c.r.co2Mode == co2Costs && c.co2Head.landlord != 0 {
		for i, l := range c.lines {
			if !l.co2 {
				continue
			}
			d := l
			d.amount, d.co2 = -c.co2Head.landlord, false
			d.text = fmt.Sprintf("Abzug CO2-Kostenanteil Vermieter (CO2KostAufG, %s, %s %%)", c.co2Head.stage, germanNumber(c.co2Head.percent, 2))
			c.lines = append(c.lines[:i+1], append([]calcLine{d}, c.lines[i+1:]...)...)
			break
		}
	}
	// 1. Kostenaufstellung
	plant, wwByGroup := 0.0, map[string]float64{}
	co2Share := share
	if c.r.hasCO2Dec {
		co2Share = round(share*100, c.r.co2Dec) / 100
	}
	co2Heat, co2WW := 0.0, 0.0
	var groupOrder []string
	for _, l := range c.lines {
		total += l.amount
		c.step(stepRow{block: "Kosten", text: costText(l, c.cgroups), amount: l.amount})
		var part float64
		switch l.alloc {
		case allocSplit:
			p, pc := share, co2Share
			if l.hasPercent {
				p, pc = l.wwPercent/100, l.wwPercent/100
			}
			part = l.amount * p
			if l.co2 {
				co2WW += l.amount * pc
				co2Heat += l.amount * (1 - pc)
			}
		case allocHotwater:
			part = l.amount
			if l.co2 {
				co2WW += l.amount
			}
		case allocHeating:
			if l.co2 {
				co2Heat += l.amount
			}
		default:
			continue
		}
		plant += l.amount
		if _, ok := wwByGroup[l.group]; !ok {
			groupOrder = append(groupOrder, l.group)
		}
		wwByGroup[l.group] += part
	}
	if needShare || c.r.wwMethod == wwFixed {
		basisText := fmt.Sprintf("%s kWh ÷ %s kWh", germanNumber(c.r.wwEnergy, 3), germanNumber(c.r.totalEnergy, 3))
		if c.r.wwMethod == wwFixed {
			basisText = "fester Anteil"
		} else if c.r.wwMethod == wwFormula {
			basisText = fmt.Sprintf("Formel § 9 Abs. 2: 2,5 kWh/(m³·K) × %s m³ × (%s − 10) K = %s kWh ÷ %s kWh", germanNumber(c.r.wwVolume, 3),
				germanNumber(c.r.wwTemp, 1), germanNumber(formula, 0), germanNumber(c.r.totalEnergy, 3))
		}
		dec := 4
		if c.r.hasWWDec && c.r.wwMethod != wwFixed {
			dec = c.r.wwDec
			basisText += fmt.Sprintf(", auf %d Stellen gerundet", dec)
		}
		c.step(stepRow{block: blockWW, text: fmt.Sprintf("Warmwasseranteil %s %% (%s)", germanNumber(share*100, dec), basisText), amount: 0})
	}
	for _, g := range groupOrder {
		a := round2(wwByGroup[g])
		if c.r.wwRound == roundTotal {
			ww += wwByGroup[g]
		} else {
			ww += a
		}
		name := c.cgroups[g]
		if name == "" {
			name = "ohne Kostengruppe"
		}
		c.step(stepRow{block: blockWW, text: "Warmwasser aus " + name, amount: a})
	}
	ww = round2(ww)
	plant = round2(plant)
	heat = round2(plant - ww)
	c.step(stepRow{block: blockHeat, text: "Gesamtkosten der Heizungsanlage", amount: plant})
	c.step(stepRow{block: blockHeat, text: "davon Heizung", amount: heat})
	c.step(stepRow{block: blockWW, text: "davon Warmwasser", amount: ww})
	// 2. Heizung und Warmwasser
	if err := c.userShares(); err != nil {
		return 0, 0, 0, err
	}
	c.block(blockHeat, heat, c.r.heatGroups, c.r.heatGroupKey, c.r.heatBase, c.r.heatArea, c.r.heatUsage, c.heatShare)
	c.block(blockWW, ww, c.r.wwGroups, c.r.wwGroupKey, c.r.wwBase, c.r.wwArea, c.r.wwUsage, timeShare)
	// 3. eigene Schlüssel und direkte Kosten
	for _, l := range c.lines {
		switch l.alloc {
		case allocKey:
			text := l.text
			if n := c.scopeName(l.scope); n != "" && !strings.Contains(text, n) {
				text += " (" + n + ")"
			}
			c.distribute(c.cgroups[l.group], l.group, text, l.key, l.scope, l.amount, l.apportionable, c.keyWeight(l.key))
		case allocDirect:
			c.step(stepRow{block: c.cgroups[l.group], text: l.text + " – Nutzer " + l.user, amount: l.amount})
			c.results = append(c.results, resultRow{user: l.user, block: c.cgroups[l.group], group: l.group, text: l.text, total: l.amount,
				amount: l.amount, share: 100, apportionable: l.apportionable})
		}
	}
	// 4. CO2-Kosten (CO2KostAufG)
	if err := c.co2Split(heat, ww, co2Heat, co2WW); err != nil {
		return 0, 0, 0, err
	}
	return heat, ww, round2(total), nil
}

// userShares: Nutzungsanteile der Nutzer mit Teilzeitraum (§ 9b HeizkostenV).
func (c *calc) userShares() error {
	var deg degreeTable
	byObject := map[string][]calcUser{}
	for i := range c.users {
		u := &c.users[i]
		f, t := clip(c.r.from, c.r.to, u.from, u.to)
		if f == c.r.from && t == c.r.to {
			continue
		}
		if c.r.heatChange == "" {
			return crud.Invalid("Nutzer %s wechselt im Zeitraum: Grundkosten Heizung bei Nutzerwechsel an der Abrechnung eintragen", u.no)
		}
		if c.r.heatChange == changeDegree && deg == nil {
			if c.r.degreeTable == "" {
				return crud.Invalid("Nutzerwechsel nach Gradtagszahlen: Tabelle fehlt an der Abrechnung")
			}
			var err error
			if deg, err = c.m.loadDegree(c.ctx, c.r.cc, c.r.degreeTable); err != nil {
				return err
			}
		}
		sh, err := shareOf(c.r.from, c.r.to, u.from, u.to, deg, c.r.degreeTable)
		if err != nil {
			return err
		}
		u.sh = sh
		text := fmt.Sprintf("Nutzer %s: Nutzungszeitraum %s–%s, Kalendertage %s %%", u.no, germanDate(f), germanDate(t), germanNumber(sh.time*100, 2))
		if c.r.heatChange == changeDegree {
			text += fmt.Sprintf(", Gradtagszahlen (%s) %s %%", c.r.degreeTable, germanNumber(sh.degree*100, 2))
		}
		c.step(stepRow{block: "Nutzerwechsel", text: text})
		if u.object != "" {
			byObject[u.object] = append(byObject[u.object], *u)
		}
	}
	for obj, us := range byObject {
		sum := 0.0
		for _, u := range us {
			sum += u.sh.time
		}
		if sum > 1+1e-9 {
			c.warnings = append(c.warnings, fmt.Sprintf("Mietobjekt %s: Nutzungszeiträume der Nutzer überschneiden sich", obj))
		}
	}
	return nil
}

const blockCO2 = "CO2-Kosten"

// co2Split: Vermieteranteil der CO2-Kosten nach Stufenmodell. Maßstab je Nutzer ist sein
// Anteil an den Kosten mit CO2-Preis (Brennstoff, Fernwärme), verteilt wie Heizung und Warmwasser.
// co2Stage: kg CO2 je m², Stufe und Vermieteranteil der Liegenschaft.
func (c *calc) co2Stage() error {
	if !c.r.hasCO2 {
		return nil
	}
	if c.r.co2Kg <= 0 || c.r.co2Scale == "" || c.r.co2Mode == "" {
		return crud.Invalid("CO2-Kosten: Emissionen in kg, Stufenmodell und „Vermieteranteil der CO2-Kosten“ an der Abrechnung eintragen")
	}
	areaKey := c.r.co2AreaKey
	if areaKey == "" {
		areaKey = c.r.heatArea
	}
	area := c.total(areaKey, "*")
	if area <= 0 {
		return crud.Invalid("CO2-Kosten: Fläche %s der Liegenschaft ist 0 – Verteilungsgröße eintragen", areaKey)
	}
	kg := c.r.co2Kg / area
	if dec, err := c.m.one(c.ctx, "SELECT kg_decimals FROM heatcost__co2_scale WHERE company_code = ? AND code = ?", c.r.cc, c.r.co2Scale); err != nil {
		return err
	} else if dec != "" {
		kg = round(kg, int(toFloat(dec)))
	}
	res, err := c.m.db.Query(c.ctx, `SELECT st.stage_no, st.from_kg, st.landlord_percent, sc.name FROM heatcost__co2_stage st
		JOIN heatcost__co2_scale sc ON sc.company_code = st.company_code AND sc.code = st.scale
		WHERE st.company_code = ? AND st.scale = ? ORDER BY st.from_kg`, c.r.cc, c.r.co2Scale)
	if err != nil {
		return err
	}
	var stage []any
	for _, r := range res.Rows {
		if toFloat(r[1]) <= kg {
			stage = r
		}
	}
	if stage == nil {
		return crud.Invalid("CO2-Stufenmodell %s hat keine Stufe für %s kg CO2 je m² – Einstellungen → CO2-Stufenmodelle", c.r.co2Scale, germanNumber(kg, 3))
	}
	pct := toFloat(stage[2])
	p := pct / 100
	landlordAll := round2(c.r.co2Cost * p)
	stageText := fmt.Sprintf("Stufe %d (ab %s kg/m²)", toInt(stage[0]), germanNumber(toFloat(stage[1]), 1))
	c.co2Head = &co2Head{kgM2: kg, percent: pct, landlord: landlordAll, stage: stageText}
	c.step(stepRow{block: blockCO2, text: fmt.Sprintf("CO2-Emissionen %s kg ÷ %s m² (%s) = %s kg CO2 je m² und Jahr", germanNumber(c.r.co2Kg, 3),
		germanNumber(area, 3), c.keys[areaKey].name, germanNumber(kg, 3))})
	c.step(stepRow{block: blockCO2, text: fmt.Sprintf("%s, %s: Vermieter %s %%, Mieter %s %%", crud.Str(stage[3]), stageText,
		germanNumber(pct, 2), germanNumber(100-pct, 2))})
	c.step(stepRow{block: blockCO2, text: "CO2-Kosten der Liegenschaft", amount: c.r.co2Cost})
	c.step(stepRow{block: blockCO2, text: "CO2-Kostenanteil aller Vermieter", amount: landlordAll})
	c.step(stepRow{block: blockCO2, text: "CO2-Kostenanteil aller Mieter", amount: round2(c.r.co2Cost - landlordAll)})
	return nil
}

func (c *calc) co2Split(heat, ww, co2Heat, co2WW float64) error {
	if c.co2Head == nil {
		return nil
	}
	pct, stageText := c.co2Head.percent, c.co2Head.stage
	p := pct / 100
	fuel := co2Heat + co2WW
	if fuel == 0 {
		c.warnings = append(c.warnings, "CO2-Kosten: keine Kosten mit CO2-Preis (Kostenart „Enthält den CO2-Preis“) – nicht auf die Nutzer verteilt")
		return nil
	}
	text := fmt.Sprintf("Maßstab: Kosten mit CO2-Preis, davon Heizung %s €, Warmwasser %s €", money(round2(co2Heat)), money(round2(co2WW)))
	if c.r.hasCO2Dec {
		text += fmt.Sprintf(" (Warmwasseranteil auf %d Stellen gerundet)", c.r.co2Dec)
	}
	c.step(stepRow{block: blockCO2, text: text, amount: round2(fuel)})
	sums := map[string][2]float64{}
	for _, r := range c.results {
		v := sums[r.user]
		switch r.block {
		case blockHeat:
			v[0] += r.amount
		case blockWW:
			v[1] += r.amount
		}
		sums[r.user] = v
	}
	deduct := c.r.co2Mode == co2Deduct || c.r.co2Mode == co2Costs
	for _, u := range c.users {
		v := sums[u.no]
		share := 0.0
		if heat != 0 {
			share += co2Heat * v[0] / heat
		}
		if ww != 0 {
			share += co2WW * v[1] / ww
		}
		share /= fuel
		if share == 0 {
			continue
		}
		cost := c.r.co2Cost * share
		row := co2Row{user: u.no, share: share * 100, emissions: c.r.co2Kg * share, cost: round2(cost), percent: pct,
			landlord: round2(cost * p), ten: round2(cost * (1 - p)), deducted: deduct}
		c.co2 = append(c.co2, row)
		if c.r.co2Mode == co2Deduct && row.landlord != 0 {
			c.results = append(c.results, resultRow{user: u.no, block: blockCO2, text: fmt.Sprintf("Vermieteranteil CO2-Kosten (CO2KostAufG, %s, %s %%)",
				stageText, germanNumber(pct, 2)), total: c.r.co2Cost, share: share * 100, amount: -row.landlord, apportionable: true})
		}
	}
	return nil
}

func costText(l calcLine, groups map[string]string) string {
	if g := groups[l.group]; g != "" {
		return g + ": " + l.text
	}
	return l.text
}

func (m *Module) computeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
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
	c, err := m.loadCalc(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(c.lines) == 0 {
		return sdk.Response{}, crud.Invalid("Abrechnung %s hat keine Kosten – Kostenaufstellung erfassen", id)
	}
	heat, ww, total, err := c.run()
	if err != nil {
		return sdk.Response{}, err
	}
	perUser := map[string]float64{}
	for _, r := range c.results {
		perUser[r.user] += r.amount
	}
	sumUsers := 0.0
	for _, v := range perUser {
		sumUsers += v
	}
	msg := fmt.Sprintf("Gerechnet: Gesamtkosten %s € (Heizung %s €, Warmwasser %s €), %d Nutzer mit zusammen %s €",
		money(total), money(heat), money(ww), len(perUser), money(round2(sumUsers)))
	if h := c.co2Head; h != nil {
		how := "abgezogen"
		switch c.r.co2Mode {
		case co2Show:
			how = "nur ausgewiesen, nicht abgezogen"
		case co2Costs:
			how = "vor der Verteilung von den Brennstoffkosten abgezogen"
		}
		msg += fmt.Sprintf("; CO2: %s kg je m², %s, Vermieter %s %% = %s € (%s)", germanNumber(h.kgM2, 3), h.stage, germanNumber(h.percent, 2), money(h.landlord), how)
	}
	if len(c.warnings) > 0 {
		msg += "\nHinweise:\n- " + strings.Join(c.warnings, "\n- ")
	}
	share := any(nil)
	if c.r.wwMethod == wwFixed {
		share = c.r.wwFixed
	} else if c.r.wwMethod == wwFormula && c.r.totalEnergy != 0 {
		share = 2.5 * c.r.wwVolume * (c.r.wwTemp - 10) / c.r.totalEnergy * 100
	} else if c.r.totalEnergy != 0 {
		share = c.r.wwEnergy / c.r.totalEnergy * 100
	}
	if f, ok := share.(float64); ok && c.r.hasWWDec && c.r.wwMethod != wwFixed {
		share = round(f, c.r.wwDec)
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range []string{"co2", "result", "step"} {
			if _, err := m.db.Exec(ctx, "DELETE FROM heatcost__"+t+" WHERE company_code = ? AND run_id = ?", cc, id); err != nil {
				return err
			}
		}
		for i, s := range c.steps {
			var basis, price any
			if s.hasBasis {
				basis, price = s.basis, s.price
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO heatcost__step (company_code, run_id, step_no, block, text, amount, key_code, scope, unit, basis, price)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, id, i+1, s.block, s.text, s.amount, nilIfEmpty(s.key), nilIfEmpty(s.scope), nilIfEmpty(s.unit), basis, price); err != nil {
				return err
			}
		}
		line := map[string]int{}
		for _, r := range c.results {
			line[r.user]++
			if _, err := m.db.Exec(ctx, `INSERT INTO heatcost__result (company_code, run_id, user_no, line_no, block, cost_group, text, total, key_code, scope,
				basis, price, value, unit, share, amount, apportionable, factor, factor_text) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				cc, id, r.user, line[r.user], r.block, nilIfEmpty(r.group), r.text, r.total, nilIfEmpty(r.key), nilIfEmpty(r.scope),
				r.basis, r.price, r.value, nilIfEmpty(r.unit), r.share, r.amount, r.apportionable, nilZero(r.factor), nilIfEmpty(r.factorText)); err != nil {
				return err
			}
		}
		for _, r := range c.co2 {
			if _, err := m.db.Exec(ctx, `INSERT INTO heatcost__co2 (company_code, run_id, user_no, share, emissions, cost, percent, landlord, tenant, deducted)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, id, r.user, r.share, r.emissions, r.cost, r.percent, r.landlord, r.ten, r.deducted); err != nil {
				return err
			}
		}
		var kg, stage, pct, landlord any
		if h := c.co2Head; h != nil {
			kg, stage, pct, landlord = h.kgM2, h.stage, h.percent, h.landlord
		}
		_, err := m.db.Exec(ctx, `UPDATE heatcost__run SET status = ?, ww_share = ?, total_cost = ?, heat_cost = ?, ww_cost = ?, message = ?, computed_at = ?,
			co2_kg_m2 = ?, co2_stage = ?, co2_percent = ?, co2_landlord = ?
			WHERE company_code = ? AND run_id = ?`, runComputed, share, total, heat, ww, msg, time.Now().UTC().Format(time.RFC3339), kg, stage, pct, landlord, cc, id)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "total_cost": total, "heat_cost": heat, "ww_cost": ww, "users": perUser}}, nil
}

// --- Einzelabrechnung -----------------------------------------------------------------

func (m *Module) statementAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
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
	st, err := m.one(ctx, "SELECT status FROM heatcost__run WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if st == runDraft {
		return sdk.Response{}, crud.Invalid("Abrechnung %s ist nach der letzten Änderung nicht gerechnet – zuerst „Rechnen“", id)
	}
	user := strings.TrimSpace(crud.Str(in.Data["st_user"]))
	q := `SELECT r.user_no, COALESCE(u.designation, ''), r.block, r.text, r.price, r.value, COALESCE(r.unit, ''), r.share, r.amount, COALESCE(r.key_code, '')
		FROM heatcost__result r JOIN heatcost__run h ON h.company_code = r.company_code AND h.run_id = r.run_id
		LEFT JOIN heatcost__user u ON u.company_code = r.company_code AND u.unit_id = h.unit_id AND u.user_no = r.user_no
		WHERE r.company_code = ? AND r.run_id = ?`
	args := []any{cc, id}
	if user != "" {
		q += " AND r.user_no = ?"
		args = append(args, user)
	}
	res, err := m.db.Query(ctx, q+" ORDER BY r.user_no, r.line_no", args...)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Kein Ergebnis für Nutzer %q", user)
	}
	keys := map[string]int{}
	if kr, err := m.db.Query(ctx, "SELECT code, decimals FROM heatcost__key WHERE company_code = ?", cc); err == nil {
		for _, k := range kr.Rows {
			keys[crud.Str(k[0])] = int(toInt(k[1]))
		}
	}
	co2 := map[string][]any{}
	if cr, err := m.db.Query(ctx, "SELECT user_no, share, emissions, cost, percent, landlord, tenant, deducted FROM heatcost__co2 WHERE company_code = ? AND run_id = ?", cc, id); err == nil {
		for _, r := range cr.Rows {
			co2[crud.Str(r[0])] = r
		}
	}
	var rows [][]any
	cur, curBlock, curName := "", "", ""
	var blockSum, userSum, all float64
	users := 0
	flushBlock := func() {
		if curBlock != "" {
			rows = append(rows, []any{"", "Summe " + curBlock, "", "", "", money(round2(blockSum))})
		}
		blockSum = 0
	}
	flushUser := func() {
		if cur == "" {
			return
		}
		flushBlock()
		rows = append(rows, []any{"", "Gesamtkosten Nutzer " + cur + " " + curName, "", "", "", money(round2(userSum))})
		if r, ok := co2[cur]; ok {
			how := "oben abgezogen"
			if !crud.AsBool(r[7]) {
				how = "nicht abgezogen – der Eigentümer verrechnet ihn in der Nebenkostenabrechnung"
			}
			rows = append(rows,
				[]any{"", fmt.Sprintf("CO2-Kosten der Nutzereinheit (%s kg CO2)", germanNumber(toFloat(r[2]), 3)), "", "", germanNumber(toFloat(r[1]), 4) + " %", money(toFloat(r[3]))},
				[]any{"", fmt.Sprintf("Vermieteranteil CO2-Kosten %s %% (%s)", germanNumber(toFloat(r[4]), 2), how), "", "", "", money(toFloat(r[5]))},
				[]any{"", "enthaltener Mieteranteil CO2-Kosten", "", "", "", money(toFloat(r[6]))})
		}
		all += userSum
		userSum = 0
	}
	sorted := res.Rows
	sort.SliceStable(sorted, func(i, j int) bool { return crud.Str(sorted[i][0]) < crud.Str(sorted[j][0]) })
	for _, r := range sorted {
		u, block := crud.Str(r[0]), crud.Str(r[2])
		if u != cur {
			flushUser()
			cur, curName, curBlock = u, crud.Str(r[1]), ""
			users++
			rows = append(rows, []any{"Nutzer " + u, curName, "", "", "", ""})
		}
		if block != curBlock {
			flushBlock()
			curBlock = block
			rows = append(rows, []any{"", block, "", "", "", ""})
		}
		amount := toFloat(r[8])
		blockSum += amount
		userSum += amount
		dec := keys[crud.Str(r[9])]
		val := ""
		if crud.Str(r[9]) != "" {
			val = germanNumber(toFloat(r[5]), dec) + " " + crud.Str(r[6])
		}
		price := ""
		if crud.Str(r[9]) != "" {
			price = germanNumber(toFloat(r[4]), 6) + " €"
		}
		rows = append(rows, []any{"", crud.Str(r[3]), price, val, germanNumber(toFloat(r[7]), 2) + " %", money(amount)})
	}
	flushUser()
	msg := fmt.Sprintf("Einzelabrechnung %s: %d Nutzer, zusammen %s €", id, users, money(round2(all)))
	return sdk.Response{Payload: map[string]any{"message": msg, "count": len(rows),
		"table": map[string]any{"columns": []string{"Nutzer", "Position", "Preis je Einheit", "Ihr Wert", "Anteil", "Betrag €"}, "rows": rows}}}, nil
}
