package accounting

import "github.com/camel/coremesh/pkg/sdk"

// Tabellen der Finanzbuchhaltung (Präfix ledger__, von DBSchema vorgegeben).
//
// Beträge stehen als ganze Zahl in der kleinsten Einheit (Rappen/Cent) in
// amount_minor; die Seite (D = Soll, C = Haben) in side. So sind Summen exakt
// und in SQL rechenbar. Belege und Positionen haben keine Zeitscheibe: Sie sind
// unveränderlich, Korrekturen sind Stornobelege.
const schemaHCL = `
table "ledger__accounts" {
  schema = schema.main
  column "code"         { type = text }
  column "name"         { type = text }
  column "account_type" { type = text }
  column "status" {
    type    = text
    default = "ACTIVE"
  }
  column "description" {
    type = text
    null = true
  }
  primary_key { columns = [column.code] }
}

table "ledger__journal_entries" {
  schema = schema.main
  column "id"            { type = text }
  column "document_no"   { type = text }
  column "company_code"  { type = text }
  column "posting_date"  { type = date }
  column "document_date" { type = date }
  column "reference" {
    type = text
    null = true
  }
  column "text"     { type = text }
  column "currency" { type = text }
  # Storno: reversal_of = stornierter Beleg, reversed_by = Stornobeleg
  column "reversal_of" {
    type = text
    null = true
  }
  column "reversed_by" {
    type = text
    null = true
  }
  column "posted_at" { type = text }
  column "posted_by" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__journal_entries_no" {
    unique  = true
    columns = [column.company_code, column.document_no]
  }
  index "ledger__journal_entries_date" { columns = [column.company_code, column.posting_date] }
}

table "ledger__journal_lines" {
  schema = schema.main
  column "id"           { type = text }
  column "entry_id"     { type = text }
  column "line_no"      { type = bigint }
  column "account_code" { type = text }
  column "side"         { type = text }
  column "amount_minor" { type = bigint }
  column "text" {
    type = text
    null = true
  }
  primary_key { columns = [column.id] }
  index "ledger__journal_lines_entry"   { columns = [column.entry_id, column.line_no] }
  index "ledger__journal_lines_account" { columns = [column.account_code] }
  foreign_key "ledger__journal_lines_entry_fk" {
    columns     = [column.entry_id]
    ref_columns = [table.ledger__journal_entries.column.id]
  }
  foreign_key "ledger__journal_lines_account_fk" {
    columns     = [column.account_code]
    ref_columns = [table.ledger__accounts.column.code]
  }
}
`

// seeds: Grundkontenplan nach dem Schweizer KMU-Kontenrahmen, vereinfacht für
// Liegenschaften, Mietverwaltung und private Buchhaltung. Weitere Konten legt
// der Benutzer an; Seeds überschreiben nichts.
var seeds = []sdk.SchemaSeed{{Table: "ledger__accounts", Rows: []map[string]any{
	account("1000", "Kasse", typeAsset),
	account("1020", "Bank", typeAsset),
	account("1100", "Forderungen gegenüber Mietern", typeAsset),
	account("1300", "Aktive Rechnungsabgrenzung", typeAsset),
	account("1600", "Liegenschaften", typeAsset),
	account("2000", "Verbindlichkeiten aus Lieferungen und Leistungen", typeLiability),
	account("2030", "Mieterkautionen", typeLiability),
	account("2300", "Passive Rechnungsabgrenzung", typeLiability),
	account("2400", "Hypotheken", typeLiability),
	account("2800", "Eigenkapital", typeEquity),
	account("3400", "Mietertrag", typeIncome),
	account("3410", "Nebenkosten-Akontozahlungen", typeIncome),
	account("3800", "Übriger Ertrag", typeIncome),
	account("6100", "Unterhalt und Reparaturen", typeExpense),
	account("6300", "Versicherungen", typeExpense),
	account("6400", "Energie und Nebenkosten", typeExpense),
	account("6500", "Verwaltungsaufwand", typeExpense),
	account("6800", "Abschreibungen", typeExpense),
	account("6900", "Hypothekarzinsen und Finanzaufwand", typeExpense),
	account("8900", "Steuern", typeExpense),
}}}

func account(code, name, typ string) map[string]any {
	return map[string]any{"code": code, "name": name, "account_type": typ, "status": statusActive}
}
