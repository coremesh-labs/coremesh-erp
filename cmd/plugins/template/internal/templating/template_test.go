package templating

import (
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func mustContain(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("fehlt: %q", p)
		}
	}
}

// Beispiele: Bundles, Vorlagen und drei Aufträge mit „Nicht löschen“; PDF über
// die Kette mit Briefschleife und Logo, HTML maskiert.
func TestExamples(t *testing.T) {
	e := setup(t)
	out := e.must(defObject, "examples", map[string]any{"data": map[string]any{"job_company": "1000"}})
	mustContain(t, fmt.Sprint(out["message"]), "Bundle latex-basis", "Vorlage BRIEF", "1000-2026-000001 (BRIEF)", "1000-2026-000003 (UEBERSICHT)")

	// BRIEF: zwei Briefe, Kette und Blöcke, Beträge, Logo im Arbeitsverzeichnis
	brief := e.jobRow("1000-2026-000001")
	if brief["status"] != stDone || brief["keep"] != true || brief["file_name"] != "Brief.pdf" {
		t.Fatalf("BRIEF: %v", brief)
	}
	tex := e.tex.tex[0]
	if strings.Count(tex, `\begin{letter}`) != 2 {
		t.Errorf("zwei Briefe erwartet:\n%s", tex)
	}
	mustContain(t, tex, `\documentclass[a4paper,11pt,parskip=half,fromalign=right`, `\usepackage[ngerman]{babel}`,
		`\setkomavar{fromname}{Hausverwaltung Musterstadt GmbH}`, `\includegraphics[width=35mm]{logo.png}`,
		`\setkomavar{date}{15. November 2026}`, `Erika Mustermann\\Lindenallee 5\\12345 Musterstadt`, `Max Mustermann\\c/o Familie Beispiel`,
		`Betriebskosten & 185,00 EUR`, `\textbf{277,50 EUR}`, `\textbf{228,50 EUR}`, `\encl{Betriebskostenabrechnung 2025}`,
		`\opening{Sehr geehrte Frau Mustermann,}`, `\closing{Mit freundlichen Grüßen}`)
	if strings.Contains(tex, "<no value>") || strings.Contains(tex, "<<") {
		t.Errorf("Reste im Quelltext:\n%s", tex)
	}
	if !slices.Contains(e.tex.files[0], "logo.png") {
		t.Errorf("Logo fehlt im Arbeitsverzeichnis: %v", e.tex.files[0])
	}
	if f := e.jobFiles("1000-2026-000001"); len(f) != 0 {
		t.Errorf("ohne Debug keine Anhänge: %v", f)
	}
	got := e.must(serviceObject, "get", map[string]any{"job_number": "1000-2026-000001"})
	if b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(got["content"])); string(b) != "%PDF-1.5 fake" || got["content_type"] != "application/pdf" {
		t.Errorf("get: %v", got)
	}
	if s := e.h.statuses("1000-2026-000001"); !slices.Equal(s, []string{stNew, stRunning, stDone}) {
		t.Errorf("Events: %v", s)
	}

	// DOKUMENT: Debug → Quelltext und Protokolle am Auftrag
	mustContain(t, e.tex.tex[1], `\ohead{\includegraphics[height=10mm]{logo.png}}`, `Wasser und Abwasser & 4.850,00 EUR`, `\textbf{15.090,50 EUR}`,
		`Stand: 1. November 2026`, `Auftrag 1000-2026-000002`)
	if f := e.jobFiles("1000-2026-000002"); !slices.Equal(f, []string{"main.log", "main.tex", "tectonic.txt"}) {
		t.Errorf("Debug-Anhänge: %v", f)
	}

	// UEBERSICHT: HTML, maskiert, Summen
	html := e.must(serviceObject, "get", map[string]any{"job_number": "1000-2026-000003"})
	b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(html["content"]))
	mustContain(t, string(b), `<html lang="de">`, "<td>– frei –</td>", `<td class="num">68,50</td>`, `<th class="num">258,90</th>`,
		`<th class="num">2.353,00 EUR</th>`, "Auftrag 1000-2026-000003")
	if html["content_type"] != "text/html; charset=utf-8" {
		t.Errorf("HTML: %v", html["content_type"])
	}
	// Viewer: HTML als Quelltext, PDF wie es ist
	c := e.must(jobObject, "content", map[string]any{"id": "1000-2026-000003"})
	if c["content_type"] != "text/plain; charset=utf-8" || c["file_name"] != "Mieterliste.html" {
		t.Errorf("Viewer HTML: %v %v", c["content_type"], c["file_name"])
	}

	// Vertragsübersicht: Querformat, Kopfzeile mit Einheit und Vertragsart, Summe je oberster Einheit, Monatsübersicht
	mustContain(t, e.tex.tex[2], `\documentclass[a4paper,landscape,10pt,parskip=half]{scrartcl}`, `\usepackage{extramarks}`,
		`\ihead{\textbf{Hausverwaltung Musterstadt GmbH}}`, `\chead{\ifnum\value{page}=1 \textbf{\firstleftxmark}\quad \firstrightxmark\else\textbf{\topleftxmark}\quad \toprightxmark\fi}`, `\ofoot{Beispielweg 12, 12345 Musterstadt}`,
		`\noalign{\extramarks{Gebäude LIN5 Lindenallee 5}{Wohnraummiete}}`, `\multicolumn{8}{@{}l}{\large\textbf{Gebäude LIN5 Lindenallee 5}}`,
		`\quad Kaltmiete & & & 01.01.2027 – 30.06.2027 & 850,00 & monatlich & 5.100,00 &  \tabularnewline`,
		`\quad Heizkosten-Vorauszahlung & & &  & 150,00 & monatlich & 1.800,00 &`, `01.01.2026 – unbefristet`,
		`\textbf{Summe Gebäude LIN5 Lindenallee 5}} & \textbf{14.370,00} & \textbf{1.140,00}`, `\extramarks{Monatsübersicht}{}`,
		`Juli 2027 & 2 & 1.210,00 & 95,00 & 1.115,00`)
	if v := e.jobRow("1000-2026-000004"); v["status"] != stDone || v["file_name"] != "Vertragsuebersicht.pdf" {
		t.Errorf("VERTRAEGE: %v", v)
	}
	// Excel: wohlgeformtes XML, ein Blatt je Zeitraum, Zahlen ohne Tausenderpunkt
	xls := e.must(serviceObject, "get", map[string]any{"job_number": "1000-2026-000005"})
	xb, _ := base64.StdEncoding.DecodeString(fmt.Sprint(xls["content"]))
	if err := xml.Unmarshal(xb, new(struct{})); err != nil || !strings.HasPrefix(string(xb), "<?xml") {
		t.Fatalf("Excel-XML: %v\n%s", err, xb)
	}
	mustContain(t, string(xb), `<Worksheet ss:Name="Verträge">`, `<Worksheet ss:Name="Monate">`,
		`<Data ss:Type="Number">875</Data>`, `<Data ss:Type="Number">5250</Data>`, `<Data ss:Type="DateTime">2027-12-31T00:00:00.000</Data>`,
		`&amp;LHausverwaltung Musterstadt GmbH`, `Summe Gebäude LIN5 Lindenallee 5`, `<Data ss:Type="Number">13230</Data>`)
	if strings.Count(string(xb), "<Worksheet ") != 2 || strings.Count(string(xb), "keine Konditionen") != 0 {
		t.Errorf("Blätter: %d", strings.Count(string(xb), "<Worksheet "))
	}
	if d := e.must(defObject, "get", map[string]any{"id": "VERTRAEGE_XLS|de"}); d["usage"] != usageContractReport || d["target"] != targetXML {
		t.Errorf("Verwendung: %v", d)
	}

	// Journal: PDF mit Belegköpfen und Positionen, CSV mit BOM und einer Zeile je Position
	mustContain(t, e.tex.tex[3], `\large\textbf{Januar 2027}`, `\noalign{\extramarks{Januar 2027}{01.01.2027}}`, `\multicolumn{7}{@{}l}{\textbf{15.01.2027}}`,
		`1500000001 & DR & \multicolumn{3}`, `\textbf{Sollstellung Miete Januar} · SOLL-01-MV-0001\enspace{\footnotesize Buchung 01.01.2027`,
		`& & 1200 & Forderungen aus Vermietung &  & 1.185,00 &  \tabularnewline`, `Vorauszahlungen BK \& HK`,
		`\textbf{Summe (2 Belege)}} & \textbf{1.435,00} & \textbf{1.435,00}`)
	csv := e.must(serviceObject, "get", map[string]any{"job_number": "1000-2026-000007"})
	cb, _ := base64.StdEncoding.DecodeString(fmt.Sprint(csv["content"]))
	lines := strings.Split(strings.TrimRight(string(cb), "\n"), "\n")
	if !strings.HasPrefix(string(cb), "\ufeffBelegnummer;Geschäftsjahr;Datum;") || len(lines) != 6 ||
		lines[1] != "1500000001;2027;01.01.2027;01.01.2027;01.01.2027;02.01.2027 09:15;DR;SOLL-01-MV-0001;Sollstellung Miete Januar;1;1200;Forderungen aus Vermietung;1.185,00;0,00;;LIN5-EG-L;" ||
		!strings.Contains(lines[5], `;"Reinigung; Treppenhaus";;BP-1002`) {
		t.Errorf("CSV:\n%s", cb)
	}

	// zweiter Aufruf: nichts doppelt, keine weiteren Aufträge
	out = e.must(defObject, "examples", map[string]any{"data": map[string]any{"job_company": "1000"}})
	if msg := fmt.Sprint(out["message"]); strings.Contains(msg, "Angelegt") || !strings.Contains(msg, "gibt es schon") {
		t.Errorf("zweiter Aufruf: %v", out["message"])
	}
	// Aktualisieren: geänderter Inhalt eines Beispiel-Bundles kommt zurück
	if err := e.m.setBundleFiles(e.ctx, "latex-basis", []zipFile{{Name: "x.text", Data: []byte("eigen")}}); err != nil {
		t.Fatal(err)
	}
	out = e.must(defObject, "examples", map[string]any{"data": map[string]any{"job_company": "1000", "ex_update": true}})
	if b, _ := e.m.loadBundle(e.ctx, "latex-basis"); len(b.Files) != 1 || b.Files[0].Name != "00-dokument.text" || !strings.Contains(fmt.Sprint(out["message"]), "latex-basis aktualisiert") {
		t.Errorf("aktualisiert: %v %v", b.Files, out["message"])
	}
}

// Kette: das konkrete Bundle überschreibt Blöcke der allgemeineren; Dateien
// ohne passende Endung zählen nicht; HTML-Werte werden maskiert.
func TestChainOverride(t *testing.T) {
	e := setup(t)
	e.bundle("a", kindTemplate, "", map[string]string{"00-root.text": `{{block "x" .}}A{{end}}-{{block "y" .}}Y{{end}}-{{.v}}`,
		"10-more.text": `{{define "y"}}Y2{{end}}`, "notes.md": "ignoriert"})
	e.bundle("b", kindTemplate, "a", map[string]string{"10-b.text": `{{define "x"}}B{{end}}`})
	e.bundle("c", kindTemplate, "b", map[string]string{"20-c.text": `{{define "y"}}C{{csvRow .list}}{{end}}`})
	e.must(defObject, "create", map[string]any{"data": map[string]any{"code": "CSV1", "language": "de", "name": "CSV", "target": targetCSV, "template_bundle": "c"}})
	out := e.must(serviceObject, "render", map[string]any{"template": "CSV1", "language": "de", "company_code": "1000",
		"data": map[string]any{"v": "x;y", "list": []any{"a;b", 1.5, `q"`}}})
	got := e.must(serviceObject, "get", map[string]any{"job_number": out["job_number"]})
	b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(got["content"]))
	if string(b) != `B-C"a;b";1.5;"q"""-x;y` || got["file_name"] != "CSV1.csv" {
		t.Errorf("CSV: %q %v", b, got["file_name"])
	}

	// Kreis in der Kette
	_, err := e.call(bundleObject, "update", map[string]any{"id": "a", "data": map[string]any{"parent": "c"}})
	if !errors.Is(err, sdk.ErrInvalidArgument) || !strings.Contains(err.Error(), "Kreis") {
		t.Errorf("Kreis: %v", err)
	}
	// Binärdateien nur für PDF, Bundle-Art passend
	e.bundle("img", kindBinary, "", map[string]string{"logo.png": "x"})
	_, err = e.call(defObject, "create", map[string]any{"data": map[string]any{"code": "H", "language": "de", "name": "H", "target": targetHTML,
		"template_bundle": "c", "binary_bundle": "img"}})
	if !errors.Is(err, sdk.ErrInvalidArgument) {
		t.Errorf("Binärdateien bei HTML: %v", err)
	}
	_, err = e.call(defObject, "create", map[string]any{"data": map[string]any{"code": "H", "language": "de", "name": "H", "target": targetHTML, "template_bundle": "img"}})
	if !errors.Is(err, sdk.ErrInvalidArgument) {
		t.Errorf("Binär-Bundle als Template: %v", err)
	}

	// HTML: keine .html-Datei → Fehler am Auftrag; mit Datei maskiert
	e.must(defObject, "create", map[string]any{"data": map[string]any{"code": "H", "language": "en", "name": "H", "target": targetHTML, "template_bundle": "c"}})
	out = e.must(serviceObject, "render", map[string]any{"template": "H", "language": "en", "company_code": "1000", "data": map[string]any{}})
	if j := e.jobRow(fmt.Sprint(out["job_number"])); j["status"] != stError || !strings.Contains(fmt.Sprint(j["status_text"]), "keine .html-Dateien") {
		t.Errorf("ohne .html: %v", j)
	}
	e.bundle("h", kindTemplate, "", map[string]string{"page.html": `<p>{{.v}} {{fmtMoney .n}} {{fmtDate "2026-10-11"}}</p>`})
	e.must(defObject, "update", map[string]any{"id": "H|en", "data": map[string]any{"template_bundle": "h"}})
	out = e.must(serviceObject, "render", map[string]any{"template": "H", "language": "en", "company_code": "1000", "data": map[string]any{"v": "<b>&", "n": 1234.5}})
	got = e.must(serviceObject, "get", map[string]any{"job_number": out["job_number"]})
	b, _ = base64.StdEncoding.DecodeString(fmt.Sprint(got["content"]))
	if string(b) != `<p>&lt;b&gt;&amp; 1,234.50 2026-10-11</p>` {
		t.Errorf("HTML: %q", b)
	}

	// unbekannte Vorlage, fehlender Buchungskreis, fehlendes Recht
	if _, err := e.call(serviceObject, "render", map[string]any{"template": "X", "company_code": "1000"}); !errors.Is(err, sdk.ErrNotFound) {
		t.Errorf("unbekannte Vorlage: %v", err)
	}
	if _, err := e.call(serviceObject, "render", map[string]any{"template": "CSV1"}); !errors.Is(err, sdk.ErrInvalidArgument) {
		t.Errorf("ohne Buchungskreis: %v", err)
	}
	e.h.deny[jobObject+".render"] = true
	if _, err := e.call(serviceObject, "render", map[string]any{"template": "CSV1", "language": "de", "company_code": "1000"}); !errors.Is(err, sdk.ErrPermissionDenied) {
		t.Errorf("ohne Recht: %v", err)
	}
}

// PDF-Fehler: Quelltext und Protokolle am Auftrag; Netzfehler wird wiederholt.
func TestPDFErrorAndRetry(t *testing.T) {
	e := setup(t)
	e.bundle("t", kindTemplate, "", map[string]string{"00.text": `\documentclass{article}\begin{document}<< tex .v >>\end{document}`})
	e.must(defObject, "create", map[string]any{"data": map[string]any{"code": "P", "language": "de", "name": "P", "target": targetPDF, "template_bundle": "t"}})

	e.tex.answer = func(int) (string, error) {
		return "error: main.tex:1: Undefined control sequence", errors.New("exit status 1")
	}
	out := e.must(serviceObject, "render", map[string]any{"template": "P", "language": "de", "company_code": "1000", "data": map[string]any{"v": "50% & mehr"}})
	nr := fmt.Sprint(out["job_number"])
	j := e.jobRow(nr)
	if j["status"] != stError || !strings.Contains(fmt.Sprint(j["status_text"]), "Undefined control sequence") {
		t.Fatalf("Fehler: %v", j)
	}
	if f := e.jobFiles(nr); !slices.Equal(f, []string{"main.log", "main.tex", "tectonic.txt"}) {
		t.Errorf("Anhänge: %v", f)
	}
	if !strings.Contains(e.tex.tex[0], `50\% \& mehr`) || e.tex.calls != 1 {
		t.Errorf("Quelltext/Versuche: %q %d", e.tex.tex[0], e.tex.calls)
	}
	if s := e.h.statuses(nr); !slices.Equal(s, []string{stNew, stRunning, stError}) {
		t.Errorf("Events: %v", s)
	}
	// Viewer eines Auftrags ohne Ergebnis: Meldung als Text
	c := e.must(jobObject, "content", map[string]any{"id": nr})
	if b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(c["content"])); !strings.Contains(string(b), "ERROR") {
		t.Errorf("Viewer: %s", b)
	}

	e.tex.calls = 0
	e.tex.answer = func(call int) (string, error) {
		if call == 1 {
			return "error: failed to download https://relay.fullyjustified.net/…", errors.New("exit status 1")
		}
		return "", nil
	}
	out = e.must(serviceObject, "render", map[string]any{"template": "P", "language": "de", "company_code": "1000", "data": map[string]any{"v": "x"}})
	if j := e.jobRow(fmt.Sprint(out["job_number"])); j["status"] != stDone || e.tex.calls != 2 {
		t.Errorf("Wiederholung: %v, %d Läufe", j["status"], e.tex.calls)
	}
}

// Bundles: hochladen (ergänzt), ersetzen, herunterladen, entpacken, einlesen, kopieren.
func TestBundleFiles(t *testing.T) {
	e := setup(t)
	e.bundle("b", kindTemplate, "", map[string]string{"00.text": "alt", "10.text": "zehn"})
	zipOf := func(files ...zipFile) string {
		raw, err := writeZip(files)
		if err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(raw)
	}
	out := e.must(bundleObject, "upload", map[string]any{"id": "b", "data": map[string]any{"upload_file": zipOf(zipFile{"00.text", []byte("neu")},
		zipFile{"20.text", []byte("zwanzig")}, zipFile{"bild.png", []byte("x")}), "upload_file_name": "neu.zip"}})
	mustContain(t, fmt.Sprint(out["message"]), "ergänzt", "jetzt 4 Dateien", "1 ohne .html, .xml oder .text")
	b, err := e.m.loadBundle(e.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Files) != 4 || string(b.Files[0].Data) != "neu" {
		t.Errorf("ergänzt: %v", b.Files)
	}
	if rec := e.must(bundleObject, "get", map[string]any{"id": "b"}); rec["files"] != 4.0 || rec["file_list"] != "00.text\n10.text\n20.text\nbild.png" {
		t.Errorf("Inhalt: %v %v", rec["files"], rec["file_list"])
	}
	// unsichere Namen
	if _, err := e.call(bundleObject, "upload", map[string]any{"id": "b", "data": map[string]any{"upload_file": zipOf(zipFile{"../x.text", []byte("x")})}}); !errors.Is(err, sdk.ErrInvalidArgument) {
		t.Errorf("Zip mit ..: %v", err)
	}
	if _, err := e.call(bundleObject, "upload", map[string]any{"id": "b", "data": map[string]any{"upload_file": base64.StdEncoding.EncodeToString([]byte("kein zip"))}}); !errors.Is(err, sdk.ErrInvalidArgument) {
		t.Errorf("kein Zip: %v", err)
	}
	// herunterladen
	dl := e.must(bundleObject, "download", map[string]any{"id": "b"})
	d, _ := dl["download"].(map[string]any)
	raw, _ := base64.StdEncoding.DecodeString(fmt.Sprint(d["content"]))
	if files, err := readZip(raw); err != nil || len(files) != 4 || d["file_name"] != "b.zip" {
		t.Errorf("Download: %v %v", err, d["file_name"])
	}
	// entpacken, auf der Festplatte ändern, einlesen
	dir := filepath.Join(e.dir, "b")
	e.must(bundleObject, "export", map[string]any{"id": "b", "data": map[string]any{"dir": dir}})
	if s, _ := os.ReadFile(filepath.Join(dir, "20.text")); string(s) != "zwanzig" {
		t.Errorf("entpackt: %q", s)
	}
	_ = os.Remove(filepath.Join(dir, "bild.png"))
	_ = os.WriteFile(filepath.Join(dir, "30.text"), []byte("dreißig"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".swp"), []byte("versteckt"), 0o644)
	_ = os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sub", "40.text"), []byte("vierzig"), 0o644)
	e.must(bundleObject, "import", map[string]any{"id": "b", "data": map[string]any{"dir": dir}})
	b, _ = e.m.loadBundle(e.ctx, "b")
	var names []string
	for _, f := range b.Files {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"00.text", "10.text", "20.text", "30.text", "sub/40.text"}) {
		t.Errorf("eingelesen: %v", names)
	}
	// nur unter den freigegebenen Verzeichnissen
	for _, bad := range []string{t.TempDir(), filepath.Join(e.dir, "..", "x")} {
		if _, err := e.call(bundleObject, "export", map[string]any{"id": "b", "data": map[string]any{"dir": bad}}); !errors.Is(err, sdk.ErrInvalidArgument) {
			t.Errorf("Verzeichnis %s: %v", bad, err)
		}
	}
	// Vorschlag des Verzeichnisses
	st := e.must(bundleObject, "formState", map[string]any{"mode": "action", "action": "export", "id": "b"})
	if f, _ := st["fields"].(map[string]any); fmt.Sprint(f["dir"]) != fmt.Sprint(map[string]any{"value": dir}) {
		t.Errorf("Vorschlag: %v", st)
	}
	// kopieren
	e.must(bundleObject, "copy", map[string]any{"id": "b", "data": map[string]any{"new_code": "b2"}})
	if c, _ := e.m.loadBundle(e.ctx, "b2"); c == nil || len(c.Files) != 5 || c.Name != "b (Kopie)" {
		t.Errorf("Kopie: %v", c)
	}
	// Vorlage kopieren (andere Sprache)
	e.must(defObject, "create", map[string]any{"data": map[string]any{"code": "T", "language": "de", "name": "T", "target": targetCSV, "template_bundle": "b", "debug": true}})
	e.must(defObject, "copy", map[string]any{"id": "T|de", "data": map[string]any{"new_code": "T", "new_language": "en", "new_name": "T en"}})
	if c := e.must(defObject, "get", map[string]any{"id": "T|en"}); c["template_bundle"] != "b" || c["debug"] != true || c["name"] != "T en" {
		t.Errorf("Kopie der Vorlage: %v", c)
	}
}

// Aufbewahrung: Fristen je Buchungskreis (Erfolg, Fehler), „Nicht löschen“ bleibt.
func TestRetention(t *testing.T) {
	e := setup(t)
	e.bundle("t", kindTemplate, "", map[string]string{"00.text": `ok`})
	e.must(defObject, "create", map[string]any{"data": map[string]any{"code": "C", "language": "de", "name": "C", "target": targetCSV, "template_bundle": "t"}})
	e.must(companyObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "keep_success_days": 2, "keep_error_days": 5}})
	var nrs []string
	for i := 0; i < 4; i++ {
		out := e.must(serviceObject, "render", map[string]any{"template": "C", "language": "de", "company_code": "1000", "keep": i == 3})
		nrs = append(nrs, fmt.Sprint(out["job_number"]))
	}
	old := func(nr, status string, days int) {
		if _, err := e.m.db.Exec(e.ctx, `UPDATE template__job SET status = ?, finished_at = ? WHERE job_number = ?`, status,
			time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339), nr); err != nil {
			t.Fatal(err)
		}
	}
	old(nrs[0], stDone, 3)  // abgelaufen
	old(nrs[1], stError, 3) // Fehler: 5 Tage
	old(nrs[2], stDone, 1)  // noch in der Frist
	old(nrs[3], stDone, 30) // Nicht löschen
	if j := e.jobRow(nrs[2]); j["expires_on"] != time.Now().AddDate(0, 0, 1).Format("02.01.2006") {
		t.Errorf("Löschen ab: %v", j["expires_on"])
	}
	e.m.lastClean = time.Time{}
	e.must(jobObject, "list", map[string]any{"query": map[string]any{}})
	for i, want := range []bool{false, true, true, true} {
		_, err := e.call(jobObject, "get", map[string]any{"id": nrs[i]})
		if (err == nil) != want {
			t.Errorf("Auftrag %d: vorhanden=%v, erwartet %v", i, err == nil, want)
		}
	}
	// Nicht löschen nachträglich setzen
	e.must(jobObject, "update", map[string]any{"id": nrs[2], "data": map[string]any{"keep": true}})
	if j := e.jobRow(nrs[2]); j["keep"] != true || j["expires_on"] != nil {
		t.Errorf("Nicht löschen: %v", j)
	}
}

func TestFuncs(t *testing.T) {
	for _, c := range []struct {
		lang string
		n    float64
		d    int
		want string
	}{{"de", 1234567.891, 2, "1.234.567,89"}, {"en", -1234.5, 2, "-1,234.50"}, {"de", 0.004, 2, "0,00"}, {"de", 999, 0, "999"}} {
		if got := formatNumber(c.lang, c.n, c.d); got != c.want {
			t.Errorf("formatNumber(%s, %v): %q, erwartet %q", c.lang, c.n, got, c.want)
		}
	}
	if got := texEscape(`a\b{c}$&#_%~^`); got != `a\textbackslash{}b\{c\}\$\&\#\_\%\textasciitilde{}\textasciicircum{}` {
		t.Errorf("tex: %s", got)
	}
	if got := texLines("Zeile 1\nZeile 2\n\nAbsatz & mehr"); got != "Zeile 1\\\\\nZeile 2\n\nAbsatz \\& mehr" {
		t.Errorf("texLines: %q", got)
	}
	for lang, want := range map[string]string{"de": "1. März 2027", "en": "March 1, 2027", "zh-CN": "2027年3月1日"} {
		if got, _ := formatDate(lang, "2027-03-01", true); got != want {
			t.Errorf("fmtDateLong %s: %q", lang, got)
		}
	}
	if n, err := number("1.234,50"); err != nil || n != 1234.5 {
		t.Errorf("number: %v %v", n, err)
	}
	l := letters(map[string]any{"sender": "S", "letters": []any{map[string]any{"to": "A"}, map[string]any{"to": "B", "sender": "X"}}})
	if fmt.Sprint(l) != "[map[sender:S to:A] map[sender:X to:B]]" {
		t.Errorf("letters: %v", l)
	}
	if fmt.Sprint(letters(map[string]any{"to": "A"})) != "[map[to:A]]" {
		t.Errorf("ein Brief")
	}
	if _, ok := sprigFuncs(false)["env"]; ok {
		t.Errorf("env darf nicht verfügbar sein")
	}
}
