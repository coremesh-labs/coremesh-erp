package ledger

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

// Kontenplanwechsel eines Buchungskreises (seit 0.17.0):
//
//	console ledger:change-chart --company=2000 --chart=SKR04 --mapping=./skr0vv-skr04.csv [--file=skr04.json] [--dry-run]
//
// Nur solange der Buchungskreis keine Belege und keine offene Vorerfassung hat.
// Die Zuordnung alte → neue Kontonummer (JSON-Objekt {"600": "4861"} oder CSV
// mit Spalten from;to) gilt für alle Fachmodule: Sie prüfen über den Hook
// ledger.chart_change (check), ob jedes bei ihnen gespeicherte Konto zugeordnet
// ist, und stellen danach um (commit). Das Hauptbuch tauscht Kontenplan und
// Sachkonten des Buchungskreises (Vorschläge wie setup-company aus --file).

var chartChangeHookDef = hook.Definition{Name: ledgerapi.HookChartChange, Owner: Name, Phases: []string{hook.PhaseCheck, hook.PhaseCommit},
	OnFailure:   hook.FailBlock,
	Description: "Kontenplanwechsel eines Buchungskreises: check vor dem Wechsel (Meldung E verhindert ihn), commit stellt gespeicherte Konten um",
	Data:        "ledgerapi.ChartChange: company_code, from_chart, to_chart, mapping {alt: neu}"}

func (m *Module) changeChartAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc := param(req.Payload, "company", "company_code_id")
	chart := strings.ToUpper(param(req.Payload, "chart", "chart_of_accounts_id"))
	dry := strings.EqualFold(param(req.Payload, "dry_run", "dry-run"), "true")
	if cc == "" || chart == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) und neuer Kontenplan (chart) sind Pflicht")
	}
	if err := requireCompanyCode(ctx, "LedgerCompanyConfig", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	if cfg.Chart == chart {
		return sdk.Response{}, crud.Invalid("Buchungskreis %s arbeitet bereits mit Kontenplan %s", cc, chart)
	}
	var mappingIn any
	if pl, _ := req.Payload.(map[string]any); pl != nil {
		mappingIn = pl["mapping"]
	}
	mapping, err := parseChartMapping(mappingIn)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := m.checkChartChange(ctx, cc, chart, mapping); err != nil {
		return sdk.Response{}, err
	}
	data := ledgerapi.ChartChange{CompanyCode: cc, FromChart: cfg.Chart, ToChart: chart, Mapping: mapping}
	res, err := hook.Call(ctx, m.services, ledgerapi.HookChartChange, hook.PhaseCheck, data)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.HasErrors() {
		return sdk.Response{}, crud.Invalid("Kontenplanwechsel nicht möglich: %s", messageTexts(res.Messages, hook.TypeError))
	}
	if dry {
		return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Prüfung erfolgreich: Buchungskreis %s kann von %s auf %s wechseln (%d Zuordnungen)",
			cc, cfg.Chart, chart, len(mapping)), "mapping": len(mapping)}}, nil
	}
	hints, err := accountHints(chart, req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	var assigned, removed int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		r, err := m.db.Exec(ctx, "DELETE FROM ledger__account_company WHERE company_code_id = ?", cc)
		if err != nil {
			return err
		}
		removed = int(r.RowsAffected)
		if _, err := m.db.Exec(ctx, "UPDATE ledger__company_config SET chart_of_accounts_id = ? WHERE company_code_id = ?", chart, cc); err != nil {
			return err
		}
		assigned, err = m.assignAccounts(ctx, cc, chart, cfg.Currency, hints)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.log.InfoContext(ctx, "Kontenplan gewechselt", "company_code", cc, "from", cfg.Chart, "to", chart, "accounts", assigned)
	commit, err := hook.Call(ctx, m.services, ledgerapi.HookChartChange, hook.PhaseCommit, data)
	msg := fmt.Sprintf("Buchungskreis %s: Kontenplan %s → %s, %d Sachkonten zugeordnet (%d entfernt)", cc, cfg.Chart, chart, assigned, removed)
	if err != nil {
		msg += " – Fachmodule nicht umgestellt: " + err.Error()
	} else if t := messageTexts(commit.Messages, ""); t != "" {
		msg += " – " + t
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "assigned": assigned, "removed": removed}}, nil
}

// checkChartChange: keine Belege, keine offene Vorerfassung, keine
// Kontensperren; Ziele der Zuordnung sind bebuchbare, aktive Konten des neuen Kontenplans.
func (m *Module) checkChartChange(ctx context.Context, cc, chart string, mapping map[string]string) error {
	for _, q := range []struct{ sql, what string }{
		{"SELECT COUNT(*) FROM ledger__journal_header WHERE company_code_id = ?", "Belege"},
		{"SELECT COUNT(*) FROM ledger__draft_header WHERE company_code_id = ? AND status = '" + draftOpen + "'", "offene Vorerfassungen"},
		{"SELECT COUNT(*) FROM ledger__period_account_lock WHERE company_code_id = ?", "Kontensperren"},
	} {
		res, err := m.db.Query(ctx, q.sql, cc)
		if err != nil {
			return err
		}
		if n := toInt(res.Rows[0][0]); n > 0 {
			return crud.Invalid("Buchungskreis %s hat %d %s – der Kontenplan kann nur ohne sie gewechselt werden", cc, n, q.what)
		}
	}
	if len(mapping) == 0 {
		return crud.Invalid("Zuordnung alte → neue Kontonummer (mapping) ist Pflicht")
	}
	var bad []string
	for from, to := range mapping {
		res, err := m.db.Query(ctx, "SELECT is_group, is_active FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ?", chart, to)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 || crud.AsBool(res.Rows[0][0]) || !crud.AsBool(res.Rows[0][1]) {
			bad = append(bad, from+" → "+to)
		}
	}
	if len(bad) > 0 {
		slices.Sort(bad)
		return crud.Invalid("Zuordnung: im Kontenplan %s kein aktives, bebuchbares Konto für %s", chart, strings.Join(bad, ", "))
	}
	return nil
}

// parseChartMapping: {"600": "4861"} oder Zeilen [{from, to}] (CSV aus der Konsole).
func parseChartMapping(v any) (map[string]string, error) {
	norm := func(s any) string { return strings.ToUpper(strings.TrimSpace(fmt.Sprint(s))) }
	out := map[string]string{}
	add := func(from, to string) error {
		if from == "" || to == "" {
			return nil
		}
		if old, ok := out[from]; ok && old != to {
			return crud.Invalid("Zuordnung: %s doppelt (%s und %s)", from, old, to)
		}
		out[from] = to
		return nil
	}
	switch x := v.(type) {
	case nil:
	case map[string]any:
		for k, val := range x {
			if strings.HasPrefix(k, "_") { // Kommentare wie "_hinweis"
				continue
			}
			if err := add(norm(k), norm(val)); err != nil {
				return nil, err
			}
		}
	case []any:
		for _, row := range x {
			r, _ := row.(map[string]any)
			from, to := r["from"], r["to"]
			if from == nil {
				from, to = r["alt"], r["neu"]
			}
			if from == nil || to == nil {
				return nil, crud.Invalid("Zuordnung: Spalten from;to (oder alt;neu) erwartet")
			}
			if err := add(norm(from), norm(to)); err != nil {
				return nil, err
			}
		}
	default:
		return nil, crud.Invalid("Zuordnung: JSON-Objekt {\"alt\": \"neu\"} oder CSV from;to erwartet")
	}
	return out, nil
}

func messageTexts(msgs []hook.Message, typ string) string {
	var out []string
	for _, m := range msgs {
		if typ == "" || m.Type == typ {
			out = append(out, m.Text)
		}
	}
	return strings.Join(out, "; ")
}
