package dms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Eingangsverzeichnisse: je Buchungskreis ein Verzeichnis auf dem Server mit
// Vorgaben (Object + Schlüssel) und einem Workflow. „Einlesen“ übernimmt neue
// Dateien als Dokumente im Eingang; „Hochladen …“ legt eine Datei aus dem
// Browser hinein. Vorgaben setzen Felder des Dokuments (Ablage, Dokumentart,
// Geschäftspartner, Ablagefrist) oder legen einen Bezug an (z. B. Building
// 2000|ROGoer14) – Workflow-Schritte, deren Feld schon belegt ist, gelten als
// übersprungen.

// paramObjects: Vorgaben, die ein Feld setzen (Object → Feld des Dokuments).
var paramObjects = map[string]string{accountObject: "file_account", docTypeObject: "doc_type", "BusinessPartner": "partner_id", retentionObject: "retention"}

func paramObjectOptions() []metamodel.Option {
	out := []metamodel.Option{
		{Value: accountObject, Label: "Ablage (DmsFileAccount)"},
		{Value: docTypeObject, Label: "Dokumentart (DmsDocType)"},
		{Value: retentionObject, Label: "Ablagefrist (DmsRetention)"},
	}
	for _, o := range refObjects {
		label := "Bezug " + o
		if o == "BusinessPartner" {
			label = "Geschäftspartner (BusinessPartner)"
		}
		out = append(out, metamodel.Option{Value: o, Label: label})
	}
	return out
}

func (m *Module) inbox() *crud.Entity {
	return &crud.Entity{
		Object: inboxObject, Title: "Eingangsverzeichnisse", Icon: "icon-inbox", Table: "dms__inbox", Section: secDocs,
		Keys: []string{"company_code", "code"}, Order: "company_code, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "path", Label: "Verzeichnis auf dem Server (absoluter Pfad)", Type: tText, Required: true, Listable: true},
			{Key: "workflow", Label: "Workflow", Type: tText, Listable: true, Ref: refWorkflow},
			{Key: "own_scans", Label: "Eigene Scans: Scans ohne Text-Ebene aufbereiten (Text-Ebene einfügen, Inhalt ersetzen)", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "file", Label: "Datei (PDF, Bild …)", Type: tFile, Required: true, ActionOnly: true, Binary: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "vorgaben", Title: "Vorgaben", Relation: &metamodel.Relation{Object: inboxParamObject, ForeignKey: "inbox",
				Match: map[string]string{"company_code": "company_code", "inbox": "code"}, Columns: []string{"line_no", "object", "value"}}},
			{Key: "dokumente", Title: "Dokumente im Eingang", Relation: &metamodel.Relation{Object: docObject, ForeignKey: "inbox",
				Match:   map[string]string{"company_code": "company_code", "inbox": "code"},
				Columns: []string{"file_name", "title", "doc_type", "file_account", "status", "wf_status"}}},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkInbox,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "scan", Label: "Einlesen", Record: true,
				Confirm: "Neue Dateien des Verzeichnisses einlesen und den Workflow starten?"}, Handle: m.scanAction},
			{ActionConfig: metamodel.ActionConfig{Name: "upload", Label: "Hochladen …", Record: true, Fields: []string{"file"}}, Handle: m.uploadAction},
		},
	}
}

func (m *Module) checkInbox(ctx context.Context, rec, _ crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	if _, err := m.companyConfig(ctx, crud.Str(rec["company_code"])); err != nil {
		return err
	}
	p := filepath.Clean(strings.TrimSpace(crud.Str(rec["path"])))
	if !filepath.IsAbs(p) {
		return crud.Invalid("Verzeichnis %q: absoluten Pfad angeben", crud.Str(rec["path"]))
	}
	if err := os.MkdirAll(p, dirMode); err != nil {
		return crud.Invalid("Verzeichnis %s nicht anlegbar: %v", p, err)
	}
	rec["path"] = p
	defaults(rec, map[string]any{"own_scans": false})
	return nil
}

func (m *Module) inboxParam() *crud.Entity {
	return &crud.Entity{
		Object: inboxParamObject, Title: "Vorgaben der Eingangsverzeichnisse", Icon: "icon-sliders", Table: "dms__inbox_param", Section: secDocs,
		Keys: []string{"company_code", "inbox", "line_no"}, Order: "company_code, inbox, line_no", Filters: []string{"company_code", "inbox"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "inbox", Label: "Eingangsverzeichnis", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "line_no", Label: "Nr.", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "object", Label: "Object", Type: tSel, Required: true, Listable: true, Options: paramObjectOptions()},
			{Key: "value", Label: "Schlüssel (z. B. 3.0.1, INVOICE, ROGoer14, GS-2026-0009)", Type: tText, Required: true, Listable: true},
		},
		Access:   &crud.Access{Object: inboxObject, Records: true, CompanyCode: "company_code"},
		Validate: m.checkInboxParam,
	}
}

func (m *Module) checkInboxParam(ctx context.Context, rec, old crud.Record) error {
	cc, object, value := crud.Str(rec["company_code"]), crud.Str(rec["object"]), strings.TrimSpace(crud.Str(rec["value"]))
	if old == nil {
		res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM dms__inbox_param WHERE company_code = ? AND inbox = ?`, cc, rec["inbox"])
		if err != nil {
			return err
		}
		rec["line_no"] = toInt(res.Rows[0][0])
	}
	v, err := m.checkParamValue(ctx, cc, object, value)
	if err != nil {
		return err
	}
	rec["value"] = v
	return nil
}

// checkParamValue: Vorgabe prüfen und Schlüssel vervollständigen.
func (m *Module) checkParamValue(ctx context.Context, cc, object, value string) (string, error) {
	switch object {
	case accountObject:
		cfg, err := m.companyConfig(ctx, cc)
		if err != nil {
			return "", err
		}
		if _, err := m.companyAccount(ctx, cfg, value); err != nil {
			return "", err
		}
		return value, nil
	case docTypeObject:
		if _, err := m.docTypeOf(ctx, trimUpper(value)); err != nil {
			return "", err
		}
		return trimUpper(value), nil
	case retentionObject:
		if _, err := m.retainUntil(ctx, trimUpper(value), today()); err != nil {
			return "", err
		}
		return trimUpper(value), nil
	case "BusinessPartner":
		if _, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": value}); err != nil {
			return "", crud.Invalid("Geschäftspartner %s nicht gefunden: %v", value, err)
		}
		return value, nil
	}
	return m.resolveRef(ctx, cc, object, value)
}

// inboxRow ist ein Eingangsverzeichnis mit Vorgaben.
type inboxRow struct {
	CC, Code, Name, Path, Workflow string
	OwnScans                       bool
	Params                         [][2]string // Object, Schlüssel
}

func (m *Module) loadInbox(ctx context.Context, cc, code string) (*inboxRow, error) {
	res, err := m.db.Query(ctx, `SELECT name, path, workflow, is_active, own_scans FROM dms__inbox WHERE company_code = ? AND code = ?`, cc, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Eingangsverzeichnis %s gibt es im Buchungskreis %s nicht", code, cc)
	}
	r := res.Rows[0]
	if !crud.AsBool(r[3]) {
		return nil, crud.Invalid("Eingangsverzeichnis %s ist inaktiv", code)
	}
	ib := &inboxRow{CC: cc, Code: code, Name: crud.Str(r[0]), Path: crud.Str(r[1]), Workflow: crud.Str(r[2]), OwnScans: crud.AsBool(r[4])}
	ps, err := m.db.Query(ctx, `SELECT object, value FROM dms__inbox_param WHERE company_code = ? AND inbox = ? ORDER BY line_no`, cc, code)
	if err != nil {
		return nil, err
	}
	for _, p := range ps.Rows {
		ib.Params = append(ib.Params, [2]string{crud.Str(p[0]), crud.Str(p[1])})
	}
	return ib, nil
}

// applyInboxParams setzt die Vorgaben am Dokument (neu eingelesen oder übergeben).
func (m *Module) applyInboxParams(ctx context.Context, docID string, ib *inboxRow) error {
	d := &docRow{ID: docID, CC: ib.CC}
	for _, p := range ib.Params {
		if field, ok := paramObjects[p[0]]; ok {
			if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+field+` = ? WHERE id = ?`, p[1], docID); err != nil {
				return err
			}
			if p[0] != "BusinessPartner" {
				continue
			}
		}
		if err := m.insertRef(ctx, d, p[0], p[1]); err != nil {
			return err
		}
	}
	return nil
}

// --- Einlesen und Hochladen ------------------------------------------------------------

// scanResult zählt das Ergebnis eines Einlesens.
type scanResult struct {
	New, Duplicate int
	Failed         []string
}

func (r scanResult) message(ib *inboxRow) string {
	msg := fmt.Sprintf("Eingang %s: %d neue Dokumente, %d schon bekannt", ib.Code, r.New, r.Duplicate)
	if len(r.Failed) > 0 {
		msg += fmt.Sprintf(", %d Fehler: %s", len(r.Failed), strings.Join(r.Failed, "; "))
	}
	return msg
}

func (m *Module) scan(ctx context.Context, cc, code string) (*inboxRow, scanResult, error) {
	var out scanResult
	if err := requireWrite(ctx, inboxObject, "scan", cc); err != nil {
		return nil, out, err
	}
	ib, err := m.loadInbox(ctx, cc, code)
	if err != nil {
		return nil, out, err
	}
	entries, err := os.ReadDir(ib.Path)
	if err != nil {
		return nil, out, crud.Invalid("Verzeichnis %s nicht lesbar: %v", ib.Path, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if !e.Type().IsRegular() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p := filepath.Join(ib.Path, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			out.Failed = append(out.Failed, e.Name()+": "+err.Error())
			continue
		}
		_, dup, err := m.ingest(ctx, ingestRequest{CC: cc, Inbox: code, FileName: e.Name(), ContentType: detectType(e.Name(), b), Path: p, Content: b})
		switch {
		case err != nil:
			out.Failed = append(out.Failed, e.Name()+": "+err.Error())
		case dup:
			out.Duplicate++
		default:
			out.New++
		}
	}
	return ib, out, nil
}

func detectType(name string, b []byte) string {
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		return t
	}
	return http.DetectContentType(b)
}

func (m *Module) scanAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(inboxObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	ib, res, err := m.scan(ctx, crud.Str(key["company_code"]), crud.Str(key["code"]))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"new": res.New, "duplicate": res.Duplicate, "failed": len(res.Failed), "message": res.message(ib)}}, nil
}

func (m *Module) scanCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Inbox string `json:"inbox"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	ib, res, err := m.scan(ctx, companyOf(req.Payload), trimUpper(in.Inbox))
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"new": res.New, "duplicate": res.Duplicate, "failed": len(res.Failed), "message": res.message(ib)}}, nil
}

// uploadAction: Datei aus dem Browser ins Eingangsverzeichnis und einlesen.
func (m *Module) uploadAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			File string `json:"file"`
			Name string `json:"file_name"`
			Type string `json:"file_type"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(inboxObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, code := crud.Str(key["company_code"]), crud.Str(key["code"])
	if err := requireWrite(ctx, inboxObject, "upload", cc); err != nil {
		return sdk.Response{}, err
	}
	b, err := base64.StdEncoding.DecodeString(in.Data.File)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Datei: Inhalt nicht lesbar (Base64 erwartet): %v", err)
	}
	if len(b) == 0 {
		return sdk.Response{}, crud.Invalid("Datei ist leer")
	}
	ib, err := m.loadInbox(ctx, cc, code)
	if err != nil {
		return sdk.Response{}, err
	}
	name := safeFileName(filepath.Base(in.Data.Name))
	if name == "" {
		name = "upload" + extFor(in.Data.Type)
	}
	p, err := writeNew(ib.Path, name, b)
	if err != nil {
		return sdk.Response{}, err
	}
	id, dup, err := m.ingest(ctx, ingestRequest{CC: cc, Inbox: code, FileName: filepath.Base(p), ContentType: firstOf(in.Data.Type, detectType(name, b)),
		Path: p, Content: b})
	if err != nil || dup {
		os.Remove(p)
	}
	if err != nil {
		return sdk.Response{}, err
	}
	if dup {
		return sdk.Response{Payload: map[string]any{"id": id, "duplicate": true, "message": "Dokument ist schon bekannt (gleicher Inhalt): " + id}}, nil
	}
	return sdk.Response{Payload: map[string]any{"id": id, "message": "Eingelesen: " + filepath.Base(p) + " – weiter unter Dokumente"}}, nil
}

func extFor(contentType string) string {
	if exts, _ := mime.ExtensionsByType(contentType); len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// writeNew schreibt eine neue Datei (vorhandener Name → name_2.ext …).
func writeNew(dir, name string, b []byte) (string, error) {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		n := name
		if i > 1 {
			n = fmt.Sprintf("%s_%d%s", base, i, ext)
		}
		p := filepath.Join(dir, n)
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("Eingang: %w", err)
		}
		_, werr := f.Write(b)
		if err := errors.Join(werr, f.Close()); err != nil {
			os.Remove(p)
			return "", fmt.Errorf("Eingang: %w", err)
		}
		return p, nil
	}
	return "", crud.Invalid("Kein freier Dateiname für %s", name)
}

// --- Übergabe an einen anderen Eingang -------------------------------------------------

// handoverAction: Dokument im Eingang an ein anderes Eingangsverzeichnis
// übergeben (verirrte Datei, z. B. zugeschickter Scan im Scanner-Eingang).
// Die Datei zieht um – war sie schon aufbereitet, mit dem aufgehobenen
// Original –, die Vorgaben des neuen Eingangs gelten, sein Workflow startet neu.
func (m *Module) handoverAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Inbox string `json:"handover_inbox"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if d.Status != stInbox || d.InboxFile == "" {
		return sdk.Response{}, crud.Invalid("Nur Dokumente im Eingang lassen sich übergeben")
	}
	target := trimUpper(in.Data.Inbox)
	if target == d.Inbox {
		return sdk.Response{}, crud.Invalid("Dokument liegt schon im Eingang %s", target)
	}
	if err := requireWrite(ctx, inboxObject, "upload", d.CC); err != nil {
		return sdk.Response{}, err
	}
	ib, err := m.loadInbox(ctx, d.CC, target)
	if err != nil {
		return sdk.Response{}, err
	}
	content, name := original(d)
	restored := content != nil
	if !restored {
		if content, err = os.ReadFile(d.InboxFile); err != nil {
			return sdk.Response{}, crud.Invalid("Datei im Eingang nicht lesbar: %v", err)
		}
		name = d.FileName
	}
	// Nie überschreiben: Name mit Herkunft, vorhanden → _2, _3 …
	base := safeFileName(filepath.Base(name))
	ext := filepath.Ext(base)
	base = strings.TrimSuffix(base, ext)
	if i := strings.Index(base, "_step_over_from_"); i >= 0 {
		base = base[:i]
	}
	p, err := writeNew(ib.Path, base+"_step_over_from_"+d.Inbox+ext, content)
	if err != nil {
		return sdk.Response{}, err
	}
	sum := sha256.Sum256(content)
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET inbox = ?, inbox_file = ?, file_name = ?, content_type = ?, size = ?, sha256 = ?,
		wf_code = ?, wf_step = NULL, wf_status = NULL WHERE id = ?`, target, p, filepath.Base(p), detectType(name, content), len(content),
		hex.EncodeToString(sum[:]), nilIfEmpty(ib.Workflow), d.ID); err != nil {
		os.Remove(p)
		return sdk.Response{}, err
	}
	dropOriginal(d)
	if err := os.Remove(d.InboxFile); err != nil && !os.IsNotExist(err) {
		m.log.WarnContext(ctx, "Datei im alten Eingang nicht entfernt", "file", d.InboxFile, "err", err.Error())
	}
	if _, err := m.db.Exec(ctx, `DELETE FROM dms__document_text WHERE document_id = ?`, d.ID); err != nil {
		return sdk.Response{}, err
	}
	if err := m.applyInboxParams(ctx, d.ID, ib); err != nil {
		return sdk.Response{}, err
	}
	line := fmt.Sprintf("Übergeben von Eingang %s an %s", d.Inbox, target)
	if restored {
		line += " (Original statt aufbereiteter Datei)"
	}
	m.appendLog(ctx, d.ID, line)
	if ib.Workflow != "" {
		if err := m.startWorkflow(ctx, d.ID, ib.Workflow); err != nil {
			return sdk.Response{}, err
		}
		return m.workflowState(ctx, d.ID)
	}
	return sdk.Response{Payload: map[string]any{"message": line}}, nil
}
