package templating

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Beispiele: Kette latex-basis (Dokument) ← latex-brief (Briefe, Schleife über
// letters) ← brief-beispiel (Inhalt), latex-basis ← dokument-beispiel, dazu
// html-beispiel und bilder-beispiel (Logo). Vorlagen BRIEF, DOKUMENT (beide
// PDF) und UEBERSICHT (HTML), je ein Auftrag mit „Nicht löschen“.
// Briefe (seit 0.5.0): latex-basis ← letter-class ← letter (LETTER,
// SERIAL_LETTER) ← settlement-letter (SETTLEMENT_LETTER) ← settlement-letter-umlagen;
// letter ← cost-sheet (COST_SHEET, seit 0.6.0).
// Berichte (seit 0.6.2): latex-bericht ← bericht-tabelle (BERICHT, Modul script).
// Anlage V (seit 0.7.0): latex-basis ← anlage-v (ANLAGE_V, Hochformat, eine Seite je Objekt).

//go:embed examples
var exampleFS embed.FS

type exampleBundle struct{ Code, Name, Kind, Parent, Description string }

var exampleBundles = []exampleBundle{
	{"latex-basis", "LaTeX-Grundgerüst", kindTemplate, "", "Dokumentklasse, Pakete und Dokument; Blöcke documentclass, preamble, body"},
	{"latex-brief", "Briefe (KOMA-Script scrlttr2)", kindTemplate, "latex-basis", "Absender, Schleife über die Briefe (letters), Blöcke letter und logo"},
	{"brief-beispiel", "Beispiel: Brief neue Vorauszahlungen", kindTemplate, "latex-brief", "Inhalt des Briefs mit Tabelle der Beträge und Logo"},
	{"dokument-beispiel", "Beispiel: Dokument Wirtschaftsplan", kindTemplate, "latex-basis", "Kopf- und Fußzeile mit Logo, Abschnitte mit Tabellen"},
	{"html-beispiel", "Beispiel: HTML-Mieterliste", kindTemplate, "", "Seite mit Block content und Tabelle"},
	{"bilder-beispiel", "Beispiel: Bilder", kindBinary, "", "Logo für die PDF-Beispiele"},
	{"latex-bericht", "Berichte im Querformat", kindTemplate, "latex-basis", "Kopfzeile Name des Geschäftspartners zum Buchungskreis, Fußzeile Seite und Anschrift; Block report"},
	{"vertraege-pdf", "Vertragsübersicht (PDF)", kindTemplate, "latex-bericht", "Je Zeitraum gleicher Konditionen, nach Mietobjekt gruppiert, mit Summen"},
	{"vertraege-excel", "Vertragsübersicht (Excel)", kindTemplate, "", "Excel-XML (SpreadsheetML): ein Blatt je Zeitraum, eine Zeile je Kondition, Summen"},
	{"journal-pdf", "Journal (PDF)", kindTemplate, "latex-bericht", "Belege mit Positionen, Summen je Monat und gesamt"},
	{"bericht-tabelle", "Bericht als Tabelle (PDF)", kindTemplate, "latex-bericht",
		"Berichte aus „Skripte und Berichte“: Titel, Untertitel, Spalten mit Breite, Abschnitte mit Zwischensumme, Zeilenart (fett, Linie), Einrückung"},
	{"anlage-v", "Einkünfte aus Vermietung je Objekt (Hochformat)", kindTemplate, "latex-basis",
		"Bericht ANLAGE_V: je Objekt eine Seite, Kopf mit Inhaber, Finanzamt, Steuernummer; Posten mit Ausweis (Einheit, Vertrag, Konto) und Ergebnis"},
	{"journal-csv", "Journal (CSV)", kindTemplate, "", "Eine Zeile je Position; Semikolon, Dezimalkomma, UTF-8 mit BOM"},
	{"tilgungsplan-pdf", "Tilgungsplan (PDF)", kindTemplate, "latex-bericht", "Kopf mit Vertrag und Summen, je Fälligkeit Zins, Tilgung, Rate, Restschuld"},
	{"letter-class", "Briefe: Dokumentklasse (scrlttr2)", kindTemplate, "latex-basis",
		"Kopf- und Fußzeilen erste und Folgeseiten (HeaderLine…/FootLine…FirstPage/FollowPage), LettersBlock ruft je Brief LetterBlock"},
	{"letter", "Brief und Serienbrief", kindTemplate, "letter-class",
		"LetterBlock gegliedert: AbsenderLine, Adresse (4 Zeilen), OrtDatum, Betreff, Anrede, Inhalt, Gruss, Signatur, Anlagen, Anhang"},
	{"settlement-letter", "Abrechnungsschreiben (allgemein)", kindTemplate, "letter",
		"Datenformat opcost.settlement/1: Einleitung, Uebersicht, Zahlung, Schluss, Aufstellung mit Rechenweg, Hinweise (leer)"},
	{"settlement-letter-umlagen", "Abrechnungsschreiben „Abrechnung Umlagen“", kindTemplate, "settlement-letter",
		"Konkrete Formulierungen: Betreff, Einleitung, Zahlung, Schluss und allgemeine Hinweise"},
	{"cost-sheet", "Kostenaufstellung (Brief)", kindTemplate, "letter",
		"Datenformat opcost.costs/1: Einleitung, Uebersicht je Gruppe, Schluss, Belegliste, Rechnungskopien aus dem DMS"},
}

type exampleDef struct {
	Code, Name, Target, Bundle, Binary, FileName, Description string
	Debug                                                     bool
	Usage, Sample                                             string // Sample: Datei der Beispieldaten (leer = Code)
}

var exampleDefs = []exampleDef{
	{"BRIEF", "Beispiel: Brief (PDF)", targetPDF, "brief-beispiel", "bilder-beispiel", "Brief.pdf", "Zwei Briefe in einem PDF; Kette brief-beispiel → latex-brief → latex-basis", false, "", ""},
	{"DOKUMENT", "Beispiel: Dokument (PDF)", targetPDF, "dokument-beispiel", "bilder-beispiel", "Wirtschaftsplan.pdf", "Dokument mit Kopf- und Fußzeile; Debug: Quelltext und Protokolle am Auftrag", true, "", ""},
	{"UEBERSICHT", "Beispiel: Mieterliste (HTML)", targetHTML, "html-beispiel", "", "Mieterliste.html", "HTML-Seite mit Tabelle", false, "", ""},
	{"VERTRAEGE", "Vertragsübersicht (PDF, quer)", targetPDF, "vertraege-pdf", "", "Vertragsuebersicht.pdf",
		"Report der Vertragsverwaltung: je Zeitraum, nach Mietobjekt, mit Zwischensummen", false, usageContractReport, ""},
	{"VERTRAEGE_XLS", "Vertragsübersicht (Excel)", targetXML, "vertraege-excel", "", "Vertragsuebersicht.xml",
		"Report der Vertragsverwaltung als Excel-Tabelle (XML, öffnet Excel und LibreOffice)", false, usageContractReport, "VERTRAEGE"},
	{"JOURNAL", "Journal (PDF, quer)", targetPDF, "journal-pdf", "", "Journal.pdf",
		"Journal der Buchhaltung: Belege mit Positionen in der gewählten Reihenfolge", false, usageJournal, ""},
	{"JOURNAL_CSV", "Journal (CSV)", targetCSV, "journal-csv", "", "Journal.csv",
		"Journal der Buchhaltung als CSV (eine Zeile je Position)", false, usageJournal, "JOURNAL"},
	{"TILGUNGSPLAN", "Tilgungsplan (PDF, quer)", targetPDF, "tilgungsplan-pdf", "", "Tilgungsplan.pdf",
		"Tilgungsplan eines Darlehensvertrags (Aktion „Tilgungsplan“ am Vertrag)", false, usageLoanSchedule, ""},
	{"BERICHT", "Bericht (PDF, quer)", targetPDF, "bericht-tabelle", "", "Bericht.pdf",
		"Allgemeine Vorlage der Berichte in „Skripte und Berichte“ (Feld Vorlage am Bericht)", false, usageScriptReport, ""},
	{"ANLAGE_V", "Einkünfte aus Vermietung und Verpachtung (PDF)", targetPDF, "anlage-v", "", "AnlageV.pdf",
		"Bericht ANLAGE_V in „Skripte und Berichte“: Überschussrechnung je Objekt für die Steuererklärung", false, usageScriptReport, ""},
	{"LETTER", "Brief (PDF)", targetPDF, "letter", "", "Brief.pdf",
		"Ein Brief; Text aus den Daten, Absender mit Kopf- und Fußzeile, Unterschrift aus files", false, usageLetter, ""},
	{"SERIAL_LETTER", "Serienbrief (PDF)", targetPDF, "letter", "", "Serienbrief.pdf",
		"Je Eintrag in letters ein Brief; gemeinsame Angaben gelten für alle", false, usageLetter, ""},
	{"SETTLEMENT_LETTER", "Abrechnungsschreiben (PDF)", targetPDF, "settlement-letter", "", "Abrechnung.pdf",
		"Je Mieter ein Abrechnungsschreiben mit Aufstellung (allgemeine Texte)", false, usageSettlement, ""},
	{"SETTLEMENT_LETTER_UMLAGEN", "Abrechnungsschreiben „Abrechnung Umlagen“ (PDF)", targetPDF, "settlement-letter-umlagen", "", "Abrechnung.pdf",
		"Je Mieter ein Abrechnungsschreiben mit eigenen Formulierungen und allgemeinen Hinweisen", false, usageSettlement, "SETTLEMENT_LETTER"},
	{"COST_SHEET", "Kostenaufstellung (PDF)", targetPDF, "cost-sheet", "", "Kostenaufstellung.pdf",
		"Schreiben an den Empfänger des Regelwerks (z. B. Messdienst): Kosten je Gruppe, Belegliste, Rechnungskopien", false, usageCosts, ""},
}

// Verwendungen: Vorlagen für den Report der Vertragsverwaltung bzw. das Journal der Buchhaltung.
const (
	usageContractReport = "CONTRACT_REPORT"
	usageJournal        = "JOURNAL"
	usageLoanSchedule   = "LOAN_SCHEDULE"
	usageLetter         = "LETTER"
	usageSettlement     = "OPCOST_SETTLEMENT"
	usageCosts          = "OPCOST_COSTS"
	usageScriptReport   = "SCRIPT_REPORT"
)

func exampleFiles(code string) ([]zipFile, error) {
	var out []zipFile
	root := path.Join("examples", code)
	err := fs.WalkDir(exampleFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := exampleFS.ReadFile(p)
		if err != nil {
			return err
		}
		out = append(out, zipFile{Name: strings.TrimPrefix(p, root+"/"), Data: b})
		return nil
	})
	sortFiles(out)
	return out, err
}

func (m *Module) examplesAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Company string `json:"job_company"`
			Update  any    `json:"ex_update"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	msg, err := m.setupExamples(ctx, strings.TrimSpace(in.Data.Company), crud.AsBool(in.Data.Update))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

func (m *Module) examplesCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company string `json:"company"`
		Update  any    `json:"update"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	msg, err := m.setupExamples(ctx, strings.TrimSpace(in.Company), crud.AsBool(in.Update))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

// setupExamples legt fehlende Beispiel-Bundles und -Vorlagen an und je Vorlage
// einen Auftrag mit „Nicht löschen“ (falls es keinen gibt). Vorhandene Bundles
// bleiben, außer update: dann bekommen sie den Inhalt dieser Version (eigene
// Änderungen daran gehen verloren; Vorlagen und eigene Bundles bleiben).
func (m *Module) setupExamples(ctx context.Context, cc string, update bool) (string, error) {
	if cc == "" {
		return "", crud.Invalid("Buchungskreis fehlt")
	}
	if err := requireWrite(ctx, defObject, "create", ""); err != nil {
		return "", err
	}
	var created []string
	for _, b := range exampleBundles {
		files, err := exampleFiles(b.Code)
		if err != nil {
			return "", err
		}
		if m.text(ctx, `SELECT code FROM template__bundle WHERE code = ?`, b.Code) != "" {
			if update {
				if err := m.setBundleFiles(ctx, b.Code, files); err != nil {
					return "", err
				}
				created = append(created, "Bundle "+b.Code+" aktualisiert")
			}
			continue
		}
		if _, err := m.set.Entity(bundleObject).Create(ctx, map[string]any{"data": map[string]any{"code": b.Code, "name": b.Name, "kind": b.Kind,
			"parent": nilIfEmpty(b.Parent), "description": b.Description}}); err != nil {
			return "", fmt.Errorf("Bundle %s: %w", b.Code, err)
		}
		if err := m.setBundleFiles(ctx, b.Code, files); err != nil {
			return "", err
		}
		created = append(created, "Bundle "+b.Code)
	}
	// erst alle Vorlagen, dann die Aufträge (die im Hintergrund schreiben)
	samples := map[string]string{}
	for _, d := range exampleDefs {
		sample, err := exampleFS.ReadFile("examples/data/" + firstOf(d.Sample, d.Code) + ".yaml")
		if err != nil {
			return "", err
		}
		samples[d.Code] = string(sample)
		if m.text(ctx, `SELECT code FROM template__definition WHERE code = ? AND language = ?`, d.Code, "de") != "" {
			continue
		}
		if _, err := m.set.Entity(defObject).Create(ctx, map[string]any{"data": map[string]any{"code": d.Code, "language": "de", "name": d.Name,
			"target": d.Target, "template_bundle": d.Bundle, "binary_bundle": nilIfEmpty(d.Binary), "file_name": d.FileName, "debug": d.Debug,
			"description": d.Description, "sample_data": string(sample), "usage": nilIfEmpty(d.Usage)}}); err != nil {
			return "", fmt.Errorf("Vorlage %s: %w", d.Code, err)
		}
		created = append(created, "Vorlage "+d.Code)
	}
	var jobs []string
	for _, d := range exampleDefs {
		if m.text(ctx, `SELECT job_number FROM template__job WHERE template = ? AND language = ? AND company_code = ? AND keep = ?`, d.Code, "de", cc, true) != "" {
			continue
		}
		data, err := parseData(samples[d.Code])
		if err != nil {
			return "", err
		}
		nr, err := m.render(ctx, renderRequest{Template: d.Code, Language: "de", CompanyCode: cc, Data: data, Keep: true})
		if err != nil {
			return "", fmt.Errorf("Auftrag %s: %w", d.Code, err)
		}
		jobs = append(jobs, nr+" ("+d.Code+")")
	}
	msg := "Aufträge mit „Nicht löschen“: " + strings.Join(jobs, ", ")
	if len(jobs) == 0 {
		msg = "Beispielaufträge gibt es schon"
	}
	if len(created) > 0 {
		msg = "Angelegt: " + strings.Join(created, ", ") + ". " + msg
	}
	return msg + ". PDF: Der erste Lauf lädt die TeX-Pakete und dauert einige Minuten.", nil
}
