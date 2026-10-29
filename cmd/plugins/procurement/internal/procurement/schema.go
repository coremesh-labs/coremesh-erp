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
  # Ausgleich Soll (Positionen) und Haben (Rechnungsbetrag): Position geändert → Betrag folgt, Betrag geändert → Differenzposition
  column "auto_balance" {
    type    = boolean
    default = true
  }
  # seit 0.10.0: Rundungsdifferenz bis zu diesem Betrag (Dezimaltext) gleicht die Steuerzeile aus –
  # Betrag laut Rechnung der Steuerzeile bzw. Rechnungsbetrag laut Beleg (netto erfasste Rechnungen)
  column "tax_tolerance" {
    type    = text
    default = "0.05"
  }
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
  # seit 0.8.0: Positionen netto (nachgelagerte Positionen wie Umsatzsteuer); ältere brutto
  column "net_amounts" {
    type    = boolean
    default = false
  }
  # nachgelagerte Konditionsarten (Komma-Liste)
  column "dependent_types" {
    type = text` + opt + `
  }
  # seit 0.12.0: geplanter Ausführungszeitraum (Liquiditätsvorschau: Zahlung zum Ende)
  column "planned_from" {
    type = date` + opt + `
  }
  column "planned_to" {
    type = date` + opt + `
  }
  primary_key { columns = [column.company_code, column.quote_id] }
  index "procurement__quote_supplier" { columns = [column.company_code, column.supplier_id] }
}

# Positionen eines Angebots (seit 0.8.0): wie die der Rechnung
table "procurement__quote_item" {
  schema = schema.main
  column "company_code"   { type = text }
  column "quote_id"       { type = text }
  column "line_no"        { type = bigint }
  column "item_text" {
    type = text` + opt + `
  }
  column "cost_category" {
    type = text` + opt + `
  }
  column "account_number" {
    type = text` + opt + `
  }
  column "amount"         { type = bigint }
  column "tax_code" {
    type = text` + opt + `
  }
  # seit 0.8.0: Menge, Mengeneinheit, Einzelpreis (Dezimaltext), Rundungsregel; Art der
  # Basis (Konditionsart aus dem Vertrag); nachgelagerte Position (Art, Wirkung, in den
  # Basispositionen verteilt) und Kostenbetrag (netto − Anteil + verteilte Zuschläge
  # und nicht abziehbare Steuer; leer = amount)
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
  column "condition_type" {
    type = text` + opt + `
  }
  column "dependent_type" {
    type = text` + opt + `
  }
  column "dependent_effect" {
    type = text` + opt + `
  }
  column "allocated" {
    type    = boolean
    default = false
  }
  column "cost_amount" {
    type = bigint` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  # seit 0.9.0: Kalkulationsschema der Hauptposition; abhängige Zeile (Hauptposition, Schritt),
  # Satz an der Zeile überschrieben, Betrag laut Rechnung (Prüfung)
  column "scheme" {
    type = text` + opt + `
  }
  column "parent_line" {
    type = bigint` + opt + `
  }
  column "scheme_step" {
    type = bigint` + opt + `
  }
  column "price_override" {
    type    = boolean
    default = false
  }
  column "stated_amount" {
    type = bigint` + opt + `
  }
  # seit 0.14.0: Positionsart (Katalog procurement__item_kind, leer = normal), Alternative zu
  # Position (line_no), Positionsnummer laut Angebot des Lieferanten (z. B. 1.060)
  column "item_kind" {
    type = text` + opt + `
  }
  column "alternative_to" {
    type = bigint` + opt + `
  }
  column "supplier_pos" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.quote_id, column.line_no] }
  foreign_key "procurement__quote_item_quote_fk" {
    columns     = [column.company_code, column.quote_id]
    ref_columns = [table.procurement__quote.column.company_code, table.procurement__quote.column.quote_id]
  }
}

# Kalkulationsschema (seit 0.9.0): einmal definierte Kette abhängiger Positionen, z. B. STROM:
# Netzentgelt, Konzessionsabgabe, Stromsteuer je kWh der Hauptposition.
table "procurement__scheme" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "note" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.code] }
}

# Schritt eines Schemas (Zeitscheibe: Satz ab einem Tag). Bezug: 0 = Hauptposition, sonst
# ein früherer Schritt; Basis QUANTITY (Menge × Satz je Einheit) oder AMOUNT (Prozent vom Betrag).
table "procurement__scheme_step" {
  schema = schema.main
  column "company_code" { type = text }
  column "scheme"       { type = text }
  column "step"         { type = bigint }
  column "valid_from"   { type = date }
  column "valid_to"     { type = date }
  column "name"         { type = text }
  column "ref_step" {
    type    = bigint
    default = 0
  }
  column "basis"        { type = text }
  column "rate"         { type = text }
  column "unit" {
    type = text` + opt + `
  }
  column "tax_code" {
    type = text` + opt + `
  }
  column "cost_category" {
    type = text` + opt + `
  }
  column "account_number" {
    type = text` + opt + `
  }
  column "rounding_rule" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.scheme, column.step, column.valid_from] }
  foreign_key "procurement__scheme_step_scheme_fk" {
    columns     = [column.company_code, column.scheme]
    ref_columns = [table.procurement__scheme.column.company_code, table.procurement__scheme.column.code]
  }
}

# Nachgelagerte Konditionsarten der Beschaffung (seit 0.8.0) – nur ohne Vertragsmodul;
# sonst gelten die des Vertragsmoduls (ContractDependentType).
table "procurement__dependent_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "method"       { type = text }
  column "tax_code" {
    type = text` + opt + `
  }
  column "percent" {
    type = text` + opt + `
  }
  column "effect"       { type = text }
  column "base_types" {
    type = text` + opt + `
  }
  column "account_number" {
    type = text` + opt + `
  }
  column "rounding_rule" {
    type = text` + opt + `
  }
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
  # seit 0.14.0: Auftrag (Positionen übernehmen, Prüfung gegen das Beauftragte)
  column "order_id" {
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
  # Rechnungsbetrag laut Beleg (brutto, kleinste Einheit) – Haben; Positionen sind Soll (Ausgleich)
  column "invoice_amount" {
    type = bigint` + opt + `
  }
  # Steuerkennzeichen (Standard der Positionen)
  column "tax_code" {
    type = text` + opt + `
  }
  # Rahmenvertrag (Rechnung aus einer Vorlage des Vertrags)
  column "framework_contract" {
    type = text` + opt + `
  }
  # OPEN (erfasst) | DRAFT (vorerfasst) | POSTED (gebucht) | CANCELLED (storniert, mit Stornobeleg) | DISCARDED (verworfen, nie gebucht)
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
  # seit 0.8.0: Positionen netto (nachgelagerte Positionen wie Umsatzsteuer); ältere brutto
  column "net_amounts" {
    type    = boolean
    default = false
  }
  # nachgelagerte Konditionsarten (Komma-Liste)
  column "dependent_types" {
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
  column "tax_code" {
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
  # seit 0.8.0: Menge, Mengeneinheit, Einzelpreis (Dezimaltext), Rundungsregel; Art der
  # Basis (Konditionsart aus dem Vertrag); nachgelagerte Position (Art, Wirkung, in den
  # Basispositionen verteilt) und Kostenbetrag (netto − Anteil + verteilte Zuschläge
  # und nicht abziehbare Steuer; leer = amount)
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
  column "condition_type" {
    type = text` + opt + `
  }
  column "dependent_type" {
    type = text` + opt + `
  }
  column "dependent_effect" {
    type = text` + opt + `
  }
  column "allocated" {
    type    = boolean
    default = false
  }
  column "cost_amount" {
    type = bigint` + opt + `
  }
  # seit 0.9.0: Kalkulationsschema der Hauptposition; abhängige Zeile (Hauptposition, Schritt),
  # Satz an der Zeile überschrieben, Betrag laut Rechnung (Prüfung)
  column "scheme" {
    type = text` + opt + `
  }
  column "parent_line" {
    type = bigint` + opt + `
  }
  column "scheme_step" {
    type = bigint` + opt + `
  }
  column "price_override" {
    type    = boolean
    default = false
  }
  column "stated_amount" {
    type = bigint` + opt + `
  }
  # seit 0.11.0: Steueranteile der Basisposition – nicht abziehbar (im Kostenbetrag), abziehbarer
  # Teil der aufzuteilenden Vorsteuer, Vorsteuerquote (Anteil, Erläuterung, Stichtag)
  column "nondeductible_tax" {
    type = bigint` + opt + `
  }
  column "deductible_tax" {
    type = bigint` + opt + `
  }
  column "tax_quota" {
    type = text` + opt + `
  }
  column "tax_quota_text" {
    type = text` + opt + `
  }
  column "tax_quota_date" {
    type = text` + opt + `
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

# Vorsteuerberichtigung (seit 0.11.0): Quote der aufzuteilenden Vorsteuer eines Jahres neu
# gerechnet, Differenz als Vorerfassung im Hauptbuch; Positionen je Rechnungsposition
table "procurement__tax_correction" {
  schema = schema.main
  column "company_code"  { type = text }
  column "correction_id" { type = text }
  column "year"          { type = bigint }
  column "posting_date"  { type = text }
  column "currency"      { type = text }
  column "amount"        { type = bigint }
  # DRAFT (vorerfasst) | POSTED (gebucht) | DISCARDED (verworfen)
  column "status"        { type = text }
  column "draft_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  column "created_at" {
    type = text` + opt + `
  }
  column "created_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.correction_id] }
}

table "procurement__tax_correction_item" {
  schema = schema.main
  column "company_code"  { type = text }
  column "correction_id" { type = text }
  column "invoice_id"    { type = text }
  column "line_no"       { type = bigint }
  column "amount"        { type = bigint }
  column "tax_quota" {
    type = text` + opt + `
  }
  column "tax_quota_text" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.correction_id, column.invoice_id, column.line_no] }
  foreign_key "procurement__tax_correction_item_fk" {
    columns     = [column.company_code, column.correction_id]
    ref_columns = [table.procurement__tax_correction.column.company_code, table.procurement__tax_correction.column.correction_id]
  }
}
`
