package bank

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Felder eines Umsatzes, denen eine Spalte zugewiesen werden kann.
var targetOptions = []metamodel.Option{
	{Value: "booking_date", Label: "Buchungstag"}, {Value: "value_date", Label: "Wertstellung"},
	{Value: "amount", Label: "Betrag"}, {Value: "debit_credit", Label: "Soll/Haben-Kennzeichen"}, {Value: "currency", Label: "Währung"},
	{Value: "counterparty_name", Label: "Name der Gegenseite"}, {Value: "counterparty_iban", Label: "IBAN der Gegenseite"},
	{Value: "counterparty_bic", Label: "BIC der Gegenseite"}, {Value: "purpose", Label: "Verwendungszweck"},
	{Value: "booking_text", Label: "Buchungstext"}, {Value: "transaction_type", Label: "Umsatzart"},
	{Value: "end_to_end_ref", Label: "End-to-End-Referenz"}, {Value: "mandate_ref", Label: "Mandatsreferenz"},
	{Value: "creditor_id", Label: "Gläubiger-ID"}, {Value: "customer_ref", Label: "Kundenreferenz"},
	{Value: "own_iban", Label: "IBAN des eigenen Kontos (Prüfung)"},
}

var transformOptions = []metamodel.Option{
	{Value: "NONE", Label: "unverändert"}, {Value: "TRIM", Label: "Leerzeichen zusammenfassen"},
	{Value: "UPPER", Label: "Großbuchstaben"}, {Value: "NEGATE", Label: "Vorzeichen umkehren (Betrag)"},
	{Value: "EXTRACT", Label: "Auszug: regulärer Ausdruck, erste Gruppe"}, {Value: "IBAN", Label: "IBAN im Text finden"},
}

var dateFormats = map[string]string{"DD.MM.YYYY": "02.01.2006", "DD.MM.YY": "02.01.06", "YYYY-MM-DD": "2006-01-02", "MM/DD/YYYY": "01/02/2006",
	"DD/MM/YYYY": "02/01/2006", "YYYYMMDD": "20060102"}

var dateFormatOptions = []metamodel.Option{
	{Value: "DD.MM.YYYY", Label: "TT.MM.JJJJ"}, {Value: "DD.MM.YY", Label: "TT.MM.JJ"}, {Value: "YYYY-MM-DD", Label: "JJJJ-MM-TT"},
	{Value: "DD/MM/YYYY", Label: "TT/MM/JJJJ"}, {Value: "MM/DD/YYYY", Label: "MM/TT/JJJJ"}, {Value: "YYYYMMDD", Label: "JJJJMMTT"},
}

var ibanStartRe = regexp.MustCompile(`\b[A-Z]{2}[0-9]{2}`)

func (m *Module) format() *crud.Entity {
	return &crud.Entity{
		Object: formatObject, Title: "Importformate", Icon: "icon-file", Table: "bank__format", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Code", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "delimiter", Label: "Trennzeichen (Standard ;)", Type: tText, Group: "Datei"},
			{Key: "skip_lines", Label: "Zeilen vor der Kopfzeile überspringen", Type: tNum, Group: "Datei"},
			{Key: "no_header", Label: "Datei ohne Kopfzeile (Spalten als Nummer 1, 2, …)", Type: tBool, Group: "Datei"},
			{Key: "newest_first", Label: "Datei beginnt mit dem neuesten Umsatz", Type: tBool, Group: "Datei"},
			{Key: "date_format", Label: "Datumsformat", Type: tSel, Options: dateFormatOptions, Group: "Werte"},
			{Key: "decimal_separator", Label: "Dezimaltrennzeichen (, oder .)", Type: tText, Group: "Werte"},
			{Key: "debit_values", Label: "Werte der Spalte Soll/Haben für Belastungen (z. B. S,Soll,D)", Type: tText, Group: "Werte"},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "spalten", Title: "Spalten", Relation: &metamodel.Relation{Object: columnObject, ForeignKey: "format",
				Match:   map[string]string{"company_code": "company_code", "format": "code"},
				Columns: []string{"line_no", "target", "source", "transform", "pattern", "fallback"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["code"] = trimUpper(rec["code"])
			defaults(rec, map[string]any{"delimiter": ";", "skip_lines": 0, "date_format": "DD.MM.YYYY", "decimal_separator": ",",
				"no_header": false, "newest_first": false})
			if d := crud.Str(rec["delimiter"]); d == `\t` || strings.EqualFold(d, "tab") {
				rec["delimiter"] = "\t"
			} else if len([]rune(d)) != 1 {
				return crud.Invalid("Trennzeichen: genau ein Zeichen (oder TAB)")
			}
			if s := crud.Str(rec["decimal_separator"]); s != "," && s != "." {
				return crud.Invalid("Dezimaltrennzeichen: , oder .")
			}
			if _, ok := dateFormats[crud.Str(rec["date_format"])]; !ok {
				return crud.Invalid("Datumsformat %q unbekannt", crud.Str(rec["date_format"]))
			}
			if toInt(rec["skip_lines"]) < 0 {
				return crud.Invalid("Zeilen überspringen: 0 oder mehr")
			}
			rec["debit_values"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["debit_values"])))
			return nil
		},
	}
}

func (m *Module) column() *crud.Entity {
	return &crud.Entity{
		Object: columnObject, Title: "Spalten des Importformats", Icon: "icon-list", Table: "bank__format_column", Section: "Einstellungen",
		Keys: []string{"company_code", "format", "line_no"}, Order: "company_code, format, line_no",
		Filters: []string{"company_code", "format", "target"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "format", Label: "Importformat", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupFormat},
			{Key: "line_no", Label: "Zeile", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "target", Label: "Feld des Umsatzes", Type: tSel, Required: true, Listable: true, Options: targetOptions},
			{Key: "source", Label: "Spalte der Datei (Name oder Nummer ab 1)", Type: tText, Required: true, Listable: true},
			{Key: "transform", Label: "Aufbereitung", Type: tSel, Listable: true, Options: transformOptions},
			{Key: "pattern", Label: "Regulärer Ausdruck (Auszug: erste Gruppe)", Type: tText, Listable: true,
				ShowIf: &metamodel.Condition{Field: "transform", Values: []string{"EXTRACT"}}},
			{Key: "fallback", Label: "Nur, wenn das Feld noch leer ist (Ersatzspalte)", Type: tBool, Listable: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Prepare: func(ctx context.Context, rec crud.Record) error {
			res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 10 FROM bank__format_column WHERE company_code = ? AND format = ?`,
				rec["company_code"], trimUpper(rec["format"]))
			if err != nil {
				return err
			}
			rec["line_no"] = toInt(res.Rows[0][0])
			return nil
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["format"] = trimUpper(rec["format"])
			rec["source"] = strings.TrimSpace(crud.Str(rec["source"]))
			defaults(rec, map[string]any{"transform": "NONE", "fallback": false})
			if crud.Str(rec["transform"]) == "EXTRACT" {
				re, err := regexp.Compile(crud.Str(rec["pattern"]))
				if err != nil {
					return crud.Invalid("Regulärer Ausdruck: %v", err)
				}
				if re.NumSubexp() < 1 {
					return crud.Invalid("Regulärer Ausdruck: eine Gruppe (…) für den Auszug ist Pflicht")
				}
			} else {
				rec["pattern"] = nil
			}
			return nil
		},
	}
}

// --- Datei lesen -----------------------------------------------------------------

type formatDef struct {
	Code, Delimiter, DateFormat, DecimalSep string
	SkipLines                               int
	NoHeader, NewestFirst                   bool
	DebitValues                             []string
	Columns                                 []columnDef
}

type columnDef struct {
	Target, Source, Transform string
	Pattern                   *regexp.Regexp
	Fallback                  bool
}

func (m *Module) loadFormat(ctx context.Context, cc, code string) (*formatDef, error) {
	res, err := m.db.Query(ctx, `SELECT code, delimiter, skip_lines, no_header, date_format, decimal_separator, newest_first, debit_values
		FROM bank__format WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Importformat %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	r := res.Rows[0]
	f := &formatDef{Code: crud.Str(r[0]), Delimiter: crud.Str(r[1]), SkipLines: int(toInt(r[2])), NoHeader: crud.AsBool(r[3]),
		DateFormat: crud.Str(r[4]), DecimalSep: crud.Str(r[5]), NewestFirst: crud.AsBool(r[6])}
	for _, v := range strings.Split(crud.Str(r[7]), ",") {
		if v = strings.TrimSpace(v); v != "" {
			f.DebitValues = append(f.DebitValues, strings.ToUpper(v))
		}
	}
	cols, err := m.db.Query(ctx, `SELECT target, source, transform, pattern, fallback FROM bank__format_column WHERE company_code = ? AND format = ?
		ORDER BY line_no`, cc, code)
	if err != nil {
		return nil, err
	}
	for _, c := range cols.Rows {
		cd := columnDef{Target: crud.Str(c[0]), Source: crud.Str(c[1]), Transform: crud.Str(c[2]), Fallback: crud.AsBool(c[4])}
		if p := crud.Str(c[3]); p != "" && cd.Transform == "EXTRACT" {
			if cd.Pattern, err = regexp.Compile(p); err != nil {
				return nil, crud.Invalid("Importformat %s: regulärer Ausdruck %q: %v", code, p, err)
			}
		}
		f.Columns = append(f.Columns, cd)
	}
	if len(f.Columns) == 0 {
		return nil, crud.Invalid("Importformat %s hat keine Spalten", code)
	}
	return f, nil
}

// parsedRow ist eine Zeile der Datei, aufbereitet.
type parsedRow struct {
	Line        int
	BookingDate string
	ValueDate   string
	Amount      int64
	Fields      map[string]string
}

// parse liest die Datei nach dem Format; Fehler je Zeile (Zeilennummer der Datei).
func (f *formatDef) parse(text string, decimals int) ([]parsedRow, []string, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = []rune(f.Delimiter)[0]
	r.LazyQuotes, r.FieldsPerRecord = true, -1
	var records [][]string
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, crud.Invalid("Datei: %v", err)
		}
		records = append(records, rec)
	}
	if f.SkipLines > len(records) {
		return nil, nil, crud.Invalid("Datei hat weniger als %d Zeilen", f.SkipLines)
	}
	records = records[f.SkipLines:]
	first := f.SkipLines + 1
	index := map[string]int{}
	if !f.NoHeader {
		if len(records) == 0 {
			return nil, nil, crud.Invalid("Datei ohne Kopfzeile")
		}
		for i, h := range records[0] {
			index[strings.ToLower(strings.TrimSpace(h))] = i
		}
		records, first = records[1:], first+1
	}
	col := func(src string) (int, bool) {
		if n, err := strconv.Atoi(src); err == nil && n >= 1 {
			return n - 1, true
		}
		i, ok := index[strings.ToLower(src)]
		return i, ok
	}
	for _, c := range f.Columns {
		if _, ok := col(c.Source); !ok {
			return nil, nil, crud.Invalid("Spalte %q fehlt in der Datei (Importformat %s)", c.Source, f.Code)
		}
	}
	layout := dateFormats[f.DateFormat]
	var rows []parsedRow
	var errs []string
	for n, rec := range records {
		line := first + n
		if len(rec) == 1 && strings.TrimSpace(rec[0]) == "" {
			continue
		}
		fields := map[string]string{}
		for _, c := range f.Columns {
			i, _ := col(c.Source)
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			v = c.apply(v)
			if v == "" || (c.Fallback && fields[c.Target] != "") {
				continue
			}
			if prev := fields[c.Target]; prev != "" && c.Target != "amount" {
				v = prev + " " + v
			}
			fields[c.Target] = v
		}
		row := parsedRow{Line: line, Fields: fields}
		d, err := time.Parse(layout, strings.TrimSpace(fields["booking_date"]))
		if err != nil {
			errs = append(errs, fmt.Sprintf("Zeile %d: Buchungstag %q passt nicht zum Format %s", line, fields["booking_date"], f.DateFormat))
			continue
		}
		row.BookingDate = d.Format(time.DateOnly)
		if v := strings.TrimSpace(fields["value_date"]); v != "" {
			if d, err := time.Parse(layout, v); err == nil {
				row.ValueDate = d.Format(time.DateOnly)
			}
		}
		amt, err := parseDecimal(fields["amount"], f.DecimalSep, decimals)
		if err != nil {
			errs = append(errs, fmt.Sprintf("Zeile %d: %v", line, err))
			continue
		}
		if dc := strings.ToUpper(strings.TrimSpace(fields["debit_credit"])); dc != "" && len(f.DebitValues) > 0 {
			if amt < 0 {
				amt = -amt
			}
			for _, v := range f.DebitValues {
				if dc == v {
					amt = -amt
				}
			}
		}
		row.Amount = amt
		rows = append(rows, row)
	}
	// Reihenfolge der Zahlungen: Buchungstag aufsteigend, am selben Tag wie in der Datei (bzw. umgekehrt)
	if f.NewestFirst {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	stableSortByDate(rows)
	return rows, errs, nil
}

func (c columnDef) apply(v string) string {
	switch c.Transform {
	case "TRIM":
		return strings.Join(strings.Fields(v), " ")
	case "UPPER":
		return strings.ToUpper(strings.TrimSpace(v))
	case "NEGATE":
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "-") {
			return v[1:]
		}
		if v == "" {
			return v
		}
		return "-" + strings.TrimPrefix(v, "+")
	case "EXTRACT":
		if c.Pattern == nil {
			return ""
		}
		if m := c.Pattern.FindStringSubmatch(v); len(m) > 1 {
			return strings.TrimSpace(m[1])
		}
		return ""
	case "IBAN":
		return findIBAN(v)
	}
	return strings.TrimSpace(v)
}

func stableSortByDate(rows []parsedRow) {
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].BookingDate < rows[j].BookingDate })
}

// findIBAN: erste gültige IBAN im Text (auch in Vierergruppen mit Leerzeichen) –
// ab jedem möglichen Anfang die längste mit richtiger Prüfziffer.
func findIBAN(text string) string {
	up := strings.ToUpper(text)
	for _, loc := range ibanStartRe.FindAllStringIndex(up, -1) {
		var chars []byte
		for i := loc[0]; i < len(up) && len(chars) < 34; i++ {
			c := up[i]
			switch {
			case c >= 'A' && c <= 'Z' || c >= '0' && c <= '9':
				chars = append(chars, c)
			case c == ' ' && i+1 < len(up) && up[i+1] != ' ':
				continue
			default:
				i = len(up)
			}
		}
		for n := len(chars); n >= 15; n-- {
			if iban := string(chars[:n]); ibanValid(iban) && ibanLengthOK(iban) {
				return iban
			}
		}
	}
	return ""
}

// ibanLengths: Länge der IBAN je Land (SEPA-Raum); andere Länder 15–34 Zeichen.
var ibanLengths = map[string]int{"AD": 24, "AT": 20, "BE": 16, "BG": 22, "CH": 21, "CY": 28, "CZ": 24, "DE": 22, "DK": 18, "EE": 20, "ES": 24,
	"FI": 18, "FR": 27, "GB": 22, "GI": 23, "GR": 27, "HR": 21, "HU": 28, "IE": 22, "IS": 26, "IT": 27, "LI": 21, "LT": 20, "LU": 20, "LV": 21,
	"MC": 27, "MT": 31, "NL": 18, "NO": 15, "PL": 28, "PT": 25, "RO": 24, "SE": 24, "SI": 19, "SK": 24, "SM": 27, "VA": 22}

func ibanLengthOK(iban string) bool {
	if n, ok := ibanLengths[iban[:2]]; ok {
		return len(iban) == n
	}
	return len(iban) >= 15 && len(iban) <= 34
}
