package realestate

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
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
