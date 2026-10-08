package bank

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

// testHost: SQLite und Attrappen der anderen Plugins – Hauptbuch (Konten,
// Feldstatus, Vorerfassung), Partner, Verträge, Beschaffung.
type testHost struct {
	db       *sql.DB
	accounts map[string]bool // Sachkonten im Buchungskreis 1000 (mit Präfix)
	drafts   []map[string]any
	items    map[string][]map[string]any // Vorerfassung → Positionen
	failPost string                      // Vorerfassung mit diesem Kopftext scheitert
	bankIBAN string                      // IBAN des Partners P9 (PartnerBankDetail)
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

// Stammdaten der Attrappe.
var (
	testContracts = []any{
		map[string]any{"contract_id": "MV-2026-0001", "external_number": "MV-MUS-001", "partner_id": "P1", "contract_type": "MV", "status": "ACTIVE",
			"valid_from": "2026-01-01", "valid_to": "9999-12-31"},
		map[string]any{"contract_id": "WH-2026-0001", "partner_id": "W1", "contract_type": "WH", "status": "ACTIVE", "valid_from": "2026-01-01"},
	}
	testPartners = []any{
		map[string]any{"id": "P1", "name1": "Muster", "name2": "Anna"},
		map[string]any{"id": "P2", "name1": "Beispiel", "name2": "Max"},
		map[string]any{"id": "P3", "name1": "Stadtwerke Musterstadt", "name2": ""},
		map[string]any{"id": "W1", "name1": "WEG Musterweg", "name2": ""},
	}
	testRoles = map[string]string{"P1": "TENANT", "P2": "TENANT", "P3": "CREDITOR", "W1": "CREDITOR"}
)

func (h *testHost) Handle(_ context.Context, req sdk.Request) (sdk.Response, error) {
	var p map[string]any
	_ = sdk.Decode(req.Payload, &p)
	q, _ := p["query"].(map[string]any)
	list := func(items ...any) (sdk.Response, error) {
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	}
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
			return list()
		}
		group := map[string]string{"SKR25-1200": "CUSTOMER", "SKR25-2900": "SUPPLIER"}[nr]
		return list(map[string]any{"account_number": nr, "field_status_group": group})
	case "FieldStatus.list":
		switch q["group_id"] {
		case "CUSTOMER":
			return list(map[string]any{"field_name": "sd_customer_id", "status": "REQUIRED"}, map[string]any{"field_name": "rent_contract_id", "status": "OPTIONAL"})
		case "SUPPLIER":
			return list(map[string]any{"field_name": "supplier_id", "status": "REQUIRED"})
		}
		return list()
	case "Contract.list":
		return list(testContracts...)
	case "Contract.get":
		for _, c := range testContracts {
			if c := c.(map[string]any); "1000|"+c["contract_id"].(string) == p["id"] {
				return sdk.Response{Payload: c}, nil
			}
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "ContractType.list":
		return list(map[string]any{"code": "MV", "direction": "RECEIVABLE"}, map[string]any{"code": "WH", "direction": "PAYABLE"})
	case "ContractType.get":
		role := map[string]string{"1000|MV": "TENANT", "1000|WH": "CREDITOR"}[fmt.Sprint(p["id"])]
		return sdk.Response{Payload: map[string]any{"main_role": role}}, nil
	case "ContractPartner.list":
		return list(map[string]any{"contract_id": "MV-2026-0001", "partner_id": "P2", "role_code": "TENANT"})
	case "BusinessPartner.list":
		return list(testPartners...)
	case "BusinessPartner.get":
		for _, x := range testPartners {
			if x.(map[string]any)["id"] == p["id"] {
				return sdk.Response{Payload: x}, nil
			}
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "PartnerBankDetail.list":
		return list(map[string]any{"bp_id": "P3", "iban": "DE02120300000000202051"}, map[string]any{"bp_id": "P9", "iban": h.bankIBAN})
	case "PartnerCompanyCode.list":
		var out []any
		for bp, role := range testRoles {
			if (q["bp_id"] == nil || q["bp_id"] == bp) && (q["role_code"] == nil || q["role_code"] == role) {
				acc := map[string]string{"TENANT": "SKR25-1200", "CREDITOR": "SKR25-2900"}[role]
				out = append(out, map[string]any{"bp_id": bp, "role_code": role, "reconciliation_account": acc})
			}
		}
		return list(out...)
	case "PartnerRoleType.list":
		return list(map[string]any{"code": "TENANT", "is_debitor": true}, map[string]any{"code": "CREDITOR", "is_debitor": false})
	case "PartnerRoleType.get":
		return sdk.Response{Payload: map[string]any{"is_debitor": p["id"] == "TENANT"}}, nil
	case "SupplierInvoice.list":
		return list(map[string]any{"invoice_id": "ER-2026-00001", "supplier_id": "P3", "supplier_reference": "RE-4711", "status": "POSTED", "total": "80.00"})
	case "SupplierInvoice.get":
		return sdk.Response{Payload: map[string]any{"invoice_id": "ER-2026-00001", "supplier_id": "P3", "status": "POSTED", "invoice_type": "ER"}}, nil
	case "InvoiceType.get":
		return sdk.Response{Payload: map[string]any{"supplier_role": "CREDITOR"}}, nil
	case "JournalDraft.create":
		d, _ := p["data"].(map[string]any)
		h.drafts = append(h.drafts, d)
		return sdk.Response{Payload: map[string]any{"id": fmt.Sprintf("D%d", len(h.drafts))}}, nil
	case "JournalDraftItem.create":
		d, _ := p["data"].(map[string]any)
		id := fmt.Sprint(d["draft_id"])
		h.items[id] = append(h.items[id], d)
		return sdk.Response{Payload: d}, nil
	case "JournalDraft.simulate":
		return sdk.Response{Payload: map[string]any{}}, nil
	case "JournalDraft.post":
		n := strings.TrimPrefix(fmt.Sprint(p["id"]), "D")
		for _, d := range h.drafts {
			if h.failPost != "" && strings.Contains(fmt.Sprint(d["header_text"]), h.failPost) && fmt.Sprintf("D%d", indexOf(h.drafts, d)+1) == p["id"] {
				return sdk.Response{}, fmt.Errorf("%w: Periode gesperrt", sdk.ErrInvalidArgument)
			}
		}
		return sdk.Response{Payload: map[string]any{"id": "E" + n, "document_number": "15000000" + n}}, nil
	}
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

func indexOf(ds []map[string]any, d map[string]any) int {
	for i := range ds {
		if fmt.Sprint(ds[i]) == fmt.Sprint(d) {
			return i
		}
	}
	return -1
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
	h := &testHost{db: db, accounts: map[string]bool{"SKR25-1800": true, "SKR25-1200": true, "SKR25-2900": true, "SKR25-6855": true},
		items: map[string][]map[string]any{}}
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

func items(m map[string]any) []map[string]any {
	l, _ := m["items"].([]any)
	out := make([]map[string]any, 0, len(l))
	for _, x := range l {
		out = append(out, x.(map[string]any))
	}
	return out
}

func sprintf(v any) string { return fmt.Sprint(v) }
