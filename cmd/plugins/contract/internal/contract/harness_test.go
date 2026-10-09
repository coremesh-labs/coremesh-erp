package contract

import (
	"context"
	"database/sql"
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
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// testHost: SQLite und Attrappen der anderen Plugins – Rechte (rules je
// "Object.action"; ohne Eintrag alles erlaubt), Partnermodul, Nummernkreise,
// Immobilien, Hauptbuch, Hooks und Events.
type testHost struct {
	db    *sql.DB
	rules map[string][]sdk.GrantRule

	partners     map[string]string             // Partner → Name
	partnerRoles map[string][]partnerRoleSlice // Partner → Rollen
	objects      map[string]map[string]any     // "<Object>|<cc>|<id>" → Datensatz
	composite    []map[string]any              // Bestandteile (CompositeItem)
	accounts     map[string]string             // "<cc>|<Konto>" → Bezeichnung ("!" am Anfang = gesperrt)
	recon        map[string]string             // "<cc>|<Konto>" → Abstimmkontoart (CUSTOMER, SUPPLIER; fehlt = NONE)
	partnerCC    map[string]string             // "<Partner>|<cc>|<Rolle>" → Abstimmkonto (Buchungskreisdaten)
	rekeyed      map[string]string             // alte Partner-ID → BP-Nummer (BusinessPartnerService.resolve)
	numbers      map[string]int                // Nummernkreis-Stand
	tags         map[string]map[string]any     // Tag-Definitionen "<Object>|<id>" (nil = Tag-Plugin fehlt)
	events       []map[string]any
	hookVeto     string // Meldung E im Hook contract.activate (check)
	hookCalls    []string
}

type partnerRoleSlice struct{ role, from, to string }

var roleTypes = []any{
	map[string]any{"code": "TENANT", "description": "Mieter", "is_debitor": true},
	map[string]any{"code": "LANDLORD", "description": "Vermieter", "is_creditor": true},
	map[string]any{"code": "OWNER", "description": "Eigentümer", "is_debitor": true}, // zahlt Hausgeld
	map[string]any{"code": "CREDITOR", "description": "Kreditor", "is_creditor": true},
	map[string]any{"code": "DEBITOR", "description": "Debitor", "is_debitor": true},
	map[string]any{"code": "GUARANTOR", "description": "Bürge"},
	map[string]any{"code": "AUTHORITY", "description": "Behörde / Amt", "is_creditor": true},
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
	q, _ := p["query"].(map[string]any)
	switch req.Object + "." + req.Action {
	case "SystemEvent.Push":
		h.events = append(h.events, p)
		return sdk.Response{Payload: map[string]any{"subscribers": 0}}, nil
	case "Account.Check":
		attrs := sdk.Attrs{}
		if a, ok := p["attrs"].(map[string]string); ok {
			maps.Copy(attrs, a)
		}
		return sdk.Response{Payload: map[string]any{"allowed": h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"])).Allows(attrs)}}, nil
	case "Account.Granted":
		return sdk.Response{Payload: h.grants(fmt.Sprint(p["object"]), fmt.Sprint(p["action"]))}, nil
	case "NumberRange.Define":
		return sdk.Response{Payload: map[string]any{"object": p["object"]}}, nil
	case "NumberRange.Next":
		var r numrange.Request
		_ = sdk.Decode(req.Payload, &r)
		key := r.Object + "|" + r.CompanyCode + "|" + r.Key
		h.numbers[key]++
		n := h.numbers[key]
		if r.Object == rangeContract {
			return sdk.Response{Payload: numrange.Result{Number: fmt.Sprintf("%s-2026-%04d", r.Key, n), Value: int64(n)}}, nil
		}
		return sdk.Response{Payload: numrange.Result{Number: fmt.Sprintf("%03d", n), Value: int64(n)}}, nil
	case "Hook.Define":
		return sdk.Response{Payload: map[string]any{}}, nil
	case "Hook.Call":
		h.hookCalls = append(h.hookCalls, fmt.Sprint(p["hook"])+"/"+fmt.Sprint(p["action"]))
		res := hook.Result{Data: p["data"], Messages: []hook.Message{}}
		if p["action"] == hook.PhaseCheck && h.hookVeto != "" {
			res.Messages = append(res.Messages, hook.Error("T-1", h.hookVeto))
		}
		return sdk.Response{Payload: res}, nil
	case "PartnerRoleType.list":
		return sdk.Response{Payload: map[string]any{"items": roleTypes}}, nil
	case "PartnerRole.list":
		items := []any{}
		for _, s := range h.partnerRoles[fmt.Sprint(q["bp_id"])] {
			if s.role == fmt.Sprint(q["role_code"]) {
				items = append(items, map[string]any{"valid_from": s.from, "valid_to": s.to})
			}
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "BusinessPartner.get":
		name, ok := h.partners[fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"id": p["id"], "name1": name}}, nil
	case "RentObject.get", "Building.get", "BusinessEntity.get":
		o, ok := h.objects[req.Object+"|"+fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, fmt.Errorf("%w: %s %v", sdk.ErrNotFound, req.Object, p["id"])
		}
		return sdk.Response{Payload: o}, nil
	case "CompositeItem.list":
		items := []any{}
		for _, it := range h.composite {
			match := true
			for _, k := range []string{"composite_id", "object_id"} {
				if v, ok := q[k]; ok && it[k] != v {
					match = false
				}
			}
			if match {
				items = append(items, it)
			}
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "GLAccountCompany.list":
		// wie der Ledger: Filter mit oder ohne Kontenplan-Präfix, Antwort mit Präfix
		nr := fmt.Sprint(q["account_number"])
		if !strings.Contains(nr, "-") {
			nr = "SKR25-" + nr
		}
		name, ok := h.accounts[fmt.Sprint(q["company_code_id"])+"|"+nr]
		if !ok {
			return sdk.Response{Payload: map[string]any{"items": []any{}}}, nil
		}
		blocked := strings.HasPrefix(name, "!")
		recon := h.recon[fmt.Sprint(q["company_code_id"])+"|"+nr]
		if recon == "" {
			recon = "NONE"
		}
		return sdk.Response{Payload: map[string]any{"items": []any{map[string]any{"account_number": nr,
			"account_name": strings.TrimPrefix(name, "!"), "is_blocked": blocked, "reconciliation_type": recon}}}}, nil
	case "CostCategory.get":
		costs := map[string][2]string{"1000|WASSER": {"SKR25-7000", "ALLOCABLE"}, "1000|VERWALT": {"SKR25-7300", "NON_ALLOCABLE"},
			"1000|RUECKL": {"SKR25-1550", "RESERVE"}, "1000|OHNE": {"", "ALLOCABLE"}}
		c, ok := costs[fmt.Sprint(p["id"])]
		if !ok {
			return sdk.Response{}, sdk.ErrNotFound
		}
		return sdk.Response{Payload: map[string]any{"account_number": c[0], "cost_type": c[1]}}, nil
	case "BusinessPartnerService.resolve":
		var in struct {
			IDs []string `json:"ids"`
		}
		_ = sdk.Decode(req.Payload, &in)
		out := map[string]string{}
		for _, id := range in.IDs {
			if n, ok := h.rekeyed[id]; ok {
				out[id] = n
			}
		}
		return sdk.Response{Payload: map[string]any{"ids": out}}, nil
	case "PartnerCompanyCode.list":
		items := []any{}
		if acc, ok := h.partnerCC[fmt.Sprint(q["bp_id"])+"|"+fmt.Sprint(q["company_code"])+"|"+fmt.Sprint(q["role_code"])]; ok {
			items = append(items, map[string]any{"bp_id": q["bp_id"], "company_code": q["company_code"], "role_code": q["role_code"],
				"reconciliation_account": acc})
		}
		return sdk.Response{Payload: map[string]any{"items": items}}, nil
	case "Currency.get":
		decimals := 2
		if p["id"] == "JPY" {
			decimals = 0
		}
		return sdk.Response{Payload: map[string]any{"code": p["id"], "decimals": decimals}}, nil
	}
	if h.tags != nil && strings.HasPrefix(req.Object, "Tag") {
		var p map[string]any
		_ = sdk.Decode(req.Payload, &p)
		switch req.Action {
		case "get":
			if r, ok := h.tags[req.Object+"|"+fmt.Sprint(p["id"])]; ok {
				return sdk.Response{Payload: r}, nil
			}
			return sdk.Response{}, sdk.ErrNotFound
		case "create":
			d, _ := p["data"].(map[string]any)
			id := map[string]string{
				"TagType":          fmt.Sprint(d["code"]),
				"TagSet":           fmt.Sprint(d["code"]),
				"TagSetItem":       fmt.Sprint(d["tag_set_code"]) + "|" + fmt.Sprint(d["tag_type_code"]),
				"TagSetAssignment": fmt.Sprint(d["entity_type"]) + "|" + fmt.Sprint(d["company_code"]) + "|" + fmt.Sprint(d["tag_set_code"]),
			}[req.Object]
			h.tags[req.Object+"|"+id] = d
			return sdk.Response{Payload: d}, nil
		}
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
	h := &testHost{db: db, rules: map[string][]sdk.GrantRule{}, partners: map[string]string{}, partnerRoles: map[string][]partnerRoleSlice{},
		objects: map[string]map[string]any{}, numbers: map[string]int{}, partnerCC: map[string]string{},
		accounts: map[string]string{"1000|SKR25-1200": "Forderungen aus Vermietung", "1000|SKR25-1600": "Verbindlichkeiten aus Lieferungen"},
		recon:    map[string]string{"1000|SKR25-1200": "CUSTOMER", "1000|SKR25-1600": "SUPPLIER"}}
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

// partner legt einen Geschäftspartner mit Rollen (seit 2000) an; Finanzrollen
// bekommen Buchungskreisdaten in 1000 (Mieter/Eigentümer 1200, Kreditor/Vermieter 1600).
func (e *env) partner(id, name string, roles ...string) {
	e.h.partners[id] = name
	for _, r := range roles {
		e.h.partnerRoles[id] = append(e.h.partnerRoles[id], partnerRoleSlice{r, "2000-01-01", "9999-12-31"})
		switch r {
		case "TENANT", "OWNER":
			e.h.partnerCC[id+"|1000|"+r] = "1200"
		case "CREDITOR", "LANDLORD":
			e.h.partnerCC[id+"|1000|"+r] = "1600"
		}
	}
}

// rentObject legt ein Mietobjekt der Immobilienverwaltung an (Attrappe).
func (e *env) rentObject(id, kind, designation string) {
	e.h.objects["RentObject|1000|"+id] = map[string]any{"object_id": id, "kind": kind, "designation": designation,
		"valid_from": "2020-01-01", "valid_to": "9999-12-31"}
}

// basics: Buchungskreis 1000 eingerichtet, Partner Müller (Mieter), Wohnung und Stellplatz.
func (e *env) basics() {
	e.t.Helper()
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.partner("BP1", "Müller", "TENANT")
	e.partner("BP2", "Schulz", "TENANT", "GUARANTOR")
	e.partner("INS", "Allianz", "CREDITOR")
	e.rentObject("LpzBrn1WG001", "UNIT", "Wohnung 1. OG links")
	e.rentObject("LpzBrn1SP001", "UNIT", "Stellplatz 1")
}

// rentContract: Mietvertrag MV mit Müller ab 2026-01-01, Wohnung, Kaltmiete 850,00.
func (e *env) rentContract() string {
	e.t.Helper()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Mietvertrag Müller",
		"partner_id": "BP1", "valid_from": "2026-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "KM",
		"calc_method": "FIXED", "amount": "850,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	return id
}

func (h *testHost) Read(_ context.Context, req sdk.Request, _ sdk.RowWriter) (sdk.ReadEnd, error) {
	return sdk.ReadEnd{}, fmt.Errorf("%w: %s.%s", sdk.ErrUnimplemented, req.Object, req.Action)
}
