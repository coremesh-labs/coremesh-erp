package ledger

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
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
				if strings.HasPrefix(a.LabelKey, "ledger.") { // Standard-Actions übersetzt iam (admin.auth.*)
					out[a.LabelKey] = a.Label
				}
			}
			for _, g := range o.Authorization.FieldGroups {
				out[g.LabelKey] = g.Label
			}
		}
		for _, a := range o.Actions {
			if a.Kind == metamodel.KindCustom {
				out[a.LabelKey] = a.Label
				if a.ConfirmKey != "" {
					out[a.ConfirmKey] = a.Confirm
				}
			}
		}
	}
	for k, v := range actionTexts {
		out[k] = v
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

// TestDumpGermanTexts schreibt i18n/de.json aus dem Metamodell (LEDGER_DUMP_I18N=1).
func TestDumpGermanTexts(t *testing.T) {
	if os.Getenv("LEDGER_DUMP_I18N") == "" {
		t.Skip("nur mit LEDGER_DUMP_I18N=1")
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
	m := d.Modules[0]
	slices.Sort(m.Services)
	if strings.Join(m.Services, ",") != "AccountBalance,CurrencyConversion,LedgerLoader,LedgerPosting" {
		t.Errorf("Services: %v", m.Services)
	}
	var cmds []string
	for _, c := range m.Commands {
		cmds = append(cmds, c.Name)
	}
	if strings.Join(cmds, ",") != "load-coa,load-rates,setup-company,periods" {
		t.Errorf("Befehle: %v", cmds)
	}
	// Belege schreibgeschützt (nur Storno), Vorerfassung speichern + buchen.
	actions := map[string]string{}
	for _, o := range d.Objects {
		var names []string
		for _, a := range o.Actions {
			names = append(names, a.Name)
		}
		actions[o.Name] = strings.Join(names, ",")
	}
	if actions["JournalEntry"] != "list,get,reverse" || actions["JournalEntryItem"] != "list,get" ||
		actions["JournalDraft"] != "list,get,create,update,simulate,post,deactivate" {
		t.Fatalf("Actions: %v", actions)
	}
}
