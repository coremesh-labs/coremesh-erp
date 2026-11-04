package techobj

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Testbestand nach Goethes Faust: Auerbachs Keller (Gebäude), Wohnung Gretchen.

func (e *env) fixtures() {
	e.h.partners["BP1"], e.h.partners["BP2"], e.h.partners["BP3"] = "Margarete (Gretchen)", "Stadtwerke Walpurgis", "Wagner (Hausmeister)"
	e.h.objects["RentObject|1000|WE01"] = map[string]any{"object_id": "WE01", "designation": "Whg. Gretchen 1. OG"}
	e.h.objects["Building|1000|GEB1"] = map[string]any{"building_id": "GEB1", "designation": "Auerbachs Keller"}
	e.h.objects["Contract|1000|VE-1"] = map[string]any{"contract_id": "VE-1", "designation": "Fernwärme Mephisto Energie", "contract_type": "VE", "partner_id": "BP2"}
	e.h.objects["Contract|1000|VE-2"] = map[string]any{"contract_id": "VE-2", "designation": "Fernwärme Walpurgis", "contract_type": "VE", "partner_id": "BP2"}
	e.h.objects["Contract|1000|MV-1"] = map[string]any{"contract_id": "MV-1", "designation": "Mietvertrag Gretchen", "contract_type": "MV", "partner_id": "BP1"}
	e.h.contractObjects = []map[string]any{
		{"contract_id": "VE-1", "object_type": "RentObject", "object_id": "WE01", "valid_from": "2020-01-01", "valid_to": "9999-12-31"},
		{"contract_id": "MV-1", "object_type": "RentObject", "object_id": "WE01", "valid_from": "2024-01-01", "valid_to": "9999-12-31"},
	}
}

func TestLocationsTypesEquipment(t *testing.T) {
	e := setup(t)
	e.fixtures()
	geb := e.create("TechLocation", map[string]any{"company_code": "1000", "designation": "Auerbachs Keller", "level": "gebaeude", "valid_from": "2020-01-01"})
	if geb["location_id"] != "LG0001" || geb["status"] != "ACTIVE" {
		t.Fatalf("Lage: %v", geb)
	}
	e.create("TechLocation", map[string]any{"company_code": "1000", "designation": "Keller", "parent_id": "LG0001", "directions": "Treppe hinab, Tür mit dem Pudel",
		"access_info": "Schlüssel Nr. 7", "contact_id": "BP3", "valid_from": "2020-01-01"})
	e.create("TechLocation", map[string]any{"company_code": "1000", "designation": "Zählerschrank", "parent_id": "LG0002", "level": "SCHRANK", "valid_from": "2020-01-01"})
	_, err := e.call("TechLocation", "update", map[string]any{"id": "1000|LG0001", "data": map[string]any{"parent_id": "LG0003"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kreis im Lagebaum")
	if got := e.must("TechLocation", "get", map[string]any{"id": "1000|LG0003"}); got["path"] != "Auerbachs Keller › Keller › Zählerschrank" {
		t.Fatalf("Pfad: %v", got["path"])
	}

	// Gerätetyp Wärmemengenzähler mit zwei Zählwerken (kWh und m³ Durchfluss).
	typ := e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "WMZ Hexenküche", "manufacturer": "Mephisto",
		"model": "WMZ-7", "medium": "WAERME"})
	if typ["type_id"] != "GT0001" {
		t.Fatalf("Typ: %v", typ)
	}
	e.create("TechObjectTypeRegister", map[string]any{"company_code": "1000", "type_id": "GT0001", "register_code": "kwh", "designation": "Wärmemenge", "unit": "KWH"})
	e.create("TechObjectTypeRegister", map[string]any{"company_code": "1000", "type_id": "GT0001", "register_code": "M3", "designation": "Durchfluss", "unit": "M3"})
	wmz := e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "WMZ Gretchen", "calibration_year": 2018, "valid_from": "2018-01-01"})
	if wmz["equipment_id"] != "ZA0001" || wmz["object_class"] != "ZAEHLER" || !strings.HasPrefix(str(wmz["calibration_due"]), "2023") {
		t.Fatalf("Zähler: %v", wmz)
	}
	regs := items(e.must("TechObjectRegister", "list", map[string]any{"query": map[string]any{"company_code": "1000", "equipment_id": "ZA0001"}}))
	if len(regs) != 2 || regs[0]["register_code"] != "KWH" || toInt(regs[1]["decimals"]) != 3 {
		t.Fatalf("Zählwerke aus dem Typ: %v", regs)
	}
	e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "number_type": "METER", "number": "6DME4058874498"})
	e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "number_type": "serial", "number": "4711"})
	found := items(e.must("TechObject", "list", map[string]any{"query": map[string]any{"company_code": "1000", "number": "4058874"}}))
	if len(found) != 1 || !strings.Contains(str(found[0]["numbers"]), "Zählernummer / Eigentumsnummer 6DME4058874498") {
		t.Fatalf("Suche über Nummer: %v", found)
	}
	// Heizung ist kein Zähler: kein Einbau in Messpunkte, aber mit Lage.
	e.create("TechObject", map[string]any{"company_code": "1000", "object_class": "HEIZUNG", "designation": "Hexenkessel", "location_id": "LG0002", "valid_from": "2020-01-01"})
	_, err = e.call("TechObject", "create", map[string]any{"data": map[string]any{"company_code": "1000", "object_class": "HEIZUNG", "type_id": "GT0001", "designation": "x"}})
	expect(t, err, sdk.ErrInvalidArgument, "Typ anderer Klasse")
}

func TestPointsInstallationReadingsLinks(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.create("TechLocation", map[string]any{"company_code": "1000", "designation": "Auerbachs Keller", "valid_from": "2020-01-01"})
	e.create("TechLocation", map[string]any{"company_code": "1000", "designation": "Hausanschlussraum", "parent_id": "LG0001", "directions": "links neben dem Fass", "valid_from": "2020-01-01"})
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "WMZ", "medium": "WAERME"})
	e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "WMZ alt", "valid_from": "2018-01-01"})
	e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "WMZ neu", "valid_from": "2024-01-01"})
	if regs := items(e.must("TechObjectRegister", "list", map[string]any{"query": map[string]any{"company_code": "1000", "equipment_id": "ZA0001"}})); len(regs) != 1 || regs[0]["unit"] != "KWH" {
		t.Fatalf("Zählwerk aus der Sparte: %v", regs)
	}
	e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": "ZA0002", "number_type": "METER", "number": "48227252"})

	mp := e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Fernwärme Gretchen", "medium": "WAERME", "market_location": "DE000123", "valid_from": "2018-01-01"})
	if mp["point_id"] != "MP0001" {
		t.Fatalf("Messpunkt: %v", mp)
	}
	e.create("MeterPointLocation", map[string]any{"company_code": "1000", "point_id": "MP0001", "location_id": "LG0002", "position": "Platz 2", "valid_from": "2018-01-01"})
	_, err := e.call("MeterPointLocation", "create", map[string]any{"data": map[string]any{"company_code": "1000", "point_id": "MP0001", "location_id": "LG0001", "valid_from": "2019-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "zwei Lagen gleichzeitig")
	e.create("MeterInstallation", map[string]any{"company_code": "1000", "point_id": "MP0001", "equipment_id": "ZA0001", "valid_from": "2018-03-08"})
	_, err = e.call("MeterInstallation", "create", map[string]any{"data": map[string]any{"company_code": "1000", "point_id": "MP0001", "equipment_id": "ZA0002", "valid_from": "2020-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "zwei Geräte gleichzeitig")
	e.create("TechObject", map[string]any{"company_code": "1000", "object_class": "RWM", "designation": "Rauchmelder", "valid_from": "2020-01-01"})
	_, err = e.call("MeterInstallation", "create", map[string]any{"data": map[string]any{"company_code": "1000", "point_id": "MP0001", "equipment_id": "RW0001", "valid_from": "2017-01-01", "valid_to": "2017-12-31"}})
	expect(t, err, sdk.ErrInvalidArgument, "kein Zähler")

	// Ablesungen: Messpunkt aus dem Einbau, Stände fallen nicht.
	read := func(eq, day, typ string, v float64) error {
		_, err := e.call("MeterReading", "create", map[string]any{"data": map[string]any{"company_code": "1000", "equipment_id": eq, "register_code": "1",
			"read_on": day, "reading_type": typ, "value": v}})
		return err
	}
	if err := read("ZA0001", "2018-12-31", "PERIODIC", 31.967); err != nil {
		t.Fatal(err)
	}
	if err := read("ZA0001", "2019-12-31", "PERIODIC", 82.708); err != nil {
		t.Fatal(err)
	}
	expect(t, read("ZA0001", "2020-06-30", "PERIODIC", 50), sdk.ErrInvalidArgument, "Stand fällt")
	expect(t, read("ZA0001", "2019-06-30", "PERIODIC", 90), sdk.ErrInvalidArgument, "größer als der folgende")
	expect(t, read("ZA0001", "2020-01-01", "GIBTSNICHT", 90), sdk.ErrInvalidArgument, "Ableseart")
	r := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0001|1|2019-12-31|PERIODIC"})
	if r["point_id"] != "MP0001" || toFloat(r["consumption"]) != 50.7 || r["unit"] != "KWH" || r["_labels"].(map[string]any)["unit"] != "kWh" {
		t.Fatalf("Ablesung: %v", r)
	}
	// Gleiche Ablesung erneut gesendet (Offline-Abgleich): keine Dublette.
	expect(t, read("ZA0001", "2019-12-31", "PERIODIC", 82.708), sdk.ErrAlreadyExists, "Dublette")

	// Zählertausch: Endstand alt, Ausbau zum Vortag, Einbau neu mit Anfangsstand 0.
	res := e.must("MeterPoint", "replace", map[string]any{"id": "1000|MP0001", "data": map[string]any{"rp_date": "2024-06-26", "rp_equipment": "ZA0002", "rp_old": "299,544"}})
	if !strings.Contains(str(res["message"]), "ZA0001 ausgebaut zum 25.06.2024") {
		t.Fatalf("Tausch: %v", res)
	}
	ins := items(e.must("MeterInstallation", "list", map[string]any{"query": map[string]any{"company_code": "1000", "point_id": "MP0001", "includeHistory": "true"}}))
	if len(ins) != 2 {
		t.Fatalf("Einbauten: %v", ins)
	}
	if got := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0002|1|2024-06-26|INSTALL"}); toFloat(got["value"]) != 0 || got["point_id"] != "MP0001" {
		t.Fatalf("Anfangsstand: %v", got)
	}
	if got := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0001|1|2024-06-26|REMOVE"}); toFloat(got["value"]) != 299.544 {
		t.Fatalf("Endstand: %v", got)
	}

	// Brücke: Mietobjekt (zeigt den Mieter), Lieferantenwechsel als neue Zeitscheibe.
	link := func(role, typ, obj, from, to string) error {
		_, err := e.call("TechLink", "create", map[string]any{"data": map[string]any{"company_code": "1000", "source_kind": "MeterPoint", "source_id": "MP0001",
			"role_code": role, "object_type": typ, "object_id": obj, "valid_from": from, "valid_to": to}})
		return err
	}
	if err := link("MEASURES", "RENTOBJ", "WE01", "2018-01-01", ""); err != nil {
		t.Fatal(err)
	}
	if err := link("SUPPLIER", "CONTRACT", "VE-1", "2018-01-01", "2024-12-31"); err != nil {
		t.Fatal(err)
	}
	expect(t, link("SUPPLIER", "CONTRACT", "VE-2", "2024-06-01", ""), sdk.ErrInvalidArgument, "zwei Lieferanten")
	if err := link("SUPPLIER", "CONTRACT", "VE-2", "2025-01-01", ""); err != nil {
		t.Fatal(err)
	}
	expect(t, link("SUPPLIER", "BUILDING", "GEB1", "2025-01-01", ""), sdk.ErrInvalidArgument, "Rolle erlaubt nur Verträge")
	expect(t, link("MEASURES", "RENTOBJ", "WE99", "2025-01-01", ""), sdk.ErrInvalidArgument, "Mietobjekt unbekannt")
	if err := link("GRID", "PARTNER", "BP2", "2018-01-01", ""); err != nil {
		t.Fatal(err)
	}

	// Quelle mit Gerät und Stand; Filter über das Object des anderen Moduls.
	ls := items(e.must("TechLink", "list", map[string]any{"query": map[string]any{"company_code": "1000", "object": "RentObject"}}))
	if len(ls) != 1 || ls[0]["object_id"] != "WE01" || !strings.Contains(str(ls[0]["source_info"]), "ZA0002") {
		t.Fatalf("Verknüpfungen zum Mietobjekt: %v", ls)
	}
	// Stände zum Mietobjekt über die Verknüpfung (gültig am Ablesetag)
	if rs := items(e.must("MeterReading", "list", map[string]any{"query": map[string]any{"company_code": "1000", "object": "RentObject", "object_id": "WE01"}})); len(rs) == 0 {
		t.Fatalf("Stände zum Mietobjekt: %v", rs)
	} else if rs2 := items(e.must("MeterReading", "list", map[string]any{"query": map[string]any{"company_code": "1000", "object": "RentObject", "object_id": "WE99"}})); len(rs2) != 0 {
		t.Fatalf("Stände zu fremdem Mietobjekt: %v", rs2)
	}
	// Misst für gleich beim Anlegen des Messpunkts – Rolle aus dem Katalog (Kennzeichen Nutzer).
	np := e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Kaltwasser Gretchen", "medium": "WAERME", "valid_from": "2026-01-01",
		"measures_type": "RENTOBJ", "measures_id": "WE01"})
	if nl := items(e.must("TechLink", "list", map[string]any{"query": map[string]any{"company_code": "1000", "source_id": np["point_id"]}})); len(nl) != 1 ||
		nl[0]["role_code"] != "MEASURES" || str(nl[0]["valid_from"]) != "2026-01-01" {
		t.Fatalf("Misst für beim Anlegen: %v", nl)
	}
	_, err = e.call("MeterPoint", "create", map[string]any{"data": map[string]any{"company_code": "1000", "designation": "x", "medium": "WAERME", "measures_type": "RENTOBJ", "measures_id": "WE99"}})
	expect(t, err, sdk.ErrInvalidArgument, "Misst für unbekanntes Mietobjekt")

	where := e.must("MeterPoint", "where", map[string]any{"id": "1000|MP0001"})
	text := tableText(where)
	for _, want := range []string{"Auerbachs Keller › Hausanschlussraum", "Platz 2", "links neben dem Fass", "ZA0002 WMZ neu", "48227252",
		"Whg. Gretchen 1. OG (MV-1 Margarete (Gretchen))", "Fernwärme Walpurgis", "Stadtwerke Walpurgis"} {
		if !strings.Contains(text, want) {
			t.Errorf("Wo ist der Zähler? – %q fehlt in %s", want, text)
		}
	}
	tour := e.must("TechLocation", "tour", map[string]any{"id": "1000|LG0001"})
	if tour["count"] != 1 || !strings.Contains(tableText(tour), "MP0001") {
		t.Fatalf("Rundgang: %v", tour)
	}
	if got := items(e.must("MeterPoint", "list", map[string]any{"query": map[string]any{"company_code": "1000", "number": "4822"}})); len(got) != 1 {
		t.Fatalf("Messpunkt über Gerätenummer: %v", got)
	}

	// Versorgungsbaum: kein Kreis, ein übergeordneter je Zeitpunkt.
	e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Fernwärme Haus", "medium": "WAERME", "valid_from": "2018-01-01"})
	e.create("MeterPointRelation", map[string]any{"company_code": "1000", "parent_id": "MP0002", "child_id": "MP0001", "relation_type": "SUB", "valid_from": "2018-01-01"})
	_, err = e.call("MeterPointRelation", "create", map[string]any{"data": map[string]any{"company_code": "1000", "parent_id": "MP0001", "child_id": "MP0002", "relation_type": "SUB", "valid_from": "2018-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kreis im Versorgungsbaum")
}

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func tableText(m map[string]any) string {
	var b strings.Builder
	tbl, _ := m["table"].(map[string]any)
	rows, _ := tbl["rows"].([][]any)
	for _, r := range rows {
		for _, c := range r {
			b.WriteString(fmt.Sprint(c) + " | ")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TestCatalogDriven: Kürzel der IDs, Vorschlagsstatus, Ableseart beim Tausch und
// verknüpfbare Objects kommen aus den Katalogen.
func TestCatalogDriven(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.must("TechObjectSetup", "setupCompany", map[string]any{"company": "1000"})
	e.must("TechIdPrefix", "update", map[string]any{"id": "1000|POINT", "data": map[string]any{"id_prefix": "zp"}})
	if p := e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Strom Hexenküche", "medium": "STROM"}); p["point_id"] != "ZP0001" || p["status"] != "ACTIVE" {
		t.Fatalf("Kürzel/Status aus dem Katalog: %v", p)
	}
	_, err := e.call("TechIdPrefix", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "ZAUBER", "name": "x", "id_prefix": "ZB"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Object")
	_, err = e.call("TechObjectStatus", "update", map[string]any{"id": "1000|PLANNED", "data": map[string]any{"is_default": true}})
	expect(t, err, sdk.ErrInvalidArgument, "zwei Vorschläge")
	_, err = e.call("TechLinkSource", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "READ", "name": "Ablesung", "source_object": "MeterReading"}})
	expect(t, err, sdk.ErrInvalidArgument, "Object ohne eigene ID")
	// Ohne Vorschlag beim Tausch keine Annahme im Code.
	e.must("TechReadingType", "update", map[string]any{"id": "1000|INSTALL", "data": map[string]any{"replace_role": ""}})
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Strom", "medium": "STROM"})
	e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "neu", "valid_from": "2024-01-01"})
	_, err = e.call("MeterPoint", "replace", map[string]any{"id": "1000|ZP0001", "data": map[string]any{"rp_date": "2025-01-01", "rp_equipment": "ZA0001"}})
	expect(t, err, sdk.ErrInvalidArgument, "kein Vorschlag Einbau")
	// Auswahl im Formular aus den Katalogen.
	e.h.objects["RentObject|list"] = map[string]any{}
	st, err := e.m.linkFormState(e.ctx, metamodelReq(map[string]string{"company_code": "1000", "source_kind": "MeterPoint"}))
	if err != nil {
		t.Fatal(err)
	}
	if o := st.Fields["source_id"].Options; len(o) != 1 || o[0].Value != "ZP0001" || len(st.Fields["source_kind"].Options) != 3 {
		t.Fatalf("Auswahl: %v", st.Fields)
	}
}

func metamodelReq(values map[string]string) metamodel.FormStateRequest {
	return metamodel.FormStateRequest{Mode: "create", Values: values}
}

// TestSetupFillsNewColumns: Nach einem Release ergänzt setup-company leere
// Zusatzspalten vorhandener Vorschlagseinträge, ohne gesetzte zu überschreiben.
func TestSetupFillsNewColumns(t *testing.T) {
	e := setup(t)
	e.must("TechObjectSetup", "setupCompany", map[string]any{"company": "1000"})
	e.h.db.Exec("UPDATE techobj__status SET is_default = false")
	e.h.db.Exec("UPDATE techobj__link_role SET show_in_where = false")
	e.h.db.Exec("UPDATE techobj__link_object SET value_field = NULL WHERE code = 'CONTRACT'")
	e.h.db.Exec("UPDATE techobj__link_object SET value_field = 'eigene_id' WHERE code = 'BUILDING'")
	e.must("TechObjectSetup", "setupCompany", map[string]any{"company": "1000"})
	var def, contract, building string
	e.h.db.QueryRow("SELECT code FROM techobj__status WHERE is_default").Scan(&def)
	e.h.db.QueryRow("SELECT value_field FROM techobj__link_object WHERE code = 'CONTRACT'").Scan(&contract)
	e.h.db.QueryRow("SELECT value_field FROM techobj__link_object WHERE code = 'BUILDING'").Scan(&building)
	var where int
	e.h.db.QueryRow("SELECT COUNT(*) FROM techobj__link_role WHERE show_in_where").Scan(&where)
	if def != "ACTIVE" || contract != "contract_id" || building != "eigene_id" || where != 4 {
		t.Fatalf("ergänzt: %q %q %q %d", def, contract, building, where)
	}
}
