package process

import (
	"fmt"
	"strings"
)

// Schema der Vorgänge (Atlas HCL, Präfix process__, keine DROPs).
//
//   - Kataloge je Buchungskreis (catalogTables): Schlüssel (company_code, code).
//   - Schrittvorlagen je Vorgangsart (type_step) und Sichten (view).
//   - Vorgang (process) mit Partnern, Schritten und Verlauf.
//   - Hinweise in der App (notice) und Ereignisjournal (event, laufende Nummer).

// catalogTable: ein Katalog je Buchungskreis mit Zusatzspalten.
type catalogTable struct {
	Table, Object, Title string
	Extra                string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "type", Object: "ProcessType", Title: "Vorgangsarten", Extra: `
  # Kürzel der Vorgangsnummer (z. B. WV → WV-2026-0001)
  column "id_prefix" { type = text }
  # Abschluss erst, wenn alle Teilvorgänge abgeschlossen sind
  column "children_first" {
    type    = boolean
    default = true
  }` + optional("perspective", "escalate_to", "event_label", "keywords", "note") +
		intCols("follow_up_days", "ack_days", "answer_days", "idle_days", "escalate_days")},
	{Table: "status", Object: "ProcessStatus", Title: "Status der Vorgänge", Extra: `
  # OPEN offen, WAITING wartet (Erinnerungsdatum), CLOSED abgeschlossen
  column "kind" { type = text }`},
	{Table: "priority", Object: "ProcessPriority", Title: "Prioritäten"},
	{Table: "role", Object: "ProcessRole", Title: "Rollen am Vorgang"},
	{Table: "resolution", Object: "ProcessResolution", Title: "Erledigungsarten"},
	{Table: "perspective", Object: "ProcessPerspective", Title: "Perspektiven"},
	{Table: "entry_kind", Object: "ProcessEntryKind", Title: "Arten der Verlaufseinträge", Extra: `
  # vom System geschrieben (Statuswechsel, Schritt erledigt …)
  column "is_system" {
    type    = boolean
    default = false
  }`},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "process__%s" {
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

// intCols: Ganzzahlen, die leer bleiben dürfen.
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

// dateCols: Datumsspalten, die leer bleiben dürfen.
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

var schemaHCL = catalogHCL() + `
# Schrittvorlage je Vorgangsart: Frist ab Eröffnung (OPEN), ab dem vorigen
# Schritt (PREV) oder vor/nach dem Stichtag des Vorgangs (EVENT, Tage auch negativ).
table "process__type_step" {
  schema = schema.main
  column "company_code" { type = text }
  column "process_type" { type = text }
  column "step_no"      { type = bigint }
  column "name"         { type = text }
  column "due_basis"    { type = text }` + optional("checklist", "role_hint") + intCols("due_days") + `
  column "required" {
    type    = boolean
    default = true
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.process_type, column.step_no] }
  foreign_key "process__type_step_type_fk" {
    columns     = [column.company_code, column.process_type]
    ref_columns = [table.process__type.column.company_code, table.process__type.column.code]
  }
}

# Sicht: gespeicherter Filter der Vorgangsliste (Katalog je Buchungskreis).
table "process__view" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }` + optional("status_kinds", "types", "perspective") + intCols("due_days", "idle_days") + `
  column "only_mine" {
    type    = boolean
    default = false
  }
  column "overdue_only" {
    type    = boolean
    default = false
  }
  column "escalated_only" {
    type    = boolean
    default = false
  }
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

# Vorgang. next_due ist die nächste Frist (Wiedervorlage, offene Schritte,
# Eingangsbestätigung, Antwort) – sortieren, Sichten und Zeitgeber nutzen sie.
# short_name: Kurzbezeichnung (Ordner im DMS „Nummer Kurzbezeichnung“).
# parent_id: übergeordneter Vorgang (Teilvorgang); sub_due: nächste Frist der
# offenen Teilvorgänge (alle Ebenen), nur zur Anzeige.
table "process__process" {
  schema = schema.main
  column "company_code" { type = text }
  column "process_id"   { type = text }
  column "process_type" { type = text }
  column "subject"      { type = text }
  column "status"       { type = text }` +
	optional("short_name", "perspective", "description", "priority", "assignee", "deputy", "partner_id", "contract_id", "object_id",
		"entity_id", "asset_id", "parent_id", "waiting_for", "next_step", "resolution", "closed_by", "last_activity", "created_by", "changed_at", "changed_by") +
	dateCols("event_date", "opened_on", "ack_due", "acknowledged_on", "answer_due", "follow_up", "next_due", "sub_due", "closed_on") + `
  column "escalation" {
    type    = bigint
    default = 0
  }
  primary_key { columns = [column.company_code, column.process_id] }
  index "process__process_due" {
    columns = [column.company_code, column.next_due]
  }
  index "process__process_assignee" {
    columns = [column.assignee]
  }
  index "process__process_parent" {
    columns = [column.company_code, column.parent_id]
  }
  foreign_key "process__process_type_fk" {
    columns     = [column.company_code, column.process_type]
    ref_columns = [table.process__type.column.company_code, table.process__type.column.code]
  }
}

# Partner in Rollen am Vorgang (Anfragender, Mieter, Eigentümer, Handwerker …).
table "process__partner" {
  schema = schema.main
  column "company_code" { type = text }
  column "process_id"   { type = text }
  column "role_code"    { type = text }
  column "partner_id"   { type = text }` + optional("note") + `
  primary_key { columns = [column.company_code, column.process_id, column.role_code, column.partner_id] }
  index "process__partner_partner" {
    columns = [column.partner_id]
  }
  foreign_key "process__partner_process_fk" {
    columns     = [column.company_code, column.process_id]
    ref_columns = [table.process__process.column.company_code, table.process__process.column.process_id]
  }
}

# Schritte des Vorgangs (aus der Schrittvorlage, ergänzbar).
table "process__step" {
  schema = schema.main
  column "company_code" { type = text }
  column "process_id"   { type = text }
  column "step_no"      { type = bigint }
  column "name"         { type = text }
  column "due_basis"    { type = text }
  column "status"       { type = text }` + optional("checklist", "role_hint", "done_by", "note") + intCols("due_days") +
	dateCols("due_date", "done_on") + `
  column "required" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.process_id, column.step_no] }
  index "process__step_due" {
    columns = [column.company_code, column.due_date]
  }
  foreign_key "process__step_process_fk" {
    columns     = [column.company_code, column.process_id]
    ref_columns = [table.process__process.column.company_code, table.process__process.column.process_id]
  }
}

# Verlauf: Notizen, Anrufe, Nachrichten und Systemeinträge.
table "process__entry" {
  schema = schema.main
  column "company_code" { type = text }
  column "process_id"   { type = text }
  column "entry_no"     { type = bigint }
  column "kind"         { type = text }
  column "entry_at"     { type = text }` + optional("text", "user_id") + `
  primary_key { columns = [column.company_code, column.process_id, column.entry_no] }
  foreign_key "process__entry_process_fk" {
    columns     = [column.company_code, column.process_id]
    ref_columns = [table.process__process.column.company_code, table.process__process.column.process_id]
  }
}

# Hinweise in der App je Benutzer (fällig, überschritten, Eskalation, zugewiesen …).
table "process__notice" {
  schema = schema.main
  column "notice_id"    { type = bigint }
  column "company_code" { type = text }
  column "user_id"      { type = text }
  column "kind"         { type = text }
  column "created_at"   { type = text }` + optional("process_id", "text") + `
  column "is_open" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.notice_id] }
  index "process__notice_user" {
    columns = [column.user_id, column.is_open]
  }
}

# Ereignisjournal (Ausgang): jede Änderung und jede Frist mit laufender Nummer.
# Andere Systeme lesen ab einer Nummer (since); pushed_at: an den
# Event-Dispatcher gemeldet (nach dem Commit, durch den Zeitgeber).
table "process__event" {
  schema = schema.main
  column "seq"          { type = bigint }
  column "company_code" { type = text }
  column "event"        { type = text }
  column "occurred_at"  { type = text }` + optional("process_id", "user_id", "data", "pushed_at") + `
  primary_key { columns = [column.seq] }
  index "process__event_pushed" {
    columns = [column.pushed_at]
  }
}
`
