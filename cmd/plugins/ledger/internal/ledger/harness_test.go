package ledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/events"
	"github.com/camel/coremesh/pkg/sdk/hook"
	"github.com/camel/coremesh/pkg/sdk/module"

	"github.com/camel/coremesh_erp/pkg/ledgerapi"
)

// testHost: SQLite mit einer Verbindung (Fremdschlüssel an), Buchungskreise
// 1000/2000 und Rechte je Buchungskreis (Account.Check/Granted) als Attrappe.
type testHost struct {
	db      *sql.DB
	events  []events.Event                     // gemeldete SystemEvents
	granted map[string][]string                // "Object.action" → Buchungskreise ("*" = alle); fehlt = alle
	rules   map[string][]sdk.GrantRule         // "Object.action" → Regeln mit Feldwerten (vor granted)
	hook    func(req hook.Request) hook.Result // Hook-Dispatcher (nil = keiner)
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

func (h *testHost) grant(object, action string) []string {
	if g, ok := h.granted[object+"."+action]; ok {
		return g
	}
	return []string{"*"}
}

// grants: Regeln mit Feldwerten (rules) oder Buchungskreise (granted).
func (h *testHost) grants(object, action string) sdk.GrantSet {
	if rules, ok := h.rules[object+"."+action]; ok {
		return sdk.GrantSet{Rules: rules}
	}
	ccs := h.grant(object, action)
	g := sdk.GrantSet{Rules: []sdk.GrantRule{{CompanyCodes: ccs}}}
	if slices.Contains(ccs, "*") {
		g.All = true
	} else {
		g.CompanyCodes = ccs
	}
	return g
}

func (h *testHost) Handle(
	_ context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	switch req.Object + "." + req.Action {
	case "Hook.Call", "Hook.Define":
		if h.hook == nil {
			return sdk.Response{}, fmt.Errorf("%w: kein Hook-Dispatcher", sdk.ErrUnimplemented)
		}
		if req.Action == "Define" {
			return sdk.Response{}, nil
		}
		var in hook.Request
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: h.hook(in)}, nil
	case "SystemEvent.Push":
		var ev events.Event
		if err := sdk.Decode(req.Payload, &ev); err != nil {
			return sdk.Response{}, err
		}
		h.events = append(h.events, ev)
		return sdk.Response{Payload: map[string]any{"subscribers": 0}}, nil
	case "CompanyCode.get":
		if id := fmt.Sprint(p["id"]); id == "1000" || id == "2000" {
			return sdk.Response{Payload: map[string]any{"id": id, "code": id}}, nil
		}
		return sdk.Response{}, fmt.Errorf("%w: Buchungskreis %v", sdk.ErrNotFound, p["id"])
	case "Account.Check":
		attrs := sdk.Attrs{}
		if a, ok := p["attrs"].(map[string]string); ok {
			maps.Copy(attrs, a)
		}
		if cc, ok := p["company_code"].(string); ok {
			attrs[sdk.AttrCompanyCode] = cc
		}
		return sdk.Response{Payload: map[string]any{"allowed": h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"])).Allows(attrs)}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"]))}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

type env struct {
	t   *testing.T
	p   *module.Plugin
	h   *testHost
	ctx context.Context
	gl  ledgerapi.Client
}

// pluginServices leitet Client-Aufrufe direkt an das Plugin (statt Dispatcher).
type pluginServices struct{ p *module.Plugin }

func (s pluginServices) Call(ctx context.Context, object, action string, payload any) (sdk.Response, error) {
	return s.p.Handle(ctx, sdk.Request{Object: object, Action: action, Payload: payload})
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	h := &testHost{db: db, granted: map[string][]string{}}
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
	for _, s := range init.Seed { // wie DBSchema: nur fehlende Zeilen
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
	// Angemeldeter Benutzer: crud prüft dann Access (System-Anfragen dürfen alles).
	e := &env{t: t, p: p, h: h, ctx: sdk.WithCall(hctx, sdk.CallContext{RequestID: "test", UserID: "tester"})}
	e.gl = ledgerapi.New(pluginServices{p})
	return e
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

// rentCompany: SKR25 geladen, Buchungskreis 1000 (EUR) eingerichtet, 2026 offen.
func (e *env) rentCompany() {
	e.t.Helper()
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR25"})
	e.must(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "SKR25", "currency": "EUR", "year": 2026})
}

// rentInvoice: Sollstellung Miete Oktober aus dem Mietmodul.
func rentInvoice(ref string) ledgerapi.PostRequest {
	a := map[string]string{"contract": "MV-0007", "object": "WE-0001-0003"}
	return ledgerapi.PostRequest{SourceModule: "RENT", SourceReference: ref, CompanyCode: "1000", PostingDate: "2026-10-01",
		Currency: "EUR", HeaderText: "Sollstellung Miete Oktober", DocumentType: "DR", Reference: "SOLL-10-MV-0007",
		Items: []ledgerapi.Item{
			{Account: "1200", Side: ledgerapi.Debit, Amount: "1250.00", Assignments: a},
			{Account: "6000", Side: ledgerapi.Credit, Amount: "1000.00", Assignments: a},
			{Account: "2800", Side: ledgerapi.Credit, Amount: "250.00", Assignments: a, Text: "BK-Vorauszahlung"},
		}}
}
