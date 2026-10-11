package templating

import (
	"bytes"
	"context"
	"fmt"
	htmltemplate "html/template"
	"path"
	"strings"
	texttemplate "text/template"
	"time"
)

// Kette der Bundles: Die Vorlage nennt das konkreteste Bundle; parent verweist
// auf das nächst allgemeinere bis zum Bundle, das das Dokument aufbaut (z. B.
// LaTeX-Grundgerüst). Zusammengebaut wird in der anderen Richtung: erst das
// allgemeinste, dann die konkreteren – deren {{define}} ersetzt die Blöcke
// ({{block}}) der allgemeineren. Ausgeführt wird die erste Datei des
// allgemeinsten Bundles.

const maxChain = 20

// templateExt: Endung einer Template-Datei (.html, .xml, .text), sonst "".
func templateExt(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".html":
		return ".html"
	case ".xml":
		return ".xml"
	case ".text":
		return ".text"
	}
	return ""
}

// extFor: Endung der Templates je Ausgabe.
func extFor(target string) string {
	switch target {
	case targetHTML:
		return ".html"
	case targetXML:
		return ".xml"
	}
	return ".text"
}

// tmplFile ist eine Template-Datei der Kette.
type tmplFile struct {
	Bundle, Name, Text string
}

// chain: Bundles vom allgemeinsten zum konkretesten.
func (m *Module) chain(ctx context.Context, code string) ([]*bundleRow, error) {
	var out []*bundleRow
	seen := map[string]bool{}
	for c := code; c != ""; {
		if seen[c] || len(out) >= maxChain {
			return nil, fmt.Errorf("Kette der Bundles ab %s bildet einen Kreis oder ist zu lang", code)
		}
		seen[c] = true
		b, err := m.loadBundle(ctx, c)
		if err != nil {
			return nil, err
		}
		if b.Kind != kindTemplate {
			return nil, fmt.Errorf("Bundle %s enthält keine Go-Templates", c)
		}
		if !b.Active {
			return nil, fmt.Errorf("Bundle %s ist inaktiv", c)
		}
		out = append([]*bundleRow{b}, out...)
		c = b.Parent
	}
	return out, nil
}

// chainFiles: Template-Dateien der Ausgabe in der Reihenfolge des Zusammenbaus.
func chainFiles(bundles []*bundleRow, target string) ([]tmplFile, error) {
	ext := extFor(target)
	var out []tmplFile
	for _, b := range bundles {
		for _, f := range b.Files { // nach Namen sortiert
			if templateExt(f.Name) == ext {
				out = append(out, tmplFile{Bundle: b.Code, Name: f.Name, Text: string(f.Data)})
			}
		}
	}
	if len(out) == 0 {
		names := make([]string, len(bundles))
		for i, b := range bundles {
			names[i] = b.Code
		}
		return nil, fmt.Errorf("keine %s-Dateien in den Bundles %s", ext, strings.Join(names, " → "))
	}
	return out, nil
}

// engineInput: alles für einen Lauf.
type engineInput struct {
	Target, Language, Job string
	Delims                [2]string
	Files                 []tmplFile
	Binaries              map[string]bool
	Today                 time.Time
	Data                  map[string]any
}

func defaultDelims(target, configured string) ([2]string, error) {
	if strings.TrimSpace(configured) != "" {
		l, r, err := parseDelims(configured)
		return [2]string{l, r}, err
	}
	if target == targetPDF {
		return [2]string{"<<", ">>"}, nil
	}
	return [2]string{"{{", "}}"}, nil
}

// rootName: Wurzel des Template-Satzes – nicht der Name einer Datei (html/template
// verlöre sonst beim gleichnamigen New die Funktionen).
const rootName = "_coremesh_root"

// execute baut das Template aus den Dateien und führt die erste aus.
func execute(in engineInput) (string, error) {
	name := func(f tmplFile) string { return f.Bundle + "/" + f.Name }
	entry := name(in.Files[0])
	var buf bytes.Buffer
	if in.Target == targetHTML {
		t := htmltemplate.New(rootName).Delims(in.Delims[0], in.Delims[1]).
			Funcs(htmltemplate.FuncMap(allFuncs(true, in.Language, in.Job, in.Today, in.Binaries)))
		for _, f := range in.Files {
			if _, err := t.New(name(f)).Parse(f.Text); err != nil {
				return "", fmt.Errorf("Template %s: %w", name(f), err)
			}
		}
		if err := t.ExecuteTemplate(&buf, entry, in.Data); err != nil {
			return "", fmt.Errorf("Ausführen: %w", err)
		}
		return buf.String(), nil
	}
	t := texttemplate.New(rootName).Delims(in.Delims[0], in.Delims[1]).
		Funcs(texttemplate.FuncMap(allFuncs(false, in.Language, in.Job, in.Today, in.Binaries)))
	for _, f := range in.Files {
		if _, err := t.New(name(f)).Parse(f.Text); err != nil {
			return "", fmt.Errorf("Template %s: %w", name(f), err)
		}
	}
	if err := t.ExecuteTemplate(&buf, entry, in.Data); err != nil {
		return "", fmt.Errorf("Ausführen: %w", err)
	}
	return buf.String(), nil
}
