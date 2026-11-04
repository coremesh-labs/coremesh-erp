package inspection

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func str(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// fixtures: Gretchens Wohnung mit Raumbuch, Mietvertrag und Kaltwasserzähler.
func (e *env) fixtures() {
	h := e.h
	h.objects["RentObject|1000|WE01"] = map[string]any{"object_id": "WE01", "designation": "Whg. Gretchen 1. OG"}
	h.objects["Contract|1000|MV-1"] = map[string]any{"contract_id": "MV-1", "designation": "Mietvertrag Gretchen", "contract_type": "MV", "partner_id": "BP1"}
	h.objects["Contract|1000|VE-1"] = map[string]any{"contract_id": "VE-1", "designation": "Fernwärme", "contract_type": "VE", "partner_id": "BP2"}
	h.objects["BusinessPartner|BP1"] = map[string]any{"id": "BP1", "type": "PERSON", "name1": "Gretchen", "name2": "Margarete"}
	h.objects["BusinessPartner|BP3"] = map[string]any{"id": "BP3", "type": "ORGANIZATION", "name1": "Faust Hausverwaltung"}
	h.objects["CompanyCode|1000"] = map[string]any{"description": "Faust Verwaltung", "partner_id": "BP3"}
	h.lists["ContractObject"] = []map[string]any{
		{"company_code": "1000", "contract_id": "VE-1", "object_type": "RentObject", "object_id": "WE01", "valid_from": "2020-01-01", "valid_to": "9999-12-31"},
		{"company_code": "1000", "contract_id": "MV-1", "object_type": "RentObject", "object_id": "WE01", "valid_from": "2024-01-01", "valid_to": "2026-12-31"},
	}
	h.lists["Room"] = []map[string]any{
		{"company_code": "1000", "object_id": "WE01", "room_no": "02", "designation": "Küche", "sort_order": 20, "is_active": true},
		{"company_code": "1000", "object_id": "WE01", "room_no": "01", "designation": "Flur", "sort_order": 10, "is_active": true},
		{"company_code": "1000", "object_id": "WE01", "room_no": "03", "designation": "Abstellraum", "sort_order": 30, "is_active": true},
	}
	h.lists["RoomComponent"] = []map[string]any{
		{"company_code": "1000", "object_id": "WE01", "room_no": "01", "component_type": "FLOOR", "description": "Laminat Eiche"},
		{"company_code": "1000", "object_id": "WE01", "room_no": "01", "component_type": "DOOR", "description": "Wohnungstür", "quantity": 1},
		{"company_code": "1000", "object_id": "WE01", "room_no": "02", "component_type": "FLOOR", "description": "Fliesen"},
	}
	h.lists["ComponentType"] = []map[string]any{
		{"company_code": "1000", "code": "FLOOR", "name": "Boden"}, {"company_code": "1000", "code": "DOOR", "name": "Türen"},
	}
	h.points = []any{map[string]any{"point_id": "MP0001", "designation": "Kaltwasser Küche", "location": "Küche, unter der Spüle", "equipment_id": "ZA0001",
		"meter_number": "4711", "registers": []any{map[string]any{"register_code": "1", "designation": "Verbrauch", "unit": "m³", "last_read_on": "2025-12-31", "last_value": 123.5}}}}
}

// TestHandover: Auszug mit Vertrag aus dem Mietobjekt, Übernahme aus Raumbuch,
// Zählern und Vertrag, Erfassung, Formular, Abschluss mit Ablesung, DMS und Vorgang.
func TestHandover(t *testing.T) {
	e := setup(t)
	e.fixtures()
	p := e.create("InspectionProtocol", map[string]any{"company_code": "1000", "protocol_type": "move_out", "object_id": "WE01", "protocol_date": "2026-12-30", "place": "Leipzig"})
	if p["protocol_id"] != "UA-2026-0001" || p["contract_id"] != "MV-1" || p["partner_id"] != "BP1" || p["status"] != statusOpen ||
		p["title"] != "Wohnungsübergabe Auszug WE01 Whg. Gretchen 1. OG – Margarete Gretchen" {
		t.Fatalf("Protokoll: %v", p)
	}
	id := map[string]any{"id": "1000|UA-2026-0001"}
	r := e.must("InspectionProtocol", "prepare", id)
	if !strings.Contains(str(r["message"]), "4 Räume/Bauteile, 1 Zählwerke, 2 Teilnehmer und 9 Prüfpunkte") {
		t.Fatalf("Übernahme: %v", r)
	}
	// zweiter Lauf übernimmt nichts doppelt
	if r := e.must("InspectionProtocol", "prepare", id); !strings.Contains(str(r["message"]), "0 Räume/Bauteile, 0 Zählwerke, 0 Teilnehmer und 0 Prüfpunkte") {
		t.Fatalf("zweite Übernahme: %v", r)
	}
	items := items(e.must("InspectionItem", "list", map[string]any{"company_code": "1000", "protocol_id": "UA-2026-0001"}))
	if len(items) != 4 || items[0]["room_name"] != "Flur" || items[0]["component_name"] != "Boden" || items[1]["description"] != "1 × Wohnungstür" || items[3]["room_name"] != "Abstellraum" {
		t.Fatalf("Räume: %v", items)
	}

	// Formular ohne Stände
	e.must("InspectionProtocol", "print", map[string]any{"id": "1000|UA-2026-0001", "data": map[string]any{"pr_mode": "BLANK"}})
	if d := e.h.rendered[0]["data"].(map[string]any); d["blank"] != true || len(d["signers"].([]any)) != 2 {
		t.Fatalf("Formular: %v", d)
	}
	// Abschluss ohne Zählerstand geht nicht
	_, err := e.call("InspectionProtocol", "print", map[string]any{"id": "1000|UA-2026-0001", "data": map[string]any{"pr_mode": "FINAL"}})
	expect(t, err, sdk.ErrInvalidArgument, "Abschluss ohne Stand")

	_, err = e.call("InspectionItem", "update", map[string]any{"id": "1000|UA-2026-0001|2", "data": map[string]any{"condition": "KAPUTT"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Zustand")
	upd := e.must("InspectionItem", "update", map[string]any{"id": "1000|UA-2026-0001|2", "data": map[string]any{"condition": "defect", "remark": "Schloss klemmt"}})
	if upd["is_defect"] != true {
		t.Fatalf("Mangel: %v", upd)
	}
	e.must("InspectionMeter", "update", map[string]any{"id": "1000|UA-2026-0001|MP0001|1", "data": map[string]any{"value": 140.25}})
	e.must("InspectionCheck", "update", map[string]any{"id": "1000|UA-2026-0001|KEY_FLAT", "data": map[string]any{"value": "3"}})
	_, err = e.call("InspectionCheck", "update", map[string]any{"id": "1000|UA-2026-0001|CLEAN", "data": map[string]any{"value": "vielleicht"}})
	expect(t, err, sdk.ErrInvalidArgument, "ja/nein")

	r = e.must("InspectionProtocol", "print", map[string]any{"id": "1000|UA-2026-0001", "data": map[string]any{"pr_mode": "FINAL"}})
	msg := str(r["message"])
	for _, want := range []string{"1 Zählerstände als Ablesung gebucht", "im DMS abgelegt als 1000-BP-0001", "1 Mängel als Vorgang SM-2026-0001"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Abschluss ohne %q: %s", want, msg)
		}
	}
	if rd := e.h.readings[0]; rd["reading_type"] != "HANDOVER" || rd["read_on"] != "2026-12-30" || toFloat(rd["value"]) != 140.25 || rd["equipment_id"] != "ZA0001" {
		t.Fatalf("Ablesung: %v", rd)
	}
	if st := e.h.stored[0]; st["doc_type"] != "REPORT" || st["partner_id"] != "BP1" || len(st["refs"].([]any)) != 3 {
		t.Fatalf("DMS: %v", st)
	}
	if pr := e.h.processes[0]; pr["process_type"] != "SCHADEN" || !strings.Contains(str(pr["description"]), "Flur, Türen: beschädigt / defekt – Schloss klemmt") || pr["object_id"] != "WE01" {
		t.Fatalf("Vorgang: %v", pr)
	}
	d := e.h.rendered[len(e.h.rendered)-1]["data"].(map[string]any)
	if d["format"] != sheetFormat || len(d["defects"].([]any)) != 1 || d["meters"].([]any)[0].(map[string]any)["value"] != "140,25" {
		t.Fatalf("Druckdaten: %v", d)
	}
	got := e.must("InspectionProtocol", "get", id)
	if got["status"] != statusDone || got["document"] != "1000-BP-0001" || got["process_id"] != "SM-2026-0001" {
		t.Fatalf("abgeschlossen: %v", got)
	}
	// abgeschlossen: nichts mehr änderbar
	_, err = e.call("InspectionMeter", "update", map[string]any{"id": "1000|UA-2026-0001|MP0001|1", "data": map[string]any{"value": 150}})
	expect(t, err, sdk.ErrInvalidArgument, "abgeschlossen")
	_, err = e.call("InspectionProtocol", "update", map[string]any{"id": "1000|UA-2026-0001", "data": map[string]any{"note": "nachträglich"}})
	expect(t, err, sdk.ErrInvalidArgument, "abgeschlossen")

	// Begehung ohne Vertrag und Zähler; Vertrag falscher Art wird abgelehnt
	_, err = e.call("InspectionProtocol", "create", map[string]any{"data": map[string]any{"company_code": "1000", "protocol_type": "MOVE_IN", "object_id": "WE01", "contract_id": "VE-1"}})
	expect(t, err, sdk.ErrInvalidArgument, "Vertragsart")
	b := e.create("InspectionProtocol", map[string]any{"company_code": "1000", "protocol_type": "INSPECTION", "object_id": "WE01", "protocol_date": "2026-05-01"})
	if b["protocol_id"] != "BG-2026-0001" || b["contract_id"] != nil {
		t.Fatalf("Begehung: %v", b)
	}
	if r := e.must("InspectionProtocol", "prepare", map[string]any{"id": "1000|BG-2026-0001"}); !strings.Contains(str(r["message"]), "0 Zählwerke") {
		t.Fatalf("Begehung übernehmen: %v", r)
	}
}
