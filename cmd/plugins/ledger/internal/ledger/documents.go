package ledger

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// Belegnummern (seit 0.13.0): Ein Beleg ist durch Buchungskreis, Geschäftsjahr
// und Belegnummer bestimmt (analog BKPF). Die Nummer kommt lückenlos aus dem
// Nummernkreis JournalEntry (Core-Plugin numrange) – in der Transaktion der
// Buchung, ein Rollback gibt sie zurück. Welches Intervall gilt, legt die
// Belegnummernvergabe fest: je Buchungskreis, Ledger und Belegart (* = alle
// übrigen). Die Intervalle eines Buchungskreises und Jahres überschneiden sich
// nicht, sodass Nummern über alle Ledger eindeutig bleiben.

const (
	numberObject    = "JournalEntry"
	numberingObject = "DocumentNumbering"
	allTypes        = "*"
	// Intervall je Ledger: n·10^9+1 … (n+1)·10^9−1 (n = 1 für das führende Ledger).
	rangeSize = int64(1_000_000_000)
)

var rangeKeyRe = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)

// docKey ist der Schlüssel eines Belegs.
type docKey struct {
	CompanyCode string
	FiscalYear  int
	Number      string
}

// id: ID des Belegs wie crud sie bildet (Buchungskreis|Jahr|Nummer).
func (k docKey) id() string {
	return strings.Join([]string{k.CompanyCode, strconv.Itoa(k.FiscalYear), k.Number}, "|")
}

// parseDocID liest eine Beleg-ID (Buchungskreis|Jahr|Nummer).
func parseDocID(id string) (docKey, error) {
	parts := strings.Split(id, "|")
	if len(parts) != 3 {
		return docKey{}, fmt.Errorf("%w: Beleg %q: Buchungskreis|Geschäftsjahr|Belegnummer erwartet", sdk.ErrInvalidArgument, id)
	}
	year, err := strconv.Atoi(parts[1])
	if err != nil {
		return docKey{}, fmt.Errorf("%w: Beleg %q: Geschäftsjahr", sdk.ErrInvalidArgument, id)
	}
	return docKey{CompanyCode: parts[0], FiscalYear: year, Number: parts[2]}, nil
}

// defineNumbering meldet den Nummernkreis an (beim Start).
func (m *Module) defineNumbering(ctx context.Context) error {
	return numrange.Define(ctx, m.services, numrange.Definition{Object: numberObject, Owner: Name,
		Description: "Belegnummern je Buchungskreis, Ledger und Geschäftsjahr (lückenlos)", PerCompanyCode: true, PerYear: true,
		Width: 10, From: rangeSize + 1, To: 2*rangeSize - 1, GapFree: true, Disjoint: true})
}

// nextNumber zieht die Belegnummer – in der laufenden Transaktion der Buchung.
func (m *Module) nextNumber(ctx context.Context, cc, ledger, docType string, year int, ref string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT range_key FROM ledger__document_numbering WHERE company_code_id = ? AND ledger = ?
		AND document_type IN (?, ?) ORDER BY CASE document_type WHEN ? THEN 1 ELSE 0 END`, cc, ledger, docType, allTypes, allTypes)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Belegnummernvergabe fehlt für Buchungskreis %s, Ledger %s (Einstellungen → Belegnummernvergabe; console ledger:setup-company)", cc, ledger)
	}
	nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: numberObject, CompanyCode: cc, Key: crud.Str(res.Rows[0][0]),
		Year: year, Reference: ref})
	if err != nil {
		return "", fmt.Errorf("Belegnummer: %w", err)
	}
	return nr.Number, nil
}

// ensureNumbering richtet die Belegnummernvergabe eines Buchungskreises ein:
// je aktivem Ledger ein Eintrag „alle Belegarten“ (Intervallschlüssel = Ledger)
// und für das Jahr je Ledger ein eigenes Intervall – das führende zuerst. Nur
// Fehlendes; liefert die Anzahl neuer Einträge und Intervalle.
func (m *Module) ensureNumbering(ctx context.Context, cc string, year int, current map[string]int64) (int, error) {
	res, err := m.db.Query(ctx, `SELECT id, is_leading FROM ledger__ledger WHERE is_active = ?`, true)
	if err != nil {
		return 0, err
	}
	type ledgerRow struct {
		id      string
		leading bool
	}
	var ledgers []ledgerRow
	for _, r := range res.Rows {
		ledgers = append(ledgers, ledgerRow{crud.Str(r[0]), crud.AsBool(r[1])})
	}
	sort.SliceStable(ledgers, func(i, j int) bool {
		if ledgers[i].leading != ledgers[j].leading {
			return ledgers[i].leading
		}
		return ledgers[i].id < ledgers[j].id
	})
	created := 0
	for n, l := range ledgers {
		key := strings.ToUpper(l.id)
		up, err := m.db.Exec(ctx, `INSERT INTO ledger__document_numbering (company_code_id, ledger, document_type, range_key, description)
			SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM ledger__document_numbering WHERE company_code_id = ? AND ledger = ? AND document_type = ?)`,
			cc, l.id, allTypes, key, "Belege des Ledgers "+l.id, cc, l.id, allTypes)
		if err != nil {
			return created, err
		}
		created += int(up.RowsAffected)
		from := int64(n+1)*rangeSize + 1
		_, err = m.services.Call(ctx, numrange.Object, "create", map[string]any{"data": map[string]any{
			"object": numberObject, "company_code": cc, "range_key": key, "year": year,
			"description":    fmt.Sprintf("Belege %s, Ledger %s, %d", cc, l.id, year),
			"from_number":    from, "to_number": from + rangeSize - 2, "current_number": current[l.id],
		}})
		switch {
		case err == nil:
			created++
		case errors.Is(err, sdk.ErrAlreadyExists):
		default:
			return created, fmt.Errorf("Nummernkreis %s/%s/%d: %w", cc, l.id, year, err)
		}
	}
	return created, nil
}

// documentNumbering: Belegnummernvergabe (Einstellungen).
func (m *Module) documentNumbering() *crud.Entity {
	return &crud.Entity{
		Object: numberingObject, Title: "Belegnummernvergabe", Icon: "icon-list", Table: "ledger__document_numbering", Section: "Einstellungen",
		Keys: []string{"company_code_id", "ledger", "document_type"}, Order: "company_code_id, ledger, document_type",
		Filters: []string{"company_code_id", "ledger"},
		Fields: []crud.Field{
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "ledger", Label: "Ledger", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refLedger},
			{Key: "document_type", Label: "Belegart (* = alle übrigen)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "range_key", Label: "Intervallschlüssel (Nummernkreis JournalEntry)", Type: tText, Required: true, Listable: true,
				Lookup: &metamodel.Lookup{Object: numrange.Object, ValueField: "range_key", LabelFields: []string{"description"},
					Filters: map[string]string{"object": "=" + numberObject, "company_code": "company_code_id"}}},
			{Key: "description", Label: "Beschreibung", Type: tText, Listable: true},
		},
		Access: readByCompany(""),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			if action == "get" || action == "list" {
				return nil
			}
			return requireCompanyCode(ctx, numberingObject, action, crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["document_type"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["document_type"])))
			rec["range_key"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["range_key"])))
			if !rangeKeyRe.MatchString(crud.Str(rec["range_key"])) {
				return crud.Invalid("Intervallschlüssel %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["range_key"]))
			}
			if dt := crud.Str(rec["document_type"]); dt != allTypes {
				res, err := m.db.Query(ctx, "SELECT 1 FROM ledger__document_type WHERE code = ?", dt)
				if err != nil {
					return err
				}
				if len(res.Rows) == 0 {
					return crud.Invalid("Belegart %q gibt es nicht (oder * für alle übrigen)", dt)
				}
			}
			return nil
		},
	}
}

// migrateDocuments (0.13.0): Belege mit GUID in die Tabellen mit fachlichem
// Schlüssel übernehmen, Nummernkreise nach numrange. Wiederholbar.
func (m *Module) migrateDocuments(ctx context.Context) error {
	// Nummernkreise: Stand je Buchungskreis und Jahr aus ledger__number_range.
	res, err := m.db.Query(ctx, `SELECT company_code_id, fiscal_year, last_number FROM ledger__number_range`)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		cc, year := crud.Str(r[0]), int(toInt(r[1]))
		cfg, err := m.config(ctx, cc)
		if err != nil {
			continue // Buchungskreis ohne Steuerung: nichts zu übernehmen
		}
		if _, err := m.ensureNumbering(ctx, cc, year, map[string]int64{cfg.Ledger: toInt(r[2])}); err != nil {
			return err
		}
	}
	if len(res.Rows) > 0 {
		if _, err := m.db.Exec(ctx, `DELETE FROM ledger__number_range`); err != nil {
			return err
		}
	}
	// Buchungskreise ohne Belegnummernvergabe (eingerichtet vor 0.13.0, noch ohne Beleg).
	cfgs, err := m.db.Query(ctx, `SELECT company_code_id FROM ledger__company_config c WHERE NOT EXISTS
		(SELECT 1 FROM ledger__document_numbering n WHERE n.company_code_id = c.company_code_id)`)
	if err != nil {
		return err
	}
	for _, r := range cfgs.Rows {
		if _, err := m.ensureNumbering(ctx, crud.Str(r[0]), time.Now().Year(), nil); err != nil {
			return err
		}
	}
	old, err := m.db.Query(ctx, `SELECT COUNT(*) FROM ledger__journal_entry_header`)
	if err != nil || toInt(old.Rows[0][0]) == 0 {
		return err
	}
	headerCols := `document_number, company_code_id, fiscal_year, posting_period, fiscal_year_period, document_type, document_date, posting_date,
		currency, local_currency, exchange_rate, reference, header_text, source_module, source_reference, reversal_flag, draft_id, created_by, created_at`
	itemCols := `company_code_id, fiscal_year, ledger, line_item_number, posting_period, posting_date, fiscal_year_period, account_kind, chart_of_accounts_id,
		account_number, shkzg, item_type, amount_document_curr, amount_local_curr, currency, local_currency, item_text, ` + strings.Join(dimColumns, ", ")
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, q := range []string{
			`INSERT INTO ledger__journal_header (` + headerCols + `, reversed_fiscal_year, reversed_document_number, reversal_fiscal_year, reversal_document_number)
			 SELECT ` + headerCols + `,
			   (SELECT o.fiscal_year FROM ledger__journal_entry_header o WHERE o.id = h.reversed_document_id),
			   (SELECT o.document_number FROM ledger__journal_entry_header o WHERE o.id = h.reversed_document_id),
			   (SELECT o.fiscal_year FROM ledger__journal_entry_header o WHERE o.id = h.reversal_document_id),
			   (SELECT o.document_number FROM ledger__journal_entry_header o WHERE o.id = h.reversal_document_id)
			 FROM ledger__journal_entry_header h`,
			`INSERT INTO ledger__journal_item (document_number, source_module, ` + itemCols + `)
			 SELECT h.document_number, COALESCE(i.source_module, h.source_module), ` + prefixed("i.", itemCols) + `
			 FROM ledger__journal_entry_item i JOIN ledger__journal_entry_header h ON h.id = i.header_id`,
			`UPDATE ledger__draft_header SET
			   posted_fiscal_year = (SELECT h.fiscal_year FROM ledger__journal_entry_header h WHERE h.id = ledger__draft_header.posted_document_id),
			   posted_document_number = (SELECT h.document_number FROM ledger__journal_entry_header h WHERE h.id = ledger__draft_header.posted_document_id),
			   posted_document_id = NULL
			 WHERE posted_document_id IS NOT NULL`,
			`DELETE FROM ledger__journal_entry_item`,
			`UPDATE ledger__journal_entry_header SET reversed_document_id = NULL, reversal_document_id = NULL`,
			`DELETE FROM ledger__journal_entry_header`,
		} {
			if _, err := m.db.Exec(ctx, q); err != nil {
				return fmt.Errorf("Übernahme der Belege: %w", err)
			}
		}
		m.log.InfoContext(ctx, "Belege übernommen (fachlicher Schlüssel)", "belege", toInt(old.Rows[0][0]))
		return nil
	})
}

// prefixed setzt vor jede Spalte einer Liste den Tabellenalias.
func prefixed(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for i, c := range parts {
		parts[i] = alias + strings.TrimSpace(c)
	}
	return strings.Join(parts, ", ")
}
