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
		!strings.HasPrefix(e.run.calls[1], "ocrmypdf --skip-text -l deu+eng ") || !strings.HasSuffix(e.run.calls[2], "/ocr.pdf -") {
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

// TestObjectStep: „Objekt wählen“ bietet Gebäude, Mietobjekte und Verträge in
// einer Liste; die Wahl legt den Bezug an.
func TestObjectStep(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "ohne Datum"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("objekt")), "file_name": "o.pdf"}})
	id := up["id"]
	next := func(data map[string]any) map[string]any {
		return e.must(docObject, "continue", map[string]any{"id": id, "data": data})
	}
	next(map[string]any{"wf_choice": "NOTICE"})
	if r := next(map[string]any{"wf_choice": "3.0.1"}); !strings.Contains(r["message"].(string), "Zu welchem Objekt") {
		t.Fatalf("nach Ablage: %v", r)
	}
	fs := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id.(string), Values: map[string]string{}})
	opts := fs["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any)
	var labels []string
	for _, o := range opts {
		labels = append(labels, o.(map[string]any)["value"].(string)+"="+o.(map[string]any)["label"].(string))
	}
	all := strings.Join(labels, "\n")
	if !strings.HasPrefix(all, "=– überspringen –") || !strings.Contains(all, "Building:2000|ROGoer14=Gebäude: 2000|ROGoer14 – Bezeichnung 2000|ROGoer14") ||
		!strings.Contains(all, "RentObject:2000|RUG1402=Mietobjekt:") || !strings.Contains(all, "Contract:2000|GS-2026-0009=Vertrag:") {
		t.Fatalf("Auswahl:\n%s", all)
	}
	links := fs["links"].([]any)
	if len(links) != 4 || links[0].(map[string]any)["url"] != "/new/Contract" || links[1].(map[string]any)["label"] != "Rechnung erfassen" ||
		!strings.Contains(fs["message"].(string), "Im neuen Fenster anlegen") {
		t.Fatalf("Links: %v", fs)
	}
	// Im zweiten Fenster angelegt (noch nicht gebucht) – Suche findet es.
	e.h.invoices = []map[string]any{{"id": "2000|ER-00099", "supplier_reference": "RE-4711"}}
	fs = e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id.(string), Values: map[string]string{"wf_search": "ER-00099"}})
	if f := fmt.Sprint(fs["fields"]); !strings.Contains(f, "SupplierInvoice:2000|ER-00099") || strings.Contains(f, "Building:") {
		t.Fatalf("Suche: %v", fs["fields"])
	}
	_, err := e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "Building:2000|GIBTSNICHT"}})
	expect(t, err, sdk.ErrInvalidArgument, "Wert nicht in der Auswahl")
	next(map[string]any{"wf_choice": "Building:2000|ROGoer14"})
	if n := e.one(`SELECT COUNT(*) FROM dms__document_ref WHERE document_id = ? AND object = 'Building' AND object_key = '2000|ROGoer14'`, id); n != int64(1) {
		t.Fatalf("Bezug: %v", n)
	}
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["wf_step"] != "050" {
		t.Fatalf("weiter mit Partner: %v", doc["wf_step"])
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
