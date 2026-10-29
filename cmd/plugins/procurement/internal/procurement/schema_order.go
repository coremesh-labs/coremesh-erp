package procurement

// orderSchemaHCL: Auftragserteilung (seit 0.14.0) – Positionsarten der Angebote,
// Auftragsarten, Textbausteine, Kontaktrollen, Auftrag mit Angebotsbezügen,
// Positionen, Texten und Kontakten.
const orderSchemaHCL = `
# Positionsart eines Angebots je Buchungskreis, z. B. normal, alternativ, Bedarfsposition
table "procurement__item_kind" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # zählt zur Summe des Angebots (alternative und Bedarfspositionen nicht)
  column "in_total" {
    type    = boolean
    default = true
  }
  # beim Beauftragen vorausgewählt (sonst wählt man sie dazu)
  column "preselected" {
    type    = boolean
    default = true
  }
  # Kürzel im Auftragsschreiben (z. B. Alt., Bed.)
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
  primary_key { columns = [column.company_code, column.code] }
}

# Auftragsart je Buchungskreis: Vorlage, Dokumentart im DMS, Absender, Unterschrift, E-Mail
table "procurement__order_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # Vorlage (Code|Sprache) der Verwendung PURCHASE_ORDER; leer = die erste
  column "template" {
    type = text` + opt + `
  }
  # Dokumentart im DMS für das Auftragsschreiben
  column "doc_type" {
    type = text` + opt + `
  }
  # Absender: erster Partner des Objekts in diesen Rollen (Komma), sonst der des Buchungskreises
  column "sender_roles" {
    type = text` + opt + `
  }
  # Unterschrift: DMS-Dokument dieser Dokumentart am Absender
  column "signature_doc_type" {
    type = text` + opt + `
  }
  # Betreff des Schreibens und der E-Mail (Platzhalter wie in den Textbausteinen)
  column "subject" {
    type = text` + opt + `
  }
  column "mail_subject" {
    type = text` + opt + `
  }
  # Textbaustein (Abschnitt MAIL) für den Text der E-Mail
  column "mail_block" {
    type = text` + opt + `
  }
  # Kommunikationskategorie der E-Mail-Adresse des Lieferanten (z. B. EMAIL)
  column "mail_category" {
    type = text` + opt + `
  }
  # Positionen im Schreiben aufführen
  column "show_items" {
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

# Textbaustein (Markdown mit Platzhaltern) je Buchungskreis
table "procurement__text_block" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # INTRO (Einleitung), AGREEMENT (Vereinbarung, nummeriert), CLOSING (Schluss), MAIL (E-Mail)
  column "section"      { type = text }
  column "text"         { type = text }
  # bei jedem neuen Auftrag übernehmen
  column "is_default" {
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

# Rolle eines Kontakts im Auftrag mit ihrem Satz (Markdown, Platzhalter {kontakt:…})
table "procurement__contact_role" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "text" {
    type = text` + opt + `
  }
  # Kommunikationskategorien im Schreiben (Komma, z. B. PHONE,MOBILE,EMAIL; leer = alle)
  column "categories" {
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

# Auftrag an einen Lieferanten (Handwerker, Dienstleister)
table "procurement__order" {
  schema = schema.main
  column "company_code"  { type = text }
  column "order_id"      { type = text }
  column "order_type"    { type = text }
  column "supplier_id"   { type = text }
  column "description"   { type = text }
  column "order_date"    { type = date }
  column "object_type" {
    type = text` + opt + `
  }
  column "object_id" {
    type = text` + opt + `
  }
  # Objektangabe im Schreiben (z. B. Wohnung, Lage); leer = Bezeichnung des Objekts
  column "object_text" {
    type = text` + opt + `
  }
  column "cost_category" {
    type = text` + opt + `
  }
  column "planned_from" {
    type = date` + opt + `
  }
  column "planned_to" {
    type = date` + opt + `
  }
  column "currency"      { type = text }
  column "net_amounts" {
    type    = boolean
    default = true
  }
  column "dependent_types" {
    type = text` + opt + `
  }
  # kleinste Einheit der Währung, brutto (Summe der Positionen)
  column "amount" {
    type    = bigint
    default = 0
  }
  # Anrede (leer = Briefanrede des Lieferanten), Betreff (leer = der Auftragsart), Unser Zeichen
  column "salutation" {
    type = text` + opt + `
  }
  column "subject" {
    type = text` + opt + `
  }
  column "our_reference" {
    type = text` + opt + `
  }
  # DRAFT (Entwurf) | ISSUED (erteilt) | CANCELLED (storniert)
  column "status"        { type = text }
  column "issued_at" {
    type = text` + opt + `
  }
  column "issued_by" {
    type = text` + opt + `
  }
  # Auftragsschreiben im DMS
  column "document_id" {
    type = text` + opt + `
  }
  column "document_name" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.order_id] }
  index "procurement__order_supplier" { columns = [column.company_code, column.supplier_id] }
}

# Angebote eines Auftrags
table "procurement__order_quote" {
  schema = schema.main
  column "company_code" { type = text }
  column "order_id"     { type = text }
  column "quote_id"     { type = text }
  primary_key { columns = [column.company_code, column.order_id, column.quote_id] }
  foreign_key "procurement__order_quote_fk" {
    columns     = [column.company_code, column.order_id]
    ref_columns = [table.procurement__order.column.company_code, table.procurement__order.column.order_id]
  }
}

# Positionen eines Auftrags: wie die des Angebots, mit Herkunft
table "procurement__order_item" {
  schema = schema.main
  column "company_code"   { type = text }
  column "order_id"       { type = text }
  column "line_no"        { type = bigint }
  column "quote_id" {
    type = text` + opt + `
  }
  column "quote_line" {
    type = bigint` + opt + `
  }
  column "supplier_pos" {
    type = text` + opt + `
  }
  column "item_kind" {
    type = text` + opt + `
  }
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
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.order_id, column.line_no] }
  foreign_key "procurement__order_item_fk" {
    columns     = [column.company_code, column.order_id]
    ref_columns = [table.procurement__order.column.company_code, table.procurement__order.column.order_id]
  }
}

# Texte eines Auftrags (Kopien der Textbausteine, änderbar)
table "procurement__order_text" {
  schema = schema.main
  column "company_code" { type = text }
  column "order_id"     { type = text }
  column "line_no"      { type = bigint }
  column "section"      { type = text }
  column "seq" {
    type    = bigint
    default = 0
  }
  column "block" {
    type = text` + opt + `
  }
  column "text"         { type = text }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.order_id, column.line_no] }
  foreign_key "procurement__order_text_fk" {
    columns     = [column.company_code, column.order_id]
    ref_columns = [table.procurement__order.column.company_code, table.procurement__order.column.order_id]
  }
}

# Kontakte eines Auftrags: Geschäftspartner in einer Rolle (als Vereinbarung im Schreiben)
table "procurement__order_contact" {
  schema = schema.main
  column "company_code" { type = text }
  column "order_id"     { type = text }
  column "line_no"      { type = bigint }
  column "partner_id"   { type = text }
  column "role"         { type = text }
  column "seq" {
    type    = bigint
    default = 0
  }
  # Ansprechpartner beim Partner (z. B. i.A. Siebel), Zusatz (z. B. Filiale Musterstadt)
  column "person" {
    type = text` + opt + `
  }
  column "addition" {
    type = text` + opt + `
  }
  # Kommunikationskategorien (leer = die der Rolle)
  column "categories" {
    type = text` + opt + `
  }
  column "show_address" {
    type    = boolean
    default = true
  }
  # eigener Satz statt dem der Rolle
  column "text" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.order_id, column.line_no] }
  foreign_key "procurement__order_contact_fk" {
    columns     = [column.company_code, column.order_id]
    ref_columns = [table.procurement__order.column.company_code, table.procurement__order.column.order_id]
  }
}
`
