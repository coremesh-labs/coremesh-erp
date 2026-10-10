package dms

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Dokumentname: Muster je Buchungskreis (DmsCompany.name_pattern) mit Platzhaltern
//
//	{account}   Nummer der Ablage, die ersten Ebenen ohne Punkt (name_account_levels, Standard 2:
//	            5.2.0 → 52.0) – Pflicht: der Name beschreibt so den Ort im Archiv
//	{doctype}   Kürzel der Dokumentart (leer = Schlüssel)
//	{partner}   Kürzel des Geschäftspartners (leer = Kurzname)
//	{company}   Kürzel des Buchungskreises (leer = Buchungskreis)
//	{object}    Schlüssel des ersten Bezugs ohne Buchungskreis, z. B. ROGoer14 oder GS-2026-0009
//	{date}      Dokumentdatum JJJJMMTT, {year} Jahr
//	{doctype}   Dokumentart (Schlüssel), {doctype_name} Bezeichnung
//	{partner}   Geschäftspartner (Suchbegriff bzw. Name)
//	{title}     Titel
//	{category}  Dokumentkategorie (Schlüssel)
//	{property}  Objekt des Dokuments (Gebäude, Wirtschaftseinheit), z. B. ROGoer14
//
// Leere Platzhalter fallen samt doppeltem Trenner weg. Die Dateiendung kommt
// aus der eingelesenen Datei.

var placeholderRe = regexp.MustCompile(`\{([a-z_]+)\}`)

var placeholders = map[string]bool{"account": true, "company": true, "object": true, "date": true, "year": true,
	"doctype": true, "doctype_name": true, "partner": true, "title": true, "category": true, "property": true}

func checkPattern(p string) error {
	if !strings.Contains(p, "{account}") {
		return crud.Invalid("Muster %q: {account} fehlt – der Dokumentname muss die Ablage enthalten", p)
	}
	for _, g := range placeholderRe.FindAllStringSubmatch(p, -1) {
		if !placeholders[g[1]] {
			return crud.Invalid("Muster %q: Platzhalter {%s} gibt es nicht", p, g[1])
		}
	}
	if strings.ContainsAny(placeholderRe.ReplaceAllString(p, ""), `/\`) {
		return crud.Invalid("Muster %q: keine Pfadtrenner", p)
	}
	return nil
}

// nameParts sind die Werte der Platzhalter.
type nameParts map[string]string

// buildName setzt den Dokumentnamen aus dem Muster zusammen (ohne Endung).
func buildName(pattern string, parts nameParts) string {
	s := placeholderRe.ReplaceAllStringFunc(pattern, func(ph string) string {
		return namePart(parts[ph[1:len(ph)-1]])
	})
	// Leere Platzhalter: doppelte und randständige Trenner entfernen.
	for _, sep := range []string{":", "_", "-", " "} {
		for strings.Contains(s, sep+sep) {
			s = strings.ReplaceAll(s, sep+sep, sep)
		}
	}
	for _, pair := range [][2]string{{":_", ":"}, {"_:", ":"}} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}
	return strings.Trim(s, ":_- ")
}

// namePart: Wert für den Dateinamen – Leerzeichen und Trenner werden zu „-“.
func namePart(v string) string {
	v = strings.TrimSpace(v)
	var b strings.Builder
	for _, r := range v {
		switch {
		case r == ' ' || r == ':' || r == '_' || r == '/' || r == '\\' || r == '|' || r < 0x20 || r == 0x7f:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	return strings.Trim(out, "-.")
}

// extOf: Dateiendung in Kleinbuchstaben (".pdf"), höchstens 10 Zeichen.
func extOf(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if len(ext) > 10 || strings.ContainsAny(ext, ` :/\`) {
		return ""
	}
	return ext
}

// shortKey: fachlicher Schlüssel ohne Buchungskreis ("2000|ROGoer14" → "ROGoer14").
func shortKey(key, cc string) string {
	if rest, ok := strings.CutPrefix(key, cc+"|"); ok {
		return strings.ReplaceAll(rest, "|", "-")
	}
	return strings.ReplaceAll(key, "|", "-")
}

// compactNumber: die ersten levels Ebenen ohne Punkt (5.2.0 → 52.0 bei 2).
func compactNumber(number string, levels int) string {
	parts := strings.Split(number, ".")
	if levels < 2 || len(parts) < 2 {
		return number
	}
	if levels > len(parts) {
		levels = len(parts)
	}
	return strings.Join(append([]string{strings.Join(parts[:levels], "")}, parts[levels:]...), ".")
}
