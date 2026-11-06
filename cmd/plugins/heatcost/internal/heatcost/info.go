package heatcost

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Monatliche Verbrauchsinformation (§ 6a HeizkostenV, seit 0.2.0) ----------------------
//
// Bei fernablesbarer Ausstattung bekommen die Nutzer monatlich den Verbrauch des Vormonats
// für Heizung und Warmwasser in kWh (Kaltwasser in m³), verglichen mit dem Vormonat, dem
// Vorjahresmonat und einem Durchschnittsnutzer (Verbrauch aller Nutzer je m² × eigene
// Fläche). Die Werte kommen aus den Ablesungen (techobj, z. B. eingelesene Monatswerte des
// Messdienstes), die Umrechnung in kWh aus der letzten gerechneten Abrechnung der Einheit:
// Heizung (Energie gesamt − Warmwasser) ÷ Verbrauchseinheiten, Warmwasser Energie ÷ m³.
//
// Versäumt der Vermieter die Information, darf der Mieter seinen Anteil an den Heiz- und
// Warmwasserkosten kürzen (§ 12 Abs. 1 Nr. 3 HeizkostenV). Je Nutzer steht deshalb das
// Kürzungsrisiko (Satz × Heiz- und Warmwasserkosten der letzten Abrechnung) und je Monat
// die Frist und der Nachweis der Bereitstellung.

const (
	infoComputed = "COMPUTED"
	infoProvided = "PROVIDED"

	usageInfo  = "HEATCOST_INFO"
	infoFormat = "heatcost.info/1"

	defaultCutPercent = 3.0 // § 12 Abs. 1 Nr. 3 HeizkostenV (an der Einheit änderbar)
)

var infoStatusOptions = []metamodel.Option{{Value: infoComputed, Label: "gerechnet"}, {Value: infoProvided, Label: "bereitgestellt"}}

// infoFields: Einstellungen der Einheit zur Verbrauchsinformation.
func infoFields() []crud.Field {
	g := "Verbrauchsinformation (§ 6a)"
	return []crud.Field{
		{Key: "info_keys", Label: "Schlüssel der Verbrauchsinformation (durch Komma, z. B. VE,WW,KW; leer = Verbrauch Heizung und Warmwasser)", Short: "Schlüssel VI", Type: tText, Group: g},
		{Key: "info_template", Label: "Vorlage (Code|Sprache, Verwendung HEATCOST_INFO; leer = erste der Verwendung)", Short: "Vorlage VI", Type: tText, Group: g},
		{Key: "info_doc_type", Label: "Dokumentart im DMS (z. B. CONSUMPTION_INFO; leer = Dokumentart der Einzelabrechnung)", Short: "Dokumentart VI", Type: tText, Group: g},
		{Key: "info_due_days", Label: "Frist in Tagen nach Monatsende (leer = Ende des Folgemonats)", Short: "Frist VI", Type: tNum, Group: g},
		{Key: "info_cut_percent", Label: "Kürzungsrecht des Mieters in % bei versäumter Information (§ 12 Abs. 1 Nr. 3; leer = 3)", Short: "Kürzung %", Type: tNum, Group: g},
		{Key: "info_month", Label: "Monat (MM.JJJJ)", Type: tText, ActionOnly: true, Required: true},
		{Key: "info_heat_factor", Label: "kWh je Einheit Heizung (leer = aus der letzten Abrechnung)", Type: tNum, ActionOnly: true},
		{Key: "info_ww_factor", Label: "kWh je m³ Warmwasser (leer = aus der letzten Abrechnung)", Type: tNum, ActionOnly: true},
	}
}

func infoMatch() map[string]string {
	return map[string]string{"company_code": "company_code", "info_id": "info_id"}
}

func (m *Module) info() *crud.Entity {
	today := func() string { return time.Now().Format(time.DateOnly) }
	return &crud.Entity{
		Object: "HeatCostInfo", Title: "Verbrauchsinformationen", Icon: "icon-chart", Table: "heatcost__info", Section: "Heizkosten", ReadOnly: true,
		Keys: []string{"company_code", "info_id"}, Order: "company_code, period_from DESC, unit_id", TitleField: "info_id",
		Filters: []string{"company_code", "unit_id", "month", "status", "overdue"}, Search: []string{"info_id", "unit_id", "month"},
		FilterExpr: map[string]func(v any) (string, []any){
			"overdue": func(v any) (string, []any) {
				if crud.AsBool(v) {
					return "status <> ? AND due_on < ?", []any{infoProvided, today()}
				}
				return "1 = 1", nil
			},
		},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "info_id", Label: "Verbrauchsinformation", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
			{Key: "unit_id", Label: "Abrechnungseinheit", Short: "Einheit", Type: tText, Listable: true, ReadOnly: true, Lookup: unitLookup},
			{Key: "month", Label: "Monat", Type: tText, Listable: true, ReadOnly: true},
			{Key: "period_from", Label: "von", Type: tDate, ReadOnly: true},
			{Key: "period_to", Label: "bis", Type: tDate, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: infoStatusOptions},
			{Key: "due_on", Label: "Bereitzustellen bis", Short: "Frist", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "provided_on", Label: "Bereitgestellt am", Short: "bereitgestellt", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "provided_by", Label: "Bereitgestellt (Weg, von)", Type: tText, ReadOnly: true},
			{Key: "risk", Label: "Kürzungsrisiko bei Versäumnis (€, je Abrechnungsjahr)", Short: "Risiko €", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "cut_percent", Label: "Kürzung %", Type: tNum, ReadOnly: true},
			{Key: "base_run", Label: "Umrechnung aus Abrechnung", Type: tText, ReadOnly: true},
			{Key: "heat_factor", Label: "kWh je Einheit Heizung", Type: tNum, ReadOnly: true},
			{Key: "ww_factor", Label: "kWh je m³ Warmwasser", Type: tNum, ReadOnly: true},
			{Key: "factor_note", Label: "Herleitung der Umrechnung", Type: tArea, ReadOnly: true},
			{Key: "message", Label: "Hinweise", Type: tArea, ReadOnly: true},
			{Key: "created_at", Label: "Gerechnet am", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Gerechnet von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "overdue", Label: "Überfällig (nicht bereitgestellt, Frist vorbei)", Type: tBool, FilterOnly: true},
			{Key: "vi_store", Label: "Im DMS ablegen (je Nutzer ein Dokument)", Type: tBool, ActionOnly: true},
			{Key: "vi_users", Label: "Nur Nutzer (durch Komma; leer = alle)", Type: tText, ActionOnly: true},
			{Key: "vi_provided_on", Label: "Bereitgestellt am", Type: tDate, ActionOnly: true, Required: true},
			{Key: "vi_provided_by", Label: "Wie (z. B. E-Mail an alle Mieter, Mieterportal)", Type: tText, ActionOnly: true, Required: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "nutzer", Title: "Nutzer", Relation: &metamodel.Relation{Object: "HeatCostInfoUser", ForeignKey: "info_id", Match: infoMatch(),
				Columns: []string{"user_no", "designation", "recipient", "area", "annual_cost", "risk", "document_id"}}},
			{Key: "werte", Title: "Werte", Relation: &metamodel.Relation{Object: "HeatCostInfoLine", ForeignKey: "info_id", Match: infoMatch(),
				Columns: []string{"user_no", "key_code", "value", "unit", "kwh", "prev_month_kwh", "prev_year_kwh", "average_kwh", "complete"}}},
			{Key: "dokumente", Title: "Dokumente", Documents: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "recompute", Label: "Neu rechnen", Record: true, Confirm: "Werte neu aus den Ablesungen rechnen?"}, Handle: m.infoRecomputeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "print", Label: "Drucken …", Icon: "icon-print", Record: true, Fields: []string{"vi_users", "vi_store"}}, Handle: m.infoPrintAction},
			{ActionConfig: metamodel.ActionConfig{Name: "provided", Label: "Bereitstellung vermerken …", Record: true, Fields: []string{"vi_provided_on", "vi_provided_by"}}, Handle: m.infoProvidedAction},
		},
		Access:      access(),
		CheckRecord: checkWrite("HeatCostInfo"),
	}
}

func (m *Module) infoUser() *crud.Entity {
	return &crud.Entity{
		Object: "HeatCostInfoUser", Title: "Nutzer der Verbrauchsinformation", Icon: "icon-user", Table: "heatcost__info_user", Section: "Heizkosten", ReadOnly: true,
		Keys: []string{"company_code", "info_id", "user_no"}, Order: "company_code, info_id, user_no", TitleField: "user_no",
		Filters: []string{"company_code", "info_id", "user_no", "object_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true, Lookup: lookupCC},
			{Key: "info_id", Label: "Verbrauchsinformation", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
			{Key: "user_no", Label: "Nutzer", Type: tText, Listable: true, ReadOnly: true},
			{Key: "object_id", Label: "Mietobjekt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "recipient", Label: "Empfänger", Type: tText, Listable: true, ReadOnly: true},
			{Key: "contracts", Label: "Mietvertrag", Type: tText, ReadOnly: true},
			{Key: "area", Label: "Fläche m²", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "annual_cost", Label: "Heiz- und Warmwasserkosten der letzten Abrechnung (€)", Short: "Kosten €", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "risk", Label: "Kürzungsrisiko (€)", Short: "Risiko €", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "document_id", Label: "Dokument im DMS", Short: "Dokument", Type: tText, ReadOnly: true},
			{Key: "note", Label: "Hinweis", Type: tText, Listable: true, ReadOnly: true},
		},
		Access: access(),
	}
}

func (m *Module) infoLine() *crud.Entity {
	return &crud.Entity{
		Object: "HeatCostInfoLine", Title: "Werte der Verbrauchsinformation", Icon: "icon-chart", Table: "heatcost__info_line", Section: "Heizkosten", ReadOnly: true,
		Keys: []string{"company_code", "info_id", "user_no", "key_code"}, Order: "company_code, info_id, user_no, key_code", TitleField: "key_code",
		Filters: []string{"company_code", "info_id", "user_no", "key_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true, Lookup: lookupCC},
			{Key: "info_id", Label: "Verbrauchsinformation", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
			{Key: "user_no", Label: "Nutzer", Type: tText, Listable: true, ReadOnly: true},
			{Key: "key_code", Label: "Schlüssel", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogLookup("HeatCostKey")},
			{Key: "value", Label: "Verbrauch", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "unit", Label: "Einheit", Type: tText, Listable: true, ReadOnly: true},
			{Key: "kwh", Label: "kWh", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "prev_month", Label: "Vormonat", Type: tNum, ReadOnly: true},
			{Key: "prev_month_kwh", Label: "Vormonat kWh", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "prev_year", Label: "Vorjahresmonat", Type: tNum, ReadOnly: true},
			{Key: "prev_year_kwh", Label: "Vorjahresmonat kWh", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "average", Label: "Durchschnittsnutzer", Type: tNum, ReadOnly: true},
			{Key: "average_kwh", Label: "Durchschnittsnutzer kWh", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "complete", Label: "Vollständig (Stände für den ganzen Monat)", Short: "vollständig", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "note", Label: "Hinweis", Type: tText, ReadOnly: true},
		},
		Access: access(),
	}
}

// --- Rechnen --------------------------------------------------------------------------------

type infoKey struct {
	code, name, unit, sources string
	dec                       int
	factor                    *float64 // kWh je Einheit (nil = keine kWh)
}

type infoUserRow struct {
	no, object, designation, location, partner string
	area, annual, risk                         float64
	note                                       string
}

type infoLineRow struct {
	user, key, unit, note string
	vals                  [3]*float64 // Monat, Vormonat, Vorjahresmonat
	avg                   *float64
	complete              bool
	factor                *float64
}

type infoCalc struct {
	cc, unit, month, from, to string
	base, factorNote          string
	heatF, wwF, cut, risk     *float64
	keys                      []infoKey
	users                     []infoUserRow
	lines                     []infoLineRow
	notes                     []string
	due                       string
}

// parseMonth: MM.JJJJ, JJJJ-MM oder ein Datum → erster und letzter Tag.
func parseMonth(s string) (string, string, string, error) {
	s = strings.TrimSpace(s)
	var t time.Time
	var err error
	for _, layout := range []string{"01.2006", "1.2006", "2006-01", "02.01.2006", "2006-01-02"} {
		if t, err = time.Parse(layout, s); err == nil {
			break
		}
	}
	if err != nil {
		return "", "", "", crud.Invalid("Monat %q: MM.JJJJ (z. B. 10.2026)", s)
	}
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	return first.Format("2006-01"), first.Format(time.DateOnly), first.AddDate(0, 1, -1).Format(time.DateOnly), nil
}

func monthShift(from string, months int) (string, string) {
	t, _ := time.Parse(time.DateOnly, from)
	f := t.AddDate(0, months, 0)
	return f.Format(time.DateOnly), f.AddDate(0, 1, -1).Format(time.DateOnly)
}

func numOrNil(v any) *float64 {
	if !isSet(v) {
		return nil
	}
	f := toFloat(v)
	return &f
}

func (m *Module) computeInfo(ctx context.Context, cc, unit, month string, heatF, wwF *float64) (*infoCalc, error) {
	mon, from, to, err := parseMonth(month)
	if err != nil {
		return nil, err
	}
	if to >= time.Now().Format(time.DateOnly) {
		return nil, crud.Invalid("Monat %s ist noch nicht vorbei", mon)
	}
	c := &infoCalc{cc: cc, unit: unit, month: mon, from: from, to: to}
	u, err := m.db.Query(ctx, `SELECT COALESCE(heat_usage_key, ''), COALESCE(ww_usage_key, ''), COALESCE(heat_area_key, ''), COALESCE(info_keys, ''),
		info_due_days, info_cut_percent FROM heatcost__unit WHERE company_code = ? AND unit_id = ?`, cc, unit)
	if err != nil {
		return nil, err
	}
	if len(u.Rows) == 0 {
		return nil, fmt.Errorf("%w: Abrechnungseinheit %s|%s", sdk.ErrNotFound, cc, unit)
	}
	x := u.Rows[0]
	heatKey, wwKey, areaKey := crud.Str(x[0]), crud.Str(x[1]), crud.Str(x[2])
	codes := splitCodes(crud.Str(x[3]))
	if len(codes) == 0 {
		for _, k := range []string{heatKey, wwKey} {
			if k != "" {
				codes = append(codes, k)
			}
		}
	}
	if len(codes) == 0 {
		return nil, crud.Invalid("Einheit %s: Schlüssel der Verbrauchsinformation fehlen (Einstellung oder Schlüssel Verbrauch Heizung/Warmwasser)", unit)
	}
	end, _ := time.Parse(time.DateOnly, to)
	if isSet(x[4]) {
		c.due = end.AddDate(0, 0, int(toInt(x[4]))).Format(time.DateOnly)
	} else {
		c.due = end.AddDate(0, 0, 1).AddDate(0, 1, -1).Format(time.DateOnly)
	}
	cut := defaultCutPercent
	if isSet(x[5]) {
		cut = toFloat(x[5])
	}
	c.cut = &cut
	// Umrechnung aus der letzten gerechneten Abrechnung der Einheit
	br, err := m.db.Query(ctx, `SELECT run_id, total_energy, ww_energy FROM heatcost__run WHERE company_code = ? AND unit_id = ? AND status <> ?
		ORDER BY period_to DESC, run_id DESC LIMIT 1`, cc, unit, runDraft)
	if err != nil {
		return nil, err
	}
	basis := map[string]float64{}
	var total, ww float64
	if len(br.Rows) > 0 {
		c.base = crud.Str(br.Rows[0][0])
		total, ww = toFloat(br.Rows[0][1]), toFloat(br.Rows[0][2])
		rs, err := m.db.Query(ctx, "SELECT key_code, total FROM heatcost__basis WHERE company_code = ? AND run_id = ? AND (scope IS NULL OR scope = '' OR scope = '*')", cc, c.base)
		if err != nil {
			return nil, err
		}
		for _, r := range rs.Rows {
			basis[crud.Str(r[0])] = toFloat(r[1])
		}
		vs, err := m.db.Query(ctx, "SELECT key_code, SUM(value) FROM heatcost__value WHERE company_code = ? AND run_id = ? GROUP BY key_code", cc, c.base)
		if err != nil {
			return nil, err
		}
		for _, r := range vs.Rows {
			if _, ok := basis[crud.Str(r[0])]; !ok {
				basis[crud.Str(r[0])] = toFloat(r[1])
			}
		}
	}
	var fnotes []string
	for _, code := range codes {
		kr, err := m.db.Query(ctx, "SELECT name, COALESCE(unit, ''), source, COALESCE(source_code, ''), decimals FROM heatcost__key WHERE company_code = ? AND code = ?", cc, code)
		if err != nil {
			return nil, err
		}
		if len(kr.Rows) == 0 {
			return nil, crud.Invalid("Schlüssel %s gibt es nicht", code)
		}
		r := kr.Rows[0]
		if crud.Str(r[2]) != srcConsumption {
			return nil, crud.Invalid("Schlüssel %s (%s) misst keinen Verbrauch", code, crud.Str(r[0]))
		}
		k := infoKey{code: code, name: crud.Str(r[0]), unit: crud.Str(r[1]), sources: crud.Str(r[3]), dec: int(toInt(r[4]))}
		switch {
		case strings.EqualFold(k.unit, "kWh"):
			one := 1.0
			k.factor = &one
		case code == heatKey:
			if heatF != nil {
				k.factor = heatF
				fnotes = append(fnotes, fmt.Sprintf("Heizung: %s kWh je %s (von Hand)", germanNumber(*heatF, 4), k.unit))
			} else if b := basis[code]; c.base != "" && b > 0 && total > ww {
				f := (total - ww) / b
				k.factor = &f
				fnotes = append(fnotes, fmt.Sprintf("Heizung: (%s − %s kWh) ÷ %s %s = %s kWh je %s (Abrechnung %s)",
					germanNumber(total, 0), germanNumber(ww, 0), germanNumber(b, 3), k.unit, germanNumber(f, 4), k.unit, c.base))
			} else {
				c.notes = append(c.notes, fmt.Sprintf("%s: ohne Umrechnung in kWh (keine gerechnete Abrechnung) – Faktor von Hand angeben", k.name))
			}
			c.heatF = k.factor
		case code == wwKey:
			if wwF != nil {
				k.factor = wwF
				fnotes = append(fnotes, fmt.Sprintf("Warmwasser: %s kWh je %s (von Hand)", germanNumber(*wwF, 4), k.unit))
			} else if b := basis[code]; c.base != "" && b > 0 && ww > 0 {
				f := ww / b
				k.factor = &f
				fnotes = append(fnotes, fmt.Sprintf("Warmwasser: %s kWh ÷ %s %s = %s kWh je %s (Abrechnung %s)",
					germanNumber(ww, 0), germanNumber(b, 3), k.unit, germanNumber(f, 4), k.unit, c.base))
			} else {
				c.notes = append(c.notes, fmt.Sprintf("%s: ohne Umrechnung in kWh (keine gerechnete Abrechnung) – Faktor von Hand angeben", k.name))
			}
			c.wwF = k.factor
		}
		c.keys = append(c.keys, k)
	}
	c.factorNote = strings.Join(fnotes, "\n")
	areaSrc := ""
	if areaKey != "" {
		if s, err := m.one(ctx, "SELECT COALESCE(source_code, '') FROM heatcost__key WHERE company_code = ? AND code = ? AND source = ?", cc, areaKey, srcArea); err == nil {
			areaSrc = s
		}
	}
	// Kosten der letzten Abrechnung je Nutzer (Heizung und Warmwasser) für das Kürzungsrisiko
	annual := map[string]float64{}
	if c.base != "" {
		ar, err := m.db.Query(ctx, "SELECT user_no, SUM(amount) FROM heatcost__result WHERE company_code = ? AND run_id = ? AND block IN (?, ?) GROUP BY user_no",
			cc, c.base, blockHeat, blockWW)
		if err != nil {
			return nil, err
		}
		for _, r := range ar.Rows {
			annual[crud.Str(r[0])] = toFloat(r[1])
		}
	}
	ur, err := m.db.Query(ctx, `SELECT user_no, object_id, COALESCE(designation, ''), COALESCE(location, ''), COALESCE(partner_id, '') FROM heatcost__user
		WHERE company_code = ? AND unit_id = ? AND object_id IS NOT NULL AND valid_from <= ? AND valid_to >= ? ORDER BY user_no`, cc, unit, to, from)
	if err != nil {
		return nil, err
	}
	if len(ur.Rows) == 0 {
		return nil, crud.Invalid("Einheit %s hat im Monat %s keine Nutzer mit Mietobjekt", unit, mon)
	}
	periods := [3][2]string{{from, to}}
	periods[1][0], periods[1][1] = monthShift(from, -1)
	periods[2][0], periods[2][1] = monthShift(from, -12)
	var risk float64
	sumV, sumA := map[string]float64{}, map[string]float64{}
	for _, r := range ur.Rows {
		usr := infoUserRow{no: crud.Str(r[0]), object: crud.Str(r[1]), designation: crud.Str(r[2]), location: crud.Str(r[3]), partner: crud.Str(r[4])}
		if areaSrc != "" {
			if a, ok, err := m.measurement(ctx, cc, usr.object, areaSrc, to); err == nil && ok {
				usr.area = a
			}
		}
		if a, ok := annual[usr.no]; ok && c.cut != nil {
			usr.annual = a
			usr.risk = round2(a * *c.cut / 100)
			risk += usr.risk
		}
		var usage [3]map[string]usageItem
		for i, p := range periods {
			if usage[i], err = m.usage(ctx, cc, usr.object, p[0], p[1]); err != nil {
				usr.note = fmt.Sprintf("Zähler nicht lesbar (%v)", err)
				usage[i] = map[string]usageItem{}
			}
		}
		for _, k := range c.keys {
			l := infoLineRow{user: usr.no, key: k.code, unit: k.unit, factor: k.factor, complete: true}
			for i := range periods {
				v, full, found := sumUsage(usage[i], k.sources)
				if found && full {
					vv := v
					l.vals[i] = &vv
				}
				if i == 0 && !(found && full) {
					l.complete = false
					if found {
						l.note = "Stände nicht für den ganzen Monat"
					} else {
						l.note = "keine Stände"
					}
				}
			}
			if l.complete && usr.area > 0 {
				sumV[k.code] += *l.vals[0]
				sumA[k.code] += usr.area
			}
			c.lines = append(c.lines, l)
		}
		c.users = append(c.users, usr)
	}
	areas := map[string]float64{}
	for _, u := range c.users {
		areas[u.no] = u.area
	}
	missing := 0
	for i := range c.lines {
		l := &c.lines[i]
		if a := areas[l.user]; a > 0 && sumA[l.key] > 0 {
			avg := sumV[l.key] / sumA[l.key] * a
			l.avg = &avg
		}
		if !l.complete {
			missing++
		}
	}
	if missing > 0 {
		c.notes = append(c.notes, fmt.Sprintf("%d Werte ohne vollständige Stände im Monat (Ablesungen einlesen, dann „Neu rechnen“)", missing))
	}
	if areaSrc == "" {
		c.notes = append(c.notes, "ohne Fläche (Schlüssel Grundkosten Heizung): kein Durchschnittsnutzer")
	}
	c.risk = &risk
	return c, nil
}

// sumUsage: Verbrauch der Sparten eines Schlüssels (KW+WW addiert); full = ganzer Zeitraum mit Ständen.
func sumUsage(usage map[string]usageItem, sources string) (v float64, full, found bool) {
	full = true
	for _, s := range strings.Split(sources, "+") {
		it, ok := usage[strings.ToUpper(strings.TrimSpace(s))]
		if !ok {
			continue
		}
		found = true
		v += it.Consumption
		if it.CoveredDays < it.PeriodDays {
			full = false
		}
	}
	return v, full && found, found
}

func mulOrNil(v, f *float64) any {
	if v == nil || f == nil {
		return nil
	}
	return round3(*v * *f)
}

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

func userOf(ctx context.Context) string { return sdk.CallFromContext(ctx).UserID }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func ptr(v *float64) any {
	if v == nil {
		return nil
	}
	return round3(*v)
}

func (m *Module) nextInfoID(ctx context.Context, cc, year string) (string, error) {
	stem := "VI-" + year + "-"
	res, err := m.db.Query(ctx, "SELECT info_id FROM heatcost__info WHERE company_code = ? AND info_id LIKE ?", cc, stem+"%")
	if err != nil {
		return "", err
	}
	n := 0
	for _, r := range res.Rows {
		if k, err := strconv.Atoi(strings.TrimPrefix(crud.Str(r[0]), stem)); err == nil && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s%04d", stem, n+1), nil
}

// saveInfo schreibt das Ergebnis (neu oder an die Stelle der bisherigen Werte).
func (m *Module) saveInfo(ctx context.Context, id string, c *infoCalc) error {
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		msg := nilIfEmpty(strings.Join(c.notes, "\n"))
		if id == "" {
			return fmt.Errorf("Verbrauchsinformation ohne Nummer")
		}
		ex, err := m.one(ctx, "SELECT 1 FROM heatcost__info WHERE company_code = ? AND info_id = ?", c.cc, id)
		if err != nil {
			return err
		}
		if ex == "" {
			_, err = m.db.Exec(ctx, `INSERT INTO heatcost__info (company_code, info_id, unit_id, month, period_from, period_to, status, due_on, base_run, factor_note, message,
				created_at, created_by, heat_factor, ww_factor, cut_percent, risk) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.cc, id, c.unit, c.month, c.from, c.to, infoComputed, c.due, nilIfEmpty(c.base), nilIfEmpty(c.factorNote), msg, nowStamp(), nilIfEmpty(userOf(ctx)),
				ptr(c.heatF), ptr(c.wwF), ptr(c.cut), round2(*c.risk))
		} else {
			_, err = m.db.Exec(ctx, `UPDATE heatcost__info SET due_on = ?, base_run = ?, factor_note = ?, message = ?, created_at = ?, created_by = ?, heat_factor = ?, ww_factor = ?,
				cut_percent = ?, risk = ? WHERE company_code = ? AND info_id = ?`, c.due, nilIfEmpty(c.base), nilIfEmpty(c.factorNote), msg, nowStamp(), nilIfEmpty(userOf(ctx)),
				ptr(c.heatF), ptr(c.wwF), ptr(c.cut), round2(*c.risk), c.cc, id)
			if err == nil {
				_, err = m.db.Exec(ctx, "DELETE FROM heatcost__info_line WHERE company_code = ? AND info_id = ?", c.cc, id)
			}
			if err == nil {
				_, err = m.db.Exec(ctx, "DELETE FROM heatcost__info_user WHERE company_code = ? AND info_id = ?", c.cc, id)
			}
		}
		if err != nil {
			return err
		}
		for _, u := range c.users {
			if _, err := m.db.Exec(ctx, `INSERT INTO heatcost__info_user (company_code, info_id, user_no, object_id, designation, area, annual_cost, risk, note)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.cc, id, u.no, u.object, nilIfEmpty(firstNonEmpty(u.designation, u.location)), u.area, u.annual, u.risk, nilIfEmpty(u.note)); err != nil {
				return err
			}
		}
		for _, l := range c.lines {
			if _, err := m.db.Exec(ctx, `INSERT INTO heatcost__info_line (company_code, info_id, user_no, key_code, unit, note, value, kwh, prev_month, prev_month_kwh,
				prev_year, prev_year_kwh, average, average_kwh, complete) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.cc, id, l.user, l.key, nilIfEmpty(l.unit), nilIfEmpty(l.note), ptr(l.vals[0]), mulOrNil(l.vals[0], l.factor), ptr(l.vals[1]), mulOrNil(l.vals[1], l.factor),
				ptr(l.vals[2]), mulOrNil(l.vals[2], l.factor), ptr(l.avg), mulOrNil(l.avg, l.factor), l.complete); err != nil {
				return err
			}
		}
		return nil
	})
}

// monthInfoAction: HeatCostUnit.monthInfo {id, data: {info_month, info_heat_factor, info_ww_factor}}.
func (m *Module) monthInfoAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, unit, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostInfo", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.computeInfo(ctx, cc, unit, crud.Str(in.Data["info_month"]), numOrNil(in.Data["info_heat_factor"]), numOrNil(in.Data["info_ww_factor"]))
	if err != nil {
		return sdk.Response{}, err
	}
	row, err := m.db.Query(ctx, "SELECT info_id, status FROM heatcost__info WHERE company_code = ? AND unit_id = ? AND month = ?", cc, unit, c.month)
	if err != nil {
		return sdk.Response{}, err
	}
	id := ""
	if len(row.Rows) > 0 {
		id = crud.Str(row.Rows[0][0])
		if crud.Str(row.Rows[0][1]) == infoProvided {
			return sdk.Response{}, crud.Invalid("Verbrauchsinformation %s für %s ist schon bereitgestellt", id, c.month)
		}
	} else if id, err = m.nextInfoID(ctx, cc, c.month[:4]); err != nil {
		return sdk.Response{}, err
	}
	if err := m.saveInfo(ctx, id, c); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"info_id": id, "message": infoMessage(id, c)}}, nil
}

func infoMessage(id string, c *infoCalc) string {
	msg := fmt.Sprintf("Verbrauchsinformation %s (%s): %d Nutzer, bereitzustellen bis %s; Kürzungsrisiko bei Versäumnis %s",
		id, c.month, len(c.users), germanDate(c.due), reportapi.MoneyDE(*c.risk)+" €")
	if len(c.notes) > 0 {
		msg += "\n" + strings.Join(c.notes, "\n")
	}
	return msg
}

func (m *Module) infoHead(ctx context.Context, cc, id string) (unit, month, status string, heatF, wwF *float64, err error) {
	r, err := m.db.Query(ctx, "SELECT unit_id, month, status, heat_factor, ww_factor, COALESCE(factor_note, '') FROM heatcost__info WHERE company_code = ? AND info_id = ?", cc, id)
	if err != nil {
		return
	}
	if len(r.Rows) == 0 {
		err = fmt.Errorf("%w: Verbrauchsinformation %s|%s", sdk.ErrNotFound, cc, id)
		return
	}
	x := r.Rows[0]
	unit, month, status = crud.Str(x[0]), crud.Str(x[1]), crud.Str(x[2])
	// von Hand gesetzte Faktoren bleiben beim Neurechnen
	if note := crud.Str(x[5]); strings.Contains(note, "Heizung:") && strings.Contains(note, "(von Hand)") {
		heatF = numOrNil(x[3])
	}
	if note := crud.Str(x[5]); strings.Contains(note, "Warmwasser:") && strings.Contains(note, "(von Hand)") {
		wwF = numOrNil(x[4])
	}
	return
}

func (m *Module) infoRecomputeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostInfo", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	unit, month, status, heatF, wwF, err := m.infoHead(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if status == infoProvided {
		return sdk.Response{}, crud.Invalid("Verbrauchsinformation %s ist bereitgestellt – Werte bleiben, wie sie bereitgestellt wurden", id)
	}
	c, err := m.computeInfo(ctx, cc, unit, month, heatF, wwF)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := m.saveInfo(ctx, id, c); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": infoMessage(id, c)}}, nil
}

func (m *Module) infoProvidedAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostInfo", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	day, err := crud.ParseDate(in.Data["vi_provided_on"])
	if err != nil {
		return sdk.Response{}, err
	}
	how := strings.TrimSpace(crud.Str(in.Data["vi_provided_by"]))
	if how == "" {
		return sdk.Response{}, crud.Invalid("Bitte angeben, wie bereitgestellt wurde (Nachweis)")
	}
	_, _, _, _, _, err = m.infoHead(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.db.Exec(ctx, "UPDATE heatcost__info SET status = ?, provided_on = ?, provided_by = ? WHERE company_code = ? AND info_id = ?",
		infoProvided, day, fmt.Sprintf("%s (%s)", how, userOf(ctx)), cc, id); err != nil {
		return sdk.Response{}, err
	}
	due, _ := m.one(ctx, "SELECT due_on FROM heatcost__info WHERE company_code = ? AND info_id = ?", cc, id)
	msg := fmt.Sprintf("Verbrauchsinformation %s: bereitgestellt am %s", id, germanDate(day))
	if d := date(due); d != "" && day > d {
		msg += fmt.Sprintf(" – nach der Frist %s", germanDate(d))
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

// --- Drucken -------------------------------------------------------------------------------

func diffPercent(a, b any) any {
	x, okA := a.(float64)
	y, okB := b.(float64)
	if !okA || !okB || y == 0 {
		return nil
	}
	return math.Round((x-y)/y*1000) / 10
}

// bars: Balkenlängen in mm (längster 60 mm) für Monat, Vormonat, Vorjahresmonat, Durchschnitt.
func bars(v ...any) map[string]any {
	mx := 0.0
	for _, x := range v {
		if f, ok := x.(float64); ok && f > mx {
			mx = f
		}
	}
	out := map[string]any{}
	for i, k := range []string{"month", "prev_month", "prev_year", "average"} {
		if f, ok := v[i].(float64); ok && mx > 0 {
			out[k] = int(math.Round(f / mx * 60))
		}
	}
	return out
}

func (m *Module) infoPrintAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if ok, err := sdk.CheckAccess(ctx, "HeatCostInfo", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	hr, err := m.db.Query(ctx, `SELECT i.unit_id, i.month, i.period_from, i.period_to, u.designation, COALESCE(u.external_no, ''), COALESCE(u.partner_id, ''),
		COALESCE(u.info_template, ''), COALESCE(u.info_doc_type, ''), COALESCE(u.doc_type, ''), COALESCE(u.sender_roles, ''), COALESCE(u.tenant_roles, ''),
		COALESCE(u.entity_id, ''), COALESCE(u.building_id, ''), COALESCE(i.factor_note, ''), COALESCE(u.heat_usage_key, ''), COALESCE(u.ww_usage_key, '')
		FROM heatcost__info i JOIN heatcost__unit u ON u.company_code = i.company_code AND u.unit_id = i.unit_id WHERE i.company_code = ? AND i.info_id = ?`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(hr.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Verbrauchsinformation %s|%s", sdk.ErrNotFound, cc, id)
	}
	x := hr.Rows[0]
	s := func(i int) string { return crud.Str(x[i]) }
	unit, month, from, to := s(0), s(1), date(x[2]), date(x[3])
	docType := firstNonEmpty(s(8), s(9))
	store := crud.AsBool(in.Data["vi_store"])
	if store {
		if err := requireWrite(ctx, "HeatCostInfo", "update", cc); err != nil {
			return sdk.Response{}, err
		}
		if docType == "" {
			return sdk.Response{}, crud.Invalid("Ablage im DMS: an der Abrechnungseinheit %s fehlt die Dokumentart", unit)
		}
	}
	tmpl := s(7)
	if tmpl == "" {
		if opts := reportapi.Templates(ctx, m.services, usageInfo); len(opts) > 0 {
			tmpl = opts[0].Value
		}
	}
	if tmpl == "" {
		return sdk.Response{}, crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → Bündel heatcost-info einspielen)", usageInfo)
	}
	if !strings.Contains(tmpl, "|") {
		tmpl += "|de"
	}
	t, _ := time.Parse(time.DateOnly, from)
	monthName := fmt.Sprintf("%s %d", monthNames[t.Month()], t.Year())
	pm, _ := monthShift(from, -1)
	py, _ := monthShift(from, -12)
	tp, _ := time.Parse(time.DateOnly, pm)
	ty, _ := time.Parse(time.DateOnly, py)
	provider := ""
	if s(6) != "" {
		provider = m.partnerName(ctx, s(6))
	}
	company := reportapi.Company(ctx, m.services, cc)
	data := map[string]any{"format": infoFormat, "title": "Verbrauchsinformation " + monthName, "printed_on": time.Now().Format(time.DateOnly), "company": company,
		"info": map[string]any{"id": id, "month": month, "month_name": monthName, "from": from, "to": to, "unit": unit, "unit_name": s(4), "external_no": s(5),
			"provider": provider, "prev_month_name": fmt.Sprintf("%s %d", monthNames[tp.Month()], tp.Year()),
			"prev_year_name": fmt.Sprintf("%s %d", monthNames[ty.Month()], ty.Year()), "factor_note": s(14)}}
	b := &letterBuilder{m: m, r: &runInfo{cc: cc, unit: unit}, company: company, senderRoles: splitCodes(s(10)), tenantRoles: splitCodes(s(11)),
		entity: s(12), building: s(13), partners: map[string]map[string]any{}, places: map[string]map[string]any{}}
	want := map[string]bool{}
	for _, u := range splitList(crud.Str(in.Data["vi_users"])) {
		want[u] = true
	}
	ur, err := m.db.Query(ctx, `SELECT iu.user_no, iu.object_id, COALESCE(iu.designation, ''), COALESCE(u.location, ''), COALESCE(u.partner_id, ''), iu.area
		FROM heatcost__info_user iu LEFT JOIN heatcost__user u ON u.company_code = iu.company_code AND u.unit_id = ? AND u.user_no = iu.user_no
		WHERE iu.company_code = ? AND iu.info_id = ? ORDER BY iu.user_no`, unit, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	keyNames := map[string]string{}
	if kr, err := m.db.Query(ctx, "SELECT code, name FROM heatcost__key WHERE company_code = ?", cc); err == nil {
		for _, k := range kr.Rows {
			keyNames[crud.Str(k[0])] = crud.Str(k[1])
		}
	}
	// Verbrauch Heizung und Warmwasser heißen so (in kWh), nicht nach dem Messgerät
	if s(15) != "" {
		keyNames[s(15)] = "Heizung"
	}
	if s(16) != "" {
		keyNames[s(16)] = "Warmwasser"
	}
	type letterOut struct {
		user, object string
		partner      string
		contracts    []string
		data         map[string]any
	}
	var letters []letterOut
	for _, r := range ur.Rows {
		no := crud.Str(r[0])
		if len(want) > 0 && !want[no] {
			continue
		}
		u := &userResult{no: no, object: crud.Str(r[1]), name: crud.Str(r[2]), location: crud.Str(r[3]), partner: crud.Str(r[4])}
		sender := b.sender(ctx, u.object, to)
		recipient, names, contracts := b.recipient(ctx, u, from, to)
		tenant := ""
		for _, p := range b.objectPartners(ctx, u.object, to) {
			if hasRole(b.tenantRoles, p.Role) && (p.From == "" || p.From <= to) && (p.To == "" || p.To >= from) {
				tenant = p.Partner
				break
			}
		}
		if tenant == "" {
			tenant = u.partner
		}
		obj := b.place(ctx, "RentObject", u.object)
		where := firstNonEmpty(u.location, crud.Str(obj["designation"]), u.object)
		lr, err := m.db.Query(ctx, `SELECT key_code, COALESCE(unit, ''), value, kwh, prev_month, prev_month_kwh, prev_year, prev_year_kwh, average, average_kwh, complete, COALESCE(note, '')
			FROM heatcost__info_line WHERE company_code = ? AND info_id = ? AND user_no = ? ORDER BY key_code`, cc, id, no)
		if err != nil {
			return sdk.Response{}, err
		}
		var rows []map[string]any
		for _, l := range lr.Rows {
			f := func(i int) any {
				if l[i] == nil {
					return nil
				}
				return toFloat(l[i])
			}
			row := map[string]any{"key": crud.Str(l[0]), "name": firstNonEmpty(keyNames[crud.Str(l[0])], crud.Str(l[0])), "unit": crud.Str(l[1]),
				"value": f(2), "kwh": f(3), "prev_month": f(4), "prev_month_kwh": f(5), "prev_year": f(6), "prev_year_kwh": f(7),
				"average": f(8), "average_kwh": f(9), "complete": crud.AsBool(l[10]), "note": crud.Str(l[11]), "has_kwh": l[3] != nil}
			row["diff_month"], row["diff_year"], row["diff_average"] = diffPercent(f(2), f(4)), diffPercent(f(2), f(6)), diffPercent(f(2), f(8))
			row["bars"] = bars(f(2), f(4), f(6), f(8))
			rows = append(rows, row)
		}
		ld := map[string]any{"sender": sender, "recipient": recipient, "tenants": strings.Join(names, ", "), "contracts": strings.Join(contracts, ", "),
			"owner": crud.Str(sender["name"]), "user_no": no, "location": u.location, "object_id": u.object, "object": obj, "area": toFloat(r[5]),
			"subject": fmt.Sprintf("Verbrauchsinformation %s – %s", monthName, where), "rows": rows}
		letters = append(letters, letterOut{user: no, object: u.object, partner: tenant, contracts: contracts, data: ld})
	}
	if len(letters) == 0 {
		return sdk.Response{}, crud.Invalid("Verbrauchsinformation %s hat keine passenden Nutzer", id)
	}
	all := make([]map[string]any, len(letters))
	for i, l := range letters {
		all[i] = l.data
	}
	data["letters"] = all
	out, err := reportapi.Render(ctx, m.services, cc, tmpl, fmt.Sprintf("Verbrauchsinformation %s (%d Nutzer)", id, len(letters)), data)
	if err != nil {
		return sdk.Response{}, err
	}
	if !store {
		return sdk.Response{Payload: out}, nil
	}
	var stored []string
	for _, l := range letters {
		one := map[string]any{}
		for k, v := range data {
			one[k] = v
		}
		one["letters"] = []map[string]any{l.data}
		res, err := reportapi.Render(ctx, m.services, cc, tmpl, "Verbrauchsinformation "+id+" Nutzer "+l.user, one)
		if err != nil {
			return sdk.Response{}, err
		}
		file, ok := res["download"].(metamodel.Content)
		if !ok || file.Content == "" {
			return sdk.Response{}, crud.Invalid("Nutzer %s: PDF nicht fertig – %s", l.user, crud.Str(res["message"]))
		}
		refs := []any{map[string]any{"object": "HeatCostInfo", "key": cc + "|" + id}}
		if l.object != "" {
			refs = append(refs, map[string]any{"object": "RentObject", "key": cc + "|" + l.object})
		}
		for _, c := range l.contracts {
			refs = append(refs, map[string]any{"object": "Contract", "key": cc + "|" + c})
		}
		resp, err := m.services.Call(ctx, "DmsDocument", "store", map[string]any{"company_code": cc, "content": file.Content,
			"file_name": fmt.Sprintf("%s_Nutzer_%s.pdf", id, l.user), "content_type": "application/pdf", "doc_type": docType, "doc_date": to,
			"title":      fmt.Sprintf("Verbrauchsinformation %s Nutzer %s %s (%s)", monthName, l.user, crud.Str(l.data["tenants"]), id),
			"partner_id": nilIfEmpty(l.partner), "refs": refs})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Nutzer %s: Ablage im DMS: %v", l.user, err)
		}
		var d struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = sdk.Decode(resp.Payload, &d)
		if _, err := m.db.Exec(ctx, "UPDATE heatcost__info_user SET document_id = ?, recipient = ?, contracts = ? WHERE company_code = ? AND info_id = ? AND user_no = ?",
			nilIfEmpty(d.ID), nilIfEmpty(crud.Str(l.data["tenants"])), nilIfEmpty(strings.Join(l.contracts, ", ")), cc, id, l.user); err != nil {
			return sdk.Response{}, err
		}
		stored = append(stored, firstNonEmpty(d.Name, d.ID))
	}
	out["message"] = crud.Str(out["message"]) + fmt.Sprintf("; %d Verbrauchsinformationen im DMS abgelegt (Bereitstellung danach vermerken)", len(stored))
	return sdk.Response{Payload: out}, nil
}
