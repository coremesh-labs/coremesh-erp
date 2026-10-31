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
  # Bezugsvertrag Pflicht (seit 0.36.0; leer = Pflicht, wenn parent_types gesetzt; aus = nur erlaubt)
  column "parent_required" {
    type = boolean` + opt + `
  }
  # Vertragspartner Pflicht (seit 0.34.0; leer = noch nicht umgestellt, s. migratePartnerRequired),
  # z. B. aus beim Kaufvertrag zur Abschreibung
  column "partner_required" {
    type = boolean` + opt + `
  }
  # Vertragsklauseln und Vertragsdokument am Vertrag (seit 0.40.0; leer = noch nicht
  # umgestellt, s. migrateClausesEnabled)
  column "clauses_enabled" {
    type = boolean` + opt + `
  }
  # Partner braucht in der Rolle der Vertragsart Buchungskreisdaten mit Abstimmkonto
  column "partner_account_required" {
    type    = boolean
    default = true
  }
  # Aktivieren ohne Konditionen erlaubt (seit 0.10.5), z. B. Hausgeld an die WEG,
  # das erst mit der Abrechnung der WEG gebucht wird
  column "without_conditions" {
    type    = boolean
    default = false
  }
  # Eingangsrechnungen aus Vorlagen (Konditionen mit Konditionsart „Vorlage“), z. B. Rahmenvertrag (seit 0.19.0);
  # seit 0.26.0 aus billing_mode abgeleitet (INVOICE, BOTH)
  column "template_invoices" {
    type    = boolean
    default = false
  }
  # seit 0.26.0: Abrechnungsweg POSTING (Sollstellung) | INVOICE (Eingangsrechnung: Konditionen sind
  # Vorlagen, keine Sollstellung) | BOTH (beides, je Konditionsart; z. B. Abschläge und Schlussrechnung)
  column "billing_mode" {
    type = text` + opt + `
  }
  # Rechnungsart der Eingangsrechnungen aus Vorlagen (Beschaffung; leer = ER)
  column "invoice_type" {
    type = text` + opt + `
  }
  # Standard-Steuerkennzeichen (Plugin ledger) der Rechnungen zu Verträgen dieser Art
  column "default_tax_code" {
    type = text` + opt + `
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

# Konditionsart (seit 0.26.0): Verhalten bei der Abrechnung – Abrechnungsform,
# Forderungsklasse, Verrechnungsreihenfolge, Monatsraten.
table "contract__condition_kind" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # CLAIM (Forderung/Entgelt) | ADVANCE (Vorauszahlung, wird abgerechnet) | PARTIAL (Abschlag, wird mit
  # der Schlussrechnung verrechnet) | INVOICE (Rechnungsposition: keine Sollstellung, Vorlage für Eingangsrechnungen)
  column "billing_form" { type = text }
  # MAIN (Hauptforderung) | SECONDARY (Nebenforderung: Kosten, Zinsen, Gebühren)
  column "claim_class"  { type = text }
  # Reihenfolge der Verrechnung von Zahlungen (§ 367 BGB: Kosten, Zinsen, Hauptleistung)
  column "clearing_order" {
    type    = bigint
    default = 30
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

# Konditionsgegenstand (bis 0.25.0 „Konditionsart“): was geleistet wird – Bezeichnung,
# Steuerkennzeichen, Rundung, Standard-Konditionsart. Die Verhaltensspalten (claim_class …
# installments) folgen seit 0.26.0 der Standard-Konditionsart (für ältere Leser).
table "contract__condition_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # seit 0.26.0: Standard-Konditionsart (contract__condition_kind) neuer Konditionen
  column "default_kind" {
    type = text` + opt + `
  }
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
  # seit 0.45.0: Monatsbetrag aus dem Wirtschaftsplan – Summe dieser Arten (ALLOCABLE,
  # NON_ALLOCABLE, RESERVE) / Monate, wenn der Plan keine Zahlung nennt
  column "plan_cost_types" {
    type = text` + opt + `
  }
  # seit 0.22.0: Rundungsregel der Konditionsart (Plugin ledger; leer = Buchungskreis)
  column "rounding_rule" {
    type = text` + opt + `
  }
  # Rechnungsvorlage (seit 0.19.0): keine Sollstellung; Vorlage für Eingangsrechnungen zum Vertrag
  column "is_template" {
    type    = boolean
    default = false
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

# Kontenfindung: zulässige Sachkonten je Vertragsart, Konditionsgegenstand und (seit 0.26.0)
# Konditionsart (leer = alle; ein Eintrag zur Konditionsart geht vor).
table "contract__account" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_type"  { type = text }
  column "condition_type" { type = text }
  column "account_number" { type = text }
  column "condition_kind" {
    type = text` + opt + `
  }
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
  # Unsere Kundennummer beim Partner (z. B. auf seinen Rechnungen) – Erkennung im DMS
  column "customer_number" {
    type = text
    null = true
  }
  column "contract_type"   { type = text }
  column "designation"     { type = text }
  column "direction"       { type = text }
  column "currency"        { type = text }
  # Vertragspartner beim Anlegen (Hauptrolle); Wechsel im Abschnitt Partner
  # leer, wenn die Vertragsart keinen Vertragspartner verlangt (seit 0.34.0)
  column "partner_id" {
    type = text` + opt + `
  }
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
  # Sollstellung ab (z. B. Übernahme aus einem Altsystem); leer = Beginn
  column "billing_start" {
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
  # Vertragsklauseln (seit 0.39.0): Set, Stand der Kopie (Fassung), Prüfsumme der Klauseln
  column "clause_set" {
    type = text` + opt + `
  }
  column "clauses_at" {
    type = text` + opt + `
  }
  column "clauses_by" {
    type = text` + opt + `
  }
  column "clauses_hash" {
    type = text` + opt + `
  }
  # seit 0.44.0: Folgevertrag (ersetzt diesen) – keine Erinnerungen mehr
  column "successor_contract_id" {
    type = text` + opt + `
  }
  # seit 0.45.0: Zahlweg STANDING_ORDER | DIRECT_DEBIT | TRANSFER (Erinnerungsregeln je Zahlweg)
  column "payment_method" {
    type = text` + opt + `
  }
  # seit 0.46.0: SEPA-Lastschriftmandat (Prüfung der Abbuchungen im Bankmodul)
  column "mandate_ref" {
    type = text` + opt + `
  }
  column "creditor_id" {
    type = text` + opt + `
  }
  column "mandate_date" {
    type = date` + opt + `
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

# Nachgelagerte Konditionsarten (seit 0.22.0): aus der eigentlichen Forderung (Basis)
# entsteht eine weitere – Steuer (Satz des Steuerkennzeichens) oder Prozent; als
# Zuschlag (kommt hinzu, z. B. Umsatzsteuer) oder Anteil (herausgerechnet, z. B.
# Materialanteil). Die Beschaffung übernimmt sie für Rechnungen und Angebote.
table "contract__dependent_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # TAX | PERCENT
  column "method"       { type = text }
  # TAX: festes Steuerkennzeichen; leer = das der Basis (Konditionsart)
  column "tax_code" {
    type = text` + opt + `
  }
  column "percent" {
    type = text` + opt + `
  }
  # SURCHARGE (Zuschlag) | SHARE (Anteil)
  column "effect"       { type = text }
  # Konditionsarten der Basis (Komma-Liste; leer = alle außer Steuer)
  column "base_types" {
    type = text` + opt + `
  }
  column "account_number" {
    type = text` + opt + `
  }
  column "rounding_rule" {
    type = text` + opt + `
  }
  # Standard für Rechnungen und Angebote ohne Vertrag (Beschaffung)
  column "invoice_default" {
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

table "contract__condition" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_id"    { type = text }
  # Vertragsart (übernommen; Kontenfindung)
  column "contract_type"  { type = text }
  # Konditionsgegenstand (contract__condition_type)
  column "condition_type" { type = text }
  # seit 0.26.0: Konditionsart (contract__condition_kind; leer beim Anlegen = Standard des Gegenstands)
  column "condition_kind" {
    type = text` + opt + `
  }
  # Objekt der Kondition ("" = ganzer Vertrag)
  column "object_id" {
    type    = text
    default = ""
  }
  # seit 0.29.0: Position – derselbe Gegenstand mehrfach je Objekt (z. B. zweimal Entgelt)
  column "position" {
    type    = bigint
    default = 1
  }
  column "valid_from"     { type = date }
  column "valid_to"       { type = date }
  # FIXED (Menge × Einzelpreis) | PER_UNIT (Preis je Einheit der Bemessung) | DEPENDENT (nachgelagert, seit 0.22.0)
  column "calc_method"    { type = text }
  # kleinste Einheit der Währung: FIXED Gesamtpreis je Periode; PER_UNIT Preis je Einheit; DEPENDENT 0
  column "amount"         { type = bigint }
  # seit 0.22.0: Menge, Mengeneinheit (Plugin ledger), Einzelpreis (Dezimaltext, bis zu den
  # Nachkommastellen der Rundungsregel), Rundungsregel, nachgelagerte Konditionsart
  column "quantity" {
    type = double` + opt + `
  }
  column "unit" {
    type = text` + opt + `
  }
  column "unit_price" {
    type = text` + opt + `
  }
  column "rounding_rule" {
    type = text` + opt + `
  }
  column "dependent_type" {
    type = text` + opt + `
  }
  # seit 0.24.0: Kalkulationsschema der Beschaffung (PricingScheme) – für Rechnungen zum Vertrag
  column "scheme" {
    type = text` + opt + `
  }
  column "measurement_type" {
    type = text` + opt + `
  }
  # MONTHLY | BIMONTHLY | QUARTERLY | HALF_YEARLY | YEARLY | ONCE
  column "frequency"      { type = text }
  column "due_day" {
    type    = bigint
    default = 1
  }
  # seit 0.16.0: Periodenbeginn (Monat 1–12; leer = Kalender), z. B. 10 = Versicherungsjahr Okt–Sep
  column "period_start_month" {
    type = bigint` + opt + `
  }
  # seit 0.17.0: vorschüssig fällig im n-ten Monat der Periode (leer = erster), z. B. 3 = Jahresmiete fällig im März
  column "due_month" {
    type = bigint` + opt + `
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
  # seit 0.44.0: Garantie bis (z. B. Preisgarantie) – Erinnerungen nach contract__reminder_rule
  column "guaranteed_until" {
    type = date` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.condition_type, column.object_id, column.position, column.valid_from] }
  foreign_key "contract__condition_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
  foreign_key "contract__condition_type_fk" {
    columns     = [column.company_code, column.condition_type]
    ref_columns = [table.contract__condition_type.column.company_code, table.contract__condition_type.column.code]
  }
}

# Erinnerungsregeln (seit 0.44.0): je Vertragsart und Konditionsart – Vorlauf vor dem
# Ablauf der Garantie (contract__condition.guaranteed_until), Abstand, Anzahl, Beschreibung.
table "contract__reminder_rule" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_type"  { type = text }
  column "condition_kind" { type = text }
  # seit 0.45.0: Anlass GUARANTEE_END (Garantie bis) | AMOUNT_CHANGE (neue Zeitscheibe mit anderem Betrag)
  column "occasion" {
    type    = text
    default = "GUARANTEE_END"
  }
  # nur Anlässe ab diesem Tag (leer = alle); beim Anlegen heute
  column "active_from" {
    type = date` + opt + `
  }
  # nur Verträge mit diesem Zahlweg (Komma-Liste; leer = alle)
  column "payment_methods" {
    type = text` + opt + `
  }
  column "description"    { type = text }
  column "lead_days"      { type = bigint }
  # leer = einmal
  column "repeat_days" {
    type = bigint` + opt + `
  }
  # leer = bis zum Ablauf
  column "max_count" {
    type = bigint` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.contract_type, column.condition_kind, column.occasion] }
  foreign_key "contract__reminder_rule_type_fk" {
    columns     = [column.company_code, column.contract_type]
    ref_columns = [table.contract__contract_type.column.company_code, table.contract__contract_type.column.code]
  }
}

# Erinnerungen (seit 0.44.0): je Kondition, Ablauf (Garantie bis) und lfd. Nummer.
# OPEN | REPEATED (neuere offen) | DONE | OBSOLETE (Folgevertrag, Garantie/Regel geändert)
table "contract__reminder" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_id"    { type = text }
  column "condition_type" { type = text }
  column "object_id"      { type = text }
  column "position"       { type = bigint }
  column "due_date"       { type = date }
  column "seq"            { type = bigint }
  column "occasion" {
    type    = text
    default = "GUARANTEE_END"
  }
  # seit 0.45.0: Einzelheiten zum Anlass (z. B. „ab 01.01.2027 244,00 statt 224,00 EUR“)
  column "detail" {
    type = text` + opt + `
  }
  column "remind_date"    { type = date }
  column "contract_type"  { type = text }
  column "condition_kind" { type = text }
  column "description"    { type = text }
  column "status"         { type = text }
  column "reason" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "created_at" {
    type = text` + opt + `
  }
  column "done_at" {
    type = text` + opt + `
  }
  column "done_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.condition_type, column.object_id, column.position, column.occasion, column.due_date, column.seq] }
  index "contract__reminder_status" {
    columns = [column.company_code, column.status]
  }
  foreign_key "contract__reminder_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
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
  # Position der Kondition (seit 0.29.0)
  column "position" {
    type    = bigint
    default = 1
  }
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
  primary_key { columns = [column.company_code, column.contract_id, column.condition_type, column.object_id, column.position, column.period_from, column.sequence] }
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

# Abschreibung (AfA) von Häusern und Wohnungen (seit 0.11.0, Zeitscheiben)
table "contract__depreciation" {
  schema = schema.main
  column "company_code"    { type = text }
  column "contract_id"     { type = text }
  column "valid_from"      { type = date }
  column "valid_to"        { type = date }
  # kleinste Einheit der Vertragswährung; kumuliert (Zugänge = neue Zeitscheibe)
  column "base_amount"     { type = bigint }
  column "land_amount" {
    type = bigint` + opt + `
  }
  # LINEAR | DEGRESSIVE
  column "method"          { type = text }
  # Prozent p. a. als Dezimaltext, z. B. 2.5
  column "rate"            { type = text }
  column "start_date" {
    type = date` + opt + `
  }
  column "opening_value" {
    type = bigint` + opt + `
  }
  # fester Jahresbetrag statt Bemessungsgrundlage × Satz (nur linear, seit 0.33.0)
  column "annual_amount" {
    type = bigint` + opt + `
  }
  # Rundungsregel des Jahresbetrags (ledger RoundingRule; leer = Konditionsart, sonst Cent), seit 0.35.0
  column "rounding_rule" {
    type = text` + opt + `
  }
  # Raten im Jahr (Teiler von 12), Rundungsrest in der letzten Rate
  column "installments" {
    type    = bigint
    default = 1
  }
  column "asset_account"   { type = text }
  column "expense_account" { type = text }
  column "condition_type"  { type = text }
  primary_key { columns = [column.company_code, column.contract_id, column.valid_from] }
  foreign_key "contract__depreciation_contract_fk" {
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

# Wirtschaftsplan am Vertrag (z. B. Hausgeld der WEG), seit 0.13.0
table "contract__plan" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "period_from"  { type = date }
  column "period_to"    { type = date }
  column "plan_date" {
    type = date` + opt + `
  }
  column "reference" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  # seit 0.45.0: Zahlung ab (leer = Wirtschaftsjahr von) – Beginn der Konditionen aus dem Plan
  column "payment_from" {
    type = date` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.period_from] }
  foreign_key "contract__plan_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

# Zahlungen laut Wirtschaftsplan (seit 0.45.0): Monatsbetrag je Konditionsgegenstand,
# wie der Plan ihn nennt (z. B. gerundet); „Konditionen übernehmen“ legt sie an.
table "contract__plan_payment" {
  schema = schema.main
  column "company_code"   { type = text }
  column "contract_id"    { type = text }
  column "period_from"    { type = date }
  column "condition_type" { type = text }
  column "amount"         { type = bigint }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.period_from, column.condition_type] }
  foreign_key "contract__plan_payment_plan_fk" {
    columns     = [column.company_code, column.contract_id, column.period_from]
    ref_columns = [table.contract__plan.column.company_code, table.contract__plan.column.contract_id, table.contract__plan.column.period_from]
  }
}

# Position eines Wirtschaftsplans
table "contract__plan_item" {
  schema = schema.main
  column "company_code"  { type = text }
  column "contract_id"   { type = text }
  column "period_from"   { type = date }
  column "line_no"       { type = bigint }
  column "description" {
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
  column "key_total" {
    type = text` + opt + `
  }
  column "key_share" {
    type = text` + opt + `
  }
  column "amount"        { type = bigint }
  column "note" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.contract_id, column.period_from, column.line_no] }
  foreign_key "contract__plan_item_plan_fk" {
    columns     = [column.company_code, column.contract_id, column.period_from]
    ref_columns = [table.contract__plan.column.company_code, table.contract__plan.column.contract_id, table.contract__plan.column.period_from]
  }
}

# Personen je Vertrag (optional, Zeitscheiben) – Umlage nach Personen
table "contract__persons" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  column "persons"      { type = bigint }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.valid_from] }
  foreign_key "contract__persons_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}

# Bankkonto bzw. Kreditkarte eines Vertrags (Zeitscheiben); von der Karte nur die letzten 4 Ziffern
table "contract__bank_account" {
  schema = schema.main
  column "company_code" { type = text }
  column "contract_id"  { type = text }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  column "kind"         { type = text }
  column "account_holder" {
    type = text` + opt + `
  }
  column "iban" {
    type = text` + opt + `
  }
  column "bic" {
    type = text` + opt + `
  }
  column "card_last4" {
    type = text` + opt + `
  }
  column "card_expiry" {
    type = text` + opt + `
  }
  column "settlement_day" {
    type = bigint` + opt + `
  }
  column "debit_contract_id" {
    type = text` + opt + `
  }
  column "credit_limit" {
    type = bigint` + opt + `
  }
  column "debit_rate" {
    type = text` + opt + `
  }
  column "credit_rate" {
    type = text` + opt + `
  }
  column "gl_account"   { type = text }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.valid_from] }
  index "contract__bank_account_gl" { columns = [column.company_code, column.gl_account] }
  foreign_key "contract__bank_account_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}
# Vertragsklauseln (seit 0.39.0): Klauselsets je Vertragsart, Klauseln mit Paragraph,
# Absatz, Markdown-Text, Bedingung, Bewertung und Hinweis; Kopie am Vertrag mit der
# gedruckten Nummer.
table "contract__clause_set" {
  schema = schema.main
  column "company_code"   { type = text }
  column "code"           { type = text }
  column "name"           { type = text }
  column "contract_types" {
    type = text` + opt + `
  }
  column "title" {
    type = text` + opt + `
  }
  column "template" {
    type = text` + opt + `
  }
  column "own_label" {
    type = text` + opt + `
  }
  column "own_roles" {
    type = text` + opt + `
  }
  column "partner_label" {
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

table "contract__clause" {
  schema = schema.main
  column "company_code" { type = text }
  column "set_code"     { type = text }
  column "clause_key"   { type = text }
  column "paragraph"    { type = bigint }
  column "subsection" {
    type    = bigint
    default = 0
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "title" {
    type = text` + opt + `
  }
  column "text" {
    type = text` + opt + `
  }
  column "cond_source" {
    type = text` + opt + `
  }
  column "cond_name" {
    type = text` + opt + `
  }
  column "cond_values" {
    type = text` + opt + `
  }
  column "cond_negate" {
    type    = boolean
    default = false
  }
  column "rating" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "changed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.set_code, column.clause_key] }
  index "contract__clause_order" {
    columns = [column.company_code, column.set_code, column.paragraph, column.subsection]
  }
  foreign_key "contract__clause_set_fk" {
    columns     = [column.company_code, column.set_code]
    ref_columns = [table.contract__clause_set.column.company_code, table.contract__clause_set.column.code]
  }
}

table "contract__clause_copy" {
  schema = schema.main
  column "company_code"  { type = text }
  column "contract_id"   { type = text }
  column "seq"           { type = bigint }
  column "clause_key"    { type = text }
  column "paragraph_no"  { type = bigint }
  column "subsection_no" { type = bigint }
  column "title" {
    type = text` + opt + `
  }
  column "text" {
    type = text` + opt + `
  }
  column "rating" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.contract_id, column.seq] }
  foreign_key "contract__clause_copy_contract_fk" {
    columns     = [column.company_code, column.contract_id]
    ref_columns = [table.contract__contract.column.company_code, table.contract__contract.column.contract_id]
  }
}
`
