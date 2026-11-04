package techobj

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Lagebaum ------------------------------------------------------------------------

// location: Knoten im Lagebaum – „nichts ist hässlicher, als den Stromzähler zu
// suchen“: Wegbeschreibung, Zugang (Schlüssel, Ansprechpartner) und Fotos im DMS.
func (m *Module) location() *crud.Entity {
	return &crud.Entity{
		Object: "TechLocation", Title: "Lagen", Icon: "icon-map-pin", Table: "techobj__location", Section: "Bestand",
		Keys: []string{"company_code", "location_id"}, Order: "company_code, location_id", TitleField: "designation",
		Filters: []string{"company_code", "parent_id", "level", "status"},
		Search:  []string{"location_id", "designation", "directions", "access_info"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"parent_id", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "location_id", Label: "Lage (ID; leer = nächste freie, z. B. LG0001)", Short: "Lage", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung (z. B. Keller Hausanschlussraum)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "parent_id", Label: "Liegt in (übergeordnete Lage)", Short: "Liegt in", Type: tText, Listable: true, Lookup: locationLookup},
			{Key: "level", Label: "Ebene", Type: tText, Listable: true, Lookup: catalogLookup("TechLocationLevel")},
			{Key: "path", Label: "Pfad", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "directions", Label: "Wegbeschreibung (z. B. Kellertür rechts, zweite Tür links)", Short: "Weg", Type: tArea, Wide: true, Group: "Zugang"},
			{Key: "access_info", Label: "Zugang (Schlüssel, Schließung, Zeiten)", Short: "Zugang", Type: tText, Group: "Zugang"},
			{Key: "contact_id", Label: "Ansprechpartner (z. B. Hausmeister)", Short: "Ansprechpartner", Type: tText, Lookup: partnerLookup, Group: "Zugang"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechObjectStatus"), Group: "Gültigkeit"},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Group: "Gültigkeit"},
			{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "unterlagen", Title: "Darin", Relation: &metamodel.Relation{Object: "TechLocation", ForeignKey: "parent_id",
				Match: match("parent_id", "location_id"), Columns: []string{"location_id", "designation", "level"}}},
			{Key: "messpunkte", Title: "Messpunkte hier", Relation: &metamodel.Relation{Object: "MeterPointLocation", ForeignKey: "location_id",
				Match: match("location_id", "location_id"), Columns: []string{"point_id", "position", "valid_from", "valid_to"}}},
			{Key: "objekte", Title: "Technische Objekte hier", Relation: &metamodel.Relation{Object: "TechObject", ForeignKey: "location_id",
				Match: match("location_id", "location_id"), Columns: []string{"equipment_id", "designation", "object_class", "position"}}},
			{Key: "verknuepfungen", Title: "Verknüpfungen", Relation: &metamodel.Relation{Object: "TechLink", ForeignKey: "source_id",
				Match: match("source_id", "location_id"), Filter: map[string]string{"source_kind": "TechLocation"},
				Columns: []string{"role_code", "object_type", "object_id", "valid_from", "valid_to"}}},
			{Key: "dokumente", Title: "Fotos und Dokumente", Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Actions:     []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "tour", Label: "Ablese-Rundgang", Record: true}, Handle: m.tourAction}},
		Access:      access(),
		CheckRecord: checkWrite("TechLocation"),
		Validate:    m.checkLocation,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			p, err := m.locationPath(ctx, crud.Str(rec["company_code"]), crud.Str(rec["location_id"]))
			if err == nil {
				rec["path"] = p
			}
			return nil
		},
	}
}

func (m *Module) checkLocation(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		if err := m.newID(ctx, rec, "location_id", "techobj__location", cc, "@LOCATION", "Lage"); err != nil {
			return err
		}
	}
	id := crud.Str(rec["location_id"])
	parent := strings.TrimSpace(crud.Str(rec["parent_id"]))
	rec["parent_id"] = nilIfEmpty(parent)
	rec["level"] = nilIfEmpty(trimUpper(rec["level"]))
	if err := m.requireCatalog(ctx, "location_level", "Ebene", cc, rec["level"]); err != nil {
		return err
	}
	if parent != "" {
		if strings.EqualFold(parent, id) {
			return crud.Invalid("Eine Lage kann nicht in sich selbst liegen")
		}
		// Kein Kreis: die übergeordnete Lage darf nicht (mittelbar) in dieser liegen.
		seen := map[string]bool{}
		for cur := parent; cur != ""; {
			p, err := m.one(ctx, "SELECT COALESCE(parent_id, '') || '|' FROM techobj__location WHERE company_code = ? AND location_id = ?", cc, cur)
			if err != nil {
				return err
			}
			if p == "" {
				if cur == parent {
					return crud.Invalid("Lage %s gibt es im Buchungskreis %s nicht", parent, cc)
				}
				break
			}
			next := strings.TrimSuffix(p, "|")
			if strings.EqualFold(next, id) {
				return crud.Invalid("%s liegt selbst in %s – kein Kreis", parent, id)
			}
			if seen[next] {
				break
			}
			seen[next], cur = true, next
		}
	}
	rec["contact_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["contact_id"])))
	if c := crud.Str(rec["contact_id"]); c != "" {
		if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": c}); err != nil {
			return crud.Invalid("Ansprechpartner %s: %v", c, err)
		}
	}
	return m.checkValidity(ctx, rec, true, true)
}

// locationPath: „Gebäude Oelßnerstr. 10 › Keller › Zählerschrank“ (von oben).
func (m *Module) locationPath(ctx context.Context, cc, id string) (string, error) {
	var parts []string
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur] && len(parts) < 20; {
		seen[cur] = true
		res, err := m.db.Query(ctx, "SELECT designation, parent_id FROM techobj__location WHERE company_code = ? AND location_id = ?", cc, cur)
		if err != nil {
			return "", err
		}
		if len(res.Rows) == 0 {
			break
		}
		parts = append([]string{crud.Str(res.Rows[0][0])}, parts...)
		cur = crud.Str(res.Rows[0][1])
	}
	return strings.Join(parts, " › "), nil
}

// locationInfo: Pfad, Wegbeschreibungen und Zugang von oben nach unten –
// was man braucht, um zum Zähler zu kommen.
type locInfo struct{ Path, Directions, Access, Contact string }

func (m *Module) locationInfo(ctx context.Context, cc, id string) (locInfo, error) {
	var chain [][]any
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur] && len(chain) < 20; {
		seen[cur] = true
		res, err := m.db.Query(ctx, `SELECT designation, parent_id, directions, access_info, contact_id FROM techobj__location
			WHERE company_code = ? AND location_id = ?`, cc, cur)
		if err != nil {
			return locInfo{}, err
		}
		if len(res.Rows) == 0 {
			break
		}
		chain = append([][]any{res.Rows[0]}, chain...)
		cur = crud.Str(res.Rows[0][1])
	}
	var out locInfo
	var path, dirs, acc []string
	for _, r := range chain {
		path = append(path, crud.Str(r[0]))
		if d := strings.TrimSpace(crud.Str(r[2])); d != "" {
			dirs = append(dirs, crud.Str(r[0])+": "+d)
		}
		if a := strings.TrimSpace(crud.Str(r[3])); a != "" {
			acc = append(acc, a)
		}
		if c := crud.Str(r[4]); c != "" {
			out.Contact = strings.TrimSpace(c + " " + m.partnerName(ctx, c))
		}
	}
	out.Path, out.Directions, out.Access = strings.Join(path, " › "), strings.Join(dirs, " – "), strings.Join(acc, "; ")
	return out, nil
}

// descendants: die Lage und alle darunter.
func (m *Module) descendants(ctx context.Context, cc, id string) ([]string, error) {
	out := []string{id}
	seen := map[string]bool{id: true}
	for i := 0; i < len(out); i++ {
		res, err := m.db.Query(ctx, "SELECT location_id FROM techobj__location WHERE company_code = ? AND parent_id = ? ORDER BY location_id", cc, out[i])
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			if c := crud.Str(r[0]); !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// tourAction: TechLocation.tour {id} – alle Messpunkte in dieser Lage und
// darunter, nach Lage sortiert (so, wie man sie abläuft), mit eingebautem
// Gerät, Zählernummer und für wen gemessen wird.
func (m *Module) tourAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Lage: id <BuKr>|<Lage>")
	}
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	locs, err := m.descendants(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	day := crud.KeyDate(ctx)
	var rows [][]any
	for _, l := range locs {
		path, err := m.locationPath(ctx, cc, l)
		if err != nil {
			return sdk.Response{}, err
		}
		res, err := m.db.Query(ctx, `SELECT pl.point_id, p.designation, COALESCE(pl.position, '') FROM techobj__point_location pl
			JOIN techobj__point p ON p.company_code = pl.company_code AND p.point_id = pl.point_id
			WHERE pl.company_code = ? AND pl.location_id = ? AND pl.valid_from <= ? AND pl.valid_to >= ?
			ORDER BY pl.position, pl.point_id`, cc, l, day, day)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, r := range res.Rows {
			pid := crud.Str(r[0])
			eq, numbers, err := m.installedAt(ctx, cc, pid, day)
			if err != nil {
				return sdk.Response{}, err
			}
			who, err := m.userText(ctx, cc, pid, day)
			if err != nil {
				return sdk.Response{}, err
			}
			rows = append(rows, []any{path, crud.Str(r[2]), pid, crud.Str(r[1]), eq, numbers, who})
		}
	}
	msg := fmt.Sprintf("Ablese-Rundgang ab %s am %s: %d Messpunkte", id, germanDate(day), len(rows))
	return sdk.Response{Payload: map[string]any{"message": msg, "count": len(rows),
		"table": map[string]any{"columns": []string{"Lage", "Position", "Messpunkt", "Bezeichnung", "Gerät", "Nummern", "misst für"}, "rows": rows}}}, nil
}
