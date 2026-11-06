package export

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// addDoc: abgelegtes Dokument im DMS (Attrappe), Bestand aus Goethes Faust.
func (e *env) addDoc(id, typ, date, partner, title string, refs ...string) {
	body := []byte("%PDF-1.4 " + id)
	sum := sha256.Sum256(body)
	e.h.docs = append(e.h.docs, map[string]any{"id": id, "company_code": "1000", "status": "FILED", "doc_type": typ, "doc_date": date,
		"partner_id": partner, "title": title, "file_name": id + ".pdf", "name": "AUERBACH_" + id, "file_account": "72.1",
		"size": float64(len(body)), "sha256": hex.EncodeToString(sum[:]), "filed_at": date + "T10:00:00Z"})
	e.h.content[id] = body
	e.h.refs[id] = map[string]bool{}
	for _, r := range refs {
		e.h.refs[id][r] = true
	}
}

func faust(t *testing.T) *env {
	e := setup(t)
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); r["created"].(int) != len(typeDefaults)+len(targetDefaults)+len(markDefaults)+len(roleDefaults)+len(contentTypeDefaults) {
		t.Fatalf("setup-company: %v", r)
	}
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); r["created"].(int) != 0 {
		t.Fatalf("setup-company zweimal: %v", r)
	}
	if _, err := e.call(setupObject, "setupCompany", map[string]any{"company": "9999"}); err == nil {
		t.Fatal("setup-company für einen unbekannten Buchungskreis")
	}
	e.h.partners["4711"] = "Auerbachs Keller"
	e.h.partners["4712"] = "Marthe Schwerdtlein"
	e.addDoc("D1", "INVOICE", "2025-03-01", "4711", "Weinlieferung März")
	e.addDoc("D2", "INVOICE", "2025-03-01", "4711", "Weinlieferung März") // gleicher Titel, eigene lfd
	e.addDoc("D3", "BANK", "2025-12-31", "", "Kontoauszug 12/2025")
	e.addDoc("D4", "INVOICE", "2024-12-31", "4711", "Vorjahr")
	e.addDoc("D5", "CONTRACT", "2025-06-01", "4712", "Mietvertrag Gartenhaus", "Contract|1000|MV-2025-0001")
	e.addDoc("D6", "INVOICE", "2025-05-05", "4712", "Ohne Leserecht")
	e.h.hidden["D6"] = true
	e.addDoc("D7", "NOTICE", "2025-07-01", "", "Grundsteuer")
	return e
}

func TestSteuerbueroRun(t *testing.T) {
	e := faust(t)
	// Steuerbüro: Dataset je Jahr – der Zeitraum ist keine Pflichtangabe mehr; Vertrag schon
	if _, err := e.call(viewObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "view_id": "vt",
		"name": "Vertrag", "view_type": "VERTRAG"}}); err == nil || !strings.Contains(err.Error(), "Vertrag") {
		t.Fatalf("Sicht ohne Vertrag: %v", err)
	}
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "stb_2025", "name": "Steuerbüro 2025", "view_type": "STEUERBUERO",
		"date_from": "2025-01-01", "date_to": "2025-12-31", "target": "ZIP"})
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|STB_2025", "data": map[string]any{"key_date": "2026-11-06"}})
	if r["run_id"] != "EX-2026-0001" || r["items"].(int) != 4 {
		t.Fatalf("Zusammenstellen (ohne Vertrag, Vorjahr, fremdes Leserecht): %v", r)
	}
	got := items(e.must(itemObject, "list", map[string]any{"query": map[string]any{"run_id": "EX-2026-0001"}}))
	var names []string
	for _, it := range got {
		names = append(names, it["folder"].(string)+"/"+it["file_name"].(string))
	}
	want := []string{"Bescheid - Festsetzung/001_2025-07-01_Grundsteuer.pdf", "Kontoauszug/002_2025-12-31_Kontoauszug 12-2025.pdf",
		"Rechnung/003_2025-03-01_Auerbachs Keller_Weinlieferung März.pdf", "Rechnung/004_2025-03-01_Auerbachs Keller_Weinlieferung März.pdf"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("Namen:\n%s", strings.Join(names, "\n"))
	}
	// nachbessern: ausschließen mit Grund, Vorjahresbeleg hinzunehmen
	if _, err := e.call(itemObject, "exclude", map[string]any{"id": "1000|EX-2026-0001|4", "data": map[string]any{}}); err == nil {
		t.Fatal("Ausschließen ohne Grund")
	}
	e.must(itemObject, "exclude", map[string]any{"id": "1000|EX-2026-0001|4", "data": map[string]any{"exclude_reason": "doppelt eingescannt"}})
	e.must(runObject, "addDocument", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"add_document": "D4", "add_reason": "Abgrenzung"}})
	if _, err := e.call(runObject, "addDocument", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"add_document": "D4"}}); err == nil {
		t.Fatal("Dokument zweimal")
	}
	if _, err := e.call(runObject, "addDocument", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"add_document": "D6"}}); err == nil {
		t.Fatal("Dokument ohne Leserecht hinzugenommen")
	}
	run := e.must(runObject, "get", map[string]any{"id": "1000|EX-2026-0001"})
	if run["item_count"] != int64(4) || run["excluded_count"] != int64(1) {
		t.Fatalf("Zähler: %v / %v", run["item_count"], run["excluded_count"])
	}
	// Ziel zip (Download)
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: runObject, Action: "export", Payload: map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"export_target": "ZIP"}}})
	if err != nil {
		t.Fatal(err)
	}
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
	stem := "EX-2026-0001 Steuerunterlagen 2025/"
	if len(files) != 7 || files[stem+"Rechnung/005_2024-12-31_Auerbachs Keller_Vorjahr.pdf"] != "%PDF-1.4 D4" {
		t.Fatalf("zip: %v", keys(files))
	}
	toc := files[stem+"Inhaltsverzeichnis.csv"]
	if !strings.HasPrefix(toc, "\ufeffNr;Ordner;Datei") || !strings.Contains(toc, "ausgeschlossen: doppelt eingescannt") || !strings.Contains(toc, "hinzugenommen Abgrenzung") {
		t.Fatalf("Inhaltsverzeichnis:\n%s", toc)
	}
	var mf manifest
	if err := json.Unmarshal([]byte(files[stem+"MANIFEST.json"]), &mf); err != nil || len(mf.Items) != 4 || len(mf.Excluded) != 1 || mf.Items[2].DocType != "Rechnung" {
		t.Fatalf("Manifest: %v %+v", err, mf)
	}
	if !strings.Contains(files[stem+"SHA256SUMS"], mf.Items[1].SHA+"  Kontoauszug/") {
		t.Fatalf("SHA256SUMS:\n%s", files[stem+"SHA256SUMS"])
	}
	// exportiert: keine Änderungen mehr
	if _, err := e.call(itemObject, "include", map[string]any{"id": "1000|EX-2026-0001|4"}); err == nil {
		t.Fatal("Änderung nach dem Export")
	}
	// Ziel Verzeichnis; ein zweites Mal wird nicht überschrieben
	dir := t.TempDir()
	e.create(targetObject, map[string]any{"company_code": "1000", "code": "stb_ordner", "name": "Ordner fürs Steuerbüro", "kind": "DIRECTORY", "path": dir})
	e.must(runObject, "export", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"export_target": "STB_ORDNER"}})
	if b, err := os.ReadFile(filepath.Join(dir, "EX-2026-0001 Steuerunterlagen 2025", "Rechnung", "005_2024-12-31_Auerbachs Keller_Vorjahr.pdf")); err != nil || string(b) != "%PDF-1.4 D4" {
		t.Fatalf("Verzeichnis: %v %q", err, b)
	}
	if _, err := e.call(runObject, "export", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"export_target": "STB_ORDNER"}}); err == nil {
		t.Fatal("Verzeichnis überschrieben")
	}
	run = e.must(runObject, "get", map[string]any{"id": "1000|EX-2026-0001"})
	if run["status"] != runExported || strings.Count(run["log"].(string), "\n") != 1 || run["manifest_sha256"] == "" {
		t.Fatalf("Lauf: %v", run)
	}
}

func TestContractView(t *testing.T) {
	e := faust(t)
	if _, err := e.call(viewObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "view_id": "GARTEN",
		"name": "Gartenhaus", "view_type": "RECHTSSTREIT"}}); err == nil {
		t.Fatal("Sicht ohne Vertrag")
	}
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "GARTEN", "name": "Gartenhaus Schwerdtlein", "view_type": "RECHTSSTREIT",
		"contract_id": "MV-2025-0001"})
	r := e.must(viewObject, "compose", map[string]any{"id": "1000|GARTEN", "data": map[string]any{}})
	if r["items"].(int) != 1 || !strings.Contains(r["message"].(string), "Regel 20 übersprungen: Partner fehlt") {
		t.Fatalf("Rechtsstreit: %v", r)
	}
	// mit Partner und Zeitraum kommt die zweite Regel dazu (ohne Doppel, ohne fremdes Leserecht)
	e.must(viewObject, "update", map[string]any{"id": "1000|GARTEN", "data": map[string]any{"partner_id": "4712", "date_from": "2025-01-01", "date_to": "2025-12-31"}})
	r = e.must(viewObject, "compose", map[string]any{"id": "1000|GARTEN", "data": map[string]any{}})
	if r["run_id"] != "" || r["added"].(int) != 0 || !strings.Contains(r["message"].(string), "0 neue Stände") {
		t.Fatalf("Rechtsstreit mit Partner (nichts Neues, kein neuer Stand): %v", r)
	}
}

func TestPattern(t *testing.T) {
	e := faust(t)
	if _, err := e.call(viewTypeObject, "update", map[string]any{"id": "1000|STEUERBUERO", "data": map[string]any{"file_pattern": "{lfd}_{gibtsnicht}"}}); err == nil {
		t.Fatal("unbekannter Platzhalter")
	}
	if clean(`Rechnung: 12/2025 "Faust"?`, 120) != "Rechnung- 12-2025 -Faust" {
		t.Fatalf("clean: %q", clean(`Rechnung: 12/2025 "Faust"?`, 120))
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
