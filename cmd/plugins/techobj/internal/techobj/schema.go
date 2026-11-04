package techobj

import (
	"fmt"
	"strings"
)

// Schema der technischen Objekte (Atlas HCL, Präfix techobj__, keine DROPs).
//
// Drei getrennte Sichten, weil sie in der Praxis nie zusammenfallen:
//   - Lagebaum (location): wo man etwas findet – Gebäude › Keller › Zählerschrank,
//   - Messpunkte (point) mit Versorgungsbaum (point_relation): was gemessen
//     wird und wer an wem hängt – unabhängig vom eingebauten Gerät,
//   - technische Objekte (equipment) mit Gerätetyp, Nummern, Zählwerken; der
//     Einbau (installation) setzt ein Gerät mit Zeitscheibe in einen Messpunkt.
//
// Objekte anderer Module (Mietobjekt, Gebäude, Objekt, Vertrag, Partner …)
// hängen nur über die Brücke (link) daran – ohne Fremdschlüssel, geprüft über
// den Dienst des zuständigen Moduls.

type catalogTable struct {
	Table, Object, Title string
	Extra                string // weitere HCL-Spalten
}

var catalogTables = []catalogTable{
	{Table: "object_class", Object: "TechObjectClass", Title: "Objektklassen", Extra: `
  # Kürzel für sprechende IDs der technischen Objekte (z. B. ZA → ZA0001)
  column "id_prefix" { type = text }
  # Zähler: hat Zählwerke und wird in Messpunkte eingebaut
  column "is_meter" {
    type    = boolean
    default = false
  }`},
	{Table: "status", Object: "TechObjectStatus", Title: "Status", Extra: `
  # Vorschlag für neue Datensätze (höchstens einer)
  column "is_default" {
    type    = boolean
    default = false
  }`},
	{Table: "id_prefix", Object: "TechIdPrefix", Title: "Kürzel der IDs", Extra: `
  # Kürzel der sprechenden IDs je Object (Schlüssel: LOCATION, TYPE, POINT)
  column "id_prefix" { type = text }`},
	{Table: "reading_status", Object: "TechReadingStatus", Title: "Status der Ablesung", Extra: `
  # Vorschlag für neue Ablesungen
  column "is_default" {
    type    = boolean
    default = false
  }
  # verworfen: zählt weder für Verbrauch noch für die Prüfung der Stände
  column "is_rejected" {
    type    = boolean
    default = false
  }`},
	{Table: "link_source", Object: "TechLinkSource", Title: "Verknüpfbar aus diesem Modul", Extra: `
  # Object dieses Moduls (MeterPoint, TechLocation, TechObject)
  column "source_object" { type = text }`},
	{Table: "location_level", Object: "TechLocationLevel", Title: "Lageebenen"},
	{Table: "unit", Object: "TechUnit", Title: "Einheiten", Extra: `
  column "decimals" {
    type    = bigint
    default = 0
  }`},
	{Table: "medium", Object: "TechMedium", Title: "Sparten", Extra: `
  # Einheit des Hauptzählwerks (Katalog Einheiten)
  column "unit" {
    type = text
    null = true
  }
  # Eichfrist in Jahren (0 = keine)
  column "calibration_years" {
    type    = bigint
    default = 0
  }
  # Zählwerk eines neuen Zählers ohne Vorlage im Gerätetyp (leer = keins)
  column "register_code" {
    type = text
    null = true
  }
  # Verbrauchsauswertung: auffällig ab so viel Prozent über dem üblichen Tagesverbrauch
  column "alert_percent" {
    type    = bigint
    default = 50
  }
  # jahreszeitabhängig (Heizung): Auffälligkeit je Jahr statt je Ablesezeitraum
  column "is_seasonal" {
    type    = boolean
    default = false
  }`},
	{Table: "number_type", Object: "TechNumberType", Title: "Nummernarten"},
	{Table: "relation_type", Object: "TechRelationType", Title: "Beziehungsarten (Versorgungsbaum)", Extra: `
  # im übergeordneten Messpunkt enthalten: Verbrauchsauswertung zeigt den Rest
  column "is_contained" {
    type    = boolean
    default = false
  }`},
	{Table: "reading_type", Object: "TechReadingType", Title: "Ablesearten", Extra: `
  # Vorschlag beim Zählertausch: Endstand des alten (REMOVE) bzw. Anfangsstand des neuen Geräts (INSTALL)
  column "replace_role" {
    type = text
    null = true
  }`},
	{Table: "link_object", Object: "TechLinkObject", Title: "Verknüpfbare Objekte", Extra: `
  # Object des anderen Moduls (Dienst), z. B. RentObject, Contract, BusinessPartner
  column "service_object" { type = text }
  # Schlüssel im Dienst mit Buchungskreis (<BuKr>|<ID>), sonst nur die ID
  column "company_scoped" {
    type    = boolean
    default = true
  }
  # Auswahl im Formular: Schlüsselfeld und Bezeichnungsfelder (durch Komma) der Liste
  column "value_field" {
    type = text
    null = true
  }
  column "label_fields" {
    type = text
    null = true
  }
  # Vertragsarten, deren Partner Nutzer des Objekts ist (z. B. MV,GM,SP) – zeigt
  # beim Messpunkt den Mieter aus dem laufenden Vertrag; leer = keiner
  column "tenant_contract_types" {
    type = text
    null = true
  }
  # Ablesemitteilung: Feld mit dem Geschäftspartner (Empfänger) und mit der
  # Kunden-/Vertragsnummer beim Partner, z. B. partner_id und external_number
  column "partner_field" {
    type = text
    null = true
  }
  column "reference_field" {
    type = text
    null = true
  }`},
	{Table: "link_role", Object: "TechLinkRole", Title: "Rollen der Verknüpfung", Extra: `
  # erlaubte verknüpfbare Objekte (Schlüssel, durch Komma; leer = alle)
  column "object_types" {
    type = text
    null = true
  }
  # höchstens ein Objekt je Rolle und Stichtag (z. B. Lieferant)
  column "is_exclusive" {
    type    = boolean
    default = false
  }
  # „misst für“: erscheint am Messpunkt, im Rundgang und in „Wo ist der Zähler?“
  column "is_user" {
    type    = boolean
    default = false
  }
  # erscheint in „Wo ist der Zähler?“
  column "show_in_where" {
    type    = boolean
    default = false
  }`},
	{Table: "notice_type", Object: "TechNoticeType", Title: "Mitteilungsarten (Ablesemitteilung)", Extra: `
  # Empfänger: das in dieser Rolle verknüpfte Objekt (z. B. Liefervertrag → Partner)
  column "link_role" { type = text }
  # Nummernart, die im Brief als Zählernummer steht (leer = alle Nummern)
  column "number_type" {
    type = text
    null = true
  }
  # Vorlage (Code|Sprache, Verwendung LETTER; leer = erste der Verwendung)
  column "template" {
    type = text
    null = true
  }
  # Dokumentart im DMS (leer = nicht ablegen)
  column "doc_type" {
    type = text
    null = true
  }
  column "subject" { type = text }
  column "body"    { type = text }`},
}

func catalogHCL() string {
	var b strings.Builder
	for _, c := range catalogTables {
		fmt.Fprintf(&b, `
table "techobj__%s" {
  schema = schema.main
  column "company_code" { type = text }
  column "code"         { type = text }
  column "name"         { type = text }
  column "sort_order" {
    type    = bigint
    default = 0
  }
  column "is_active" {
    type    = boolean
    default = true
  }%s
  primary_key { columns = [column.company_code, column.code] }
}
`, c.Table, c.Extra)
	}
	return b.String()
}

// optional: Textspalten, die leer bleiben dürfen.
func optional(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = text
    null = true
  }`, c)
	}
	return b.String()
}

func optNum(cols ...string) string {
	var b strings.Builder
	for _, c := range cols {
		fmt.Fprintf(&b, `
  column %q {
    type = double
    null = true
  }`, c)
	}
	return b.String()
}

const validity = `
  column "valid_from" { type = date }
  column "valid_to"   { type = date }`

var schemaHCL = catalogHCL() + `
# Lagebaum: Gebäude › Geschoss › Raum › Zählerschrank – mit Wegbeschreibung
# und Zugang, damit man den Zähler findet.
table "techobj__location" {
  schema = schema.main
  column "company_code" { type = text }
  column "location_id"  { type = text }
  column "designation"  { type = text }` +
	optional("parent_id", "level", "directions", "access_info", "contact_id", "note", "status") + validity +
	optional("changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.location_id] }
  index "techobj__location_parent" {
    columns = [column.company_code, column.parent_id]
  }
}

# Gerätetyp (Modell): Hersteller, Modell, Vorlage der Zählwerke; hier hängen
# Datenblatt, Anleitung, Zulassung für alle Geräte des Typs.
table "techobj__equipment_type" {
  schema = schema.main
  column "company_code" { type = text }
  column "type_id"      { type = text }
  column "object_class" { type = text }
  column "designation"  { type = text }` +
	optional("manufacturer", "model", "medium", "principle", "note", "status") + `
  column "calibration_years" {
    type = bigint
    null = true
  }
  column "lifetime_years" {
    type = bigint
    null = true
  }` + optional("changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.type_id] }
}

# Zählwerke eines Gerätetyps (Vorlage) bzw. eines Geräts.
table "techobj__type_register" {
  schema = schema.main
  column "company_code"  { type = text }
  column "type_id"       { type = text }
  column "register_code" { type = text }
  column "designation"   { type = text }
  column "unit"          { type = text }
  column "decimals" {
    type    = bigint
    default = 0
  }` + optNum("overflow", "factor") + `
  primary_key { columns = [column.company_code, column.type_id, column.register_code] }
  foreign_key "techobj__type_register_type_fk" {
    columns     = [column.company_code, column.type_id]
    ref_columns = [table.techobj__equipment_type.column.company_code, table.techobj__equipment_type.column.type_id]
  }
}

# Technisches Objekt (Exemplar): Zähler, Heizung, Aufzug, Rauchwarnmelder …
table "techobj__equipment" {
  schema = schema.main
  column "company_code" { type = text }
  column "equipment_id" { type = text }
  column "object_class" { type = text }
  column "designation"  { type = text }` +
	optional("type_id", "location_id", "position", "note", "status") + `
  column "build_year" {
    type = bigint
    null = true
  }
  column "calibration_year" {
    type = bigint
    null = true
  }` + validity + optional("changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.equipment_id] }
  index "techobj__equipment_type_idx" {
    columns = [column.company_code, column.type_id]
  }
}

# Nummern je Gerät: Seriennummer, Zählernummer, Funk-ID, Nummer beim Messdienst …
table "techobj__equipment_number" {
  schema = schema.main
  column "company_code" { type = text }
  column "equipment_id" { type = text }
  column "number_type"  { type = text }
  column "number"       { type = text }` + optional("note") + `
  primary_key { columns = [column.company_code, column.equipment_id, column.number_type, column.number] }
  index "techobj__equipment_number_no" {
    columns = [column.company_code, column.number]
  }
  foreign_key "techobj__equipment_number_eq_fk" {
    columns     = [column.company_code, column.equipment_id]
    ref_columns = [table.techobj__equipment.column.company_code, table.techobj__equipment.column.equipment_id]
  }
}

table "techobj__register" {
  schema = schema.main
  column "company_code"  { type = text }
  column "equipment_id"  { type = text }
  column "register_code" { type = text }
  column "designation"   { type = text }
  column "unit"          { type = text }
  column "decimals" {
    type    = bigint
    default = 0
  }` + optNum("overflow", "factor") + `
  primary_key { columns = [column.company_code, column.equipment_id, column.register_code] }
  foreign_key "techobj__register_eq_fk" {
    columns     = [column.company_code, column.equipment_id]
    ref_columns = [table.techobj__equipment.column.company_code, table.techobj__equipment.column.equipment_id]
  }
}

# Messpunkt (Zählerplatz): bleibt am Ort, auch ohne eingebautes Gerät.
table "techobj__point" {
  schema = schema.main
  column "company_code" { type = text }
  column "point_id"     { type = text }
  column "designation"  { type = text }
  column "medium"       { type = text }` +
	optional("object_class", "registers", "market_location", "provider_number", "host_id", "note", "status") + validity +
	optional("changed_at", "changed_by") + `
  primary_key { columns = [column.company_code, column.point_id] }
}

# Lage des Messpunkts (Zeitscheibe): Knoten im Lagebaum und Position dort.
table "techobj__point_location" {
  schema = schema.main
  column "company_code" { type = text }
  column "point_id"     { type = text }
  column "location_id"  { type = text }` + validity + optional("position") + `
  primary_key { columns = [column.company_code, column.point_id, column.valid_from] }
  index "techobj__point_location_loc" {
    columns = [column.company_code, column.location_id]
  }
  foreign_key "techobj__point_location_point_fk" {
    columns     = [column.company_code, column.point_id]
    ref_columns = [table.techobj__point.column.company_code, table.techobj__point.column.point_id]
  }
  foreign_key "techobj__point_location_loc_fk" {
    columns     = [column.company_code, column.location_id]
    ref_columns = [table.techobj__location.column.company_code, table.techobj__location.column.location_id]
  }
}

# Versorgungsbaum (Zeitscheibe): übergeordneter ↔ untergeordneter Messpunkt.
table "techobj__point_relation" {
  schema = schema.main
  column "company_code"  { type = text }
  column "parent_id"     { type = text }
  column "child_id"      { type = text }
  column "relation_type" { type = text }` + validity + optional("note") + `
  primary_key { columns = [column.company_code, column.parent_id, column.child_id, column.valid_from] }
  index "techobj__point_relation_child" {
    columns = [column.company_code, column.child_id]
  }
  foreign_key "techobj__point_relation_parent_fk" {
    columns     = [column.company_code, column.parent_id]
    ref_columns = [table.techobj__point.column.company_code, table.techobj__point.column.point_id]
  }
  foreign_key "techobj__point_relation_child_fk" {
    columns     = [column.company_code, column.child_id]
    ref_columns = [table.techobj__point.column.company_code, table.techobj__point.column.point_id]
  }
}

# Einbau (Zeitscheibe): Gerät ↔ Messpunkt.
table "techobj__installation" {
  schema = schema.main
  column "company_code" { type = text }
  column "point_id"     { type = text }
  column "equipment_id" { type = text }` + validity + optional("note") + `
  primary_key { columns = [column.company_code, column.point_id, column.equipment_id, column.valid_from] }
  index "techobj__installation_eq" {
    columns = [column.company_code, column.equipment_id]
  }
  foreign_key "techobj__installation_point_fk" {
    columns     = [column.company_code, column.point_id]
    ref_columns = [table.techobj__point.column.company_code, table.techobj__point.column.point_id]
  }
  foreign_key "techobj__installation_eq_fk" {
    columns     = [column.company_code, column.equipment_id]
    ref_columns = [table.techobj__equipment.column.company_code, table.techobj__equipment.column.equipment_id]
  }
}

# Ablesung je Zählwerk und Tag: neutrales Faktum (wann, welcher Stand, woher,
# wer). Abrechnung, Tarife, Preise gehören nicht hierher.
table "techobj__reading" {
  schema = schema.main
  column "company_code"  { type = text }
  column "equipment_id"  { type = text }
  column "register_code" { type = text }
  column "read_on"       { type = date }
  column "reading_type"  { type = text }
  column "value"         { type = double }` +
	optional("read_time", "point_id", "reader", "source", "reported_to", "note", "status", "changed_at", "changed_by", "reported_on", "report_document") + `
  primary_key { columns = [column.company_code, column.equipment_id, column.register_code, column.read_on, column.reading_type] }
  index "techobj__reading_point" {
    columns = [column.company_code, column.point_id, column.read_on]
  }
  foreign_key "techobj__reading_register_fk" {
    columns     = [column.company_code, column.equipment_id, column.register_code]
    ref_columns = [table.techobj__register.column.company_code, table.techobj__register.column.equipment_id, table.techobj__register.column.register_code]
  }
}

# Brücke (Zeitscheibe): Messpunkt, Lageknoten oder technisches Objekt ↔ Objekt
# eines anderen Moduls in einer Rolle (Standort, misst für, Lieferant,
# Netzbetreiber, Messdienst, Wartung …).
table "techobj__link" {
  schema = schema.main
  column "company_code" { type = text }
  column "source_kind"  { type = text }
  column "source_id"    { type = text }
  column "role_code"    { type = text }
  column "object_type"  { type = text }
  column "object_id"    { type = text }` + validity + optional("note") + `
  primary_key { columns = [column.company_code, column.source_kind, column.source_id, column.role_code, column.object_type, column.object_id, column.valid_from] }
  index "techobj__link_target" {
    columns = [column.company_code, column.object_type, column.object_id]
  }
}
`
