package dms

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// partnerObject: Object der Geschäftspartner (Feld partner_id der Dokumente).
const partnerObject = "BusinessPartner"

// Suchtext der Bezüge (seit 0.15.0): Die Freitextsuche der Dokumente geht über Titel,
// Name, Notiz, Datei, erkannten Text und den Suchtext (dms__document_search). Er
// enthält die Texte der aktiven Bezüge – Kopf, Positionen (getAggregate, sonst get)
// und ihre Klartexte (_labels, z. B. Lieferant, Kostenart) – und den Namen des
// Geschäftspartners. Die Objects liegen in anderen Modulen; der Suchtext entsteht neu
// nach jedem Workflow-Lauf, Ablegen, Ändern des Dokuments und seiner Bezüge, beim
// ersten Start für Dokumente ohne Suchtext und – seit 0.16.0 – wenn ein anderes Modul
// eine Änderung meldet (SystemEvent, onSearchEvent): am Bezug selbst (gleicher
// Schlüssel) oder an einem Unterdatensatz (Schlüssel beginnt mit dem des Bezugs, z. B.
// Rechnungsposition 2000|ER-…|1), bzw. am Geschäftspartner des Dokuments. Die
// Zustellung ist „höchstens einmal“; „Suchtexte aktualisieren“ am Buchungskreis
// (DmsCompany) baut alle Dokumente neu auf.

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
		if resp, err := m.services.Call(ctx, partnerObject, "get", map[string]any{"id": d.Partner}); err == nil {
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
	m.setProperty(ctx, d) // seit 0.30.0: Objekt für die Auswahl der Dokumentliste
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

// searchEventObject: Empfänger der Änderungsmeldungen (Action onEvent).
const searchEventObject = "DmsSearch"

func (m *Module) registerSearchEvents(r *module.Router) {
	r.Object(searchEventObject).Handle(events.CallbackAction, m.onSearchEvent)
}

// subscribeSearchEvents: alle Änderungen aller Objects – welche Dokumente betroffen
// sind, entscheiden ihre Bezüge (keine feste Liste von Objects).
func (m *Module) subscribeSearchEvents(ctx context.Context) {
	if err := events.Register(ctx, m.services, events.Subscription{Object: events.All, Action: events.All, CompanyCode: events.All,
		Callback: searchEventObject}); err != nil {
		m.log.WarnContext(ctx, "Event nicht abonniert", "callback", searchEventObject, "err", err.Error())
	}
}

// onSearchEvent: Suchtexte der Dokumente neu, deren Bezug oder Geschäftspartner sich geändert hat.
func (m *Module) onSearchEvent(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	ev, err := events.Decode(req.Payload)
	if err != nil || ev.EntityID == "" {
		return sdk.Response{}, err
	}
	docs := m.docsAffectedBy(ctx, ev)
	for _, id := range docs {
		m.refreshSearch(ctx, id)
	}
	return sdk.Response{Payload: map[string]any{"refreshed": len(docs)}}, nil
}

// docsAffectedBy: Dokumente mit dem Datensatz (oder seinem Oberdatensatz) als aktivem
// Bezug bzw. mit ihm als Geschäftspartner (Partner: Feld id, Zeitscheiben haben id|gültig ab).
func (m *Module) docsAffectedBy(ctx context.Context, ev events.Event) []string {
	res, err := m.db.Query(ctx, `SELECT DISTINCT r.document_id FROM dms__document_ref r JOIN dms__document d ON d.id = r.document_id
		WHERE r.is_active = ? AND d.status IN (?, ?) AND (r.object_key = ? OR substr(?, 1, length(r.object_key) + 1) = r.object_key || '|')`,
		true, stInbox, stFiled, ev.EntityID, ev.EntityID)
	if err != nil {
		m.log.WarnContext(ctx, "Suchtext: betroffene Dokumente nicht ermittelt", "object", ev.Object, "err", err.Error())
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(rows [][]any) {
		for _, r := range rows {
			if id := crud.Str(r[0]); !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	add(res.Rows)
	if ev.Object == partnerObject {
		pid := firstOf(crud.Str(ev.Data["id"]), strings.SplitN(ev.EntityID, "|", 2)[0])
		if res, err := m.db.Query(ctx, `SELECT id FROM dms__document WHERE partner_id = ? AND status IN (?, ?)`, pid, stInbox, stFiled); err == nil {
			add(res.Rows)
		}
	}
	return out
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
