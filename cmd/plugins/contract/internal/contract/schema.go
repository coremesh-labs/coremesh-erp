package contract

// Schema der Vertragsverwaltung (Atlas HCL, Präfix contract__, keine DROPs).
//
//   - Kataloge je Buchungskreis: Vertragsarten, Konditionsarten,
//     Partnerrollen (Aktivierung aus dem Partnermodul), Kontenfindung.
//   - Vertrag (Kopf) mit Laufzeit und Status; Partner, Objekte, Konditionen
//     und Kündigungsregeln mit Zeitscheibe.
//   - Beträge als ganze Zahl in der kleinsten Einheit der Währung (Cent), wie
//     im Hauptbuch.

const opt = `
    null = true`

var schemaHCL = `
table "contract__contract_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # RECEIVABLE (wir erhalten, z. B. Miete) | PAYABLE (wir zahlen, z. B. Versicherung)
  column "direction"    { type = text }
  # Rolle des Vertragspartners (Pflicht beim Anlegen), aus contract__partner_role
  column "main_role"    { type = text }
  # Intervallschlüssel im Nummernkreis Contract (numrange)
  column "range_key"    { type = text }
  column "needs_object" {
    type    = boolean
    default = false
  }
  # erlaubte Objekte (RentObject, Building, BusinessEntity), kommagetrennt; leer = alle
  column "object_types" {
    type = text` + opt + `
  }
  # ein Objekt zu einem Zeitpunkt nur in einem Vertrag dieser Arten (Leerstand)
  column "exclusive_objects" {
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

table "contract__condition_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # MAIN (Hauptforderung) | SECONDARY (Nebenforderung: Kosten, Zinsen, Gebühren)
  column "claim_class"  { type = text }
  # Vorauszahlung, die später abgerechnet wird (Betriebs-, Heizkosten, Hausgeld)
  column "is_advance" {
    type    = boolean
    default = false
  }
  # Reihenfolge der Verrechnung von Zahlungen (§ 367 BGB: Kosten, Zinsen, Hauptleistung)
  column "clearing_order" {
    type    = bigint
    default = 30
  }
  column "tax_code" {
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

table "contract__partner_role" {
  schema = schema.main
  column "company_code" { type = text }
  column "role_code"    { type = text }
  column "name"         { type = text }
  column "is_exclusive" {
    type    = boolean
    default = false
  }
  column "with_share" {
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
  primary_key { columns = [column.company_code, column.role_code] }
}

# Kontenfindung: zulässige Sachkonten je Vertragsart und Konditionsart.
table "contract__account" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_type"  { type = text }
  column "condition_type" { type = text }
  column "account_number" { type = text }
  # Bezeichnung aus dem Hauptbuch (beim Speichern übernommen)
  column "account_name" {
    type = text` + opt + `
  }
  column "is_default" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.contract_type, column.condition_type, column.account_number] }
  foreign_key "contract__account_type_fk" {
    columns     = [column.company_code, column.contract_type]
    ref_columns = [table.contract__contract_type.column.company_code, table.contract__contract_type.column.code]
  }
  foreign_key "contract__account_condition_fk" {
    columns     = [column.company_code, column.condition_type]
    ref_columns = [table.contract__condition_type.column.company_code, table.contract__condition_type.column.code]
  }
}

table "contract__contract" {
  schema = schema.main
  column "company_code"    { type = text }
  column "contract_id"     { type = text }
  column "external_number" { type = text }
  column "contract_type"   { type = text }
  column "designation"     { type = text }
  column "direction"       { type = text }
  column "currency"        { type = text }
  # Vertragspartner beim Anlegen (Hauptrolle); Wechsel im Abschnitt Partner
  column "partner_id"      { type = text }
  # DRAFT | ACTIVE | TERMINATED
  column "status"          { type = text }
  column "valid_from"      { type = date }
  column "valid_to"        { type = date }
  column "signed_date" {
    type = date` + opt + `
  }
  column "notice_received" {
    type = date` + opt + `
  }
  column "terminated_by" {
    type = text` + opt + `
  }
  column "termination_reason" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "changed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id] }
  index "contract__contract_external" {
    columns = [column.company_code, column.external_number]
  }
  foreign_key "contract__contract_type_fk" {
    columns     = [column.company_code, column.contract_type]
    ref_columns = [table.contract__contract_type.column.company_code, table.contract__contract_type.column.code]
  }
}

table "contract__partner" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "role_code"    { type = text }
  column "partner_id"   { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  column "share" {
    type = double` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.role_code, column.partner_id, column.valid_from] }
  index "contract__partner_partner" {
    columns = [column.partner_id]
  }
  foreign_key "contract__partner_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

table "contract__object" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  # Object des Objekts (RentObject, Building, BusinessEntity)
  column "object_type"  { type = text }
  column "object_id"    { type = text }
  column "is_main" {
    type    = boolean
    default = false
  }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  primary_key { columns = [column.company_code, column.contract_id, column.object_type, column.object_id, column.valid_from] }
  index "contract__object_object" {
    columns = [column.company_code, column.object_id]
  }
  foreign_key "contract__object_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

table "contract__condition" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_id"    { type = text }
  # Vertragsart (übernommen; Kontenfindung)
  column "contract_type"  { type = text }
  column "condition_type" { type = text }
  # Objekt der Kondition ("" = ganzer Vertrag)
  column "object_id" {
    type    = text
    default = ""
  }
  column "valid_from"     { type = date }
  column "valid_to"       { type = date }
  # FIXED (Betrag) | PER_UNIT (Preis je Einheit der Bemessung)
  column "calc_method"    { type = text }
  # kleinste Einheit der Währung; bei PER_UNIT Preis je Einheit
  column "amount"         { type = bigint }
  column "measurement_type" {
    type = text` + opt + `
  }
  # MONTHLY | QUARTERLY | HALF_YEARLY | YEARLY | ONCE
  column "frequency"      { type = text }
  column "due_day" {
    type    = bigint
    default = 1
  }
  # IN_ADVANCE (vorschüssig) | IN_ARREARS (nachschüssig)
  column "payment_mode"   { type = text }
  column "account_number" {
    type = text` + opt + `
  }
  # abweichender Zahler (Geschäftspartner); leer = Vertragspartner
  column "payer_id" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.condition_type, column.object_id, column.valid_from] }
  foreign_key "contract__condition_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
  foreign_key "contract__condition_type_fk" {
    columns     = [column.company_code, column.condition_type]
    ref_columns = [table.contract__condition_type.column.company_code, table.contract__condition_type.column.code]
  }
}

# Kündigungs- und Laufzeitregeln (Zeitscheibe).
table "contract__notice_term" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  column "notice_period_months" {
    type    = bigint
    default = 3
  }
  # Kündigung muss bis zu diesem Tag des Monats eingehen, damit der Monat zählt
  column "notice_deadline_day" {
    type    = bigint
    default = 3
  }
  # Kündigungsausschluss ab Vertragsbeginn
  column "minimum_duration_months" {
    type    = bigint
    default = 0
  }
  column "has_renewal_option" {
    type    = boolean
    default = false
  }
  column "renewal_months" {
    type    = bigint
    default = 0
  }
  primary_key { columns = [column.company_code, column.contract_id, column.valid_from] }
  foreign_key "contract__notice_term_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}
`
