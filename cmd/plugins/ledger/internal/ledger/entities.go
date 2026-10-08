package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

const (
	tText = metamodel.TypeText
	tSel  = metamodel.TypeSelect
	tDate = metamodel.TypeDate
	tNum  = metamodel.TypeNumber
	tArea = metamodel.TypeTextarea
	tBool = metamodel.TypeBoolean

	// entryObject ist das Object der Belege; seine Rechte je Buchungskreis
	// (JournalEntry.list/get/post/reverse) gelten auch für Positionen,
	// Vorerfassung und Salden.
	entryObject = "JournalEntry"

	sideDebit  = "S" // Soll
	sideCredit = "H" // Haben

	draftOpen      = "DRAFT"
	draftPosted    = "POSTED"
	draftDiscarded = "DISCARDED"

	moduleManual = "MANUAL"

	balanceObject    = "AccountBalance"
	conversionObject = "CurrencyConversion"
	loaderObject     = "LedgerLoader"
)

// dimColumns sind die Kontierungsspalten des Universal Journals, in die
// Fachmodule über das Modul-Mapping schreiben dürfen.
var dimColumns = []string{
	"cost_center", "profit_center", "segment",
	"sd_sales_order_id", "sd_sales_org", "sd_customer_id",
	"rent_object_id", "rent_contract_id",
	"purchase_order_id", "supplier_id",
	"dimension_custom_1", "dimension_custom_2",
}

var (
	codeRe    = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	accountRe = regexp.MustCompile(`^[0-9A-Z][0-9A-Z._-]{0,19}$`)

	accountTypes = []metamodel.Option{
		{Value: "BALANCE_SHEET", Label: "Bestandskonto (Bilanz)"},
		{Value: "PRIMARY_COST", Label: "Primäre Kosten / Aufwand"},
		{Value: "SECONDARY_COST", Label: "Sekundäre Kosten"},
		{Value: "REVENUE", Label: "Erlös"},
		{Value: "NON_OPERATING", Label: "Neutraler Aufwand/Ertrag"},
	}
	reconTypes = []metamodel.Option{
		{Value: "NONE", Label: "Kein Abstimmkonto"},
		{Value: "CUSTOMER", Label: "Debitoren"},
		{Value: "SUPPLIER", Label: "Kreditoren"},
		{Value: "ASSET", Label: "Anlagen"},
	}
	taxCategories = []metamodel.Option{
		{Value: "NONE", Label: "Ohne Steuer"},
		{Value: "ANY", Label: "Jedes Steuerkennzeichen (*)"},
		{Value: "INPUT_ONLY", Label: "Nur Vorsteuer (-)"},
		{Value: "OUTPUT_ONLY", Label: "Nur Umsatzsteuer (+)"},
		{Value: "INPUT_TAX_ACCOUNT", Label: "Vorsteuerkonto (<)"},
		{Value: "OUTPUT_TAX_ACCOUNT", Label: "Umsatzsteuerkonto (>)"},
	}
	rateTypes = []metamodel.Option{
		{Value: "M", Label: "M – Durchschnittskurs"},
		{Value: "B", Label: "B – Briefkurs (Verkauf)"},
		{Value: "G", Label: "G – Geldkurs (Kauf)"},
	}
	fyVariants = []metamodel.Option{{Value: "K4", Label: "K4 – Kalenderjahr, 12 + 4 Sonderperioden"}}
	sides      = []metamodel.Option{{Value: sideDebit, Label: "Soll"}, {Value: sideCredit, Label: "Haben"}}
	docTypes   = []metamodel.Option{
		{Value: "SA", Label: "SA – Sachkontenbeleg"},
		{Value: "DR", Label: "DR – Debitorenrechnung"},
		{Value: "DZ", Label: "DZ – Debitorenzahlung"},
		{Value: "KR", Label: "KR – Kreditorenrechnung"},
		{Value: "KZ", Label: "KZ – Kreditorenzahlung"},
		{Value: "AB", Label: "AB – Verrechnung / Storno"},
	}
	draftStatus = []metamodel.Option{{Value: draftOpen, Label: "In Erfassung"}, {Value: draftPosted, Label: "Gebucht"},
		{Value: draftDiscarded, Label: "Verworfen"}}
	specialPeriods = []metamodel.Option{{Value: "13", Label: "13"}, {Value: "14", Label: "14"}, {Value: "15", Label: "15"}, {Value: "16", Label: "16"}}

	refChart       = &crud.Ref{Table: "ledger__chart_of_accounts", Column: "id", Label: "Kontenplan", Object: "ChartOfAccounts", LabelFields: []string{"name"}}
	refLedger      = &crud.Ref{Table: "ledger__ledger", Column: "id", Label: "Ledger", ActiveField: "is_active", Object: "Ledger", LabelFields: []string{"name"}}
	refCurrency    = &crud.Ref{Table: "ledger__currency", Column: "code", Label: "Währung", ActiveField: "is_active", Object: "Currency", LabelFields: []string{"name"}}
	refAccountKind = &crud.Ref{Table: "ledger__account_type", Column: "code", Label: "Kontoart", ActiveField: "is_active", Object: "AccountType", LabelFields: []string{"name"}}
	lookupCC       = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupEntry    = &metamodel.Lookup{Object: entryObject, ValueField: "id", LabelFields: []string{"document_number"}}
	lookupDraft    = &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}
)

func (m *Module) entities() []*crud.Entity {
	es := []*crud.Entity{
		m.journalDraft(), m.journalDraftItem(), m.journalEntry(), m.journalEntryItem(),
		m.chartOfAccounts(), m.glAccount(), m.glAccountCompany(), m.companyConfig(),
		m.accountTypeEntity(), m.postingPeriodEntity(), m.fiscalPeriod(), m.periodAccountLock(), m.documentTypeEntity(), m.fieldStatusGroup(), m.fieldStatus(),
		m.ledgerDef(), m.currencyEntity(), m.exchangeRate(),
	}
	for _, e := range es {
		m.withMigration(e)
		if slices.Contains(e.Filters, "account_number") {
			// Filter mit oder ohne Kontenplan: 1200 findet SKR25-1200.
			e.FilterExpr = map[string]func(any) (string, []any){"account_number": accountFilter}
		}
	}
	return es
}

// withMigration: Vor dem ersten Zugriff auf Datensätze die Daten älterer
// Versionen umstellen (migrations.go).
func (m *Module) withMigration(e *crud.Entity) {
	scope, check := e.ListScope, e.CheckRecord
	e.ListScope = func(ctx context.Context) (string, []any, bool, error) {
		if err := m.migrate(ctx); err != nil {
			return "", nil, false, err
		}
		if scope == nil {
			return "", nil, false, nil
		}
		return scope(ctx)
	}
	e.CheckRecord = func(ctx context.Context, action string, rec crud.Record) error {
		if err := m.migrate(ctx); err != nil {
			return err
		}
		if check == nil {
			return nil
		}
		return check(ctx, action, rec)
	}
}

// dimFields: Felder der Kontierungsspalten (Belegposition und Vorerfassung).
func dimFields(readOnly bool) []crud.Field {
	labels := map[string]string{
		"cost_center": "Kostenstelle", "profit_center": "Profit-Center", "segment": "Segment",
		"sd_sales_order_id": "Kundenauftrag (SD)", "sd_sales_org": "Verkaufsorganisation (SD)", "sd_customer_id": "Kunde / Debitor (SD)",
		"rent_object_id": "Mietobjekt (RENT)", "rent_contract_id": "Mietvertrag (RENT)",
		"purchase_order_id": "Bestellung (Einkauf)", "supplier_id": "Lieferant / Kreditor (Einkauf)",
		"dimension_custom_1": "Freie Dimension 1", "dimension_custom_2": "Freie Dimension 2",
	}
	var out []crud.Field
	for _, c := range dimColumns {
		f := crud.Field{Key: c, Label: labels[c], Type: tText, ReadOnly: readOnly}
		if !readOnly {
			f.Group = "Kontierung"
		}
		if c == "rent_object_id" {
			// Mietobjekt aus der Immobilienverwaltung (Plugin realestate); fehlt es, bleibt das Feld Text.
			f.Lookup = &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}}
		}
		if c == "rent_contract_id" {
			// Vertrag aus der Vertragsverwaltung (Plugin contract).
			f.Lookup = &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"}}
		}
		out = append(out, f)
	}
	return out
}

// --- Stammdaten ------------------------------------------------------------------

// ChartOfAccounts: Kontenplan (Kopf), z. B. SKR04, SKR25.
func (m *Module) chartOfAccounts() *crud.Entity {
	return &crud.Entity{
		Object: "ChartOfAccounts", Title: "Kontenpläne", Icon: "icon-layers", Table: "ledger__chart_of_accounts", Section: "Kontenplan",
		Keys: []string{"id"}, Order: "id", Search: []string{"id", "name"}, TitleField: "name",
		Fields: []crud.Field{
			{Key: "id", Label: "Kontenplan", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "country", Label: "Land (ISO-2)", Type: tText, Listable: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Kontenplan", Fields: []string{"id", "name", "description", "country"}},
			{Key: "konten", Title: "Sachkonten", Relation: &metamodel.Relation{Object: "GLAccount", ForeignKey: "chart_of_accounts_id",
				Columns: []string{"account_number", "name", "account_type", "account_group", "is_active"}}},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "load", Label: "Kontenrahmen laden …", Fields: []string{"id"}},
			Handle: m.loadCoaAction}},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["id"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["id"])))
				if !codeRe.MatchString(crud.Str(rec["id"])) {
					return crud.Invalid("Kontenplan %q: Großbuchstaben, Ziffern, _ und - (höchstens 20 Zeichen)", crud.Str(rec["id"]))
				}
			}
			return nil
		},
	}
}

// GLAccount: Sachkonto im Kontenplan (analog SKA1), unabhängig vom Buchungskreis.
func (m *Module) glAccount() *crud.Entity {
	return &crud.Entity{
		Object: "GLAccount", Title: "Sachkonten (Kontenplan)", Icon: "icon-list", Table: "ledger__account_master", Section: "Kontenplan",
		Keys: []string{"chart_of_accounts_id", "account_number"}, Order: "chart_of_accounts_id, account_number",
		Search: []string{"account_number", "name"}, Filters: []string{"chart_of_accounts_id", "account_type", "is_active"},
		TitleField: "name",
		Actions:    m.lockActions("GLAccount", "is_active", false, "Konto"),
		Decorate: func(_ context.Context, rec crud.Record) error {
			hideLockAction(rec, !crud.AsBool(rec["is_active"]))
			return nil
		},
		Fields: []crud.Field{
			{Key: "chart_of_accounts_id", Label: "Kontenplan", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refChart},
			{Key: "account_number", Label: "Kontonummer (Kontenplan-Nummer)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "account_type", Label: "Kontotyp", Type: tSel, Required: true, Listable: true, Options: accountTypes},
			{Key: "account_kind", Label: "Kontoart", Type: tText, Listable: true, Ref: refAccountKind},
			{Key: "account_group", Label: "Kontengruppe / -klasse", Type: tText, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if crud.Str(rec["account_kind"]) == "" {
				rec["account_kind"] = "S"
			}
			if old == nil {
				// Immer <Kontenplan>-<Nummer>: "1200" wird ergänzt, ein fremder Kontenplan abgelehnt.
				no, err := m.accountKey(ctx, crud.Str(rec["chart_of_accounts_id"]), crud.Str(rec["account_number"]))
				if err != nil {
					return err
				}
				rec["account_number"], rec["is_active"] = no, true
			}
			return nil
		},
	}
}

// GLAccountCompany: Sachkonto im Buchungskreis (analog SKB1): Währung,
// Abstimmkonto, Steuerkategorie, Buchungssperre.
func (m *Module) glAccountCompany() *crud.Entity {
	return &crud.Entity{
		Object: "GLAccountCompany", Title: "Sachkonten (Buchungskreis)", Icon: "icon-list", Table: "ledger__account_company", Section: "Kontenplan",
		Events: true, // Kopie in contract-billing
		Keys:   []string{"id"}, Surrogate: true, Order: "company_code_id, account_number",
		Search: []string{"account_number", "alternative_account_number"}, Filters: []string{"company_code_id", "account_number", "reconciliation_type", "is_blocked"},
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "chart_of_accounts_id", Label: "Kontenplan", Type: tText, Listable: true, ReadOnly: true},
			{Key: "account_number", Label: "Kontonummer", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "account_name", Label: "Bezeichnung", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "currency", Label: "Kontowährung", Type: tText, Listable: true, Ref: refCurrency},
			{Key: "reconciliation_type", Label: "Abstimmkonto für", Type: tSel, Listable: true, Options: reconTypes},
			{Key: "alternative_account_number", Label: "Alternative Kontonummer", Type: tText},
			{Key: "tax_category", Label: "Steuerkategorie", Type: tSel, Options: taxCategories},
			{Key: "field_status_group", Label: "Feldstatusgruppe", Type: tText, Listable: true, Ref: refFSG},
			{Key: "is_blocked", Label: "Buchungssperre", Type: tBool, Listable: true, ReadOnly: true},
		},
		Actions: m.lockActions("GLAccountCompany", "is_blocked", true, "Konto im Buchungskreis"),
		Access:  readByCompany(""),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "GLAccountCompany", action, crud.Str(rec["company_code_id"]))
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			rec["account_name"] = m.accountName(ctx, crud.Str(rec["chart_of_accounts_id"]), crud.Str(rec["account_number"]))
			hideLockAction(rec, crud.AsBool(rec["is_blocked"]))
			return nil
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			for k, def := range map[string]string{"reconciliation_type": "NONE", "tax_category": "NONE"} {
				if crud.Str(rec[k]) == "" {
					rec[k] = def
				}
			}
			if old != nil {
				return nil
			}
			cc := crud.Str(rec["company_code_id"])
			cfg, err := m.config(ctx, cc)
			if err != nil {
				return err
			}
			rec["chart_of_accounts_id"] = cfg.Chart
			no, err := m.accountKey(ctx, cfg.Chart, crud.Str(rec["account_number"]))
			if err != nil {
				return err
			}
			rec["account_number"] = no
			if crud.Str(rec["currency"]) == "" {
				rec["currency"] = cfg.Currency
			}
			ma, err := m.masterAccount(ctx, cfg.Chart, crud.Str(rec["account_number"]))
			if err != nil {
				return err
			}
			if crud.Str(rec["field_status_group"]) == "" {
				rec["field_status_group"] = defaultGroup(ma.Type, crud.Str(rec["reconciliation_type"]), crud.Str(rec["tax_category"]))
			}
			res, err := m.db.Query(ctx, "SELECT 1 FROM ledger__account_company WHERE company_code_id = ? AND account_number = ?", cc, rec["account_number"])
			if err != nil {
				return err
			}
			if len(res.Rows) > 0 {
				return crud.Invalid("Konto %s ist dem Buchungskreis %s bereits zugeordnet", crud.Str(rec["account_number"]), cc)
			}
			return nil
		},
	}
}

// LedgerCompanyConfig: Steuerung des Hauptbuchs je Buchungskreis inkl.
// Modul-Mapping (welche Kontierung eines Moduls in welche ACDOCA-Spalte geht).
func (m *Module) companyConfig() *crud.Entity {
	return &crud.Entity{
		Object: "LedgerCompanyConfig", Title: "Buchungskreise (Hauptbuch)", Icon: "icon-settings", Table: "ledger__company_config", Section: "Einstellungen",
		Keys: []string{"company_code_id"}, Order: "company_code_id",
		Fields: []crud.Field{
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "leading_ledger", Label: "Führendes Ledger", Type: tText, Required: true, Listable: true, Ref: refLedger},
			{Key: "chart_of_accounts_id", Label: "Kontenplan", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refChart},
			{Key: "currency", Label: "Hauswährung", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refCurrency},
			{Key: "fiscal_year_variant", Label: "Geschäftsjahresvariante", Type: tSel, Options: fyVariants},
			{Key: "exchange_rate_type", Label: "Kurstyp für Umrechnung", Type: tSel, Options: rateTypes},
			{Key: "module_field_mapping", Label: "Modul-Mapping (JSON, leer = Standard)", Type: tArea},
			{Key: "setup_year", Label: "Perioden öffnen für Geschäftsjahr", Type: tNum, Virtual: true},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Buchungskreis einrichten …",
			Fields: []string{"company_code_id", "chart_of_accounts_id", "currency", "setup_year"}}, Handle: m.setupCompanyAction}},
		Access: &crud.Access{Records: true, CompanyCode: "company_code_id",
			FieldGroups: []metamodel.FieldGroup{{Key: "mapping", Label: "Modul-Mapping", Fields: []string{"module_field_mapping"}}}},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "LedgerCompanyConfig", action, crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			for k, def := range map[string]string{"fiscal_year_variant": "K4", "exchange_rate_type": "M"} {
				if crud.Str(rec[k]) == "" {
					rec[k] = def
				}
			}
			mapping, err := parseMapping(crud.Str(rec["module_field_mapping"]))
			if err != nil {
				return err
			}
			if mapping == nil {
				rec["module_field_mapping"] = nil
			} else {
				b, _ := json.MarshalIndent(mapping, "", "  ")
				rec["module_field_mapping"] = string(b)
			}
			if old == nil {
				return m.companyCodeExists(ctx, crud.Str(rec["company_code_id"]))
			}
			return nil
		},
	}
}

// Ledger: führendes (0L) und parallele Ledger.
func (m *Module) ledgerDef() *crud.Entity {
	return &crud.Entity{
		Object: "Ledger", Title: "Ledger", Icon: "icon-book", Table: "ledger__ledger", Section: "Einstellungen",
		Keys: []string{"id"}, Order: "id", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "id", Label: "Ledger", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "is_leading", Label: "Führend", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["id"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["id"])))
				if !regexp.MustCompile(`^[0-9A-Z]{2}$`).MatchString(crud.Str(rec["id"])) {
					return crud.Invalid("Ledger: zwei Zeichen, z. B. 0L oder 2L")
				}
				rec["is_active"] = true
			}
			return nil
		},
	}
}

func (m *Module) currencyEntity() *crud.Entity {
	return &crud.Entity{
		Object: "Currency", Title: "Währungen", Icon: "icon-money", Table: "ledger__currency", Section: "Währungen",
		Keys: []string{"code"}, Order: "code", Search: []string{"code", "name"}, StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Währung (ISO 4217)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "decimals", Label: "Nachkommastellen", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if !currencyRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Währung %q: dreistelliger ISO-4217-Code", crud.Str(rec["code"]))
				}
				if d := toInt(rec["decimals"]); d < 0 || d > 4 {
					return crud.Invalid("Nachkommastellen: 0–4")
				}
				rec["is_active"] = true
			}
			return nil
		},
	}
}

// ExchangeRate: Tageskurs je Kurstyp und Währungspaar ab valid_from (analog TCURR).
func (m *Module) exchangeRate() *crud.Entity {
	return &crud.Entity{
		Object: "ExchangeRate", Title: "Wechselkurse", Icon: "icon-money", Table: "ledger__exchange_rate", Section: "Währungen",
		Keys:  []string{"rate_type", "from_currency", "to_currency", "valid_from"},
		Order: "rate_type, from_currency, to_currency, valid_from DESC", Filters: []string{"rate_type", "from_currency", "to_currency"},
		Fields: []crud.Field{
			{Key: "rate_type", Label: "Kurstyp", Type: tSel, Required: true, Listable: true, Immutable: true, Options: rateTypes},
			{Key: "from_currency", Label: "Von Währung", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refCurrency},
			{Key: "to_currency", Label: "Nach Währung", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refCurrency},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "rate", Label: "Kurs", Type: tText, Required: true, Listable: true},
			{Key: "from_factor", Label: "Faktor Von-Währung", Type: tNum, Listable: true},
			{Key: "to_factor", Label: "Faktor Nach-Währung", Type: tNum, Listable: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error { return checkRate(rec) },
	}
}

func checkRate(rec crud.Record) error {
	if crud.Str(rec["from_currency"]) == crud.Str(rec["to_currency"]) {
		return crud.Invalid("Von- und Nach-Währung müssen verschieden sein")
	}
	r, err := rateOf(crud.Str(rec["rate"]))
	if err != nil {
		return crud.Invalid("%v", err)
	}
	rec["rate"] = r.FloatString(10)
	rec["rate"] = strings.TrimRight(strings.TrimRight(crud.Str(rec["rate"]), "0"), ".")
	for _, k := range []string{"from_factor", "to_factor"} {
		if rec[k] == nil || crud.Str(rec[k]) == "" {
			rec[k] = int64(1)
		}
		if toInt(rec[k]) < 1 {
			return crud.Invalid("%s muss mindestens 1 sein", k)
		}
		rec[k] = toInt(rec[k])
	}
	return nil
}

// --- Belege ------------------------------------------------------------------------

// JournalEntry: Belegkopf (analog BKPF). Schreibgeschützt: Belege entstehen
// nur durch Buchen (Vorerfassung oder Service LedgerPosting) und Storno.
func (m *Module) journalEntry() *crud.Entity {
	return &crud.Entity{
		Object: entryObject, Title: "Buchungsbelege", Icon: "icon-book", Table: "ledger__journal_entry_header", Section: "Belege",
		Keys: []string{"id"}, Surrogate: true, ReadOnly: true,
		Order:      "posting_date DESC, document_number DESC",
		Search:     []string{"document_number", "header_text", "reference", "source_reference"},
		Filters:    []string{"company_code_id", "fiscal_year", "posting_period", "fiscal_year_period", "source_module", "reversal_flag", "document_type"},
		TitleField: "document_number",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "document_number", Label: "Belegnummer", Type: tText, ReadOnly: true, Listable: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, ReadOnly: true, Listable: true, Lookup: lookupCC},
			{Key: "fiscal_year", Label: "Geschäftsjahr", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "posting_period", Label: "Periode", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "fiscal_year_period", Label: "Jahr/Periode", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "document_type", Label: "Belegart", Type: tText, ReadOnly: true, Listable: true, Ref: refDocType},
			{Key: "document_date", Label: "Belegdatum", Type: tDate, ReadOnly: true},
			{Key: "posting_date", Label: "Buchungsdatum", Type: tDate, Required: true, Listable: true},
			{Key: "currency", Label: "Belegwährung", Type: tText, ReadOnly: true},
			{Key: "local_currency", Label: "Hauswährung", Type: tText, ReadOnly: true},
			{Key: "exchange_rate", Label: "Umrechnungskurs", Type: tText, ReadOnly: true},
			{Key: "total", Label: "Betrag", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "reference", Label: "Referenz", Type: tText, ReadOnly: true},
			{Key: "header_text", Label: "Belegkopftext", Type: tText, Listable: true},
			{Key: "source_module", Label: "Herkunft (Modul)", Type: tText, ReadOnly: true, Listable: true},
			{Key: "source_reference", Label: "Referenz im Modul", Type: tText, ReadOnly: true},
			{Key: "reversal_flag", Label: "Storno", Type: tBool, ReadOnly: true, Listable: true},
			{Key: "reversed_document_id", Label: "Storno zu Beleg", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "reversal_document_id", Label: "Storniert durch", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText, ReadOnly: true, Lookup: lookupDraft},
			{Key: "created_by", Label: "Erfasst von", Type: tText, ReadOnly: true},
			{Key: "created_at", Label: "Erfasst am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Belegkopf", Fields: []string{"document_number", "company_code_id", "document_type", "fiscal_year", "posting_period",
				"posting_date", "document_date", "header_text", "reference", "total", "currency", "local_currency", "exchange_rate"}},
			{Key: "positionen", Title: "Positionen (Universal Journal)", Relation: &metamodel.Relation{Object: "JournalEntryItem", ForeignKey: "header_id",
				Columns: []string{"line_item_number", "ledger", "account_number", "account_name", "debit", "credit", "local_amount", "cost_center", "rent_object_id", "rent_contract_id", "item_text"}}},
			{Key: "herkunft", Title: "Herkunft und Storno", Collapsed: true, Fields: []string{"source_module", "source_reference", "draft_id",
				"reversal_flag", "reversed_document_id", "reversal_document_id", "created_by", "created_at", "id"}},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "reverse", Label: "Stornieren …", Record: true,
			Fields: []string{"posting_date", "header_text"}}, Handle: m.reverseAction}},
		Access:   readByCompany(""),
		Decorate: m.decorateEntry,
	}
}

// JournalEntryItem: Einzelposten des Universal Journals (analog ACDOCA).
func (m *Module) journalEntryItem() *crud.Entity {
	fields := []crud.Field{
		{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
		{Key: "header_id", Label: "Beleg", Type: tText, ReadOnly: true, Lookup: lookupEntry},
		{Key: "line_item_number", Label: "Pos.", Type: tNum, ReadOnly: true, Listable: true},
		{Key: "ledger", Label: "Ledger", Type: tText, ReadOnly: true, Listable: true},
		{Key: "company_code_id", Label: "Buchungskreis", Type: tText, ReadOnly: true, Listable: true},
		{Key: "fiscal_year", Label: "Geschäftsjahr", Type: tNum, ReadOnly: true},
		{Key: "posting_period", Label: "Periode", Type: tNum, ReadOnly: true},
		{Key: "fiscal_year_period", Label: "Jahr/Periode", Type: tNum, ReadOnly: true, Listable: true},
		{Key: "account_kind", Label: "Kontoart", Type: tText, ReadOnly: true, Ref: refAccountKind},
		{Key: "posting_date", Label: "Buchungsdatum", Type: tDate, ReadOnly: true, Listable: true},
		{Key: "chart_of_accounts_id", Label: "Kontenplan", Type: tText, ReadOnly: true},
		{Key: "account_number", Label: "Konto", Type: tText, ReadOnly: true, Listable: true},
		{Key: "account_name", Label: "Kontobezeichnung", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
		{Key: "shkzg", Label: "Soll/Haben", Type: tSel, ReadOnly: true, Options: sides},
		{Key: "item_type", Label: "Positionsart", Type: tSel, ReadOnly: true, Listable: true, Options: itemTypes},
		{Key: "amount_document_curr", Label: "Betrag Belegwährung (kleinste Einheit)", Type: tNum, ReadOnly: true},
		{Key: "amount_local_curr", Label: "Betrag Hauswährung (kleinste Einheit)", Type: tNum, ReadOnly: true},
		{Key: "debit", Label: "Soll", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
		{Key: "credit", Label: "Haben", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
		{Key: "local_amount", Label: "Hauswährung", Type: tText, Virtual: true, ReadOnly: true},
		{Key: "currency", Label: "Belegwährung", Type: tText, ReadOnly: true},
		{Key: "local_currency", Label: "Hauswährung", Type: tText, ReadOnly: true},
		{Key: "item_text", Label: "Positionstext", Type: tText, ReadOnly: true, Listable: true},
		{Key: "source_module", Label: "Herkunft", Type: tText, ReadOnly: true},
	}
	fields = append(fields, dimFields(true)...)
	return &crud.Entity{
		Object: "JournalEntryItem", Title: "Einzelposten (Universal Journal)", Icon: "icon-list", Table: "ledger__journal_entry_item", Section: "Belege",
		Keys: []string{"id"}, Surrogate: true, ReadOnly: true,
		Order:   "posting_date DESC, header_id, line_item_number",
		Filters: append([]string{"header_id", "company_code_id", "ledger", "fiscal_year", "posting_period", "fiscal_year_period", "account_kind", "account_number", "shkzg", "source_module"}, dimColumns...),
		Fields:  fields,
		Access:  readByCompany(entryObject),
		Decorate: func(ctx context.Context, rec crud.Record) error {
			dd, _ := m.cur.decimals(ctx, crud.Str(rec["currency"]))
			ld, _ := m.cur.decimals(ctx, crud.Str(rec["local_currency"]))
			doc, loc := toInt(rec["amount_document_curr"]), toInt(rec["amount_local_curr"])
			if doc >= 0 {
				rec["debit"] = formatAmount(doc, dd)
			} else {
				rec["credit"] = formatAmount(-doc, dd)
			}
			rec["local_amount"] = formatAmount(loc, ld) + " " + crud.Str(rec["local_currency"])
			rec["account_name"] = m.accountName(ctx, crud.Str(rec["chart_of_accounts_id"]), crud.Str(rec["account_number"]))
			if rec["source_module"] == nil {
				rec["source_module"] = m.healSourceModule(ctx, crud.Str(rec["header_id"]))
			}
			return nil
		},
	}
}

func (m *Module) decorateEntry(ctx context.Context, rec crud.Record) error {
	res, err := m.db.Query(ctx, `SELECT COALESCE(SUM(amount_document_curr), 0) FROM ledger__journal_entry_item
		WHERE header_id = ? AND amount_document_curr > 0 AND ledger = (SELECT MIN(ledger) FROM ledger__journal_entry_item WHERE header_id = ?)`, rec["id"], rec["id"])
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	d, _ := m.cur.decimals(ctx, crud.Str(rec["currency"]))
	rec["total"] = formatAmount(toInt(res.Rows[0][0]), d) + " " + crud.Str(rec["currency"])
	return nil
}

// --- Vorerfassung ------------------------------------------------------------------

// JournalDraft: Buchungskopf der Vorerfassung (analog VBKPF). Speichern und
// ändern, solange der Status DRAFT ist; "Buchen" erzeugt den Beleg.
func (m *Module) journalDraft() *crud.Entity {
	return &crud.Entity{
		Object: "JournalDraft", Title: "Vorerfassung", Icon: "icon-edit", Table: "ledger__draft_header", Section: "Belege",
		FormState: m.draftFormState,
		Keys:      []string{"id"}, Surrogate: true, Order: "changed_at DESC",
		Events: true,
		Search: []string{"header_text", "reference"}, Filters: []string{"company_code_id", "status"},
		StatusField: "status", StatusActive: draftOpen, StatusInactive: draftDiscarded,
		TitleField: "header_text",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Trigger: true, Lookup: lookupCC},
			{Key: "document_type", Label: "Belegart", Type: tText, Listable: true, Trigger: true, Ref: refDocType},
			{Key: "posting_date", Label: "Buchungsdatum", Type: tDate, Required: true, Listable: true, Trigger: true},
			{Key: "special_period", Label: "Sonderperiode", Type: tSel, Options: specialPeriods},
			{Key: "document_date", Label: "Belegdatum", Type: tDate},
			{Key: "currency", Label: "Belegwährung", Type: tText, Required: true, Listable: true},
			{Key: "header_text", Label: "Belegkopftext", Type: tText, Required: true, Listable: true},
			{Key: "reference", Label: "Referenz", Type: tText},
			{Key: "balance", Label: "Saldo Soll − Haben", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "status", Label: "Status", Type: tSel, ReadOnly: true, Listable: true, Options: draftStatus},
			{Key: "posted_document_id", Label: "Gebuchter Beleg", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Buchungskopf", Fields: []string{"company_code_id", "document_type", "posting_date", "special_period", "document_date", "currency",
				"header_text", "reference", "balance", "status", "posted_document_id"}},
			{Key: "positionen", Title: "Buchungspositionen", Relation: &metamodel.Relation{Object: "JournalDraftItem", ForeignKey: "draft_id",
				Columns: []string{"line_item_number", "account_number", "account_name", "shkzg", "amount", "cost_center", "rent_object_id", "rent_contract_id", "item_text"}}},
			{Key: "protokoll", Title: "Protokoll", Collapsed: true, Fields: []string{"changed_by", "changed_at", "id"}},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "simulate", Label: "Prüfen", Record: true, Confirm: "Vorerfassung prüfen (ohne zu buchen)?"},
				Handle: m.draftSimulateAction},
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen", Record: true,
				Confirm: "Vorerfassung jetzt buchen? Danach ist der Beleg nicht mehr änderbar."}, Handle: m.draftPostAction},
		},
		Access: readByCompany(entryObject),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			if action != "get" && action != "list" && crud.Str(rec["status"]) != draftOpen {
				return crud.Invalid("Vorerfassung ist %s und nicht mehr änderbar", strings.ToLower(statusLabel(crud.Str(rec["status"]))))
			}
			if action == "get" {
				return nil // Lesen: Access (JournalEntry.read)
			}
			return requireCompanyCode(ctx, entryObject, "post", crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["currency"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["currency"])))
			if _, err := m.cur.decimals(ctx, crud.Str(rec["currency"])); err != nil {
				return err
			}
			if crud.Str(rec["document_type"]) == "" {
				rec["document_type"] = "SA"
			}
			if old == nil {
				rec["status"] = draftOpen
				cc := crud.Str(rec["company_code_id"])
				if _, err := m.config(ctx, cc); err != nil {
					return err
				}
				if err := requireCompanyCode(ctx, entryObject, "post", cc); err != nil {
					return err
				}
			}
			rec["changed_at"], rec["changed_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
			return nil
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			d, _ := m.cur.decimals(ctx, crud.Str(rec["currency"]))
			res, err := m.db.Query(ctx, "SELECT shkzg, amount FROM ledger__draft_item WHERE draft_id = ?", rec["id"])
			if err != nil {
				return err
			}
			var sum int64
			for _, r := range res.Rows {
				n, err := parseAmount(crud.Str(r[1]), d)
				if err != nil {
					continue
				}
				if crud.Str(r[0]) == sideCredit {
					n = -n
				}
				sum += n
			}
			rec["balance"] = formatAmount(sum, d) + " " + crud.Str(rec["currency"])
			rec["_locked"] = crud.Str(rec["status"]) != draftOpen // Oberfläche: nicht mehr änderbar
			return nil
		},
	}
}

func statusLabel(s string) string {
	for _, o := range draftStatus {
		if o.Value == s {
			return o.Label
		}
	}
	return s
}

// JournalDraftItem: Buchungsposition der Vorerfassung (analog VBSEG).
func (m *Module) journalDraftItem() *crud.Entity {
	fields := []crud.Field{
		{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
		{Key: "draft_id", Label: "Vorerfassung", Type: tText, Required: true, Immutable: true, Lookup: lookupDraft},
		{Key: "line_item_number", Label: "Pos.", Type: tNum, Listable: true, Group: "Position"},
		{Key: "item_type", Label: "Positionsart", Type: tSel, Listable: true, Trigger: true, Options: itemTypes, Group: "Position"},
		{Key: "account_number", Label: "Konto", Type: tText, Required: true, Listable: true, Trigger: true, Group: "Position",
			Lookup: &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
				Columns: []string{"account_number", "account_name", "reconciliation_type", "field_status_group"},
				Filters: map[string]string{"company_code_id": "draft_id.company_code_id", "is_blocked": "=false"}}},
		{Key: "account_name", Label: "Kontobezeichnung", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
		{Key: "shkzg", Label: "Soll/Haben", Type: tSel, Required: true, Listable: true, Options: sides, Group: "Position"},
		{Key: "amount", Label: "Betrag", Type: tText, Required: true, Listable: true, Group: "Position"},
		{Key: "item_text", Label: "Positionstext", Type: tText, Listable: true, Group: "Position"},
	}
	fields = append(fields, dimFields(false)...)
	return &crud.Entity{
		Object: "JournalDraftItem", Title: "Vorerfassung – Positionen", Icon: "icon-list", Table: "ledger__draft_item", Section: "Belege",
		FormState: m.draftItemFormState,
		Events:    true,
		Keys:      []string{"id"}, Surrogate: true, Order: "draft_id, line_item_number", Filters: []string{"draft_id", "account_number"},
		Fields: fields,
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Entfernen", Record: true,
			Confirm: "Position aus der Vorerfassung entfernen?"}, Handle: m.draftItemRemoveAction}},
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			// Positionen folgen dem Kopf: Leserecht JournalEntry.read im Buchungskreis des Kopfs.
			g, err := sdk.Grants(ctx, entryObject, metamodel.ActionRead)
			if err != nil {
				return "", nil, false, err
			}
			where, args := g.SQL(map[string]string{sdk.AttrCompanyCode: "company_code_id"})
			return "draft_id IN (SELECT id FROM ledger__draft_header WHERE " + where + ")", args, false, nil
		},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			d, err := m.draftHeader(ctx, crud.Str(rec["draft_id"]))
			if err != nil {
				return err
			}
			if action == "get" || action == "list" {
				return requireRead(ctx, entryObject, d.CompanyCode)
			}
			return d.editable(ctx)
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			if d, err := m.draftHeader(ctx, crud.Str(rec["draft_id"])); err == nil {
				rec["_locked"] = d.Status != draftOpen
				if cfg, err := m.config(ctx, d.CompanyCode); err == nil {
					rec["account_name"] = m.accountName(ctx, cfg.Chart, crud.Str(rec["account_number"]))
				}
			}
			return nil
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			d, err := m.draftHeader(ctx, crud.Str(rec["draft_id"]))
			if err != nil {
				return err
			}
			if err := d.editable(ctx); err != nil {
				return err
			}
			chart, err := m.companyChart(ctx, d.CompanyCode)
			if err != nil {
				return err
			}
			if rec["account_number"], err = m.accountKey(ctx, chart, crud.Str(rec["account_number"])); err != nil {
				return err
			}
			if s := strings.ToUpper(crud.Str(rec["shkzg"])); s == sideDebit || s == sideCredit {
				rec["shkzg"] = s
			} else {
				return crud.Invalid("Soll/Haben: S oder H")
			}
			dec, err := m.cur.decimals(ctx, d.Currency)
			if err != nil {
				return err
			}
			n, err := parseAmount(crud.Str(rec["amount"]), dec)
			if err != nil {
				return crud.Invalid("%v", err)
			}
			rec["amount"] = formatAmount(n, dec)
			// Regeln wie beim Buchen: Positionsart, Feldstatus, Partner.
			rule, err := m.rule(ctx, d.CompanyCode, d.DocumentType, crud.Str(rec["account_number"]), crud.Str(rec["item_type"]))
			if err != nil {
				return err
			}
			rec["item_type"] = rule.ItemType
			values := map[string]string{}
			for _, f := range statusFields {
				values[f] = crud.Str(rec[f])
			}
			if err := rule.check(crud.Str(rec["account_number"]), values); err != nil {
				return err
			}
			if rec["line_item_number"] == nil || toInt(rec["line_item_number"]) == 0 {
				res, err := m.db.Query(ctx, "SELECT COALESCE(MAX(line_item_number), 0) FROM ledger__draft_item WHERE draft_id = ?", d.ID)
				if err != nil {
					return err
				}
				rec["line_item_number"] = toInt(res.Rows[0][0]) + 1
			}
			_, err = m.db.Exec(ctx, "UPDATE ledger__draft_header SET changed_at = ?, changed_by = ? WHERE id = ?",
				time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID), d.ID)
			return err
		},
	}
}

// --- Hilfsfunktionen ---------------------------------------------------------------

// companyConfig ist die Steuerung eines Buchungskreises.
type companyConfig struct {
	CompanyCode, Ledger, Chart, Currency, RateType string
	Mapping                                        map[string]map[string]string
}

func (m *Module) config(ctx context.Context, cc string) (*companyConfig, error) {
	res, err := m.db.Query(ctx, `SELECT leading_ledger, chart_of_accounts_id, currency, exchange_rate_type, module_field_mapping
		FROM ledger__company_config WHERE company_code_id = ?`, cc)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Buchungskreis %q ist im Hauptbuch nicht eingerichtet (console ledger:setup-company)", cc)
	}
	r := res.Rows[0]
	c := &companyConfig{CompanyCode: cc, Ledger: crud.Str(r[0]), Chart: crud.Str(r[1]), Currency: crud.Str(r[2]), RateType: crud.Str(r[3])}
	if c.Mapping, err = parseMapping(crud.Str(r[4])); err != nil {
		return nil, err
	}
	return c, nil
}

// defaultMapping: Standard-Zuordnung der Kontierungsobjekte der drei Module.
// MANUAL (Vorerfassung) schreibt die Spalten direkt (Schlüssel = Spaltenname).
func defaultMapping() map[string]map[string]string {
	manual := map[string]string{}
	for _, c := range dimColumns {
		manual[c] = c
	}
	return map[string]map[string]string{
		"SD":          {"sales_order": "sd_sales_order_id", "sales_org": "sd_sales_org", "customer": "sd_customer_id", "cost_center": "cost_center", "profit_center": "profit_center"},
		"RENT":        {"object": "rent_object_id", "contract": "rent_contract_id", "building": "dimension_custom_1", "tenant": "sd_customer_id", "cost_center": "cost_center"},
		"PROCUREMENT": {"purchase_order": "purchase_order_id", "supplier": "supplier_id", "cost_center": "cost_center", "object": "rent_object_id"},
		moduleManual:  manual,
	}
}

var moduleKeyRe = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,19}$`)
var assignKeyRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// parseMapping prüft das JSON {"RENT": {"object": "rent_object_id", …}, …}:
// Modulnamen in Großbuchstaben, Zielspalten nur aus dimColumns. Leer = nil.
func parseMapping(s string) (map[string]map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var mp map[string]map[string]string
	if err := json.Unmarshal([]byte(s), &mp); err != nil {
		return nil, crud.Invalid("Modul-Mapping: JSON {\"MODUL\": {\"kontierung\": \"spalte\"}} erwartet (%v)", err)
	}
	for mod, fields := range mp {
		if !moduleKeyRe.MatchString(mod) {
			return nil, crud.Invalid("Modul-Mapping: Modul %q (Großbuchstaben, z. B. RENT)", mod)
		}
		targets := map[string]string{}
		for key, col := range fields {
			if !assignKeyRe.MatchString(key) {
				return nil, crud.Invalid("Modul-Mapping %s: Kontierung %q (Kleinbuchstaben, Ziffern, _)", mod, key)
			}
			if !slices.Contains(dimColumns, col) {
				return nil, crud.Invalid("Modul-Mapping %s.%s: Spalte %q gibt es nicht – erlaubt: %s", mod, key, col, strings.Join(dimColumns, ", "))
			}
			if other, dup := targets[col]; dup {
				return nil, crud.Invalid("Modul-Mapping %s: %s und %s schreiben beide in %s", mod, other, key, col)
			}
			targets[col] = key
		}
	}
	return mp, nil
}

// mappingFor: Mapping des Moduls – konfiguriert, sonst Standard.
func (c *companyConfig) mappingFor(module string) (map[string]string, error) {
	if module == moduleManual {
		return defaultMapping()[moduleManual], nil
	}
	if mp, ok := c.Mapping[module]; ok {
		return mp, nil
	}
	if mp, ok := defaultMapping()[module]; ok {
		return mp, nil
	}
	known := []string{}
	for k := range c.Mapping {
		known = append(known, k)
	}
	for k := range defaultMapping() {
		if !slices.Contains(known, k) {
			known = append(known, k)
		}
	}
	sort.Strings(known)
	return nil, crud.Invalid("Modul %s hat im Buchungskreis %s kein Mapping (bekannt: %s)", module, c.CompanyCode, strings.Join(known, ", "))
}

type masterAcc struct {
	Name, Type string
	Active     bool
}

func (m *Module) masterAccount(ctx context.Context, chart, account string) (*masterAcc, error) {
	res, err := m.db.Query(ctx, "SELECT name, account_type, is_active FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ?", chart, account)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Konto %s gibt es im Kontenplan %s nicht", account, chart)
	}
	a := &masterAcc{Name: crud.Str(res.Rows[0][0]), Type: crud.Str(res.Rows[0][1]), Active: crud.AsBool(res.Rows[0][2])}
	if !a.Active {
		return nil, crud.Invalid("Konto %s ist im Kontenplan %s inaktiv", account, chart)
	}
	return a, nil
}

func (m *Module) accountName(ctx context.Context, chart, account string) any {
	res, err := m.db.Query(ctx, "SELECT name FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ?", chart, account)
	if err != nil || len(res.Rows) == 0 {
		return nil
	}
	return res.Rows[0][0]
}

type draftHead struct {
	ID, CompanyCode, Status, Currency, PostingDate, DocumentDate, DocumentType, HeaderText, Reference string
	SpecialPeriod                                                                                     int
}

func (m *Module) draftHeader(ctx context.Context, id string) (*draftHead, error) {
	res, err := m.db.Query(ctx, `SELECT id, company_code_id, status, currency, posting_date, document_date, document_type, header_text, reference,
		special_period FROM ledger__draft_header WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Vorerfassung %s", sdk.ErrNotFound, id)
	}
	r := res.Rows[0]
	pd, _ := crud.ParseDate(r[4])
	dd := ""
	if r[5] != nil {
		dd, _ = crud.ParseDate(r[5])
	}
	return &draftHead{ID: crud.Str(r[0]), CompanyCode: crud.Str(r[1]), Status: crud.Str(r[2]), Currency: crud.Str(r[3]),
		PostingDate: pd, DocumentDate: dd, DocumentType: crud.Str(r[6]), HeaderText: crud.Str(r[7]), Reference: crud.Str(r[8]), SpecialPeriod: int(toInt(r[9]))}, nil
}

// editable: Positionen ändern nur in offenen Vorerfassungen, mit Recht JournalEntry.post.
func (d *draftHead) editable(ctx context.Context) error {
	if d.Status != draftOpen {
		return crud.Invalid("Vorerfassung ist %s und nicht mehr änderbar", strings.ToLower(statusLabel(d.Status)))
	}
	return requireCompanyCode(ctx, entryObject, "post", d.CompanyCode)
}

func (m *Module) companyCodeExists(ctx context.Context, cc string) error {
	if _, err := m.services.Call(ctx, "CompanyCode", "get", map[string]any{"id": cc}); err != nil {
		return crud.Invalid("Buchungskreis %q gibt es nicht (%v)", cc, err)
	}
	return nil
}

// requireCompanyCode: object.action im Buchungskreis cc (Account.Check in iam).
func requireCompanyCode(ctx context.Context, object, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
	}
	return nil
}

// readByCompany: Datensätze nur mit Leserecht <Object>.read im Buchungskreis
// (crud setzt es durch); object leer = das eigene Object.
func readByCompany(object string) *crud.Access {
	return &crud.Access{Object: object, Records: true, CompanyCode: "company_code_id"}
}

// requireRead: Leserecht <Object>.read im Buchungskreis.
func requireRead(ctx context.Context, object, cc string) error {
	ok, err := sdk.Authorize(ctx, object, metamodel.ActionRead, sdk.Attrs{sdk.AttrCompanyCode: cc})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s im Buchungskreis %s", sdk.ErrNotFound, object, cc)
	}
	return nil
}

// requireWrite: Ändern, Anlegen, Beenden … im Buchungskreis (object.action);
// Lesen prüft crud über Access.
func requireWrite(ctx context.Context, object, action, cc string) error {
	if action == "get" || action == "list" {
		return nil
	}
	return requireCompanyCode(ctx, object, action, cc)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// actionTexts übersetzen Standard-Actions abweichend vom Frontend-Text
// (Übersetzungsschlüssel des Moduls gehen vor core.action.*).
var actionTexts = map[string]string{
	"ledger.JournalDraft.actions.deactivate":         "Verwerfen",
	"ledger.JournalDraft.actions.deactivate.confirm": "Vorerfassung verwerfen? Sie bleibt als verworfen erhalten und kann nicht mehr gebucht werden.",
}

// healSourceModule: Einzelposten von vor 0.6.0 haben keine Herkunft. Beim
// ersten Lesen wird sie aus dem Belegkopf übernommen und für alle Positionen
// des Belegs nachgetragen (Darstellungsregeln je Position, z. B. RENT).
func (m *Module) healSourceModule(ctx context.Context, headerID string) any {
	res, err := m.db.Query(ctx, "SELECT source_module FROM ledger__journal_entry_header WHERE id = ?", headerID)
	if err != nil || len(res.Rows) == 0 || res.Rows[0][0] == nil {
		return nil
	}
	src := crud.Str(res.Rows[0][0])
	_, _ = m.db.Exec(ctx, "UPDATE ledger__journal_entry_item SET source_module = ? WHERE header_id = ? AND source_module IS NULL", src, headerID)
	return src
}
