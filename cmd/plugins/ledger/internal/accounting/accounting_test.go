package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
	"github.com/camel/coremesh/pkg/sdk/module"
)

// testHost: SQLite mit einer Verbindung, Buchungskreise 1000/2000 und Rechte
// je Buchungskreis (Account.Check/Granted) als Attrappe.
type testHost struct {
	db      *sql.DB
	granted map[string][]string // action → Buchungskreise ("*" = alle)
}

func (h *testHost) Log(context.Context, sdk.LogLevel, string, map[string]string) error { return nil }

func (h *testHost) Query(ctx context.Context, _ string, q string, args ...any) (*sdk.QueryResult, error) {
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	res := &sdk.QueryResult{Columns: cols}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		res.Rows = append(res.Rows, vals)
	}
	return res, rows.Err()
}

func (h *testHost) Exec(ctx context.Context, _ string, q string, args ...any) (sdk.ExecResult, error) {
	r, err := h.db.ExecContext(ctx, q, args...)
	if err != nil {
		return sdk.ExecResult{}, err
	}
	n, _ := r.RowsAffected()
	return sdk.ExecResult{RowsAffected: n}, nil
}

func (h *testHost) BeginTx(ctx context.Context, _ string, _ sql.TxOptions) (string, error) {
	_, err := h.db.ExecContext(ctx, "BEGIN")
	return "tx", err
}
func (h *testHost) CommitTx(ctx context.Context, _ string) error {
	_, err := h.db.ExecContext(ctx, "COMMIT")
	return err
}
func (h *testHost) RollbackTx(ctx context.Context, _ string) error {
	_, err := h.db.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	return err
}

func (h *testHost) allows(action, cc string) bool {
	g := h.granted[action]
	return slices.Contains(g, "*") || slices.Contains(g, cc)
}

func (h *testHost) Handle(_ context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	switch req.Object + "." + req.Action {
	case "CompanyCode.get":
		if id := fmt.Sprint(p["id"]); id == "1000" || id == "2000" {
			return sdk.Response{Payload: map[string]any{"id": id, "code": id}}, nil
		}
		return sdk.Response{}, fmt.Errorf("%w: Buchungskreis %v", sdk.ErrNotFound, p["id"])
	case "Account.Check":
		return sdk.Response{Payload: map[string]any{"allowed": h.allows(fmt.Sprint(p["action"]), fmt.Sprint(p["company_code"]))}}, nil
	case "Account.Granted":
		g := h.granted[fmt.Sprint(p["action"])]
		if slices.Contains(g, "*") {
			return sdk.Response{Payload: map[string]any{"all": true, "company_codes": []any{}}}, nil
		}
		ccs := []any{}
		for _, c := range g {
			ccs = append(ccs, c)
		}
		return sdk.Response{Payload: map[string]any{"all": false, "company_codes": ccs}}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

type env struct {
	t   *testing.T
	p   *module.Plugin
	h   *testHost
	ctx context.Context
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	all := []string{"*"}
	h := &testHost{db: db, granted: map[string][]string{"post": all, "reverse": all, "list": all, "get": all}}
	p := module.NewPlugin(module.Info{Name: "ledger", Version: "test"}, New())
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	hctx := sdk.WithHost(ctx, h)
	if err := p.Configure(hctx, sdk.Config{Host: h}); err != nil {
		t.Fatal(err)
	}
	resp, err := p.Handle(ctx, sdk.Request{Object: sdk.ObjectDBSchema, Action: sdk.ActionInit, Payload: sdk.SchemaInitRequest{Module: "ledger"}})
	if err != nil {
		t.Fatal(err)
	}
	init := resp.Payload.(sdk.SchemaInitResponse)
	drv, err := sqlite.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	var desired schema.Schema
	if err := sqlite.EvalHCLBytes([]byte(init.Schema), &desired, nil); err != nil {
		t.Fatalf("schemaHCL: %v", err)
	}
	desired.Name = "main"
	for _, tb := range desired.Tables {
		tb.Schema = &desired
		if !strings.HasPrefix(tb.Name, "ledger__") {
			t.Fatalf("Tabelle ohne Präfix: %s", tb.Name)
		}
	}
	current, _ := drv.InspectSchema(ctx, "main", nil)
	changes, err := drv.SchemaDiff(current, &desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := drv.ApplyChanges(ctx, changes); err != nil {
		t.Fatal(err)
	}
	for _, s := range init.Seed { // Seeds wie DBSchema: nur fehlende Zeilen
		for _, row := range s.Rows {
			var cols, marks []string
			var args []any
			for k, v := range row {
				cols, marks, args = append(cols, k), append(marks, "?"), append(args, v)
			}
			if _, err := db.Exec("INSERT OR IGNORE INTO "+s.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...); err != nil {
				t.Fatal(err)
			}
		}
	}
	return &env{t: t, p: p, h: h, ctx: hctx}
}

func (e *env) call(object, action string, payload any) (map[string]any, error) {
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: object, Action: action, Payload: payload})
	if err != nil {
		return nil, err
	}
	m, _ := resp.Payload.(map[string]any)
	return m, nil
}

func (e *env) must(object, action string, payload any) map[string]any {
	e.t.Helper()
	m, err := e.call(object, action, payload)
	if err != nil {
		e.t.Fatalf("%s.%s: %v", object, action, err)
	}
	return m
}

func expect(t *testing.T, err error, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: %v erwartet, bekommen %v", what, target, err)
	}
}

func rent(amount string) map[string]any {
	return map[string]any{"data": map[string]any{"company_code": "1000", "posting_date": "2026-10-01", "currency": "chf",
		"text": "Miete Oktober", "debit_account": "1020", "credit_account": "3400", "amount": amount}}
}

func TestParseAmount(t *testing.T) {
	for in, want := range map[string]int64{"1500": 150000, "1500.5": 150050, "1'500.50": 150050, "0.05": 5, "12,3": 1230} {
		if got, err := parseAmount(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "-5", "1.234", "abc", "1e3"} {
		if _, err := parseAmount(in); err == nil {
			t.Errorf("%q angenommen", in)
		}
	}
	if formatAmount(-150005) != "-1500.05" || formatAmount(7) != "0.07" {
		t.Fatal("formatAmount")
	}
}

func TestPostSimpleAndLines(t *testing.T) {
	e := setup(t)
	// Kontenplan aus den Seeds.
	if a := e.must("GLAccount", "get", map[string]any{"id": "3400"}); a["name"] != "Mietertrag" || a["account_type"] != typeIncome {
		t.Fatalf("Seed: %v", a)
	}

	// Einfache Buchung „Bank an Mietertrag“.
	b := e.must("JournalEntry", "post", rent("1'500.00"))
	if b["document_no"] != "2026-000001" || b["currency"] != "CHF" || b["total"] != "1500.00 CHF" || b["document_date"] != "2026-10-01" {
		t.Fatalf("Beleg: %v", b)
	}
	lines := items(e.must("JournalLine", "list", map[string]any{"query": map[string]any{"entry_id": b["id"]}}))
	if len(lines) != 2 || lines[0]["account_code"] != "1020" || lines[0]["debit"] != "1500.00" || lines[1]["credit"] != "1500.00" {
		t.Fatalf("Positionen: %v", lines)
	}
	if lines[1]["_labels"].(map[string]any)["account_code"] != "Mietertrag" {
		t.Fatalf("Label: %v", lines[1]["_labels"])
	}

	// Sammelbuchung: Miete + Nebenkosten auf Bank, Nummer läuft weiter.
	b2 := e.must("JournalEntry", "post", map[string]any{"data": map[string]any{"company_code": "1000", "posting_date": "2026-10-02",
		"currency": "CHF", "text": "Miete und Akonto", "lines": []any{
			map[string]any{"account_code": "1020", "debit": "1800"},
			map[string]any{"account_code": "3400", "credit": "1500"},
			map[string]any{"account_code": "3410", "credit": "300", "text": "Akonto NK"},
		}}})
	if b2["document_no"] != "2026-000002" || b2["total"] != "1800.00 CHF" {
		t.Fatalf("Sammelbuchung: %v", b2)
	}
	// Eigene Nummernfolge je Buchungskreis.
	other := rent("10")
	other["data"].(map[string]any)["company_code"] = "2000"
	if b3 := e.must("JournalEntry", "post", other); b3["document_no"] != "2026-000001" {
		t.Fatalf("Buchungskreis 2000: %v", b3["document_no"])
	}

	// Belege sind schreibgeschützt.
	_, err := e.call("JournalEntry", "update", map[string]any{"id": b["id"], "data": map[string]any{"text": "x"}})
	expect(t, err, sdk.ErrUnimplemented, "update")
	_, err = e.call("JournalEntry", "create", map[string]any{"data": map[string]any{"text": "x"}})
	expect(t, err, sdk.ErrUnimplemented, "create")
}

func TestPostValidation(t *testing.T) {
	e := setup(t)
	e.must("GLAccount", "create", map[string]any{"data": map[string]any{"code": "9999", "name": "Alt", "account_type": typeExpense}})
	e.must("GLAccount", "deactivate", map[string]any{"id": "9999"})

	unbalanced := map[string]any{"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "lines": []any{
		map[string]any{"account_code": "1020", "debit": "100"}, map[string]any{"account_code": "3400", "credit": "90"}}}}
	for name, payload := range map[string]map[string]any{
		"Soll ≠ Haben":       unbalanced,
		"Betrag 0":           rent("0"),
		"Betrag negativ":     rent("-5"),
		"3 Nachkommastellen": rent("1.005"),
		"gleiches Konto":     {"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "debit_account": "1020", "credit_account": "1020", "amount": "1"}},
		"unbekanntes Konto":  {"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "debit_account": "4711", "credit_account": "1020", "amount": "1"}},
		"gesperrtes Konto":   {"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "debit_account": "9999", "credit_account": "1020", "amount": "1"}},
		"Buchungskreis":      {"data": map[string]any{"company_code": "3000", "currency": "CHF", "text": "x", "debit_account": "1000", "credit_account": "1020", "amount": "1"}},
		"Währung":            {"data": map[string]any{"company_code": "1000", "currency": "Franken", "text": "x", "debit_account": "1000", "credit_account": "1020", "amount": "1"}},
		"ohne Text":          {"data": map[string]any{"company_code": "1000", "currency": "CHF", "debit_account": "1000", "credit_account": "1020", "amount": "1"}},
		"eine Position":      {"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "lines": []any{map[string]any{"account_code": "1020", "debit": "1"}}}},
		"Soll und Haben":     {"data": map[string]any{"company_code": "1000", "currency": "CHF", "text": "x", "lines": []any{map[string]any{"account_code": "1020", "debit": "1", "credit": "1"}, map[string]any{"account_code": "3400", "credit": "1"}}}},
	} {
		_, err := e.call("JournalEntry", "post", payload)
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
	if n := len(items(e.must("JournalEntry", "list", nil))); n != 0 {
		t.Fatalf("%d Belege trotz Fehlern", n)
	}
}

func TestReverse(t *testing.T) {
	e := setup(t)
	b := e.must("JournalEntry", "post", rent("1500"))
	_, err := e.call("JournalEntry", "reverse", map[string]any{"id": b["id"], "data": map[string]any{"posting_date": "2026-09-30"}})
	expect(t, err, sdk.ErrInvalidArgument, "Storno vor dem Beleg")

	s := e.must("JournalEntry", "reverse", map[string]any{"id": b["id"], "data": map[string]any{"posting_date": "2026-10-15"}})
	if s["reversal_of"] != b["id"] || s["text"] != "Storno 2026-000001" || s["document_no"] != "2026-000002" {
		t.Fatalf("Storno: %v", s)
	}
	if o := e.must("JournalEntry", "get", map[string]any{"id": b["id"]}); o["reversed_by"] != s["id"] {
		t.Fatalf("Original: %v", o)
	}
	lines := items(e.must("JournalLine", "list", map[string]any{"query": map[string]any{"entry_id": s["id"]}}))
	if lines[0]["account_code"] != "1020" || lines[0]["credit"] != "1500.00" || lines[1]["debit"] != "1500.00" {
		t.Fatalf("vertauschte Seiten: %v", lines)
	}
	_, err = e.call("JournalEntry", "reverse", map[string]any{"id": b["id"]})
	expect(t, err, sdk.ErrInvalidArgument, "doppeltes Storno")
	_, err = e.call("JournalEntry", "reverse", map[string]any{"id": s["id"]})
	expect(t, err, sdk.ErrInvalidArgument, "Storno vom Storno")

	// Saldo nach Storno: alles ausgeglichen.
	bal := e.must("AccountBalance", "list", map[string]any{"company_code": "1000"})
	for _, r := range bal["items"].([]balanceRow) {
		if r.BalanceMin != 0 {
			t.Fatalf("Saldo nach Storno: %+v", r)
		}
	}
}

func TestBalancesAndCompanyCodeRights(t *testing.T) {
	e := setup(t)
	e.must("JournalEntry", "post", rent("1500"))
	expense := map[string]any{"data": map[string]any{"company_code": "1000", "posting_date": "2026-11-05", "currency": "CHF",
		"text": "Reparatur Heizung", "debit_account": "6100", "credit_account": "1020", "amount": "420.40"}}
	e.must("JournalEntry", "post", expense)
	other := rent("99")
	other["data"].(map[string]any)["company_code"] = "2000"
	e.must("JournalEntry", "post", other)

	bal := e.must("AccountBalance", "list", map[string]any{"company_code": "1000"})
	byAcc := map[string]balanceRow{}
	for _, r := range bal["items"].([]balanceRow) {
		byAcc[r.Account] = r
	}
	if byAcc["1020"].Balance != "1079.60" || byAcc["3400"].Balance != "-1500.00" || byAcc["6100"].Balance != "420.40" {
		t.Fatalf("Salden: %+v", byAcc)
	}
	if tot := bal["totals"].([]balanceTotal); len(tot) != 1 || tot[0].Debit != tot[0].Credit || tot[0].Debit != "1920.40" {
		t.Fatalf("Summen: %+v", tot)
	}
	// Zeitraum: nur Oktober.
	oct := e.must("AccountBalance", "list", map[string]any{"company_code": "1000", "date_to": "2026-10-31"})
	if len(oct["items"].([]balanceRow)) != 2 {
		t.Fatalf("Oktober: %+v", oct["items"])
	}

	// Rechte je Buchungskreis: nur 2000 sichtbar, in 1000 nicht buchen.
	e.h.granted = map[string][]string{"list": {"2000"}, "get": {"2000"}, "post": {"2000"}}
	if items := items(e.must("JournalEntry", "list", nil)); len(items) != 1 || items[0]["company_code"] != "2000" {
		t.Fatalf("Liste: %v", items)
	}
	if n := len(e.must("AccountBalance", "list", nil)["items"].([]balanceRow)); n != 2 {
		t.Fatalf("Salden 2000: %d Zeilen", n)
	}
	_, err := e.call("JournalEntry", "post", rent("1"))
	expect(t, err, sdk.ErrPermissionDenied, "Buchen in 1000")
	_, err = e.call("AccountBalance", "list", map[string]any{"company_code": "1000"})
	expect(t, err, sdk.ErrPermissionDenied, "Salden 1000")
}

// TestMetamodelAndTranslations: Metamodelle gültig, alle Texte in allen Sprachen.
func TestMetamodelAndTranslations(t *testing.T) {
	e := setup(t)
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: sdk.ObjectCatalog, Action: sdk.ActionDescribe})
	if err != nil {
		t.Fatal(err)
	}
	d := resp.Payload.(metamodel.DescribeResponse)
	need := func(key string) {
		for _, loc := range metamodel.Locales {
			if d.Translations[loc][key] == "" {
				t.Errorf("%s: %s fehlt", loc, key)
			}
		}
	}
	for _, o := range d.Objects {
		if err := o.Validate(); err != nil {
			t.Error(err)
		}
		need(o.TitleKey)
		for _, f := range o.Fields {
			need(f.LabelKey)
			for _, opt := range f.Options {
				need(opt.LabelKey)
			}
		}
		for _, s := range o.Sections {
			need(s.TitleKey)
		}
		for _, a := range o.Actions {
			if a.Kind == metamodel.KindCustom {
				need(a.LabelKey)
			}
		}
	}
	for _, m := range d.Modules {
		need(m.TitleKey)
		need(m.DescriptionKey)
		if len(m.Services) != 1 || m.Services[0] != balanceObject {
			t.Errorf("Services: %v", m.Services)
		}
	}
	// Belege: keine generischen Schreib-Actions, Storno je Datensatz.
	for _, o := range d.Objects {
		if o.Name != entryObject {
			continue
		}
		var names []string
		for _, a := range o.Actions {
			names = append(names, a.Name)
		}
		if strings.Join(names, ",") != "list,get,post,reverse" {
			t.Fatalf("Actions: %v", names)
		}
	}
}

// items: die Datensätze einer list-Antwort.
func items(m map[string]any) []map[string]any {
	var out []map[string]any
	switch v := m["items"].(type) {
	case []map[string]any:
		return v
	case []any:
		for _, x := range v {
			out = append(out, x.(map[string]any))
		}
	}
	return out
}
