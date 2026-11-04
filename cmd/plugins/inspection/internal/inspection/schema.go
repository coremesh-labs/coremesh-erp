package inspection

import (
	"fmt"
	"strings"
)

// Schema der Begehungen und Protokolle (Atlas HCL, Präfix inspection__, keine DROPs).
//
// Ein Protokoll (protocol) gehört zu einem Mietobjekt und – bei Ein- und Auszug –
// zu einem Mietvertrag. Es hat Teilnehmer (participant), Zustand je Raum und
// Bauteil (item, Vorschlag aus dem Raumbuch), Zählerstände je Zählwerk (meter,
// Vorschlag aus den technischen Objekten) und eine Prüfliste (check: Schlüssel,
// Rauchwarnmelder, besenrein …). Raumbuch und Zähler werden beim Übernehmen
// kopiert: das Protokoll hält fest, was am Tag war.

type catalogTable struct {
	Table, Object, Title string
	Extra                string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "protocol_type", Object: "InspectionType", Title: "Protokollarten", Extra: `
  # Kürzel der Protokollnummern (z. B. UE → UE-2026-0001)
  column "id_prefix" { type = text }` + optional("reading_type", "contract_types", "template", "doc_type", "defect_process_type", "intro", "closing") + `
  column "with_rooms" {
    type    = boolean
    default = true
  }
  column "with_meters" {
    type    = boolean
    default = true
  }`},
	{Table: "condition", Object: "InspectionCondition", Title: "Zustände", Extra: `
  # Mangel: kommt in die Mängelliste und in den Vorgang
  column "is_defect" {
    type    = boolean
    default = false
  }`},
	{Table: "checklist_item", Object: "InspectionChecklist", Title: "Prüfliste", Extra: `
  # für Protokollarten (Schlüssel durch Komma; leer = alle)` + optional("protocol_types", "hint") + `
  column "kind" {
    type    = text
    default = "CHECK"
  }`},
	{Table: "participant_role", Object: "InspectionRole", Title: "Rollen der Teilnehmer", Extra: `
  # Herkunft beim Übernehmen: CONTRACT (Partner des Vertrags), COMPANY (Partner des Buchungskreises), leer = von Hand` +
		optional("source") + `
  column "signs" {
    type    = boolean
    default = true
  }`},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "inspection__%s" {
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

// child: Tabelle eines Protokolls (Fremdschlüssel auf das Protokoll).
func child(table, key, cols string) string {
	return fmt.Sprintf(`
table "inspection__%s" {
  schema = schema.main
  column "company_code" { type = text }
  column "protocol_id"  { type = text }%s
  primary_key { columns = [column.company_code, column.protocol_id, %s] }
  foreign_key "inspection__%s_protocol" {
    columns     = [column.company_code, column.protocol_id]
    ref_columns = [table.inspection__protocol.column.company_code, table.inspection__protocol.column.protocol_id]
  }
}
`, table, cols, key, table)
}

const sortOrder = `
  column "sort_order" {
    type    = bigint
    default = 0
  }`

var schemaHCL = catalogHCL() + `
table "inspection__protocol" {
  schema = schema.main
  column "company_code"  { type = text }
  column "protocol_id"   { type = text }
  column "protocol_type" { type = text }
  column "object_id"     { type = text }
  column "protocol_date" { type = date }
  column "status" {
    type    = text
    default = "OPEN"
  }` + optional("contract_id", "partner_id", "title", "place", "note", "document", "process_id", "done_at", "done_by", "changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.protocol_id] }
}
` + child("participant", "column.line_no", `
  column "line_no" { type = bigint }
  column "role"    { type = text }
  column "name"    { type = text }`+optional("partner_id", "note")+`
  column "present" {
    type    = boolean
    default = true
  }
  column "signs" {
    type    = boolean
    default = true
  }`) + child("item", "column.line_no", `
  column "line_no"   { type = bigint }
  column "room_name" { type = text }`+optional("room_no", "component_type", "component_name", "description", "condition", "remark")+sortOrder+`
  column "is_defect" {
    type    = boolean
    default = false
  }`) + child("check", "column.item_code", `
  column "item_code" { type = text }
  column "name"      { type = text }
  column "kind" {
    type    = text
    default = "CHECK"
  }`+optional("value", "hint", "note")+sortOrder) + child("meter", "column.point_id, column.register_code", `
  column "point_id"      { type = text }
  column "register_code" { type = text }
  column "designation"   { type = text }`+optional("equipment_id", "meter_number", "location", "unit", "last_read_on", "note")+optNum("last_value", "value")+sortOrder+`
  column "reading_written" {
    type    = boolean
    default = false
  }`)
