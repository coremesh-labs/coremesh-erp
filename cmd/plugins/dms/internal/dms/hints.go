package dms

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Hinweise aus dem Dokument (seit 0.10.0, Katalog DmsHint seit 0.11.0): Was der Text
// hergibt, auch wenn der Absender nicht lesbar ist (Briefkopf als Bild oder in
// Zierschrift). Jeder Eintrag des Katalogs ergibt den Platzhalter {hint.<Schlüssel>}:
//
//	PATTERN  erster Treffer des Musters im erkannten Text (Zeichen entfernen, Prüfung
//	         z. B. IBAN, Werte aus Object.Feld ausschließen – z. B. eigene Konten)
//	LOOKUP   Wert aus einem anderen Object (Suche Feld=Wert mit Platzhaltern, neuester
//	         zuerst), z. B. der Name laut Kontoauszug; mit Feld für den Geschäftspartner
//	         auch {hint.<Schlüssel>_partner} – zählt beim Erkennen (wfrecognize.go)
//	MATCH    Erkennungsregel: Werte eines Objects im Text (nur Schritt Erkennen)
//
// Die Reihenfolge zählt: Ein Hinweis kann frühere verwenden ({hint.iban} in der Suche).
// Verwendung: Kontext und Vorgaben der Schritte; Zuordnungen „Wert aus dem Dokument“
// der Maske („partner.name1“ = {hint.payee}) füllen leere Felder.
// Zeilen des Kontexts mit leerem Hinweis entfallen.

const (
	hintPattern = "PATTERN"
	hintLookup  = "LOOKUP"
)

var (
	hintKinds = []metamodel.Option{
		{Value: hintPattern, Label: "Muster im erkannten Text"},
		{Value: hintLookup, Label: "Wert aus einem anderen Object (z. B. Kontoauszug)"},
		{Value: hintMatch, Label: "Erkennungsregel: Werte eines Objects im Text (Punkte für ein Ziel)"},
	}
	normalizeOptions = []metamodel.Option{
		{Value: normName, Label: "Name (ohne Groß-/Kleinschreibung, Umlaute, Satzzeichen)"},
		{Value: normAlnum, Label: "Nummer (nur Buchstaben und Ziffern)"},
		{Value: normPhone, Label: "Telefonnummer"},
		{Value: normIBAN, Label: "IBAN"},
	}
	// hintChecks: Prüfungen eines Treffers (Auswahl im Katalog).
	hintChecks = []metamodel.Option{{Value: "IBAN", Label: "IBAN (Prüfziffer)"}}
	hintCheck  = map[string]func(string) bool{"IBAN": func(v string) bool { return ibanValid(strings.ToUpper(v)) }}
	hintCodeRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,29}$`)
	hintRe     = regexp.MustCompile(`\{hint\.[A-Za-z_]+\}`)
)

// hintDef ist ein Eintrag des Katalogs.
type hintDef struct {
	Code, Name, Kind, Pattern, Check, Strip, Exclude string
	Source, Match, ValueField, PartnerField, Sort    string
}

func (m *Module) hintDefs(ctx context.Context) []hintDef {
	res, err := m.db.Query(ctx, `SELECT code, name, kind, pattern, check_rule, strip, exclude, source, match, value_field, partner_field, sort_field
		FROM dms__hint WHERE is_active = ? ORDER BY sort_order, code`, true)
	if err != nil {
		return nil
	}
	out := make([]hintDef, 0, len(res.Rows))
	for _, r := range res.Rows {
		s := func(i int) string { return crud.Str(r[i]) }
		out = append(out, hintDef{Code: s(0), Name: s(1), Kind: s(2), Pattern: s(3), Check: s(4), Strip: s(5), Exclude: s(6),
			Source: s(7), Match: s(8), ValueField: s(9), PartnerField: s(10), Sort: s(11)})
	}
	return out
}

// docHints: Hinweise zum Dokument (leere Werte entfallen).
func (m *Module) docHints(ctx context.Context, d *docRow) map[string]string {
	out := map[string]string{}
	text := m.docText(ctx, d.ID)
	if strings.TrimSpace(text) == "" {
		return out
	}
	for _, h := range m.hintDefs(ctx) {
		switch h.Kind {
		case hintPattern:
			if v := m.patternHint(ctx, h, text); v != "" {
				out[h.Code] = v
			}
		case hintLookup:
			v, p := m.lookupHint(ctx, d, h, out)
			if v != "" {
				out[h.Code] = v
			}
			if p != "" {
				out[h.Code+"_partner"] = p
			}
		}
	}
	return out
}

func stripChars(v, chars string) string {
	for _, c := range chars {
		v = strings.ReplaceAll(v, string(c), "")
	}
	return strings.TrimSpace(v)
}

// patternHint: erster Treffer, der die Prüfung besteht und nicht ausgeschlossen ist.
func (m *Module) patternHint(ctx context.Context, h hintDef, text string) string {
	re, err := regexp.Compile(h.Pattern)
	if err != nil {
		return ""
	}
	var excluded map[string]bool
	for _, f := range re.FindAllString(text, -1) {
		v := stripChars(f, h.Strip)
		if check := hintCheck[h.Check]; check != nil && !check(v) {
			continue
		}
		if h.Exclude != "" {
			if excluded == nil {
				excluded = m.excludedValues(ctx, h)
			}
			if excluded[strings.ToUpper(v)] {
				continue
			}
		}
		return v
	}
	return ""
}

// excludedValues: Werte aus „Object.Feld“ (z. B. die IBANs der eigenen Konten).
func (m *Module) excludedValues(ctx context.Context, h hintDef) map[string]bool {
	out := map[string]bool{}
	object, field, ok := strings.Cut(strings.TrimSpace(h.Exclude), ".")
	if !ok {
		return out
	}
	for _, it := range m.listItems(ctx, object, map[string]any{}) {
		out[strings.ToUpper(stripChars(crud.Str(it[field]), h.Strip))] = true
	}
	return out
}

// lookupHint: Wert (und Partner) des neuesten passenden Datensatzes der Quelle.
func (m *Module) lookupHint(ctx context.Context, d *docRow, h hintDef, hints map[string]string) (string, string) {
	query := map[string]any{}
	for _, p := range strings.Split(h.Match, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		v = hintRe.ReplaceAllStringFunc(strings.TrimSpace(v), func(ph string) string {
			return hints[strings.TrimSuffix(strings.TrimPrefix(ph, "{hint."), "}")]
		})
		if v = m.expand(ctx, d, v); strings.TrimSpace(v) == "" {
			return "", "" // Suche ohne Wert: kein Hinweis
		}
		query[strings.TrimSpace(k)] = v
	}
	// Treffer genau prüfen (Leerzeichen und Groß-/Kleinschreibung egal), falls die Quelle nicht
	// filtert; Felder, die die Quelle nicht liefert, zählen nicht.
	var hits []map[string]any
	for _, it := range m.listItems(ctx, h.Source, query) {
		ok := true
		for k, v := range query {
			if x, has := it[k]; has && !strings.EqualFold(stripChars(crud.Str(x), " "), stripChars(crud.Str(v), " ")) {
				ok = false
				break
			}
		}
		if ok {
			hits = append(hits, it)
		}
	}
	if h.Sort != "" {
		sort.SliceStable(hits, func(i, j int) bool { return crud.Str(hits[i][h.Sort]) > crud.Str(hits[j][h.Sort]) })
	}
	value, partner := "", ""
	for _, it := range hits {
		if value == "" {
			value = strings.TrimSpace(crud.Str(it[h.ValueField]))
		}
		if partner == "" && h.PartnerField != "" {
			partner = crud.Str(it[h.PartnerField])
		}
	}
	return value, partner
}

// listItems: Liste eines Objects (Fehler: leer – fehlt das Modul, gibt es keine Hinweise).
func (m *Module) listItems(ctx context.Context, object string, query map[string]any) []map[string]any {
	resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": query})
	if err != nil {
		return nil
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return nil
	}
	return l.Items
}

// expandLines: Zeilen (durch | getrennt) mit Platzhaltern; Zeilen mit leerem Hinweis entfallen.
func (m *Module) expandLines(ctx context.Context, d *docRow, t string) string {
	var hints map[string]string
	var out []string
	for _, line := range strings.Split(t, "|") {
		if hintRe.MatchString(line) {
			if hints == nil {
				hints = m.docHints(ctx, d)
			}
			missing := false
			for _, ph := range hintRe.FindAllString(line, -1) {
				if hints[strings.TrimSuffix(strings.TrimPrefix(ph, "{hint."), "}")] == "" {
					missing = true
				}
			}
			if missing {
				continue
			}
		}
		out = append(out, m.expand(ctx, d, line))
	}
	return strings.Join(out, "\n")
}

// applyHints: Zuordnungen „Wert aus dem Dokument“ (banks.iban = {hint.iban}) in leere Felder
// des Ergebnisses; die Form (Liste oder Einzelobjekt) kommt aus der leeren Struktur der KI-Anfrage.
func (m *Module) applyHints(ctx context.Context, d *docRow, s *stepDef, request string, result map[string]any) (map[string]any, []string) {
	maps := s.mappings(mapHint, m.categoryOf(ctx, d))
	if len(maps) == 0 {
		return result, nil
	}
	var empty map[string]any
	if resp, err := m.services.Call(ctx, "Ai", "schema", map[string]any{"request": request, "company_code": d.CC}); err == nil {
		var sc struct {
			Empty map[string]any `json:"empty"`
		}
		if sdk.Decode(resp.Payload, &sc) == nil {
			empty = sc.Empty
		}
	}
	if result == nil {
		result = map[string]any{}
	}
	var used []string
	for _, p := range maps {
		v := strings.TrimSpace(m.expand(ctx, d, strings.TrimSpace(p.Value)))
		if p.Field == "" || v == "" || hintRe.MatchString(v) {
			continue
		}
		if fillPath(result, empty, strings.Split(p.Field, "."), v) {
			used = append(used, p.Field)
		}
	}
	return result, used
}

// fillPath setzt den Wert, wenn das Feld leer ist; Listen: die erste Zeile (fehlt sie, neu).
func fillPath(rec, shape map[string]any, path []string, v string) bool {
	if len(path) == 0 {
		return false
	}
	key := path[0]
	if len(path) == 1 {
		if x, ok := rec[key]; ok && x != nil && strings.TrimSpace(crud.Str(x)) != "" {
			return false
		}
		rec[key] = v
		return true
	}
	var subShape map[string]any
	_, isList := shape[key].([]any)
	if l, ok := shape[key].([]any); ok && len(l) > 0 {
		subShape, _ = l[0].(map[string]any)
	} else {
		subShape, _ = shape[key].(map[string]any)
	}
	switch cur := rec[key].(type) {
	case []any:
		if len(cur) == 0 {
			cur = []any{map[string]any{}}
			rec[key] = cur
		}
		row, ok := cur[0].(map[string]any)
		if !ok {
			return false
		}
		return fillPath(row, subShape, path[1:], v)
	case map[string]any:
		return fillPath(cur, subShape, path[1:], v)
	case nil:
		row := map[string]any{}
		if !fillPath(row, subShape, path[1:], v) {
			return false
		}
		if isList {
			rec[key] = []any{row}
		} else {
			rec[key] = row
		}
		return true
	}
	return false
}
