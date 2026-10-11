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
// Auswahlschritte bieten unten rechts „Anlegen“, wenn sie eine Option mit dem
// Ergebnis CREATE haben (z. B. weiter zu einem AI_OBJECT-Schritt).
//
// Variablen (Ziel var, Parameter var): Auswahl eines Schritts für spätere Schritte,
// z. B. die Vertragsart – Platzhalter {var.<Name>} und {var.<Name>_label}.

const (
	kAIObject = "AI_OBJECT"
	oCreate   = "CREATE"
	btnCreate = "createNew"

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
	Object  string         `json:"object"`
	Request string         `json:"request"`
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

// aiPrefill fragt die KI (einmal je Schritt und Dokument) und merkt sich das Ergebnis.
func (m *Module) aiPrefill(ctx context.Context, d *docRow, s *stepDef) {
	object, request, err := m.aiRequestFor(ctx, d, s)
	st := aiState{Object: object, Request: request}
	if old := m.aiStateOf(ctx, d, s); old != nil && old.Request == request && old.Status != "" {
		return
	}
	if err != nil {
		st.Status, st.Message = "ERROR", err.Error()
	} else {
		in := map[string]any{"request": request, "company_code": d.CC, "context": m.expand(ctx, d, strings.ReplaceAll(s.param(ctx, "context"), "|", "\n"))}
		if text := m.docText(ctx, d.ID); strings.TrimSpace(text) != "" {
			in["text"] = text
		} else if strings.HasPrefix(d.ContentType, "image/") {
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
	if err := m.setWfVar(ctx, d.ID, "ai."+s.Step, st); err != nil {
		m.log.WarnContext(ctx, "KI-Ergebnis nicht gespeichert", "doc", d.ID, "err", err.Error())
	}
	m.appendLog(ctx, d.ID, fmt.Sprintf("%s %s: KI %s – %s", s.Step, s.Name, firstOf(st.LogID, request), st.Message))
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
	resp, err := m.services.Call(ctx, "Ai", "form", map[string]any{"request": ai.Request, "result": ai.Result,
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
	for k, v := range f.Values {
		v := v
		st.Fields[k] = metamodel.FieldState{Value: &v}
	}
	switch ai.Status {
	case "DONE":
		st.Message += " – von der KI vorbelegt (Protokoll " + ai.LogID + "); bitte prüfen und ergänzen, „Weiter“ legt es an."
	default:
		st.Message += " – keine Vorbelegung (" + ai.Message + "); bitte ausfüllen, „Weiter“ legt es an."
	}
}

// aiCreate: Werte der Maske → Objekt anlegen → am Dokument setzen; liefert die ID.
func (m *Module) aiCreate(ctx context.Context, d *docRow, s *stepDef, data map[string]any) (string, error) {
	ai := m.aiStateOf(ctx, d, s)
	if ai == nil || ai.Request == "" {
		object, request, err := m.aiRequestFor(ctx, d, s)
		if err != nil {
			return "", err
		}
		ai = &aiState{Object: object, Request: request}
	}
	values := map[string]any{}
	for k, v := range data {
		if strings.HasPrefix(k, "f__") {
			values[k] = v
		}
	}
	resp, err := m.services.Call(ctx, "Ai", "collect", map[string]any{"request": ai.Request, "values": values, "company_code": d.CC})
	if err != nil {
		return "", err
	}
	var col struct {
		Result map[string]any `json:"result"`
	}
	if err := sdk.Decode(resp.Payload, &col); err != nil {
		return "", err
	}
	resp, err = m.services.Call(ctx, "Ai", "create", map[string]any{"request": ai.Request, "result": col.Result,
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
	return cr.ID, nil
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
