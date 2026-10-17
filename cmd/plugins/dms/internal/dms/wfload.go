package dms

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

type stepOpt struct{ Outcome, Value, Next, Label string }

// condition ist eine Bedingung eines Schritts (DmsStepCondition).
type condition struct {
	Role, Left, Op, Right, Desc string
}

// mapping ist eine Zuordnung eines Schritts (DmsStepMapping).
type mapping struct {
	Role, Field, Value, Target, Category, Desc string
}

// stepDef ist ein Schritt mit Einstellungen, Bedingungen, Zuordnungen, Texten und Optionen.
type stepDef struct {
	Workflow, Step, Name, Kind, Question string
	Target, TargetObject, VarName        string
	IfSet                                string
	AllowRestart                         bool
	// Auswahl (auch KI-Auswahl)
	SourceKind, SourceObject, SourceAction, ValueField, LabelFields string
	AutoSingle                                                      bool
	// Eingabe
	InputType, DefaultValue, DateFormat string
	// Maske, KI
	AIRequest, Context, FileTypes, AIWins string
	Confirm, FileInput                    bool
	// Befehl
	Command, Languages string
	// Aktion
	ActionObject, ActionName string
	ChangesData              bool
	// Erkennen
	Rules            string
	MinScore, MinGap int64
	// Programmschritte mit Ziel
	Apply string
	// Benutzerschritte: Link „… öffnen“ auf einen Bezug
	OpenObject, OpenLabel string

	Conditions []condition
	Mappings   []mapping
	Texts      map[string]map[string]string // Schlüssel (question, context, button.<Ergebnis>) → Sprache → Text
	Options    []stepOpt
}

// stepCols: Spalten von dms__step in der Reihenfolge von scanStep.
const stepCols = `name, kind, target, is_active, question, target_object, var_name, if_set, allow_restart, source_kind, source_object,
	source_action, value_field, label_fields, auto_single, input_type, default_value, date_format, ai_request, context, confirm,
	file_input, file_types, ai_wins, command, languages, action_object, action_name, changes_data, rules, min_score, min_gap,
	apply, open_object, open_label`

func (m *Module) loadStep(ctx context.Context, wf, step string) (*stepDef, error) {
	res, err := m.db.Query(ctx, `SELECT `+stepCols+` FROM dms__step WHERE workflow = ? AND step = ?`, wf, step)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Schritt %s gibt es im Workflow %s nicht", step, wf)
	}
	r := res.Rows[0]
	if !crud.AsBool(r[3]) {
		return nil, crud.Invalid("Schritt %s im Workflow %s ist inaktiv", step, wf)
	}
	str := func(i int) string { return strings.TrimSpace(crud.Str(r[i])) }
	s := &stepDef{Workflow: wf, Step: step, Name: str(0), Kind: str(1), Target: str(2), Question: str(4), TargetObject: str(5),
		VarName: str(6), IfSet: firstOf(str(7), ifSetAsk), AllowRestart: r[8] == nil || crud.AsBool(r[8]), SourceKind: str(9),
		SourceObject: str(10), SourceAction: firstOf(str(11), "list"), ValueField: firstOf(str(12), "id"), LabelFields: firstOf(str(13), "name"),
		AutoSingle: crud.AsBool(r[14]), InputType: firstOf(str(15), inText), DefaultValue: str(16), DateFormat: str(17), AIRequest: str(18),
		Context: crud.Str(r[19]), Confirm: crud.AsBool(r[20]), FileInput: crud.AsBool(r[21]), FileTypes: str(22), AIWins: str(23),
		Command: str(24), Languages: firstOf(str(25), "deu+eng"), ActionObject: str(26), ActionName: str(27),
		ChangesData: r[28] == nil || crud.AsBool(r[28]), Rules: str(29), MinScore: toInt(r[30]), MinGap: toInt(r[31]),
		Apply: firstOf(str(32), applyPropose), OpenObject: str(33), OpenLabel: str(34), Texts: map[string]map[string]string{}}
	if r[31] == nil {
		s.MinGap = 2
	}
	conds, err := m.db.Query(ctx, `SELECT role, left_expr, operator, right_expr, description FROM dms__step_condition
		WHERE workflow = ? AND step = ? AND is_active = ? ORDER BY line_no`, wf, step, true)
	if err != nil {
		return nil, err
	}
	for _, c := range conds.Rows {
		s.Conditions = append(s.Conditions, condition{Role: crud.Str(c[0]), Left: crud.Str(c[1]), Op: crud.Str(c[2]), Right: crud.Str(c[3]), Desc: crud.Str(c[4])})
	}
	maps, err := m.db.Query(ctx, `SELECT role, field, value, target, only_category, description FROM dms__step_mapping
		WHERE workflow = ? AND step = ? AND is_active = ? ORDER BY line_no`, wf, step, true)
	if err != nil {
		return nil, err
	}
	for _, p := range maps.Rows {
		s.Mappings = append(s.Mappings, mapping{Role: crud.Str(p[0]), Field: strings.TrimSpace(crud.Str(p[1])), Value: crud.Str(p[2]),
			Target: strings.TrimSpace(crud.Str(p[3])), Category: strings.TrimSpace(crud.Str(p[4])), Desc: crud.Str(p[5])})
	}
	texts, err := m.db.Query(ctx, `SELECT text_key, locale, value FROM dms__step_text WHERE workflow = ? AND step = ?`, wf, step)
	if err != nil {
		return nil, err
	}
	for _, t := range texts.Rows {
		k := crud.Str(t[0])
		if s.Texts[k] == nil {
			s.Texts[k] = map[string]string{}
		}
		s.Texts[k][crud.Str(t[1])] = crud.Str(t[2])
	}
	opts, err := m.db.Query(ctx, `SELECT outcome, value, next_step, label FROM dms__step_option WHERE workflow = ? AND step = ? AND is_active = ?`, wf, step, true)
	if err != nil {
		return nil, err
	}
	for _, o := range opts.Rows {
		s.Options = append(s.Options, stepOpt{Outcome: crud.Str(o[0]), Value: crud.Str(o[1]), Next: crud.Str(o[2]), Label: crud.Str(o[3])})
	}
	return s, nil
}

func (m *Module) steps(ctx context.Context, wf string) ([]*stepDef, error) {
	res, err := m.db.Query(ctx, `SELECT step FROM dms__step WHERE workflow = ? AND is_active = ? ORDER BY step`, wf, true)
	if err != nil {
		return nil, err
	}
	out := make([]*stepDef, 0, len(res.Rows))
	for _, r := range res.Rows {
		s, err := m.loadStep(ctx, wf, crud.Str(r[0]))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *Module) startStep(ctx context.Context, wf string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT start_step, is_active FROM dms__workflow WHERE code = ?`, wf)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Workflow %s gibt es nicht", wf)
	}
	if !crud.AsBool(res.Rows[0][1]) {
		return "", crud.Invalid("Workflow %s ist inaktiv", wf)
	}
	if s := crud.Str(res.Rows[0][0]); s != "" {
		return s, nil
	}
	first, err := m.db.Query(ctx, `SELECT MIN(step) FROM dms__step WHERE workflow = ? AND is_active = ?`, wf, true)
	if err != nil {
		return "", err
	}
	if s := crud.Str(first.Rows[0][0]); s != "" {
		return s, nil
	}
	return "", crud.Invalid("Workflow %s hat keine Schritte", wf)
}

// localeOf: Sprache der Anfrage (de-DE → de).
func localeOf(ctx context.Context) string {
	l := sdk.CallFromContext(ctx).Metadata["locale"]
	if strings.HasPrefix(strings.ToLower(l), "zh") {
		return "zh-CN"
	}
	if i := strings.IndexAny(l, "-_"); i > 0 {
		l = l[:i]
	}
	return strings.ToLower(l)
}

// text: Text des Schritts in der Sprache der Anfrage (Übersetzungen), sonst def (Deutsch).
func (s *stepDef) text(ctx context.Context, key, def string) string {
	if t := s.Texts[key][localeOf(ctx)]; t != "" {
		return t
	}
	return def
}

func (s *stepDef) question(ctx context.Context) string {
	return s.text(ctx, "question", firstOf(s.Question, s.Name))
}

// mappings: Zuordnungen der Rolle (für die Kategorie cat: ohne Kategorie und die der Kategorie).
func (s *stepDef) mappings(role, cat string) []mapping {
	var out []mapping
	for _, p := range s.Mappings {
		if p.Role == role && (p.Category == "" || strings.EqualFold(p.Category, cat)) {
			out = append(out, p)
		}
	}
	return out
}

// conditions: Bedingungen der Rolle.
func (s *stepDef) conditions(role string) []condition {
	var out []condition
	for _, c := range s.Conditions {
		if c.Role == role {
			out = append(out, c)
		}
	}
	return out
}

// targetKey: Schlüssel des Ziels (Feld, ref:<Object>, var:<Name>).
func (s *stepDef) targetKey() string { return targetKey(s.Target, s.TargetObject, s.VarName) }

// next: nächster Schritt zum Ergebnis (Wert gleich vor Ergebnis; übersprungen
// fällt auf das positive Ergebnis der Art zurück). ok = false: keine Option.
func (s *stepDef) next(outcome, value string) (string, bool) {
	if value != "" {
		for _, o := range s.Options {
			if o.Outcome == oValue && o.Value == value {
				return o.Next, true
			}
		}
	}
	try := []string{outcome}
	if outcome == oSkipped {
		try = append(try, positive(s))
	}
	for _, t := range try {
		if o := s.option(t); o != nil {
			return o.Next, true
		}
	}
	return "", false
}

// option: Option zum Ergebnis (ohne Wert) oder nil.
func (s *stepDef) option(outcome string) *stepOpt {
	for i, o := range s.Options {
		if o.Outcome == outcome && o.Value == "" {
			return &s.Options[i]
		}
	}
	return nil
}
