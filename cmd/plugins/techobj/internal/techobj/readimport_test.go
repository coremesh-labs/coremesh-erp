package techobj

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestImportReadings: Monatswerte des Messdienstes für den Heizkostenverteiler in Gretchens Stube.
func TestImportReadings(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Heizkostenverteiler", "medium": "WW"})
	e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "HKV Gretchen", "valid_from": "2020-01-01"})
	e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "number_type": "PROVIDER", "number": "0047110"})
	e.create("MeterReading", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "register_code": "1", "read_on": "2024-12-31", "reading_type": "PERIODIC", "value": 10})
	file := func(s string) map[string]any {
		return map[string]any{"import_file": base64.StdEncoding.EncodeToString([]byte(s)), "file_name": "walpurgis.csv"}
	}
	// eine Zeile je Stand, Latin-1, Dezimalkomma; unbekanntes Gerät, Zahlfehler
	long := "Ger\xe4t;Datum;Stand\n47110;31.01.2025;12,5\n47110;28.02.2025;20\n9999;28.02.2025;1\n47110;31.03.2025;x\n"
	d := file(long)
	d["import_check"] = true
	r := e.must("TechImportProfile", "readFile", map[string]any{"id": "1000|STANDARD", "data": d})
	if r["created"].(int) != 2 || r["unknown"].(int) != 1 || r["failed"].(int) != 1 {
		t.Fatalf("Prüflauf: %v", r["message"])
	}
	if items(e.must("MeterReading", "list", map[string]any{"query": map[string]any{"reading_type": "MONTHLY"}})) != nil {
		t.Fatal("Prüflauf hat gespeichert")
	}
	r = e.must("TechImportProfile", "readFile", map[string]any{"id": "1000|STANDARD", "data": file(long)})
	if r["created"].(int) != 2 || !strings.Contains(r["message"].(string), "Nummer 9999 nicht gefunden") {
		t.Fatalf("Einlesen: %v", r["message"])
	}
	got := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0001|1|2025-01-31|MONTHLY"})
	if toFloat(got["value"]) != 12.5 || got["source"] != "walpurgis.csv" || got["reader"] != "Messdienst (Fernablesung)" {
		t.Fatalf("Ablesung: %v", got)
	}
	// Monatstabelle: Februar schon da (gleich), März neu, April fällt (Rücklauf → Fehler)
	wide := "Nummer;02.2025;03.2025;04.2025\n0047110;20;31,25;5\n"
	r = e.must("TechImportProfile", "readFile", map[string]any{"id": "1000|MONATE", "data": file(wide)})
	if r["created"].(int) != 1 || r["same"].(int) != 1 || r["failed"].(int) != 1 {
		t.Fatalf("Monatstabelle: %v", r["message"])
	}
	// Verbrauch statt Stand: zum vorigen Stand addiert
	e.must("TechImportProfile", "update", map[string]any{"id": "1000|STANDARD", "data": map[string]any{"value_kind": "CONSUMPTION"}})
	r = e.must("TechImportProfile", "readFile", map[string]any{"id": "1000|STANDARD", "data": file("Gerät;Datum;Verbrauch\n47110;30.04.2025;2\n47110;31.05.2025;1,5\n")})
	if r["created"].(int) != 2 {
		t.Fatalf("Verbrauch: %v", r["message"])
	}
	if got := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0001|1|2025-05-31|MONTHLY"}); toFloat(got["value"]) != 34.75 {
		t.Fatalf("Stand aus Verbrauch: %v", got["value"])
	}
	if _, err := e.call("TechImportProfile", "update", map[string]any{"id": "1000|STANDARD", "data": map[string]any{"date_format": "TT.JJJJ"}}); err == nil {
		t.Fatal("Datumsformat ohne Monat")
	}
}
