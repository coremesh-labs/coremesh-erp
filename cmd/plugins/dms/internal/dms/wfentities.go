package dms

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Workflows: für alle Buchungskreise, kopierbar. Ein Workflow besteht aus
// Schritten (DmsWorkflowStep) mit Einstellungen je Art, Bedingungen
// (DmsStepCondition), Zuordnungen (DmsStepMapping), Übersetzungen (DmsStepText)
// und Optionen (DmsStepOption: Ergebnis → nächster Schritt, Text des Knopfs).

const secWorkflow = "Workflows"

// wfFormat: Form der Schritte seit 0.12.0 (3: 0.12.1, 4: 0.12.5).
const wfFormat = 4

// itemFieldRe: Feld in der Vorlage des Werts einer Auswahl („{condition_type}|{object_id}“).
var itemFieldRe = regexp.MustCompile(`\{[A-Za-z0-9_]+\}`)

func showKinds(kinds ...string) *metamodel.Condition {
	return &metamodel.Condition{Field: "kind", Values: kinds}
}

func (m *Module) workflow() *crud.Entity {
	return &crud.Entity{
		Object: workflowObject, Title: "Workflows", Icon: "icon-flow", Table: "dms__workflow", Section: secWorkflow,
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name",
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "start_step", Label: "Erster Schritt (leer = kleinster)", Type: tText, Listable: true},
			{Key: "restart_keep", Label: "Felder, die „Neu starten“ und „Von vorn“ behalten (Komma-Liste, z. B. note)", Type: tText},
			{Key: "format", Label: "Form der Schritte", Type: tNum, ReadOnly: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "copy_code", Label: "Schlüssel der Kopie", Type: tText, Required: true, ActionOnly: true},
			{Key: "copy_name", Label: "Bezeichnung der Kopie (leer = wie Vorlage)", Type: tText, ActionOnly: true},
			{Key: "dry_document", Label: "Dokument (ID; leer = alle wartenden im Eingang)", Type: tText, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "schritte", Title: "Schritte", Relation: &metamodel.Relation{Object: stepObject, ForeignKey: "workflow",
				Columns: []string{"step", "name", "kind", "target", "is_active"}}},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["start_step"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["start_step"])))
			if rec["format"] == nil || toInt(rec["format"]) == 0 {
				rec["format"] = wfFormat
			}
			return checkCode(rec, "code", "Schlüssel")
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "copy", Label: "Kopieren …", Record: true, Fields: []string{"copy_code", "copy_name"}}, Handle: m.copyWorkflowAction},
			{ActionConfig: metamodel.ActionConfig{Name: "renumber", Label: "Schritte neu nummerieren (10er-Schritte)", Record: true,
				Confirm: "Schritte in der bisherigen Reihenfolge als 010, 020, 030 … nummerieren? Optionen, Bedingungen, Zuordnungen und Dokumente im Workflow werden mit umgestellt."},
				Handle: m.renumberWorkflowAction},
			{ActionConfig: metamodel.ActionConfig{Name: "check", Label: "Prüfen", Record: true,
				Confirm: "Workflow prüfen (Schritte, Optionen, Einstellungen)?"}, Handle: m.checkWorkflowAction},
			{ActionConfig: metamodel.ActionConfig{Name: "dryRun", Label: "Probelauf …", Record: true, Fields: []string{"dry_document"}}, Handle: m.dryRunAction},
		},
	}
}

func (m *Module) step() *crud.Entity {
	match := map[string]string{"workflow": "workflow", "step": "step"}
	user := []string{kInput, kSelect, kForm}
	return &crud.Entity{
		Object: stepObject, Title: "Workflow-Schritte", Icon: "icon-step", Table: "dms__step", Section: secWorkflow,
		Keys: []string{"workflow", "step"}, Order: "workflow, step", Filters: []string{"workflow", "kind"}, TitleField: "name", StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt (z. B. 010)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: kindOptions, Trigger: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "question", Label: "Frage (Platzhalter wie {partner}, {doc.title}; Übersetzungen unter Texte)", Type: tText, ShowIf: showKinds(user...)},
			{Key: "target", Label: "Ziel (Feld des Dokuments, Bezug oder Variable)", Type: tSel, Listable: true, Options: m.targetOptions(), Trigger: true,
				ShowIf: showKinds(kSelect, kInput, kForm, kAI, kCommand)},
			{Key: "target_object", Label: "Bezug: Object (leer = aus der Auswahl „Object:Schlüssel“)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "target", Values: []string{"ref"}}},
			{Key: "var_name", Label: "Name der Variablen ({var.<Name>})", Type: tText,
				ShowIf: &metamodel.Condition{Field: "target", Values: []string{"var"}}},
			{Key: "if_set", Label: "Ziel schon belegt", Type: tSel, Options: ifSetOptions, ShowIf: showKinds(kSelect, kInput)},
			{Key: "allow_restart", Label: "„Von vorn“ anbieten", Type: tBool, ShowIf: showKinds(user...)},
			// Auswahl
			{Key: "source_kind", Label: "Auswahl aus", Type: tSel, Options: sourceKindOptions, ShowIf: showKinds(kSelect, kAI), Trigger: true},
			{Key: "source_object", Label: "Object der Liste (z. B. BusinessPartner)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "source_kind", Values: []string{srcObject}}},
			{Key: "source_action", Label: "Aktion der Liste (leer = list)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "source_kind", Values: []string{srcObject}}},
			{Key: "value_field", Label: "Feld mit dem Wert (leer = id; Vorlage wie {condition_type}|{object_id})", Type: tText,
				ShowIf: &metamodel.Condition{Field: "source_kind", Values: []string{srcObject}}},
			{Key: "label_fields", Label: "Felder der Bezeichnung (Komma-Liste, leer = name)", Type: tText,
				ShowIf: &metamodel.Condition{Field: "source_kind", Values: []string{srcObject}}},
			{Key: "auto_single", Label: "Nur eine Möglichkeit: automatisch wählen", Type: tBool, ShowIf: showKinds(kSelect)},
			// Eingabe
			{Key: "input_type", Label: "Eingabe", Type: tSel, Options: inputTypeOptions, ShowIf: showKinds(kInput)},
			{Key: "default_value", Label: "Vorgabe (Platzhalter, z. B. {name_proposal})", Type: tText, ShowIf: showKinds(kInput)},
			{Key: "date_format", Label: "Datumsformat (z. B. TT.MM.JJJJ; leer = üblich)", Type: tText, ShowIf: showKinds(kInput)},
			// KI, Maske
			{Key: "ai_request", Label: "KI-Anfrage (Modul ai, Text-Anfrage)", Type: tText, ShowIf: showKinds(kAI),
				Lookup: &metamodel.Lookup{Object: "AiRequest", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "context", Label: "Kontext für die KI (Zeilen mit |; Platzhalter, Zeilen mit leerem {hint.…} entfallen)", Type: tArea, ShowIf: showKinds(kAI, kForm)},
			{Key: "confirm", Label: "Vor dem Anlegen Vorschau zeigen", Type: tBool, ShowIf: showKinds(kForm)},
			{Key: "file_input", Label: "Knopf „KI liest die Datei“", Type: tBool, ShowIf: showKinds(kForm)},
			{Key: "file_types", Label: "Datei an die KI nur für (z. B. application/pdf, image/*; leer = alle)", Type: tText, ShowIf: showKinds(kForm)},
			{Key: "ai_wins", Label: "KI geht der Vorbelegung vor bei (Felder, Komma-Liste)", Type: tText, ShowIf: showKinds(kForm)},
			// Befehl
			{Key: "command", Label: "Befehl", Type: tText, ShowIf: showKinds(kCommand),
				Ref: &crud.Ref{Table: "dms__command", Column: "code", Label: "Befehl", Object: commandObject, LabelFields: []string{"name"}, ActiveField: "is_active"}},
			{Key: "languages", Label: "Sprachen der Texterkennung (z. B. deu+eng)", Type: tText, ShowIf: showKinds(kCommand)},
			// Aktion
			{Key: "action_object", Label: "Object der Aktion (z. B. SupplierInvoice, DmsDocument)", Type: tText, ShowIf: showKinds(kAction)},
			{Key: "action_name", Label: "Aktion (z. B. fromTemplate, file, takeover)", Type: tText, ShowIf: showKinds(kAction)},
			{Key: "changes_data", Label: "Ändert Daten (im Probelauf nicht ausführen)", Type: tBool, ShowIf: showKinds(kAction)},
			// Erkennen
			{Key: "rules", Label: "Erkennungsregeln (Schlüssel oder Gruppen, Komma-Liste; leer = alle mit Ziel)", Type: tText, ShowIf: showKinds(kRecognize)},
			{Key: "min_score", Label: "Mindestpunkte (leer = 3)", Type: tNum, ShowIf: showKinds(kRecognize)},
			{Key: "min_gap", Label: "Abstand zum nächsten Kandidaten (leer = 2)", Type: tNum, ShowIf: showKinds(kRecognize)},
			{Key: "apply", Label: "Ergebnis", Type: tSel, Options: applyOptions, ShowIf: showKinds(kAI, kRecognize, kAction)},
			// Link
			{Key: "open_object", Label: "Link „… öffnen“ auf Bezüge des Objects (z. B. SupplierInvoice)", Type: tText, ShowIf: showKinds(user...)},
			{Key: "open_label", Label: "Text des Links (leer = Object)", Type: tText, ShowIf: showKinds(user...)},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "optionen", Title: "Optionen (Ergebnis → nächster Schritt, Knopf)", Relation: &metamodel.Relation{Object: optionObject, ForeignKey: "step", Match: match,
				Columns: []string{"outcome", "value", "next_step", "label"}}},
			{Key: "bedingungen", Title: "Bedingungen", Relation: &metamodel.Relation{Object: conditionObject, ForeignKey: "step", Match: match,
				Columns: []string{"line_no", "role", "left_expr", "operator", "right_expr", "description"}}},
			{Key: "zuordnungen", Title: "Zuordnungen", Relation: &metamodel.Relation{Object: mappingObject, ForeignKey: "step", Match: match,
				Columns: []string{"line_no", "role", "field", "value", "target", "only_category"}}},
			{Key: "texte", Title: "Übersetzungen", Collapsed: true, Relation: &metamodel.Relation{Object: stepTextObject, ForeignKey: "step", Match: match,
				Columns: []string{"text_key", "locale", "value"}}},
		},
		Validate: m.checkStep,
	}
}

// checkStep: Einstellungen passen zur Art.
func (m *Module) checkStep(ctx context.Context, rec, _ crud.Record) error {
	rec["step"] = strings.TrimSpace(crud.Str(rec["step"]))
	if rec["step"] == "" || strings.ContainsAny(crud.Str(rec["step"]), "|~ ") {
		return crud.Invalid("Schritt: ohne Leerzeichen, | und ~")
	}
	kind := crud.Str(rec["kind"])
	if optionLabel(kindOptions, kind) == kind {
		return crud.Invalid("Art %q gibt es nicht", kind)
	}
	str := func(k string) string { return strings.TrimSpace(crud.Str(rec[k])) }
	target := str("target")
	switch kind {
	case kSelect:
		if target == "" {
			return crud.Invalid("Auswahl: Ziel angeben")
		}
		if str("source_kind") == "" {
			return crud.Invalid("Auswahl: „Auswahl aus“ angeben")
		}
		if str("source_kind") == srcObject && str("source_object") == "" {
			return crud.Invalid("Auswahl aus einer Liste: Object angeben")
		}
	case kInput:
		if target == "" && str("input_type") != inYesNo {
			return crud.Invalid("Eingabe: Ziel angeben (nur Ja/Nein geht ohne)")
		}
	case kAI:
		if str("ai_request") == "" {
			return crud.Invalid("KI: KI-Anfrage angeben")
		}
	case kCommand:
		if str("command") == "" {
			return crud.Invalid("Befehl: Befehl aus dem Katalog angeben")
		}
	case kAction:
		if str("action_object") == "" || str("action_name") == "" {
			return crud.Invalid("Aktion: Object und Aktion angeben")
		}
	}
	switch target {
	case "":
	case "var":
		if str("var_name") == "" {
			return crud.Invalid("Ziel Variable: Name angeben")
		}
	case "ref":
	default:
		if m.docField(target) == nil {
			return crud.Invalid("Ziel %q ist kein änderbares Feld des Dokuments", target)
		}
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
			{Key: "label", Label: "Text des Knopfs (Benutzerschritte; leer = Vorgabe)", Type: tText, Listable: true},
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
	rec["label"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["label"])))
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

func (m *Module) stepCondition() *crud.Entity {
	return &crud.Entity{
		Object: conditionObject, Title: "Bedingungen der Schritte", Icon: "icon-filter", Table: "dms__step_condition", Section: secWorkflow,
		Keys: []string{"workflow", "step", "line_no"}, Order: "workflow, step, line_no", Filters: []string{"workflow", "step"}, StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "line_no", Label: "Zeile (Reihenfolge; leer = ans Ende)", Type: tNum, Listable: true, Immutable: true},
			{Key: "role", Label: "Rolle", Type: tSel, Required: true, Listable: true, Options: condRoleOptions},
			{Key: "left_expr", Label: "Wert (Platzhalter, z. B. {doc.content_type}, {ref.Contract}, {var.kind})", Type: tText, Required: true, Listable: true},
			{Key: "operator", Label: "Vergleich", Type: tSel, Required: true, Listable: true, Options: opOptions},
			{Key: "right_expr", Label: "mit (Platzhalter; Liste mit Komma)", Type: tText, Listable: true},
			{Key: "description", Label: "Hinweis, wenn nicht erfüllt (Ablauf, Verlauf)", Short: "Hinweis", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			if _, err := m.loadStep(ctx, crud.Str(rec["workflow"]), crud.Str(rec["step"])); err != nil {
				return err
			}
			return m.nextLine(ctx, "dms__step_condition", rec)
		},
	}
}

// nextLine: Zeile ans Ende (in 10er-Schritten), wenn keine angegeben ist.
func (m *Module) nextLine(ctx context.Context, table string, rec crud.Record) error {
	if toInt(rec["line_no"]) > 0 {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) FROM `+table+` WHERE workflow = ? AND step = ?`, rec["workflow"], rec["step"])
	if err != nil {
		return err
	}
	rec["line_no"] = (toInt(res.Rows[0][0])/10 + 1) * 10
	return nil
}

func (m *Module) stepMapping() *crud.Entity {
	return &crud.Entity{
		Object: mappingObject, Title: "Zuordnungen der Schritte", Icon: "icon-sliders", Table: "dms__step_mapping", Section: secWorkflow,
		Keys: []string{"workflow", "step", "line_no"}, Order: "workflow, step, line_no", Filters: []string{"workflow", "step", "role"}, StatusField: "is_active",
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "line_no", Label: "Zeile (Reihenfolge; leer = ans Ende)", Type: tNum, Listable: true, Immutable: true},
			{Key: "role", Label: "Rolle", Type: tSel, Required: true, Listable: true, Options: mapRoleOptions},
			{Key: "field", Label: "Feld bzw. Object (Filter, Wert, Parameter, Feld der Antwort, Object der KI-Anfrage, gruppe.feld)", Type: tText, Required: true, Listable: true},
			{Key: "value", Label: "Wert (Platzhalter; Bezeichnung, KI-Anfrage, Object.Action)", Type: tText, Listable: true},
			{Key: "target", Label: "Ziel (nur Ergebnis: Feld des Dokuments, ref:<Object>, var:<Name>)", Type: tText, Listable: true},
			{Key: "only_category", Label: "Nur für Dokumentkategorie (leer = alle)", Short: "Kategorie", Type: tText, Listable: true, Ref: refCategory},
			{Key: "description", Label: "Beschreibung (Filter: Hinweis an der Auswahl)", Type: tText},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["field"] = strings.TrimSpace(crud.Str(rec["field"]))
			rec["only_category"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["only_category"])))
			if crud.Str(rec["role"]) == mapResult {
				t := strings.TrimSpace(crud.Str(rec["target"]))
				if t == "" {
					return crud.Invalid("Ergebnis: Ziel angeben")
				}
				if tt, _ := splitTarget(t); tt != "ref" && tt != "var" && m.docField(tt) == nil {
					return crud.Invalid("Ziel %q ist kein änderbares Feld des Dokuments", t)
				}
			}
			if _, err := m.loadStep(ctx, crud.Str(rec["workflow"]), crud.Str(rec["step"])); err != nil {
				return err
			}
			return m.nextLine(ctx, "dms__step_mapping", rec)
		},
	}
}

func (m *Module) stepText() *crud.Entity {
	return &crud.Entity{
		Object: stepTextObject, Title: "Übersetzungen der Schritte", Icon: "icon-language", Table: "dms__step_text", Section: secWorkflow,
		Keys: []string{"workflow", "step", "text_key", "locale"}, Order: "workflow, step, text_key, locale", Filters: []string{"workflow", "step"},
		Fields: []crud.Field{
			{Key: "workflow", Label: "Workflow", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refWorkflow},
			{Key: "step", Label: "Schritt", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "text_key", Label: "Text (question, context, button.<Ergebnis>)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "locale", Label: "Sprache (de, en, zh-CN)", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "value", Label: "Text", Type: tArea, Required: true, Listable: true},
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			k := strings.TrimSpace(crud.Str(rec["text_key"]))
			if k != "question" && k != "context" && !strings.HasPrefix(k, "button.") {
				return crud.Invalid("Text %q: question, context oder button.<Ergebnis>", k)
			}
			rec["text_key"] = k
			switch l := strings.TrimSpace(crud.Str(rec["locale"])); l {
			case "de", "en", "zh-CN":
				rec["locale"] = l
			default:
				return crud.Invalid("Sprache %q: de, en oder zh-CN", l)
			}
			_, err := m.loadStep(ctx, crud.Str(rec["workflow"]), crud.Str(rec["step"]))
			return err
		},
	}
}

// copyWorkflowAction: Workflow mit Schritten, Bedingungen, Zuordnungen, Texten und Optionen kopieren.
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
			"description": src["description"], "start_step": src["start_step"], "restart_keep": src["restart_keep"], "format": src["format"]}}); err != nil {
			return err
		}
		for _, t := range []string{"dms__step", "dms__step_condition", "dms__step_mapping", "dms__step_text", "dms__step_option"} {
			if err := m.copyRows(ctx, t, "workflow", in.ID, code); err != nil {
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

// copyRows kopiert die Zeilen der Tabelle mit col = from als col = to (alle Spalten).
func (m *Module) copyRows(ctx context.Context, table, col, from, to string) error {
	res, err := m.db.Query(ctx, `SELECT * FROM `+table+` WHERE `+col+` = ?`, from)
	if err != nil {
		return err
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(res.Columns)), ", ")
	for _, r := range res.Rows {
		vals := append([]any(nil), r...)
		for i, c := range res.Columns {
			if c == col {
				vals[i] = to
			}
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO `+table+` (`+strings.Join(res.Columns, ", ")+`) VALUES (`+ph+`)`, vals...); err != nil {
			return err
		}
	}
	return nil
}

// checkWorkflowAction: Hinweise zum Workflow als Tabelle.
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

// checkWorkflow: Schritte erreichbar, Optionen zeigen auf vorhandene Schritte und
// passen zur Art, Einstellungen vollständig, Ablegen kommt vor.
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
		if s.Kind == kAction && s.ActionObject == docObject && s.ActionName == "file" {
			hasFile = true
		}
		out = append(out, m.checkStepDef(ctx, s)...)
		for _, o := range s.Options {
			if !contains(stepOutcomes(s), o.Outcome) {
				out = append(out, fmt.Sprintf("Schritt %s: Option %s kommt bei der Art %s nicht vor", s.Step,
					optionLabel(outcomeOptions, o.Outcome), optionLabel(kindOptions, s.Kind)))
			}
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
		if s.Kind == kCommand || s.Kind == kAI || s.Kind == kAction || s.Kind == kRecognize {
			if s.option(oFailed) == nil {
				out = append(out, fmt.Sprintf("Schritt %s: keine Option „nicht erfolgreich“ – bei einem Fehler hält der Workflow an", s.Step))
			}
		}
	}
	for _, s := range steps {
		if !reached[s.Step] {
			out = append(out, fmt.Sprintf("Schritt %s %s ist nicht erreichbar", s.Step, s.Name))
		}
	}
	if !hasFile {
		out = append(out, "Kein Schritt „Ablegen“ (Aktion DmsDocument.file) – Dokumente bleiben im Eingang, bis jemand „Ablegen“ wählt")
	}
	return out, nil
}

// stepOutcomes: Ergebnisse, die ein Schritt der Art liefern kann.
func stepOutcomes(s *stepDef) []string {
	switch s.Kind {
	case kSelect:
		return []string{oSelected, oCreate, oSkipped, oEmpty, oValue}
	case kInput:
		return []string{oEntered, oYes, oNo, oSkipped, oValue}
	case kForm:
		return []string{oCreated, oSkipped}
	case kCheck:
		return []string{oYes, oNo, oSkipped}
	}
	return []string{oSuccess, oFailed, oSkipped, oValue}
}

// checkStepDef: Einstellungen eines Schritts.
func (m *Module) checkStepDef(ctx context.Context, s *stepDef) []string {
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf("Schritt %s: ", s.Step)+fmt.Sprintf(f, a...)) }
	switch s.Kind {
	case kSelect:
		if s.SourceKind == srcList && len(s.mappings(mapValue, "")) == 0 {
			add("Auswahl aus festen Werten ohne Zuordnungen „fester Wert“")
		}
	case kForm:
		if len(s.mappings(mapRequest, "")) == 0 && !hasCategoryMapping(s, mapRequest) {
			add("Maske ohne Zuordnung „KI-Anfrage je Object“")
		}
	case kCommand:
		if _, err := m.loadCommand(ctx, s.Command); err != nil {
			add("%v", err)
		}
	case kCheck:
		if len(s.conditions(condCheck)) == 0 {
			add("Prüfen ohne Bedingungen „Prüfen“ – ergibt immer Ja")
		}
	case kRecognize:
		if len(m.ruleDefs(ctx, s.Rules)) == 0 {
			add("keine Erkennungsregeln mit Ziel")
		}
	}
	for _, p := range s.Mappings {
		if p.Category != "" && m.text(ctx, `SELECT code FROM dms__doc_category WHERE code = ?`, p.Category) == "" {
			add("Zuordnung %s: Kategorie %s gibt es nicht", p.Field, p.Category)
		}
	}
	return out
}

func hasCategoryMapping(s *stepDef, role string) bool {
	for _, p := range s.Mappings {
		if p.Role == role {
			return true
		}
	}
	return false
}
