package dms

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Suchtext der Bezüge (seit 0.15.0): Die Freitextsuche der Dokumente geht über Titel,
// Name, Notiz, Datei, erkannten Text und den Suchtext (dms__document_search). Er
// enthält die Texte der aktiven Bezüge – Kopf, Positionen (getAggregate, sonst get)
// und ihre Klartexte (_labels, z. B. Lieferant, Kostenart) – und den Namen des
// Geschäftspartners. Die Objects liegen in anderen Modulen; der Suchtext ist deren
// Stand beim letzten Aufbau: nach jedem Workflow-Lauf, Ablegen, Ändern des Dokuments
// und seiner Bezüge, für alle Dokumente mit „Suchtexte aktualisieren“ am Buchungskreis
// (DmsCompany) bzw. beim ersten Start für Dokumente ohne Suchtext.

// searchTextMax: Länge des Suchtexts je Dokument (Zeichen).
const searchTextMax = 20000

// Technische Werte gehören nicht in den Suchtext: Prüfsummen und IDs (Hex), Zeitstempel.
var (
	searchHexRe  = regexp.MustCompile(`^[0-9a-f]{24,}$`)
	searchTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)
)

// refreshSearch baut den Suchtext eines Dokuments neu; Fehler der anderen Module
// lassen den Bezug aus (Hinweis im Protokoll).
func (m *Module) refreshSearch(ctx context.Context, docID string) {
	if dryRunOf(ctx) != nil {
		return
	}
	d, err := m.loadDoc(ctx, docID)
	if err != nil {
		return
	}
	seen := map[string]bool{}
	var parts []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] || searchHexRe.MatchString(v) || searchTimeRe.MatchString(v) {
			return
		}
		seen[v] = true
		parts = append(parts, v)
	}
	if d.Partner != "" {
		if resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": d.Partner}); err == nil {
			collectStrings(resp.Payload, add)
		}
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	for _, r := range refs {
		add(r.Object + " " + shortKey(r.Key, d.CC))
		resp, err := m.services.Call(ctx, r.Object, "getAggregate", map[string]any{"id": r.Key})
		if err != nil {
			resp, err = m.services.Call(ctx, r.Object, "get", map[string]any{"id": r.Key})
		}
		if err != nil {
			m.log.WarnContext(ctx, "Suchtext: Bezug nicht lesbar", "document", d.ID, "object", r.Object, "key", r.Key, "err", err.Error())
			continue
		}
		collectStrings(resp.Payload, add)
	}
	text := strings.Join(parts, "\n")
	if len(text) > searchTextMax {
		text = text[:searchTextMax]
	}
	if _, err := m.db.Exec(ctx, `DELETE FROM dms__document_search WHERE document_id = ?`, d.ID); err != nil {
		m.log.WarnContext(ctx, "Suchtext nicht gespeichert", "document", d.ID, "err", err.Error())
		return
	}
	if _, err := m.db.Exec(ctx, `INSERT INTO dms__document_search (document_id, text, refreshed_at) VALUES (?, ?, ?)`, d.ID, text, now()); err != nil {
		m.log.WarnContext(ctx, "Suchtext nicht gespeichert", "document", d.ID, "err", err.Error())
	}
}

// collectStrings: alle Texte einer Antwort (Felder, Positionen, Klartexte), Schlüssel sortiert.
func collectStrings(v any, add func(string)) {
	switch x := v.(type) {
	case string:
		add(x)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			collectStrings(x[k], add)
		}
	case []any:
		for _, e := range x {
			collectStrings(e, add)
		}
	case nil, bool:
	case float64, int, int64:
		add(fmt.Sprint(x))
	default:
		var m map[string]any
		if sdk.Decode(x, &m) == nil {
			collectStrings(m, add)
		}
	}
}

// refreshSearchAll: Suchtexte der Dokumente eines Buchungskreises (onlyMissing: nur ohne Suchtext).
func (m *Module) refreshSearchAll(ctx context.Context, cc string, onlyMissing bool) (int, error) {
	q := `SELECT id FROM dms__document WHERE status IN (?, ?)`
	args := []any{stInbox, stFiled}
	if cc != "" {
		q += ` AND company_code = ?`
		args = append(args, cc)
	}
	if onlyMissing {
		q += ` AND NOT EXISTS (SELECT 1 FROM dms__document_search s WHERE s.document_id = dms__document.id)`
	}
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	for _, r := range res.Rows {
		m.refreshSearch(ctx, crud.Str(r[0]))
	}
	return len(res.Rows), nil
}

// refreshSearchAction: „Suchtexte aktualisieren“ am Buchungskreis.
func (m *Module) refreshSearchAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	n, err := m.refreshSearchAll(ctx, in.ID, false)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"refreshed": n, "message": fmt.Sprintf("Buchungskreis %s: Suchtexte von %d Dokumenten aktualisiert", in.ID, n)}}, nil
}
