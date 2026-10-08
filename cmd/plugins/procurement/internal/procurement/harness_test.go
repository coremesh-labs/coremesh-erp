package procurement

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
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// testHost: SQLite und Attrappen der anderen Plugins – Partnermodul,
// Immobilien, Hauptbuch (Konten, Feldstatus, Vorerfassung, Storno), Nummernkreise.
type testHost struct {
	db       *sql.DB
	partners map[string]string            // Partner → Name
	accounts map[string]string            // "<cc>|<Konto mit Präfix>" → Feldstatusgruppe
	status   map[string]map[string]string // Gruppe → Feld → Status
	supplier map[string]string            // "<Partner>|<cc>|<Rolle>" → Abstimmkonto ("!" = Buchungssperre)
	objects  map[string]bool              // "<Object>|<cc>|<id>"
	numbers  map[string]int
	drafts   map[string]map[string]any   // Vorerfassungen (Kopf)
	lines    map[string][]map[string]any // Vorerfassung → Positionen
	calls    []string                    // Hauptbuch-Aufrufe
	failSim  string                      // Meldung von JournalDraft.simulate
	costs    map[string][2]string        // Kostenart (Modul Betriebskosten) → Sachkonto, Art
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
	data, _ := p["data"].(map[string]any)
	switch req.Object + "." + req.Action {
	case "SystemEvent.Register", "SystemEvent.Push":
		return sdk.Response{Payload: map[string]any{}}, nil
	case "Account.Check":
		return sdk.Response{Payload: map[string]any{"allowed": true}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: sdk.GrantSet{CompanyCodeGrant: sdk.CompanyCodeGrant{All: true}, Rules: []sdk.GrantRule{{CompanyCodes: []string{"*"}}}}}, nil
	case "NumberRange.Define":
		return sdk.Response{Payload: map[string]any{"object": p["object"]}}, nil
	case "NumberRange.Next":
		var r numrange.Request
		_ = sdk.Decode(req.Payload, &r)
		key := r.Object + "|" + r.CompanyCode + "|" + r.Key
		h.numbers[key]++
		prefix := "AN"
		if r.Object == rangeInvoice {
			prefix = r.Key
		}
		return sdk.Response{Payload: numrange.Result{Number: fmt.Sprintf("%s-%d-%05d", prefix, r.Year, h.numbers[key])}}, nil
	case "CostCategory.get":
		id := fmt.Sprint(p["id"])
		c, ok := h.costs[strings.TrimPrefix(id, "1000|")]
		if !ok || !strings.HasPrefix(id, "1000|") {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"code": strings.TrimPrefix(id, "1000|"), "name": "Kostenart " + id, "account_number": c[0],
			"cost_type": c[1], "is_active": true}}, nil
	case "Currency.get":
		return sdk.Response{Payload: map[string]any{"code": p["id"], "decimals": 2}}, nil
	case "BusinessPartner.get":
		name, ok := h.partners[fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"id": p["id"], "name1": name, "search_term": strings.ToUpper(name)}}, nil
	case "RentObject.get", "Building.get", "BusinessEntity.get":
		if !h.objects[req.Object+"|"+fmt.Sprint(p["id"])] {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"designation": "Objekt"}}, nil
	case "GLAccountCompany.list":
		nr := fmt.Sprint(q["account_number"])
		if !strings.Contains(nr, "-") {
			nr = "SKR25-" + nr
		}
		grp, ok := h.accounts[fmt.Sprint(q["company_code_id"])+"|"+nr]
		if !ok {
			return sdk.Response{Payload: map[string]any{"items": []any{}}}, nil
		}
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"account_number": nr, "field_status_group": grp}}}}, nil
	case "FieldStatus.list":
		items := []any{}
		for f, s := range h.status[fmt.Sprint(q["group_id"])] {
			items = append(items, map[string]any{"field_name": f, "status": s})
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "PartnerCompanyCode.list":
		items := []any{}
		if acc, ok := h.supplier[fmt.Sprint(q["bp_id"])+"|"+fmt.Sprint(q["company_code"])+"|"+fmt.Sprint(q["role_code"])]; ok {
			items = append(items, map[string]any{"reconciliation_account": strings.TrimPrefix(acc, "!"), "posting_block": strings.HasPrefix(acc, "!")})
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "JournalDraft.create":
		id := fmt.Sprintf("D%d", len(h.drafts)+1)
		h.drafts[id] = data
		h.calls = append(h.calls, "create "+id)
		return sdk.Response{Payload: map[string]any{"id": id}}, nil
	case "JournalDraftItem.create":
		id := fmt.Sprint(data["draft_id"])
		h.lines[id] = append(h.lines[id], data)
		return sdk.Response{Payload: map[string]any{}}, nil
	case "JournalDraft.simulate":
		h.calls = append(h.calls, "simulate "+fmt.Sprint(p["id"]))
		if h.failSim != "" {
			return sdk.Response{}, fmt.Errorf("%w: %s", sdk.ErrInvalidArgument, h.failSim)
		}
		return sdk.Response{Payload: map[string]any{}}, nil
	case "JournalDraft.post":
		h.calls = append(h.calls, "post "+fmt.Sprint(p["id"]))
		return sdk.Response{Payload: map[string]any{"id": "1000|2026|1000000099", "document_number": "1000000099"}}, nil
	case "JournalDraft.deactivate":
		h.calls = append(h.calls, "deactivate "+fmt.Sprint(p["id"]))
		return sdk.Response{Payload: map[string]any{}}, nil
	case "JournalEntry.reverse":
		h.calls = append(h.calls, "reverse "+fmt.Sprint(p["id"])+" "+fmt.Sprint(data["posting_date"]))
		return sdk.Response{Payload: map[string]any{"id": "1000|2026|1000000100", "document_number": "1000000100"}}, nil
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
	h := &testHost{db: db, partners: map[string]string{"HW1": "Klempner Schulze", "HW2": "Maler Weiß"},
		accounts: map[string]string{"1000|SKR25-6300": "COST", "1000|SKR25-6400": "COST", "1000|SKR25-2900": "SUPPLIER"},
		status: map[string]map[string]string{
			"COST":     {"rent_object_id": "OPTIONAL", "dimension_custom_1": "OPTIONAL", "cost_center": "OPTIONAL", "supplier_id": "SUPPRESS"},
			"SUPPLIER": {"supplier_id": "REQUIRED", "rent_object_id": "OPTIONAL"},
		},
		supplier: map[string]string{"HW1|1000|CREDITOR": "2900", "HW2|1000|CREDITOR": "!2900"},
		objects:  map[string]bool{"RentObject|1000|LpzBrn1WG001": true, "Building|1000|LpzBrn1": true},
		numbers:  map[string]int{}, drafts: map[string]map[string]any{}, lines: map[string][]map[string]any{},
		costs: map[string][2]string{"INSTAND": {"SKR25-6300", "NON_ALLOCABLE"}, "WASSER": {"SKR25-6400", "ALLOCABLE"}, "OHNEKONTO": {"", "ALLOCABLE"}}}
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
