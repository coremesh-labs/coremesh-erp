package bank

// opt: Spalte darf leer sein.
const opt = `
    null = true`

// schemaHCL: Soll-Schema (Atlas-HCL) der Bank. Tabellen mit dem Pflicht-Präfix bank__.
const schemaHCL = `
# Importformat (CSV) je Buchungskreis: Trennzeichen, Kopfzeile, Datums- und Zahlenformat, Reihenfolge der Datei
table "bank__format" {
  schema = schema.main
  column "company_code" { type = text }
  column "code" { type = text }
  column "name" { type = text }
  column "delimiter" {
    type    = text
    default = ";"
  }
  # Zeilen vor der Kopfzeile
  column "skip_lines" {
    type    = bigint
    default = 0
  }
  # Datei ohne Kopfzeile: Spalten als Nummer (1, 2, …)
  column "no_header" {
    type    = boolean
    default = false
  }
  column "date_format" {
    type    = text
    default = "DD.MM.YYYY"
  }
  column "decimal_separator" {
    type    = text
    default = ","
  }
  # Datei beginnt mit dem neuesten Umsatz
  column "newest_first" {
    type    = boolean
    default = false
  }
  # Werte der Spalte Soll/Haben, die eine Belastung bedeuten (z. B. S,Soll,D)
  column "debit_values" {
    type = text` + opt + `
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.code] }
}

# Spaltenzuordnung: Spalte der Datei → Feld des Umsatzes, mit Aufbereitung
table "bank__format_column" {
  schema = schema.main
  column "company_code" { type = text }
  column "format" { type = text }
  column "line_no" { type = bigint }
  # Feld des Umsatzes
  column "target" { type = text }
  # Spaltenname (oder Nummer ab 1)
  column "source" { type = text }
  # NONE | TRIM | UPPER | NEGATE | EXTRACT | IBAN
  column "transform" {
    type    = text
    default = "NONE"
  }
  # EXTRACT: regulärer Ausdruck, erste Gruppe
  column "pattern" {
    type = text` + opt + `
  }
  # nur, wenn das Feld noch leer ist (z. B. Empfänger, wenn Sender leer)
  column "fallback" {
    type    = boolean
    default = false
  }
  primary_key { columns = [column.company_code, column.format, column.line_no] }
  foreign_key "bank__format_column_format_fk" {
    columns     = [column.company_code, column.format]
    ref_columns = [table.bank__format.column.company_code, table.bank__format.column.code]
  }
}

# Bankkonto: Kopfdaten, Sachkonto, Belegarten, Reihenfolge der Buchung
table "bank__account" {
  schema = schema.main
  column "company_code" { type = text }
  column "account_id" { type = text }
  column "designation" { type = text }
  column "iban" { type = text }
  column "bic" {
    type = text` + opt + `
  }
  column "bank_name" {
    type = text` + opt + `
  }
  column "currency" { type = text }
  # Sachkonto Bank im Hauptbuch
  column "gl_account" { type = text }
  # Vertrag Bankkonto (Vertragsmodul, optional)
  column "contract_id" {
    type = text` + opt + `
  }
  # Importformat (Vorschlag)
  column "format" {
    type = text` + opt + `
  }
  column "document_type_in" {
    type    = text
    default = "DZ"
  }
  column "document_type_out" {
    type    = text
    default = "KZ"
  }
  column "document_type_gl" {
    type    = text
    default = "SA"
  }
  # Nicht zugeordnete Umsätze überspringen statt die Buchung anzuhalten
  column "post_out_of_order" {
    type    = boolean
    default = false
  }
  column "is_active" {
    type    = boolean
    default = true
  }
  primary_key { columns = [column.company_code, column.account_id] }
  index "bank__account_iban" { columns = [column.company_code, column.iban] }
}

# Einlesen einer Datei
table "bank__import" {
  schema = schema.main
  column "company_code" { type = text }
  column "import_id" { type = text }
  column "account_id" { type = text }
  column "format" { type = text }
  column "file_name" {
    type = text` + opt + `
  }
  column "imported_at" { type = text }
  column "imported_by" {
    type = text` + opt + `
  }
  column "rows_read" { type = bigint }
  column "rows_new" { type = bigint }
  column "rows_duplicate" { type = bigint }
  column "date_from" {
    type = date` + opt + `
  }
  column "date_to" {
    type = date` + opt + `
  }
  column "message" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.import_id] }
}

# Umsatz eines Bankkontos: Daten aus der Datei, Zuordnung, Buchung (Rechenweg nachvollziehbar)
table "bank__transaction" {
  schema = schema.main
  column "company_code" { type = text }
  column "account_id" { type = text }
  # Reihenfolge der Zahlungen (Buchungstag, Datei)
  column "txn_no" { type = bigint }
  column "booking_date" { type = date }
  column "value_date" {
    type = date` + opt + `
  }
  # kleinste Einheit, Eingang +, Ausgang −
  column "amount" { type = bigint }
  column "currency" { type = text }
  column "counterparty_name" {
    type = text` + opt + `
  }
  column "counterparty_iban" {
    type = text` + opt + `
  }
  column "counterparty_bic" {
    type = text` + opt + `
  }
  column "purpose" {
    type = text` + opt + `
  }
  column "booking_text" {
    type = text` + opt + `
  }
  column "transaction_type" {
    type = text` + opt + `
  }
  column "end_to_end_ref" {
    type = text` + opt + `
  }
  column "mandate_ref" {
    type = text` + opt + `
  }
  column "creditor_id" {
    type = text` + opt + `
  }
  column "customer_ref" {
    type = text` + opt + `
  }
  column "import_id" {
    type = text` + opt + `
  }
  # Doppelte erkennen
  column "fingerprint" { type = text }
  # OPEN | PROPOSED | MATCHED | POSTED | IGNORED
  column "status" {
    type    = text
    default = "OPEN"
  }
  # CONTRACT | PARTNER | INVOICE | ACCOUNT
  column "target_type" {
    type = text` + opt + `
  }
  column "contract_id" {
    type = text` + opt + `
  }
  column "partner_id" {
    type = text` + opt + `
  }
  column "role_code" {
    type = text` + opt + `
  }
  column "invoice_id" {
    type = text` + opt + `
  }
  column "gl_account" {
    type = text` + opt + `
  }
  # RULE | REFERENCE | INVOICE | IBAN | NAME | MANUAL
  column "match_source" {
    type = text` + opt + `
  }
  column "match_note" {
    type = text` + opt + `
  }
  column "rule_no" {
    type = bigint` + opt + `
  }
  column "draft_id" {
    type = text` + opt + `
  }
  column "document_number" {
    type = text` + opt + `
  }
  # Wie gebucht wurde
  column "posting_trace" {
    type = text` + opt + `
  }
  column "posted_at" {
    type = text` + opt + `
  }
  column "changed_at" {
    type = text` + opt + `
  }
  column "changed_by" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.account_id, column.txn_no] }
  index "bank__transaction_fingerprint" { columns = [column.company_code, column.account_id, column.fingerprint] }
  index "bank__transaction_status" { columns = [column.company_code, column.account_id, column.status] }
  foreign_key "bank__transaction_account_fk" {
    columns     = [column.company_code, column.account_id]
    ref_columns = [table.bank__account.column.company_code, table.bank__account.column.account_id]
  }
}

# Zuordnungsregel: gelernt aus einer Zuordnung von Hand, wirkt erst nach Bestätigung
table "bank__rule" {
  schema = schema.main
  column "company_code" { type = text }
  column "rule_no" { type = bigint }
  # leer = alle Bankkonten
  column "account_id" {
    type = text` + opt + `
  }
  # PROPOSED | CONFIRMED | REJECTED
  column "status" {
    type    = text
    default = "PROPOSED"
  }
  # IN | OUT | ANY
  column "direction" {
    type    = text
    default = "ANY"
  }
  # Name der Gegenseite (normalisiert, genau)
  column "counterparty_name" {
    type = text` + opt + `
  }
  column "counterparty_iban" {
    type = text` + opt + `
  }
  # Verwendungszweck enthält (ohne Groß-/Kleinschreibung)
  column "purpose_contains" {
    type = text` + opt + `
  }
  # genauer Betrag (optional)
  column "amount" {
    type = bigint` + opt + `
  }
  column "target_type" { type = text }
  column "contract_id" {
    type = text` + opt + `
  }
  column "partner_id" {
    type = text` + opt + `
  }
  column "role_code" {
    type = text` + opt + `
  }
  column "gl_account" {
    type = text` + opt + `
  }
  # Umsatz, aus dem die Regel gelernt wurde
  column "learned_from" {
    type = text` + opt + `
  }
  column "hits" {
    type    = bigint
    default = 0
  }
  column "confirmed_at" {
    type = text` + opt + `
  }
  column "confirmed_by" {
    type = text` + opt + `
  }
  column "note" {
    type = text` + opt + `
  }
  primary_key { columns = [column.company_code, column.rule_no] }
}

`
