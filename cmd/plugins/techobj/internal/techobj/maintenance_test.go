package techobj

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestMaintenanceOverviewPointsFor: Wartungsnachweis mit nächster Fälligkeit aus
// dem Katalog, Vorgang „Wartung beauftragen“, Übersicht der Fälligkeiten und der
// Dienst für das Übergabeprotokoll (Messpunkte eines Mietobjekts).
func TestMaintenanceOverviewPointsFor(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.create("TechObject", map[string]any{"company_code": "1000", "object_class": "HEIZUNG", "designation": "Gastherme Hexenküche", "valid_from": "2020-01-01"})
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Wasserzähler", "medium": "KW"})
	e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "Wasserzähler", "valid_from": "2020-01-01", "calibration_year": 2019})
	e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "number_type": "METER", "number": "4711"})

	// Wartungsart passt nicht zur Objektklasse
	_, err := e.call("TechMaintenance", "create", map[string]any{"data": map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "maintenance_type": "HZ_WARTUNG", "done_on": "2025-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Wartungsart für falsche Klasse")

	last := time.Now().AddDate(-1, 0, 10).Format("2006-01-02")
	w := e.create("TechMaintenance", map[string]any{"company_code": "1000", "equipment_id": "HZ0001", "maintenance_type": "hz_wartung", "done_on": last, "performed_by": "Mephisto Heizungsbau", "result": "ohne Mängel"})
	if want := parseDay(last).AddDate(1, 0, 0).Format("2006-01-02"); date(w["next_due"]) != want {
		t.Fatalf("nächste Fälligkeit: %v, erwartet %s", w["next_due"], want)
	}
	got := e.must("TechObject", "get", map[string]any{"id": "1000|HZ0001"})
	if s := str(got["maintenance_due"]); !strings.Contains(s, "Wartung Heizungsanlage: fällig") || !strings.Contains(s, "Schornsteinfeger / Abgasmessung (Pflicht): noch nie erfasst") {
		t.Fatalf("Fälligkeiten am Gerät: %q", s)
	}

	// Vorgang beauftragen: ohne Art die nächste fällige (Abgas, nie erfasst = heute)
	r := e.must("TechObject", "orderMaintenance", map[string]any{"id": "1000|HZ0001", "data": map[string]any{"mo_note": "Termin mit Gretchen abstimmen"}})
	if len(e.h.processes) != 1 || e.h.processes[0]["process_type"] != "WARTUNG" || !strings.HasPrefix(str(e.h.processes[0]["subject"]), "Schornsteinfeger") ||
		!strings.Contains(str(e.h.processes[0]["description"]), "Termin mit Gretchen") || !strings.Contains(str(r["message"]), "WA-2026-0001") {
		t.Fatalf("Vorgang: %v %v", r, e.h.processes)
	}

	// Messpunkt am Mietobjekt, ohne Ablesung
	e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Kaltwasser Gretchen", "medium": "KW", "valid_from": "2020-01-01"})
	e.create("MeterInstallation", map[string]any{"company_code": "1000", "point_id": "MP0001", "equipment_id": "ZA0001", "valid_from": "2020-01-01"})
	e.create("TechLink", map[string]any{"company_code": "1000", "source_kind": "MeterPoint", "source_id": "MP0001", "role_code": "MEASURES", "object_type": "RENTOBJ", "object_id": "WE01", "valid_from": "2020-01-01"})
	e.create("MeterReading", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "register_code": "1", "read_on": "2025-12-31", "reading_type": "HISTORIC", "value": 123.5})

	ov := e.must("TechObject", "overview", map[string]any{"data": map[string]any{"ov_company": "1000", "ov_days": 400}})
	var tab struct{ Rows [][]any }
	_ = sdk.Decode(ov["table"], &tab)
	var kinds []string
	for _, row := range tab.Rows {
		kinds = append(kinds, str(row[1]))
	}
	all := strings.Join(kinds, ",")
	for _, want := range []string{"Wartung", "Eichfrist", "Ablesung"} {
		if !strings.Contains(all, want) {
			t.Fatalf("Übersicht ohne %s: %v", want, tab.Rows)
		}
	}

	// Mitteilung: nur für Stände, deren Ableseart eine Mitteilung braucht (nicht HISTORIC)
	if strings.Contains(all, "Mitteilung") {
		t.Fatalf("historischer Stand als offene Mitteilung: %v", tab.Rows)
	}
	e.create("TechLink", map[string]any{"company_code": "1000", "source_kind": "MeterPoint", "source_id": "MP0001", "role_code": "SUPPLIER", "object_type": "CONTRACT", "object_id": "VE-1", "valid_from": "2020-01-01"})
	e.create("MeterReading", map[string]any{"company_code": "1000", "equipment_id": "ZA0001", "register_code": "1", "read_on": time.Now().AddDate(0, 0, -5).Format("2006-01-02"), "reading_type": "PERIODIC", "value": 130})
	ov = e.must("TechObject", "overview", map[string]any{"data": map[string]any{"ov_company": "1000", "ov_days": 30}})
	_ = sdk.Decode(ov["table"], &tab)
	found := false
	for _, row := range tab.Rows {
		found = found || str(row[1]) == "Mitteilung"
	}
	if !found {
		t.Fatalf("offene Mitteilung fehlt: %v", tab.Rows)
	}

	res := e.must(setupObject, "pointsFor", map[string]any{"company_code": "1000", "object": "RentObject", "id": "WE01"})
	its := items(res)
	if len(its) != 1 || its[0]["point_id"] != "MP0001" || its[0]["equipment_id"] != "ZA0001" || !strings.Contains(str(its[0]["meter_number"]), "4711") {
		t.Fatalf("pointsFor: %v", res)
	}
	var regs []map[string]any
	_ = sdk.Decode(its[0]["registers"], &regs)
	if len(regs) != 1 || toFloat(regs[0]["last_value"]) != 130 {
		t.Fatalf("Zählwerke: %v", regs)
	}
}

// TestUsageFor: Verbrauch je Sparte im Zeitraum mit Bewertungsfaktor, tagesgenau;
// Zahl der Messpunkte (für die Heizkostenabrechnung).
func TestUsageFor(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Wasserzähler", "medium": "KW"})
	for i, v := range [][3]float64{{10, 70, 1}, {0, 32, 3.409}} { // Kaltwasser, zweiter mit Faktor
		eq := fmt.Sprintf("ZA%04d", i+1)
		mp := fmt.Sprintf("MP%04d", i+1)
		e.create("TechObject", map[string]any{"company_code": "1000", "type_id": "GT0001", "designation": "Zähler", "valid_from": "2020-01-01"})
		if v[2] != 1 {
			e.must("TechObjectRegister", "update", map[string]any{"id": "1000|" + eq + "|1", "data": map[string]any{"factor": v[2]}})
		}
		e.create("MeterPoint", map[string]any{"company_code": "1000", "designation": "Kaltwasser Gretchen", "medium": "KW", "valid_from": "2020-01-01"})
		e.create("MeterInstallation", map[string]any{"company_code": "1000", "point_id": mp, "equipment_id": eq, "valid_from": "2020-01-01"})
		e.create("TechLink", map[string]any{"company_code": "1000", "source_kind": "MeterPoint", "source_id": mp, "role_code": "MEASURES", "object_type": "RENTOBJ", "object_id": "WE01", "valid_from": "2020-01-01"})
		for _, r := range [][2]any{{"2024-12-31", v[0]}, {"2025-12-31", v[1]}} {
			e.create("MeterReading", map[string]any{"company_code": "1000", "equipment_id": eq, "register_code": "1", "read_on": r[0], "reading_type": "HISTORIC", "value": r[1]})
		}
	}
	res := e.must(setupObject, "usageFor", map[string]any{"company_code": "1000", "object": "RentObject", "id": "WE01", "from": "2025-01-01", "to": "2025-12-31"})
	its := items(res)
	if len(its) != 1 || its[0]["medium"] != "KW" || its[0]["points"] != 2 || math.Abs(toFloat(its[0]["consumption"])-(60+32*3.409)) > 1e-9 ||
		its[0]["covered_days"] != 365 || its[0]["period_days"] != 365 {
		t.Fatalf("usageFor: %v", its)
	}
	half := items(e.must(setupObject, "usageFor", map[string]any{"company_code": "1000", "object": "RentObject", "id": "WE01", "from": "2026-01-01", "to": "2026-06-30"}))
	if len(half) != 1 || toFloat(half[0]["consumption"]) != 0 || half[0]["covered_days"] != 0 {
		t.Fatalf("ohne Stände: %v", half)
	}
}
