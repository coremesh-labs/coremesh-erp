package dms

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Maske (Schrittart FORM): Objekt mit KI vorbelegen und anlegen. Die Objekt-Anfrage
// des Moduls ai kommt aus den Zuordnungen „KI-Anfrage je Object“ (die, deren Object
// zur Kategorie des Dokuments gehört; ein Eintrag gilt immer). Die KI bekommt den
// erkannten Text und den Kontext des Schritts; das Ergebnis zeigt die Maske mit den
// Vorgaben (Zuordnungen „Vorgabe“, gruppe.feld = Wert) und Werten aus dem Dokument
// (Zuordnungen „Wert aus dem Dokument“, nur leere Felder). „Anlegen“ legt das
// Objekt mit seinen Unter-Objekten an und setzt das Ziel des Schritts (Feld, z. B.
// Geschäftspartner, oder Bezug).
//
// Vorbelegung (Zuordnung „Vorbelegung“, Object = Object.Action): Ist ein Vertrag am
// Dokument, liefert die Action einen Entwurf; mit Positionen, wenn die Bedingungen
// „Positionen aus der Vorbelegung, wenn“ erfüllt sind (keine = immer), sonst nur den
// Kopf. Mit Positionen fragt der Schritt die KI mit der Anfrage aus „KI-Anfrage bei
// Positionen aus der Vorbelegung“ (z. B. nur den Kopf); bei den Feldern ai_wins geht
// die KI vor.
//
// Knöpfe: Anlegen (mit Bestätigung erst „Vorschau“, dann „Ja, so anlegen“ / „Nein,
// zurück“), KI liest die Datei (file_input, Dateiarten file_types), KI neu fragen,
// Überspringen (mit Option). Antworten der KI kommen in den Zwischenspeicher des
// Dokuments; „KI neu fragen“ verwirft sie.

// aiRequestFor: Object und Anfrage des Schritts für das Dokument.
func (m *Module) aiRequestFor(ctx context.Context, d *docRow, s *stepDef) (object, request string, err error) {
	if f, ok := ctx.Value(forcedKey{}).(forcedRequest); ok {
		return f.Object, f.Request, nil
	}
	entries := s.mappings(mapRequest, m.categoryOf(ctx, d))
	var valid []mapping
	for _, e := range entries {
		if e.Field != "" && strings.TrimSpace(e.Value) != "" {
			valid = append(valid, e)
		}
	}
	if len(valid) == 0 {
		return "", "", crud.Invalid("Schritt %s: keine KI-Anfrage (Zuordnung „KI-Anfrage je Object“) für Kategorie %s", s.Step, firstOf(m.categoryOf(ctx, d), "–"))
	}
	if len(valid) == 1 {
		return valid[0].Field, strings.TrimSpace(valid[0].Value), nil
	}
	objs, _ := m.docObjects(ctx, d)
	for _, e := range valid {
		for _, o := range objs {
			if o.Object == e.Field {
				return e.Field, strings.TrimSpace(e.Value), nil
			}
		}
	}
	return "", "", crud.Invalid("Zur Dokumentkategorie %s passt keine KI-Anfrage des Schritts %s", firstOf(m.categoryOf(ctx, d), "–"), s.Step)
}

// aiDefaults: Vorgaben „gruppe.feld“ → Wert (Platzhalter ersetzt, leere entfallen).
func (m *Module) aiDefaults(ctx context.Context, d *docRow, s *stepDef) map[string]string {
	out := map[string]string{}
	for _, p := range s.mappings(mapDefault, m.categoryOf(ctx, d)) {
		if v := strings.TrimSpace(m.expand(ctx, d, p.Value)); p.Field != "" && v != "" && v != "–" {
			out[p.Field] = v
		}
	}
	return out
}

// aiState ist der Stand der Maske am Dokument (wf_vars „ai.<Schritt>“).
type aiState struct {
	Object  string `json:"object"`
	Request string `json:"request"` // Struktur der Maske und Anlage
	// RunRequest: an die KI gesendete Anfrage, wenn sie abweicht (z. B. nur der Kopf,
	// die Positionen kommen aus dem Vertrag).
	RunRequest string         `json:"run_request,omitempty"`
	CacheKey   string         `json:"cache_key,omitempty"`
	LogID      string         `json:"log_id"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Result     map[string]any `json:"result"`
	Cached     bool           `json:"cached,omitempty"`
	File       bool           `json:"file,omitempty"`    // die KI hat auch die Datei gelesen
	Pending    map[string]any `json:"pending,omitempty"` // Vorschau: geprüfte Werte, noch nicht angelegt
}

// aiAnswer: Antwort der KI im Zwischenspeicher.
type aiAnswer struct {
	LogID   string         `json:"log_id"`
	Status  string         `json:"status"`
	Message string         `json:"message"`
	Result  map[string]any `json:"result"`
}

func (m *Module) aiStateOf(ctx context.Context, d *docRow, s *stepDef) *aiState {
	raw, ok := m.wfVars(ctx, d.ID)["ai."+s.Step]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var st aiState
	if json.Unmarshal(b, &st) != nil {
		return nil
	}
	return &st
}

// aiPrefill: Maske vorbelegen (einmal je Betreten; die Antwort der KI aus dem Zwischenspeicher).
func (m *Module) aiPrefill(ctx context.Context, d *docRow, s *stepDef) {
	m.aiPrefillWith(ctx, d, s, false, false)
}

// aiPrefillWith: withFile – die KI bekommt zum Text auch die Datei; fresh – KI neu fragen.
func (m *Module) aiPrefillWith(ctx context.Context, d *docRow, s *stepDef, withFile, fresh bool) {
	object, request, err := m.aiRequestFor(ctx, d, s)
	mode := m.prefillMode(ctx, d, s)
	run := m.aiRunRequest(ctx, d, s, object, request, mode)
	st := aiState{Object: object, Request: request, File: withFile}
	if run != request {
		st.RunRequest = run
	}
	if old := m.aiStateOf(ctx, d, s); !withFile && !fresh && old != nil && old.Request == request && old.RunRequest == st.RunRequest && old.Status != "" && old.Status != "ERROR" {
		return
	}
	if err != nil {
		st.Status, st.Message = "ERROR", err.Error()
	} else {
		text := m.docText(ctx, d.ID)
		ctxText := m.expandLines(ctx, d, s.text(ctx, "context", s.Context))
		in := map[string]any{"request": run, "company_code": d.CC, "context": ctxText}
		if strings.TrimSpace(text) != "" {
			in["text"] = text
		}
		file := withFile || strings.TrimSpace(text) == "" && strings.HasPrefix(d.ContentType, "image/")
		if file {
			if b, err := m.content(ctx, d); err == nil {
				in["input"], in["content_type"] = base64.StdEncoding.EncodeToString(b), d.ContentType
			}
		}
		st.CacheKey = aiCacheKey("FORM", run, text, ctxText, fmt.Sprint(file), d.SHA)
		var ans aiAnswer
		if fresh {
			m.aiCacheDrop(ctx, d.ID, st.CacheKey)
		}
		switch {
		case m.aiCacheGet(ctx, d.ID, st.CacheKey, &ans):
			st.Cached = true
		case dryRunOf(ctx) != nil:
			ans = aiAnswer{Status: "ERROR", Message: "Probelauf: KI nicht gefragt (keine gespeicherte Antwort)"}
		default:
			resp, err := m.services.Call(ctx, "Ai", "run", in)
			if err != nil {
				ans = aiAnswer{Status: "ERROR", Message: "KI nicht verfügbar: " + err.Error()}
			} else {
				_ = sdk.Decode(resp.Payload, &ans)
				if ans.Status == "DONE" {
					m.aiCachePut(ctx, d.ID, st.CacheKey, ans)
				}
			}
		}
		st.LogID, st.Status, st.Message, st.Result = ans.LogID, ans.Status, ans.Message, ans.Result
		if st.Cached {
			st.Message += " (gespeicherte Antwort)"
		}
	}
	if pre, note := m.aiPrefillFrom(ctx, d, s, object, mode); pre != nil {
		st.Result = mergePrefill(pre, st.Result, aiWins(s.AIWins))
		st.Message += " – " + note
	}
	if request != "" {
		var used []string
		if st.Result, used = m.applyHints(ctx, d, s, request, st.Result); len(used) > 0 {
			st.Message += " – aus dem Dokument: " + strings.Join(used, ", ")
		}
	}
	if withFile {
		st.Message += " – mit Datei"
	}
	if err := m.setWfVar(ctx, d.ID, "ai."+s.Step, st); err != nil {
		m.log.WarnContext(ctx, "KI-Ergebnis nicht gespeichert", "doc", d.ID, "err", err.Error())
	}
	m.appendLog(ctx, d.ID, fmt.Sprintf("%s %s: KI %s – %s", s.Step, s.Name, firstOf(st.LogID, run), st.Message))
}

// formButtons: Knöpfe der Maske (Vorschau: Ja/Nein; sonst Anlegen, Datei, neu fragen, Überspringen).
func (m *Module) formButtons(ctx context.Context, d *docRow, s *stepDef, ai *aiState) []metamodel.FormButton {
	if ai != nil && ai.Pending != nil {
		return []metamodel.FormButton{
			{Value: btnPreviewOK, Label: s.text(ctx, "button."+btnPreviewOK, "Ja, so anlegen"), Primary: true, NoValidate: true},
			{Value: btnPreviewNo, Label: s.text(ctx, "button."+btnPreviewNo, "Nein, zurück zur Bearbeitung"), NoValidate: true},
		}
	}
	out := m.buttons(ctx, d, s)
	if ai == nil || ai.Request == "" {
		return out
	}
	extra := []metamodel.FormButton{}
	if m.fileAsk(ctx, d, s) && !ai.File {
		extra = append(extra, metamodel.FormButton{Value: btnAIFile, Label: s.text(ctx, "button."+btnAIFile, "KI liest die Datei"), NoValidate: true})
	}
	extra = append(extra, metamodel.FormButton{Value: btnAIRefresh, Label: s.text(ctx, "button."+btnAIRefresh, "KI neu fragen"), NoValidate: true,
		Confirm: "KI neu fragen? Eingaben in der Maske gehen verloren."})
	if len(out) == 0 {
		return extra
	}
	return append(append([]metamodel.FormButton{out[0]}, extra...), out[1:]...)
}

// aiFormState: Maske des Schritts – die Felder des Objekts, vorbelegt.
func (m *Module) aiFormState(ctx context.Context, d *docRow, s *stepDef, st *metamodel.FormState) {
	ai := m.aiStateOf(ctx, d, s)
	if ai == nil {
		m.aiPrefill(ctx, d, s)
		ai = m.aiStateOf(ctx, d, s)
	}
	st.Buttons = m.formButtons(ctx, d, s, ai)
	if ai == nil || ai.Request == "" {
		if ai != nil {
			st.Message += " – " + ai.Message
		}
		return
	}
	result := ai.Result
	if ai.Pending != nil {
		result = ai.Pending
	}
	resp, err := m.services.Call(ctx, "Ai", "form", map[string]any{"request": ai.Request, "result": result,
		"defaults": m.aiDefaults(ctx, d, s), "company_code": d.CC})
	if err != nil {
		st.Message += " – " + err.Error()
		return
	}
	var f struct {
		Fields []metamodel.FieldDefinition `json:"fields"`
		Values map[string]string           `json:"values"`
	}
	if err := sdk.Decode(resp.Payload, &f); err != nil {
		st.Message += " – " + err.Error()
		return
	}
	st.Extra = f.Fields
	ro := ai.Pending != nil
	for k, v := range f.Values {
		v := v
		fs := metamodel.FieldState{Value: &v}
		if ro {
			fs.ReadOnly = &ro
		}
		st.Fields[k] = fs
	}
	if ro {
		for _, fd := range f.Fields {
			if _, ok := st.Fields[fd.Key]; !ok {
				st.Fields[fd.Key] = metamodel.FieldState{ReadOnly: &ro}
			}
		}
		st.Message += " – Vorschau: So wird es angelegt. Erst „Ja, so anlegen“ legt es an."
		return
	}
	if m.fileAsk(ctx, d, s) && !ai.File {
		st.Message += " – Absender nicht erkannt? „KI liest die Datei“ schickt auch die Datei an die KI (Briefkopf, Bilder)."
	}
	next := "„Anlegen“ legt es an."
	if s.Confirm {
		next = "„Vorschau“ zeigt, was angelegt wird."
	}
	switch ai.Status {
	case "DONE":
		st.Message += " – von der KI vorbelegt (Protokoll " + ai.LogID + ai.extra() + "); bitte prüfen und ergänzen, " + next
	default:
		st.Message += " – keine Vorbelegung durch die KI (" + ai.Message + "); bitte ausfüllen, " + next
	}
}

// extra: Zusatz zur Meldung (Hinweise, Datei) – der Teil der Meldung nach dem Status.
func (a *aiState) extra() string {
	var parts []string
	if a.Cached {
		parts = append(parts, "gespeicherte Antwort")
	}
	if _, rest, ok := strings.Cut(a.Message, " – "); ok {
		parts = append(parts, rest)
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, " – ")
}

// fileAsk: Knopf „KI liest die Datei“ passt (file_input; Dateiarten laut file_types, leer = alle).
func (m *Module) fileAsk(_ context.Context, d *docRow, s *stepDef) bool {
	if s.Kind != kForm || !s.FileInput {
		return false
	}
	return strings.TrimSpace(s.FileTypes) == "" || evalOp(opIn, d.ContentType, s.FileTypes)
}

// aiStateFor: Stand der KI am Schritt, sonst die Anfrage ohne Ergebnis.
func (m *Module) aiStateFor(ctx context.Context, d *docRow, s *stepDef) (*aiState, error) {
	ai := m.aiStateOf(ctx, d, s)
	if ai == nil || ai.Request == "" {
		object, request, err := m.aiRequestFor(ctx, d, s)
		if err != nil {
			return nil, err
		}
		ai = &aiState{Object: object, Request: request}
	}
	return ai, nil
}

// formContinue: Knopf der Maske. done = Schritt erledigt (value = ID des angelegten
// Objekts, leer = übersprungen); sonst bleibt die Maske (Vorschau, neu vorbelegt).
func (m *Module) formContinue(ctx context.Context, d *docRow, s *stepDef, button string, data map[string]any) (bool, string, error) {
	switch button {
	case btnAIFile:
		if !m.fileAsk(ctx, d, s) {
			return false, "", crud.Invalid("Schritt %s %s: Datei an die KI ist hier nicht vorgesehen", s.Step, s.Name)
		}
		m.aiPrefillWith(ctx, d, s, true, false)
		return false, "", nil
	case btnAIRefresh:
		ai := m.aiStateOf(ctx, d, s)
		m.aiPrefillWith(ctx, d, s, ai != nil && ai.File, true)
		return false, "", nil
	case btnPreviewOK, btnPreviewNo:
		id, err := m.aiDecide(ctx, d, s, button)
		return button == btnPreviewOK && err == nil, id, err
	case oSkipped:
		if s.option(oSkipped) == nil {
			return false, "", crud.Invalid("Schritt %s %s: Überspringen ist hier nicht vorgesehen", s.Step, s.Name)
		}
		_ = m.setWfVar(ctx, d.ID, "ai."+s.Step, nil)
		return true, "", nil
	case oCreated:
		if s.Confirm {
			return false, "", m.aiPreview(ctx, d, s, data)
		}
		id, err := m.aiCreate(ctx, d, s, data)
		return err == nil, id, err
	}
	return false, "", crud.Invalid("Schritt %s %s: Knopf %q gibt es hier nicht", s.Step, s.Name, button)
}

// formTargetSet: Ziel des Schritts schon belegt (Feld bzw. Bezug zum Object der Anfrage).
func (m *Module) formTargetSet(ctx context.Context, d *docRow, s *stepDef) bool {
	t, arg := splitTarget(s.targetKey())
	if t != "ref" {
		return m.targetValue(ctx, d, s.targetKey()) != ""
	}
	object := arg
	if object == "" {
		o, _, err := m.aiRequestFor(ctx, d, s)
		if err != nil {
			return false
		}
		object = o
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	for _, r := range refs {
		if r.Object == object {
			return true
		}
	}
	return false
}

// aiCreate: Werte der Maske → Objekt anlegen → am Dokument setzen; liefert die ID.
func (m *Module) aiCreate(ctx context.Context, d *docRow, s *stepDef, data map[string]any) (string, error) {
	ai, err := m.aiStateFor(ctx, d, s)
	if err != nil {
		return "", err
	}
	result, err := m.aiCollect(ctx, d, ai, data)
	if err != nil {
		return "", err
	}
	return m.aiCreateFrom(ctx, d, s, ai, result)
}

// aiPreview: Werte der Maske prüfen und als Vorschau merken (Bestätigung).
func (m *Module) aiPreview(ctx context.Context, d *docRow, s *stepDef, data map[string]any) error {
	ai, err := m.aiStateFor(ctx, d, s)
	if err != nil {
		return err
	}
	result, err := m.aiCollect(ctx, d, ai, data)
	if err != nil {
		return err
	}
	ai.Pending = result
	return m.setWfVar(ctx, d.ID, "ai."+s.Step, ai)
}

// aiDecide: Antwort auf die Vorschau – Ja legt an (ID), Nein zurück zur Maske mit den Eingaben ("").
func (m *Module) aiDecide(ctx context.Context, d *docRow, s *stepDef, choice string) (string, error) {
	ai, err := m.aiStateFor(ctx, d, s)
	if err != nil {
		return "", err
	}
	switch choice {
	case btnPreviewOK:
		return m.aiCreateFrom(ctx, d, s, ai, ai.Pending)
	case btnPreviewNo:
		ai.Result, ai.Pending = ai.Pending, nil
		return "", m.setWfVar(ctx, d.ID, "ai."+s.Step, ai)
	}
	return "", crud.Invalid("Bitte „Ja, so anlegen“ oder „Nein, zurück zur Bearbeitung“ wählen")
}

// aiCollect: Werte der Maske → Struktur (geprüft vom Modul ai).
func (m *Module) aiCollect(ctx context.Context, d *docRow, ai *aiState, data map[string]any) (map[string]any, error) {
	values := map[string]any{}
	for k, v := range data {
		if strings.HasPrefix(k, "f__") {
			values[k] = v
		}
	}
	resp, err := m.services.Call(ctx, "Ai", "collect", map[string]any{"request": ai.Request, "values": values, "company_code": d.CC})
	if err != nil {
		return nil, err
	}
	var col struct {
		Result map[string]any `json:"result"`
	}
	if err := sdk.Decode(resp.Payload, &col); err != nil {
		return nil, err
	}
	return col.Result, nil
}

// aiCreateFrom: Struktur anlegen, Ziel setzen, Rückmeldung für den nächsten Schritt.
func (m *Module) aiCreateFrom(ctx context.Context, d *docRow, s *stepDef, ai *aiState, result map[string]any) (string, error) {
	if result == nil {
		return "", crud.Invalid("Nichts zum Anlegen – bitte die Maske ausfüllen")
	}
	resp, err := m.services.Call(ctx, "Ai", "create", map[string]any{"request": ai.Request, "result": result,
		"defaults": m.aiDefaults(ctx, d, s), "company_code": d.CC})
	if err != nil {
		return "", err
	}
	var cr struct {
		Object string `json:"object"`
		ID     string `json:"id"`
	}
	if err := sdk.Decode(resp.Payload, &cr); err != nil || cr.ID == "" {
		return "", crud.Invalid("%s nicht angelegt", firstOf(cr.Object, ai.Object))
	}
	object := firstOf(cr.Object, ai.Object)
	key, value := s.targetKey(), cr.ID
	if t, arg := splitTarget(key); t == "ref" && arg == "" {
		key, value = "ref:"+object, cr.ID
	} else if t == "" {
		key, value = "ref:"+object, cr.ID // ohne Ziel: Bezug
	}
	if _, err := m.setTargetValue(ctx, d, key, value, ""); err != nil {
		return "", err
	}
	_ = m.setWfVar(ctx, d.ID, "ai."+s.Step, nil)
	note := m.objectLabel(ctx, d, object) + " " + cr.ID + " angelegt"
	if key == "partner_id" {
		note = "Geschäftspartner " + m.partnerName(ctx, cr.ID) + " (" + cr.ID + ") angelegt und am Dokument eingetragen"
	}
	_ = m.setWfVar(ctx, d.ID, noticeVar, note)
	m.appendLog(ctx, d.ID, s.Step+" "+s.Name+": "+note)
	return cr.ID, nil
}

// objectLabel: Bezeichnung des Objects laut Kategorie des Dokuments (Katalog), sonst der Name.
func (m *Module) objectLabel(ctx context.Context, d *docRow, object string) string {
	objs, _ := m.docObjects(ctx, d)
	for _, o := range objs {
		if o.Object == object && o.Label != "" {
			return o.Label
		}
	}
	return object
}

func (m *Module) text(ctx context.Context, q string, args ...any) string {
	res, err := m.db.Query(ctx, q, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

// --- Nachträglich anlegen (Aktion am Dokument, auch abgelegt) --------------------------

// forcedRequest: Anfrage, die die Aktion „Objekt mit KI anlegen …“ vorgibt.
type forcedRequest struct {
	Object, Request string
	Items           *bool // Positionen aus dem Vertrag übernehmen (Feld contract_items)
}
type forcedKey struct{}

// laterChoices: Anfragen der Masken im Workflow des Dokuments (Wert Schritt|Object|Anfrage).
func (m *Module) laterChoices(ctx context.Context, d *docRow) []metamodel.Option {
	wf := firstOf(d.WfCode, erpWorkflow)
	res, err := m.db.Query(ctx, `SELECT s.step, p.field, p.value FROM dms__step s JOIN dms__step_mapping p ON p.workflow = s.workflow AND p.step = s.step
		AND p.role = ? AND p.is_active = ? WHERE s.workflow = ? AND s.kind = ? AND s.is_active = ? ORDER BY s.step, p.line_no`, mapRequest, true, wf, kForm, true)
	if err != nil {
		return nil
	}
	var out []metamodel.Option
	seen := map[string]bool{}
	for _, r := range res.Rows {
		o, q := strings.TrimSpace(crud.Str(r[1])), strings.TrimSpace(crud.Str(r[2]))
		if o == "" || q == "" || seen[q] {
			continue
		}
		seen[q] = true
		name := q
		if resp, err := m.services.Call(ctx, "AiRequest", "get", map[string]any{"id": q}); err == nil {
			var rq struct {
				Name string `json:"name"`
			}
			if sdk.Decode(resp.Payload, &rq) == nil && rq.Name != "" {
				name = rq.Name
			}
		}
		out = append(out, metamodel.Option{Value: crud.Str(r[0]) + "|" + o + "|" + q, Label: name})
	}
	return out
}

// laterStep: Schritt und vorgegebene Anfrage zur Auswahl „Schritt|Object|Anfrage“.
func (m *Module) laterStep(ctx context.Context, d *docRow, choice string, items bool) (context.Context, *stepDef, error) {
	parts := strings.Split(choice, "|")
	if len(parts) != 3 {
		return ctx, nil, crud.Invalid("Bitte wählen, was angelegt werden soll")
	}
	s, err := m.loadStep(ctx, firstOf(d.WfCode, erpWorkflow), parts[0])
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, forcedKey{}, forcedRequest{Object: parts[1], Request: parts[2], Items: &items}), s, nil
}

func (m *Module) laterFormState(ctx context.Context, d *docRow, values map[string]string) metamodel.FormState {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	opts := m.laterChoices(ctx, d)
	st.Fields["ai_choice"] = metamodel.FieldState{Options: opts}
	if m.docContract(ctx, d) == "" {
		hidden := false
		st.Fields["contract_items"] = metamodel.FieldState{Visible: &hidden}
	}
	st.Message = "Objekt nachträglich aus dem Dokument anlegen (auch nach dem Ablegen): wählen, was entstehen soll – die KI füllt die Maske vor."
	if values["ai_choice"] == "" {
		return st
	}
	ctx2, s, err := m.laterStep(ctx, d, values["ai_choice"], crud.AsBool(values["contract_items"]))
	if err != nil {
		st.Message = err.Error()
		return st
	}
	st.Message = ""
	m.aiFormState(ctx2, d, s, &st)
	st.Buttons = nil // eigene Aktion, ihr Knopf legt an
	st.Message = strings.TrimPrefix(st.Message, " – ")
	return st
}

func (m *Module) aiCreateLaterAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	ctx2, s, err := m.laterStep(ctx, d, crud.Str(in.Data["ai_choice"]), crud.AsBool(in.Data["contract_items"]))
	if err != nil {
		return sdk.Response{}, err
	}
	id, err := m.aiCreate(ctx2, d, s, in.Data)
	if err != nil {
		return sdk.Response{}, err
	}
	m.appendLog(ctx, d.ID, "Nachträglich angelegt (KI): "+id)
	return sdk.Response{Payload: map[string]any{"message": "Angelegt und am Dokument eingetragen: " + id}}, nil
}

// --- Vorbelegung (Zuordnung „Vorbelegung“) ----------------------------------------------

const (
	prefillHead = "head" // nur der Kopf aus der Vorbelegung, die KI liest die Positionen
	prefillFull = "full" // Kopf und Positionen aus der Vorbelegung
)

// aiPrefillFrom: Ist ein Vertrag am Dokument, liefert Object.Action der Vorbelegung
// den Entwurf {result: …} zum Vertrag.
func (m *Module) aiPrefillFrom(ctx context.Context, d *docRow, s *stepDef, object, mode string) (map[string]any, string) {
	contract := m.docContract(ctx, d)
	if contract == "" || mode == "" {
		return nil, ""
	}
	for _, p := range s.mappings(mapPrefill, m.categoryOf(ctx, d)) {
		if p.Field != object {
			continue
		}
		obj, action, ok := strings.Cut(strings.TrimSpace(p.Value), ".")
		if !ok {
			continue
		}
		resp, err := m.services.Call(ctx, obj, action, map[string]any{"company_code": d.CC, "contract_id": strings.TrimPrefix(contract, d.CC+"|"),
			"date": firstOf(d.DocDate, today())})
		if err != nil {
			return nil, "Vorbelegung aus dem Vertrag nicht möglich: " + err.Error()
		}
		var out struct {
			Result map[string]any `json:"result"`
		}
		if sdk.Decode(resp.Payload, &out) != nil || out.Result == nil {
			return nil, ""
		}
		if mode == prefillHead {
			for k, v := range out.Result {
				if _, list := v.([]any); list {
					delete(out.Result, k)
				}
			}
			return out.Result, "Kopf vorbelegt aus Vertrag " + strings.TrimPrefix(contract, d.CC+"|")
		}
		return out.Result, "vorbelegt aus Vertrag " + strings.TrimPrefix(contract, d.CC+"|")
	}
	return nil, ""
}

// docContract: Vertrag am Dokument (Schlüssel mit Buchungskreis) oder "".
func (m *Module) docContract(ctx context.Context, d *docRow) string {
	contract := ""
	refs, _ := m.activeRefs(ctx, d.ID)
	for _, r := range refs {
		if r.Object == "Contract" {
			contract = r.Key
		}
	}
	return contract
}

// prefillMode: keine Vorbelegung (kein Vertrag, keine Zuordnung), mit Positionen
// (Bedingungen erfüllt bzw. keine; nachträglich: Feld contract_items) oder nur der Kopf.
func (m *Module) prefillMode(ctx context.Context, d *docRow, s *stepDef) string {
	if len(s.mappings(mapPrefill, m.categoryOf(ctx, d))) == 0 || m.docContract(ctx, d) == "" {
		return ""
	}
	conds := s.conditions(condPrefill)
	if f, ok := ctx.Value(forcedKey{}).(forcedRequest); ok && f.Items != nil && len(conds) > 0 {
		if *f.Items {
			return prefillFull
		}
		return prefillHead
	}
	if ok, _ := m.allHold(ctx, d, conds); !ok {
		return prefillHead
	}
	return prefillFull
}

// aiRunRequest: Anfrage an die KI – bei Positionen aus der Vorbelegung die aus
// „KI-Anfrage bei Positionen aus der Vorbelegung“, sonst die des Schritts.
func (m *Module) aiRunRequest(ctx context.Context, d *docRow, s *stepDef, object, request, mode string) string {
	if mode != prefillFull {
		return request
	}
	for _, p := range s.mappings(mapRequestPrefill, m.categoryOf(ctx, d)) {
		if p.Field == object && strings.TrimSpace(p.Value) != "" {
			return strings.TrimSpace(p.Value)
		}
	}
	return request
}

// aiWins: Felder, bei denen das Ergebnis der KI der Vorbelegung vorgeht (ai_wins).
func aiWins(param string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(param) == "" {
		param = "header_text,amount,service_from,service_to"
	}
	for _, f := range strings.Split(param, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out[f] = true
		}
	}
	return out
}

// mergePrefill: Vorbelegung (z. B. aus dem Vertrag) und Ergebnis der KI – die
// Vorbelegung geht vor, außer bei den Feldern wins (wenn die KI sie gefüllt hat);
// Listen gleicher Länge Zeile für Zeile, sonst die Liste der Vorbelegung.
func mergePrefill(pre, ai map[string]any, wins map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range ai {
		out[k] = v
	}
	for k, pv := range pre {
		switch p := pv.(type) {
		case map[string]any:
			a, _ := ai[k].(map[string]any)
			out[k] = mergeRecord(p, a, wins)
		case []any:
			a, _ := ai[k].([]any)
			if len(p) == 0 {
				continue
			}
			if len(a) == len(p) {
				rows := make([]any, len(p))
				for i := range p {
					pr, _ := p[i].(map[string]any)
					ar, _ := a[i].(map[string]any)
					rows[i] = mergeRecord(pr, ar, wins)
				}
				out[k] = rows
			} else {
				out[k] = p
			}
		default:
			if _, ok := ai[k]; !ok {
				out[k] = pv
			}
		}
	}
	return out
}

func mergeRecord(pre, ai map[string]any, wins map[string]bool) map[string]any {
	out := map[string]any{}
	for k, v := range ai {
		out[k] = v
	}
	for k, v := range pre {
		if v == nil || fmt.Sprint(v) == "" {
			continue
		}
		if a, ok := ai[k]; ok && wins[k] && a != nil && fmt.Sprint(a) != "" {
			continue
		}
		out[k] = v
	}
	return out
}
