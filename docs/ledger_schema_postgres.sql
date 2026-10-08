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

-- Belegart (analog T003): erlaubte Positionsarten, Referenz Pflicht
CREATE TABLE ledger__document_type (
    code               text PRIMARY KEY CHECK (code ~ '^[0-9A-Z]{2}$'),
    name               text NOT NULL,
    allowed_item_types text NOT NULL,  -- z. B. 'CUSTOMER,GL,TAX'
    reference_required boolean NOT NULL DEFAULT false,
    is_active          boolean NOT NULL DEFAULT true
);

-- Feldstatusgruppe (analog T004F/T004V): Status je Kontierungsfeld
CREATE TABLE ledger__field_status_group (
    id          text PRIMARY KEY,
    name        text NOT NULL,
    description text,
    is_active   boolean NOT NULL DEFAULT true
);

CREATE TABLE ledger__field_status (
    group_id   text NOT NULL REFERENCES ledger__field_status_group (id),
    field_name text NOT NULL,
    status     text NOT NULL DEFAULT 'OPTIONAL' CHECK (status IN ('SUPPRESS', 'OPTIONAL', 'REQUIRED')),
    PRIMARY KEY (group_id, field_name)
);

-- Kontensperren je Periode (analog Kontointervalle in OB52): Ausnahmen zum
-- Periodenstatus für Kontenbereiche; bei Widerspruch gilt CLOSED
CREATE TABLE ledger__period_account_lock (
    id              text PRIMARY KEY,
    company_code_id text NOT NULL,
    ledger          text NOT NULL REFERENCES ledger__ledger (id),
    fiscal_year     bigint NOT NULL,
    period_from     bigint NOT NULL CHECK (period_from BETWEEN 1 AND 16),
    period_to       bigint NOT NULL CHECK (period_to BETWEEN 1 AND 16),
    account_from    text NOT NULL,
    account_to      text NOT NULL,
    status          text NOT NULL CHECK (status IN ('OPEN', 'CLOSED')),
    reason          text,
    is_active       boolean NOT NULL DEFAULT true,
    changed_at      timestamptz,
    changed_by      text,
    CHECK (period_from <= period_to AND account_from <= account_to)
);
CREATE INDEX ledger__period_account_lock_period ON ledger__period_account_lock (company_code_id, ledger, fiscal_year);

-- A. Zentraler Kontenplan (analog SKA1)
CREATE TABLE ledger__account_master (
    chart_of_accounts_id text NOT NULL REFERENCES ledger__chart_of_accounts (id),
    account_number       text NOT NULL,
    name                 text NOT NULL,
    description          text,
    account_type         text NOT NULL
        CHECK (account_type IN ('BALANCE_SHEET', 'PRIMARY_COST', 'SECONDARY_COST', 'REVENUE', 'NON_OPERATING')),
    account_group        text,
    account_kind         text NOT NULL DEFAULT 'S',   -- Kontoart (ledger__account_type), Nummer immer <Kontenplan>-<Nummer>
    is_active            boolean NOT NULL DEFAULT true,
    PRIMARY KEY (chart_of_accounts_id, account_number)
);

-- Kontoarten (analog KOART): A, D, K, M, S, V
CREATE TABLE ledger__account_type (
    code               text PRIMARY KEY,
    name               text NOT NULL,
    own_period_control boolean NOT NULL DEFAULT false,
    is_active          boolean NOT NULL DEFAULT true
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
    field_status_group         text REFERENCES ledger__field_status_group (id),
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

-- F. Buchungsperioden (seit 0.8.0)
-- Periodendefinition 01–16 ohne Geschäftsjahr
CREATE TABLE ledger__posting_period (
    period          bigint PRIMARY KEY CHECK (period BETWEEN 1 AND 16),
    name            text NOT NULL,
    is_special      boolean NOT NULL DEFAULT false,
    calendar_month  bigint NOT NULL CHECK (calendar_month BETWEEN 1 AND 12)
);

-- Periodendefinition je Buchungskreis (Vorlage: ledger__posting_period)
CREATE TABLE ledger__period_definition (
    company_code_id text NOT NULL,
    period          bigint NOT NULL CHECK (period BETWEEN 1 AND 16),
    name            text NOT NULL,
    is_special      boolean NOT NULL DEFAULT false,
    calendar_month  bigint NOT NULL CHECK (calendar_month BETWEEN 1 AND 12),
    PRIMARY KEY (company_code_id, period)
);

-- Offene Perioden: offen ist, was mit is_open = true hier steht (Verlauf bleibt)
CREATE TABLE ledger__open_period (
    id              text PRIMARY KEY,
    company_code_id text NOT NULL,
    ledger          text NOT NULL REFERENCES ledger__ledger (id),
    fiscal_year     bigint NOT NULL CHECK (fiscal_year BETWEEN 1900 AND 2999),
    account_kind    text NOT NULL DEFAULT '+',     -- Kontoart oder + (alle, Hauptschalter)
    posting_period  bigint NOT NULL REFERENCES ledger__posting_period (period),
    is_open         boolean NOT NULL DEFAULT true,
    opened_at       timestamptz,
    opened_by       text,
    closed_at       timestamptz,
    closed_by       text
);
CREATE INDEX ledger__open_period_key ON ledger__open_period (company_code_id, ledger, fiscal_year, posting_period, is_open);

-- bis 0.7.0: Periodenstatus je Jahr (nur noch Quelle der einmaligen Übernahme)
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
-- bis 0.12.0: Belegkopf mit GUID; ab 0.13.0 leer (übernommen nach ledger__journal_header)
CREATE TABLE ledger__journal_entry_header (
    id                   text PRIMARY KEY,
    document_number      text NOT NULL,
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL CHECK (posting_period BETWEEN 1 AND 16),
    fiscal_year_period   bigint,                -- JJJJPPP, z. B. 2026010 (Auswertungen)
    document_type        text NOT NULL DEFAULT 'SA' REFERENCES ledger__document_type (code),
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
    document_type      text NOT NULL DEFAULT 'SA' REFERENCES ledger__document_type (code),
    posting_date       date NOT NULL,
    special_period     text,                 -- Sonderperiode 13–16 (Buchungsdatum im Dezember)
    document_date      date,
    currency           text NOT NULL,
    header_text        text,
    reference          text,
    status             text NOT NULL DEFAULT 'DRAFT' CHECK (status IN ('DRAFT', 'POSTED', 'DISCARDED')),
    posted_document_id text,  -- bis 0.12.0 GUID des Belegs, ab 0.13.0 leer
    posted_fiscal_year     bigint,  -- gebuchter Beleg im Buchungskreis des Kopfs, gesetzt = gesperrt
    posted_document_number text,
    changed_by         text,
    changed_at         timestamptz,
    CHECK ((status = 'POSTED') = (posted_document_number IS NOT NULL))
);
CREATE INDEX ledger__draft_header_company ON ledger__draft_header (company_code_id, status);

CREATE TABLE ledger__draft_item (
    id                 text PRIMARY KEY,
    draft_id           text NOT NULL REFERENCES ledger__draft_header (id),
    line_item_number   bigint NOT NULL,
    account_number     text NOT NULL,
    shkzg              text NOT NULL CHECK (shkzg IN ('S', 'H')),
    item_type          text NOT NULL DEFAULT 'GL' CHECK (item_type IN ('GL', 'CUSTOMER', 'SUPPLIER', 'TAX', 'ASSET')),
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
-- bis 0.12.0: Einzelposten mit GUID; ab 0.13.0 leer (übernommen nach ledger__journal_item)
CREATE TABLE ledger__journal_entry_item (
    id                   text PRIMARY KEY,
    header_id            text NOT NULL REFERENCES ledger__journal_entry_header (id),
    line_item_number     bigint NOT NULL,
    ledger               text NOT NULL REFERENCES ledger__ledger (id),
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL,
    posting_date         date NOT NULL,
    fiscal_year_period   bigint,                -- JJJJPPP
    account_kind         text,                  -- Kontoart der Position (S, D, K, A …)
    source_module        text,                 -- Herkunft wie im Belegkopf (Darstellungsregeln je Position)
    chart_of_accounts_id text NOT NULL,
    account_number       text NOT NULL,
    shkzg                text NOT NULL CHECK (shkzg IN ('S', 'H')),
    item_type            text NOT NULL DEFAULT 'GL' CHECK (item_type IN ('GL', 'CUSTOMER', 'SUPPLIER', 'TAX', 'ASSET')),  -- analog KOART
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

-- Belegkopf (analog BKPF), ab 0.13.0: Buchungskreis, Geschäftsjahr, Belegnummer
-- (lückenlos aus numrange, Objekt JournalEntry, Intervall laut ledger__document_numbering)
CREATE TABLE ledger__journal_header (
    document_number      text NOT NULL,
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL CHECK (posting_period BETWEEN 1 AND 16),
    fiscal_year_period   bigint,
    document_type        text NOT NULL DEFAULT 'SA' REFERENCES ledger__document_type (code),
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
    reversed_fiscal_year     bigint,  -- Storno: stornierter Beleg (gleicher Buchungskreis)
    reversed_document_number text,
    reversal_fiscal_year     bigint,  -- Original: Stornobeleg
    reversal_document_number text,
    draft_id             text,
    created_by           text,
    created_at           timestamptz NOT NULL,
    PRIMARY KEY (company_code_id, fiscal_year, document_number)
);
CREATE UNIQUE INDEX ledger__journal_header_source_uk ON ledger__journal_header (company_code_id, source_module, source_reference);
CREATE INDEX ledger__journal_header_date ON ledger__journal_header (company_code_id, posting_date);

-- Einzelposten (analog ACDOCA), ab 0.13.0: Beleg, Ledger, Position (fortlaufend je Beleg und Ledger)
CREATE TABLE ledger__journal_item (
    document_number      text NOT NULL,
    line_item_number     bigint NOT NULL,
    ledger               text NOT NULL REFERENCES ledger__ledger (id),
    company_code_id      text NOT NULL,
    fiscal_year          bigint NOT NULL,
    posting_period       bigint NOT NULL,
    posting_date         date NOT NULL,
    fiscal_year_period   bigint,
    account_kind         text,
    chart_of_accounts_id text NOT NULL,
    account_number       text NOT NULL,
    shkzg                text NOT NULL CHECK (shkzg IN ('S', 'H')),
    source_module        text,
    item_type            text NOT NULL DEFAULT 'GL',
    amount_document_curr bigint NOT NULL,
    amount_local_curr    bigint NOT NULL,
    currency             text NOT NULL,
    local_currency       text NOT NULL,
    item_text            text,
    cost_center          text,
    profit_center        text,
    segment              text,
    sd_sales_order_id    text,
    sd_sales_org         text,
    sd_customer_id       text,
    rent_object_id       text,
    rent_contract_id     text,
    purchase_order_id    text,
    supplier_id          text,
    dimension_custom_1   text,
    dimension_custom_2   text,
    PRIMARY KEY (company_code_id, fiscal_year, document_number, ledger, line_item_number),
    FOREIGN KEY (company_code_id, fiscal_year, document_number) REFERENCES ledger__journal_header (company_code_id, fiscal_year, document_number),
    FOREIGN KEY (chart_of_accounts_id, account_number) REFERENCES ledger__account_master (chart_of_accounts_id, account_number),
    CHECK ((shkzg = 'S' AND amount_document_curr > 0) OR (shkzg = 'H' AND amount_document_curr < 0))
);
CREATE INDEX ledger__journal_item_account ON ledger__journal_item (company_code_id, ledger, fiscal_year, account_number);
CREATE INDEX ledger__journal_item_period ON ledger__journal_item (company_code_id, ledger, fiscal_year, posting_period);
CREATE INDEX ledger__journal_item_cost ON ledger__journal_item (cost_center);
CREATE INDEX ledger__journal_item_profit ON ledger__journal_item (profit_center);
CREATE INDEX ledger__journal_item_rentobj ON ledger__journal_item (rent_object_id);
CREATE INDEX ledger__journal_item_rentctr ON ledger__journal_item (rent_contract_id);
CREATE INDEX ledger__journal_item_cust ON ledger__journal_item (sd_customer_id);
CREATE INDEX ledger__journal_item_supp ON ledger__journal_item (supplier_id);

-- Belegnummernvergabe (ab 0.13.0): Intervall des Nummernkreises JournalEntry je
-- Buchungskreis, Ledger und Belegart (* = alle übrigen)
CREATE TABLE ledger__document_numbering (
    company_code_id text NOT NULL,
    ledger          text NOT NULL REFERENCES ledger__ledger (id),
    document_type   text NOT NULL,
    range_key       text NOT NULL,
    description     text,
    PRIMARY KEY (company_code_id, ledger, document_type)
);

-- Rekursive bzw. zyklische Fremdschlüssel (nach dem Anlegen beider Tabellen):
--   Storno ↔ Original (Selbstbezug des Belegkopfs), Vorerfassung ↔ gebuchter Beleg.
ALTER TABLE ledger__journal_entry_header
    ADD CONSTRAINT ledger__journal_entry_header_reversed_fk FOREIGN KEY (reversed_document_id) REFERENCES ledger__journal_entry_header (id),
    ADD CONSTRAINT ledger__journal_entry_header_reversal_fk FOREIGN KEY (reversal_document_id) REFERENCES ledger__journal_entry_header (id),
    ADD CONSTRAINT ledger__journal_entry_header_draft_fk FOREIGN KEY (draft_id) REFERENCES ledger__draft_header (id);
ALTER TABLE ledger__journal_header
    ADD CONSTRAINT ledger__journal_header_reversed_fk FOREIGN KEY (company_code_id, reversed_fiscal_year, reversed_document_number)
        REFERENCES ledger__journal_header (company_code_id, fiscal_year, document_number),
    ADD CONSTRAINT ledger__journal_header_reversal_fk FOREIGN KEY (company_code_id, reversal_fiscal_year, reversal_document_number)
        REFERENCES ledger__journal_header (company_code_id, fiscal_year, document_number),
    ADD CONSTRAINT ledger__journal_header_draft_fk FOREIGN KEY (draft_id) REFERENCES ledger__draft_header (id);
ALTER TABLE ledger__draft_header
    ADD CONSTRAINT ledger__draft_header_posted_fk FOREIGN KEY (company_code_id, posted_fiscal_year, posted_document_number)
        REFERENCES ledger__journal_header (company_code_id, fiscal_year, document_number);
