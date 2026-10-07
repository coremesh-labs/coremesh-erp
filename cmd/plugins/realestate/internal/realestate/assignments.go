package realestate

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Vertragsobjekt: Bestandteile (Zeitscheibe) -------------------------------------

// compositeItem: Ein Vertragsobjekt fasst Mieteinheiten, Stellplätze und Flächen
// zusammen. Ein Objekt gehört zu einem Zeitpunkt zu höchstens einem
// Vertragsobjekt; Zuordnungen enden mit „Beenden …“ (Zeitscheibe).
func (m *Module) compositeItem() *crud.Entity {
	return &crud.Entity{
		Object: "CompositeItem", Title: "Vertragsobjekte – Bestandteile", Icon: "icon-list", Table: "realestate__composite_item", Section: "Bestand",
		Keys: []string{"company_code", "composite_id", "object_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, composite_id, object_id, valid_from", Filters: []string{"company_code", "composite_id", "object_id"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"composite_id", "object_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "composite_id", Label: "Vertragsobjekt", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code", "kind": "=" + kindComposite}}},
			crud.Field{Key: "object_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code"}}},
		),
		Access: &crud.Access{Object: "RentObject", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "RentObject", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkCompositeItem,
	}
}

func (m *Module) checkCompositeItem(ctx context.Context, rec, _ crud.Record) error {
	cc, comp, obj := crud.Str(rec["company_code"]), crud.Str(rec["composite_id"]), crud.Str(rec["object_id"])
	kinds, err := m.kindsOf(ctx, cc, comp, obj)
	if err != nil {
		return err
	}
	switch {
	case kinds[comp] != kindComposite:
		return crud.Invalid("%s ist kein Vertragsobjekt im Buchungskreis %s", comp, cc)
	case kinds[obj] == "":
		return crud.Invalid("Objekt %s gibt es im Buchungskreis %s nicht", obj, cc)
	case kinds[obj] == kindComposite || kinds[obj] == kindPool:
		return crud.Invalid("%s ist ein %s – Bestandteile sind Mieteinheiten, Stellplätze und Flächen", obj, kindLabel(kinds[obj]))
	}
	// Höchstens ein Vertragsobjekt je Objekt und Zeitpunkt.
	res, err := m.db.Query(ctx, `SELECT composite_id, valid_from, valid_to FROM realestate__composite_item
		WHERE company_code = ? AND object_id = ? AND composite_id <> ? AND valid_from <= ? AND valid_to >= ?`,
		cc, obj, comp, rec["valid_to"], rec["valid_from"])
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		from, _ := crud.ParseDate(res.Rows[0][1])
		to, _ := crud.ParseDate(res.Rows[0][2])
		return crud.Invalid("%s gehört %s–%s bereits zum Vertragsobjekt %s", obj, from, to, crud.Str(res.Rows[0][0]))
	}
	return nil
}

// kindsOf: Objektart je ID (leer = gibt es nicht).
func (m *Module) kindsOf(ctx context.Context, cc string, ids ...string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		res, err := m.db.Query(ctx, "SELECT kind FROM realestate__rent_object WHERE company_code = ? AND object_id = ?", cc, id)
		if err != nil {
			return nil, err
		}
		if len(res.Rows) > 0 {
			out[id] = crud.Str(res.Rows[0][0])
		}
	}
	return out, nil
}

// --- Bemessungen (Zeitscheibe) ------------------------------------------------------

// measurement: Bemessungen für jedes Objekt (Wirtschaftseinheit, Gebäude,
// Mietobjekte), z. B. Wohnfläche oder Miteigentumsanteil, mit Zeitscheibe.
func (m *Module) measurement() *crud.Entity {
	return &crud.Entity{
		Object: "Measurement", Title: "Bemessungen", Icon: "icon-ruler", Table: "realestate__measurement", Section: "Bestand",
		Keys: []string{"company_code", "object_id", "measurement_type", "valid_from"}, TimeSlice: true,
		Order: "company_code, object_id, measurement_type, valid_from", Filters: []string{"company_code", "object_id", "object_level", "measurement_type"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"object_id", "object_level", "measurement_type", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "object_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true},
			crud.Field{Key: "object_level", Label: "Ebene", Type: tSel, ReadOnly: true, Listable: true, Options: levelOptions},
			crud.Field{Key: "measurement_type", Label: "Bemessungsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("MeasurementType")},
			crud.Field{Key: "value", Label: "Wert", Type: tNum, Required: true, Listable: true},
			crud.Field{Key: "unit", Label: "Maßeinheit (leer = Standard der Bemessungsart)", Type: tText, Listable: true, Lookup: catalogLookup("MeasureUnit")},
		),
		Access: &crud.Access{Object: "RentObject", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Measurement", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkMeasurement,
	}
}

var levelOptions = []metamodel.Option{
	{Value: "ENTITY", Label: "Wirtschaftseinheit"}, {Value: "BUILDING", Label: "Gebäude"},
	{Value: kindUnit, Label: "Mieteinheit"}, {Value: kindSpace, Label: "Fläche"},
	{Value: kindPool, Label: "Pool"}, {Value: kindComposite, Label: "Vertragsobjekt"},
}

// objectLevel: Ebene einer ID im Buchungskreis (leer = gibt es nicht).
func (m *Module) objectLevel(ctx context.Context, cc, id string) (string, error) {
	for _, q := range []struct{ level, sql string }{
		{"ENTITY", "SELECT 'ENTITY' FROM realestate__business_entity WHERE company_code = ? AND entity_id = ?"},
		{"BUILDING", "SELECT 'BUILDING' FROM realestate__building WHERE company_code = ? AND building_id = ?"},
		{"", "SELECT kind FROM realestate__rent_object WHERE company_code = ? AND object_id = ?"},
	} {
		res, err := m.db.Query(ctx, q.sql, cc, id)
		if err != nil {
			return "", err
		}
		if len(res.Rows) > 0 {
			return crud.Str(res.Rows[0][0]), nil
		}
	}
	return "", nil
}

func (m *Module) checkMeasurement(ctx context.Context, rec, old crud.Record) error {
	cc, obj, typ := crud.Str(rec["company_code"]), crud.Str(rec["object_id"]), crud.Str(rec["measurement_type"])
	level, err := m.objectLevel(ctx, cc, obj)
	if err != nil {
		return err
	}
	if level == "" {
		return crud.Invalid("Objekt %s gibt es im Buchungskreis %s nicht", obj, cc)
	}
	rec["object_level"] = level
	res, err := m.db.Query(ctx, "SELECT default_unit, is_area FROM realestate__measurement_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, typ, true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Bemessungsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", typ, cc)
	}
	if crud.Str(rec["unit"]) == "" {
		rec["unit"] = res.Rows[0][0]
	}
	if err := m.requireCatalog(ctx, "measure_unit", "Maßeinheit", cc, rec["unit"]); err != nil {
		return err
	}
	if toFloat(rec["value"]) < 0 {
		return crud.Invalid("Wert darf nicht negativ sein")
	}
	// Fläche aus einem Pool: Summe der Flächen ≤ Gesamtfläche des Pools.
	if level != kindSpace || !crud.AsBool(res.Rows[0][1]) {
		return nil
	}
	pool, err := m.db.Query(ctx, "SELECT pool_id FROM realestate__rent_object WHERE company_code = ? AND object_id = ?", cc, obj)
	if err != nil || len(pool.Rows) == 0 || pool.Rows[0][0] == nil {
		return err
	}
	date, _ := crud.ParseDate(rec["valid_from"])
	return m.checkPool(ctx, poolCheck{cc: cc, pool: crud.Str(pool.Rows[0][0]), date: date,
		values: map[string]float64{obj: toFloat(rec["value"])}, valueType: typ})
}

// poolCheck: Pool-Prüfung zu einem Stichtag mit noch nicht gespeicherten Werten.
type poolCheck struct {
	cc, pool, date string
	total          *float64           // geänderte Gesamtfläche
	areaType       *string            // geänderte Flächenart
	spaces         []string           // Flächen, die neu zum Pool kommen
	values         map[string]float64 // geänderte Bemessung je Fläche …
	valueType      string             // … in dieser Bemessungsart
}

// checkPool: Die Flächen eines Pools dürfen zum Stichtag zusammen nicht größer
// sein als seine Gesamtfläche (in der Flächenart des Pools).
func (m *Module) checkPool(ctx context.Context, c poolCheck) error {
	res, err := m.db.Query(ctx, "SELECT total_area, area_type FROM realestate__rent_object WHERE company_code = ? AND object_id = ? AND kind = ?", c.cc, c.pool, kindPool)
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	total, areaType := toFloat(res.Rows[0][0]), crud.Str(res.Rows[0][1])
	if c.total != nil {
		total = *c.total
	}
	if c.areaType != nil {
		areaType = *c.areaType
	}
	if areaType == "" || (c.valueType != "" && c.valueType != areaType) {
		return nil // ohne Flächenart keine Prüfung; andere Bemessungsart betrifft den Pool nicht
	}
	rows, err := m.db.Query(ctx, "SELECT object_id FROM realestate__rent_object WHERE company_code = ? AND kind = ? AND pool_id = ?", c.cc, kindSpace, c.pool)
	if err != nil {
		return err
	}
	spaces := slices.Clone(c.spaces)
	for _, r := range rows.Rows {
		if id := crud.Str(r[0]); !slices.Contains(spaces, id) {
			spaces = append(spaces, id)
		}
	}
	sum := 0.0
	for _, s := range spaces {
		if v, ok := c.values[s]; ok {
			sum += v
			continue
		}
		v, err := m.db.Query(ctx, `SELECT value FROM realestate__measurement WHERE company_code = ? AND object_id = ? AND measurement_type = ?
			AND valid_from <= ? AND valid_to >= ?`, c.cc, s, areaType, c.date, c.date)
		if err != nil {
			return err
		}
		if len(v.Rows) > 0 {
			sum += toFloat(v.Rows[0][0])
		}
	}
	if sum > total+1e-9 {
		return crud.Invalid("Die Flächen des Pools %s ergeben am %s %s %s – mehr als seine Gesamtfläche %s (%s)",
			c.pool, c.date, formatNum(sum), areaType, formatNum(total), strings.Join(spaces, ", "))
	}
	return nil
}

func formatNum(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}
