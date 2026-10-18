package contract

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Report (seit 0.20.0, Aufbau seit 0.21.0): Vertragsübersicht der laufenden
// Verträge (aktiv oder gekündigt, Laufzeit im Zeitraum).
//
//   - Objektstruktur aus den Immobilien: Wirtschaftseinheit → Gebäude →
//     Mietobjekt (Bestandteile eines Vertragsobjekts und geschnittene Flächen
//     darunter). Jeder Vertrag erscheint einmal – unter der obersten Einheit
//     seines Hauptobjekts, darin nach Vertragsart.
//   - Ein gewähltes Objekt schränkt auf es und alle darunter liegenden ein;
//     Vertragsarten sind mehrfach wählbar (leer = alle).
//   - Konditionen mit ihrer Gültigkeit im Zeitraum (jede Zeitscheibe eine Zeile),
//     Beträge im Zeitraum tagesgenau je Monat abgegrenzt; Zwischensumme nur je
//     oberster Einheit, dazu die Monatsübersicht.
//
// Ausgegeben wird über eine Vorlage des Moduls template mit der Verwendung
// CONTRACT_REPORT (z. B. PDF quer, Excel); Kopf- und Fußzeile kommen aus dem
// Geschäftspartner des Buchungskreises.

const (
	reportObject        = "ContractReport"
	usageContractReport = "CONTRACT_REPORT"
	usageLoanSchedule   = "LOAN_SCHEDULE"
	reportFormState     = "formState"
)

var reportDef = metamodel.ObjectDefinition{
	Name: reportObject, Title: "Vertragsübersicht", Icon: "icon-printer", FormState: reportFormState,
	Lifecycle: metamodel.Lifecycle{Type: metamodel.LifecycleImmutable},
	Fields: []metamodel.FieldDefinition{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Editable: true, ActionOnly: true, Lookup: lookupCC, Trigger: true},
		{Key: "contract_types", Label: "Vertragsarten (keine Auswahl = alle)", Type: tSel, Multiple: true, Editable: true, ActionOnly: true},
		{Key: "entity_id", Label: "Wirtschaftseinheit (leer = alle)", Short: "WE", Type: tText, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "BusinessEntity", ValueField: "entity_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "building_id", Label: "Gebäude (leer = alle)", Type: tText, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "object_id", Label: "Mietobjekt (leer = alle)", Type: tText, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "period_from", Label: "Zeitraum von", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "period_to", Label: "Zeitraum bis", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "template", Label: "Vorlage (Ausgabe)", Type: tSel, Required: true, Editable: true, ActionOnly: true},
	},
	Actions: []metamodel.ActionConfig{{Name: "run", Kind: metamodel.KindCustom, Label: "Übersicht erzeugen", FormState: true,
		Fields: []string{"company_code", "contract_types", "entity_id", "building_id", "object_id", "period_from", "period_to", "template"}}},
}

func (m *Module) registerReport(r *module.Router) {
	r.Object(reportObject).Section("Auswertungen").Describe(reportDef).
		Handle("run", m.reportAction).
		Handle(reportFormState, m.reportFormStateAction)
}

func (m *Module) reportFormStateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in metamodel.FormStateRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if in.Values == nil {
		in.Values = map[string]string{}
	}
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	set := func(k, v string) {
		if strings.TrimSpace(in.Values[k]) == "" && v != "" {
			st.Fields[k] = metamodel.FieldState{Value: &v}
		}
	}
	cc := strings.TrimSpace(in.Values["company_code"])
	if cc == "" {
		if ccs := m.companyCodes(ctx); len(ccs) == 1 {
			cc = ccs[0]
			set("company_code", cc)
		}
	}
	year := time.Now().Year()
	set("period_from", fmt.Sprintf("%d-01-01", year))
	set("period_to", fmt.Sprintf("%d-12-31", year))
	var types []metamodel.Option
	if cc != "" {
		if res, err := m.db.Query(ctx, `SELECT code, name FROM contract__contract_type WHERE company_code = ? ORDER BY code`, cc); err == nil {
			for _, r := range res.Rows {
				types = append(types, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[0]) + " – " + crud.Str(r[1])})
			}
		}
	}
	st.Fields["contract_types"] = metamodel.FieldState{Options: types}
	tmpls := reportapi.Templates(ctx, m.services, usageContractReport)
	tf := metamodel.FieldState{Options: tmpls}
	if len(tmpls) > 0 && strings.TrimSpace(in.Values["template"]) == "" {
		tf.Value = &tmpls[0].Value
	}
	st.Fields["template"] = tf
	if len(tmpls) > 0 {
		st.Message = "Laufende Verträge (aktiv oder gekündigt) im Zeitraum, je oberster Einheit (Wirtschaftseinheit, Gebäude, Mietobjekt) und Vertragsart; Beträge je Monat abgegrenzt. Ein gewähltes Objekt schließt alle darunter liegenden ein."
	} else {
		st.Message = "Keine Vorlage mit der Verwendung CONTRACT_REPORT (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt PDF und Excel an)."
	}
	return sdk.Response{Payload: st}, nil
}

// companyCodes: Buchungskreise, die der Benutzer sieht.
func (m *Module) companyCodes(ctx context.Context) []string {
	resp, err := m.services.Call(ctx, "CompanyCode", "list", map[string]any{"query": map[string]any{}})
	if err != nil {
		return nil
	}
	var l struct {
		Items []struct {
			Code string `json:"code"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return nil
	}
	var out []string
	for _, it := range l.Items {
		out = append(out, it.Code)
	}
	return out
}

// --- Objektstruktur ------------------------------------------------------------------

// node ist ein Objekt der Immobilien.
type node struct {
	Kind, ID, Label, Parent string // Parent: Schlüssel des übergeordneten Objekts ("" = oberstes)
	Order                   int    // Sortierung der obersten Einheiten: Wirtschaftseinheit, Gebäude, Mietobjekt
}

func nodeKey(kind, id string) string { return kind + "|" + id }

// objectTree: Objekte des Buchungskreises mit Eltern (Wirtschaftseinheit → Gebäude →
// Mietobjekt; Bestandteile unter dem Vertragsobjekt, geschnittene Flächen unter dem Pool).
func (m *Module) objectTree(ctx context.Context, cc string) map[string]*node {
	tree := map[string]*node{}
	list := func(object string) []map[string]any {
		resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": map[string]any{"company_code": cc}})
		if err != nil {
			return nil
		}
		var l struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) != nil {
			return nil
		}
		return l.Items
	}
	for _, e := range list("BusinessEntity") {
		id := crud.Str(e["entity_id"])
		tree[nodeKey("BusinessEntity", id)] = &node{Kind: "BusinessEntity", ID: id, Label: strings.TrimSpace("Wirtschaftseinheit " + id + " " + crud.Str(e["designation"]))}
	}
	for _, b := range list("Building") {
		id := crud.Str(b["building_id"])
		n := &node{Kind: "Building", ID: id, Label: strings.TrimSpace("Gebäude " + id + " " + crud.Str(b["designation"])), Order: 1}
		if e := crud.Str(b["entity_id"]); e != "" {
			n.Parent = nodeKey("BusinessEntity", e)
		}
		tree[nodeKey("Building", id)] = n
	}
	for _, r := range list("RentObject") {
		id := crud.Str(r["object_id"])
		n := &node{Kind: "RentObject", ID: id, Label: strings.TrimSpace(id + " " + crud.Str(r["designation"])), Order: 2}
		switch {
		case crud.Str(r["pool_id"]) != "":
			n.Parent = nodeKey("RentObject", crud.Str(r["pool_id"]))
		case crud.Str(r["building_id"]) != "":
			n.Parent = nodeKey("Building", crud.Str(r["building_id"]))
		case crud.Str(r["entity_id"]) != "":
			n.Parent = nodeKey("BusinessEntity", crud.Str(r["entity_id"]))
		}
		tree[nodeKey("RentObject", id)] = n
	}
	// Bestandteile eines Vertragsobjekts gehören zu ihm (heute gültig).
	today := time.Now().Format(time.DateOnly)
	for _, c := range list("CompositeItem") {
		if from, to := dateOf(c["valid_from"]), dateOf(c["valid_to"]); (from != "" && from > today) || (to != "" && to < today) {
			continue
		}
		if n := tree[nodeKey("RentObject", crud.Str(c["object_id"]))]; n != nil && tree[nodeKey("RentObject", crud.Str(c["composite_id"]))] != nil {
			n.Parent = nodeKey("RentObject", crud.Str(c["composite_id"]))
		}
	}
	// Eltern, die es nicht gibt, und Kreise lösen.
	for _, n := range tree {
		seen := map[*node]bool{n: true}
		for p := n; p.Parent != ""; {
			pp := tree[p.Parent]
			if pp == nil || seen[pp] {
				p.Parent = ""
				break
			}
			seen[pp] = true
			p = pp
		}
	}
	return tree
}

// topOf: oberstes Objekt über key (key selbst, wenn es keins gibt).
func topOf(tree map[string]*node, key string) string {
	for i := 0; i < 50; i++ {
		n := tree[key]
		if n == nil || n.Parent == "" {
			return key
		}
		key = n.Parent
	}
	return key
}

// under: key ist root oder liegt darunter.
func under(tree map[string]*node, key, root string) bool {
	for i := 0; i < 50 && key != ""; i++ {
		if key == root {
			return true
		}
		n := tree[key]
		if n == nil {
			return false
		}
		key = n.Parent
	}
	return false
}

// --- Daten der Übersicht ------------------------------------------------------------

type reportFilter struct {
	CC, From, To string
	Types        []string
	Root         string // gewähltes Objekt (Art|ID), "" = alle
}

type rContract struct {
	ID, Type, Status, Direction, Currency, Partner, From, To, Designation string
}

type rCondition struct {
	Contract, Type, Name, Object, From, To, Calc, Measure, Freq, Note, Due, TaxCode, Dependent, UnitPrice, Rounding string
	Amount, Sort                                                                               int64
}

type rObject struct {
	Contract, Type, ID, From, To string
	Main                         bool
}

// month ist ein Kalendermonat im Zeitraum (From, To: Teil im Zeitraum).
type month struct {
	Key, Label, From, To string
	Days                 int // Tage des ganzen Monats
}

var monthNamesDE = []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

func monthsOf(from, to string) []month {
	f, err1 := time.Parse(time.DateOnly, from)
	t, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil {
		return nil
	}
	var out []month
	for c := time.Date(f.Year(), f.Month(), 1, 0, 0, 0, 0, time.UTC); !c.After(t); c = c.AddDate(0, 1, 0) {
		end := c.AddDate(0, 1, -1)
		out = append(out, month{Key: c.Format("2006-01"), Label: fmt.Sprintf("%s %d", monthNamesDE[c.Month()-1], c.Year()),
			From: maxStr(c.Format(time.DateOnly), from), To: minStr(end.Format(time.DateOnly), to), Days: end.Day()})
	}
	return out
}

func maxStr(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func minStr(a, b string) string {
	if a < b {
		return a
	}
	return b
}

// days: Tage von from bis to (beide eingeschlossen; 0, wenn leer).
func days(from, to string) int {
	f, err1 := time.Parse(time.DateOnly, from)
	t, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil || t.Before(f) {
		return 0
	}
	return int(t.Sub(f).Hours()/24) + 1
}

// reportData sammelt die Übersicht (Daten für die Vorlage).
func (m *Module) reportData(ctx context.Context, f reportFilter) (map[string]any, error) {
	q := `SELECT contract_id, contract_type, status, direction, currency, partner_id, valid_from, valid_to, designation
		FROM contract__contract WHERE company_code = ? AND status IN (?, ?) AND valid_from <= ? AND valid_to >= ?`
	args := []any{f.CC, statusActive, statusTerminated, f.To, f.From}
	if len(f.Types) > 0 {
		q += ` AND contract_type IN (?` + strings.Repeat(", ?", len(f.Types)-1) + `)`
		for _, t := range f.Types {
			args = append(args, t)
		}
	}
	res, err := m.db.Query(ctx, q+` ORDER BY contract_id`, args...)
	if err != nil {
		return nil, err
	}
	contracts := map[string]*rContract{}
	var ids []string
	for _, r := range res.Rows {
		c := &rContract{ID: crud.Str(r[0]), Type: crud.Str(r[1]), Status: crud.Str(r[2]), Direction: crud.Str(r[3]), Currency: crud.Str(r[4]),
			Partner: crud.Str(r[5]), From: dateOf(r[6]), To: dateOf(r[7]), Designation: crud.Str(r[8])}
		contracts[c.ID] = c
		ids = append(ids, c.ID)
	}
	res, err = m.db.Query(ctx, `SELECT contract_id, object_type, object_id, is_main, valid_from, valid_to FROM contract__object
		WHERE company_code = ? AND valid_from <= ? AND valid_to >= ? ORDER BY contract_id, valid_from`, f.CC, f.To, f.From)
	if err != nil {
		return nil, err
	}
	objects := map[string][]rObject{}
	for _, r := range res.Rows {
		o := rObject{Contract: crud.Str(r[0]), Type: crud.Str(r[1]), ID: crud.Str(r[2]), Main: crud.AsBool(r[3]), From: dateOf(r[4]), To: dateOf(r[5])}
		if contracts[o.Contract] != nil {
			objects[o.Contract] = append(objects[o.Contract], o)
		}
	}
	tree := m.objectTree(ctx, f.CC)
	// Hauptobjekt je Vertrag (im Zeitraum): Hauptobjekt, sonst ein Mietobjekt, sonst das erste.
	mainOf := map[string]string{}
	for _, id := range ids {
		var pick *rObject
		for i, o := range objects[id] {
			switch {
			case pick == nil, o.Main && !pick.Main, o.Main == pick.Main && o.Type == "RentObject" && pick.Type != "RentObject":
				pick = &objects[id][i]
			}
		}
		if pick != nil {
			mainOf[id] = nodeKey(pick.Type, pick.ID)
		}
	}
	// Einschränkung auf ein Objekt und alles darunter (irgendein Objekt des Vertrags).
	if f.Root != "" {
		var keep []string
		for _, id := range ids {
			for _, o := range objects[id] {
				if under(tree, nodeKey(o.Type, o.ID), f.Root) {
					keep = append(keep, id)
					break
				}
			}
		}
		ids = keep
	}
	res, err = m.db.Query(ctx, `SELECT k.contract_id, k.condition_type, t.name, COALESCE(k.object_id, ''), k.valid_from, k.valid_to, k.calc_method, k.amount,
		COALESCE(k.measurement_type, ''), k.frequency, COALESCE(k.note, ''), t.sort_order, k.due_date, COALESCE(t.tax_code, ''), COALESCE(k.dependent_type, ''),
		COALESCE(k.unit_price, ''), COALESCE(k.rounding_rule, '')
		FROM contract__condition k JOIN contract__condition_type t ON t.company_code = k.company_code AND t.code = k.condition_type`+templateJoins+`
		WHERE k.company_code = ? AND k.valid_from <= ? AND k.valid_to >= ? AND NOT `+templateSQL+`
		ORDER BY t.sort_order, k.condition_type, k.position, k.valid_from`, f.CC, f.To, f.From)
	if err != nil {
		return nil, err
	}
	conditions := map[string][]rCondition{}
	for _, r := range res.Rows {
		k := rCondition{Contract: crud.Str(r[0]), Type: crud.Str(r[1]), Name: crud.Str(r[2]), Object: crud.Str(r[3]), From: dateOf(r[4]), To: dateOf(r[5]),
			Calc: crud.Str(r[6]), Amount: toInt(r[7]), Measure: crud.Str(r[8]), Freq: crud.Str(r[9]), Note: crud.Str(r[10]), Sort: toInt(r[11]), Due: dateOf(r[12]),
			TaxCode: crud.Str(r[13]), Dependent: crud.Str(r[14]), UnitPrice: crud.Str(r[15]), Rounding: crud.Str(r[16])}
		conditions[k.Contract] = append(conditions[k.Contract], k)
	}
	typeNames := map[string]string{}
	if res, err := m.db.Query(ctx, `SELECT code, name FROM contract__contract_type WHERE company_code = ?`, f.CC); err == nil {
		for _, r := range res.Rows {
			typeNames[crud.Str(r[0])] = crud.Str(r[1])
		}
	}
	partners := map[string]string{}
	partnerOf := func(id string) string {
		if id == "" {
			return ""
		}
		if n, ok := partners[id]; ok {
			return n
		}
		n := firstNonEmpty(reportapi.DisplayName(ctx, m.services, id), id)
		partners[id] = n
		return n
	}
	decimals := map[string]int{}
	major := func(minor int64, cur string) float64 {
		d, ok := decimals[cur]
		if !ok {
			d = m.currencyDecimals(ctx, cur)
			decimals[cur] = d
		}
		return float64(minor) / math.Pow10(d)
	}
	freqLabel := map[string]string{}
	for _, o := range frequencyOptions {
		freqLabel[o.Value] = o.Label
	}
	months := monthsOf(f.From, f.To)
	labelOf := func(key string) string {
		if n := tree[key]; n != nil {
			return n.Label
		}
		if key == "" {
			return "Ohne Objekt"
		}
		kind, id, _ := strings.Cut(key, "|")
		return strings.TrimSpace(map[string]string{"Building": "Gebäude ", "BusinessEntity": "Wirtschaftseinheit "}[kind] + id)
	}

	// Gruppen: oberste Einheit → Vertragsart → Verträge
	type tGroup struct {
		code, name string
		contracts  []map[string]any
	}
	type uGroup struct {
		key, label        string
		order, count      int
		in, out           float64
		monthIn, monthOut []float64
		types             map[string]*tGroup
	}
	units := map[string]*uGroup{}
	totalIn, totalOut := make([]float64, len(months)), make([]float64, len(months))
	active := make([]int, len(months))
	for _, id := range ids {
		c := contracts[id]
		top := ""
		if mainOf[id] != "" {
			top = topOf(tree, mainOf[id])
		}
		u := units[top]
		if u == nil {
			order := 3
			if n := tree[top]; n != nil {
				order = n.Order
			}
			u = &uGroup{key: top, label: labelOf(top), order: order, monthIn: make([]float64, len(months)), monthOut: make([]float64, len(months)),
				types: map[string]*tGroup{}}
			units[top] = u
		}
		u.count++
		for i, mo := range months {
			if c.From <= mo.To && c.To >= mo.From {
				active[i]++
			}
		}
		in := c.Direction == dirReceivable
		var conds []any
		var cIn, cOut float64
		var depConds []rCondition
		var baseRows []reportBase
		for _, k := range conditions[id] {
			if k.Calc == calcDependent {
				depConds = append(depConds, k)
				continue
			}
			from, to := maxStr(k.From, maxStr(f.From, c.From)), minStr(k.To, minStr(f.To, c.To))
			if from > to {
				continue
			}
			amount, name := k.Amount, k.Name
			if k.Calc == "PER_UNIT" {
				obj := k.Object
				if obj == "" {
					_, obj, _ = strings.Cut(mainOf[id], "|")
				}
				if v, err := m.measurementOf(ctx, f.CC, obj, k.Measure, from); err == nil {
					amount = scale(k.Amount, v)
					if p, err := pricing.ParseDecimal(k.UnitPrice); err == nil && k.UnitPrice != "" { // genau: Einzelpreis × Bemessung
						dec := m.currencyDecimals(ctx, c.Currency)
						amount = m.roundingFor(ctx, f.CC, k.Type, k.Rounding, dec).Minor(new(big.Rat).Mul(v, p), dec)
					}
					name += fmt.Sprintf(" (je %s × %s)", k.Measure, v.FloatString(2))
				} else {
					name += " (je " + k.Measure + ", Bemessung fehlt)"
				}
			}
			amt := major(amount, c.Currency)
			monthly := round2(amt * monthlyFactor(k.Freq))
			// je Monat abgegrenzt: Monatsbetrag × Anteil der Tage; einmalig im Monat der Fälligkeit
			var sum float64
			monthVals := make([]float64, len(months))
			for i, mo := range months {
				var v float64
				if k.Freq == "ONCE" {
					if due := firstNonEmpty(k.Due, k.From); due >= mo.From && due <= mo.To && due >= from && due <= to {
						v = amt
					}
				} else if d := days(maxStr(from, mo.From), minStr(to, mo.To)); d > 0 {
					v = monthly * float64(d) / float64(mo.Days)
				}
				monthVals[i] = v
				if v == 0 {
					continue
				}
				sum += v
				if in {
					u.monthIn[i] += v
					totalIn[i] += v
				} else {
					u.monthOut[i] += v
					totalOut[i] += v
				}
			}
			sum = round2(sum)
			baseRows = append(baseRows, reportBase{k: k, monthly: monthly, sum: sum, months: monthVals})
			row := map[string]any{"code": k.Type, "name": name, "amount": amt, "frequency": freqLabel[k.Freq],
				"valid_from": from, "valid_to": to, "partial": from > maxStr(f.From, c.From) || to < minStr(f.To, c.To),
				"monthly_in": 0.0, "monthly_out": 0.0, "period_in": 0.0, "period_out": 0.0, "note": k.Note, "object": k.Object}
			if in {
				row["monthly_in"], row["period_in"] = monthly, sum
				cIn += sum
			} else {
				row["monthly_out"], row["period_out"] = monthly, sum
				cOut += sum
			}
			conds = append(conds, row)
		}
		// nachgelagerte Konditionen aus den Basiswerten (je Monat, Monatsbetrag, Zeitraum)
		for _, dr := range m.reportDependents(ctx, f.CC, depConds, baseRows, months) {
			share := dr.effect == pricing.EffectShare // im Basisbetrag enthalten: nicht noch einmal summieren
			row := map[string]any{"code": dr.k.Type, "name": dr.name, "amount": dr.monthly, "frequency": freqLabel["MONTHLY"],
				"valid_from": maxStr(dr.k.From, maxStr(f.From, c.From)), "valid_to": minStr(dr.k.To, minStr(f.To, c.To)), "partial": false,
				"monthly_in": 0.0, "monthly_out": 0.0, "period_in": 0.0, "period_out": 0.0, "note": dr.k.Note, "object": dr.k.Object, "share": share}
			if in {
				row["monthly_in"], row["period_in"] = dr.monthly, dr.sum
			} else {
				row["monthly_out"], row["period_out"] = dr.monthly, dr.sum
			}
			if !share {
				for i, v := range dr.months {
					if in {
						u.monthIn[i] += v
						totalIn[i] += v
					} else {
						u.monthOut[i] += v
						totalOut[i] += v
					}
				}
				if in {
					cIn += dr.sum
				} else {
					cOut += dr.sum
				}
			}
			conds = append(conds, row)
		}
		u.in, u.out = u.in+cIn, u.out+cOut
		t := u.types[c.Type]
		if t == nil {
			t = &tGroup{code: c.Type, name: firstNonEmpty(typeNames[c.Type], c.Type)}
			u.types[c.Type] = t
		}
		obj := mainOf[id]
		t.contracts = append(t.contracts, map[string]any{"id": c.ID, "designation": c.Designation, "type": c.Type, "type_name": t.name,
			"partner": partnerOf(c.Partner), "partner_id": c.Partner, "valid_from": c.From, "valid_to": c.To, "open_end": c.To >= "9999-12-31",
			"status": c.Status, "direction": c.Direction, "currency": c.Currency, "object": labelOf(obj), "object_key": obj,
			"period_in": round2(cIn), "period_out": round2(cOut), "conditions": conds})
	}

	// sortieren: Einheiten (Wirtschaftseinheit, Gebäude, Mietobjekt, ohne), Arten, Verträge nach Objekt
	var ukeys []string
	for k := range units {
		ukeys = append(ukeys, k)
	}
	sort.Slice(ukeys, func(i, j int) bool {
		a, b := units[ukeys[i]], units[ukeys[j]]
		if a.order != b.order {
			return a.order < b.order
		}
		return a.key < b.key
	})
	var us []any
	var sumIn, sumOut float64
	for _, k := range ukeys {
		u := units[k]
		var tkeys []string
		for tk := range u.types {
			tkeys = append(tkeys, tk)
		}
		sort.Strings(tkeys)
		var ts []any
		for _, tk := range tkeys {
			t := u.types[tk]
			sort.SliceStable(t.contracts, func(i, j int) bool {
				oi, oj := crud.Str(t.contracts[i]["object_key"]), crud.Str(t.contracts[j]["object_key"])
				if oi != oj {
					return oi < oj
				}
				return crud.Str(t.contracts[i]["id"]) < crud.Str(t.contracts[j]["id"])
			})
			cs := make([]any, len(t.contracts))
			for i, c := range t.contracts {
				cs[i] = c
			}
			ts = append(ts, map[string]any{"code": t.code, "name": t.name, "contracts": cs})
		}
		var ms []any
		for i, mo := range months {
			ms = append(ms, map[string]any{"month": mo.Key, "label": mo.Label, "in": round2(u.monthIn[i]), "out": round2(u.monthOut[i])})
		}
		sumIn, sumOut = sumIn+u.in, sumOut+u.out
		us = append(us, map[string]any{"key": k, "label": u.label, "contracts": u.count, "period_in": round2(u.in), "period_out": round2(u.out),
			"types": ts, "months": ms})
	}
	var ms []any
	for i, mo := range months {
		ms = append(ms, map[string]any{"month": mo.Key, "label": mo.Label, "contracts": active[i], "in": round2(totalIn[i]), "out": round2(totalOut[i]),
			"balance": round2(totalIn[i] - totalOut[i])})
	}

	parts := []string{"Buchungskreis " + f.CC}
	if len(f.Types) > 0 {
		var names []string
		for _, t := range f.Types {
			names = append(names, strings.TrimSpace(t+" "+typeNames[t]))
		}
		parts = append(parts, "Vertragsarten "+strings.Join(names, ", "))
	} else {
		parts = append(parts, "alle Vertragsarten")
	}
	if f.Root != "" {
		parts = append(parts, labelOf(f.Root)+" mit allen Objekten darunter")
	}
	parts = append(parts, germanDate(f.From)+" – "+germanDate(f.To))
	return map[string]any{
		"title": "Vertragsübersicht", "subtitle": strings.Join(parts, " · "), "company": reportapi.Company(ctx, m.services, f.CC),
		"generated": time.Now().Format(time.DateOnly),
		"filter":    map[string]any{"company_code": f.CC, "contract_types": f.Types, "object": f.Root, "from": f.From, "to": f.To},
		"units":     us, "months": ms, "contracts": len(ids),
		"period_in": round2(sumIn), "period_out": round2(sumOut), "period_balance": round2(sumIn - sumOut),
	}, nil
}

// monthlyFactor: Anteil je Monat (einmalige Konditionen zählen nicht).
func monthlyFactor(freq string) float64 {
	switch freq {
	case "MONTHLY":
		return 1
	case "BIMONTHLY":
		return 1.0 / 2
	case "QUARTERLY":
		return 1.0 / 3
	case "HALF_YEARLY":
		return 1.0 / 6
	case "YEARLY":
		return 1.0 / 12
	}
	return 0
}

// dateOf: Datum einer Spalte als JJJJ-MM-TT (Treiber liefern Text oder Zeit).
func dateOf(v any) string {
	d, _ := crud.ParseDate(v)
	return d
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// --- Aktion ------------------------------------------------------------------------

func (m *Module) reportAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			CC       string `json:"company_code"`
			Types    string `json:"contract_types"`
			Entity   string `json:"entity_id"`
			Building string `json:"building_id"`
			Object   string `json:"object_id"`
			From     string `json:"period_from"`
			To       string `json:"period_to"`
			Template string `json:"template"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d := in.Data
	f := reportFilter{CC: strings.TrimSpace(d.CC)}
	for _, t := range strings.Split(d.Types, ",") {
		if t = trimUpper(t); t != "" {
			f.Types = append(f.Types, t)
		}
	}
	switch {
	case strings.TrimSpace(d.Object) != "":
		f.Root = nodeKey("RentObject", strings.TrimSpace(d.Object))
	case strings.TrimSpace(d.Building) != "":
		f.Root = nodeKey("Building", strings.TrimSpace(d.Building))
	case strings.TrimSpace(d.Entity) != "":
		f.Root = nodeKey("BusinessEntity", strings.TrimSpace(d.Entity))
	}
	if f.CC == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis fehlt")
	}
	var err error
	if f.From, err = crud.ParseDate(d.From); err != nil || f.From == "" {
		return sdk.Response{}, crud.Invalid("Zeitraum von: Datum fehlt")
	}
	if f.To, err = crud.ParseDate(d.To); err != nil || f.To == "" {
		return sdk.Response{}, crud.Invalid("Zeitraum bis: Datum fehlt")
	}
	if f.To < f.From {
		return sdk.Response{}, crud.Invalid("Zeitraum: „bis“ liegt vor „von“")
	}
	if len(monthsOf(f.From, f.To)) > 120 {
		return sdk.Response{}, crud.Invalid("Zeitraum höchstens 10 Jahre")
	}
	if !strings.Contains(d.Template, "|") {
		return sdk.Response{}, crud.Invalid("Vorlage fehlt")
	}
	if ok, err := sdk.Authorize(ctx, "Contract", metamodel.ActionRead, sdk.Attrs{sdk.AttrCompanyCode: f.CC}); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Leseberechtigung für Verträge im Buchungskreis %s", sdk.ErrPermissionDenied, f.CC)
	}
	data, err := m.reportData(ctx, f)
	if err != nil {
		return sdk.Response{}, err
	}
	// Monatsübersicht auf dem Bildschirm
	var rows [][]string
	ms, _ := data["months"].([]any)
	for _, x := range ms {
		mm := x.(map[string]any)
		rows = append(rows, []string{crud.Str(mm["label"]), fmt.Sprint(mm["contracts"]), reportapi.MoneyDE(mm["in"].(float64)),
			reportapi.MoneyDE(mm["out"].(float64)), reportapi.MoneyDE(mm["balance"].(float64))})
	}
	rows = append(rows, []string{"Summe Zeitraum", fmt.Sprint(data["contracts"]), reportapi.MoneyDE(data["period_in"].(float64)),
		reportapi.MoneyDE(data["period_out"].(float64)), reportapi.MoneyDE(data["period_balance"].(float64))})
	units, _ := data["units"].([]any)
	out, err := reportapi.Render(ctx, m.services, f.CC, d.Template, fmt.Sprintf("Vertragsübersicht: %d Verträge in %d Einheiten", data["contracts"], len(units)), data)
	if err != nil {
		return sdk.Response{}, err
	}
	out["table"] = map[string]any{"columns": []string{"Monat", "Verträge", "Einnahmen", "Ausgaben", "Saldo"}, "rows": rows}
	return sdk.Response{Payload: out}, nil
}

// reportBase: Werte einer Kondition im Bericht (Basis nachgelagerter Konditionen).
type reportBase struct {
	k            rCondition
	monthly, sum float64
	months       []float64
}

type reportDep struct {
	k            rCondition
	name, effect string
	monthly, sum float64
	months       []float64
}

// reportDependents: nachgelagerte Konditionen eines Vertrags aus den Basiswerten.
func (m *Module) reportDependents(ctx context.Context, cc string, deps []rCondition, base []reportBase, months []month) []reportDep {
	if len(deps) == 0 || len(base) == 0 {
		return nil
	}
	var defs []pricing.Dependent
	byCode := map[string]rCondition{}
	for _, k := range deps {
		d, err := m.dependentTypeOf(ctx, cc, k.Dependent)
		if err != nil {
			continue
		}
		d.Name = firstNonEmpty(k.Note, d.Name, k.Name)
		defs = append(defs, d)
		byCode[d.Code] = k
	}
	apply := func(val func(b reportBase) float64, at string) map[string]float64 {
		var lines []pricing.Line
		for i, b := range base {
			lines = append(lines, pricing.Line{Key: fmt.Sprint(i), Kinds: []string{b.k.Type}, TaxCode: b.k.TaxCode, Amount: int64(math.Round(val(b) * 100))})
		}
		var active []pricing.Dependent
		for _, d := range defs {
			if k := byCode[d.Code]; at == "" || (k.From <= at && at <= k.To) {
				active = append(active, d)
			}
		}
		res, err := pricing.Apply(active, lines, 2, func(code string) (*big.Rat, error) {
			r, _, _, err := pricing.New(m.services).TaxRate(ctx, code)
			return r, err
		}, func(code string) pricing.Rounding { return pricing.New(m.services).Rounding(ctx, cc, code, 2) })
		out := map[string]float64{}
		if err != nil {
			return out
		}
		for _, r := range res {
			out[r.Dependent.Code] += float64(r.Amount) / 100
		}
		return out
	}
	monthly := apply(func(b reportBase) float64 { return b.monthly }, "")
	perMonth := make([]map[string]float64, len(months))
	for i, mo := range months {
		perMonth[i] = apply(func(b reportBase) float64 { return b.months[i] }, mo.From)
	}
	var out []reportDep
	for _, d := range defs {
		r := reportDep{k: byCode[d.Code], name: d.Name, effect: d.Effect, monthly: round2(monthly[d.Code]), months: make([]float64, len(months))}
		for i := range months {
			r.months[i] = perMonth[i][d.Code]
			r.sum += r.months[i]
		}
		r.sum = round2(r.sum)
		out = append(out, r)
	}
	return out
}
