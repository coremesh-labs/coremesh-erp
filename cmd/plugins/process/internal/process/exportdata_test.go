package process

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExportData: Vorgang als Objektdaten process/1 (Vorgang_Objektdaten_Konzept.txt) –
// Umfang Mieter (Namen, öffentlicher Verlauf, Teilvorgänge kurz), „ich“, Dokumente aus
// dem Index, keine leeren Felder, gleiche Bytes ohne Änderung, neuer Stand nach einem Eintrag.
func TestExportData(t *testing.T) {
	e := setup(t)
	e.at("2026-11-03")
	e.h.partners["BP-GRETCHEN"] = "Margarete (Gretchen)"
	e.h.partners["BP-VALENTIN"] = "Valentin Sanitär"
	e.create(processObject, map[string]any{"company_code": "1000", "process_type": "SCHADEN", "subject": "Wasserschaden Küche Whg 16",
		"partner_id": "BP-GRETCHEN", "description": "Wasser **unter** der Spüle"})
	e.create(partnerObject, map[string]any{"company_code": "1000", "process_id": "SM-2026-0001", "role_code": "TENANT", "partner_id": "BP-GRETCHEN"})
	e.create(partnerObject, map[string]any{"company_code": "1000", "process_id": "SM-2026-0001", "role_code": "CRAFTSMAN", "partner_id": "BP-VALENTIN"})
	e.create(processObject, map[string]any{"company_code": "1000", "process_type": "ANFRAGE", "subject": "Angebot Trocknung",
		"parent_id": "SM-2026-0001", "follow_up": "2026-11-10"})
	e.at("2026-11-04")
	e.must(stepObject, "done", map[string]any{"id": "1000|SM-2026-0001|10"})
	e.must(processObject, "note", map[string]any{"id": "1000|SM-2026-0001", "data": map[string]any{"entry_kind": "CALL", "entry_text": "Gretchen: Nachbar war schuld"}})
	e.must(processObject, "note", map[string]any{"id": "1000|SM-2026-0001", "data": map[string]any{"entry_kind": "MAIL", "entry_text": "Termin Gutachter bestätigt"}})

	mieter := map[string]any{"code": "MIETER", "name": "Mieter", "beschreibung": true, "zustaendig": true, "beteiligte": "NAMEN", "schritte": true,
		"texte": true, "verlauf": "OEFFENTLICH", "dokumente": true, "teilvorgaenge": "KURZ"}
	in := map[string]any{"id": "1000|SM-2026-0001", "umfang": mieter, "partner": "BP-GRETCHEN",
		"dokumente": []any{map[string]any{"doc_id": "D1", "ref": "o/abc", "titel": "Foto Küche", "datum": "2026-11-04", "content_type": "image/jpeg"}}}
	out := e.must(processObject, "exportData", in)
	if out["content_type"] != processContentType || out["name"] != "vorgang.json" || out["stand"] != "2026-11-04T09:00:00Z" || out["partner"] != "BP-GRETCHEN" {
		t.Fatalf("Kopf: %v", out)
	}
	raw, _ := json.Marshal(out["data"])
	if strings.Contains(string(raw), `:""`) || strings.Contains(string(raw), ":null") || strings.Contains(string(raw), ":[]") || strings.Contains(string(raw), "_partner") {
		t.Fatalf("leere Felder: %s", raw)
	}
	var d struct {
		Schema, Stand string
		Umfang        struct {
			Code  string
			Teile []string
		}
		Vorgang struct {
			Nummer, Ampel  string
			BeschreibungMD string `json:"beschreibung_md"`
			Status         struct{ Klasse string }
			Zustaendig     struct{ Name string }
			Beteiligte     []struct {
				Rolle   struct{ Code string }
				Partner struct{ ID, Name string }
				Ich     bool
				Kontakt any
			}
			Schritte []struct {
				Nr               int
				Status, Erledigt string
				Pflicht          bool
			}
			Verlauf []struct {
				Nr   int
				Text string
				Art  struct {
					Code   string
					System bool
				}
				Von *struct{ Name string }
			}
			Dokumente     []struct{ Ref, Titel string }
			Teilvorgaenge []map[string]any
			Bezuege       []struct{ Art, ID, Name string }
		}
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	v := d.Vorgang
	if d.Schema != "process/1" || d.Umfang.Code != "MIETER" || strings.Join(d.Umfang.Teile, ",") != "beschreibung,zustaendig,beteiligte,schritte,texte,verlauf,dokumente,teilvorgaenge" {
		t.Fatalf("Abbild: %s", raw)
	}
	if v.Nummer != "SM-2026-0001" || v.Status.Klasse != "OFFEN" || v.Zustaendig.Name != "mephisto" || v.BeschreibungMD != "Wasser **unter** der Spüle" {
		t.Fatalf("Vorgang: %s", raw)
	}
	if len(v.Beteiligte) != 2 || v.Beteiligte[1].Partner.Name != "Margarete (Gretchen)" || !v.Beteiligte[1].Ich || v.Beteiligte[0].Ich || v.Beteiligte[0].Kontakt != nil {
		t.Fatalf("Beteiligte: %+v", v.Beteiligte)
	}
	if len(v.Schritte) != 6 || v.Schritte[0].Status != "ERLEDIGT" || v.Schritte[0].Erledigt != "2026-11-04" || v.Schritte[1].Status != "OFFEN" || v.Schritte[5].Pflicht {
		t.Fatalf("Schritte: %+v", v.Schritte)
	}
	var texts []string
	for _, h := range v.Verlauf {
		texts = append(texts, h.Art.Code+":"+h.Text)
		if h.Art.Code == "CALL" || (h.Art.System && h.Von != nil) {
			t.Fatalf("Verlauf: %+v", v.Verlauf)
		}
	}
	if len(v.Verlauf) < 2 || !strings.Contains(strings.Join(texts, "|"), "MAIL:Termin Gutachter bestätigt") || !strings.Contains(strings.Join(texts, "|"), "SYSTEM:") {
		t.Fatalf("Verlauf (öffentlich, mit Systemeinträgen): %v", texts)
	}
	if len(v.Dokumente) != 1 || v.Dokumente[0].Ref != "o/abc" || len(v.Bezuege) != 1 || v.Bezuege[0].Art != "PARTNER" || v.Bezuege[0].Name != "Margarete (Gretchen)" {
		t.Fatalf("Dokumente/Bezüge: %+v %+v", v.Dokumente, v.Bezuege)
	}
	if len(v.Teilvorgaenge) != 1 || v.Teilvorgaenge[0]["nummer"] != "AF-2026-0001" || v.Teilvorgaenge[0]["schritte"] != nil || v.Teilvorgaenge[0]["ampel"] == nil {
		t.Fatalf("Teilvorgänge kurz: %v", v.Teilvorgaenge)
	}
	// ohne Änderung: gleiche Bytes, auch an einem späteren Tag
	e.at("2026-11-20")
	again, _ := json.Marshal(e.must(processObject, "exportData", in)["data"])
	if string(again) != string(raw) {
		t.Fatalf("nicht deterministisch:\n%s\n%s", raw, again)
	}
	// knapper Umfang: nur Kopf und Daten
	small, _ := json.Marshal(e.must(processObject, "exportData", map[string]any{"id": "1000|SM-2026-0001", "umfang": map[string]any{"code": "KURZ"}})["data"])
	for _, k := range []string{"beteiligte", "schritte", "verlauf", "dokumente", "teilvorgaenge", "beschreibung_md", "zustaendig", "\"ich\""} {
		if strings.Contains(string(small), k) {
			t.Fatalf("knapper Umfang mit %s: %s", k, small)
		}
	}
	// ein Eintrag ⇒ neuer Stand
	e.must(processObject, "note", map[string]any{"id": "1000|SM-2026-0001", "data": map[string]any{"entry_kind": "NOTE", "entry_text": "intern"}})
	if out := e.must(processObject, "exportData", in); out["stand"] != "2026-11-20T09:00:00Z" {
		t.Fatalf("neuer Stand: %v", out["stand"])
	}
	// Beteiligte für die Bezugsregel BETEILIGTE
	p := e.must(processObject, "participants", map[string]any{"id": "1000|SM-2026-0001"})
	if p["partner_id"] != "BP-GRETCHEN" || len(p["items"].([]any)) != 2 {
		t.Fatalf("participants: %v", p)
	}
	if _, err := e.call(processObject, "exportData", map[string]any{"id": "1000|XX-2026-0001"}); err == nil {
		t.Fatal("unbekannter Vorgang")
	}
}
