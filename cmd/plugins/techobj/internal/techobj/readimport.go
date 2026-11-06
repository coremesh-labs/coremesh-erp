package techobj

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Ablesungen einlesen (seit 0.7.0) ----------------------------------------------------
//
// Export des Messdienstes (z. B. Monatswerte fernablesbarer Geräte) nach einem
// Importprofil: Spalten, Datumsformat, Dezimalkomma, Nummernart der Gerätenummer,
// Ableseart. Jede Zeile wird eine gewöhnliche Ablesung (dieselben Prüfungen wie von
// Hand); vorhandene Stände bleiben, abweichende werden gemeldet, nicht überschrieben.

const (
	layoutLong = "LONG"
	layoutWide = "WIDE"

	valueStand       = "STAND"
	valueConsumption = "CONSUMPTION"
)

var layoutOptions = []metamodel.Option{
	{Value: layoutLong, Label: "eine Zeile je Stand (Gerät, Datum, Stand)"},
	{Value: layoutWide, Label: "Monatstabelle (eine Zeile je Gerät, Datum in der Kopfzeile)"},
}

var valueKindOptions = []metamodel.Option{
	{Value: valueStand, Label: "Zählerstand (bei Heizkostenverteilern: Stand seit dem Stichtag)"},
	{Value: valueConsumption, Label: "Verbrauch seit dem vorigen Stand (wird zum Stand addiert)"},
}

func (m *Module) checkImportProfile(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["layout"], rec["value_kind"] = trimUpper(rec["layout"]), trimUpper(rec["value_kind"])
	if rec["layout"] != layoutLong && rec["layout"] != layoutWide {
		return crud.Invalid("Aufbau %q gibt es nicht (LONG, WIDE)", crud.Str(rec["layout"]))
	}
	if rec["value_kind"] != valueStand && rec["value_kind"] != valueConsumption {
		return crud.Invalid("Wert %q gibt es nicht (STAND, CONSUMPTION)", crud.Str(rec["value_kind"]))
	}
	if _, err := separatorOf(crud.Str(rec["separator"])); err != nil {
		return err
	}
	if toInt(rec["col_number"]) < 1 || toInt(rec["col_value"]) < 1 {
		return crud.Invalid("Spalten Gerätenummer und Stand: ab 1")
	}
	if rec["layout"] == layoutLong && toInt(rec["col_date"]) < 1 {
		return crud.Invalid("Spalte Datum fehlt (eine Zeile je Stand)")
	}
	if rec["layout"] == layoutWide && toInt(rec["header_rows"]) < 1 {
		return crud.Invalid("Monatstabelle: das Datum steht in der Kopfzeile – mindestens 1 Kopfzeile")
	}
	if toInt(rec["header_rows"]) < 0 {
		return crud.Invalid("Kopfzeilen: nicht negativ")
	}
	if _, _, err := goDateLayout(crud.Str(rec["date_format"])); err != nil {
		return err
	}
	rec["number_type"] = nilIfEmpty(trimUpper(rec["number_type"]))
	if rec["number_type"] != nil {
		if err := m.requireCatalog(ctx, "number_type", "Nummernart", cc, rec["number_type"]); err != nil {
			return err
		}
	}
	rec["register_code"] = nilIfEmpty(trimUpper(rec["register_code"]))
	rec["reading_type"] = trimUpper(rec["reading_type"])
	rec["reader"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["reader"])))
	return m.requireCatalog(ctx, "reading_type", "Ableseart", cc, rec["reading_type"])
}

func separatorOf(s string) (rune, error) {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "TAB", `\T`:
		return '\t', nil
	case "":
		if s == "\t" {
			return '\t', nil
		}
	}
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) != 1 {
		return 0, crud.Invalid("Trennzeichen %q: ein Zeichen oder TAB", s)
	}
	r, _ := utf8.DecodeRuneInString(s)
	return r, nil
}

// goDateLayout: TT.MM.JJJJ → 02.01.2006; withDay = Tag im Format (sonst Monatsletzter).
func goDateLayout(f string) (layout string, withDay bool, err error) {
	f = strings.ToUpper(strings.TrimSpace(f))
	if f == "" {
		return "", false, crud.Invalid("Datumsformat fehlt (z. B. TT.MM.JJJJ)")
	}
	withDay = strings.Contains(f, "TT")
	if !strings.Contains(f, "MM") || !strings.Contains(f, "JJ") {
		return "", false, crud.Invalid("Datumsformat %q: Monat (MM) und Jahr (JJJJ oder JJ) gehören hinein", f)
	}
	r := strings.NewReplacer("JJJJ", "2006", "JJ", "06", "TT", "02", "MM", "01")
	return r.Replace(f), withDay, nil
}

type importProfile struct {
	code, layout, kind, numberType, register, readingType, reader string
	sep                                                           rune
	header, colNo, colReg, colDate, colValue                      int
	dateLayout                                                    string
	withDay, comma                                                bool
}

func (m *Module) loadImportProfile(ctx context.Context, cc, code string) (*importProfile, error) {
	res, err := m.db.Query(ctx, `SELECT layout, separator, header_rows, col_number, COALESCE(col_register, 0), COALESCE(col_date, 0), col_value, date_format,
		decimal_comma, value_kind, COALESCE(number_type, ''), COALESCE(register_code, ''), reading_type, COALESCE(reader, '')
		FROM techobj__import_profile WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Importprofil %s|%s", sdk.ErrNotFound, cc, code)
	}
	r := res.Rows[0]
	p := &importProfile{code: code, layout: crud.Str(r[0]), header: int(toInt(r[2])), colNo: int(toInt(r[3])), colReg: int(toInt(r[4])),
		colDate: int(toInt(r[5])), colValue: int(toInt(r[6])), comma: crud.AsBool(r[8]), kind: crud.Str(r[9]), numberType: crud.Str(r[10]),
		register: crud.Str(r[11]), readingType: crud.Str(r[12]), reader: crud.Str(r[13])}
	if p.sep, err = separatorOf(crud.Str(r[1])); err != nil {
		return nil, err
	}
	if p.dateLayout, p.withDay, err = goDateLayout(crud.Str(r[7])); err != nil {
		return nil, err
	}
	return p, nil
}

// decodeText: UTF-8 (mit oder ohne BOM), sonst Windows-1252/Latin-1 wie aus Excel.
func decodeText(b []byte) string {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func (p *importProfile) date(s string) (string, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse(p.dateLayout, s)
	if err != nil {
		return "", fmt.Errorf("Datum %q passt nicht zum Format", s)
	}
	if !p.withDay {
		t = t.AddDate(0, 1, -1) // Monatsletzter
	}
	return t.Format(time.DateOnly), nil
}

func (p *importProfile) number(s string) (float64, bool, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "-" {
		return 0, false, nil
	}
	if p.comma {
		s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
	} else {
		s = strings.ReplaceAll(s, ",", "")
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, " ", ""), 64)
	if err != nil {
		return 0, false, fmt.Errorf("Wert %q ist keine Zahl", s)
	}
	return f, true, nil
}

type importLine struct {
	row            int
	number, reg    string
	day            string
	value          float64
	equipment      string
	equipmentFound bool
}

func cell(rec []string, col int) string {
	if col < 1 || col > len(rec) {
		return ""
	}
	return strings.TrimSpace(rec[col-1])
}

// parse: Zeilen der Datei nach dem Profil (ohne Gerätesuche).
func (p *importProfile) parse(text string) ([]importLine, []string, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma, r.LazyQuotes, r.FieldsPerRecord, r.TrimLeadingSpace = p.sep, true, -1, true
	var lines []importLine
	var problems []string
	var header []string
	for row := 1; ; row++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, crud.Invalid("Zeile %d: %v", row, err)
		}
		if row <= p.header {
			header = rec
			continue
		}
		no := cell(rec, p.colNo)
		if no == "" {
			continue
		}
		reg := strings.ToUpper(cell(rec, p.colReg))
		if p.layout == layoutWide {
			for col := p.colValue; col <= len(rec); col++ {
				v, ok, err := p.number(rec[col-1])
				if err != nil {
					problems = append(problems, fmt.Sprintf("Zeile %d, Spalte %d: %v", row, col, err))
					continue
				}
				if !ok {
					continue
				}
				day, err := p.date(cell(header, col))
				if err != nil {
					problems = append(problems, fmt.Sprintf("Kopfzeile, Spalte %d: %v", col, err))
					continue
				}
				lines = append(lines, importLine{row: row, number: no, reg: reg, day: day, value: v})
			}
			continue
		}
		day, err := p.date(cell(rec, p.colDate))
		if err != nil {
			problems = append(problems, fmt.Sprintf("Zeile %d: %v", row, err))
			continue
		}
		v, ok, err := p.number(cell(rec, p.colValue))
		if err != nil {
			problems = append(problems, fmt.Sprintf("Zeile %d: %v", row, err))
			continue
		}
		if ok {
			lines = append(lines, importLine{row: row, number: no, reg: reg, day: day, value: v})
		}
	}
	return lines, problems, nil
}

// equipmentOf: Gerät zur Nummer (Nummernart des Profils; auch ohne führende Nullen).
func (m *Module) equipmentOf(ctx context.Context, cc, numberType, no string) (string, error) {
	q := "SELECT DISTINCT equipment_id FROM techobj__equipment_number WHERE company_code = ? AND (number = ? OR LTRIM(number, '0') = LTRIM(?, '0'))"
	args := []any{cc, no, no}
	if numberType != "" {
		q += " AND number_type = ?"
		args = append(args, numberType)
	}
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return "", err
	}
	switch len(res.Rows) {
	case 0:
		return "", nil
	case 1:
		return crud.Str(res.Rows[0][0]), nil
	}
	return "", crud.Invalid("Nummer %s gehört zu mehreren Geräten", no)
}

// importReadingsAction: TechImportProfile.readFile {id, data: {import_file, file_name, import_check}}.
func (m *Module) importReadingsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, code, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Importprofil %q: Buchungskreis|Schlüssel erwartet", in.ID)
	}
	check := crud.AsBool(in.Data["import_check"])
	if !check {
		if err := checkWrite("MeterReading")(ctx, "create", crud.Record{"company_code": cc}); err != nil {
			return sdk.Response{}, err
		}
	}
	p, err := m.loadImportProfile(ctx, cc, code)
	if err != nil {
		return sdk.Response{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(crud.Str(in.Data["import_file"]))
	if err != nil {
		raw = []byte(crud.Str(in.Data["import_file"]))
	}
	fileName := strings.TrimSpace(crud.Str(in.Data["file_name"]))
	if fileName == "" {
		fileName = "Import " + time.Now().Format("02.01.2006")
	}
	out, err := m.importReadings(ctx, cc, p, decodeText(raw), fileName, check)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

type importResult struct {
	read, created, same, differ, unknown, failed int
	notes                                        []string
}

func (r *importResult) note(s string) {
	if len(r.notes) < 30 {
		r.notes = append(r.notes, s)
	} else if len(r.notes) == 30 {
		r.notes = append(r.notes, "…")
	}
}

func (m *Module) importReadings(ctx context.Context, cc string, p *importProfile, text, fileName string, check bool) (map[string]any, error) {
	lines, problems, err := p.parse(text)
	if err != nil {
		return nil, err
	}
	var res importResult
	res.read = len(lines)
	for _, s := range problems {
		res.failed++
		res.note(s)
	}
	unknown := map[string]bool{}
	// Gerät und Zählwerk; dann je Zählwerk nach Datum (Verbrauch → Stand)
	regs := map[string]string{}
	for i := range lines {
		l := &lines[i]
		eq, err := m.equipmentOf(ctx, cc, p.numberType, l.number)
		if err != nil {
			return nil, err
		}
		if eq == "" {
			if !unknown[l.number] {
				unknown[l.number] = true
				res.note(fmt.Sprintf("Gerät mit Nummer %s nicht gefunden (Zeile %d)", l.number, l.row))
			}
			res.unknown++
			continue
		}
		l.equipment, l.equipmentFound = eq, true
		if l.reg == "" {
			l.reg = p.register
		}
		if l.reg == "" {
			if r, ok := regs[eq]; ok {
				l.reg = r
			} else {
				rr, err := m.db.Query(ctx, "SELECT register_code FROM techobj__register WHERE company_code = ? AND equipment_id = ?", cc, eq)
				if err != nil {
					return nil, err
				}
				if len(rr.Rows) == 1 {
					l.reg = crud.Str(rr.Rows[0][0])
				}
				regs[eq] = l.reg
			}
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.equipment != b.equipment {
			return a.equipment < b.equipment
		}
		if a.reg != b.reg {
			return a.reg < b.reg
		}
		return a.day < b.day
	})
	last := map[string]float64{} // Stand je Zählwerk (Verbrauch → Stand, auch im Prüflauf)
	entity := m.set.Entity("MeterReading")
	for _, l := range lines {
		if !l.equipmentFound {
			continue
		}
		if l.reg == "" {
			res.failed++
			res.note(fmt.Sprintf("Zeile %d, Gerät %s: Zählwerk nicht eindeutig – Spalte oder Zählwerk im Profil angeben", l.row, l.equipment))
			continue
		}
		value := l.value
		k := l.equipment + "|" + l.reg
		if p.kind == valueConsumption {
			prev, ok := last[k]
			if !ok {
				s, err := m.one(ctx, `SELECT value FROM techobj__reading WHERE company_code = ? AND equipment_id = ? AND register_code = ? AND read_on < ?
					ORDER BY read_on DESC, value DESC LIMIT 1`, cc, l.equipment, l.reg, l.day)
				if err != nil {
					return nil, err
				}
				if s == "" {
					res.failed++
					res.note(fmt.Sprintf("Zeile %d, Gerät %s: kein voriger Stand für den Verbrauch", l.row, l.equipment))
					continue
				}
				prev = toFloat(s)
			}
			value = prev + l.value
		}
		old, err := m.db.Query(ctx, "SELECT value FROM techobj__reading WHERE company_code = ? AND equipment_id = ? AND register_code = ? AND read_on = ? AND reading_type = ?",
			cc, l.equipment, l.reg, l.day, p.readingType)
		if err != nil {
			return nil, err
		}
		if len(old.Rows) > 0 {
			ov := toFloat(old.Rows[0][0])
			last[k] = ov
			if fmt.Sprintf("%.6f", ov) == fmt.Sprintf("%.6f", value) {
				res.same++
			} else {
				res.differ++
				res.note(fmt.Sprintf("Gerät %s am %s: vorhanden %s, Datei %s – nicht überschrieben", l.equipment, germanDate(l.day), fmtNum(ov), fmtNum(value)))
			}
			continue
		}
		last[k] = value
		if check {
			res.created++
			continue
		}
		data := map[string]any{"company_code": cc, "equipment_id": l.equipment, "register_code": l.reg, "read_on": l.day,
			"reading_type": p.readingType, "value": value, "source": fileName, "reader": nilIfEmpty(p.reader)}
		if _, err := entity.Create(ctx, map[string]any{"data": data}); err != nil {
			res.failed++
			res.note(fmt.Sprintf("Gerät %s am %s: %v", l.equipment, germanDate(l.day), err))
			delete(last, k)
			continue
		}
		res.created++
	}
	verb := "eingelesen"
	if check {
		verb = "neu (Prüflauf, nichts gespeichert)"
	}
	msg := fmt.Sprintf("%d Werte gelesen: %d %s, %d schon vorhanden", res.read, res.created, verb, res.same)
	if res.differ > 0 {
		msg += fmt.Sprintf(", %d abweichend", res.differ)
	}
	if res.unknown > 0 {
		msg += fmt.Sprintf(", %d ohne Gerät (%d Nummern)", res.unknown, len(unknown))
	}
	if res.failed > 0 {
		msg += fmt.Sprintf(", %d Fehler", res.failed)
	}
	if len(res.notes) > 0 {
		msg += "\n" + strings.Join(res.notes, "\n")
	}
	return map[string]any{"read": res.read, "created": res.created, "same": res.same, "differ": res.differ, "unknown": res.unknown,
		"failed": res.failed, "message": msg}, nil
}
