package templating

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// exampleTex: LaTeX-Quelltext einer Beispielvorlage über die Kette ihrer Bundles
// mit den Beispieldaten (ohne Datenbank).
func exampleTex(t *testing.T, code string) string {
	t.Helper()
	var def *exampleDef
	for i := range exampleDefs {
		if exampleDefs[i].Code == code {
			def = &exampleDefs[i]
		}
	}
	if def == nil {
		t.Fatalf("Vorlage %s fehlt", code)
	}
	parent := map[string]string{}
	for _, b := range exampleBundles {
		parent[b.Code] = b.Parent
	}
	var chain []string
	for c := def.Bundle; c != ""; c = parent[c] {
		chain = append([]string{c}, chain...)
	}
	var files []tmplFile
	for _, c := range chain {
		fs, err := exampleFiles(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			files = append(files, tmplFile{Bundle: c, Name: f.Name, Text: string(f.Data)})
		}
	}
	sample, err := exampleFS.ReadFile("examples/data/" + firstOf(def.Sample, def.Code) + ".yaml")
	if err != nil {
		t.Fatal(err)
	}
	data, err := parseData(string(sample))
	if err != nil {
		t.Fatal(err)
	}
	bin := map[string]bool{}
	extra, err := dataFiles(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range extra {
		bin[f.Name] = true
		if dir := os.Getenv("OC_DUMP_TEX"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644)
		}
	}
	tex, err := execute(engineInput{Target: targetPDF, Language: "de", Job: "J1", Delims: [2]string{"<<", ">>"}, Files: files, Binaries: bin,
		Today: time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC), Data: data})
	if err != nil {
		t.Fatalf("%s: %v", code, err)
	}
	if strings.Contains(tex, "<no value>") || strings.Contains(tex, "<<") {
		t.Errorf("%s: Reste im Quelltext:\n%s", code, tex)
	}
	if dir := os.Getenv("OC_DUMP_TEX"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, code+".tex"), []byte(tex), 0o644)
	}
	return tex
}

// Briefe: Blöcke der Dokumentklasse, Gliederung des Briefs, Serienbrief mit
// Unterschrift, Abrechnungsschreiben mit Übersicht, Aufstellung und Hinweisen.
func TestLetterTemplates(t *testing.T) {
	tex := exampleTex(t, "LETTER")
	mustContain(t, tex, `{scrlttr2}`, `\setkomavar{firsthead}{{\Huge\itshape Erika Beispiel}}`, `IBAN: DE00 0000`, `erika.beispiel@example.org`,
		`\setkomavar{backaddress}{Erika Beispiel, Musterweg 1, 12345 Musterstadt}`, `\begin{letter}{Herr Max Mustermann\\Lindenallee 5\\12345 Musterstadt}`,
		`\opening{Sehr geehrter Herr Mustermann,}`, `\setkomavar{date}{15. November 2026}`, `\setkomavar{place}{Musterstadt}`, `\closing{Mit freundlichen Grüßen}`,
		`\cfoot{Seite \thepage}`)
	if strings.Contains(tex, `\includegraphics`) {
		t.Error("ohne Unterschrift kein Bild")
	}

	tex = exampleTex(t, "SERIAL_LETTER")
	if strings.Count(tex, `\begin{letter}`) != 2 {
		t.Errorf("zwei Briefe erwartet:\n%s", tex)
	}
	mustContain(t, tex, `Frau Monika Muster und Herr Peter Muster\\Lindenallee 5`, `\opening{Sehr geehrte Frau Muster, Sehr geehrter Herr Muster,}`,
		`Beispiel GmbH\\z. Hd. Frau Schulz\\Lindenallee 7`, `\opening{Sehr geehrte Damen und Herren,}`,
		`\includegraphics[height=2cm,angle=-10,smash=br,align=t,vshift=0.5cm]{unterschrift.png}`, `\setplength{sigbeforevskip}{2mm}`,
		`\usepackage{graphbox}`)

	tex = exampleTex(t, "SETTLEMENT_LETTER")
	mustContain(t, tex, `\setkomavar{subject}{Abrechnung Betriebs- und Heizkosten 2025\\Objekt: Lindenallee 5 – 12345 Musterstadt}`,
		`Betriebskosten & 1.332,70 & 1.320,00 & 12,70 & Forderung`, `\textbf{Gesamt} & 4.493,55 & 4.200,00 & 293,55 & Forderung`,
		`Bitte überweisen Sie 293,55 Euro auf das unten angegebene Konto bis zum 30.04.2026.`,
		`Ihr Guthaben von 506,45 Euro überweisen wir auf die uns bekannte Bankverbindung:`,
		`Für die Überweisung Ihres Guthabens von 506,45 Euro teilen Sie uns bitte Ihre Bankverbindung (Kontoinhaber, IBAN) mit.`,
		`Hausmeister & 2.070,60 & 107,30 m² / 747,60 m² $\times$ 365 / 365 Tage & 0,144 & 297,18`,
		`Warmwasser- und Heizkosten & & gesonderte Abrechnung & & 3.160,85`, `\textbf{Betriebskosten} & & insgesamt & & \textbf{1.332,70}`)
	if strings.Contains(tex, "Jobcenter") {
		t.Error("allgemeine Vorlage ohne Hinweise")
	}

	tex = exampleTex(t, "SETTLEMENT_LETTER_UMLAGEN")
	mustContain(t, tex, `\setkomavar{subject}{Abrechnung Umlagen 2025\\Objekt: Lindenallee 5 – 12345 Musterstadt}`,
		`für die gemietete Wohnung „Lindenallee 5 – 12345 Musterstadt“ angefallen sind`, `Ihr Abrechnungszeitraum ist 01.01.2025 bis 31.12.2025.`,
		`Bitte überweisen Sie den offenen Betrag in Höhe von 293,55 Euro auf das unten angegebene Konto bis zum 30.04.2026.`,
		`dem Jobcenter bzw. dem Sozialamt vorgelegt`, `Übersicht der Kosten und Vorauszahlungen`)
	mustContain(t, tex, `Wir überweisen das Guthaben in Höhe von 506,45 Euro bis zum 30.04.2026 an die uns bekannte Bankverbindung:`,
		`IBAN: & DE11 1111 1111 1111 1111 11`, `Kontoinhaber: & Monika Muster`, `Haben Sie inzwischen eine andere Bankverbindung, teilen Sie uns das bitte mit.`,
		`Bitte teilen Sie uns dazu Ihre Bankverbindung (Kontoinhaber, IBAN) mit.`, `506,45 & Guthaben`)
}

// Kostenaufstellung: Übersicht je Gruppe mit Zwischensumme, Belegliste, Rechnungskopie.
func TestCostSheetTemplate(t *testing.T) {
	tex := exampleTex(t, "COST_SHEET")
	mustContain(t, tex, `\usepackage{pdfpages}`, `\setkomavar{subject}{Unterlagen Messdienst 2025\\Objekt: Lindenallee 5 – 12345 Musterstadt}`,
		`\begin{letter}{Muster Messdienst GmbH\\Zählerweg 3\\54321 Messstadt}`, `\opening{Sehr geehrte Damen und Herren,}`,
		`\multicolumn{2}{@{}l}{\textbf{Heizungsbetrieb}}`, `\textit{Zwischensumme} & \textit{512,40}`, `\textbf{Gesamt} & \textbf{4.959,06}`,
		`Heizungsbetrieb & 512,40`, `Kopien der Rechnungen liegen bei`, `\encl{Belegliste\\Rechnungskopien (1)}`,
		`ER-2025-00012\,* & Stadtwerke Musterstadt (4711-2025): Erdgas 2025, 52.380 kWh & 4.210,50`,
		`Ohne Kopie: Beleg 1000000123 (kein PDF im DMS).`, `\includepdf[pages=-,pagecommand={\thispagestyle{empty}}]{anlage-01.pdf}`)
	if strings.Contains(tex, `\textbf{Wartung Heizung}`) { // Konto ohne Belege nicht in der Belegliste
		t.Error("Belegliste mit leerem Konto")
	}
}

// TestAnlageVExample: Vorlage ANLAGE_V – je Objekt eine Seite mit Kopf des Inhabers.
func TestAnlageVExample(t *testing.T) {
	tex := exampleTex(t, "ANLAGE_V")
	for _, want := range []string{"Finanzamt Musterstadt I", "Steuernummer 231/000/00000", "12345 / 678 / 90123",
		"Wohnung OG rechts \\& Garage", "2,00 \\% von 77.409", "\\clearpage", "Verlust"} {
		if !strings.Contains(tex, want) {
			t.Errorf("ANLAGE_V: %q fehlt", want)
		}
	}
}

// TestMieteinnahmenExample: Vorlage MIETEINNAHMEN – Übersicht je Mieter und Monatstabelle.
func TestMieteinnahmenExample(t *testing.T) {
	tex := exampleTex(t, "MIETEINNAHMEN")
	for _, want := range []string{"Summe Objekt", "+ 150,00 (Nachzahlung 2026)", "Max Mustermann\\newline EG links", "Einnahmen und Kosten", "\\leftskip=2em Abschreibungen auf Gebäude & 6221 & 2.400,00", "11.352,40", "\\makecell[tr]{700,00\\\\200,00\\\\\\textbf{900,00}}", "Mär"} {
		if !strings.Contains(tex, want) {
			t.Errorf("MIETEINNAHMEN: %q fehlt", want)
		}
	}
}
