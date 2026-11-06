package export

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Objektdaten (seit 0.6.0, Vorgang_Objektdaten_Konzept.txt): ein Fachmodul liefert
// ein Fachobjekt als JSON (z. B. Process.exportData → process/1). Die Sichtart nennt
// die Aktion (export_call, Object.Aktion) – jedes Modul kann Objekte liefern.
//
//   - Je Dataset (Schlüssel = Objekt, z. B. je Vorgang) steht das Abbild als eigenes
//     Element im Stand (document_id obj:<Prüfsumme>, Datei vorgang.json); ändert sich
//     der Inhalt, kommt ein neues Element und das alte gilt als ersetzt.
//   - Was hinausgeht, bestimmt der Umfang (Katalog „Umfänge der Objektdaten“, je Sicht,
//     Vorschlag an der Sichtart).
//   - Verweise auf Dokumente: beim Erzeugen "ref":"doc:<id>", beim Schreiben ersetzt durch
//     den Pfad der Datendatei (Ablage: o/<sha256>) bzw. die Datei (Verzeichnis, zip).
//   - Bezugsregel BETEILIGTE: Teilnehmer sieht das Dataset, wenn sein Partner am Objekt
//     beteiligt ist (participants_call, z. B. Process.participants) – mit einer der
//     Rollen der Freigabe (leer = alle Beteiligten, auch der Hauptpartner).

const (
	scopeObject = "ExportDataScope"
	objPrefix   = "obj:"
	ruleParties = "BETEILIGTE"

	partiesNone    = "KEINE"
	partiesNames   = "NAMEN"
	partiesContact = "MIT_KONTAKT"
	histNone       = "KEINE"
	histSystem     = "SYSTEM"
	histPublic     = "OEFFENTLICH"
	histPublicSys  = "ALLE_OEFFENTLICHEN_UND_SYSTEM"
	childNone      = "KEINE"
	childShort     = "KURZ"
	childFull      = "VOLL"
)

var partiesOptions = []metamodel.Option{
	{Value: partiesNone, Label: "keine"}, {Value: partiesNames, Label: "mit Namen"}, {Value: partiesContact, Label: "mit Namen und Kontakt (Telefon, E-Mail, Anschrift)"},
}

var historyOptions = []metamodel.Option{
	{Value: histNone, Label: "kein Verlauf"}, {Value: histSystem, Label: "nur Systemeinträge"},
	{Value: histPublic, Label: "Einträge öffentlicher Arten"}, {Value: histPublicSys, Label: "öffentliche Einträge und alle Systemeinträge"},
}

var childOptions = []metamodel.Option{
	{Value: childNone, Label: "keine"}, {Value: childShort, Label: "kurz (Nummer, Betreff, Status, Frist)"}, {Value: childFull, Label: "vollständig"},
}

var lookupScope = &metamodel.Lookup{Object: scopeObject, ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}

// dataScope: Katalog Umfänge der Objektdaten.
func (m *Module) dataScope() *crud.Entity {
	return &crud.Entity{
		Object: scopeObject, Customizing: true, Title: "Umfänge der Objektdaten", Icon: "icon-filter", Table: "export__data_scope", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tBool, Listable: true},
			{Key: "assignee", Label: "Sachbearbeiter und Vertretung (Name)", Short: "Sachbearbeiter", Type: tBool, Listable: true},
			{Key: "parties", Label: "Beteiligte", Type: tSel, Required: true, Listable: true, Options: partiesOptions},
			{Key: "steps", Label: "Schritte", Type: tBool, Listable: true},
			{Key: "texts", Label: "Texte (nie „nur lokal“)", Short: "Texte", Type: tBool, Listable: true},
			{Key: "history", Label: "Verlauf", Type: tSel, Required: true, Listable: true, Options: historyOptions},
			{Key: "history_max", Label: "Verlauf: höchstens … neueste Einträge (leer = alle)", Short: "höchstens", Type: tNum},
			{Key: "documents", Label: "Dokumente (die die Sicht ohnehin liefert)", Short: "Dokumente", Type: tBool, Listable: true},
			{Key: "children", Label: "Teilvorgänge", Type: tSel, Required: true, Listable: true, Options: childOptions},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, scopeObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if err := codeCheck(rec, old); err != nil {
				return err
			}
			for _, f := range []struct {
				key  string
				opts []metamodel.Option
				def  string
			}{{"parties", partiesOptions, partiesNone}, {"history", historyOptions, histNone}, {"children", childOptions, childNone}} {
				v := firstNonEmpty(trimUpper(rec[f.key]), f.def)
				if !validOption(f.opts, v) {
					return crud.Invalid("%s: %q gibt es nicht", f.key, v)
				}
				rec[f.key] = v
			}
			if n := intPtr(rec["history_max"]); n == nil || *n <= 0 {
				rec["history_max"] = nil
			}
			return nil
		},
	}
}

// scopeDefaults: Vorschläge (Konzept Abschnitt 5).
var scopeDefaults = []map[string]any{
	{"code": "MIETER", "name": "Mieter", "description": true, "assignee": true, "parties": partiesNames, "steps": true, "texts": true,
		"history": histPublic, "documents": true, "children": childShort},
	{"code": "HANDWERKER", "name": "Handwerker", "description": true, "assignee": true, "parties": partiesContact, "steps": true,
		"history": histPublic, "documents": true, "children": childNone},
	{"code": "BEIRAT", "name": "Verwaltungsbeirat", "description": true, "assignee": true, "parties": partiesNames, "steps": true, "texts": true,
		"history": histPublicSys, "documents": true, "children": childFull},
	{"code": "EIGENTUEMER", "name": "Eigentümer", "description": true, "assignee": true, "parties": partiesNames, "steps": true, "texts": true,
		"history": histPublicSys, "documents": true, "children": childFull},
}

func (m *Module) setupScopes(ctx context.Context, cc string) (int, error) {
	n := 0
	for i, d := range scopeDefaults {
		r, err := m.one(ctx, "SELECT 1 FROM export__data_scope WHERE company_code = ? AND code = ?", cc, d["code"])
		if err != nil {
			return 0, err
		}
		if r != nil {
			continue
		}
		b := func(k string) bool { v, _ := d[k].(bool); return v }
		if _, err := m.db.Exec(ctx, `INSERT INTO export__data_scope (company_code, code, name, description, assignee, parties, steps, texts, history, documents, children,
			sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, d["code"], d["name"], b("description"), b("assignee"), d["parties"], b("steps"),
			b("texts"), d["history"], b("documents"), d["children"], (i+1)*10, true); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// umfang: der Umfang als Eingabe des Fachmoduls (Feldnamen des Konzepts); nil = keiner gewählt.
func (m *Module) umfang(ctx context.Context, cc, code string) (map[string]any, error) {
	if code == "" {
		return nil, nil
	}
	r, err := m.one(ctx, `SELECT name, description, assignee, COALESCE(parties, ''), steps, texts, COALESCE(history, ''), history_max, documents, COALESCE(children, '')
		FROM export__data_scope WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil || r == nil {
		return nil, err
	}
	return map[string]any{"code": code, "name": crud.Str(r[0]), "beschreibung": crud.AsBool(r[1]), "zustaendig": crud.AsBool(r[2]), "beteiligte": crud.Str(r[3]),
		"schritte": crud.AsBool(r[4]), "texte": crud.AsBool(r[5]), "verlauf": crud.Str(r[6]), "verlauf_max": toInt(r[7]), "dokumente": crud.AsBool(r[8]),
		"teilvorgaenge": crud.Str(r[9])}, nil
}

// objCfg: Objektdaten einer Sicht.
type objCfg struct {
	call, scope string
}

func (m *Module) objCfgOf(ctx context.Context, cc, viewID string) (objCfg, error) {
	r, err := m.one(ctx, `SELECT COALESCE(t.export_call, ''), COALESCE(v.scope, ''), COALESCE(t.scope, '') FROM export__view v
		JOIN export__view_type t ON t.company_code = v.company_code AND t.code = v.view_type WHERE v.company_code = ? AND v.view_id = ?`, cc, viewID)
	if err != nil || r == nil {
		return objCfg{}, err
	}
	return objCfg{call: crud.Str(r[0]), scope: firstNonEmpty(crud.Str(r[1]), crud.Str(r[2]))}, nil
}

// objDoc: Dokument des Stands, auf das das Abbild verweist.
type objDoc struct {
	id, title, date, docType string
}

// objResult: Ergebnis des Fachmoduls.
type objResult struct {
	ContentType string         `json:"content_type"`
	Name        string         `json:"name"`
	Titel       string         `json:"titel"`
	Datum       string         `json:"datum"`
	Stand       string         `json:"stand"`
	Partner     string         `json:"partner"`
	Art         *codeName      `json:"art"`
	Data        map[string]any `json:"data"`
}

// buildObject: Abbild des Objekts zum Dataset erzeugen; liefert das Element (doc) mit
// document_id obj:<Prüfsumme> – gleiche Prüfsumme wie zuletzt ⇒ unverändert.
func (m *Module) buildObject(ctx context.Context, c objCfg, ds dsRow, docs []objDoc, nm *namer) (doc, bool, error) {
	obj, act, ok := strings.Cut(c.call, ".")
	if !ok {
		return doc{}, false, crud.Invalid("Objektdaten: Object.Aktion erwartet (z. B. Process.exportData), nicht %q", c.call)
	}
	um, err := m.umfang(ctx, ds.cc, c.scope)
	if err != nil {
		return doc{}, false, err
	}
	if um == nil {
		um = map[string]any{}
	}
	var list []any
	for _, d := range docs {
		e := map[string]any{"doc_id": d.id, "ref": "doc:" + d.id, "titel": d.title, "datum": d.date}
		if d.docType != "" {
			e["art"], e["art_name"] = d.docType, nm.docType(d.docType)
		}
		list = append(list, e)
	}
	key := firstNonEmpty(ds.process, ds.key)
	resp, err := m.services.Call(ctx, obj, act, map[string]any{"id": ds.cc + "|" + key, "umfang": um, "partner": ds.partner, "dokumente": list})
	if err != nil {
		return doc{}, false, fmt.Errorf("%s: %s – %w", ds.id, c.call, err)
	}
	var res objResult
	if err := sdk.Decode(resp.Payload, &res); err != nil {
		return doc{}, false, err
	}
	if res.ContentType == "" || res.Data == nil {
		return doc{}, false, crud.Invalid("%s: %s liefert keine Objektdaten", ds.id, c.call)
	}
	data, err := json.Marshal(res.Data) // Schlüssel sortiert: gleicher Inhalt, gleiche Bytes
	if err != nil {
		return doc{}, false, err
	}
	h := sha256.Sum256(data)
	sum := hex.EncodeToString(h[:])
	id := objPrefix + sum[:32]
	r, err := m.one(ctx, "SELECT 1 FROM export__object WHERE company_code = ? AND object_id = ?", ds.cc, id)
	if err != nil {
		return doc{}, false, err
	}
	if r == nil {
		var code, name string
		if res.Art != nil {
			code, name = res.Art.Code, res.Art.Name
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO export__object (company_code, object_id, dataset_id, content_type, data, created_at, stand, art_code, art_name, file_name)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, ds.cc, id, ds.id, res.ContentType, string(data), m.stamp(), nilIfEmpty(res.Stand), nilIfEmpty(code), nilIfEmpty(name),
			nilIfEmpty(res.Name)); err != nil {
			return doc{}, false, err
		}
	}
	d := doc{ID: id, Title: firstNonEmpty(res.Titel, key), FileName: firstNonEmpty(res.Name, "objekt.json"), Partner: res.Partner,
		Date: res.Datum, SHA: sum, Size: int64(len(data))}
	if d.Partner != "" {
		d.partnerName = nm.partner(d.Partner)
	}
	return d, true, nil
}

// objectContent: Inhalt eines Abbilds mit aufgelösten Verweisen (doc_id → ref, Content-Type).
func (m *Module) objectContent(ctx context.Context, cc, id string, refs map[string][2]string) (data []byte, ct, artCode, artName string, err error) {
	r, err := m.one(ctx, "SELECT data, content_type, COALESCE(art_code, ''), COALESCE(art_name, '') FROM export__object WHERE company_code = ? AND object_id = ?", cc, id)
	if err != nil {
		return nil, "", "", "", err
	}
	if r == nil {
		return nil, "", "", "", fmt.Errorf("%w: Objektdaten %s", sdk.ErrNotFound, id)
	}
	var v any
	if err := json.Unmarshal([]byte(crud.Str(r[0])), &v); err != nil {
		return nil, "", "", "", err
	}
	v = resolveRefs(v, refs)
	data, err = json.Marshal(v)
	return data, crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), err
}

// resolveRefs: {"ref":"doc:<id>"} → Pfad und Content-Type; unbekannte Dokumente fallen weg.
func resolveRefs(v any, refs map[string][2]string) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = resolveRefs(e, refs)
		}
		if ref, ok := x["ref"].(string); ok && strings.HasPrefix(ref, "doc:") {
			to, ok := refs[strings.TrimPrefix(ref, "doc:")]
			if !ok {
				return nil
			}
			x["ref"] = to[0]
			if to[1] != "" {
				x["content_type"] = to[1]
			}
		}
		return x
	case []any:
		out := make([]any, 0, len(x))
		for _, e := range x {
			if e = resolveRefs(e, refs); e != nil {
				out = append(out, e)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	return v
}

func isObject(docID string) bool { return strings.HasPrefix(docID, objPrefix) }

// --- Bezugsregel BETEILIGTE -------------------------------------------------------------

type parties struct {
	main  string
	roles map[string][]string // Partner → Rollen am Objekt
}

// partiesOf: Beteiligte des Objekts zum Dataset (participants_call der Sichtart).
func (m *Module) partiesOf(ctx context.Context, cc, viewType, key string) (*parties, error) {
	r, err := m.one(ctx, "SELECT COALESCE(participants_call, '') FROM export__view_type WHERE company_code = ? AND code = ?", cc, viewType)
	if err != nil {
		return nil, err
	}
	call := ""
	if r != nil {
		call = crud.Str(r[0])
	}
	obj, act, ok := strings.Cut(call, ".")
	if !ok || key == "" {
		return &parties{roles: map[string][]string{}}, nil
	}
	resp, err := m.services.Call(ctx, obj, act, map[string]any{"id": cc + "|" + key})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", call, err)
	}
	var out struct {
		Items []struct {
			Rolle     string `json:"rolle"`
			PartnerID string `json:"partner_id"`
		} `json:"items"`
		PartnerID string `json:"partner_id"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	p := &parties{main: out.PartnerID, roles: map[string][]string{}}
	for _, it := range out.Items {
		p.roles[it.PartnerID] = append(p.roles[it.PartnerID], strings.ToUpper(it.Rolle))
	}
	return p, nil
}

// allows: Partner ist beteiligt – mit einer der Rollen (leer = jede Rolle und der Hauptpartner).
func (p *parties) allows(partner string, roles []string) bool {
	if p == nil || partner == "" {
		return false
	}
	if len(roles) == 0 {
		return partner == p.main || len(p.roles[partner]) > 0
	}
	for _, have := range p.roles[partner] {
		for _, r := range roles {
			if have == r {
				return true
			}
		}
	}
	return false
}
