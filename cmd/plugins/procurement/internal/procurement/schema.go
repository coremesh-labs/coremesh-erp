package procurement

// opt: Spalte darf leer sein.
const opt = `
    null = true`

// schemaHCL: Soll-Schema (Atlas-HCL) der Beschaffung. Tabellen mit dem
// Pflicht-Präfix procurement__.
const schemaHCL = `
# Rechnungsart je Buchungskreis: Belegarten im Hauptbuch, Lieferantenrolle, Nummernkreis
table "procurement__invoice_type" {
  schema = schema.main
  column "company_code"  { type = text }
  column "code"          { type = text }
  column "name"          { type = text }
  # Belegart im Hauptbuch (Rechnung) und bei negativem Saldo (Gutschrift)
  column "document_type" { type = text }
  column "credit_document_type" { type = text }
  # Rolle des Lieferanten im Partnermodul (Finanzrolle mit Abstimmkonto)
  column "supplier_role" { type = text }
  # Intervallschlüssel im Nummernkreis SupplierInvoice
  column "range_key"     { type = text }
  # geprüfte Vorerfassung gleich buchen (sonst bleibt sie offen)
  column "auto_post" {
    type    = boolean
    default = false
  }
  # Positionen mit umlagefähiger Kostenart brauchen einen Leistungszeitraum (Nebenkostenabrechnung)
  column "service_period_required" {
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

# bis 0.1.1: Kostenarten; seit 0.2.0 im Modul Betriebskosten (opcost) – ungenutzt
table "procurement__cost_category" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # Vorschlag Sachkonto (Aufwand)
  column "account_number" {
    type = text` + opt + `
  }
  # Vorschlag: auf Mieter umlagefähig (Nebenkostenabrechnung)
  column "allocable" {
    type    = boolean
    default = false
  }
  # Nr. nach § 2 BetrKV (z. B. 2 = Wasserversorgung), leer = keine Betriebskosten
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

# Angebot eines Lieferanten (Handwerker, Dienstleister)
table "procurement__quote" {
  schema = schema.main
  column "company_code"  { type = text }
  column "quote_id"      { type = text }
  column "supplier_id"   { type = text }
  column "supplier_reference" {
    type = text` + opt + `
  }
  column "object_type" {
    type = text` + opt + `
  }
  column "object_id" {
    type = text` + opt + `
  }
  column "cost_category" {
    type = text` + opt + `
  }
  column "description"   { type = text }
  column "quote_date"    { type = date }
  column "valid_until" {
    type = date` + opt + `
  }
  # kleinste Einheit der Währung, brutto
  column "amount"        { type = bigint }
  column "currency"      { type = text }
  # OPEN | ACCEPTED | REJECTED
  column "status"        { type = text }
  column "decided_at" {
    type = text` + opt + `
  }
  column "decided_by" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.quote_id] }
  index "procurement__quote_supplier" { columns = [column.company_code, column.supplier_id] }
}

# Eingangsrechnung (Kopf)
table "procurement__invoice" {
  schema = schema.main
  column "company_code"  { type = text }
  column "invoice_id"    { type = text }
  column "invoice_type"  { type = text }
  column "supplier_id"   { type = text }
  # Rechnungsnummer des Lieferanten (je Lieferant eindeutig)
  column "supplier_reference" { type = text }
  column "invoice_date"  { type = date }
  column "posting_date"  { type = date }
  column "due_date" {
    type = date` + opt + `
  }
  column "currency"      { type = text }
  column "quote_id" {
    type = text` + opt + `
  }
  # Standard-Objekt der Positionen
  column "object_type" {
    type = text` + opt + `
  }
  column "object_id" {
    type = text` + opt + `
  }
  column "header_text" {
    type = text` + opt + `
  }
  # OPEN (erfasst) | DRAFT (vorerfasst) | POSTED (gebucht) | CANCELLED (storniert)
  column "status"        { type = text }
  column "draft_id" {
    type = text` + opt + `
  }
  column "document_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  column "reversal_document_id" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "changed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.invoice_id] }
  index "procurement__invoice_supplier" { columns = [column.company_code, column.supplier_id, column.supplier_reference] }
  index "procurement__invoice_draft" { columns = [column.draft_id] }
  foreign_key "procurement__invoice_type_fk" {
    columns     = [column.company_code, column.invoice_type]
    ref_columns = [table.procurement__invoice_type.column.company_code, table.procurement__invoice_type.column.code]
  }
}

# Position einer Eingangsrechnung
table "procurement__invoice_item" {
  schema = schema.main
  column "company_code"   { type = text }
  column "invoice_id"     { type = text }
  column "line_no"        { type = bigint }
  column "cost_category" {
    type = text` + opt + `
  }
  column "account_number" { type = text }
  # kleinste Einheit, brutto; negativ = Gutschrift
  column "amount"         { type = bigint }
  column "object_type" {
    type = text` + opt + `
  }
  column "object_id" {
    type = text` + opt + `
  }
  # auf Mieter umlagefähig (Nebenkostenabrechnung)
  column "allocable" {
    type    = boolean
    default = false
  }
  # Leistungszeitraum (Zuordnung zur Abrechnungsperiode)
  column "service_from" {
    type = date` + opt + `
  }
  column "service_to" {
    type = date` + opt + `
  }
  column "cost_center" {
    type = text` + opt + `
  }
  # Kosten eines Mieters (z. B. zusätzliche Anfahrt des Messdienstes): direkt auf seinen Vertrag
  column "contract_id" {
    type = text` + opt + `
  }
  column "item_text" {
    type = text` + opt + `
  }
  # „Entfernen“ einer Position (solange die Rechnung erfasst ist)
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.invoice_id, column.line_no] }
  foreign_key "procurement__invoice_item_invoice_fk" {
    columns     = [column.company_code, column.invoice_id]
    ref_columns = [table.procurement__invoice.column.company_code, table.procurement__invoice.column.invoice_id]
  }
}

# Kontierung der Objekte: welches Feld im Hauptbuch eine Objektart bekommt
table "procurement__object_posting" {
  schema = schema.main
  column "company_code" { type = text }
  column "object_type"  { type = text }
  # Kontierungsfeld der Einzelposten, z. B. rent_object_id, dimension_custom_1
  column "ledger_field" { type = text }
  primary_key { columns = [column.company_code, column.object_type] }
}
`
