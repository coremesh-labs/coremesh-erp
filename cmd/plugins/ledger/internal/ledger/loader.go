package ledger

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
)

// Ladevorgänge (Service LedgerLoader, Konsolenbefehle ledger:…):
//
//	console ledger:load-coa --chart=SKR04                      mitgelieferter Kontenrahmen (coa/skr04.json)
//	console ledger:load-coa --chart=SKR25 --file=./skr25.csv   eigene Datei (JSON oder CSV)
//	console ledger:load-rates --file=./kurse.csv               Tageskurse
//	console ledger:setup-company --company=1000 --chart=SKR04 --currency=EUR --year=2026
//	console ledger:periods --company=1000 --year=2026 --from=1 --to=12 --status=OPEN
//
// Alle Ladevorgänge sind idempotent (Upsert): Ein zweiter Lauf erzeugt keine
// Duplikate, sondern meldet unveränderte bzw. aktualisierte Datensätze.

//go:embed coa/*.json
var coaFiles embed.FS

// coaFile ist das Format der Kontenrahmen-Dateien.
type coaFile struct {
	Chart       string       `json:"chart_of_accounts_id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Country     string       `json:"country"`
	Accounts    []coaAccount `json:"accounts"`
}

type coaAccount struct {
	Number      string `json:"account_number"`
	Name        string `json:"name"`
	Type        string `json:"account_type"`
	Group       string `json:"account_group,omitempty"`
	Description string `json:"description,omitempty"`
	Active      *bool  `json:"is_active,omitempty"`
	// Vorschlag für die Zuordnung zum Buchungskreis (ledger:setup-company).
	Reconciliation string `json:"reconciliation_type,omitempty"`
	TaxCategory    string `json:"tax_category,omitempty"`
}

// param liest einen Parameter der Konsole (--name) oder eines Formulars
// ({data: {feld}}), in dieser Reihenfolge.
func param(payload any, names ...string) string {
	p, _ := payload.(map[string]any)
	d := dataOf(payload)
	for _, n := range names {
		for _, src := range []map[string]any{p, d} {
			if v, ok := src[n]; ok && v != nil && crud.Str(v) != "" {
				return strings.TrimSpace(crud.Str(v))
			}
		}
	}
	return ""
}

// embeddedCOA liefert den mitgelieferten Kontenrahmen (oder nil).
func embeddedCOA(chart string) (*coaFile, error) {
	b, err := coaFiles.ReadFile("coa/" + strings.ToLower(chart) + ".json")
	if err != nil {
		return nil, nil
	}
	var f coaFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("coa/%s.json: %w", strings.ToLower(chart), err)
	}
	return &f, nil
}

// coaFromFile: Inhalt von --file – JSON-Objekt {accounts:[…]}, JSON-Liste
// oder CSV-Zeilen (von der CLI bereits zu Objekten gemacht).
func coaFromFile(v any) (*coaFile, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var f coaFile
	if _, isList := v.([]any); isList {
		err = json.Unmarshal(b, &f.Accounts)
	} else {
		err = json.Unmarshal(b, &f)
	}
	if err != nil {
		return nil, crud.Invalid("Datei: Kontenrahmen im Format {\"accounts\": [{\"account_number\", \"name\", \"account_type\"}]} oder CSV erwartet (%v)", err)
	}
	return &f, nil
}

func (m *Module) loadCoaAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	chart := strings.ToUpper(param(req.Payload, "chart", "id"))
	if !codeRe.MatchString(chart) {
		return sdk.Response{}, crud.Invalid("Kontenplan %q: z. B. SKR04 oder SKR25", chart)
	}
	p, _ := req.Payload.(map[string]any)
	var f *coaFile
	var err error
	if file, ok := p["file"]; ok && file != nil {
		f, err = coaFromFile(file)
	} else if f, err = embeddedCOA(chart); err == nil && f == nil {
		err = crud.Invalid("für %s ist kein Kontenrahmen mitgeliefert (SKR04, SKR25) – Datei mit --file angeben", chart)
	}
	if err != nil {
		return sdk.Response{}, err
	}
	if f.Chart != "" && !strings.EqualFold(f.Chart, chart) {
		return sdk.Response{}, crud.Invalid("Datei enthält Kontenplan %s, angegeben ist %s", f.Chart, chart)
	}
	var stats struct{ Inserted, Updated, Unchanged int }
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		name := f.Name
		if name == "" {
			name = chart
		}
		if err := m.upsert(ctx, "ledger__chart_of_accounts", []string{"id"}, map[string]any{"id": chart, "name": name,
			"description": nilIfEmpty(f.Description), "country": nilIfEmpty(f.Country)}, nil); err != nil {
			return err
		}
		seen := map[string]bool{}
		for i, a := range f.Accounts {
			row, err := a.row(chart)
			if err != nil {
				return crud.Invalid("Konto %d (%s): %v", i+1, a.Number, err)
			}
			if seen[a.Number] {
				return crud.Invalid("Konto %s ist in der Datei doppelt", a.Number)
			}
			seen[a.Number] = true
			if err := m.upsert(ctx, "ledger__account_master", []string{"chart_of_accounts_id", "account_number"}, row, &stats); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{
		"chart_of_accounts_id": chart, "accounts": len(f.Accounts),
		"inserted": stats.Inserted, "updated": stats.Updated, "unchanged": stats.Unchanged,
		"message": fmt.Sprintf("Kontenplan %s: %d neu, %d geändert, %d unverändert", chart, stats.Inserted, stats.Updated, stats.Unchanged),
	}}, nil
}

func (a coaAccount) row(chart string) (map[string]any, error) {
	a.Number = strings.ToUpper(strings.TrimSpace(a.Number))
	if !accountRe.MatchString(a.Number) {
		return nil, fmt.Errorf("Kontonummer ungültig")
	}
	if strings.TrimSpace(a.Name) == "" {
		return nil, fmt.Errorf("Bezeichnung fehlt")
	}
	a.Type = strings.ToUpper(strings.TrimSpace(a.Type))
	if !slices.ContainsFunc(accountTypes, func(o metamodelOption) bool { return o.Value == a.Type }) {
		return nil, fmt.Errorf("Kontoart %q – erlaubt: BALANCE_SHEET, PRIMARY_COST, SECONDARY_COST, REVENUE, NON_OPERATING", a.Type)
	}
	active := a.Active == nil || *a.Active
	return map[string]any{"chart_of_accounts_id": chart, "account_number": a.Number, "name": strings.TrimSpace(a.Name),
		"account_type": a.Type, "account_group": nilIfEmpty(a.Group), "description": nilIfEmpty(a.Description), "is_active": active}, nil
}

// upsert schreibt row (Schlüssel keys): neu, geändert oder unverändert.
func (m *Module) upsert(ctx context.Context, table string, keys []string, row map[string]any, stats *struct{ Inserted, Updated, Unchanged int }) error {
	var cols []string
	for c := range row {
		cols = append(cols, c)
	}
	slices.Sort(cols)
	where, wargs := []string{}, []any{}
	for _, k := range keys {
		where, wargs = append(where, k+" = ?"), append(wargs, row[k])
	}
	res, err := m.db.Query(ctx, "SELECT "+strings.Join(cols, ", ")+" FROM "+table+" WHERE "+strings.Join(where, " AND "), wargs...)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		args := make([]any, len(cols))
		for i, c := range cols {
			args[i] = row[c]
		}
		if _, err := m.db.Exec(ctx, "INSERT INTO "+table+" ("+strings.Join(cols, ", ")+") VALUES ("+marks+")", args...); err != nil {
			return err
		}
		if stats != nil {
			stats.Inserted++
		}
		return nil
	}
	var sets []string
	var args []any
	for i, c := range cols {
		if slices.Contains(keys, c) || same(res.Rows[0][i], row[c]) {
			continue
		}
		sets, args = append(sets, c+" = ?"), append(args, row[c])
	}
	if len(sets) == 0 {
		if stats != nil {
			stats.Unchanged++
		}
		return nil
	}
	if _, err := m.db.Exec(ctx, "UPDATE "+table+" SET "+strings.Join(sets, ", ")+" WHERE "+strings.Join(where, " AND "), append(args, wargs...)...); err != nil {
		return err
	}
	if stats != nil {
		stats.Updated++
	}
	return nil
}

// same vergleicht einen gelesenen mit einem zu schreibenden Wert (Bool und Zahl
// kommen je nach Treiber als int64).
func same(db, v any) bool {
	switch x := v.(type) {
	case nil:
		return db == nil
	case bool:
		return crud.AsBool(db) == x
	case int, int64, float64:
		return toInt(db) == toInt(x)
	}
	return crud.Str(db) == crud.Str(v)
}

// --- Kurse -------------------------------------------------------------------------

func (m *Module) loadRatesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	raw := p["file"]
	if obj, ok := raw.(map[string]any); ok {
		raw = obj["rates"]
	}
	rows, ok := raw.([]any)
	if !ok {
		return sdk.Response{}, crud.Invalid("Kurse: Liste [{rate_type, from_currency, to_currency, valid_from, rate}] bzw. {\"rates\": […]} oder CSV erwartet")
	}
	var stats struct{ Inserted, Updated, Unchanged int }
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for i, r := range rows {
			in, _ := r.(map[string]any)
			rec := map[string]any{}
			for _, k := range []string{"rate_type", "from_currency", "to_currency", "rate", "from_factor", "to_factor"} {
				rec[k] = strings.ToUpper(strings.TrimSpace(crud.Str(in[k])))
			}
			if rec["rate_type"] == "" {
				rec["rate_type"] = "M"
			}
			d, err := crud.ParseDate(in["valid_from"])
			if err != nil {
				return crud.Invalid("Kurs %d: valid_from %v", i+1, err)
			}
			rec["valid_from"] = d
			for _, k := range []string{"from_factor", "to_factor"} {
				if rec[k] == "" {
					rec[k] = nil
				} else if n, err := strconv.ParseInt(crud.Str(rec[k]), 10, 64); err == nil {
					rec[k] = n
				}
			}
			if !slices.ContainsFunc(rateTypes, func(o metamodelOption) bool { return o.Value == rec["rate_type"] }) {
				return crud.Invalid("Kurs %d: Kurstyp %v – M, B oder G", i+1, rec["rate_type"])
			}
			for _, k := range []string{"from_currency", "to_currency"} {
				if _, err := m.cur.decimals(ctx, crud.Str(rec[k])); err != nil {
					return crud.Invalid("Kurs %d: %v", i+1, err)
				}
			}
			if err := checkRate(rec); err != nil {
				return crud.Invalid("Kurs %d: %v", i+1, strings.TrimPrefix(err.Error(), sdk.ErrInvalidArgument.Error()+": "))
			}
			if err := m.upsert(ctx, "ledger__exchange_rate", []string{"rate_type", "from_currency", "to_currency", "valid_from"}, rec, &stats); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"rates": len(rows), "inserted": stats.Inserted, "updated": stats.Updated, "unchanged": stats.Unchanged,
		"message": fmt.Sprintf("Kurse: %d neu, %d geändert, %d unverändert", stats.Inserted, stats.Updated, stats.Unchanged)}}, nil
}

// --- Buchungskreis einrichten ------------------------------------------------------

// setupCompanyAction: Steuerung anlegen (falls neu), alle aktiven Konten des
// Kontenplans dem Buchungskreis zuordnen (mit Vorschlägen für Abstimmkonto und
// Steuerkategorie aus dem mitgelieferten Kontenrahmen) und optional die
// Perioden 1–12 eines Jahres öffnen. Idempotent.
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc := param(req.Payload, "company", "company_code_id")
	chart := strings.ToUpper(param(req.Payload, "chart", "chart_of_accounts_id"))
	cur := strings.ToUpper(param(req.Payload, "currency"))
	year := toInt(param(req.Payload, "year", "setup_year"))
	if cc == "" || chart == "" || cur == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis, Kontenplan und Währung sind Pflicht")
	}
	if err := requireCompanyCode(ctx, "LedgerCompanyConfig", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	if err := m.companyCodeExists(ctx, cc); err != nil {
		return sdk.Response{}, err
	}
	var created bool
	var assigned, opened int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.cur.decimals(ctx, cur); err != nil {
			return err
		}
		res, err := m.db.Query(ctx, "SELECT COUNT(*) FROM ledger__account_master WHERE chart_of_accounts_id = ? AND is_active = ?", chart, true)
		if err != nil {
			return err
		}
		if toInt(res.Rows[0][0]) == 0 {
			return crud.Invalid("Kontenplan %s hat keine aktiven Konten – zuerst console ledger:load-coa --chart=%s", chart, chart)
		}
		cfg, err := m.config(ctx, cc)
		switch {
		case err == nil && (cfg.Chart != chart || cfg.Currency != cur):
			return crud.Invalid("Buchungskreis %s ist bereits mit Kontenplan %s und Währung %s eingerichtet", cc, cfg.Chart, cfg.Currency)
		case err != nil:
			lead, err := m.db.Query(ctx, "SELECT id FROM ledger__ledger WHERE is_leading = ? AND is_active = ? ORDER BY id LIMIT 1", true, true)
			if err != nil || len(lead.Rows) == 0 {
				return crud.Invalid("kein aktives führendes Ledger")
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__company_config (company_code_id, leading_ledger, chart_of_accounts_id, currency,
				fiscal_year_variant, exchange_rate_type) VALUES (?, ?, ?, ?, 'K4', 'M')`, cc, lead.Rows[0][0], chart, cur); err != nil {
				return err
			}
			created = true
		}
		hints := map[string]coaAccount{}
		if f, _ := embeddedCOA(chart); f != nil {
			for _, a := range f.Accounts {
				hints[a.Number] = a
			}
		}
		res, err = m.db.Query(ctx, `SELECT account_number FROM ledger__account_master m WHERE chart_of_accounts_id = ? AND is_active = ?
			AND NOT EXISTS (SELECT 1 FROM ledger__account_company c WHERE c.company_code_id = ? AND c.account_number = m.account_number)
			ORDER BY account_number`, chart, true, cc)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			acc := crud.Str(r[0])
			recon, tax := orDefault(hints[acc].Reconciliation, "NONE"), orDefault(hints[acc].TaxCategory, "NONE")
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__account_company (id, company_code_id, chart_of_accounts_id, account_number, currency,
				reconciliation_type, tax_category, is_blocked) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, crud.NewID(), cc, chart, acc, cur, recon, tax, false); err != nil {
				return err
			}
			assigned++
		}
		if year > 0 {
			cfg, err := m.config(ctx, cc)
			if err != nil {
				return err
			}
			if opened, err = m.setPeriods(ctx, cc, cfg.Ledger, int(year), 1, 12, "OPEN"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Buchungskreis %s: %d Konten zugeordnet", cc, assigned)
	if created {
		msg = fmt.Sprintf("Buchungskreis %s eingerichtet (Kontenplan %s, %s): %d Konten zugeordnet", cc, chart, cur, assigned)
	}
	if year > 0 {
		msg += fmt.Sprintf(", Perioden 1–12/%d offen", year)
	}
	return sdk.Response{Payload: map[string]any{"company_code_id": cc, "created": created, "accounts_assigned": assigned,
		"periods_changed": opened, "message": msg}}, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// --- Perioden ----------------------------------------------------------------------

func (m *Module) setPeriodsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc := param(req.Payload, "company", "company_code_id")
	year := int(toInt(param(req.Payload, "year", "fiscal_year")))
	from := int(toInt(param(req.Payload, "from", "posting_period")))
	to := int(toInt(param(req.Payload, "to", "period_to")))
	status := strings.ToUpper(param(req.Payload, "status"))
	if to == 0 {
		to = from
	}
	switch {
	case cc == "" || year < 1900 || year > 2999:
		return sdk.Response{}, crud.Invalid("Buchungskreis und Geschäftsjahr sind Pflicht")
	case from < 1 || to > 16 || from > to:
		return sdk.Response{}, crud.Invalid("Perioden %d–%d: 1–16, von ≤ bis", from, to)
	case status != "OPEN" && status != "CLOSED":
		return sdk.Response{}, crud.Invalid("Status OPEN oder CLOSED")
	}
	if err := requireCompanyCode(ctx, "FiscalPeriod", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		ledger := strings.ToUpper(param(req.Payload, "ledger"))
		if ledger == "" {
			cfg, err := m.config(ctx, cc)
			if err != nil {
				return err
			}
			ledger = cfg.Ledger
		}
		var err error
		n, err = m.setPeriods(ctx, cc, ledger, year, from, to, status)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	word := map[string]string{"OPEN": "offen", "CLOSED": "gesperrt"}[status]
	return sdk.Response{Payload: map[string]any{"changed": n,
		"message": fmt.Sprintf("Perioden %d–%d/%d im Buchungskreis %s %s (%d geändert)", from, to, year, cc, word, n)}}, nil
}

func (m *Module) setPeriods(ctx context.Context, cc, ledger string, year, from, to int, status string) (int, error) {
	var stats struct{ Inserted, Updated, Unchanged int }
	now, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	for p := from; p <= to; p++ {
		res, err := m.db.Query(ctx, `SELECT status FROM ledger__fiscal_period_status WHERE company_code_id = ? AND ledger = ? AND fiscal_year = ? AND posting_period = ?`,
			cc, ledger, year, p)
		if err != nil {
			return 0, err
		}
		if len(res.Rows) > 0 && crud.Str(res.Rows[0][0]) == status {
			continue
		}
		if err := m.upsert(ctx, "ledger__fiscal_period_status", []string{"company_code_id", "ledger", "fiscal_year", "posting_period"},
			map[string]any{"company_code_id": cc, "ledger": ledger, "fiscal_year": year, "posting_period": p, "status": status,
				"changed_at": now, "changed_by": user}, &stats); err != nil {
			return 0, err
		}
	}
	return stats.Inserted + stats.Updated, nil
}
