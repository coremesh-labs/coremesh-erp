package dms

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Erkennen (Schrittart RECOGNIZE): Die Erkennungsregeln des Katalogs DmsHint
// mit Ziel geben Punkte für Kandidaten eines Ziels (z. B. Geschäftspartner,
// Bezug Vertrag, Dokumentdatum):
//
//	MATCH    Werte eines Objects (Quelle, Suche) im erkannten Text bzw. in einem
//	         Hinweis (compare_hint) – verglichen nach der Aufbereitung (NAME: ohne
//	         Groß-/Kleinschreibung und Umlaute, ALNUM: nur Buchstaben und Ziffern,
//	         PHONE: Telefonnummern, IBAN: gültige IBANs); Kandidat ist das Feld
//	         candidate_field des Treffers, mit partner_field zählen die Punkte auch
//	         für dessen Geschäftspartner. Regeln einer Gruppe zählen je Kandidat
//	         einmal; requires: nur, wenn die genannte Regel für den Kandidaten traf;
//	         points_field (seit 0.27.0): Punkte aus dem Treffer, der beste zählt
//	         (z. B. Stichworte DmsKeyword).
//	LOOKUP   mit Ziel und partner_field: der Partner des Treffers (z. B. Kontoauszug)
//	PATTERN  mit Ziel: der Treffer des Musters (z. B. Dokumentdatum)
//
// Ein Kandidat gilt als erkannt, wenn er mindestens min_score Punkte hat und
// min_gap vor dem nächsten liegt; er wird Vorschlag (bzw. bei „direkt setzen“
// Wert) des Ziels. Kandidaten mit Partner zählen nur, wenn er zum erkannten
// Partner passt. Die Regeln des Schritts (rules, Komma-Liste von Schlüsseln
// oder Gruppen) schränken ein; leer = alle Regeln mit Ziel.

const hintMatch = "MATCH"

// Aufbereitung beim Vergleich.
const (
	normName  = "NAME"
	normAlnum = "ALNUM"
	normPhone = "PHONE"
	normIBAN  = "IBAN"
)

var (
	ibanRe   = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]{4}){3,7}(?:[ ]?[A-Z0-9]{1,3})?\b`)
	phoneRe  = regexp.MustCompile(`[+0(][\d ()/\-]{6,}\d`)
	nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)
)

// foldText: klein, Umlaute ausgeschrieben, nur Buchstaben/Ziffern mit Leerzeichen.
func foldText(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss", "Ä", "ae", "Ö", "oe", "Ü", "ue")
	return " " + strings.TrimSpace(nonAlnum.ReplaceAllString(r.Replace(strings.ToLower(s)), " ")) + " "
}

// compactAlnum: nur Buchstaben und Ziffern, groß (Nummern mit und ohne Trennzeichen gleich).
func compactAlnum(s string) string {
	return strings.ToUpper(strings.ReplaceAll(foldText(s), " ", ""))
}

// phoneDigits: Ziffern einer Telefonnummer, +49 bzw. 0049 als 0.
func phoneDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if strings.HasPrefix(strings.TrimSpace(s), "+") {
		d = strings.TrimPrefix(d, "49")
		return "0" + d
	}
	if strings.HasPrefix(d, "0049") {
		return "0" + strings.TrimPrefix(d, "0049")
	}
	return d
}

// ibanValid: Prüfziffer nach ISO 13616 (mod 97 = 1).
func ibanValid(iban string) bool {
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	rem := 0
	for _, r := range iban[4:] + iban[:4] {
		switch {
		case r >= '0' && r <= '9':
			rem = (rem*10 + int(r-'0')) % 97
		case r >= 'A' && r <= 'Z':
			v := int(r-'A') + 10
			rem = (rem*100 + v) % 97
		default:
			return false
		}
	}
	return rem == 1
}

// refsText: Bezüge für Fragen ({refs}).
func (m *Module) refsText(ctx context.Context, d *docRow) string {
	refs, _ := m.activeRefs(ctx, d.ID)
	var out []string
	for _, r := range refs {
		out = append(out, r.Object+" "+shortKey(r.Key, d.CC))
	}
	if len(out) == 0 {
		return "–"
	}
	return strings.Join(out, ", ")
}

// haystack: aufbereiteter Text, in dem gesucht wird.
type haystack struct {
	folded, compact string
	phones          []string
	ibans           map[string]bool
}

func newHaystack(text string) *haystack {
	h := &haystack{folded: foldText(text), compact: compactAlnum(text), ibans: map[string]bool{}}
	for _, f := range phoneRe.FindAllString(text, -1) {
		if p := phoneDigits(f); len(p) >= 7 {
			h.phones = append(h.phones, p)
		}
	}
	for _, f := range ibanRe.FindAllString(strings.ToUpper(text), -1) {
		if i := strings.ReplaceAll(f, " ", ""); ibanValid(i) {
			h.ibans[i] = true
		}
	}
	return h
}

// contains: Wert v (Aufbereitung norm, Mindestlänge min) kommt vor; both – auch der Text im Wert (Hinweis).
func (h *haystack) contains(norm, v string, min int, both bool) bool {
	switch norm {
	case normIBAN:
		return h.ibans[strings.ToUpper(strings.ReplaceAll(v, " ", ""))]
	case normPhone:
		p := phoneDigits(v)
		if strings.Contains(v, "@") || len(p) < max(min, 7) {
			return false
		}
		for _, t := range h.phones {
			if t == p || strings.HasSuffix(t, p) || strings.HasSuffix(p, t) && len(t) >= 9 {
				return true
			}
		}
		return false
	case normAlnum:
		n := compactAlnum(v)
		return len(n) >= max(min, 1) && (strings.Contains(h.compact, n) || both && len(h.compact) >= max(min, 1) && strings.Contains(n, h.compact))
	}
	n := foldText(v)
	if len(strings.TrimSpace(n)) < max(min, 1) {
		return false
	}
	return strings.Contains(h.folded, n) || both && len(strings.TrimSpace(h.folded)) >= max(min, 1) && strings.Contains(n, h.folded)
}

// ruleDef: Erkennungsregel (Eintrag des Katalogs mit Ziel).
type ruleDef struct {
	hintDef
	Target, Normalize, CompareField, CompareHint, CandidateField, Group, Requires, PointsField string
	Points, MinLength                                                                          int64
}

func (m *Module) ruleDefs(ctx context.Context, only string) []ruleDef {
	res, err := m.db.Query(ctx, `SELECT code, target, normalize, compare_field, compare_hint, candidate_field, rule_group, requires, points, min_length, points_field
		FROM dms__hint WHERE is_active = ? AND target IS NOT NULL AND target <> '' ORDER BY sort_order, code`, true)
	if err != nil {
		return nil
	}
	defs := map[string]hintDef{}
	for _, h := range m.hintDefs(ctx) {
		defs[h.Code] = h
	}
	want := splitList(only)
	var out []ruleDef
	for _, r := range res.Rows {
		s := func(i int) string { return strings.TrimSpace(crud.Str(r[i])) }
		rd := ruleDef{hintDef: defs[s(0)], Target: s(1), Normalize: firstOf(s(2), normName), CompareField: s(3), CompareHint: s(4),
			CandidateField: s(5), Group: s(6), Requires: s(7), Points: toInt(r[8]), MinLength: toInt(r[9]), PointsField: s(10)}
		if rd.Code == "" || len(want) > 0 && !contains(want, rd.Code) && !(rd.Group != "" && contains(want, rd.Group)) {
			continue
		}
		out = append(out, rd)
	}
	return out
}

// candidate: Kandidat eines Ziels.
type candidate struct {
	value, partner string
	score          int64
	why            []string
	rules, groups  map[string]bool
	points         map[string]int64 // Punkte je Regel (Punkte aus der Quelle: der beste Treffer zählt)
}

// runRecognizeStep: Programmschritt Erkennen.
func (m *Module) runRecognizeStep(ctx context.Context, d *docRow, s *stepDef) (string, string, string) {
	text := m.docText(ctx, d.ID)
	if strings.TrimSpace(text) == "" {
		return oFailed, "", "kein erkannter Text – vorher Texterkennung"
	}
	rules := m.ruleDefs(ctx, s.Rules)
	if len(rules) == 0 {
		return oFailed, "", "keine Erkennungsregeln mit Ziel (Einstellungen → Hinweise aus dem Dokument)"
	}
	hay := newHaystack(text)
	hints := m.docHints(ctx, d)
	cands := map[string]map[string]*candidate{} // Ziel → Wert → Kandidat
	hit := func(target, value, partner string, r ruleDef, pts int64) {
		if value == "" {
			return
		}
		if cands[target] == nil {
			cands[target] = map[string]*candidate{}
		}
		c := cands[target][value]
		if c == nil {
			c = &candidate{value: value, partner: partner, rules: map[string]bool{}, groups: map[string]bool{}, points: map[string]int64{}}
			cands[target][value] = c
		}
		if c.rules[r.Code] && pts > c.points[r.Code] {
			c.score += pts - c.points[r.Code]
			c.points[r.Code] = pts
			return
		}
		if c.rules[r.Code] || r.Group != "" && c.groups[r.Group] || r.Requires != "" && !c.rules[r.Requires] {
			return
		}
		c.rules[r.Code] = true
		if r.Group != "" {
			c.groups[r.Group] = true
		}
		c.score += pts
		c.points[r.Code] = pts
		c.why = append(c.why, r.Name)
	}
	lists := map[string][]map[string]any{}
	for _, r := range rules {
		switch r.Kind {
		case hintPattern:
			if v := hints[r.Code]; v != "" {
				if r.Target == "doc_date" {
					if dv, err := parseDate(v, ""); err == nil {
						v = dv
					} else {
						continue
					}
				}
				hit(r.Target, v, "", r, r.Points)
			}
		case hintLookup:
			if p := hints[r.Code+"_partner"]; p != "" {
				hit(r.Target, p, "", r, r.Points)
			}
		case hintMatch:
			hs := hay
			if r.CompareHint != "" {
				v := strings.TrimSpace(hints[r.CompareHint])
				if v == "" {
					continue
				}
				hs = newHaystack(v)
			}
			query := m.matchQuery(ctx, d, r.Match)
			key := r.Source + "?" + fmt.Sprint(query)
			items, ok := lists[key]
			if !ok {
				items = m.listItems(ctx, r.Source, query)
				lists[key] = items
			}
			for _, it := range items {
				if hs.contains(r.Normalize, crud.Str(it[r.CompareField]), int(r.MinLength), r.CompareHint != "") {
					partner := ""
					if r.PartnerField != "" {
						partner = crud.Str(it[r.PartnerField])
					}
					pts := r.Points
					if r.PointsField != "" && crud.Str(it[r.PointsField]) != "" {
						pts = toInt(it[r.PointsField])
					}
					hit(r.Target, crud.Str(it[firstOf(r.CandidateField, "id")]), partner, r, pts)
					if partner != "" && r.Target != "partner_id" {
						hit("partner_id", partner, "", r, pts)
					}
				}
			}
		}
	}
	minScore, minGap := s.MinScore, s.MinGap
	if minScore <= 0 {
		minScore = 3
	}
	// Partner zuerst – Kandidaten anderer Ziele mit Partner zählen nur für ihn.
	targets := sortedKeys(func() map[string]bool {
		out := map[string]bool{}
		for t := range cands {
			out[t] = true
		}
		return out
	}())
	sort.SliceStable(targets, func(i, j int) bool { return targets[i] == "partner_id" && targets[j] != "partner_id" })
	partner := d.Partner
	var notes []string
	found := 0
	for _, t := range targets {
		var ranked []*candidate
		for _, c := range cands[t] {
			if c.partner != "" && partner != "" && c.partner != partner {
				continue
			}
			ranked = append(ranked, c)
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].score != ranked[j].score {
				return ranked[i].score > ranked[j].score
			}
			return ranked[i].value < ranked[j].value
		})
		if len(ranked) == 0 || ranked[0].score < minScore || len(ranked) > 1 && ranked[0].score-ranked[1].score < minGap {
			if len(ranked) > 0 {
				notes = append(notes, fmt.Sprintf("%s: nicht eindeutig (%d Kandidaten)", m.targetLabel(t), len(ranked)))
			}
			continue
		}
		w := ranked[0]
		label := shortKey(w.value, d.CC)
		if t == "partner_id" {
			label = m.partnerName(ctx, w.value) + " (" + w.value + ")"
			if partner == "" {
				partner = w.value
			}
		}
		if _, err := m.applyResult(ctx, d, s, t, &proposalRec{Value: w.value, Label: label, Source: "Schritt " + s.Step, Score: w.score, Why: w.why}); err != nil {
			notes = append(notes, m.targetLabel(t)+": "+err.Error())
			continue
		}
		found++
		notes = append(notes, fmt.Sprintf("%s %s (%s; %d Punkte)", m.targetLabel(t), label, strings.Join(w.why, ", "), w.score))
	}
	if found == 0 {
		return oFailed, "", firstOf(strings.Join(notes, "; "), "nichts eindeutig erkannt")
	}
	return oSuccess, "", strings.Join(notes, "; ")
}

// matchQuery: Suche „Feld=Wert; …“ mit Platzhaltern (leere Werte entfallen).
func (m *Module) matchQuery(ctx context.Context, d *docRow, spec string) map[string]any {
	q := map[string]any{}
	for _, p := range strings.Split(spec, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || strings.TrimSpace(k) == "" {
			continue
		}
		if v = strings.TrimSpace(m.expand(ctx, d, strings.TrimSpace(v))); v != "" {
			q[strings.TrimSpace(k)] = v
		}
	}
	return q
}
