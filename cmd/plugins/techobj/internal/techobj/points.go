package techobj

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Messpunkt -------------------------------------------------------------------------

// point: der benötigte Zählerplatz – bleibt am Ort, auch wenn das Gerät
// getauscht wird oder gerade keins eingebaut ist.
func (m *Module) point() *crud.Entity {
	return &crud.Entity{
		Object: "MeterPoint", Title: "Messpunkte", Icon: "icon-gauge", Table: "techobj__point", Section: "Bestand",
		Keys: []string{"company_code", "point_id"}, Order: "company_code, medium, point_id", TitleField: "designation",
		Filters: []string{"company_code", "medium", "status", "location", "number"},
		Search:  []string{"point_id", "designation", "market_location", "provider_number"},
		FilterExpr: map[string]func(v any) (string, []any){
			// Messpunkte in dieser Lage (je Zeitscheibe).
			"location": func(v any) (string, []any) {
				return "point_id IN (SELECT point_id FROM techobj__point_location WHERE location_id = ?)", []any{strings.TrimSpace(crud.Str(v))}
			},
			// Messpunkte, in die ein Gerät mit dieser Nummer eingebaut ist oder war.
			"number": func(v any) (string, []any) {
				return `point_id IN (SELECT i.point_id FROM techobj__installation i JOIN techobj__equipment_number n
					ON n.company_code = i.company_code AND n.equipment_id = i.equipment_id WHERE n.number LIKE ?)`,
					[]any{"%" + strings.TrimSpace(crud.Str(v)) + "%"}
			},
		},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"medium", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "point_id", Label: "Messpunkt (ID; leer = nächste freie, z. B. MP0001)", Short: "Messpunkt", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung (z. B. Strom Whg. 3)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "medium", Label: "Sparte", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("TechMedium")},
			{Key: "location_text", Label: "Lage", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "installed", Label: "Eingebautes Gerät", Short: "Gerät", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "measures", Label: "Misst für", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "location", Label: "Lage", Type: tText, FilterOnly: true, Lookup: locationLookup},
			{Key: "number", Label: "Nummer eines eingebauten Geräts", Type: tText, FilterOnly: true},
			{Key: "market_location", Label: "Zählpunktbezeichnung / MaLo-ID", Short: "Zählpunkt", Type: tText, Group: "Kennungen"},
			{Key: "provider_number", Label: "Nummer beim Versorger / Messdienst", Short: "Nr. Versorger", Type: tText, Group: "Kennungen"},
			{Key: "object_class", Label: "Objektklasse der Geräte (leer = jede Zählerklasse)", Short: "Klasse", Type: tText, Lookup: catalogLookup("TechObjectClass"), Group: "Vorgaben"},
			{Key: "registers", Label: "Benötigte Zählwerke (durch Komma, z. B. HT, NT)", Short: "Zählwerke", Type: tText, Group: "Vorgaben"},
			{Key: "host_id", Label: "Gehört zu Anlage (z. B. Betriebsstunden der Heizung)", Short: "Anlage", Type: tText, Lookup: equipmentLookup, Group: "Vorgaben"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechObjectStatus"), Group: "Gültigkeit"},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Group: "Gültigkeit"},
			{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "rp_date", Label: "Tauschdatum", Type: tDate, ActionOnly: true},
			{Key: "rp_equipment", Label: "Neues Gerät (technisches Objekt)", Type: tText, ActionOnly: true, Lookup: equipmentLookup},
			{Key: "rp_old", Label: "Endstand altes Gerät (eine Zahl oder je Zählwerk, z. B. HT=123,4; NT=56)", Type: tText, ActionOnly: true},
			{Key: "rp_new", Label: "Anfangsstand neues Gerät (leer = 0)", Type: tText, ActionOnly: true},
			{Key: "rp_type", Label: "Ableseart (leer = Vorschläge der Ablesearten für Ausbau und Einbau)", Type: tText, ActionOnly: true, Lookup: catalogLookup("TechReadingType")},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "lage", Title: "Lage", Relation: &metamodel.Relation{Object: "MeterPointLocation", ForeignKey: "point_id",
				Match: match("point_id", "point_id"), Columns: []string{"location_id", "position", "valid_from", "valid_to"}}},
			{Key: "einbau", Title: "Eingebaute Geräte", Relation: &metamodel.Relation{Object: "MeterInstallation", ForeignKey: "point_id",
				Match: match("point_id", "point_id"), Columns: []string{"equipment_id", "valid_from", "valid_to", "note"}}},
			{Key: "verknuepfungen", Title: "Verknüpfungen (Mietobjekt, Lieferant, Netzbetreiber …)", Relation: &metamodel.Relation{Object: "TechLink", ForeignKey: "source_id",
				Match: match("source_id", "point_id"), Filter: map[string]string{"source_kind": "MeterPoint"},
				Columns: []string{"role_code", "object_type", "object_id", "valid_from", "valid_to"}}},
			{Key: "unter", Title: "Untergeordnete Messpunkte", Collapsed: true, Relation: &metamodel.Relation{Object: "MeterPointRelation", ForeignKey: "parent_id",
				Match: match("parent_id", "point_id"), Columns: []string{"child_id", "relation_type", "valid_from", "valid_to"}}},
			{Key: "ueber", Title: "Hängt an", Collapsed: true, Relation: &metamodel.Relation{Object: "MeterPointRelation", ForeignKey: "child_id",
				Match: match("child_id", "point_id"), Columns: []string{"parent_id", "relation_type", "valid_from", "valid_to"}}},
			{Key: "ablesungen", Title: "Ablesungen", Collapsed: true, Relation: &metamodel.Relation{Object: "MeterReading", ForeignKey: "point_id",
				Match: match("point_id", "point_id"), Columns: []string{"read_on", "equipment_id", "register_code", "value", "reading_type"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "where", Label: "Wo ist der Zähler?", Record: true}, Handle: m.whereAction},
			{ActionConfig: metamodel.ActionConfig{Name: "replace", Label: "Zählertausch …", Record: true,
				Fields: []string{"rp_date", "rp_equipment", "rp_old", "rp_new", "rp_type"}}, Handle: m.replaceAction},
		},
		Access:      access(),
		CheckRecord: checkWrite("MeterPoint"),
		Validate:    m.checkPoint,
		Decorate:    m.decoratePoint,
	}
}

func (m *Module) checkPoint(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		if err := m.newID(ctx, rec, "point_id", "techobj__point", cc, "@POINT", "Messpunkt"); err != nil {
			return err
		}
	}
	rec["medium"], rec["object_class"] = trimUpper(rec["medium"]), nilIfEmpty(trimUpper(rec["object_class"]))
	if err := m.requireCatalog(ctx, "medium", "Sparte", cc, rec["medium"]); err != nil {
		return err
	}
	if err := m.requireCatalog(ctx, "object_class", "Objektklasse", cc, rec["object_class"]); err != nil {
		return err
	}
	rec["registers"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["registers"])), ","))
	for _, k := range []string{"market_location", "provider_number", "host_id"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	if h := crud.Str(rec["host_id"]); h != "" {
		if ok, err := m.one(ctx, "SELECT 1 FROM techobj__equipment WHERE company_code = ? AND equipment_id = ?", cc, h); err != nil {
			return err
		} else if ok == "" {
			return crud.Invalid("Anlage %s gibt es im Buchungskreis %s nicht", h, cc)
		}
	}
	return m.checkValidity(ctx, rec, true, true)
}

func (m *Module) decoratePoint(ctx context.Context, rec crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["point_id"])
	day := crud.KeyDate(ctx)
	loc, pos, err := m.pointLocationAt(ctx, cc, id, day)
	if err != nil {
		return err
	}
	if loc != "" {
		path, err := m.locationPath(ctx, cc, loc)
		if err != nil {
			return err
		}
		if pos != "" {
			path += " (" + pos + ")"
		}
		rec["location_text"] = path
	}
	eq, nums, err := m.installedAt(ctx, cc, id, day)
	if err != nil {
		return err
	}
	if nums != "" {
		eq += " – " + nums
	}
	rec["installed"] = eq
	who, err := m.userText(ctx, cc, id, day)
	if err != nil {
		return err
	}
	rec["measures"] = who
	return nil
}

func (m *Module) pointLocationAt(ctx context.Context, cc, point, day string) (string, string, error) {
	res, err := m.db.Query(ctx, `SELECT location_id, COALESCE(position, '') FROM techobj__point_location
		WHERE company_code = ? AND point_id = ? AND valid_from <= ? AND valid_to >= ?`, cc, point, day, day)
	if err != nil || len(res.Rows) == 0 {
		return "", "", err
	}
	return crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), nil
}

// installedAt: eingebautes Gerät am Tag („ZA0001 Stromzähler“) und seine Nummern.
func (m *Module) installedAt(ctx context.Context, cc, point, day string) (string, string, error) {
	res, err := m.db.Query(ctx, `SELECT e.equipment_id, e.designation FROM techobj__installation i
		JOIN techobj__equipment e ON e.company_code = i.company_code AND e.equipment_id = i.equipment_id
		WHERE i.company_code = ? AND i.point_id = ? AND i.valid_from <= ? AND i.valid_to >= ?`, cc, point, day, day)
	if err != nil || len(res.Rows) == 0 {
		return "", "", err
	}
	id := crud.Str(res.Rows[0][0])
	nums, err := m.numbersText(ctx, cc, id)
	return id + " " + crud.Str(res.Rows[0][1]), nums, err
}

func (m *Module) installedID(ctx context.Context, cc, point, day string) (string, error) {
	return m.one(ctx, `SELECT equipment_id FROM techobj__installation WHERE company_code = ? AND point_id = ? AND valid_from <= ? AND valid_to >= ?`,
		cc, point, day, day)
}

// whereAction: MeterPoint.where {id} – Lage mit Wegbeschreibung, Zugang,
// Ansprechpartner, eingebautem Gerät und Nummern.
func (m *Module) whereAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Messpunkt: id <BuKr>|<Messpunkt>")
	}
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	name, err := m.one(ctx, "SELECT designation FROM techobj__point WHERE company_code = ? AND point_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if name == "" {
		return sdk.Response{}, fmt.Errorf("%w: Messpunkt %s", sdk.ErrNotFound, id)
	}
	day := crud.KeyDate(ctx)
	loc, pos, err := m.pointLocationAt(ctx, cc, id, day)
	if err != nil {
		return sdk.Response{}, err
	}
	var rows [][]any
	add := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			rows = append(rows, []any{k, v})
		}
	}
	if loc == "" {
		add("Lage", "keine Lage hinterlegt – im Abschnitt „Lage“ eintragen")
	} else {
		info, err := m.locationInfo(ctx, cc, loc)
		if err != nil {
			return sdk.Response{}, err
		}
		add("Lage", info.Path)
		add("Position", pos)
		add("Weg", info.Directions)
		add("Zugang", info.Access)
		add("Ansprechpartner", info.Contact)
	}
	eq, nums, err := m.installedAt(ctx, cc, id, day)
	if err != nil {
		return sdk.Response{}, err
	}
	if eq == "" {
		eq = "kein Gerät eingebaut"
	}
	add("Gerät", eq)
	add("Nummern", nums)
	roles, err := m.db.Query(ctx, "SELECT code, name FROM techobj__link_role WHERE company_code = ? AND show_in_where = ? AND is_active = ? ORDER BY sort_order, code",
		cc, true, true)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range roles.Rows {
		t, err := m.linkText(ctx, cc, "MeterPoint", id, crud.Str(r[0]), day)
		if err != nil {
			return sdk.Response{}, err
		}
		add(crud.Str(r[1]), t)
	}
	return sdk.Response{Payload: map[string]any{"message": id + " " + name,
		"table": map[string]any{"columns": []string{"", ""}, "rows": rows}}}, nil
}

// --- Lage des Messpunkts, Versorgungsbaum, Einbau -----------------------------------------

func (m *Module) pointLocation() *crud.Entity {
	return &crud.Entity{
		Object: "MeterPointLocation", Title: "Lage der Messpunkte", Icon: "icon-map-pin", Table: "techobj__point_location", Section: "Bestand",
		Keys: []string{"company_code", "point_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, point_id, valid_from", Filters: []string{"company_code", "point_id", "location_id"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"point_id", "location_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "point_id", Label: "Messpunkt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: pointLookup},
			crud.Field{Key: "location_id", Label: "Lage", Type: tText, Required: true, Listable: true, Lookup: locationLookup},
			crud.Field{Key: "position", Label: "Position (z. B. Platz 3 von links)", Short: "Position", Type: tText, Listable: true},
		),
		Access:      access(),
		CheckRecord: checkWrite("MeterPoint"),
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			cc, point := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["point_id"]))
			loc := strings.TrimSpace(crud.Str(rec["location_id"]))
			rec["point_id"], rec["location_id"] = point, loc
			if ok, err := m.one(ctx, "SELECT 1 FROM techobj__location WHERE company_code = ? AND location_id = ?", cc, loc); err != nil {
				return err
			} else if ok == "" {
				return crud.Invalid("Lage %s gibt es im Buchungskreis %s nicht", loc, cc)
			}
			r, err := m.overlap(ctx, "techobj__point_location", "location_id", "company_code = ? AND point_id = ? AND valid_from <> ?",
				date(rec["valid_from"]), date(rec["valid_to"]), cc, point, date(rec["valid_from"]))
			if err != nil {
				return err
			}
			if r != nil {
				return crud.Invalid("%s liegt %s–%s bereits in %s – dort erst beenden", point, germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
			}
			return nil
		},
	}
}

func (m *Module) pointRelation() *crud.Entity {
	return &crud.Entity{
		Object: "MeterPointRelation", Title: "Versorgungsbaum", Icon: "icon-git-branch", Table: "techobj__point_relation", Section: "Bestand",
		Keys: []string{"company_code", "parent_id", "child_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, parent_id, child_id, valid_from", Filters: []string{"company_code", "parent_id", "child_id", "relation_type"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"parent_id", "child_id", "relation_type", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "parent_id", Label: "Übergeordneter Messpunkt (z. B. Hauptzähler)", Short: "Übergeordnet", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: pointLookup},
			crud.Field{Key: "child_id", Label: "Untergeordneter Messpunkt", Short: "Untergeordnet", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: pointLookup},
			crud.Field{Key: "relation_type", Label: "Beziehungsart", Short: "Art", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("TechRelationType")},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access:      access(),
		CheckRecord: checkWrite("MeterPoint"),
		Validate:    m.checkRelation,
	}
}

func (m *Module) checkRelation(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	parent, child := strings.TrimSpace(crud.Str(rec["parent_id"])), strings.TrimSpace(crud.Str(rec["child_id"]))
	rec["parent_id"], rec["child_id"], rec["relation_type"] = parent, child, trimUpper(rec["relation_type"])
	from, to := date(rec["valid_from"]), date(rec["valid_to"])
	if strings.EqualFold(parent, child) {
		return crud.Invalid("Ein Messpunkt kann nicht an sich selbst hängen")
	}
	for _, p := range []string{parent, child} {
		if ok, err := m.one(ctx, "SELECT 1 FROM techobj__point WHERE company_code = ? AND point_id = ?", cc, p); err != nil {
			return err
		} else if ok == "" {
			return crud.Invalid("Messpunkt %s gibt es im Buchungskreis %s nicht", p, cc)
		}
	}
	if err := m.requireCatalog(ctx, "relation_type", "Beziehungsart", cc, rec["relation_type"]); err != nil {
		return err
	}
	// Höchstens ein übergeordneter Messpunkt je Zeitpunkt.
	r, err := m.overlap(ctx, "techobj__point_relation", "parent_id", "company_code = ? AND child_id = ? AND NOT (parent_id = ? AND valid_from = ?)",
		from, to, cc, child, parent, from)
	if err != nil {
		return err
	}
	if r != nil {
		return crud.Invalid("%s hängt %s–%s bereits an %s – dort erst beenden", child, germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
	}
	// Kein Kreis: parent darf im Zeitraum nicht (mittelbar) unter child hängen.
	seen := map[string]bool{parent: true}
	for cur := parent; ; {
		up, err := m.one(ctx, `SELECT parent_id FROM techobj__point_relation WHERE company_code = ? AND child_id = ? AND valid_from <= ? AND valid_to >= ?`,
			cc, cur, to, from)
		if err != nil {
			return err
		}
		if up == "" {
			return nil
		}
		if strings.EqualFold(up, child) {
			return crud.Invalid("%s hängt im Zeitraum selbst unter %s – kein Kreis", parent, child)
		}
		if seen[up] {
			return nil
		}
		seen[up], cur = true, up
	}
}

func (m *Module) installation() *crud.Entity {
	return &crud.Entity{
		Object: "MeterInstallation", Title: "Einbau", Icon: "icon-tool", Table: "techobj__installation", Section: "Bestand",
		Keys: []string{"company_code", "point_id", "equipment_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, point_id, valid_from", Filters: []string{"company_code", "point_id", "equipment_id"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"point_id", "equipment_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "point_id", Label: "Messpunkt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: pointLookup},
			crud.Field{Key: "equipment_id", Label: "Gerät (technisches Objekt)", Short: "Gerät", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: equipmentLookup},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		),
		Access:      access(),
		CheckRecord: checkWrite("MeterPoint"),
		Validate:    m.checkInstallation,
	}
}

func (m *Module) checkInstallation(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	point, eq := strings.TrimSpace(crud.Str(rec["point_id"])), strings.TrimSpace(crud.Str(rec["equipment_id"]))
	rec["point_id"], rec["equipment_id"] = point, eq
	from, to := date(rec["valid_from"]), date(rec["valid_to"])
	want, err := m.db.Query(ctx, "SELECT COALESCE(object_class, '') FROM techobj__point WHERE company_code = ? AND point_id = ?", cc, point)
	if err != nil {
		return err
	}
	if len(want.Rows) == 0 {
		return crud.Invalid("Messpunkt %s gibt es im Buchungskreis %s nicht", point, cc)
	}
	res, err := m.db.Query(ctx, `SELECT e.object_class, COALESCE(c.is_meter, false) FROM techobj__equipment e
		LEFT JOIN techobj__object_class c ON c.company_code = e.company_code AND c.code = e.object_class
		WHERE e.company_code = ? AND e.equipment_id = ?`, cc, eq)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Gerät %s gibt es im Buchungskreis %s nicht", eq, cc)
	}
	class := crud.Str(res.Rows[0][0])
	if !crud.AsBool(res.Rows[0][1]) {
		return crud.Invalid("%s gehört zur Objektklasse %s – kein Zähler (Objektklassen: „Zähler“ ankreuzen)", eq, class)
	}
	if w := crud.Str(want.Rows[0][0]); w != "" && w != class {
		return crud.Invalid("Messpunkt %s verlangt Geräte der Objektklasse %s, %s ist %s", point, w, eq, class)
	}
	r, err := m.overlap(ctx, "techobj__installation", "equipment_id", "company_code = ? AND point_id = ? AND NOT (equipment_id = ? AND valid_from = ?)",
		from, to, cc, point, eq, from)
	if err != nil {
		return err
	}
	if r != nil {
		return crud.Invalid("In %s ist %s–%s bereits %s eingebaut – dort erst ausbauen (oder „Zählertausch …“)", point,
			germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
	}
	r, err = m.overlap(ctx, "techobj__installation", "point_id", "company_code = ? AND equipment_id = ? AND NOT (point_id = ? AND valid_from = ?)",
		from, to, cc, eq, point, from)
	if err != nil {
		return err
	}
	if r != nil {
		return crud.Invalid("%s ist %s–%s bereits in %s eingebaut", eq, germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
	}
	return nil
}

// --- Zählertausch ----------------------------------------------------------------------

// replaceAction: MeterPoint.replace {id, data: {rp_date, rp_equipment, rp_old,
// rp_new, rp_type}} – in einem Schritt: Endstand des alten Geräts (Ausbau),
// Einbau bis zum Vortag beenden, neues Gerät ab dem Tauschtag einbauen,
// Anfangsstand (Einbau). Messpunkt, Lage und Verknüpfungen bleiben.
func (m *Module) replaceAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Date      string `json:"rp_date"`
			Equipment string `json:"rp_equipment"`
			Old       string `json:"rp_old"`
			New       string `json:"rp_new"`
			Type      string `json:"rp_type"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, point, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Messpunkt: id <BuKr>|<Messpunkt>")
	}
	if err := requireWrite(ctx, "MeterPoint", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	day, err := crud.ParseDate(in.Data.Date)
	if err != nil || in.Data.Date == "" {
		return sdk.Response{}, crud.Invalid("Tauschdatum ist Pflicht")
	}
	newEq := strings.TrimSpace(in.Data.Equipment)
	if newEq == "" {
		return sdk.Response{}, crud.Invalid("Neues Gerät ist Pflicht")
	}
	oldEq, err := m.installedID(ctx, cc, point, day)
	if err != nil {
		return sdk.Response{}, err
	}
	if oldEq == newEq {
		return sdk.Response{}, crud.Invalid("%s ist bereits eingebaut", newEq)
	}
	removeType, installType := trimUpper(in.Data.Type), trimUpper(in.Data.Type)
	for role, v := range map[string]*string{"REMOVE": &removeType, "INSTALL": &installType} {
		if *v != "" {
			continue
		}
		if *v, err = m.one(ctx, "SELECT code FROM techobj__reading_type WHERE company_code = ? AND replace_role = ? AND is_active = ? ORDER BY sort_order",
			cc, role, true); err != nil {
			return sdk.Response{}, err
		}
		if *v == "" {
			return sdk.Response{}, crud.Invalid("Keine Ableseart mit Vorschlag beim Zählertausch %s – Einstellungen → Ablesearten (oder Ableseart angeben)", role)
		}
	}
	var msg []string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if oldEq != "" {
			if err := m.recordValues(ctx, cc, oldEq, point, day, removeType, in.Data.Old, false); err != nil {
				return fmt.Errorf("Endstand %s: %w", oldEq, err)
			}
			prev := dayBefore(day)
			res, err := m.db.Query(ctx, `SELECT valid_from FROM techobj__installation WHERE company_code = ? AND point_id = ? AND equipment_id = ?
				AND valid_from <= ? AND valid_to >= ?`, cc, point, oldEq, day, day)
			if err != nil || len(res.Rows) == 0 {
				return err
			}
			if date(res.Rows[0][0]) > prev {
				return crud.Invalid("%s wurde erst am %s eingebaut", oldEq, germanDate(date(res.Rows[0][0])))
			}
			if _, err := m.db.Exec(ctx, `UPDATE techobj__installation SET valid_to = ? WHERE company_code = ? AND point_id = ? AND equipment_id = ?
				AND valid_from <= ? AND valid_to >= ?`, prev, cc, point, oldEq, day, day); err != nil {
				return err
			}
			msg = append(msg, fmt.Sprintf("%s ausgebaut zum %s", oldEq, germanDate(prev)))
		}
		if _, err := m.create(ctx, "MeterInstallation", map[string]any{"data": map[string]any{
			"company_code": cc, "point_id": point, "equipment_id": newEq, "valid_from": day, "valid_to": crud.DateMax}}); err != nil {
			return err
		}
		if err := m.recordValues(ctx, cc, newEq, point, day, installType, in.Data.New, true); err != nil {
			return fmt.Errorf("Anfangsstand %s: %w", newEq, err)
		}
		msg = append(msg, fmt.Sprintf("%s eingebaut ab %s", newEq, germanDate(day)))
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Zählertausch " + point + ": " + strings.Join(msg, ", ")}}, nil
}

// recordValues: Stände aus „123,4“ (einziges Zählwerk) oder „HT=1; NT=2“ als
// Ablesungen speichern; leer = 0 nur beim Einbau (zero), sonst Pflicht.
func (m *Module) recordValues(ctx context.Context, cc, eq, point, day, typ, text string, zero bool) error {
	res, err := m.db.Query(ctx, "SELECT register_code FROM techobj__register WHERE company_code = ? AND equipment_id = ? ORDER BY register_code", cc, eq)
	if err != nil {
		return err
	}
	var regs []string
	for _, r := range res.Rows {
		regs = append(regs, crud.Str(r[0]))
	}
	if len(regs) == 0 {
		return crud.Invalid("%s hat keine Zählwerke – erst unter „Zählwerke“ anlegen", eq)
	}
	values := map[string]string{}
	text = strings.TrimSpace(text)
	switch {
	case text == "" && zero:
		for _, r := range regs {
			values[r] = "0"
		}
	case text == "":
		return crud.Invalid("Stand fehlt (Zählwerke %s)", strings.Join(regs, ", "))
	case !strings.Contains(text, "="):
		if len(regs) != 1 {
			return crud.Invalid("mehrere Zählwerke (%s) – Stände als %s=…; angeben", strings.Join(regs, ", "), regs[0])
		}
		values[regs[0]] = text
	default:
		for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ';' || r == '\n' }) {
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				return crud.Invalid("%q: Zählwerk=Stand", strings.TrimSpace(part))
			}
			values[trimUpper(k)] = strings.TrimSpace(v)
		}
	}
	for reg, v := range values {
		if _, err := m.create(ctx, "MeterReading", map[string]any{"data": map[string]any{
			"company_code": cc, "equipment_id": eq, "register_code": reg, "read_on": day, "reading_type": typ,
			"value": toFloat(v), "point_id": point}}); err != nil {
			return err
		}
	}
	return nil
}
