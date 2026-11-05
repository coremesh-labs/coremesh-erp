package heatcost

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// --- Werte übernehmen ----------------------------------------------------------------
//
// Für jeden Nutzer mit Mietobjekt und jeden Schlüssel mit Herkunft aus den
// Stammdaten: Fläche (Bemessung des Mietobjekts am Ende des Zeitraums, Modul
// realestate), Verbrauch der Sparte im Zeitraum (Messpunkte „misst für“ das
// Mietobjekt, Stände mit Bewertungsfaktor, tagesgenau – Modul techobj) und Zahl
// der Messpunkte der Sparte. Von Hand erfasste Werte bleiben erhalten.

type fetchKey struct{ code, source, sourceCode string }

type usageItem struct {
	Medium      string  `json:"medium"`
	Consumption float64 `json:"consumption"`
	Points      int     `json:"points"`
	CoveredDays int     `json:"covered_days"`
	PeriodDays  int     `json:"period_days"`
	Unit        string  `json:"unit"`
	Detail      string  `json:"detail"`
}

func (m *Module) fetchAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
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
	unit, err := m.openRun(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	head, err := m.db.Query(ctx, `SELECT period_from, period_to, COALESCE(heat_change, ''), COALESCE(degree_table, ''), COALESCE(heat_usage_key, '')
		FROM heatcost__run WHERE company_code = ? AND run_id = ?`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	from, to := date(head.Rows[0][0]), date(head.Rows[0][1])
	heatChange, degreeTab, heatUsage := crud.Str(head.Rows[0][2]), crud.Str(head.Rows[0][3]), crud.Str(head.Rows[0][4])
	var deg degreeTable
	kr, err := m.db.Query(ctx, "SELECT code, source, COALESCE(source_code, '') FROM heatcost__key WHERE company_code = ? AND is_active = ? AND source <> ? ORDER BY sort_order, code",
		cc, true, srcManual)
	if err != nil {
		return sdk.Response{}, err
	}
	var keys []fetchKey
	for _, k := range kr.Rows {
		keys = append(keys, fetchKey{crud.Str(k[0]), crud.Str(k[1]), crud.Str(k[2])})
	}
	ur, err := m.db.Query(ctx, `SELECT user_no, object_id, valid_from, valid_to, no_interim FROM heatcost__user WHERE company_code = ? AND unit_id = ? AND object_id IS NOT NULL
		AND valid_from <= ? AND valid_to >= ? ORDER BY user_no`, cc, unit, to, from)
	if err != nil {
		return sdk.Response{}, err
	}
	type val struct {
		user, key, source, note string
		value                   float64
	}
	var vals []val
	var notes []string
	for _, u := range ur.Rows {
		user, obj := crud.Str(u[0]), crud.Str(u[1])
		// Nutzungszeitraum im Abrechnungszeitraum; Bemessung am Ende des Nutzungszeitraums.
		uf, ut := clip(from, to, date(u[2]), date(u[3]))
		noInterim := crud.AsBool(u[4]) && (uf != from || ut != to)
		var sh shares
		if noInterim {
			if heatChange == "" {
				return sdk.Response{}, crud.Invalid("Nutzer %s ohne Zwischenablesung: Grundkosten Heizung bei Nutzerwechsel an der Abrechnung eintragen", user)
			}
			if heatChange == changeDegree && deg == nil {
				if degreeTab == "" {
					return sdk.Response{}, crud.Invalid("Nutzerwechsel nach Gradtagszahlen: Tabelle fehlt an der Abrechnung")
				}
				if deg, err = m.loadDegree(ctx, cc, degreeTab); err != nil {
					return sdk.Response{}, err
				}
			}
			var d degreeTable
			if heatChange == changeDegree {
				d = deg
			}
			if sh, err = shareOf(from, to, uf, ut, d, degreeTab); err != nil {
				return sdk.Response{}, err
			}
		}
		var usage map[string]usageItem
		for _, k := range keys {
			switch k.source {
			case srcArea:
				v, ok, err := m.measurement(ctx, cc, obj, k.sourceCode, ut)
				if err != nil {
					notes = append(notes, fmt.Sprintf("Nutzer %s: Fläche nicht lesbar (%v)", user, err))
					continue
				}
				if ok {
					vals = append(vals, val{user, k.code, srcArea, fmt.Sprintf("Bemessung %s %s am %s", k.sourceCode, obj, germanDate(ut)), v})
				}
			case srcConsumption, srcDevices:
				if usage == nil {
					// Mit Zwischenablesung: Verbrauch im Nutzungszeitraum; ohne: im ganzen Zeitraum, dann anteilig.
					pf, pt := uf, ut
					if noInterim {
						pf, pt = from, to
					}
					if usage, err = m.usage(ctx, cc, obj, pf, pt); err != nil {
						notes = append(notes, fmt.Sprintf("Nutzer %s: Zähler nicht lesbar (%v)", user, err))
						usage = map[string]usageItem{}
					}
				}
				// Mehrere Sparten mit „+“ (z. B. KW+WW = Wasser kalt und warm) werden addiert.
				var it usageItem
				ok := false
				for _, code := range strings.Split(k.sourceCode, "+") {
					if x, found := usage[strings.TrimSpace(code)]; found {
						if !ok {
							it = x
						} else {
							it.Consumption += x.Consumption
							it.Points += x.Points
							it.CoveredDays = min(it.CoveredDays, x.CoveredDays)
							it.Detail += "; " + x.Detail
						}
						ok = true
					}
				}
				if !ok {
					continue
				}
				if k.source == srcDevices {
					vals = append(vals, val{user, k.code, "METER", fmt.Sprintf("%d Messpunkte %s", it.Points, k.sourceCode), float64(it.Points)})
					continue
				}
				note := it.Detail
				if it.CoveredDays < it.PeriodDays {
					note = fmt.Sprintf("nur %d von %d Tagen mit Ständen – %s", it.CoveredDays, it.PeriodDays, note)
					notes = append(notes, fmt.Sprintf("Nutzer %s, %s: nur %d von %d Tagen mit Ständen", user, k.code, it.CoveredDays, it.PeriodDays))
				}
				value := it.Consumption
				if noInterim {
					f, how := sh.time, "Kalendertagen"
					if k.code == heatUsage && heatChange == changeDegree {
						f, how = sh.degree, "Gradtagszahlen "+degreeTab
					}
					value = it.Consumption * f
					note = fmt.Sprintf("ohne Zwischenablesung (§ 9b Abs. 3): %s im Zeitraum × %s %% nach %s – %s",
						germanNumber(it.Consumption, 3), germanNumber(f*100, 2), how, note)
				}
				vals = append(vals, val{user, k.code, "METER", note, value})
			}
		}
	}
	written, kept := 0, 0
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, v := range vals {
			src, err := m.one(ctx, "SELECT source FROM heatcost__value WHERE company_code = ? AND run_id = ? AND user_no = ? AND key_code = ?", cc, id, v.user, v.key)
			if err != nil {
				return err
			}
			switch src {
			case srcManual:
				kept++
				continue
			case "":
				_, err = m.db.Exec(ctx, "INSERT INTO heatcost__value (company_code, run_id, user_no, key_code, value, source, note) VALUES (?, ?, ?, ?, ?, ?, ?)",
					cc, id, v.user, v.key, v.value, v.source, nilIfEmpty(v.note))
			default:
				_, err = m.db.Exec(ctx, "UPDATE heatcost__value SET value = ?, source = ?, note = ? WHERE company_code = ? AND run_id = ? AND user_no = ? AND key_code = ?",
					v.value, v.source, nilIfEmpty(v.note), cc, id, v.user, v.key)
			}
			if err != nil {
				return err
			}
			written++
		}
		return m.touch(ctx, cc, id)
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("%d Werte von %d Nutzern übernommen", written, len(ur.Rows))
	if kept > 0 {
		msg += fmt.Sprintf(", %d von Hand erfasste Werte beibehalten", kept)
	}
	if len(notes) > 0 {
		msg += "\nHinweise:\n- " + strings.Join(notes, "\n- ")
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "written": written, "kept": kept}}, nil
}

// measurement: Bemessung des Mietobjekts am Tag (Modul realestate).
func (m *Module) measurement(ctx context.Context, cc, obj, typ, day string) (float64, bool, error) {
	items, err := m.list(ctx, "Measurement", map[string]any{"company_code": cc, "object_id": obj, "measurement_type": typ, "includeHistory": "true"})
	if err != nil {
		return 0, false, err
	}
	for _, it := range items {
		if f, t := date(it["valid_from"]), date(it["valid_to"]); f <= day && (t == "" || day <= t) {
			return toFloat(it["value"]), true, nil
		}
	}
	return 0, false, nil
}

// usage: Verbrauch und Messpunkte je Sparte (Modul techobj, TechObjectSetup.usageFor).
func (m *Module) usage(ctx context.Context, cc, obj, from, to string) (map[string]usageItem, error) {
	resp, err := m.services.Call(ctx, "TechObjectSetup", "usageFor", map[string]any{"company_code": cc, "object": "RentObject", "id": obj, "from": from, "to": to})
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []usageItem `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	res := map[string]usageItem{}
	for _, it := range out.Items {
		res[strings.ToUpper(it.Medium)] = it
	}
	return res, nil
}
