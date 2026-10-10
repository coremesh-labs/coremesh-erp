package dms

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Workflows: für alle Buchungskreise, kopierbar. Ein Workflow besteht aus
// Schritten (DmsWorkflowStep), deren Parametern (DmsStepParam:
// Entscheidungstexte je Sprache, Object.Action für die Auswahl, Muster, Prompt …)
// und Optionen (DmsStepOption: Ergebnis → nächster Schritt). Ergebnis ist ein
// Dokument mit Ablage, Dokumentart, Bezügen – und im Schritt „Ablegen“ das
// abgelegte Dokument.

const secWorkflow = "Workflows"

// Arten der Schritte.
const (
	kSelect  = "SELECT"  // Auswahl (Werte aus Object.Action oder Liste)
	kText    = "TEXT"    // Texteingabe
	kDate    = "DATE"    // Datum
	kConfirm = "CONFIRM" // Ja/Nein
	kSet     = "SET"     // Wert setzen (Vorlage)
	kOCR     = "OCR"     // Texterkennung (pdftotext, Tesseract)
	kExtract = "EXTRACT" // Muster im erkannten Text
	kAI      = "AI"      // KI-Anfrage (OpenAI-kompatibel)
	kFile    = "FILE"    // Ablegen
)

// Ergebnisse eines Schritts.
const (
	oSelected = "SELECTED"
	oEntered  = "ENTERED"
	oYes      = "YES"
	oNo       = "NO"
	oSuccess  = "SUCCESS"
	oFailed   = "FAILED"
	oSkipped  = "SKIPPED"
	oValue    = "VALUE"
)

var (
	kindOptions = []metamodel.Option{
		{Value: kSelect, Label: "Benutzer: Auswahl"},
		{Value: kText, Label: "Benutzer: Texteingabe"},
		{Value: kDate, Label: "Benutzer: Datum"},
		{Value: kConfirm, Label: "Benutzer: Bestätigung (Ja/Nein)"},
		{Value: kSet, Label: "Programm: Wert setzen"},
		{Value: kOCR, Label: "Programm: Texterkennung (pdftotext, Tesseract)"},
		{Value: kExtract, Label: "Programm: Muster im Text"},
		{Value: kAI, Label: "Programm: KI-Anfrage"},
		{Value: kFile, Label: "Programm: Ablegen"},
	}
	targetOptions = []metamodel.Option{
		{Value: "file_account", Label: "Ablage"},
		{Value: "doc_type", Label: "Dokumentart"},
		{Value: "partner_id", Label: "Geschäftspartner"},
		{Value: "doc_date", Label: "Dokumentdatum"},
		{Value: "title", Label: "Titel"},
		{Value: "note", Label: "Notiz"},
		{Value: "retention", Label: "Ablagefrist"},
		{Value: "ref", Label: "Bezug (Parameter ref_object)"},
	}
	outcomeOptions = []metamodel.Option{
		{Value: oSelected, Label: "Auswahl erfolgt"},
		{Value: oEntered, Label: "Eingabe erfolgt"},
		{Value: oYes, Label: "Ja"},
		{Value: oNo, Label: "Nein"},
		{Value: oSuccess, Label: "Programmschritt erfolgreich"},
		{Value: oFailed, Label: "Programmschritt nicht erfolgreich"},
		{Value: oSkipped, Label: "Übersprungen (Feld schon belegt, z. B. durch das Eingangsverzeichnis)"},
		{Value: oValue, Label: "Wert gleich (Spalte Wert)"},
	}
	// userKinds warten auf eine Eingabe.
	userKinds = map[string]bool{kSelect: true, kText: true, kDate: true, kConfirm: true}
	// targetKinds brauchen ein Ziel.
	targetKinds = map[string]bool{kSelect: true, kText: true, kDate: true, kSet: true, kExtract: true, kAI: true}
)

func (m *Module) workflow() *crud.Entity {
	return &crud.Entity{
		Object: workflowObject, Title: "Workflows", Icon: "icon-flow", Table: "dms__workflow", Section: secWorkflow,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "start_step", Label: "Erster Schritt (leer = kleinster)", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "copy_code", Label: "Schlüssel der Kopie", Type: tText, Required: true, ActionOnly: true},
			{Key: "copy_name", Label: "Bezeichnung der Kopie (leer = wie Vorlage)", Type: tText, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "schritte", Title: "Schritte", Relation: &metamodel.Relation{Object: stepObject, ForeignKey: "workflow",
				Columns: []string{"step", "name", "kind", "target"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["start_step"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["start_step"])))
			return checkCode(rec, "code", "Schlüssel")
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "copy", Label: "Kopieren …", Record: true, Fields: []string{"copy_code", "copy_name"}}, Handle: m.copyWorkflowAction},
			{ActionConfig: metamodel.ActionConfig{Name: "check", Label: "Prüfen", Record: true,
				Confirm: "Workflow prüfen (Schritte, Optionen, Parameter)?"}, Handle: m.checkWorkflowAction},
		},
	}
}

func (m *Module) step() *crud.Entity {
	match := map[string]string{"workflow": "workflow", "step": "step"}
	return &crud.Entity{
		Object: stepObject, Title: "Workflow-Schritte", Icon: "icon-step", Table: "dms__step", Section: secWorkflow,
		Keys: []string{"workflow", "step"}, Order: "workflow, step", Filters: []string{"workflow"}, TitleField: "name", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt (z. B. 010)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: kindOptions},
			{Key: "target", Label: "Setzt Feld", Type: tSel, Listable: true, Options: targetOptions},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "parameter", Title: "Parameter", Relation: &metamodel.Relation{Object: paramObject, ForeignKey: "step", Match: match,
				Columns: []string{"param", "locale", "value"}}},
			{Key: "optionen", Title: "Optionen", Relation: &metamodel.Relation{Object: optionObject, ForeignKey: "step", Match: match,
				Columns: []string{"outcome", "value", "next_step"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["step"] = strings.TrimSpace(crud.Str(rec["step"]))
			if rec["step"] == "" || strings.ContainsAny(crud.Str(rec["step"]), "| ") {
				return crud.Invalid("Schritt: ohne Leerzeichen und |")
			}
			kind, target := crud.Str(rec["kind"]), crud.Str(rec["target"])
			if targetKinds[kind] && target == "" {
				return crud.Invalid("Schritt %s (%s): Feld angeben, das der Schritt setzt", rec["step"], optionLabel(kindOptions, kind))
			}
			if !targetKinds[kind] {
				rec["target"] = nil
			}
			return nil
		},
	}
}

func (m *Module) stepParam() *crud.Entity {
	return &crud.Entity{
		Object: paramObject, Title: "Parameter der Schritte", Icon: "icon-sliders", Table: "dms__step_param", Section: secWorkflow,
		Keys: []string{"workflow", "step", "param", "locale"}, Order: "workflow, step, param, locale", Filters: []string{"workflow", "step"}, StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "param", Label: "Parameter (question, source, value_field, label_fields, filter.<Feld>, options, optional, value, ref_object, pattern, date_format, languages, endpoint, model, api_key_env, prompt)",
				Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "locale", Label: "Sprache der Entscheidungstexte und Prompts (de, en, zh-CN; leer = alle)", Type: tText, Listable: true, Immutable: true},
			{Key: "value", Label: "Wert", Type: tArea, Required: true, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: m.checkStepParam,
	}
}

// stepParams: bekannte Parameter.
var stepParams = map[string]bool{"question": true, "source": true, "value_field": true, "label_fields": true, "options": true, "optional": true,
	"value": true, "ref_object": true, "pattern": true, "date_format": true, "languages": true, "min_chars": true, "endpoint": true,
	"model": true, "api_key_env": true, "prompt": true, "max_text": true}

func (m *Module) checkStepParam(ctx context.Context, rec, _ crud.Record) error {
	p := strings.TrimSpace(crud.Str(rec["param"]))
	if !stepParams[p] && !strings.HasPrefix(p, "filter.") {
		return crud.Invalid("Parameter %q gibt es nicht", p)
	}
	rec["param"] = p
	switch l := strings.TrimSpace(crud.Str(rec["locale"])); l {
	case "", "de", "en", "zh-CN":
		rec["locale"] = l
	default:
		return crud.Invalid("Sprache %q: de, en, zh-CN oder leer (alle)", l)
	}
	if _, err := m.loadStep(ctx, crud.Str(rec["workflow"]), crud.Str(rec["step"])); err != nil {
		return err
	}
	return nil
}

func (m *Module) stepOption() *crud.Entity {
	return &crud.Entity{
		Object: optionObject, Title: "Optionen der Schritte", Icon: "icon-branch", Table: "dms__step_option", Section: secWorkflow,
		Keys: []string{"workflow", "step", "outcome", "value"}, Order: "workflow, step, outcome, value", Filters: []string{"workflow", "step"}, StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "outcome", Label: "Ergebnis", Type: tSel, Required: true, Listable: true, Immutable: true, Options: outcomeOptions},
			{Key: "value", Label: "Wert (nur Ergebnis „Wert gleich“)", Type: tText, Listable: true, Immutable: true},
			{Key: "next_step", Label: "Weiter mit Schritt (leer = Ende)", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: m.checkStepOption,
	}
}

func (m *Module) checkStepOption(ctx context.Context, rec, _ crud.Record) error {
	wf := crud.Str(rec["workflow"])
	if _, err := m.loadStep(ctx, wf, crud.Str(rec["step"])); err != nil {
		return err
	}
	rec["value"] = strings.TrimSpace(crud.Str(rec["value"]))
	if (rec["outcome"] == oValue) != (rec["value"] != "") {
		return crud.Invalid("Wert nur und immer beim Ergebnis „Wert gleich“")
	}
	next := strings.TrimSpace(crud.Str(rec["next_step"]))
	rec["next_step"] = nilIfEmpty(next)
	if next != "" {
		if next == crud.Str(rec["step"]) {
			return crud.Invalid("Schritt %s kann nicht auf sich selbst folgen", next)
		}
		if _, err := m.loadStep(ctx, wf, next); err != nil {
			return err
		}
	}
	return nil
}

// copyWorkflowAction: Workflow mit Schritten, Parametern und Optionen kopieren.
func (m *Module) copyWorkflowAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Code string `json:"copy_code"`
			Name string `json:"copy_name"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, workflowObject, "create", ""); err != nil {
		return sdk.Response{}, err
	}
	src, err := m.set.Entity(workflowObject).Load(ctx, crud.Record{"code": in.ID})
	if err != nil {
		return sdk.Response{}, err
	}
	code := trimUpper(in.Data.Code)
	name := firstOf(in.Data.Name, crud.Str(src["name"]))
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.set.Entity(workflowObject).Create(ctx, map[string]any{"data": map[string]any{"code": code, "name": name,
			"description": src["description"], "start_step": src["start_step"]}}); err != nil {
			return err
		}
		for _, q := range []string{
			`INSERT INTO dms__step (workflow, step, name, kind, target, is_active) SELECT ?, step, name, kind, target, is_active FROM dms__step WHERE workflow = ?`,
			`INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active) SELECT ?, step, param, locale, value, is_active FROM dms__step_param WHERE workflow = ?`,
			`INSERT INTO dms__step_option (workflow, step, outcome, value, next_step, is_active) SELECT ?, step, outcome, value, next_step, is_active FROM dms__step_option WHERE workflow = ?`,
		} {
			if _, err := m.db.Exec(ctx, q, code, in.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"code": code, "message": fmt.Sprintf("Workflow %s nach %s kopiert", in.ID, code)}}, nil
}

// checkWorkflowAction: Schritte erreichbar, Optionen zeigen auf vorhandene
// Schritte, Auswahl hat eine Quelle, Ablegen kommt vor.
func (m *Module) checkWorkflowAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	problems, err := m.checkWorkflow(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(problems) == 0 {
		return sdk.Response{Payload: map[string]any{"message": "Workflow " + in.ID + ": keine Auffälligkeiten"}}, nil
	}
	rows := make([][]any, len(problems))
	for i, p := range problems {
		rows[i] = []any{p}
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Workflow %s: %d Hinweise", in.ID, len(problems)),
		"table": map[string]any{"columns": []string{"Hinweis"}, "rows": rows}}}, nil
}

func (m *Module) checkWorkflow(ctx context.Context, code string) ([]string, error) {
	steps, err := m.steps(ctx, code)
	if err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return []string{"Workflow hat keine Schritte"}, nil
	}
	var out []string
	start, err := m.startStep(ctx, code)
	if err != nil {
		return nil, err
	}
	byCode := map[string]*stepDef{}
	for _, s := range steps {
		byCode[s.Step] = s
	}
	reached, queue, hasFile := map[string]bool{start: true}, []string{start}, false
	for len(queue) > 0 {
		s := byCode[queue[0]]
		queue = queue[1:]
		if s == nil {
			continue
		}
		if s.Kind == kFile {
			hasFile = true
		}
		if s.Kind == kSelect && s.param(ctx, "source") == "" && s.param(ctx, "options") == "" {
			out = append(out, fmt.Sprintf("Schritt %s: Auswahl ohne Parameter source bzw. options", s.Step))
		}
		if s.Kind == kAI && s.param(ctx, "prompt") == "" {
			out = append(out, fmt.Sprintf("Schritt %s: KI-Anfrage ohne Parameter prompt", s.Step))
		}
		if s.Kind == kExtract && s.param(ctx, "pattern") == "" {
			out = append(out, fmt.Sprintf("Schritt %s: Muster ohne Parameter pattern", s.Step))
		}
		for _, o := range s.Options {
			if o.Next == "" {
				continue
			}
			if byCode[o.Next] == nil {
				out = append(out, fmt.Sprintf("Schritt %s: Option %s zeigt auf fehlenden Schritt %s", s.Step, o.Outcome, o.Next))
				continue
			}
			if !reached[o.Next] {
				reached[o.Next] = true
				queue = append(queue, o.Next)
			}
		}
	}
	for _, s := range steps {
		if !reached[s.Step] {
			out = append(out, fmt.Sprintf("Schritt %s %s ist nicht erreichbar", s.Step, s.Name))
		}
	}
	if !hasFile {
		out = append(out, "Kein Schritt „Ablegen“ – Dokumente bleiben im Eingang, bis jemand „Ablegen“ wählt")
	}
	return out, nil
}
