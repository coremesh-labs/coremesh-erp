package realestate

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// texts sammelt alle Übersetzungsschlüssel des Moduls mit dem deutschen Text
// aus dem Metamodell.
func texts(t *testing.T, d metamodel.DescribeResponse) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, o := range d.Objects {
		out[o.TitleKey] = o.Title
		for _, f := range o.Fields {
			out[f.LabelKey] = f.Label
			if f.GroupKey != "" {
				out[f.GroupKey] = f.Group
			}
			for _, opt := range f.Options {
				out[opt.LabelKey] = opt.Label
			}
		}
		for _, s := range o.Sections {
			out[s.TitleKey] = s.Title
		}
		if o.Authorization != nil {
			for _, a := range o.Authorization.Actions {
				if strings.HasPrefix(a.LabelKey, "realestate.") { // Standard-Actions übersetzt iam (admin.auth.*)
					out[a.LabelKey] = a.Label
				}
			}
			for _, g := range o.Authorization.FieldGroups {
				out[g.LabelKey] = g.Label
			}
		}
		for _, a := range o.Actions {
			// Eigene Actions und Standard-Actions mit eigenem Text (z. B. „Schließen“).
			if a.Kind == metamodel.KindCustom || !standardLabels[a.Label] {
				out[a.LabelKey] = a.Label
				if a.ConfirmKey != "" {
					out[a.ConfirmKey] = a.Confirm
				}
			}
		}
	}
	for _, m := range d.Modules {
		out[m.TitleKey], out[m.DescriptionKey] = m.Title, m.Description
		for _, o := range m.Objects {
			if o.SectionKey != "" {
				out[o.SectionKey] = o.Section
			}
		}
	}
	return out
}

func describe(t *testing.T) metamodel.DescribeResponse {
	e := setup(t)
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: sdk.ObjectCatalog, Action: sdk.ActionDescribe})
	if err != nil {
		t.Fatal(err)
	}
	return resp.Payload.(metamodel.DescribeResponse)
}

// TestDumpGermanTexts schreibt i18n/de.json aus dem Metamodell (RE_DUMP_I18N=1).
func TestDumpGermanTexts(t *testing.T) {
	if os.Getenv("RE_DUMP_I18N") == "" {
		t.Skip("nur mit RE_DUMP_I18N=1")
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(texts(t, describe(t))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("i18n/de.json", buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMetamodelCommandsTranslations(t *testing.T) {
	d := describe(t)
	for _, o := range d.Objects {
		if err := o.Validate(); err != nil {
			t.Error(err)
		}
	}
	keys := texts(t, d)
	for _, loc := range metamodel.Locales {
		for k := range keys {
			if d.Translations[loc][k] == "" {
				t.Errorf("%s: %s fehlt", loc, k)
			}
		}
		for k := range d.Translations[loc] {
			if _, ok := keys[k]; !ok {
				t.Errorf("%s: verwaister Schlüssel %s", loc, k)
			}
		}
	}
	if len(d.Modules) != 1 {
		t.Fatalf("Module: %v", d.Modules)
	}
	if cmds := d.Modules[0].Commands; len(cmds) != 1 || cmds[0].Name != "setup-company" {
		t.Errorf("Befehle: %v", cmds)
	}
}

// standardLabels: Texte, die der WebServer für Standard-Actions selbst mitbringt.
var standardLabels = map[string]bool{"Übersicht": true, "Anzeigen": true, "Neu": true, "Bearbeiten": true, "Inaktivieren": true, "Beenden …": true}
