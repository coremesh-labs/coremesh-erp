package heatcost

import (
	"fmt"
	"strings"
)

// Schema der Heizkostenabrechnung (Atlas HCL, Präfix heatcost__, keine DROPs).
//
// Stammdaten: Abrechnungseinheit (unit) mit Nutzergruppen (user_group) und
// Nutzern (user). Eine Abrechnung (run) hat ihre Kostenaufstellung (cost),
// Verteilungsgrößen der Liegenschaft je Schlüssel und Gruppe (basis), Werte der
// Nutzer je Schlüssel (value), den Rechenweg (step) und das Ergebnis je Nutzer
// (result). Rechenweg und Ergebnis schreibt nur „Rechnen“.

type catalogTable struct {
	Table, Object, Title string
	Extra                string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "cost_group", Object: "HeatCostGroup", Title: "Kostengruppen"},
	{Table: "cost_kind", Object: "HeatCostKind", Title: "Kostenarten", Extra: `
  # Aufteilung: SPLIT (nach Warmwasseranteil), HEATING, HOTWATER, KEY (eigener Schlüssel), DIRECT (an einen Nutzer)
  column "allocation" {
    type    = text
    default = "SPLIT"
  }
  column "apportionable" {
    type    = boolean
    default = true
  }` + optional("cost_group", "key_code", "text") + optNum("ww_percent")},
	{Table: "key", Object: "HeatCostKey", Title: "Verteilerschlüssel", Extra: `
  # Herkunft der Werte der Nutzer: MANUAL, AREA (Bemessung), CONSUMPTION (Verbrauch der Sparte), DEVICES (Zahl der Geräte der Sparte)
  column "source" {
    type    = text
    default = "MANUAL"
  }
  column "decimals" {
    type    = bigint
    default = 3
  }` + optional("source_code", "unit")},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "heatcost__%s" {
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

func optNum(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = double
    null = true
  }`, c)
	}
	return b.String()
}

// runChild: Tabelle einer Abrechnung (Fremdschlüssel auf die Abrechnung).
func runChild(table, key, cols string) string {
	return fmt.Sprintf(`
table "heatcost__%s" {
  schema = schema.main
  column "company_code" { type = text }
  column "run_id"       { type = text }%s
  primary_key { columns = [column.company_code, column.run_id, %s] }
  foreign_key "heatcost__%s_run" {
    columns     = [column.company_code, column.run_id]
    ref_columns = [table.heatcost__run.column.company_code, table.heatcost__run.column.run_id]
  }
}
`, table, cols, key, table)
}

// Einstellungen der Blöcke Heizung und Warmwasser (an der Einheit, in die Abrechnung kopiert).
var blockColumns = `
  column "heat_base_percent" {
    type    = double
    default = 30
  }
  column "ww_base_percent" {
    type    = double
    default = 30
  }
  column "ww_method" {
    type    = text
    default = "METER"
  }` + optional("heat_area_key", "heat_usage_key", "heat_groups", "heat_group_key",
	"ww_area_key", "ww_usage_key", "ww_groups", "ww_group_key") + optNum("ww_fixed_percent")

var schemaHCL = catalogHCL() + `
table "heatcost__unit" {
  schema = schema.main
  column "company_code" { type = text }
  column "unit_id"      { type = text }
  column "designation"  { type = text }
  column "status" {
    type    = text
    default = "ACTIVE"
  }` + optional("entity_id", "building_id", "partner_id", "external_no", "note", "changed_at", "changed_by") + blockColumns + `
  primary_key { columns = [column.company_code, column.unit_id] }
}

table "heatcost__user_group" {
  schema = schema.main
  column "company_code" { type = text }
  column "unit_id"      { type = text }
  column "code"         { type = text }
  column "designation"  { type = text }
  column "sort_order" {
    type    = bigint
    default = 0
  }` + optional("note") + `
  primary_key { columns = [column.company_code, column.unit_id, column.code] }
  foreign_key "heatcost__user_group_unit" {
    columns     = [column.company_code, column.unit_id]
    ref_columns = [table.heatcost__unit.column.company_code, table.heatcost__unit.column.unit_id]
  }
}

table "heatcost__user" {
  schema = schema.main
  column "company_code" { type = text }
  column "unit_id"      { type = text }
  column "user_no"      { type = text }
  column "designation"  { type = text }
  column "valid_from" {
    type    = date
    default = "1900-01-01"
  }
  column "valid_to" {
    type    = date
    default = "9999-12-31"
  }` + optional("object_id", "groups", "location", "note") + `
  primary_key { columns = [column.company_code, column.unit_id, column.user_no] }
  foreign_key "heatcost__user_unit" {
    columns     = [column.company_code, column.unit_id]
    ref_columns = [table.heatcost__unit.column.company_code, table.heatcost__unit.column.unit_id]
  }
}

table "heatcost__run" {
  schema = schema.main
  column "company_code" { type = text }
  column "run_id"       { type = text }
  column "unit_id"      { type = text }
  column "period_from"  { type = date }
  column "period_to"    { type = date }
  column "status" {
    type    = text
    default = "DRAFT"
  }` + optional("title", "predecessor", "note", "message", "computed_at", "changed_at", "changed_by") +
	optNum("ww_energy", "total_energy", "ww_share", "total_cost", "heat_cost", "ww_cost") + blockColumns + `
  primary_key { columns = [column.company_code, column.run_id] }
  foreign_key "heatcost__run_unit" {
    columns     = [column.company_code, column.unit_id]
    ref_columns = [table.heatcost__unit.column.company_code, table.heatcost__unit.column.unit_id]
  }
}
` + runChild("cost", "column.line_no", `
  column "line_no"    { type = bigint }
  column "cost_kind"  { type = text }
  column "text"       { type = text }
  column "amount"     { type = double }
  column "allocation" { type = text }`+optional("cost_group", "key_code", "scope", "user_no", "invoice_ref", "note")+optNum("ww_percent")+`
  column "sort_order" {
    type    = bigint
    default = 0
  }`) + runChild("basis", "column.key_code, column.scope", `
  column "key_code" { type = text }
  # Nutzergruppe oder * (ganze Liegenschaft)
  column "scope"    { type = text }
  column "total"    { type = double }`+optional("note")) + runChild("value", "column.user_no, column.key_code", `
  column "user_no"  { type = text }
  column "key_code" { type = text }
  column "value"    { type = double }
  column "source" {
    type    = text
    default = "MANUAL"
  }`+optional("note")) + runChild("step", "column.step_no", `
  column "step_no" { type = bigint }
  column "block"   { type = text }
  column "text"    { type = text }
  column "amount"  { type = double }`+optional("key_code", "scope", "unit")+optNum("basis", "price")) + runChild("result", "column.user_no, column.line_no", `
  column "user_no" { type = text }
  column "line_no" { type = bigint }
  column "block"   { type = text }
  column "text"    { type = text }
  column "amount"  { type = double }`+optional("key_code", "scope", "unit", "cost_group")+optNum("total", "basis", "price", "value", "share")+`
  column "apportionable" {
    type    = boolean
    default = true
  }`)
