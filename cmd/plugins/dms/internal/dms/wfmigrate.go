package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Übernahme der Workflows in die Form 2 (0.12.0, wiederholbar: nur Workflows ohne
// Form). ERP und STANDARD entstehen neu aus der Vorlage; andere Workflows werden
// aus ihren Parametern übernommen, soweit es eine Entsprechung gibt (sonst bleibt
// der Schritt inaktiv mit Hinweis in der Beschreibung). Wartende Dokumente bleiben
// am gleichen Schritt (ERP: die frühere Bestätigung der Erkennung wird die
// Partnerauswahl) und bekommen ihren Ablauf; gibt es den Schritt nicht mehr,
// hält der Workflow an („Workflow neu starten“).

// Schrittarten bis 0.11.x.
const (
	lText             = "TEXT"
	lDate             = "DATE"
	lConfirm          = "CONFIRM"
	lSet              = "SET"
	lOCR              = "OCR"
	lExtract          = "EXTRACT"
	lFile             = "FILE"
	lTakeover         = "TAKEOVER"
	lIdentify         = "IDENTIFY"
	lName             = "NAME"
	lTemplateInvoice  = "TEMPLATE_INVOICE"
	lAIObject         = "AI_OBJECT"
	legacyInactiveTag = "Bei der Übernahme in Form 2 (0.12.0) deaktiviert: "
)

// legacyStepMap: Schritte, die beim Umbau eines mitgelieferten Workflows anders heißen.
var legacyStepMap = map[string]map[string]string{erpWorkflow: {"040": "060"}}

func (m *Module) migrateWorkflows(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT code, start_step FROM dms__workflow WHERE format IS NULL OR format < ?`, wfFormat)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		code, start := crud.Str(r[0]), crud.Str(r[1])
		err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
			if b, ok := builtinWorkflows()[code]; ok {
				if err := m.replaceSteps(ctx, code, b.steps); err != nil {
					return err
				}
			} else {
				steps, err := m.legacySteps(ctx, code)
				if err != nil {
					return err
				}
				if err := m.replaceSteps(ctx, code, steps); err != nil {
					return err
				}
				if _, err := m.db.Exec(ctx, `UPDATE dms__workflow SET start_step = ? WHERE code = ?`, nilIfEmpty(start), code); err != nil {
					return err
				}
			}
			return m.migrateWaitingDocs(ctx, code)
		})
		if err != nil {
			return fmt.Errorf("Workflow %s in Form 2: %w", code, err)
		}
		m.log.InfoContext(ctx, "Workflow in Form 2 übernommen", "workflow", code)
	}
	return nil
}

// migrateWaitingDocs: laufende Dokumente des Workflows – Schritt umhängen, Ablauf anlegen.
func (m *Module) migrateWaitingDocs(ctx context.Context, wf string) error {
	res, err := m.db.Query(ctx, `SELECT id, wf_step, wf_status, wf_trail FROM dms__document WHERE wf_code = ? AND status = ? AND wf_status IN (?, ?)`,
		wf, stInbox, wfWaiting, wfRunning)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		id, step := crud.Str(r[0]), crud.Str(r[1])
		if n, ok := legacyStepMap[wf][step]; ok {
			step = n
		}
		if err := m.initOverview(ctx, id, wf); err != nil {
			return err
		}
		var trail []trailEntry
		_ = json.Unmarshal([]byte(crud.Str(r[3])), &trail)
		for i := range trail {
			if n, ok := legacyStepMap[wf][trail[i].Step]; ok {
				trail[i].Step = n
			}
			if trail[i].Step != "" {
				m.markStep(ctx, id, trail[i].Step, stepDone, "", "", "vor 0.12.0")
			}
		}
		if err := m.saveTrail(ctx, id, trail); err != nil {
			return err
		}
		s, err := m.loadStep(ctx, wf, step)
		switch {
		case err == nil && userKinds[s.Kind]:
			m.markStep(ctx, id, step, stepWaiting, "", "", "")
			if err := m.setWorkflow(ctx, id, step, wfWaiting); err != nil {
				return err
			}
			m.appendLog(ctx, id, "Workflow in Form 2 übernommen – wartet bei Schritt "+step)
		default:
			if err := m.setWorkflow(ctx, id, step, wfFailed); err != nil {
				return err
			}
			m.appendLog(ctx, id, "Workflow in Form 2 übernommen – Schritt "+step+" passt nicht mehr, bitte „Workflow neu starten“")
		}
	}
	return nil
}

// legacyParams: Parameter eines Schritts bis 0.11.x (Sprache → Wert, leer = alle).
type legacyParams map[string]map[string]string

func (p legacyParams) get(k string) string {
	if v := p[k][""]; v != "" {
		return v
	}
	return p[k]["de"]
}

var legacyHintCodeRe = regexp.MustCompile(`[^a-z0-9_]+`)

// legacySteps: Schritte eines Workflows aus der Form bis 0.11.x.
func (m *Module) legacySteps(ctx context.Context, wf string) ([]wfStep, error) {
	res, err := m.db.Query(ctx, `SELECT step, name, kind, target, is_active FROM dms__step WHERE workflow = ? ORDER BY step`, wf)
	if err != nil {
		return nil, err
	}
	params := map[string]legacyParams{}
	if m.tableExists(ctx, "dms__step_param") {
		pr, err := m.db.Query(ctx, `SELECT step, param, locale, value FROM dms__step_param WHERE workflow = ? AND is_active = ?`, wf, true)
		if err != nil {
			return nil, err
		}
		for _, r := range pr.Rows {
			st, k := crud.Str(r[0]), crud.Str(r[1])
			if params[st] == nil {
				params[st] = legacyParams{}
			}
			if params[st][k] == nil {
				params[st][k] = map[string]string{}
			}
			params[st][k][crud.Str(r[2])] = crud.Str(r[3])
		}
	}
	or, err := m.db.Query(ctx, `SELECT step, outcome, value, next_step FROM dms__step_option WHERE workflow = ? AND is_active = ?`, wf, true)
	if err != nil {
		return nil, err
	}
	opts := map[string][]wfOpt{}
	for _, r := range or.Rows {
		if crud.Str(r[2]) != "" {
			continue // Wert gleich: nicht übernommen
		}
		opts[crud.Str(r[0])] = append(opts[crud.Str(r[0])], wfOpt{outcome: crud.Str(r[1]), next: crud.Str(r[3])})
	}
	var out []wfStep
	for _, r := range res.Rows {
		st, name, kind, target := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3])
		s := m.legacyStep(ctx, wf, st, name, kind, target, params[st])
		for _, o := range opts[st] {
			if s.kind == kForm && o.outcome == oSelected {
				o.outcome = oCreated
			}
			s.opts = append(s.opts, o)
		}
		if p := params[st]; p.get("optional") == "true" && s.kind == kSelect {
			for _, o := range s.opts {
				if o.outcome == oSelected {
					s.opts = append(s.opts, wfOpt{oSkipped, o.next, "Überspringen"})
					break
				}
			}
		}
		if !crud.AsBool(r[4]) {
			s.set["is_active"] = false
		}
		out = append(out, s)
	}
	return out, nil
}

func (m *Module) legacyStep(ctx context.Context, wf, st, name, kind, target string, p legacyParams) wfStep {
	s := wfStep{step: st, name: name, kind: kind, set: map[string]any{}}
	for loc, v := range p["question"] {
		switch loc {
		case "", "de":
			s.set["question"] = v
		default:
			s.texts = append(s.texts, [3]string{"question", loc, v})
		}
	}
	setTarget := func() {
		if target == "" {
			return
		}
		s.set["target"] = target
		switch target {
		case "ref":
			if o := p.get("ref_object"); o != "" {
				s.set["target_object"] = o
			}
		case "var":
			s.set["var_name"] = p.get("var")
		}
	}
	inactive := func(why string) {
		s.kind = kCheck
		s.set = map[string]any{"is_active": false, "description": legacyInactiveTag + why}
	}
	user := func() {
		s.set["if_set"] = ifSetSkip
		if o := p.get("open_ref"); o != "" {
			s.set["open_object"], s.set["open_label"] = o, p.get("open_label")
		}
	}
	source := func() {
		switch {
		case p.get("tree") == "true":
			s.set["source_kind"] = srcTree
		case p.get("objects") == "category" || p.get("objects") == "doctype":
			s.set["source_kind"] = srcCategory
		case p.get("options") != "":
			s.set["source_kind"] = srcList
			for _, e := range strings.Split(p.get("options"), ";") {
				if k, v, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
					s.maps = append(s.maps, wfMap{role: mapValue, field: strings.TrimSpace(k), value: strings.TrimSpace(v)})
				}
			}
		case p.get("source") != "":
			src := strings.TrimSpace(strings.Split(p.get("source"), ";")[0])
			obj, act, _ := strings.Cut(src, ".")
			s.set["source_kind"], s.set["source_object"], s.set["source_action"] = srcObject, obj, act
			s.set["value_field"], s.set["label_fields"] = p.get("value_field"), p.get("label_fields")
		}
		for k, v := range p {
			if f, ok := strings.CutPrefix(k, "filter."); ok {
				s.maps = append(s.maps, wfMap{role: mapFilter, field: f, value: v[""]})
			}
		}
		for _, e := range strings.Split(p.get("category_filter"), ";") {
			cat, fs, ok := strings.Cut(strings.TrimSpace(e), ":")
			if !ok {
				continue
			}
			desc := ""
			if i := strings.Index(fs, "("); i >= 0 {
				desc, fs = strings.Trim(strings.TrimSpace(fs[i:]), "()"), fs[:i]
			}
			for _, f := range strings.Split(fs, ",") {
				if k, v, ok := strings.Cut(strings.TrimSpace(f), "="); ok {
					s.maps = append(s.maps, wfMap{role: mapFilter, field: strings.TrimSpace(k), value: strings.TrimSpace(v), cat: strings.TrimSpace(cat), desc: desc})
				}
			}
		}
		if p.get("auto_single") == "true" {
			s.set["auto_single"] = true
		}
	}
	switch kind {
	case kSelect:
		setTarget()
		user()
		source()
	case lText, lDate:
		s.kind = kInput
		setTarget()
		user()
		s.set["input_type"] = map[string]string{lText: inText, lDate: inDate}[kind]
		if f := p.get("date_format"); f != "" {
			s.set["date_format"] = f
		}
	case lConfirm:
		s.kind = kInput
		user()
		s.set["input_type"] = inYesNo
	case lName:
		s.kind = kInput
		s.set["target"], s.set["input_type"], s.set["default_value"] = "name_override", inText, "{name_proposal}"
	case kAI:
		if p.get("ai_request") == "" {
			inactive("KI-Schritt ohne KI-Anfrage des Moduls ai (direkter Aufruf entfällt)")
			break
		}
		setTarget()
		source()
		delete(s.set, "if_set")
		s.set["ai_request"] = p.get("ai_request")
		s.set["context"] = strings.TrimSpace(strings.NewReplacer("{text}", "", "{options}", "").Replace(p.get("prompt")))
	case lOCR:
		s.kind = kAction
		s.set["action_object"], s.set["action_name"], s.set["changes_data"] = docObject, "recognize", false
	case lFile:
		s.kind = kAction
		s.set["action_object"], s.set["action_name"], s.set["changes_data"] = docObject, "file", true
	case lTakeover:
		s.kind = kAction
		s.set["action_object"], s.set["action_name"], s.set["changes_data"] = docObject, "takeover", false
	case lIdentify:
		s.kind = kRecognize
		s.set["rules"], s.set["apply"] = recognizeRules, applyPropose
		if v := toInt(p.get("min_score")); v > 0 {
			s.set["min_score"] = v
		}
		if v := p.get("min_gap"); v != "" {
			s.set["min_gap"] = toInt(v)
		}
	case lExtract:
		code := legacyHintCodeRe.ReplaceAllString(strings.ToLower("wf_"+wf+"_"+st), "_")
		if len(code) > 30 {
			code = code[:30]
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__hint (code, name, kind, pattern, target, points, sort_order, is_active)
			SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM dms__hint WHERE code = ?)`,
			code, "Workflow "+wf+", Schritt "+st+": "+name, hintPattern, p.get("pattern"), target, 5, 900, true, code); err != nil {
			inactive("Muster nicht übernommen: " + err.Error())
			break
		}
		s.kind = kRecognize
		s.set["rules"], s.set["apply"], s.set["min_score"] = code, applySet, 1
	case lAIObject:
		s.kind = kForm
		if p.get("bind") == "partner" {
			s.set["target"] = "partner_id"
		} else {
			s.set["target"] = "ref"
		}
		for _, e := range strings.Split(p.get("requests"), ";") {
			if o, r, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
				s.maps = append(s.maps, wfMap{role: mapRequest, field: strings.TrimSpace(o), value: strings.TrimSpace(r)})
			}
		}
		for _, e := range strings.Split(p.get("requests_prefill"), ";") {
			if o, r, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
				s.maps = append(s.maps, wfMap{role: mapRequestPrefill, field: strings.TrimSpace(o), value: strings.TrimSpace(r)})
			}
		}
		for _, e := range strings.Split(p.get("prefill"), ";") {
			if o, r, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
				s.maps = append(s.maps, wfMap{role: mapPrefill, field: strings.TrimSpace(o), value: strings.TrimSpace(r)})
			}
		}
		for _, spec := range []struct{ param, role string }{{"defaults", mapDefault}, {"hints", mapHint}} {
			for _, e := range strings.Split(p.get(spec.param), ";") {
				if k, v, ok := strings.Cut(strings.TrimSpace(e), "="); ok {
					s.maps = append(s.maps, wfMap{role: spec.role, field: strings.TrimSpace(k), value: strings.TrimSpace(v)})
				}
			}
		}
		if w := p.get("prefill_when"); w != "" {
			s.conds = append(s.conds, legacyWhen(w, condPrefill)...)
		}
		s.set["context"] = p.get("context")
		s.set["confirm"] = p.get("confirm") == "true"
		s.set["file_input"] = p.get("file_input") == "ask"
		s.set["file_types"], s.set["ai_wins"] = p.get("file_types"), p.get("ai_wins")
	case lTemplateInvoice:
		inactive("Rechnung aus Vorlage ist seit 0.12.0 Auswahl, Eingabe und Aktion (siehe Workflow ERP, Schritte 080–084)")
	case lSet:
		inactive("Wert setzen gibt es seit 0.12.0 nicht mehr – Eingabe mit Vorgabe verwenden")
	default:
		inactive("Art " + kind + " unbekannt")
	}
	if w := p.get("when"); w != "" && s.set["is_active"] != false {
		s.conds = append(s.conds, legacyWhen(w, condApplies)...)
	}
	for k, v := range s.set {
		if v == "" {
			delete(s.set, k)
		}
	}
	return s
}

// legacyWhen: Bedingung when (mehrere mit &) als Bedingungen.
func legacyWhen(w, role string) []wfCond {
	var out []wfCond
	for _, c := range strings.Split(w, "&") {
		c = strings.TrimSpace(c)
		switch {
		case strings.HasPrefix(c, "ref_kind:"):
			out = append(out, wfCond{role, "{doctype.ref_kind}", opIn, strings.TrimPrefix(c, "ref_kind:"), "entfällt für Dokumentart {doc.doc_type}"})
		case strings.HasPrefix(c, "object:"):
			out = append(out, wfCond{role, "{category.objects}", opHas, strings.TrimPrefix(c, "object:"), "entfällt für Dokumentkategorie {doc.doc_category}"})
		case strings.HasPrefix(c, "no_ref:"):
			o := strings.TrimPrefix(c, "no_ref:")
			out = append(out, wfCond{role, "{ref." + o + "}", opEmpty, "", o + " schon zugeordnet"})
		case strings.HasPrefix(c, "ref:"):
			o := strings.TrimPrefix(c, "ref:")
			out = append(out, wfCond{role, "{ref." + o + "}", opNotEmpty, "", "kein " + o + " zugeordnet"})
		case strings.HasPrefix(c, "var:"):
			n, v, _ := strings.Cut(strings.TrimPrefix(c, "var:"), "=")
			out = append(out, wfCond{role, "{var." + strings.TrimSpace(n) + "}", opEq, strings.TrimSpace(v), ""})
		case c == "category_partner" || c == "doctype_partner":
			out = append(out, wfCond{role, "{category.partner}", opEq, "true", "entfällt für Dokumentkategorie {doc.doc_category}"})
		case c == "has_invoice":
			out = append(out, wfCond{role, "{ref.SupplierInvoice}", opNotEmpty, "", "keine Eingangsrechnung zugeordnet"})
		case c == "contract_templates":
			out = append(out, wfCond{role, "{ref.Contract}", opNotEmpty, "", "kein Vertrag am Dokument"})
		case c == "identified":
			out = append(out, wfCond{role, "{doc.partner_id}{refs}", opNe, "–", "nichts erkannt"})
		}
	}
	return out
}
