package heatcost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Nutzerwechsel (§ 9b HeizkostenV, seit 0.1.4) ------------------------------------
//
// Vor- und Nachnutzer sind zwei Nutzer mit demselben Mietobjekt und eigenem
// Nutzungszeitraum. Verbrauchskosten folgen der Zwischenablesung (Werte je
// Nutzungszeitraum), Grundkosten der Heizung nach Gradtagszahlen oder
// Kalendertagen (Einstellung heat_change), Grundkosten Warmwasser und Schlüssel
// mit „zeitanteilig“ nach Kalendertagen. Ohne Zwischenablesung (§ 9b Abs. 3)
// wird der Verbrauch des Mietobjekts im ganzen Zeitraum nach denselben
// Maßstäben aufgeteilt. Gradtagszahlen: Katalog je Tabelle, Jahr und Monat
// (Promille), angebrochene Monate nach Tagen.

const (
	changeDegree = "DEGREE_DAYS"
	changeTime   = "TIME"
)

var heatChangeOptions = []metamodel.Option{
	{Value: changeDegree, Label: "nach Gradtagszahlen", Short: "Gradtagszahlen"},
	{Value: changeTime, Label: "nach Kalendertagen (zeitanteilig)", Short: "Kalendertage"},
}

// degreeDefaults: Vorschlag VDI 2067 / DIN 4713 (Promille je Monat; Juni bis August zusammen 40, nach Tagen geteilt).
var degreeDefaults = map[string]map[int]float64{
	"VDI2067": {1: 170, 2: 150, 3: 130, 4: 80, 5: 40, 6: 13.043, 7: 13.478, 8: 13.479, 9: 30, 10: 80, 11: 120, 12: 160},
}

var degreeYears = []int{2024, 2025, 2026}

// degreeDaysDefault: Monatswerte einer Tabelle für die Vorschlagsjahre, soweit sie fehlen.
func (m *Module) degreeDaysDefault(ctx context.Context, cc, table string) (int, error) {
	n := 0
	for _, y := range degreeYears {
		if has, err := m.one(ctx, "SELECT 1 FROM heatcost__degree_day WHERE company_code = ? AND table_code = ? AND year = ? LIMIT 1", cc, table, y); err != nil {
			return 0, err
		} else if has != "" {
			continue
		}
		for mo := 1; mo <= 12; mo++ {
			v, ok := degreeDefaults[table][mo]
			if !ok {
				continue
			}
			if _, err := m.db.Exec(ctx, "INSERT INTO heatcost__degree_day (company_code, table_code, year, month, promille) VALUES (?, ?, ?, ?, ?)", cc, table, y, mo, v); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

var monthNames = []string{"", "Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

// degreeDay: Monatswert einer Tabelle (Einstellungen → Gradtagszahlen → Werte).
func (m *Module) degreeDay() *crud.Entity {
	e := &crud.Entity{
		Object: "HeatCostDegreeDay", Customizing: true, Title: "Gradtagszahlen je Monat", Icon: "icon-tag", Table: "heatcost__degree_day", Section: "Einstellungen",
		Keys: []string{"company_code", "table_code", "year", "month"}, Order: "company_code, table_code, year, month",
		Filters: []string{"company_code", "table_code", "year"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "table_code", Label: "Tabelle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("HeatCostDegreeTable")},
			{Key: "year", Label: "Jahr", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "month", Label: "Monat (1–12)", Short: "Monat", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "promille", Label: "Gradtagszahl in Promille des Jahres", Short: "Promille", Type: tNum, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung (z. B. Quelle)", Short: "Bemerkung", Type: tText, Listable: true},
		},
		Access:      &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: checkWrite("HeatCostDegreeTable"),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			rec["table_code"] = trimUpper(rec["table_code"])
			if ok, err := m.exists(ctx, "degree_table", cc, crud.Str(rec["table_code"])); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Tabelle der Gradtagszahlen %s gibt es nicht", crud.Str(rec["table_code"]))
			}
			if y := toInt(rec["year"]); y < 1900 || y > 2200 {
				return crud.Invalid("Jahr %v: 1900 bis 2200", rec["year"])
			}
			if mo := toInt(rec["month"]); mo < 1 || mo > 12 {
				return crud.Invalid("Monat %v: 1 bis 12", rec["month"])
			}
			if p := toFloat(rec["promille"]); p < 0 || p > 1000 {
				return crud.Invalid("Gradtagszahl %v ‰: 0 bis 1000", p)
			}
			rec["note"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["note"])))
			return nil
		},
	}
	m.withLabels(e)
	return e
}

// copyYearAction: HeatCostDegreeTable.copyYear {id, data: {dy_from, dy_to}} – Monatswerte eines Jahres kopieren.
func (m *Module) copyYearAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, table, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostDegreeTable", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	from, to := toInt(in.Data["dy_from"]), toInt(in.Data["dy_to"])
	if to < 1900 || to > 2200 {
		return sdk.Response{}, crud.Invalid("Neues Jahr fehlt")
	}
	if from == 0 {
		last, err := m.one(ctx, "SELECT MAX(year) FROM heatcost__degree_day WHERE company_code = ? AND table_code = ? AND year < ?", cc, table, to)
		if err != nil {
			return sdk.Response{}, err
		}
		from = int64(toFloat(last))
	}
	if has, err := m.one(ctx, "SELECT 1 FROM heatcost__degree_day WHERE company_code = ? AND table_code = ? AND year = ? LIMIT 1", cc, table, to); err != nil {
		return sdk.Response{}, err
	} else if has != "" {
		return sdk.Response{}, crud.Invalid("Für %d gibt es in %s schon Werte – dort ändern", to, table)
	}
	res, err := m.db.Exec(ctx, `INSERT INTO heatcost__degree_day (company_code, table_code, year, month, promille, note)
		SELECT company_code, table_code, ?, month, promille, ? FROM heatcost__degree_day WHERE company_code = ? AND table_code = ? AND year = ?`,
		to, fmt.Sprintf("kopiert aus %d", from), cc, table, from)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.RowsAffected == 0 {
		return sdk.Response{}, crud.Invalid("In %s gibt es für %d keine Werte zum Kopieren", table, from)
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%d Monatswerte von %d nach %d kopiert – bei Bedarf an die Werte des Jahres anpassen", res.RowsAffected, from, to)}}, nil
}

// --- Nutzungsanteile -------------------------------------------------------------------

func parseDay(s string) time.Time {
	t, _ := time.Parse(time.DateOnly, s)
	return t
}

func daysIn(y int, mo time.Month) int {
	return time.Date(y, mo+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// clip: Nutzungszeitraum im Abrechnungszeitraum (leer = ganz).
func clip(from, to, uf, ut string) (string, string) {
	if uf != "" && uf > from {
		from = uf
	}
	if ut != "" && ut < to {
		to = ut
	}
	return from, to
}

// shares: Anteil eines Nutzungszeitraums am Abrechnungszeitraum nach Kalendertagen
// und nach Gradtagszahlen (degree = nil: nur Kalendertage).
type shares struct {
	time, degree    float64
	partial         bool
	days, allDays   int
	degPart, degAll float64
}

type degreeTable map[[2]int]float64 // (Jahr, Monat) → Promille

func (m *Module) loadDegree(ctx context.Context, cc, table string) (degreeTable, error) {
	res, err := m.db.Query(ctx, "SELECT year, month, promille FROM heatcost__degree_day WHERE company_code = ? AND table_code = ?", cc, table)
	if err != nil {
		return nil, err
	}
	out := degreeTable{}
	for _, r := range res.Rows {
		out[[2]int{int(toInt(r[0])), int(toInt(r[1]))}] = toFloat(r[2])
	}
	return out, nil
}

// weightOf: Summe der Tageswerte im Zeitraum (Gradtagszahl des Monats ÷ Tage des Monats).
func (d degreeTable) weightOf(table, from, to string) (float64, error) {
	sum := 0.0
	for day := parseDay(from); !day.After(parseDay(to)); day = day.AddDate(0, 0, 1) {
		v, ok := d[[2]int{day.Year(), int(day.Month())}]
		if !ok {
			return 0, crud.Invalid("Gradtagszahlen %s: Wert für %s %d fehlt – Einstellungen → Gradtagszahlen („Jahr anlegen …“)", table, monthNames[day.Month()], day.Year())
		}
		sum += v / float64(daysIn(day.Year(), day.Month()))
	}
	return sum, nil
}

func dayCount(from, to string) float64 {
	return parseDay(to).Sub(parseDay(from)).Hours()/24 + 1
}

// shareOf: Anteil des Nutzungszeitraums (uf…ut) am Abrechnungszeitraum.
func shareOf(from, to, uf, ut string, d degreeTable, table string) (shares, error) {
	f, t := clip(from, to, uf, ut)
	if f > t {
		return shares{partial: true, allDays: int(dayCount(from, to))}, nil
	}
	s := shares{time: 1, degree: 1}
	if f == from && t == to {
		return s, nil
	}
	s.partial = true
	s.days, s.allDays = int(dayCount(f, t)), int(dayCount(from, to))
	s.time = dayCount(f, t) / dayCount(from, to)
	s.degree = s.time
	if d != nil {
		all, err := d.weightOf(table, from, to)
		if err != nil {
			return s, err
		}
		part, err := d.weightOf(table, f, t)
		if err != nil {
			return s, err
		}
		if all > 0 {
			s.degree = part / all
		}
		s.degPart, s.degAll = part, all
	}
	return s, nil
}
