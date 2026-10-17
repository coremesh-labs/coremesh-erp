package dms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// KI-Anfrage (Schrittart AI): Text-Anfrage des Moduls ai mit dem erkannten Text
// und den Zeilen des Kontexts (Platzhalter; Zeilen mit leerem Hinweis entfallen);
// mit einer Quelle (wie bei der Auswahl) nur deren Werte als Antwort. Das
// Ergebnis wird Vorschlag bzw. Wert des Ziels. Antworten der KI kommen in den
// Zwischenspeicher des Dokuments (ai_cache): gleiche Anfrage, gleicher Text,
// gleicher Kontext → dieselbe Antwort, auch nach „Von vorn“; „KI neu fragen“
// an der Maske fragt erneut.

// aiCacheKey: Schlüssel im Zwischenspeicher.
func aiCacheKey(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

func (m *Module) aiCacheGet(ctx context.Context, docID, key string, out any) bool {
	all := map[string]json.RawMessage{}
	if s := m.text(ctx, `SELECT ai_cache FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &all)
	}
	raw, ok := all[key]
	return ok && json.Unmarshal(raw, out) == nil
}

func (m *Module) aiCachePut(ctx context.Context, docID, key string, v any) {
	all := map[string]json.RawMessage{}
	if s := m.text(ctx, `SELECT ai_cache FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &all)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	all[key] = b
	if b, err = json.Marshal(all); err == nil {
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET ai_cache = ? WHERE id = ?`, string(b), docID); err != nil {
			m.log.WarnContext(ctx, "KI-Zwischenspeicher nicht geschrieben", "document", docID, "err", err.Error())
		}
	}
}

func (m *Module) aiCacheDrop(ctx context.Context, docID, key string) {
	all := map[string]json.RawMessage{}
	if s := m.text(ctx, `SELECT ai_cache FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &all)
	}
	if _, ok := all[key]; !ok {
		return
	}
	delete(all, key)
	b, _ := json.Marshal(all)
	_, _ = m.db.Exec(ctx, `UPDATE dms__document SET ai_cache = ? WHERE id = ?`, string(b), docID)
}

// runAIStep: Programmschritt KI-Anfrage.
func (m *Module) runAIStep(ctx context.Context, d *docRow, s *stepDef) (string, string, string) {
	if s.AIRequest == "" {
		return oFailed, "", "KI-Anfrage am Schritt fehlt"
	}
	var allowed []choice
	if s.SourceKind != "" {
		cs, err := m.choices(ctx, d, s, "")
		if err != nil {
			return oFailed, "", err.Error()
		}
		allowed = cs
	}
	lines := []string{m.expandLines(ctx, d, s.text(ctx, "context", s.Context))}
	if len(allowed) > 0 {
		var opts []string
		for _, c := range allowed {
			if strings.HasPrefix(c.Label, c.Value+" – ") {
				opts = append(opts, c.Label)
			} else {
				opts = append(opts, c.Value+" – "+c.Label)
			}
		}
		lines = append(lines, "Antworte nur mit dem Schlüssel vor dem Gedankenstrich, erlaubt: "+strings.Join(opts, "; "))
	}
	text := m.docText(ctx, d.ID)
	ctxText := strings.TrimSpace(strings.Join(lines, "\n"))
	key := aiCacheKey("AI", s.AIRequest, text, ctxText)
	var answer string
	cached := m.aiCacheGet(ctx, d.ID, key, &answer)
	if !cached {
		if dryRunOf(ctx) != nil {
			return oSkipped, "", "Probelauf: KI nicht gefragt (keine gespeicherte Antwort)"
		}
		resp, err := m.services.Call(ctx, "Ai", "run", map[string]any{"request": s.AIRequest, "text": text, "context": ctxText, "company_code": d.CC})
		if err != nil {
			return oFailed, "", "KI: " + err.Error()
		}
		var out struct {
			Status, Message, Text string
		}
		if err := sdk.Decode(resp.Payload, &out); err != nil {
			return oFailed, "", err.Error()
		}
		if out.Status != "DONE" {
			return oFailed, "", "KI: " + out.Message
		}
		answer = out.Text
		m.aiCachePut(ctx, d.ID, key, answer)
	}
	answer = strings.Trim(strings.TrimSpace(answer), "\"'`. ")
	if v, _, ok := strings.Cut(answer, " – "); ok {
		answer = strings.TrimSpace(v)
	}
	label := answer
	if len(allowed) > 0 {
		if !containsChoice(allowed, answer) {
			return oFailed, "", fmt.Sprintf("KI-Antwort %q steht nicht zur Auswahl", answer)
		}
		for _, c := range allowed {
			if c.Value == answer {
				label = c.Label
			}
		}
	}
	info := "KI"
	if cached {
		info = "KI (gespeicherte Antwort)"
	}
	if s.Target == "" {
		return oSuccess, answer, info
	}
	v, err := m.applyResult(ctx, d, s, s.targetKey(), &proposalRec{Value: answer, Label: label, Source: "KI, Schritt " + s.Step})
	if err != nil {
		return oFailed, "", err.Error()
	}
	return oSuccess, firstOf(v, answer), info
}
