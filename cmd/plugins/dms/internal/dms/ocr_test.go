package dms

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// minimalPDF: einseitiges PDF mit einer Textzeile (gültige xref-Tabelle).
func minimalPDF(text string) []byte {
	stream := fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET", text)
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

// TestRealTextRecognition: Texterkennung mit dem echten pdftotext (übersprungen, wenn es fehlt).
func TestRealTextRecognition(t *testing.T) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext nicht installiert")
	}
	e := setup(t)
	e.m.run = execRunner{}
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "echt", "name": "Echt", "path": e.in, "workflow": "STANDARD"})
	content := minimalPDF("Grundsteuerbescheid vom 15.02.2025 Biesnitzer Strasse")
	id, dup, err := e.m.ingest(e.ctx, ingestRequest{CC: "2000", Inbox: "ECHT", FileName: "echt.pdf", ContentType: "application/pdf",
		Path: writeTemp(t, e.in, content), Content: content})
	if err != nil || dup {
		t.Fatal(err, dup)
	}
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["doc_date"] != "2025-02-15" || doc["wf_step"] != "030" || doc["wf_status"] != wfWaiting {
		t.Fatalf("Workflow nach echter Texterkennung: %v", doc)
	}
	if txt := e.m.docText(context.Background(), id); !strings.Contains(txt, "Biesnitzer") {
		t.Fatalf("Text: %q", txt)
	}
}

func writeTemp(t *testing.T, dir string, b []byte) string {
	t.Helper()
	p, err := writeNew(dir, "echt.pdf", b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestOCRCommands: Befehle aus den Parametern des Schritts, Platzhalter,
// Ersatzbefehl bei zu wenig Text, nur erlaubte Programme.
func TestOCRCommands(t *testing.T) {
	e := setup(t)
	e.company()
	if n := e.one(`SELECT COUNT(*) FROM dms__step_param WHERE workflow = 'STANDARD' AND step = '010'`); n != int64(7) {
		t.Fatalf("Parameter im Standard-Workflow: %v", n)
	}
	// Eigener Befehl mit Anführungszeichen; zu wenig Text → Ersatzbefehl (zwei Zeilen).
	e.must(paramObject, "update", map[string]any{"id": "STANDARD|010|command|", "data": map[string]any{"value": `pdftotext -f 1 -l 2 "{in}" -`}})
	e.must(paramObject, "update", map[string]any{"id": "STANDARD|010|min_chars|", "data": map[string]any{"value": "1000"}})
	e.run.text = "kurz"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "x", "name": "X", "path": e.in, "workflow": "STANDARD"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|X", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ocr")), "file_name": "a.pdf"}})
	if len(e.run.calls) != 3 || !strings.HasPrefix(e.run.calls[0], "pdftotext -f 1 -l 2 /") || !strings.HasSuffix(e.run.calls[0], "/in.pdf -") ||
		!strings.HasPrefix(e.run.calls[1], "ocrmypdf --skip-text -l eng ") || !strings.HasSuffix(e.run.calls[2], "/ocr.pdf -") {
		t.Fatalf("Aufrufe: %q", e.run.calls)
	}
	doc := e.must(docObject, "get", map[string]any{"id": up["id"]})
	if !strings.Contains(doc["wf_log"].(string), "010 Text erkennen: Programmschritt nicht erfolgreich") {
		t.Fatalf("Verlauf: %s", doc["wf_log"])
	}
	// Nicht erlaubtes Programm.
	e.must(paramObject, "update", map[string]any{"id": "STANDARD|010|command|", "data": map[string]any{"value": "rm -rf {work}"}})
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|X", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("rm")), "file_name": "b.pdf"}})
	doc = e.must(docObject, "get", map[string]any{"id": up["id"]})
	if !strings.Contains(doc["wf_log"].(string), `Programm "rm" ist nicht erlaubt`) {
		t.Fatalf("Verlauf: %s", doc["wf_log"])
	}
	// Erlaubte Programme aus der Einstellung.
	e.m.settings.AllowedPrograms = []string{"/opt/ocr/bin/myocr"}
	if !e.m.programAllowed("/opt/ocr/bin/myocr") || e.m.programAllowed("pdftotext") {
		t.Fatal("allowed_programs")
	}
}

func TestSplitCommand(t *testing.T) {
	args, err := splitCommand(`ocrmypdf -l deu "{in}" '{work}/a b.pdf'`)
	if err != nil || strings.Join(args, "|") != "ocrmypdf|-l|deu|{in}|{work}/a b.pdf" {
		t.Fatalf("%q %v", args, err)
	}
	if _, err := splitCommand(`x "offen`); err == nil {
		t.Fatal("offenes Anführungszeichen")
	}
}

// TestOwnScans: Eigene Scans ohne Text-Ebene werden aufbereitet (Inhalt
// ersetzt, PDF/A), zugeschickte Scans und PDFs mit Text bleiben unverändert;
// der Scan wird auch nach der Aufbereitung als doppelt erkannt.
func TestOwnScans(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "scanner", "name": "Scanner", "path": e.in + "/scanner",
		"workflow": "STANDARD", "own_scans": true})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "post", "name": "Post", "path": e.in + "/post", "workflow": "STANDARD"})
	upload := func(inbox, name string, b []byte) map[string]any {
		up := e.must(inboxObject, "upload", map[string]any{"id": "2000|" + inbox, "data": map[string]any{
			"file": base64.StdEncoding.EncodeToString(b), "file_name": name}})
		return e.must(docObject, "get", map[string]any{"id": up["id"]})
	}
	content := func(id any) []byte {
		c := e.must(docObject, "content", map[string]any{"id": id})
		b, _ := base64.StdEncoding.DecodeString(c["content"].(string))
		return b
	}
	e.run.text, e.run.ocrText = "", "Rechnung vom 03.04.2026 mit erkanntem Text aus dem Scan"
	scan := pdf("eigener Scan")
	doc := upload("SCANNER", "scan.pdf", scan)
	b := content(doc["id"])
	if !bytes.HasPrefix(b, []byte("%PDF-OCR")) || doc["sha256"] == doc["source_sha256"] || doc["doc_date"] != "2026-04-03" ||
		!strings.Contains(doc["wf_log"].(string), "Scan aufbereitet") || !strings.Contains(e.run.calls[1], "--output-type pdfa") {
		t.Fatalf("eigener Scan: %v %q", doc, e.run.calls)
	}
	if !strings.HasSuffix(doc["inbox_file"].(string), "/scanner/scan.pdf") {
		t.Fatalf("Datei im Eingang: %v", doc["inbox_file"])
	}
	// Derselbe Scan noch einmal: doppelt (Prüfsumme der eingelesenen Datei).
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|SCANNER", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(scan), "file_name": "scan2.pdf"}})
	if up["duplicate"] != true {
		t.Fatalf("doppelter Scan: %v", up)
	}
	// Zugeschickter Scan: Text nur für Suche und Workflow, Datei unverändert.
	e.run.calls = nil
	post := pdf("zugeschickt")
	doc = upload("POST", "brief.pdf", post)
	if !bytes.Equal(content(doc["id"]), post) || doc["sha256"] != doc["source_sha256"] ||
		!strings.HasSuffix(e.run.calls[1], "/ocr.pdf") {
		t.Fatalf("zugeschickter Scan: %v %q", doc, e.run.calls)
	}
	// PDF mit Text-Ebene im Scanner-Eingang: unverändert.
	e.run.text, e.run.calls = "Echtes PDF mit Text-Ebene vom 01.02.2026", nil
	real := pdf("echt")
	doc = upload("SCANNER", "echt.pdf", real)
	if !bytes.Equal(content(doc["id"]), real) || len(e.run.calls) != 1 {
		t.Fatalf("PDF mit Text: %v %q", doc, e.run.calls)
	}
	// Bild aus dem Scanner wird zum PDF.
	e.run.calls = nil
	doc = upload("SCANNER", "foto.jpg", []byte("\xff\xd8\xff JPEG"))
	if doc["content_type"] != "application/pdf" || doc["file_name"] != "foto.pdf" || !strings.Contains(e.run.calls[0], "--image-dpi 300") {
		t.Fatalf("Bild: %v %q", doc, e.run.calls)
	}
}

// TestHandover: Zugeschickter Scan landet im Scanner-Eingang (aufbereitet),
// Übergabe an den PDF-Eingang bringt das Original zurück; Workflow startet neu.
func TestHandover(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "scans", "name": "Scans", "path": e.in + "/scans",
		"workflow": "STANDARD", "own_scans": true})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "Original-PDFs", "path": e.in + "/pdf", "workflow": "STANDARD"})
	e.run.text, e.run.ocrText = "", "Bescheid vom 05.05.2025"
	orig := pdf("zugeschickter Scan")
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|SCANS", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(orig), "file_name": "bescheid.pdf"}})
	id := up["id"]
	if m, _ := filepath.Glob(e.in + "/scans/.original/*"); len(m) != 1 {
		t.Fatalf("Original aufgehoben: %v", m)
	}
	if err := os.WriteFile(e.in+"/pdf/bescheid_step_over_from_SCANS.pdf", []byte("schon da"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.must(docObject, "handover", map[string]any{"id": id, "data": map[string]any{"handover_inbox": "pdf"}})
	doc := e.must(docObject, "get", map[string]any{"id": id})
	c := e.must(docObject, "content", map[string]any{"id": id})
	b, _ := base64.StdEncoding.DecodeString(c["content"].(string))
	if !bytes.Equal(b, orig) || doc["inbox"] != "PDF" || doc["sha256"] != doc["source_sha256"] ||
		!strings.Contains(doc["wf_log"].(string), "Übergeben von Eingang SCANS an PDF (Original") || r["status"] != wfWaiting {
		t.Fatalf("Übergabe: %v %v", doc, r)
	}
	if !strings.HasSuffix(doc["inbox_file"].(string), "/pdf/bescheid_step_over_from_SCANS_2.pdf") {
		t.Fatalf("Name nach Übergabe (vorhandene Datei nicht überschrieben): %v", doc["inbox_file"])
	}
	if b, _ := os.ReadFile(e.in + "/pdf/bescheid_step_over_from_SCANS.pdf"); string(b) != "schon da" {
		t.Fatal("vorhandene Datei überschrieben")
	}
	if m, _ := filepath.Glob(e.in + "/scans/*"); len(m) != 1 { // nur noch das leere .original
		t.Fatalf("alter Eingang: %v", m)
	}
	if _, err := os.Stat(doc["inbox_file"].(string)); err != nil || !strings.Contains(doc["inbox_file"].(string), "/pdf/") {
		t.Fatalf("neue Datei: %v %v", doc["inbox_file"], err)
	}
	_, err := e.call(docObject, "handover", map[string]any{"id": id, "data": map[string]any{"handover_inbox": "pdf"}})
	expect(t, err, sdk.ErrInvalidArgument, "Übergabe an denselben Eingang")
}

// TestObjectStep: „Objekt wählen“ bietet nur die Objekte der Dokumentart
// (Bescheid → Verträge, mit „anlegen“); mit Parameter source mehrere Listen
// (Gebäude, Mietobjekte, Verträge) in einer Auswahl. Die Wahl legt den Bezug an.
func TestObjectStep(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "ohne Datum"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	upload := func(tag string) string {
		up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
			"file": base64.StdEncoding.EncodeToString(pdf(tag)), "file_name": tag + ".pdf"}})
		return up["id"].(string)
	}
	options := func(id string, values map[string]string) (string, map[string]any) {
		fs := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: values})
		var l []string
		for _, o := range fs["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any) {
			l = append(l, o.(map[string]any)["value"].(string)+"="+o.(map[string]any)["label"].(string))
		}
		return strings.Join(l, "\n"), fs
	}
	id := upload("objekt")
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "NOTICE"}})
	if r := e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "3.0.1"}}); !strings.Contains(r["message"].(string), "Zu welchem Vorgang") {
		t.Fatalf("nach Ablage: %v", r)
	}
	all, fs := options(id, map[string]string{})
	if !strings.Contains(all, "Contract:2000|GS-2026-0009=Vertrag: GS-2026-0009") || strings.Contains(all, "Building:") ||
		strings.Contains(all, "SupplierInvoice:") {
		t.Fatalf("nur Verträge:\n%s", all)
	}
	links := fs["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["url"] != "/new/Contract" || !strings.Contains(fs["message"].(string), "Im neuen Fenster anlegen") {
		t.Fatalf("Links: %v", fs)
	}
	_, err := e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "Building:2000|ROGoer14"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gebäude bei Bescheid")
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "Contract:2000|GS-2026-0009"}})
	if n := e.one(`SELECT COUNT(*) FROM dms__document_ref WHERE document_id = ? AND object = 'Contract' AND object_key = '2000|GS-2026-0009'`, id); n != int64(1) {
		t.Fatalf("Bezug: %v", n)
	}
	// Mehrere Listen über source (statt objects).
	e.must(paramObject, "deactivate", map[string]any{"id": "STANDARD|045|objects|"})
	e.create(paramObject, map[string]any{"workflow": "STANDARD", "step": "045", "param": "source",
		"value": "Building.list=Gebäude; RentObject.list=Mietobjekt; SupplierInvoice.list=Rechnung"})
	e.create(paramObject, map[string]any{"workflow": "STANDARD", "step": "045", "param": "create", "value": "SupplierInvoice=Rechnung erfassen"})
	e.create(paramObject, map[string]any{"workflow": "STANDARD", "step": "045", "param": "label_fields", "value": "designation, supplier_reference"})
	e.h.invoices = []map[string]any{{"id": "2000|ER-00099", "supplier_reference": "RE-4711"}}
	id = upload("mehrere")
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "OTHER"}})
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "3.0.1"}})
	all, _ = options(id, map[string]string{})
	if !strings.HasPrefix(all, "=– überspringen –") || !strings.Contains(all, "Building:2000|ROGoer14=Gebäude: 2000|ROGoer14 – Bezeichnung 2000|ROGoer14") ||
		!strings.Contains(all, "RentObject:2000|RUG1402=Mietobjekt:") || !strings.Contains(all, "SupplierInvoice:2000|ER-00099=Rechnung:") {
		t.Fatalf("Auswahl:\n%s", all)
	}
	if all, _ = options(id, map[string]string{"wf_search": "ER-00099"}); !strings.Contains(all, "SupplierInvoice:2000|ER-00099") || strings.Contains(all, "Building:") {
		t.Fatalf("Suche:\n%s", all)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "Building:2000|ROGoer14"}})
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "050" {
		t.Fatalf("weiter mit Partner: %v", d["wf_step"])
	}
}

// TestLookProgramWithoutPath: Plugin ohne PATH (Host vererbt keine Umgebung) findet Programme trotzdem.
func TestLookProgramWithoutPath(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("kein /bin/sh")
	}
	t.Setenv("PATH", "")
	if p, err := lookProgram("sh"); err != nil || !strings.HasSuffix(p, "/sh") {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := lookProgram("gibt-es-nicht-dms"); err == nil || !strings.Contains(err.Error(), "nicht gefunden") {
		t.Fatalf("fehlendes Programm: %v", err)
	}
}

// TestTreeSelection: Ablage Ebene für Ebene – oberste Ebene, Gruppe öffnen,
// zurück, Ablage wählen; Suche über alle Ebenen; nur unterste Ebene speicherbar.
func TestTreeSelection(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "ohne Datum"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ebenen")), "file_name": "e.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "NOTICE"}})
	opts := func(values map[string]string) (string, string) {
		fs := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: values})
		var l []string
		for _, o := range fs["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any) {
			l = append(l, o.(map[string]any)["value"].(string)+"="+o.(map[string]any)["label"].(string))
		}
		return strings.Join(l, "\n"), fs["message"].(string)
	}
	o, msg := opts(map[string]string{})
	if o != "=– Ablage wählen –\n3=▸ 3 Steuern Lasten Abgaben\n4=▸ 4 Finanzen Bankwesen\n5=▸ 5 Dienstleistungen Verträge" ||
		!strings.Contains(msg, "oberste Ebene") {
		t.Fatalf("oberste Ebene:\n%s\n%s", o, msg)
	}
	o, msg = opts(map[string]string{"wf_choice": "3"})
	if o != "^3=▾ 3 Steuern Lasten Abgaben – bitte wählen\n^=↑ zurück zur obersten Ebene\n3.0=▸ 3.0 Grundsteuer\n3.1=▸ 3.1 Kommunale Gebühren" ||
		!strings.Contains(msg, "Ebene: 3 Steuern Lasten Abgaben") {
		t.Fatalf("Ebene 3:\n%s\n%s", o, msg)
	}
	o, msg = opts(map[string]string{"wf_choice": "3.0"})
	if !strings.Contains(o, "^3=↑ zurück zu 3 Steuern Lasten Abgaben") || !strings.Contains(o, "3.0.1=3.0.1 Grundsteuerbescheide") ||
		!strings.Contains(msg, "3 Steuern Lasten Abgaben › 3.0 Grundsteuer") {
		t.Fatalf("Ebene 3.0:\n%s\n%s", o, msg)
	}
	// Gewählte Ablage bleibt mit ihren Geschwistern stehen; zurück führt eine Ebene hoch.
	if o, _ = opts(map[string]string{"wf_choice": "3.0.1"}); !strings.Contains(o, "3.0.0=3.0.0 Einheitswertbescheide") {
		t.Fatalf("Geschwister:\n%s", o)
	}
	if o, _ = opts(map[string]string{"wf_choice": "^3"}); !strings.Contains(o, "3.0=▸ 3.0 Grundsteuer") {
		t.Fatalf("zurück:\n%s", o)
	}
	if o, msg = opts(map[string]string{"wf_search": "wirtschafts"}); !strings.Contains(o, "5.2.3.2=5.2.3.2 Wirtschaftsplan") || strings.Contains(o, "▸") {
		t.Fatalf("Suche:\n%s\n%s", o, msg)
	}
	_, err := e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "^3"}})
	expect(t, err, sdk.ErrInvalidArgument, "zurück speichern")
	_, err = e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "3.0"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gruppe speichern")
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "3.0.1"}})
	if r := e.must(workflowObject, "check", map[string]any{"id": "STANDARD"}); !strings.Contains(r["message"].(string), "keine Auffälligkeiten") {
		t.Fatalf("Prüfen: %v", r)
	}
	if doc := e.must(docObject, "get", map[string]any{"id": id}); doc["file_account"] != "3.0.1" || doc["wf_step"] != "045" {
		t.Fatalf("gewählt: %v %v", doc["file_account"], doc["wf_step"])
	}
}

// TestMissingLanguage: deu fehlt – zugeschickter Scan wird mit eng erkannt,
// der Text gespeichert, die Datei bleibt unverändert, der Verlauf nennt es.
func TestMissingLanguage(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	e.run.text, e.run.ocrText = "", "Rechnung vom 03.04.2026 aus dem zugeschickten Scan"
	orig := pdf("fremder Scan")
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(orig), "file_name": "s.pdf"}})
	id := up["id"].(string)
	var ocr string
	for _, c := range e.run.calls {
		if strings.HasPrefix(c, "ocrmypdf") {
			ocr = c
		}
	}
	if !strings.Contains(ocr, "-l eng ") {
		t.Fatalf("Sprachen: %q", e.run.calls)
	}
	if txt := e.m.docText(e.ctx, id); !strings.Contains(txt, "zugeschickten Scan") {
		t.Fatalf("Text: %q", txt)
	}
	c := e.must(docObject, "content", map[string]any{"id": id})
	if b, _ := base64.StdEncoding.DecodeString(c["content"].(string)); !bytes.Equal(b, orig) {
		t.Fatal("Datei verändert")
	}
	d := e.must(docObject, "get", map[string]any{"id": id})
	if !strings.Contains(d["wf_log"].(string), "Sprachdaten deu fehlen – erkannt mit eng (installieren z. B. tesseract-ocr-deu)") {
		t.Fatalf("Verlauf: %s", d["wf_log"])
	}
	// Mit deu installiert: beide Sprachen, kein Hinweis.
	e.run.langs, e.run.calls = "List of available languages (3):\ndeu\neng\nosd\n", nil
	e.must(docObject, "recognize", map[string]any{"id": id})
	if !strings.Contains(strings.Join(e.run.calls, "|"), "-l deu+eng ") {
		t.Fatalf("mit deu: %q", e.run.calls)
	}
}
