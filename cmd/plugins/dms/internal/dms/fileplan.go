package dms

import (
	"context"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablageplan: Ablagen mit Nummer, Name und übergeordneter Ablage – wie ein
// Kontenplan für alle Buchungskreise. Dokumente liegen nur in Ablagen ohne
// Unterablagen (unterste Ebene). Je Buchungskreis sind die Ablagen aktiviert
// (DmsFileAccountCompany), mit abweichender Frist oder abweichendem Archiv.

const secPlan = "Ablageplan"

var numberRe = regexp.MustCompile(`^[0-9A-Za-z]+(\.[0-9A-Za-z]+)*$`)

func (m *Module) plan() *crud.Entity {
	return &crud.Entity{
		Object: planObject, Title: "Ablagepläne", Icon: "icon-sitemap", Table: "dms__plan", Section: secPlan,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			// Eingaben der Aktion „Ablageplan einlesen …“
			{Key: "import_code", Label: "Ablageplan (Schlüssel; vorhandener wird ergänzt)", Type: tText, ActionOnly: true},
			{Key: "import_name", Label: "Bezeichnung (leer = oberster Knoten der Mindmap)", Type: tText, ActionOnly: true},
			{Key: "file", Label: "Mindmap (Freemind-XML, Knoten „1.0.0 Name“)", Type: tFile, Required: true, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "ablagen", Title: "Ablagen", Relation: &metamodel.Relation{Object: accountObject, ForeignKey: "plan", Match: map[string]string{"plan": "code"},
				Columns: []string{"number", "name", "parent", "retention", "is_leaf"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error { return checkCode(rec, "code", "Schlüssel") },
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "import", Label: "Ablageplan einlesen …",
				Fields: []string{"import_code", "import_name", "file"}}, Handle: m.importPlanAction},
		},
	}
}

func (m *Module) fileAccount() *crud.Entity {
	return &crud.Entity{
		Object: accountObject, Title: "Ablagen", Icon: "icon-folder", Table: "dms__account", Section: secPlan,
		Keys: []string{"plan", "number"}, Order: "plan, sort_key", StatusField: "is_active", TitleField: "name",
		Filters: []string{"plan", "parent"}, Search: []string{"number", "name"},
		Fields: []crud.Field{
			{Key: "plan", Label: "Ablageplan", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refPlan},
			{Key: "number", Label: "Nummer", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "parent", Label: "Übergeordnete Ablage (leer = aus der Nummer, 3.0.1 → 3.0)", Type: tText, Listable: true},
			{Key: "retention", Label: "Ablagefrist", Type: tText, Listable: true, Ref: refRetention},
			{Key: "sort_key", Label: "Sortierschlüssel", Type: tText, ReadOnly: true},
			{Key: "is_leaf", Label: "Unterste Ebene (nimmt Dokumente auf)", Type: tBool, Listable: true, ReadOnly: true, Virtual: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: m.checkFileAccount,
		Decorate: m.decorateFileAccount,
	}
}

func (m *Module) checkFileAccount(ctx context.Context, rec, old crud.Record) error {
	plan, number := crud.Str(rec["plan"]), strings.TrimSpace(crud.Str(rec["number"]))
	if !numberRe.MatchString(number) {
		return crud.Invalid("Nummer %q: Ziffern und Buchstaben, Ebenen mit Punkt getrennt (z. B. 3.0.1)", number)
	}
	rec["number"], rec["sort_key"] = number, sortKey(number)
	parent := strings.TrimSpace(crud.Str(rec["parent"]))
	if parent == "" {
		if i := strings.LastIndex(number, "."); i > 0 {
			parent = number[:i]
		}
	}
	if parent == number {
		return crud.Invalid("Ablage %s kann nicht ihre eigene übergeordnete Ablage sein", number)
	}
	rec["parent"] = nilIfEmpty(parent)
	if parent == "" {
		return nil
	}
	// Übergeordnete Ablage im selben Plan, ohne Kreis.
	for p, seen := parent, map[string]bool{number: true}; p != ""; {
		if seen[p] {
			return crud.Invalid("Ablage %s: übergeordnete Ablagen bilden einen Kreis (%s)", number, p)
		}
		seen[p] = true
		res, err := m.db.Query(ctx, `SELECT parent FROM dms__account WHERE plan = ? AND number = ?`, plan, p)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Übergeordnete Ablage %s gibt es im Ablageplan %s nicht", p, plan)
		}
		p = crud.Str(res.Rows[0][0])
	}
	if old == nil || crud.Str(old["parent"]) != parent {
		// Wird die übergeordnete Ablage zur Gruppe, darf sie keine Dokumente enthalten.
		res, err := m.db.Query(ctx, `SELECT COUNT(*) FROM dms__document d JOIN dms__company c ON c.company_code = d.company_code
			WHERE c.plan = ? AND d.file_account = ? AND d.status <> ?`, plan, parent, stRemoved)
		if err != nil {
			return err
		}
		if n := toInt(res.Rows[0][0]); n > 0 {
			return crud.Invalid("Ablage %s enthält %d Dokumente – Unterablagen nur, wenn sie leer ist (Dokumente nur in der untersten Ebene)", parent, n)
		}
	}
	return nil
}

func (m *Module) decorateFileAccount(ctx context.Context, rec crud.Record) error {
	leaf, err := m.isLeaf(ctx, crud.Str(rec["plan"]), crud.Str(rec["number"]))
	rec["is_leaf"] = leaf
	return err
}

// isLeaf: Ablage ohne aktive Unterablagen.
func (m *Module) isLeaf(ctx context.Context, plan, number string) (bool, error) {
	res, err := m.db.Query(ctx, `SELECT COUNT(*) FROM dms__account WHERE plan = ? AND parent = ? AND is_active = ?`, plan, number, true)
	if err != nil {
		return false, err
	}
	return toInt(res.Rows[0][0]) == 0, nil
}

// sortKey: Nummernteile mit führenden Nullen (1.10 nach 1.9).
func sortKey(number string) string {
	parts := strings.Split(number, ".")
	for i, p := range parts {
		if len(p) < 6 && strings.Trim(p, "0123456789") == "" {
			parts[i] = strings.Repeat("0", 6-len(p)) + p
		}
	}
	return strings.Join(parts, ".")
}

// accountInfo: Ablage mit Pfad (oberste Ebene zuerst).
type accountInfo struct {
	Number, Name, Retention string
	Path                    []Level
}

func (m *Module) accountPath(ctx context.Context, plan, number string) (*accountInfo, error) {
	var path []Level
	info := &accountInfo{Number: number}
	for p, depth := number, 0; p != "" && depth < 50; depth++ {
		res, err := m.db.Query(ctx, `SELECT name, parent, retention, is_active FROM dms__account WHERE plan = ? AND number = ?`, plan, p)
		if err != nil {
			return nil, err
		}
		if len(res.Rows) == 0 {
			return nil, crud.Invalid("Ablage %s gibt es im Ablageplan %s nicht", p, plan)
		}
		r := res.Rows[0]
		if p == number {
			if !crud.AsBool(r[3]) {
				return nil, crud.Invalid("Ablage %s ist inaktiv", number)
			}
			info.Name, info.Retention = crud.Str(r[0]), crud.Str(r[2])
		}
		path = append([]Level{{Number: p, Name: crud.Str(r[0])}}, path...)
		p = crud.Str(r[1])
	}
	info.Path = path
	return info, nil
}

func (m *Module) fileAccountCompany() *crud.Entity {
	return &crud.Entity{
		Object: accountCCObject, Title: "Ablagen im Buchungskreis", Icon: "icon-folder-open", Table: "dms__account_company", Section: secPlan,
		Keys: []string{"company_code", "number"}, Order: "company_code, number", Filters: []string{"company_code", "leaf"},
		// leaf=true: nur Ablagen der untersten Ebene (Auswahl im Workflow).
		FilterExpr: map[string]func(v any) (string, []any){"leaf": func(v any) (string, []any) {
			if !crud.AsBool(v) {
				return "1 = 1", nil
			}
			return `NOT EXISTS (SELECT 1 FROM dms__account a JOIN dms__company c ON c.plan = a.plan
				WHERE c.company_code = dms__account_company.company_code AND a.parent = dms__account_company.number AND a.is_active = ?)`, []any{true}
		}},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "number", Label: "Ablage", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: accountObject, ValueField: "number", LabelFields: []string{"name"}, Columns: []string{"plan", "number", "name", "is_leaf"}}},
			{Key: "retention", Label: "Ablagefrist (abweichend)", Type: tText, Listable: true, Ref: refRetention},
			{Key: "archive", Label: "Archiv (abweichend)", Type: tText, Listable: true, Ref: refArchive},
			{Key: "is_blocked", Label: "Gesperrt (keine neuen Dokumente)", Type: tBool, Listable: true},
			{Key: "gl_accounts", Label: "Sachkonten (durch Komma): Dokumente zu diesen Konten gehören hierher", Type: tText, Listable: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkFileAccountCompany,
		Decorate: m.decorateFileAccountCompany,
	}
}

func (m *Module) checkFileAccountCompany(ctx context.Context, rec, _ crud.Record) error {
	cfg, err := m.companyConfig(ctx, crud.Str(rec["company_code"]))
	if err != nil {
		return err
	}
	rec["number"] = strings.TrimSpace(crud.Str(rec["number"]))
	if _, err := m.accountPath(ctx, cfg.Plan, crud.Str(rec["number"])); err != nil {
		return err
	}
	defaults(rec, map[string]any{"is_blocked": false})
	accs := splitList(crud.Str(rec["gl_accounts"]))
	if len(accs) > 0 {
		if leaf, err := m.isLeaf(ctx, cfg.Plan, crud.Str(rec["number"])); err != nil {
			return err
		} else if !leaf {
			return crud.Invalid("Sachkonten nur an Ablagen der untersten Ebene")
		}
	}
	for _, a := range accs {
		if err := m.checkGLAccount(ctx, cfg.CC, a); err != nil {
			return err
		}
		if other, err := m.folderOfAccount(ctx, cfg.CC, a); err != nil {
			return err
		} else if other != "" && other != crud.Str(rec["number"]) {
			return crud.Invalid("Sachkonto %s gehört schon zur Ablage %s", a, other)
		}
	}
	rec["gl_accounts"] = strings.Join(accs, ", ")
	return nil
}

// checkGLAccount: Sachkonto im Buchungskreis (Hauptbuch); ohne Hauptbuch keine Prüfung.
func (m *Module) checkGLAccount(ctx context.Context, cc, number string) error {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if err != nil {
		return nil
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &out) != nil || len(out.Items) == 0 {
		return crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht", number, cc)
	}
	return nil
}

// folderOfAccount: Ablage, der das Sachkonto im Buchungskreis zugeordnet ist ("" = keine).
func (m *Module) folderOfAccount(ctx context.Context, cc, account string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT number, gl_accounts FROM dms__account_company WHERE company_code = ? AND gl_accounts <> ''`, cc)
	if err != nil {
		return "", err
	}
	for _, r := range res.Rows {
		if contains(splitList(crud.Str(r[1])), account) {
			return crud.Str(r[0]), nil
		}
	}
	return "", nil
}

func (m *Module) decorateFileAccountCompany(ctx context.Context, rec crud.Record) error {
	res, err := m.db.Query(ctx, `SELECT a.name FROM dms__account a JOIN dms__company c ON c.plan = a.plan
		WHERE c.company_code = ? AND a.number = ?`, rec["company_code"], rec["number"])
	if err == nil && len(res.Rows) > 0 {
		labels(rec)["number"] = crud.Str(rec["number"]) + " " + crud.Str(res.Rows[0][0])
	}
	return err
}

func labels(rec crud.Record) map[string]any {
	l, ok := rec["_labels"].(map[string]any)
	if !ok {
		l = map[string]any{}
		rec["_labels"] = l
	}
	return l
}

// companyAccount: Ablage im Buchungskreis zum Ablegen – aktiv, unterste Ebene,
// nicht gesperrt; mit Frist und Archiv (Buchungskreis vor Ablage vor Dokumentart).
type companyAccount struct {
	accountInfo
	Archive string
}

func (m *Module) companyAccount(ctx context.Context, cfg *companyConfig, number string) (*companyAccount, error) {
	info, err := m.accountPath(ctx, cfg.Plan, number)
	if err != nil {
		return nil, err
	}
	leaf, err := m.isLeaf(ctx, cfg.Plan, number)
	if err != nil {
		return nil, err
	}
	if !leaf {
		return nil, crud.Invalid("Ablage %s %s hat Unterablagen – Dokumente nur in der untersten Ebene", number, info.Name)
	}
	res, err := m.db.Query(ctx, `SELECT retention, archive, is_blocked FROM dms__account_company WHERE company_code = ? AND number = ?`, cfg.CC, number)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Ablage %s %s ist im Buchungskreis %s nicht aktiviert (Buchungskreise → „Ablagen übernehmen“)", number, info.Name, cfg.CC)
	}
	r := res.Rows[0]
	if crud.AsBool(r[2]) {
		return nil, crud.Invalid("Ablage %s %s ist im Buchungskreis %s gesperrt", number, info.Name, cfg.CC)
	}
	out := &companyAccount{accountInfo: *info, Archive: cfg.Archive}
	if v := crud.Str(r[0]); v != "" {
		out.Retention = v
	}
	if v := crud.Str(r[1]); v != "" {
		out.Archive = v
	}
	return out, nil
}

// activateAccountsAction: alle Ablagen des Plans im Buchungskreis anlegen.
func (m *Module) activateAccountsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	n, err := m.activateAccounts(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"activated": n, "message": fmt.Sprintf("Buchungskreis %s: %d Ablagen aktiviert", in.ID, n)}}, nil
}

func (m *Module) activateAccounts(ctx context.Context, cc string) (int, error) {
	if err := requireWrite(ctx, companyObject, "update", cc); err != nil {
		return 0, err
	}
	cfg, err := m.companyConfig(ctx, cc)
	if err != nil {
		return 0, err
	}
	res, err := m.db.Exec(ctx, `INSERT INTO dms__account_company (company_code, number, is_blocked)
		SELECT ?, a.number, ? FROM dms__account a WHERE a.plan = ? AND a.is_active = ?
		AND NOT EXISTS (SELECT 1 FROM dms__account_company x WHERE x.company_code = ? AND x.number = a.number)`, cc, false, cfg.Plan, true, cc)
	if err != nil {
		return 0, err
	}
	return int(res.RowsAffected), nil
}

// --- Mindmap einlesen (Freemind-XML) ------------------------------------------------

type mmNode struct {
	Text  string   `xml:"TEXT,attr"`
	Nodes []mmNode `xml:"node"`
}

type mmMap struct {
	Nodes []mmNode `xml:"node"`
}

var nodeRe = regexp.MustCompile(`^([0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)\s+(.+)$`)

// planEntry ist eine Ablage aus der Mindmap.
type planEntry struct{ Number, Name, Parent string }

// parseMindmap liest Ablagen aus einer Freemind-Mindmap. Ein oberster Knoten
// ohne Nummer benennt den Plan; Knoten tiefer brauchen eine Nummer.
func parseMindmap(content string) (title string, entries []planEntry, err error) {
	var mm mmMap
	if err := xml.Unmarshal([]byte(content), &mm); err != nil {
		return "", nil, crud.Invalid("Mindmap nicht lesbar: %v", err)
	}
	var bad []string
	seen := map[string]bool{}
	var walk func(n mmNode, parent string)
	walk = func(n mmNode, parent string) {
		text := strings.Join(strings.Fields(n.Text), " ")
		g := nodeRe.FindStringSubmatch(text)
		if g == nil || !strings.ContainsAny(g[1], "0123456789") {
			bad = append(bad, text)
			return
		}
		if seen[g[1]] {
			bad = append(bad, text+" (Nummer doppelt)")
			return
		}
		seen[g[1]] = true
		entries = append(entries, planEntry{Number: g[1], Name: g[2], Parent: parent})
		for _, c := range n.Nodes {
			walk(c, g[1])
		}
	}
	for _, top := range mm.Nodes {
		text := strings.Join(strings.Fields(top.Text), " ")
		if g := nodeRe.FindStringSubmatch(text); g != nil && strings.ContainsAny(g[1], "0123456789") {
			walk(top, "")
			continue
		}
		if title == "" {
			title = text
		}
		for _, c := range top.Nodes {
			walk(c, "")
		}
	}
	if len(bad) > 0 {
		if len(bad) > 5 {
			bad = append(bad[:5], fmt.Sprintf("… (%d weitere)", len(bad)-5))
		}
		return "", nil, crud.Invalid("Knoten ohne Nummer: %s", strings.Join(bad, "; "))
	}
	if len(entries) == 0 {
		return "", nil, crud.Invalid("Mindmap enthält keine Ablagen")
	}
	return title, entries, nil
}

func (m *Module) importPlanAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Code string `json:"import_code"`
			Name string `json:"import_name"`
			File string `json:"file"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	return m.importPlan(ctx, in.Data.Code, in.Data.Name, in.Data.File)
}

func (m *Module) importPlanCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Plan string `json:"plan"`
		Name string `json:"name"`
		File string `json:"file"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	return m.importPlan(ctx, in.Plan, in.Name, in.File)
}

// importPlan legt den Plan und fehlende Ablagen an und aktualisiert Namen
// und übergeordnete Ablagen; Ablagen, die in der Mindmap fehlen, bleiben.
func (m *Module) importPlan(ctx context.Context, code, name, content string) (sdk.Response, error) {
	code = trimUpper(code)
	if !codeRe.MatchString(code) {
		return sdk.Response{}, crud.Invalid("Ablageplan %q: Großbuchstaben, Ziffern, _ und - (höchstens 20 Zeichen)", code)
	}
	title, entries, err := parseMindmap(content)
	if err != nil {
		return sdk.Response{}, err
	}
	if strings.TrimSpace(name) == "" {
		name = orDefault(title, code)
	}
	plans, accounts := m.set.Entity(planObject), m.set.Entity(accountObject)
	created, updated := 0, 0
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := plans.Load(ctx, crud.Record{"code": code}); err != nil {
			if _, err := plans.Create(ctx, map[string]any{"data": map[string]any{"code": code, "name": name}}); err != nil {
				return err
			}
		}
		for _, e := range entries {
			id := code + "|" + e.Number
			old, err := accounts.Load(ctx, crud.Record{"plan": code, "number": e.Number})
			if err != nil {
				if _, err := accounts.Create(ctx, map[string]any{"data": map[string]any{"plan": code, "number": e.Number, "name": e.Name,
					"parent": e.Parent}}); err != nil {
					return fmt.Errorf("Ablage %s: %w", e.Number, err)
				}
				created++
				continue
			}
			if crud.Str(old["name"]) != e.Name || crud.Str(old["parent"]) != e.Parent {
				if _, err := accounts.Update(ctx, map[string]any{"id": id, "data": map[string]any{"name": e.Name, "parent": e.Parent}}); err != nil {
					return fmt.Errorf("Ablage %s: %w", e.Number, err)
				}
				updated++
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"plan": code, "created": created, "updated": updated,
		"message": fmt.Sprintf("Ablageplan %s: %d Ablagen gelesen, %d angelegt, %d aktualisiert", code, len(entries), created, updated)}}, nil
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
