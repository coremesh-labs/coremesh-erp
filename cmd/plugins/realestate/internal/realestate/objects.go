package realestate

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Sprechende IDs:
//
//	Wirtschaftseinheit  frei, z. B. LpzBrn
//	Gebäude             <Wirtschaftseinheit><Nummer>, z. B. LpzBrn1
//	Mietobjekt          <Gebäude><Kürzel der Nutzungsart><Nummer, 3-stellig>, z. B. LpzBrn1WG001
//
// Ohne Eingabe vergibt das Modul die nächste freie ID; eine eingegebene ID muss
// mit der ID der übergeordneten Ebene beginnen. IDs sind systemweit eindeutig
// (über alle Ebenen und Buchungskreise, ohne Groß-/Kleinschreibung) – so
// verweisen Bemessungen, Hauptbuch und Verträge eindeutig auf ein Objekt.

var idRe = regexp.MustCompile(`^[A-Za-z0-9]{2,30}$`)

// idTaken: Ist die ID irgendwo vergeben?
func (m *Module) idTaken(ctx context.Context, id string) (bool, error) {
	for _, q := range []string{
		"SELECT 1 FROM realestate__business_entity WHERE LOWER(entity_id) = LOWER(?)",
		"SELECT 1 FROM realestate__building WHERE LOWER(building_id) = LOWER(?)",
		"SELECT 1 FROM realestate__rent_object WHERE LOWER(object_id) = LOWER(?)",
	} {
		res, err := m.db.Query(ctx, q, id)
		if err != nil {
			return false, err
		}
		if len(res.Rows) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// checkNewID: Format, Präfix der übergeordneten Ebene (ohne Groß-/Klein-
// schreibung; übernommen wird die Schreibweise der übergeordneten ID),
// Eindeutigkeit. Liefert die ID in der gespeicherten Schreibweise.
func (m *Module) checkNewID(ctx context.Context, id, parent, what string) (string, error) {
	if !idRe.MatchString(id) {
		return "", crud.Invalid("%s-ID %q: 2–30 Buchstaben und Ziffern", what, id)
	}
	if parent != "" {
		if len(id) <= len(parent) || !strings.EqualFold(id[:len(parent)], parent) {
			return "", crud.Invalid("%s-ID %q muss mit %s beginnen", what, id, parent)
		}
		id = parent + id[len(parent):]
	}
	taken, err := m.idTaken(ctx, id)
	if err != nil {
		return "", err
	}
	if taken {
		return "", fmt.Errorf("%w: ID %s ist bereits vergeben", sdk.ErrAlreadyExists, id)
	}
	return id, nil
}

// nextBuildingID: <Wirtschaftseinheit><n> mit dem kleinsten freien n.
func (m *Module) nextBuildingID(ctx context.Context, entity string) (string, error) {
	for n := 1; n < 1000; n++ {
		id := entity + strconv.Itoa(n)
		taken, err := m.idTaken(ctx, id)
		if err != nil {
			return "", err
		}
		if !taken {
			return id, nil
		}
	}
	return "", crud.Invalid("keine freie Gebäude-ID für %s", entity)
}

// nextObjectID: <Gebäude><Kürzel><nnn> – eins höher als die höchste vergebene Nummer.
func (m *Module) nextObjectID(ctx context.Context, building, prefix string) (string, error) {
	base := building + prefix
	res, err := m.db.Query(ctx, "SELECT object_id FROM realestate__rent_object WHERE LOWER(object_id) LIKE LOWER(?)", base+"%")
	if err != nil {
		return "", err
	}
	max := 0
	for _, r := range res.Rows {
		if n, err := strconv.Atoi(crud.Str(r[0])[len(base):]); err == nil && n > max {
			max = n
		}
	}
	for n := max + 1; n < 100000; n++ {
		id := fmt.Sprintf("%s%03d", base, n)
		taken, err := m.idTaken(ctx, id)
		if err != nil {
			return "", err
		}
		if !taken {
			return id, nil
		}
	}
	return "", crud.Invalid("keine freie Objekt-ID für %s", base)
}

// --- Wirtschaftseinheit -----------------------------------------------------------

func (m *Module) businessEntity() *crud.Entity {
	return &crud.Entity{
		Object: "BusinessEntity", Title: "Wirtschaftseinheiten", Icon: "icon-building", Table: "realestate__business_entity", Section: "Bestand",
		Keys: []string{"company_code", "entity_id"}, Order: "company_code, entity_id", TitleField: "designation",
		Filters: []string{"company_code", "entity_type", "status"}, Search: []string{"entity_id", "designation", "city"},
		Events: true, CompanyCodeField: "company_code",
		Fields: append([]crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "entity_id", Label: "Wirtschaftseinheit (ID, z. B. LpzBrn)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "entity_type", Label: "Art", Type: tText, Listable: true, Lookup: catalogLookup("EntityType")},
			{Key: "street", Label: "Straße", Type: tText, Group: "Adresse"},
			{Key: "zip", Label: "PLZ", Type: tText, Group: "Adresse"},
			{Key: "city", Label: "Ort", Type: tText, Listable: true, Group: "Adresse"},
		}, validityFields()...),
		Sections: []metamodel.SectionDefinition{
			{Key: "gebaeude", Title: "Gebäude", Relation: &metamodel.Relation{Object: "Building", ForeignKey: "entity_id", Match: match("entity_id", "entity_id"),
				Columns: []string{"building_id", "designation", "street", "city", "status"}}},
			{Key: "bemessungen", Title: "Bemessungen", Collapsed: true, Relation: &metamodel.Relation{Object: "Measurement", ForeignKey: "object_id", Match: match("object_id", "entity_id"),
				Columns: []string{"measurement_type", "value", "unit", "valid_from", "valid_to"}}},
		},
		Access: access("entity_id"),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "BusinessEntity", action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if old == nil {
				// Erste Wirtschaftseinheit im Buchungskreis: Kataloge mit Vorschlagswerten.
				if _, err := m.setupCatalogs(ctx, cc); err != nil {
					return err
				}
				id, err := m.checkNewID(ctx, strings.TrimSpace(crud.Str(rec["entity_id"])), "", "Wirtschaftseinheit")
				if err != nil {
					return err
				}
				rec["entity_id"] = id
			}
			if err := m.requireCatalog(ctx, "entity_type", "Art", cc, rec["entity_type"]); err != nil {
				return err
			}
			return m.checkValidity(ctx, rec)
		},
	}
}

// --- Gebäude ----------------------------------------------------------------------

func (m *Module) building() *crud.Entity {
	return &crud.Entity{
		Object: "Building", Title: "Gebäude", Icon: "icon-home", Table: "realestate__building", Section: "Bestand",
		Keys: []string{"company_code", "building_id"}, Order: "company_code, building_id", TitleField: "designation",
		Filters: []string{"company_code", "entity_id", "building_type", "status"}, Search: []string{"building_id", "designation", "street", "city"},
		Events: true, CompanyCodeField: "company_code",
		Fields: append([]crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "entity_id", Label: "Wirtschaftseinheit", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessEntity", ValueField: "entity_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "building_id", Label: "Gebäude (ID; leer = nächste freie, z. B. LpzBrn1)", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "building_type", Label: "Gebäudeart", Type: tText, Listable: true, Lookup: catalogLookup("BuildingType")},
			{Key: "street", Label: "Straße", Type: tText, Listable: true, Group: "Adresse"},
			{Key: "zip", Label: "PLZ", Type: tText, Group: "Adresse"},
			{Key: "city", Label: "Ort", Type: tText, Listable: true, Group: "Adresse"},
			{Key: "construction_year", Label: "Baujahr", Type: tNum},
		}, validityFields()...),
		Sections: []metamodel.SectionDefinition{
			{Key: "objekte", Title: "Mietobjekte", Relation: &metamodel.Relation{Object: "RentObject", ForeignKey: "building_id", Match: match("building_id", "building_id"),
				Columns: []string{"object_id", "kind", "designation", "usage_type", "floor", "status"}}},
			{Key: "bemessungen", Title: "Bemessungen", Collapsed: true, Relation: &metamodel.Relation{Object: "Measurement", ForeignKey: "object_id", Match: match("object_id", "building_id"),
				Columns: []string{"measurement_type", "value", "unit", "valid_from", "valid_to"}}},
		},
		Access: access("entity_id", "building_id"),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Building", action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if old == nil {
				entity := strings.TrimSpace(crud.Str(rec["entity_id"]))
				res, err := m.db.Query(ctx, "SELECT street, zip, city FROM realestate__business_entity WHERE company_code = ? AND entity_id = ?", cc, entity)
				if err != nil {
					return err
				}
				if len(res.Rows) == 0 {
					return crud.Invalid("Wirtschaftseinheit %s gibt es im Buchungskreis %s nicht", entity, cc)
				}
				// Adresse der Wirtschaftseinheit als Vorschlag.
				for i, k := range []string{"street", "zip", "city"} {
					if crud.Str(rec[k]) == "" {
						rec[k] = res.Rows[0][i]
					}
				}
				id := strings.TrimSpace(crud.Str(rec["building_id"]))
				if id == "" {
					if id, err = m.nextBuildingID(ctx, entity); err != nil {
						return err
					}
				} else if id, err = m.checkNewID(ctx, id, entity, "Gebäude"); err != nil {
					return err
				}
				rec["entity_id"], rec["building_id"] = entity, id
			}
			if y := toInt(rec["construction_year"]); rec["construction_year"] != nil && (y < 1000 || y > 2999) {
				return crud.Invalid("Baujahr %d ungültig", y)
			}
			if err := m.requireCatalog(ctx, "building_type", "Gebäudeart", cc, rec["building_type"]); err != nil {
				return err
			}
			return m.checkValidity(ctx, rec)
		},
	}
}

// --- Mietobjekte: gemeinsamer Stamm und Sichten je Art ------------------------------

// rentObjectFields: Felder aller Arten; kind bestimmt, welche eine Sicht zeigt.
func rentObjectFields(kind string) []crud.Field {
	fs := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "building_id", Label: "Gebäude", Type: tText, Required: true, Listable: true, Immutable: true,
			Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"},
				Filters: map[string]string{"company_code": "company_code"}}},
		{Key: "object_id", Label: "Objekt (ID; leer = nächste freie, z. B. LpzBrn1WG001)", Type: tText, Listable: true, Immutable: true},
		{Key: "kind", Label: "Objektart", Type: tSel, Options: kindOptions, ReadOnly: kind != "", Listable: kind == ""},
		{Key: "entity_id", Label: "Wirtschaftseinheit", Type: tText, ReadOnly: true},
		{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
		{Key: "usage_type", Label: "Nutzungsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("UsageType")},
	}
	if kind == kindUnit || kind == kindSpace || kind == "" {
		fs = append(fs,
			crud.Field{Key: "floor", Label: "Geschoss", Type: tText, Listable: true, Lookup: catalogLookup("Floor"), Group: "Lage"},
			crud.Field{Key: "location", Label: "Lage", Type: tText, Listable: kind != "", Lookup: catalogLookup("Location"), Group: "Lage"})
	}
	if kind == kindSpace || kind == "" {
		fs = append(fs, crud.Field{Key: "pool_id", Label: "Aus Pool", Type: tText, Listable: kind != "",
			Lookup: &metamodel.Lookup{Object: "PooledSpace", ValueField: "object_id", LabelFields: []string{"designation"},
				Filters: map[string]string{"company_code": "company_code", "building_id": "building_id"}}})
	}
	if kind == kindPool || kind == "" {
		fs = append(fs,
			crud.Field{Key: "area_type", Label: "Flächenart", Type: tText, Listable: kind != "", Lookup: catalogLookup("MeasurementType")},
			crud.Field{Key: "total_area", Label: "Gesamtfläche", Type: tNum, Listable: kind != ""})
	}
	return append(fs, validityFields()...)
}

// rentObjectBase: Sicht auf realestate__rent_object; kind leer = alle Arten.
func (m *Module) rentObjectBase(object, title, kind string) *crud.Entity {
	measurements := metamodel.SectionDefinition{Key: "bemessungen", Title: "Bemessungen", Relation: &metamodel.Relation{Object: "Measurement", ForeignKey: "object_id", Match: match("object_id", "object_id"),
		Columns: []string{"measurement_type", "value", "unit", "valid_from", "valid_to"}}}
	e := &crud.Entity{
		Object: object, Title: title, Icon: "icon-door", Table: "realestate__rent_object", Section: "Bestand",
		Keys: []string{"company_code", "object_id"}, Order: "company_code, building_id, object_id", TitleField: "designation",
		Filters: []string{"company_code", "entity_id", "building_id", "usage_type", "status"},
		Search:  []string{"object_id", "designation"},
		Events:  true, CompanyCodeField: "company_code",
		Fields:   rentObjectFields(kind),
		Sections: []metamodel.SectionDefinition{measurements},
		Access:   access("entity_id", "building_id"),
	}
	if kind == "" {
		// Gemeinsamer Stamm: Lesen und Verweise (Verträge, Hauptbuch, Bemessungen).
		e.ReadOnly = true
		e.Filters = append(e.Filters, "kind")
		e.Access = &crud.Access{Records: true, CompanyCode: "company_code", Fields: []string{"entity_id", "building_id"}}
		return e
	}
	e.ListScope = func(context.Context) (string, []any, bool, error) { return "kind = ?", []any{kind}, false, nil }
	e.CheckRecord = func(ctx context.Context, action string, rec crud.Record) error {
		if k := crud.Str(rec["kind"]); k != "" && k != kind {
			return fmt.Errorf("%w: %s %s", sdk.ErrNotFound, title, crud.Str(rec["object_id"]))
		}
		return requireWrite(ctx, "RentObject", action, crud.Str(rec["company_code"]))
	}
	e.Validate = func(ctx context.Context, rec, old crud.Record) error { return m.checkRentObject(ctx, kind, rec, old) }
	return e
}

func (m *Module) rentObject() *crud.Entity {
	return m.rentObjectBase("RentObject", "Mietobjekte (alle)", "")
}

func (m *Module) rentalUnit() *crud.Entity {
	return m.rentObjectBase("RentalUnit", "Mieteinheiten", kindUnit)
}

func (m *Module) rentalSpace() *crud.Entity {
	return m.rentObjectBase("RentalSpace", "Flächen", kindSpace)
}

func (m *Module) pooledSpace() *crud.Entity {
	e := m.rentObjectBase("PooledSpace", "Flächenpools", kindPool)
	e.Sections = append(e.Sections, metamodel.SectionDefinition{Key: "flaechen", Title: "Geschnittene Flächen",
		Relation: &metamodel.Relation{Object: "RentalSpace", ForeignKey: "pool_id", Match: match("pool_id", "object_id"), Columns: []string{"object_id", "designation", "usage_type", "status"}}})
	return e
}

func (m *Module) compositeUnit() *crud.Entity {
	e := m.rentObjectBase("CompositeUnit", "Vertragsobjekte", kindComposite)
	e.Sections = append([]metamodel.SectionDefinition{{Key: "bestandteile", Title: "Bestandteile",
		Relation: &metamodel.Relation{Object: "CompositeItem", ForeignKey: "composite_id", Match: match("composite_id", "object_id"), Columns: []string{"object_id", "valid_from", "valid_to"}}}}, e.Sections...)
	return e
}

// checkRentObject: Gebäude, Nutzungsart (für die Art zugelassen), ID, Pool, Kataloge.
func (m *Module) checkRentObject(ctx context.Context, kind string, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["kind"] = kind
	if old == nil {
		building := strings.TrimSpace(crud.Str(rec["building_id"]))
		res, err := m.db.Query(ctx, "SELECT entity_id FROM realestate__building WHERE company_code = ? AND building_id = ?", cc, building)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Gebäude %s gibt es im Buchungskreis %s nicht", building, cc)
		}
		rec["building_id"], rec["entity_id"] = building, res.Rows[0][0]
		prefix, err := m.usagePrefix(ctx, cc, crud.Str(rec["usage_type"]), kind)
		if err != nil {
			return err
		}
		id := strings.TrimSpace(crud.Str(rec["object_id"]))
		if id == "" {
			if id, err = m.nextObjectID(ctx, building, prefix); err != nil {
				return err
			}
		} else if id, err = m.checkNewID(ctx, id, building, "Objekt"); err != nil {
			return err
		}
		rec["object_id"] = id
	}
	for _, c := range []struct{ table, field, label string }{
		{"floor", "floor", "Geschoss"}, {"location", "location", "Lage"}, {"measurement_type", "area_type", "Flächenart"},
	} {
		if err := m.requireCatalog(ctx, c.table, c.label, cc, rec[c.field]); err != nil {
			return err
		}
	}
	switch kind {
	case kindSpace:
		if pool := crud.Str(rec["pool_id"]); pool != "" {
			res, err := m.db.Query(ctx, "SELECT building_id FROM realestate__rent_object WHERE company_code = ? AND object_id = ? AND kind = ?", cc, pool, kindPool)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 || crud.Str(res.Rows[0][0]) != crud.Str(rec["building_id"]) {
				return crud.Invalid("Pool %s gibt es im Gebäude %s nicht", pool, crud.Str(rec["building_id"]))
			}
		}
	case kindPool:
		if toFloat(rec["total_area"]) < 0 {
			return crud.Invalid("Gesamtfläche darf nicht negativ sein")
		}
	}
	if err := m.checkValidity(ctx, rec); err != nil {
		return err
	}
	// Pool-Prüfung: geänderte Gesamtfläche oder Zuordnung einer Fläche.
	switch kind {
	case kindPool:
		if old != nil {
			total, areaType := toFloat(rec["total_area"]), crud.Str(rec["area_type"])
			return m.checkPool(ctx, poolCheck{cc: cc, pool: crud.Str(rec["object_id"]), date: crud.Today(), total: &total, areaType: &areaType})
		}
	case kindSpace:
		if pool := crud.Str(rec["pool_id"]); pool != "" {
			return m.checkPool(ctx, poolCheck{cc: cc, pool: pool, date: crud.Today(), spaces: []string{crud.Str(rec["object_id"])}})
		}
	}
	return nil
}

// usagePrefix: Kürzel der Nutzungsart, die für die Objektart zugelassen ist.
func (m *Module) usagePrefix(ctx context.Context, cc, usage, kind string) (string, error) {
	res, err := m.db.Query(ctx, "SELECT id_prefix, kinds FROM realestate__usage_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, usage, true)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Nutzungsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", usage, cc)
	}
	if kinds := crud.Str(res.Rows[0][1]); kinds != "" && !strings.Contains(","+kinds+",", ","+kind+",") {
		return "", crud.Invalid("Nutzungsart %s ist für %s nicht zugelassen (nur %s)", usage, kindLabel(kind), kinds)
	}
	return crud.Str(res.Rows[0][0]), nil
}

func kindLabel(kind string) string {
	for _, o := range kindOptions {
		if o.Value == kind {
			return o.Label
		}
	}
	return kind
}

// match: Unter-Objekte hängen über Buchungskreis und eine ID am Master
// (zusammengesetzter Schlüssel, metamodel.Relation.Match).
func match(childField, masterField string) map[string]string {
	return map[string]string{"company_code": "company_code", childField: masterField}
}
