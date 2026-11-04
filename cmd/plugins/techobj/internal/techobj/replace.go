package techobj

import (
	"context"
	"fmt"
	"strconv"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// --- Tausch nach Alter (seit 0.6.2) ---------------------------------------------------
//
// Je Zählerart (Sparte) – abweichend je Gerätetyp – drei Grenzen in Jahren:
// Tausch empfohlen, Tausch Pflicht (leer = Eichfrist) und untragbar. Gezählt
// wird ab dem Eichjahr, sonst dem Baujahr, sonst dem Jahr des ersten Einbaus.

const (
	replaceOK       = ""
	replaceShould   = "SHOULD"
	replaceMust     = "MUST"
	replaceCritical = "CRITICAL"
)

type replaceInfo struct {
	level                  string // replaceShould … (leer = noch nichts fällig)
	owner                  string // Eigentümer tauscht selbst (z. B. Versorger): nur Hinweis
	fromCalibration        bool   // Pflicht aus der Eichfrist (keine eigene Grenze)
	plant                  string // Pflicht aus der Anlage („AN0001 Oelßnerstr. 10“)
	base                   int64  // Bezugsjahr
	baseText               string // „Eichjahr“, „Baujahr“, „Einbau“
	should, must, critical int64  // Jahr, ab dem die Stufe gilt (0 = keine Grenze)
}

func (r replaceInfo) text() string {
	if t := r.levelText(); t != "" && r.owner != "" {
		return t + " – Tausch durch " + r.owner
	} else {
		return t
	}
}

// ours: Tausch ist unsere Aufgabe (Übersicht „Fälligkeiten“).
func (r replaceInfo) ours() bool { return r.level != "" && r.owner == "" }

func (r replaceInfo) levelText() string {
	next := func(label string, y int64) string {
		if y == 0 {
			return ""
		}
		return fmt.Sprintf("; %s ab %d", label, y)
	}
	base := fmt.Sprintf(" (%s %d", r.baseText, r.base)
	switch r.level {
	case replaceCritical:
		return fmt.Sprintf("untragbar seit %d – sofort tauschen", r.critical) + base + ")"
	case replaceMust:
		if r.fromCalibration {
			return fmt.Sprintf("Eichfrist abgelaufen Ende %d – Tausch Pflicht", r.must-1) + base + next("untragbar", r.critical) + ")"
		}
		if r.plant != "" {
			base += ", Regel der Anlage " + r.plant
		}
		return fmt.Sprintf("Tausch Pflicht seit %d", r.must) + base + next("untragbar", r.critical) + ")"
	case replaceShould:
		return fmt.Sprintf("Tausch empfohlen seit %d", r.should) + base + next("Pflicht", r.must) + next("untragbar", r.critical) + ")"
	}
	return ""
}

// checkReplaceYears: empfohlen ≤ Pflicht ≤ untragbar (soweit angegeben).
func checkReplaceYears(rec crud.Record) error {
	var last int64
	for _, k := range []string{"replace_should_years", "replace_must_years", "replace_critical_years"} {
		if crud.Str(rec[k]) == "" {
			continue
		}
		v := toInt(rec[k])
		if v < 0 {
			return crud.Invalid("Jahre dürfen nicht negativ sein")
		}
		if v < last {
			return crud.Invalid("Tausch: empfohlen ≤ Pflicht ≤ untragbar (in Jahren)")
		}
		last = v
	}
	return nil
}

// replaceState: Stufe des Tauschs eines Geräts heute (Grenzen aus Gerätetyp, sonst Sparte).
func (m *Module) replaceState(ctx context.Context, cc, eq string) (replaceInfo, error) {
	res, err := m.db.Query(ctx, `SELECT COALESCE(e.calibration_year, 0), COALESCE(e.build_year, 0),
			(SELECT MIN(i.valid_from) FROM techobj__installation i WHERE i.company_code = e.company_code AND i.equipment_id = e.equipment_id),
			t.replace_should_years, t.replace_must_years, t.replace_critical_years, t.calibration_years,
			md.replace_should_years, md.replace_must_years, md.replace_critical_years, md.calibration_years,
			COALESCE(o.name, ''), COALESCE(o.replace_by_us, ?),
			-- ausgebauter Zähler (früher eingebaut, heute nicht): kein Tausch mehr (seit 0.6.4)
			COALESCE(k.is_meter, false) AND EXISTS (SELECT 1 FROM techobj__installation i WHERE i.company_code = e.company_code AND i.equipment_id = e.equipment_id)
				AND NOT EXISTS (SELECT 1 FROM techobj__installation i WHERE i.company_code = e.company_code AND i.equipment_id = e.equipment_id AND i.valid_from <= ? AND i.valid_to >= ?)
		FROM techobj__equipment e
		LEFT JOIN techobj__object_class k ON k.company_code = e.company_code AND k.code = e.object_class
		LEFT JOIN techobj__ownership o ON o.company_code = e.company_code AND o.code = e.ownership
		LEFT JOIN techobj__equipment_type t ON t.company_code = e.company_code AND t.type_id = e.type_id
		LEFT JOIN techobj__medium md ON md.company_code = e.company_code AND md.code = t.medium
		WHERE e.company_code = ? AND e.equipment_id = ?`, true, crud.KeyDate(ctx), crud.KeyDate(ctx), cc, eq)
	if err != nil || len(res.Rows) == 0 {
		return replaceInfo{}, err
	}
	r := res.Rows[0]
	var info replaceInfo
	if crud.AsBool(r[13]) {
		return info, nil
	}
	if !crud.AsBool(r[12]) {
		info.owner = crud.Str(r[11]) // ohne Eigentum: wie eigenes Gerät
	}
	switch {
	case toInt(r[0]) > 0:
		info.base, info.baseText = toInt(r[0]), "Eichjahr"
	case toInt(r[1]) > 0:
		info.base, info.baseText = toInt(r[1]), "Baujahr"
	case len(date(r[2])) >= 4:
		y, _ := strconv.ParseInt(date(r[2])[:4], 10, 64)
		info.base, info.baseText = y, "Einbau"
	default:
		return info, nil
	}
	// Gerätetyp vor Sparte; Pflicht ohne Angabe = Eichfrist
	pick := func(typ, med any) int64 {
		if typ != nil {
			return toInt(typ)
		}
		if med != nil {
			return toInt(med)
		}
		return 0
	}
	should, must, critical := pick(r[3], r[7]), pick(r[4], r[8]), pick(r[5], r[9])
	// Regel einer Anlage (nächste mit Angabe, aufwärts) geht Gerätetyp und Sparte vor.
	if years, plant, err := m.plantReplaceYears(ctx, cc, eq); err != nil {
		return info, err
	} else if years > 0 {
		must, info.plant = years, plant
		if critical > 0 && critical < must {
			critical = 0
		}
		if should >= must {
			should = 0
		}
	}
	if must == 0 {
		must, info.fromCalibration = pick(r[6], r[10]), true
	}
	at := func(years int64) int64 {
		if years <= 0 {
			return 0
		}
		return info.base + years
	}
	info.should, info.must, info.critical = at(should), at(must), at(critical)
	if info.fromCalibration && info.must > 0 {
		info.must++ // Eichfrist läuft bis Ende des Jahres Eichjahr + Frist
	}
	now, _ := strconv.ParseInt(crud.KeyDate(ctx)[:4], 10, 64)
	switch {
	case info.critical > 0 && now >= info.critical:
		info.level = replaceCritical
	case info.must > 0 && now >= info.must:
		info.level = replaceMust
	case info.should > 0 && now >= info.should:
		info.level = replaceShould
	}
	return info, nil
}

// plantReplaceYears: „Tausch nach Jahren“ der Anlage, zu der das Gerät heute gehört
// (über seinen Messpunkt oder direkt), sonst der nächsten übergeordneten mit Angabe.
func (m *Module) plantReplaceYears(ctx context.Context, cc, eq string) (int64, string, error) {
	day := crud.KeyDate(ctx)
	res, err := m.db.Query(ctx, `WITH RECURSIVE up(plant_id, depth) AS (
			SELECT pp.plant_id, 0 FROM techobj__installation i JOIN techobj__plant_point pp ON pp.company_code = i.company_code AND pp.point_id = i.point_id
				AND pp.valid_from <= ? AND pp.valid_to >= ?
			WHERE i.company_code = ? AND i.equipment_id = ? AND i.valid_from <= ? AND i.valid_to >= ?
			UNION
			SELECT po.plant_id, 0 FROM techobj__plant_object po WHERE po.company_code = ? AND po.equipment_id = ? AND po.valid_from <= ? AND po.valid_to >= ?
			UNION ALL
			SELECT p.parent_id, up.depth + 1 FROM up JOIN techobj__plant p ON p.company_code = ? AND p.plant_id = up.plant_id
			WHERE p.parent_id IS NOT NULL AND p.parent_id <> '' AND up.depth < 20)
		SELECT p.replace_must_years, p.plant_id || ' ' || p.designation FROM up JOIN techobj__plant p ON p.company_code = ? AND p.plant_id = up.plant_id
		WHERE p.replace_must_years > 0 ORDER BY up.depth LIMIT 1`,
		day, day, cc, eq, day, day, cc, eq, day, day, cc, cc)
	if err != nil || len(res.Rows) == 0 {
		return 0, "", err
	}
	return toInt(res.Rows[0][0]), crud.Str(res.Rows[0][1]), nil
}
