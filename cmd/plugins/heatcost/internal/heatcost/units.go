package heatcost

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ermittlung des Warmwasseranteils (§ 9 HeizkostenV).
const (
	wwMeter   = "METER"
	wwFixed   = "FIXED"
	wwFormula = "FORMULA"

	roundGroup = "PER_GROUP"
	roundTotal = "TOTAL"
	restLast   = "LAST"
	restNone   = "NONE"
	co2Deduct  = "DEDUCT"
	co2Show    = "SHOW"
	co2Costs   = "COSTS"
)

var wwMethodOptions = []metamodel.Option{
	{Value: wwMeter, Label: "nach Wärmezähler (Energie Warmwasser ÷ Gesamtenergie)"},
	{Value: wwFormula, Label: "nach Formel § 9 Abs. 2 (2,5 kWh/m³/K × Warmwassermenge × (Temperatur − 10 °C))", Short: "Formel § 9 Abs. 2"},
	{Value: wwFixed, Label: "fester Anteil in %"},
}

// seit 0.1.2: Rundung und CO2-Kosten
var wwRoundOptions = []metamodel.Option{
	{Value: roundGroup, Label: "je Kostengruppe runden", Short: "je Kostengruppe"},
	{Value: roundTotal, Label: "nur die Summe runden", Short: "Summe"},
}

var groupRestOptions = []metamodel.Option{
	{Value: restLast, Label: "Rundungsrest an die letzte Gruppe", Short: "letzte Gruppe"},
	{Value: restNone, Label: "jede Gruppe gerundet, ohne Ausgleich", Short: "ohne Ausgleich"},
}

var co2ModeOptions = []metamodel.Option{
	{Value: co2Deduct, Label: "Vermieteranteil in der Heizkostenabrechnung abziehen", Short: "abziehen"},
	{Value: co2Costs, Label: "vor der Verteilung von den Brennstoffkosten abziehen (Zeile in der Kostenaufstellung)", Short: "von den Kosten"},
	{Value: co2Show, Label: "nur ausweisen – der Eigentümer verrechnet ihn in der Nebenkostenabrechnung (z. B. WEG)", Short: "nur ausweisen"},
}

var unitStatusOptions = []metamodel.Option{{Value: "ACTIVE", Label: "aktiv"}, {Value: "INACTIVE", Label: "inaktiv"}}

// blockFields: Einstellungen der Blöcke Heizung und Warmwasser – an der Einheit
// und (kopiert, änderbar) in der Abrechnung.
func blockFields() []crud.Field {
	g := "Heizung"
	w := "Warmwasser"
	return []crud.Field{
		{Key: "heat_base_percent", Label: "Grundkosten Heizung in % (Rest nach Verbrauch)", Short: "Grund Heizung %", Type: tNum, Group: g},
		{Key: "heat_area_key", Label: "Schlüssel Grundkosten Heizung", Short: "Schlüssel Grund", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: g},
		{Key: "heat_usage_key", Label: "Schlüssel Verbrauchskosten Heizung", Short: "Schlüssel Verbrauch", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: g},
		{Key: "heat_groups", Label: "Heizung zuerst auf Nutzergruppen verteilen (Gruppen durch Komma; leer = ganze Liegenschaft)", Short: "Gruppen Heizung", Type: tText, Group: g},
		{Key: "heat_group_key", Label: "Schlüssel für die Verteilung auf die Gruppen", Short: "Gruppenschlüssel", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: g},
		{Key: "ww_method", Label: "Warmwasseranteil", Short: "WW-Anteil", Type: tSel, Options: wwMethodOptions, Group: w},
		{Key: "ww_fixed_percent", Label: "Fester Warmwasseranteil in %", Short: "WW fest %", Type: tNum, Group: w},
		{Key: "ww_base_percent", Label: "Grundkosten Warmwasser in % (Rest nach Verbrauch)", Short: "Grund WW %", Type: tNum, Group: w},
		{Key: "ww_area_key", Label: "Schlüssel Grundkosten Warmwasser", Short: "Schlüssel Grund WW", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: w},
		{Key: "ww_usage_key", Label: "Schlüssel Verbrauchskosten Warmwasser", Short: "Schlüssel Verbrauch WW", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: w},
		{Key: "ww_groups", Label: "Warmwasser zuerst auf Nutzergruppen verteilen (Gruppen durch Komma; leer = ganze Liegenschaft)", Short: "Gruppen WW", Type: tText, Group: w},
		{Key: "ww_group_key", Label: "Schlüssel für die Verteilung auf die Gruppen (Warmwasser)", Short: "Gruppenschlüssel WW", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: w},
		{Key: "ww_temp", Label: "Mittlere Warmwassertemperatur in °C (Formel § 9 Abs. 2)", Short: "WW-Temperatur", Type: tNum, Group: w},
		{Key: "ww_decimals", Label: "Warmwasseranteil auf Nachkommastellen (in %) runden (leer = ungerundet)", Short: "WW-Stellen", Type: tNum, Group: "Rundung"},
		{Key: "ww_round", Label: "Warmwasseranteil runden", Short: "Rundung WW", Type: tSel, Options: wwRoundOptions, Group: "Rundung"},
		{Key: "group_rest", Label: "Rundungsrest bei der Verteilung auf Nutzergruppen", Short: "Rundungsrest", Type: tSel, Options: groupRestOptions, Group: "Rundung"},
		{Key: "heat_change", Label: "Grundkosten Heizung bei Nutzerwechsel (§ 9b Abs. 2)", Short: "Nutzerwechsel Heizung", Type: tSel, Options: heatChangeOptions, Group: "Nutzerwechsel"},
		{Key: "degree_table", Label: "Gradtagszahlen (Tabelle)", Short: "Gradtagszahlen", Type: tText, Lookup: catalogLookup("HeatCostDegreeTable"), Group: "Nutzerwechsel"},
		{Key: "co2_scale", Label: "CO2-Stufenmodell (z. B. Denkmalschutz: halbierter Vermieteranteil)", Short: "Stufenmodell", Type: tText, Lookup: catalogLookup("HeatCostCo2Scale"), Group: "CO2-Kosten"},
		{Key: "co2_mode", Label: "Vermieteranteil der CO2-Kosten", Short: "CO2 Vermieter", Type: tSel, Options: co2ModeOptions, Group: "CO2-Kosten"},
		{Key: "co2_area_key", Label: "Fläche für kg CO2 je m² (Schlüssel; leer = Schlüssel Grundkosten Heizung)", Short: "Fläche CO2", Type: tText, Lookup: catalogLookup("HeatCostKey"), Group: "CO2-Kosten"},
		{Key: "co2_ww_decimals", Label: "Warmwasseranteil für die CO2-Aufteilung auf Nachkommastellen (in %) runden (leer = ungerundet)", Short: "CO2 WW-Stellen", Type: tNum, Group: "CO2-Kosten"},
	}
}

var blockKeys = []string{"heat_base_percent", "heat_area_key", "heat_usage_key", "heat_groups", "heat_group_key",
	"ww_method", "ww_fixed_percent", "ww_base_percent", "ww_area_key", "ww_usage_key", "ww_groups", "ww_group_key",
	"ww_temp", "ww_round", "group_rest", "co2_scale", "co2_mode", "co2_area_key", "co2_ww_decimals", "heat_change", "degree_table", "ww_decimals"}

// profileKeys: Einstellungen, die eine Vorgabe (Katalog HeatCostProfile) liefert.
var profileKeys = []string{"heat_base_percent", "heat_area_key", "heat_usage_key", "ww_method", "ww_fixed_percent",
	"ww_base_percent", "ww_area_key", "ww_usage_key", "ww_temp", "ww_round", "group_rest", "co2_scale", "co2_mode", "co2_area_key", "co2_ww_decimals", "heat_change", "degree_table", "ww_decimals", "sender_roles", "tenant_roles"}

// fromProfile: leere Einstellungen aus der Vorgabe füllen (leer = erste aktive Vorgabe).
func (m *Module) fromProfile(ctx context.Context, cc string, rec crud.Record) error {
	code := trimUpper(rec["profile"])
	if code == "" {
		v, err := m.one(ctx, "SELECT code FROM heatcost__profile WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code LIMIT 1", cc, true)
		if err != nil {
			return err
		}
		code = v
	}
	if code == "" {
		rec["profile"] = nil
		return nil
	}
	res, err := m.db.Query(ctx, "SELECT "+strings.Join(profileKeys, ", ")+" FROM heatcost__profile WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Vorgabe %q gibt es nicht (oder inaktiv) – Einstellungen → Vorgaben für Abrechnungseinheiten", code)
	}
	rec["profile"] = code
	for i, k := range profileKeys {
		if !isSet(rec[k]) {
			rec[k] = res.Rows[0][i]
		}
	}
	return nil
}

// checkBlocks: Einstellungen der Blöcke prüfen (unit = Einheit der Gruppen; profile =
// Vorgabe mit Gruppen- und Einheitsfeldern weglassen).
func (m *Module) checkBlocks(ctx context.Context, cc, unit string, rec crud.Record) error {
	return m.checkSettings(ctx, cc, unit, rec, true)
}

func (m *Module) checkSettings(ctx context.Context, cc, unit string, rec crud.Record, withGroups bool) error {
	names := map[string]string{"heat_base_percent": "Grundkosten Heizung", "ww_base_percent": "Grundkosten Warmwasser",
		"heat_area_key": "Schlüssel Grundkosten Heizung", "heat_usage_key": "Schlüssel Verbrauchskosten Heizung",
		"ww_area_key": "Schlüssel Grundkosten Warmwasser", "ww_usage_key": "Schlüssel Verbrauchskosten Warmwasser"}
	for _, k := range []string{"heat_base_percent", "ww_base_percent"} {
		if !isSet(rec[k]) {
			return crud.Invalid("%s in %% fehlt – eintragen oder eine Vorgabe wählen (Einstellungen → Vorgaben für Abrechnungseinheiten)", names[k])
		}
		if p := toFloat(rec[k]); p < 0 || p > 100 {
			return crud.Invalid("%s: %v %% – 0 bis 100", names[k], p)
		}
	}
	keys := []string{"heat_area_key", "heat_usage_key", "ww_area_key", "ww_usage_key", "co2_area_key"}
	if withGroups {
		keys = append(keys, "heat_group_key", "ww_group_key")
	}
	for _, k := range keys {
		v := trimUpper(rec[k])
		rec[k] = nilIfEmpty(v)
		if v == "" {
			if names[k] != "" {
				return crud.Invalid("%s fehlt – eintragen oder eine Vorgabe wählen", names[k])
			}
			continue
		}
		if ok, err := m.exists(ctx, "key", cc, v); err != nil {
			return err
		} else if !ok {
			return crud.Invalid("Schlüssel %s gibt es nicht – Einstellungen → Verteilerschlüssel", v)
		}
	}
	if err := checkWWMethod(rec); err != nil {
		return err
	}
	if err := m.checkRounding(ctx, cc, rec); err != nil {
		return err
	}
	if !withGroups {
		return nil
	}
	for _, b := range []string{"heat", "ww"} {
		groups := splitCodes(crud.Str(rec[b+"_groups"]))
		rec[b+"_groups"] = nilIfEmpty(strings.Join(groups, ","))
		if len(groups) == 0 {
			continue
		}
		if rec[b+"_group_key"] == nil {
			return crud.Invalid("Verteilung auf Nutzergruppen: Schlüssel für die Gruppen fehlt")
		}
		for _, g := range groups {
			if v, err := m.one(ctx, "SELECT 1 FROM heatcost__user_group WHERE company_code = ? AND unit_id = ? AND code = ?", cc, unit, g); err != nil {
				return err
			} else if v == "" {
				return crud.Invalid("Nutzergruppe %s gibt es in der Abrechnungseinheit %s nicht", g, unit)
			}
		}
	}
	return nil
}

// checkRounding: Rundung und CO2-Einstellungen (seit 0.1.2).
func (m *Module) checkRounding(ctx context.Context, cc string, rec crud.Record) error {
	for _, o := range []struct {
		key, name string
		opts      []metamodel.Option
		required  bool
	}{{"ww_round", "Warmwasseranteil runden", wwRoundOptions, true}, {"group_rest", "Rundungsrest bei Nutzergruppen", groupRestOptions, true},
		{"co2_mode", "Vermieteranteil der CO2-Kosten", co2ModeOptions, false}, {"heat_change", "Grundkosten Heizung bei Nutzerwechsel", heatChangeOptions, true}} {
		v := trimUpper(rec[o.key])
		rec[o.key] = nilIfEmpty(v)
		if v == "" {
			if o.required {
				return crud.Invalid("%s fehlt – eintragen oder eine Vorgabe wählen", o.name)
			}
			continue
		}
		if !validOption(o.opts, v) {
			return crud.Invalid("%s: %q ist keine der Möglichkeiten", o.name, v)
		}
	}
	sc := trimUpper(rec["co2_scale"])
	rec["co2_scale"] = nilIfEmpty(sc)
	if sc != "" {
		if ok, err := m.exists(ctx, "co2_scale", cc, sc); err != nil {
			return err
		} else if !ok {
			return crud.Invalid("CO2-Stufenmodell %s gibt es nicht – Einstellungen → CO2-Stufenmodelle", sc)
		}
	}
	dt := trimUpper(rec["degree_table"])
	rec["degree_table"] = nilIfEmpty(dt)
	if dt != "" {
		if ok, err := m.exists(ctx, "degree_table", cc, dt); err != nil {
			return err
		} else if !ok {
			return crud.Invalid("Gradtagszahlen %s gibt es nicht – Einstellungen → Gradtagszahlen", dt)
		}
	} else if rec["heat_change"] == changeDegree {
		return crud.Invalid("Nutzerwechsel nach Gradtagszahlen: Tabelle der Gradtagszahlen fehlt – eintragen oder eine Vorgabe wählen")
	}
	if isSet(rec["ww_decimals"]) {
		if d := toFloat(rec["ww_decimals"]); d < 0 || d > 6 || d != float64(int(d)) {
			return crud.Invalid("Nachkommastellen Warmwasseranteil: 0 bis 6")
		}
	} else {
		rec["ww_decimals"] = nil
	}
	if isSet(rec["co2_ww_decimals"]) {
		if d := toFloat(rec["co2_ww_decimals"]); d < 0 || d > 6 || d != float64(int(d)) {
			return crud.Invalid("Nachkommastellen Warmwasseranteil für CO2: 0 bis 6")
		}
	} else {
		rec["co2_ww_decimals"] = nil
	}
	return nil
}

func checkWWMethod(rec crud.Record) error {
	rec["ww_method"] = trimUpper(rec["ww_method"])
	if !validOption(wwMethodOptions, crud.Str(rec["ww_method"])) {
		return crud.Invalid("Warmwasseranteil %q: METER (nach Wärmezähler), FORMULA (Formel § 9 Abs. 2) oder FIXED (fester Anteil) – eintragen oder eine Vorgabe wählen", crud.Str(rec["ww_method"]))
	}
	if isSet(rec["ww_temp"]) {
		if t := toFloat(rec["ww_temp"]); t <= 10 || t > 100 {
			return crud.Invalid("Warmwassertemperatur %v °C: über 10 bis 100", t)
		}
	} else {
		rec["ww_temp"] = nil
		if rec["ww_method"] == wwFormula {
			return crud.Invalid("Formel § 9 Abs. 2: mittlere Warmwassertemperatur fehlt – eintragen oder eine Vorgabe wählen")
		}
	}
	if rec["ww_method"] == wwFixed {
		if p := toFloat(rec["ww_fixed_percent"]); !isSet(rec["ww_fixed_percent"]) || p < 0 || p > 100 {
			return crud.Invalid("Fester Warmwasseranteil: 0 bis 100 %%")
		}
	} else if !isSet(rec["ww_fixed_percent"]) {
		rec["ww_fixed_percent"] = nil
	}
	return nil
}

func (m *Module) unit() *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "unit_id", Label: "Abrechnungseinheit", Short: "Einheit", Type: tText, Listable: true, ReadOnly: true},
		{Key: "designation", Label: "Bezeichnung (z. B. Liegenschaft Friedrichshafner Str. 97–111)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true, Wide: true},
		{Key: "entity_id", Label: "Wirtschaftseinheit", Short: "WE", Type: tText, Listable: true, Lookup: entityLookup},
		{Key: "building_id", Label: "Gebäude", Type: tText, Listable: true, Lookup: buildingLookup},
		{Key: "partner_id", Label: "Messdienst bzw. Abrechnungsdienst", Short: "Messdienst", Type: tText, Listable: true, Lookup: partnerLookup},
		{Key: "external_no", Label: "Liegenschaftsnummer beim Messdienst", Short: "Liegenschaft", Type: tText, Listable: true},
		{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: unitStatusOptions},
		{Key: "profile", Label: "Vorgabe (füllt leere Einstellungen zu Heizung und Warmwasser; leer = erste Vorgabe)", Short: "Vorgabe", Type: tText, Listable: true, Lookup: catalogLookup("HeatCostProfile")},
		{Key: "note", Label: "Bemerkung", Type: tArea},
	}
	fields = append(fields, blockFields()...)
	fields = append(fields, outputFields()...)
	fields = append(fields,
		crud.Field{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		crud.Field{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser})
	um := map[string]string{"company_code": "company_code", "unit_id": "unit_id"}
	return &crud.Entity{
		Object: "HeatCostUnit", Title: "Abrechnungseinheiten", Icon: "icon-building", Table: "heatcost__unit", Section: "Heizkosten",
		Keys: []string{"company_code", "unit_id"}, Order: "company_code, unit_id", TitleField: "designation",
		StatusField: "status", StatusActive: "ACTIVE", StatusInactive: "INACTIVE",
		Filters: []string{"company_code", "entity_id", "building_id", "status"}, Search: []string{"unit_id", "designation", "external_no"},
		Fields: fields,
		Sections: []metamodel.SectionDefinition{
			{Key: "gruppen", Title: "Nutzergruppen", Relation: &metamodel.Relation{Object: "HeatCostUserGroup", ForeignKey: "unit_id", Match: um,
				Columns: []string{"code", "designation", "sort_order"}}},
			{Key: "nutzer", Title: "Nutzer", Relation: &metamodel.Relation{Object: "HeatCostUser", ForeignKey: "unit_id", Match: um,
				Columns: []string{"user_no", "designation", "object_id", "location", "groups", "valid_from", "valid_to"}}},
			{Key: "abrechnungen", Title: "Abrechnungen", Relation: &metamodel.Relation{Object: "HeatCostRun", ForeignKey: "unit_id", Match: um,
				Columns: []string{"run_id", "title", "period_from", "period_to", "status", "total_cost"}}},
			{Key: "dokumente", Title: "Dokumente", Documents: true},
		},
		Access:      access(),
		CheckRecord: checkWrite("HeatCostUnit"),
		Prepare:     m.prepareUnit,
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			for _, k := range []string{"entity_id", "building_id", "partner_id", "external_no", "template"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			for _, k := range []string{"doc_type", "nk_definition", "nk_heat_account", "nk_co2_account", "sender_roles", "tenant_roles"} {
				rec[k] = nilIfEmpty(trimUpper(rec[k]))
			}
			if err := m.fromProfile(ctx, cc, rec); err != nil {
				return err
			}
			if err := m.checkBlocks(ctx, cc, crud.Str(rec["unit_id"]), rec); err != nil {
				return err
			}
			m.stamp(ctx, rec)
			return nil
		},
	}
}

// prepareUnit: Kataloge und Nummer (AE0001 …).
func (m *Module) prepareUnit(ctx context.Context, rec crud.Record) error {
	cc := strings.TrimSpace(crud.Str(rec["company_code"]))
	if cc == "" {
		return nil
	}
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		_, err := m.setupCatalogs(ctx, cc)
		return err
	}); err != nil {
		return err
	}
	last, err := m.one(ctx, "SELECT MAX(unit_id) FROM heatcost__unit WHERE company_code = ? AND unit_id LIKE 'AE%'", cc)
	if err != nil {
		return err
	}
	n := 0
	if last != "" {
		fmt.Sscanf(strings.TrimPrefix(last, "AE"), "%d", &n)
	}
	rec["unit_id"], rec["status"] = fmt.Sprintf("AE%04d", n+1), "ACTIVE"
	return nil
}

func unitHead() []crud.Field {
	return []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "unit_id", Label: "Abrechnungseinheit", Short: "Einheit", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: unitLookup},
	}
}

func (m *Module) userGroup() *crud.Entity {
	return &crud.Entity{
		Object: "HeatCostUserGroup", Title: "Nutzergruppen", Icon: "icon-users", Table: "heatcost__user_group", Section: "Heizkosten",
		Keys: []string{"company_code", "unit_id", "code"}, Order: "company_code, unit_id, sort_order, code", TitleField: "designation",
		Filters: []string{"company_code", "unit_id"},
		Fields: append(unitHead(),
			crud.Field{Key: "code", Label: "Gruppe (Kürzel, z. B. FH97_99)", Short: "Gruppe", Type: tText, Required: true, Listable: true, Immutable: true},
			crud.Field{Key: "designation", Label: "Bezeichnung (z. B. FH 97–99)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "sort_order", Label: "Reihenfolge", Short: "Reihenf.", Type: tNum, Listable: true},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText}),
		Access:      access(),
		CheckRecord: checkWrite("HeatCostUnit"),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["code"] = trimUpper(rec["code"])
			if !codeRe.MatchString(crud.Str(rec["code"])) {
				return crud.Invalid("Gruppe %q: 1–16 Großbuchstaben, Ziffern oder _", crud.Str(rec["code"]))
			}
			return nil
		},
	}
}

func (m *Module) user() *crud.Entity {
	return &crud.Entity{
		Object: "HeatCostUser", Title: "Nutzer", Icon: "icon-user", Table: "heatcost__user", Section: "Heizkosten",
		Keys: []string{"company_code", "unit_id", "user_no"}, Order: "company_code, unit_id, user_no", TitleField: "designation",
		Filters: []string{"company_code", "unit_id", "object_id"}, Search: []string{"user_no", "designation", "location"},
		Fields: append(unitHead(),
			crud.Field{Key: "user_no", Label: "Nutzernummer (z. B. Whg-Nr. des Messdienstes)", Short: "Nutzer", Type: tText, Required: true, Listable: true, Immutable: true},
			crud.Field{Key: "object_id", Label: "Mietobjekt (leer = fremde Einheit, Werte nur von Hand)", Short: "Mietobjekt", Type: tText, Listable: true, Lookup: rentObjectLookup},
			crud.Field{Key: "designation", Label: "Bezeichnung (leer = Mietobjekt)", Short: "Bezeichnung", Type: tText, Listable: true},
			crud.Field{Key: "location", Label: "Lage (z. B. 99 DG rechts)", Short: "Lage", Type: tText, Listable: true},
			crud.Field{Key: "groups", Label: "Nutzergruppen (durch Komma, z. B. FH97_99,W97_99)", Short: "Gruppen", Type: tText, Listable: true},
			crud.Field{Key: "valid_from", Label: "Nutzer ab", Type: tDate, Listable: true},
			crud.Field{Key: "valid_to", Label: "Nutzer bis", Type: tDate, Listable: true},
			crud.Field{Key: "no_interim", Label: "Keine Zwischenablesung beim Nutzerwechsel (§ 9b Abs. 3: Verbrauch des Mietobjekts nach Gradtagszahlen bzw. Kalendertagen aufteilen)", Short: "ohne Zwischenablesung", Type: tBool, Listable: true},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText}),
		Access:      access(),
		CheckRecord: checkWrite("HeatCostUnit"),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc, unit := crud.Str(rec["company_code"]), crud.Str(rec["unit_id"])
			rec["user_no"] = strings.TrimSpace(crud.Str(rec["user_no"]))
			rec["object_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["object_id"])))
			groups := splitCodes(crud.Str(rec["groups"]))
			for _, g := range groups {
				if v, err := m.one(ctx, "SELECT 1 FROM heatcost__user_group WHERE company_code = ? AND unit_id = ? AND code = ?", cc, unit, g); err != nil {
					return err
				} else if v == "" {
					return crud.Invalid("Nutzergruppe %s gibt es in der Abrechnungseinheit %s nicht", g, unit)
				}
			}
			rec["groups"] = nilIfEmpty(strings.Join(groups, ","))
			rec["no_interim"] = crud.AsBool(rec["no_interim"])
			for k, d := range map[string]string{"valid_from": "1900-01-01", "valid_to": "9999-12-31"} {
				if strings.TrimSpace(crud.Str(rec[k])) == "" {
					rec[k] = d
				} else if v, err := crud.ParseDate(rec[k]); err != nil {
					return err
				} else {
					rec[k] = v
				}
			}
			if strings.TrimSpace(crud.Str(rec["designation"])) == "" {
				name := ""
				if o := crud.Str(rec["object_id"]); o != "" {
					name = m.rentObjectName(ctx, cc, o)
				}
				if name == "" {
					name = "Nutzer " + crud.Str(rec["user_no"])
				}
				rec["designation"] = name
			}
			return nil
		},
	}
}

// rentObjectName: Bezeichnung des Mietobjekts (Modul realestate; leer, wenn nicht lesbar).
func (m *Module) rentObjectName(ctx context.Context, cc, id string) string {
	resp, err := m.services.Call(ctx, "RentObject", "get", map[string]any{"id": cc + "|" + id})
	if err != nil {
		return ""
	}
	var o map[string]any
	if sdk.Decode(resp.Payload, &o) != nil {
		return ""
	}
	return strings.TrimSpace(crud.Str(o["designation"]))
}
