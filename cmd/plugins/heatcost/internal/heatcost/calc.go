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
}

type calcUser struct {
	no, name string
	groups   []string
}

type calcLine struct {
	no                                         int64
	kind, text, alloc, group, key, scope, user string
	amount, wwPercent                          float64
	hasPercent, apportionable                  bool
}

type calcKey struct {
	name, unit string
	decimals   int
}

type stepRow struct {
	block, text, key, scope, unit string
	amount, basis, price          float64
	hasBasis                      bool
}

type resultRow struct {
	user, block, group, text, key, scope, unit string
	total, basis, price, value, share, amount  float64
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
}

func (m *Module) loadCalc(ctx context.Context, cc, id string) (*calc, error) {
	c := &calc{m: m, ctx: ctx, groups: map[string]string{}, keys: map[string]calcKey{}, cgroups: map[string]string{}, cgOrder: map[string]int64{},
		basis: map[[2]string]float64{}, values: map[string]map[string]float64{}}
	res, err := m.db.Query(ctx, `SELECT unit_id, period_from, period_to, ww_energy, total_energy, ww_method, ww_fixed_percent, heat_base_percent, ww_base_percent,
		COALESCE(heat_area_key, ''), COALESCE(heat_usage_key, ''), COALESCE(heat_group_key, ''), COALESCE(heat_groups, ''),
		COALESCE(ww_area_key, ''), COALESCE(ww_usage_key, ''), COALESCE(ww_group_key, ''), COALESCE(ww_groups, ''), status
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
		wwArea: s(13), wwUsage: s(14), wwGroupKey: s(15), wwGroups: splitCodes(s(16))}
	if s(17) == runFinal {
		return nil, crud.Invalid("Abrechnung %s ist festgeschrieben", id)
	}
	if res, err = m.db.Query(ctx, "SELECT code, designation FROM heatcost__user_group WHERE company_code = ? AND unit_id = ?", cc, c.r.unit); err != nil {
		return nil, err
	}
	for _, g := range res.Rows {
		c.groups[crud.Str(g[0])] = crud.Str(g[1])
	}
	if res, err = m.db.Query(ctx, `SELECT user_no, designation, COALESCE(groups, '') FROM heatcost__user
		WHERE company_code = ? AND unit_id = ? AND valid_from <= ? AND valid_to >= ? ORDER BY user_no`, cc, c.r.unit, c.r.to, c.r.from); err != nil {
		return nil, err
	}
	for _, u := range res.Rows {
		c.users = append(c.users, calcUser{no: crud.Str(u[0]), name: crud.Str(u[1]), groups: splitCodes(crud.Str(u[2]))})
	}
	if res, err = m.db.Query(ctx, "SELECT code, name, COALESCE(unit, ''), decimals FROM heatcost__key WHERE company_code = ?", cc); err != nil {
		return nil, err
	}
	for _, k := range res.Rows {
		c.keys[crud.Str(k[0])] = calcKey{name: crud.Str(k[1]), unit: crud.Str(k[2]), decimals: int(toInt(k[3]))}
	}
	if res, err = m.db.Query(ctx, "SELECT code, name, sort_order FROM heatcost__cost_group WHERE company_code = ?", cc); err != nil {
		return nil, err
	}
	for _, g := range res.Rows {
		c.cgroups[crud.Str(g[0])], c.cgOrder[crud.Str(g[0])] = crud.Str(g[1]), toInt(g[2])
	}
	if res, err = m.db.Query(ctx, `SELECT l.line_no, l.cost_kind, l.text, l.allocation, COALESCE(l.cost_group, ''), COALESCE(l.key_code, ''), COALESCE(l.scope, ''),
		COALESCE(l.user_no, ''), l.amount, l.ww_percent, COALESCE(k.apportionable, ?)
		FROM heatcost__cost l LEFT JOIN heatcost__cost_kind k ON k.company_code = l.company_code AND k.code = l.cost_kind
		WHERE l.company_code = ? AND l.run_id = ? ORDER BY l.sort_order, l.line_no`, true, cc, id); err != nil {
		return nil, err
	}
	for _, l := range res.Rows {
		c.lines = append(c.lines, calcLine{no: toInt(l[0]), kind: crud.Str(l[1]), text: crud.Str(l[2]), alloc: crud.Str(l[3]), group: crud.Str(l[4]),
			key: crud.Str(l[5]), scope: crud.Str(l[6]), user: crud.Str(l[7]), amount: toFloat(l[8]), wwPercent: toFloat(l[9]), hasPercent: isSet(l[9]),
			apportionable: crud.AsBool(l[10])})
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
func (c *calc) total(key, scope string) float64 {
	if scope == "" {
		scope = "*"
	}
	if v, ok := c.basis[[2]string{key, scope}]; ok {
		return v
	}
	sum := 0.0
	for _, u := range c.users {
		if c.inScope(u, scope) {
			sum += c.values[u.no][key]
		}
	}
	return sum
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
func (c *calc) distribute(block, group, text, key, scope string, amount float64, apportionable bool) {
	basis := c.total(key, scope)
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
		c.results = append(c.results, resultRow{user: u.no, block: block, group: group, text: text, key: key, scope: scope, unit: c.keyUnit(key),
			total: amount, basis: basis, price: price, value: v, share: v / basis * 100, amount: round2(amount * v / basis), apportionable: apportionable})
	}
}

// block: Heizung bzw. Warmwasser – ggf. auf Gruppen, dann Grund- und Verbrauchskosten.
func (c *calc) block(name string, amount float64, groups []string, groupKey string, base float64, areaKey, usageKey string) {
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
			if i == len(groups)-1 {
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
		c.distribute(name, "", fmt.Sprintf("%s – %s %% Grundkosten", label, germanNumber(base, 2)), areaKey, p.scope, grund, true)
		c.distribute(name, "", fmt.Sprintf("%s – %s %% Verbrauchskosten", label, germanNumber(100-base, 2)), usageKey, p.scope, round2(p.amount-grund), true)
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
	switch c.r.wwMethod {
	case wwFixed:
		share = c.r.wwFixed / 100
	default:
		if needShare && (!c.r.hasWWEnergy || !c.r.hasTotalEnergy || c.r.totalEnergy == 0) {
			return 0, 0, 0, crud.Invalid("Warmwasseranteil nach Wärmezähler: Energie für Warmwasser und Gesamtenergie an der Abrechnung eintragen")
		}
		if c.r.totalEnergy != 0 {
			share = c.r.wwEnergy / c.r.totalEnergy
		}
	}
	// 1. Kostenaufstellung
	plant, wwByGroup := 0.0, map[string]float64{}
	var groupOrder []string
	for _, l := range c.lines {
		total += l.amount
		c.step(stepRow{block: "Kosten", text: costText(l, c.cgroups), amount: l.amount})
		var part float64
		switch l.alloc {
		case allocSplit:
			p := share
			if l.hasPercent {
				p = l.wwPercent / 100
			}
			part = l.amount * p
		case allocHotwater:
			part = l.amount
		case allocHeating:
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
		}
		c.step(stepRow{block: blockWW, text: fmt.Sprintf("Warmwasseranteil %s %% (%s)", germanNumber(share*100, 4), basisText), amount: 0})
	}
	for _, g := range groupOrder {
		a := round2(wwByGroup[g])
		ww += a
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
	c.block(blockHeat, heat, c.r.heatGroups, c.r.heatGroupKey, c.r.heatBase, c.r.heatArea, c.r.heatUsage)
	c.block(blockWW, ww, c.r.wwGroups, c.r.wwGroupKey, c.r.wwBase, c.r.wwArea, c.r.wwUsage)
	// 3. eigene Schlüssel und direkte Kosten
	for _, l := range c.lines {
		switch l.alloc {
		case allocKey:
			text := l.text
			if n := c.scopeName(l.scope); n != "" && !strings.Contains(text, n) {
				text += " (" + n + ")"
			}
			c.distribute(c.cgroups[l.group], l.group, text, l.key, l.scope, l.amount, l.apportionable)
		case allocDirect:
			c.step(stepRow{block: c.cgroups[l.group], text: l.text + " – Nutzer " + l.user, amount: l.amount})
			c.results = append(c.results, resultRow{user: l.user, block: c.cgroups[l.group], group: l.group, text: l.text, total: l.amount,
				amount: l.amount, share: 100, apportionable: l.apportionable})
		}
	}
	return heat, ww, round2(total), nil
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
	if len(c.warnings) > 0 {
		msg += "\nHinweise:\n- " + strings.Join(c.warnings, "\n- ")
	}
	share := any(nil)
	if c.r.wwMethod == wwFixed {
		share = c.r.wwFixed
	} else if c.r.totalEnergy != 0 {
		share = c.r.wwEnergy / c.r.totalEnergy * 100
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range []string{"result", "step"} {
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
				basis, price, value, unit, share, amount, apportionable) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				cc, id, r.user, line[r.user], r.block, nilIfEmpty(r.group), r.text, r.total, nilIfEmpty(r.key), nilIfEmpty(r.scope),
				r.basis, r.price, r.value, nilIfEmpty(r.unit), r.share, r.amount, r.apportionable); err != nil {
				return err
			}
		}
		_, err := m.db.Exec(ctx, `UPDATE heatcost__run SET status = ?, ww_share = ?, total_cost = ?, heat_cost = ?, ww_cost = ?, message = ?, computed_at = ?
			WHERE company_code = ? AND run_id = ?`, runComputed, share, total, heat, ww, msg, time.Now().UTC().Format(time.RFC3339), cc, id)
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
