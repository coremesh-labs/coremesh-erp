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
}

type exampleDef struct {
	Code, Name, Target, Bundle, Binary, FileName, Description string
	Debug                                                     bool
}

var exampleDefs = []exampleDef{
	{"BRIEF", "Beispiel: Brief (PDF)", targetPDF, "brief-beispiel", "bilder-beispiel", "Brief.pdf", "Zwei Briefe in einem PDF; Kette brief-beispiel → latex-brief → latex-basis", false},
	{"DOKUMENT", "Beispiel: Dokument (PDF)", targetPDF, "dokument-beispiel", "bilder-beispiel", "Wirtschaftsplan.pdf", "Dokument mit Kopf- und Fußzeile; Debug: Quelltext und Protokolle am Auftrag", true},
	{"UEBERSICHT", "Beispiel: Mieterliste (HTML)", targetHTML, "html-beispiel", "", "Mieterliste.html", "HTML-Seite mit Tabelle", false},
}

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
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	msg, err := m.setupExamples(ctx, strings.TrimSpace(in.Data.Company))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

func (m *Module) examplesCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company string `json:"company"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	msg, err := m.setupExamples(ctx, strings.TrimSpace(in.Company))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

// setupExamples legt fehlende Beispiel-Bundles und -Vorlagen an (vorhandene
// bleiben unverändert) und je Vorlage einen Auftrag mit „Nicht löschen“.
func (m *Module) setupExamples(ctx context.Context, cc string) (string, error) {
	if cc == "" {
		return "", crud.Invalid("Buchungskreis fehlt")
	}
	if err := requireWrite(ctx, defObject, "create", ""); err != nil {
		return "", err
	}
	var created []string
	for _, b := range exampleBundles {
		if m.text(ctx, `SELECT code FROM template__bundle WHERE code = ?`, b.Code) != "" {
			continue
		}
		files, err := exampleFiles(b.Code)
		if err != nil {
			return "", err
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
		sample, err := exampleFS.ReadFile("examples/data/" + d.Code + ".yaml")
		if err != nil {
			return "", err
		}
		samples[d.Code] = string(sample)
		if m.text(ctx, `SELECT code FROM template__definition WHERE code = ? AND language = ?`, d.Code, "de") != "" {
			continue
		}
		if _, err := m.set.Entity(defObject).Create(ctx, map[string]any{"data": map[string]any{"code": d.Code, "language": "de", "name": d.Name,
			"target": d.Target, "template_bundle": d.Bundle, "binary_bundle": nilIfEmpty(d.Binary), "file_name": d.FileName, "debug": d.Debug,
			"description": d.Description, "sample_data": string(sample)}}); err != nil {
			return "", fmt.Errorf("Vorlage %s: %w", d.Code, err)
		}
		created = append(created, "Vorlage "+d.Code)
	}
	var jobs []string
	for _, d := range exampleDefs {
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
	if len(created) > 0 {
		msg = "Angelegt: " + strings.Join(created, ", ") + ". " + msg
	}
	return msg + ". PDF: Der erste Lauf lädt die TeX-Pakete und dauert einige Minuten.", nil
}
