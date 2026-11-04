package techobj

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Verbrauchsauswertung ----------------------------------------------------------------
//
// Verbrauch eines Messpunkts je Zählwerk und Kalenderjahr, auch über Zählertausch
// hinweg: je Gerät der Unterschied aufeinanderfolgender Stände (Überlauf und
// Faktor des Zählwerks), tagesgenau auf die Jahre verteilt. Auffällig ist ein
// Zeitraum, dessen Tagesverbrauch den üblichen (Median der Zeiträume) um mehr als
// „Auffällig ab %“ der Sparte übersteigt – ein Hinweis auf Leckage, Defekt oder
// Ablesefehler. Bei Unterzählern (Beziehungsart „im übergeordneten enthalten“) folgt der Rest.

type segment struct {
	from, to string // Ablesetage
	value    float64
}

type series struct {
	register, name, unit string
	decimals             int
	segments             []segment
}

func parseDay(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

func days(from, to string) int { return int(parseDay(to).Sub(parseDay(from)).Hours() / 24) }

// pointSeries: Zeiträume je Zählwerk aus den Ablesungen der im Messpunkt
// eingebauten Geräte (innerhalb des Einbaus, am Tauschtag eingeschlossen).
func (m *Module) pointSeries(ctx context.Context, cc, point string) ([]*series, error) {
	inst, err := m.db.Query(ctx, `SELECT equipment_id, valid_from, valid_to FROM techobj__installation WHERE company_code = ? AND point_id = ? ORDER BY valid_from`, cc, point)
	if err != nil {
		return nil, err
	}
	byReg := map[string]*series{}
	var order []string
	for _, i := range inst.Rows {
		eq, from, to := crud.Str(i[0]), date(i[1]), date(i[2])
		res, err := m.db.Query(ctx, `SELECT r.register_code, r.read_on, r.value, COALESCE(g.overflow, 0), COALESCE(g.factor, 1), COALESCE(g.designation, r.register_code),
			COALESCE(u.name, g.unit, ''), COALESCE(g.decimals, 0)
			FROM techobj__reading r
			LEFT JOIN techobj__register g ON g.company_code = r.company_code AND g.equipment_id = r.equipment_id AND g.register_code = r.register_code
			LEFT JOIN techobj__unit u ON u.company_code = g.company_code AND u.code = g.unit
			WHERE r.company_code = ? AND r.equipment_id = ?
			AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = r.company_code AND s.code = r.status AND s.is_rejected)
			ORDER BY r.register_code, r.read_on, r.value`, cc, eq)
		if err != nil {
			return nil, err
		}
		end := to
		if end < "9999-12-31" {
			end = parseDay(to).AddDate(0, 0, 1).Format("2006-01-02") // Ausbaustand am Tauschtag
		}
		last := map[string][2]any{}
		for _, r := range res.Rows {
			reg, day, v := crud.Str(r[0]), date(r[1]), toFloat(r[2])
			if day < from || day > end {
				continue
			}
			s, ok := byReg[reg]
			if !ok {
				s = &series{register: reg, name: crud.Str(r[5]), unit: crud.Str(r[6]), decimals: int(toInt(r[7]))}
				byReg[reg] = s
				order = append(order, reg)
			}
			if p, ok := last[reg]; ok {
				d := v - p[1].(float64)
				if d < 0 && toFloat(r[3]) > 0 {
					d += toFloat(r[3])
				}
				if pd := p[0].(string); pd != day {
					s.segments = append(s.segments, segment{from: pd, to: day, value: d * toFloat(r[4])})
				}
			}
			last[reg] = [2]any{day, v}
		}
	}
	out := make([]*series, 0, len(order))
	for _, k := range order {
		sort.Slice(byReg[k].segments, func(i, j int) bool { return byReg[k].segments[i].from < byReg[k].segments[j].from })
		out = append(out, byReg[k])
	}
	return out, nil
}

// yearly: Verbrauch und erfasste Tage je Kalenderjahr (tagesgenau verteilt).
func yearly(segs []segment) (map[int]float64, map[int]int) {
	sum, cover := map[int]float64{}, map[int]int{}
	for _, s := range segs {
		n := days(s.from, s.to)
		if n <= 0 {
			continue
		}
		rate := s.value / float64(n)
		// Ein Stand gilt am Ende des Ablesetags: der Zeitraum umfasst die Tage nach
		// der ersten bis einschließlich der zweiten Ablesung (31.12. → 31.12. = ein Jahr).
		end := parseDay(s.to).AddDate(0, 0, 1)
		for d := parseDay(s.from).AddDate(0, 0, 1); d.Before(end); {
			next := time.Date(d.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
			if end.Before(next) {
				next = end
			}
			k := int(next.Sub(d).Hours() / 24)
			sum[d.Year()] += rate * float64(k)
			cover[d.Year()] += k
			d = next
		}
	}
	return sum, cover
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

func num(v float64, dec int) string {
	s := strconv.FormatFloat(v, 'f', dec, 64)
	return strings.Replace(s, ".", ",", 1)
}

func (m *Module) consumptionAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "consumption", Label: "Verbrauch und Auffälligkeiten", Icon: "icon-chart", Record: true}, Handle: m.consumption}
}

func (m *Module) consumption(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, point, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Messpunkt: id <BuKr>|<Messpunkt>")
	}
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	res, err := m.db.Query(ctx, `SELECT p.designation, COALESCE(md.alert_percent, 0) FROM techobj__point p
		LEFT JOIN techobj__medium md ON md.company_code = p.company_code AND md.code = p.medium WHERE p.company_code = ? AND p.point_id = ?`, cc, point)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Messpunkt %s", sdk.ErrNotFound, point)
	}
	name, alert := crud.Str(res.Rows[0][0]), toFloat(res.Rows[0][1])
	all, err := m.pointSeries(ctx, cc, point)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(all) == 0 {
		return sdk.Response{Payload: map[string]any{"message": point + " " + name + ": noch keine zwei Ablesungen – kein Verbrauch"}}, nil
	}
	// Unterzähler: Verbrauch je Jahr im selben Zählwerk und derselben Einheit
	subs, err := m.db.Query(ctx, `SELECT r.child_id, p.designation FROM techobj__point_relation r
		JOIN techobj__relation_type t ON t.company_code = r.company_code AND t.code = r.relation_type
		JOIN techobj__point p ON p.company_code = r.company_code AND p.point_id = r.child_id
		WHERE r.company_code = ? AND r.parent_id = ? AND t.is_contained = ? ORDER BY r.child_id`, cc, point, true)
	if err != nil {
		return sdk.Response{}, err
	}
	type child struct {
		id, name string
		series   []*series
	}
	var children []child
	for _, r := range subs.Rows {
		s, err := m.pointSeries(ctx, cc, crud.Str(r[0]))
		if err != nil {
			return sdk.Response{}, err
		}
		children = append(children, child{crud.Str(r[0]), crud.Str(r[1]), s})
	}
	cols := []string{"Zählwerk", "Jahr", "Verbrauch", "Tage erfasst", "je Tag", "ggü. Vorjahr"}
	if len(children) > 0 {
		cols = append(cols, "Unterzähler", "Rest")
	}
	var rows [][]any
	var notes []string
	for _, s := range all {
		sum, cover := yearly(s.segments)
		var subSum map[int]float64
		var subNames []string
		for _, c := range children {
			for _, cs := range c.series {
				if cs.register == s.register && cs.unit == s.unit {
					y, _ := yearly(cs.segments)
					if subSum == nil {
						subSum = map[int]float64{}
					}
					for k, v := range y {
						subSum[k] += v
					}
					subNames = append(subNames, c.id+" "+c.name)
				}
			}
		}
		var years []int
		for y := range sum {
			years = append(years, y)
		}
		sort.Ints(years)
		unit := strings.TrimSpace(" " + s.unit)
		for i, y := range years {
			perDay := sum[y] / float64(max(cover[y], 1))
			change := ""
			if i > 0 && years[i-1] == y-1 && cover[y-1] > 0 {
				prev := sum[y-1] / float64(cover[y-1])
				if prev > 0 {
					change = fmt.Sprintf("%+.0f %%", (perDay/prev-1)*100)
				}
			}
			coverText := strconv.Itoa(cover[y])
			if full := time.Date(y+1, 1, 1, 0, 0, 0, 0, time.UTC).Sub(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)).Hours() / 24; float64(cover[y]) < full {
				coverText += " (teilweise)"
			}
			row := []any{s.name, strconv.Itoa(y), num(sum[y], s.decimals) + " " + unit, coverText, num(perDay, s.decimals) + " " + unit, change}
			if len(children) > 0 {
				if subSum != nil {
					row = append(row, num(subSum[y], s.decimals)+" "+unit, num(sum[y]-subSum[y], s.decimals)+" "+unit)
				} else {
					row = append(row, "", "")
				}
			}
			rows = append(rows, row)
		}
		if len(subNames) > 0 {
			notes = append(notes, s.name+": Unterzähler "+strings.Join(subNames, ", ")+" – Rest = Verbrauch minus Unterzähler")
		}
		// Auffällige Zeiträume (ab einer Woche) gegenüber dem Median der Tagesverbräuche
		var rates []float64
		for _, g := range s.segments {
			if n := days(g.from, g.to); n >= 7 {
				rates = append(rates, g.value/float64(n))
			}
		}
		med := median(rates)
		if alert > 0 && med > 0 && len(rates) >= 3 {
			for _, g := range s.segments {
				n := days(g.from, g.to)
				if n < 7 {
					continue
				}
				if r := g.value / float64(n); r > med*(1+alert/100) {
					notes = append(notes, fmt.Sprintf("auffällig %s: %s–%s %s %s = %s %s/Tag, %.0f %% über üblich (%s %s/Tag)", s.name, germanDate(g.from), germanDate(g.to),
						num(g.value, s.decimals), unit, num(r, s.decimals+1), unit, (r/med-1)*100, num(med, s.decimals+1), unit))
				}
			}
		}
		for _, g := range s.segments {
			if g.value < 0 {
				notes = append(notes, fmt.Sprintf("Rückgang %s: %s–%s %s %s – Ablesung prüfen (Tausch ohne Einbau, Fehlablesung?)", s.name, germanDate(g.from), germanDate(g.to), num(g.value, s.decimals), unit))
			}
		}
	}
	msg := point + " " + name + ": Verbrauch je Kalenderjahr (tagesgenau verteilt, über Zählertausch hinweg)"
	if alert > 0 {
		msg += fmt.Sprintf("; auffällig ab %.0f %% über dem üblichen Tagesverbrauch (Sparte)", alert)
	}
	if len(notes) == 0 {
		notes = append(notes, "keine Auffälligkeiten")
	}
	for _, n := range notes {
		row := make([]any, len(cols))
		row[0], row[1] = "Hinweis", n
		for i := 2; i < len(cols); i++ {
			row[i] = ""
		}
		rows = append(rows, row)
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "table": map[string]any{"columns": cols, "rows": rows}}}, nil
}
