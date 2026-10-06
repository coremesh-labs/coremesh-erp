package accounting

import (
	"context"
	"regexp"
	"strings"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
)

const (
	tText = metamodel.TypeText
	tSel  = metamodel.TypeSelect
	tDate = metamodel.TypeDate
	tNum  = metamodel.TypeNumber
	tArea = metamodel.TypeTextarea

	typeAsset     = "ASSET"
	typeLiability = "LIABILITY"
	typeEquity    = "EQUITY"
	typeIncome    = "INCOME"
	typeExpense   = "EXPENSE"

	statusActive = "ACTIVE"
	statusLocked = "LOCKED"

	sideDebit  = "D" // Soll
	sideCredit = "C" // Haben

	// entryObject ist das Object der Buchungsbelege (Rechte je Buchungskreis).
	entryObject = "JournalEntry"
)

var (
	accountCodeRe = regexp.MustCompile(`^[0-9A-Z][0-9A-Z._-]{0,19}$`)

	accountTypes = []metamodel.Option{
		{Value: typeAsset, Label: "Aktiven"},
		{Value: typeLiability, Label: "Passiven (Fremdkapital)"},
		{Value: typeEquity, Label: "Eigenkapital"},
		{Value: typeIncome, Label: "Ertrag"},
		{Value: typeExpense, Label: "Aufwand"},
	}
	accountStatuses = []metamodel.Option{{Value: statusActive, Label: "Aktiv"}, {Value: statusLocked, Label: "Gesperrt"}}
	sides           = []metamodel.Option{{Value: sideDebit, Label: "Soll"}, {Value: sideCredit, Label: "Haben"}}

	refAccount    = &crud.Ref{Table: "ledger__accounts", Column: "code", Label: "Konto", Object: "GLAccount", LabelFields: []string{"name"}}
	lookupAccount = &metamodel.Lookup{Object: "GLAccount", ValueField: "code", LabelFields: []string{"name"}}
	lookupCC      = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupEntry   = &metamodel.Lookup{Object: entryObject, ValueField: "id", LabelFields: []string{"document_no"}}
)

func (m *Module) entities() []*crud.Entity {
	return []*crud.Entity{m.account(), m.journalEntry(), m.journalLine()}
}

// Account: Sachkonto des Kontenplans. Kein Löschen: Konten mit Buchungen
// werden gesperrt (LOCKED) und nehmen keine neuen Buchungen mehr an.
func (m *Module) account() *crud.Entity {
	return &crud.Entity{
		Object: "GLAccount", Title: "Kontenplan", Icon: "icon-list", Table: "ledger__accounts", Section: "Stammdaten",
		Keys: []string{"code"}, Order: "code", Search: []string{"code", "name"}, Filters: []string{"account_type", "status"},
		StatusField: "status", StatusActive: statusActive, StatusInactive: statusLocked,
		TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Kontonummer", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "account_type", Label: "Kontoart", Type: tSel, Required: true, Listable: true, Immutable: true, Options: accountTypes},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: accountStatuses},
			{Key: "description", Label: "Beschreibung", Type: tArea},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if !accountCodeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Kontonummer %q: Ziffern, Großbuchstaben, . _ - (1–20 Zeichen)", crud.Str(rec["code"]))
				}
				rec["status"] = statusActive
			}
			return nil
		},
	}
}

// JournalEntry: Buchungsbeleg (Kopf). Schreibgeschützt – Belege entstehen nur
// über post (Soll = Haben wird mit allen Positionen geprüft) und reverse.
func (m *Module) journalEntry() *crud.Entity {
	return &crud.Entity{
		Object: entryObject, Title: "Buchungen", Icon: "icon-book", Table: "ledger__journal_entries", Section: "Buchhaltung",
		Keys: []string{"id"}, Surrogate: true, ReadOnly: true,
		Order: "posting_date DESC, document_no DESC", Search: []string{"document_no", "text", "reference"},
		Filters:    []string{"company_code", "currency", "reversal_of"},
		TitleField: "document_no",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "document_no", Label: "Belegnummer", Type: tText, Listable: true, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Lookup: lookupCC},
			{Key: "posting_date", Label: "Buchungsdatum", Type: tDate, Required: true, Listable: true},
			{Key: "document_date", Label: "Belegdatum", Type: tDate},
			{Key: "reference", Label: "Referenz (z. B. Rechnungsnummer)", Type: tText},
			{Key: "text", Label: "Buchungstext", Type: tText, Required: true, Listable: true},
			{Key: "currency", Label: "Währung", Type: tText, Required: true, Listable: true},
			// Eingabe einer einfachen Buchung „Soll an Haben“ (Formular der Action post).
			{Key: "debit_account", Label: "Soll-Konto", Type: tText, Virtual: true, Lookup: lookupAccount},
			{Key: "credit_account", Label: "Haben-Konto", Type: tText, Virtual: true, Lookup: lookupAccount},
			{Key: "amount", Label: "Betrag", Type: tText, Virtual: true},
			{Key: "total", Label: "Betrag", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "reversal_of", Label: "Storno von", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "reversed_by", Label: "Storniert durch", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "posted_at", Label: "Gebucht am", Type: tText, ReadOnly: true},
			{Key: "posted_by", Label: "Gebucht von", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "beleg", Title: "Beleg", Fields: []string{"document_no", "company_code", "posting_date", "document_date", "reference", "text", "currency", "total"}},
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: "JournalLine", ForeignKey: "entry_id",
				Columns: []string{"line_no", "account_code", "debit", "credit", "text"}}},
			{Key: "protokoll", Title: "Protokoll", Collapsed: true, Fields: []string{"reversal_of", "reversed_by", "posted_at", "posted_by", "id"}},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …",
				Fields: []string{"company_code", "posting_date", "document_date", "debit_account", "credit_account", "amount", "currency", "text", "reference"}},
				Handle: m.post},
			{ActionConfig: metamodel.ActionConfig{Name: "reverse", Label: "Stornieren …", Record: true,
				Fields: []string{"posting_date", "text"}}, Handle: m.reverse},
		},
		ListScope: companyScope,
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireCompanyCode(ctx, "get", crud.Str(rec["company_code"]))
		},
		Decorate: m.decorateEntry,
	}
}

// JournalLine: Position eines Belegs (Soll oder Haben). Schreibgeschützt wie der Beleg.
func (m *Module) journalLine() *crud.Entity {
	return &crud.Entity{
		Object: "JournalLine", Title: "Buchungspositionen", Icon: "icon-list", Table: "ledger__journal_lines", Section: "Buchhaltung",
		Keys: []string{"id"}, Surrogate: true, ReadOnly: true,
		Order: "entry_id, line_no", Filters: []string{"entry_id", "account_code", "side"},
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "entry_id", Label: "Beleg", Type: tText, ReadOnly: true, Lookup: lookupEntry},
			{Key: "line_no", Label: "Pos.", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "account_code", Label: "Konto", Type: tText, ReadOnly: true, Listable: true, Ref: refAccount},
			{Key: "side", Label: "Seite", Type: tSel, ReadOnly: true, Options: sides},
			{Key: "amount_minor", Label: "Betrag (Rappen)", Type: tNum, ReadOnly: true},
			{Key: "debit", Label: "Soll", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "credit", Label: "Haben", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "text", Label: "Text", Type: tText, ReadOnly: true, Listable: true},
		},
		// Positionen sind nur sichtbar, wenn der Beleg sichtbar ist.
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			where, args, none, err := companyScope(ctx)
			if where != "" {
				where = "entry_id IN (SELECT id FROM ledger__journal_entries WHERE " + where + ")"
			}
			return where, args, none, err
		},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			res, err := m.db.Query(ctx, "SELECT company_code FROM ledger__journal_entries WHERE id = ?", rec["entry_id"])
			if err != nil || len(res.Rows) == 0 {
				return err
			}
			return requireCompanyCode(ctx, "get", crud.Str(res.Rows[0][0]))
		},
		Decorate: func(_ context.Context, rec crud.Record) error {
			amount := formatAmount(toInt(rec["amount_minor"]))
			if crud.Str(rec["side"]) == sideDebit {
				rec["debit"] = amount
			} else {
				rec["credit"] = amount
			}
			return nil
		},
	}
}

// decorateEntry: Belegsumme (Summe der Soll-Seite) für Liste und Anzeige.
func (m *Module) decorateEntry(ctx context.Context, rec crud.Record) error {
	res, err := m.db.Query(ctx, "SELECT COALESCE(SUM(amount_minor), 0) FROM ledger__journal_lines WHERE entry_id = ? AND side = ?", rec["id"], sideDebit)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		rec["total"] = formatAmount(toInt(res.Rows[0][0])) + " " + crud.Str(rec["currency"])
	}
	return nil
}

// companyScope: nur Belege der Buchungskreise, für die der Benutzer
// JournalEntry.list hat.
func companyScope(ctx context.Context) (string, []any, bool, error) {
	g, err := sdk.GrantedCompanyCodes(ctx, entryObject, "list")
	if err != nil || g.All {
		return "", nil, false, err
	}
	if g.None() {
		return "", nil, true, nil
	}
	marks := make([]string, len(g.CompanyCodes))
	args := make([]any, len(g.CompanyCodes))
	for i, cc := range g.CompanyCodes {
		marks[i], args[i] = "?", cc
	}
	return "company_code IN (" + strings.Join(marks, ", ") + ")", args, false, nil
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
