package procurement

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	// Vertragsmodul: nachgelagerte Arten (nil = Vertragsmodul fehlt) und die des Vertrags RV-1
	contractDeps []any
	rvDeps       []any
	// seit 0.11.0: Konto für nicht abziehbare Vorsteuer je Sachkonto, Vorsteuerquote je Objekt
	taxSub     map[string]string
	quotas     map[string]string
	quotaCalls []string
	// seit 0.14.0: Auftragsschreiben – Daten an die Vorlage, Ablage im DMS, E-Mail-Adressen
	rendered map[string]any
	stored   map[string]any
	mails    map[string]string // Partner → E-Mail
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
		} else if r.Object == rangeOrder {
			prefix = "AU"
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
	case "Contract.get":
		switch p["id"] {
		case "1000|MV-1":
			return sdk.Response{Payload: map[string]any{"contract_id": "MV-1"}}, nil
		case "1000|RV-1":
			return sdk.Response{Payload: map[string]any{"contract_id": "RV-1", "contract_type": "RV", "partner_id": "HW1", "currency": "EUR"}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "ContractType.get":
		if p["id"] == "1000|RV" {
			return sdk.Response{Payload: map[string]any{"code": "RV", "template_invoices": true, "default_tax_code": "VN19"}}, nil
		}
		return sdk.Response{Payload: map[string]any{"code": p["id"], "template_invoices": false}}, nil
	case "ContractCondition.get":
		switch p["id"] {
		case "1000|RV-1|RT||1":
			return sdk.Response{Payload: map[string]any{"condition_type": "RT", "account_number": "6300", "amount": "150.00", "note": "Reparatur", "is_template": true}}, nil
		case "1000|RV-1|EN||1": // Hausmeister: Entgelt und Material netto, Vorsteuer 19 %
			return sdk.Response{Payload: map[string]any{"condition_type": "EN", "account_number": "6350", "amount": "309.80", "note": "Entgelt", "is_template": true}}, nil
		case "1000|RV-1|MA||1":
			return sdk.Response{Payload: map[string]any{"condition_type": "MA", "account_number": "6351", "amount": "15.00", "note": "Material", "is_template": true}}, nil
		case "1000|RV-1|EN||2": // zweites Entgelt (Position 2)
			return sdk.Response{Payload: map[string]any{"condition_type": "EN", "position": 2, "account_number": "6351", "amount": "50.00", "note": "Entgelt 2", "is_template": true}}, nil
		case "1000|RV-1|KM||1":
			return sdk.Response{Payload: map[string]any{"condition_type": "KM", "account_number": "6300", "amount": "1.00", "is_template": false}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "ContractCondition.list":
		return sdk.Response{Payload: map[string]any{"items": []any{
			map[string]any{"contract_id": "RV-1", "condition_type": "EN", "account_number": "6350", "amount": "1.234,50", "valid_from": "2026-01-01", "valid_to": "9999-12-31"},
			map[string]any{"contract_id": "RV-1", "condition_type": "EN", "account_number": "6350", "amount": "1.000,00", "valid_from": "2025-01-01", "valid_to": "2025-12-31"},
			map[string]any{"contract_id": "RV-1", "condition_type": "MA", "account_number": "6351", "amount": "20.00", "note": "Material", "valid_from": "2026-01-01"}}}}, nil
	case "Unit.get":
		d := map[string]int{"STK": 0, "H": 2, "KWH": 0, "PCT": 2}
		if v, ok := d[fmt.Sprint(p["id"])]; ok {
			return sdk.Response{Payload: map[string]any{"code": p["id"], "decimals": v}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "LedgerCompanyConfig.get":
		return sdk.Response{Payload: map[string]any{"company_code_id": p["id"]}}, nil
	case "RoundingRule.get":
		if p["id"] == "KFM4" {
			return sdk.Response{Payload: map[string]any{"code": "KFM4", "mode": "HALF_UP", "increment": "0.01", "price_decimals": 4}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "RoundingRule.list":
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"code": "KFM", "mode": "HALF_UP", "increment": "0.01",
			"price_decimals": 2, "is_default": true, "is_active": true}}}}, nil
	case "ContractDependentType.list":
		if h.contractDeps == nil {
			return sdk.Response{}, fmt.Errorf("unimplemented: %s.%s", req.Object, req.Action)
		}
		return sdk.Response{Payload: map[string]any{"items": h.contractDeps}}, nil
	case "ContractCondition.positions":
		return sdk.Response{Payload: map[string]any{"currency": "EUR", "items": []any{
			map[string]any{"condition_type": "EN", "name": "Entgelt", "account_number": "6350", "quantity": "1", "unit": "STK", "unit_price": "1234.5", "amount": "1234.50"},
			map[string]any{"condition_type": "MA", "name": "Material", "note": "Material", "account_number": "6351", "quantity": "4", "unit": "STK", "unit_price": "5", "amount": "20.00"}},
			"dependents": h.rvDeps}}, nil
	case "ConditionType.get":
		if p["id"] == "1000|EN" || p["id"] == "1000|MA" {
			return sdk.Response{Payload: map[string]any{"code": strings.TrimPrefix(fmt.Sprint(p["id"]), "1000|"), "name": "Hausmeister", "tax_code": "V19"}}, nil
		}
		return sdk.Response{Payload: map[string]any{"code": "RT", "name": "Rechnung nach Vorlage"}}, nil
	case "ContractObject.list":
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"contract_id": "RV-1", "object_type": "Building", "object_id": "LpzBrn1", "is_main": true}}}}, nil
	case "TaxCode.get":
		switch p["id"] {
		case "V19":
			return sdk.Response{Payload: map[string]any{"code": "V19", "kind": "INPUT", "rate": "19", "account_number": "1406"}}, nil
		case "VN19":
			return sdk.Response{Payload: map[string]any{"code": "VN19", "kind": "NONDEDUCTIBLE", "rate": "19"}}, nil
		case "VA19":
			return sdk.Response{Payload: map[string]any{"code": "VA19", "kind": "PARTIAL", "rate": "19", "account_number": "1406", "split_measurement": "WFL"}}, nil
		}
		return sdk.Response{}, sdk.ErrNotFound
	case "ContractCondition.taxQuota":
		h.quotaCalls = append(h.quotaCalls, fmt.Sprintf("%v %v %v %v", p["object_type"], p["object_id"], p["date"], p["measurement_type"]))
		qv, ok := h.quotas[fmt.Sprint(p["object_id"])]
		if !ok {
			return sdk.Response{}, fmt.Errorf("%w: keine Mietobjekte", sdk.ErrInvalidArgument)
		}
		return sdk.Response{Payload: map[string]any{"quota": qv, "text": "Test"}}, nil
	case "TemplateDefinition.list":
		if q["usage"] != usageOrder {
			return sdk.Response{Payload: map[string]any{"items": []any{}}}, nil
		}
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"code": "ORDER_LETTER", "language": "de", "name": "Auftragsschreiben", "target": "PDF"}}}}, nil
	case "Template.render":
		h.rendered, _ = p["data"].(map[string]any)
		return sdk.Response{Payload: map[string]any{"job_number": "J1"}}, nil
	case "Template.get":
		return sdk.Response{Payload: map[string]any{"status": "DONE", "content_type": "application/pdf", "file_name": "Auftrag.pdf",
			"content": base64.StdEncoding.EncodeToString([]byte("%PDF-Auftrag"))}}, nil
	case "DmsDocument.store":
		h.stored = p
		return sdk.Response{Payload: map[string]any{"id": "DOC1", "name": "AU_2026_Auftrag"}}, nil
	case "DmsDocument.content":
		if p["id"] != "DOC1" {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"content": base64.StdEncoding.EncodeToString([]byte("%PDF-Auftrag")), "content_type": "application/pdf",
			"file_name": "AU_2026_Auftrag.pdf"}}, nil
	case "PartnerContact.list":
		var items []any
		if m, ok := h.mails[fmt.Sprint(q["bp_id"])]; ok {
			items = append(items, map[string]any{"comm_type_code": "EMAIL", "value": m, "is_default": true})
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "PartnerCommType.get":
		return sdk.Response{Payload: map[string]any{"category_code": p["id"]}}, nil
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
		nr = strings.TrimPrefix(nr, "SKR25-")
		grp, ok := h.accounts[fmt.Sprint(q["company_code_id"])+"|"+nr]
		if !ok {
			return sdk.Response{Payload: map[string]any{"items": []any{}}}, nil
		}
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"account_number": nr, "field_status_group": grp, "nondeductible_tax_account": h.taxSub[nr]}}}}, nil
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
	case "JournalEntry.changeHeader":
		h.calls = append(h.calls, "changeHeader "+fmt.Sprint(p["id"])+" "+fmt.Sprint(data["new_reference"])+" "+fmt.Sprint(data["new_header_text"])+
			" "+fmt.Sprint(data["change_reason"]))
		return sdk.Response{Payload: map[string]any{"message": "Beleg 1000000099: Referenz geändert"}}, nil
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
		accounts: map[string]string{"1000|6300": "COST", "1000|6400": "COST", "1000|6350": "COST", "1000|6351": "COST", "1000|2900": "SUPPLIER", "1000|1406": ""},
		status: map[string]map[string]string{
			"COST":     {"rent_object_id": "OPTIONAL", "dimension_custom_1": "OPTIONAL", "cost_center": "OPTIONAL", "supplier_id": "SUPPRESS"},
			"SUPPLIER": {"supplier_id": "REQUIRED", "rent_object_id": "OPTIONAL"},
		},
		supplier: map[string]string{"HW1|1000|CREDITOR": "2900", "HW2|1000|CREDITOR": "!2900"},
		objects:  map[string]bool{"RentObject|1000|LpzBrn1WG001": true, "Building|1000|LpzBrn1": true},
		taxSub:   map[string]string{}, quotas: map[string]string{}, mails: map[string]string{"HW1": "info@schulze.example"},
		numbers: map[string]int{}, drafts: map[string]map[string]any{}, lines: map[string][]map[string]any{},
		costs: map[string][2]string{"INSTAND": {"6300", "NON_ALLOCABLE"}, "WASSER": {"6400", "ALLOCABLE"}, "OHNEKONTO": {"", "ALLOCABLE"}}}
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

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.h.db.Exec(q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) rows(q string, args ...any) [][]any {
	e.t.Helper()
	rs, err := e.h.db.Query(q, args...)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rs.Close()
	cols, _ := rs.Columns()
	var out [][]any
	for rs.Next() {
		row := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range row {
			ptr[i] = &row[i]
		}
		if err := rs.Scan(ptr...); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, row)
	}
	return out
}
