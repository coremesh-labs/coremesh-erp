package opcost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// runSnapshot: Stand eines Laufs vor dem Neurechnen – Zeitpunkt, Kosten je Topf,
// Kosten und Vorauszahlungen je Mieter (Cent). Rechnen liest Sachkonten,
// Rechnungen und Eingaben jedes Mal neu; der Vergleich zeigt, was sich geändert hat.
type runSnapshot struct {
	computedAt string
	pools      map[string]int64
	tenants    map[string][2]int64
}

func (m *Module) snapshot(ctx context.Context, k runKey) (runSnapshot, error) {
	s := runSnapshot{pools: map[string]int64{}, tenants: map[string][2]int64{}}
	s.computedAt = m.text(ctx, `SELECT computed_at FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From)
	if p := m.text(ctx, `SELECT pool_totals FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`, k.CC, k.Definition, k.From); p != "" {
		if err := json.Unmarshal([]byte(p), &s.pools); err != nil {
			return s, err
		}
	}
	rows, err := m.query(ctx, `SELECT contract_id, costs, advances FROM opcost__tenant WHERE company_code = ? AND definition = ? AND period_from = ?`,
		[]string{"contract_id", "costs", "advances"}, k.CC, k.Definition, k.From)
	if err != nil {
		return s, err
	}
	for _, r := range rows {
		s.tenants[fmt.Sprint(r["contract_id"])] = [2]int64{toInt(r["costs"]), toInt(r["advances"])}
	}
	return s, nil
}

// changeText: Hinweis für das Ergebnis des Neurechnens – Zeitpunkt und
// Änderungen gegenüber der letzten Rechnung (Kosten je Konto, je Mieter).
func (m *Module) changeText(ctx context.Context, k runKey, before, after runSnapshot) string {
	now := "Neu gerechnet am " + time.Now().Local().Format("02.01.2006 15:04") + " (Sachkonten, Rechnungen und Eingaben aktuell gelesen)."
	if before.computedAt == "" {
		return now
	}
	names := map[string]string{}
	if rows, err := m.query(ctx, `SELECT code, name FROM opcost__account WHERE company_code = ? AND definition = ?`, []string{"code", "name"}, k.CC, k.Definition); err == nil {
		for _, r := range rows {
			names[fmt.Sprint(r["code"])] = fmt.Sprint(r["name"])
		}
	}
	label := func(code string) string {
		if n := names[code]; n != "" && n != code {
			return n + " (" + code + ")"
		}
		return code
	}
	var changes []string
	for _, code := range unionKeys(before.pools, after.pools) {
		if a, b := before.pools[code], after.pools[code]; a != b {
			changes = append(changes, label(code)+" "+centsText(a)+" → "+centsText(b))
		}
	}
	contracts := map[string]bool{}
	for c := range before.tenants {
		contracts[c] = true
	}
	for c := range after.tenants {
		contracts[c] = true
	}
	var cs []string
	for c := range contracts {
		cs = append(cs, c)
	}
	sort.Strings(cs)
	for _, c := range cs {
		a, b := before.tenants[c], after.tenants[c]
		if a[0] != b[0] {
			changes = append(changes, "Mieter "+c+" Kosten "+centsText(a[0])+" → "+centsText(b[0]))
		}
		if a[1] != b[1] {
			changes = append(changes, "Mieter "+c+" Vorauszahlungen "+centsText(a[1])+" → "+centsText(b[1]))
		}
	}
	last := before.computedAt
	if t, err := time.Parse(time.RFC3339, before.computedAt); err == nil {
		last = t.Local().Format("02.01.2006 15:04")
	}
	if len(changes) == 0 {
		return now + " Keine Änderung gegenüber der Rechnung vom " + last + "."
	}
	return now + " Geändert gegenüber der Rechnung vom " + last + ": " + strings.Join(changes, " · ") + "."
}

func unionKeys(a, b map[string]int64) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range []map[string]int64{a, b} {
		for k := range m {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}
