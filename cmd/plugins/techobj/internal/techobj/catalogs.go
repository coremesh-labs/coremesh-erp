package techobj

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
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
	case "status":
		fields = append(fields, crud.Field{Key: "is_default", Label: "Vorschlag für neue Datensätze", Short: "Vorschlag", Type: tBool, Listable: true})
	case "id_prefix":
		fields = append(fields, crud.Field{Key: "id_prefix", Label: "Kürzel der IDs (z. B. LG → LG0001)", Short: "Kürzel", Type: tText, Required: true, Listable: true})
	case "reading_status":
		fields = append(fields,
			crud.Field{Key: "is_default", Label: "Vorschlag für neue Ablesungen", Short: "Vorschlag", Type: tBool, Listable: true},
			crud.Field{Key: "is_rejected", Label: "Verworfen (zählt nicht für Verbrauch und Prüfung)", Short: "Verworfen", Type: tBool, Listable: true})
	case "link_source":
		fields = append(fields, crud.Field{Key: "source_object", Label: "Object dieses Moduls (MeterPoint, TechLocation, TechObject)", Short: "Object", Type: tText, Required: true, Listable: true})
	case "reading_type":
		fields = append(fields, crud.Field{Key: "replace_role", Label: "Vorschlag beim Zählertausch", Short: "Tausch", Type: tSel, Listable: true, Options: replaceRoleOptions})
	case "unit":
		fields = append(fields, crud.Field{Key: "decimals", Label: "Nachkommastellen", Short: "Stellen", Type: tNum, Listable: true})
	case "medium":
		fields = append(fields,
			crud.Field{Key: "unit", Label: "Einheit des Hauptzählwerks", Short: "Einheit", Type: tText, Listable: true, Lookup: catalogLookup("TechUnit")},
			crud.Field{Key: "calibration_years", Label: "Eichfrist in Jahren (0 = keine)", Short: "Eichfrist", Type: tNum, Listable: true},
			crud.Field{Key: "register_code", Label: "Zählwerk neuer Zähler ohne Vorlage im Gerätetyp (z. B. 1; leer = keins)", Short: "Zählwerk", Type: tText},
			crud.Field{Key: "alert_percent", Label: "Auffällig ab % über dem üblichen Tagesverbrauch (Verbrauchsauswertung; 0 = nie)", Short: "Auffällig ab %", Type: tNum, Listable: true},
			crud.Field{Key: "reading_days", Label: "Ablesung fällig nach Tagen (Übersicht; 0 = nie)", Short: "Ablesung alle", Type: tNum, Listable: true},
			crud.Field{Key: "is_seasonal", Label: "Jahreszeitabhängig (Heizung: Auffälligkeit je Jahr statt je Ablesezeitraum)", Short: "jahreszeitabh.", Type: tBool, Listable: true})
	case "link_object":
		fields = append(fields,
			crud.Field{Key: "service_object", Label: "Object im anderen Modul (z. B. RentObject, Contract, BusinessPartner)", Short: "Object", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "company_scoped", Label: "Schlüssel mit Buchungskreis (<BuKr>|<ID>)", Short: "mit BuKr", Type: tBool, Listable: true},
			crud.Field{Key: "value_field", Label: "Schlüsselfeld in der Liste des Objects (z. B. object_id; leer = keine Auswahl)", Short: "Schlüsselfeld", Type: tText},
			crud.Field{Key: "label_fields", Label: "Bezeichnungsfelder (durch Komma, z. B. designation oder name1, name2)", Short: "Bezeichnung", Type: tText},
			crud.Field{Key: "tenant_contract_types", Label: "Vertragsarten des Nutzers (z. B. MV, GM, SP; zeigt den Mieter)", Short: "Nutzerverträge", Type: tText},
			crud.Field{Key: "partner_field", Label: "Feld mit dem Geschäftspartner (Empfänger der Ablesemitteilung, z. B. partner_id; beim Partner id)", Short: "Partnerfeld", Type: tText},
			crud.Field{Key: "reference_field", Label: "Feld mit der Kunden-/Vertragsnummer beim Partner (z. B. external_number)", Short: "Nummernfeld", Type: tText})
	case "link_role":
		fields = append(fields,
			crud.Field{Key: "object_types", Label: "Erlaubte Objekte (Schlüssel, durch Komma; leer = alle)", Short: "Objekte", Type: tText, Listable: true},
			crud.Field{Key: "is_exclusive", Label: "Exklusiv (ein Objekt je Stichtag)", Short: "Exklusiv", Type: tBool, Listable: true},
			crud.Field{Key: "is_user", Label: "Nutzer („misst für“: am Messpunkt, im Rundgang)", Short: "Nutzer", Type: tBool, Listable: true},
			crud.Field{Key: "show_in_where", Label: "In „Wo ist der Zähler?“ zeigen", Short: "Wo-Anzeige", Type: tBool, Listable: true})
	case "maintenance_type":
		fields = append(fields,
			crud.Field{Key: "object_classes", Label: "Für Objektklassen (Schlüssel, durch Komma, z. B. HEIZUNG)", Short: "Klassen", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "interval_months", Label: "Abstand in Monaten", Short: "Monate", Type: tNum, Listable: true},
			crud.Field{Key: "lead_days", Label: "Vorlauf in Tagen (Übersicht)", Short: "Vorlauf", Type: tNum, Listable: true},
			crud.Field{Key: "is_legal", Label: "Gesetzlich vorgeschrieben", Short: "Pflicht", Type: tBool, Listable: true},
			crud.Field{Key: "process_type", Label: "Vorgangsart beim Beauftragen (Modul Vorgänge, z. B. WARTUNG; leer = kein Vorgang)", Short: "Vorgangsart", Type: tText})
	case "relation_type":
		fields = append(fields, crud.Field{Key: "is_contained", Label: "Im übergeordneten enthalten (Verbrauchsauswertung zeigt den Rest)", Short: "enthalten", Type: tBool, Listable: true})
	case "notice_type":
		fields = append(fields,
			crud.Field{Key: "link_role", Label: "Empfänger: in dieser Rolle verknüpftes Objekt (z. B. Lieferant)", Short: "Rolle", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("TechLinkRole")},
			crud.Field{Key: "number_type", Label: "Zählernummer im Brief (Nummernart; leer = alle Nummern)", Short: "Nummernart", Type: tText, Listable: true, Lookup: catalogLookup("TechNumberType")},
			crud.Field{Key: "template", Label: "Vorlage (Code|Sprache, Verwendung LETTER; leer = erste der Verwendung)", Short: "Vorlage", Type: tText},
			crud.Field{Key: "doc_type", Label: "Dokumentart im DMS (leer = nicht ablegen)", Short: "Dokumentart", Type: tText, Listable: true},
			crud.Field{Key: "subject", Label: "Betreff (Platzhalter wie im Text)", Short: "Betreff", Type: tText, Required: true},
			crud.Field{Key: "body", Label: "Text – Platzhalter: {datum} Ablesedatum, {staende} Zähler und Stände (je Zeile), {kundennummer}, {vertrag}, {objekt} Verbrauchsstelle", Short: "Text", Type: tArea, Required: true, Wide: true})
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
			case "status", "reading_status":
				return m.singleDefault(ctx, c.Table, rec)
			case "id_prefix":
				return m.checkIDPrefix(ctx, rec)
			case "link_source":
				obj := strings.TrimSpace(crud.Str(rec["source_object"]))
				if _, ok := m.sourceTable(obj); !ok {
					return crud.Invalid("Object %q: nur Objects dieses Moduls mit eigener ID (MeterPoint, TechLocation, TechObject)", obj)
				}
				rec["source_object"] = obj
			case "medium":
				rec["register_code"] = nilIfEmpty(trimUpper(rec["register_code"]))
				rec["unit"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["unit"])))
				if toInt(rec["calibration_years"]) < 0 {
					return crud.Invalid("Eichfrist darf nicht negativ sein")
				}
				if toInt(rec["alert_percent"]) < 0 {
					return crud.Invalid("Auffällig ab %%: nicht negativ")
				}
				return m.requireCatalog(ctx, "unit", "Einheit", cc, rec["unit"])
			case "unit":
				if d := toInt(rec["decimals"]); d < 0 || d > 6 {
					return crud.Invalid("Nachkommastellen: 0 bis 6")
				}
			case "link_object":
				rec["service_object"] = strings.TrimSpace(crud.Str(rec["service_object"]))
				rec["value_field"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["value_field"])))
				rec["label_fields"] = nilIfEmpty(strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["label_fields"])), " ", ""))
				rec["tenant_contract_types"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["tenant_contract_types"])), ","))
				rec["partner_field"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["partner_field"])))
				rec["reference_field"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["reference_field"])))
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
			case "notice_type":
				return m.checkNoticeType(ctx, rec)
			case "maintenance_type":
				classes := splitCodes(crud.Str(rec["object_classes"]))
				if len(classes) == 0 {
					return crud.Invalid("Objektklassen fehlen")
				}
				for _, c := range classes {
					if ok, err := m.exists(ctx, "object_class", cc, c); err != nil {
						return err
					} else if !ok {
						return crud.Invalid("Objektklasse %s gibt es im Buchungskreis %s nicht", c, cc)
					}
				}
				rec["object_classes"] = strings.Join(classes, ",")
				if toInt(rec["interval_months"]) <= 0 {
					return crud.Invalid("Abstand in Monaten: mindestens 1")
				}
				if toInt(rec["lead_days"]) < 0 {
					return crud.Invalid("Vorlauf: nicht negativ")
				}
				rec["process_type"] = nilIfEmpty(trimUpper(rec["process_type"]))
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

var replaceRoleOptions = []metamodel.Option{
	{Value: "REMOVE", Label: "Endstand des alten Geräts (Ausbau)"}, {Value: "INSTALL", Label: "Anfangsstand des neuen Geräts (Einbau)"},
}

// singleDefault: höchstens ein Vorschlag je Katalog und Buchungskreis.
func (m *Module) singleDefault(ctx context.Context, table string, rec crud.Record) error {
	if !crud.AsBool(rec["is_default"]) {
		return nil
	}
	other, err := m.one(ctx, "SELECT code FROM techobj__"+table+" WHERE company_code = ? AND is_default = ? AND code <> ?",
		crud.Str(rec["company_code"]), true, crud.Str(rec["code"]))
	if err != nil {
		return err
	}
	if other != "" {
		return crud.Invalid("Vorschlag ist bereits %s – dort erst abwählen", other)
	}
	return nil
}

// idObjects: Objects mit sprechender ID aus dem Katalog „Kürzel der IDs“
// (Schlüssel → Tabelle, Spalte); technische Objekte nehmen das Kürzel der Objektklasse.
var idObjects = map[string][2]string{
	"LOCATION": {"techobj__location", "location_id"},
	"TYPE":     {"techobj__equipment_type", "type_id"},
	"POINT":    {"techobj__point", "point_id"},
}

func (m *Module) checkIDPrefix(ctx context.Context, rec crud.Record) error {
	if _, ok := idObjects[crud.Str(rec["code"])]; !ok {
		return crud.Invalid("Schlüssel %s: LOCATION (Lagen), TYPE (Gerätetypen) oder POINT (Messpunkte)", crud.Str(rec["code"]))
	}
	prefix := strings.TrimSpace(crud.Str(rec["id_prefix"]))
	if !prefixRe.MatchString(prefix) {
		return crud.Invalid("Kürzel %q: 1–4 Buchstaben", prefix)
	}
	rec["id_prefix"] = strings.ToUpper(prefix)
	return nil
}

// prefixOf: Kürzel der IDs für LOCATION, TYPE, POINT aus dem Katalog.
func (m *Module) prefixOf(ctx context.Context, cc, code string) (string, error) {
	p, err := m.one(ctx, "SELECT id_prefix FROM techobj__id_prefix WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return "", err
	}
	if p == "" {
		return "", crud.Invalid("Kein Kürzel der IDs für %s im Buchungskreis %s – Einstellungen → Kürzel der IDs (oder ID selbst eingeben)", code, cc)
	}
	return p, nil
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
		{"code": "PLANNED", "name": "geplant"}, {"code": "ACTIVE", "name": "in Betrieb", "is_default": true},
		{"code": "STOCK", "name": "im Lager"}, {"code": "RETIRED", "name": "außer Betrieb / ausgebaut"},
	},
	"id_prefix": {
		{"code": "LOCATION", "name": "Lagen", "id_prefix": "LG"}, {"code": "TYPE", "name": "Gerätetypen", "id_prefix": "GT"},
		{"code": "POINT", "name": "Messpunkte", "id_prefix": "MP"},
	},
	"reading_status": {
		{"code": "ENTERED", "name": "erfasst", "is_default": true}, {"code": "CHECKED", "name": "geprüft"},
		{"code": "REJECTED", "name": "verworfen", "is_rejected": true},
	},
	"link_source": {
		{"code": "POINT", "name": "Messpunkt", "source_object": "MeterPoint"}, {"code": "LOCATION", "name": "Lage", "source_object": "TechLocation"},
		{"code": "EQUIPMENT", "name": "Technisches Objekt", "source_object": "TechObject"},
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
		{"code": "STROM", "name": "Strom", "unit": "KWH", "calibration_years": 8, "register_code": "1"},
		{"code": "GAS", "name": "Gas", "unit": "M3", "calibration_years": 8, "register_code": "1", "is_seasonal": true},
		{"code": "KW", "name": "Kaltwasser", "unit": "M3", "calibration_years": 6, "register_code": "1"},
		{"code": "WW", "name": "Warmwasser", "unit": "M3", "calibration_years": 5, "register_code": "1"},
		{"code": "WAERME", "name": "Wärme (Wärmemengenzähler)", "unit": "KWH", "calibration_years": 5, "register_code": "1", "is_seasonal": true},
		{"code": "HKV", "name": "Heizkostenverteiler", "unit": "EINH", "calibration_years": 0, "register_code": "1", "is_seasonal": true},
		{"code": "SONST", "name": "Sonstiges (Betriebsstunden, Druck …)", "unit": "H", "calibration_years": 0},
	},
	"number_type": {
		{"code": "SERIAL", "name": "Seriennummer (Fabriknummer)"},
		{"code": "METER", "name": "Zählernummer / Eigentumsnummer"},
		{"code": "RADIO", "name": "Funk-ID / Modulnummer"},
		{"code": "PROVIDER", "name": "Gerätenummer beim Messdienst"},
	},
	"relation_type": {
		{"code": "SUB", "name": "Unterzähler (im übergeordneten enthalten)", "is_contained": true},
		{"code": "REST", "name": "Restverbrauch (übergeordneter minus Unterzähler)"},
		{"code": "SUM", "name": "Summe (rechnerischer Messpunkt)"},
	},
	"reading_type": {
		{"code": "PERIODIC", "name": "turnusmäßige Ablesung"}, {"code": "INTERIM", "name": "Zwischenablesung"},
		{"code": "HANDOVER", "name": "Übergabe (Ein-/Auszug)"}, {"code": "INSTALL", "name": "Einbau", "replace_role": "INSTALL"},
		{"code": "REMOVE", "name": "Ausbau", "replace_role": "REMOVE"}, {"code": "SUPPLIER", "name": "Versorgerwechsel"},
		{"code": "ESTIMATE", "name": "Schätzung"}, {"code": "PROVIDER", "name": "Ablesung durch Versorger / Messdienst"},
		{"code": "HISTORIC", "name": "historisch nachgetragen"},
	},
	"link_object": {
		{"code": "RENTOBJ", "name": "Mietobjekt", "service_object": "RentObject", "company_scoped": true, "tenant_contract_types": "MV,GM,SP",
			"value_field": "object_id", "label_fields": "designation"},
		{"code": "BUILDING", "name": "Gebäude", "service_object": "Building", "company_scoped": true, "value_field": "building_id", "label_fields": "designation"},
		{"code": "ENTITY", "name": "Wirtschaftseinheit", "service_object": "BusinessEntity", "company_scoped": true, "value_field": "entity_id", "label_fields": "designation"},
		{"code": "ASSET", "name": "Objekt (Wohnung, Haus, Auto …)", "service_object": "Asset", "company_scoped": true, "value_field": "asset_id", "label_fields": "designation"},
		{"code": "CONTRACT", "name": "Vertrag", "service_object": "Contract", "company_scoped": true, "value_field": "contract_id", "label_fields": "designation",
			"partner_field": "partner_id", "reference_field": "external_number"},
		{"code": "PARTNER", "name": "Geschäftspartner", "service_object": "BusinessPartner", "company_scoped": false, "value_field": "id", "label_fields": "name1,name2",
			"partner_field": "id"},
	},
	"link_role": {
		{"code": "SITE", "name": "Standort (gehört zu)", "object_types": "RENTOBJ,BUILDING,ENTITY,ASSET"},
		{"code": "MEASURES", "name": "misst für (Nutzer)", "object_types": "RENTOBJ,BUILDING,ENTITY,ASSET,CONTRACT", "is_user": true, "show_in_where": true},
		{"code": "SUPPLIER", "name": "Lieferant (Liefervertrag)", "object_types": "CONTRACT", "is_exclusive": true, "show_in_where": true},
		{"code": "GRID", "name": "Netzbetreiber / Messstellenbetreiber", "object_types": "PARTNER,CONTRACT", "is_exclusive": true, "show_in_where": true},
		{"code": "PROVIDER", "name": "Messdienst (Abrechnung, Gerätemiete)", "object_types": "CONTRACT,PARTNER", "show_in_where": true},
		{"code": "WEG", "name": "WEG / Verwaltung (Meldung)", "object_types": "CONTRACT,PARTNER"},
		{"code": "SERVICE", "name": "Wartung / Service", "object_types": "CONTRACT,PARTNER"},
		{"code": "OWNER", "name": "Eigentümer", "object_types": "PARTNER"},
	},
	"maintenance_type": {
		{"code": "HZ_WARTUNG", "name": "Wartung Heizungsanlage", "object_classes": "HEIZUNG", "process_type": "WARTUNG"},
		{"code": "ABGAS", "name": "Schornsteinfeger / Abgasmessung", "object_classes": "HEIZUNG", "is_legal": true, "process_type": "WARTUNG"},
		{"code": "RWM_PRUEF", "name": "Prüfung Rauchwarnmelder", "object_classes": "RWM", "is_legal": true, "process_type": "WARTUNG"},
		{"code": "AF_HAUPT", "name": "Hauptprüfung Aufzug (ZÜS)", "object_classes": "AUFZUG", "interval_months": 24, "lead_days": 60, "is_legal": true, "process_type": "WARTUNG"},
		{"code": "AF_WARTUNG", "name": "Wartung Aufzug", "object_classes": "AUFZUG", "process_type": "WARTUNG"},
		{"code": "LEGIONELLEN", "name": "Trinkwasseruntersuchung (Legionellen)", "object_classes": "WARMWASSER", "interval_months": 36, "lead_days": 60, "is_legal": true, "process_type": "WARTUNG"},
		{"code": "LU_FILTER", "name": "Wartung Lüftung / Filterwechsel", "object_classes": "LUEFTUNG", "process_type": "WARTUNG"},
		{"code": "PU_WARTUNG", "name": "Wartung Pumpe / Hebeanlage", "object_classes": "PUMPE", "process_type": "WARTUNG"},
	},
	"notice_type": {
		{"code": "SUPPLIER", "name": "Zählerstandsmitteilung an den Lieferanten", "link_role": "SUPPLIER", "number_type": "METER", "doc_type": "LETTER",
			"subject": "Zählerstandsmitteilung zum {datum} – Kundennummer {kundennummer}", "body": noticeBody},
		{"code": "GRID", "name": "Zählerstandsmitteilung an den Netzbetreiber", "link_role": "GRID", "number_type": "METER", "doc_type": "LETTER",
			"subject": "Zählerstandsmitteilung zum {datum} – {kundennummer}", "body": noticeBody},
	},
}

// noticeBody: Vorschlag für den Text der Ablesemitteilung (im Katalog änderbar).
const noticeBody = "für die Verbrauchsstelle {objekt} teilen wir Ihnen die Zählerstände vom {datum} mit:\n\n{staende}\n\n" +
	"Bitte legen Sie diese Stände Ihrer Abrechnung zugrunde und bestätigen Sie uns den Eingang."

// catalogColumns: Zusatzspalten der Vorschlagswerte.
var catalogColumns = []string{"code", "name", "id_prefix", "is_meter", "is_default", "is_rejected", "source_object", "decimals", "unit",
	"calibration_years", "register_code", "replace_role", "service_object", "company_scoped", "value_field", "label_fields",
	"tenant_contract_types", "object_types", "is_exclusive", "is_user", "show_in_where",
	"is_contained", "is_seasonal", "object_classes", "interval_months", "lead_days", "is_legal", "process_type", "partner_field", "reference_field", "link_role", "number_type", "template", "doc_type", "subject", "body"}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an.
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	// Ein Katalog, der im Buchungskreis schon Einträge hat, bekommt Vorschlagswerte
	// nur auf ausdrücklichen Wunsch (setupCompany) – sonst kämen bewusst entfernte zurück.
	explicit := ctx.Value(setupKey{}) != nil
	n := 0
	for _, c := range catalogTables {
		if !explicit {
			if has, err := m.one(ctx, "SELECT 1 FROM techobj__"+c.Table+" WHERE company_code = ? LIMIT 1", cc); err != nil {
				return 0, err
			} else if has != "" {
				continue
			}
		}
		// Kennzeichen, die im Katalog noch niemand trägt, gelten als neu (Spalte aus
		// einem Release) – vor dem Durchlauf festgestellt, nicht je Eintrag.
		fresh := map[string]bool{}
		if explicit {
			for _, row := range catalogDefaults[c.Table] {
				for k, v := range row {
					if b, ok := v.(bool); ok && b {
						if _, seen := fresh[k]; seen {
							continue
						}
						taken, err := m.one(ctx, "SELECT 1 FROM techobj__"+c.Table+" WHERE company_code = ? AND "+k+" = ?", cc, true)
						if err != nil {
							return 0, err
						}
						fresh[k] = taken == ""
					}
				}
			}
		}
		for i, row := range catalogDefaults[c.Table] {
			ok, err := m.exists(ctx, c.Table, cc, crud.Str(row["code"]))
			if err != nil {
				return 0, err
			}
			if ok {
				if explicit {
					k, err := m.fillDefaults(ctx, c.Table, cc, row, fresh)
					if err != nil {
						return 0, err
					}
					n += k
				}
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

// fillDefaults: Zusatzspalten eines vorhandenen Vorschlagseintrags ergänzen
// (neue Spalten nach einem Release): leere Texte bekommen den Vorschlag, ein
// Kennzeichen nur, wenn es im Katalog des Buchungskreises noch niemand trägt.
func (m *Module) fillDefaults(ctx context.Context, table, cc string, row map[string]any, fresh map[string]bool) (int, error) {
	n := 0
	for _, k := range catalogColumns {
		v, ok := row[k]
		if !ok || k == "code" || k == "name" {
			continue
		}
		var q string
		var args []any
		if b, isBool := v.(bool); isBool {
			if !b || !fresh[k] {
				continue
			}
			q, args = "UPDATE techobj__"+table+" SET "+k+" = ? WHERE company_code = ? AND code = ?", []any{true, cc, row["code"]}
		} else {
			q, args = "UPDATE techobj__"+table+" SET "+k+" = ? WHERE company_code = ? AND code = ? AND ("+k+" IS NULL OR "+k+" = '')", []any{v, cc, row["code"]}
			if _, isNum := v.(int); isNum {
				continue // Zahlen haben Standardwerte in der Tabelle
			}
		}
		res, err := m.db.Exec(ctx, q, args...)
		if err != nil {
			return 0, err
		}
		if res.RowsAffected > 0 {
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
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der technischen Objekte angelegt bzw. ergänzt", cc, n)}}, nil
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
