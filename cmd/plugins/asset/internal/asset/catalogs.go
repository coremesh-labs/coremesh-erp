package asset

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Kataloge je Buchungskreis. Jeder Buchungskreis prägt sie selbst aus; beim
// ersten Objekt im Buchungskreis (oder mit asset:setup-company) entstehen die
// Vorschlagswerte (catalogDefaults), soweit sie fehlen.

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z]{1,4}$`)
)

func (m *Module) catalogEntities() []*crud.Entity {
	out := make([]*crud.Entity, 0, len(catalogTables))
	for _, c := range catalogTables {
		out = append(out, m.catalogEntity(c))
	}
	return out
}

func (m *Module) catalogEntity(c catalogTable) *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	switch c.Table {
	case "asset_type":
		fields = append(fields,
			crud.Field{Key: "id_prefix", Label: "Kürzel für Objekt-IDs (z. B. AU)", Short: "Kürzel", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "part_types", Label: "Erlaubte Bestandteile (Objektarten, z. B. REIFEN; leer = keine)", Short: "Bestandteile", Type: tText, Listable: true})
	case "asset_role":
		fields = append(fields,
			crud.Field{Key: "is_exclusive", Label: "Exklusiv (eine Person je Stichtag)", Short: "Exklusiv", Type: tBool, Listable: true})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Customizing: true, Title: c.Title, Icon: "icon-tag", Table: "asset__" + c.Table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: fields,
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, c.Object, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["code"]))
				}
			}
			if c.Table == "asset_type" {
				return m.checkAssetType(ctx, rec)
			}
			return nil
		},
	}
}

// checkAssetType: Kürzel (eindeutig im Buchungskreis), Bestandteile (Objektarten
// des Katalogs; die eigene Art ist erlaubt, z. B. Gerät ← Zubehör-Gerät).
func (m *Module) checkAssetType(ctx context.Context, rec crud.Record) error {
	cc, code := crud.Str(rec["company_code"]), crud.Str(rec["code"])
	prefix := strings.TrimSpace(crud.Str(rec["id_prefix"]))
	if !prefixRe.MatchString(prefix) {
		return crud.Invalid("Kürzel %q: 1–4 Buchstaben, z. B. AU", prefix)
	}
	rec["id_prefix"] = strings.ToUpper(prefix)
	res, err := m.db.Query(ctx, `SELECT code FROM asset__asset_type WHERE company_code = ? AND UPPER(id_prefix) = ? AND code <> ?`,
		cc, rec["id_prefix"], code)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		return crud.Invalid("Kürzel %s ist schon bei Objektart %s vergeben", rec["id_prefix"], crud.Str(res.Rows[0][0]))
	}
	types := splitCodes(crud.Str(rec["part_types"]))
	for _, t := range types {
		if t == code {
			continue
		}
		ok, err := m.exists(ctx, "asset_type", cc, t)
		if err != nil {
			return err
		}
		if !ok {
			return crud.Invalid("Bestandteil: Objektart %s gibt es im Buchungskreis %s nicht", t, cc)
		}
	}
	rec["part_types"] = nilIfEmpty(strings.Join(types, ","))
	return nil
}

// splitCodes: "reifen, geraet" → [REIFEN GERAET] (ohne Doppelte).
func splitCodes(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.FieldsFunc(strings.ToUpper(text), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// catalogHas: Gibt es den aktiven Eintrag code im Katalog des Buchungskreises?
func (m *Module) catalogHas(ctx context.Context, table, cc, code string) (bool, error) {
	res, err := m.db.Query(ctx, "SELECT 1 FROM asset__"+table+" WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// requireCatalog: leerer Wert ist erlaubt, sonst muss er im Katalog stehen.
func (m *Module) requireCatalog(ctx context.Context, table, label, cc string, v any) error {
	code := crud.Str(v)
	if code == "" {
		return nil
	}
	ok, err := m.catalogHas(ctx, table, cc, code)
	if err != nil {
		return err
	}
	if !ok {
		return crud.Invalid("%s %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Kataloge unter Objekte → Einstellungen", label, code, cc)
	}
	return nil
}

// exists: Eintrag code im Katalog (auch inaktiv).
func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	res, err := m.db.Query(ctx, "SELECT 1 FROM asset__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// catalogDefaults: Vorschlagswerte je Katalog (Schlüssel, Bezeichnung, Zusatzspalten).
var catalogDefaults = map[string][]map[string]any{
	"asset_type": {
		{"code": "WOHNUNG", "name": "Wohnung", "id_prefix": "WO", "part_types": "GERAET"},
		{"code": "AUTO", "name": "Auto", "id_prefix": "AU", "part_types": "REIFEN"},
		{"code": "REIFEN", "name": "Reifensatz", "id_prefix": "RS"},
		{"code": "FAHRRAD", "name": "Fahrrad", "id_prefix": "FR"},
		{"code": "HANDY", "name": "Handy", "id_prefix": "HY"},
		{"code": "GERAET", "name": "Gerät", "id_prefix": "GE", "part_types": "GERAET"},
		{"code": "REISE", "name": "Reise", "id_prefix": "RE"},
		{"code": "TIER", "name": "Haustier", "id_prefix": "TI"},
		{"code": "SONST", "name": "Sonstiges", "id_prefix": "SO"},
	},
	"asset_status": {
		{"code": "PLANNED", "name": "geplant"}, {"code": "ACTIVE", "name": "aktiv"},
		{"code": "SOLD", "name": "verkauft"}, {"code": "RETIRED", "name": "abgegangen"},
	},
	"asset_role": {
		{"code": "OWNER", "name": "Eigentümer"}, {"code": "USER", "name": "Nutzer"},
		{"code": "HOLDER", "name": "Halter", "is_exclusive": true}, {"code": "DRIVER", "name": "Fahrer"},
		{"code": "INSURED", "name": "Versicherte Person"}, {"code": "TRAVELER", "name": "Reisender"},
	},
}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an und liefert
// die Zahl der neuen Einträge.
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	n := 0
	for _, c := range catalogTables {
		for i, row := range catalogDefaults[c.Table] {
			ok, err := m.exists(ctx, c.Table, cc, crud.Str(row["code"]))
			if err != nil {
				return 0, err
			}
			if ok {
				continue
			}
			cols := []string{"company_code", "sort_order", "is_active"}
			args := []any{cc, (i + 1) * 10, true}
			for _, k := range []string{"code", "name", "id_prefix", "part_types", "is_exclusive"} {
				if v, ok := row[k]; ok {
					cols, args = append(cols, k), append(args, v)
				}
			}
			if _, err := m.db.Exec(ctx, "INSERT INTO asset__"+c.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

// setupCompanyAction: AssetSetup.setupCompany {company} (Konsole asset:setup-company).
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
		Data        struct {
			CompanyCode string `json:"company_code"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company + in.CompanyCode + in.Data.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, "AssetType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		n, err = m.setupCatalogs(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n,
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der Objekte angelegt", cc, n)}}, nil
}

// withCatalogLabels: Felder mit Katalog-Lookup zeigen die Bezeichnung aus dem
// Katalog des Buchungskreises (statt des Schlüssels).
func (m *Module) withCatalogLabels(e *crud.Entity) {
	tables := map[string]string{}
	for _, c := range catalogTables {
		tables[c.Object] = c.Table
	}
	fields := map[string]string{} // Feld → Katalogtabelle
	for _, f := range e.Fields {
		if f.Lookup != nil && tables[f.Lookup.Object] != "" {
			fields[f.Key] = tables[f.Lookup.Object]
		}
	}
	if len(fields) == 0 {
		return
	}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		if decorate != nil {
			if err := decorate(ctx, rec); err != nil {
				return err
			}
		}
		cc := crud.Str(rec["company_code"])
		for key, table := range fields {
			code := crud.Str(rec[key])
			if code == "" {
				continue
			}
			res, err := m.db.Query(ctx, "SELECT name FROM asset__"+table+" WHERE company_code = ? AND code = ?", cc, code)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				continue
			}
			setLabel(rec, key, crud.Str(res.Rows[0][0]))
		}
		return nil
	}
}

func setLabel(rec crud.Record, key, label string) {
	labels, _ := rec["_labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
		rec["_labels"] = labels
	}
	labels[key] = label
}
