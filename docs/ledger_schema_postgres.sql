-- =====================================================================================
-- CoreMesh ERP – Hauptbuch (Plugin ledger), PostgreSQL-DDL
-- =====================================================================================
-- Maßgeblich für die Migration ist das Atlas-Schema in
-- cmd/plugins/ledger/internal/ledger/schema.go (DBSchema erzeugt daraus die Migration,
-- ohne DROPs). Dieses DDL beschreibt dasselbe Schema für PostgreSQL; der Test
-- TestSchemaDDLInSync prüft, dass jede Tabelle und Spalte hier vorkommt.
--
-- Abweichungen gegenüber SQLite: jsonb statt text (module_field_mapping),
-- timestamptz statt text (Zeitstempel) und zusätzliche CHECK-Constraints.
--
-- Tabellennamen: Präfix ledger__ (Pflicht-Präfix des Plugins). Im DBSchema-Modus
-- "isolation: schema" liegen die Tabellen stattdessen im Schema mod_ledger.
--
-- Beträge: bigint in der kleinsten Einheit der Währung (ledger__currency.decimals),
-- vorzeichenbehaftet wie ACDOCA (Soll +, Haben −); Summe eines Belegs je Ledger = 0.
-- Buchungskreise (company_code_id) gehören dem Core-Plugin iam; sie werden über
-- dessen Actions geprüft, nicht per Fremdschlüssel.
-- =====================================================================================

-- Kontenplan (Kopf), z. B. SKR04, SKR25
CREATE TABLE ledger__chart_of_accounts (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    description text,
    country     text
);

-- Ledger: führend (0L) und parallel (2L, IFRS)
CREATE TABLE ledger__ledger (
    id         text PRIMARY KEY CHECK (id ~ '^[0-9A-Z]{2}$'),
    name       text NOT NULL,
    is_leading boolean NOT NULL DEFAULT false,
    is_active  boolean NOT NULL DEFAULT true
);

-- Währungen (ISO 4217) mit Nachkommastellen für die kleinste Einheit
CREATE TABLE ledger__currency (
    code      text PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
    name      text NOT NULL,
    decimals  bigint NOT NULL DEFAULT 2 CHECK (decimals BETWEEN 0 AND 4),
    is_active boolean NOT NULL DEFAULT true
);

-- Tageskurse (analog TCURR): from_factor × from = rate × to_factor × to, gültig ab valid_from
CREATE TABLE ledger__exchange_rate (
    rate_type     text NOT NULL CHECK (rate_type IN ('M', 'B', 'G')),
    from_currency text NOT NULL REFERENCES ledger__currency (code),
    to_currency   text NOT NULL REFERENCES ledger__currency (code),
    valid_from    date NOT NULL,
    rate          numeric(22, 10) NOT NULL CHECK (rate > 0),
    from_factor   bigint NOT NULL DEFAULT 1 CHECK (from_factor >= 1),
    to_factor     bigint NOT NULL DEFAULT 1 CHECK (to_factor >= 1),
    PRIMARY KEY (rate_type, from_currency, to_currency, valid_from),
    CHECK (from_currency <> to_currency)
);

-- A. Zentraler Kontenplan (analog SKA1)
CREATE TABLE ledger__account_master (
    chart_of_accounts_id text NOT NULL REFERENCES ledger__chart_of_accounts (id),
    account_number       text NOT NULL,
    name                 text NOT NULL,
    description          text,
    account_type         text NOT NULL
        CHECK (account_type IN ('BALANCE_SHEET', 'PRIMARY_COST', 'SECONDARY_COST', 'REVENUE', 'NON_OPERATING')),
    account_group        text,
    is_active            boolean NOT NULL DEFAULT true,
    PRIMARY KEY (chart_of_accounts_id, account_number)
);

-- B. Sachkonto im Buchungskreis (analog SKB1)
CREATE TABLE ledger__account_company (
    id                         text PRIMARY KEY,
    company_code_id            text NOT NULL,
    chart_of_accounts_id       text NOT NULL,
    account_number             text NOT NULL,
    currency                   text NOT NULL REFERENCES ledger__currency (code),
    reconciliation_type        text NOT NULL DEFAULT 'NONE' CHECK (reconciliation_type IN ('NONE', 'CUSTOMER', 'SUPPLIER', 'ASSET')),
    alternative_account_number text,
    tax_category               text NOT NULL DEFAULT 'NONE'
        CHECK (tax_category IN ('NONE', 'ANY', 'INPUT_ONLY', 'OUTPUT_ONLY', 'INPUT_TAX_ACCOUNT', 'OUTPUT_TAX_ACCOUNT')),
    is_blocked                 boolean NOT NULL DEFAULT false,
    FOREIGN KEY (chart_of_accounts_id, account_number) REFERENCES ledger__account_master (chart_of_accounts_id, account_number)
);
CREATE UNIQUE INDEX ledger__account_company_uk ON ledger__account_company (company_code_id, account_number);

-- C. Steuerung je Buchungskreis inkl. Modul-Mapping
--    module_field_mapping: {"RENT": {"object": "rent_object_id", "contract": "rent_contract_id"}, "SD": {...}, ...}
CREATE TABLE ledger__company_config (
    company_code_id      text PRIMARY KEY,
    leading_ledger       text NOT NULL REFERENCES ledger__ledger (id),
    chart_of_accounts_id text NOT NULL REFERENCES ledger__chart_of_accounts (id),
    currency             text NOT NULL REFERENCES ledger__currency (code),
    fiscal_year_variant  text NOT NULL DEFAULT 'K4',
    exchange_rate_type   text NOT NULL DEFAULT 'M' CHECK (exchange_rate_type IN ('M', 'B', 'G')),
    module_field_mapping jsonb
);

-- F. Periodensperre (analog OB52); ohne Eintrag ist eine Periode gesperrt
CREATE TABLE ledger__fiscal_period_status (
    company_code_id text NOT NULL,
    ledger          text NOT NULL REFERENCES ledger__ledger (id),
    fiscal_year     bigint NOT NULL CHECK (fiscal_year BETWEEN 1900 AND 2999),
    posting_period  bigint NOT NULL CHECK (posting_period BETWEEN 1 AND 16),
    status          text NOT NULL DEFAULT 'CLOSED' CHECK (status IN ('OPEN', 'CLOSED')),
    changed_at      timestamptz,
    changed_by      text,
    PRIMARY KEY (company_code_id, ledger, fiscal_year, posting_period)
);

-- Belegnummernkreis je Buchungskreis und Geschäftsjahr (analog NRIV)
CREATE TABLE ledger__number_range (
    company_code_id text NOT NULL,
    fiscal_year     bigint NOT NULL,
    last_number     bigint NOT NULL,
    PRIMARY KEY (company_code_id, fiscal_year)
);

-- D. Belegkopf (analog BKPF)
CREATE TABLE ledger__journal_entry_header (
    id                   text PRIMARY KEY,
    document_number      text NOT NULL,
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL CHECK (posting_period BETWEEN 1 AND 16),
    document_type        text NOT NULL DEFAULT 'SA',
    document_date        date NOT NULL,
    posting_date         date NOT NULL,
    currency             text NOT NULL,
    local_currency       text NOT NULL,
    exchange_rate        numeric(22, 10) NOT NULL,
    reference            text,
    header_text          text,
    source_module        text NOT NULL,
    source_reference     text,
    reversal_flag        boolean NOT NULL DEFAULT false,
    reversed_document_id text,  -- Storno: Verweis auf den stornierten Beleg (rekursiv)
    reversal_document_id text,  -- Original: Verweis auf den Stornobeleg (rekursiv)
    draft_id             text,  -- Herkunft aus der Vorerfassung
    created_by           text,
    created_at           timestamptz NOT NULL
);
CREATE UNIQUE INDEX ledger__journal_entry_header_docno_uk ON ledger__journal_entry_header (company_code_id, fiscal_year, document_number);
CREATE UNIQUE INDEX ledger__journal_entry_header_source_uk ON ledger__journal_entry_header (company_code_id, source_module, source_reference);
CREATE INDEX ledger__journal_entry_header_date ON ledger__journal_entry_header (company_code_id, posting_date);

-- Vorerfassung (analog VBKPF/VBSEG): änderbar bis zum Buchen, danach gesperrt
CREATE TABLE ledger__draft_header (
    id                 text PRIMARY KEY,
    company_code_id    text NOT NULL,
    document_type      text NOT NULL DEFAULT 'SA',
    posting_date       date NOT NULL,
    document_date      date,
    currency           text NOT NULL,
    header_text        text,
    reference          text,
    status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'POSTED', 'DISCARDED')),
    posted_document_id text,  -- gebuchter Beleg; gesetzt = Vorerfassung gesperrt
    changed_by         text,
    changed_at         timestamptz,
    CHECK ((status = 'POSTED') = (posted_document_id IS NOT NULL))
);
CREATE INDEX ledger__draft_header_company ON ledger__draft_header (company_code_id, status);

CREATE TABLE ledger__draft_item (
    id                 text PRIMARY KEY,
    draft_id           text NOT NULL REFERENCES ledger__draft_header (id),
    line_item_number   bigint NOT NULL,
    account_number     text NOT NULL,
    shkzg              text NOT NULL CHECK (shkzg IN ('S', 'H')),
    amount             numeric(23, 4) NOT NULL CHECK (amount > 0),
    item_text          text,
    cost_center        text,
    profit_center      text,
    segment            text,
    sd_sales_order_id  text,
    sd_sales_org       text,
    sd_customer_id     text,
    rent_object_id     text,
    rent_contract_id   text,
    purchase_order_id  text,
    supplier_id        text,
    dimension_custom_1 text,
    dimension_custom_2 text
);
CREATE INDEX ledger__draft_item_line ON ledger__draft_item (draft_id, line_item_number);

-- E. Universal Journal / Einzelposten (analog ACDOCA)
CREATE TABLE ledger__journal_entry_item (
    id                   text PRIMARY KEY,
    header_id            text NOT NULL REFERENCES ledger__journal_entry_header (id),
    line_item_number     bigint NOT NULL,
    ledger               text NOT NULL REFERENCES ledger__ledger (id),
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL,
    posting_date         date NOT NULL,
    chart_of_accounts_id text NOT NULL,
    account_number       text NOT NULL,
    shkzg                text NOT NULL CHECK (shkzg IN ('S', 'H')),
    amount_document_curr bigint NOT NULL,  -- WSL, kleinste Einheit der Belegwährung
    amount_local_curr    bigint NOT NULL,  -- HSL, kleinste Einheit der Hauswährung
    currency             text NOT NULL,
    local_currency       text NOT NULL,
    item_text            text,
    -- Allgemeine Kontierungen
    cost_center          text,
    profit_center        text,
    segment              text,
    -- Modul 1: Vertrieb (SD)
    sd_sales_order_id    text,
    sd_sales_org         text,
    sd_customer_id       text,
    -- Modul 2: Vermietung (RENT)
    rent_object_id       text,
    rent_contract_id     text,
    -- Modul 3: Einkauf (PROCUREMENT)
    purchase_order_id    text,
    supplier_id          text,
    -- Freie Dimensionen
    dimension_custom_1   text,
    dimension_custom_2   text,
    FOREIGN KEY (chart_of_accounts_id, account_number) REFERENCES ledger__account_master (chart_of_accounts_id, account_number),
    CHECK ((shkzg = 'S' AND amount_document_curr > 0) OR (shkzg = 'H' AND amount_document_curr < 0))
);
CREATE UNIQUE INDEX ledger__journal_entry_item_line_uk ON ledger__journal_entry_item (header_id, ledger, line_item_number);
CREATE INDEX ledger__journal_entry_item_account ON ledger__journal_entry_item (company_code_id, ledger, fiscal_year, account_number);
CREATE INDEX ledger__journal_entry_item_period ON ledger__journal_entry_item (company_code_id, ledger, fiscal_year, posting_period);
CREATE INDEX ledger__journal_entry_item_cost ON ledger__journal_entry_item (cost_center);
CREATE INDEX ledger__journal_entry_item_profit ON ledger__journal_entry_item (profit_center);
CREATE INDEX ledger__journal_entry_item_rentobj ON ledger__journal_entry_item (rent_object_id);
CREATE INDEX ledger__journal_entry_item_rentctr ON ledger__journal_entry_item (rent_contract_id);
CREATE INDEX ledger__journal_entry_item_cust ON ledger__journal_entry_item (sd_customer_id);
CREATE INDEX ledger__journal_entry_item_supp ON ledger__journal_entry_item (supplier_id);

-- Rekursive bzw. zyklische Fremdschlüssel (nach dem Anlegen beider Tabellen):
--   Storno ↔ Original (Selbstbezug des Belegkopfs), Vorerfassung ↔ gebuchter Beleg.
ALTER TABLE ledger__journal_entry_header
    ADD CONSTRAINT ledger__journal_entry_header_reversed_fk FOREIGN KEY (reversed_document_id) REFERENCES ledger__journal_entry_header (id),
    ADD CONSTRAINT ledger__journal_entry_header_reversal_fk FOREIGN KEY (reversal_document_id) REFERENCES ledger__journal_entry_header (id),
    ADD CONSTRAINT ledger__journal_entry_header_draft_fk FOREIGN KEY (draft_id) REFERENCES ledger__draft_header (id);
ALTER TABLE ledger__draft_header
    ADD CONSTRAINT ledger__draft_header_posted_fk FOREIGN KEY (posted_document_id) REFERENCES ledger__journal_entry_header (id);
