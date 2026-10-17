package ledger

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Konten im Buchungskreis (seit 0.19.0): Der Kontenplan bleibt vollständig,
// im Buchungskreis stehen nur die Konten, die man wirklich braucht.
//
//   - Grundausstattung: Konten des Kontenplans mit „Grundausstattung“
//     (is_starter, in der Oberfläche oder in der Datei von load-coa) ordnet
//     „Buchungskreis einrichten“ zu; Konten = ALL ordnet wie bisher alle zu.
//     Hat der Kontenplan keine Grundausstattung, gelten alle.
//   - „Im Buchungskreis aktivieren …“ am Sachkonto des Kontenplans: ein
//     weiteres Konto zuordnen, sobald man es braucht (z. B. Forderungen gegen
//     den Geschäftsführer). Vorschläge wie bei der Einrichtung.
//   - „Nicht benötigte Konten entfernen …“ am Buchungskreis: Zuordnungen ohne
//     Buchung und Vorerfassung, außerhalb der Grundausstattung und von keinem
//     Fachmodul verwendet (Partner, Verträge, Bank … – gefragt über den Hook
//     ledger.chart_change, Phase check). Zuerst nur prüfen.

const (
	accountsStarter = "STARTER"
	accountsAll     = "ALL"
)

var setupAccountOptions = []metamodelOption{
	{Value: accountsStarter, Label: "Grundausstattung des Kontenplans (weitere Konten bei Bedarf aktivieren)"},
	{Value: accountsAll, Label: "alle Konten des Kontenplans"}}

// accountFilter: STARTER oder ALL; leer = STARTER, wenn der Kontenplan eine
// Grundausstattung hat, sonst ALL.
func (m *Module) accountFilter(ctx context.Context, chart, mode string) (string, error) {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	switch mode {
	case accountsAll, accountsStarter:
		return mode, nil
	case "":
		res, err := m.db.Query(ctx, "SELECT COUNT(*) FROM ledger__account_master WHERE chart_of_accounts_id = ? AND is_starter = ? AND is_group = ?",
			chart, true, false)
		if err != nil {
			return "", err
		}
		if toInt(res.Rows[0][0]) > 0 {
			return accountsStarter, nil
		}
		return accountsAll, nil
	}
	return "", crud.Invalid("Konten: %s oder %s", accountsStarter, accountsAll)
}

// assignAccount ordnet ein Konto dem Buchungskreis zu (Vorschläge aus hint,
// sonst aus der Kontoart des Kontenplans).
func (m *Module) assignAccount(ctx context.Context, cc, chart, cur, acc, typ, kind string, hint coaAccount) error {
	// ohne Vorschlag: Abstimmkonto aus der Kontoart des Kontenplans (D, K, A)
	recon := orDefault(hint.Reconciliation, orDefault(map[string]string{"D": "CUSTOMER", "K": "SUPPLIER", "A": "ASSET"}[kind], "NONE"))
	tax := orDefault(hint.TaxCategory, "NONE")
	group := orDefault(hint.FieldStatus, defaultGroup(typ, recon, tax))
	_, err := m.db.Exec(ctx, `INSERT INTO ledger__account_company (id, company_code_id, chart_of_accounts_id, account_number, currency,
		reconciliation_type, tax_category, field_status_group, is_blocked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), cc, chart, acc, cur, recon, tax, group, false)
	return err
}

// accountsChanged meldet geänderte Zuordnungen eines Buchungskreises (die
// Kopie in contract-billing lädt neu).
func (m *Module) accountsChanged(ctx context.Context, cc, action string, n int) {
	if n == 0 || m.services == nil {
		return
	}
	if err := events.Push(ctx, m.services, events.Event{Object: "GLAccountCompany", Action: action, CompanyCode: cc, Source: Name,
		Data: map[string]any{"accounts": n}}); err != nil {
		m.log.WarnContext(ctx, "SystemEvent nicht gemeldet", "object", "GLAccountCompany", "err", err.Error())
	}
}

// --- Im Buchungskreis aktivieren ------------------------------------------------

var activateFields = []crud.Field{
	{Key: "activate_company", Label: "Buchungskreis", Type: tText, ActionOnly: true, Lookup: lookupCC},
}

func (m *Module) activateAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "activateInCompany", Label: "Im Buchungskreis aktivieren …", Record: true,
		Fields: []string{"activate_company"}}, Handle: m.activateInCompany}
}

func (m *Module) activateInCompany(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Company string `json:"activate_company"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("GLAccount").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	chart, acc := crud.Str(key["chart_of_accounts_id"]), crud.Str(key["account_number"])
	cc := strings.TrimSpace(in.Data.Company)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis ist Pflicht")
	}
	if err := requireWrite(ctx, "GLAccountCompany", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	if cfg.Chart != chart {
		return sdk.Response{}, crud.Invalid("Buchungskreis %s arbeitet mit Kontenplan %s, nicht mit %s", cc, cfg.Chart, chart)
	}
	res, err := m.db.Query(ctx, "SELECT account_type, account_kind, is_active, is_group FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ?",
		chart, acc)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Konto %s gibt es im Kontenplan %s nicht", acc, chart)
	}
	r := res.Rows[0]
	switch {
	case crud.AsBool(r[3]):
		return sdk.Response{}, crud.Invalid("Konto %s ist eine Kontengruppe – nur bebuchbare Konten lassen sich aktivieren", acc)
	case !crud.AsBool(r[2]):
		return sdk.Response{}, crud.Invalid("Konto %s ist im Kontenplan gesperrt", acc)
	}
	if m.exists(ctx, "SELECT 1 FROM ledger__account_company WHERE company_code_id = ? AND account_number = ?", cc, acc) {
		return sdk.Response{}, crud.Invalid("Konto %s ist im Buchungskreis %s bereits aktiv", acc, cc)
	}
	hints, err := accountHints(chart, nil)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := m.assignAccount(ctx, cc, chart, cfg.Currency, acc, crud.Str(r[0]), crud.Str(r[1]), hints[acc]); err != nil {
		return sdk.Response{}, err
	}
	m.accountsChanged(ctx, cc, "create", 1)
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Konto %s „%v“ ist im Buchungskreis %s aktiv", acc, m.accountName(ctx, chart, acc), cc)}}, nil
}

func (m *Module) exists(ctx context.Context, sql string, args ...any) bool {
	res, err := m.db.Query(ctx, sql, args...)
	return err == nil && len(res.Rows) > 0
}

// notAssigned: Meldung für ein Konto ohne Zuordnung – mit Hinweis, wenn es
// im Kontenplan steht.
func (m *Module) notAssigned(ctx context.Context, cc, account string) string {
	msg := fmt.Sprintf("Konto %s ist dem Buchungskreis %s nicht zugeordnet", account, cc)
	if cfg, err := m.config(ctx, cc); err == nil &&
		m.exists(ctx, "SELECT 1 FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ? AND is_group = ?", cfg.Chart, account, false) {
		msg += fmt.Sprintf(" – es steht im Kontenplan %s: Sachkonten (Kontenplan) → %s → „Im Buchungskreis aktivieren …“", cfg.Chart, account)
	}
	return msg
}

// --- Nicht benötigte Konten entfernen -------------------------------------------

var cleanupFields = []crud.Field{
	{Key: "cleanup_check_only", Label: "Nur prüfen (nichts entfernen)", Type: tBool, ActionOnly: true},
}

func (m *Module) cleanupAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "removeUnusedAccounts", Label: "Nicht benötigte Konten entfernen …", Record: true,
		Fields: []string{"cleanup_check_only"},
		Confirm: "Konten ohne Buchung, außerhalb der Grundausstattung und ohne Verwendung in den Fachmodulen aus dem Buchungskreis entfernen? " +
			"Sie bleiben im Kontenplan und lassen sich jederzeit wieder aktivieren."}, Handle: m.removeUnusedAccounts}
}

func (m *Module) removeUnusedAccounts(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			CheckOnly any `json:"cleanup_check_only"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.ID)
	if err := requireWrite(ctx, "GLAccountCompany", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT c.account_number, COALESCE(a.is_starter, ?) FROM ledger__account_company c
		LEFT JOIN ledger__account_master a ON a.chart_of_accounts_id = c.chart_of_accounts_id AND a.account_number = c.account_number
		WHERE c.company_code_id = ? ORDER BY c.account_number`, false, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	posted, err := m.postedAccounts(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	var candidates []string
	starter, booked := 0, 0
	for _, r := range res.Rows {
		acc := crud.Str(r[0])
		switch {
		case crud.AsBool(r[1]):
			starter++
		case posted[acc]:
			booked++
		default:
			candidates = append(candidates, acc)
		}
	}
	// Fachmodule fragen: Mapping ohne die Kandidaten – jedes Modul meldet die
	// Kandidaten, die es gespeichert hat.
	mapping := map[string]string{}
	for _, r := range res.Rows {
		if acc := crud.Str(r[0]); !slices.Contains(candidates, acc) {
			mapping[acc] = acc
		}
	}
	used, err := m.usedByModules(ctx, cc, cfg.Chart, mapping)
	if err != nil {
		return sdk.Response{}, err
	}
	var remove []string
	for _, acc := range candidates {
		if !used[acc] {
			remove = append(remove, acc)
		}
	}
	kept := fmt.Sprintf("%d bleiben (Grundausstattung %d, gebucht %d, in Fachmodulen verwendet %d)",
		len(res.Rows)-len(remove), starter, booked, len(candidates)-len(remove))
	if checkOnly := in.Data.CheckOnly; checkOnly == true || strings.EqualFold(crud.Str(checkOnly), "true") {
		return sdk.Response{Payload: map[string]any{"removable": len(remove), "accounts": remove,
			"message": fmt.Sprintf("Prüfung: %d Konten im Buchungskreis %s nicht benötigt, %s", len(remove), cc, kept)}}, nil
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, acc := range remove {
			if _, err := m.db.Exec(ctx, "DELETE FROM ledger__account_company WHERE company_code_id = ? AND account_number = ?", cc, acc); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.accountsChanged(ctx, cc, "delete", len(remove))
	m.log.InfoContext(ctx, "Nicht benötigte Konten entfernt", "company_code", cc, "removed", len(remove))
	return sdk.Response{Payload: map[string]any{"removed": len(remove),
		"message": fmt.Sprintf("Buchungskreis %s: %d Konten entfernt (im Kontenplan bleiben sie), %s", cc, len(remove), kept)}}, nil
}

// postedAccounts: Konten mit Einzelposten oder Vorerfassung im Buchungskreis.
func (m *Module) postedAccounts(ctx context.Context, cc string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, q := range []string{
		"SELECT DISTINCT account_number FROM ledger__journal_item WHERE company_code_id = ?",
		"SELECT DISTINCT account_number FROM ledger__journal_entry_item WHERE company_code_id = ?",
		"SELECT DISTINCT i.account_number FROM ledger__draft_item i JOIN ledger__draft_header h ON h.id = i.draft_id WHERE h.company_code_id = ?",
	} {
		res, err := m.db.Query(ctx, q, cc)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			out[crud.Str(r[0])] = true
		}
	}
	return out, nil
}

// usedByModules: Konten, die die Fachmodule in der Zuordnung vermissen
// (Hook ledger.chart_change, Phase check, alter = neuer Kontenplan). Die
// Meldungen enden mit der Liste „…: 1200, 1800“; eine Meldung ohne Liste
// bricht ab (lieber nichts entfernen als ein verwendetes Konto).
func (m *Module) usedByModules(ctx context.Context, cc, chart string, mapping map[string]string) (map[string]bool, error) {
	data := ledgerapi.ChartChange{CompanyCode: cc, FromChart: chart, ToChart: chart, Mapping: mapping}
	res, err := hook.Call(ctx, m.services, ledgerapi.HookChartChange, hook.PhaseCheck, data)
	if err != nil {
		return nil, fmt.Errorf("Fachmodule nicht erreichbar – es wird nichts entfernt: %w", err)
	}
	used := map[string]bool{}
	for _, msg := range res.Messages {
		if msg.Type != hook.TypeError {
			continue
		}
		i := strings.LastIndex(msg.Text, ": ")
		if i < 0 {
			return nil, crud.Invalid("Meldung eines Fachmoduls ohne Kontenliste – es wird nichts entfernt: %s", msg.Text)
		}
		for _, a := range strings.Split(msg.Text[i+2:], ",") {
			if a = strings.TrimSpace(a); a != "" {
				used[a] = true
			}
		}
	}
	return used, nil
}

// checkTaxAccount: Konto für nicht abziehbare Vorsteuer (seit 0.26.0) muss im
// Buchungskreis zugeordnet sein und darf nicht das Konto selbst sein.
func (m *Module) checkTaxAccount(ctx context.Context, rec crud.Record) error {
	acc := strings.ToUpper(strings.TrimSpace(crud.Str(rec["nondeductible_tax_account"])))
	if acc == "" {
		rec["nondeductible_tax_account"] = nil
		return nil
	}
	cc := crud.Str(rec["company_code_id"])
	if cfg, err := m.config(ctx, cc); err == nil {
		if no, err := m.accountKey(ctx, cfg.Chart, acc); err != nil {
			return err
		} else {
			acc = no
		}
	}
	if acc == crud.Str(rec["account_number"]) {
		return crud.Invalid("Konto für nicht abziehbare Vorsteuer: ein anderes Konto als %s angeben (leer = in dieses Konto)", acc)
	}
	res, err := m.db.Query(ctx, "SELECT 1 FROM ledger__account_company WHERE company_code_id = ? AND account_number = ?", cc, acc)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Konto für nicht abziehbare Vorsteuer %s ist dem Buchungskreis %s nicht zugeordnet", acc, cc)
	}
	rec["nondeductible_tax_account"] = acc
	return nil
}
