package process

import (
	"context"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/textservice"
)

// Druck des Vorgangs („Drucken …“): Vorlage mit der Verwendung PROCESS_SHEET
// (Vorlagen → „Beispiele anlegen …“ legt sie an), Datenformat process.sheet/1.
// Wahlweise mit allen Teilvorgängen (alle Ebenen, als flache Liste mit level)
// und mit dem Verlauf.

const (
	usageSheet  = "PROCESS_SHEET"
	sheetFormat = "process.sheet/1"
)

// refFields: Bezüge des Vorgangs im Druck (Beschriftung aus den Feldern).
var refFields = []string{"partner_id", "contract_id", "object_id", "entity_id", "asset_id"}

// sheetFormState: Auswahl der Vorlage, Vorschlag die erste.
func (m *Module) sheetFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" || req.Action != "print" {
		return st, nil
	}
	opts := reportapi.Templates(ctx, m.services, usageSheet)
	fs := metamodel.FieldState{Options: opts}
	if len(opts) > 0 && req.Values["print_template"] == "" {
		fs.Value = &opts[0].Value
	}
	st.Fields["print_template"] = fs
	if len(opts) == 0 {
		st.Message = "Keine Vorlage mit der Verwendung " + usageSheet + " (Vorlagen → „Beispiele anlegen …“ legt PROCESS_SHEET an)."
	}
	return st, nil
}

// printAction: Process.print {id, data: {print_template, print_children, print_history}}.
func (m *Module) printAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	tmpl := strings.TrimSpace(crud.Str(in.Data["print_template"]))
	if tmpl == "" {
		if opts := reportapi.Templates(ctx, m.services, usageSheet); len(opts) > 0 {
			tmpl = opts[0].Value
		}
	}
	if tmpl == "" {
		return sdk.Response{}, crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → „Beispiele anlegen …“ legt PROCESS_SHEET an)", usageSheet)
	}
	if !strings.Contains(tmpl, "|") {
		tmpl += "|de"
	}
	data, err := m.sheetData(ctx, cc, id, crud.AsBool(in.Data["print_children"]), crud.AsBool(in.Data["print_history"]))
	if err != nil {
		return sdk.Response{}, err
	}
	out, err := reportapi.Render(ctx, m.services, cc, tmpl, "Vorgang "+id, data)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

// sheetData: Daten process.sheet/1 – der Vorgang und (children) seine Teilvorgänge
// in der Reihenfolge des Baums.
func (m *Module) sheetData(ctx context.Context, cc, id string, children, history bool) (map[string]any, error) {
	if row, err := m.one(ctx, "SELECT process_id FROM process__process WHERE company_code = ? AND process_id = ?", cc, id); err != nil {
		return nil, err
	} else if row == nil {
		return nil, m.mustExist(ctx, cc, id)
	}
	var list []any
	seen := map[string]bool{}
	var walk func(id string, level int) error
	walk = func(id string, level int) error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		p, err := m.sheetProcess(ctx, cc, id, level, history)
		if err != nil {
			return err
		}
		list = append(list, p)
		if !children {
			return nil
		}
		res, err := m.db.Query(ctx, "SELECT process_id FROM process__process WHERE company_code = ? AND parent_id = ? ORDER BY process_id", cc, id)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			if err := walk(crud.Str(r[0]), level+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(id, 0); err != nil {
		return nil, err
	}
	return map[string]any{"format": sheetFormat, "title": "Vorgang " + id, "printed_on": m.today(),
		"company": reportapi.Company(ctx, m.services, cc), "with_children": children, "with_history": history, "processes": list}, nil
}

func (m *Module) sheetProcess(ctx context.Context, cc, id string, level int, history bool) (map[string]any, error) {
	// get prüft das Leserecht und setzt die Ampel (signal)
	resp, err := m.set.Entity(processObject).Get(ctx, map[string]any{"id": cc + "|" + id})
	if err != nil {
		return nil, err
	}
	var rec crud.Record
	if err := sdk.Decode(resp.Payload, &rec); err != nil {
		return nil, err
	}
	str := func(k string) string { return strings.TrimSpace(crud.Str(rec[k])) }
	p := map[string]any{"level": level, "id": id, "short_name": str("short_name"), "subject": str("subject"),
		"type": str("process_type"), "type_name": m.name(ctx, "type", cc, str("process_type")),
		"status": str("status"), "status_name": m.name(ctx, "status", cc, str("status")),
		"priority_name": codeName(m, ctx, "priority", cc, str("priority")), "resolution_name": codeName(m, ctx, "resolution", cc, str("resolution")),
		"assignee": m.userName(ctx, str("assignee")), "deputy": m.userName(ctx, str("deputy")), "parent_id": str("parent_id"),
		"waiting_for": str("waiting_for"), "description": str("description"), "next_step": str("next_step"), "signal": str("signal")}
	for _, k := range []string{"opened_on", "event_date", "ack_due", "acknowledged_on", "answer_due", "follow_up", "next_due", "closed_on"} {
		p[k] = dateStr(rec[k])
	}
	var refs []any
	for _, k := range refFields {
		v := str(k)
		if v == "" {
			continue
		}
		value := v
		if k == "partner_id" {
			if n := m.partnerName(ctx, v); n != "" {
				value = n + " (" + v + ")"
			}
		}
		refs = append(refs, map[string]any{"key": k, "label": m.fieldShort(k), "value": value})
	}
	p["refs"] = refs

	var steps []any
	res, err := m.db.Query(ctx, "SELECT step_no, name, status, due_date, done_on, role_hint, note FROM process__step WHERE company_code = ? AND process_id = ? ORDER BY step_no", cc, id)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		steps = append(steps, map[string]any{"no": toInt(r[0]), "name": crud.Str(r[1]), "status": crud.Str(r[2]), "status_name": optionLabel(stepStates, crud.Str(r[2])),
			"due_date": dateStr(r[3]), "done_on": dateStr(r[4]), "role_hint": crud.Str(r[5]), "note": crud.Str(r[6])})
	}
	p["steps"] = steps

	var partners []any
	res, err = m.db.Query(ctx, "SELECT role_code, partner_id, note FROM process__partner WHERE company_code = ? AND process_id = ? ORDER BY role_code, partner_id", cc, id)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		pid := crud.Str(r[1])
		partners = append(partners, map[string]any{"role": m.name(ctx, "role", cc, crud.Str(r[0])), "id": pid,
			"name": firstNonEmpty(m.partnerName(ctx, pid), pid), "note": crud.Str(r[2])})
	}
	p["partners"] = partners
	p["texts"] = m.sheetTexts(ctx, cc, id)

	if history {
		var entries []any
		res, err = m.db.Query(ctx, "SELECT entry_at, kind, text, user_id FROM process__entry WHERE company_code = ? AND process_id = ? ORDER BY entry_no", cc, id)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			entries = append(entries, map[string]any{"at": crud.Str(r[0]), "date": dateStr(r[0]), "kind": m.name(ctx, "entry_kind", cc, crud.Str(r[1])),
				"text": crud.Str(r[2]), "user": m.userName(ctx, crud.Str(r[3]))})
		}
		p["entries"] = entries
	}
	return p, nil
}

// sheetTexts: Texte des Vorgangs (Markdown) in der Reihenfolge der Kategorien,
// je Kategorie und Position die deutsche Fassung, sonst die erste.
func (m *Module) sheetTexts(ctx context.Context, cc, id string) []any {
	l, err := textservice.New(m.services).List(ctx, textservice.ListRequest{EntityType: processObject, EntityID: cc + "|" + id, EffectiveDate: m.today()})
	if err != nil {
		return nil
	}
	type key struct {
		cat string
		pos int
	}
	chosen := map[key]textservice.Text{}
	var order []key
	for _, t := range l.Items {
		k := key{t.Category, t.Position}
		old, ok := chosen[k]
		if !ok {
			order = append(order, k)
		}
		if !ok || (t.Language == "de" && old.Language != "de") {
			chosen[k] = t
		}
	}
	rank, names := map[string]int{}, map[string]string{}
	for i, c := range l.Categories {
		rank[c.Code], names[c.Code] = i+1, c.Name
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		ra, rb := rank[a.cat], rank[b.cat]
		if ra != rb {
			return ra != 0 && (rb == 0 || ra < rb)
		}
		if a.cat != b.cat {
			return a.cat < b.cat
		}
		return a.pos < b.pos
	})
	var out []any
	for _, k := range order {
		t := chosen[k]
		if strings.TrimSpace(t.Content) == "" {
			continue
		}
		out = append(out, map[string]any{"category": t.Category, "category_name": firstNonEmpty(t.CategoryName, names[t.Category], t.Category),
			"position": t.Position, "content": strings.TrimSpace(t.Content), "changed_at": dateStr(t.ChangedAt)})
	}
	return out
}

// codeName: Name eines Katalogeintrags; leer bei leerem Code.
func codeName(m *Module, ctx context.Context, table, cc, code string) string {
	if code == "" {
		return ""
	}
	return m.name(ctx, table, cc, code)
}

// userName: Anzeigename des Benutzers, sonst die Kennung.
func (m *Module) userName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "User", "get", map[string]any{"id": id})
	if err != nil {
		return id
	}
	var u struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}
	if sdk.Decode(resp.Payload, &u) != nil {
		return id
	}
	return firstNonEmpty(u.DisplayName, u.Username, id)
}

// fieldShort: Kurzbeschriftung eines Felds des Vorgangs.
func (m *Module) fieldShort(key string) string {
	for _, f := range m.set.Entity(processObject).Fields {
		if f.Key == key {
			return firstNonEmpty(f.Short, f.Label)
		}
	}
	return key
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
