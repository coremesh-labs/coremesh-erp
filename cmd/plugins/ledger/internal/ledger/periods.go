package ledger

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-lab/coremesh/pkg/sdk"
	"github.com/coremesh-lab/coremesh/pkg/sdk/crud"
	"github.com/coremesh-lab/coremesh/pkg/sdk/metamodel"
)

// Buchungsperioden:
//
//   - Periodendefinition (PostingPeriod, ledger__period_definition) je
//     Buchungskreis: Perioden 01–16 ohne Geschäftsjahr – Bezeichnung,
//     Sonderperiode, Kalendermonat. Neue Buchungskreise erhalten die Vorlage
//     ledger__posting_period.
//   - Offene Perioden (FiscalPeriod, ledger__open_period): eine Liste je
//     Buchungskreis, Ledger, Kontoart, Geschäftsjahr und Periode. Offen ist,
//     was dort steht; zum Jahreswechsel stehen einfach Perioden beider Jahre
//     darin. Schließen nimmt die Periode aus der Liste (is_open = false,
//     Verlauf bleibt), Öffnen legt eine neue Zeile an.
//   - Kontoart (analog OB52): "+" ist der Hauptschalter und muss immer offen
//     sein. Kontoarten mit eigener Periodensteuerung (AccountType) brauchen
//     zusätzlich eine eigene offene Periode – z. B. D und K zum Monatsende
//     schließen, S für Abschlussbuchungen offen lassen.
//
// Kontensperren je Periode (PeriodAccountLock) bleiben Ausnahmen dazu.

const allKinds = "+"

// accountTypeEntity: Kontoarten (A, D, K, M, S, V).
func (m *Module) accountTypeEntity() *crud.Entity {
	return &crud.Entity{
		Object: "AccountType", Title: "Kontoarten", Icon: "icon-tag", Table: "ledger__account_type", Section: "Einstellungen",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Kontoart", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "own_period_control", Label: "Eigene Periodensteuerung", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if c := crud.Str(rec["code"]); len(c) != 1 || c < "A" || c > "Z" {
					return crud.Invalid("Kontoart: ein Buchstabe, z. B. S (\"+\" steht in den Perioden für alle)")
				}
			}
			return nil
		},
	}
}

// itemKinds: Kontoart je Positionsart (Steuern sind Sachkontenpositionen).
var itemKinds = map[string]string{itemGL: "S", itemTax: "S", itemCustomer: "D", itemSupplier: "K", itemAsset: "A"}

// kindOf: Kontoart einer Positionsart.
func kindOf(itemType string) string {
	if k, ok := itemKinds[itemType]; ok {
		return k
	}
	return "S"
}

// postingPeriodEntity: Periodendefinition 01–16 je Buchungskreis.
func (m *Module) postingPeriodEntity() *crud.Entity {
	return &crud.Entity{
		Object: "PostingPeriod", Title: "Periodendefinition", Icon: "icon-calendar", Table: "ledger__period_definition", Section: "Einstellungen",
		Keys: []string{"company_code_id", "period"}, Order: "company_code_id, period", TitleField: "name", Filters: []string{"company_code_id"},
		Fields: []crud.Field{
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "period", Label: "Periode", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "is_special", Label: "Sonderperiode", Type: tBool, Listable: true},
			{Key: "calendar_month", Label: "Kalendermonat", Type: tNum, Required: true, Listable: true},
		},
		Access: readByCompany(""),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "PostingPeriod", action, crud.Str(rec["company_code_id"]))
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
	rangeFields := []string{"company_code_id", "ledger", "account_kind", "fiscal_year", "posting_period", "period_to"}
	return &crud.Entity{
		Object: "FiscalPeriod", Title: "Offene Buchungsperioden", Icon: "icon-calendar", Table: "ledger__open_period", Section: "Einstellungen",
		Keys: []string{"id"}, Surrogate: true, CreateOnly: true,
		Order:   "company_code_id, ledger, account_kind, fiscal_year DESC, posting_period",
		Filters: []string{"company_code_id", "ledger", "account_kind", "fiscal_year", "posting_period"},
		// Liste = offene Perioden; geschlossene nur mit „Inaktive anzeigen“.
		StatusField: "is_open", EndLabel: "Schließen", EndConfirm: "Periode schließen? Danach ist sie nicht mehr bebuchbar.",
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "ledger", Label: "Ledger", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refLedger},
			{Key: "account_kind", Label: "Kontoart (+ = alle)", Type: tText, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "AccountType", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "fiscal_year", Label: "Geschäftsjahr", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "posting_period", Label: "Periode", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "period_to", Label: "bis Periode", Type: tNum, Virtual: true},
			{Key: "is_open", Label: "Offen", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "opened_at", Label: "Geöffnet am", Type: tText, ReadOnly: true, Listable: true},
			{Key: "opened_by", Label: "Geöffnet von", Type: tText, ReadOnly: true},
			{Key: "closed_at", Label: "Geschlossen am", Type: tText, ReadOnly: true},
			{Key: "closed_by", Label: "Geschlossen von", Type: tText, ReadOnly: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "openRange", Label: "Perioden öffnen …", Fields: rangeFields}, Handle: m.rangeAction("OPEN")},
			{ActionConfig: metamodel.ActionConfig{Name: "closeRange", Label: "Perioden schließen …", Fields: rangeFields}, Handle: m.rangeAction("CLOSED")},
		},
		Authorization: periodAuthorization,
		Access:        &crud.Access{Records: true, CompanyCode: "company_code_id", Fields: []string{"ledger", "account_kind", "fiscal_year", "posting_period"}},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "FiscalPeriod", action, crud.Str(rec["company_code_id"]))
		},
		Decorate: func(_ context.Context, rec crud.Record) error {
			if crud.Str(rec["account_kind"]) == allKinds {
				labels, _ := rec["_labels"].(map[string]any)
				if labels == nil {
					labels = map[string]any{}
					rec["_labels"] = labels
				}
				labels["account_kind"] = "+ (alle Kontoarten)"
			}
			return nil
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old != nil {
				return nil // Schlüssel sind fest; Schließen über deactivate
			}
			cc, ledger := crud.Str(rec["company_code_id"]), crud.Str(rec["ledger"])
			year, period := int(toInt(rec["fiscal_year"])), int(toInt(rec["posting_period"]))
			kind, err := m.checkKind(ctx, crud.Str(rec["account_kind"]))
			if err != nil {
				return err
			}
			rec["account_kind"] = kind
			if year < 1900 || year > 2999 {
				return crud.Invalid("Geschäftsjahr %d ungültig", year)
			}
			if _, ok, err := m.periodDef(ctx, cc, period); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Periode %d ist im Buchungskreis %s nicht definiert (Periodendefinition)", period, cc)
			}
			open, err := m.listedOpen(ctx, cc, ledger, kind, year, period)
			if err != nil {
				return err
			}
			if open {
				return crud.Invalid("Periode %d/%d (Kontoart %s) ist bereits offen", period, year, kind)
			}
			rec["opened_at"], rec["opened_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
			return nil
		},
		OnEnd: func(ctx context.Context, _ string, _ crud.Record) (map[string]any, error) {
			return map[string]any{"closed_at": time.Now().UTC().Format(time.RFC3339), "closed_by": nilIfEmpty(sdk.CallFromContext(ctx).UserID)}, nil
		},
	}
}

// checkKind: "+" (Standard) oder eine aktive Kontoart.
func (m *Module) checkKind(ctx context.Context, kind string) (string, error) {
	kind = strings.ToUpper(strings.TrimSpace(kind))
	if kind == "" || kind == allKinds {
		return allKinds, nil
	}
	res, err := m.db.Query(ctx, "SELECT 1 FROM ledger__account_type WHERE code = ? AND is_active = ?", kind, true)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Kontoart %q gibt es nicht (oder inaktiv) – + für alle", kind)
	}
	return kind, nil
}

// listedOpen: Steht die Periode für die Kontoart (oder +) offen in der Liste?
func (m *Module) listedOpen(ctx context.Context, cc, ledger, kind string, year, period int) (bool, error) {
	if err := m.migrate(ctx); err != nil {
		return false, err
	}
	res, err := m.db.Query(ctx, `SELECT 1 FROM ledger__open_period WHERE company_code_id = ? AND ledger = ? AND account_kind = ?
		AND fiscal_year = ? AND posting_period = ? AND is_open = ?`, cc, ledger, kind, year, period, true)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// periodOpen: Ist die Periode für eine Kontoart buchbar? "+" muss offen sein;
// Kontoarten mit eigener Periodensteuerung zusätzlich für sich selbst.
func (m *Module) periodOpen(ctx context.Context, cc, ledger, kind string, year, period int) (bool, error) {
	open, err := m.listedOpen(ctx, cc, ledger, allKinds, year, period)
	if err != nil || !open || kind == "" || kind == allKinds {
		return open, err
	}
	res, err := m.db.Query(ctx, "SELECT own_period_control FROM ledger__account_type WHERE code = ?", kind)
	if err != nil {
		return false, err
	}
	if len(res.Rows) == 0 || !crud.AsBool(res.Rows[0][0]) {
		return true, nil
	}
	return m.listedOpen(ctx, cc, ledger, kind, year, period)
}

// periodDef: Definition einer Periode im Buchungskreis.
type periodDef struct {
	Period, Month int
	Name          string
	Special       bool
}

func (m *Module) periodDefs(ctx context.Context, cc string) ([]periodDef, error) {
	if err := m.migrate(ctx); err != nil {
		return nil, err
	}
	res, err := m.db.Query(ctx, `SELECT period, name, is_special, calendar_month FROM ledger__period_definition WHERE company_code_id = ? ORDER BY period`, cc)
	if err != nil {
		return nil, err
	}
	out := make([]periodDef, len(res.Rows))
	for i, r := range res.Rows {
		out[i] = periodDef{Period: int(toInt(r[0])), Name: crud.Str(r[1]), Special: crud.AsBool(r[2]), Month: int(toInt(r[3]))}
	}
	return out, nil
}

func (m *Module) periodDef(ctx context.Context, cc string, period int) (periodDef, bool, error) {
	defs, err := m.periodDefs(ctx, cc)
	if err != nil {
		return periodDef{}, false, err
	}
	for _, d := range defs {
		if d.Period == period {
			return d, true, nil
		}
	}
	return periodDef{}, false, nil
}

// definePeriods legt die Periodendefinition eines Buchungskreises aus der
// Vorlage an (wenn er noch keine hat).
func (m *Module) definePeriods(ctx context.Context, cc string) error {
	_, err := m.db.Exec(ctx, `INSERT INTO ledger__period_definition (company_code_id, period, name, is_special, calendar_month)
		SELECT ?, period, name, is_special, calendar_month FROM ledger__posting_period
		WHERE NOT EXISTS (SELECT 1 FROM ledger__period_definition WHERE company_code_id = ?)`, cc, cc)
	return err
}

// periodFor bestimmt die Periode eines Buchungsdatums im Buchungskreis: ohne
// Angabe die normale Periode des Kalendermonats, mit Angabe eine
// Sonderperiode desselben Monats.
func (m *Module) periodFor(ctx context.Context, cc string, month time.Month, requested int) (int, error) {
	defs, err := m.periodDefs(ctx, cc)
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
		return 0, crud.Invalid("Periode %d ist im Buchungskreis %s nicht definiert (Periodendefinition)", requested, cc)
	}
	return 0, crud.Invalid("Für den Monat %d ist im Buchungskreis %s keine Periode definiert (Periodendefinition)", month, cc)
}

// setPeriods öffnet bzw. schließt die Perioden from–to eines Jahres für eine
// Kontoart (+ = alle) und liefert die Zahl der geänderten Perioden.
func (m *Module) setPeriods(ctx context.Context, cc, ledger, kind string, year, from, to int, status string) (int, error) {
	kind, err := m.checkKind(ctx, kind)
	if err != nil {
		return 0, err
	}
	if err := m.definePeriods(ctx, cc); err != nil {
		return 0, err
	}
	now, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	changed := 0
	for p := from; p <= to; p++ {
		if _, ok, err := m.periodDef(ctx, cc, p); err != nil {
			return 0, err
		} else if !ok {
			continue // nicht definiert: nichts zu öffnen oder zu schließen
		}
		open, err := m.listedOpen(ctx, cc, ledger, kind, year, p)
		if err != nil {
			return 0, err
		}
		switch {
		case status == "OPEN" && !open:
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__open_period (id, company_code_id, ledger, account_kind, fiscal_year, posting_period, is_open, opened_at, opened_by)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), cc, ledger, kind, year, p, true, now, user); err != nil {
				return 0, fmt.Errorf("Periode %d/%d öffnen: %w", p, year, err)
			}
			changed++
		case status == "CLOSED" && open:
			if _, err := m.db.Exec(ctx, `UPDATE ledger__open_period SET is_open = ?, closed_at = ?, closed_by = ?
				WHERE company_code_id = ? AND ledger = ? AND account_kind = ? AND fiscal_year = ? AND posting_period = ? AND is_open = ?`,
				false, now, user, cc, ledger, kind, year, p, true); err != nil {
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
	res, err := m.db.Query(ctx, `SELECT COUNT(*) FROM ledger__open_period`)
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][0]) > 0 {
		return nil
	}
	old, err := m.db.Query(ctx, `SELECT company_code_id, ledger, fiscal_year, posting_period, changed_at, changed_by
		FROM ledger__fiscal_period_status WHERE status = 'OPEN'`)
	if err != nil {
		return err
	}
	for _, r := range old.Rows {
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__open_period (id, company_code_id, ledger, account_kind, fiscal_year, posting_period, is_open, opened_at, opened_by)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), r[0], r[1], allKinds, r[2], r[3], true, r[4], r[5]); err != nil {
			return err
		}
	}
	if len(old.Rows) > 0 {
		m.log.InfoContext(ctx, "Offene Perioden übernommen", "rows", strconv.Itoa(len(old.Rows)))
	}
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
