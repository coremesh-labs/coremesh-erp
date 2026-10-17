package dms

// opt: Spalte darf leer sein.
const opt = `
    null = true`

// schemaHCL: Soll-Schema (Atlas-HCL) des Dokumentenmanagements. Tabellen mit dem Pflicht-Präfix dms__.
const schemaHCL = `
# Ablagefrist (Katalog): Jahre/Monate ab Dokumentdatum oder ab Ende des Kalenderjahres, oder dauerhaft
table "dms__retention" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "years" {
    type    = bigint
    default = 0
  }
  column "months" {
    type    = bigint
    default = 0
  }
  # DOC_DATE | END_OF_YEAR | FOREVER
  column "start" {
    type    = text
    default = "END_OF_YEAR"
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Dokumentkategorie (Katalog): zu welchem kaufmännischen Objekt ein Dokument gehört (Vertrag, Rechnung, Gebäude …)
table "dms__doc_category" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Hinweise aus dem Dokument (Katalog, seit 0.11.0): Platzhalter {hint.<code>} – Muster im
# erkannten Text (PATTERN) oder Wert aus einem anderen Object (LOOKUP), z. B. Name laut Kontoauszug
table "dms__hint" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "kind" { type = text }
  column "pattern" {
    type = text` + opt + `
  }
  column "check_rule" {
    type = text` + opt + `
  }
  column "strip" {
    type = text` + opt + `
  }
  column "exclude" {
    type = text` + opt + `
  }
  column "source" {
    type = text` + opt + `
  }
  column "match" {
    type = text` + opt + `
  }
  column "value_field" {
    type = text` + opt + `
  }
  column "partner_field" {
    type = text` + opt + `
  }
  column "sort_field" {
    type = text` + opt + `
  }
  # Erkennen (seit 0.12.0): Ziel, Punkte, Vergleich (MATCH)
  column "target" {
    type = text` + opt + `
  }
  column "points" {
    type    = bigint
    default = 0
  }
  column "normalize" {
    type = text` + opt + `
  }
  column "compare_field" {
    type = text` + opt + `
  }
  column "compare_hint" {
    type = text` + opt + `
  }
  column "candidate_field" {
    type = text` + opt + `
  }
  column "min_length" {
    type    = bigint
    default = 0
  }
  column "rule_group" {
    type = text` + opt + `
  }
  column "requires" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Dokumentart (Katalog): Art, mit der das Dokument am Objekt gespeichert wird; Kategorie,
# Vorschlag der Ablagefrist, Dokumentart der Verweise (Plugin document)
table "dms__doc_type" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "retention" {
    type = text` + opt + `
  }
  # Dokumentart (DocumentKind) der Verweise an Verträgen, Objekten …
  column "ref_kind" {
    type = text` + opt + `
  }
  column "category" {
    type = text` + opt + `
  }
  # Kürzel im Dateinamen ({doctype}), eindeutig
  column "short" {
    type = text` + opt + `
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
  foreign_key "dms__doc_type_retention_fk" {
    columns     = [column.retention]
    ref_columns = [table.dms__retention.column.code]
  }
  foreign_key "dms__doc_type_category_fk" {
    columns     = [column.category]
    ref_columns = [table.dms__doc_category.column.code]
  }
}

# Kaufmännische Objekte je Dokumentkategorie (z. B. Rechnung → Eingangsrechnung): Auswahl, Partner,
# Datum, Titel und Sachkonto des Objekts für die Übernahme ins Dokument
table "dms__category_object" {
  schema = schema.main
  column "category" { type = text }
  column "object" { type = text }
  column "label" { type = text }
  column "list_action" {
    type    = text
    default = "list"
  }
  # weitere Filter der Auswahl: feld=wert; feld=wert
  column "filters" {
    type    = text
    default = ""
  }
  # Feld des Objekts mit dem Geschäftspartner (Auswahl nach Partner, Übernahme)
  column "partner_field" {
    type    = text
    default = ""
  }
  # seit 0.3.7 ungenutzt: das Dokumentdatum kommt nur aus dem Dokument
  column "date_field" {
    type    = text
    default = ""
  }
  column "title_field" {
    type    = text
    default = ""
  }
  # Sachkonten: Object mit den Positionen, Verbindungsfeld, Feld des Sachkontos
  column "account_object" {
    type    = text
    default = ""
  }
  column "account_link" {
    type    = text
    default = ""
  }
  column "account_field" {
    type    = text
    default = ""
  }
  # Felder der Positionen mit Kostenart und „umlagefähig“ (Ablagefindung)
  column "cost_category_field" {
    type = text` + opt + `
  }
  column "allocable_field" {
    type = text` + opt + `
  }
  column "can_create" {
    type    = boolean
    default = true
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.category, column.object] }
  foreign_key "dms__category_object_category_fk" {
    columns     = [column.category]
    ref_columns = [table.dms__doc_category.column.code]
  }
}

# Veraltet (dms 0.2.0/0.2.1): Objekte je Dokumentart – Migrate übernimmt sie nach dms__category_object.
# Entfällt in einer späteren Version.
table "dms__doc_type_object" {
  schema = schema.main
  column "doc_type" { type = text }
  column "object" { type = text }
  column "label" { type = text }
  column "list_action" {
    type    = text
    default = "list"
  }
  # weitere Filter der Auswahl: feld=wert; feld=wert
  column "filters" {
    type    = text
    default = ""
  }
  # Feld des Objekts mit dem Geschäftspartner (Auswahl nach Partner, Übernahme)
  column "partner_field" {
    type    = text
    default = ""
  }
  column "date_field" {
    type    = text
    default = ""
  }
  column "title_field" {
    type    = text
    default = ""
  }
  # Sachkonten: Object mit den Positionen, Verbindungsfeld, Feld des Sachkontos
  column "account_object" {
    type    = text
    default = ""
  }
  column "account_link" {
    type    = text
    default = ""
  }
  column "account_field" {
    type    = text
    default = ""
  }
  column "can_create" {
    type    = boolean
    default = true
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.doc_type, column.object] }
  foreign_key "dms__doc_type_object_type_fk" {
    columns     = [column.doc_type]
    ref_columns = [table.dms__doc_type.column.code]
  }
}

# Ablageplan (wie ein Kontenplan, für alle Buchungskreise)
table "dms__plan" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "description" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Ablage im Ablageplan (hierarchisch über parent; Dokumente nur in der untersten Ebene)
table "dms__account" {
  schema = schema.main
  column "plan" { type = text }
  column "number" { type = text }
  column "name" { type = text }
  column "parent" {
    type = text` + opt + `
  }
  column "retention" {
    type = text` + opt + `
  }
  # Sortierschlüssel (Nummernteile mit führenden Nullen: 1.10 nach 1.9)
  column "sort_key" { type = text }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.plan, column.number] }
  index "dms__account_parent" { columns = [column.plan, column.parent] }
  foreign_key "dms__account_plan_fk" {
    columns     = [column.plan]
    ref_columns = [table.dms__plan.column.code]
  }
  foreign_key "dms__account_retention_fk" {
    columns     = [column.retention]
    ref_columns = [table.dms__retention.column.code]
  }
}

# Archiv: Ablage der Dateien (Art FILE: Verzeichnisbaum unter root)
table "dms__archive" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "kind" {
    type    = text
    default = "FILE"
  }
  column "root" { type = text }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Einstellungen je Buchungskreis: Ablageplan, Archiv, Dateiname, Prüfungen
table "dms__company" {
  schema = schema.main
  column "company_code" { type = text }
  column "plan" { type = text }
  column "archive" { type = text }
  # Kürzel im Dateinamen ({company}), z. B. VV
  column "short_name" {
    type = text` + opt + `
  }
  column "name_pattern" { type = text }
  # Verzeichnis unter der Ablage: PROPERTY (Objekt des Dokuments) | NONE
  column "object_dir" {
    type    = text
    default = "PROPERTY"
  }
  # Verzeichnis für Dokumente ohne Objekt
  column "object_dir_fallback" {
    type    = text
    default = "Allgemein"
  }
  # Ablagenummer im Dateinamen: so viele Ebenen ohne Punkt am Anfang (2: 5.2.0 → 52.0)
  column "name_account_levels" {
    type    = bigint
    default = 2
  }
  # Buchungen nur mit abgelegtem Dokument (Regeln dms__posting_rule)
  column "check_postings" {
    type    = boolean
    default = false
  }
  # Verträge und Konditionen nur mit Dokument (Regeln dms__contract_rule)
  column "check_contracts" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code] }
  foreign_key "dms__company_plan_fk" {
    columns     = [column.plan]
    ref_columns = [table.dms__plan.column.code]
  }
  foreign_key "dms__company_archive_fk" {
    columns     = [column.archive]
    ref_columns = [table.dms__archive.column.code]
  }
}

# Ablage im Buchungskreis (aktiviert; Frist und Archiv abweichend)
table "dms__account_company" {
  schema = schema.main
  column "company_code" { type = text }
  column "number" { type = text }
  column "retention" {
    type = text` + opt + `
  }
  column "archive" {
    type = text` + opt + `
  }
  column "is_blocked" {
    type    = boolean
    default = false
  }
  # Sachkonten (durch Komma): Dokumente zu Buchungen auf diese Konten gehören in diese Ablage
  column "gl_accounts" {
    type    = text
    default = ""
  }
  # Kostenarten (durch Komma) der Positionen; zusammen mit den Sachkonten (alle gesetzten müssen passen)
  column "cost_categories" {
    type    = text
    default = ""
  }
  # Umlagefähig: YES/NO (leer = egal)
  column "allocable" {
    type    = text
    default = ""
  }
  primary_key { columns = [column.company_code, column.number] }
  foreign_key "dms__account_company_company_fk" {
    columns     = [column.company_code]
    ref_columns = [table.dms__company.column.company_code]
  }
  foreign_key "dms__account_company_retention_fk" {
    columns     = [column.retention]
    ref_columns = [table.dms__retention.column.code]
  }
  foreign_key "dms__account_company_archive_fk" {
    columns     = [column.archive]
    ref_columns = [table.dms__archive.column.code]
  }
}

# Workflow (für alle Buchungskreise, kopierbar)
table "dms__workflow" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "description" {
    type = text` + opt + `
  }
  # erster Schritt (leer = kleinster)
  column "start_step" {
    type = text` + opt + `
  }
  # Form der Schritte (2 = seit 0.12.0: typisierte Einstellungen, Bedingungen, Zuordnungen; leer = alt)
  column "format" {
    type = bigint` + opt + `
  }
  # Felder des Kopfs, die ein Neustart behält (Komma-Liste, seit 0.11.0)
  column "restart_keep" {
    type    = text
    default = "note"` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Schritt: Benutzer (INPUT, SELECT, FORM) oder Programm (COMMAND, AI, ACTION, RECOGNIZE, CHECK)
table "dms__step" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "name" { type = text }
  column "kind" { type = text }
  # Ziel: Feld des Dokuments, ref oder var
  column "target" {
    type = text` + opt + `
  }
  # Frage (Platzhalter); Übersetzungen in dms__step_text
  column "question" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  # Ziel ref: Object (leer = aus der Auswahl Object:Schlüssel); Ziel var: Name
  column "target_object" {
    type = text` + opt + `
  }
  column "var_name" {
    type = text` + opt + `
  }
  # Benutzerschritt, Feld schon belegt: ASK | SKIP
  column "if_set" {
    type    = text
    default = "ASK"
  }
  column "allow_restart" {
    type    = boolean
    default = true
  }
  # Auswahl: OBJECT | LIST | TREE | CATEGORY
  column "source_kind" {
    type = text` + opt + `
  }
  column "source_object" {
    type = text` + opt + `
  }
  column "source_action" {
    type = text` + opt + `
  }
  column "value_field" {
    type = text` + opt + `
  }
  column "label_fields" {
    type = text` + opt + `
  }
  column "auto_single" {
    type    = boolean
    default = false
  }
  # Eingabe: TEXT | DATE | NUMBER | YESNO
  column "input_type" {
    type = text` + opt + `
  }
  column "default_value" {
    type = text` + opt + `
  }
  column "date_format" {
    type = text` + opt + `
  }
  # KI-Anfrage (Modul ai), Kontext; Maske: Bestätigung, Datei an die KI
  column "ai_request" {
    type = text` + opt + `
  }
  column "context" {
    type = text` + opt + `
  }
  column "confirm" {
    type    = boolean
    default = false
  }
  column "file_input" {
    type    = boolean
    default = false
  }
  column "file_types" {
    type = text` + opt + `
  }
  column "ai_wins" {
    type = text` + opt + `
  }
  # Befehl (Katalog dms__command), Sprachen der Texterkennung
  column "command" {
    type = text` + opt + `
  }
  column "languages" {
    type = text` + opt + `
  }
  # Aktion: Object.Action; changes_data = im Probelauf nicht ausführen
  column "action_object" {
    type = text` + opt + `
  }
  column "action_name" {
    type = text` + opt + `
  }
  column "changes_data" {
    type    = boolean
    default = true
  }
  # Erkennen: Regeln (Komma-Liste), Mindestpunkte, Abstand
  column "rules" {
    type = text` + opt + `
  }
  column "min_score" {
    type = bigint` + opt + `
  }
  column "min_gap" {
    type = bigint` + opt + `
  }
  # Programmschritte mit Ziel: PROPOSE | SET
  column "apply" {
    type    = text
    default = "PROPOSE"
  }
  # Benutzerschritt: Link „… öffnen“ auf Bezüge dieses Objects
  column "open_object" {
    type = text` + opt + `
  }
  column "open_label" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.workflow, column.step] }
  foreign_key "dms__step_workflow_fk" {
    columns     = [column.workflow]
    ref_columns = [table.dms__workflow.column.code]
  }
}

# Parameter eines Schritts (bis 0.11.x; nur noch für die Übernahme in die Form 2)
table "dms__step_param" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "param" { type = text }
  # Sprache (de, en, zh-CN); leer = alle
  column "locale" {
    type    = text
    default = ""
  }
  column "value" { type = text }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.workflow, column.step, column.param, column.locale] }
  foreign_key "dms__step_param_step_fk" {
    columns     = [column.workflow, column.step]
    ref_columns = [table.dms__step.column.workflow, table.dms__step.column.step]
  }
}

# Option eines Schritts: Ergebnis (gewählt, eingegeben, erfolgreich …) → nächster Schritt
table "dms__step_option" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "outcome" { type = text }
  # nur Ergebnis VALUE: der gesetzte Wert
  column "value" {
    type    = text
    default = ""
  }
  # leer = Workflow endet
  column "next_step" {
    type = text` + opt + `
  }
  # Text des Knopfs (Benutzerschritte; leer = Vorgabe)
  column "label" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.workflow, column.step, column.outcome, column.value] }
  foreign_key "dms__step_option_step_fk" {
    columns     = [column.workflow, column.step]
    ref_columns = [table.dms__step.column.workflow, table.dms__step.column.step]
  }
}

# Eingangsverzeichnis je Buchungskreis: Verzeichnis, Workflow, Vorgaben (Objekt + Schlüssel)
table "dms__inbox" {
  schema = schema.main
  column "company_code" { type = text }
  column "code" { type = text }
  column "name" { type = text }
  column "path" { type = text }
  column "workflow" {
    type = text` + opt + `
  }
  # Vorentscheidung „verwerfen“: Dokumente warten hier ohne Workflow auf die endgültige Entscheidung
  column "discard" {
    type    = boolean
    default = false
  }
  # Eigene Scans: Scans ohne Text-Ebene aufbereiten (ocrmypdf), Inhalt ersetzen
  column "own_scans" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.code] }
  foreign_key "dms__inbox_company_fk" {
    columns     = [column.company_code]
    ref_columns = [table.dms__company.column.company_code]
  }
  foreign_key "dms__inbox_workflow_fk" {
    columns     = [column.workflow]
    ref_columns = [table.dms__workflow.column.code]
  }
}

# Vorgabe eines Eingangsverzeichnisses: Object und Schlüssel (z. B. Building 2000|ROBad1, DmsDocType INVOICE)
table "dms__inbox_param" {
  schema = schema.main
  column "company_code" { type = text }
  column "inbox" { type = text }
  column "line_no" { type = bigint }
  column "object" { type = text }
  column "value" { type = text }
  primary_key { columns = [column.company_code, column.inbox, column.line_no] }
  foreign_key "dms__inbox_param_inbox_fk" {
    columns     = [column.company_code, column.inbox]
    ref_columns = [table.dms__inbox.column.company_code, table.dms__inbox.column.code]
  }
}


# Bedingung eines Schritts (seit 0.12.0): APPLIES (Schritt gilt nur, wenn), CHECK (Prüfen), PREFILL (Maske)
table "dms__step_condition" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "line_no" { type = bigint }
  column "role" { type = text }
  column "left_expr" { type = text }
  column "operator" { type = text }
  column "right_expr" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.workflow, column.step, column.line_no] }
  foreign_key "dms__step_condition_step_fk" {
    columns     = [column.workflow, column.step]
    ref_columns = [table.dms__step.column.workflow, table.dms__step.column.step]
  }
}

# Zuordnung eines Schritts (seit 0.12.0): Filter, Werte, Parameter, Ergebnis, KI-Anfragen, Vorgaben …
table "dms__step_mapping" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "line_no" { type = bigint }
  column "role" { type = text }
  column "field" { type = text }
  column "value" {
    type = text` + opt + `
  }
  column "target" {
    type = text` + opt + `
  }
  # nur für diese Dokumentkategorie (leer = alle)
  column "only_category" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.workflow, column.step, column.line_no] }
  foreign_key "dms__step_mapping_step_fk" {
    columns     = [column.workflow, column.step]
    ref_columns = [table.dms__step.column.workflow, table.dms__step.column.step]
  }
}

# Übersetzung eines Schritts (seit 0.12.0): question, context, button.<Ergebnis> je Sprache
table "dms__step_text" {
  schema = schema.main
  column "workflow" { type = text }
  column "step" { type = text }
  column "text_key" { type = text }
  column "locale" { type = text }
  column "value" { type = text }
  primary_key { columns = [column.workflow, column.step, column.text_key, column.locale] }
  foreign_key "dms__step_text_step_fk" {
    columns     = [column.workflow, column.step]
    ref_columns = [table.dms__step.column.workflow, table.dms__step.column.step]
  }
}

# Befehl (Katalog, seit 0.12.0): Programmzeilen ohne Shell, nur erlaubte Programme (Plugin-Konfiguration)
table "dms__command" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "lines" { type = text }
  column "content_types" {
    type = text` + opt + `
  }
  column "result" {
    type    = text
    default = "TEXT"
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "description" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Ablauf eines Dokuments (seit 0.12.0): Stand je Schritt des Workflows
table "dms__document_step" {
  schema = schema.main
  column "document_id" { type = text }
  column "step" { type = text }
  column "company_code" { type = text }
  column "name" { type = text }
  column "kind" {
    type = text` + opt + `
  }
  column "status" {
    type    = text
    default = "OPEN"
  }
  column "outcome" {
    type = text` + opt + `
  }
  column "value" {
    type = text` + opt + `
  }
  column "info" {
    type = text` + opt + `
  }
  column "seq" {
    type    = bigint
    default = 0
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "changed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.document_id, column.step] }
}

# Dokument: im Eingang (Datei im Eingangsverzeichnis) oder abgelegt (Datei im Archiv, nur lesbar)
table "dms__document" {
  schema = schema.main
  column "id" { type = text }
  # Variablen und KI-Ergebnisse des Workflows (JSON)
  column "wf_vars" {
    type = text
    null = true
  }
  # Antworten der KI je Anfrage, Text und Kontext (JSON, seit 0.12.0) – „Von vorn“ fragt nicht neu
  column "ai_cache" {
    type = text
    null = true
  }
  column "company_code" { type = text }
  # Dokumentname (eindeutig im Buchungskreis, ab Ablage): beschreibt den Ort im Archiv
  column "name" {
    type = text` + opt + `
  }
  column "title" {
    type = text` + opt + `
  }
  column "doc_category" {
    type = text` + opt + `
  }
  column "doc_type" {
    type = text` + opt + `
  }
  column "file_account" {
    type = text` + opt + `
  }
  column "partner_id" {
    type = text` + opt + `
  }
  column "doc_date" {
    type = date` + opt + `
  }
  column "retention" {
    type = text` + opt + `
  }
  column "retain_until" {
    type = date` + opt + `
  }
  column "archive" {
    type = text` + opt + `
  }
  # Pfad im Archiv (relativ zum Wurzelverzeichnis)
  column "location" {
    type = text` + opt + `
  }
  column "file_name" { type = text }
  column "content_type" { type = text }
  column "size" {
    type    = bigint
    default = 0
  }
  column "sha256" { type = text }
  # Prüfsumme der eingelesenen Datei (vor der Aufbereitung) – erkennt doppelte Scans
  column "source_sha256" {
    type = text` + opt + `
  }
  # INBOX | FILED | REMOVED | DISCARDED
  column "status" {
    type    = text
    default = "INBOX"
  }
  column "inbox" {
    type = text` + opt + `
  }
  # Datei im Eingangsverzeichnis (bis zur Ablage)
  column "inbox_file" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "wf_code" {
    type = text` + opt + `
  }
  column "wf_step" {
    type = text` + opt + `
  }
  # RUNNING | WAITING | DONE | FAILED
  column "wf_status" {
    type = text` + opt + `
  }
  column "wf_log" {
    type = text` + opt + `
  }
  # erledigte Schritte (JSON) – für „zurück“
  column "wf_trail" {
    type = text` + opt + `
  }
  # bestätigter Dokumentname (ohne Endung; leer = nach Muster)
  column "name_override" {
    type = text` + opt + `
  }
  column "created_at" { type = text }
  column "created_by" {
    type = text` + opt + `
  }
  column "filed_at" {
    type = text` + opt + `
  }
  column "filed_by" {
    type = text` + opt + `
  }
  column "removed_at" {
    type = text` + opt + `
  }
  column "removed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.id] }
  index "dms__document_name" {
    unique  = true
    columns = [column.company_code, column.name]
  }
  index "dms__document_hash" { columns = [column.company_code, column.sha256] }
  index "dms__document_source_hash" { columns = [column.company_code, column.source_sha256] }
  index "dms__document_status" { columns = [column.company_code, column.status] }
}

# Erkannter Text eines Dokuments (Volltextsuche; eigene Tabelle, damit Listen leicht bleiben)
table "dms__document_text" {
  schema = schema.main
  column "document_id" { type = text }
  column "text" { type = text }
  primary_key { columns = [column.document_id] }
  foreign_key "dms__document_text_document_fk" {
    columns     = [column.document_id]
    ref_columns = [table.dms__document.column.id]
  }
}

# Bezug eines Dokuments: Object und fachlicher Schlüssel (Vertrag, Gebäude, Eingangsrechnung, Vorerfassung …)
table "dms__document_ref" {
  schema = schema.main
  column "document_id" { type = text }
  column "line_no" { type = bigint }
  column "company_code" { type = text }
  column "object" { type = text }
  column "object_key" { type = text }
  # Verweis im Plugin document (Documents.attach)
  column "doc_ref_id" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.document_id, column.line_no] }
  index "dms__document_ref_target" { columns = [column.object, column.object_key] }
  foreign_key "dms__document_ref_document_fk" {
    columns     = [column.document_id]
    ref_columns = [table.dms__document.column.id]
  }
}

# Prüfregel Buchung je Buchungskreis: Belegart → nötige Ablagen
table "dms__posting_rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "rule_no" { type = bigint }
  # Belegart des Hauptbuchs (leer = alle)
  column "document_type" {
    type    = text
    default = ""
  }
  # Ablagen (Nummern oder Anfänge, durch Komma getrennt; leer = jede)
  column "file_accounts" {
    type    = text
    default = ""
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.rule_no] }
  foreign_key "dms__posting_rule_company_fk" {
    columns     = [column.company_code]
    ref_columns = [table.dms__company.column.company_code]
  }
}

# Bezugsquelle einer Vorerfassung: Object mit Feld draft_id → Bezug (Object, Schlüssel aus Vorlage)
table "dms__posting_source" {
  schema = schema.main
  column "code" { type = text }
  column "object" { type = text }
  column "filter_field" { type = text }
  column "ref_object" { type = text }
  # Schlüssel aus Feldern des Datensatzes, z. B. {company_code}|{contract_id}
  column "key_template" { type = text }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Prüfregel Vertrag je Buchungskreis: Aktivieren bzw. neue Kondition → nötiges Dokument
table "dms__contract_rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "rule_no" { type = bigint }
  # ACTIVATE | CONDITION
  column "event" { type = text }
  column "contract_type" {
    type    = text
    default = ""
  }
  # Konditionsarten (durch Komma getrennt; leer = alle)
  column "condition_types" {
    type    = text
    default = ""
  }
  column "file_accounts" {
    type    = text
    default = ""
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.rule_no] }
  foreign_key "dms__contract_rule_company_fk" {
    columns     = [column.company_code]
    ref_columns = [table.dms__company.column.company_code]
  }
}
`

func row(kv ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i].(string)] = kv[i+1]
	}
	return m
}

// Vorschlagswerte (DBSchema fügt nur fehlende ein).
var (
	seedRetentions = []map[string]any{
		row("code", "AO10", "name", "10 Jahre ab Ende des Kalenderjahres (Bücher, Inventare, Jahresabschlüsse – § 147 AO)", "years", 10, "start", "END_OF_YEAR"),
		row("code", "AO8", "name", "8 Jahre ab Ende des Kalenderjahres (Buchungsbelege, Rechnungen – § 147 AO seit 2025)", "years", 8, "start", "END_OF_YEAR"),
		row("code", "AO6", "name", "6 Jahre ab Ende des Kalenderjahres (Handels- und Geschäftsbriefe)", "years", 6, "start", "END_OF_YEAR"),
		row("code", "Y3", "name", "3 Jahre ab Ende des Kalenderjahres", "years", 3, "start", "END_OF_YEAR"),
		row("code", "FOREVER", "name", "Dauerhaft (Grundbuch, Verträge, Bescheide)", "start", "FOREVER"),
	}
	seedCategories = []map[string]any{
		row("code", "CONTRACT", "name", "Vertrag", "sort_order", 10),
		row("code", "INVOICE", "name", "Rechnung / Gutschrift", "sort_order", 20),
		row("code", "OBJECT", "name", "Gebäude / Mietobjekt", "sort_order", 30),
		row("code", "RECEIPT", "name", "Beleg (Barbeleg, Quittung)", "sort_order", 40),
		row("code", "PLAN", "name", "Wirtschaftsplan", "sort_order", 50),
		row("code", "OTHER", "name", "Sonstiges", "sort_order", 900),
	}
	// seedHints: Hinweise für Geschäftspartner ohne lesbaren Absender (Briefkopf als Bild).
	seedHints = []map[string]any{
		row("code", "iban", "name", "IBAN im Text (keine eigene)", "kind", "PATTERN", "sort_order", 10,
			"pattern", `(?i)\b[A-Z]{2}\d{2}(?:[ ]?[A-Z0-9]{4}){3,7}(?:[ ]?[A-Z0-9]{1,3})?\b`, "check_rule", "IBAN", "strip", " ", "exclude", "BankAccount.iban",
			"description", "Erste gültige IBAN, die keine eigene Bankverbindung ist"),
		row("code", "payee", "name", "Name laut Kontoauszug", "kind", "LOOKUP", "sort_order", 20,
			"source", "BankTransaction", "match", "counterparty_iban={hint.iban}; company_code={company_code}",
			"value_field", "counterparty_name", "partner_field", "partner_id", "sort_field", "booking_date", "target", "partner_id", "points", 5,
			"description", "Gegenseite des jüngsten Bankumsatzes mit dieser IBAN; Partner, falls zugeordnet"),
		row("code", "tax_number", "name", "Steuernummer", "kind", "PATTERN", "sort_order", 30,
			"pattern", `\b\d{2,3} ?/ ?\d{3} ?/ ?\d{4,5}\b`, "strip", " "),
		row("code", "vat_id", "name", "USt-IdNr.", "kind", "PATTERN", "sort_order", 40,
			"pattern", `\bDE ?\d{9}\b`, "strip", " "),
		// Erkennen (seit 0.12.0): Punkte für Partner, Vertrag und Dokumentdatum.
		row("code", "doc_date", "name", "Dokumentdatum (erstes Datum im Text)", "kind", "PATTERN", "sort_order", 50,
			"pattern", `\b\d{1,2}\.\d{1,2}\.\d{4}\b`, "target", "doc_date", "points", 5),
		row("code", "m_iban", "name", "IBAN", "kind", "MATCH", "sort_order", 110, "source", "PartnerBankDetail", "compare_field", "iban",
			"normalize", "IBAN", "candidate_field", "bp_id", "target", "partner_id", "points", 5,
			"description", "Bankverbindung eines Geschäftspartners im Text"),
		row("code", "m_phone", "name", "Telefon", "kind", "MATCH", "sort_order", 120, "source", "PartnerContact", "compare_field", "value",
			"normalize", "PHONE", "candidate_field", "bp_id", "target", "partner_id", "points", 3, "min_length", 7),
		row("code", "m_contract_customer", "name", "Kundennummer", "kind", "MATCH", "sort_order", 130, "source", "Contract",
			"match", "company_code={company_code}", "compare_field", "customer_number", "normalize", "ALNUM", "min_length", 5,
			"candidate_field", "id", "partner_field", "partner_id", "rule_group", "contract", "target", "ref:Contract", "points", 5),
		row("code", "m_contract_external", "name", "Vertragsnummer (extern)", "kind", "MATCH", "sort_order", 131, "source", "Contract",
			"match", "company_code={company_code}", "compare_field", "external_number", "normalize", "ALNUM", "min_length", 5,
			"candidate_field", "id", "partner_field", "partner_id", "rule_group", "contract", "target", "ref:Contract", "points", 5),
		row("code", "m_contract_id", "name", "Vertragsnummer", "kind", "MATCH", "sort_order", 132, "source", "Contract",
			"match", "company_code={company_code}", "compare_field", "contract_id", "normalize", "ALNUM", "min_length", 5,
			"candidate_field", "id", "partner_field", "partner_id", "rule_group", "contract", "target", "ref:Contract", "points", 5),
		row("code", "m_name1", "name", "Name", "kind", "MATCH", "sort_order", 140, "source", "BusinessPartner", "compare_field", "name1",
			"normalize", "NAME", "min_length", 4, "target", "partner_id", "points", 2),
		row("code", "m_name2", "name", "Name 2", "kind", "MATCH", "sort_order", 141, "source", "BusinessPartner", "compare_field", "name2",
			"normalize", "NAME", "min_length", 3, "requires", "m_name1", "target", "partner_id", "points", 1),
		row("code", "m_short", "name", "Kürzel", "kind", "MATCH", "sort_order", 150, "source", "BusinessPartner", "compare_field", "abbreviation",
			"normalize", "NAME", "min_length", 3, "target", "partner_id", "points", 2),
		row("code", "m_payee_name", "name", "Partnername im Kontoauszug", "kind", "MATCH", "sort_order", 160, "source", "BusinessPartner",
			"compare_field", "name1", "compare_hint", "payee", "normalize", "NAME", "min_length", 4, "target", "partner_id", "points", 4),
	}
	// seedCommands: Befehle der Texterkennung (Programme laut allowed_programs).
	seedCommands = []map[string]any{
		row("code", "READ_TEXT", "name", "Textdatei lesen", "lines", "@read {in}", "content_types", "text/*", "result", "TEXT", "sort_order", 5),
		row("code", "PDFTEXT", "name", "Text-Ebene des PDF (pdftotext)", "lines", "pdftotext -layout {in} -", "content_types", "application/pdf",
			"result", "TEXT", "sort_order", 10),
		row("code", "OCR_PDF", "name", "Texterkennung PDF (ocrmypdf, Datei bleibt)", "lines", "ocrmypdf --skip-text -l {lang} {in} {work}/ocr.pdf\npdftotext -layout {work}/ocr.pdf -",
			"content_types", "application/pdf", "result", "TEXT", "sort_order", 20),
		row("code", "OCR_IMAGE", "name", "Texterkennung Bild (tesseract)", "lines", "tesseract {in} - -l {lang}", "content_types", "image/*",
			"result", "TEXT", "sort_order", 30),
		row("code", "PREPARE_PDF", "name", "Scan (PDF) aufbereiten: PDF/A mit Text-Ebene", "lines", "ocrmypdf --skip-text --output-type pdfa -l {lang} {in} {out}",
			"content_types", "application/pdf", "result", "FILE", "sort_order", 40),
		row("code", "PREPARE_IMAGE", "name", "Scan (Bild) aufbereiten: PDF/A mit Text-Ebene", "lines", "ocrmypdf --image-dpi 300 --output-type pdfa -l {lang} {in} {out}",
			"content_types", "image/*", "result", "FILE", "sort_order", 50),
	}
	seedDocTypes = []map[string]any{
		row("code", "CONTRACT", "name", "Vertrag", "short", "VT", "category", "CONTRACT", "ref_kind", "CONTRACT", "retention", "FOREVER", "sort_order", 10),
		row("code", "AMENDMENT", "name", "Nachtrag", "short", "NT", "category", "CONTRACT", "ref_kind", "AMENDMENT", "retention", "FOREVER", "sort_order", 20),
		row("code", "INVOICE", "name", "Rechnung", "short", "RE", "category", "INVOICE", "ref_kind", "INVOICE", "retention", "AO8", "sort_order", 30),
		row("code", "ECONOMIC_PLAN", "name", "Wirtschaftsplan", "short", "WP", "category", "PLAN", "ref_kind", "CONTRACT", "retention", "AO10", "sort_order", 35),
		row("code", "CREDIT_NOTE", "name", "Gutschrift", "short", "GS", "category", "INVOICE", "ref_kind", "INVOICE", "retention", "AO8", "sort_order", 35),
		row("code", "CONTRACT_INVOICE", "name", "Rechnungsdokument", "short", "VR", "category", "CONTRACT", "ref_kind", "INVOICE", "retention", "AO8", "sort_order", 22),
		row("code", "CONTRACT_CREDIT", "name", "Gutschrift", "short", "VG", "category", "CONTRACT", "ref_kind", "INVOICE", "retention", "AO8", "sort_order", 24),
		row("code", "NOTICE", "name", "Bescheid", "short", "BS", "category", "CONTRACT", "ref_kind", "NOTICE", "retention", "FOREVER", "sort_order", 40),
		row("code", "STATEMENT", "name", "Abrechnung", "short", "AB", "category", "CONTRACT", "ref_kind", "STATEMENT", "retention", "AO8", "sort_order", 50),
		row("code", "BANK", "name", "Kontoauszug", "short", "KA", "category", "OTHER", "ref_kind", "OTHER", "retention", "AO8", "sort_order", 60),
		row("code", "LETTER", "name", "Schriftverkehr", "short", "SV", "category", "CONTRACT", "ref_kind", "CORRESPONDENCE", "retention", "AO6", "sort_order", 70),
		row("code", "PLAN", "name", "Plan / Zeichnung", "short", "PL", "category", "OBJECT", "ref_kind", "OTHER", "retention", "FOREVER", "sort_order", 80),
		row("code", "REPORT", "name", "Bericht / Protokoll", "short", "BP", "category", "OBJECT", "ref_kind", "OTHER", "retention", "AO10", "sort_order", 90),
		row("code", "CASH_RECEIPT", "name", "Barbeleg", "short", "BB", "category", "RECEIPT", "ref_kind", "OTHER", "retention", "AO8", "sort_order", 95),
		row("code", "OTHER", "name", "Sonstiges", "short", "SO", "category", "OTHER", "ref_kind", "OTHER", "sort_order", 900),
	}
	// Kaufmännische Objekte je Dokumentkategorie (Vorschlag).
	seedCategoryObjects = []map[string]any{
		row("category", "CONTRACT", "object", "Contract", "label", "Vertrag", "partner_field", "partner_id", "date_field", "signed_date",
			"title_field", "designation", "account_object", "ContractCondition", "account_link", "contract_id", "account_field", "account_number",
			"can_create", true, "sort_order", 10),
		row("category", "INVOICE", "object", "SupplierInvoice", "label", "Eingangsrechnung", "partner_field", "supplier_id", "date_field", "invoice_date",
			"title_field", "supplier_reference", "account_object", "SupplierInvoiceItem", "account_link", "invoice_id", "account_field", "account_number",
			"cost_category_field", "cost_category", "allocable_field", "allocable",
			"can_create", true, "sort_order", 10),
		row("category", "OBJECT", "object", "Building", "label", "Gebäude", "title_field", "designation", "can_create", true, "sort_order", 10),
		row("category", "PLAN", "object", "ContractPlan", "label", "Wirtschaftsplan", "date_field", "plan_date", "title_field", "reference",
			"can_create", true, "sort_order", 10),
		row("category", "OBJECT", "object", "RentObject", "label", "Mietobjekt", "title_field", "designation", "can_create", true, "sort_order", 20),
		row("category", "RECEIPT", "object", "JournalDraft", "label", "Vorerfassung", "filters", "company_code_id={company_code}; status=DRAFT",
			"date_field", "document_date", "title_field", "header_text", "account_object", "JournalDraftItem", "account_link", "draft_id=id",
			"account_field", "account_number", "can_create", true, "sort_order", 10),
	}
	// Bezugsquellen der Vorerfassung: Eingangsrechnung und Sollstellung eines Vertrags.
	seedPostingSources = []map[string]any{
		row("code", "INVOICE", "object", "SupplierInvoice", "filter_field", "draft_id", "ref_object", "SupplierInvoice", "key_template", "{company_code}|{invoice_id}"),
		row("code", "CONTRACT", "object", "ContractPosting", "filter_field", "draft_id", "ref_object", "Contract", "key_template", "{company_code}|{contract_id}"),
	}
)
