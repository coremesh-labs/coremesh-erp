package dms

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
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
	if n := e.one(`SELECT COUNT(*) FROM dms__step_param WHERE workflow = 'STANDARD' AND step = '010'`); n != int64(5) {
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
