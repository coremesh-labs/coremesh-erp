package dms

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablauf eines Workflows am Dokument: Der aktuelle Schritt steht in wf_step.
// Programmschritte laufen sofort; ein Benutzerschritt wartet (WAITING), bis
// jemand „Weiter …“ wählt – außer sein Feld ist schon belegt (z. B. durch das
// Eingangsverzeichnis): dann gilt er als übersprungen. Das Ergebnis bestimmt
// über die Optionen den nächsten Schritt; ohne passende Option endet der
// Workflow. Jeder Schritt steht mit Ergebnis im Verlauf (wf_log).

// Status des Workflows am Dokument.
const (
	wfRunning = "RUNNING"
	wfWaiting = "WAITING"
	wfDone    = "DONE"
	wfFailed  = "FAILED"
)

// maxSteps begrenzt einen Lauf (Schutz vor Kreisen ohne Benutzerschritt).
const maxSteps = 100

type stepOpt struct{ Outcome, Value, Next string }

// stepDef ist ein Schritt mit Parametern und Optionen.
type stepDef struct {
	Workflow, Step, Name, Kind, Target string
	Params                             map[string]map[string]string // param → Sprache → Wert
	Options                            []stepOpt
}

func (m *Module) loadStep(ctx context.Context, wf, step string) (*stepDef, error) {
	res, err := m.db.Query(ctx, `SELECT name, kind, target, is_active FROM dms__step WHERE workflow = ? AND step = ?`, wf, step)
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
	s := &stepDef{Workflow: wf, Step: step, Name: crud.Str(r[0]), Kind: crud.Str(r[1]), Target: crud.Str(r[2]), Params: map[string]map[string]string{}}
	ps, err := m.db.Query(ctx, `SELECT param, locale, value FROM dms__step_param WHERE workflow = ? AND step = ? AND is_active = ?`, wf, step, true)
	if err != nil {
		return nil, err
	}
	for _, p := range ps.Rows {
		k := crud.Str(p[0])
		if s.Params[k] == nil {
			s.Params[k] = map[string]string{}
		}
		s.Params[k][crud.Str(p[1])] = crud.Str(p[2])
	}
	os, err := m.db.Query(ctx, `SELECT outcome, value, next_step FROM dms__step_option WHERE workflow = ? AND step = ? AND is_active = ?`, wf, step, true)
	if err != nil {
		return nil, err
	}
	for _, o := range os.Rows {
		s.Options = append(s.Options, stepOpt{Outcome: crud.Str(o[0]), Value: crud.Str(o[1]), Next: crud.Str(o[2])})
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

// param: Wert in der Sprache der Anfrage, sonst ohne Sprache, sonst Deutsch.
func (s *stepDef) param(ctx context.Context, key string) string {
	v := s.Params[key]
	for _, l := range []string{localeOf(ctx), "", "de", "en"} {
		if t, ok := v[l]; ok {
			return t
		}
	}
	return ""
}

// stepTitle: „Schritt 055 · Dokumentart wählen: Frage“ – bei der Dokumentart mit der Kategorie.
func (m *Module) stepTitle(ctx context.Context, d *docRow, s *stepDef) string {
	t := "Schritt " + s.Step + " · " + s.Name + ": " + s.question(ctx)
	if s.Target == "doc_type" {
		if c := m.categoryOf(ctx, d); c != "" {
			res, err := m.db.Query(ctx, `SELECT name FROM dms__doc_category WHERE code = ?`, c)
			if err == nil && len(res.Rows) > 0 {
				t += " (Kategorie " + crud.Str(res.Rows[0][0]) + ")"
			}
		}
	}
	return t
}

func (s *stepDef) question(ctx context.Context) string {
	return firstOf(s.param(ctx, "question"), s.Name)
}

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
		try = append(try, positive(s.Kind))
	}
	for _, t := range try {
		for _, o := range s.Options {
			if o.Outcome == t && o.Value == "" {
				return o.Next, true
			}
		}
	}
	return "", false
}

// positive: Ergebnis eines erfolgreichen Schritts der Art.
func positive(kind string) string {
	switch kind {
	case kSelect:
		return oSelected
	case kText, kDate, kName:
		return oEntered
	case kConfirm:
		return oYes
	}
	return oSuccess
}

// startWorkflow setzt den Workflow auf den ersten Schritt und lässt ihn laufen.
func (m *Module) startWorkflow(ctx context.Context, docID, wf string) error {
	start, err := m.startStep(ctx, wf)
	if err != nil {
		return err
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET wf_code = ?, wf_step = ?, wf_status = ? WHERE id = ?`, wf, start, wfRunning, docID); err != nil {
		return err
	}
	m.appendLog(ctx, docID, "Workflow "+wf+" gestartet")
	m.pushTrail(ctx, docID, "", false)
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
		if d.WfCode == "" || d.WfStep == "" || d.WfStatus == wfDone || d.WfStatus == wfFailed {
			return nil
		}
		s, err := m.loadStep(ctx, d.WfCode, d.WfStep)
		if err != nil {
			m.appendLog(ctx, docID, err.Error())
			return m.setWorkflow(ctx, docID, d.WfStep, wfFailed)
		}
		if userKinds[s.Kind] {
			if ok, why := m.stepApplies(ctx, d, s); !ok {
				if err := m.advanceTrail(ctx, d, s, oSkipped, "", why, false); err != nil {
					return err
				}
				continue
			}
			set, err := m.targetSet(ctx, d, s)
			if err != nil {
				return err
			}
			if !set && s.Kind == kSelect && s.param(ctx, "auto_single") == "true" {
				// Nur eine Möglichkeit: ohne Frage setzen.
				if cs, err := m.choices(ctx, d, s, ""); err == nil {
					var only []choice
					for _, c := range cs {
						if c.Value != "" {
							only = append(only, c)
						}
					}
					if len(only) == 1 {
						v, err := m.setTarget(ctx, d, s, only[0].Value)
						if err == nil {
							if err := m.advance(ctx, d, s, oSelected, v, "einzige Möglichkeit"); err != nil {
								return err
							}
							continue
						}
					}
				}
			}
			if !set {
				return m.setWorkflow(ctx, docID, s.Step, wfWaiting)
			}
			if err := m.advance(ctx, d, s, oSkipped, "", "Feld schon belegt"); err != nil {
				return err
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

// advance schreibt das Ergebnis in den Verlauf und geht zum nächsten Schritt.
func (m *Module) advance(ctx context.Context, d *docRow, s *stepDef, outcome, value, info string) error {
	return m.advanceTrail(ctx, d, s, outcome, value, info, userKinds[s.Kind])
}

// advanceTrail: wie advance; revisit = „zurück“ darf zu diesem Schritt (Fragen,
// auch übersprungene – nicht die, die für die Dokumentart entfallen).
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

// targetSet: Feld des Schritts schon belegt (Bezug: einer zum Object ref_object).
func (m *Module) targetSet(ctx context.Context, d *docRow, s *stepDef) (bool, error) {
	switch s.Target {
	case "":
		return false, nil
	case "ref":
		for _, o := range m.refObjects(ctx, d, s) {
			res, err := m.db.Query(ctx, `SELECT COUNT(*) FROM dms__document_ref WHERE document_id = ? AND object = ? AND is_active = ?`,
				d.ID, o, true)
			if err != nil {
				return false, err
			}
			if toInt(res.Rows[0][0]) > 0 {
				return true, nil
			}
		}
		return false, nil
	}
	return docField(d, s.Target) != "", nil
}

func docField(d *docRow, field string) string {
	switch field {
	case "file_account":
		return d.Account
	case "doc_type":
		return d.DocType
	case "doc_category":
		return d.Category
	case "partner_id":
		return d.Partner
	case "doc_date":
		return d.DocDate
	case "title":
		return d.Title
	case "retention":
		return d.Retention
	}
	return ""
}

// setTarget prüft den Wert und setzt das Feld bzw. legt den Bezug an.
func (m *Module) setTarget(ctx context.Context, d *docRow, s *stepDef, value string) (string, error) {
	value = strings.TrimSpace(value)
	switch s.Target {
	case "file_account", "doc_type", "doc_category", "partner_id", "retention", "ref":
		object := map[string]string{"file_account": accountObject, "doc_type": docTypeObject, "doc_category": categoryObject, "partner_id": "BusinessPartner",
			"retention": retentionObject, "ref": s.param(ctx, "ref_object")}[s.Target]
		if s.Target == "ref" && object == "" {
			// Auswahl aus mehreren Quellen: „Object:Schlüssel“
			o, k, ok := strings.Cut(value, ":")
			if !ok || !contains(m.refObjects(ctx, d, s), o) {
				return "", crud.Invalid("Bezug %q: Object:Schlüssel erwartet (%s)", value, strings.Join(m.refObjects(ctx, d, s), ", "))
			}
			object, value = o, k
		}
		v, err := m.checkParamValue(ctx, d.CC, object, value)
		if err != nil {
			return "", err
		}
		if s.Target == "ref" {
			return v, m.insertRef(ctx, d, object, v)
		}
		value = v
	case "doc_date":
		v, err := parseDate(value, s.param(ctx, "date_format"))
		if err != nil {
			return "", err
		}
		value = v
	case "title", "note":
	default:
		return "", crud.Invalid("Schritt %s: Feld %q gibt es nicht", s.Step, s.Target)
	}
	if value == "" {
		return "", crud.Invalid("%s: Wert fehlt", optionLabel(targetOptions, s.Target))
	}
	if s.Target == "doc_type" {
		rec := crud.Record{"doc_type": value, "doc_category": d.Category}
		if err := m.checkCategory(ctx, rec); err != nil {
			return "", err
		}
		if d.Category == "" && crud.Str(rec["doc_category"]) != "" {
			if _, err := m.db.Exec(ctx, `UPDATE dms__document SET doc_category = ? WHERE id = ?`, rec["doc_category"], d.ID); err != nil {
				return "", err
			}
		}
	}
	_, err := m.db.Exec(ctx, `UPDATE dms__document SET `+s.Target+` = ? WHERE id = ?`, value, d.ID)
	return value, err
}

func (m *Module) insertRef(ctx context.Context, d *docRow, object, key string) error {
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1, SUM(CASE WHEN object = ? AND object_key = ? AND is_active = ? THEN 1 ELSE 0 END)
		FROM dms__document_ref WHERE document_id = ?`, object, key, true, d.ID)
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][1]) > 0 {
		return nil
	}
	_, err = m.db.Exec(ctx, `INSERT INTO dms__document_ref (document_id, line_no, company_code, object, object_key, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, toInt(res.Rows[0][0]), d.CC, object, key, true)
	return err
}

// --- Benutzerschritte: „Weiter …“ ---------------------------------------------------------

// choice ist ein Auswahlwert.
type choice struct{ Value, Label string }

// choices: Auswahlwerte eines Schritts – aus der Liste options ("A=Text; B=Text")
// oder aus Object.Action (source) mit Filtern filter.<Feld> (Vorlagen).
func (m *Module) choices(ctx context.Context, d *docRow, s *stepDef, search string) ([]choice, error) {
	var out []choice
	if s.Kind == kConfirm {
		return []choice{{oYes, "Ja"}, {oNo, "Nein"}}, nil
	}
	if s.byDocType(ctx) {
		return m.objectChoices(ctx, d, search)
	}
	if s.param(ctx, "optional") == "true" {
		out = append(out, choice{"", "– überspringen –"})
	}
	if opts := s.param(ctx, "options"); opts != "" {
		for _, p := range strings.Split(opts, ";") {
			v, l, _ := strings.Cut(strings.TrimSpace(p), "=")
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, choice{v, firstOf(strings.TrimSpace(l), v)})
			}
		}
		return out, nil
	}
	src := s.param(ctx, "source")
	if src == "" {
		return out, nil
	}
	query := map[string]any{}
	for k := range s.Params {
		if f, ok := strings.CutPrefix(k, "filter."); ok {
			query[f] = m.expand(ctx, d, s.param(ctx, k))
		}
	}
	if q := strings.TrimSpace(search); q != "" {
		query["q"] = q
	}
	srcs := sources(src)
	vf := firstOf(s.param(ctx, "value_field"), "id")
	lfs := splitList(firstOf(s.param(ctx, "label_fields"), "name"))
	for _, sc := range srcs {
		resp, err := m.services.Call(ctx, sc.Object, sc.Action, map[string]any{"query": query})
		if err != nil {
			return nil, crud.Invalid("Auswahl aus %s.%s: %v", sc.Object, sc.Action, err)
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := sdk.Decode(resp.Payload, &list); err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			v := crud.Str(it[vf])
			if v == "" {
				continue
			}
			lbl, _ := it["_labels"].(map[string]any)
			var parts []string
			for _, f := range lfs {
				t := crud.Str(lbl[f])
				if t == "" {
					t = crud.Str(it[f])
				}
				if t != "" && t != v {
					parts = append(parts, t)
				}
			}
			label := v
			if len(parts) > 0 {
				label = v + " – " + strings.Join(parts, " ")
			}
			// Mehrere Quellen: Wert mit Object („Building:2000|ROBad1“), Text mit Bezeichnung der Quelle.
			if len(srcs) > 1 {
				v = sc.Object + ":" + v
				label = firstOf(sc.Label, sc.Object) + ": " + label
			}
			out = append(out, choice{v, label})
		}
	}
	if len(out) > 500 {
		out = out[:500]
	}
	return out, nil
}

// --- Ablage Ebene für Ebene ---------------------------------------------------------------

// tree: Auswahl der Ablage Ebene für Ebene (Parameter tree = true, Ziel file_account).
func (s *stepDef) tree(ctx context.Context) bool {
	return s.Target == "file_account" && s.param(ctx, "tree") == "true"
}

// treeUp: Präfix des Werts „zurück zur Ebene …“.
const treeUp = "^"

// companyLevel ist eine aktive, nicht gesperrte Ablage des Buchungskreises.
type companyLevel struct {
	Number, Name, Parent string
	Leaf                 bool
}

func (m *Module) companyLevels(ctx context.Context, cc string) (map[string]*companyLevel, []*companyLevel, error) {
	res, err := m.db.Query(ctx, `SELECT a.number, a.name, a.parent FROM dms__account a
		JOIN dms__company c ON c.plan = a.plan AND c.company_code = ?
		JOIN dms__account_company x ON x.company_code = c.company_code AND x.number = a.number AND x.is_blocked = ?
		WHERE a.is_active = ? ORDER BY a.sort_key`, cc, false, true)
	if err != nil {
		return nil, nil, err
	}
	by := map[string]*companyLevel{}
	var all []*companyLevel
	for _, r := range res.Rows {
		l := &companyLevel{Number: crud.Str(r[0]), Name: crud.Str(r[1]), Parent: crud.Str(r[2]), Leaf: true}
		by[l.Number] = l
		all = append(all, l)
	}
	for _, l := range all {
		if p := by[l.Parent]; p != nil {
			p.Leaf = false
		}
	}
	return by, all, nil
}

// treeChoices: Auswahl auf der Ebene des gewählten Werts – Gruppe gewählt:
// ihre Unterablagen; Ablage der untersten Ebene: ihre Geschwister (sie
// bleibt gewählt); „^Nummer“: zurück auf diese Ebene. Mit Suchtext: alle
// Ablagen der untersten Ebene, deren Nummer oder Name passt. Liefert die
// Auswahl und den Pfad der Ebene für den Hinweis.
func (m *Module) treeChoices(ctx context.Context, d *docRow, value, search string) ([]choice, string, error) {
	by, all, err := m.companyLevels(ctx, d.CC)
	if err != nil {
		return nil, "", err
	}
	if q := strings.ToLower(strings.TrimSpace(search)); q != "" {
		out := []choice{{"", "– Ablage wählen –"}}
		for _, l := range all {
			if l.Leaf && (strings.HasPrefix(l.Number, q) || strings.Contains(strings.ToLower(l.Name), q)) {
				out = append(out, choice{l.Number, l.Number + " " + l.Name})
			}
		}
		return out, fmt.Sprintf("Suche „%s“: %d Ablagen", search, len(out)-1), nil
	}
	level := ""
	switch v := strings.TrimSpace(value); {
	case strings.HasPrefix(v, treeUp):
		level = strings.TrimPrefix(v, treeUp)
	case by[v] != nil && !by[v].Leaf:
		level = v
	case by[v] != nil:
		level = by[v].Parent
	}
	if level != "" && by[level] == nil {
		level = ""
	}
	var out []choice
	if level == "" {
		out = append(out, choice{"", "– Ablage wählen –"})
	} else {
		up := by[level].Parent
		upLabel := "↑ zurück zur obersten Ebene"
		if p := by[up]; p != nil {
			upLabel = "↑ zurück zu " + p.Number + " " + p.Name
		}
		out = append(out, choice{treeUp + level, "▾ " + level + " " + by[level].Name + " – bitte wählen"}, choice{treeUp + up, upLabel})
	}
	for _, l := range all {
		if l.Parent != level && !(level == "" && by[l.Parent] == nil) {
			continue // nicht auf dieser Ebene (oberste: auch Ablagen ohne aktive übergeordnete)
		}
		if l.Leaf {
			out = append(out, choice{l.Number, l.Number + " " + l.Name})
		} else {
			out = append(out, choice{l.Number, "▸ " + l.Number + " " + l.Name})
		}
	}
	var path []string
	for p := level; p != "" && by[p] != nil; p = by[p].Parent {
		path = append([]string{by[p].Number + " " + by[p].Name}, path...)
	}
	if len(path) == 0 {
		return out, "oberste Ebene", nil
	}
	return out, "Ebene: " + strings.Join(path, " › "), nil
}

// createLinks: Parameter create („Contract=Vertrag anlegen; SupplierInvoice=Rechnung erfassen“)
// → Links auf das Formular „Neu“ (neues Fenster).
func createLinks(ctx context.Context, s *stepDef) []metamodel.FormLink {
	var out []metamodel.FormLink
	for _, p := range strings.Split(s.param(ctx, "create"), ";") {
		object, label, _ := strings.Cut(strings.TrimSpace(p), "=")
		if object = strings.TrimSpace(object); object != "" {
			out = append(out, metamodel.FormLink{Label: firstOf(strings.TrimSpace(label), object+" anlegen"), URL: "/new/" + object})
		}
	}
	return out
}

// source ist eine Quelle der Auswahl.
type source struct{ Object, Action, Label string }

// sources: „Building.list=Gebäude; RentObject.list=Mietobjekt; Contract“ –
// Object.Action (Standard list) mit Bezeichnung, durch ; getrennt.
func sources(spec string) []source {
	var out []source
	for _, p := range strings.Split(spec, ";") {
		def, label, _ := strings.Cut(strings.TrimSpace(p), "=")
		object, action, _ := strings.Cut(strings.TrimSpace(def), ".")
		if object == "" {
			continue
		}
		out = append(out, source{Object: object, Action: firstOf(action, "list"), Label: strings.TrimSpace(label)})
	}
	return out
}

// refObjectsOf: Objects eines Bezug-Schritts – ref_object oder die Quellen.
func refObjectsOf(ctx context.Context, s *stepDef) []string {
	if o := s.param(ctx, "ref_object"); o != "" {
		return []string{o}
	}
	var out []string
	for _, sc := range sources(s.param(ctx, "source")) {
		out = append(out, sc.Object)
	}
	return out
}

var tmplRe = regexp.MustCompile(`\{([a-z_]+)(?:\.([A-Za-z_]+))?\}`)

// expand ersetzt Platzhalter: {company_code}, {doc.<Feld>}, {ref.<Object>}, {text}, {file_name}.
func (m *Module) expand(ctx context.Context, d *docRow, t string) string {
	return tmplRe.ReplaceAllStringFunc(t, func(ph string) string {
		g := tmplRe.FindStringSubmatch(ph)
		switch g[1] {
		case "company_code":
			return d.CC
		case "file_name":
			return d.FileName
		case "doc":
			return docField(d, g[2])
		case "ref":
			refs, _ := m.activeRefs(ctx, d.ID)
			for _, r := range refs {
				if r.Object == g[2] {
					return r.Key
				}
			}
			return ""
		case "text":
			return m.docText(ctx, d.ID)
		}
		return ph
	})
}

func (m *Module) docText(ctx context.Context, id string) string {
	res, err := m.db.Query(ctx, `SELECT text FROM dms__document_text WHERE document_id = ?`, id)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

// waitingStep: Dokument mit wartendem Benutzerschritt.
func (m *Module) waitingStep(ctx context.Context, d *docRow) (*stepDef, error) {
	if d.WfStatus != wfWaiting {
		return nil, crud.Invalid("Am Dokument wartet kein Workflow-Schritt (Status %s)", firstOf(optionLabel(wfStatusOptions, d.WfStatus), "ohne Workflow"))
	}
	return m.loadStep(ctx, d.WfCode, d.WfStep)
}

func (m *Module) documentFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
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
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		st.Message = err.Error()
		hide("wf_search", "wf_choice", "wf_text", "wf_date")
		return st, nil
	}
	st.Message = m.stepTitle(ctx, d, s)
	yes := true
	st.Links = m.createLinks(ctx, d, s)
	switch s.Kind {
	case kSelect, kConfirm:
		var cs []choice
		var err error
		if s.tree(ctx) {
			var path string
			cs, path, err = m.treeChoices(ctx, d, req.Values["wf_choice"], req.Values["wf_search"])
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
		if !s.tree(ctx) {
			sort.SliceStable(opts, func(i, j int) bool { return opts[i].Value == "" && opts[j].Value != "" })
		}
		st.Fields["wf_choice"] = metamodel.FieldState{Options: opts, Required: &yes}
		hide("wf_text", "wf_date")
		if s.Kind == kConfirm {
			hide("wf_search")
		} else if len(st.Links) > 0 {
			st.Message += " – noch nicht vorhanden? Im neuen Fenster anlegen (wird dort noch nicht gebucht), dann hier suchen."
		}
	case kDate:
		// Vorschlag: das Datum aus dem Dokument, sonst heute.
		v := firstOf(docField(d, s.Target), today())
		st.Fields["wf_date"] = metamodel.FieldState{Required: &yes, Value: &v}
		hide("wf_choice", "wf_text", "wf_search")
	case kName:
		name, err := m.proposeName(ctx, d)
		if err != nil {
			st.Message += " – " + err.Error()
		} else {
			st.Message += " – Endung " + firstOf(extOf(d.FileName), "(keine)") + " wird ergänzt"
		}
		st.Fields["wf_text"] = metamodel.FieldState{Required: &yes, Value: &name}
		hide("wf_choice", "wf_date", "wf_search")
	default:
		st.Fields["wf_text"] = metamodel.FieldState{Required: &yes}
		hide("wf_choice", "wf_date", "wf_search")
	}
	return st, nil
}

// continueAction: Eingabe zum wartenden Schritt, dann weiter.
func (m *Module) continueAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Search string `json:"wf_search"`
			Choice string `json:"wf_choice"`
			Text   string `json:"wf_text"`
			Date   string `json:"wf_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	s, err := m.waitingStep(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	outcome, value := positive(s.Kind), ""
	switch s.Kind {
	case kConfirm:
		if in.Data.Choice != oYes && in.Data.Choice != oNo {
			return sdk.Response{}, crud.Invalid("Bitte Ja oder Nein wählen")
		}
		outcome = in.Data.Choice
	case kSelect:
		value = strings.TrimSpace(in.Data.Choice)
		if value == "" {
			if s.param(ctx, "optional") != "true" {
				return sdk.Response{}, crud.Invalid("Bitte einen Wert wählen")
			}
			outcome = oSkipped
			break
		}
		if s.tree(ctx) {
			if strings.HasPrefix(value, treeUp) {
				return sdk.Response{}, crud.Invalid("Bitte eine Ablage der untersten Ebene wählen")
			}
		} else if cs, err := m.choices(ctx, d, s, in.Data.Search); err == nil && len(cs) > 0 && !containsChoice(cs, value) {
			return sdk.Response{}, crud.Invalid("Wert %q steht nicht zur Auswahl", value)
		}
	case kDate:
		value = in.Data.Date
	case kName:
		v, err := m.setName(ctx, d, in.Data.Text)
		if err != nil {
			return sdk.Response{}, err
		}
		value = v
	default:
		value = in.Data.Text
	}
	if outcome != oSkipped && s.Kind != kConfirm && s.Kind != kName {
		v, err := m.setTarget(ctx, d, s, value)
		if err != nil {
			return sdk.Response{}, err
		}
		value = v
	}
	if err := m.advance(ctx, d, s, outcome, value, "von "+firstOf(sdk.CallFromContext(ctx).Metadata["username"], sdk.CallFromContext(ctx).UserID)); err != nil {
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
	if err := m.startWorkflow(ctx, d.ID, wf); err != nil {
		return sdk.Response{}, err
	}
	return m.workflowState(ctx, d.ID)
}

// parseDate: JJJJ-MM-TT, TT.MM.JJJJ oder nach date_format (Go-Layout bzw. TT.MM.JJJJ-Schreibweise).
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

// goLayout: TT.MM.JJJJ bzw. DD.MM.YYYY → Go-Layout.
func goLayout(f string) string {
	r := strings.NewReplacer("JJJJ", "2006", "YYYY", "2006", "JJ", "06", "YY", "06", "MM", "01", "TT", "02", "DD", "02")
	return r.Replace(f)
}
