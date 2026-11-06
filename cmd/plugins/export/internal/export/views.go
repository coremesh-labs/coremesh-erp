package export

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

const runPrefix = "EX" // Nummer der Läufe: EX-JJJJ-NNNN

func errorsIsUnimplemented(err error) bool {
	return errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable)
}

// view: gespeicherte Sicht (Sichtart + Parameter) mit „Zusammenstellen“.
func (m *Module) view() *crud.Entity {
	return &crud.Entity{
		Object: viewObject, Title: "Sichten", Icon: "icon-layers", Table: "export__view", Section: "Sichten",
		Keys: []string{"company_code", "view_id"}, Order: "company_code, view_id", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "view_type"}, Search: []string{"view_id", "name", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "view_id", Label: "Sicht (Kürzel, z. B. STB_2025)", Short: "Sicht", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung (z. B. Steuerbüro 2025)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true, Wide: true},
			{Key: "view_type", Label: "Sichtart", Type: tText, Required: true, Listable: true, Lookup: catalogLookup(viewTypeObject)},
			{Key: "date_from", Label: "Zeitraum von (Belegdatum)", Short: "von", Type: tDate, Listable: true, Group: "Angaben"},
			{Key: "date_to", Label: "Zeitraum bis", Short: "bis", Type: tDate, Listable: true, Group: "Angaben"},
			{Key: "building_id", Label: "Gebäude", Type: tText, Group: "Angaben", Lookup: lookupBuilding},
			{Key: "object_id", Label: "Mietobjekt", Type: tText, Group: "Angaben", Lookup: lookupRentObject},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Group: "Angaben", Lookup: lookupContract},
			{Key: "partner_id", Label: "Partner", Type: tText, Group: "Angaben", Lookup: lookupPartner},
			{Key: "process_id", Label: "Vorgang", Type: tText, Group: "Angaben", Lookup: lookupProcess},
			{Key: "target", Label: "Ziel (Vorschlag beim Exportieren)", Short: "Ziel", Type: tText, Lookup: catalogLookup(targetObject)},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "key_by", Label: "Schlüssel der Datasets (leer = aus der Sichtart)", Short: "Schlüssel", Type: tSel, Options: keyOptions, Group: "Datasets"},
			{Key: "rhythm", Label: "Rhythmus für neue Datasets (leer = aus der Sichtart)", Short: "Rhythmus", Type: tSel, Options: rhythmOptions, Group: "Datasets"},
			{Key: "lifetime_months", Label: "Lebensdauer in Monaten nach dem Zeitraum (leer = aus der Sichtart)", Short: "Lebensdauer", Type: tNum, Group: "Datasets"},
			{Key: "pointer", Label: "Neue Schlüssel gesucht bis (Änderungszeiger)", Type: tText, ReadOnly: true, Group: "Datasets"},
			{Key: "event_seq", Label: "Ereignisjournal der Vorgänge gelesen bis Nr.", Type: tNum, ReadOnly: true, Group: "Datasets"},
			{Key: "last_run", Label: "Letzter Stand", Type: tText, Listable: true, ReadOnly: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "key_date", Label: "Stichtag (leer = heute)", Short: "Stichtag", Type: tDate, ActionOnly: true},
			{Key: "refresh_full", Label: "Vollständig prüfen (alle Dokumente, auch: passt nicht mehr)", Type: tBool, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "datasets", Title: "Datasets", Relation: &metamodel.Relation{Object: datasetObject, ForeignKey: "view_id",
				Match: match("view_id", "view_id"), Columns: []string{"dataset_id", "name", "status", "rhythm", "next_due", "item_count", "marked_count", "delete_after"}}},
			{Key: "freigaben", Title: "Freigaben für die App", Relation: &metamodel.Relation{Object: grantObject, ForeignKey: "view_id",
				Match: match("view_id", "view_id"), Columns: []string{"grant_no", "role", "rule", "module", "valid_to", "is_active"}}},
			{Key: "laeufe", Title: "Stände (Läufe)", Relation: &metamodel.Relation{Object: runObject, ForeignKey: "view_id",
				Match: match("view_id", "view_id"), Columns: []string{"run_id", "dataset_id", "key_date", "status", "item_count", "new_count", "marked_count", "exported_at", "target"}}}},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "compose", Label: "Zusammenstellen …", Icon: "icon-layers", Record: true, Fields: []string{"key_date", "refresh_full"}},
				Handle: m.composeAction},
		},
		Access: &crud.Access{Object: viewObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, viewObject, action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkView,
	}
}

func (m *Module) checkView(ctx context.Context, rec, old crud.Record) error {
	if old == nil {
		rec["view_id"] = trimUpper(rec["view_id"])
		if !codeRe.MatchString(crud.Str(rec["view_id"])) {
			return crud.Invalid("Sicht %q: A–Z, 0–9, _ (höchstens 24 Zeichen)", crud.Str(rec["view_id"]))
		}
	}
	for _, k := range []string{"building_id", "object_id", "contract_id", "partner_id", "process_id", "target", "note"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	rec["key_by"], rec["rhythm"] = nilIfEmpty(trimUpper(rec["key_by"])), nilIfEmpty(trimUpper(rec["rhythm"]))
	if k := crud.Str(rec["key_by"]); k != "" && !validOption(keyOptions, k) {
		return crud.Invalid("Schlüssel %q gibt es nicht", k)
	}
	if r := crud.Str(rec["rhythm"]); r != "" && !validOption(rhythmOptions, r) {
		return crud.Invalid("Rhythmus %q gibt es nicht", r)
	}
	if lt := intPtr(rec["lifetime_months"]); lt != nil && *lt < 0 {
		return crud.Invalid("Lebensdauer: 0 oder mehr Monate")
	} else if lt == nil {
		rec["lifetime_months"] = nil
	}
	if old != nil {
		// geänderte Angaben: beim nächsten Erneuern alles prüfen (Zeiger zurücksetzen)
		changed := false
		for _, k := range []string{"date_from", "date_to", "building_id", "object_id", "contract_id", "partner_id", "process_id"} {
			a, b := crud.Str(old[k]), crud.Str(rec[k])
			if strings.HasPrefix(k, "date_") {
				a, b = dateStr(old[k]), dateStr(rec[k])
			}
			if a != b {
				changed = true
			}
		}
		if changed {
			rec["pointer"] = nil
			if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET pointer = NULL WHERE company_code = ? AND view_id = ?", rec["company_code"], rec["view_id"]); err != nil {
				return err
			}
		}
	}
	if old != nil && crud.Str(old["key_by"]) != crud.Str(rec["key_by"]) {
		if r, err := m.one(ctx, "SELECT COUNT(*) FROM export__dataset WHERE company_code = ? AND view_id = ? AND status <> ?", rec["company_code"], rec["view_id"], dsDeleted); err != nil {
			return err
		} else if r != nil && toInt(r[0]) > 0 {
			return crud.Invalid("Die Sicht hat Datasets – den Schlüssel nicht mehr ändern (neue Sicht anlegen)")
		}
	}
	for _, k := range []string{"date_from", "date_to"} {
		d, err := dateOrNil(rec[k])
		if err != nil {
			return err
		}
		rec[k] = d
	}
	if f, t := crud.Str(rec["date_from"]), crud.Str(rec["date_to"]); f != "" && t != "" && t < f {
		return crud.Invalid("Zeitraum: bis liegt vor von")
	}
	cc := crud.Str(rec["company_code"])
	vt, err := m.one(ctx, "SELECT params, COALESCE(key_by, '') FROM export__view_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, rec["view_type"], true)
	if err != nil {
		return err
	}
	if vt == nil {
		return crud.Invalid("Sichtart %s gibt es im Buchungskreis %s nicht", crud.Str(rec["view_type"]), cc)
	}
	return missingParams(effectiveParams(crud.Str(vt[0]), firstNonEmpty(crud.Str(rec["key_by"]), crud.Str(vt[1]), keySicht)), rec)
}

// missingParams: Pflichtangaben der Sichtart an der Sicht.
func missingParams(params string, rec map[string]any) error {
	var miss []string
	for _, p := range splitCodes(params) {
		f := paramFields[p]
		if p == "period" {
			if crud.Str(rec["date_from"]) == "" || crud.Str(rec["date_to"]) == "" {
				miss = append(miss, "Zeitraum von–bis")
			}
			continue
		}
		if f != "" && crud.Str(rec[f]) == "" {
			miss = append(miss, paramLabel(p))
		}
	}
	if len(miss) > 0 {
		return crud.Invalid("Die Sichtart braucht: %s", strings.Join(miss, ", "))
	}
	return nil
}

func paramLabel(p string) string {
	for _, o := range paramOptions {
		if o.Value == p {
			return o.Label
		}
	}
	return p
}

// --- Zusammenstellen ------------------------------------------------------------------

type viewRow struct {
	cc, id, name, typ string
	params            map[string]string // Feld der Sicht → Wert
}

type typeRow struct {
	code, name, params, folder, file, sortBy string
}

type ruleRow struct {
	no                                                 int
	name, docType, category, account, refParam, refObj string
	folder                                             string
	byPartner, inPeriod, exclude                       bool
}

// doc: Dokument aus dem DMS (DmsDocument.list).
type doc struct {
	ID, Name, Title, FileName, DocType, Category, Account, Partner, Date, Filed, SHA string
	Size                                                                             int64
	rule                                                                             int
	folderPattern                                                                    string
	ruleName                                                                         string
	partnerName                                                                      string
}

func docOf(r map[string]any) doc {
	return doc{ID: crud.Str(r["id"]), Name: crud.Str(r["name"]), Title: crud.Str(r["title"]), FileName: crud.Str(r["file_name"]),
		DocType: crud.Str(r["doc_type"]), Category: crud.Str(r["doc_category"]), Account: crud.Str(r["file_account"]),
		Partner: crud.Str(r["partner_id"]), Date: dateStr(r["doc_date"]), Filed: crud.Str(r["filed_at"]), SHA: crud.Str(r["sha256"]),
		Size: int64(toInt(r["size"]))}
}

func (m *Module) loadView(ctx context.Context, cc, id string) (*viewRow, *typeRow, []ruleRow, error) {
	r, err := m.one(ctx, `SELECT name, view_type, date_from, date_to, building_id, object_id, contract_id, partner_id, process_id
		FROM export__view WHERE company_code = ? AND view_id = ?`, cc, id)
	if err != nil {
		return nil, nil, nil, err
	}
	if r == nil {
		return nil, nil, nil, fmt.Errorf("%w: Sicht %s|%s", sdk.ErrNotFound, cc, id)
	}
	v := &viewRow{cc: cc, id: id, name: crud.Str(r[0]), typ: crud.Str(r[1]), params: map[string]string{
		"date_from": dateStr(r[2]), "date_to": dateStr(r[3]), "building_id": crud.Str(r[4]), "object_id": crud.Str(r[5]),
		"contract_id": crud.Str(r[6]), "partner_id": crud.Str(r[7]), "process_id": crud.Str(r[8])}}
	t, err := m.one(ctx, `SELECT name, COALESCE(params, ''), COALESCE(folder_pattern, ''), file_pattern, sort_by FROM export__view_type
		WHERE company_code = ? AND code = ?`, cc, v.typ)
	if err != nil {
		return nil, nil, nil, err
	}
	if t == nil {
		return nil, nil, nil, crud.Invalid("Sichtart %s gibt es nicht", v.typ)
	}
	typ := &typeRow{code: v.typ, name: crud.Str(t[0]), params: crud.Str(t[1]), folder: crud.Str(t[2]), file: crud.Str(t[3]), sortBy: crud.Str(t[4])}
	res, err := m.db.Query(ctx, `SELECT rule_no, COALESCE(name, ''), COALESCE(doc_type, ''), COALESCE(doc_category, ''), COALESCE(file_account, ''),
		COALESCE(ref_param, ''), COALESCE(ref_object, ''), COALESCE(folder, ''), by_partner, in_period, exclude
		FROM export__rule WHERE company_code = ? AND view_type = ? AND is_active = ? ORDER BY rule_no`, cc, v.typ, true)
	if err != nil {
		return nil, nil, nil, err
	}
	var rules []ruleRow
	for _, x := range res.Rows {
		rules = append(rules, ruleRow{no: toInt(x[0]), name: crud.Str(x[1]), docType: crud.Str(x[2]), category: crud.Str(x[3]), account: crud.Str(x[4]),
			refParam: crud.Str(x[5]), refObj: crud.Str(x[6]), folder: crud.Str(x[7]), byPartner: crud.AsBool(x[8]), inPeriod: crud.AsBool(x[9]), exclude: crud.AsBool(x[10])})
	}
	if len(rules) == 0 {
		return nil, nil, nil, crud.Invalid("Sichtart %s hat keine aktiven Regeln", v.typ)
	}
	return v, typ, rules, nil
}

// select: Dokumente einer Regel über DmsDocument.list (Rechte des Benutzers).
// skip: die Sicht hat die Angabe der Regel nicht.
func (m *Module) selectRule(ctx context.Context, cc string, params map[string]string, r ruleRow, extra map[string]any) (docs []doc, skip string, err error) {
	base := map[string]any{"company_code": cc, "status": "FILED"}
	for k, x := range extra {
		base[k] = x
	}
	if r.docType != "" {
		base["doc_type"] = r.docType
	}
	if r.category != "" {
		base["doc_category"] = r.category
	}
	if r.byPartner {
		p := params["partner_id"]
		if p == "" {
			return nil, "Partner fehlt", nil
		}
		base["partner_id"] = p
	}
	if r.inPeriod {
		if f := params["date_from"]; f != "" {
			base["date_from"] = f
		}
		if t := params["date_to"]; t != "" {
			base["date_to"] = t
		}
	}
	queries := []map[string]any{base}
	if r.refParam != "" {
		val := params[paramFields[r.refParam]]
		if val == "" {
			return nil, paramLabel(r.refParam) + " fehlt", nil
		}
		// Schlüssel im DMS mit Buchungskreis (2000|MV-2026-0001) oder ohne (Partner-Nr.)
		queries = nil
		for _, key := range []string{cc + "|" + val, val} {
			q := map[string]any{}
			for k, x := range base {
				q[k] = x
			}
			q["ref"] = r.refObj + "|" + key
			queries = append(queries, q)
		}
	}
	seen := map[string]bool{}
	for _, q := range queries {
		resp, err := m.services.Call(ctx, "DmsDocument", "list", map[string]any{"query": q})
		if err != nil {
			return nil, "", fmt.Errorf("Dokumente lesen (Regel %d): %w", r.no, err)
		}
		for _, it := range listItems(resp.Payload) {
			d := docOf(it)
			if seen[d.ID] || (r.account != "" && !accountMatch(d.Account, r.account)) {
				continue
			}
			seen[d.ID] = true
			docs = append(docs, d)
		}
	}
	return docs, "", nil
}

// accountMatch: Ablage gleich oder Unterablage (72 → 72, 72.1, 72.1.3).
func accountMatch(account, prefix string) bool {
	return account == prefix || strings.HasPrefix(account, prefix+".")
}

func listItems(p any) []map[string]any {
	var out []map[string]any
	var m map[string]any
	switch x := p.(type) {
	case map[string]any:
		m = x
	default:
		_ = sdk.Decode(p, &m)
	}
	items, _ := m["items"].([]any)
	for _, it := range items {
		if r, ok := it.(map[string]any); ok {
			out = append(out, r)
		}
	}
	return out
}

// composeAction: ExportView.compose {id, data: {key_date, refresh_full}} – neue Schlüssel
// suchen und alle aktiven Datasets der Sicht erneuern (neuer Stand nur bei Änderungen).
func (m *Module) composeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Sicht")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, runObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	o := refreshOpts{all: true, full: crud.AsBool(in.Data["refresh_full"])}
	if d, err := dateOrNil(in.Data["key_date"]); err != nil {
		return sdk.Response{}, err
	} else if d != nil {
		o.keyDate = d.(string)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, err := m.refreshView(ctx, cc, id, o)
	if err != nil {
		return sdk.Response{}, err
	}
	var items int
	if r.lastRun != "" {
		if x, err := m.one(ctx, "SELECT item_count FROM export__run WHERE company_code = ? AND run_id = ?", cc, r.lastRun); err == nil && x != nil {
			items = toInt(x[0])
		}
	}
	return sdk.Response{Payload: map[string]any{"run_id": r.lastRun, "items": items, "created": r.created, "added": r.added, "marked": r.marked,
		"message": r.message("Sicht " + id)}}, nil
}

func (m *Module) insertItem(ctx context.Context, cc, runID string, no int, d doc, folder, file string, added bool, reason string) error {
	_, err := m.db.Exec(ctx, `INSERT INTO export__item (company_code, run_id, item_no, document_id, folder, file_name, title, doc_type, doc_category,
		partner_id, partner_name, file_account, sha256, reason, doc_date, rule_no, size, excluded, added) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cc, runID, no, d.ID, nilIfEmpty(folder), file, nilIfEmpty(d.Title), nilIfEmpty(d.DocType), nilIfEmpty(d.Category), nilIfEmpty(d.Partner),
		nilIfEmpty(d.partnerName), nilIfEmpty(d.Account), nilIfEmpty(d.SHA), nilIfEmpty(reason), nilIfEmpty(d.Date), nilIfZero(d.rule), d.Size, false, added)
	return err
}

func nilIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func (m *Module) nextRunID(ctx context.Context, cc, year string) (string, error) {
	stem := runPrefix + "-" + year + "-"
	res, err := m.db.Query(ctx, "SELECT run_id FROM export__run WHERE company_code = ? AND run_id LIKE ?", cc, stem+"%")
	if err != nil {
		return "", err
	}
	n := 0
	for _, r := range res.Rows {
		if k, err := strconv.Atoi(strings.TrimPrefix(crud.Str(r[0]), stem)); err == nil && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s%04d", stem, n+1), nil
}

// sortDocs: Reihenfolge der Sichtart (laufende Nummer).
func sortDocs(docs []doc, by string, nm *namer) {
	key := func(d doc) []string {
		switch by {
		case "TYPE":
			return []string{nm.docType(d.DocType), d.Date, d.Title}
		case "PARTNER":
			return []string{nm.partner(d.Partner), d.Date, d.Title}
		case "RULE":
			return []string{fmt.Sprintf("%06d", d.rule), d.Date, d.Title}
		case "FILED":
			return []string{d.Filed, d.Title}
		}
		return []string{d.Date, d.Filed, d.Title}
	}
	sort.SliceStable(docs, func(i, j int) bool {
		a, b := key(docs[i]), key(docs[j])
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
}
