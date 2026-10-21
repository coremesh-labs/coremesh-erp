package dms

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Duplikat suchen (Schrittart DUPLICATE, seit 0.14.0): Ein zweiter Scan desselben
// Papiers hat einen anderen Inhalts-Hash, und die Texterkennung liest ihn nie ganz
// gleich – die Wörter taugen deshalb nicht zum Vergleich (Monatsrechnungen eines
// Absenders lesen sich fast gleich). Verglichen werden die Zahlen im erkannten Text
// (Belegnummer, Beträge, Daten, IBAN, Kundennummer): Anteil gemeinsamer Zahlen an
// allen Zahlen beider Dokumente (Jaccard). Kandidaten sind die Dokumente des
// Buchungskreises im Eingang oder abgelegt; das ähnlichste ab der Mindestübereinstimmung
// (min_score in Prozent, leer = 90) kommt in das Ziel des Schritts (Variable,
// z. B. {var.duplicate} und {var.duplicate_label}). Ergebnis Ja = Duplikat gefunden.

const (
	dupMinPercent = 90 // Vorgabe der Mindestübereinstimmung
	dupMinDigits  = 3  // Zahlen mit weniger Ziffern (Seite 1, 19 %) zählen nicht
	dupMinNumbers = 5  // Dokumente mit weniger Zahlen sind nicht vergleichbar
)

var dupNumberRe = regexp.MustCompile(`\d[\d.,]*\d`)

// numberPrint: Zahlen des Texts ohne Trennzeichen (31.01.2026 → 31012026, 1.234,56 → 123456).
func numberPrint(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range dupNumberRe.FindAllString(text, -1) {
		n := strings.NewReplacer(".", "", ",", "").Replace(f)
		if len(n) >= dupMinDigits {
			out[n] = true
		}
	}
	return out
}

// jaccard: gemeinsame an allen Zahlen in Prozent.
func jaccard(a, b map[string]bool) int {
	common := 0
	for k := range a {
		if b[k] {
			common++
		}
	}
	all := len(a) + len(b) - common
	if all == 0 {
		return 0
	}
	return common * 100 / all
}

// runDuplicateStep: Programmschritt Duplikat suchen.
func (m *Module) runDuplicateStep(ctx context.Context, d *docRow, s *stepDef) (string, string, string) {
	mine := numberPrint(m.docText(ctx, d.ID))
	if len(mine) < dupMinNumbers {
		return oNo, "", fmt.Sprintf("zu wenige Zahlen im erkannten Text (%d) – kein Vergleich", len(mine))
	}
	minPct := int(s.MinScore)
	if minPct <= 0 {
		minPct = dupMinPercent
	}
	res, err := m.db.Query(ctx, `SELECT d.id, COALESCE(d.name, d.file_name, d.id), t.text FROM dms__document d
		JOIN dms__document_text t ON t.document_id = d.id
		WHERE d.company_code = ? AND d.id <> ? AND d.status IN (?, ?)`, d.CC, d.ID, stInbox, stFiled)
	if err != nil {
		return oFailed, "", err.Error()
	}
	bestID, bestName, best := "", "", 0
	for _, r := range res.Rows {
		other := numberPrint(fmt.Sprint(r[2]))
		if len(other) < dupMinNumbers {
			continue
		}
		if p := jaccard(mine, other); p > best {
			bestID, bestName, best = fmt.Sprint(r[0]), fmt.Sprint(r[1]), p
		}
	}
	if best < minPct {
		if bestID == "" {
			return oNo, "", "kein vergleichbares Dokument"
		}
		return oNo, "", fmt.Sprintf("ähnlichstes Dokument %s: %d %% gleiche Zahlen (Duplikat ab %d %%)", bestName, best, minPct)
	}
	label := fmt.Sprintf("%s (%d %% gleiche Zahlen)", bestName, best)
	if key := s.targetKey(); key != "" {
		if _, err := m.applyResult(ctx, d, s, key, &proposalRec{Value: bestID, Label: label, Source: "Schritt " + s.Step, Score: int64(best)}); err != nil {
			return oFailed, "", err.Error()
		}
	}
	return oYes, bestID, "Duplikat von " + label
}
