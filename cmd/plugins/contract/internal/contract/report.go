package contract

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Report (seit 0.20.0): Vertragsübersicht der laufenden Verträge (aktiv oder
// gekündigt, Laufzeit im Zeitraum) – wahlweise einer Vertragsart, eines
// Gebäudes oder Mietobjekts. Ändern sich Verträge, Objekte oder Konditionen im
// Zeitraum, entsteht je Abschnitt gleicher Konditionen eine eigene Übersicht,
// nach Mietobjekt (Hauptobjekt des Vertrags) gruppiert, mit Summen je
// Mietobjekt und Abschnitt (monatlich, Einnahmen und Ausgaben getrennt).
// Ausgegeben wird über eine Vorlage des Moduls template mit der Verwendung
// CONTRACT_REPORT (z. B. PDF quer, Excel); Kopf- und Fußzeile kommen aus dem
// Geschäftspartner des Buchungskreises.

const (
	reportObject        = "ContractReport"
	usageContractReport = "CONTRACT_REPORT"
	maxReportPeriods    = 120
	reportFormState     = "formState"
)

var reportDef = metamodel.ObjectDefinition{
	Name: reportObject, Title: "Vertragsübersicht", Icon: "icon-printer", FormState: reportFormState,
	Lifecycle: metamodel.Lifecycle{Type: metamodel.LifecycleImmutable},
	Fields: []metamodel.FieldDefinition{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Editable: true, ActionOnly: true, Lookup: lookupCC, Trigger: true},
		{Key: "contract_type", Label: "Vertragsart (leer = alle)", Type: tSel, Editable: true, ActionOnly: true},
		{Key: "building_id", Label: "Gebäude (leer = alle)", Type: tText, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "object_id", Label: "Mietobjekt (leer = alle)", Type: tText, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "period_from", Label: "Zeitraum von", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "period_to", Label: "Zeitraum bis", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "template", Label: "Vorlage (Ausgabe)", Type: tSel, Required: true, Editable: true, ActionOnly: true},
	},
	Actions: []metamodel.ActionConfig{{Name: "run", Kind: metamodel.KindCustom, Label: "Übersicht erzeugen", FormState: true,
		Fields: []string{"company_code", "contract_type", "building_id", "object_id", "period_from", "period_to", "template"}}},
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
	types := []metamodel.Option{{Value: "", Label: "alle Vertragsarten"}}
	if cc != "" {
		if res, err := m.db.Query(ctx, `SELECT code, name FROM contract__contract_type WHERE company_code = ? ORDER BY code`, cc); err == nil {
			for _, r := range res.Rows {
				types = append(types, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[0]) + " – " + crud.Str(r[1])})
			}
		}
	}
	st.Fields["contract_type"] = metamodel.FieldState{Options: types, Value: st.Fields["contract_type"].Value}
	tmpls := reportapi.Templates(ctx, m.services, usageContractReport)
	st.Fields["template"] = metamodel.FieldState{Options: tmpls}
	if len(tmpls) > 0 {
		set("template", tmpls[0].Value)
		st.Fields["template"] = metamodel.FieldState{Options: tmpls, Value: st.Fields["template"].Value}
		st.Message = "Laufende Verträge (aktiv oder gekündigt) im Zeitraum, nach Mietobjekt; ändert sich etwas im Zeitraum, je Abschnitt eine eigene Übersicht."
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

// --- Daten der Übersicht ------------------------------------------------------------

type reportFilter struct {
	CC, Type, Building, Object, From, To string
}

type rContract struct {
	ID, Type, Status, Direction, Currency, Partner, From, To, Designation string
}

type rCondition struct {
	Contract, Type, Name, Object, From, To, Calc, Measure, Freq, Note string
	Amount                                                            int64
	Sort                                                              int64
}

type rObject struct {
	Contract, Type, ID, From, To string
	Main                         bool
}

// reportData sammelt die Übersicht (Daten für die Vorlage) und die Kennzahlen je Abschnitt.
func (m *Module) reportData(ctx context.Context, f reportFilter) (map[string]any, error) {
	q := `SELECT contract_id, contract_type, status, direction, currency, partner_id, valid_from, valid_to, designation
		FROM contract__contract WHERE company_code = ? AND status IN (?, ?) AND valid_from <= ? AND valid_to >= ?`
	args := []any{f.CC, statusActive, statusTerminated, f.To, f.From}
	if f.Type != "" {
		q += ` AND contract_type = ?`
		args = append(args, f.Type)
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
	// Objekte der Verträge
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
	rentObjects, buildings := m.reportObjects(ctx, f.CC)
	// Einschränkung auf Gebäude oder Mietobjekt
	if f.Building != "" || f.Object != "" {
		allowed := map[string]bool{}
		if f.Object != "" {
			allowed[f.Object] = true
		} else {
			allowed[f.Building] = true
			for id, ro := range rentObjects {
				if ro.Building == f.Building {
					allowed[id] = true
				}
			}
		}
		var keep []string
		for _, id := range ids {
			for _, o := range objects[id] {
				if allowed[o.ID] {
					keep = append(keep, id)
					break
				}
			}
		}
		ids = keep
	}
	// Konditionen (ohne Rechnungsvorlagen)
	res, err = m.db.Query(ctx, `SELECT c.contract_id, c.condition_type, t.name, COALESCE(c.object_id, ''), c.valid_from, c.valid_to, c.calc_method, c.amount,
		COALESCE(c.measurement_type, ''), c.frequency, COALESCE(c.note, ''), t.sort_order
		FROM contract__condition c JOIN contract__condition_type t ON t.company_code = c.company_code AND t.code = c.condition_type
		WHERE c.company_code = ? AND c.valid_from <= ? AND c.valid_to >= ? AND t.is_template = ?
		ORDER BY t.sort_order, c.condition_type, c.valid_from`, f.CC, f.To, f.From, false)
	if err != nil {
		return nil, err
	}
	conditions := map[string][]rCondition{}
	for _, r := range res.Rows {
		k := rCondition{Contract: crud.Str(r[0]), Type: crud.Str(r[1]), Name: crud.Str(r[2]), Object: crud.Str(r[3]), From: dateOf(r[4]), To: dateOf(r[5]),
			Calc: crud.Str(r[6]), Amount: toInt(r[7]), Measure: crud.Str(r[8]), Freq: crud.Str(r[9]), Note: crud.Str(r[10]), Sort: toInt(r[11])}
		conditions[k.Contract] = append(conditions[k.Contract], k)
	}
	typeNames := map[string]string{}
	if res, err := m.db.Query(ctx, `SELECT code, name FROM contract__contract_type WHERE company_code = ?`, f.CC); err == nil {
		for _, r := range res.Rows {
			typeNames[crud.Str(r[0])] = crud.Str(r[1])
		}
	}

	// Abschnitte: Grenzen, an denen ein Vertrag, Objekt oder eine Kondition beginnt oder endet
	cuts := map[string]bool{f.From: true}
	mark := func(from, to string) {
		if from > f.From && from <= f.To {
			cuts[from] = true
		}
		if n := nextDay(to); to < f.To && n > f.From {
			cuts[n] = true
		}
	}
	for _, id := range ids {
		c := contracts[id]
		mark(c.From, c.To)
		for _, o := range objects[id] {
			mark(o.From, o.To)
		}
		for _, k := range conditions[id] {
			mark(k.From, k.To)
		}
	}
	starts := make([]string, 0, len(cuts))
	for d := range cuts {
		starts = append(starts, d)
	}
	sort.Strings(starts)
	if len(starts) > maxReportPeriods {
		return nil, crud.Invalid("%d Abschnitte im Zeitraum – bitte Zeitraum, Vertragsart oder Objekt einschränken (höchstens %d)", len(starts), maxReportPeriods)
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

	var periods []any
	for i, ps := range starts {
		pe := f.To
		if i+1 < len(starts) {
			pe = prevDay(starts[i+1])
		}
		type group struct {
			key, label string
			in, out    float64
			contracts  []any
		}
		groups := map[string]*group{}
		count := 0
		var pin, pout float64
		for _, id := range ids {
			c := contracts[id]
			if c.From > ps || c.To < pe {
				continue
			}
			count++
			okey, olabel := groupOf(objects[id], ps, rentObjects, buildings)
			g := groups[okey]
			if g == nil {
				g = &group{key: okey, label: olabel}
				groups[okey] = g
			}
			var conds []any
			for _, k := range conditions[id] {
				if k.From > ps || k.To < pe {
					continue
				}
				amount := k.Amount
				name := k.Name
				if k.Calc == "PER_UNIT" {
					obj := firstNonEmpty(k.Object, mainObjectAt(objects[id], ps))
					if v, err := m.measurementOf(ctx, f.CC, obj, k.Measure, ps); err == nil {
						amount = scale(k.Amount, v)
						name += fmt.Sprintf(" (je %s × %s)", k.Measure, v.FloatString(2))
					} else {
						name += " (je " + k.Measure + ", Bemessung fehlt)"
					}
				}
				amt := major(amount, c.Currency)
				monthly := round2(amt * monthlyFactor(k.Freq))
				in, out := 0.0, 0.0
				if c.Direction == dirReceivable {
					in = monthly
				} else {
					out = monthly
				}
				g.in, g.out = g.in+in, g.out+out
				pin, pout = pin+in, pout+out
				conds = append(conds, map[string]any{"code": k.Type, "name": name, "amount": amt, "frequency": freqLabel[k.Freq],
					"monthly_in": in, "monthly_out": out, "note": k.Note, "object": k.Object})
			}
			g.contracts = append(g.contracts, map[string]any{"id": c.ID, "designation": c.Designation, "type": c.Type, "type_name": firstNonEmpty(typeNames[c.Type], c.Type),
				"partner": partnerOf(c.Partner), "partner_id": c.Partner, "valid_from": c.From, "valid_to": c.To, "open_end": c.To >= "9999-12-31",
				"status": c.Status, "direction": c.Direction, "currency": c.Currency, "conditions": conds})
		}
		var keys []string
		for k := range groups {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var gs []any
		for _, k := range keys {
			g := groups[k]
			gs = append(gs, map[string]any{"key": strings.SplitN(g.key, "|", 2)[1], "label": g.label, "monthly_in": round2(g.in), "monthly_out": round2(g.out),
				"contracts": g.contracts})
		}
		if count == 0 {
			continue
		}
		periods = append(periods, map[string]any{"from": ps, "to": pe, "sheet": sheetName(ps, pe), "contracts": count,
			"monthly_in": round2(pin), "monthly_out": round2(pout), "groups": gs})
	}

	company := reportapi.Company(ctx, m.services, f.CC)
	parts := []string{"Buchungskreis " + f.CC}
	if f.Type != "" {
		parts = append(parts, "Vertragsart "+f.Type+" "+typeNames[f.Type])
	} else {
		parts = append(parts, "alle Vertragsarten")
	}
	if f.Object != "" {
		parts = append(parts, "Mietobjekt "+f.Object+" "+rentObjects[f.Object].Designation)
	} else if f.Building != "" {
		parts = append(parts, "Gebäude "+f.Building+" "+buildings[f.Building])
	}
	parts = append(parts, germanDate(f.From)+" – "+germanDate(f.To))
	return map[string]any{
		"title": "Vertragsübersicht", "subtitle": strings.Join(parts, " · "), "company": company, "generated": time.Now().Format(time.DateOnly),
		"filter":  map[string]any{"company_code": f.CC, "contract_type": f.Type, "building_id": f.Building, "object_id": f.Object, "from": f.From, "to": f.To},
		"periods": periods,
	}, nil
}

type rentObjectInfo struct{ Designation, Building string }

// reportObjects: Bezeichnungen der Mietobjekte (mit Gebäude) und Gebäude des Buchungskreises.
func (m *Module) reportObjects(ctx context.Context, cc string) (map[string]rentObjectInfo, map[string]string) {
	ros, bs := map[string]rentObjectInfo{}, map[string]string{}
	if resp, err := m.services.Call(ctx, "RentObject", "list", map[string]any{"query": map[string]any{"company_code": cc}}); err == nil {
		var l struct {
			Items []struct {
				ID          string `json:"object_id"`
				Designation string `json:"designation"`
				Building    string `json:"building_id"`
			} `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) == nil {
			for _, it := range l.Items {
				ros[it.ID] = rentObjectInfo{it.Designation, it.Building}
			}
		}
	}
	if resp, err := m.services.Call(ctx, "Building", "list", map[string]any{"query": map[string]any{"company_code": cc}}); err == nil {
		var l struct {
			Items []struct {
				ID          string `json:"building_id"`
				Designation string `json:"designation"`
			} `json:"items"`
		}
		if sdk.Decode(resp.Payload, &l) == nil {
			for _, it := range l.Items {
				bs[it.ID] = it.Designation
			}
		}
	}
	return ros, bs
}

// groupOf: Gruppe des Vertrags am Tag – Hauptobjekt, wenn es ein Mietobjekt ist,
// sonst das erste Mietobjekt, sonst das Hauptobjekt (Gebäude …), sonst „ohne Objekt“.
// Der Schlüssel sortiert Mietobjekte vor Gebäuden vor dem Rest.
func groupOf(objs []rObject, day string, ros map[string]rentObjectInfo, bs map[string]string) (key, label string) {
	var at []rObject
	for _, o := range objs {
		if o.From <= day && day <= o.To {
			at = append(at, o)
		}
	}
	pick := func(match func(rObject) bool) *rObject {
		for i := range at {
			if match(at[i]) {
				return &at[i]
			}
		}
		return nil
	}
	o := pick(func(o rObject) bool { return o.Main && o.Type == "RentObject" })
	if o == nil {
		o = pick(func(o rObject) bool { return o.Type == "RentObject" })
	}
	if o == nil {
		o = pick(func(o rObject) bool { return o.Main })
	}
	if o == nil && len(at) > 0 {
		o = &at[0]
	}
	switch {
	case o == nil:
		return "9|", "Ohne Objekt"
	case o.Type == "RentObject":
		return "1|" + o.ID, strings.TrimSpace(o.ID + " " + ros[o.ID].Designation)
	case o.Type == "Building":
		return "2|" + o.ID, strings.TrimSpace("Gebäude " + firstNonEmpty(bs[o.ID], o.ID))
	}
	return "3|" + o.ID, "Wirtschaftseinheit " + o.ID
}

func mainObjectAt(objs []rObject, day string) string {
	for _, o := range objs {
		if o.Main && o.From <= day && day <= o.To {
			return o.ID
		}
	}
	for _, o := range objs {
		if o.From <= day && day <= o.To {
			return o.ID
		}
	}
	return ""
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

func prevDay(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, -1).Format(time.DateOnly)
}

// sheetName: Name eines Excel-Blatts (höchstens 31 Zeichen, ohne : / \ ? * [ ]).
func sheetName(from, to string) string {
	f := func(d string) string {
		t, err := time.Parse(time.DateOnly, d)
		if err != nil {
			return d
		}
		return t.Format("02.01.06")
	}
	return f(from) + " - " + f(to)
}

// --- Aktion ------------------------------------------------------------------------

func (m *Module) reportAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			CC       string `json:"company_code"`
			Type     string `json:"contract_type"`
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
	f := reportFilter{CC: strings.TrimSpace(d.CC), Type: trimUpper(d.Type), Building: strings.TrimSpace(d.Building), Object: strings.TrimSpace(d.Object)}
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
	// Kennzahlen je Abschnitt auf dem Bildschirm
	table := map[string]any{"columns": []string{"Zeitraum", "Verträge", "Einnahmen mtl.", "Ausgaben mtl."}}
	var rows [][]string
	periods, _ := data["periods"].([]any)
	for _, p := range periods {
		pm := p.(map[string]any)
		rows = append(rows, []string{germanDate(crud.Str(pm["from"])) + " – " + germanDate(crud.Str(pm["to"])), fmt.Sprint(pm["contracts"]),
			reportapi.MoneyDE(pm["monthly_in"].(float64)), reportapi.MoneyDE(pm["monthly_out"].(float64))})
	}
	table["rows"] = rows
	out, err := reportapi.Render(ctx, m.services, f.CC, d.Template, fmt.Sprintf("Vertragsübersicht: %d Abschnitte im Zeitraum", len(periods)), data)
	if err != nil {
		return sdk.Response{}, err
	}
	out["table"] = table
	return sdk.Response{Payload: out}, nil
}
