package realestate

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge je Buchungskreis. Jeder Buchungskreis prägt sie selbst aus; beim
// Anlegen der ersten Wirtschaftseinheit (oder mit realestate:setup-company)
// entstehen die Vorschlagswerte (catalogDefaults), soweit sie fehlen.

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z]{1,4}$`)
)

// objectKinds: Arten der Mietobjekte.
const (
	kindUnit      = "UNIT"
	kindSpace     = "SPACE"
	kindPool      = "POOL"
	kindComposite = "COMPOSITE"
)

var kindOptions = []metamodel.Option{
	{Value: kindUnit, Label: "Mieteinheit"}, {Value: kindSpace, Label: "Fläche"},
	{Value: kindPool, Label: "Pool"}, {Value: kindComposite, Label: "Vertragsobjekt"},
}

func (m *Module) catalogEntities() []*crud.Entity {
	out := make([]*crud.Entity, 0, len(catalogTables))
	for _, c := range catalogTables {
		out = append(out, m.catalogEntity(c))
	}
	return out
}

func (m *Module) catalogEntity(c catalogTable) *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	switch c.Table {
	case "usage_type":
		fields = append(fields,
			crud.Field{Key: "id_prefix", Label: "Kürzel für Objekt-IDs (z. B. WG)", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "kinds", Label: "Gilt für (UNIT, SPACE, POOL, COMPOSITE; leer = alle)", Type: tText, Listable: true})
	case "measurement_type":
		fields = append(fields,
			crud.Field{Key: "default_unit", Label: "Standard-Maßeinheit", Type: tText, Listable: true, Lookup: catalogLookup("MeasureUnit")},
			crud.Field{Key: "is_area", Label: "Fläche (Pool-Prüfung)", Type: tBool, Listable: true})
	case "entity_type":
		fields = append(fields,
			crud.Field{Key: "area_check", Label: "Flächen der Mieteinheiten ≤ Fläche des Gebäudes", Type: tBool, Listable: true})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Title: c.Title, Icon: "icon-tag", Table: "realestate__" + c.Table, Section: "Einstellungen",
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
			return m.checkCatalogEntry(ctx, c.Table, rec)
		},
	}
}

// checkCatalogEntry: Zusatzregeln einzelner Kataloge.
func (m *Module) checkCatalogEntry(ctx context.Context, table string, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	switch table {
	case "usage_type":
		prefix := strings.TrimSpace(crud.Str(rec["id_prefix"]))
		if !prefixRe.MatchString(prefix) {
			return crud.Invalid("Kürzel %q: 1–4 Buchstaben, z. B. WG", prefix)
		}
		rec["id_prefix"] = prefix
		res, err := m.db.Query(ctx, `SELECT code FROM realestate__usage_type WHERE company_code = ? AND LOWER(id_prefix) = LOWER(?) AND code <> ?`,
			cc, prefix, rec["code"])
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			return crud.Invalid("Kürzel %s ist schon bei Nutzungsart %s vergeben", prefix, crud.Str(res.Rows[0][0]))
		}
		var kinds []string
		for _, k := range strings.Split(trimUpper(rec["kinds"]), ",") {
			if k = strings.TrimSpace(k); k == "" {
				continue
			}
			if !slices.ContainsFunc(kindOptions, func(o metamodel.Option) bool { return o.Value == k }) {
				return crud.Invalid("Objektart %q – erlaubt: UNIT, SPACE, POOL, COMPOSITE", k)
			}
			kinds = append(kinds, k)
		}
		rec["kinds"] = nilIfEmpty(strings.Join(kinds, ","))
	case "measurement_type":
		return m.requireCatalog(ctx, "measure_unit", "Standard-Maßeinheit", cc, rec["default_unit"])
	}
	return nil
}

// catalogHas: Gibt es den aktiven Eintrag code im Katalog des Buchungskreises?
func (m *Module) catalogHas(ctx context.Context, table, cc, code string) (bool, error) {
	res, err := m.db.Query(ctx, "SELECT 1 FROM realestate__"+table+" WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
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
		return crud.Invalid("%s %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Kataloge unter Immobilien → Einstellungen", label, code, cc)
	}
	return nil
}

// catalogDefaults: Vorschlagswerte je Katalog (Schlüssel, Bezeichnung, Zusatzspalten).
var catalogDefaults = map[string][]map[string]any{
	"usage_type": {
		{"code": "WOHNEN", "name": "Wohnen", "id_prefix": "WG", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "GEWERBE", "name": "Gewerbe", "id_prefix": "GW", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "BUERO", "name": "Büro", "id_prefix": "BU", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "LAGER", "name": "Lager", "id_prefix": "LG", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "STELLPLATZ", "name": "Stellplatz", "id_prefix": "SP", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "GARAGE", "name": "Garage", "id_prefix": "GA", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "KELLER", "name": "Keller", "id_prefix": "KE", "kinds": "UNIT,SPACE,COMPOSITE"},
		{"code": "FREIFLAECHE", "name": "Freifläche", "id_prefix": "FF", "kinds": "SPACE,POOL,COMPOSITE"},
		{"code": "POOL", "name": "Flächenpool", "id_prefix": "PL", "kinds": "POOL"},
	},
	"measurement_type": {
		{"code": "WFL", "name": "Wohnfläche (WoFlV)", "default_unit": "M2", "is_area": true},
		{"code": "NFL", "name": "Nutzfläche", "default_unit": "M2", "is_area": true},
		{"code": "HZF", "name": "Heizfläche", "default_unit": "M2", "is_area": true},
		{"code": "MEA", "name": "Miteigentumsanteil", "default_unit": "TSD", "is_area": false},
		{"code": "ZI", "name": "Zimmer", "default_unit": "ST", "is_area": false},
		{"code": "SPL", "name": "Stellplätze", "default_unit": "ST", "is_area": false},
		{"code": "VOL", "name": "Rauminhalt", "default_unit": "M3", "is_area": false},
	},
	"measure_unit": {
		{"code": "M2", "name": "m²"}, {"code": "M3", "name": "m³"}, {"code": "ST", "name": "Stück"},
		{"code": "TSD", "name": "Tausendstel (‰)"}, {"code": "ZTSD", "name": "Zehntausendstel (‱)"}, {"code": "PCT", "name": "Prozent"},
	},
	"object_status": {
		{"code": "PLANNED", "name": "geplant"}, {"code": "ACTIVE", "name": "aktiv"},
		{"code": "BLOCKED", "name": "gesperrt"}, {"code": "RETIRED", "name": "abgegangen"},
	},
	"entity_type": {
		{"code": "OWN", "name": "Eigenbestand", "area_check": true}, {"code": "WEG", "name": "WEG-Verwaltung"},
		{"code": "ETW", "name": "Eigentumswohnungen in WEG (Eigenbestand)"},
		{"code": "SEV", "name": "Sondereigentumsverwaltung"}, {"code": "MGMT", "name": "Fremdverwaltung"},
	},
	"building_type": {
		{"code": "RES", "name": "Wohnhaus"}, {"code": "COM", "name": "Gewerbeobjekt"},
		{"code": "MIX", "name": "Mischnutzung"}, {"code": "GAR", "name": "Garage / Parkhaus"},
	},
	"floor": {
		{"code": "UG", "name": "Untergeschoss"}, {"code": "EG", "name": "Erdgeschoss"},
		{"code": "OG1", "name": "1. Obergeschoss"}, {"code": "OG2", "name": "2. Obergeschoss"},
		{"code": "OG3", "name": "3. Obergeschoss"}, {"code": "OG4", "name": "4. Obergeschoss"},
		{"code": "DG", "name": "Dachgeschoss"},
	},
	"location": {
		{"code": "L", "name": "links"}, {"code": "M", "name": "Mitte"}, {"code": "R", "name": "rechts"},
		{"code": "VH", "name": "Vorderhaus"}, {"code": "HH", "name": "Hinterhaus"}, {"code": "SF", "name": "Seitenflügel"},
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
			for _, k := range []string{"code", "name", "id_prefix", "kinds", "default_unit", "is_area", "area_check"} {
				if v, ok := row[k]; ok {
					cols, args = append(cols, k), append(args, v)
				}
			}
			if _, err := m.db.Exec(ctx, "INSERT INTO realestate__"+c.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

// exists: Eintrag code im Katalog (auch inaktiv).
func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	res, err := m.db.Query(ctx, "SELECT 1 FROM realestate__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	if err != nil {
		return false, err
	}
	return len(res.Rows) > 0, nil
}

// setupCompanyAction: RealEstateSetup.setupCompany {company} (Konsole realestate:setup-company).
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
	if err := requireWrite(ctx, "UsageType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var n, roles int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		if n, err = m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		roles, err = m.setupPartnerRoles(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	rules, hint := m.setupDisplayRules(ctx)
	msg := fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge, %d Partnerrollen, %d Darstellungsregeln angelegt", cc, n, roles, rules)
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n, "partner_roles": roles, "display_rules": rules,
		"message": joinNonEmpty(". ", msg, hint)}}, nil
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
			res, err := m.db.Query(ctx, "SELECT name FROM realestate__"+table+" WHERE company_code = ? AND code = ?", cc, code)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				continue
			}
			labels, _ := rec["_labels"].(map[string]any)
			if labels == nil {
				labels = map[string]any{}
				rec["_labels"] = labels
			}
			labels[key] = crud.Str(res.Rows[0][0])
		}
		return nil
	}
}
