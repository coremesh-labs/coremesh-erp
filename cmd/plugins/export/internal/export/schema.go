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
  column "name"         { type = text }` + optional("params", "folder_pattern", "file_pattern", "sort_by", "note",
	// seit 0.3.0: Datasets – Schlüssel, Rhythmus, Name, Erzeugen vor dem Sammeln, Ende und Lebensdauer
	"key_by", "rhythm", "dataset_pattern", "refresh_call", "end_field",
	// seit 0.4.0: Freigabe (Vorschlag) für Sichten ohne eigene Freigaben
	"grant_role", "grant_rule") + intCols("lifetime_months", "end_days") + boolCol("replace_same", false) + catalogTail + `
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
  column "kind"         { type = text }` + optional("path", "note",
	// seit 0.4.0: Objektspeicher (Ablage für die PWA): Anbieter, Adressen, Aufbewahrung
	"provider", "public_url", "pwa_url") + intCols("keep_days", "warn_mb") + catalogTail + `
# Gespeicherte Sicht, z. B. „Steuerbüro 2025“.
table "export__view" {
  schema = schema.main
  column "company_code" { type = text }
  column "view_id"      { type = text }
  column "name"         { type = text }
  column "view_type"    { type = text }` + dateCols("date_from", "date_to") +
	optional("building_id", "object_id", "contract_id", "partner_id", "process_id", "target", "note", "last_run",
		// seit 0.3.0: Schlüssel und Rhythmus (leer = Sichtart), Sammelzeiger für neue Schlüssel
		"key_by", "rhythm", "pointer", "checked_at") + intCols("lifetime_months", "event_seq") + boolCol("is_active", true) + `
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
	optional("view_type", "params", "created_by", "exported_at", "exported_by", "target", "export_path", "manifest_sha256", "message", "log",
		"dataset_id") + intCols("item_count", "excluded_count", "total_size", "state_no", "new_count", "marked_count") + `
  primary_key { columns = [column.company_code, column.run_id] }
}

# Dokument eines Laufs: Ordner und Dateiname nach der Sichtart, Prüfsumme aus dem DMS.
table "export__item" {
  schema = schema.main
  column "company_code" { type = text }
  column "run_id"       { type = text }
  column "item_no"      { type = bigint }
  column "document_id"  { type = text }` +
	optional("folder", "file_name", "title", "doc_type", "doc_category", "partner_id", "partner_name", "file_account", "sha256", "reason",
		// seit 0.3.0: Kennzeichen (storniert, ersetzt, gelöscht …), ersetzt durch, Stand der Aufnahme
		"mark", "mark_note", "mark_on", "replaced_by", "first_run", "source_name") +
	dateCols("doc_date") + intCols("rule_no", "size") + boolCol("excluded", false) + boolCol("added", false) + boolCol("is_new", false) + `
  primary_key { columns = [column.company_code, column.run_id, column.item_no] }
  index "export__item_document" {
    columns = [column.company_code, column.run_id, column.document_id]
  }
  foreign_key "export__item_run_fk" {
    columns     = [column.company_code, column.run_id]
    ref_columns = [table.export__run.column.company_code, table.export__run.column.run_id]
  }
}
` + datasetHCL + ablageHCL

// Dataset (seit 0.3.0): dauerhaftes Ergebnis einer Sicht je Schlüssel; die Läufe sind
// seine Stände. Kennzeichenregeln markieren Elemente (z. B. storniert) über den Status
// eines verknüpften Objekts.
var datasetHCL = `
table "export__dataset" {
  schema = schema.main
  column "company_code" { type = text }
  column "dataset_id"   { type = text }
  column "view_id"      { type = text }
  column "name"         { type = text }
  column "status"       { type = text }
  column "created_at"   { type = text }` +
	optional("key_value", "view_type", "key_by", "rhythm", "partner_id", "process_id", "pointer", "created_by", "closed_at", "deleted_at", "deleted_by",
		"last_run", "last_delivery", "refreshed_at", "note", "log",
		// seit 0.4.0: in der Ablage (Objektspeicher) – ID, Ziel, Zeitpunkt, letzter Index
		"store_id", "store_target", "store_at", "store_index") +
	dateCols("period_from", "period_to", "next_due", "delete_after") + intCols("item_count", "marked_count", "state_count", "store_version") + boolCol("pending", false) + `
  primary_key { columns = [column.company_code, column.dataset_id] }
  index "export__dataset_key" {
    columns = [column.company_code, column.view_id, column.key_value]
  }
}

table "export__mark_rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }` + optional("ref_object", "key_field", "status_field", "statuses", "mark_label", "note") + catalogTail

// Ablage für die PWA (seit 0.4.0, Konzept Ablage_Dateiformat_Konzept.txt Abschnitt 7):
// Rollen und Teilnehmer, Freigaben je Sicht, Zugriffe, Schlüssel, Dateien der Ablage.
var ablageHCL = `
table "export__role" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }` + optional("note") + boolCol("pin_required", false) + catalogTail + `
table "export__content_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "content_type" { type = text }
  column "name"         { type = text }` + optional("packing", "extension", "note") + `
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.content_type] }
}

table "export__participant" {
  schema = schema.main
  column "company_code"   { type = text }
  column "participant_id" { type = text }
  column "name"           { type = text }
  column "status"         { type = text }
  column "created_at"     { type = text }` +
	optional("partner_id", "user_id", "email", "target", "start_id", "uid", "user_key", "user_public", "created_by", "setup_at", "setup_by",
		"locked_at", "start_hash", "start_at", "note", "log") + intCols("key_no", "start_version") + `
  primary_key { columns = [column.company_code, column.participant_id] }
}

table "export__participant_role" {
  schema = schema.main
  column "company_code"   { type = text }
  column "participant_id" { type = text }
  column "role_no"        { type = bigint }
  column "role"           { type = text }` + optional("partner_id", "note") + dateCols("valid_from", "valid_to") + `
  primary_key { columns = [column.company_code, column.participant_id, column.role_no] }
  foreign_key "export__participant_role_fk" {
    columns     = [column.company_code, column.participant_id]
    ref_columns = [table.export__participant.column.company_code, table.export__participant.column.participant_id]
  }
}

table "export__grant" {
  schema = schema.main
  column "company_code" { type = text }
  column "view_id"      { type = text }
  column "grant_no"     { type = bigint }
  column "role"         { type = text }
  column "rule"         { type = text }` + optional("module", "note") + dateCols("valid_to") + boolCol("is_active", true) + `
  primary_key { columns = [column.company_code, column.view_id, column.grant_no] }
}

table "export__access" {
  schema = schema.main
  column "company_code"   { type = text }
  column "participant_id" { type = text }
  column "dataset_id"     { type = text }
  column "role"           { type = text }
  column "since"          { type = text }` + optional("until") + `
  primary_key { columns = [column.company_code, column.participant_id, column.dataset_id] }
}

table "export__key" {
  schema = schema.main
  column "key_name"   { type = text }
  column "kind"       { type = text }
  column "created_at" { type = text }` + optional("company_code", "dataset_id", "participant_id", "public", "secret", "revoked_at", "note") + intCols("version") + `
  primary_key { columns = [column.key_name] }
}

table "export__store_file" {
  schema = schema.main
  column "company_code" { type = text }
  column "target"       { type = text }
  column "path"         { type = text }
  column "kind"         { type = text }` +
	optional("dataset_id", "document_id", "source_sha", "key_name", "content_type", "file_name", "written_at", "unused_since", "deleted_at") + intCols("size") + `
  primary_key { columns = [column.company_code, column.target, column.path] }
  index "export__store_file_dataset" {
    columns = [column.company_code, column.target, column.dataset_id]
  }
}
`
