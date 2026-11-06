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
  }
  # seit 0.1.2: Kosten enthalten den CO2-Preis (Brennstoff, Fernwärme) – Maßstab für die CO2-Kosten der Nutzer
  column "co2" {
    type    = boolean
    default = false
  }` + optional("cost_group", "key_code", "text") + optNum("ww_percent")},
	// seit 0.1.2: Stufenmodelle CO2KostAufG (Stufen in heatcost__co2_stage)
	{Table: "co2_scale", Object: "HeatCostCo2Scale", Title: "CO2-Stufenmodelle", Extra: optional("note") + optNum("kg_decimals")},
	// seit 0.1.4: Gradtagszahlen (Werte je Jahr und Monat in heatcost__degree_day)
	{Table: "degree_table", Object: "HeatCostDegreeTable", Title: "Gradtagszahlen", Extra: optional("note")},
	// seit 0.1.1: Vorgaben für Abrechnungseinheiten (Grundanteil, Schlüssel, Warmwasseranteil)
	{Table: "profile", Object: "HeatCostProfile", Title: "Vorgaben für Abrechnungseinheiten", Extra: optional("heat_area_key", "heat_usage_key",
		"ww_method", "ww_area_key", "ww_usage_key", "note", "sender_roles", "tenant_roles") + optNum("heat_base_percent", "ww_base_percent", "ww_fixed_percent") + settingColumns},
	{Table: "key", Object: "HeatCostKey", Title: "Verteilerschlüssel", Extra: `
  # Herkunft der Werte der Nutzer: MANUAL, AREA (Bemessung), CONSUMPTION (Verbrauch der Sparte), DEVICES (Zahl der Geräte der Sparte)
  column "source" {
    type    = text
    default = "MANUAL"
  }
  column "decimals" {
    type    = bigint
    default = 3
  }
  # seit 0.1.4: bei Nutzerwechsel nach Kalendertagen gewichten (Flächen, Geräte)
  column "time_weighted" {
    type = boolean
    null = true
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
	"ww_area_key", "ww_usage_key", "ww_groups", "ww_group_key") + optNum("ww_fixed_percent") + settingColumns

// seit 0.1.2: Rundung, Formel § 9 Abs. 2, CO2-Kosten (Einheit, Abrechnung, Vorgabe)
var settingColumns = optional("ww_round", "group_rest", "co2_mode", "co2_scale", "co2_area_key", "heat_change", "degree_table", "time_round") + optNum("ww_temp", "co2_ww_decimals", "ww_decimals")

// seit 0.2.0: monatliche Verbrauchsinformation (§ 6a HeizkostenV) an der Einheit
var infoColumns = optional("info_keys", "info_template", "info_doc_type") + optNum("info_due_days", "info_cut_percent")

// seit 0.1.1: Vorgabe der Einheit
var unitProfile = optional("profile", "template", "doc_type", "nk_definition", "nk_heat_account", "nk_co2_account", "sender_roles", "tenant_roles") // Druck und Übergabe seit 0.1.3, Rollen seit 0.1.6

var schemaHCL = catalogHCL() + `
table "heatcost__degree_day" {
  schema = schema.main
  column "company_code" { type = text }
  column "table_code"   { type = text }
  column "year"         { type = bigint }
  column "month"        { type = bigint }
  column "promille"     { type = double }` + optional("note") + `
  primary_key { columns = [column.company_code, column.table_code, column.year, column.month] }
  foreign_key "heatcost__degree_day_table" {
    columns     = [column.company_code, column.table_code]
    ref_columns = [table.heatcost__degree_table.column.company_code, table.heatcost__degree_table.column.code]
  }
}

table "heatcost__co2_stage" {
  schema = schema.main
  column "company_code"     { type = text }
  column "scale"            { type = text }
  column "stage_no"         { type = bigint }
  column "from_kg"          { type = double }
  column "landlord_percent" { type = double }` + optional("note") + `
  primary_key { columns = [column.company_code, column.scale, column.stage_no] }
  foreign_key "heatcost__co2_stage_scale" {
    columns     = [column.company_code, column.scale]
    ref_columns = [table.heatcost__co2_scale.column.company_code, table.heatcost__co2_scale.column.code]
  }
}

table "heatcost__unit" {
  schema = schema.main
  column "company_code" { type = text }
  column "unit_id"      { type = text }
  column "designation"  { type = text }
  column "status" {
    type    = text
    default = "ACTIVE"
  }` + optional("entity_id", "building_id", "partner_id", "external_no", "note", "changed_at", "changed_by") + blockColumns + unitProfile + infoColumns + `
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
  }
  # seit 0.1.4: keine Zwischenablesung beim Nutzerwechsel (§ 9b Abs. 3)
  column "no_interim" {
    type    = boolean
    default = false
  }` + optional("object_id", "groups", "location", "note", "partner_id") + `
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
	optNum("ww_energy", "total_energy", "ww_share", "total_cost", "heat_cost", "ww_cost") + blockColumns +
	optNum("ww_volume", "co2_cost", "co2_kg", "co2_kg_m2", "co2_percent", "co2_landlord") + optional("co2_stage") + `
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
  column "amount"  { type = double }`+optional("key_code", "scope", "unit", "cost_group", "factor_text")+optNum("total", "basis", "price", "value", "share", "factor")+`
  column "apportionable" {
    type    = boolean
    default = true
  }`) + runChild("co2", "column.user_no", `
  column "user_no" { type = text }`+optNum("share", "emissions", "cost", "percent", "landlord", "tenant")+`
  column "deducted" {
    type    = boolean
    default = false
  }`) + infoHCL

func dateCols(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = date
    null = true
  }`, c)
	}
	return b.String()
}

// seit 0.2.0: monatliche Verbrauchsinformation (§ 6a HeizkostenV) einer Einheit für einen
// Monat; Faktoren kWh je Einheit aus der Abrechnung base_run (oder von Hand); Status
// COMPUTED (gerechnet) bzw. PROVIDED (bereitgestellt, Nachweis gegen die Kürzung § 12).
var infoHCL = `
table "heatcost__info" {
  schema = schema.main
  column "company_code" { type = text }
  column "info_id"      { type = text }
  column "unit_id"      { type = text }
  column "month"        { type = text }
  column "period_from"  { type = date }
  column "period_to"    { type = date }
  column "status" {
    type    = text
    default = "COMPUTED"
  }` + dateCols("due_on", "provided_on") + optional("base_run", "factor_note", "message", "created_at", "created_by", "provided_by") + optNum("heat_factor", "ww_factor", "cut_percent", "risk") + `
  primary_key { columns = [column.company_code, column.info_id] }
  index "heatcost__info_month" {
    columns = [column.company_code, column.unit_id, column.month]
  }
  foreign_key "heatcost__info_unit" {
    columns     = [column.company_code, column.unit_id]
    ref_columns = [table.heatcost__unit.column.company_code, table.heatcost__unit.column.unit_id]
  }
}

# Nutzer im Monat: Fläche, Empfänger, Kürzungsrisiko (Satz × Heiz- und Warmwasserkosten des
# Nutzers in der Abrechnung base_run) und das abgelegte Dokument.
table "heatcost__info_user" {
  schema = schema.main
  column "company_code" { type = text }
  column "info_id"      { type = text }
  column "user_no"      { type = text }` + optional("object_id", "designation", "recipient", "contracts", "document_id", "note") + optNum("area", "annual_cost", "risk") + `
  primary_key { columns = [column.company_code, column.info_id, column.user_no] }
  foreign_key "heatcost__info_user_info" {
    columns     = [column.company_code, column.info_id]
    ref_columns = [table.heatcost__info.column.company_code, table.heatcost__info.column.info_id]
  }
}

# Zeile je Nutzer und Schlüssel (Sparte): Verbrauch des Monats, Vormonat, Vorjahresmonat und
# Durchschnittsnutzer (je m² der Einheit × Fläche des Nutzers), jeweils auch in kWh.
table "heatcost__info_line" {
  schema = schema.main
  column "company_code" { type = text }
  column "info_id"      { type = text }
  column "user_no"      { type = text }
  column "key_code"     { type = text }` + optional("unit", "note") +
	optNum("value", "kwh", "prev_month", "prev_month_kwh", "prev_year", "prev_year_kwh", "average", "average_kwh") + `
  column "complete" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.info_id, column.user_no, column.key_code] }
  foreign_key "heatcost__info_line_info" {
    columns     = [column.company_code, column.info_id]
    ref_columns = [table.heatcost__info.column.company_code, table.heatcost__info.column.info_id]
  }
}
`
