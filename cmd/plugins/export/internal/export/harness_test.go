package export

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// testHost: SQLite (Fremdschlüssel an), Rechte als Attrappe (rules je
// "Object.action"; ohne Eintrag ist alles erlaubt) und ein DMS als Attrappe.
type testHost struct {
	db       *sql.DB
	rules    map[string][]sdk.GrantRule
	partners map[string]string
	docs     []map[string]any           // DmsDocument (abgelegt)
	content  map[string][]byte          // Inhalt je Dokument
	refs     map[string]map[string]bool // Dokument → "Object|Schlüssel"
	hidden   map[string]bool            // Dokumente ohne Leserecht
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

func (h *testHost) grants(object, action string) sdk.GrantSet {
	if rules, ok := h.rules[object+"."+action]; ok {
		return sdk.GrantSet{Rules: rules}
	}
	return sdk.GrantSet{CompanyCodeGrant: sdk.CompanyCodeGrant{All: true}, Rules: []sdk.GrantRule{{CompanyCodes: []string{"*"}}}}
}

func (h *testHost) Handle(_ context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	if p == nil {
		_ = sdk.Decode(req.Payload, &p)
	}
	switch req.Object + "." + req.Action {
	case "DmsDocument.list":
		q, _ := p["query"].(map[string]any)
		var out []any
		for _, d := range h.docs {
			if h.hidden[fmt.Sprint(d["id"])] || !docMatches(d, q, h.refs[fmt.Sprint(d["id"])]) {
				continue
			}
			out = append(out, maps.Clone(d))
		}
		return sdk.Response{Payload: map[string]any{"items": out}}, nil
	case "DmsDocument.get", "DmsDocument.content":
		for _, d := range h.docs {
			if d["id"] == p["id"] && !h.hidden[fmt.Sprint(d["id"])] {
				if req.Action == "get" {
					return sdk.Response{Payload: maps.Clone(d)}, nil
				}
				return sdk.Response{Payload: metamodel.Content{Content: base64.StdEncoding.EncodeToString(h.content[fmt.Sprint(d["id"])]),
					ContentType: "application/pdf", FileName: fmt.Sprint(d["file_name"])}}, nil
			}
		}
		return sdk.Response{}, fmt.Errorf("%w: Dokument %v", sdk.ErrNotFound, p["id"])
	case "DmsDocType.get":
		names := map[string]string{"INVOICE": "Rechnung", "BANK": "Kontoauszug", "CONTRACT": "Vertrag", "NOTICE": "Bescheid / Festsetzung"}
		if n, ok := names[fmt.Sprint(p["id"])]; ok {
			return sdk.Response{Payload: map[string]any{"code": p["id"], "name": n}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "DmsDocCategory.get":
		return sdk.Response{}, sdk.ErrNotFound
	case "BusinessPartner.get":
		name, ok := h.partners[fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"id": p["id"], "name1": name}}, nil
	case "CompanyCode.get":
		if fmt.Sprint(p["id"]) == "1000" {
			return sdk.Response{Payload: map[string]any{"code": "1000"}}, nil
		}
		return sdk.Response{}, fmt.Errorf("%w: Buchungskreis %v", sdk.ErrNotFound, p["id"])
	case "Account.Check":
		attrs := sdk.Attrs{}
		if a, ok := p["attrs"].(map[string]string); ok {
			maps.Copy(attrs, a)
		}
		return sdk.Response{Payload: map[string]any{"allowed": h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"])).Allows(attrs)}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"]))}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

// docMatches: Filter von DmsDocument.list (wie dms: Gleichheit, ref, date_from/to).
func docMatches(d, q map[string]any, refs map[string]bool) bool {
	for k, v := range q {
		s := fmt.Sprint(v)
		switch k {
		case "ref":
			if !refs[s] {
				return false
			}
		case "date_from":
			if fmt.Sprint(d["doc_date"]) < s {
				return false
			}
		case "date_to":
			if fmt.Sprint(d["doc_date"]) > s {
				return false
			}
		default:
			if fmt.Sprint(d[k]) != s {
				return false
			}
		}
	}
	return true
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
	h := &testHost{db: db, rules: map[string][]sdk.GrantRule{}, partners: map[string]string{}, content: map[string][]byte{},
		refs: map[string]map[string]bool{}, hidden: map[string]bool{}}
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
	return &env{t: t, m: mod, p: p, h: h, ctx: sdk.WithCall(hctx, sdk.CallContext{RequestID: "test", UserID: "wagner"})}
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

func items(m map[string]any) []map[string]any {
	var out []map[string]any
	for _, it := range m["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

func expect(t *testing.T, err error, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("%s: erwartet %v, bekommen %v", what, target, err)
	}
}

func (h *testHost) Read(_ context.Context, req sdk.Request, _ sdk.RowWriter) (sdk.ReadEnd, error) {
	return sdk.ReadEnd{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}
