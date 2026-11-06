package export

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func (e *env) at(day string) {
	t, _ := time.Parse(time.RFC3339, day+"T08:00:00Z")
	e.m.now = func() time.Time { return t }
}

func (e *env) dataset(id string) map[string]any {
	e.t.Helper()
	return e.must(datasetObject, "get", map[string]any{"id": "1000|" + id})
}

func (e *env) runItems(run string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, it := range items(e.must(itemObject, "list", map[string]any{"query": map[string]any{"run_id": run}})) {
		out[it["document_id"].(string)] = it
	}
	return out
}

func zipFiles(t *testing.T, resp sdk.Response) map[string]string {
	t.Helper()
	dl := resp.Payload.(map[string]any)["download"].(metamodel.Content)
	raw, _ := base64.StdEncoding.DecodeString(dl.Content)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		files[f.Name] = string(b)
	}
	return files
}

// Steuerbüro: eine Sicht ohne Zeitraum, je Jahr ein Dataset; das Dataset wächst über
// mehrere Stände, Stornos und Löschungen im DMS werden gekennzeichnet, nichts geht verloren.
func TestSteuerDataset(t *testing.T) {
	e := faust(t)
	e.h.refs["D1"]["SupplierInvoice|1000|ER-2025-0001"] = true
	e.at("2026-01-10")
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "STB", "name": "Steuerbüro", "view_type": "STEUERBUERO"})
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	if r["created"].(int) != 1 || r["items"].(int) != 4 {
		t.Fatalf("erster Stand (2025 ab Vorjahr; 2024 nicht): %v", r)
	}
	ds := e.dataset("DS-2026-0001")
	if ds["name"] != "Steuerunterlagen 2025" || ds["key_value"] != "2025" || ds["rhythm"] != rhQuarterly || ds["next_due"] != "2026-04-01" {
		t.Fatalf("Dataset: %v", ds)
	}
	// liefern
	if _, err := e.p.Handle(e.ctx, sdk.Request{Object: runObject, Action: "export", Payload: map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"export_target": "ZIP"}}}); err != nil {
		t.Fatal(err)
	}
	// nichts Neues: kein neuer Stand
	r = e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	if r["run_id"] != "" {
		t.Fatalf("ohne Änderung ein Stand: %v", r)
	}
	// später: Nachzügler aus 2025, Rechnung storniert, Kontoauszug im DMS gelöscht, Beleg aus 2026
	e.at("2026-02-01")
	e.addDoc("D8", "INVOICE", "2025-12-20", "4711", "Nachzügler Dezember")
	e.h.docs[len(e.h.docs)-1]["filed_at"] = "2026-01-20T09:00:00Z"
	e.addDoc("D9", "INVOICE", "2026-01-15", "4711", "Januar")
	e.h.docs[len(e.h.docs)-1]["filed_at"] = "2026-01-20T09:00:00Z"
	e.h.invoices = append(e.h.invoices, map[string]any{"invoice_id": "ER-2025-0001", "status": "CANCELLED"})
	for _, d := range e.h.docs {
		if d["id"] == "D3" {
			d["status"], d["removed_at"] = "REMOVED", "2026-01-25T07:00:00Z"
		}
	}
	r = e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	if r["created"].(int) != 1 || r["added"].(int) != 2 || r["marked"].(int) != 2 {
		t.Fatalf("zweiter Lauf (2026 neu; 2025: Nachzügler, Storno, gelöscht): %v", r)
	}
	ds = e.dataset("DS-2026-0001")
	if ds["state_count"] != int64(2) || ds["last_run"] != "EX-2026-0002" || ds["item_count"] != int64(5) || ds["marked_count"] != int64(2) {
		t.Fatalf("Dataset nach Stand 2: %v", ds)
	}
	got := e.runItems("EX-2026-0002")
	if got["D1"]["mark"] != markCancelled || got["D3"]["mark"] != markRemoved || got["D8"]["is_new"] != true || got["D2"]["is_new"] != false ||
		got["D1"]["item_no"] != e.runItems("EX-2026-0001")["D1"]["item_no"] {
		t.Fatalf("Elemente Stand 2: %v", got)
	}
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: runObject, Action: "export", Payload: map[string]any{"id": "1000|EX-2026-0002", "data": map[string]any{"export_target": "ZIP"}}})
	if err != nil {
		t.Fatal(err)
	}
	files := zipFiles(t, resp)
	toc := files["EX-2026-0002 Steuerunterlagen 2025/Inhaltsverzeichnis.csv"]
	if !strings.Contains(toc, "storniert (SupplierInvoice ER-2025-0001)") || !strings.Contains(toc, "ohne Datei;;im DMS gelöscht") ||
		!strings.Contains(toc, "Nachzügler Dezember") || strings.Contains(toc, "Januar") {
		t.Fatalf("Inhaltsverzeichnis Stand 2:\n%s", toc)
	}
	for name := range files {
		if strings.Contains(name, "Kontoauszug 12-2025") {
			t.Fatalf("gelöschter Kontoauszug mit Datei: %s", name)
		}
	}
	if l := e.dataset("DS-2026-0001")["log"].(string); !strings.Contains(l, "Lieferung EX-2026-0002 (Stand 2)") || strings.HasPrefix(l, "\n") {
		t.Fatal("Lieferung nicht im Protokoll")
	}
	// Stände eines Datasets lassen sich nicht verwerfen
	if _, err := e.call(runObject, "discard", map[string]any{"id": "1000|EX-2026-0003"}); err == nil {
		t.Fatal("Stand verworfen")
	}
}

// Energie-Monitor: je Mieter und Monat ein Dataset, ein neuer Druck ersetzt den alten,
// nach der Lebensdauer wird das Dataset gelöscht (Protokoll bleibt).
func TestEnergyMonitor(t *testing.T) {
	e := faust(t)
	e.at("2026-11-05")
	vi := func(id, partner, date, file, filed string) {
		e.addDoc(id, "CONSUMPTION_INFO", date, partner, "Verbrauchsinformation "+id)
		d := e.h.docs[len(e.h.docs)-1]
		d["file_name"], d["filed_at"] = file, filed
	}
	vi("V1", "4712", "2026-09-30", "VI-2026-0001_Nutzer_016.pdf", "2026-10-03T10:00:00Z")
	vi("V2", "4712", "2026-10-31", "VI-2026-0002_Nutzer_016.pdf", "2026-11-03T10:00:00Z")
	vi("V3", "4711", "2026-10-31", "VI-2026-0002_Nutzer_001.pdf", "2026-11-03T10:00:00Z")
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "EM", "name": "Energie-Monitor", "view_type": "VERBRAUCHSINFO"})
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|EM", "data": map[string]any{}})
	if r["created"].(int) != 2 {
		t.Fatalf("Oktober je Mieter (September vor dem Vormonat nicht): %v", r)
	}
	var marthe map[string]any
	for _, d := range items(e.must(datasetObject, "list", map[string]any{"query": map[string]any{"view_id": "EM"}})) {
		if d["partner_id"] == "4712" {
			marthe = d
		}
	}
	if marthe["name"] != "Energie-Monitor Marthe Schwerdtlein 10/2026" || marthe["delete_after"] != "2028-04-30" || marthe["next_due"] != "2026-12-01" {
		t.Fatalf("Dataset Marthe: %v", marthe)
	}
	id := marthe["dataset_id"].(string)
	// Neudruck derselben Verbrauchsinformation ersetzt die alte (im offenen Stand)
	e.at("2026-11-06")
	vi("V4", "4712", "2026-10-31", "VI-2026-0002_Nutzer_016.pdf", "2026-11-06T07:00:00Z")
	rr := e.must(datasetObject, "refresh", map[string]any{"id": "1000|" + id, "data": map[string]any{}})
	if rr["added"].(int) != 1 || rr["marked"].(int) != 1 || !strings.Contains(rr["message"].(string), "im offenen Stand") {
		t.Fatalf("Neudruck: %v", rr)
	}
	run := e.dataset(id)["last_run"].(string)
	got := e.runItems(run)
	if got["V2"]["mark"] != markReplaced || got["V2"]["replaced_by"] != "V4" || got["V4"]["mark"] != nil {
		t.Fatalf("ersetzt: %v", got)
	}
	// Rhythmus je Dataset einstellbar
	e.must(datasetObject, "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"rhythm": rhManual}})
	if d := e.dataset(id); d["next_due"] != nil {
		t.Fatalf("nur von Hand: %v", d)
	}
	// Lebensdauer abgelaufen: Zeitgeber löscht
	e.at("2028-05-01")
	e.m.tick(e.ctx)
	d := e.dataset(id)
	if d["status"] != dsDeleted || !strings.Contains(d["log"].(string), "Stand "+run+": 2 Dokumente, nicht geliefert") || !strings.Contains(d["log"].(string), "Lebensdauer") {
		t.Fatalf("gelöscht: %v", d)
	}
	if len(e.runItems(run)) != 0 {
		t.Fatal("Elemente nach dem Löschen")
	}
	if _, err := e.call(runObject, "export", map[string]any{"id": "1000|" + run, "data": map[string]any{"export_target": "ZIP"}}); err == nil {
		t.Fatal("gelöschtes Dataset exportiert")
	}
}

// Vorgang (seit 0.6.0): bei jeder Änderung (Ereignisjournal) liefert das Vorgangsmodul die
// Objektdaten; neuer Inhalt ⇒ neues Element vorgang.json, das alte gilt als ersetzt; gleicher
// Inhalt ⇒ kein neuer Stand; mit dem Abschluss endet das Dataset.
func TestProcessDataset(t *testing.T) {
	e := faust(t)
	e.at("2026-11-06")
	e.h.processes["1000|AF-2026-0001"] = map[string]any{"process_id": "AF-2026-0001", "subject": "Wasserschaden Gretchen", "partner_id": "4712"}
	e.h.events = append(e.h.events, map[string]any{"seq": 1, "process_id": "AF-2026-0001", "event": "CREATED"})
	e.addDoc("P1", "INVOICE", "2026-11-02", "4711", "Trocknung", "Process|1000|AF-2026-0001")
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "VG", "name": "Vorgänge", "view_type": "VORGANG"})
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|VG", "data": map[string]any{}})
	if r["created"].(int) != 1 || r["items"].(int) != 2 || e.h.sheets != 0 || e.h.exports != 1 {
		t.Fatalf("Vorgang (Objektdaten und Rechnung): %v, Drucke %d, Abbilder %d", r, e.h.sheets, e.h.exports)
	}
	ds := e.dataset("DS-2026-0001")
	if ds["name"] != "Vorgang AF-2026-0001" || ds["pending"] != false {
		t.Fatalf("Dataset: %v", ds)
	}
	obj := func() (string, map[string]map[string]any) {
		got := e.runItems(e.dataset("DS-2026-0001")["last_run"].(string))
		cur := ""
		for id, it := range got {
			if isObject(id) && it["mark"] == nil {
				cur = id
			}
		}
		return cur, got
	}
	first, got := obj()
	if first == "" || got[first]["file_name"] != "vorgang.json" || got[first]["folder"] != nil || got[first]["partner_id"] != "4712" {
		t.Fatalf("Element der Objektdaten: %v", got)
	}
	var data map[string]any
	r0, _ := e.m.one(e.ctx, "SELECT data FROM export__object WHERE object_id = ?", first)
	_ = json.Unmarshal([]byte(r0[0].(string)), &data)
	if docs := data["vorgang"].(map[string]any)["dokumente"].([]any); len(docs) != 1 || docs[0].(map[string]any)["ref"] != "doc:P1" ||
		data["umfang"].(map[string]any)["code"] != "MIETER" {
		t.Fatalf("Abbild: %v", data)
	}
	// Ereignis ohne Änderung des Inhalts: kein neuer Stand
	states := e.dataset("DS-2026-0001")["state_count"]
	e.h.events = append(e.h.events, map[string]any{"seq": 2, "process_id": "AF-2026-0001", "event": "VIEWED"})
	e.m.tick(e.ctx)
	if e.h.exports != 2 || e.dataset("DS-2026-0001")["state_count"] != states {
		t.Fatalf("ohne Änderung: %d Abbilder, Stände %v", e.h.exports, e.dataset("DS-2026-0001")["state_count"])
	}
	// Änderung → neues Abbild ersetzt das alte
	e.h.processes["1000|AF-2026-0001"]["changed_at"] = "2026-11-06T10:00:00Z"
	e.h.events = append(e.h.events, map[string]any{"seq": 3, "process_id": "AF-2026-0001", "event": "STEP"})
	e.m.tick(e.ctx)
	second, got := obj()
	if second == "" || second == first || got[first]["mark"] != markReplaced || got[first]["replaced_by"] != second {
		t.Fatalf("Änderung: %v", got)
	}
	// zip: vorgang.json mit Verweis auf die Datei im Lauf
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: runObject, Action: "export", Payload: map[string]any{"id": "1000|" + e.dataset("DS-2026-0001")["last_run"].(string),
		"data": map[string]any{"export_target": "ZIP"}}})
	if err != nil {
		t.Fatal(err)
	}
	files := zipFiles(t, resp)
	var vj string
	for name, body := range files {
		if strings.HasSuffix(name, "/vorgang.json") {
			vj = body
		}
	}
	if !strings.Contains(vj, `"ref":"`) || strings.Contains(vj, "doc:P1") || !strings.Contains(vj, "Trocknung.pdf") {
		t.Fatalf("vorgang.json: %q (%v)", vj, keysOf(files))
	}
	// Abschluss: Dataset geht verloren
	e.h.processes["1000|AF-2026-0001"]["closed_on"] = "2026-11-06"
	e.m.tick(e.ctx)
	if d := e.dataset("DS-2026-0001"); d["status"] != dsDeleted {
		t.Fatalf("nach dem Abschluss: %v", d)
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Übernahme: exportierte Läufe aus 0.2 werden Stände eines Datasets.
func TestMigrateRuns(t *testing.T) {
	e := faust(t)
	e.at("2026-11-06")
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "STB_2025", "name": "Steuerbüro 2025", "view_type": "STEUERBUERO",
		"date_from": "2025-01-01", "date_to": "2025-12-31"})
	for _, q := range []string{
		`INSERT INTO export__run (company_code, run_id, view_id, name, status, key_date, created_at, view_type, item_count, excluded_count) VALUES ('1000', 'EX-2026-0001', 'STB_2025', 'Steuerbüro 2025', 'OPEN', '2026-11-05', '2026-11-05T09:00:00Z', 'STEUERBUERO', 1, 0)`,
		`INSERT INTO export__run (company_code, run_id, view_id, name, status, key_date, created_at, view_type, item_count, excluded_count) VALUES ('1000', 'EX-2026-0002', 'STB_2025', 'Steuerbüro 2025', 'EXPORTED', '2026-11-06', '2026-11-06T07:00:00Z', 'STEUERBUERO', 1, 0)`,
		`INSERT INTO export__item (company_code, run_id, item_no, document_id, file_name, doc_type, size, excluded, added) VALUES ('1000', 'EX-2026-0002', 1, 'D1', '001_Weinlieferung.pdf', 'INVOICE', 10, 0, 0)`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	ds := e.dataset("DS-2026-0001")
	if ds["name"] != "Steuerunterlagen 2025" || ds["last_delivery"] != "EX-2026-0002" || ds["state_count"] != int64(1) {
		t.Fatalf("Übernahme: %v", ds)
	}
	// alter offener Lauf: verwerfen
	e.must(runObject, "discard", map[string]any{"id": "1000|EX-2026-0001"})
	// nächster Stand: alles aus 2025, das noch fehlt, D1 bleibt mit seiner Nummer
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|STB_2025", "data": map[string]any{}})
	got := e.runItems(r["run_id"].(string))
	if r["run_id"] != "EX-2026-0003" || got["D1"]["item_no"] != int64(1) || got["D1"]["is_new"] != false || got["D2"]["is_new"] != true {
		t.Fatalf("nach der Übernahme: %v %v", r, got)
	}
}
