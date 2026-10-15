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

// Objekt mit KI anlegen (Schrittart AI_OBJECT, seit 0.5.0): Der Schritt nimmt
// die Objekt-Anfrage des Moduls ai, die zur Kategorie des Dokuments passt
// (Parameter requests „SupplierInvoice=SUPPLIER_INVOICE; Contract=SUPPLIER_CONTRACT“,
// ein Eintrag gilt immer), schickt den erkannten Text mit den Zusatzzeilen
// (Parameter context, Platzhalter wie in Fragen) und zeigt das Ergebnis als
// Eingabemaske mit Vorgaben (Parameter defaults „invoice.company_code={company_code};
// invoice.supplier_id={doc.partner_id}“). „Weiter“ legt das Objekt mit seinen
// Unter-Objekten an und setzt es am Dokument (Parameter bind: partner = Geschäftspartner,
// sonst Bezug). Ein Fehler der KI hält nichts auf: Die Maske bleibt dann leer.
//
// Bestätigung (Parameter confirm=true, seit 0.10.0): „Weiter“ zeigt erst eine Vorschau
// (die Maske nur lesbar); angelegt wird erst auf „Ja, so anlegen“, „Nein“ führt
// zurück zur Maske. Nach dem Anlegen steht die Rückmeldung über dem nächsten Schritt.
//
// Datei an die KI (Parameter file_input=ask): Knopf „KI liest die Datei“ – die KI
// bekommt auf Nachfrage zum Text auch die Datei (PDF, Bild), z. B. wenn der
// Absender nur im Briefkopf steht. Vorbelegung aus dem Dokument: Parameter hints
// (siehe hints.go).
//
// Auswahlschritte bieten unten rechts „Anlegen“, wenn sie eine Option mit dem
// Ergebnis CREATE haben (z. B. weiter zu einem AI_OBJECT-Schritt).
//
// Variablen (Ziel var, Parameter var): Auswahl eines Schritts für spätere Schritte,
// z. B. die Vertragsart – Platzhalter {var.<Name>} und {var.<Name>_label}.

const (
	kAIObject = "AI_OBJECT"
	oCreate   = "CREATE"
	btnCreate = "createNew"
	btnFile   = "aiReadFile"

	// Auswahl im Schritt mit Parameter optional=true
	aiCreateChoice = "CREATE"
	aiSkipChoice   = "SKIP"
)

// --- Variablen des Workflows (dms__document.wf_vars, JSON) ------------------------------

func (m *Module) wfVars(ctx context.Context, docID string) map[string]any {
	out := map[string]any{}
	if s := m.text(ctx, `SELECT wf_vars FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

func (m *Module) setWfVar(ctx context.Context, docID, key string, v any) error {
	vars := m.wfVars(ctx, docID)
	if v == nil {
		delete(vars, key)
	} else {
		vars[key] = v
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document SET wf_vars = ? WHERE id = ?`, string(b), docID)
	return err
}

func (m *Module) varText(ctx context.Context, docID, name string) string {
	v := m.wfVars(ctx, docID)["var."+name]
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// --- Anfrage zum Dokument ---------------------------------------------------------------

// aiRequestFor: Object und Anfrage des Schritts – der Eintrag, dessen Object zur
// Kategorie des Dokuments gehört; ein einziger Eintrag gilt immer.
func (m *Module) aiRequestFor(ctx context.Context, d *docRow, s *stepDef) (object, request string, err error) {
	if f, ok := ctx.Value(forcedKey{}).(forcedRequest); ok {
		return f.Object, f.Request, nil
	}
	type entry struct{ object, request string }
	var entries []entry
	for _, p := range strings.Split(s.param(ctx, "requests"), ";") {
		o, r, ok := strings.Cut(strings.TrimSpace(p), "=")
		if ok && strings.TrimSpace(o) != "" && strings.TrimSpace(r) != "" {
			entries = append(entries, entry{strings.TrimSpace(o), strings.TrimSpace(r)})
		}
	}
	if len(entries) == 0 {
		return "", "", crud.Invalid("Schritt %s: Parameter requests (Object=Anfrage) fehlt", s.Step)
	}
	if len(entries) == 1 {
		return entries[0].object, entries[0].request, nil
	}
	objs, _ := m.docObjects(ctx, d)
	for _, e := range entries {
		for _, o := range objs {
			if o.Object == e.object {
				return e.object, e.request, nil
			}
		}
	}
	return "", "", crud.Invalid("Zur Dokumentkategorie %s passt keine KI-Anfrage des Schritts %s", m.categoryOf(ctx, d), s.Step)
}

// aiDefaults: Parameter defaults → „name.feld“: Wert (Platzhalter ersetzt, leere entfallen).
func (m *Module) aiDefaults(ctx context.Context, d *docRow, s *stepDef) map[string]string {
	out := map[string]string{}
	for _, p := range strings.Split(s.param(ctx, "defaults"), ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok {
			continue
		}
		if v = strings.TrimSpace(m.expand(ctx, d, v)); v != "" && v != "–" {
			out[strings.TrimSpace(k)] = v
		}
	}
	return out
}

// aiState ist das Ergebnis der KI am Dokument (wf_vars „ai.<Schritt>“).
type aiState struct {
	Object  string `json:"object"`
	Request string `json:"request"` // Struktur der Maske und Anlage
	// RunRequest: an die KI gesendete Anfrage, wenn sie abweicht (z. B. nur der Kopf,
	// die Positionen kommen aus dem Vertrag).
	RunRequest string         `json:"run_request,omitempty"`
	LogID      string         `json:"log_id"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Result     map[string]any `json:"result"`
	File       bool           `json:"file,omitempty"`    // die KI hat auch die Datei gelesen
	Pending    map[string]any `json:"pending,omitempty"` // Vorschau: geprüfte Werte, noch nicht angelegt
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

// aiPrefill fragt die KI (einmal je Schritt und Dokument, nach einem Fehler beim
// nächsten Betreten erneut) und merkt sich das Ergebnis.
func (m *Module) aiPrefill(ctx context.Context, d *docRow, s *stepDef) {
	m.aiPrefillWith(ctx, d, s, false)
}

// aiPrefillWith: withFile – die KI bekommt zum Text auch die Datei (Knopf „KI liest die Datei“).
func (m *Module) aiPrefillWith(ctx context.Context, d *docRow, s *stepDef, withFile bool) {
	object, request, err := m.aiRequestFor(ctx, d, s)
	mode := m.prefillMode(ctx, d, s)
	run := m.aiRunRequest(ctx, s, object, request, mode)
	st := aiState{Object: object, Request: request, File: withFile}
	if run != request {
		st.RunRequest = run
	}
	if old := m.aiStateOf(ctx, d, s); !withFile && old != nil && old.Request == request && old.RunRequest == st.RunRequest && old.Status != "" && old.Status != "ERROR" {
		return
	}
	if err != nil {
		st.Status, st.Message = "ERROR", err.Error()
	} else {
		in := map[string]any{"request": run, "company_code": d.CC, "context": m.expandLines(ctx, d, s.param(ctx, "context"))}
		text := m.docText(ctx, d.ID)
		if strings.TrimSpace(text) != "" {
			in["text"] = text
		}
		if withFile || strings.TrimSpace(text) == "" && strings.HasPrefix(d.ContentType, "image/") {
			if b, err := m.content(ctx, d); err == nil {
				in["input"], in["content_type"] = base64.StdEncoding.EncodeToString(b), d.ContentType
			}
		}
		resp, err := m.services.Call(ctx, "Ai", "run", in)
		if err != nil {
			st.Status, st.Message = "ERROR", "KI nicht verfügbar: "+err.Error()
		} else {
			var out struct {
				LogID   string         `json:"log_id"`
				Status  string         `json:"status"`
				Message string         `json:"message"`
				Result  map[string]any `json:"result"`
			}
			_ = sdk.Decode(resp.Payload, &out)
			st.LogID, st.Status, st.Message, st.Result = out.LogID, out.Status, out.Message, out.Result
		}
	}
	if pre, note := m.aiPrefillFrom(ctx, d, s, object, mode); pre != nil {
		st.Result = mergePrefill(pre, st.Result, aiWins(s.param(ctx, "ai_wins")))
		st.Message += " – " + note
	}
	if request != "" {
		var used []string
		if st.Result, used = m.applyHints(ctx, d, s, request, st.Result); len(used) > 0 {
			st.Message += " – aus Dokument und Kontoauszug: " + strings.Join(used, ", ")
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

// aiFormState: Maske des Schritts – die Felder des Objekts, vorbelegt.
func (m *Module) aiFormState(ctx context.Context, d *docRow, s *stepDef, st *metamodel.FormState) {
	ai := m.aiStateOf(ctx, d, s)
	if ai == nil {
		m.aiPrefill(ctx, d, s)
		ai = m.aiStateOf(ctx, d, s)
	}
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
		yes := true
		v := oYes
		st.Fields["wf_choice"] = metamodel.FieldState{Options: []metamodel.Option{{Value: oYes, Label: "Ja, so anlegen"},
			{Value: oNo, Label: "Nein, zurück zur Bearbeitung"}}, Value: &v, Required: &yes}
		st.Message += " – Vorschau: So wird es angelegt. Erst „Ja, so anlegen“ und „Weiter“ legt es an."
		return
	}
	if m.fileAsk(ctx, d, s) && !ai.File {
		st.Message += " – Absender nicht erkannt? „KI liest die Datei“ schickt auch die Datei an die KI (Briefkopf, Bilder)."
	}
	next := "„Weiter“ legt es an."
	if s.param(ctx, "confirm") == "true" {
		next = "„Weiter“ zeigt die Vorschau."
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
	if _, rest, ok := strings.Cut(a.Message, " – "); ok {
		return "; " + rest
	}
	return ""
}

// fileAsk: Knopf „KI liest die Datei“ passt (Parameter file_input=ask; Dateiarten laut
// file_types, z. B. „application/pdf, image/*“; leer = alle).
func (m *Module) fileAsk(ctx context.Context, d *docRow, s *stepDef) bool {
	if s.Kind != kAIObject || s.param(ctx, "file_input") != "ask" {
		return false
	}
	types := splitList(s.param(ctx, "file_types"))
	if len(types) == 0 {
		return true
	}
	for _, t := range types {
		if t == d.ContentType || strings.HasSuffix(t, "/*") && strings.HasPrefix(d.ContentType, strings.TrimSuffix(t, "*")) {
			return true
		}
	}
	return false
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

// aiPreview: Werte der Maske prüfen und als Vorschau merken (Parameter confirm).
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
	case oYes:
		return m.aiCreateFrom(ctx, d, s, ai, ai.Pending)
	case oNo:
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

// aiCreateFrom: Struktur anlegen, am Dokument setzen, Rückmeldung für den nächsten Schritt.
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
	if s.param(ctx, "bind") == "partner" {
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET partner_id = ? WHERE id = ?`, cr.ID, d.ID); err != nil {
			return "", err
		}
	} else if err := m.insertRef(ctx, d, cr.Object, cr.ID); err != nil {
		return "", err
	}
	_ = m.setWfVar(ctx, d.ID, "ai."+s.Step, nil)
	note := m.objectLabel(ctx, d, firstOf(cr.Object, ai.Object)) + " " + cr.ID + " angelegt"
	if s.param(ctx, "bind") == "partner" {
		note = "Geschäftspartner " + m.partnerName(ctx, cr.ID) + " (" + cr.ID + ") angelegt und am Dokument eingetragen"
	}
	_ = m.setWfVar(ctx, d.ID, noticeVar, note)
	m.appendLog(ctx, d.ID, s.Step+" "+s.Name+": "+note)
	return cr.ID, nil
}

// noticeVar: Rückmeldung über dem nächsten Schritt (bis zur nächsten Eingabe).
const noticeVar = "notice"

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

// aiReadFileAction: Knopf „KI liest die Datei“ – die KI füllt die Maske neu, mit Text und Datei.
func (m *Module) aiReadFileAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	if !m.fileAsk(ctx, d, s) {
		return sdk.Response{}, crud.Invalid("Schritt %s %s: Datei an die KI ist hier nicht vorgesehen (Parameter file_input=ask)", s.Step, s.Name)
	}
	_ = m.setWfVar(ctx, d.ID, noticeVar, nil)
	m.aiPrefillWith(ctx, d, s, true)
	return m.workflowState(ctx, d.ID)
}

// aiTargetSet: Ziel des Schritts schon belegt (Partner bzw. Bezug zum Object).
func (m *Module) aiTargetSet(ctx context.Context, d *docRow, s *stepDef) bool {
	if s.param(ctx, "bind") == "partner" {
		return d.Partner != ""
	}
	object, _, err := m.aiRequestFor(ctx, d, s)
	if err != nil {
		return false
	}
	refs, _ := m.activeRefs(ctx, d.ID)
	for _, r := range refs {
		if r.Object == object {
			return true
		}
	}
	return false
}

// hasCreate: Der Schritt hat eine Option „Anlegen“.
func hasCreate(s *stepDef) bool {
	_, ok := s.next(oCreate, "")
	return ok
}

// canCreate: „Anlegen“ führt (über entfallende Schritte hinweg) zu einem KI-Schritt
// mit einer Anfrage für dieses Dokument.
func (m *Module) canCreate(ctx context.Context, d *docRow, s *stepDef) bool {
	next, ok := s.next(oCreate, "")
	for i := 0; ok && next != "" && i < 5; i++ {
		ns, err := m.loadStep(ctx, d.WfCode, next)
		if err != nil {
			return false
		}
		if ns.Kind == kAIObject {
			_, _, err := m.aiRequestFor(ctx, d, ns)
			return err == nil
		}
		if applies, _ := m.stepApplies(ctx, d, ns); applies && !userKinds[ns.Kind] {
			return false
		}
		out := oSelected
		if applies, _ := m.stepApplies(ctx, d, ns); !applies {
			out = oSkipped
		}
		next, ok = ns.next(out, "")
	}
	return false
}

// createNewAction: Knopf „Anlegen“ – weiter über die Option CREATE des wartenden Schritts.
func (m *Module) createNewAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	m.clearNotice(ctx, d.ID)
	if !hasCreate(s) || !m.canCreate(ctx, d, s) {
		return sdk.Response{}, crud.Invalid("Schritt %s %s bietet hier kein Anlegen mit KI", s.Step, s.Name)
	}
	if err := m.advance(ctx, d, s, oCreate, "", "Anlegen gewählt"); err != nil {
		return sdk.Response{}, err
	}
	if err := m.runWorkflow(ctx, d.ID); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// aiAsk: Programmschritt KI-Anfrage über das Modul ai (Parameter ai_request,
// eine Text-Anfrage); prompt und {options} kommen als Zusatzzeilen mit.
func (m *Module) aiAsk(ctx context.Context, d *docRow, s *stepDef, allowed []choice, prompt string) (string, error) {
	lines := []string{m.expand(ctx, d, prompt)}
	if len(allowed) > 0 {
		var opts []string
		for _, c := range allowed {
			opts = append(opts, c.Label)
		}
		lines = append(lines, "Antworte nur mit dem Schlüssel vor dem Gedankenstrich, erlaubt: "+strings.Join(opts, "; "))
	}
	resp, err := m.services.Call(ctx, "Ai", "run", map[string]any{"request": s.param(ctx, "ai_request"), "text": m.docText(ctx, d.ID),
		"context": strings.Join(lines, "\n"), "company_code": d.CC})
	if err != nil {
		return "", fmt.Errorf("KI: %w", err)
	}
	var out struct {
		Status, Message, Text string
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	if out.Status != "DONE" {
		return "", fmt.Errorf("KI: %s", out.Message)
	}
	return out.Text, nil
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

// laterChoices: Anfragen der KI-Schritte im Workflow des Dokuments (Wert Schritt|Object|Anfrage).
func (m *Module) laterChoices(ctx context.Context, d *docRow) []metamodel.Option {
	wf := firstOf(d.WfCode, erpWorkflow)
	res, err := m.db.Query(ctx, `SELECT s.step, s.name, p.value FROM dms__step s JOIN dms__step_param p ON p.workflow = s.workflow AND p.step = s.step
		AND p.param = 'requests' AND p.is_active = ? WHERE s.workflow = ? AND s.kind = ? AND s.is_active = ? ORDER BY s.step`, true, wf, kAIObject, true)
	if err != nil {
		return nil
	}
	var out []metamodel.Option
	seen := map[string]bool{}
	for _, r := range res.Rows {
		for _, e := range strings.Split(crud.Str(r[2]), ";") {
			o, q, ok := strings.Cut(strings.TrimSpace(e), "=")
			if !ok || seen[strings.TrimSpace(q)] {
				continue
			}
			seen[strings.TrimSpace(q)] = true
			name := strings.TrimSpace(q)
			if resp, err := m.services.Call(ctx, "AiRequest", "get", map[string]any{"id": name}); err == nil {
				var rq struct {
					Name string `json:"name"`
				}
				if sdk.Decode(resp.Payload, &rq) == nil && rq.Name != "" {
					name = rq.Name
				}
			}
			out = append(out, metamodel.Option{Value: crud.Str(r[0]) + "|" + strings.TrimSpace(o) + "|" + strings.TrimSpace(q), Label: name})
		}
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

// --- Vorbelegung aus dem Vertrag (Parameter prefill) -----------------------------------

// aiPrefillFrom: Parameter prefill „SupplierInvoice=SupplierInvoice.contractDraft“ – ist
// ein Vertrag am Dokument, liefert die Action den Entwurf {result: …} zum Vertrag.
func (m *Module) aiPrefillFrom(ctx context.Context, d *docRow, s *stepDef, object, mode string) (map[string]any, string) {
	contract := m.docContract(ctx, d)
	if contract == "" || mode == "" {
		return nil, ""
	}
	for _, p := range strings.Split(s.param(ctx, "prefill"), ";") {
		o, act, ok := strings.Cut(strings.TrimSpace(p), "=")
		if !ok || strings.TrimSpace(o) != object {
			continue
		}
		obj, action, ok := strings.Cut(strings.TrimSpace(act), ".")
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

const (
	prefillHead = "head" // nur der Kopf aus dem Vertrag, die KI liest die Positionen
	prefillFull = "full" // Kopf und Positionen aus dem Vertrag
)

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

// prefillMode: Vorbelegung aus dem Vertrag – keine (kein Vertrag, kein Parameter prefill),
// mit Positionen (Bedingung prefill_when erfüllt bzw. leer; nachträglich: Feld
// contract_items) oder nur der Kopf.
func (m *Module) prefillMode(ctx context.Context, d *docRow, s *stepDef) string {
	if strings.TrimSpace(s.param(ctx, "prefill")) == "" || m.docContract(ctx, d) == "" {
		return ""
	}
	w := strings.TrimSpace(s.param(ctx, "prefill_when"))
	if f, ok := ctx.Value(forcedKey{}).(forcedRequest); ok && f.Items != nil && w != "" {
		if *f.Items {
			return prefillFull
		}
		return prefillHead
	}
	if w == "" {
		return prefillFull
	}
	for _, c := range strings.Split(w, "&") {
		if ok, _ := m.whenApplies(ctx, d, strings.TrimSpace(c)); !ok {
			return prefillHead
		}
	}
	return prefillFull
}

// aiRunRequest: Anfrage an die KI – bei Positionen aus dem Vertrag die aus
// requests_prefill („SupplierInvoice=SUPPLIER_INVOICE_HEAD“), sonst die des Schritts.
func (m *Module) aiRunRequest(ctx context.Context, s *stepDef, object, request, mode string) string {
	if mode != prefillFull {
		return request
	}
	for _, p := range strings.Split(s.param(ctx, "requests_prefill"), ";") {
		if o, r, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.TrimSpace(o) == object && strings.TrimSpace(r) != "" {
			return strings.TrimSpace(r)
		}
	}
	return request
}

// aiWins: Felder, bei denen das Ergebnis der KI der Vorbelegung vorgeht (Parameter ai_wins).
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
