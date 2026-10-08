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
  # Bezugsvertrag Pflicht: erlaubte Vertragsarten (z. B. Kaution → MV,GM,SP); leer = kein Bezug
  column "parent_types" {
    type = text` + opt + `
  }
  # Vertragsabrechnung: zulässige Abweichung der Prüfungen (Dezimaltext, z. B. 0.05)
  column "settlement_tolerance" {
    type    = text
    default = "0.05"
  }
  # Partner braucht in der Rolle der Vertragsart Buchungskreisdaten mit Abstimmkonto
  column "partner_account_required" {
    type    = boolean
    default = true
  }
  # bis 0.4.0: Abstimmkonto der Sollstellung; seit 0.5.0 aus den Buchungskreisdaten des Partners (ungenutzt)
  column "reconciliation_account" {
    type = text` + opt + `
  }
  column "posting_document_type" {
    type = text` + opt + `
  }
  # Belegart, wenn ein Beleg per Saldo eine Gutschrift ist (leer = DG bzw. KG)
  column "credit_document_type" {
    type = text` + opt + `
  }
  # Sollstellung: automatisch ins Hauptbuch buchen (sonst bleibt die Vorerfassung offen)
  column "auto_post" {
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
  # einmalige Konditionen: Anzahl Monatsraten (Standard der Kondition), z. B. Kaution 3
  column "installments" {
    type    = bigint
    default = 1
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
  # Bezugsvertrag (z. B. der Mietvertrag einer Kaution), gleicher Buchungskreis
  column "parent_contract_id" {
    type = text` + opt + `
  }
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
  index "contract__contract_parent" {
    columns = [column.company_code, column.parent_contract_id]
  }
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
  # nur einmalig: Monatsraten (leer beim Anlegen = Standard der Konditionsart)
  column "installments" {
    type = bigint` + opt + `
  }
  # nur einmalig: Fälligkeit (leer = Beginn der Kondition)
  column "due_date" {
    type = date` + opt + `
  }
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

# Buchungslauf der Sollstellung (Plugin contract-billing rechnet und bucht)
table "contract__posting_run" {
  schema = schema.main
  column "id"           { type = text }
  column "company_code" { type = text }
  column "to_date"      { type = text }
  column "contract_id" {
    type = text` + opt + `
  }
  # RUNNING | DONE | PARTIAL | FAILED | EMPTY
  column "status"       { type = text }
  column "documents" {
    type    = bigint
    default = 0
  }
  column "drafts" {
    type    = bigint
    default = 0
  }
  column "items" {
    type    = bigint
    default = 0
  }
  column "errors" {
    type    = bigint
    default = 0
  }
  # davon Nachberechnungen (Fälligkeiten)
  column "corrections" {
    type    = bigint
    default = 0
  }
  column "message" {
    type = text` + opt + `
  }
  column "messages" {
    type = text` + opt + `
  }
  column "started_at"   { type = text }
  column "started_by" {
    type = text` + opt + `
  }
  column "finished_at" {
    type = text` + opt + `
  }
  primary_key { columns = [column.id] }
  index "contract__posting_run_company" { columns = [column.company_code, column.started_at] }
}

# Sollstellung: eine Kondition für einen Zeitraum, vorerfasst oder gebucht
table "contract__posting" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_id"    { type = text }
  column "condition_type" { type = text }
  # leer = Kondition für den ganzen Vertrag
  column "object_id"      { type = text }
  column "period_from"    { type = text }
  # 0 = erste Sollstellung, 1, 2 … weitere Vermerke (Nachberechnung) zum selben Schlüssel
  column "sequence" {
    type    = bigint
    default = 0
  }
  # ORIGINAL (Sollstellung zur Fälligkeit) | CORRECTION (Nachberechnung zum Lauf)
  column "kind" {
    type    = text
    default = "ORIGINAL"
  }
  # Beginn der Kalenderperiode (Abgleich der Nachberechnung); leer bei Vermerken vor 0.4.0
  column "billing_period" {
    type = text` + opt + `
  }
  column "period_to"      { type = text }
  column "due_date"       { type = text }
  # kleinste Einheit der Währung (Cent)
  column "amount"         { type = bigint }
  column "currency"       { type = text }
  column "account_number" { type = text }
  column "rent_object_id" {
    type = text` + opt + `
  }
  # DRAFT (vorerfasst) | POSTED (gebucht)
  column "status"         { type = text }
  column "draft_id"       { type = text }
  column "document_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  column "run_id" {
    type = text` + opt + `
  }
  column "recorded_at"    { type = text }
  primary_key { columns = [column.company_code, column.contract_id, column.condition_type, column.object_id, column.period_from, column.sequence] }
  index "contract__posting_draft" { columns = [column.draft_id] }
  index "contract__posting_run_id" { columns = [column.run_id] }
}

# Darlehenskonditionen je Vertrag (Zeitscheiben, z. B. neue Zinsbindung)
table "contract__loan" {
  schema = schema.main
  column "company_code"      { type = text }
  column "contract_id"       { type = text }
  column "valid_from"        { type = date }
  column "valid_to"          { type = date }
  # kleinste Einheit der Vertragswährung
  column "principal"         { type = bigint }
  column "disbursement_date" { type = date }
  # Übernahme eines laufenden Darlehens: Betrag = Anfangsbestand, Auszahlung nicht buchen
  column "takeover" {
    type    = boolean
    default = false
  }
  # ANNUITY | INSTALLMENT | BULLET
  column "repayment_type"    { type = text }
  # Prozent p. a. als Dezimaltext (exakt), z. B. 3.45
  column "interest_rate"     { type = text }
  column "installment" {
    type = bigint` + opt + `
  }
  column "frequency"         { type = text }
  column "due_day" {
    type    = bigint
    default = 30
  }
  # 30/360 | ACT/360 | ACT/365
  column "day_count"         { type = text }
  column "fixed_until" {
    type = date` + opt + `
  }
  column "loan_account"      { type = text }
  column "interest_account"  { type = text }
  column "interest_type"     { type = text }
  column "principal_type"    { type = text }
  column "special_type"      { type = text }
  column "disbursement_type" { type = text }
  primary_key { columns = [column.company_code, column.contract_id, column.valid_from] }
  foreign_key "contract__loan_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

# Sondertilgungen
table "contract__loan_payment" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "payment_date" { type = date }
  column "amount"       { type = bigint }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.payment_date] }
  foreign_key "contract__loan_payment_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

# Vertragsabrechnung (Versorger, Grundsteuer, WEG-Jahresabrechnung)
table "contract__settlement" {
  schema = schema.main
  column "company_code"    { type = text }
  column "contract_id"     { type = text }
  column "period_from"     { type = date }
  column "period_to"       { type = date }
  column "settlement_date" { type = date }
  column "posting_date"    { type = date }
  column "reference" {
    type = text` + opt + `
  }
  # laut Abrechnung, kleinste Einheit; Ergebnis: Nachzahlung > 0, Guthaben < 0
  column "stated_advances" {
    type = bigint` + opt + `
  }
  column "stated_result" {
    type = bigint` + opt + `
  }
  # Anteil an der Erhaltungsrücklage
  column "reserve_opening" {
    type = bigint` + opt + `
  }
  column "reserve_withdrawal" {
    type = bigint` + opt + `
  }
  column "reserve_closing" {
    type = bigint` + opt + `
  }
  column "reserve_account" {
    type = text` + opt + `
  }
  column "withdrawal_account" {
    type = text` + opt + `
  }
  # OPEN | DRAFT | POSTED | CANCELLED
  column "status"          { type = text }
  column "check_result" {
    type = text` + opt + `
  }
  column "draft_id" {
    type = text` + opt + `
  }
  column "document_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.period_from] }
  index "contract__settlement_draft" { columns = [column.draft_id] }
  foreign_key "contract__settlement_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

# Position einer Vertragsabrechnung
table "contract__settlement_item" {
  schema = schema.main
  column "company_code"  { type = text }
  column "contract_id"   { type = text }
  column "period_from"   { type = date }
  column "line_no"       { type = bigint }
  column "settlement_group" {
    type = text` + opt + `
  }
  column "cost_category" { type = text }
  # ALLOCABLE | NON_ALLOCABLE | RESERVE (aus der Kostenart übernommen)
  column "cost_type"     { type = text }
  column "total_cost" {
    type = bigint` + opt + `
  }
  column "allocation_key" {
    type = text` + opt + `
  }
  # Dezimaltext (exakt), z. B. 1000 und 85.32
  column "key_total" {
    type = text` + opt + `
  }
  column "key_share" {
    type = text` + opt + `
  }
  column "amount"        { type = bigint }
  column "account_number" { type = text }
  column "object_type" {
    type = text` + opt + `
  }
  column "object_id" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.contract_id, column.period_from, column.line_no] }
  foreign_key "contract__settlement_item_settlement_fk" {
    columns     = [column.company_code, column.contract_id, column.period_from]
    ref_columns = [table.contract__settlement.column.company_code, table.contract__settlement.column.contract_id, table.contract__settlement.column.period_from]
  }
}
`
