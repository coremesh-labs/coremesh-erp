package heatcost

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge je Buchungskreis. Bei der ersten Abrechnungseinheit im Buchungskreis
// (oder mit heatcost:setup-company) entstehen die Vorschlagswerte, soweit sie fehlen.

var codeRe = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)

// Aufteilung einer Kostenart.
const (
	allocSplit    = "SPLIT"
	allocHeating  = "HEATING"
	allocHotwater = "HOTWATER"
	allocKey      = "KEY"
	allocDirect   = "DIRECT"
)

var allocationOptions = []metamodel.Option{
	{Value: allocSplit, Label: "Heizung und Warmwasser nach Warmwasseranteil"},
	{Value: allocHeating, Label: "nur Heizung"},
	{Value: allocHotwater, Label: "nur Warmwasser"},
	{Value: allocKey, Label: "nach eigenem Schlüssel (z. B. Hausnebenkosten)"},
	{Value: allocDirect, Label: "direkt an einen Nutzer"},
}

// Herkunft der Werte eines Schlüssels beim „Werte übernehmen“.
const (
	srcManual      = "MANUAL"
	srcArea        = "AREA"
	srcConsumption = "CONSUMPTION"
	srcDevices     = "DEVICES"
)

var sourceOptions = []metamodel.Option{
	{Value: srcManual, Label: "Eingabe"},
	{Value: srcArea, Label: "Fläche bzw. Bemessung des Mietobjekts (Bemessungsart)"},
	{Value: srcConsumption, Label: "Verbrauch der Sparte im Zeitraum (Zähler mit Bewertungsfaktor)"},
	{Value: srcDevices, Label: "Zahl der Messpunkte der Sparte"},
}

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
	case "cost_kind":
		fields = append(fields,
			crud.Field{Key: "cost_group", Label: "Kostengruppe (Abschnitt der Kostenaufstellung)", Short: "Gruppe", Type: tText, Listable: true, Lookup: catalogLookup("HeatCostGroup")},
			crud.Field{Key: "allocation", Label: "Aufteilung", Type: tSel, Required: true, Listable: true, Options: allocationOptions},
			crud.Field{Key: "key_code", Label: "Schlüssel (bei Aufteilung nach eigenem Schlüssel)", Short: "Schlüssel", Type: tText, Listable: true, Lookup: catalogLookup("HeatCostKey")},
			crud.Field{Key: "ww_percent", Label: "Warmwasseranteil in % (leer = aus der Abrechnung, z. B. nach Wärmezähler)", Short: "WW %", Type: tNum},
			crud.Field{Key: "apportionable", Label: "Auf Mieter umlegbar", Short: "Umlegbar", Type: tBool, Listable: true},
			crud.Field{Key: "text", Label: "Ausweistext (leer = Bezeichnung)", Short: "Ausweistext", Type: tText})
	case "key":
		fields = append(fields,
			crud.Field{Key: "unit", Label: "Einheit (z. B. m², VE, m³, Stück)", Short: "Einheit", Type: tText, Listable: true},
			crud.Field{Key: "source", Label: "Herkunft der Werte der Nutzer", Short: "Herkunft", Type: tSel, Required: true, Listable: true, Options: sourceOptions},
			crud.Field{Key: "source_code", Label: "Bemessungsart bzw. Sparte (z. B. HZF, HKV, WW, KW)", Short: "Bemessung/Sparte", Type: tText, Listable: true},
			crud.Field{Key: "decimals", Label: "Nachkommastellen", Short: "Stellen", Type: tNum})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Short: "Reihenf.", Type: tNum, Listable: true},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Customizing: true, Title: c.Title, Icon: "icon-tag", Table: "heatcost__" + c.Table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields:      fields,
		Access:      &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: checkWrite(c.Object),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			return m.checkCatalog(ctx, c.Table, rec, old)
		},
	}
}

func (m *Module) checkCatalog(ctx context.Context, table string, rec, old crud.Record) error {
	rec["code"] = trimUpper(rec["code"])
	if !codeRe.MatchString(crud.Str(rec["code"])) {
		return crud.Invalid("Schlüssel %q: 1–16 Großbuchstaben, Ziffern oder _", crud.Str(rec["code"]))
	}
	cc := crud.Str(rec["company_code"])
	switch table {
	case "cost_kind":
		rec["allocation"] = trimUpper(rec["allocation"])
		if crud.Str(rec["allocation"]) == "" {
			rec["allocation"] = allocSplit
		}
		if !validOption(allocationOptions, crud.Str(rec["allocation"])) {
			return crud.Invalid("Aufteilung %q: SPLIT, HEATING, HOTWATER, KEY oder DIRECT", crud.Str(rec["allocation"]))
		}
		rec["cost_group"], rec["key_code"] = nilIfEmpty(trimUpper(rec["cost_group"])), nilIfEmpty(trimUpper(rec["key_code"]))
		if g := crud.Str(rec["cost_group"]); g != "" {
			if ok, err := m.exists(ctx, "cost_group", cc, g); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Kostengruppe %s gibt es nicht", g)
			}
		}
		if k := crud.Str(rec["key_code"]); k != "" {
			if ok, err := m.exists(ctx, "key", cc, k); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Schlüssel %s gibt es nicht", k)
			}
		} else if crud.Str(rec["allocation"]) == allocKey {
			return crud.Invalid("Aufteilung nach eigenem Schlüssel: Schlüssel fehlt")
		}
		if isSet(rec["ww_percent"]) {
			if p := toFloat(rec["ww_percent"]); p < 0 || p > 100 {
				return crud.Invalid("Warmwasseranteil %v %%: 0 bis 100", p)
			}
		} else {
			rec["ww_percent"] = nil
		}
		if rec["apportionable"] == nil || crud.Str(rec["apportionable"]) == "" {
			rec["apportionable"] = true
		}
	case "key":
		rec["source"] = trimUpper(rec["source"])
		if crud.Str(rec["source"]) == "" {
			rec["source"] = srcManual
		}
		if !validOption(sourceOptions, crud.Str(rec["source"])) {
			return crud.Invalid("Herkunft %q: MANUAL, AREA, CONSUMPTION oder DEVICES", crud.Str(rec["source"]))
		}
		rec["source_code"] = nilIfEmpty(trimUpper(rec["source_code"]))
		if crud.Str(rec["source"]) != srcManual && rec["source_code"] == nil {
			return crud.Invalid("Herkunft %s: Bemessungsart bzw. Sparte fehlt", crud.Str(rec["source"]))
		}
		if !isSet(rec["decimals"]) {
			rec["decimals"] = 3
		}
	}
	return nil
}

func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	v, err := m.one(ctx, "SELECT 1 FROM heatcost__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	return v != "", err
}

// catalogDefaults: Vorschlagswerte je Katalog (im Buchungskreis änderbar). Die
// Kostenarten folgen § 7 und § 8 HeizkostenV und der üblichen Kostenaufstellung
// der Messdienste; Gerätemieten und Wasser können auch als Hausnebenkosten nach
// eigenem Schlüssel laufen.
var catalogDefaults = map[string][]map[string]any{
	"cost_group": {
		{"code": "BRENNSTOFF", "name": "Brennstoffkosten"},
		{"code": "HEIZBETRIEB", "name": "Weitere Heizungsbetriebskosten"},
		{"code": "HAUSNEBEN", "name": "Hausnebenkosten"},
		{"code": "DIREKT", "name": "Nutzerdirektkosten"},
	},
	"key": {
		{"code": "HZF", "name": "Heizfläche", "unit": "m²", "source": srcArea, "source_code": "HZF", "decimals": 3},
		{"code": "WFL", "name": "Wohnfläche", "unit": "m²", "source": srcArea, "source_code": "WFL", "decimals": 3},
		{"code": "VE", "name": "Verbrauchseinheiten Heizkostenverteiler", "unit": "VE", "source": srcConsumption, "source_code": "HKV", "decimals": 3},
		{"code": "WAERME", "name": "Wärmeverbrauch (Wärmezähler)", "unit": "kWh", "source": srcConsumption, "source_code": "WAERME", "decimals": 3},
		{"code": "WW", "name": "Warmwasser", "unit": "m³", "source": srcConsumption, "source_code": "WW", "decimals": 3},
		{"code": "KW", "name": "Kaltwasser", "unit": "m³", "source": srcConsumption, "source_code": "KW", "decimals": 3},
		{"code": "STCK_HKV", "name": "Heizkostenverteiler (Stück)", "unit": "Stück", "source": srcDevices, "source_code": "HKV", "decimals": 0},
		{"code": "STCK_WW", "name": "Warmwasserzähler (Stück)", "unit": "Stück", "source": srcDevices, "source_code": "WW", "decimals": 0},
		{"code": "STCK_KW", "name": "Kaltwasserzähler (Stück)", "unit": "Stück", "source": srcDevices, "source_code": "KW", "decimals": 0},
		{"code": "NE", "name": "Nutzeinheiten", "unit": "Stück", "source": srcManual, "decimals": 0},
	},
	"cost_kind": {
		{"code": "FERNWAERME", "name": "Fernwärme", "cost_group": "BRENNSTOFF", "allocation": allocSplit},
		{"code": "GAS", "name": "Erdgas", "cost_group": "BRENNSTOFF", "allocation": allocSplit},
		{"code": "OEL", "name": "Heizöl", "cost_group": "BRENNSTOFF", "allocation": allocSplit},
		{"code": "STROM", "name": "Betriebsstrom Pumpe und Brenner", "cost_group": "HEIZBETRIEB", "allocation": allocSplit},
		{"code": "WARTUNG", "name": "Wartung und Reinigung der Anlage", "cost_group": "HEIZBETRIEB", "allocation": allocSplit},
		{"code": "SCHORNSTEIN", "name": "Schornsteinfeger und Immissionsmessung", "cost_group": "HEIZBETRIEB", "allocation": allocSplit},
		{"code": "MESSDIENST", "name": "Heizkostenabrechnung (Messdienst)", "cost_group": "HEIZBETRIEB", "allocation": allocSplit},
		{"code": "FUNK", "name": "Miete Funkzentralen", "cost_group": "HEIZBETRIEB", "allocation": allocSplit},
		{"code": "MIETE_WMZ_WW", "name": "Miete Wärmezähler Warmwasser", "cost_group": "HEIZBETRIEB", "allocation": allocHotwater},
		{"code": "MIETE_WWZ", "name": "Miete Warmwasserzähler", "cost_group": "HEIZBETRIEB", "allocation": allocHotwater},
		{"code": "MIETE_HKV", "name": "Miete Heizkostenverteiler", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "STCK_HKV"},
		{"code": "MIETE_KWZ", "name": "Miete Kaltwasserzähler", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "STCK_KW"},
		{"code": "KW_ABR", "name": "Kaltwasserabrechnung", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "STCK_KW"},
		{"code": "TW_VERBRAUCH", "name": "Verbrauch Trinkwasser", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "KW"},
		{"code": "AW_VERBRAUCH", "name": "Verbrauch Abwasser", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "KW"},
		{"code": "TW_GRUND", "name": "Grundpreis Trinkwasser", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "HZF"},
		{"code": "AW_GRUND", "name": "Grundpreis Abwasser", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "HZF"},
		{"code": "WASSER_GUTSCHRIFT", "name": "Erstattung / Gutschrift Wasser und Abwasser", "cost_group": "HAUSNEBEN", "allocation": allocKey, "key_code": "KW"},
		{"code": "DIREKT", "name": "Nutzerdirektkosten", "cost_group": "DIREKT", "allocation": allocDirect},
	},
}

// catalogColumns: Zusatzspalten der Vorschlagswerte.
var catalogColumns = []string{"code", "name", "cost_group", "allocation", "key_code", "ww_percent", "text", "unit", "source", "source_code", "decimals"}

type setupKey struct{}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an. Ein Katalog,
// der schon Einträge hat, bekommt sie nur auf ausdrücklichen Wunsch (setupCompany).
// Reihenfolge wie catalogTables: Gruppen und Schlüssel vor den Kostenarten.
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	explicit := ctx.Value(setupKey{}) != nil
	n := 0
	for _, c := range []string{"cost_group", "key", "cost_kind"} {
		if !explicit {
			if has, err := m.one(ctx, "SELECT 1 FROM heatcost__"+c+" WHERE company_code = ? LIMIT 1", cc); err != nil {
				return 0, err
			} else if has != "" {
				continue
			}
		}
		for i, row := range catalogDefaults[c] {
			ok, err := m.exists(ctx, c, cc, crud.Str(row["code"]))
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
			if _, err := m.db.Exec(ctx, "INSERT INTO heatcost__"+c+" ("+strings.Join(cols, ", ")+") VALUES ("+
				strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...); err != nil {
				return 0, err
			}
			n++
		}
	}
	return n, nil
}

// setupCompanyAction: HeatCostSetup.setupCompany {company} (Konsole heatcost:setup-company).
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
	if err := requireWrite(ctx, "HeatCostKind", "create", cc); err != nil {
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
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der Heizkostenabrechnung angelegt", cc, n)}}, nil
}
