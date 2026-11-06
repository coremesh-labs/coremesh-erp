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
	// seit 0.3.0: Vorgänge (Ereignisjournal, Abschluss, Druck ins DMS) und Eingangsrechnungen
	events    []map[string]any
	processes map[string]map[string]any
	invoices  []map[string]any
	sheets    int
	exports   int
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
	case "ProcessEvent.list":
		q, _ := p["query"].(map[string]any)
		since := toInt(q["since"])
		var out []any
		for _, ev := range h.events {
			if toInt(ev["seq"]) > since {
				out = append(out, maps.Clone(ev))
			}
		}
		return sdk.Response{Payload: map[string]any{"items": out}}, nil
	case "Process.get":
		if pr, ok := h.processes[fmt.Sprint(p["id"])]; ok {
			return sdk.Response{Payload: maps.Clone(pr)}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "Process.exportData":
		// Objektdaten wie process: Stand = changed_at, Dokumente mit Verweis
		h.exports++
		key := fmt.Sprint(p["id"])
		_, pid, _ := strings.Cut(key, "|")
		pr, ok := h.processes[key]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		stand := fmt.Sprint(pr["changed_at"])
		if pr["changed_at"] == nil {
			stand = "2026-11-06T09:00:00Z"
		}
		var docs []any
		ds, _ := p["dokumente"].([]any)
		for _, d := range ds {
			d := d.(map[string]any)
			docs = append(docs, map[string]any{"ref": d["ref"], "titel": d["titel"]})
		}
		um, _ := p["umfang"].(map[string]any)
		v := map[string]any{"nummer": pid, "betreff": pr["subject"]}
		if more, ok := pr["vorgang"].(map[string]any); ok { // ausführliches Abbild (Prüfablage)
			maps.Copy(v, more)
		}
		if len(docs) > 0 {
			v["dokumente"] = docs
		}
		data := map[string]any{"schema": "process/1", "stand": stand, "umfang": map[string]any{"code": um["code"]}, "vorgang": v}
		return sdk.Response{Payload: map[string]any{"content_type": "application/vnd.coremesh.process+json;v=1", "name": "vorgang.json", "titel": "Vorgang " + pid,
			"datum": stand[:10], "stand": stand, "partner": pr["partner_id"], "art": map[string]any{"code": "VORGANG", "name": "Vorgang"}, "data": data}}, nil
	case "Process.participants":
		pr, ok := h.processes[fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		items, _ := pr["participants"].([]any)
		if items == nil {
			items = []any{}
		}
		return sdk.Response{Payload: map[string]any{"items": items, "partner_id": pr["partner_id"]}}, nil
	case "Process.storeSheet":
		// Vorgang als PDF ins DMS (wie process: gleiche Datei, neues Dokument)
		h.sheets++
		key := fmt.Sprint(p["id"])
		_, pid, _ := strings.Cut(key, "|")
		id := fmt.Sprintf("S%d", h.sheets)
		h.docs = append(h.docs, map[string]any{"id": id, "company_code": "1000", "status": "FILED", "doc_type": "REPORT", "doc_date": "2026-11-06",
			"title": "Vorgang " + pid, "file_name": "Vorgang_" + pid + ".pdf", "name": "VG_" + id, "size": float64(10), "sha256": "",
			"filed_at": fmt.Sprintf("2026-11-06T12:%02d:00Z", h.sheets)})
		h.content[id] = []byte("%PDF-1.4 " + id)
		h.refs[id] = map[string]bool{"Process|" + key: true}
		return sdk.Response{Payload: map[string]any{"id": id}}, nil
	case "SupplierInvoice.list":
		q, _ := p["query"].(map[string]any)
		var out []any
		for _, inv := range h.invoices {
			if fmt.Sprint(inv["status"]) == fmt.Sprint(q["status"]) {
				out = append(out, maps.Clone(inv))
			}
		}
		return sdk.Response{Payload: map[string]any{"items": out}}, nil
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
		case "filed_since":
			if fmt.Sprint(d["filed_at"]) < s {
				return false
			}
		case "changed_since":
			at := fmt.Sprint(d["filed_at"])
			if r, ok := d["removed_at"]; ok && r != nil {
				at = fmt.Sprint(r)
			}
			if at < s {
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
		refs: map[string]map[string]bool{}, hidden: map[string]bool{}, processes: map[string]map[string]any{}}
	mod := New()
	p := module.NewPlugin(module.Info{Name: Name, Version: "test"}, mod)
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	hctx := sdk.WithHost(ctx, h)
	settings := map[string]any{"modules": map[string]any{Name: map[string]any{"check_minutes": -1}}} // ohne Zeitgeber
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
