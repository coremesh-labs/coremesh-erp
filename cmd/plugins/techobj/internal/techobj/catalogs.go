package techobj

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Kataloge je Buchungskreis. Jeder Buchungskreis prägt sie selbst aus; beim
// ersten Datensatz im Buchungskreis (oder mit techobj:setup-company) entstehen
// die Vorschlagswerte (catalogDefaults), soweit sie fehlen.

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9_]{1,12}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z]{1,4}$`)
	idRe     = regexp.MustCompile(`^[A-Za-z0-9_-]{2,30}$`)
)

func (m *Module) catalogEntities() []*crud.Entity {
	out := make([]*crud.Entity, 0, len(catalogTables))
	for _, c := range catalogTables {
		e := m.catalogEntity(c)
		m.withLabels(e)
		out = append(out, e)
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
	case "object_class":
		fields = append(fields,
			crud.Field{Key: "id_prefix", Label: "Kürzel für IDs (z. B. ZA → ZA0001)", Short: "Kürzel", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "is_meter", Label: "Zähler (hat Zählwerke, wird in Messpunkte eingebaut)", Short: "Zähler", Type: tBool, Listable: true})
	case "unit":
		fields = append(fields, crud.Field{Key: "decimals", Label: "Nachkommastellen", Short: "Stellen", Type: tNum, Listable: true})
	case "medium":
		fields = append(fields,
			crud.Field{Key: "unit", Label: "Einheit des Hauptzählwerks", Short: "Einheit", Type: tText, Listable: true, Lookup: catalogLookup("TechUnit")},
			crud.Field{Key: "calibration_years", Label: "Eichfrist in Jahren (0 = keine)", Short: "Eichfrist", Type: tNum, Listable: true})
	case "link_object":
		fields = append(fields,
			crud.Field{Key: "service_object", Label: "Object im anderen Modul (z. B. RentObject, Contract, BusinessPartner)", Short: "Object", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "company_scoped", Label: "Schlüssel mit Buchungskreis (<BuKr>|<ID>)", Short: "mit BuKr", Type: tBool, Listable: true},
			crud.Field{Key: "tenant_contract_types", Label: "Vertragsarten des Nutzers (z. B. MV, GM, SP; zeigt den Mieter)", Short: "Nutzerverträge", Type: tText})
	case "link_role":
		fields = append(fields,
			crud.Field{Key: "object_types", Label: "Erlaubte Objekte (Schlüssel, durch Komma; leer = alle)", Short: "Objekte", Type: tText, Listable: true},
			crud.Field{Key: "is_exclusive", Label: "Exklusiv (ein Objekt je Stichtag)", Short: "Exklusiv", Type: tBool, Listable: true})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Customizing: true, Title: c.Title, Icon: "icon-tag", Table: "techobj__" + c.Table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields:      fields,
		Access:      &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: checkWrite(c.Object),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["code"]))
				}
			}
			switch c.Table {
			case "object_class":
				return m.checkClass(ctx, rec)
			case "medium":
				rec["unit"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["unit"])))
				if toInt(rec["calibration_years"]) < 0 {
					return crud.Invalid("Eichfrist darf nicht negativ sein")
				}
				return m.requireCatalog(ctx, "unit", "Einheit", cc, rec["unit"])
			case "unit":
				if d := toInt(rec["decimals"]); d < 0 || d > 6 {
					return crud.Invalid("Nachkommastellen: 0 bis 6")
				}
			case "link_object":
				rec["service_object"] = strings.TrimSpace(crud.Str(rec["service_object"]))
				rec["tenant_contract_types"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["tenant_contract_types"])), ","))
			case "link_role":
				types := splitCodes(crud.Str(rec["object_types"]))
				for _, t := range types {
					if ok, err := m.exists(ctx, "link_object", cc, t); err != nil {
						return err
					} else if !ok {
						return crud.Invalid("Verknüpfbares Objekt %s gibt es im Buchungskreis %s nicht", t, cc)
					}
				}
				rec["object_types"] = nilIfEmpty(strings.Join(types, ","))
			}
			return nil
		},
	}
}

// checkClass: Kürzel eindeutig im Buchungskreis.
func (m *Module) checkClass(ctx context.Context, rec crud.Record) error {
	prefix := strings.TrimSpace(crud.Str(rec["id_prefix"]))
	if !prefixRe.MatchString(prefix) {
		return crud.Invalid("Kürzel %q: 1–4 Buchstaben, z. B. ZA", prefix)
	}
	rec["id_prefix"] = strings.ToUpper(prefix)
	other, err := m.one(ctx, `SELECT code FROM techobj__object_class WHERE company_code = ? AND UPPER(id_prefix) = ? AND code <> ?`,
		crud.Str(rec["company_code"]), rec["id_prefix"], crud.Str(rec["code"]))
	if err != nil {
		return err
	}
	if other != "" {
		return crud.Invalid("Kürzel %s ist schon bei Objektklasse %s vergeben", rec["id_prefix"], other)
	}
	return nil
}

// splitCodes: "a, b" → [A B] (ohne Doppelte).
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
	v, err := m.one(ctx, "SELECT 1 FROM techobj__"+table+" WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	return v != "", err
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
		return crud.Invalid("%s %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Kataloge unter Technische Objekte → Einstellungen", label, code, cc)
	}
	return nil
}

func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	v, err := m.one(ctx, "SELECT 1 FROM techobj__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	return v != "", err
}

// catalogDefaults: Vorschlagswerte je Katalog. Was eine Objektklasse, Sparte
// oder Rolle ausmacht, steht hier nur als Vorschlag – jeder Buchungskreis
// ändert und ergänzt die Kataloge selbst.
var catalogDefaults = map[string][]map[string]any{
	"object_class": {
		{"code": "ZAEHLER", "name": "Zähler", "id_prefix": "ZA", "is_meter": true},
		{"code": "HKV", "name": "Heizkostenverteiler", "id_prefix": "HK", "is_meter": true},
		{"code": "HEIZUNG", "name": "Heizungsanlage / Wärmeerzeuger", "id_prefix": "HZ"},
		{"code": "WARMWASSER", "name": "Warmwasserbereiter / Speicher", "id_prefix": "WW"},
		{"code": "RWM", "name": "Rauchwarnmelder", "id_prefix": "RW"},
		{"code": "AUFZUG", "name": "Aufzug", "id_prefix": "AF"},
		{"code": "LUEFTUNG", "name": "Lüftung / Klima", "id_prefix": "LU"},
		{"code": "PUMPE", "name": "Pumpe / Hebeanlage", "id_prefix": "PU"},
		{"code": "SONST", "name": "Sonstige Anlage", "id_prefix": "SO"},
	},
	"status": {
		{"code": "PLANNED", "name": "geplant"}, {"code": "ACTIVE", "name": "in Betrieb"},
		{"code": "STOCK", "name": "im Lager"}, {"code": "RETIRED", "name": "außer Betrieb / ausgebaut"},
	},
	"location_level": {
		{"code": "GEBAEUDE", "name": "Gebäude"}, {"code": "GESCHOSS", "name": "Geschoss / Bereich"},
		{"code": "EINHEIT", "name": "Wohnung / Einheit"}, {"code": "RAUM", "name": "Raum"},
		{"code": "SCHRANK", "name": "Zählerschrank / Stelle"},
	},
	"unit": {
		{"code": "KWH", "name": "kWh", "decimals": 1}, {"code": "MWH", "name": "MWh", "decimals": 3},
		{"code": "M3", "name": "m³", "decimals": 3}, {"code": "EINH", "name": "Einheiten (HKV)", "decimals": 0},
		{"code": "H", "name": "Betriebsstunden", "decimals": 0}, {"code": "BAR", "name": "bar", "decimals": 2},
	},
	"medium": {
		{"code": "STROM", "name": "Strom", "unit": "KWH", "calibration_years": 8},
		{"code": "GAS", "name": "Gas", "unit": "M3", "calibration_years": 8},
		{"code": "KW", "name": "Kaltwasser", "unit": "M3", "calibration_years": 6},
		{"code": "WW", "name": "Warmwasser", "unit": "M3", "calibration_years": 5},
		{"code": "WAERME", "name": "Wärme (Wärmemengenzähler)", "unit": "KWH", "calibration_years": 5},
		{"code": "HKV", "name": "Heizkostenverteiler", "unit": "EINH", "calibration_years": 0},
		{"code": "SONST", "name": "Sonstiges (Betriebsstunden, Druck …)", "unit": "H", "calibration_years": 0},
	},
	"number_type": {
		{"code": "SERIAL", "name": "Seriennummer (Fabriknummer)"},
		{"code": "METER", "name": "Zählernummer / Eigentumsnummer"},
		{"code": "RADIO", "name": "Funk-ID / Modulnummer"},
		{"code": "PROVIDER", "name": "Gerätenummer beim Messdienst"},
	},
	"relation_type": {
		{"code": "SUB", "name": "Unterzähler (im übergeordneten enthalten)"},
		{"code": "REST", "name": "Restverbrauch (übergeordneter minus Unterzähler)"},
		{"code": "SUM", "name": "Summe (rechnerischer Messpunkt)"},
	},
	"reading_type": {
		{"code": "PERIODIC", "name": "turnusmäßige Ablesung"}, {"code": "INTERIM", "name": "Zwischenablesung"},
		{"code": "HANDOVER", "name": "Übergabe (Ein-/Auszug)"}, {"code": "INSTALL", "name": "Einbau"},
		{"code": "REMOVE", "name": "Ausbau"}, {"code": "SUPPLIER", "name": "Versorgerwechsel"},
		{"code": "ESTIMATE", "name": "Schätzung"}, {"code": "PROVIDER", "name": "Ablesung durch Versorger / Messdienst"},
		{"code": "HISTORIC", "name": "historisch nachgetragen"},
	},
	"link_object": {
		{"code": "RENTOBJ", "name": "Mietobjekt", "service_object": "RentObject", "company_scoped": true, "tenant_contract_types": "MV,GM,SP"},
		{"code": "BUILDING", "name": "Gebäude", "service_object": "Building", "company_scoped": true},
		{"code": "ENTITY", "name": "Wirtschaftseinheit", "service_object": "BusinessEntity", "company_scoped": true},
		{"code": "ASSET", "name": "Objekt (Wohnung, Haus, Auto …)", "service_object": "Asset", "company_scoped": true},
		{"code": "CONTRACT", "name": "Vertrag", "service_object": "Contract", "company_scoped": true},
		{"code": "PARTNER", "name": "Geschäftspartner", "service_object": "BusinessPartner", "company_scoped": false},
	},
	"link_role": {
		{"code": "SITE", "name": "Standort (gehört zu)", "object_types": "RENTOBJ,BUILDING,ENTITY,ASSET"},
		{"code": "MEASURES", "name": "misst für (Nutzer)", "object_types": "RENTOBJ,BUILDING,ENTITY,ASSET,CONTRACT"},
		{"code": "SUPPLIER", "name": "Lieferant (Liefervertrag)", "object_types": "CONTRACT", "is_exclusive": true},
		{"code": "GRID", "name": "Netzbetreiber / Messstellenbetreiber", "object_types": "PARTNER,CONTRACT", "is_exclusive": true},
		{"code": "PROVIDER", "name": "Messdienst (Abrechnung, Gerätemiete)", "object_types": "CONTRACT,PARTNER"},
		{"code": "WEG", "name": "WEG / Verwaltung (Meldung)", "object_types": "CONTRACT,PARTNER"},
		{"code": "SERVICE", "name": "Wartung / Service", "object_types": "CONTRACT,PARTNER"},
		{"code": "OWNER", "name": "Eigentümer", "object_types": "PARTNER"},
	},
}

// catalogColumns: Zusatzspalten der Vorschlagswerte.
var catalogColumns = []string{"code", "name", "id_prefix", "is_meter", "decimals", "unit", "calibration_years", "service_object",
	"company_scoped", "tenant_contract_types", "object_types", "is_exclusive"}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an.
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	if ok, err := m.catalogHas(ctx, "status", cc, "ACTIVE"); err != nil || ok {
		// Bereits eingerichtet: nur auf ausdrücklichen Wunsch (setupCompany) ergänzen.
		if err != nil || ctx.Value(setupKey{}) == nil {
			return 0, err
		}
	}
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
			for _, k := range catalogColumns {
				if v, ok := row[k]; ok {
					cols, args = append(cols, k), append(args, v)
				}
			}
			if _, err := m.db.Exec(ctx, "INSERT INTO techobj__"+c.Table+" ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

type setupKey struct{}

// setupCompanyAction: TechObjectSetup.setupCompany {company} (Konsole techobj:setup-company).
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
	if err := requireWrite(ctx, "TechObjectClass", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		n, err = m.setupCatalogs(context.WithValue(ctx, setupKey{}, true), cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n,
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der technischen Objekte angelegt", cc, n)}}, nil
}

// refTables: Lookup-Object → Tabelle, Schlüsselspalte, Bezeichnung – für die
// Anzeige der Bezeichnung statt des Schlüssels (_labels).
func refTables() map[string][3]string {
	out := map[string][3]string{
		"TechLocation":   {"techobj__location", "location_id", "designation"},
		"TechObjectType": {"techobj__equipment_type", "type_id", "designation"},
		"TechObject":     {"techobj__equipment", "equipment_id", "designation"},
		"MeterPoint":     {"techobj__point", "point_id", "designation"},
	}
	for _, c := range catalogTables {
		out[c.Object] = [3]string{"techobj__" + c.Table, "code", "name"}
	}
	return out
}

// withLabels: Felder mit Lookup auf Kataloge und Objekte dieses Moduls zeigen
// die Bezeichnung des Buchungskreises; Partner zeigen den Namen.
func (m *Module) withLabels(e *crud.Entity) {
	tables := refTables()
	type ref struct {
		key     string
		t       [3]string
		partner bool
	}
	var refs []ref
	for _, f := range e.Fields {
		if f.Lookup == nil {
			continue
		}
		if t, ok := tables[f.Lookup.Object]; ok {
			refs = append(refs, ref{key: f.Key, t: t})
		} else if f.Lookup.Object == "BusinessPartner" {
			refs = append(refs, ref{key: f.Key, partner: true})
		}
	}
	if len(refs) == 0 {
		return
	}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		cc := crud.Str(rec["company_code"])
		for _, r := range refs {
			v := crud.Str(rec[r.key])
			if v == "" {
				continue
			}
			if r.partner {
				if name := m.partnerName(ctx, v); name != "" {
					setLabel(rec, r.key, name)
				}
				continue
			}
			label, err := m.one(ctx, "SELECT "+r.t[2]+" FROM "+r.t[0]+" WHERE company_code = ? AND "+r.t[1]+" = ?", cc, v)
			if err != nil {
				return err
			}
			if label != "" {
				setLabel(rec, r.key, label)
			}
		}
		if decorate != nil {
			return decorate(ctx, rec)
		}
		return nil
	}
}
