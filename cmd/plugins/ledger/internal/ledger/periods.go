package ledger

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
)

// Buchungsperioden (seit 0.8.0):
//
//   - Periodendefinition (PostingPeriod, ledger__posting_period): Perioden
//     01–16 ohne Geschäftsjahr – Bezeichnung, Sonderperiode, Kalendermonat.
//   - Offene Perioden (FiscalPeriod, ledger__open_period): eine Liste je
//     Buchungskreis, Ledger, Geschäftsjahr und Periode. Offen ist, was dort
//     steht; zum Jahreswechsel stehen einfach Perioden beider Jahre darin.
//     Schließen nimmt die Periode aus der Liste (is_open = false, Verlauf
//     bleibt), Öffnen legt eine neue Zeile an.
//
// Kontensperren je Periode (PeriodAccountLock) bleiben Ausnahmen dazu.

var refPostingPeriod = &crud.Ref{Table: "ledger__posting_period", Column: "period", Label: "Periode", Object: "PostingPeriod", LabelFields: []string{"name"}}

// postingPeriodEntity: Periodendefinition 01–16.
func (m *Module) postingPeriodEntity() *crud.Entity {
	return &crud.Entity{
		Object: "PostingPeriod", Title: "Periodendefinition", Icon: "icon-calendar", Table: "ledger__posting_period", Section: "Einstellungen",
		Keys: []string{"period"}, Order: "period", TitleField: "name",
		Fields: []crud.Field{
			{Key: "period", Label: "Periode", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "is_special", Label: "Sonderperiode", Type: tBool, Listable: true},
			{Key: "calendar_month", Label: "Kalendermonat", Type: tNum, Required: true, Listable: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			if p := toInt(rec["period"]); p < 1 || p > 16 {
				return crud.Invalid("Periode %d: 1–16", p)
			}
			if mo := toInt(rec["calendar_month"]); mo < 1 || mo > 12 {
				return crud.Invalid("Kalendermonat %d: 1–12", mo)
			}
			return nil
		},
	}
}

// fiscalPeriod: offene Buchungsperioden (Liste).
func (m *Module) fiscalPeriod() *crud.Entity {
	return &crud.Entity{
		Object: "FiscalPeriod", Title: "Offene Buchungsperioden", Icon: "icon-calendar", Table: "ledger__open_period", Section: "Einstellungen",
		Keys: []string{"id"}, Surrogate: true, CreateOnly: true,
		Order:   "company_code_id, ledger, fiscal_year DESC, posting_period",
		Filters: []string{"company_code_id", "ledger", "fiscal_year", "posting_period"},
		// Liste = offene Perioden; geschlossene nur mit „Inaktive anzeigen“.
		StatusField: "is_open", EndLabel: "Schließen", EndConfirm: "Periode schließen? Danach ist sie nicht mehr bebuchbar.",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "ledger", Label: "Ledger", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refLedger},
			{Key: "fiscal_year", Label: "Geschäftsjahr", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "posting_period", Label: "Periode", Type: tNum, Required: true, Listable: true, Immutable: true, Ref: refPostingPeriod},
			{Key: "period_to", Label: "bis Periode", Type: tNum, Virtual: true},
			{Key: "is_open", Label: "Offen", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "opened_at", Label: "Geöffnet am", Type: tText, ReadOnly: true, Listable: true},
			{Key: "opened_by", Label: "Geöffnet von", Type: tText, ReadOnly: true},
			{Key: "closed_at", Label: "Geschlossen am", Type: tText, ReadOnly: true},
			{Key: "closed_by", Label: "Geschlossen von", Type: tText, ReadOnly: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "openRange", Label: "Perioden öffnen …",
				Fields: []string{"company_code_id", "ledger", "fiscal_year", "posting_period", "period_to"}}, Handle: m.rangeAction("OPEN")},
			{ActionConfig: metamodel.ActionConfig{Name: "closeRange", Label: "Perioden schließen …",
				Fields: []string{"company_code_id", "ledger", "fiscal_year", "posting_period", "period_to"}}, Handle: m.rangeAction("CLOSED")},
		},
		Authorization: periodAuthorization,
		Access:        &crud.Access{Records: true, CompanyCode: "company_code_id", Fields: []string{"ledger", "fiscal_year", "posting_period"}},
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			return "", nil, false, m.migratePeriods(ctx)
		},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "FiscalPeriod", action, crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old != nil {
				return nil // Schlüssel sind fest; Schließen über deactivate
			}
			if y := toInt(rec["fiscal_year"]); y < 1900 || y > 2999 {
				return crud.Invalid("Geschäftsjahr %d ungültig", y)
			}
			open, err := m.periodOpen(ctx, crud.Str(rec["company_code_id"]), crud.Str(rec["ledger"]), int(toInt(rec["fiscal_year"])), int(toInt(rec["posting_period"])))
			if err != nil {
				return err
			}
			if open {
				return crud.Invalid("Periode %d/%d ist bereits offen", toInt(rec["posting_period"]), toInt(rec["fiscal_year"]))
			}
			rec["opened_at"], rec["opened_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
			return nil
		},
		OnEnd: func(ctx context.Context, _ string, _ crud.Record) (map[string]any, error) {
			return map[string]any{"closed_at": time.Now().UTC().Format(time.RFC3339), "closed_by": nilIfEmpty(sdk.CallFromContext(ctx).UserID)}, nil
		},
	}
}

// periodOpen: Steht die Periode in der Liste der offenen Perioden?
func (m *Module) periodOpen(ctx context.Context, cc, ledger string, year, period int) (bool, error) {
	if err := m.migratePeriods(ctx); err != nil {
		return false, err
	}
	res, err := m.db.Query(ctx, `SELECT 1 FROM ledger__open_period
		WHERE company_code_id = ? AND ledger = ? AND fiscal_year = ? AND posting_period = ? AND is_open = ?`, cc, ledger, year, period, true)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// postingPeriod: Definition einer Periode (nil = nicht definiert).
type periodDef struct {
	Period, Month int
	Name          string
	Special       bool
}

func (m *Module) periodDefs(ctx context.Context) ([]periodDef, error) {
	res, err := m.db.Query(ctx, `SELECT period, name, is_special, calendar_month FROM ledger__posting_period ORDER BY period`)
	if err != nil {
		return nil, err
	}
	out := make([]periodDef, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = periodDef{Period: int(toInt(r[0])), Name: crud.Str(r[1]), Special: crud.AsBool(r[2]), Month: int(toInt(r[3]))}
	}
	return out, nil
}

// periodFor bestimmt die Periode eines Buchungsdatums: ohne Angabe die normale
// Periode des Kalendermonats, mit Angabe eine Sonderperiode desselben Monats.
func (m *Module) periodFor(ctx context.Context, month time.Month, requested int) (int, error) {
	defs, err := m.periodDefs(ctx)
	if err != nil {
		return 0, err
	}
	for _, d := range defs {
		switch {
		case requested == 0 && !d.Special && d.Month == int(month):
			return d.Period, nil
		case requested != 0 && d.Period == requested:
			if !d.Special || d.Month != int(month) {
				return 0, crud.Invalid("Periode %d (%s) ist keine Sonderperiode für den Monat %d des Buchungsdatums", requested, d.Name, month)
			}
			return d.Period, nil
		}
	}
	if requested != 0 {
		return 0, crud.Invalid("Periode %d ist nicht definiert (Periodendefinition)", requested)
	}
	return 0, crud.Invalid("Für den Monat %d ist keine Periode definiert (Periodendefinition)", month)
}

// setPeriods öffnet bzw. schließt die Perioden from–to eines Jahres und
// liefert die Zahl der geänderten Perioden.
func (m *Module) setPeriods(ctx context.Context, cc, ledger string, year, from, to int, status string) (int, error) {
	if err := m.migratePeriods(ctx); err != nil {
		return 0, err
	}
	now, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	changed := 0
	for p := from; p <= to; p++ {
		open, err := m.periodOpen(ctx, cc, ledger, year, p)
		if err != nil {
			return 0, err
		}
		switch {
		case status == "OPEN" && !open:
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__open_period (id, company_code_id, ledger, fiscal_year, posting_period, is_open, opened_at, opened_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), cc, ledger, year, p, true, now, user); err != nil {
				return 0, fmt.Errorf("Periode %d/%d öffnen: %w", p, year, err)
			}
			changed++
		case status == "CLOSED" && open:
			if _, err := m.db.Exec(ctx, `UPDATE ledger__open_period SET is_open = ?, closed_at = ?, closed_by = ?
				WHERE company_code_id = ? AND ledger = ? AND fiscal_year = ? AND posting_period = ? AND is_open = ?`,
				false, now, user, cc, ledger, year, p, true); err != nil {
				return 0, err
			}
			changed++
		}
	}
	return changed, nil
}

// migratePeriods übernimmt einmalig die offenen Perioden aus
// ledger__fiscal_period_status (bis 0.7.0) – nur, solange die neue Liste noch
// nie benutzt wurde.
func (m *Module) migratePeriods(ctx context.Context) error {
	if m.periodsMigrated.Load() {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT COUNT(*) FROM ledger__open_period`)
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][0]) == 0 {
		old, err := m.db.Query(ctx, `SELECT company_code_id, ledger, fiscal_year, posting_period, changed_at, changed_by
			FROM ledger__fiscal_period_status WHERE status = 'OPEN'`)
		if err != nil {
			return err
		}
		for _, r := range old.Rows {
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__open_period (id, company_code_id, ledger, fiscal_year, posting_period, is_open, opened_at, opened_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), r[0], r[1], r[2], r[3], true, r[4], r[5]); err != nil {
				return err
			}
		}
		if len(old.Rows) > 0 {
			m.log.InfoContext(ctx, "Offene Perioden übernommen", "rows", strconv.Itoa(len(old.Rows)))
		}
	}
	m.periodsMigrated.Store(true)
	return nil
}

// rangeAction: Perioden von–bis eines Jahres öffnen bzw. schließen (Formular der Liste).
func (m *Module) rangeAction(status string) func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		data := dataOf(req.Payload)
		data["status"] = status
		return m.setPeriodsAction(ctx, sdk.Request{Object: req.Object, Action: req.Action, Payload: map[string]any{"data": data}})
	}
}
