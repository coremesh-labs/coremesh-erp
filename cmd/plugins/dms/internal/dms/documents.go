package dms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/docservice"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Dokumente: im Eingang (Datei im Eingangsverzeichnis, Felder änderbar) oder
// abgelegt (Datei im Archiv, nur lesbar; Kopf fest, Bezüge und Notiz änderbar).
// Die Detailansicht zeigt Kopfzeile, Aktionen und das Dokument (Viewer);
// fester Link: /file/DmsDocument/<id>.

const secDocs = "Dokumente"

var (
	statusOptions = []metamodel.Option{
		{Value: stInbox, Label: "Im Eingang"},
		{Value: stFiled, Label: "Abgelegt"},
		{Value: stRemoved, Label: "Gelöscht (Frist abgelaufen)"},
	}
	wfStatusOptions = []metamodel.Option{
		{Value: wfRunning, Label: "Läuft"},
		{Value: wfWaiting, Label: "Wartet auf Eingabe"},
		{Value: wfDone, Label: "Fertig"},
		{Value: wfFailed, Label: "Fehler"},
	}
	lookupFileAccount = &metamodel.Lookup{Object: accountObject, ValueField: "number", LabelFields: []string{"name"},
		Columns: []string{"plan", "number", "name", "is_leaf"}}
	refObjects = []string{"Contract", "Building", "RentObject", "BusinessEntity", "SupplierInvoice", "JournalDraft", "JournalEntry", "BusinessPartner", "BankAccount"}
)

// headFields: Kopf eines Dokuments – nach der Ablage fest.
var headFields = []string{"title", "doc_type", "file_account", "partner_id", "doc_date", "retention"}

func (m *Module) document() *crud.Entity {
	return &crud.Entity{
		Object: docObject, Title: "Dokumente", Icon: "icon-file", Table: "dms__document", Section: secDocs,
		Keys: []string{"id"}, Surrogate: true, NoCreate: true, Order: "company_code, created_at DESC", TitleField: "title",
		Filters: []string{"company_code", "status", "file_account", "doc_type", "partner_id", "inbox", "wf_status", "ref_object", "ref_key"},
		// Suche auch im erkannten Text (eigene Tabelle).
		Search: []string{"name", "title", "note", "file_name",
			"(SELECT t.text FROM dms__document_text t WHERE t.document_id = dms__document.id)"},
		FilterExpr: map[string]func(v any) (string, []any){
			"ref_object": func(v any) (string, []any) {
				return "EXISTS (SELECT 1 FROM dms__document_ref r WHERE r.document_id = dms__document.id AND r.is_active = ? AND r.object = ?)", []any{true, crud.Str(v)}
			},
			"ref_key": func(v any) (string, []any) {
				return "EXISTS (SELECT 1 FROM dms__document_ref r WHERE r.document_id = dms__document.id AND r.is_active = ? AND r.object_key = ?)", []any{true, crud.Str(v)}
			},
		},
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "title", Label: "Titel", Type: tText, Listable: true},
			{Key: "doc_type", Label: "Dokumentart", Type: tText, Listable: true, Ref: refDocType},
			{Key: "file_account", Label: "Ablage", Type: tText, Listable: true, Lookup: lookupFileAccount},
			{Key: "partner_id", Label: "Geschäftspartner", Type: tText, Listable: true, Lookup: lookupPartner},
			{Key: "doc_date", Label: "Dokumentdatum", Type: tDate, Listable: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: statusOptions},
			{Key: "name", Label: "Dokumentname (Ort im Archiv)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "note", Label: "Notiz", Type: tArea},
			{Key: "retention", Label: "Ablagefrist (leer = aus Ablage bzw. Dokumentart)", Type: tText, Ref: refRetention},
			{Key: "retain_until", Label: "Aufzubewahren bis (leer = dauerhaft)", Type: tDate, ReadOnly: true},
			{Key: "archive", Label: "Archiv", Type: tText, ReadOnly: true, Ref: refArchive},
			{Key: "location", Label: "Ort im Archiv", Type: tText, ReadOnly: true},
			{Key: "link", Label: "Fester Link", Type: tText, ReadOnly: true, Virtual: true},
			{Key: "filed_at", Label: "Abgelegt am", Type: tText, ReadOnly: true},
			{Key: "filed_by", Label: "Abgelegt von", Type: tText, ReadOnly: true},
			{Key: "wf_code", Label: "Workflow", Type: tText, ReadOnly: true, Ref: refWorkflow},
			{Key: "wf_step", Label: "Schritt", Type: tText, ReadOnly: true},
			{Key: "wf_status", Label: "Workflow-Status", Type: tSel, Listable: true, ReadOnly: true, Options: wfStatusOptions},
			{Key: "wf_question", Label: "Offene Frage", Type: tText, ReadOnly: true, Virtual: true},
			{Key: "wf_log", Label: "Verlauf", Type: tArea, ReadOnly: true},
			{Key: "file_name", Label: "Datei (eingelesen)", Type: tText, ReadOnly: true},
			{Key: "content_type", Label: "Inhaltstyp", Type: tText, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true},
			{Key: "sha256", Label: "Prüfsumme (SHA-256)", Type: tText, ReadOnly: true},
			{Key: "source_sha256", Label: "Prüfsumme der eingelesenen Datei (vor der Aufbereitung)", Type: tText, ReadOnly: true},
			{Key: "inbox", Label: "Eingangsverzeichnis", Type: tText, ReadOnly: true},
			{Key: "inbox_file", Label: "Datei im Eingang", Type: tText, ReadOnly: true},
			{Key: "created_at", Label: "Eingelesen am", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Eingelesen von", Type: tText, ReadOnly: true},
			{Key: "removed_at", Label: "Gelöscht am", Type: tText, ReadOnly: true},
			{Key: "removed_by", Label: "Gelöscht von", Type: tText, ReadOnly: true},
			// Eingaben der Aktionen
			{Key: "wf_choice", Label: "Auswahl", Type: tSel, ActionOnly: true},
			{Key: "wf_text", Label: "Eingabe", Type: tText, ActionOnly: true},
			{Key: "wf_date", Label: "Datum", Type: tDate, ActionOnly: true},
			{Key: "ref_object", Label: "Bezug: Object", Type: tSel, ActionOnly: true, Required: true, Options: refObjectOptions()},
			{Key: "ref_key", Label: "Bezug: Schlüssel (z. B. GS-2026-0009, ROGoer14)", Type: tText, ActionOnly: true, Required: true},
			{Key: "restart_workflow", Label: "Workflow (leer = der des Eingangsverzeichnisses)", Type: tText, ActionOnly: true, Ref: refWorkflow},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Kopfdaten", Fields: []string{"title", "doc_type", "file_account", "partner_id", "doc_date", "status", "wf_question", "name", "link"}},
			{Key: "dokument", Title: "Dokument", Viewer: true},
			{Key: "bezuege", Title: "Bezüge", Relation: &metamodel.Relation{Object: refObject, ForeignKey: "document_id",
				Columns: []string{"object", "object_key", "doc_ref_id", "is_active"}}},
			{Key: "workflow", Title: "Workflow", Collapsed: true, Fields: []string{"wf_code", "wf_step", "wf_status", "wf_log"}},
			{Key: "ablage", Title: "Ablage", Collapsed: true, Fields: []string{"company_code", "retention", "retain_until", "archive", "location", "filed_at", "filed_by", "note"}},
			{Key: "datei", Title: "Datei", Collapsed: true, Fields: []string{"id", "file_name", "content_type", "size", "sha256", "source_sha256", "inbox", "inbox_file",
				"created_at", "created_by", "removed_at", "removed_by"}},
			{Key: "text", Title: "Erkannter Text", Collapsed: true, Relation: &metamodel.Relation{Object: textObject, ForeignKey: "document_id",
				Columns: []string{"text"}}},
		},
		Access:    &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate:  m.checkDocument,
		Decorate:  m.decorateDocument,
		FormState: m.documentFormState,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "continue", Label: "Weiter …", Record: true, FormState: true,
				Fields: []string{"wf_choice", "wf_text", "wf_date"}}, Handle: m.continueAction},
			{ActionConfig: metamodel.ActionConfig{Name: "file", Label: "Ablegen", Record: true,
				Confirm: "Dokument im Archiv ablegen? Danach ist die Datei nur lesbar und der Kopf fest."}, Handle: m.fileAction},
			{ActionConfig: metamodel.ActionConfig{Name: "addRef", Label: "Bezug hinzufügen …", Record: true,
				Fields: []string{"ref_object", "ref_key"}}, Handle: m.addRefAction},
			{ActionConfig: metamodel.ActionConfig{Name: "recognize", Label: "Text erkennen", Record: true,
				Confirm: "Text des Dokuments erkennen (pdftotext, sonst Texterkennung)?"}, Handle: m.recognizeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "restart", Label: "Workflow neu starten …", Record: true,
				Fields: []string{"restart_workflow"}}, Handle: m.restartAction},
			{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Nach Fristablauf löschen", Record: true,
				Confirm: "Dokument endgültig aus dem Archiv löschen? Nur nach Ablauf der Ablagefrist möglich."}, Handle: m.removeAction},
		},
		Routes: map[string]module.HandlerFunc{metamodel.ContentAction: m.contentAction},
	}
}

func refObjectOptions() []metamodel.Option {
	out := make([]metamodel.Option, len(refObjects))
	for i, o := range refObjects {
		out[i] = metamodel.Option{Value: o, Label: o}
	}
	return out
}

func (m *Module) checkDocument(ctx context.Context, rec, old crud.Record) error {
	if old != nil && crud.Str(old["status"]) != stInbox {
		for _, k := range headFields {
			if crud.Str(rec[k]) != crud.Str(old[k]) {
				return crud.Invalid("Dokument ist %s – Kopfdaten sind fest (nur Notiz und Bezüge änderbar)",
					strings.ToLower(optionLabel(statusOptions, crud.Str(old["status"]))))
			}
		}
		return nil
	}
	rec["file_account"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["file_account"])))
	if a := crud.Str(rec["file_account"]); a != "" {
		cfg, err := m.companyConfig(ctx, crud.Str(rec["company_code"]))
		if err != nil {
			return err
		}
		if _, err := m.accountPath(ctx, cfg.Plan, a); err != nil {
			return err
		}
	}
	rec["partner_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["partner_id"])))
	return nil
}

func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}

func (m *Module) decorateDocument(ctx context.Context, rec crud.Record) error {
	rec["link"] = fileLink(crud.Str(rec["id"]))
	if crud.Str(rec["wf_status"]) == wfWaiting {
		if st, err := m.loadStep(ctx, crud.Str(rec["wf_code"]), crud.Str(rec["wf_step"])); err == nil {
			rec["wf_question"] = st.question(ctx)
		}
	}
	return nil
}

// fileLink: fester Link des Dokuments.
func fileLink(id string) string { return "/file/" + docObject + "/" + url.PathEscape(id) }

// --- Dokument lesen ------------------------------------------------------------------

// docRow ist ein Dokument für die Verarbeitung.
type docRow struct {
	ID, CC, Name, Title, DocType, Account, Partner, DocDate, Retention, Archive, Location string
	FileName, ContentType, SHA, Status, Inbox, InboxFile, WfCode, WfStep, WfStatus, WfLog string
	Size                                                                                  int64
}

const docCols = `id, company_code, name, title, doc_type, file_account, partner_id, doc_date, retention, archive, location,
	file_name, content_type, sha256, status, inbox, inbox_file, wf_code, wf_step, wf_status, wf_log, size`

func scanDoc(r []any) *docRow {
	s := func(i int) string { return crud.Str(r[i]) }
	return &docRow{ID: s(0), CC: s(1), Name: s(2), Title: s(3), DocType: s(4), Account: s(5), Partner: s(6), DocDate: dateOf(r[7]),
		Retention: s(8), Archive: s(9), Location: s(10), FileName: s(11), ContentType: s(12), SHA: s(13), Status: s(14), Inbox: s(15),
		InboxFile: s(16), WfCode: s(17), WfStep: s(18), WfStatus: s(19), WfLog: s(20), Size: toInt(r[21])}
}

// loadDoc liest ein Dokument ohne Rechteprüfung (die prüft der Aufrufer).
func (m *Module) loadDoc(ctx context.Context, id string) (*docRow, error) {
	res, err := m.db.Query(ctx, `SELECT `+docCols+` FROM dms__document WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Dokument %s", sdk.ErrNotFound, id)
	}
	return scanDoc(res.Rows[0]), nil
}

// readableDoc: Dokument über die Entity (Leserecht im Buchungskreis) – id oder Dokumentname.
func (m *Module) readableDoc(ctx context.Context, idOrName string) (*docRow, error) {
	e := m.set.Entity(docObject)
	id := idOrName
	if _, err := e.Get(ctx, map[string]any{"id": id}); err != nil {
		if !errors.Is(err, sdk.ErrNotFound) {
			return nil, err
		}
		res, qerr := m.db.Query(ctx, `SELECT id FROM dms__document WHERE name = ?`, idOrName)
		if qerr != nil {
			return nil, qerr
		}
		if len(res.Rows) != 1 {
			return nil, err
		}
		id = crud.Str(res.Rows[0][0])
		if _, err := e.Get(ctx, map[string]any{"id": id}); err != nil {
			return nil, err
		}
	}
	return m.loadDoc(ctx, id)
}

// writableDoc: Dokument mit Recht action im Buchungskreis.
func (m *Module) writableDoc(ctx context.Context, payload any, action string) (*docRow, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(payload, &in); err != nil {
		return nil, err
	}
	d, err := m.readableDoc(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	if err := requireWrite(ctx, docObject, action, d.CC); err != nil {
		return nil, err
	}
	return d, nil
}

// content: Inhalt aus dem Eingangsverzeichnis bzw. dem Archiv.
func (m *Module) content(ctx context.Context, d *docRow) ([]byte, error) {
	switch d.Status {
	case stInbox:
		b, err := os.ReadFile(d.InboxFile)
		if err != nil {
			return nil, crud.Invalid("Datei im Eingang nicht lesbar (%s): %v", d.InboxFile, err)
		}
		return b, nil
	case stFiled:
		a, err := m.archiveByCode(ctx, d.Archive)
		if err != nil {
			return nil, err
		}
		return a.Load(d.Location)
	}
	return nil, crud.Invalid("Dokument %s ist gelöscht", d.ID)
}

func (m *Module) contentAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.readableDoc(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	b, err := m.content(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	name := d.Name
	if name == "" {
		name = d.FileName
	}
	return sdk.Response{Payload: metamodel.Content{Content: base64.StdEncoding.EncodeToString(b), ContentType: d.ContentType, FileName: name}}, nil
}

// --- Einlesen --------------------------------------------------------------------------

// ingestRequest: neue Datei im Eingangsverzeichnis.
type ingestRequest struct {
	CC, Inbox, FileName, ContentType, Path string
	Content                                []byte
}

// ingest legt das Dokument im Eingang an (Status INBOX), übernimmt die
// Vorgaben des Eingangsverzeichnisses und startet dessen Workflow. Ist der
// Inhalt schon bekannt (Prüfsumme), liefert es das vorhandene Dokument.
func (m *Module) ingest(ctx context.Context, r ingestRequest) (id string, duplicate bool, err error) {
	sum := sha256.Sum256(r.Content)
	sha := hex.EncodeToString(sum[:])
	res, err := m.db.Query(ctx, `SELECT id FROM dms__document WHERE company_code = ? AND (sha256 = ? OR source_sha256 = ?) AND status <> ?`,
		r.CC, sha, sha, stRemoved)
	if err != nil {
		return "", false, err
	}
	if len(res.Rows) > 0 {
		return crud.Str(res.Rows[0][0]), true, nil
	}
	ib, err := m.loadInbox(ctx, r.CC, r.Inbox)
	if err != nil {
		return "", false, err
	}
	id = crud.NewID()
	call := sdk.CallFromContext(ctx)
	if _, err := m.db.Exec(ctx, `INSERT INTO dms__document (id, company_code, file_name, content_type, size, sha256, source_sha256, status, inbox, inbox_file,
		wf_code, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, r.CC, r.FileName, orDefault(r.ContentType, "application/octet-stream"),
		len(r.Content), sha, sha, stInbox, r.Inbox, r.Path, nilIfEmpty(ib.Workflow), now(), nilIfEmpty(call.UserID)); err != nil {
		return "", false, err
	}
	if err := m.applyInboxParams(ctx, id, ib); err != nil {
		return "", false, err
	}
	if ib.Workflow != "" {
		if err := m.startWorkflow(ctx, id, ib.Workflow); err != nil {
			m.log.WarnContext(ctx, "Workflow nicht gestartet", "document", id, "err", err.Error())
		}
	}
	return id, false, nil
}

// --- Ablegen -----------------------------------------------------------------------------

func (m *Module) fileAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "file")
	if err != nil {
		return sdk.Response{}, err
	}
	name, warnings, err := m.fileDocument(ctx, d.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "Abgelegt: " + name
	if len(warnings) > 0 {
		msg += " – " + strings.Join(warnings, "; ")
	}
	return sdk.Response{Payload: map[string]any{"name": name, "message": msg}}, nil
}

// fileDocument legt ein Dokument im Archiv ab: Ablage (unterste Ebene,
// aktiv im Buchungskreis), Dokumentart und -datum sind Pflicht. Liefert den
// Dokumentnamen und Hinweise (z. B. Verweise, die nicht angelegt werden konnten).
func (m *Module) fileDocument(ctx context.Context, id string) (string, []string, error) {
	d, err := m.loadDoc(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if d.Status != stInbox {
		return "", nil, crud.Invalid("Dokument ist schon abgelegt bzw. gelöscht (%s)", d.Status)
	}
	var missing []string
	for _, f := range [][2]string{{d.Account, "Ablage"}, {d.DocType, "Dokumentart"}, {d.DocDate, "Dokumentdatum"}} {
		if f[0] == "" {
			missing = append(missing, f[1])
		}
	}
	if len(missing) > 0 {
		return "", nil, crud.Invalid("Zum Ablegen fehlt: %s", strings.Join(missing, ", "))
	}
	cfg, err := m.companyConfig(ctx, d.CC)
	if err != nil {
		return "", nil, err
	}
	acc, err := m.companyAccount(ctx, cfg, d.Account)
	if err != nil {
		return "", nil, err
	}
	dt, err := m.docTypeOf(ctx, d.DocType)
	if err != nil {
		return "", nil, err
	}
	retention := firstOf(d.Retention, acc.Retention, dt.Retention)
	until, err := m.retainUntil(ctx, retention, d.DocDate)
	if err != nil {
		return "", nil, err
	}
	content, err := m.content(ctx, d)
	if err != nil {
		return "", nil, err
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return "", nil, err
	}
	partner := m.partnerName(ctx, d.Partner)
	parts := nameParts{"account": d.Account, "company": firstOf(cfg.ShortName, cfg.CC), "date": strings.ReplaceAll(d.DocDate, "-", ""),
		"year": d.DocDate[:4], "doctype": d.DocType, "doctype_name": dt.Name, "partner": partner, "title": d.Title}
	var refKeys []string
	for _, r := range refs {
		refKeys = append(refKeys, r.Object+" "+r.Key)
	}
	if len(refs) > 0 {
		parts["object"] = shortKey(refs[0].Key, d.CC)
	}
	base := buildName(cfg.Pattern, parts)
	name, err := m.uniqueName(ctx, d.CC, base, extOf(d.FileName))
	if err != nil {
		return "", nil, err
	}
	a, err := m.archiveByCode(ctx, acc.Archive)
	if err != nil {
		return "", nil, err
	}
	loc, err := a.Store(StoreRequest{Content: content, Name: name, FileAccount: d.Account, Path: acc.Path, References: refKeys,
		DocDate: d.DocDate, RetainUntil: until, DocType: d.DocType, Partner: d.Partner})
	if err != nil {
		return "", nil, err
	}
	title := firstOf(d.Title, dt.Name+" "+d.DocDate)
	call := sdk.CallFromContext(ctx)
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET name = ?, title = ?, archive = ?, location = ?, status = ?, retention = ?, retain_until = ?,
		filed_at = ?, filed_by = ?, inbox_file = NULL WHERE id = ? AND status = ?`, name, title, acc.Archive, loc, stFiled, nilIfEmpty(retention),
		nilIfEmpty(until), now(), nilIfEmpty(call.UserID), d.ID, stInbox); err != nil {
		if rerr := a.Remove(loc); rerr != nil {
			m.log.ErrorContext(ctx, "Archiv: Datei nach Fehler nicht entfernt", "location", loc, "err", rerr.Error())
		}
		return "", nil, err
	}
	var warnings []string
	if d.InboxFile != "" {
		if err := os.Remove(d.InboxFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			warnings = append(warnings, "Datei im Eingang nicht entfernt: "+err.Error())
		}
	}
	d.Name, d.Title, d.Status = name, title, stFiled
	for _, r := range refs {
		if err := m.attachRef(ctx, d, dt, r); err != nil {
			warnings = append(warnings, fmt.Sprintf("Verweis an %s %s nicht angelegt: %v", r.Object, r.Key, err))
		}
	}
	return name, warnings, nil
}

func firstOf(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// uniqueName: Dokumentname im Buchungskreis; vorhanden → _2, _3 …
func (m *Module) uniqueName(ctx context.Context, cc, base, ext string) (string, error) {
	if base == "" {
		return "", crud.Invalid("Dokumentname ist leer – Muster des Dateinamens prüfen")
	}
	for i := 1; i < 1000; i++ {
		name := base + ext
		if i > 1 {
			name = fmt.Sprintf("%s_%d%s", base, i, ext)
		}
		res, err := m.db.Query(ctx, `SELECT 1 FROM dms__document WHERE company_code = ? AND name = ?`, cc, name)
		if err != nil {
			return "", err
		}
		if len(res.Rows) == 0 {
			return name, nil
		}
	}
	return "", crud.Invalid("Kein freier Dokumentname für %s", base)
}

// docTypeInfo ist eine Dokumentart.
type docTypeInfo struct{ Code, Name, Retention, RefKind string }

func (m *Module) docTypeOf(ctx context.Context, code string) (*docTypeInfo, error) {
	res, err := m.db.Query(ctx, `SELECT name, retention, ref_kind FROM dms__doc_type WHERE code = ? AND is_active = ?`, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Dokumentart %s gibt es nicht", code)
	}
	r := res.Rows[0]
	return &docTypeInfo{Code: code, Name: crud.Str(r[0]), Retention: crud.Str(r[1]), RefKind: crud.Str(r[2])}, nil
}

// retainUntil: Ende der Ablagefrist (leer = dauerhaft bzw. ohne Frist).
func (m *Module) retainUntil(ctx context.Context, code, docDate string) (string, error) {
	if code == "" {
		return "", nil
	}
	res, err := m.db.Query(ctx, `SELECT years, months, start FROM dms__retention WHERE code = ?`, code)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Ablagefrist %s gibt es nicht", code)
	}
	r := res.Rows[0]
	return retentionEnd(crud.Str(r[2]), int(toInt(r[0])), int(toInt(r[1])), docDate)
}

// retentionEnd: Fristende ab Dokumentdatum bzw. ab Ende des Kalenderjahres.
func retentionEnd(start string, years, months int, docDate string) (string, error) {
	if start == "FOREVER" {
		return "", nil
	}
	d, err := time.Parse(time.DateOnly, docDate)
	if err != nil {
		return "", crud.Invalid("Dokumentdatum %q ungültig", docDate)
	}
	if start == "END_OF_YEAR" {
		d = time.Date(d.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}
	return d.AddDate(years, months, 0).Format(time.DateOnly), nil
}

// partnerName: Suchbegriff bzw. Name des Partners (leer, wenn unbekannt).
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return id
	}
	var bp struct {
		SearchTerm string `json:"search_term"`
		Name1      string `json:"name1"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return id
	}
	return firstOf(bp.SearchTerm, bp.Name1, id)
}

// --- Löschen nach Fristablauf ----------------------------------------------------------

func (m *Module) removeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "remove")
	if err != nil {
		return sdk.Response{}, err
	}
	if d.Status != stFiled {
		return sdk.Response{}, crud.Invalid("Nur abgelegte Dokumente lassen sich nach Fristablauf löschen")
	}
	res, err := m.db.Query(ctx, `SELECT retain_until FROM dms__document WHERE id = ?`, d.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	until := dateOf(res.Rows[0][0])
	if until == "" {
		return sdk.Response{}, crud.Invalid("Dokument %s ist dauerhaft aufzubewahren", d.Name)
	}
	if until >= today() {
		return sdk.Response{}, crud.Invalid("Dokument %s ist bis %s aufzubewahren", d.Name, until)
	}
	a, err := m.archiveByCode(ctx, d.Archive)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := a.Remove(d.Location); err != nil {
		return sdk.Response{}, err
	}
	call := sdk.CallFromContext(ctx)
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET status = ?, removed_at = ?, removed_by = ? WHERE id = ?`,
		stRemoved, now(), nilIfEmpty(call.UserID), d.ID); err != nil {
		return sdk.Response{}, err
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range refs {
		m.detachRef(ctx, r)
	}
	return sdk.Response{Payload: map[string]any{"message": "Gelöscht: " + d.Name}}, nil
}

// --- Bezüge ------------------------------------------------------------------------------

const textObject = "DmsDocumentText"

func (m *Module) documentText() *crud.Entity {
	return &crud.Entity{
		Object: textObject, Title: "Erkannter Text", Icon: "icon-file-text", Table: "dms__document_text", Section: secDocs,
		Keys: []string{"document_id"}, ReadOnly: true, Filters: []string{"document_id"},
		Fields: []crud.Field{
			{Key: "document_id", Label: "Dokument", Type: tText, Listable: true},
			{Key: "text", Label: "Text", Type: tArea, Listable: true},
		},
		Access: &crud.Access{Object: docObject},
	}
}

func (m *Module) documentRef() *crud.Entity {
	return &crud.Entity{
		Object: refObject, Title: "Bezüge", Icon: "icon-link", Table: "dms__document_ref", Section: secDocs,
		Keys: []string{"document_id", "line_no"}, Order: "document_id, line_no", StatusField: "is_active",
		Filters: []string{"document_id", "object", "object_key"},
		Fields: []crud.Field{
			{Key: "document_id", Label: "Dokument", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: docObject, ValueField: "id", LabelFields: []string{"title"}}},
			{Key: "line_no", Label: "Nr.", Type: tNum, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, ReadOnly: true},
			{Key: "object", Label: "Object", Type: tSel, Required: true, Listable: true, Immutable: true, Options: refObjectOptions()},
			{Key: "object_key", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "doc_ref_id", Label: "Verweis (Plugin document)", Type: tText, ReadOnly: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access:      &crud.Access{Object: docObject, Records: true, CompanyCode: "company_code"},
		Validate:    m.checkRef,
		AfterCreate: m.afterRefCreate,
		OnEnd: func(ctx context.Context, _ string, rec crud.Record) (map[string]any, error) {
			m.detachRef(ctx, refRow{DocID: crud.Str(rec["document_id"]), Line: toInt(rec["line_no"]), Object: crud.Str(rec["object"]),
				Key: crud.Str(rec["object_key"]), DocRefID: crud.Str(rec["doc_ref_id"])})
			return map[string]any{"doc_ref_id": nil}, nil
		},
	}
}

func (m *Module) checkRef(ctx context.Context, rec, old crud.Record) error {
	if old != nil {
		return nil
	}
	d, err := m.loadDoc(ctx, crud.Str(rec["document_id"]))
	if err != nil {
		return err
	}
	if d.Status == stRemoved {
		return crud.Invalid("Dokument ist gelöscht")
	}
	key, err := m.resolveRef(ctx, d.CC, crud.Str(rec["object"]), crud.Str(rec["object_key"]))
	if err != nil {
		return err
	}
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1, SUM(CASE WHEN object = ? AND object_key = ? AND is_active = ? THEN 1 ELSE 0 END)
		FROM dms__document_ref WHERE document_id = ?`, rec["object"], key, true, d.ID)
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][1]) > 0 {
		return crud.Invalid("Bezug %s %s gibt es schon", crud.Str(rec["object"]), key)
	}
	rec["line_no"], rec["company_code"], rec["object_key"] = toInt(res.Rows[0][0]), d.CC, key
	return nil
}

// resolveRef: Schlüssel eines Bezugs prüfen – ohne Buchungskreis eingegeben
// (GS-2026-0009) wird er ergänzt (2000|GS-2026-0009).
func (m *Module) resolveRef(ctx context.Context, cc, object, key string) (string, error) {
	key = strings.TrimSpace(key)
	if object == "" || key == "" {
		return "", crud.Invalid("Bezug: Object und Schlüssel angeben")
	}
	try := []string{key}
	if !strings.HasPrefix(key, cc+"|") {
		try = append(try, cc+"|"+key)
	}
	var last error
	for _, k := range try {
		resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": k})
		if err == nil {
			var rec struct {
				ID any `json:"id"`
			}
			if sdk.Decode(resp.Payload, &rec) == nil && crud.Str(rec.ID) != "" {
				return crud.Str(rec.ID), nil
			}
			return k, nil
		}
		last = err
	}
	return "", crud.Invalid("Bezug %s %s nicht gefunden: %v", object, key, last)
}

func (m *Module) afterRefCreate(ctx context.Context, rec crud.Record) error {
	d, err := m.loadDoc(ctx, crud.Str(rec["document_id"]))
	if err != nil || d.Status != stFiled {
		return err
	}
	dt, err := m.docTypeOf(ctx, d.DocType)
	if err != nil {
		return err
	}
	r := refRow{DocID: d.ID, Line: toInt(rec["line_no"]), Object: crud.Str(rec["object"]), Key: crud.Str(rec["object_key"])}
	if err := m.attachRef(ctx, d, dt, r); err != nil {
		m.log.WarnContext(ctx, "Verweis nicht angelegt", "document", d.ID, "object", r.Object, "key", r.Key, "err", err.Error())
	}
	return nil
}

func (m *Module) addRefAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Object string `json:"ref_object"`
			Key    string `json:"ref_key"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.set.Entity(refObject).Create(ctx, map[string]any{"data": map[string]any{"document_id": d.ID, "object": in.Data.Object,
		"object_key": in.Data.Key}}); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Bezug " + in.Data.Object + " " + in.Data.Key + " hinzugefügt"}}, nil
}

// refRow ist ein Bezug.
type refRow struct {
	DocID       string
	Line        int64
	Object, Key string
	DocRefID    string
}

func (m *Module) activeRefs(ctx context.Context, docID string) ([]refRow, error) {
	res, err := m.db.Query(ctx, `SELECT line_no, object, object_key, doc_ref_id FROM dms__document_ref WHERE document_id = ? AND is_active = ?
		ORDER BY line_no`, docID, true)
	if err != nil {
		return nil, err
	}
	out := make([]refRow, 0, len(res.Rows))
	for _, r := range res.Rows {
		out = append(out, refRow{DocID: docID, Line: toInt(r[0]), Object: crud.Str(r[1]), Key: crud.Str(r[2]), DocRefID: crud.Str(r[3])})
	}
	return out, nil
}

// attachRef: Verweis am Ziel (Object Documents) mit Dokumentname und festem Link.
func (m *Module) attachRef(ctx context.Context, d *docRow, dt *docTypeInfo, r refRow) error {
	if r.DocRefID != "" {
		return nil
	}
	if r.Object == "BusinessPartner" || r.Object == "JournalDraft" || r.Object == "JournalEntry" {
		return nil // Objects ohne Abschnitt Dokumente: der Bezug steht nur im DMS
	}
	doc, err := docservice.New(m.services).Attach(ctx, docservice.AttachRequest{EntityType: r.Object, EntityID: r.Key,
		Document: docservice.Document{DocType: firstOf(dt.RefKind, "OTHER"), Title: firstOf(d.Title, d.Name), DocDate: d.DocDate,
			Location: d.Name, Link: fileLink(d.ID), Note: "DMS"}})
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document_ref SET doc_ref_id = ? WHERE document_id = ? AND line_no = ?`, doc.ID, r.DocID, r.Line)
	return err
}

func (m *Module) detachRef(ctx context.Context, r refRow) {
	if r.DocRefID == "" {
		return
	}
	if err := docservice.New(m.services).Detach(ctx, docservice.DetachRequest{ID: r.DocRefID}); err != nil {
		m.log.WarnContext(ctx, "Verweis nicht entfernt", "ref", r.DocRefID, "err", err.Error())
	}
}
