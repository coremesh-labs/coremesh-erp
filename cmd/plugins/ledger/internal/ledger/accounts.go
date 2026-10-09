package ledger

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Kontonummern (seit 0.15.0): Gespeichert wird überall nur die Nummer (1200) –
// im Kontenplan, im Buchungskreis, in Belegen, Vorerfassung, Kontensperren und
// in den Fachmodulen. Der Kontenplan ergibt sich aus dem Buchungskreis
// (Sachkonto im Buchungskreis = Buchungskreis + Nummer). Eingaben mit dem
// Präfix des eigenen Kontenplans (SKR25-1200, Altbestand 0.9.0–0.14.x) werden
// gekürzt; eine Nummer mit dem Präfix eines anderen Kontenplans wird abgelehnt.

// accountKey: Kontonummer (ohne Kontenplan) im Kontenplan chart.
func (m *Module) accountKey(ctx context.Context, chart, input string) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(input))
	if v == "" {
		return "", nil
	}
	if strings.HasPrefix(v, chart+"-") {
		v = strings.TrimPrefix(v, chart+"-")
	} else if other, ok := m.foreignChart(ctx, chart, v); ok {
		return "", crud.Invalid("Konto %s gehört zum Kontenplan %s, nicht zu %s", input, other, chart)
	}
	if !accountRe.MatchString(v) {
		return "", crud.Invalid("Kontonummer %q: Ziffern, Großbuchstaben, . _ - (höchstens 20 Zeichen, ohne Kontenplan)", input)
	}
	return v, nil
}

// foreignChart: Beginnt v mit dem Präfix eines anderen Kontenplans?
func (m *Module) foreignChart(ctx context.Context, chart, v string) (string, bool) {
	res, err := m.db.Query(ctx, "SELECT id FROM ledger__chart_of_accounts WHERE id <> ?", chart)
	if err != nil {
		return "", false
	}
	for _, r := range res.Rows {
		if id := crud.Str(r[0]); strings.HasPrefix(v, id+"-") {
			return id, true
		}
	}
	return "", false
}

// shortAccount: Nummer ohne Kontenplan (Altbestand mit Präfix wird gekürzt).
func shortAccount(chart, key string) string { return strings.TrimPrefix(key, chart+"-") }

// companyChart: Kontenplan eines Buchungskreises.
func (m *Module) companyChart(ctx context.Context, cc string) (string, error) {
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return "", err
	}
	return cfg.Chart, nil
}

// migrateAccounts stellt Kontonummern <Kontenplan>-<Nummer> (0.9.0–0.14.x)
// einmalig auf die reine Nummer um: erst die kurzen Konten anlegen, dann alle
// Verweise umstellen, dann die alten Konten entfernen (Fremdschlüssel bleiben
// gültig). Datenbestände vor 0.9.0 haben bereits reine Nummern.
func (m *Module) migrateAccounts(ctx context.Context) error {
	prefixed := func(numberCol, chartExpr string) string {
		return " " + numberCol + " LIKE " + chartExpr + " || '-%'"
	}
	strip := func(numberCol, chartExpr string) string {
		return "SUBSTR(" + numberCol + ", LENGTH(" + chartExpr + ") + 2)"
	}
	cfgChart := func(ccCol string) string {
		return "(SELECT c.chart_of_accounts_id FROM ledger__company_config c WHERE c.company_code_id = " + ccCol + ")"
	}
	draftChart := cfgChart("(SELECT h.company_code_id FROM ledger__draft_header h WHERE h.id = draft_id)")
	lockChart := cfgChart("company_code_id")
	// Einzelposten der Belege (ledger__journal_item, seit 0.13.0) – unabhängig
	// vom Kontenplan, denn 0.15.0 hat sie übersehen (0.17.0 holt das nach).
	if r, err := m.db.Exec(ctx, `UPDATE ledger__journal_item SET account_number = `+strip("account_number", "chart_of_accounts_id")+
		` WHERE`+prefixed("account_number", "chart_of_accounts_id")); err != nil {
		return err
	} else if r.RowsAffected > 0 {
		m.log.InfoContext(ctx, "Einzelposten ohne Kontenplan-Präfix", "items", r.RowsAffected)
	}
	res, err := m.db.Query(ctx, "SELECT COUNT(*) FROM ledger__account_master WHERE"+prefixed("account_number", "chart_of_accounts_id"))
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][0]) == 0 {
		return nil
	}
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		chart := "chart_of_accounts_id"
		steps := []string{
			`INSERT INTO ledger__account_master (chart_of_accounts_id, account_number, name, description, account_type, account_group, account_kind, is_active, parent_number, is_group)
			 SELECT chart_of_accounts_id, ` + strip("account_number", chart) + `, name, description, account_type, account_group, account_kind, is_active,
			 CASE WHEN` + prefixed("parent_number", chart) + ` THEN ` + strip("parent_number", chart) + ` ELSE parent_number END, is_group
			 FROM ledger__account_master a WHERE` + prefixed("account_number", chart) + `
			 AND NOT EXISTS (SELECT 1 FROM ledger__account_master b WHERE b.chart_of_accounts_id = a.chart_of_accounts_id
			 AND b.account_number = ` + strip("a.account_number", "a.chart_of_accounts_id") + `)`,
			`UPDATE ledger__account_company SET account_number = ` + strip("account_number", chart) + ` WHERE` + prefixed("account_number", chart),
			`UPDATE ledger__journal_entry_item SET account_number = ` + strip("account_number", chart) + ` WHERE` + prefixed("account_number", chart),
			`UPDATE ledger__account_master SET parent_number = ` + strip("parent_number", chart) + ` WHERE` + prefixed("parent_number", chart),
			`DELETE FROM ledger__account_master WHERE` + prefixed("account_number", chart),
			`UPDATE ledger__draft_item SET account_number = ` + strip("account_number", draftChart) + `
			 WHERE account_number IS NOT NULL AND` + prefixed("account_number", draftChart),
			`UPDATE ledger__period_account_lock SET account_from = ` + strip("account_from", lockChart) + ` WHERE` + prefixed("account_from", lockChart),
			`UPDATE ledger__period_account_lock SET account_to = ` + strip("account_to", lockChart) + ` WHERE` + prefixed("account_to", lockChart),
		}
		for _, q := range steps {
			if _, err := m.db.Exec(ctx, q); err != nil {
				return err
			}
		}
		m.log.InfoContext(ctx, "Kontonummern ohne Kontenplan-Präfix", "accounts", toInt(res.Rows[0][0]))
		return nil
	})
}

// accountFilter: Listenfilter auf die Kontonummer – ohne Kontenplan (1200)
// oder in der alten Schreibweise mit Kontenplan (SKR25-1200).
func accountFilter(v any) (string, []any) {
	s := strings.ToUpper(strings.TrimSpace(crud.Str(v)))
	short := s
	if prefix, rest, ok := strings.Cut(s, "-"); ok && rest != "" && strings.ContainsAny(prefix, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		short = rest
	}
	return "(account_number = ? OR account_number = ?)", []any{s, short}
}
