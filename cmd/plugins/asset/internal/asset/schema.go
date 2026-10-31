package asset

import (
	"fmt"
	"strings"
)

// Schema der Objekte (Atlas HCL, Präfix asset__, keine DROPs).
//
//   - Kataloge je Buchungskreis (catalogTables): Schlüssel (company_code, code).
//   - Objekt (asset) mit Objektart aus dem Katalog; was eine Art ausmacht
//     (Kürzel, erlaubte Bestandteile), steht im Katalog, nicht im Code.
//   - Zeitscheiben für Bestandteile (part) und Personen in Rollen (person).

// catalogTable: ein Katalog je Buchungskreis mit Zusatzspalten.
type catalogTable struct {
	Table, Object, Title string
	Extra                string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "asset_type", Object: "AssetType", Title: "Objektarten", Extra: `
  # Kürzel für sprechende Objekt-IDs (z. B. AU → AU001)
  column "id_prefix" { type = text }
  # Objektarten, die Bestandteil sein dürfen (z. B. Auto: REIFEN); leer = keine
  column "part_types" {
    type = text
    null = true
  }`},
	{Table: "asset_status", Object: "AssetStatus", Title: "Objektstatus"},
	{Table: "asset_role", Object: "AssetRole", Title: "Rollen am Objekt", Extra: `
  # höchstens eine Person je Objekt und Stichtag (z. B. Halter)
  column "is_exclusive" {
    type    = boolean
    default = false
  }`},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "asset__%s" {
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

// optional: Textspalten, die leer bleiben dürfen.
func optional(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = text
    null = true
  }`, c)
	}
	return b.String()
}

var schemaHCL = catalogHCL() + `
# Objekt: alles, woran Verträge, Kosten, Dokumente und Personen hängen –
# Wohnung, Auto, Reifensatz, Handy, Reise, Gerät …
table "asset__asset" {
  schema = schema.main
  column "company_code" { type = text }
  column "asset_id"     { type = text }
  column "asset_type"   { type = text }
  column "designation"  { type = text }` +
	optional("identifier", "manufacturer", "model", "street", "zip", "city", "country", "currency", "note") + `
  column "purchase_date" {
    type = date
    null = true
  }
  column "purchase_price" {
    type = double
    null = true
  }
  column "warranty_until" {
    type = date
    null = true
  }` + optional("status") + `
  column "valid_from" { type = date }
  column "valid_to"   { type = date }` + optional("changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.asset_id] }
  index "asset__asset_type_idx" {
    columns = [column.company_code, column.asset_type]
  }
}

# Bestandteile (Zeitscheibe): Auto ← Sommer- und Winterreifen. Ein Objekt ist
# zu einem Zeitpunkt Bestandteil höchstens eines anderen.
table "asset__part" {
  schema = schema.main
  column "company_code" { type = text }
  column "asset_id"     { type = text }
  column "part_id"      { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }` + optional("note") + `
  primary_key { columns = [column.company_code, column.asset_id, column.part_id, column.valid_from] }
  index "asset__part_part" {
    columns = [column.company_code, column.part_id]
  }
  foreign_key "asset__part_asset_fk" {
    columns     = [column.company_code, column.asset_id]
    ref_columns = [table.asset__asset.column.company_code, table.asset__asset.column.asset_id]
  }
  foreign_key "asset__part_part_fk" {
    columns     = [column.company_code, column.part_id]
    ref_columns = [table.asset__asset.column.company_code, table.asset__asset.column.asset_id]
  }
}

# Personen (Geschäftspartner) in einer Rolle am Objekt, mit Zeitscheibe:
# Eigentümer, Nutzer, Halter, Fahrer …
table "asset__person" {
  schema = schema.main
  column "company_code" { type = text }
  column "asset_id"     { type = text }
  column "role_code"    { type = text }
  column "partner_id"   { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }` + optional("note") + `
  primary_key { columns = [column.company_code, column.asset_id, column.role_code, column.partner_id, column.valid_from] }
  index "asset__person_partner" {
    columns = [column.partner_id]
  }
  foreign_key "asset__person_asset_fk" {
    columns     = [column.company_code, column.asset_id]
    ref_columns = [table.asset__asset.column.company_code, table.asset__asset.column.asset_id]
  }
  foreign_key "asset__person_role_fk" {
    columns     = [column.company_code, column.role_code]
    ref_columns = [table.asset__asset_role.column.company_code, table.asset__asset_role.column.code]
  }
}
`
