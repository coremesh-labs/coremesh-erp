package realestate

import (
	"fmt"
	"strings"
)

// Schema der Immobilienverwaltung (Atlas HCL, Präfix realestate__, keine DROPs).
//
//   - Kataloge je Buchungskreis (catalogTables): Schlüssel (company_code, code).
//   - Wirtschaftseinheit → Gebäude → Mietobjekte (gemeinsamer Stamm
//     rent_object mit der Art UNIT, SPACE, POOL, COMPOSITE).
//   - Zeitscheiben nur, wo sich Werte über die Zeit ändern: Zuordnung zum
//     Vertragsobjekt (composite_item) und Bemessungen (measurement).
//   - Gültigkeit der Objekte als Zeitraum (valid_from, valid_to) und Status
//     (Katalog), ohne Versionierung.

// catalogTable: ein Katalog je Buchungskreis mit Zusatzspalten.
type catalogTable struct {
	Table, Object, Title, Section string
	Extra                         string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "usage_type", Object: "UsageType", Title: "Nutzungsarten", Extra: `
  # Kürzel für sprechende Objekt-IDs (z. B. WG → LpzBrn1WG001)
  column "id_prefix" { type = text }
  # Objektarten, für die die Nutzungsart gilt (UNIT,SPACE,POOL,COMPOSITE); leer = alle
  column "kinds" {
    type = text
    null = true
  }`},
	{Table: "measurement_type", Object: "MeasurementType", Title: "Bemessungsarten", Extra: `
  column "default_unit" {
    type = text
    null = true
  }
  # Fläche: zählt bei der Prüfung Pool ≥ Summe der Flächen
  column "is_area" {
    type    = boolean
    default = false
  }`},
	{Table: "measure_unit", Object: "MeasureUnit", Title: "Maßeinheiten"},
	{Table: "object_status", Object: "ObjectStatus", Title: "Objektstatus"},
	{Table: "entity_type", Object: "EntityType", Title: "Arten der Wirtschaftseinheit"},
	{Table: "building_type", Object: "BuildingType", Title: "Gebäudearten"},
	{Table: "floor", Object: "Floor", Title: "Geschosse"},
	{Table: "location", Object: "Location", Title: "Lagen"},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "realestate__%s" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }%s
  primary_key { columns = [column.company_code, column.code] }
}
`, c.Table, c.Extra)
	}
	return b.String()
}

const validity = `
  column "status" {
    type = text
    null = true
  }
  column "valid_from" { type = date }
  column "valid_to"   { type = date }
  column "changed_at" {
    type = text
    null = true
  }
  column "changed_by" {
    type = text
    null = true
  }`

var schemaHCL = catalogHCL() + `
table "realestate__business_entity" {
  schema = schema.main
  column "company_code" { type = text }
  column "entity_id"    { type = text }
  column "designation"  { type = text }
  column "entity_type" {
    type = text
    null = true
  }
  column "street" {
    type = text
    null = true
  }
  column "zip" {
    type = text
    null = true
  }
  column "city" {
    type = text
    null = true
  }` + validity + `
  primary_key { columns = [column.company_code, column.entity_id] }
}

table "realestate__building" {
  schema = schema.main
  column "company_code" { type = text }
  column "building_id"  { type = text }
  column "entity_id"    { type = text }
  column "designation"  { type = text }
  column "building_type" {
    type = text
    null = true
  }
  column "street" {
    type = text
    null = true
  }
  column "zip" {
    type = text
    null = true
  }
  column "city" {
    type = text
    null = true
  }
  column "construction_year" {
    type = bigint
    null = true
  }` + validity + `
  primary_key { columns = [column.company_code, column.building_id] }
  foreign_key "realestate__building_entity_fk" {
    columns     = [column.company_code, column.entity_id]
    ref_columns = [table.realestate__business_entity.column.company_code, table.realestate__business_entity.column.entity_id]
  }
}

# Gemeinsamer Stamm aller Mietobjekte: Mieteinheit (UNIT), Fläche (SPACE),
# Pool (POOL) und Vertragsobjekt (COMPOSITE). Verträge, Kontierungen im
# Hauptbuch, Bemessungen und Tags verweisen auf object_id.
table "realestate__rent_object" {
  schema = schema.main
  column "company_code" { type = text }
  column "object_id"    { type = text }
  column "kind"         { type = text }
  column "entity_id"    { type = text }
  column "building_id"  { type = text }
  column "designation"  { type = text }
  column "usage_type"   { type = text }
  column "floor" {
    type = text
    null = true
  }
  column "location" {
    type = text
    null = true
  }
  # Fläche: Pool, aus dem sie geschnitten ist
  column "pool_id" {
    type = text
    null = true
  }
  # Pool: Gesamtfläche und deren Bemessungsart
  column "area_type" {
    type = text
    null = true
  }
  column "total_area" {
    type = double
    null = true
  }` + validity + `
  primary_key { columns = [column.company_code, column.object_id] }
  index "realestate__rent_object_building" {
    columns = [column.company_code, column.building_id, column.kind]
  }
  foreign_key "realestate__rent_object_building_fk" {
    columns     = [column.company_code, column.building_id]
    ref_columns = [table.realestate__building.column.company_code, table.realestate__building.column.building_id]
  }
}

# Vertragsobjekt ↔ enthaltene Objekte (Zeitscheibe): ein Objekt gehört zu einem
# Zeitpunkt zu höchstens einem Vertragsobjekt.
table "realestate__composite_item" {
  schema = schema.main
  column "company_code" { type = text }
  column "composite_id" { type = text }
  column "object_id"    { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  primary_key { columns = [column.company_code, column.composite_id, column.object_id, column.valid_from] }
  index "realestate__composite_item_object" {
    columns = [column.company_code, column.object_id]
  }
  foreign_key "realestate__composite_item_composite_fk" {
    columns     = [column.company_code, column.composite_id]
    ref_columns = [table.realestate__rent_object.column.company_code, table.realestate__rent_object.column.object_id]
  }
  foreign_key "realestate__composite_item_object_fk" {
    columns     = [column.company_code, column.object_id]
    ref_columns = [table.realestate__rent_object.column.company_code, table.realestate__rent_object.column.object_id]
  }
}

# Bemessungen (Zeitscheibe) für jedes Objekt: Wirtschaftseinheit, Gebäude und
# Mietobjekte – z. B. Wohnfläche, Miteigentumsanteil, Zimmerzahl.
table "realestate__measurement" {
  schema = schema.main
  column "company_code"     { type = text }
  column "object_id"        { type = text }
  column "object_level"     { type = text }
  column "measurement_type" { type = text }
  column "valid_from"       { type = date }
  column "valid_to"         { type = date }
  column "value"            { type = double }
  column "unit" {
    type = text
    null = true
  }
  primary_key { columns = [column.company_code, column.object_id, column.measurement_type, column.valid_from] }
}
`
