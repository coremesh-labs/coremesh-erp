package ledger

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Bilanzschema (seit 0.27.0): Gliederung der Bilanz als Katalog, nicht im Code.
//
//   - BalanceSheetSchema: Schema je Kontenplan (z. B. HGB266K für SKR04).
//   - BalanceSheetItem: Posten mit Seite (Aktiva/Passiva), übergeordnetem Posten,
//     Reihenfolge und Konten. Konten sind Bereiche oder einzelne Nummern
//     ("0200-0399, 1376"); ein Konto gehört zum Bereich, wenn seine ersten
//     Stellen (so viele wie die Grenze hat) darin liegen – Unterkonten wie
//     315001 zählen so zu 3150. Quelle RESULT: Saldo aller Erfolgskonten
//     (Jahresergebnis), ohne Konten.
//
// Gerechnet wird nicht hier: Die Bilanz ist ein Bericht im Modul script
// (Skripte und Berichte), der Schema, Posten und Salden (AccountBalance.list) liest.
// Vorschläge liegen in fs/<kontenplan>.json („Vorschlag laden …“).

const (
	fsSchemaObject = "BalanceSheetSchema"
	fsItemObject   = "BalanceSheetItem"
)

//go:embed fs/*.json
var fsFiles embed.FS

var (
	fsSides = []metamodel.Option{
		{Value: "ASSETS", Label: "Aktiva"},
		{Value: "LIABILITIES", Label: "Passiva"},
	}
	fsSources = []metamodel.Option{
		{Value: "ACCOUNTS", Label: "Konten"},
		{Value: "RESULT", Label: "Jahresergebnis (Erfolgskonten)"},
	}
	fsAccountsRe = regexp.MustCompile(`^\d+(-\d+)?(,\d+(-\d+)?)*$`)
	fsCodeRe     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9._-]{0,29}$`)
	refFsSchema  = &crud.Ref{Table: "ledger__fs_schema", Column: "code", Label: "Bilanzschema", ActiveField: "is_active", Object: fsSchemaObject, LabelFields: []string{"name"}}
)

func (m *Module) fsSchemaEntity() *crud.Entity {
	return &crud.Entity{
		Object: fsSchemaObject, Title: "Bilanzschemata", Icon: "icon-layers", Table: "ledger__fs_schema", Section: "Kontenplan",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Filters: []string{"chart_of_accounts_id", "is_active"},
		Fields: []crud.Field{
			{Key: "code", Label: "Bilanzschema", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "chart_of_accounts_id", Label: "Kontenplan", Type: tText, Required: true, Listable: true, Ref: refChart},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Bilanzschema", Fields: []string{"code", "name", "chart_of_accounts_id", "description", "is_active"}},
			{Key: "posten", Title: "Posten", Relation: &metamodel.Relation{Object: fsItemObject, ForeignKey: "schema_code",
				Columns: []string{"sort_order", "item_code", "side", "label", "parent_code", "source", "accounts"}}},
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "loadProposal", Label: "Vorschlag laden …",
			Confirm: "Mitgelieferten Vorschlag für den Kontenplan laden? Fehlende Posten werden angelegt, vorhandene bleiben unverändert.",
			Fields:  []string{"chart_of_accounts_id"}}, Handle: m.fsLoadProposalAction}},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if !fsCodeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Bilanzschema %q: Großbuchstaben, Ziffern, Punkt, _ und - (höchstens 30 Zeichen)", crud.Str(rec["code"]))
				}
			}
			return nil
		},
	}
}

func (m *Module) fsItemEntity() *crud.Entity {
	isAccounts := &metamodel.Condition{Field: "source", Values: []string{"ACCOUNTS"}}
	return &crud.Entity{
		Object: fsItemObject, Title: "Bilanzposten", Icon: "icon-list", Table: "ledger__fs_item", Section: "Kontenplan",
		Keys: []string{"schema_code", "item_code"}, Order: "schema_code, sort_order, item_code", TitleField: "label",
		Search: []string{"item_code", "label", "accounts"}, Filters: []string{"schema_code", "side", "parent_code", "source"},
		Fields: []crud.Field{
			{Key: "schema_code", Label: "Bilanzschema", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refFsSchema},
			{Key: "item_code", Label: "Posten", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "sort_order", Label: "Reihenfolge", Short: "Pos.", Type: tNum, Listable: true},
			{Key: "side", Label: "Seite", Type: tSel, Required: true, Listable: true, Options: fsSides},
			{Key: "label", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "parent_code", Label: "Übergeordneter Posten (Summe)", Short: "Übergeordnet", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: fsItemObject, ValueField: "item_code", LabelFields: []string{"label"}, Filters: map[string]string{"schema_code": "schema_code"}}},
			{Key: "source", Label: "Quelle (leer = Konten)", Type: tSel, Listable: true, Options: fsSources, Trigger: true},
			{Key: "accounts", Label: "Konten (Bereiche und Nummern, z. B. 0200-0399, 1376; Unterkonten zählen zu ihren ersten Stellen)", Short: "Konten",
				Type: tText, Listable: true, ShowIf: isAccounts},
			{Key: "description", Label: "Beschreibung", Type: tArea},
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["item_code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["item_code"])))
			if !fsCodeRe.MatchString(crud.Str(rec["item_code"])) {
				return crud.Invalid("Posten %q: Großbuchstaben, Ziffern, Punkt, _ und - (höchstens 30 Zeichen)", crud.Str(rec["item_code"]))
			}
			if crud.Str(rec["source"]) == "" {
				rec["source"] = "ACCOUNTS"
			}
			if crud.Str(rec["source"]) == "RESULT" {
				rec["accounts"] = nil
			} else {
				acc := strings.Join(strings.Fields(crud.Str(rec["accounts"])), "")
				if acc != "" && !fsAccountsRe.MatchString(acc) {
					return crud.Invalid("Konten %q: Bereiche und Nummern mit Komma, z. B. 0200-0399, 1376", crud.Str(rec["accounts"]))
				}
				for _, r := range strings.Split(acc, ",") {
					if lo, hi, ok := strings.Cut(r, "-"); ok && (len(lo) != len(hi) || lo > hi) {
						return crud.Invalid("Konten %q: von und bis gleich lang, von ≤ bis", r)
					}
				}
				rec["accounts"] = nilIfEmpty(strings.ReplaceAll(acc, ",", ", "))
			}
			parent := strings.ToUpper(strings.TrimSpace(crud.Str(rec["parent_code"])))
			if parent == crud.Str(rec["item_code"]) {
				return crud.Invalid("Posten %s: nicht sich selbst übergeordnet", parent)
			}
			if parent != "" {
				res, err := m.db.Query(ctx, `SELECT 1 FROM ledger__fs_item WHERE schema_code = ? AND item_code = ?`, crud.Str(rec["schema_code"]), parent)
				if err != nil {
					return err
				}
				if len(res.Rows) == 0 {
					return crud.Invalid("Übergeordneter Posten %s fehlt im Bilanzschema %s", parent, crud.Str(rec["schema_code"]))
				}
			}
			rec["parent_code"] = nilIfEmpty(parent)
			return nil
		},
	}
}

type fsProposal struct {
	Code, Name, Chart, Description string
	Items                          []struct {
		Code, Side, Parent, Label, Accounts, Source string
	}
}

// fsLoadProposalAction legt das mitgelieferte Schema eines Kontenplans an
// (fs/<kontenplan>.json); fehlende Posten kommen hinzu, vorhandene bleiben.
func (m *Module) fsLoadProposalAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	chart := strings.ToUpper(param(req.Payload, "chart", "chart_of_accounts_id"))
	b, err := fsFiles.ReadFile("fs/" + strings.ToLower(chart) + ".json")
	if err != nil || !codeRe.MatchString(chart) {
		return sdk.Response{}, crud.Invalid("für den Kontenplan %q ist kein Bilanzschema mitgeliefert (SKR04)", chart)
	}
	var p fsProposal
	if err := json.Unmarshal(b, &p); err != nil {
		return sdk.Response{}, fmt.Errorf("fs/%s.json: %w", strings.ToLower(chart), err)
	}
	if err := m.migrate(ctx); err != nil {
		return sdk.Response{}, err
	}
	exists := func(q string, args ...any) (bool, error) {
		res, err := m.db.Query(ctx, q, args...)
		return err == nil && len(res.Rows) > 0, err
	}
	created := 0
	if ok, err := exists(`SELECT 1 FROM ledger__fs_schema WHERE code = ?`, p.Code); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		if _, err := m.set.Entity(fsSchemaObject).Create(ctx, map[string]any{"data": map[string]any{"code": p.Code, "name": p.Name,
			"chart_of_accounts_id": chart, "description": p.Description}}); err != nil {
			return sdk.Response{}, err
		}
	}
	for i, it := range p.Items {
		if ok, err := exists(`SELECT 1 FROM ledger__fs_item WHERE schema_code = ? AND item_code = ?`, p.Code, it.Code); err != nil {
			return sdk.Response{}, err
		} else if ok {
			continue
		}
		if _, err := m.set.Entity(fsItemObject).Create(ctx, map[string]any{"data": map[string]any{"schema_code": p.Code, "item_code": it.Code,
			"sort_order": (i + 1) * 10, "side": it.Side, "label": it.Label, "parent_code": it.Parent, "source": firstNonEmpty(it.Source, "ACCOUNTS"),
			"accounts": it.Accounts}}); err != nil {
			return sdk.Response{}, fmt.Errorf("Posten %s: %w", it.Code, err)
		}
		created++
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bilanzschema %s (%s): %d Posten angelegt, %d vorhanden", p.Code, p.Name, created, len(p.Items)-created)}}, nil
}
