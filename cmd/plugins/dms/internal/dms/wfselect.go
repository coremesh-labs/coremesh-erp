package dms

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Auswahl (SELECT, auch die erlaubten Antworten einer KI-Anfrage): Quelle
// OBJECT (Liste eines Objects mit Filtern – Zuordnungen „Filter“, je Kategorie
// möglich), LIST (Zuordnungen „Wert“), TREE (Ablage Ebene für Ebene) oder
// CATEGORY (kaufmännische Objekte der Dokumentkategorie, beim Partner nur seine).
// Kein Wert ist vorgewählt – außer einem Vorschlag oder dem bisherigen Wert.

// choice ist ein Auswahlwert.
type choice struct{ Value, Label string }

// choices: Auswahlwerte eines Schritts (ohne Baum; der hat treeChoices).
func (m *Module) choices(ctx context.Context, d *docRow, s *stepDef, search string) ([]choice, error) {
	switch s.SourceKind {
	case srcCategory:
		return m.objectChoices(ctx, d, search)
	case srcList:
		var out []choice
		q := strings.ToLower(strings.TrimSpace(search))
		for _, p := range s.mappings(mapValue, m.categoryOf(ctx, d)) {
			c := choice{p.Field, firstOf(strings.TrimSpace(p.Value), p.Field)}
			if q == "" || strings.Contains(strings.ToLower(c.Label), q) || strings.Contains(strings.ToLower(c.Value), q) {
				out = append(out, c)
			}
		}
		return out, nil
	case srcTree:
		cs, _, err := m.treeChoices(ctx, d, "", search)
		return cs, err
	case srcObject:
	default:
		return nil, nil
	}
	if s.SourceObject == "" {
		return nil, crud.Invalid("Schritt %s: Object der Auswahl fehlt", s.Step)
	}
	query := m.selectFilters(ctx, d, s)
	if q := strings.TrimSpace(search); q != "" {
		query["q"] = q
	}
	resp, err := m.services.Call(ctx, s.SourceObject, s.SourceAction, map[string]any{"query": query})
	if err != nil {
		return nil, crud.Invalid("Auswahl aus %s.%s: %v", s.SourceObject, s.SourceAction, err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &list); err != nil {
		return nil, err
	}
	lfs := splitList(s.LabelFields)
	var out []choice
	for _, it := range list.Items {
		if !matchesFilters(it, query) {
			continue
		}
		v := itemValue(it, s.ValueField)
		if v == "" {
			continue
		}
		lbl, _ := it["_labels"].(map[string]any)
		var parts []string
		for _, f := range lfs {
			t := crud.Str(lbl[f])
			if t == "" {
				t = crud.Str(it[f])
			}
			if t != "" && t != v && !contains(parts, t) {
				parts = append(parts, t)
			}
		}
		label := v
		if len(parts) > 0 {
			label = v + " – " + strings.Join(parts, " · ")
		}
		out = append(out, choice{v, label})
	}
	if len(out) > 500 {
		out = out[:500]
	}
	return out, nil
}

// itemValue: Wert eines Eintrags – Feld oder Vorlage mit Feldern („{condition_type}|{object_id}“).
func itemValue(it map[string]any, field string) string {
	if !strings.Contains(field, "{") {
		return crud.Str(it[field])
	}
	// leere Felder am Ende samt Trennzeichen entfallen („RT|“ → „RT“)
	return strings.TrimRight(itemFieldRe.ReplaceAllStringFunc(field, func(ph string) string {
		return crud.Str(it[strings.Trim(ph, "{}")])
	}), "|;,- ")
}

// selectFilters: Filter der Auswahl (Platzhalter ersetzt; je Kategorie des Dokuments).
func (m *Module) selectFilters(ctx context.Context, d *docRow, s *stepDef) map[string]any {
	query := map[string]any{}
	for _, p := range s.mappings(mapFilter, m.categoryOf(ctx, d)) {
		if p.Field != "" {
			query[p.Field] = m.expand(ctx, d, strings.TrimSpace(p.Value))
		}
	}
	return query
}

// matchesFilters: Eintrag passt zu den Filtern, soweit er die Felder hat – falls die
// Quelle einen Filter nicht kennt (z. B. is_template).
func matchesFilters(it, query map[string]any) bool {
	for k, v := range query {
		if k == "q" {
			continue
		}
		x, has := it[k]
		if !has {
			continue
		}
		got := crud.Str(x)
		if b, ok := x.(bool); ok {
			got = fmt.Sprint(b)
		}
		// Komma-Liste = einer der Werte (seit 0.17.1, z. B. status OPEN,DRAFT,POSTED)
		ok := false
		for _, want := range strings.Split(crud.Str(v), ",") {
			if strings.EqualFold(strings.TrimSpace(got), strings.TrimSpace(want)) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// filterText: Hinweis auf Filter mit Beschreibung (z. B. „nur Partner mit Kreditor-Rolle“).
func (m *Module) filterText(ctx context.Context, d *docRow, s *stepDef) string {
	var out []string
	for _, p := range s.mappings(mapFilter, m.categoryOf(ctx, d)) {
		if p.Desc != "" {
			out = append(out, p.Desc)
		}
	}
	return strings.Join(out, ", ")
}

// --- Ablage Ebene für Ebene ---------------------------------------------------------------

// treeUp: Präfix des Werts „zurück zur Ebene …“.
const treeUp = "^"

// companyLevel ist eine aktive, nicht gesperrte Ablage des Buchungskreises.
type companyLevel struct {
	Number, Name, Parent string
	Leaf                 bool
}

func (m *Module) companyLevels(ctx context.Context, cc string) (map[string]*companyLevel, []*companyLevel, error) {
	res, err := m.db.Query(ctx, `SELECT a.number, a.name, a.parent FROM dms__account a
		JOIN dms__company c ON c.plan = a.plan AND c.company_code = ?
		JOIN dms__account_company x ON x.company_code = c.company_code AND x.number = a.number AND x.is_blocked = ?
		WHERE a.is_active = ? ORDER BY a.sort_key`, cc, false, true)
	if err != nil {
		return nil, nil, err
	}
	by := map[string]*companyLevel{}
	var all []*companyLevel
	for _, r := range res.Rows {
		l := &companyLevel{Number: crud.Str(r[0]), Name: crud.Str(r[1]), Parent: crud.Str(r[2]), Leaf: true}
		by[l.Number] = l
		all = append(all, l)
	}
	for _, l := range all {
		if p := by[l.Parent]; p != nil {
			p.Leaf = false
		}
	}
	return by, all, nil
}

// treeChoices: Auswahl auf der Ebene des gewählten Werts – Gruppe gewählt:
// ihre Unterablagen; Ablage der untersten Ebene: ihre Geschwister (sie
// bleibt gewählt); „^Nummer“: zurück auf diese Ebene. Mit Suchtext: alle
// Ablagen der untersten Ebene, deren Nummer oder Name passt. Liefert die
// Auswahl und den Pfad der Ebene für den Hinweis.
func (m *Module) treeChoices(ctx context.Context, d *docRow, value, search string) ([]choice, string, error) {
	by, all, err := m.companyLevels(ctx, d.CC)
	if err != nil {
		return nil, "", err
	}
	if q := strings.ToLower(strings.TrimSpace(search)); q != "" {
		var out []choice
		for _, l := range all {
			if l.Leaf && (strings.HasPrefix(l.Number, q) || strings.Contains(strings.ToLower(l.Name), q)) {
				out = append(out, choice{l.Number, l.Number + " " + l.Name})
			}
		}
		return out, fmt.Sprintf("Suche „%s“: %d Ablagen", search, len(out)), nil
	}
	level := ""
	switch v := strings.TrimSpace(value); {
	case strings.HasPrefix(v, treeUp):
		level = strings.TrimPrefix(v, treeUp)
	case by[v] != nil && !by[v].Leaf:
		level = v
	case by[v] != nil:
		level = by[v].Parent
	}
	if level != "" && by[level] == nil {
		level = ""
	}
	var out []choice
	if level != "" {
		up := by[level].Parent
		upLabel := "↑ zurück zur obersten Ebene"
		if p := by[up]; p != nil {
			upLabel = "↑ zurück zu " + p.Number + " " + p.Name
		}
		// die geöffnete Ebene steht gewählt oben – so ist „↑ zurück“ eine Änderung der Auswahl
		out = append(out, choice{treeUp + level, "▾ " + level + " " + by[level].Name + " – bitte wählen"}, choice{treeUp + up, upLabel})
	}
	for _, l := range all {
		if l.Parent != level && !(level == "" && by[l.Parent] == nil) {
			continue // nicht auf dieser Ebene (oberste: auch Ablagen ohne aktive übergeordnete)
		}
		if l.Leaf {
			out = append(out, choice{l.Number, l.Number + " " + l.Name})
		} else {
			out = append(out, choice{l.Number, "▸ " + l.Number + " " + l.Name})
		}
	}
	var path []string
	for p := level; p != "" && by[p] != nil; p = by[p].Parent {
		path = append([]string{by[p].Number + " " + by[p].Name}, path...)
	}
	if len(path) == 0 {
		return out, "oberste Ebene", nil
	}
	return out, "Ebene: " + strings.Join(path, " › "), nil
}

// --- kaufmännische Objekte der Dokumentkategorie ------------------------------------------

// objectChoices: kaufmännische Objekte der Kategorie – beim gewählten Partner nur
// seine, mit den Filtern der Kategorie und der Suche (Wert Object:Schlüssel).
func (m *Module) objectChoices(ctx context.Context, d *docRow, search string) ([]choice, error) {
	objs, err := m.docObjects(ctx, d)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, crud.Invalid("Für die Dokumentkategorie %s sind keine kaufmännischen Objekte eingerichtet (Dokumentkategorien → Kaufmännische Objekte)", m.categoryOf(ctx, d))
	}
	var out []choice
	var failed []string
	for _, o := range objs {
		query := map[string]any{"company_code": d.CC}
		for _, f := range strings.Split(o.Filters, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(f), "="); ok {
				query[strings.TrimSpace(k)] = m.expand(ctx, d, strings.TrimSpace(v))
			}
		}
		if o.PartnerField != "" && d.Partner != "" {
			query[o.PartnerField] = d.Partner
		}
		if q := strings.TrimSpace(search); q != "" {
			query["q"] = q
		}
		resp, err := m.services.Call(ctx, o.Object, o.Action, map[string]any{"query": query})
		if err != nil { // seit 0.16.1: übrige Objects bleiben wählbar
			failed = append(failed, fmt.Sprintf("Auswahl aus %s: %v", o.Label, err))
			m.log.WarnContext(ctx, "Auswahl: Object nicht lesbar", "object", o.Object, "err", err.Error())
			continue
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := sdk.Decode(resp.Payload, &list); err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			id := crud.Str(it["id"])
			// Nach dem Partner filtert die Quelle (seit 0.17.1 ohne zweite Prüfung: Verträge
			// auch mit dem Partner als weiterem Vertragspartner, z. B. Mitmieter).
			if id == "" {
				continue
			}
			label := o.Label + ": " + shortKey(id, d.CC)
			var parts []string
			for _, f := range []string{o.TitleField, "designation", o.DateField} {
				if v := crud.Str(it[f]); f != "" && v != "" && !contains(parts, v) {
					parts = append(parts, v)
				}
			}
			if len(parts) > 0 {
				label += " – " + strings.Join(parts, " · ")
			}
			out = append(out, choice{o.Object + ":" + id, label})
		}
	}
	if len(failed) == len(objs) {
		return nil, crud.Invalid("%s", strings.Join(failed, "; "))
	}
	if len(out) > 500 {
		out = out[:500]
	}
	return out, nil
}

// createLinks: „… anlegen“ im neuen Fenster – Objects der Kategorie mit can_create
// (Auswahl der kaufmännischen Objekte) und die Zuordnungen „Link“.
func (m *Module) createLinks(ctx context.Context, d *docRow, s *stepDef) []metamodel.FormLink {
	if s.Kind != kSelect {
		return nil
	}
	var out []metamodel.FormLink
	if s.SourceKind == srcCategory {
		objs, _ := m.docObjects(ctx, d)
		for _, o := range objs {
			if o.CanCreate {
				out = append(out, metamodel.FormLink{Label: o.Label + " anlegen", URL: "/new/" + o.Object})
			}
		}
	}
	for _, p := range s.mappings(mapLink, m.categoryOf(ctx, d)) {
		if p.Field != "" {
			out = append(out, metamodel.FormLink{Label: firstOf(strings.TrimSpace(p.Value), p.Field+" anlegen"), URL: "/new/" + p.Field})
		}
	}
	return out
}

// openLinks: „… öffnen“ – Bezüge des Objects open_object im neuen Fenster;
// „Object={Platzhalter}“ öffnet den Datensatz mit diesem Schlüssel (seit 0.14.0,
// z. B. DmsDocument={var.duplicate}).
func (m *Module) openLinks(ctx context.Context, d *docRow, s *stepDef) []metamodel.FormLink {
	if s.OpenObject == "" {
		return nil
	}
	if object, spec, ok := strings.Cut(s.OpenObject, "="); ok {
		object = strings.TrimSpace(object)
		key := strings.TrimSpace(m.expand(ctx, d, strings.TrimSpace(spec)))
		if object == "" || key == "" || strings.Contains(key, "{") {
			return nil
		}
		return []metamodel.FormLink{{Label: firstOf(s.OpenLabel, object) + " öffnen", URL: "/open/" + object + "/" + strings.ReplaceAll(key, "|", "%7C")}}
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	var out []metamodel.FormLink
	for _, r := range refs {
		if r.Object == s.OpenObject {
			out = append(out, metamodel.FormLink{Label: firstOf(s.OpenLabel, s.OpenObject) + " " + shortKey(r.Key, d.CC) + " öffnen",
				URL: "/open/" + s.OpenObject + "/" + strings.ReplaceAll(r.Key, "|", "%7C")})
		}
	}
	return out
}

// sortedKeys: Schlüssel einer Menge, sortiert.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
