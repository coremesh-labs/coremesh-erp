package templating

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// Schnittstelle für andere Plugins:
//
//	Template.render {template, language, company_code, data, keep?} → {job_number, status}
//	SystemEvent TemplateJob.status {job_number, status, status_text} (NEW, RUNNING, DONE, ERROR)
//	Template.get {job_number} → {job_number, status, status_text, content_type, file_name, content (Base64)}

// renderRequest: Auftrag eines anderen Plugins (oder des Probeauftrags).
type renderRequest struct {
	Template    string `json:"template"`
	Language    string `json:"language"`
	CompanyCode string `json:"company_code"`
	Data        any    `json:"data"`
	Keep        bool   `json:"keep"`
}

func (m *Module) renderAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in renderRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	nr, err := m.render(ctx, in)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"job_number": nr, "status": m.text(ctx, `SELECT status FROM template__job WHERE job_number = ?`, nr)}}, nil
}

// render legt den Auftrag an und rechnet ihn (im Hintergrund, settings.sync: sofort).
func (m *Module) render(ctx context.Context, in renderRequest) (string, error) {
	cc := strings.TrimSpace(in.CompanyCode)
	if cc == "" {
		return "", crud.Invalid("Buchungskreis (company_code) fehlt")
	}
	code := strings.TrimSpace(in.Template)
	if code == "" {
		return "", crud.Invalid("Vorlage (template) fehlt")
	}
	lang := firstOf(in.Language, metamodel.LocaleDE)
	if err := requireWrite(ctx, jobObject, "render", cc); err != nil {
		return "", err
	}
	def, err := m.loadDefinition(ctx, code, lang)
	if err != nil {
		return "", err
	}
	if !def.Active {
		return "", crud.Invalid("Vorlage %s (%s) ist inaktiv", code, lang)
	}
	data, err := normalizeData(in.Data)
	if err != nil {
		return "", err
	}
	m.cleanup(ctx)
	nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: jobObject, CompanyCode: cc, Reference: code})
	if err != nil {
		return "", fmt.Errorf("Auftragsnummer: %w", err)
	}
	request, err := yaml.Marshal(map[string]any{"template": code, "language": lang, "company_code": cc, "target": def.Target, "data": data})
	if err != nil {
		return "", err
	}
	if _, err := m.db.Exec(ctx, `INSERT INTO template__job (job_number, company_code, template, language, target, status, status_text, request, keep, created_at, created_by)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nr.Number, cc, code, lang, def.Target, stNew, "angelegt", string(request), in.Keep, m.nowText(),
		nilIfEmpty(sdk.CallFromContext(ctx).UserID)); err != nil {
		return "", err
	}
	j := job{Number: nr.Number, CC: cc, Def: def, Data: data}
	m.status(ctx, j, stNew, "angelegt")
	if m.settings.Sync {
		m.process(ctx, j)
	} else {
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.process(context.WithoutCancel(ctx), j)
		}()
	}
	return nr.Number, nil
}

// job ist ein laufender Auftrag.
type job struct {
	Number, CC string
	Def        *defRow
	Data       map[string]any
}

// status: Status des Auftrags setzen und als SystemEvent melden.
func (m *Module) status(ctx context.Context, j job, st, text string) {
	if st != stNew {
		if _, err := m.db.Exec(ctx, `UPDATE template__job SET status = ?, status_text = ? WHERE job_number = ?`, st, text, j.Number); err != nil {
			m.log.WarnContext(ctx, "Status nicht gesetzt", "job", j.Number, "err", err.Error())
		}
	}
	if err := events.Push(ctx, m.services, events.Event{Object: jobObject, Action: "status", CompanyCode: j.CC, EntityID: j.Number, Source: Name,
		Data: map[string]any{"job_number": j.Number, "status": st, "status_text": text, "template": j.Def.Code, "language": j.Def.Language}}); err != nil {
		m.log.WarnContext(ctx, "Event nicht gesendet", "job", j.Number, "err", err.Error())
	}
}

// process rechnet den Auftrag und legt Ergebnis bzw. Fehler ab.
func (m *Module) process(ctx context.Context, j job) {
	m.status(ctx, j, stRunning, "in Arbeit")
	var out *output
	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("Abbruch: %v", r)
			}
		}()
		out, err = m.produce(ctx, j)
		return err
	}()
	if out == nil {
		out = &output{}
	}
	st, text := stDone, fmt.Sprintf("fertig: %s (%d Bytes)", out.FileName, len(out.Content))
	if err != nil {
		st, text = stError, err.Error()
		if len(text) > 2000 {
			text = text[:2000] + " …"
		}
		if j.Def.Debug && out.Source != "" && !hasFile(out.Files, out.SourceName) {
			out.Files = append(out.Files, zipFile{Name: out.SourceName, Data: []byte(out.Source)})
		}
	}
	for _, f := range out.Files {
		if _, ferr := m.db.Exec(ctx, `INSERT INTO template__job_file (job_number, name, company_code, content_type, size, content) VALUES (?, ?, ?, ?, ?, ?)`,
			j.Number, f.Name, j.CC, fileType(f.Name), len(f.Data), base64.StdEncoding.EncodeToString(f.Data)); ferr != nil {
			m.log.WarnContext(ctx, "Anhang nicht gespeichert", "job", j.Number, "file", f.Name, "err", ferr.Error())
		}
	}
	var content any
	if err == nil {
		content = base64.StdEncoding.EncodeToString(out.Content)
	}
	if _, uerr := m.db.Exec(ctx, `UPDATE template__job SET content = ?, content_type = ?, file_name = ?, size = ?, finished_at = ? WHERE job_number = ?`,
		content, nilIfEmpty(out.ContentType), nilIfEmpty(out.FileName), len(out.Content), m.nowText(), j.Number); uerr != nil {
		st, text = stError, "Ergebnis nicht gespeichert: "+uerr.Error()
	}
	m.status(ctx, j, st, text)
}

func hasFile(fs []zipFile, name string) bool {
	for _, f := range fs {
		if f.Name == name {
			return true
		}
	}
	return false
}

// output ist das Ergebnis eines Laufs.
type output struct {
	Content               []byte
	ContentType, FileName string
	Source, SourceName    string // erzeugter Text (bei PDF: LaTeX)
	Files                 []zipFile
}

func (m *Module) produce(ctx context.Context, j job) (*output, error) {
	def := j.Def
	out := &output{FileName: resultName(def)}
	bundles, err := m.chain(ctx, def.TemplateBundle)
	if err != nil {
		return out, err
	}
	files, err := chainFiles(bundles, def.Target)
	if err != nil {
		return out, err
	}
	var binaries []zipFile
	names := map[string]bool{}
	if def.Target == targetPDF && def.BinaryBundle != "" {
		b, err := m.loadBundle(ctx, def.BinaryBundle)
		if err != nil {
			return out, err
		}
		if b.Kind != kindBinary {
			return out, fmt.Errorf("Bundle %s enthält keine Binärdateien", b.Code)
		}
		binaries = b.Files
		for _, f := range binaries {
			names[f.Name] = true
		}
	}
	delims, err := defaultDelims(def.Target, def.Delims)
	if err != nil {
		return out, err
	}
	text, err := execute(engineInput{Target: def.Target, Language: def.Language, Job: j.Number, Delims: delims, Files: files,
		Binaries: names, Today: m.now(), Data: j.Data})
	if err != nil {
		return out, err
	}
	out.Source = text
	switch def.Target {
	case targetHTML:
		out.Content, out.ContentType, out.SourceName = []byte(text), "text/html; charset=utf-8", "ergebnis.html"
	case targetXML:
		out.Content, out.ContentType, out.SourceName = []byte(text), "application/xml; charset=utf-8", "ergebnis.xml"
	case targetCSV:
		out.Content, out.ContentType, out.SourceName = []byte(text), "text/csv; charset=utf-8", "ergebnis.csv"
	case targetPDF:
		out.SourceName = "main.tex"
		res, err := m.renderPDF(ctx, text, binaries, def.Debug)
		if res != nil {
			out.Files = res.Files
		}
		if err != nil {
			if !hasFile(out.Files, "main.tex") {
				out.Files = append(out.Files, zipFile{Name: "main.tex", Data: []byte(text)})
			}
			return out, err
		}
		out.Content, out.ContentType = res.PDF, "application/pdf"
	}
	return out, nil
}

// resultName: Dateiname des Ergebnisses mit der Endung der Ausgabe.
func resultName(def *defRow) string {
	ext := map[string]string{targetHTML: ".html", targetXML: ".xml", targetCSV: ".csv", targetPDF: ".pdf"}[def.Target]
	n := firstOf(def.FileName, def.Code)
	if !strings.EqualFold(path.Ext(n), ext) {
		n += ext
	}
	return n
}

// fileType: MIME-Typ eines Anhangs.
func fileType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".html":
		return "text/html; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	}
	return "text/plain; charset=utf-8"
}

// viewType: Anzeige im Viewer – PDF und Bilder wie sie sind, Text (auch HTML,
// XML, CSV) als Quelltext; der Download behält den Dateinamen.
func viewType(ct string) string {
	switch {
	case strings.HasPrefix(ct, "application/pdf"), strings.HasPrefix(ct, "image/"):
		return ct
	}
	return "text/plain; charset=utf-8"
}

func (m *Module) getAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		JobNumber string `json:"job_number"`
		ID        string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	nr := firstOf(in.JobNumber, in.ID)
	if nr == "" {
		return sdk.Response{}, crud.Invalid("Auftragsnummer (job_number) fehlt")
	}
	// Leserecht wie in der Oberfläche (Buchungskreis).
	if _, err := m.set.Entity(jobObject).Get(ctx, map[string]any{"id": nr}); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT status, status_text, content_type, file_name, content FROM template__job WHERE job_number = ?`, nr)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Auftrag %s", sdk.ErrNotFound, nr)
	}
	r := res.Rows[0]
	return sdk.Response{Payload: map[string]any{"job_number": nr, "status": crud.Str(r[0]), "status_text": crud.Str(r[1]),
		"content_type": crud.Str(r[2]), "file_name": crud.Str(r[3]), "content": crud.Str(r[4])}}, nil
}

// --- Aufträge in der Oberfläche ---------------------------------------------------------------

func (m *Module) job() *crud.Entity {
	return &crud.Entity{
		Object: jobObject, Title: "Aufträge", Icon: "icon-printer", Table: "template__job", Section: "Aufträge",
		Keys: []string{"job_number"}, Order: "created_at DESC, job_number DESC", NoCreate: true, TitleField: "job_number",
		Filters: []string{"company_code", "template", "language", "target", "status", "keep"}, Search: []string{"job_number", "template", "status_text"},
		Fields: []crud.Field{
			{Key: "job_number", Label: "Auftrag", Type: tText, Listable: true, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "template", Label: "Vorlage", Type: tText, Listable: true, ReadOnly: true},
			{Key: "language", Label: "Sprache", Type: tSel, Listable: true, ReadOnly: true, Options: languages},
			{Key: "target", Label: "Ausgabe", Type: tSel, Listable: true, ReadOnly: true, Options: targets},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: jobStatus},
			{Key: "status_text", Label: "Meldung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "keep", Label: "Nicht löschen", Type: tBool, Listable: true},
			{Key: "expires_on", Label: "Wird gelöscht ab", Type: tText, Listable: true, ReadOnly: true, Virtual: true},
			{Key: "file_name", Label: "Datei", Type: tText, ReadOnly: true},
			{Key: "content_type", Label: "Inhaltstyp", Type: tText, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true},
			{Key: "created_at", Label: "Angelegt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "created_by", Label: "Angelegt von", Type: tText, ReadOnly: true},
			{Key: "finished_at", Label: "Abgeschlossen", Type: tText, ReadOnly: true},
			{Key: "request", Label: "Eingegangene Daten (YAML)", Type: tArea, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "auftrag", Title: "Auftrag", Fields: []string{"job_number", "company_code", "template", "language", "target", "status", "status_text",
				"keep", "expires_on", "file_name", "content_type", "size", "created_at", "created_by", "finished_at"}},
			{Key: "ergebnis", Title: "Ergebnis", Viewer: true},
			{Key: "anhaenge", Title: "Anhänge (Protokolle, Quelltext)", Collapsed: true, Relation: &metamodel.Relation{Object: jobFileObject, ForeignKey: "job_number",
				Columns: []string{"name", "content_type", "size"}}},
			{Key: "daten", Title: "Eingegangene Daten", Collapsed: true, Fields: []string{"request"}},
		},
		Access:    &crud.Access{Records: true, CompanyCode: "company_code"},
		ListScope: func(ctx context.Context) (string, []any, bool, error) { m.cleanup(ctx); return "", nil, false, nil },
		Decorate:  m.decorateJob,
		Authorization: &metamodel.Authorization{Actions: []metamodel.AuthAction{
			{Name: "render", Label: "Aufträge erzeugen (Template.render)"}}},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "repeat", Label: "Erneut ausführen", Record: true,
				Confirm: "Neuen Auftrag mit derselben Vorlage und denselben Daten anlegen (z. B. nach Änderungen am Template)?"}, Handle: m.repeatJobAction},
		},
		Routes: map[string]module.HandlerFunc{metamodel.ContentAction: m.jobContentAction},
	}
}

func (m *Module) jobFile() *crud.Entity {
	return &crud.Entity{
		Object: jobFileObject, Title: "Anhänge der Aufträge", Icon: "icon-paperclip", Table: "template__job_file", Section: "Aufträge",
		Keys: []string{"job_number", "name"}, Order: "job_number DESC, name", ReadOnly: true, TitleField: "name",
		Filters: []string{"job_number", "company_code"},
		Fields: []crud.Field{
			{Key: "job_number", Label: "Auftrag", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Datei", Type: tText, Listable: true, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, ReadOnly: true},
			{Key: "content_type", Label: "Inhaltstyp", Type: tText, Listable: true, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "datei", Title: "Datei", Fields: []string{"job_number", "name", "company_code", "content_type", "size"}},
			{Key: "inhalt", Title: "Inhalt", Viewer: true},
		},
		Access: &crud.Access{Object: jobObject, Records: true, CompanyCode: "company_code"},
		Routes: map[string]module.HandlerFunc{metamodel.ContentAction: m.jobFileContentAction},
	}
}

func (m *Module) jobContentAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	resp, err := m.set.Entity(jobObject).Get(ctx, map[string]any{"id": in.ID})
	if err != nil {
		return sdk.Response{}, err
	}
	rec, _ := resp.Payload.(crud.Record)
	res, err := m.db.Query(ctx, `SELECT content, content_type, file_name, status_text FROM template__job WHERE job_number = ?`, in.ID)
	if err != nil || len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Auftrag %s", sdk.ErrNotFound, in.ID)
	}
	r := res.Rows[0]
	if crud.Str(r[0]) == "" {
		msg := fmt.Sprintf("Auftrag %s: %s – %s", in.ID, crud.Str(rec["status"]), crud.Str(r[3]))
		return sdk.Response{Payload: metamodel.Content{Content: base64.StdEncoding.EncodeToString([]byte(msg)), ContentType: "text/plain; charset=utf-8",
			FileName: in.ID + ".txt"}}, nil
	}
	return sdk.Response{Payload: metamodel.Content{Content: crud.Str(r[0]), ContentType: viewType(crud.Str(r[1])), FileName: crud.Str(r[2])}}, nil
}

func (m *Module) jobFileContentAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	e := m.set.Entity(jobFileObject)
	if _, err := e.Get(ctx, map[string]any{"id": in.ID}); err != nil {
		return sdk.Response{}, err
	}
	key, err := e.ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT content, content_type, name FROM template__job_file WHERE job_number = ? AND name = ?`, key["job_number"], key["name"])
	if err != nil || len(res.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Anhang %s", sdk.ErrNotFound, in.ID)
	}
	r := res.Rows[0]
	return sdk.Response{Payload: metamodel.Content{Content: crud.Str(r[0]), ContentType: viewType(crud.Str(r[1])), FileName: crud.Str(r[2])}}, nil
}

func (m *Module) repeatJobAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.set.Entity(jobObject).Get(ctx, map[string]any{"id": in.ID}); err != nil {
		return sdk.Response{}, err
	}
	var r struct {
		Template    string         `yaml:"template"`
		Language    string         `yaml:"language"`
		CompanyCode string         `yaml:"company_code"`
		Data        map[string]any `yaml:"data"`
	}
	if err := yaml.Unmarshal([]byte(m.text(ctx, `SELECT request FROM template__job WHERE job_number = ?`, in.ID)), &r); err != nil {
		return sdk.Response{}, fmt.Errorf("Auftrag %s: Eingang nicht lesbar: %w", in.ID, err)
	}
	nr, err := m.render(ctx, renderRequest{Template: r.Template, Language: r.Language, CompanyCode: r.CompanyCode, Data: r.Data})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Neuer Auftrag " + nr + " angelegt", "job_number": nr}}, nil
}

// --- Aufbewahrung -----------------------------------------------------------------------------

func (m *Module) company() *crud.Entity {
	return &crud.Entity{
		Object: companyObject, Title: "Aufbewahrung je Buchungskreis", Icon: "icon-clock", Table: "template__company", Section: "Einstellungen",
		Keys: []string{"company_code"}, Order: "company_code",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "keep_success_days", Label: "Aufträge mit Erfolg aufbewahren (Tage)", Type: tNum, Required: true, Listable: true},
			{Key: "keep_error_days", Label: "Aufträge mit Fehler aufbewahren (Tage)", Type: tNum, Required: true, Listable: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["company_code"] = strings.TrimSpace(crud.Str(rec["company_code"]))
			for _, k := range []string{"keep_success_days", "keep_error_days"} {
				if toInt(rec[k]) < 0 || toInt(rec[k]) > 3650 {
					return crud.Invalid("Aufbewahrung: 0 bis 3650 Tage")
				}
			}
			return nil
		},
	}
}

// keepDays: Fristen je Buchungskreis (ohne Eintrag: Konfiguration).
func (m *Module) keepDays(ctx context.Context) (map[string][2]int, [2]int) {
	s, f := m.settings.keepDays()
	def := [2]int{s, f}
	out := map[string][2]int{}
	res, err := m.db.Query(ctx, `SELECT company_code, keep_success_days, keep_error_days FROM template__company`)
	if err != nil {
		return out, def
	}
	for _, r := range res.Rows {
		out[crud.Str(r[0])] = [2]int{int(toInt(r[1])), int(toInt(r[2]))}
	}
	return out, def
}

func (m *Module) decorateJob(ctx context.Context, rec crud.Record) error {
	if crud.AsBool(rec["keep"]) || crud.Str(rec["finished_at"]) == "" {
		rec["expires_on"] = nil
		return nil
	}
	byCC, def := m.keepDays(ctx)
	d, ok := byCC[crud.Str(rec["company_code"])]
	if !ok {
		d = def
	}
	days := d[0]
	if crud.Str(rec["status"]) == stError {
		days = d[1]
	}
	t, err := time.Parse(time.RFC3339, crud.Str(rec["finished_at"]))
	if err != nil {
		return nil
	}
	rec["expires_on"] = t.AddDate(0, 0, days).Local().Format("02.01.2006")
	return nil
}

// cleanup löscht abgelaufene Aufträge (ohne „Nicht löschen“) und schließt
// liegengebliebene (Neustart während des Laufs) – höchstens alle 10 Minuten.
func (m *Module) cleanup(ctx context.Context) {
	m.cleanMu.Lock()
	if !m.lastClean.IsZero() && m.now().Sub(m.lastClean) < 10*time.Minute {
		m.cleanMu.Unlock()
		return
	}
	m.lastClean = m.now()
	m.cleanMu.Unlock()

	byCC, def := m.keepDays(ctx)
	res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM template__job WHERE keep = ?`, false)
	if err != nil {
		return
	}
	removed := 0
	for _, r := range res.Rows {
		cc := crud.Str(r[0])
		d, ok := byCC[cc]
		if !ok {
			d = def
		}
		for i, st := range []string{stDone, stError} {
			cut := m.now().UTC().AddDate(0, 0, -d[i]).Format(time.RFC3339)
			where := `company_code = ? AND keep = ? AND status = ? AND finished_at IS NOT NULL AND finished_at < ?`
			if _, err := m.db.Exec(ctx, `DELETE FROM template__job_file WHERE job_number IN (SELECT job_number FROM template__job WHERE `+where+`)`,
				cc, false, st, cut); err != nil {
				m.log.WarnContext(ctx, "Anhänge nicht gelöscht", "err", err.Error())
				continue
			}
			n, err := m.db.Exec(ctx, `DELETE FROM template__job WHERE `+where, cc, false, st, cut)
			if err != nil {
				m.log.WarnContext(ctx, "Aufträge nicht gelöscht", "err", err.Error())
				continue
			}
			removed += int(n.RowsAffected)
		}
	}
	stale := m.now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	if _, err := m.db.Exec(ctx, `UPDATE template__job SET status = ?, status_text = ?, finished_at = ? WHERE status IN (?, ?) AND created_at < ?`,
		stError, "abgebrochen (Neustart während des Laufs)", m.nowText(), stNew, stRunning, stale); err != nil {
		m.log.WarnContext(ctx, "Liegengebliebene Aufträge nicht geschlossen", "err", err.Error())
	}
	if removed > 0 {
		m.log.InfoContext(ctx, "Abgelaufene Aufträge gelöscht", "anzahl", removed)
	}
}
