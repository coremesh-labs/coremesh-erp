package opcost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// testHost: SQLite und Attrappen der anderen Plugins – Partnermodul,
// Immobilien, Hauptbuch (Konten, Feldstatus, Vorerfassung, Storno), Nummernkreise.
type testHost struct {
	db       *sql.DB
	accounts map[string]bool // Sachkonten im Buchungskreis 1000 (mit Präfix)
}

func (h *testHost) Log(context.Context, sdk.LogLevel, string, map[string]string) error { return nil }

func (h *testHost) Query(ctx context.Context, _ string, q string, args ...any) (*sdk.QueryResult, error) {
	rows, err := h.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := &sdk.QueryResult{Columns: cols}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, vals)
	}
	return out, rows.Err()
}

func (h *testHost) Exec(ctx context.Context, _ string, q string, args ...any) (sdk.ExecResult, error) {
	res, err := h.db.ExecContext(ctx, q, args...)
	if err != nil {
		return sdk.ExecResult{}, err
	}
	n, _ := res.RowsAffected()
	return sdk.ExecResult{RowsAffected: n}, nil
}

func (h *testHost) BeginTx(context.Context, string, sql.TxOptions) (string, error) { return "tx", nil }
func (h *testHost) CommitTx(context.Context, string) error                         { return nil }
func (h *testHost) RollbackTx(context.Context, string) error                       { return nil }

func (h *testHost) Handle(_ context.Context, req sdk.Request) (sdk.Response, error) {
	var p map[string]any
	_ = sdk.Decode(req.Payload, &p)
	q, _ := p["query"].(map[string]any)
	switch req.Object + "." + req.Action {
	case "Account.Check":
		return sdk.Response{Payload: map[string]any{"allowed": true}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: sdk.GrantSet{CompanyCodeGrant: sdk.CompanyCodeGrant{All: true}, Rules: []sdk.GrantRule{{CompanyCodes: []string{"*"}}}}}, nil
	case "GLAccountCompany.list":
		nr := fmt.Sprint(q["account_number"])
		if !strings.Contains(nr, "-") {
			nr = "SKR25-" + nr
		}
		if !h.accounts[nr] {
			return sdk.Response{Payload: map[string]any{"items": []any{}}}, nil
		}
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"account_number": nr}}}}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

type env struct {
	t   *testing.T
	m   *Module
	p   *module.Plugin
	h   *testHost
	ctx context.Context
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
	h := &testHost{db: db, accounts: map[string]bool{"SKR25-7000": true, "SKR25-1550": true}}
	mod := New()
	p := module.NewPlugin(module.Info{Name: Name, Version: "test"}, mod)
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	hctx := sdk.WithHost(ctx, h)
	if err := p.Configure(hctx, sdk.Config{Host: h}); err != nil {
		t.Fatal(err)
	}
	resp, err := p.Handle(ctx, sdk.Request{Object: sdk.ObjectDBSchema, Action: sdk.ActionInit, Payload: sdk.SchemaInitRequest{Module: Name}})
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
		if !strings.HasPrefix(tb.Name, Name+"__") {
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
	return &env{t: t, m: mod, p: p, h: h, ctx: sdk.WithCall(hctx, sdk.CallContext{RequestID: "test", UserID: "tester"})}
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

func (e *env) create(object string, data map[string]any) map[string]any {
	e.t.Helper()
	return e.must(object, "create", map[string]any{"data": data})
}

func (e *env) try(object string, data map[string]any) error {
	_, err := e.call(object, "create", map[string]any{"data": data})
	return err
}

func expect(t *testing.T, err error, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: erwartet %v, bekommen %v", what, target, err)
	}
}

func (h *testHost) Read(context.Context, sdk.Request, sdk.RowWriter) (sdk.ReadEnd, error) {
	return sdk.ReadEnd{}, sdk.ErrUnimplemented
}
