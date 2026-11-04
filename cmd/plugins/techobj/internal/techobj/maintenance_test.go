package techobj

import (
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

	res := e.must(setupObject, "pointsFor", map[string]any{"company_code": "1000", "object": "RentObject", "id": "WE01"})
	its := items(res)
	if len(its) != 1 || its[0]["point_id"] != "MP0001" || its[0]["equipment_id"] != "ZA0001" || !strings.Contains(str(its[0]["meter_number"]), "4711") {
		t.Fatalf("pointsFor: %v", res)
	}
	var regs []map[string]any
	_ = sdk.Decode(its[0]["registers"], &regs)
	if len(regs) != 1 || toFloat(regs[0]["last_value"]) != 123.5 {
		t.Fatalf("Zählwerke: %v", regs)
	}
}
