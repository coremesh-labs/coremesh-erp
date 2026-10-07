package ledger

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Kontonummern (seit 0.9.0): Ein Sachkonto heißt überall <Kontenplan>-<Nummer>,
// z. B. SKR25-1200 – im Kontenplan, im Buchungskreis, in Belegen, Vorerfassung
// und Kontensperren. Eingaben ohne Präfix (Oberfläche, Fachmodule, Dateien)
// ergänzt accountKey um den Kontenplan; eine Nummer mit dem Präfix eines
// anderen Kontenplans wird abgelehnt.

// accountKey: vollständige Kontonummer im Kontenplan chart.
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
	return chart + "-" + v, nil
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

// shortAccount: Nummer ohne Kontenplan (z. B. für die Kontenklasse).
func shortAccount(chart, key string) string { return strings.TrimPrefix(key, chart+"-") }

// companyChart: Kontenplan eines Buchungskreises.
func (m *Module) companyChart(ctx context.Context, cc string) (string, error) {
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return "", err
	}
	return cfg.Chart, nil
}

// migrateAccounts stellt Kontonummern aus Versionen vor 0.9.0 einmalig auf
// <Kontenplan>-<Nummer> um: erst die neuen Konten anlegen, dann alle Verweise
// umstellen, dann die alten Konten entfernen (Fremdschlüssel bleiben gültig).
func (m *Module) migrateAccounts(ctx context.Context) error {
	notPrefixed := func(numberCol, chartExpr string) string {
		return " " + numberCol + " NOT LIKE " + chartExpr + " || '-%'"
	}
	cfgChart := func(ccCol string) string {
		return "(SELECT c.chart_of_accounts_id FROM ledger__company_config c WHERE c.company_code_id = " + ccCol + ")"
	}
	res, err := m.db.Query(ctx, "SELECT COUNT(*) FROM ledger__account_master WHERE"+notPrefixed("account_number", "chart_of_accounts_id"))
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][0]) == 0 {
		return nil
	}
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		steps := []string{
			`INSERT INTO ledger__account_master (chart_of_accounts_id, account_number, name, description, account_type, account_group, is_active)
			 SELECT chart_of_accounts_id, chart_of_accounts_id || '-' || account_number, name, description, account_type, account_group, is_active
			 FROM ledger__account_master WHERE` + notPrefixed("account_number", "chart_of_accounts_id"),
			`UPDATE ledger__account_company SET account_number = chart_of_accounts_id || '-' || account_number WHERE` + notPrefixed("account_number", "chart_of_accounts_id"),
			`UPDATE ledger__journal_entry_item SET account_number = chart_of_accounts_id || '-' || account_number WHERE` + notPrefixed("account_number", "chart_of_accounts_id"),
			`DELETE FROM ledger__account_master WHERE` + notPrefixed("account_number", "chart_of_accounts_id"),
			`UPDATE ledger__draft_item SET account_number = ` + cfgChart("(SELECT h.company_code_id FROM ledger__draft_header h WHERE h.id = draft_id)") + ` || '-' || account_number
			 WHERE account_number IS NOT NULL AND` + notPrefixed("account_number", cfgChart("(SELECT h.company_code_id FROM ledger__draft_header h WHERE h.id = draft_id)")),
			`UPDATE ledger__period_account_lock SET account_from = ` + cfgChart("company_code_id") + ` || '-' || account_from,
			 account_to = ` + cfgChart("company_code_id") + ` || '-' || account_to WHERE` + notPrefixed("account_from", cfgChart("company_code_id")),
		}
		for _, q := range steps {
			if _, err := m.db.Exec(ctx, q); err != nil {
				return err
			}
		}
		m.log.InfoContext(ctx, "Kontonummern auf <Kontenplan>-<Nummer> umgestellt", "accounts", toInt(res.Rows[0][0]))
		return nil
	})
}

// accountFilter: Listenfilter auf die Kontonummer – vollständig
// (SKR25-1200) oder ohne Kontenplan (1200).
func accountFilter(v any) (string, []any) {
	s := strings.ToUpper(strings.TrimSpace(crud.Str(v)))
	return "(account_number = ? OR account_number LIKE ?)", []any{s, "%-" + s}
}
