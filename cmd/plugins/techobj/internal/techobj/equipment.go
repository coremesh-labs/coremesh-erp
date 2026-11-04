package techobj

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/docservice"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Gerätetyp ---------------------------------------------------------------------

// equipmentType: Modell eines Herstellers – Vorlage der Zählwerke, Eichfrist und
// die Unterlagen für alle Geräte dieses Typs (Datenblatt, Anleitung, Zulassung).
func (m *Module) equipmentType() *crud.Entity {
	return &crud.Entity{
		Object: "TechObjectType", Title: "Gerätetypen", Icon: "icon-book", Table: "techobj__equipment_type", Section: "Bestand",
		Keys: []string{"company_code", "type_id"}, Order: "company_code, object_class, manufacturer, model", TitleField: "designation",
		Filters: []string{"company_code", "object_class", "medium", "status"},
		Search:  []string{"type_id", "designation", "manufacturer", "model"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"object_class", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "type_id", Label: "Gerätetyp (ID; leer = nächste freie, z. B. GT0001)", Short: "Typ", Type: tText, Listable: true, Immutable: true},
			{Key: "object_class", Label: "Objektklasse", Short: "Klasse", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("TechObjectClass")},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "manufacturer", Label: "Hersteller", Type: tText, Listable: true},
			{Key: "model", Label: "Modell / Typbezeichnung", Short: "Modell", Type: tText, Listable: true},
			{Key: "medium", Label: "Sparte", Type: tText, Listable: true, Lookup: catalogLookup("TechMedium")},
			{Key: "principle", Label: "Messprinzip / Bauart (z. B. Funk, elektronisch, Verdunster)", Short: "Bauart", Type: tText},
			{Key: "calibration_years", Label: "Eichfrist in Jahren (leer = aus der Sparte)", Short: "Eichfrist", Type: tNum},
			{Key: "lifetime_years", Label: "Lebensdauer / Batterie in Jahren", Short: "Lebensdauer", Type: tNum},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechObjectStatus")},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "zaehlwerke", Title: "Zählwerke (Vorlage)", Relation: &metamodel.Relation{Object: "TechObjectTypeRegister", ForeignKey: "type_id",
				Match: match("type_id", "type_id"), Columns: []string{"register_code", "designation", "unit", "decimals", "overflow", "factor"}}},
			{Key: "geraete", Title: "Geräte dieses Typs", Relation: &metamodel.Relation{Object: "TechObject", ForeignKey: "type_id",
				Match: match("type_id", "type_id"), Columns: []string{"equipment_id", "designation", "status"}}},
			{Key: "dokumente", Title: "Unterlagen (Datenblatt, Anleitung, Zulassung)", Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Access:      access(),
		CheckRecord: checkWrite("TechObjectType"),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if old == nil {
				if _, err := m.setupCatalogs(ctx, cc); err != nil {
					return err
				}
				if err := m.newID(ctx, rec, "type_id", "techobj__equipment_type", cc, "@TYPE", "Gerätetyp"); err != nil {
					return err
				}
			}
			rec["object_class"], rec["medium"] = trimUpper(rec["object_class"]), nilIfEmpty(trimUpper(rec["medium"]))
			if err := m.requireCatalog(ctx, "object_class", "Objektklasse", cc, rec["object_class"]); err != nil {
				return err
			}
			if err := m.requireCatalog(ctx, "medium", "Sparte", cc, rec["medium"]); err != nil {
				return err
			}
			for _, k := range []string{"calibration_years", "lifetime_years"} {
				if crud.Str(rec[k]) == "" {
					rec[k] = nil
				} else if toInt(rec[k]) < 0 {
					return crud.Invalid("Jahre dürfen nicht negativ sein")
				}
			}
			if err := m.defaultStatus(ctx, rec, cc); err != nil {
				return err
			}
			m.stamp(ctx, rec)
			return nil
		},
	}
}

// registerFields: Zählwerk (Vorlage am Typ bzw. am Gerät).
func registerFields(owner crud.Field) []crud.Field {
	return []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		owner,
		{Key: "register_code", Label: "Zählwerk (Kennung, z. B. 1, HT, NT, M3)", Short: "Zählwerk", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "designation", Label: "Bezeichnung (z. B. Wärmemenge, Durchfluss)", Short: "Bezeichnung", Type: tText, Required: true, Listable: true},
		{Key: "unit", Label: "Einheit", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("TechUnit")},
		{Key: "decimals", Label: "Nachkommastellen (leer = aus der Einheit)", Short: "Stellen", Type: tNum, Listable: true},
		{Key: "overflow", Label: "Überlauf bei (z. B. 99999; leer = keiner)", Short: "Überlauf", Type: tNum, Listable: true},
		{Key: "factor", Label: "Wandlerfaktor (leer = 1)", Short: "Faktor", Type: tNum, Listable: true},
	}
}

func (m *Module) checkRegister(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["register_code"] = trimUpper(rec["register_code"])
	if !codeRe.MatchString(crud.Str(rec["register_code"])) {
		return crud.Invalid("Zählwerk %q: A–Z, 0–9, _ (höchstens 12 Zeichen)", crud.Str(rec["register_code"]))
	}
	rec["unit"] = trimUpper(rec["unit"])
	if err := m.requireCatalog(ctx, "unit", "Einheit", cc, rec["unit"]); err != nil {
		return err
	}
	if crud.Str(rec["decimals"]) == "" {
		d, err := m.one(ctx, "SELECT decimals FROM techobj__unit WHERE company_code = ? AND code = ?", cc, rec["unit"])
		if err != nil {
			return err
		}
		rec["decimals"] = toInt(d)
	}
	for _, k := range []string{"overflow", "factor"} {
		if crud.Str(rec[k]) == "" {
			rec[k] = nil
		} else if toFloat(rec[k]) <= 0 {
			return crud.Invalid("Überlauf und Faktor müssen größer als 0 sein")
		}
	}
	return nil
}

func (m *Module) typeRegister() *crud.Entity {
	return &crud.Entity{
		Object: "TechObjectTypeRegister", Title: "Zählwerke der Gerätetypen", Icon: "icon-list", Table: "techobj__type_register", Section: "Bestand",
		Keys: []string{"company_code", "type_id", "register_code"}, Order: "company_code, type_id, register_code",
		Filters:     []string{"company_code", "type_id"},
		Fields:      registerFields(crud.Field{Key: "type_id", Label: "Gerätetyp", Short: "Typ", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: typeLookup}),
		Access:      access(),
		CheckRecord: checkWrite("TechObjectType"),
		Validate:    func(ctx context.Context, rec, _ crud.Record) error { return m.checkRegister(ctx, rec) },
	}
}

// --- Technisches Objekt (Exemplar) ---------------------------------------------------

func (m *Module) equipment() *crud.Entity {
	return &crud.Entity{
		Object: "TechObject", Title: "Technische Objekte", Icon: "icon-gauge", Table: "techobj__equipment", Section: "Bestand",
		Keys: []string{"company_code", "equipment_id"}, Order: "company_code, object_class, equipment_id", TitleField: "designation",
		Filters: []string{"company_code", "object_class", "type_id", "location_id", "status", "number"},
		Search:  []string{"equipment_id", "designation"},
		FilterExpr: map[string]func(v any) (string, []any){
			// Suche über jede Nummer (Seriennummer, Zählernummer, Funk-ID …), auch Teile davon.
			"number": func(v any) (string, []any) {
				return "equipment_id IN (SELECT equipment_id FROM techobj__equipment_number WHERE number LIKE ?)",
					[]any{"%" + strings.TrimSpace(crud.Str(v)) + "%"}
			},
		},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"object_class", "type_id", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "object_class", Label: "Objektklasse", Short: "Klasse", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechObjectClass")},
			{Key: "equipment_id", Label: "Technisches Objekt (ID; leer = nächste freie, z. B. ZA0001)", Short: "Objekt", Type: tText, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "type_id", Label: "Gerätetyp", Short: "Typ", Type: tText, Listable: true, Lookup: typeLookup},
			{Key: "numbers", Label: "Nummern", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "number", Label: "Nummer (Serien-, Zähler-, Funknummer …)", Type: tText, FilterOnly: true},
			{Key: "installed", Label: "Eingebaut in (Messpunkt)", Short: "Eingebaut in", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "location_id", Label: "Lage (bei Anlagen ohne Messpunkt, z. B. Heizung)", Short: "Lage", Type: tText, Lookup: locationLookup, Group: "Lage"},
			{Key: "position", Label: "Position in der Lage", Short: "Position", Type: tText, Group: "Lage"},
			{Key: "build_year", Label: "Baujahr", Type: tNum, Group: "Prüfung"},
			{Key: "calibration_year", Label: "Eichjahr (Jahr der Eichung bzw. Herstellung)", Short: "Eichjahr", Type: tNum, Group: "Prüfung"},
			{Key: "calibration_due", Label: "Eichung gültig bis (Jahresende)", Short: "Eichung bis", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Group: "Prüfung"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechObjectStatus"), Group: "Gültigkeit"},
			{Key: "valid_from", Label: "Gültig ab", Type: tDate, Group: "Gültigkeit"},
			{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "nummern", Title: "Nummern", Relation: &metamodel.Relation{Object: "TechObjectNumber", ForeignKey: "equipment_id",
				Match: match("equipment_id", "equipment_id"), Columns: []string{"number_type", "number", "note"}}},
			{Key: "zaehlwerke", Title: "Zählwerke", Relation: &metamodel.Relation{Object: "TechObjectRegister", ForeignKey: "equipment_id",
				Match: match("equipment_id", "equipment_id"), Columns: []string{"register_code", "designation", "unit", "decimals", "overflow", "factor"}}},
			{Key: "einbau", Title: "Einbau", Relation: &metamodel.Relation{Object: "MeterInstallation", ForeignKey: "equipment_id",
				Match: match("equipment_id", "equipment_id"), Columns: []string{"point_id", "valid_from", "valid_to", "note"}}},
			{Key: "ablesungen", Title: "Ablesungen", Collapsed: true, Relation: &metamodel.Relation{Object: "MeterReading", ForeignKey: "equipment_id",
				Match: match("equipment_id", "equipment_id"), Columns: []string{"read_on", "register_code", "value", "reading_type", "point_id"}}},
			{Key: "verknuepfungen", Title: "Verknüpfungen (Wartung, Eigentümer …)", Collapsed: true, Relation: &metamodel.Relation{Object: "TechLink", ForeignKey: "source_id",
				Match: match("source_id", "equipment_id"), Filter: map[string]string{"source_kind": "TechObject"},
				Columns: []string{"role_code", "object_type", "object_id", "valid_from", "valid_to"}}},
			{Key: "dokumente", Title: "Dokumente (Eichschein, Prüfprotokoll, Typenschild)", Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "papers", Label: "Alle Unterlagen (mit Gerätetyp)", Record: true}, Handle: m.papersAction},
		},
		Access:      access(),
		CheckRecord: checkWrite("TechObject"),
		Validate:    m.checkEquipment,
		AfterCreate: m.copyTypeRegisters,
		// Objektklasse aus dem Gerätetyp, wenn nicht angegeben (vor der Pflichtprüfung).
		Prepare: func(ctx context.Context, rec crud.Record) error {
			if trimUpper(rec["object_class"]) == "" && strings.TrimSpace(crud.Str(rec["type_id"])) != "" {
				c, err := m.one(ctx, "SELECT object_class FROM techobj__equipment_type WHERE company_code = ? AND type_id = ?",
					crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["type_id"])))
				if err != nil {
					return err
				}
				rec["object_class"] = c
			}
			return nil
		},
		Decorate: m.decorateEquipment,
	}
}

func (m *Module) checkEquipment(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	typ := strings.TrimSpace(crud.Str(rec["type_id"]))
	rec["type_id"] = nilIfEmpty(typ)
	if old == nil {
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		rec["object_class"] = trimUpper(rec["object_class"])
		prefix, err := m.one(ctx, "SELECT id_prefix FROM techobj__object_class WHERE company_code = ? AND code = ? AND is_active = ?", cc, rec["object_class"], true)
		if err != nil {
			return err
		}
		if prefix == "" {
			return crud.Invalid("Objektklasse %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Technische Objekte → Einstellungen → Objektklassen",
				crud.Str(rec["object_class"]), cc)
		}
		if err := m.newID(ctx, rec, "equipment_id", "techobj__equipment", cc, prefix, "Technisches Objekt"); err != nil {
			return err
		}
	}
	if typ != "" {
		c, err := m.one(ctx, "SELECT object_class FROM techobj__equipment_type WHERE company_code = ? AND type_id = ?", cc, typ)
		if err != nil {
			return err
		}
		if c == "" {
			return crud.Invalid("Gerätetyp %s gibt es im Buchungskreis %s nicht", typ, cc)
		}
		if c != crud.Str(rec["object_class"]) {
			return crud.Invalid("Gerätetyp %s gehört zur Objektklasse %s, das Objekt zu %s", typ, c, crud.Str(rec["object_class"]))
		}
	}
	loc := strings.TrimSpace(crud.Str(rec["location_id"]))
	rec["location_id"] = nilIfEmpty(loc)
	if loc != "" {
		if ok, err := m.one(ctx, "SELECT 1 FROM techobj__location WHERE company_code = ? AND location_id = ?", cc, loc); err != nil {
			return err
		} else if ok == "" {
			return crud.Invalid("Lage %s gibt es im Buchungskreis %s nicht", loc, cc)
		}
	}
	year := time.Now().Year() + 1
	for _, k := range []string{"build_year", "calibration_year"} {
		if crud.Str(rec[k]) == "" {
			rec[k] = nil
			continue
		}
		if y := toInt(rec[k]); y < 1900 || int(y) > year {
			return crud.Invalid("Jahr %d: 1900 bis %d", y, year)
		}
		rec[k] = toInt(rec[k])
	}
	return m.checkValidity(ctx, rec, true, true)
}

// copyTypeRegisters: Ein neuer Zähler übernimmt die Zählwerke seines Gerätetyps;
// ohne Vorlage das Zählwerk der Sparte des Typs (Katalog Sparten) in deren Einheit.
func (m *Module) copyTypeRegisters(ctx context.Context, rec crud.Record) error {
	cc, id, typ := crud.Str(rec["company_code"]), crud.Str(rec["equipment_id"]), crud.Str(rec["type_id"])
	if typ == "" {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT register_code, designation, unit, decimals, overflow, factor FROM techobj__type_register
		WHERE company_code = ? AND type_id = ? ORDER BY register_code`, cc, typ)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		if _, err := m.db.Exec(ctx, `INSERT INTO techobj__register (company_code, equipment_id, register_code, designation, unit, decimals, overflow, factor)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, cc, id, r[0], r[1], r[2], r[3], r[4], r[5]); err != nil {
			return err
		}
	}
	if len(res.Rows) > 0 {
		return nil
	}
	meter, err := m.one(ctx, `SELECT c.is_meter FROM techobj__object_class c WHERE c.company_code = ? AND c.code = ?`, cc, rec["object_class"])
	if err != nil || !crud.AsBool(meter) {
		return err
	}
	res, err = m.db.Query(ctx, `SELECT md.register_code, md.name, md.unit, COALESCE(u.decimals, 0) FROM techobj__equipment_type t
		JOIN techobj__medium md ON md.company_code = t.company_code AND md.code = t.medium
		LEFT JOIN techobj__unit u ON u.company_code = md.company_code AND u.code = md.unit
		WHERE t.company_code = ? AND t.type_id = ? AND md.unit IS NOT NULL AND md.register_code IS NOT NULL`, cc, typ)
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	r := res.Rows[0]
	_, err = m.db.Exec(ctx, `INSERT INTO techobj__register (company_code, equipment_id, register_code, designation, unit, decimals)
		VALUES (?, ?, ?, ?, ?, ?)`, cc, id, r[0], r[1], r[2], r[3])
	return err
}

func (m *Module) decorateEquipment(ctx context.Context, rec crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["equipment_id"])
	nums, err := m.numbersText(ctx, cc, id)
	if err != nil {
		return err
	}
	rec["numbers"] = nums
	day := crud.KeyDate(ctx)
	p, err := m.one(ctx, `SELECT i.point_id || ' ' || p.designation FROM techobj__installation i
		JOIN techobj__point p ON p.company_code = i.company_code AND p.point_id = i.point_id
		WHERE i.company_code = ? AND i.equipment_id = ? AND i.valid_from <= ? AND i.valid_to >= ?`, cc, id, day, day)
	if err != nil {
		return err
	}
	rec["installed"] = p
	if y := toInt(rec["calibration_year"]); y > 0 {
		years, err := m.calibrationYears(ctx, cc, crud.Str(rec["type_id"]))
		if err != nil {
			return err
		}
		if years > 0 {
			due := y + years
			text := strconv.FormatInt(due, 10)
			if int(due) < time.Now().Year() {
				text += " (abgelaufen)"
			}
			rec["calibration_due"] = text
		}
	}
	return nil
}

// calibrationYears: Eichfrist des Gerätetyps, sonst seiner Sparte.
func (m *Module) calibrationYears(ctx context.Context, cc, typ string) (int64, error) {
	if typ == "" {
		return 0, nil
	}
	res, err := m.db.Query(ctx, `SELECT t.calibration_years, md.calibration_years FROM techobj__equipment_type t
		LEFT JOIN techobj__medium md ON md.company_code = t.company_code AND md.code = t.medium
		WHERE t.company_code = ? AND t.type_id = ?`, cc, typ)
	if err != nil || len(res.Rows) == 0 {
		return 0, err
	}
	if res.Rows[0][0] != nil {
		return toInt(res.Rows[0][0]), nil
	}
	return toInt(res.Rows[0][1]), nil
}

// numbersText: „Zählernummer 33403018, Seriennummer 4711“.
func (m *Module) numbersText(ctx context.Context, cc, id string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT COALESCE(t.name, n.number_type), n.number FROM techobj__equipment_number n
		LEFT JOIN techobj__number_type t ON t.company_code = n.company_code AND t.code = n.number_type
		WHERE n.company_code = ? AND n.equipment_id = ? ORDER BY t.sort_order, n.number`, cc, id)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, r := range res.Rows {
		name, _, _ := strings.Cut(crud.Str(r[0]), " (")
		parts = append(parts, name+" "+crud.Str(r[1]))
	}
	return strings.Join(parts, ", "), nil
}

// papersAction: TechObject.papers {id} – Dokumente des Geräts und seines Typs.
func (m *Module) papersAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Technisches Objekt: id <BuKr>|<ID>")
	}
	typ, err := m.one(ctx, "SELECT COALESCE(type_id, '') FROM techobj__equipment WHERE company_code = ? AND equipment_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	docs := docservice.New(m.services)
	var rows [][]any
	add := func(entity, key, from string) error {
		l, err := docs.List(ctx, docservice.ListRequest{EntityType: entity, EntityID: key})
		if err != nil {
			return err
		}
		for _, d := range l.Items {
			link := d.Link
			if link == "" {
				link = d.Location
			}
			rows = append(rows, []any{from, d.DocTypeName, d.Title, germanDate(d.DocDate), link})
		}
		return nil
	}
	if err := add("TechObject", cc+"|"+id, "Gerät"); err != nil {
		return sdk.Response{}, err
	}
	if typ != "" {
		if err := add("TechObjectType", cc+"|"+typ, "Gerätetyp "+typ); err != nil {
			return sdk.Response{}, err
		}
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%s: %d Unterlagen", id, len(rows)),
		"table": map[string]any{"columns": []string{"Herkunft", "Dokumentart", "Titel", "Datum", "Ablage / Link"}, "rows": rows}}}, nil
}

// --- Nummern und Zählwerke -------------------------------------------------------------

func (m *Module) number() *crud.Entity {
	return &crud.Entity{
		Object: "TechObjectNumber", Title: "Nummern", Icon: "icon-hash", Table: "techobj__equipment_number", Section: "Bestand",
		Keys: []string{"company_code", "equipment_id", "number_type", "number"}, Order: "company_code, equipment_id, number_type",
		Filters: []string{"company_code", "equipment_id", "number_type"}, Search: []string{"number"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "equipment_id", Label: "Technisches Objekt", Short: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: equipmentLookup},
			{Key: "number_type", Label: "Nummernart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechNumberType")},
			{Key: "number", Label: "Nummer", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		},
		Access:      access(),
		CheckRecord: checkWrite("TechObject"),
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			rec["number_type"], rec["number"] = trimUpper(rec["number_type"]), strings.TrimSpace(crud.Str(rec["number"]))
			if err := m.requireCatalog(ctx, "number_type", "Nummernart", cc, rec["number_type"]); err != nil {
				return err
			}
			if old != nil {
				return nil
			}
			other, err := m.one(ctx, `SELECT equipment_id FROM techobj__equipment_number WHERE company_code = ? AND number_type = ? AND number = ? AND equipment_id <> ?`,
				cc, rec["number_type"], rec["number"], crud.Str(rec["equipment_id"]))
			if err != nil {
				return err
			}
			if other != "" {
				crud.Warn(ctx, "Nummer %s ist auch bei %s eingetragen", crud.Str(rec["number"]), other)
			}
			return nil
		},
	}
}

func (m *Module) register() *crud.Entity {
	return &crud.Entity{
		Object: "TechObjectRegister", Title: "Zählwerke", Icon: "icon-list", Table: "techobj__register", Section: "Bestand",
		Keys: []string{"company_code", "equipment_id", "register_code"}, Order: "company_code, equipment_id, register_code",
		Filters:     []string{"company_code", "equipment_id"},
		Fields:      registerFields(crud.Field{Key: "equipment_id", Label: "Technisches Objekt", Short: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: equipmentLookup}),
		Access:      access(),
		CheckRecord: checkWrite("TechObject"),
		Validate:    func(ctx context.Context, rec, _ crud.Record) error { return m.checkRegister(ctx, rec) },
	}
}
