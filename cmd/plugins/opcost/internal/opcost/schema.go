package opcost

const opt = `
    null = true`

// schemaHCL: Soll-Schema der Betriebskosten (Präfix opcost__).
const schemaHCL = `
# Kostenart je Buchungskreis: Betriebskosten (umlagefähig), nicht umlagefähig, Rücklagenzuführung
table "opcost__cost_category" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # ALLOCABLE (umlagefähig) | NON_ALLOCABLE (nicht umlagefähig) | RESERVE (Zuführung Erhaltungsrücklage)
  column "cost_type"    { type = text }
  # Vorschlag Sachkonto: Aufwand bzw. bei RESERVE das Bilanzkonto der Rücklage
  column "account_number" {
    type = text` + opt + `
  }
  # Nr. nach § 2 BetrKV
  column "betrkv_no" {
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
  primary_key { columns = [column.company_code, column.code] }
}

# Verteilerschlüssel je Buchungskreis
table "opcost__allocation_key" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # MEASUREMENT (Bemessung) | UNITS (Anzahl Einheiten) | PERSONS (Personen) | CONSUMPTION (Verbrauch) | DIRECT (direkt)
  column "basis"        { type = text }
  # bei MEASUREMENT: Bemessungsart der Immobilienverwaltung (z. B. MEA, WFL)
  column "measurement_type" {
    type = text` + opt + `
  }
  column "unit" {
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
  primary_key { columns = [column.company_code, column.code] }
}
`
