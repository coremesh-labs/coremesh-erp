package dms

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	_ "modernc.org/sqlite"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/docservice"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// testHost: SQLite und Attrappen der anderen Plugins – Partner, Verträge,
// Gebäude, Eingangsrechnungen, Sollstellungen, Dokumentverweise.
type testHost struct {
	db           *sql.DB
	mu           sync.Mutex
	attached     []docservice.AttachRequest
	detached     []string
	invoices     []map[string]any // SupplierInvoice.list
	items        []map[string]any // SupplierInvoiceItem.list
	postings     []map[string]any // ContractPosting.list
	self         *module.Plugin   // das Plugin selbst (Aufrufe eigener Objects)
	aiCalls      []sdk.Request    // Aufrufe des Moduls ai
	fromTemplate []map[string]any // SupplierInvoice.fromTemplate
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

var (
	testPartners = []map[string]any{
		{"id": "P1", "name1": "Stadt Görlitz", "search_term": "Stadt Görlitz"},
		{"id": "P2", "name1": "Pfitzner", "search_term": ""},
		{"id": "P3", "name1": "Haus und Grundstücksservice GmbH", "search_term": "HAUSUNDGRUNDSTUECKSS", "abbreviation": "HUG"},
	}
	testObjects = map[string][]string{
		"Contract":        {"2000|GS-2026-0009", "2000|WH-2026-0001"},
		"Building":        {"2000|ROGoer14"},
		"RentObject":      {"2000|RUG1402"},
		"SupplierInvoice": {"2000|ER-00007"},
		"JournalDraft":    {"D1", "D2", "D9"},
	}
)

func (h *testHost) Handle(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var p map[string]any
	_ = sdk.Decode(req.Payload, &p)
	q, _ := p["query"].(map[string]any)
	list := func(items ...map[string]any) (sdk.Response, error) {
		out := make([]any, len(items))
		for i, it := range items {
			out[i] = it
		}
		return sdk.Response{Payload: map[string]any{"items": out}}, nil
	}
	if strings.HasPrefix(req.Object, "Dms") && h.self != nil {
		return h.self.Handle(ctx, req) // eigene Objects über den Host wie im Betrieb
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	switch req.Object + "." + req.Action {
	case "Account.Check":
		return sdk.Response{Payload: map[string]any{"allowed": true}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: sdk.GrantSet{CompanyCodeGrant: sdk.CompanyCodeGrant{All: true}, Rules: []sdk.GrantRule{{CompanyCodes: []string{"*"}}}}}, nil
	case "BusinessPartner.get":
		for _, x := range testPartners {
			if x["id"] == p["id"] {
				return sdk.Response{Payload: x}, nil
			}
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "BusinessPartner.list":
		return list(testPartners...)
	case "Ai.run", "Ai.form", "Ai.collect", "Ai.create":
		h.aiCalls = append(h.aiCalls, sdk.Request{Object: req.Object, Action: req.Action, Payload: p})
		switch req.Action {
		case "run":
			return sdk.Response{Payload: map[string]any{"log_id": "KI-1", "status": "DONE", "message": "Struktur gefüllt",
				"result": map[string]any{"partner": map[string]any{"name1": "Neue GmbH"}}}}, nil
		case "form":
			return sdk.Response{Payload: map[string]any{"fields": []any{map[string]any{"key": "f__partner__name1", "label": "Name 1", "type": "text", "group": "Partner"}},
				"values": map[string]any{"f__partner__name1": "Neue GmbH"}}}, nil
		case "collect":
			return sdk.Response{Payload: map[string]any{"result": map[string]any{"partner": map[string]any{"name1": p["values"].(map[string]any)["f__partner__name1"]}}}}, nil
		}
		return sdk.Response{Payload: map[string]any{"object": "BusinessPartner", "id": "BPNEW"}}, nil
	case "SupplierInvoice.contractDraft":
		return sdk.Response{Payload: map[string]any{"result": map[string]any{
			"invoice": map[string]any{"framework_contract": p["contract_id"], "object_id": "OBJ1", "header_text": "Vertrag"},
			"items":   []any{map[string]any{"item_text": "Wartung", "amount": "100.00", "account_number": "6350"}}}}}, nil
	case "SupplierInvoice.list":
		var out []map[string]any
		for _, inv := range h.invoices {
			if (q["q"] == nil || strings.Contains(fmt.Sprint(inv["id"]), fmt.Sprint(q["q"]))) &&
				(q["supplier_id"] == nil || q["supplier_id"] == inv["supplier_id"]) {
				out = append(out, inv)
			}
		}
		return list(out...)
	case "SupplierInvoice.get":
		for _, inv := range h.invoices {
			if inv["id"] == p["id"] {
				return sdk.Response{Payload: inv}, nil
			}
		}
		if p["id"] == "2000|ER-00007" {
			return sdk.Response{Payload: map[string]any{"id": p["id"]}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "SupplierInvoiceItem.list":
		var out []map[string]any
		for _, it := range h.items {
			if it["invoice_id"] == q["invoice_id"] {
				out = append(out, it)
			}
		}
		return list(out...)
	case "JournalDraft.list":
		if q["status"] != "DRAFT" || q["company_code_id"] != "2000" {
			return list()
		}
		return list(map[string]any{"id": "D9", "header_text": "Straßenbahnticket", "document_date": "2026-09-30", "status": "DRAFT"})
	case "JournalDraft.get":
		if p["id"] == "D9" {
			return sdk.Response{Payload: map[string]any{"id": "D9", "header_text": "Straßenbahnticket", "document_date": "2026-09-30", "status": "DRAFT"}}, nil
		}
		if p["id"] == "D1" || p["id"] == "D2" {
			return sdk.Response{Payload: map[string]any{"id": p["id"]}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "JournalDraftItem.list":
		if q["draft_id"] != "D9" {
			return list()
		}
		return list(map[string]any{"draft_id": "D9", "account_number": "6673"}, map[string]any{"draft_id": "D9", "account_number": "1600"})
	case "GLAccountCompany.list":
		return list(map[string]any{"account_number": q["account_number"]})
	case "PartnerBankDetail.list":
		return list(map[string]any{"bp_id": "P2", "iban": "DE89370400440532013000"}, map[string]any{"bp_id": "P1", "iban": "DE02120300000000202051"})
	case "PartnerContact.list":
		return list(map[string]any{"bp_id": "P3", "comm_type_code": "TEL", "value": "+49 351 1234567"}, map[string]any{"bp_id": "P1", "value": "info@goerlitz.de"})
	case "Contract.list":
		return list(map[string]any{"id": "2000|WH-2026-0001", "contract_id": "WH-2026-0001", "partner_id": "P2", "customer_number": "KD-4711-08"},
			map[string]any{"id": "2000|GS-2026-0009", "contract_id": "GS-2026-0009", "partner_id": "P1", "external_number": "01-10528932-002-0001"})
	case "ContractCondition.list":
		if q["contract_id"] == "WH-2026-0001" {
			return list(map[string]any{"contract_id": "WH-2026-0001", "condition_type": "RT", "object_id": "", "is_template": true,
				"account_number": "6320", "note": "Reparatur", "_labels": map[string]any{"amount": "150.00 EUR"}},
				map[string]any{"contract_id": "WH-2026-0001", "condition_type": "HG", "is_template": false})
		}
		return list()
	case "SupplierInvoice.fromTemplate":
		h.fromTemplate = append(h.fromTemplate, p)
		return sdk.Response{Payload: map[string]any{"id": "2000|ER-77", "invoice_id": "ER-77"}}, nil
	case "ContractPosting.list":
		return list(h.postings...)
	case "Documents.attach":
		var in docservice.AttachRequest
		_ = sdk.Decode(req.Payload, &in)
		h.attached = append(h.attached, in)
		return sdk.Response{Payload: docservice.Document{ID: fmt.Sprintf("R%d", len(h.attached))}}, nil
	case "Documents.detach":
		h.detached = append(h.detached, fmt.Sprint(p["id"]))
		return sdk.Response{Payload: map[string]any{}}, nil
	}
	if req.Action == "list" && testObjects[req.Object] != nil {
		var out []map[string]any
		for _, k := range testObjects[req.Object] {
			if qs := fmt.Sprint(q["q"]); q["q"] != nil && !strings.Contains(k, qs) {
				continue
			}
			out = append(out, map[string]any{"id": k, "designation": "Bezeichnung " + k})
		}
		return list(out...)
	}
	if req.Object+"."+req.Action == "ContractObject.list" {
		if q["contract_id"] == "WH-2026-0001" {
			return list(map[string]any{"contract_id": "WH-2026-0001", "object_type": "RentObject", "object_id": "RUG1402", "is_main": true})
		}
		return list()
	}
	if req.Action == "get" {
		for _, k := range testObjects[req.Object] {
			if k == p["id"] {
				rec := map[string]any{"id": k}
				switch k {
				case "2000|ROGoer14":
					rec["designation"] = "Biesnitzer Str. 14"
				case "2000|RUG1402":
					rec["building_id"] = "ROGoer14"
				}
				return sdk.Response{Payload: rec}, nil
			}
		}
		return sdk.Response{}, fmt.Errorf("%w: %s %v", sdk.ErrNotFound, req.Object, p["id"])
	}
	_ = q
	return sdk.Response{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}

// fakeRunner: Texterkennung ohne Programme – pdftotext liefert text.
type fakeRunner struct {
	text    string // pdftotext
	ocrText string // pdftotext auf eine aufbereitete Datei (beginnt mit %PDF-OCR)
	langs   string // Ausgabe von tesseract --list-langs (leer = chi_sim, eng, osd)
	calls   []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	if !(name == "tesseract" && len(args) == 1 && args[0] == "--list-langs") { // Sprachabfrage nicht mitzählen
		f.calls = append(f.calls, strings.Join(append([]string{name}, args...), " "))
	}
	switch name {
	case "pdftotext":
		if b, err := os.ReadFile(args[len(args)-2]); err == nil && bytes.HasPrefix(b, []byte("%PDF-OCR")) {
			return []byte(f.ocrText), nil
		}
		return []byte(f.text), nil
	case "tesseract":
		if len(args) == 1 && args[0] == "--list-langs" {
			return []byte(firstOf(f.langs, "List of available languages in \"/usr/share/tesseract-ocr/5/tessdata/\" (3):\nchi_sim\neng\nosd\n")), nil
		}
	case "ocrmypdf":
		// ocrmypdf … {in} {out}: aufbereitete Datei schreiben
		in, out := args[len(args)-2], args[len(args)-1]
		b, _ := os.ReadFile(in)
		return nil, os.WriteFile(out, append([]byte("%PDF-OCR\n"), b...), 0o600)
	}
	return nil, nil
}

// fakeHTTP: KI-Dienst mit fester Antwort.
type fakeHTTP struct {
	answer string
	body   string
}

func (f *fakeHTTP) Do(r *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(r.Body)
	f.body = string(b)
	if r.Header.Get("Authorization") != "Bearer sk-test" {
		return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("unauthorized"))}, nil
	}
	resp := fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, f.answer)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte(resp)))}, nil
}

type env struct {
	t    *testing.T
	m    *Module
	p    *module.Plugin
	h    *testHost
	run  *fakeRunner
	ai   *fakeHTTP
	ctx  context.Context
	root string // Archiv
	in   string // Eingangsverzeichnis
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	// Gesperrte Archiv-Verzeichnisse (0555) vor dem Aufräumen wieder freigeben.
	t.Cleanup(func() {
		filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err == nil && e.IsDir() {
				os.Chmod(p, 0o755)
			}
			return nil
		})
	})
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "t.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	h := &testHost{db: db}
	mod := New()
	run, ai := &fakeRunner{}, &fakeHTTP{}
	mod.run, mod.http = run, ai
	mod.getenv = func(k string) string {
		if k == "OPENAI_API_KEY" {
			return "sk-test"
		}
		return ""
	}
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
	for _, s := range init.Seed {
		for _, r := range s.Rows {
			var cols, marks []string
			var args []any
			for k, v := range r {
				cols, marks, args = append(cols, k), append(marks, "?"), append(args, v)
			}
			if _, err := db.Exec("INSERT INTO "+s.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+strings.Join(marks, ", ")+")", args...); err != nil {
				t.Fatalf("Seed %s: %v", s.Table, err)
			}
		}
	}
	h.self = p
	e := &env{t: t, m: mod, p: p, h: h, run: run, ai: ai, root: filepath.Join(dir, "archiv"), in: filepath.Join(dir, "eingang"),
		ctx: sdk.WithCall(hctx, sdk.CallContext{RequestID: "test", UserID: "tester", Metadata: map[string]string{"locale": "de-DE", "username": "tester"}})}
	return e
}

func (e *env) call(object, action string, payload any) (map[string]any, error) {
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: object, Action: action, Payload: payload})
	if err != nil {
		return nil, err
	}
	var m map[string]any
	_ = sdk.Decode(resp.Payload, &m)
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

func (e *env) one(q string, args ...any) any {
	e.t.Helper()
	var v any
	if err := e.h.db.QueryRow(q, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return v
}

// testPlan: kleine Mindmap wie der Ablageplan der Immobilienakte (ausgedacht).
const testPlan = `<map version="0.9.0">
  <node TEXT="OBJEKTAKTE (TOP)">
    <node TEXT="3 Steuern Lasten Abgaben">
      <node TEXT="3.0 Grundsteuer">
        <node TEXT="3.0.0 Einheitswertbescheide"/>
        <node TEXT="3.0.1 Grundsteuerbescheide (Jahresbescheide)"/>
      </node>
      <node TEXT="3.1 Kommunale Gebühren">
        <node TEXT="3.1.1 Straßenreinigung Winterdienst"/>
      </node>
    </node>
    <node TEXT="4 Finanzen Bankwesen">
      <node TEXT="4.2 Buchhaltung">
        <node TEXT="4.2.0 Eingangsrechnungen (Kreditorisch)"/>
      </node>
    </node>
    <node TEXT="5 Dienstleistungen Verträge">
      <node TEXT="5.2 Management">
        <node TEXT="5.2.3 WEG Verwaltung">
          <node TEXT="5.2.3.2 Wirtschaftsplan"/>
        </node>
      </node>
    </node>
  </node>
</map>`

// company: Plan IMMO, Archiv, Buchungskreis 2000 (Kürzel VV), Standard-Workflow.
func (e *env) company() {
	e.t.Helper()
	e.must(setupObject, "importPlan", map[string]any{"plan": "IMMO", "file": testPlan})
	e.must(setupObject, "setupCompany", map[string]any{"company": "2000", "plan": "IMMO", "archive": "MAIN", "root": e.root, "short_name": "VV"})
}

// pdf: kleiner PDF-Inhalt (Byte-genau prüfbar, auch Nicht-UTF-8).
func pdf(tag string) []byte {
	return append([]byte("%PDF-1.4\n"+tag+"\n"), 0xe2, 0xe3, 0xcf, 0xd3, '\n')
}
