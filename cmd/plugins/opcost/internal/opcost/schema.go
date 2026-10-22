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

# Regelwerk der Nebenkostenabrechnung je Abrechnungseinheit (z. B. Nebenkosten, haushaltsnahe Dienstleistungen)
table "opcost__definition" {
  schema = schema.main
  column "company_code"  { type = text }
  column "code"          { type = text }
  column "name"          { type = text }
  # Building | BusinessEntity
  column "unit_type"     { type = text }
  column "unit_id"       { type = text }
  # Rechengenauigkeit: Cent × Faktor (ganzzahlig)
  column "scale" {
    type    = bigint
    default = 100000
  }
  # TENANTS_OR_OWNER (Rest ≥ Anzahl Mieter in Cent: verteilen, sonst Eigentümer) | OWNER
  # | COMMERCIAL (jede Position kaufmännisch, kein Posten Rundung beim Mieter, Differenz an den Eigentümer)
  column "rounding_rule" {
    type    = text
    default = "TENANTS_OR_OWNER"
  }
  # Vertragsarten der Mieter, Konditionsarten der Vorauszahlungen (Listen, Komma)
  column "tenant_contract_types" { type = text }
  column "advance_types" {
    type = text` + opt + `
  }
  # Ledger der Kostenquelle „Sachkonto“
  column "ledger" {
    type    = text
    default = "0L"
  }
  # SETTLEMENT (Abrechnung, wird gebucht) | STATEMENT (Aufstellung ohne Buchung, z. B. haushaltsnahe Dienstleistungen)
  column "mode" {
    type    = text
    default = "SETTLEMENT"
  }
  # Buchung der freigegebenen Abrechnung
  column "advance_account" {
    type = text` + opt + `
  }
  column "revenue_account" {
    type = text` + opt + `
  }
  column "document_type" {
    type    = text
    default = "DR"
  }
  column "credit_document_type" {
    type    = text
    default = "DG"
  }
  column "auto_post" {
    type    = boolean
    default = false
  }
  # Freigabe trotz Fehlern in den Prüfungen (z. B. fehlende Versorgerabrechnung)
  column "release_with_errors" {
    type    = boolean
    default = false
  }
  # Abrechnungsschreiben: Absender = erster wirksamer Partner des Mietobjekts in diesen Rollen (Liste, Komma),
  # sonst der Partner des Buchungskreises; Unterschrift = DMS-Dokument dieser Dokumentart am Absender
  column "sender_roles" {
    type    = text
    default = "SEADM,OWNER"
  }
  column "signature_doc_type" {
    type    = text
    default = "SIGNATURE"
  }
  # Kostenaufstellung (Art COSTS): Empfänger des Schreibens (Geschäftspartner, z. B. Messdienst)
  column "cost_recipient" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.code] }
}

# Regel (Schritt) eines Regelwerks
table "opcost__rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "rule_no"      { type = bigint }
  column "step"         { type = bigint }
  column "description"  { type = text }
  column "kind"         { type = text }
  column "cost_category" {
    type = text` + opt + `
  }
  # COLLECT (Quelle → Topf) | TRANSFER (Topf → Topf) | DISTRIBUTE (Topf → Mietobjekte) | SUM (Konten → Konto) |
  # ADVANCE (Vorauszahlungen → Konto) | BALANCE (Konto A − Konto B → Nachzahlung bzw. Guthaben)
  # COLLECT: LEDGER (Sachkonten) | INVOICE (Eingangsrechnungen, Kostenart) | CONTRACT_SETTLEMENT (Vertragsabrechnungen, Kostenart) | INPUT (Eingaben am Lauf)
  # ADVANCE: BOOKED (Sollstellungen der Konditionsarten) | INPUT (Eingaben am Lauf)
  column "source_type" {
    type = text` + opt + `
  }
  column "source_value" {
    type = text` + opt + `
  }
  # Topf: Ziel bei COLLECT, Quelle bei TRANSFER und DISTRIBUTE
  column "pool"         { type = text }
  column "to_pool" {
    type = text` + opt + `
  }
  column "share_pct" {
    type = text` + opt + `
  }
  column "allocation_key" {
    type = text` + opt + `
  }
  # Zuordnung der Quelle zum Zeitraum: SERVICE_PERIOD | POSTING_DATE | DOCUMENT_DATE
  column "assignment" {
    type = text` + opt + `
  }
  column "vacancy_to_tenants" {
    type    = boolean
    default = false
  }
  # DISTRIBUTE: Gesamtschlüssel OBJECTS (Summe der Mietobjekte) | BUILDING (Bemessung des Gebäudes, z. B. MEA der WEG – Rest an die übrigen Eigentümer)
  column "key_total" {
    type    = text
    default = "OBJECTS"
  }
  # DISTRIBUTE (seit 0.7.0): nicht bei Mietern mit Tag am Mietvertrag (Werte: Komma-Liste, leer = vorhanden) – Anteil trägt der Eigentümer
  column "exclude_tag" {
    type = text` + opt + `
  }
  column "exclude_values" {
    type = text` + opt + `
  }
  # BALANCE: Buchung – Sachkonto der Vorauszahlungen (leer: Vorauszahlungen nicht buchen) und Erlöskonto
  column "post_advance_account" {
    type = text` + opt + `
  }
  column "post_revenue_account" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.definition, column.rule_no] }
  foreign_key "opcost__rule_definition_fk" {
    columns     = [column.company_code, column.definition]
    ref_columns = [table.opcost__definition.column.company_code, table.opcost__definition.column.code]
  }
}

# Abrechnungslauf eines Regelwerks für einen Zeitraum
table "opcost__run" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "period_from"  { type = date }
  column "period_to"    { type = date }
  # DRAFT (neu rechenbar) | RELEASED (freigegeben, unveränderlich) | POSTED (gebucht)
  column "status"       { type = text }
  column "scale" {
    type    = bigint
    default = 100000
  }
  column "computed_at" {
    type = text` + opt + `
  }
  column "summary" {
    type = text` + opt + `
  }
  column "check_result" {
    type = text` + opt + `
  }
  # Belegung im Zeitraum (JSON: Vertrag, Mieter, Mietobjekt, von, bis) – für die Eingabezeilen
  column "occupancy" {
    type = text` + opt + `
  }
  # Kosten je Topf der Abrechnungseinheit (JSON: Konto → Cent)
  column "pool_totals" {
    type = text` + opt + `
  }
  # Anzahl Fehler der Prüfungen (Freigabe gesperrt, außer das Regelwerk erlaubt es)
  column "failures" {
    type = bigint` + opt + `
  }
  column "released_at" {
    type = text` + opt + `
  }
  column "released_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.definition, column.period_from] }
  foreign_key "opcost__run_definition_fk" {
    columns     = [column.company_code, column.definition]
    ref_columns = [table.opcost__definition.column.company_code, table.opcost__definition.column.code]
  }
}

# Journal eines Laufs (Rechenweg, doppelte Buchführung auf Abrechnungskonten)
table "opcost__journal" {
  schema = schema.main
  column "company_code"   { type = text }
  column "definition"     { type = text }
  column "period_from"    { type = date }
  column "line_no"        { type = bigint }
  column "step"           { type = bigint }
  column "rule_no"        { type = bigint }
  column "debit_account"  { type = text }
  column "credit_account" { type = text }
  # Cent × Faktor des Laufs
  column "amount"         { type = bigint }
  column "formula" {
    type = text` + opt + `
  }
  column "source_ref" {
    type = text` + opt + `
  }
  column "cost_category" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.definition, column.period_from, column.line_no] }
}

# Ergebnis je Mieter (Vertrag): Kosten je Kostenart, Vorauszahlungen, Saldo, Buchung
table "opcost__tenant" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "period_from"  { type = date }
  column "contract_id"  { type = text }
  column "partner_id"   { type = text }
  # Mietobjekt der Kontierung (meiste Nutzungstage)
  column "rent_object_id" {
    type = text` + opt + `
  }
  column "usage_from"   { type = date }
  column "usage_to"     { type = date }
  # Cent
  column "costs"        { type = bigint }
  column "advances"     { type = bigint }
  column "balance"      { type = bigint }
  column "lines" {
    type = text` + opt + `
  }
  # Buchungszeilen je Saldo-Regel (JSON: rule, cost, advance in Cent)
  column "post_lines" {
    type = text` + opt + `
  }
  # Rechenweg je Konto (JSON aus contract-billing: account, rent_object_id, total, key, weight, weight_total,
  # use, use_total, use_key, factor, amount; Beträge in Cent) – für das Abrechnungsschreiben
  column "details" {
    type = text` + opt + `
  }
  # OPEN | DRAFT | POSTED
  column "status" {
    type    = text
    default = "OPEN"
  }
  column "draft_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.definition, column.period_from, column.contract_id] }
}

# Abrechnungskonto (internes Konto) eines Regelwerks: Ausweistext, Ausweisgruppe, Bindung, Eingabe
table "opcost__account" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "code"         { type = text }
  # Text auf der Abrechnung
  column "name"         { type = text }
  # Ausweisgruppe (z. B. Heiz- und Warmwasserkosten, Andere Kosten)
  column "display_group" {
    type = text` + opt + `
  }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  # CONTRACT (je Vertrag zusammengefasst) | OBJECT (je Mietobjekt ausweisen)
  column "binding" {
    type    = text
    default = "CONTRACT"
  }
  # Eingabe am Lauf: NONE | TENANT_OBJECT (je Mieter und Wohnung) | TENANT (je Mieter) | OBJECT (je Wohnung) | TOTAL (gesamt)
  column "input_mode" {
    type    = text
    default = "NONE"
  }
  column "on_statement" {
    type    = boolean
    default = true
  }
  # heizkostenrelevant (Aufstellung für den Messdienst)
  column "heating" {
    type    = boolean
    default = false
  }
  # haushaltsnahe Dienstleistung (§ 35a EStG)
  column "hhs" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.definition, column.code] }
  foreign_key "opcost__account_definition_fk" {
    columns     = [column.company_code, column.definition]
    ref_columns = [table.opcost__definition.column.company_code, table.opcost__definition.column.code]
  }
}

# Eingabe am Lauf (Heizkosten, CO2, WEG-Gesamtkosten, Vorauszahlungen …), Cent
table "opcost__input" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "period_from"  { type = date }
  column "line_no"      { type = bigint }
  column "account"      { type = text }
  column "contract_id" {
    type = text` + opt + `
  }
  column "rent_object_id" {
    type = text` + opt + `
  }
  column "amount" {
    type    = bigint
    default = 0
  }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.definition, column.period_from, column.line_no] }
}

# Ergebnis je Ebene: Mieter, Mietobjekt (bei Ausweis je Mietobjekt), Abrechnungskonto, Cent
table "opcost__result" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "period_from"  { type = date }
  column "line_no"      { type = bigint }
  column "contract_id"  { type = text }
  column "rent_object_id" {
    type = text` + opt + `
  }
  column "account"      { type = text }
  column "level"        { type = bigint }
  column "amount"       { type = bigint }
  column "formula" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.definition, column.period_from, column.line_no] }
}

# Vorlagen je Regelwerk (Abrechnung an Mieter, Gesamtschau, Excel)
table "opcost__template" {
  schema = schema.main
  column "company_code" { type = text }
  column "definition"   { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  # TENANT_LETTER | OBJECT_OVERVIEW | EXCEL | COST_SHEET
  column "usage"        { type = text }
  column "template" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.definition, column.code] }
  foreign_key "opcost__template_definition_fk" {
    columns     = [column.company_code, column.definition]
    ref_columns = [table.opcost__definition.column.company_code, table.opcost__definition.column.code]
  }
}
`
