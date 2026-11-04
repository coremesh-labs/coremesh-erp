package techobj

import (
	"strings"
	"testing"
)

// TestNoticeAndConsumption: Ablesemitteilung an den Lieferanten (alle Messpunkte
// des Vertrags in einem Brief, Zählernummer und Zählpunkt) und Verbrauchsauswertung
// mit Unterzähler und auffälligem Zeitraum.
func TestNoticeAndConsumption(t *testing.T) {
	e := setup(t)
	e.fixtures()
	e.h.objects["Contract|1000|VE-1"]["external_number"] = "231000264298"
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Wasserzähler", "medium": "KW"})
	e.create("TechObjectType", map[string]any{"company_code": "1000", "object_class": "ZAEHLER", "designation": "Stromzähler", "medium": "STROM"})
	for _, d := range []map[string]any{
		{"type_id": "GT0001", "designation": "Wasser alt"}, {"type_id": "GT0001", "designation": "Wasser neu"},
		{"type_id": "GT0002", "designation": "Hausstrom"}, {"type_id": "GT0002", "designation": "Strom Heizung"},
	} {
		d["company_code"], d["valid_from"] = "1000", "2020-01-01"
		e.create("TechObject", d)
	}
	for eq, n := range map[string]string{"ZA0001": "19298716", "ZA0002": "11152994", "ZA0003": "1ITR0054902739", "ZA0004": "7858829"} {
		e.create("TechObjectNumber", map[string]any{"company_code": "1000", "equipment_id": eq, "number_type": "METER", "number": n})
	}
	for _, p := range []map[string]any{
		{"designation": "Wasser", "medium": "KW"}, {"designation": "Strom", "medium": "STROM", "market_location": "DE0001234567890000000000000012345"},
		{"designation": "Strom Heizung", "medium": "STROM"},
	} {
		p["company_code"], p["valid_from"] = "1000", "2020-01-01"
		e.create("MeterPoint", p)
	}
	inst := func(p, eq, from, to string) {
		d := map[string]any{"company_code": "1000", "point_id": p, "equipment_id": eq, "valid_from": from}
		if to != "" {
			d["valid_to"] = to
		}
		e.create("MeterInstallation", d)
	}
	inst("MP0001", "ZA0001", "2020-01-01", "2025-10-07")
	inst("MP0001", "ZA0002", "2025-10-08", "")
	inst("MP0002", "ZA0003", "2020-01-01", "")
	inst("MP0003", "ZA0004", "2020-01-01", "")
	e.create("MeterPointRelation", map[string]any{"company_code": "1000", "parent_id": "MP0002", "child_id": "MP0003", "relation_type": "SUB", "valid_from": "2020-01-01"})
	for _, p := range []string{"MP0001", "MP0002"} {
		e.create("TechLink", map[string]any{"company_code": "1000", "source_kind": "MeterPoint", "source_id": p, "role_code": "SUPPLIER", "object_type": "CONTRACT", "object_id": "VE-1", "valid_from": "2020-01-01"})
	}
	read := func(eq, day, typ string, v float64) {
		e.create("MeterReading", map[string]any{"company_code": "1000", "equipment_id": eq, "register_code": "1", "read_on": day, "reading_type": typ, "value": v})
	}
	read("ZA0001", "2021-12-31", "HISTORIC", 1110)
	read("ZA0001", "2022-12-31", "HISTORIC", 1640)
	read("ZA0001", "2023-12-31", "HISTORIC", 2180)
	read("ZA0001", "2024-12-31", "HISTORIC", 2709)
	read("ZA0001", "2025-10-08", "REMOVE", 3096)
	read("ZA0002", "2025-10-08", "INSTALL", 1)
	read("ZA0002", "2025-12-31", "HISTORIC", 314.249) // auffällig: 313 m³ in 84 Tagen
	read("ZA0003", "2024-12-31", "HISTORIC", 10512)
	read("ZA0003", "2025-12-31", "HISTORIC", 12128.75)
	read("ZA0004", "2024-12-31", "HISTORIC", 7285)
	read("ZA0004", "2025-12-31", "HISTORIC", 8524.1)

	// Vorschau: ein Brief für beide Messpunkte des Vertrags, nichts vermerkt
	r := e.must("MeterPoint", "notice", map[string]any{"id": "1000|MP0001", "data": map[string]any{"nt_date": "2025-12-31"}})
	if !strings.HasPrefix(str(r["message"]), "Vorschau") || len(e.h.rendered) != 1 || len(e.h.stored) != 0 {
		t.Fatalf("Vorschau: %v", r)
	}
	data := e.h.rendered[0]["data"].(map[string]any)
	text := str(data["text"])
	for _, want := range []string{"Zähler 11152994: 314,249 m³", "Strom (Zählpunkt/Netznummer DE0001234567890000000000000012345)", "Zähler 1ITR0054902739: 12128,8 kWh", "vom 31.12.2025"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Text ohne %q:\n%s", want, text)
		}
	}
	if s := str(data["subject"]); s != "Zählerstandsmitteilung zum 31.12.2025 – Kundennummer 231000264298" {
		t.Fatalf("Betreff: %q", s)
	}
	if rc := data["recipient"].(map[string]any); rc["name"] != "Stadtwerke Walpurgis" {
		t.Fatalf("Empfänger: %v", rc)
	}
	// Tauschtag: Endstand alt und Anfangsstand neu mit Ableseart
	e.must("MeterPoint", "notice", map[string]any{"id": "1000|MP0001", "data": map[string]any{"nt_date": "2025-10-08"}})
	text = str(e.h.rendered[1]["data"].(map[string]any)["text"])
	if !strings.Contains(text, "Zähler 19298716: 3096,000 m³ (Ausbau)") || !strings.Contains(text, "Zähler 11152994: 1,000 m³ (Einbau)") {
		t.Fatalf("Tauschtag:\n%s", text)
	}
	// Endgültig: im DMS mit Bezügen, an den Ablesungen vermerkt
	r = e.must("MeterPoint", "notice", map[string]any{"id": "1000|MP0002", "data": map[string]any{"nt_date": "2025-12-31", "nt_final": true}})
	if len(e.h.stored) != 1 || e.h.stored[0]["doc_type"] != "LETTER" || len(e.h.stored[0]["refs"].([]any)) != 3 {
		t.Fatalf("DMS: %v", e.h.stored)
	}
	got := e.must("MeterReading", "get", map[string]any{"id": "1000|ZA0003|1|2025-12-31|HISTORIC"})
	if got["reported_to"] != "Stadtwerke Walpurgis, 231000264298" || got["report_document"] != "1000-SV-0001" || str(got["reported_on"]) == "" {
		t.Fatalf("vermerkt: %v", got)
	}
	_, err := e.call("MeterPoint", "notice", map[string]any{"id": "1000|MP0003", "data": map[string]any{"nt_date": "2025-12-31"}})
	if err == nil || !strings.Contains(err.Error(), "keine Verknüpfung") {
		t.Fatalf("ohne Lieferant: %v", err)
	}

	// Verbrauch: Wasser über den Tausch hinweg, 2025 auffällig
	c := e.must("MeterPoint", "consumption", map[string]any{"id": "1000|MP0001"})
	tab := tableText(c)
	if !strings.Contains(tab, "2025") || !strings.Contains(tab, "auffällig") {
		t.Fatalf("Wasser:\n%s", tab)
	}
	// Strom mit Unterzähler: Rest = 1616,75 − 1239,1
	c = e.must("MeterPoint", "consumption", map[string]any{"id": "1000|MP0002"})
	tab = tableText(c)
	if !strings.Contains(tab, "1239,1 kWh") || !strings.Contains(tab, "377,6 kWh") && !strings.Contains(tab, "377,7 kWh") {
		t.Fatalf("Strom mit Unterzähler:\n%s", tab)
	}
}
