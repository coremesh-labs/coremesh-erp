package export

import (
	"fmt"
	"strings"
)

// Schema der Sichten (Atlas HCL, Präfix export__, keine DROPs).
//
//	view_type ──< rule          Sichtart (Katalog je Buchungskreis) mit Auswahlregeln
//	target                      Ziele (Verzeichnis, zip)
//	view                        gespeicherte Sicht: Sichtart + Parameter
//	run ──< item                Lauf zu einem Stichtag mit seinen Dokumenten

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

func intCols(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = bigint
    null = true
  }`, c)
	}
	return b.String()
}

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

func boolCol(name string, def bool) string {
	return fmt.Sprintf(`
  column %q {
    type    = boolean
    default = %v
  }`, name, def)
}

const catalogTail = `
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.code] }
}
`

var schemaHCL = `
# Sichtart: was zusammengestellt wird (Regeln), wie gegliedert (Ordner) und benannt (Datei).
# params: Parameter, die eine Sicht dieser Art braucht (Komma: period, building, object, contract, partner, process).
table "export__view_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }` + optional("params", "folder_pattern", "file_pattern", "sort_by", "note") + catalogTail + `
# Regel einer Sichtart: Dokumente des Buchungskreises (abgelegt), eingeschränkt nach
# Dokumentart, Kategorie, Ablage (Anfang der Nummer), Bezug (Object + Parameter der Sicht),
# Partner und Zeitraum; exclude nimmt Treffer wieder heraus; folder übersteuert die Gliederung.
table "export__rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "view_type"    { type = text }
  column "rule_no"      { type = bigint }` + optional("name", "doc_type", "doc_category", "file_account", "ref_object", "ref_param", "folder") +
	boolCol("by_partner", false) + boolCol("in_period", true) + boolCol("exclude", false) + boolCol("is_active", true) + `
  primary_key { columns = [column.company_code, column.view_type, column.rule_no] }
  foreign_key "export__rule_type_fk" {
    columns     = [column.company_code, column.view_type]
    ref_columns = [table.export__view_type.column.company_code, table.export__view_type.column.code]
  }
}

# Ziel: DIRECTORY (Ordner auf dem Server) oder ZIP (Download; mit Pfad als Datei dort).
table "export__target" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "kind"         { type = text }` + optional("path", "note") + catalogTail + `
# Gespeicherte Sicht, z. B. „Steuerbüro 2025“.
table "export__view" {
  schema = schema.main
  column "company_code" { type = text }
  column "view_id"      { type = text }
  column "name"         { type = text }
  column "view_type"    { type = text }` + dateCols("date_from", "date_to") +
	optional("building_id", "object_id", "contract_id", "partner_id", "process_id", "target", "note", "last_run") + boolCol("is_active", true) + `
  primary_key { columns = [column.company_code, column.view_id] }
  foreign_key "export__view_type_fk" {
    columns     = [column.company_code, column.view_type]
    ref_columns = [table.export__view_type.column.company_code, table.export__view_type.column.code]
  }
}

# Lauf: die Sicht zu einem Stichtag ausgeführt; params hält die Parameter fest (JSON).
# Status OPEN (nachbessern) bzw. EXPORTED; Ausgaben stehen im Protokoll (log).
table "export__run" {
  schema = schema.main
  column "company_code" { type = text }
  column "run_id"       { type = text }
  column "view_id"      { type = text }
  column "name"         { type = text }
  column "status"       { type = text }
  column "key_date"     { type = date }
  column "created_at"   { type = text }` +
	optional("view_type", "params", "created_by", "exported_at", "exported_by", "target", "export_path", "manifest_sha256", "message", "log") +
	intCols("item_count", "excluded_count", "total_size") + `
  primary_key { columns = [column.company_code, column.run_id] }
}

# Dokument eines Laufs: Ordner und Dateiname nach der Sichtart, Prüfsumme aus dem DMS.
table "export__item" {
  schema = schema.main
  column "company_code" { type = text }
  column "run_id"       { type = text }
  column "item_no"      { type = bigint }
  column "document_id"  { type = text }` +
	optional("folder", "file_name", "title", "doc_type", "doc_category", "partner_id", "partner_name", "file_account", "sha256", "reason") +
	dateCols("doc_date") + intCols("rule_no", "size") + boolCol("excluded", false) + boolCol("added", false) + `
  primary_key { columns = [column.company_code, column.run_id, column.item_no] }
  index "export__item_document" {
    columns = [column.company_code, column.run_id, column.document_id]
  }
  foreign_key "export__item_run_fk" {
    columns     = [column.company_code, column.run_id]
    ref_columns = [table.export__run.column.company_code, table.export__run.column.run_id]
  }
}
`
