package templating

// opt: Spalte darf leer sein.
const opt = `
    null = true`

// schemaHCL: Soll-Schema (Atlas-HCL) der Vorlagen. Tabellen mit dem Pflicht-Präfix template__.
// Inhalte (Zip-Dateien, Ergebnisse, Protokolle) stehen Base64-kodiert in Textspalten.
const schemaHCL = `
# Bundle: Zip-Datei mit Go-Templates (Art TEMPLATE, Kette über parent: vom
# konkreten zum allgemeinen) oder mit Binärdateien (Art BINARY, z. B. Bilder für PDF)
table "template__bundle" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "kind" { type = text }
  column "parent" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  # Zip-Datei, Base64
  column "content" {
    type = text` + opt + `
  }
  column "files" {
    type    = bigint
    default = 0
  }
  column "size" {
    type    = bigint
    default = 0
  }
  # Dateien im Bundle, je Zeile eine
  column "file_list" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Vorlage je Sprache, für alle Buchungskreise
table "template__definition" {
  schema = schema.main
  column "code" { type = text }
  column "language" { type = text }
  column "name" { type = text }
  # HTML, XML, CSV, PDF
  column "target" { type = text }
  column "template_bundle" { type = text }
  column "binary_bundle" {
    type = text` + opt + `
  }
  # Begrenzer der Templates, z. B. "<< >>" (leer = Vorschlag der Ausgabeart)
  column "delimiters" {
    type = text` + opt + `
  }
  column "file_name" {
    type = text` + opt + `
  }
  # Protokolle und Quelltext immer am Auftrag
  column "debug" {
    type    = boolean
    default = false
  }
  # Beispieldaten (YAML) für Probeaufträge
  column "sample_data" {
    type = text` + opt + `
  }
  column "description" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code, column.language] }
}

# Aufbewahrung der Aufträge je Buchungskreis (Tage nach Abschluss)
table "template__company" {
  schema = schema.main
  column "company_code" { type = text }
  column "keep_success_days" {
    type    = bigint
    default = 7
  }
  column "keep_error_days" {
    type    = bigint
    default = 30
  }
  primary_key { columns = [column.company_code] }
}

# Auftrag: Eingang (YAML), Status, Ergebnis
table "template__job" {
  schema = schema.main
  column "job_number" { type = text }
  column "company_code" { type = text }
  column "template" { type = text }
  column "language" { type = text }
  column "target" { type = text }
  # NEW, RUNNING, DONE, ERROR
  column "status" { type = text }
  column "status_text" {
    type = text` + opt + `
  }
  # eingegangene Daten mit Vorlage und Sprache (YAML)
  column "request" { type = text }
  # Ergebnis, Base64
  column "content" {
    type = text` + opt + `
  }
  column "content_type" {
    type = text` + opt + `
  }
  column "file_name" {
    type = text` + opt + `
  }
  column "size" {
    type    = bigint
    default = 0
  }
  # nicht löschen (Aufbewahrungsfrist gilt nicht)
  column "keep" {
    type    = boolean
    default = false
  }
  column "created_at" { type = text }
  column "created_by" {
    type = text` + opt + `
  }
  column "finished_at" {
    type = text` + opt + `
  }
  primary_key { columns = [column.job_number] }
  index "template__job_company" { columns = [column.company_code, column.status] }
}

# Anhänge eines Auftrags: Protokolle, Hilfsdateien und Quelltext (Fehler, Debug)
table "template__job_file" {
  schema = schema.main
  column "job_number" { type = text }
  column "name" { type = text }
  column "company_code" { type = text }
  column "content_type" { type = text }
  column "size" {
    type    = bigint
    default = 0
  }
  column "content" { type = text }
  primary_key { columns = [column.job_number, column.name] }
}
`
