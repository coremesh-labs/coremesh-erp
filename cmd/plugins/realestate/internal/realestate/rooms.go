package realestate

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// --- Raumbuch (seit 0.4.0) ------------------------------------------------------------
//
// Räume je Mietobjekt (Raumart aus dem Katalog, Fläche, Reihenfolge) und ihre
// Bauteile und Ausstattung (Boden, Wände, Fenster, Heizkörper …) mit
// Beschreibung (z. B. „Laminat Eiche“, „2 Fenster Kunststoff, Rollladen“).
// Ein neuer Raum bekommt die Bauteile seiner Raumart. Andere Module – etwa
// Begehungen und Übergabeprotokolle – lesen das Raumbuch über Room.list und
// RoomComponent.list.

var roomHCL = `
table "realestate__room" {
  schema = schema.main
  column "company_code" { type = text }
  column "object_id"    { type = text }
  column "room_no"      { type = text }
  column "room_type"    { type = text }
  column "designation"  { type = text }
  column "area" {
    type = double
    null = true
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "note" {
    type = text
    null = true
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.object_id, column.room_no] }
}

table "realestate__room_component" {
  schema = schema.main
  column "company_code"   { type = text }
  column "object_id"      { type = text }
  column "room_no"        { type = text }
  column "component_type" { type = text }
  column "description" {
    type = text
    null = true
  }
  column "quantity" {
    type = double
    null = true
  }
  column "note" {
    type = text
    null = true
  }
  primary_key { columns = [column.company_code, column.object_id, column.room_no, column.component_type] }
  foreign_key "realestate__room_component_room" {
    columns     = [column.company_code, column.object_id, column.room_no]
    ref_columns = [table.realestate__room.column.company_code, table.realestate__room.column.object_id, table.realestate__room.column.room_no]
  }
}
`

// seit 0.5.0: Abbildung der Raumkürzel eines Kürzelschemas (z. B. Messdienst) auf
// unsere Raumarten – „KU“ bei Messdienst A und „K“ bei Messdienst B sind beide KUECHE.
var roomMapHCL = `
table "realestate__room_type_map" {
  schema = schema.main
  column "company_code" { type = text }
  column "scheme"       { type = text }
  column "ext_code"     { type = text }
  column "room_type"    { type = text }
  column "designation" {
    type = text
    null = true
  }
  column "note" {
    type = text
    null = true
  }
  primary_key { columns = [column.company_code, column.scheme, column.ext_code] }
}
`

var rentObjectLookup = &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
	Filters: map[string]string{"company_code": "company_code"}}

func (m *Module) room() *crud.Entity {
	return &crud.Entity{
		Object: "Room", Title: "Raumbuch", Icon: "icon-door", Table: "realestate__room", Section: "Bestand",
		Keys: []string{"company_code", "object_id", "room_no"}, Order: "company_code, object_id, sort_order, room_no", TitleField: "designation",
		Filters: []string{"company_code", "object_id", "room_type"}, Search: []string{"designation", "room_no"},
		StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "object_id", Label: "Mietobjekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: rentObjectLookup},
			{Key: "room_no", Label: "Raum (Nr.; leer = nächste, z. B. 01)", Short: "Nr.", Type: tText, Listable: true, Immutable: true},
			{Key: "room_type", Label: "Raumart", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("RoomType")},
			{Key: "designation", Label: "Bezeichnung (leer = Raumart, z. B. Kinderzimmer straßenseitig)", Short: "Bezeichnung", Type: tText, Listable: true},
			{Key: "area", Label: "Fläche (m²)", Short: "Fläche", Type: tNum, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge (Rundgang im Protokoll)", Short: "Reihenfolge", Type: tNum},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "bauteile", Title: "Bauteile und Ausstattung", Relation: &metamodel.Relation{Object: "RoomComponent", ForeignKey: "room_no",
				Match:   map[string]string{"company_code": "company_code", "object_id": "object_id", "room_no": "room_no"},
				Columns: []string{"component_type", "description", "quantity", "note"}}},
			{Key: "dokumente", Title: "Fotos und Dokumente", Collapsed: true, Documents: true},
		},
		Access: &crud.Access{Object: "RentObject", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "RentObject", "update", crud.Str(rec["company_code"]))
		},
		Validate:    m.checkRoom,
		AfterCreate: m.roomComponentsFromType,
	}
}

func (m *Module) checkRoom(ctx context.Context, rec, old crud.Record) error {
	cc, obj := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["object_id"]))
	rec["object_id"], rec["room_type"] = obj, trimUpper(rec["room_type"])
	if old == nil {
		res, err := m.db.Query(ctx, "SELECT 1 FROM realestate__rent_object WHERE company_code = ? AND object_id = ?", cc, obj)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Mietobjekt %s gibt es im Buchungskreis %s nicht", obj, cc)
		}
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		if strings.TrimSpace(crud.Str(rec["room_no"])) == "" {
			res, err := m.db.Query(ctx, "SELECT room_no FROM realestate__room WHERE company_code = ? AND object_id = ?", cc, obj)
			if err != nil {
				return err
			}
			n := 0
			for _, r := range res.Rows {
				var k int
				if _, err := fmt.Sscanf(crud.Str(r[0]), "%d", &k); err == nil && k > n {
					n = k
				}
			}
			rec["room_no"] = fmt.Sprintf("%02d", n+1)
		}
		rec["room_no"] = strings.TrimSpace(crud.Str(rec["room_no"]))
		if crud.Str(rec["sort_order"]) == "" || toFloat(rec["sort_order"]) == 0 {
			var k int
			_, _ = fmt.Sscanf(crud.Str(rec["room_no"]), "%d", &k)
			rec["sort_order"] = k * 10
		}
	}
	res, err := m.db.Query(ctx, "SELECT name FROM realestate__room_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, rec["room_type"], true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Raumart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", crud.Str(rec["room_type"]), cc)
	}
	if strings.TrimSpace(crud.Str(rec["designation"])) == "" {
		rec["designation"] = res.Rows[0][0]
	}
	if toFloat(rec["area"]) < 0 {
		return crud.Invalid("Fläche darf nicht negativ sein")
	}
	return nil
}

// roomComponentsFromType: Bauteile der Raumart an den neuen Raum.
func (m *Module) roomComponentsFromType(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	comps, err := m.db.Query(ctx, "SELECT COALESCE(components, '') FROM realestate__room_type WHERE company_code = ? AND code = ?", cc, rec["room_type"])
	if err != nil || len(comps.Rows) == 0 {
		return err
	}
	for _, c := range strings.Split(crud.Str(comps.Rows[0][0]), ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO realestate__room_component (company_code, object_id, room_no, component_type) VALUES (?, ?, ?, ?)
			ON CONFLICT DO NOTHING`, cc, rec["object_id"], rec["room_no"], c); err != nil {
			return err
		}
	}
	return nil
}

func (m *Module) roomComponent() *crud.Entity {
	return &crud.Entity{
		Object: "RoomComponent", Title: "Raumbuch – Bauteile", Icon: "icon-list", Table: "realestate__room_component", Section: "Bestand",
		Keys:    []string{"company_code", "object_id", "room_no", "component_type"},
		Order:   "company_code, object_id, room_no, component_type",
		Filters: []string{"company_code", "object_id", "room_no", "component_type"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "object_id", Label: "Mietobjekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: rentObjectLookup},
			{Key: "room_no", Label: "Raum", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Room", ValueField: "room_no", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code", "object_id": "object_id"}}},
			{Key: "component_type", Label: "Bauteil / Ausstattung", Short: "Bauteil", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ComponentType")},
			{Key: "description", Label: "Beschreibung (z. B. Laminat Eiche, 2 Fenster Kunststoff mit Rollladen)", Short: "Beschreibung", Type: tText, Listable: true, Wide: true},
			{Key: "quantity", Label: "Anzahl", Type: tNum, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tArea},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Entfernen", Record: true,
			Confirm: "Bauteil aus dem Raum entfernen?"}, Handle: m.removeRoomComponent}},
		Access: &crud.Access{Object: "RentObject", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "RentObject", "update", crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			cc := crud.Str(rec["company_code"])
			rec["component_type"] = trimUpper(rec["component_type"])
			res, err := m.db.Query(ctx, "SELECT 1 FROM realestate__room WHERE company_code = ? AND object_id = ? AND room_no = ?", cc, rec["object_id"], rec["room_no"])
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				return crud.Invalid("Raum %s im Mietobjekt %s gibt es nicht", crud.Str(rec["room_no"]), crud.Str(rec["object_id"]))
			}
			if toFloat(rec["quantity"]) < 0 {
				return crud.Invalid("Anzahl darf nicht negativ sein")
			}
			return m.requireCatalog(ctx, "component_type", "Bauteil", cc, rec["component_type"])
		},
	}
}

// removeRoomComponent: RoomComponent.remove {id: <BuKr>|<Mietobjekt>|<Raum>|<Bauteil>}.
func (m *Module) removeRoomComponent(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	p := strings.Split(in.ID, "|")
	if len(p) != 4 {
		return sdk.Response{}, crud.Invalid("Bauteil: id <BuKr>|<Mietobjekt>|<Raum>|<Bauteil>")
	}
	if err := requireWrite(ctx, "RentObject", "update", p[0]); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Exec(ctx, "DELETE FROM realestate__room_component WHERE company_code = ? AND object_id = ? AND room_no = ? AND component_type = ?", p[0], p[1], p[2], p[3])
	if err != nil {
		return sdk.Response{}, err
	}
	if res.RowsAffected == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Bauteil %s", sdk.ErrNotFound, in.ID)
	}
	return sdk.Response{Payload: map[string]any{"message": "Bauteil entfernt"}}, nil
}

// --- Abbildung der Raumkürzel (seit 0.5.0) -------------------------------------------
//
// Unser Schema: Raumart mit einheitlichem Kürzel (Katalog Raumarten, Spalte short).
// Fremde Schemata (Katalog Kürzelschemata, z. B. ein Messdienst) bilden ihre
// Kürzel hier auf unsere Raumarten ab, wahlweise mit eigener Bezeichnung
// (z. B. Freudenberg „AWC“ → ANBAUWC). RoomTypeMap.resolve löst ein Kürzel auf:
// zuerst die Abbildung des Schemas, dann unser Kürzel, dann der Schlüssel der Raumart.

func (m *Module) roomTypeMap() *crud.Entity {
	return &crud.Entity{
		Object: "RoomTypeMap", Customizing: true, Title: "Abbildung Raumkürzel", Icon: "icon-shuffle", Table: "realestate__room_type_map", Section: "Einstellungen",
		Keys: []string{"company_code", "scheme", "ext_code"}, Order: "company_code, scheme, ext_code", TitleField: "ext_code",
		Filters: []string{"company_code", "scheme", "room_type"}, Search: []string{"ext_code", "designation"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "scheme", Label: "Kürzelschema (z. B. Messdienst)", Short: "Schema", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("CodeScheme")},
			{Key: "ext_code", Label: "Kürzel im Schema (z. B. KU, AWC)", Short: "Kürzel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "room_type", Label: "Raumart", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("RoomType")},
			{Key: "designation", Label: "Bezeichnung des Raums (leer = Raumart)", Short: "Bezeichnung", Type: tText, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tArea},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "RoomTypeMap", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkRoomTypeMap,
		Routes:   map[string]module.HandlerFunc{"resolve": m.resolveRoomCode},
	}
}

func (m *Module) checkRoomTypeMap(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["room_type"] = trimUpper(rec["room_type"])
	if old == nil {
		rec["scheme"], rec["ext_code"] = trimUpper(rec["scheme"]), strings.TrimSpace(crud.Str(rec["ext_code"]))
		if crud.Str(rec["ext_code"]) == "" {
			return crud.Invalid("Kürzel im Schema fehlt")
		}
		if err := m.requireCatalog(ctx, "code_scheme", "Kürzelschema", cc, rec["scheme"]); err != nil {
			return err
		}
		res, err := m.db.Query(ctx, `SELECT ext_code FROM realestate__room_type_map WHERE company_code = ? AND scheme = ? AND LOWER(ext_code) = LOWER(?)`,
			cc, rec["scheme"], rec["ext_code"])
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			return crud.Invalid("Kürzel %s ist im Schema %s schon abgebildet", crud.Str(res.Rows[0][0]), crud.Str(rec["scheme"]))
		}
	}
	if ok, err := m.catalogHas(ctx, "room_type", cc, crud.Str(rec["room_type"])); err != nil {
		return err
	} else if !ok {
		return crud.Invalid("Raumart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", crud.Str(rec["room_type"]), cc)
	}
	rec["designation"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["designation"])))
	return nil
}

// resolveRoomCode: RoomTypeMap.resolve {company_code, scheme (leer = nur unser Schema), code}
// → {room_type, name, short, designation, via: MAP | SHORT | CODE}.
func (m *Module) resolveRoomCode(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		Scheme      string `json:"scheme"`
		Code        string `json:"code"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, scheme, code := strings.TrimSpace(in.CompanyCode), strings.ToUpper(strings.TrimSpace(in.Scheme)), strings.TrimSpace(in.Code)
	if cc == "" || code == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Kürzel sind Pflicht")
	}
	if ok, err := sdk.CheckAccess(ctx, "RoomTypeMap", "list", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung für RoomTypeMap.list im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	type step struct{ via, sql string }
	steps := []step{
		{"SHORT", `SELECT code, NULL FROM realestate__room_type WHERE company_code = ? AND UPPER(short) = UPPER(?) AND is_active = ?`},
		{"CODE", `SELECT code, NULL FROM realestate__room_type WHERE company_code = ? AND code = UPPER(?) AND is_active = ?`},
	}
	args := [][]any{{cc, code, true}, {cc, code, true}}
	if scheme != "" {
		steps = append([]step{{"MAP", `SELECT room_type, designation FROM realestate__room_type_map WHERE company_code = ? AND scheme = ? AND LOWER(ext_code) = LOWER(?)`}}, steps...)
		args = append([][]any{{cc, scheme, code}}, args...)
	}
	for i, s := range steps {
		res, err := m.db.Query(ctx, s.sql, args[i]...)
		if err != nil {
			return sdk.Response{}, err
		}
		if len(res.Rows) == 0 {
			continue
		}
		rt := crud.Str(res.Rows[0][0])
		t, err := m.db.Query(ctx, `SELECT name, COALESCE(short, '') FROM realestate__room_type WHERE company_code = ? AND code = ?`, cc, rt)
		if err != nil {
			return sdk.Response{}, err
		}
		if len(t.Rows) == 0 {
			continue
		}
		name, short := crud.Str(t.Rows[0][0]), crud.Str(t.Rows[0][1])
		des := crud.Str(res.Rows[0][1])
		if des == "" {
			des = name
		}
		return sdk.Response{Payload: map[string]any{"room_type": rt, "name": name, "short": short, "designation": des, "via": s.via}}, nil
	}
	where := "in unserem Schema"
	if scheme != "" {
		where = "im Schema " + scheme + " und in unserem Schema"
	}
	return sdk.Response{}, fmt.Errorf("%w: Raumkürzel %q %s nicht bekannt – Abbildung unter Immobilien → Einstellungen → Abbildung Raumkürzel anlegen", sdk.ErrNotFound, code, where)
}
