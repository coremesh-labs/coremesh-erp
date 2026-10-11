package templating

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func (m *Module) definition() *crud.Entity {
	return &crud.Entity{
		Object: defObject, Title: "Vorlagen", Icon: "icon-file-text", Table: "template__definition", Section: "Vorlagen",
		Keys: []string{"code", "language"}, Order: "code, language", StatusField: "is_active", TitleField: "name",
		Filters: []string{"language", "target", "template_bundle", "binary_bundle", "is_active"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "language", Label: "Sprache", Type: tSel, Required: true, Listable: true, Immutable: true, Options: languages},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "target", Label: "Ausgabe", Type: tSel, Required: true, Listable: true, Options: targets, Trigger: true},
			{Key: "template_bundle", Label: "Bundle mit Go-Templates (das konkreteste der Kette)", Type: tText, Required: true, Listable: true,
				Ref: refBundle, Lookup: lookupBundle(kindTemplate)},
			{Key: "binary_bundle", Label: "Bundle mit Binärdateien (Bilder für PDF)", Type: tText, Listable: true,
				Ref: refBundle, Lookup: lookupBundle(kindBinary), ShowIf: &metamodel.Condition{Field: "target", Values: []string{targetPDF}}},
			{Key: "delimiters", Label: "Begrenzer der Templates (leer = PDF << >>, sonst {{ }})", Type: tText},
			{Key: "file_name", Label: "Dateiname des Ergebnisses (leer = Schlüssel und Endung)", Type: tText},
			{Key: "debug", Label: "Debug: Quelltext und Protokolle immer am Auftrag", Type: tBool, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "sample_data", Label: "Beispieldaten (YAML) für Probeaufträge", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "new_code", Label: "Neuer Schlüssel", Type: tText, Required: true, ActionOnly: true},
			{Key: "new_language", Label: "Sprache der Kopie", Type: tSel, Required: true, ActionOnly: true, Options: languages},
			{Key: "new_name", Label: "Neue Bezeichnung", Type: tText, ActionOnly: true},
			{Key: "job_company", Label: "Buchungskreis", Type: tText, Required: true, ActionOnly: true, Lookup: lookupCC},
			{Key: "job_data", Label: "Daten (YAML oder JSON)", Type: tArea, ActionOnly: true},
			{Key: "job_keep", Label: "Nicht löschen", Type: tBool, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "vorlage", Title: "Vorlage", Fields: []string{"code", "language", "name", "target", "template_bundle", "binary_bundle",
				"delimiters", "file_name", "debug", "description", "is_active"}},
			{Key: "beispiel", Title: "Beispieldaten", Collapsed: true, Fields: []string{"sample_data"}},
			{Key: "auftraege", Title: "Aufträge", Collapsed: true, Relation: &metamodel.Relation{Object: jobObject, ForeignKey: "template",
				Match:   map[string]string{"template": "code", "language": "language"},
				Columns: []string{"job_number", "company_code", "status", "status_text", "keep", "created_at"}}},
		},
		Validate: m.checkDefinition,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "copy", Label: "Kopieren …", Record: true, Fields: []string{"new_code", "new_language", "new_name"}},
				Handle: m.copyDefinitionAction},
			{ActionConfig: metamodel.ActionConfig{Name: "tryRender", Label: "Probeauftrag …", Record: true, Fields: []string{"job_company", "job_data", "job_keep"},
				FormState: true}, Handle: m.tryRenderAction},
			{ActionConfig: metamodel.ActionConfig{Name: "examples", Label: "Beispiele anlegen …", Fields: []string{"job_company"}}, Handle: m.examplesAction},
		},
		FormState: m.definitionFormState,
	}
}

func (m *Module) checkDefinition(ctx context.Context, rec, _ crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	rec["target"] = trimUpper(rec["target"])
	switch rec["target"] {
	case targetHTML, targetXML, targetCSV, targetPDF:
	default:
		return crud.Invalid("Ausgabe %q: HTML, XML, CSV oder PDF", rec["target"])
	}
	for _, b := range []struct{ field, kind, label string }{{"template_bundle", kindTemplate, "Go-Templates"}, {"binary_bundle", kindBinary, "Binärdateien"}} {
		code := strings.TrimSpace(crud.Str(rec[b.field]))
		rec[b.field] = nilIfEmpty(code)
		if code == "" {
			continue
		}
		if k := m.text(ctx, `SELECT kind FROM template__bundle WHERE code = ?`, code); k != b.kind {
			return crud.Invalid("Bundle %s enthält keine %s", code, b.label)
		}
	}
	if rec["binary_bundle"] != nil && rec["target"] != targetPDF {
		return crud.Invalid("Binärdateien gibt es nur für PDF")
	}
	if d := strings.TrimSpace(crud.Str(rec["delimiters"])); d != "" {
		if _, _, err := parseDelims(d); err != nil {
			return err
		}
	}
	rec["delimiters"] = nilIfEmpty(crud.Str(rec["delimiters"]))
	rec["file_name"] = nilIfEmpty(crud.Str(rec["file_name"]))
	if s := strings.TrimSpace(crud.Str(rec["sample_data"])); s != "" {
		if _, err := parseData(s); err != nil {
			return crud.Invalid("Beispieldaten: %v", err)
		}
	}
	return nil
}

// parseDelims: "<< >>" → links, rechts.
func parseDelims(s string) (string, string, error) {
	f := strings.Fields(s)
	if len(f) != 2 {
		return "", "", crud.Invalid("Begrenzer %q: links und rechts mit Leerzeichen getrennt, z. B. << >>", s)
	}
	return f[0], f[1], nil
}

// parseData: YAML oder JSON → Map (Zahlen wie aus JSON: float64).
func parseData(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return map[string]any{}, nil
	}
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		return nil, err
	}
	return normalizeData(v)
}

// normalizeData: beliebige Daten → map[string]any mit JSON-Typen.
func normalizeData(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("Daten: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, crud.Invalid("Daten: ein Objekt (Schlüssel: Wert) erwartet")
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func (m *Module) definitionFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode == "action" && req.Action == "tryRender" && strings.TrimSpace(req.Values["job_data"]) == "" {
		key, err := m.set.Entity(defObject).ParseID(req.ID)
		if err == nil {
			s := m.text(ctx, `SELECT sample_data FROM template__definition WHERE code = ? AND language = ?`, key["code"], key["language"])
			if s != "" {
				st.Fields["job_data"] = metamodel.FieldState{Value: &s}
			}
		}
		st.Message = "Legt einen Auftrag mit diesen Daten an (wie Template.render eines anderen Moduls); das Ergebnis steht unter Aufträge."
	}
	if req.Mode != "action" {
		pdf := req.Values["target"] == targetPDF
		st.Fields["binary_bundle"] = metamodel.FieldState{Visible: &pdf}
	}
	return st, nil
}

// defRow ist eine Vorlage.
type defRow struct {
	Code, Language, Name, Target, TemplateBundle, BinaryBundle, Delims, FileName string
	Debug, Active                                                                bool
}

func (m *Module) loadDefinition(ctx context.Context, code, language string) (*defRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, language, name, target, template_bundle, binary_bundle, delimiters, file_name, debug, is_active
		FROM template__definition WHERE code = ? AND language = ?`, code, language)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Vorlage %s (%s)", sdk.ErrNotFound, code, language)
	}
	r := res.Rows[0]
	return &defRow{Code: crud.Str(r[0]), Language: crud.Str(r[1]), Name: crud.Str(r[2]), Target: crud.Str(r[3]), TemplateBundle: crud.Str(r[4]),
		BinaryBundle: crud.Str(r[5]), Delims: crud.Str(r[6]), FileName: crud.Str(r[7]), Debug: crud.AsBool(r[8]), Active: crud.AsBool(r[9])}, nil
}

type defActionIn struct {
	ID   string `json:"id"`
	Data struct {
		NewCode     string `json:"new_code"`
		NewLanguage string `json:"new_language"`
		NewName     string `json:"new_name"`
		Company     string `json:"job_company"`
		Data        string `json:"job_data"`
		Keep        any    `json:"job_keep"`
	} `json:"data"`
}

func (m *Module) copyDefinitionAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in defActionIn
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	e := m.set.Entity(defObject)
	key, err := e.ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	src, err := e.Load(ctx, key)
	if err != nil {
		return sdk.Response{}, err
	}
	data := map[string]any{}
	for _, f := range []string{"target", "template_bundle", "binary_bundle", "delimiters", "file_name", "debug", "description", "sample_data"} {
		data[f] = src[f]
	}
	data["code"], data["language"] = strings.TrimSpace(in.Data.NewCode), firstOf(in.Data.NewLanguage, crud.Str(src["language"]))
	data["name"] = firstOf(in.Data.NewName, crud.Str(src["name"]))
	if _, err := e.Create(ctx, map[string]any{"data": data}); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Vorlage %s (%s) nach %s (%s) kopiert – die Bundles sind dieselben; für eigene Änderungen das Bundle kopieren und hier eintragen",
		src["code"], src["language"], data["code"], data["language"])}}, nil
}

func (m *Module) tryRenderAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in defActionIn
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(defObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	data, err := parseData(in.Data.Data)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Daten: %v", err)
	}
	nr, err := m.render(ctx, renderRequest{Template: crud.Str(key["code"]), Language: crud.Str(key["language"]), CompanyCode: strings.TrimSpace(in.Data.Company),
		Data: data, Keep: crud.AsBool(in.Data.Keep)})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Auftrag " + nr + " angelegt – Ergebnis unter Aufträge", "job_number": nr}}, nil
}
