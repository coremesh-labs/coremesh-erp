package process

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Objektdaten (seit 0.6.0): der Vorgang als JSON process/1 für die Ablage
// (Vorgang_Objektdaten_Konzept.txt, Abschnitte 3–6).
//
//   - Process.exportData {id, umfang, partner, dokumente} – Systemaufruf des
//     Exports mit den Rechten des Auftraggebers; was hinausgeht, bestimmt der
//     Umfang (Katalog im Export, je Sicht). Texte nur öffentlich (ohne „nur lokal“),
//     Verlauf nach dem Feld „öffentlich“ der Eintragsart.
//   - Process.participants {id} – Partner am Vorgang mit Rolle (Bezugsregel BETEILIGTE).
//
// Deterministisch: gleicher Stand und gleiche Eingabe ergeben dieselben Bytes
// (keine Erzeugungszeit; die Ampel gilt zum Stand). Feste Klassen (status.klasse,
// schritt.status, ampel, bezug.art) sind Teil des Formats, keine Fachregeln.

const (
	processContentType = "application/vnd.coremesh.process+json;v=1"
	processSchema      = "process/1"
	maxSubLevels       = 5
)

// scope: der Umfang aus dem Export (Felder wie Konzept Abschnitt 5).
type scope struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Beschreibung  bool   `json:"beschreibung"`
	Zustaendig    bool   `json:"zustaendig"`
	Beteiligte    string `json:"beteiligte"` // KEINE | NAMEN | MIT_KONTAKT
	Schritte      bool   `json:"schritte"`
	Texte         bool   `json:"texte"`
	Verlauf       string `json:"verlauf"` // KEINE | SYSTEM | OEFFENTLICH | ALLE_OEFFENTLICHEN_UND_SYSTEM
	VerlaufMax    int    `json:"verlauf_max"`
	Dokumente     bool   `json:"dokumente"`
	Teilvorgaenge string `json:"teilvorgaenge"` // KEINE | KURZ | VOLL
}

const (
	scopeNone      = "KEINE"
	scopeNames     = "NAMEN"
	scopeContact   = "MIT_KONTAKT"
	histSystem     = "SYSTEM"
	histPublic     = "OEFFENTLICH"
	histPublicSys  = "ALLE_OEFFENTLICHEN_UND_SYSTEM"
	subShort       = "KURZ"
	subFull        = "VOLL"
	signalGreen    = "GRUEN"
	signalYellow   = "GELB"
	signalRed      = "ROT"
	outStepOpen    = "OFFEN"
	outStepDone    = "ERLEDIGT"
	outStepSkipped = "ENTFALLEN"
)

// parts: die Teile, die der Umfang einschließt (in der Reihenfolge des Konzepts).
func (s scope) parts() []string {
	var out []string
	add := func(on bool, name string) {
		if on {
			out = append(out, name)
		}
	}
	add(s.Beschreibung, "beschreibung")
	add(s.Zustaendig, "zustaendig")
	add(s.Beteiligte != "" && s.Beteiligte != scopeNone, "beteiligte")
	add(s.Schritte, "schritte")
	add(s.Texte, "texte")
	add(s.Verlauf != "" && s.Verlauf != scopeNone, "verlauf")
	add(s.Dokumente, "dokumente")
	add(s.Teilvorgaenge != "" && s.Teilvorgaenge != scopeNone, "teilvorgaenge")
	return out
}

// docIn: ein Dokument, das der Export schon im Index hat.
type docIn struct {
	DocID       string `json:"doc_id"`
	Ref         string `json:"ref"`
	Titel       string `json:"titel"`
	Datum       string `json:"datum"`
	Art         string `json:"art"`
	ArtName     string `json:"art_name"`
	ContentType string `json:"content_type"`
}

type exportIn struct {
	ID        string  `json:"id"`
	Umfang    scope   `json:"umfang"`
	Partner   string  `json:"partner"`
	Dokumente []docIn `json:"dokumente"`
}

// statusClass: Art des Status → feste Klasse des Formats.
var statusClass = map[string]string{kindOpen: "OFFEN", kindWaiting: "WARTET", kindClosed: "ERLEDIGT"}

// stepClass: Stand des Schritts → feste Klasse des Formats.
var stepClass = map[string]string{stepOpen: outStepOpen, stepDone: outStepDone, stepSkipped: outStepSkipped}

// refKinds: Bezugsfelder des Vorgangs → feste Art des Bezugs und Object zum Nachschlagen des Namens.
var refKinds = []struct{ field, art, object string }{
	{"partner_id", "PARTNER", ""}, {"contract_id", "VERTRAG", "Contract"}, {"object_id", "MIETOBJEKT", "RentObject"},
	{"entity_id", "WIRTSCHAFTSEINHEIT", "BusinessEntity"}, {"asset_id", "ANLAGE", "Asset"},
}

func (m *Module) exportDataAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in exportIn
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	b := &abbild{m: m, ctx: ctx, cc: cc, s: in.Umfang, partner: strings.TrimSpace(in.Partner), docs: in.Dokumente, kinds: map[string]entryKind{}}
	v, err := b.process(ctx, id, 0, false)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, f := range b.post {
		f()
	}
	stand := b.stand
	if stand == "" {
		stand = "1970-01-01T00:00:00Z"
	}
	data := map[string]any{"schema": processSchema, "sprache": "de", "stand": stand,
		"quelle":  map[string]any{"system": "coremesh", "buchungskreis": compact(map[string]any{"code": cc, "name": m.companyName(ctx, cc)})},
		"vorgang": v}
	if u := compact(map[string]any{"code": in.Umfang.Code, "name": in.Umfang.Name, "teile": in.Umfang.parts()}); u != nil {
		data["umfang"] = u
	}
	return sdk.Response{Payload: map[string]any{"content_type": processContentType, "name": "vorgang.json", "titel": "Vorgang " + id,
		"datum": stand[:10], "stand": stand, "partner": crud.Str(v["_partner"]), "data": strip(data)}}, nil
}

// abbild: Aufbau eines Abbilds (rekursiv über die Teilvorgänge).
type abbild struct {
	m       *Module
	ctx     context.Context
	cc      string
	s       scope
	partner string
	docs    []docIn
	stand   string
	kinds   map[string]entryKind
	post    []func() // Ampeln, sobald der Stand feststeht
}

type entryKind struct {
	name             string
	system, isPublic bool
}

func (b *abbild) later(t string) {
	if t = normStamp(t); t > b.stand {
		b.stand = t
	}
}

// process: ein Vorgang; level > 0 für Teilvorgänge, short = nur die Kurzfassung.
func (b *abbild) process(ctx context.Context, id string, level int, short bool) (map[string]any, error) {
	m, cc := b.m, b.cc
	// get prüft das Leserecht
	resp, err := m.set.Entity(processObject).Get(ctx, map[string]any{"id": cc + "|" + id})
	if err != nil {
		return nil, err
	}
	var rec crud.Record
	if err := sdk.Decode(resp.Payload, &rec); err != nil {
		return nil, err
	}
	str := func(k string) string { return strings.TrimSpace(crud.Str(rec[k])) }
	b.later(str("changed_at"))
	b.later(str("last_activity"))
	kind := ""
	if row, err := m.one(ctx, "SELECT kind FROM process__status WHERE company_code = ? AND code = ?", cc, str("status")); err == nil && row != nil {
		kind = crud.Str(row[0])
	}
	v := map[string]any{"nummer": id, "kurz": str("short_name"), "betreff": str("subject"),
		"art":    b.code("type", str("process_type")),
		"status": compact(map[string]any{"code": str("status"), "name": m.name(ctx, "status", cc, str("status")), "klasse": statusClass[kind]}),
	}
	daten := map[string]any{"naechste_frist": dateStr(rec["next_due"]), "abgeschlossen": dateStr(rec["closed_on"])}
	if !short {
		v["perspektive"] = b.code("perspective", str("perspective"))
		v["prioritaet"] = b.code("priority", str("priority"))
		if kind == kindClosed {
			v["erledigung"] = b.code("resolution", str("resolution"))
		}
		if kind == kindWaiting {
			v["wartet_auf"] = str("waiting_for")
		}
		v["naechster_schritt"] = str("next_step")
		if b.s.Beschreibung {
			v["beschreibung_md"] = str("description")
		}
		if b.s.Zustaendig {
			v["zustaendig"] = named(m.userName(ctx, str("assignee")))
			v["vertretung"] = named(m.userName(ctx, str("deputy")))
		}
		if p := str("parent_id"); p != "" && level == 0 {
			if row, err := m.one(ctx, "SELECT subject FROM process__process WHERE company_code = ? AND process_id = ?", cc, p); err == nil && row != nil {
				v["uebergeordnet"] = map[string]any{"nummer": p, "betreff": crud.Str(row[0])}
			}
		}
		for k, f := range map[string]string{"eroeffnet": "opened_on", "stichtag": "event_date", "eingang_bestaetigt": "acknowledged_on",
			"bestaetigung_bis": "ack_due", "antwort_bis": "answer_due", "wiedervorlage": "follow_up"} {
			daten[k] = dateStr(rec[f])
		}
		var refs []any
		for _, r := range refKinds {
			val := str(r.field)
			if val == "" {
				continue
			}
			name := ""
			if r.object == "" {
				name = m.partnerName(ctx, val)
			} else {
				name = m.objectName(ctx, r.object, cc+"|"+val)
			}
			refs = append(refs, map[string]any{"art": r.art, "id": val, "name": name})
		}
		v["bezuege"] = refs
		if err := b.details(ctx, v, id); err != nil {
			return nil, err
		}
	}
	v["daten"] = daten
	b.post = append(b.post, func() { v["ampel"] = b.signal(rec, kind) })
	if level == 0 {
		v["_partner"] = str("partner_id")
	}
	if b.s.Teilvorgaenge == subShort || b.s.Teilvorgaenge == subFull {
		if level+1 < maxSubLevels {
			res, err := m.db.Query(ctx, "SELECT process_id FROM process__process WHERE company_code = ? AND parent_id = ? ORDER BY process_id", cc, id)
			if err != nil {
				return nil, err
			}
			var subs []any
			for _, r := range res.Rows {
				sub, err := b.process(ctx, crud.Str(r[0]), level+1, b.s.Teilvorgaenge == subShort)
				if err != nil {
					if errorsIsDenied(err) {
						continue
					}
					return nil, err
				}
				subs = append(subs, sub)
			}
			v["teilvorgaenge"] = subs
		}
	}
	return v, nil
}

// details: Beteiligte, Schritte, Texte, Verlauf und Dokumente nach Umfang.
func (b *abbild) details(ctx context.Context, v map[string]any, id string) error {
	m, cc := b.m, b.cc
	if b.s.Beteiligte == scopeNames || b.s.Beteiligte == scopeContact {
		res, err := m.db.Query(ctx, "SELECT role_code, partner_id, note FROM process__partner WHERE company_code = ? AND process_id = ? ORDER BY role_code, partner_id", cc, id)
		if err != nil {
			return err
		}
		var list []any
		for _, r := range res.Rows {
			pid := crud.Str(r[1])
			e := map[string]any{"rolle": b.code("role", crud.Str(r[0])), "partner": map[string]any{"id": pid, "name": firstNonEmpty(m.partnerName(ctx, pid), pid)},
				"notiz": strings.TrimSpace(crud.Str(r[2]))}
			if b.s.Beteiligte == scopeContact {
				e["kontakt"] = m.partnerContact(ctx, pid)
			}
			if b.partner != "" && pid == b.partner {
				e["ich"] = true
			}
			list = append(list, e)
		}
		v["beteiligte"] = list
	}
	if b.s.Schritte {
		res, err := m.db.Query(ctx, `SELECT step_no, name, status, due_date, done_on, required, role_hint, note, checklist
			FROM process__step WHERE company_code = ? AND process_id = ? ORDER BY step_no`, cc, id)
		if err != nil {
			return err
		}
		var list []any
		for _, r := range res.Rows {
			st := stepClass[crud.Str(r[2])]
			e := map[string]any{"nr": toInt(r[0]), "name": crud.Str(r[1]), "status": st, "pflicht": crud.AsBool(r[5]),
				"rolle_hinweis": crud.Str(r[6]), "notiz": crud.Str(r[7]), "pruefliste_md": checklistMD(crud.Str(r[8]))}
			if st == outStepOpen {
				e["faellig"] = dateStr(r[3])
			} else {
				e["erledigt"] = dateStr(r[4])
			}
			list = append(list, e)
		}
		v["schritte"] = list
	}
	if b.s.Texte {
		var list []any
		for _, t := range m.sheetTexts(ctx, cc, id, true) {
			t := t.(map[string]any)
			list = append(list, map[string]any{"kategorie": map[string]any{"code": t["category"], "name": t["category_name"]},
				"position": t["position"], "inhalt_md": t["content"], "geaendert": t["changed_at"]})
			if d := crud.Str(t["changed_at"]); d != "" {
				b.later(d)
			}
		}
		v["texte"] = list
	}
	if mode := b.s.Verlauf; mode == histSystem || mode == histPublic || mode == histPublicSys {
		res, err := m.db.Query(ctx, "SELECT entry_no, entry_at, kind, text, user_id FROM process__entry WHERE company_code = ? AND process_id = ? ORDER BY entry_no", cc, id)
		if err != nil {
			return err
		}
		var list []any
		for _, r := range res.Rows {
			b.later(crud.Str(r[1]))
			k := b.kind(ctx, crud.Str(r[2]))
			if !(mode == histSystem && k.system || mode == histPublic && k.isPublic || mode == histPublicSys && (k.isPublic || k.system)) {
				continue
			}
			e := map[string]any{"nr": toInt(r[0]), "zeit": normStamp(crud.Str(r[1])),
				"art": compact(map[string]any{"code": crud.Str(r[2]), "name": k.name, "system": k.system}), "text": crud.Str(r[3])}
			if !k.system {
				e["von"] = named(m.userName(ctx, crud.Str(r[4])))
			}
			list = append(list, e)
		}
		if n := b.s.VerlaufMax; n > 0 && len(list) > n {
			list = list[len(list)-n:]
			v["verlauf_gekuerzt"] = true
		}
		v["verlauf"] = list
	}
	if b.s.Dokumente {
		var list []any
		for _, d := range b.docs {
			if strings.TrimSpace(d.Ref) == "" {
				continue
			}
			var art any
			if d.Art != "" {
				art = map[string]any{"code": d.Art, "name": firstNonEmpty(d.ArtName, d.Art)}
			}
			list = append(list, map[string]any{"ref": d.Ref, "titel": d.Titel, "datum": d.Datum, "art": art, "content_type": d.ContentType})
		}
		v["dokumente"] = list
	}
	return nil
}

// signal: Ampel zum Stand (nicht zur Erzeugung) – sonst änderte sich das Abbild ohne Änderung des Vorgangs.
func (b *abbild) signal(rec crud.Record, kind string) string {
	if kind == kindClosed {
		return signalGreen
	}
	day := b.stand
	if len(day) >= 10 {
		day = day[:10]
	}
	if toInt(rec["escalation"]) >= levelEscalate {
		return signalRed
	}
	worst := signalGreen
	for _, k := range []string{"next_due", "sub_due"} {
		d := dateStr(rec[k])
		switch {
		case d == "" || day == "":
		case d < day:
			return signalRed
		case d == day:
			worst = signalYellow
		}
	}
	return worst
}

func (b *abbild) code(table, code string) any {
	if code == "" {
		return nil
	}
	return map[string]any{"code": code, "name": b.m.name(b.ctx, table, b.cc, code)}
}

func (b *abbild) kind(ctx context.Context, code string) entryKind {
	if k, ok := b.kinds[code]; ok {
		return k
	}
	k := entryKind{name: code}
	if row, err := b.m.one(ctx, "SELECT name, is_system, is_public FROM process__entry_kind WHERE company_code = ? AND code = ?", b.cc, code); err == nil && row != nil {
		k = entryKind{name: crud.Str(row[0]), system: crud.AsBool(row[1]), isPublic: crud.AsBool(row[2])}
	}
	b.kinds[code] = k
	return k
}

// participantsAction: Process.participants {id} → {items: [{rolle, partner_id}], partner_id}
// – Partner am Vorgang (mit Rolle) und der Hauptpartner, für die Bezugsregel BETEILIGTE.
func (m *Module) participantsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.set.Entity(processObject).Get(ctx, map[string]any{"id": cc + "|" + id}); err != nil {
		return sdk.Response{}, err
	}
	row, err := m.one(ctx, "SELECT COALESCE(partner_id, '') FROM process__process WHERE company_code = ? AND process_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, "SELECT role_code, partner_id FROM process__partner WHERE company_code = ? AND process_id = ? ORDER BY role_code, partner_id", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	items := []any{}
	for _, r := range res.Rows {
		items = append(items, map[string]any{"rolle": crud.Str(r[0]), "partner_id": crud.Str(r[1])})
	}
	main := ""
	if row != nil {
		main = crud.Str(row[0])
	}
	return sdk.Response{Payload: map[string]any{"items": items, "partner_id": main}}, nil
}

// --- Hilfen -----------------------------------------------------------------------

func named(name string) any {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return map[string]any{"name": name}
}

// checklistMD: Prüfliste (eine Zeile je Punkt) als Markdown-Liste.
func checklistMD(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, "- "+l)
		}
	}
	return strings.Join(out, "\n")
}

// normStamp: Zeitpunkt als RFC 3339 in UTC; ein reines Datum bleibt Datum (Mitternacht UTC).
func normStamp(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, f := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", time.DateOnly} {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// compact: leere Felder weg (Konzept 3.5); nil, wenn nichts bleibt.
func compact(v map[string]any) map[string]any {
	out := strip(v).(map[string]any)
	if len(out) == 0 {
		return nil
	}
	return out
}

// strip: rekursiv leere Werte (nil, "", false, leere Liste/Objekt) entfernen; interne Felder (_…) auch.
func strip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, e := range x {
			if strings.HasPrefix(k, "_") {
				continue
			}
			if e = strip(e); !empty(e) {
				out[k] = e
			}
		}
		return out
	case []any:
		out := []any{}
		for _, e := range x {
			if e = strip(e); !empty(e) {
				out = append(out, e)
			}
		}
		return out
	case []string:
		out := []any{}
		for _, e := range x {
			if e != "" {
				out = append(out, e)
			}
		}
		return out
	}
	return v
}

func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case bool:
		return !x
	case map[string]any:
		return len(x) == 0
	case []any:
		return len(x) == 0
	}
	return false
}

func errorsIsDenied(err error) bool {
	return errors.Is(err, sdk.ErrPermissionDenied)
}

// objectName: Bezeichnung eines Objekts eines anderen Moduls (leer, wenn es fehlt oder nicht lesbar ist).
func (m *Module) objectName(ctx context.Context, object, key string) string {
	resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": key})
	if err != nil {
		return ""
	}
	var rec map[string]any
	if sdk.Decode(resp.Payload, &rec) != nil {
		return ""
	}
	for _, k := range []string{"name", "title", "description", "subject"} {
		if s := strings.TrimSpace(crud.Str(rec[k])); s != "" {
			return s
		}
	}
	return ""
}

// companyName: Bezeichnung des Buchungskreises.
func (m *Module) companyName(ctx context.Context, cc string) string {
	return crud.Str(reportapi.Company(ctx, m.services, cc)["name"])
}

// partnerContact: Telefon, E-Mail (Standard je Kategorie) und Hauptanschrift des Partners.
func (m *Module) partnerContact(ctx context.Context, id string) any {
	p := reportapi.Partner(ctx, m.services, id)
	if p == nil {
		return nil
	}
	c := map[string]any{}
	if cm, ok := p["contact"].(map[string]any); ok {
		c["telefon"], c["email"] = cm["PHONE"], cm["EMAIL"]
	}
	street, zip, city := crud.Str(p["street"]), crud.Str(p["zip"]), crud.Str(p["city"])
	c["adresse"] = strings.Trim(strings.TrimSpace(street+", "+strings.TrimSpace(zip+" "+city)), ", ")
	return compact(c)
}
