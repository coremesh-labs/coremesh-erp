package realestate

import (
	"context"
	"fmt"
	"regexp"
	"slices"
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
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"entity_type"},
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
			measurementSection("entity_id"),
			partnerSection("entity_id"),
			{Key: "merkmale", Title: "Merkmale", Tags: true},
		},
		Actions: []crud.Action{m.partnersActionConfig(),
			{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Buchungskreis einrichten …", Fields: []string{"company_code"}}, Handle: m.setupCompanyAction}},
		Access: access("entity_id"),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "BusinessEntity", action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if old == nil {
				// Erste Wirtschaftseinheit im Buchungskreis: Kataloge mit Vorschlagswerten,
				// Partnerrollen, die das Partnermodul kennt.
				if _, err := m.setupCatalogs(ctx, cc); err != nil {
					return err
				}
				if _, err := m.setupPartnerRoles(ctx, cc); err != nil {
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
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"entity_id", "building_type"},
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
			measurementSection("building_id"),
			partnerSection("building_id"),
			{Key: "merkmale", Title: "Merkmale", Tags: true},
		},
		Actions: []crud.Action{m.partnersActionConfig()},
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

// --- Mietobjekte: eine Maske für alle Arten ----------------------------------------

// rentObject: gemeinsamer Stamm aller Mietobjekte – Mieteinheit, Fläche, Pool
// und Vertragsobjekt in einer Maske. Die Art (kind) wird zuerst gewählt; welche
// Felder und Abschnitte je Art erscheinen, steuern Darstellungsregeln (iam,
// Vorschlag über setup-company, siehe display.go). Felder, die zur Art nicht
// passen, leert die Prüfung.
func (m *Module) rentObject() *crud.Entity {
	return &crud.Entity{
		Object: "RentObject", Title: "Mietobjekte", Icon: "icon-door", Table: "realestate__rent_object", Section: "Bestand",
		Keys: []string{"company_code", "object_id"}, Order: "company_code, building_id, object_id", TitleField: "designation",
		Filters: []string{"company_code", "entity_id", "building_id", "kind", "usage_type", "status", "pool_id"},
		Search:  []string{"object_id", "designation"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"kind", "usage_type", "entity_id", "building_id"},
		Fields: append([]crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "kind", Label: "Objektart", Type: tSel, Options: kindOptions, Required: true, Listable: true, Immutable: true},
			{Key: "building_id", Label: "Gebäude", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "object_id", Label: "Objekt (ID; leer = nächste freie, z. B. LpzBrn1WG001)", Type: tText, Listable: true, Immutable: true},
			{Key: "entity_id", Label: "Wirtschaftseinheit", Type: tText, ReadOnly: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "usage_type", Label: "Nutzungsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("UsageType")},
			// Mieteinheit, Fläche
			{Key: "floor", Label: "Geschoss", Type: tText, Listable: true, Lookup: catalogLookup("Floor"), Group: "Lage"},
			{Key: "location", Label: "Lage", Type: tText, Lookup: catalogLookup("Location"), Group: "Lage"},
			// Fläche
			{Key: "pool_id", Label: "Aus Pool", Type: tText,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code", "building_id": "building_id", "kind": "=" + kindPool}}},
			// Pool
			{Key: "area_type", Label: "Flächenart", Type: tText, Lookup: catalogLookup("MeasurementType")},
			{Key: "total_area", Label: "Gesamtfläche", Type: tNum},
		}, validityFields()...),
		Sections: []metamodel.SectionDefinition{
			{Key: "bestandteile", Title: "Bestandteile", Relation: &metamodel.Relation{Object: "CompositeItem", ForeignKey: "composite_id",
				Match: match("composite_id", "object_id"), Columns: []string{"object_id", "valid_from", "valid_to"}}},
			{Key: "flaechen", Title: "Geschnittene Flächen", Relation: &metamodel.Relation{Object: "RentObject", ForeignKey: "pool_id",
				Match: match("pool_id", "object_id"), Columns: []string{"object_id", "designation", "usage_type", "status"}}},
			measurementSection("object_id"),
			partnerSection("object_id"),
			{Key: "merkmale", Title: "Merkmale", Tags: true},
		},
		Actions: []crud.Action{m.partnersActionConfig()},
		Access: access("entity_id", "building_id"),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "RentObject", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkRentObject,
	}
}

// measurementSection: Bemessungen eines Objekts (masterField: dessen ID).
func measurementSection(masterField string) metamodel.SectionDefinition {
	return metamodel.SectionDefinition{Key: "bemessungen", Title: "Bemessungen", Collapsed: true,
		Relation: &metamodel.Relation{Object: "Measurement", ForeignKey: "object_id", Match: match("object_id", masterField),
			Columns: []string{"measurement_type", "value", "unit", "valid_from", "valid_to"}}}
}

// partnerSection: Partnerzuordnungen eines Objekts (masterField: dessen ID).
func partnerSection(masterField string) metamodel.SectionDefinition {
	return metamodel.SectionDefinition{Key: "partner", Title: "Partner",
		Relation: &metamodel.Relation{Object: objectPartnerObject, ForeignKey: "object_id", Match: match("object_id", masterField),
			Columns: []string{"role_code", "partner_id", "share", "valid_from", "valid_to"}}}
}

// kindFields: Felder, die nur bei bestimmten Arten Sinn haben.
var kindFields = map[string][]string{
	kindUnit:      {"floor", "location"},
	kindSpace:     {"floor", "location", "pool_id"},
	kindPool:      {"area_type", "total_area"},
	kindComposite: {},
}

// checkRentObject: Art, Gebäude, Nutzungsart (für die Art zugelassen), ID, Pool,
// Kataloge. Felder, die zur Art nicht passen, werden geleert.
func (m *Module) checkRentObject(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	kind := trimUpper(rec["kind"])
	if old != nil {
		kind = crud.Str(old["kind"])
	}
	own, ok := kindFields[kind]
	if !ok {
		return crud.Invalid("Objektart %q: Mieteinheit, Fläche, Pool oder Vertragsobjekt", crud.Str(rec["kind"]))
	}
	rec["kind"] = kind
	for _, fs := range kindFields {
		for _, f := range fs {
			if !slices.Contains(own, f) {
				rec[f] = nil
			}
		}
	}
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
