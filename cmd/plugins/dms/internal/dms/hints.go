package dms

import (
	"context"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Hinweise aus dem Dokument (seit 0.10.0): Was der Text sicher hergibt, auch wenn
// der Absender nicht lesbar ist (Briefkopf als Bild oder in Zierschrift):
//
//	{hint.iban}       erste gültige IBAN im Text, die keine eigene Bankverbindung ist
//	{hint.payee}      Gegenseite der Bankumsätze mit dieser IBAN (Name laut Kontoauszug)
//	{hint.tax_number} Steuernummer (z. B. 123/456/78901)
//	{hint.vat_id}     USt-IdNr. (z. B. DE123456789)
//
// Verwendung: Platzhalter in context und defaults; Parameter hints des KI-Schritts
// („partner.name1={hint.payee}; banks.iban={hint.iban}“) füllt leere Felder der Maske.
// Zeilen von context mit leerem Hinweis entfallen.

var (
	taxNumberRe = regexp.MustCompile(`\b\d{2,3}/\d{3}/\d{4,5}\b`)
	vatIDRe     = regexp.MustCompile(`\bDE ?\d{9}\b`)
	hintRe      = regexp.MustCompile(`\{hint\.[A-Za-z_]+\}`)
)

// docHints: Hinweise zum Dokument (leere Werte entfallen).
func (m *Module) docHints(ctx context.Context, d *docRow) map[string]string {
	text := m.docText(ctx, d.ID)
	out := map[string]string{}
	if strings.TrimSpace(text) == "" {
		return out
	}
	own := m.ownIBANs(ctx)
	for _, f := range ibanRe.FindAllString(strings.ToUpper(text), -1) {
		if i := compactIBAN(f); ibanValid(i) && !own[i] {
			out["iban"] = i
			break
		}
	}
	if out["iban"] != "" {
		if p := m.payeeOf(ctx, d.CC, out["iban"]); p.Name != "" {
			out["payee"] = p.Name
		}
	}
	if t := taxNumberRe.FindString(text); t != "" {
		out["tax_number"] = t
	}
	if v := vatIDRe.FindString(text); v != "" {
		out["vat_id"] = strings.ReplaceAll(v, " ", "")
	}
	return out
}

// payee ist die Gegenseite der Bankumsätze mit einer IBAN.
type payee struct{ Name, Partner string }

// payeeOf: Name und (falls zugeordnet) Geschäftspartner der jüngsten Bankumsätze mit der IBAN.
func (m *Module) payeeOf(ctx context.Context, cc, iban string) payee {
	var out payee
	latest := ""
	for _, t := range m.listItems(ctx, "BankTransaction", map[string]any{"company_code": cc, "counterparty_iban": iban}) {
		if compactIBAN(crud.Str(t["counterparty_iban"])) != iban {
			continue
		}
		if day := crud.Str(t["booking_date"]); day >= latest {
			latest = day
			out.Name = firstOf(strings.TrimSpace(crud.Str(t["counterparty_name"])), out.Name)
			out.Partner = firstOf(crud.Str(t["partner_id"]), out.Partner)
		}
	}
	return out
}

// ownIBANs: IBANs der eigenen Bankkonten (Hausverwaltung als Empfänger, nicht der Absender).
func (m *Module) ownIBANs(ctx context.Context) map[string]bool {
	own := map[string]bool{}
	for _, a := range m.listItems(ctx, "BankAccount", map[string]any{}) {
		own[compactIBAN(crud.Str(a["iban"]))] = true
	}
	return own
}

func compactIBAN(s string) string {
	return strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(s)), " ", "")
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

// applyHints: Parameter hints („banks.iban={hint.iban}“) in leere Felder des Ergebnisses;
// die Form (Liste oder Einzelobjekt) kommt aus der leeren Struktur der KI-Anfrage.
func (m *Module) applyHints(ctx context.Context, d *docRow, s *stepDef, request string, result map[string]any) (map[string]any, []string) {
	spec := strings.TrimSpace(s.param(ctx, "hints"))
	if spec == "" {
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
	for _, p := range strings.Split(spec, ";") {
		path, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		if v = strings.TrimSpace(m.expand(ctx, d, strings.TrimSpace(v))); v == "" || hintRe.MatchString(v) {
			continue
		}
		if fillPath(result, empty, strings.Split(strings.TrimSpace(path), "."), v) {
			used = append(used, strings.TrimSpace(path))
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
