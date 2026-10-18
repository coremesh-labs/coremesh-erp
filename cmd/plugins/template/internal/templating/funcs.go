package templating

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/sprig/v3"
)

// Hilfsfunktionen der Templates: Sprig v3 (Strings, Listen, Maps, Datum,
// Mathematik, default, …; ohne Zugriff auf Umgebung und Netz) und eigene:
//
//	tex, texLines            LaTeX maskieren (texLines: Zeilen → \\, Leerzeile → Absatz)
//	xmlEscape                XML maskieren
//	csvField, csvRow         CSV-Feld (Semikolon, Anführungszeichen) bzw. Zeile aus einer Liste
//	fmtNumber v [Stellen]    Zahl in der Sprache der Vorlage (de: 1.234,56)
//	fmtMoney v [Währung]     Betrag mit zwei Stellen, z. B. 1.234,56 EUR
//	fromMinor v Stellen      kleinste Einheit → Betrag (125050, 2 → 1250.5)
//	sumBy Liste Feld         Summe eines Felds über eine Liste von Maps
//	rawNumber v              Zahl ohne Exponent mit Punkt (z. B. für Excel-XML: 1500000.5)
//	fmtDate, fmtDateLong     Datum (JJJJ-MM-TT) in der Sprache: 11.10.2026 bzw. 11. Oktober 2026
//	today                    heutiges Datum (JJJJ-MM-TT)
//	letters                  Briefe: Liste letters der Daten, ohne sie die Daten als ein Brief
//	image Name               Datei des Bundles mit Binärdateien oder der Daten (files), Fehler, wenn sie fehlt
//	job, lang                Auftragsnummer, Sprache der Vorlage
func funcMap(lang, job string, today time.Time, binaries map[string]bool) map[string]any {
	f := map[string]any{
		"tex":       texEscape,
		"texLines":  texLines,
		"xmlEscape": xmlEscape,
		"csvField":  csvField,
		"csvRow":    csvRow,
		"fmtNumber": func(v any, decimals ...int) (string, error) {
			n, err := number(v)
			if err != nil {
				return "", err
			}
			d := 2
			if len(decimals) > 0 {
				d = decimals[0]
			}
			return formatNumber(lang, n, d), nil
		},
		"fmtMoney": func(v any, currency ...string) (string, error) {
			n, err := number(v)
			if err != nil {
				return "", err
			}
			s := formatNumber(lang, n, 2)
			if len(currency) > 0 && currency[0] != "" {
				s += " " + currency[0]
			}
			return s, nil
		},
		"fromMinor": func(v any, decimals int) (float64, error) {
			n, err := number(v)
			return n / math.Pow10(decimals), err
		},
		"sumBy": sumBy,
		"rawNumber": func(v any) (string, error) {
			n, err := number(v)
			return strconv.FormatFloat(n, 'f', -1, 64), err
		},
		"fmtDate":     func(v any) (string, error) { return formatDate(lang, v, false) },
		"fmtDateLong": func(v any) (string, error) { return formatDate(lang, v, true) },
		"today":       func() string { return today.Format(time.DateOnly) },
		"letters":     letters,
		"image": func(name string) (string, error) {
			if !binaries[name] {
				return "", fmt.Errorf("Datei %q fehlt im Bundle mit Binärdateien", name)
			}
			return name, nil
		},
		"job":  func() string { return job },
		"lang": func() string { return lang },
	}
	return f
}

// sprigFuncs: Sprig ohne Funktionen, die Umgebung oder Netz des Servers erreichen.
func sprigFuncs(html bool) map[string]any {
	var f map[string]any
	if html {
		f = map[string]any(sprig.HtmlFuncMap())
	} else {
		f = map[string]any(sprig.TxtFuncMap())
	}
	for _, k := range []string{"env", "expandenv", "getHostByName"} {
		delete(f, k)
	}
	return f
}

func allFuncs(html bool, lang, job string, today time.Time, binaries map[string]bool) map[string]any {
	f := sprigFuncs(html)
	for k, v := range funcMap(lang, job, today, binaries) {
		f[k] = v
	}
	return f
}

var texReplacer = strings.NewReplacer(
	`\`, `\textbackslash{}`, `{`, `\{`, `}`, `\}`, `$`, `\$`, `&`, `\&`, `#`, `\#`, `_`, `\_`, `%`, `\%`,
	`~`, `\textasciitilde{}`, `^`, `\textasciicircum{}`, "\r", "")

func texEscape(v any) string { return texReplacer.Replace(str(v)) }

// texLines: maskiert; einfache Zeilenumbrüche → \\, Leerzeilen → Absatz.
func texLines(v any) string {
	s := strings.ReplaceAll(str(v), "\r\n", "\n")
	paras := strings.Split(strings.TrimSpace(s), "\n\n")
	for i, p := range paras {
		lines := strings.Split(strings.TrimSpace(p), "\n")
		for j, l := range lines {
			lines[j] = texEscape(strings.TrimSpace(l))
		}
		paras[i] = strings.Join(lines, "\\\\\n")
	}
	return strings.Join(paras, "\n\n")
}

var xmlReplacer = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

func xmlEscape(v any) string { return xmlReplacer.Replace(str(v)) }

func csvField(v any) string {
	s := str(v)
	if strings.ContainsAny(s, ";\"\n\r") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

func csvRow(v ...any) string {
	var items []any
	if len(v) == 1 {
		if l, ok := v[0].([]any); ok {
			items = l
		} else {
			items = v
		}
	} else {
		items = v
	}
	out := make([]string, len(items))
	for i, x := range items {
		out[i] = csvField(x)
	}
	return strings.Join(out, ";")
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

// number: Zahl aus JSON-Werten und Texten ("1234.5", "1.234,5" mit Komma als Dezimaltrenner).
func number(v any) (float64, error) {
	switch x := v.(type) {
	case nil:
		return 0, nil
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case json.Number:
		return x.Float64()
	case string:
		s := strings.TrimSpace(strings.ReplaceAll(x, " ", ""))
		if s == "" {
			return 0, nil
		}
		if strings.Contains(s, ",") {
			s = strings.ReplaceAll(strings.ReplaceAll(s, ".", ""), ",", ".")
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("keine Zahl: %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("keine Zahl: %v", v)
}

// formatNumber: Tausender- und Dezimaltrenner der Sprache (de: 1.234,56; sonst 1,234.56).
func formatNumber(lang string, n float64, decimals int) string {
	if decimals < 0 {
		decimals = 0
	}
	s := strconv.FormatFloat(math.Abs(n), 'f', decimals, 64)
	whole, frac, _ := strings.Cut(s, ".")
	thousand, dec := ",", "."
	if lang == "de" {
		thousand, dec = ".", ","
	}
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteString(thousand)
		}
		b.WriteRune(r)
	}
	out := b.String()
	if frac != "" {
		out += dec + frac
	}
	if n < 0 && strings.Trim(out, "0.,") != "" {
		out = "-" + out
	}
	return out
}

func sumBy(list any, field string) (float64, error) {
	items, ok := list.([]any)
	if !ok {
		return 0, fmt.Errorf("sumBy: Liste erwartet")
	}
	var sum float64
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		n, err := number(m[field])
		if err != nil {
			return 0, err
		}
		sum += n
	}
	return math.Round(sum*1e6) / 1e6, nil
}

var monthsDE = []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

func formatDate(lang string, v any, long bool) (string, error) {
	var t time.Time
	switch x := v.(type) {
	case time.Time:
		t = x
	default:
		s := strings.TrimSpace(str(v))
		if s == "" {
			return "", nil
		}
		var err error
		if t, err = time.Parse(time.DateOnly, s[:min(10, len(s))]); err != nil {
			return "", fmt.Errorf("Datum %q: JJJJ-MM-TT erwartet", s)
		}
	}
	switch {
	case lang == "de" && long:
		return fmt.Sprintf("%d. %s %d", t.Day(), monthsDE[t.Month()-1], t.Year()), nil
	case lang == "de":
		return t.Format("02.01.2006"), nil
	case lang == "zh-CN":
		return fmt.Sprintf("%d年%d月%d日", t.Year(), int(t.Month()), t.Day()), nil
	case long:
		return t.Format("January 2, 2006"), nil
	}
	return t.Format("2006-01-02"), nil
}

// letters: Liste letters der Daten; ohne sie sind die Daten ein Brief. Jeder
// Brief erbt die übrigen Angaben der Daten (z. B. sender, date, closing), eigene
// Angaben des Briefs gehen vor.
func letters(data any) []any {
	m, ok := data.(map[string]any)
	if !ok {
		return []any{data}
	}
	l, ok := m["letters"].([]any)
	if !ok {
		return []any{data}
	}
	out := make([]any, len(l))
	for i, x := range l {
		lm, ok := x.(map[string]any)
		if !ok {
			out[i] = x
			continue
		}
		merged := map[string]any{}
		for k, v := range m {
			if k != "letters" {
				merged[k] = v
			}
		}
		for k, v := range lm {
			merged[k] = v
		}
		out[i] = merged
	}
	return out
}
