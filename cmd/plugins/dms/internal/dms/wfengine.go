package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablauf eines Workflows am Dokument: Der aktuelle Schritt steht in wf_step.
// Programmschritte laufen sofort; ein Benutzerschritt wartet (WAITING), bis
// jemand einen seiner Knöpfe drückt. Übersprungen wird ein Schritt nur
// ausdrücklich: Bedingung „Schritt gilt nur, wenn“ nicht erfüllt, Feld schon
// belegt bei „Überspringen“, Auswahl mit nur einer Möglichkeit bei „automatisch“
// – der Grund steht im Ablauf (DmsDocumentStep) und im Verlauf (wf_log).

// Status des Workflows am Dokument.
const (
	wfRunning = "RUNNING"
	wfWaiting = "WAITING"
	wfDone    = "DONE"
	wfFailed  = "FAILED"
)

// maxSteps begrenzt einen Lauf (Schutz vor Kreisen ohne Benutzerschritt).
const maxSteps = 100

// Knöpfe der Maske, die keine Option sind.
const (
	btnAIFile    = "_ai_file"    // KI liest die Datei
	btnAIRefresh = "_ai_refresh" // KI neu fragen
	btnPreviewOK = "_yes"        // Vorschau: so anlegen
	btnPreviewNo = "_no"         // Vorschau: zurück zur Bearbeitung
)

// stepTitle: „Schritt 055 · Dokumentart wählen: Frage“ – bei der Dokumentart mit der Kategorie.
func (m *Module) stepTitle(ctx context.Context, d *docRow, s *stepDef) string {
	t := "Schritt " + s.Step + " · " + s.Name + ": " + m.expand(ctx, d, s.question(ctx))
	if s.Target == "doc_type" {
		if c := m.categoryOf(ctx, d); c != "" {
			if n := m.text(ctx, `SELECT name FROM dms__doc_category WHERE code = ?`, c); n != "" {
				t += " (Kategorie " + n + ")"
			}
		}
	}
	return t
}

// startWorkflow setzt den Workflow auf den ersten Schritt und lässt ihn laufen.
func (m *Module) startWorkflow(ctx context.Context, docID, wf string) error {
	start, err := m.startStep(ctx, wf)
	if err != nil {
		return err
	}
	// Neustart: Variablen, Vorschläge und Rückmeldung des vorigen Laufs verwerfen
	// (Antworten der KI bleiben im Zwischenspeicher ai_cache).
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET wf_code = ?, wf_step = ?, wf_status = ?, wf_vars = NULL WHERE id = ?`, wf, start, wfRunning, docID); err != nil {
		return err
	}
	m.appendLog(ctx, docID, "Workflow "+wf+" gestartet")
	m.pushTrail(ctx, docID, "", false)
	if err := m.initOverview(ctx, docID, wf); err != nil {
		return err
	}
	return m.runWorkflow(ctx, docID)
}

func (m *Module) appendLog(ctx context.Context, docID, line string) {
	stamp := time.Now().Format("2006-01-02 15:04")
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET wf_log = COALESCE(wf_log || char(10), '') || ? WHERE id = ?`, stamp+" "+line, docID); err != nil {
		m.log.WarnContext(ctx, "Verlauf nicht geschrieben", "document", docID, "err", err.Error())
	}
}

func (m *Module) setWorkflow(ctx context.Context, docID, step, status string) error {
	_, err := m.db.Exec(ctx, `UPDATE dms__document SET wf_step = ?, wf_status = ? WHERE id = ?`, nilIfEmpty(step), status, docID)
	return err
}

// runWorkflow führt Programmschritte aus, bis ein Benutzerschritt wartet oder der Workflow endet.
func (m *Module) runWorkflow(ctx context.Context, docID string) error {
	for i := 0; i < maxSteps; i++ {
		d, err := m.loadDoc(ctx, docID)
		if err != nil {
			return err
		}
		if d.WfCode == "" || d.WfStep == "" || d.WfStatus == wfDone || d.WfStatus == wfFailed || d.WfStatus == wfWaiting {
			return nil
		}
		s, err := m.loadStep(ctx, d.WfCode, d.WfStep)
		if err != nil {
			m.appendLog(ctx, docID, err.Error())
			m.markStep(ctx, docID, d.WfStep, stepFailed, "", "", err.Error())
			return m.setWorkflow(ctx, docID, d.WfStep, wfFailed)
		}
		if ok, why := m.stepApplies(ctx, d, s); !ok {
			if err := m.advanceTrail(ctx, d, s, oSkipped, "", why, false); err != nil {
				return err
			}
			continue
		}
		if userKinds[s.Kind] {
			wait, err := m.enterUserStep(ctx, d, s)
			if err != nil {
				return err
			}
			if wait {
				if dry := dryRunOf(ctx); dry != nil {
					return dry.userStep(ctx, m, d, s)
				}
				m.markStep(ctx, docID, s.Step, stepWaiting, "", "", "")
				return m.setWorkflow(ctx, docID, s.Step, wfWaiting)
			}
			continue
		}
		outcome, value, info := m.execute(ctx, d, s)
		if err := m.advance(ctx, d, s, outcome, value, info); err != nil {
			return err
		}
	}
	m.appendLog(ctx, docID, fmt.Sprintf("Abbruch nach %d Schritten – Optionen bilden vermutlich einen Kreis", maxSteps))
	return m.setWorkflow(ctx, docID, "", wfFailed)
}

// stepApplies: Bedingungen „Schritt gilt nur, wenn“ und was die Art voraussetzt.
func (m *Module) stepApplies(ctx context.Context, d *docRow, s *stepDef) (bool, string) {
	if ok, why := m.allHold(ctx, d, s.conditions(condApplies)); !ok {
		return false, why
	}
	switch {
	case s.Kind == kSelect && s.SourceKind == srcCategory:
		if objs, _ := m.docObjects(ctx, d); len(objs) == 0 {
			return false, "keine kaufmännischen Objekte für Dokumentkategorie " + firstOf(m.categoryOf(ctx, d), "–")
		}
	case s.Kind == kForm:
		if _, _, err := m.aiRequestFor(ctx, d, s); err != nil {
			return false, err.Error()
		}
	}
	return true, ""
}

// enterUserStep: Benutzerschritt betreten – wartet (true) oder ist ausdrücklich
// erledigt (Feld belegt und „überspringen“, leere Auswahl, nur eine Möglichkeit).
func (m *Module) enterUserStep(ctx context.Context, d *docRow, s *stepDef) (bool, error) {
	if s.Kind == kForm {
		if m.formTargetSet(ctx, d, s) {
			return false, m.advanceTrail(ctx, d, s, oSkipped, "", "schon vorhanden", false)
		}
		m.aiPrefill(ctx, d, s)
		return true, nil
	}
	key := s.targetKey()
	current := m.targetValue(ctx, d, key)
	if current != "" && s.IfSet == ifSetSkip {
		return false, m.advanceTrail(ctx, d, s, oSkipped, current, "Feld schon belegt", true)
	}
	if s.Kind != kSelect {
		return true, nil
	}
	cs, err := m.choices(ctx, d, s, "")
	if err != nil {
		return true, nil // Fehler zeigt die Maske
	}
	if len(cs) == 0 && s.option(oEmpty) != nil {
		return false, m.advanceTrail(ctx, d, s, oEmpty, "", "keine Werte zur Auswahl", false)
	}
	if s.AutoSingle && len(cs) == 1 && current == "" && s.SourceKind != srcTree {
		v, err := m.setTargetValue(ctx, d, key, cs[0].Value, cs[0].Label)
		if err == nil {
			return false, m.advanceTrail(ctx, d, s, oSelected, v, "einzige Möglichkeit", true)
		}
	}
	return true, nil
}

// advance schreibt das Ergebnis in den Verlauf und geht zum nächsten Schritt.
func (m *Module) advance(ctx context.Context, d *docRow, s *stepDef, outcome, value, info string) error {
	return m.advanceTrail(ctx, d, s, outcome, value, info, userKinds[s.Kind])
}

// advanceTrail: wie advance; revisit = „zurück“ darf zu diesem Schritt.
func (m *Module) advanceTrail(ctx context.Context, d *docRow, s *stepDef, outcome, value, info string, revisit bool) error {
	m.pushTrail(ctx, d.ID, s.Step, revisit)
	next, ok := s.next(outcome, value)
	line := fmt.Sprintf("%s %s: %s", s.Step, s.Name, optionLabel(outcomeOptions, outcome))
	if value != "" {
		line += " (" + value + ")"
	}
	if info != "" {
		line += " – " + info
	}
	status := stepDone
	switch outcome {
	case oSkipped, oEmpty:
		status = stepSkipped
	case oFailed:
		status = stepFailed
	}
	m.markStep(ctx, d.ID, s.Step, status, outcome, value, info)
	if dry := dryRunOf(ctx); dry != nil {
		dry.add(s, outcome, value, info)
	}
	switch {
	case ok && next != "":
		m.appendLog(ctx, d.ID, line+" → "+next)
		return m.setWorkflow(ctx, d.ID, next, wfRunning)
	case !ok && outcome == oFailed:
		m.appendLog(ctx, d.ID, line+" → keine Option, Workflow angehalten")
		return m.setWorkflow(ctx, d.ID, s.Step, wfFailed)
	}
	m.appendLog(ctx, d.ID, line+" → Ende")
	return m.setWorkflow(ctx, d.ID, "", wfDone)
}

// execute führt einen Programmschritt aus: Ergebnis, Wert, Hinweis.
func (m *Module) execute(ctx context.Context, d *docRow, s *stepDef) (outcome, value, info string) {
	switch s.Kind {
	case kCommand:
		return m.runCommandStep(ctx, d, s)
	case kAI:
		return m.runAIStep(ctx, d, s)
	case kAction:
		return m.runActionStep(ctx, d, s)
	case kRecognize:
		return m.runRecognizeStep(ctx, d, s)
	case kCheck:
		if ok, why := m.allHold(ctx, d, s.conditions(condCheck)); !ok {
			return oNo, "", why
		}
		return oYes, "", ""
	}
	return oFailed, "", "Art " + s.Kind + " ist kein Programmschritt"
}

// applyResult: Ergebnis eines Programmschritts für ein Ziel – Vorschlag oder direkt
// (nur, wenn das Ziel leer ist; Variablen immer).
func (m *Module) applyResult(ctx context.Context, d *docRow, s *stepDef, key string, p *proposalRec) (string, error) {
	t, _ := splitTarget(key)
	if t == "var" || s.Apply == applySet {
		if t != "var" && m.targetValue(ctx, d, key) != "" {
			return "", nil
		}
		return m.setTargetValue(ctx, d, key, p.Value, p.Label)
	}
	return p.Value, m.setProposal(ctx, d.ID, key, p)
}

// --- Benutzerschritte: Knöpfe und Maske ---------------------------------------------------

func (m *Module) waitingStep(ctx context.Context, d *docRow) (*stepDef, error) {
	if d.WfStatus != wfWaiting {
		return nil, crud.Invalid("Am Dokument wartet kein Workflow-Schritt (Status %s)", firstOf(optionLabel(wfStatusOptions, d.WfStatus), "ohne Workflow"))
	}
	return m.loadStep(ctx, d.WfCode, d.WfStep)
}

// buttons: Knöpfe des wartenden Schritts – je Option ein Knopf (Text an der Option,
// sonst Übersetzung bzw. Vorgabe), der Hauptknopf immer.
func (m *Module) buttons(ctx context.Context, d *docRow, s *stepDef) []metamodel.FormButton {
	var out []metamodel.FormButton
	for i, o := range buttonOutcomes(s) {
		opt := s.option(o)
		if i > 0 && opt == nil {
			continue
		}
		label := defaultButtonLabel(s, o)
		if opt != nil && strings.TrimSpace(opt.Label) != "" {
			label = opt.Label
		}
		label = m.expand(ctx, d, s.text(ctx, "button."+o, label))
		out = append(out, metamodel.FormButton{Value: o, Label: label, Primary: i == 0, NoValidate: noValidate(o)})
	}
	return out
}

func (m *Module) documentFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode == "action" && req.Action == "aiCreateLater" {
		d, err := m.readableDoc(ctx, req.ID)
		if err != nil {
			return st, err
		}
		return m.laterFormState(ctx, d, req.Values), nil
	}
	if req.Mode != "action" || req.Action != "continue" {
		return st, nil
	}
	d, err := m.readableDoc(ctx, req.ID)
	if err != nil {
		return st, err
	}
	hide := func(keys ...string) {
		f := false
		for _, k := range keys {
			st.Fields[k] = metamodel.FieldState{Visible: &f}
		}
	}
	notice := ""
	if n := m.wfVars(ctx, d.ID)[noticeVar]; n != nil {
		notice = "✔ " + fmt.Sprint(n) + " – "
	}
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		st.Message = notice + err.Error()
		hide("wf_search", "wf_choice", "wf_text", "wf_date")
		st.HideButtons = []string{"back", "startOver"}
		return st, nil
	}
	st.Message = notice + m.stepTitle(ctx, d, s)
	st.Buttons = m.buttons(ctx, d, s)
	if !m.hasPreviousQuestion(ctx, d.ID) {
		st.HideButtons = append(st.HideButtons, "back")
	}
	if !s.AllowRestart {
		st.HideButtons = append(st.HideButtons, "startOver")
	}
	if cl := m.createLinks(ctx, d, s); len(cl) > 0 {
		st.Links = cl
		st.Message += " – fehlt der Eintrag: im neuen Fenster anlegen, dann „Liste aktualisieren“ bzw. die Suche"
	}
	st.Links = append(st.Links, m.openLinks(ctx, d, s)...)
	yes := true
	key := s.targetKey()
	prop := m.proposalFor(ctx, d, key)
	current := m.targetValue(ctx, d, key)
	switch s.Kind {
	case kSelect:
		var cs []choice
		value := req.Values["wf_choice"]
		if value == "" {
			if prop != nil {
				value = prop.Value
			} else {
				value = current
			}
		}
		if s.SourceKind == srcTree {
			var path string
			cs, path, err = m.treeChoices(ctx, d, value, req.Values["wf_search"])
			if path != "" {
				st.Message += " – " + path
			}
		} else {
			cs, err = m.choices(ctx, d, s, req.Values["wf_search"])
		}
		if err != nil {
			st.Message += " – " + err.Error()
		}
		opts := make([]metamodel.Option, len(cs))
		for i, c := range cs {
			opts[i] = metamodel.Option{Value: c.Value, Label: c.Label}
		}
		fs := metamodel.FieldState{Options: opts, Required: &yes}
		if s.SourceKind == srcTree && !containsChoice(cs, value) && containsChoice(cs, treeUp+value) {
			value = treeUp + value // geöffnete Gruppe
		}
		if value != "" && containsChoice(cs, value) {
			fs.Value = &value
		}
		st.Fields["wf_choice"] = fs
		hide("wf_text", "wf_date")
		switch {
		case prop != nil && containsChoice(cs, prop.Value):
			st.Message += " – " + prop.text()
		case prop != nil:
			st.Message += " – " + prop.text() + " steht nicht zur Auswahl"
		case current != "" && fs.Value != nil:
			st.Message += " – vorbelegt mit dem bisherigen Wert"
		}
		if f := m.filterText(ctx, d, s); f != "" {
			st.Message += " – Auswahl eingeschränkt: " + f
		}
		if len(cs) == 0 && err == nil {
			st.Message += " – keine Werte zur Auswahl"
		}
	case kInput:
		value := firstOf(prefill(prop), current, m.expand(ctx, d, s.DefaultValue))
		switch s.InputType {
		case inYesNo:
			hide("wf_choice", "wf_text", "wf_date", "wf_search")
		case inDate:
			if value == "" {
				value = today()
			}
			st.Fields["wf_date"] = metamodel.FieldState{Required: &yes, Value: &value}
			hide("wf_choice", "wf_text", "wf_search")
		default:
			st.Fields["wf_text"] = metamodel.FieldState{Required: &yes, Value: &value}
			hide("wf_choice", "wf_date", "wf_search")
		}
		if prop != nil {
			st.Message += " – " + prop.text()
		}
		if s.Target == "name_override" {
			st.Message += " – Endung " + firstOf(extOf(d.FileName), "(keine)") + " wird ergänzt"
		}
	case kForm:
		m.aiFormState(ctx, d, s, &st)
		hide("wf_choice", "wf_date", "wf_search", "wf_text")
	}
	return st, nil
}

func prefill(p *proposalRec) string {
	if p == nil {
		return ""
	}
	return p.Value
}

// continueAction: Eingabe zum wartenden Schritt, dann weiter (Knopf im Payload button).
func (m *Module) continueAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Button string `json:"button"`
		Data   struct {
			Search string `json:"wf_search"`
			Choice string `json:"wf_choice"`
			Text   string `json:"wf_text"`
			Date   string `json:"wf_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	var raw struct {
		Data map[string]any `json:"data"`
	}
	_ = sdk.Decode(req.Payload, &raw)
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	m.clearNotice(ctx, d.ID)
	outcome := firstOf(strings.TrimSpace(in.Button), positive(s))
	by := "von " + firstOf(sdk.CallFromContext(ctx).Metadata["username"], sdk.CallFromContext(ctx).UserID)
	if s.Kind == kForm {
		done, value, err := m.formContinue(ctx, d, s, outcome, raw.Data)
		if err != nil {
			return sdk.Response{}, err
		}
		if !done {
			return m.workflowState(ctx, d.ID)
		}
		if value == "" {
			outcome = oSkipped
		} else {
			outcome = oCreated
		}
		return m.finishUserStep(ctx, d, s, outcome, value, by)
	}
	if !contains(buttonOutcomes(s), outcome) {
		return sdk.Response{}, crud.Invalid("Schritt %s %s: Knopf %q gibt es hier nicht", s.Step, s.Name, outcome)
	}
	if outcome != positive(s) {
		if outcome != oNo && s.option(outcome) == nil {
			return sdk.Response{}, crud.Invalid("Schritt %s %s: keine Option für %s", s.Step, s.Name, optionLabel(outcomeOptions, outcome))
		}
		return m.finishUserStep(ctx, d, s, outcome, "", by)
	}
	key, value, label := s.targetKey(), "", ""
	switch s.Kind {
	case kSelect:
		value = strings.TrimSpace(in.Data.Choice)
		if value == "" {
			return sdk.Response{}, crud.Invalid("Bitte einen Wert wählen")
		}
		if s.SourceKind == srcTree {
			if strings.HasPrefix(value, treeUp) {
				return sdk.Response{}, crud.Invalid("Bitte eine Ablage der untersten Ebene wählen")
			}
			if by, _, err := m.companyLevels(ctx, d.CC); err == nil && by[value] != nil && !by[value].Leaf {
				return sdk.Response{}, crud.Invalid("Ablage %s hat Unterablagen – bitte eine der untersten Ebene wählen", value)
			}
		} else {
			cs, err := m.choices(ctx, d, s, in.Data.Search)
			if err == nil && !containsChoice(cs, value) {
				return sdk.Response{}, crud.Invalid("Wert %q steht nicht zur Auswahl", value)
			}
			for _, c := range cs {
				if c.Value == value {
					label = c.Label
				}
			}
		}
	case kInput:
		switch s.InputType {
		case inYesNo:
			key = s.targetKey()
			if s.Target != "" {
				value = oYes
			}
		case inDate:
			value = in.Data.Date
			if value != "" {
				v, err := parseDate(value, s.DateFormat)
				if err != nil {
					return sdk.Response{}, err
				}
				value = v
			}
		default:
			value = in.Data.Text
		}
		if s.InputType != inYesNo && strings.TrimSpace(value) == "" {
			return sdk.Response{}, crud.Invalid("Bitte etwas eingeben")
		}
		if s.InputType == inNumber {
			if _, err := parseNumber(value); err != nil {
				return sdk.Response{}, err
			}
		}
	}
	if s.Target != "" {
		v, err := m.setTargetValue(ctx, d, key, value, label)
		if err != nil {
			return sdk.Response{}, err
		}
		value = v
	}
	return m.finishUserStep(ctx, d, s, outcome, value, by)
}

// finishUserStep: Ergebnis des Benutzerschritts, dann weiter bis zum nächsten wartenden Schritt.
func (m *Module) finishUserStep(ctx context.Context, d *docRow, s *stepDef, outcome, value, info string) (sdk.Response, error) {
	if err := m.advance(ctx, d, s, outcome, value, info); err != nil {
		return sdk.Response{}, err
	}
	if err := m.runWorkflow(ctx, d.ID); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

func containsChoice(cs []choice, v string) bool {
	for _, c := range cs {
		if c.Value == v {
			return true
		}
	}
	return false
}

// workflowState: Meldung zum Stand des Workflows.
func (m *Module) workflowState(ctx context.Context, id string) (sdk.Response, error) {
	d, err := m.loadDoc(ctx, id)
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "Workflow: " + firstOf(optionLabel(wfStatusOptions, d.WfStatus), "–")
	if d.WfStatus == wfWaiting {
		if s, err := m.loadStep(ctx, d.WfCode, d.WfStep); err == nil {
			msg += " – " + m.stepTitle(ctx, d, s)
		}
	}
	if d.Status == stFiled {
		msg += " – abgelegt als " + d.Name
	}
	return sdk.Response{Payload: map[string]any{"status": d.WfStatus, "step": d.WfStep, "message": msg}}, nil
}

func (m *Module) restartAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Workflow string `json:"restart_workflow"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if d.Status != stInbox {
		return sdk.Response{}, crud.Invalid("Workflow nur für Dokumente im Eingang")
	}
	wf := trimUpper(in.Data.Workflow)
	if wf == "" {
		if d.Inbox != "" {
			if ib, err := m.loadInbox(ctx, d.CC, d.Inbox); err == nil {
				wf = ib.Workflow
			}
		}
		wf = firstOf(wf, d.WfCode)
	}
	if wf == "" {
		return sdk.Response{}, crud.Invalid("Workflow angeben – das Eingangsverzeichnis hat keinen")
	}
	// Neustart: Partner, Kategorie, Bezüge … wie beim Einlesen, damit alles neu gefragt wird.
	if err := m.resetToInbox(ctx, d, wf); err != nil {
		return sdk.Response{}, err
	}
	m.appendLog(ctx, d.ID, "Neustart: Stand wie beim Einlesen")
	if err := m.startWorkflow(ctx, d.ID, wf); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// parseDate: JJJJ-MM-TT, TT.MM.JJJJ oder nach format (Go-Layout bzw. TT.MM.JJJJ-Schreibweise).
func parseDate(v, format string) (string, error) {
	v = strings.TrimSpace(v)
	layouts := []string{time.DateOnly, "02.01.2006", "2.1.2006", "02.01.06"}
	if format != "" {
		layouts = append([]string{goLayout(format)}, layouts...)
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, v); err == nil {
			return t.Format(time.DateOnly), nil
		}
	}
	return "", crud.Invalid("Datum %q nicht lesbar (TT.MM.JJJJ oder JJJJ-MM-TT)", v)
}

func parseNumber(v string) (float64, error) {
	var f float64
	if _, err := fmt.Sscan(strings.ReplaceAll(strings.TrimSpace(v), ",", "."), &f); err != nil {
		return 0, crud.Invalid("Zahl %q nicht lesbar", v)
	}
	return f, nil
}

// goLayout: TT.MM.JJJJ bzw. DD.MM.YYYY → Go-Layout.
func goLayout(f string) string {
	r := strings.NewReplacer("JJJJ", "2006", "YYYY", "2006", "JJ", "06", "YY", "06", "MM", "01", "TT", "02", "DD", "02")
	return r.Replace(f)
}

// clearNotice: Rückmeldung (z. B. „angelegt“) gilt bis zur nächsten Eingabe.
func (m *Module) clearNotice(ctx context.Context, docID string) {
	if _, ok := m.wfVars(ctx, docID)[noticeVar]; ok {
		_ = m.setWfVar(ctx, docID, noticeVar, nil)
	}
}

// noticeVar: Rückmeldung über dem nächsten Schritt (bis zur nächsten Eingabe).
const noticeVar = "notice"

// --- Zurück --------------------------------------------------------------------------------

// trailEntry: Stand nach einem Schritt.
type trailEntry struct {
	Step  string            `json:"step"`
	User  bool              `json:"user"` // Frage – „zurück“ darf hierher (auch übersprungen, nicht entfallen)
	State map[string]string `json:"state"`
	Refs  []string          `json:"refs"` // Object|Schlüssel
	Vars  string            `json:"vars,omitempty"`
}

func (m *Module) snapshot(ctx context.Context, docID, step string, user bool) (trailEntry, error) {
	e := trailEntry{Step: step, User: user, State: map[string]string{}}
	doc := m.docValues(ctx, docID)
	for _, f := range m.stateFields() {
		e.State[f] = doc[f]
	}
	e.Vars = doc["wf_vars"]
	refs, err := m.activeRefs(ctx, docID)
	if err != nil {
		return e, err
	}
	for _, r := range refs {
		e.Refs = append(e.Refs, r.Object+"|"+r.Key)
	}
	return e, nil
}

func (m *Module) trail(ctx context.Context, docID string) []trailEntry {
	var out []trailEntry
	if s := m.text(ctx, `SELECT wf_trail FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

func (m *Module) saveTrail(ctx context.Context, docID string, t []trailEntry) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document SET wf_trail = ? WHERE id = ?`, string(b), docID)
	return err
}

// pushTrail: Stand nach dem Schritt anhängen (Anfang: step leer).
func (m *Module) pushTrail(ctx context.Context, docID, step string, user bool) {
	e, err := m.snapshot(ctx, docID, step, user)
	if err == nil {
		t := m.trail(ctx, docID)
		if step == "" {
			t = nil
		}
		err = m.saveTrail(ctx, docID, append(t, e))
	}
	if err != nil {
		m.log.WarnContext(ctx, "Workflow-Stand nicht gespeichert", "document", docID, "err", err.Error())
	}
}

// hasPreviousQuestion: Es gibt eine beantwortete Frage, zu der „Zurück“ führt.
func (m *Module) hasPreviousQuestion(ctx context.Context, docID string) bool {
	t := m.trail(ctx, docID)
	for i := len(t) - 1; i > 0; i-- {
		if t[i].User {
			return true
		}
	}
	return false
}

// restoreState: Felder, Bezüge und Variablen wie im Stand e (neuere Bezüge werden inaktiv).
func (m *Module) restoreState(ctx context.Context, docID string, e trailEntry) error {
	fields := m.stateFields()
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+2)
	for _, f := range fields {
		sets = append(sets, f+" = ?")
		args = append(args, nilIfEmpty(e.State[f]))
	}
	sets = append(sets, "wf_vars = ?")
	args = append(args, nilIfEmpty(e.Vars))
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, docID)...); err != nil {
		return err
	}
	refs, err := m.activeRefs(ctx, docID)
	if err != nil {
		return err
	}
	for _, r := range refs {
		if !contains(e.Refs, r.Object+"|"+r.Key) {
			if _, err := m.db.Exec(ctx, `UPDATE dms__document_ref SET is_active = ? WHERE document_id = ? AND line_no = ?`, false, docID, r.Line); err != nil {
				return err
			}
		}
	}
	return nil
}

// backTo: zur Frage step (leer = die letzte beantwortete) – deren Antwort und alles danach zurücknehmen.
func (m *Module) backTo(ctx context.Context, d *docRow, step string) error {
	if d.Status != stInbox {
		return crud.Invalid("Dokument ist schon abgelegt – zurück nicht möglich")
	}
	t := m.trail(ctx, d.ID)
	i := len(t) - 1
	for ; i > 0 && !(t[i].User && (step == "" || t[i].Step == step)); i-- {
	}
	if i <= 0 {
		if step != "" {
			return crud.Invalid("Schritt %s ist keine beantwortete Frage dieses Laufs", step)
		}
		return crud.Invalid("Keine vorige Frage – das ist der erste Schritt")
	}
	target, before := t[i], t[i-1]
	if err := m.restoreState(ctx, d.ID, before); err != nil {
		return err
	}
	if err := m.saveTrail(ctx, d.ID, t[:i]); err != nil {
		return err
	}
	var reset []string
	for _, e := range t[i:] {
		reset = append(reset, e.Step)
	}
	if d.WfStep != "" {
		reset = append(reset, d.WfStep)
	}
	m.resetSteps(ctx, d.ID, reset)
	m.markStep(ctx, d.ID, target.Step, stepWaiting, "", "", "")
	if err := m.setWorkflow(ctx, d.ID, target.Step, wfWaiting); err != nil {
		return err
	}
	m.appendLog(ctx, d.ID, "Zurück zu Schritt "+target.Step)
	return nil
}

// backAction: zur vorigen beantworteten Frage.
func (m *Module) backAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	m.clearNotice(ctx, d.ID)
	// Vorschau einer Maske: zurück zur Bearbeitung (Eingaben bleiben).
	if s, err := m.waitingStep(ctx, d); err == nil && s.Kind == kForm {
		if ai := m.aiStateOf(ctx, d, s); ai != nil && ai.Pending != nil {
			if _, err := m.aiDecide(ctx, d, s, btnPreviewNo); err != nil {
				return sdk.Response{}, err
			}
			return m.workflowState(ctx, d.ID)
		}
	}
	if err := m.backTo(ctx, d, ""); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// startOverAction: Workflow von vorn – Stand wie beim Start (Vorgaben des
// Eingangsverzeichnisses bleiben), dann neu starten.
func (m *Module) startOverAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	m.clearNotice(ctx, d.ID)
	if d.Status != stInbox {
		return sdk.Response{}, crud.Invalid("Dokument ist schon abgelegt – von vorn nicht möglich")
	}
	wf := d.WfCode
	if wf == "" && d.Inbox != "" {
		if ib, err := m.loadInbox(ctx, d.CC, d.Inbox); err == nil {
			wf = ib.Workflow
		}
	}
	if wf == "" {
		return sdk.Response{}, crud.Invalid("Dokument hat keinen Workflow")
	}
	if err := m.resetToInbox(ctx, d, wf); err != nil {
		return sdk.Response{}, err
	}
	m.appendLog(ctx, d.ID, "Von vorn")
	if err := m.startWorkflow(ctx, d.ID, wf); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// resetToInbox: Stand wie beim Einlesen – Kopf leer (außer den Feldern restart_keep des
// Workflows), Bezüge inaktiv, dann die Vorgaben des Eingangsverzeichnisses (Neustart, „Von vorn“).
func (m *Module) resetToInbox(ctx context.Context, d *docRow, wf string) error {
	keep := splitList(m.text(ctx, `SELECT restart_keep FROM dms__workflow WHERE code = ?`, wf))
	sets := []string{"id = id"}
	for _, f := range m.stateFields() {
		if !contains(keep, f) {
			sets = append(sets, f+" = NULL")
		}
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+strings.Join(sets, ", ")+` WHERE id = ?`, d.ID); err != nil {
		return err
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document_ref SET is_active = ? WHERE document_id = ? AND is_active = ?`, false, d.ID, true); err != nil {
		return err
	}
	if d.Inbox == "" {
		return nil
	}
	ib, err := m.loadInbox(ctx, d.CC, d.Inbox)
	if err != nil {
		return nil // Eingang gibt es nicht mehr: ohne Vorgaben
	}
	return m.applyInboxParams(ctx, d.ID, ib)
}

// --- Ablauf am Dokument (DmsDocumentStep) ---------------------------------------------------

// Status eines Schritts im Ablauf.
const (
	stepOpen    = "OPEN"
	stepWaiting = "WAITING"
	stepDone    = "DONE"
	stepSkipped = "SKIPPED"
	stepFailed  = "FAILED"
)

var stepStatusOptions = []metamodel.Option{
	{Value: stepOpen, Label: "offen"}, {Value: stepWaiting, Label: "▶ aktuell"}, {Value: stepDone, Label: "✔ erledigt"},
	{Value: stepSkipped, Label: "– übersprungen"}, {Value: stepFailed, Label: "✖ Fehler"},
}

// initOverview: alle aktiven Schritte des Workflows als offen.
func (m *Module) initOverview(ctx context.Context, docID, wf string) error {
	if _, err := m.db.Exec(ctx, `DELETE FROM dms__document_step WHERE document_id = ?`, docID); err != nil {
		return err
	}
	_, err := m.db.Exec(ctx, `INSERT INTO dms__document_step (document_id, step, company_code, name, kind, status, seq)
		SELECT d.id, s.step, d.company_code, s.name, s.kind, ?, 0 FROM dms__step s JOIN dms__document d ON d.id = ?
		WHERE s.workflow = ? AND s.is_active = ?`, stepOpen, docID, wf, true)
	return err
}

// markStep: Stand eines Schritts im Ablauf.
func (m *Module) markStep(ctx context.Context, docID, step, status, outcome, value, info string) {
	call := sdk.CallFromContext(ctx)
	if _, err := m.db.Exec(ctx, `UPDATE dms__document_step SET status = ?, outcome = ?, value = ?, info = ?, changed_at = ?, changed_by = ?,
		seq = (SELECT COALESCE(MAX(seq), 0) + 1 FROM dms__document_step WHERE document_id = ?) WHERE document_id = ? AND step = ?`,
		status, nilIfEmpty(outcome), nilIfEmpty(value), nilIfEmpty(info), now(), nilIfEmpty(firstOf(call.Metadata["username"], call.UserID)),
		docID, docID, step); err != nil {
		m.log.WarnContext(ctx, "Ablauf nicht geschrieben", "document", docID, "err", err.Error())
	}
}

// resetSteps: Schritte wieder offen (nach „zurück“).
func (m *Module) resetSteps(ctx context.Context, docID string, steps []string) {
	for _, s := range steps {
		if _, err := m.db.Exec(ctx, `UPDATE dms__document_step SET status = ?, outcome = NULL, value = NULL, info = NULL WHERE document_id = ? AND step = ?`,
			stepOpen, docID, s); err != nil {
			m.log.WarnContext(ctx, "Ablauf nicht zurückgesetzt", "document", docID, "err", err.Error())
		}
	}
}

func (m *Module) documentStep() *crud.Entity {
	return &crud.Entity{
		Object: docStepObject, Title: "Ablauf der Dokumente", Icon: "icon-step", Table: "dms__document_step", Section: secDocs,
		Keys: []string{"document_id", "step"}, Order: "document_id, step", Filters: []string{"document_id", "status"}, ReadOnly: true,
		TitleField: "name",
		Fields: []crud.Field{
			{Key: "document_id", Label: "Dokument", Type: tText, Listable: true, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true},
			{Key: "step", Label: "Schritt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "kind", Label: "Art", Type: tSel, Listable: true, ReadOnly: true, Options: kindOptions},
			{Key: "status", Label: "Stand", Type: tSel, Listable: true, ReadOnly: true, Options: stepStatusOptions},
			{Key: "outcome", Label: "Ergebnis", Type: tSel, Listable: true, ReadOnly: true, Options: outcomeOptions},
			{Key: "value", Label: "Wert", Type: tText, Listable: true, ReadOnly: true},
			{Key: "info", Label: "Hinweis", Type: tText, Listable: true, ReadOnly: true},
			{Key: "seq", Label: "Reihenfolge im Lauf", Type: tNum, ReadOnly: true},
			{Key: "changed_at", Label: "Am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Von", Type: tText, ReadOnly: true},
		},
		Access: &crud.Access{Object: docObject, Records: true, CompanyCode: "company_code"},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "backHere", Label: "Hierher zurück", Record: true,
				Confirm: "Zu diesem Schritt zurück? Seine Antwort und alles danach werden zurückgenommen."}, Handle: m.backHereAction},
		},
	}
}

// backHereAction: Ablauf → „Hierher zurück“ zu einer beantworteten Frage.
func (m *Module) backHereAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	ent := m.set.Entity(docStepObject)
	key, err := ent.ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	rec, err := ent.Load(ctx, key)
	if err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, map[string]any{"id": crud.Str(rec["document_id"])}, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	m.clearNotice(ctx, d.ID)
	if err := m.backTo(ctx, d, crud.Str(rec["step"])); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// overviewSorted: Schritte des Ablaufs in Workflow-Reihenfolge (für Tests und Probelauf).
func (m *Module) overview(ctx context.Context, docID string) []map[string]string {
	res, err := m.db.Query(ctx, `SELECT step, name, status, outcome, value, info FROM dms__document_step WHERE document_id = ? ORDER BY step`, docID)
	if err != nil {
		return nil
	}
	var out []map[string]string
	for _, r := range res.Rows {
		out = append(out, map[string]string{"step": crud.Str(r[0]), "name": crud.Str(r[1]), "status": crud.Str(r[2]),
			"outcome": crud.Str(r[3]), "value": crud.Str(r[4]), "info": crud.Str(r[5])})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i]["step"] < out[j]["step"] })
	return out
}
