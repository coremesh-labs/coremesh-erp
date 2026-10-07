package ledger

import "github.com/camel/coremesh/pkg/sdk"

// Schema des Hauptbuchs (Atlas HCL, maßgeblich für die Migration; das
// PostgreSQL-DDL in docs/ledger_schema_postgres.sql beschreibt dasselbe Schema
// und wird per Test gegen diese Definition geprüft).
//
// Konventionen:
//   - Präfix ledger__ (von DBSchema vorgegeben), keine DROPs.
//   - Beträge als ganze Zahl in der kleinsten Einheit der Währung
//     (ledger__currency.decimals), vorzeichenbehaftet wie in ACDOCA:
//     Soll positiv, Haben negativ. Summe eines Belegs = 0.
//   - Fremdschlüssel nur auf eigene Tabellen; Buchungskreise gehören dem
//     Core-Plugin iam und werden über dessen Actions geprüft.
//   - JSON-Spalten (module_field_mapping) sind in SQLite text, in PostgreSQL jsonb.
const schemaHCL = `
table "ledger__chart_of_accounts" {
  schema = schema.main
  column "id"   { type = text }
  column "name" { type = text }
  column "description" {
    type = text
    null = true
  }
  column "country" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
}

table "ledger__ledger" {
  schema = schema.main
  column "id"   { type = text }
  column "name" { type = text }
  column "is_leading" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.id] }
}

table "ledger__currency" {
  schema = schema.main
  column "code" { type = text }
  column "name" { type = text }
  column "decimals" {
    type    = bigint
    default = 2
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

table "ledger__exchange_rate" {
  schema = schema.main
  column "rate_type"     { type = text }
  column "from_currency" { type = text }
  column "to_currency"   { type = text }
  column "valid_from"    { type = date }
  column "rate"          { type = text }
  column "from_factor" {
    type    = bigint
    default = 1
  }
  column "to_factor" {
    type    = bigint
    default = 1
  }
  primary_key { columns = [column.rate_type, column.from_currency, column.to_currency, column.valid_from] }
  foreign_key "ledger__exchange_rate_from_fk" {
    columns     = [column.from_currency]
    ref_columns = [table.ledger__currency.column.code]
  }
  foreign_key "ledger__exchange_rate_to_fk" {
    columns     = [column.to_currency]
    ref_columns = [table.ledger__currency.column.code]
  }
}

# Belegart (analog T003): erlaubte Positionsarten, Referenz Pflicht
table "ledger__document_type" {
  schema = schema.main
  column "code"               { type = text }
  column "name"               { type = text }
  column "allowed_item_types" { type = text }
  column "reference_required" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.code] }
}

# Feldstatusgruppe (analog T004F/T004V): Status je Kontierungsfeld
table "ledger__field_status_group" {
  schema = schema.main
  column "id"   { type = text }
  column "name" { type = text }
  column "description" {
    type = text
    null = true
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.id] }
}

table "ledger__field_status" {
  schema = schema.main
  column "group_id"   { type = text }
  column "field_name" { type = text }
  column "status" {
    type    = text
    default = "OPTIONAL"
  }
  primary_key { columns = [column.group_id, column.field_name] }
  foreign_key "ledger__field_status_group_fk" {
    columns     = [column.group_id]
    ref_columns = [table.ledger__field_status_group.column.id]
  }
}

# Kontensperren je Periode (analog Kontointervalle in OB52): Ausnahmen zum
# Periodenstatus für Kontenbereiche. Bei Widerspruch gilt CLOSED.
table "ledger__period_account_lock" {
  schema = schema.main
  column "id"              { type = text }
  column "company_code_id" { type = text }
  column "ledger"          { type = text }
  column "fiscal_year"     { type = bigint }
  column "period_from"     { type = bigint }
  column "period_to"       { type = bigint }
  column "account_from"    { type = text }
  column "account_to"      { type = text }
  column "status"          { type = text }
  column "reason" {
    type = text
    null = true
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  column "changed_at" {
    type = text
    null = true
  }
  column "changed_by" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__period_account_lock_period" { columns = [column.company_code_id, column.ledger, column.fiscal_year] }
  foreign_key "ledger__period_account_lock_ledger_fk" {
    columns     = [column.ledger]
    ref_columns = [table.ledger__ledger.column.id]
  }
}

table "ledger__account_master" {
  schema = schema.main
  column "chart_of_accounts_id" { type = text }
  column "account_number"       { type = text }
  column "name"                 { type = text }
  column "description" {
    type = text
    null = true
  }
  column "account_type" { type = text }
  column "account_group" {
    type = text
    null = true
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.chart_of_accounts_id, column.account_number] }
  foreign_key "ledger__account_master_chart_fk" {
    columns     = [column.chart_of_accounts_id]
    ref_columns = [table.ledger__chart_of_accounts.column.id]
  }
}

table "ledger__account_company" {
  schema = schema.main
  column "id"                   { type = text }
  column "company_code_id"      { type = text }
  column "chart_of_accounts_id" { type = text }
  column "account_number"       { type = text }
  column "currency"             { type = text }
  column "reconciliation_type" {
    type    = text
    default = "NONE"
  }
  column "alternative_account_number" {
    type = text
    null = true
  }
  column "tax_category" {
    type    = text
    default = "NONE"
  }
  column "field_status_group" {
    type = text
    null = true
  }
  column "is_blocked" {
    type    = boolean
    default = false
  }
  primary_key { columns = [column.id] }
  index "ledger__account_company_uk" {
    unique  = true
    columns = [column.company_code_id, column.account_number]
  }
  foreign_key "ledger__account_company_master_fk" {
    columns     = [column.chart_of_accounts_id, column.account_number]
    ref_columns = [table.ledger__account_master.column.chart_of_accounts_id, table.ledger__account_master.column.account_number]
  }
  foreign_key "ledger__account_company_currency_fk" {
    columns     = [column.currency]
    ref_columns = [table.ledger__currency.column.code]
  }
  foreign_key "ledger__account_company_fsg_fk" {
    columns     = [column.field_status_group]
    ref_columns = [table.ledger__field_status_group.column.id]
  }
}

table "ledger__company_config" {
  schema = schema.main
  column "company_code_id"      { type = text }
  column "leading_ledger"       { type = text }
  column "chart_of_accounts_id" { type = text }
  column "currency"             { type = text }
  column "fiscal_year_variant" {
    type    = text
    default = "K4"
  }
  column "exchange_rate_type" {
    type    = text
    default = "M"
  }
  column "module_field_mapping" {
    type = text
    null = true
  }
  primary_key { columns = [column.company_code_id] }
  foreign_key "ledger__company_config_ledger_fk" {
    columns     = [column.leading_ledger]
    ref_columns = [table.ledger__ledger.column.id]
  }
  foreign_key "ledger__company_config_chart_fk" {
    columns     = [column.chart_of_accounts_id]
    ref_columns = [table.ledger__chart_of_accounts.column.id]
  }
  foreign_key "ledger__company_config_currency_fk" {
    columns     = [column.currency]
    ref_columns = [table.ledger__currency.column.code]
  }
}

table "ledger__fiscal_period_status" {
  schema = schema.main
  column "company_code_id" { type = text }
  column "ledger"          { type = text }
  column "fiscal_year"     { type = bigint }
  column "posting_period"  { type = bigint }
  column "status" {
    type    = text
    default = "CLOSED"
  }
  column "changed_at" {
    type = text
    null = true
  }
  column "changed_by" {
    type = text
    null = true
  }
  primary_key { columns = [column.company_code_id, column.ledger, column.fiscal_year, column.posting_period] }
  foreign_key "ledger__fiscal_period_status_ledger_fk" {
    columns     = [column.ledger]
    ref_columns = [table.ledger__ledger.column.id]
  }
}

table "ledger__number_range" {
  schema = schema.main
  column "company_code_id" { type = text }
  column "fiscal_year"     { type = bigint }
  column "last_number"     { type = bigint }
  primary_key { columns = [column.company_code_id, column.fiscal_year] }
}

table "ledger__journal_entry_header" {
  schema = schema.main
  column "id"              { type = text }
  column "document_number" { type = text }
  column "company_code_id" { type = text }
  column "fiscal_year"     { type = bigint }
  column "posting_period"  { type = bigint }
  column "document_type" {
    type    = text
    default = "SA"
  }
  column "document_date"  { type = date }
  column "posting_date"   { type = date }
  column "currency"       { type = text }
  column "local_currency" { type = text }
  column "exchange_rate"  { type = text }
  column "reference" {
    type = text
    null = true
  }
  column "header_text" {
    type = text
    null = true
  }
  column "source_module" { type = text }
  column "source_reference" {
    type = text
    null = true
  }
  column "reversal_flag" {
    type    = boolean
    default = false
  }
  column "reversed_document_id" {
    type = text
    null = true
  }
  column "reversal_document_id" {
    type = text
    null = true
  }
  # Herkunft aus der Vorerfassung (manuelle Buchung)
  column "draft_id" {
    type = text
    null = true
  }
  column "created_by" {
    type = text
    null = true
  }
  column "created_at" { type = text }
  primary_key { columns = [column.id] }
  index "ledger__journal_entry_header_docno_uk" {
    unique  = true
    columns = [column.company_code_id, column.fiscal_year, column.document_number]
  }
  index "ledger__journal_entry_header_source_uk" {
    unique  = true
    columns = [column.company_code_id, column.source_module, column.source_reference]
  }
  index "ledger__journal_entry_header_date" { columns = [column.company_code_id, column.posting_date] }
  # Rekursive Fremdschlüssel: Storno und stornierter Beleg verweisen aufeinander.
  foreign_key "ledger__journal_entry_header_reversed_fk" {
    columns     = [column.reversed_document_id]
    ref_columns = [table.ledger__journal_entry_header.column.id]
  }
  foreign_key "ledger__journal_entry_header_reversal_fk" {
    columns     = [column.reversal_document_id]
    ref_columns = [table.ledger__journal_entry_header.column.id]
  }
  foreign_key "ledger__journal_entry_header_doctype_fk" {
    columns     = [column.document_type]
    ref_columns = [table.ledger__document_type.column.code]
  }
  foreign_key "ledger__journal_entry_header_draft_fk" {
    columns     = [column.draft_id]
    ref_columns = [table.ledger__draft_header.column.id]
  }
}

# Vorerfassung (analog SAP VBKPF/VBSEG): Arbeitstabellen der manuellen
# Buchung. Änderbar, solange status = DRAFT. "Buchen" schreibt den Beleg und
# setzt posted_document_id – ab dann ist die Vorerfassung gesperrt.
table "ledger__draft_header" {
  schema = schema.main
  column "id"              { type = text }
  column "company_code_id" { type = text }
  column "document_type" {
    type    = text
    default = "SA"
  }
  column "posting_date" { type = date }
  column "document_date" {
    type = date
    null = true
  }
  column "currency" { type = text }
  column "header_text" {
    type = text
    null = true
  }
  column "reference" {
    type = text
    null = true
  }
  column "status" {
    type    = text
    default = "DRAFT"
  }
  column "posted_document_id" {
    type = text
    null = true
  }
  column "changed_by" {
    type = text
    null = true
  }
  column "changed_at" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__draft_header_company" { columns = [column.company_code_id, column.status] }
  foreign_key "ledger__draft_header_doctype_fk" {
    columns     = [column.document_type]
    ref_columns = [table.ledger__document_type.column.code]
  }
  foreign_key "ledger__draft_header_posted_fk" {
    columns     = [column.posted_document_id]
    ref_columns = [table.ledger__journal_entry_header.column.id]
  }
}

table "ledger__draft_item" {
  schema = schema.main
  column "id"               { type = text }
  column "draft_id"         { type = text }
  column "line_item_number" { type = bigint }
  column "account_number"   { type = text }
  column "shkzg"            { type = text }
  column "item_type" {
    type    = text
    default = "GL"
  }
  column "amount"           { type = text }
  column "item_text" {
    type = text
    null = true
  }
  column "cost_center" {
    type = text
    null = true
  }
  column "profit_center" {
    type = text
    null = true
  }
  column "segment" {
    type = text
    null = true
  }
  column "sd_sales_order_id" {
    type = text
    null = true
  }
  column "sd_sales_org" {
    type = text
    null = true
  }
  column "sd_customer_id" {
    type = text
    null = true
  }
  column "rent_object_id" {
    type = text
    null = true
  }
  column "rent_contract_id" {
    type = text
    null = true
  }
  column "purchase_order_id" {
    type = text
    null = true
  }
  column "supplier_id" {
    type = text
    null = true
  }
  column "dimension_custom_1" {
    type = text
    null = true
  }
  column "dimension_custom_2" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__draft_item_line" { columns = [column.draft_id, column.line_item_number] }
  foreign_key "ledger__draft_item_draft_fk" {
    columns     = [column.draft_id]
    ref_columns = [table.ledger__draft_header.column.id]
  }
}

table "ledger__journal_entry_item" {
  schema = schema.main
  column "id"                   { type = text }
  column "header_id"            { type = text }
  column "line_item_number"     { type = bigint }
  column "ledger"               { type = text }
  column "company_code_id"      { type = text }
  column "fiscal_year"          { type = bigint }
  column "posting_period"       { type = bigint }
  column "posting_date"         { type = date }
  column "chart_of_accounts_id" { type = text }
  column "account_number"       { type = text }
  column "shkzg"                { type = text }
  column "item_type" {
    type    = text
    default = "GL"
  }
  column "amount_document_curr" { type = bigint }
  column "amount_local_curr"    { type = bigint }
  column "currency"             { type = text }
  column "local_currency"       { type = text }
  column "item_text" {
    type = text
    null = true
  }
  column "cost_center" {
    type = text
    null = true
  }
  column "profit_center" {
    type = text
    null = true
  }
  column "segment" {
    type = text
    null = true
  }
  column "sd_sales_order_id" {
    type = text
    null = true
  }
  column "sd_sales_org" {
    type = text
    null = true
  }
  column "sd_customer_id" {
    type = text
    null = true
  }
  column "rent_object_id" {
    type = text
    null = true
  }
  column "rent_contract_id" {
    type = text
    null = true
  }
  column "purchase_order_id" {
    type = text
    null = true
  }
  column "supplier_id" {
    type = text
    null = true
  }
  column "dimension_custom_1" {
    type = text
    null = true
  }
  column "dimension_custom_2" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__journal_entry_item_line_uk" {
    unique  = true
    columns = [column.header_id, column.ledger, column.line_item_number]
  }
  index "ledger__journal_entry_item_account" { columns = [column.company_code_id, column.ledger, column.fiscal_year, column.account_number] }
  index "ledger__journal_entry_item_period"  { columns = [column.company_code_id, column.ledger, column.fiscal_year, column.posting_period] }
  index "ledger__journal_entry_item_cost"    { columns = [column.cost_center] }
  index "ledger__journal_entry_item_profit"  { columns = [column.profit_center] }
  index "ledger__journal_entry_item_rentobj" { columns = [column.rent_object_id] }
  index "ledger__journal_entry_item_rentctr" { columns = [column.rent_contract_id] }
  index "ledger__journal_entry_item_cust"    { columns = [column.sd_customer_id] }
  index "ledger__journal_entry_item_supp"    { columns = [column.supplier_id] }
  foreign_key "ledger__journal_entry_item_header_fk" {
    columns     = [column.header_id]
    ref_columns = [table.ledger__journal_entry_header.column.id]
  }
  foreign_key "ledger__journal_entry_item_ledger_fk" {
    columns     = [column.ledger]
    ref_columns = [table.ledger__ledger.column.id]
  }
  foreign_key "ledger__journal_entry_item_account_fk" {
    columns     = [column.chart_of_accounts_id, column.account_number]
    ref_columns = [table.ledger__account_master.column.chart_of_accounts_id, table.ledger__account_master.column.account_number]
  }
}
`

// seeds: Währungen (ISO 4217 mit Nachkommastellen), Ledger und die Köpfe der
// mitgelieferten Kontenrahmen. Die Konten selbst lädt der Konsolenbefehl
// ledger:load-coa.
var seeds = append(append([]sdk.SchemaSeed{documentTypeSeeds}, fieldStatusSeeds()...), baseSeeds...)

var baseSeeds = []sdk.SchemaSeed{
	{Table: "ledger__currency", Rows: []map[string]any{
		currency("EUR", "Euro", 2), currency("CHF", "Schweizer Franken", 2), currency("USD", "US-Dollar", 2),
		currency("GBP", "Pfund Sterling", 2), currency("JPY", "Yen", 0),
	}},
	{Table: "ledger__ledger", Rows: []map[string]any{
		{"id": "0L", "name": "Führendes Ledger (HGB)", "is_leading": true, "is_active": true},
		{"id": "2L", "name": "Paralleles Ledger (IFRS)", "is_leading": false, "is_active": true},
	}},
	{Table: "ledger__chart_of_accounts", Rows: []map[string]any{
		{"id": "SKR04", "name": "DATEV SKR 04", "description": "Abschlussgliederungsprinzip, Kontenklassen 0–9", "country": "DE"},
		{"id": "SKR25", "name": "Kontenrahmen Wohnungswirtschaft (interne Kennung SKR25)", "description": "In Anlehnung an den GdW-Kontenrahmen; keine offizielle DATEV-Bezeichnung", "country": "DE"},
	}},
}

func currency(code, name string, decimals int) map[string]any {
	return map[string]any{"code": code, "name": name, "decimals": decimals, "is_active": true}
}
