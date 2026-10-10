package dms

import (
	"bytes"
	"context"
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
