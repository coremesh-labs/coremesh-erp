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
	billing  map[string]any  // Antwort von ContractBilling.computeOpCost
	request  map[string]any  // letzte Anfrage an computeOpCost
	drafts   []map[string]any
	items    []map[string]any
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
	case "Building.get":
		if p["id"] == "1000|GEB1" {
			return sdk.Response{Payload: map[string]any{"building_id": "GEB1"}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "ContractBilling.computeOpCost":
		if h.billing == nil {
			return sdk.Response{}, sdk.ErrUnimplemented
		}
		h.request = p
		return sdk.Response{Payload: h.billing}, nil
	case "Contract.get":
		return sdk.Response{Payload: map[string]any{"contract_type": "MV"}}, nil
	case "ContractType.get":
		return sdk.Response{Payload: map[string]any{"main_role": "TENANT"}}, nil
	case "PartnerCompanyCode.list":
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"reconciliation_account": "1200"}}}}, nil
	case "FieldStatus.list":
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"field_name": "rent_contract_id", "status": "OPTIONAL"}, map[string]any{"field_name": "rent_object_id", "status": "REQUIRED"}}}}, nil
	case "JournalDraft.create":
		d, _ := p["data"].(map[string]any)
		h.drafts = append(h.drafts, d)
		return sdk.Response{Payload: map[string]any{"id": fmt.Sprintf("D%d", len(h.drafts))}}, nil
	case "JournalDraftItem.create":
		d, _ := p["data"].(map[string]any)
		h.items = append(h.items, d)
		return sdk.Response{Payload: d}, nil
	case "JournalDraft.simulate":
		return sdk.Response{Payload: map[string]any{}}, nil
	case "JournalDraft.post":
		return sdk.Response{Payload: map[string]any{"id": "E1", "document_number": "1800000001"}}, nil
	case "GLAccountCompany.list":
		nr := fmt.Sprint(q["account_number"])
		nr = strings.TrimPrefix(nr, "SKR25-")
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
	h := &testHost{db: db, accounts: map[string]bool{"7000": true, "1550": true, "2800": true, "6100": true}}
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
