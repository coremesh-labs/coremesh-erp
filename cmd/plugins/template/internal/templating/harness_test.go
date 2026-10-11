package templating

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// testHost: SQLite und Attrappen – Rechte (deny: verbotene "Object.action"),
// Nummernkreis, Events.
type testHost struct {
	db      *sql.DB
	mu      sync.Mutex
	numbers map[string]int
	events  []map[string]any
	deny    map[string]bool
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

func (h *testHost) Read(context.Context, sdk.Request, sdk.RowWriter) (sdk.ReadEnd, error) {
	return sdk.ReadEnd{}, sdk.ErrUnimplemented
}

func (h *testHost) Handle(_ context.Context, req sdk.Request) (sdk.Response, error) {
	var p map[string]any
	_ = sdk.Decode(req.Payload, &p)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch req.Object + "." + req.Action {
	case "Account.Check":
		return sdk.Response{Payload: map[string]any{"allowed": !h.deny[fmt.Sprint(p["object"])+"."+fmt.Sprint(p["action"])]}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: sdk.GrantSet{CompanyCodeGrant: sdk.CompanyCodeGrant{All: true}, Rules: []sdk.GrantRule{{CompanyCodes: []string{"*"}}}}}, nil
	case "NumberRange.Define":
		return sdk.Response{Payload: map[string]any{}}, nil
	case "NumberRange.Next":
		cc := fmt.Sprint(p["company_code"])
		h.numbers[cc]++
		return sdk.Response{Payload: map[string]any{"number": fmt.Sprintf("%s-2026-%06d", cc, h.numbers[cc])}}, nil
	case "SystemEvent.Push":
		h.events = append(h.events, p)
		return sdk.Response{Payload: map[string]any{"subscribers": 0}}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

// statuses: gemeldete Status eines Auftrags.
func (h *testHost) statuses(job string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, ev := range h.events {
		d, _ := ev["data"].(map[string]any)
		if ev["entity_id"] == job && d != nil {
			out = append(out, fmt.Sprint(d["status"]))
		}
	}
	return out
}

// fakeTex: Attrappe von Tectonic – merkt sich den Quelltext, schreibt PDF und Protokoll.
type fakeTex struct {
	mu     sync.Mutex
	calls  int
	tex    []string
	files  [][]string                             // Dateien im Arbeitsverzeichnis je Lauf
	answer func(call int) (out string, err error) // nil: Erfolg
}

func (f *fakeTex) run(_ context.Context, dir, _ string, _, _ []string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	b, _ := os.ReadFile(filepath.Join(dir, "main.tex"))
	f.tex = append(f.tex, string(b))
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	f.files = append(f.files, names)
	_ = os.WriteFile(filepath.Join(dir, "main.log"), []byte("This is XeTeX (fake)\n"), 0o644)
	if f.answer != nil {
		if out, err := f.answer(f.calls); err != nil {
			return []byte(out), err
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "main.pdf"), []byte("%PDF-1.5 fake"), 0o644)
	return []byte("note: Writing `main.pdf`"), nil
}

type env struct {
	t   *testing.T
	m   *Module
	p   *module.Plugin
	h   *testHost
	tex *fakeTex
	dir string // freigegebenes Verzeichnis
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
	h := &testHost{db: db, numbers: map[string]int{}, deny: map[string]bool{}}
	mod := New()
	tex := &fakeTex{}
	mod.tex = tex.run
	p := module.NewPlugin(module.Info{Name: Name, Version: "test"}, mod)
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	hctx := sdk.WithHost(ctx, h)
	settings := map[string]any{"modules": map[string]any{Name: map[string]any{"sync": true, "allowed_dirs": []any{dir},
		"tectonic_path": "/fake/tectonic", "cache_dir": filepath.Join(t.TempDir(), "cache"), "retry_wait": "1ms"}}}
	if err := p.Configure(hctx, sdk.Config{Host: h, Settings: settings}); err != nil {
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
	return &env{t: t, m: mod, p: p, h: h, tex: tex, dir: dir, ctx: sdk.WithCall(hctx, sdk.CallContext{RequestID: "test", UserID: "tester"})}
}

func (e *env) call(object, action string, payload any) (map[string]any, error) {
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: object, Action: action, Payload: payload})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (e *env) must(object, action string, payload any) map[string]any {
	e.t.Helper()
	out, err := e.call(object, action, payload)
	if err != nil {
		e.t.Fatalf("%s.%s: %v", object, action, err)
	}
	return out
}

// bundle legt ein Bundle mit Dateien an (Name → Inhalt).
func (e *env) bundle(code, kind, parent string, files map[string]string) {
	e.t.Helper()
	e.must(bundleObject, "create", map[string]any{"data": map[string]any{"code": code, "name": code, "kind": kind, "parent": parent}})
	var fs []zipFile
	for n, c := range files {
		fs = append(fs, zipFile{Name: n, Data: []byte(c)})
	}
	if err := e.m.setBundleFiles(e.ctx, code, fs); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) jobRow(nr string) map[string]any {
	e.t.Helper()
	return e.must(jobObject, "get", map[string]any{"id": nr})
}

func (e *env) jobFiles(nr string) []string {
	e.t.Helper()
	res, err := e.m.db.Query(e.ctx, `SELECT name FROM template__job_file WHERE job_number = ? ORDER BY name`, nr)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, fmt.Sprint(r[0]))
	}
	return out
}
