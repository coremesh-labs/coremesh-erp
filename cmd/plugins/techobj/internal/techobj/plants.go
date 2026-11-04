package techobj

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Anlagen (seit 0.6.0) ------------------------------------------------------------
//
// Eine Anlage fasst Messpunkte zusammen und kann aus Teilanlagen bestehen –
// flach gespeichert (Verweis auf die übergeordnete Anlage), bewegt wird man
// über Sichten: Teilanlagen, „Teil von“ mit Pfad, alle Messpunkte und
// Zählerstände mit Teilanlagen und die Struktur als Baum.

// plantClosure: alle Paare (Anlage, Anlage oder Teilanlage darunter) – auch
// die Anlage selbst; für Filter „mit Teilanlagen“.
const plantClosure = `WITH RECURSIVE plant_tree(company_code, top_id, plant_id) AS (
		SELECT company_code, plant_id, plant_id FROM techobj__plant
		UNION SELECT t.company_code, t.top_id, p.plant_id FROM plant_tree t
			JOIN techobj__plant p ON p.company_code = t.company_code AND p.parent_id = t.plant_id)`

func (m *Module) plant() *crud.Entity {
	return &crud.Entity{
		Object: "TechPlant", Title: "Anlagen", Icon: "icon-layers", Table: "techobj__plant", Section: "Bestand",
		Keys: []string{"company_code", "plant_id"}, Order: "company_code, plant_id", TitleField: "designation",
		Filters: []string{"company_code", "parent_id", "plant_type", "room_type", "location_id", "status", "top"},
		Search:  []string{"plant_id", "designation", "note"},
		FilterExpr: map[string]func(v any) (string, []any){
			// nur oberste Anlagen (ohne „Teil von“)
			"top": func(v any) (string, []any) {
				if crud.AsBool(v) {
					return "(parent_id IS NULL OR parent_id = '')", nil
				}
				return "1 = 1", nil
			},
		},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"parent_id", "plant_type", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "plant_id", Label: "Anlage (ID; leer = nächste freie, z. B. AN0001)", Short: "Anlage", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung (z. B. Heizkostenverteilung Goethestr. 29)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "plant_type", Label: "Art", Type: tText, Listable: true, Lookup: catalogLookup("TechPlantType")},
			{Key: "parent_id", Label: "Teil von (übergeordnete Anlage)", Short: "Teil von", Type: tText, Listable: true, Lookup: plantLookup},
			{Key: "path", Label: "Pfad", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "points_text", Label: "Messpunkte (direkt / mit Teilanlagen)", Short: "Messpunkte", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "location_id", Label: "Lage (optional)", Short: "Lage", Type: tText, Lookup: locationLookup},
			{Key: "room_no", Label: "Raum-Nr. im Raumbuch des Mietobjekts (Zimmer-Ebene)", Short: "Raum", Type: tText, Group: "Raum"},
			{Key: "room_type", Label: "Raumart (unser Kürzelschema)", Short: "Raumart", Type: tText, Listable: true, Group: "Raum",
				Lookup: &metamodel.Lookup{Object: "RoomType", ValueField: "code", LabelFields: []string{"name"}, Filters: ccFilter}},
			{Key: "bd_company", Label: "Buchungskreis", Type: tText, ActionOnly: true, Lookup: lookupCC},
			{Key: "bd_building", Label: "Gebäude (Modul Immobilien)", Type: tText, ActionOnly: true,
				Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}, Filters: map[string]string{"company_code": "bd_company"}}},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechObjectStatus"), Group: "Gültigkeit"},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Group: "Gültigkeit"},
			{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "top", Label: "Nur oberste Anlagen", Type: tBool, FilterOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "teile", Title: "Teilanlagen", Relation: &metamodel.Relation{Object: "TechPlant", ForeignKey: "parent_id",
				Match: match("parent_id", "plant_id"), Columns: []string{"plant_id", "designation", "plant_type", "points_text", "status"}}},
			{Key: "messpunkte", Title: "Messpunkte dieser Ebene", Relation: &metamodel.Relation{Object: "TechPlantPoint", ForeignKey: "plant_id",
				Match: match("plant_id", "plant_id"), Columns: []string{"point_id", "medium", "point_info", "valid_from", "valid_to", "note"}}},
			{Key: "alle", Title: "Alle Messpunkte mit Teilanlagen", Collapsed: true, Relation: &metamodel.Relation{Object: "TechPlantPoint", ForeignKey: "within",
				Match: match("within", "plant_id"), Columns: []string{"plant_id", "point_id", "medium", "point_info", "valid_from", "valid_to"}}},
			{Key: "staende", Title: "Zählerstände mit Teilanlagen", Collapsed: true, Relation: &metamodel.Relation{Object: "MeterReading", ForeignKey: "plant",
				Match: match("plant", "plant_id"), Columns: []string{"read_on", "point_id", "equipment_id", "register_code", "value", "unit", "consumption", "reading_type"}}},
			{Key: "verknuepfungen", Title: "Verknüpfungen (Mietobjekt, Vertrag, Messdienst …)", Relation: &metamodel.Relation{Object: "TechLink", ForeignKey: "source_id",
				Match: match("source_id", "plant_id"), Filter: map[string]string{"source_kind": "TechPlant"},
				Columns: []string{"role_code", "object_type", "object_id", "valid_from", "valid_to"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "structure", Label: "Struktur anzeigen", Record: true}, Handle: m.structureAction},
			{ActionConfig: metamodel.ActionConfig{Name: "build", Label: "Aus Immobilien aufbauen …", Fields: []string{"bd_company", "bd_building"},
				Requires: []string{"Building", "RentObject", "Room"}}, Handle: m.buildAction},
		},
		Access:      access(),
		CheckRecord: checkWrite("TechPlant"),
		Validate:    m.checkPlant,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["plant_id"])
			if p, err := m.plantPath(ctx, cc, crud.Str(rec["parent_id"])); err == nil && p != "" {
				rec["path"] = p + " › " + crud.Str(rec["designation"])
			} else {
				rec["path"] = rec["designation"]
			}
			day := crud.KeyDate(ctx)
			direct, err := m.one(ctx, `SELECT COUNT(*) FROM techobj__plant_point WHERE company_code = ? AND plant_id = ? AND valid_from <= ? AND valid_to >= ?`, cc, id, day, day)
			if err != nil {
				return err
			}
			all, err := m.one(ctx, plantClosure+` SELECT COUNT(DISTINCT pp.point_id) FROM techobj__plant_point pp JOIN plant_tree t ON t.company_code = pp.company_code AND t.plant_id = pp.plant_id
				WHERE t.company_code = ? AND t.top_id = ? AND pp.valid_from <= ? AND pp.valid_to >= ?`, cc, id, day, day)
			if err != nil {
				return err
			}
			rec["points_text"] = direct + " / " + all
			return nil
		},
	}
}

func (m *Module) checkPlant(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		if err := m.newID(ctx, rec, "plant_id", "techobj__plant", cc, "@PLANT", "Anlage"); err != nil {
			return err
		}
	}
	id := crud.Str(rec["plant_id"])
	parent := strings.TrimSpace(crud.Str(rec["parent_id"]))
	rec["parent_id"], rec["plant_type"] = nilIfEmpty(parent), nilIfEmpty(trimUpper(rec["plant_type"]))
	rec["location_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["location_id"])))
	rec["room_no"], rec["room_type"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["room_no"]))), nilIfEmpty(trimUpper(rec["room_type"]))
	if err := m.requireCatalog(ctx, "plant_type", "Anlagenart", cc, rec["plant_type"]); err != nil {
		return err
	}
	if l := crud.Str(rec["location_id"]); l != "" {
		if ok, err := m.one(ctx, "SELECT 1 FROM techobj__location WHERE company_code = ? AND location_id = ?", cc, l); err != nil {
			return err
		} else if ok == "" {
			return crud.Invalid("Lage %s gibt es im Buchungskreis %s nicht", l, cc)
		}
	}
	if parent != "" {
		if strings.EqualFold(parent, id) {
			return crud.Invalid("Eine Anlage kann nicht Teil von sich selbst sein")
		}
		// kein Kreis: die übergeordnete Anlage darf nicht (mittelbar) Teil dieser sein
		seen := map[string]bool{}
		for cur := parent; cur != ""; {
			res, err := m.db.Query(ctx, "SELECT COALESCE(parent_id, '') FROM techobj__plant WHERE company_code = ? AND plant_id = ?", cc, cur)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				if cur == parent {
					return crud.Invalid("Anlage %s gibt es im Buchungskreis %s nicht", parent, cc)
				}
				break
			}
			next := crud.Str(res.Rows[0][0])
			if strings.EqualFold(next, id) {
				return crud.Invalid("%s ist selbst Teil von %s – kein Kreis", parent, id)
			}
			if seen[next] {
				break
			}
			seen[next], cur = true, next
		}
	}
	return m.checkValidity(ctx, rec, true, true)
}

// plantPath: „Heizkostenverteilung › Nutzeinheit WE 04“ (von oben) bis einschließlich id.
func (m *Module) plantPath(ctx context.Context, cc, id string) (string, error) {
	var parts []string
	seen := map[string]bool{}
	for cur := id; cur != "" && !seen[cur] && len(parts) < 20; {
		seen[cur] = true
		res, err := m.db.Query(ctx, "SELECT designation, parent_id FROM techobj__plant WHERE company_code = ? AND plant_id = ?", cc, cur)
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

func (m *Module) plantPoint() *crud.Entity {
	return &crud.Entity{
		Object: "TechPlantPoint", Title: "Messpunkte der Anlagen", Icon: "icon-gauge", Table: "techobj__plant_point", Section: "Bestand",
		Keys: []string{"company_code", "plant_id", "point_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, plant_id, point_id, valid_from", Filters: []string{"company_code", "plant_id", "point_id", "within", "medium"},
		FilterExpr: map[string]func(v any) (string, []any){
			// Messpunkte dieser Anlage und aller Teilanlagen
			"within": func(v any) (string, []any) {
				return `plant_id IN (` + plantClosure + ` SELECT plant_id FROM plant_tree WHERE company_code = techobj__plant_point.company_code AND top_id = ?)`,
					[]any{strings.TrimSpace(crud.Str(v))}
			},
			// Messpunkte einer Sparte (Kategorie des Messpunkts)
			"medium": func(v any) (string, []any) {
				return `point_id IN (SELECT point_id FROM techobj__point WHERE company_code = techobj__plant_point.company_code AND medium = ?)`, []any{trimUpper(v)}
			},
		},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"plant_id", "point_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "plant_id", Label: "Anlage", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: plantLookup},
			crud.Field{Key: "point_id", Label: "Messpunkt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: pointLookup},
			crud.Field{Key: "medium", Label: "Sparte", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Lookup: catalogLookup("TechMedium")},
			crud.Field{Key: "point_info", Label: "Gerät, Nummer und letzter Stand", Short: "Gerät / Stand", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
			crud.Field{Key: "within", Label: "In Anlage (mit Teilanlagen)", Type: tText, FilterOnly: true, Lookup: plantLookup},
		),
		Access:      access(),
		CheckRecord: checkWrite("TechPlant"),
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			cc := crud.Str(rec["company_code"])
			plant, point := strings.TrimSpace(crud.Str(rec["plant_id"])), strings.TrimSpace(crud.Str(rec["point_id"]))
			rec["plant_id"], rec["point_id"] = plant, point
			if ok, err := m.one(ctx, "SELECT 1 FROM techobj__plant WHERE company_code = ? AND plant_id = ?", cc, plant); err != nil {
				return err
			} else if ok == "" {
				return crud.Invalid("Anlage %s gibt es im Buchungskreis %s nicht", plant, cc)
			}
			if ok, err := m.one(ctx, "SELECT 1 FROM techobj__point WHERE company_code = ? AND point_id = ?", cc, point); err != nil {
				return err
			} else if ok == "" {
				return crud.Invalid("Messpunkt %s gibt es im Buchungskreis %s nicht", point, cc)
			}
			r, err := m.overlap(ctx, "techobj__plant_point", "plant_id", "company_code = ? AND plant_id = ? AND point_id = ? AND valid_from <> ?",
				date(rec["valid_from"]), date(rec["valid_to"]), cc, plant, point, date(rec["valid_from"]))
			if err != nil {
				return err
			}
			if r != nil {
				return crud.Invalid("%s gehört %s–%s bereits zu %s", point, germanDate(date(r[1])), germanDate(date(r[2])), plant)
			}
			return nil
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			cc := crud.Str(rec["company_code"])
			info, err := m.sourceInfo(ctx, cc, "MeterPoint", crud.Str(rec["point_id"]))
			if err != nil {
				return err
			}
			rec["point_info"] = info
			if md, err := m.one(ctx, "SELECT medium FROM techobj__point WHERE company_code = ? AND point_id = ?", cc, rec["point_id"]); err != nil {
				return err
			} else {
				rec["medium"] = md
			}
			return nil
		},
	}
}

// structureAction: TechPlant.structure {id} – die Anlage als Baum von oben nach
// unten: je Ebene die Anlage, darunter ihre Messpunkte mit Gerät, Nummern,
// letztem Stand und für wen gemessen wird.
func (m *Module) structureAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Anlage: id <BuKr>|<Anlage>")
	}
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	day := crud.KeyDate(ctx)
	var rows [][]any
	points := 0
	seen := map[string]bool{}
	var walk func(plant string, depth int) error
	walk = func(plant string, depth int) error {
		if seen[plant] || depth > 20 {
			return nil
		}
		seen[plant] = true
		res, err := m.db.Query(ctx, `SELECT p.designation, COALESCE(t.name, p.plant_type, '') FROM techobj__plant p
			LEFT JOIN techobj__plant_type t ON t.company_code = p.company_code AND t.code = p.plant_type
			WHERE p.company_code = ? AND p.plant_id = ?`, cc, plant)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Anlage %s gibt es im Buchungskreis %s nicht", plant, cc)
		}
		indent := strings.Repeat("    ", depth)
		rows = append(rows, []any{indent + "▸ " + plant, crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), "", "", ""})
		pts, err := m.db.Query(ctx, `SELECT pp.point_id, p.designation, COALESCE(md.name, p.medium) FROM techobj__plant_point pp
			JOIN techobj__point p ON p.company_code = pp.company_code AND p.point_id = pp.point_id
			LEFT JOIN techobj__medium md ON md.company_code = p.company_code AND md.code = p.medium
			WHERE pp.company_code = ? AND pp.plant_id = ? AND pp.valid_from <= ? AND pp.valid_to >= ? ORDER BY p.medium, pp.point_id`, cc, plant, day, day)
		if err != nil {
			return err
		}
		for _, r := range pts.Rows {
			pid := crud.Str(r[0])
			info, err := m.sourceInfo(ctx, cc, "MeterPoint", pid)
			if err != nil {
				return err
			}
			who, err := m.userText(ctx, cc, pid, day)
			if err != nil {
				return err
			}
			loc, pos, err := m.pointLocationAt(ctx, cc, pid, day)
			if err != nil {
				return err
			}
			if loc != "" {
				if loc, err = m.locationPath(ctx, cc, loc); err != nil {
					return err
				}
				if pos != "" {
					loc += " (" + pos + ")"
				}
			}
			points++
			rows = append(rows, []any{indent + "    · " + pid, crud.Str(r[1]), "Messpunkt " + crud.Str(r[2]), info, who, loc})
		}
		kids, err := m.db.Query(ctx, "SELECT plant_id FROM techobj__plant WHERE company_code = ? AND parent_id = ? ORDER BY plant_id", cc, plant)
		if err != nil {
			return err
		}
		for _, k := range kids.Rows {
			if err := walk(crud.Str(k[0]), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(id, 0); err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Struktur von %s am %s: %d Anlagen, %d Messpunkte", id, germanDate(day), len(seen), points)
	return sdk.Response{Payload: map[string]any{"message": msg, "count": points,
		"table": map[string]any{"columns": []string{"Anlage / Messpunkt", "Bezeichnung", "Art / Sparte", "Gerät, Nummer, letzter Stand", "misst für", "Lage"}, "rows": rows}}}, nil
}

// plantPointsExists: der Messpunkt pointExpr gehört am Tag dayExpr zur Anlage
// plantExpr oder einer ihrer Teilanlagen (SQL-Ausdrücke der äußeren Abfrage).
func plantPointsExists(ccExpr, plantExpr, pointExpr, dayExpr string) string {
	return `EXISTS (` + plantClosure + ` SELECT 1 FROM techobj__plant_point pp JOIN plant_tree t ON t.company_code = pp.company_code AND t.plant_id = pp.plant_id
		WHERE t.company_code = ` + ccExpr + ` AND t.top_id = ` + plantExpr + ` AND pp.point_id = ` + pointExpr + ` AND ` + dayExpr + ` BETWEEN pp.valid_from AND pp.valid_to)`
}
