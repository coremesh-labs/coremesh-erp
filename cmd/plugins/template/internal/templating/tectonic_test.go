package templating

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestRealTectonic übersetzt die PDF-Beispiele mit echtem Tectonic
// (TP_TECTONIC=<Programm>, TP_TECTONIC_CACHE=<Cache>; TP_PDF_OUT=<Verzeichnis> speichert die PDFs).
func TestRealTectonic(t *testing.T) {
	bin := os.Getenv("TP_TECTONIC")
	if bin == "" {
		t.Skip("nur mit TP_TECTONIC=<Pfad zu tectonic>")
	}
	e := setup(t)
	e.m.tex = execTex
	e.m.settings.TectonicPath = bin
	if c := os.Getenv("TP_TECTONIC_CACHE"); c != "" {
		e.m.settings.CacheDir = c
	}
	e.must(defObject, "examples", map[string]any{"data": map[string]any{"job_company": "1000"}})
	for _, nr := range []string{"1000-2026-000001", "1000-2026-000002", "1000-2026-000004", "1000-2026-000006"} {
		j := e.jobRow(nr)
		if j["status"] != stDone {
			t.Fatalf("%s: %v %v", nr, j["status"], j["status_text"])
		}
		got := e.must(serviceObject, "get", map[string]any{"job_number": nr})
		b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(got["content"]))
		if len(b) < 1000 || string(b[:4]) != "%PDF" {
			t.Fatalf("%s: kein PDF (%d Bytes)", nr, len(b))
		}
		if out := os.Getenv("TP_PDF_OUT"); out != "" {
			_ = os.WriteFile(filepath.Join(out, fmt.Sprint(got["file_name"])), b, 0o644)
		}
	}
}
