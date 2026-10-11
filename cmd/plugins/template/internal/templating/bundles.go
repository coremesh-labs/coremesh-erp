package templating

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

var codeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,59}$`)

func checkCode(rec crud.Record, key, label string) error {
	v := strings.TrimSpace(crud.Str(rec[key]))
	if !codeRe.MatchString(v) {
		return crud.Invalid("%s %q: Buchstaben, Ziffern, _ . - (höchstens 60 Zeichen)", label, v)
	}
	rec[key] = v
	return nil
}

var refBundle = &crud.Ref{Table: "template__bundle", Column: "code", Label: "Bundle", Object: bundleObject, LabelFields: []string{"name"},
	ActiveField: "is_active"}

func lookupBundle(kind string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: bundleObject, ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"kind": "=" + kind}}
}

func (m *Module) bundle() *crud.Entity {
	return &crud.Entity{
		Object: bundleObject, Title: "Bundles", Icon: "icon-package", Table: "template__bundle", Section: "Vorlagen",
		Keys: []string{"code"}, Order: "kind DESC, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"kind", "parent", "is_active"}, Search: []string{"code", "name", "file_list"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Immutable: true, Options: bundleKinds},
			{Key: "parent", Label: "Allgemeineres Bundle (Kette: Blöcke von dort werden hier überschrieben)", Type: tText, Listable: true,
				Ref: refBundle, Lookup: lookupBundle(kindTemplate), ShowIf: &metamodel.Condition{Field: "kind", Values: []string{kindTemplate}}},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "files", Label: "Dateien", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true},
			{Key: "changed_at", Label: "Inhalt geändert", Type: tText, ReadOnly: true},
			{Key: "file_list", Label: "Inhalt", Type: tArea, ReadOnly: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "upload_file", Label: "Zip-Datei", Type: tFile, Required: true, ActionOnly: true, Binary: true},
			{Key: "replace", Label: "Inhalt ersetzen (sonst ergänzen: gleichnamige Dateien werden ersetzt, neue kommen hinzu)", Type: tBool, ActionOnly: true},
			{Key: "dir", Label: "Verzeichnis auf dem Server", Type: tText, Required: true, ActionOnly: true},
			{Key: "new_code", Label: "Neuer Schlüssel", Type: tText, Required: true, ActionOnly: true},
			{Key: "new_name", Label: "Neue Bezeichnung", Type: tText, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "bundle", Title: "Bundle", Fields: []string{"code", "name", "kind", "parent", "description", "is_active"}},
			{Key: "inhalt", Title: "Inhalt", Fields: []string{"files", "size", "changed_at", "file_list"}},
			{Key: "vorlagen", Title: "Vorlagen mit diesem Bundle", Collapsed: true, Relation: &metamodel.Relation{Object: defObject, ForeignKey: "template_bundle",
				Columns: []string{"code", "language", "name", "target"}}},
		},
		Validate: m.checkBundle,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "upload", Label: "Zip hochladen …", Record: true, Fields: []string{"upload_file", "replace"}}, Handle: m.uploadBundleAction},
			{ActionConfig: metamodel.ActionConfig{Name: "download", Label: "Als Zip herunterladen", Record: true,
				Confirm: "Bundle als Zip-Datei herunterladen?"}, Handle: m.downloadBundleAction},
			{ActionConfig: metamodel.ActionConfig{Name: "export", Label: "In Verzeichnis entpacken …", Record: true, Fields: []string{"dir"}, FormState: true},
				Handle: m.exportBundleAction},
			{ActionConfig: metamodel.ActionConfig{Name: "import", Label: "Aus Verzeichnis einlesen …", Record: true, Fields: []string{"dir"}, FormState: true},
				Handle: m.importBundleAction},
			{ActionConfig: metamodel.ActionConfig{Name: "copy", Label: "Kopieren …", Record: true, Fields: []string{"new_code", "new_name"}}, Handle: m.copyBundleAction},
		},
		FormState: m.bundleFormState,
	}
}

func (m *Module) checkBundle(ctx context.Context, rec, old crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	rec["kind"] = trimUpper(rec["kind"])
	if rec["kind"] != kindTemplate && rec["kind"] != kindBinary {
		return crud.Invalid("Art %q: TEMPLATE oder BINARY", rec["kind"])
	}
	parent := strings.TrimSpace(crud.Str(rec["parent"]))
	rec["parent"] = nilIfEmpty(parent)
	if parent == "" {
		return nil
	}
	if rec["kind"] != kindTemplate {
		return crud.Invalid("Nur Bundles mit Go-Templates bilden Ketten (allgemeineres Bundle)")
	}
	// Kette: allgemeinere Bundles sind Go-Templates, ohne Kreis.
	code := crud.Str(rec["code"])
	seen := map[string]bool{code: true}
	for p := parent; p != ""; {
		if seen[p] {
			return crud.Invalid("Kette %s → %s bildet einen Kreis", code, parent)
		}
		seen[p] = true
		res, err := m.db.Query(ctx, `SELECT kind, parent FROM template__bundle WHERE code = ?`, p)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Bundle %s gibt es nicht", p)
		}
		if crud.Str(res.Rows[0][0]) != kindTemplate {
			return crud.Invalid("Bundle %s enthält keine Go-Templates", p)
		}
		p = crud.Str(res.Rows[0][1])
	}
	return nil
}

func (m *Module) bundleFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" || (req.Action != "export" && req.Action != "import") {
		return st, nil
	}
	if len(m.settings.roots) == 0 {
		st.Message = "Keine Verzeichnisse freigegeben (Konfiguration: settings.modules.template.allowed_dirs)."
		return st, nil
	}
	if strings.TrimSpace(req.Values["dir"]) == "" {
		code, _ := m.set.Entity(bundleObject).ParseID(req.ID)
		dir := filepath.Join(m.settings.roots[0], crud.Str(code["code"]))
		st.Fields["dir"] = metamodel.FieldState{Value: &dir}
	}
	if req.Action == "export" {
		st.Message = "Entpackt alle Dateien des Bundles in das Verzeichnis; gleichnamige Dateien werden überschrieben. Bearbeiten auf der Festplatte, danach „Aus Verzeichnis einlesen …“. Erlaubt: " + strings.Join(m.settings.roots, ", ")
	} else {
		st.Message = "Liest alle Dateien des Verzeichnisses (mit Unterverzeichnissen, ohne versteckte) und ersetzt damit den Inhalt des Bundles. Erlaubt: " + strings.Join(m.settings.roots, ", ")
	}
	return st, nil
}

// bundleRow ist ein Bundle mit Inhalt.
type bundleRow struct {
	Code, Name, Kind, Parent string
	Active                   bool
	Files                    []zipFile
}

func (m *Module) loadBundle(ctx context.Context, code string) (*bundleRow, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, kind, parent, content, is_active FROM template__bundle WHERE code = ?`, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, fmt.Errorf("%w: Bundle %s", sdk.ErrNotFound, code)
	}
	r := res.Rows[0]
	b := &bundleRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Kind: crud.Str(r[2]), Parent: crud.Str(r[3]), Active: crud.AsBool(r[5])}
	if s := crud.Str(r[4]); s != "" {
		raw, err := base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("Bundle %s: Inhalt beschädigt: %w", code, err)
		}
		if b.Files, err = readZip(raw); err != nil {
			return nil, fmt.Errorf("Bundle %s: %w", code, err)
		}
	}
	return b, nil
}

// setBundleFiles speichert den Inhalt (Zip) mit Anzahl, Größe und Dateiliste.
func (m *Module) setBundleFiles(ctx context.Context, code string, files []zipFile) error {
	raw, err := writeZip(files)
	if err != nil {
		return err
	}
	var size int64
	names := make([]string, len(files))
	for i, f := range files {
		size += int64(len(f.Data))
		names[i] = f.Name
	}
	_, err = m.db.Exec(ctx, `UPDATE template__bundle SET content = ?, files = ?, size = ?, file_list = ?, changed_at = ? WHERE code = ?`,
		base64.StdEncoding.EncodeToString(raw), len(files), size, strings.Join(names, "\n"), m.nowText(), code)
	return err
}

type bundleActionIn struct {
	ID   string `json:"id"`
	Data struct {
		File     string `json:"upload_file"`
		FileName string `json:"upload_file_name"`
		Replace  any    `json:"replace"`
		Dir      string `json:"dir"`
		NewCode  string `json:"new_code"`
		NewName  string `json:"new_name"`
	} `json:"data"`
}

// bundleFor: Bundle der Aktion; write: Änderungsrecht nötig.
func (m *Module) bundleFor(ctx context.Context, req sdk.Request, write bool) (*bundleRow, bundleActionIn, error) {
	var in bundleActionIn
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return nil, in, err
	}
	key, err := m.set.Entity(bundleObject).ParseID(in.ID)
	if err != nil {
		return nil, in, err
	}
	if write {
		if err := requireWrite(ctx, bundleObject, "update", ""); err != nil {
			return nil, in, err
		}
	}
	b, err := m.loadBundle(ctx, crud.Str(key["code"]))
	return b, in, err
}

func (m *Module) uploadBundleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	b, in, err := m.bundleFor(ctx, req, true)
	if err != nil {
		return sdk.Response{}, err
	}
	raw, err := base64.StdEncoding.DecodeString(in.Data.File)
	if err != nil || len(raw) == 0 {
		return sdk.Response{}, crud.Invalid("Zip-Datei fehlt")
	}
	add, err := readZip(raw)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(add) == 0 {
		return sdk.Response{}, crud.Invalid("Die Zip-Datei %s enthält keine Dateien", in.Data.FileName)
	}
	files := add
	verb := "ersetzt"
	if !crud.AsBool(in.Data.Replace) {
		files, verb = mergeFiles(b.Files, add), "ergänzt"
	}
	if err := m.setBundleFiles(ctx, b.Code, files); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bundle %s %s: %d Dateien aus %s, jetzt %d Dateien%s",
		b.Code, verb, len(add), in.Data.FileName, len(files), m.ignoredNote(b.Kind, files))}}, nil
}

// ignoredNote: Hinweis auf Dateien, die beim Rechnen nicht zählen (Template-Bundles).
func (m *Module) ignoredNote(kind string, files []zipFile) string {
	if kind != kindTemplate {
		return ""
	}
	n := 0
	for _, f := range files {
		if templateExt(f.Name) == "" {
			n++
		}
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d ohne .html, .xml oder .text – beim Rechnen ignoriert)", n)
}

func (m *Module) downloadBundleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	b, _, err := m.bundleFor(ctx, req, false)
	if err != nil {
		return sdk.Response{}, err
	}
	raw, err := writeZip(b.Files)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bundle %s: %d Dateien", b.Code, len(b.Files)),
		"download": metamodel.Content{Content: base64.StdEncoding.EncodeToString(raw), ContentType: "application/zip", FileName: b.Code + ".zip"}}}, nil
}

func (m *Module) exportBundleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	b, in, err := m.bundleFor(ctx, req, false)
	if err != nil {
		return sdk.Response{}, err
	}
	dir, err := m.allowedDir(in.Data.Dir)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := exportFiles(dir, b.Files); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bundle %s: %d Dateien nach %s entpackt", b.Code, len(b.Files), dir)}}, nil
}

func (m *Module) importBundleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	b, in, err := m.bundleFor(ctx, req, true)
	if err != nil {
		return sdk.Response{}, err
	}
	dir, err := m.allowedDir(in.Data.Dir)
	if err != nil {
		return sdk.Response{}, err
	}
	files, err := importFiles(dir)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(files) == 0 {
		return sdk.Response{}, crud.Invalid("Verzeichnis %s enthält keine Dateien", dir)
	}
	if err := m.setBundleFiles(ctx, b.Code, files); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bundle %s: %d Dateien aus %s eingelesen%s", b.Code, len(files), dir,
		m.ignoredNote(b.Kind, files))}}, nil
}

func (m *Module) copyBundleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	b, in, err := m.bundleFor(ctx, req, false)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, bundleObject, "create", ""); err != nil {
		return sdk.Response{}, err
	}
	name := firstOf(in.Data.NewName, b.Name+" (Kopie)")
	res, err := m.db.Query(ctx, `SELECT description FROM template__bundle WHERE code = ?`, b.Code)
	if err != nil {
		return sdk.Response{}, err
	}
	data := map[string]any{"code": strings.TrimSpace(in.Data.NewCode), "name": name, "kind": b.Kind, "parent": nilIfEmpty(b.Parent)}
	if len(res.Rows) > 0 {
		data["description"] = res.Rows[0][0]
	}
	if _, err := m.set.Entity(bundleObject).Create(ctx, map[string]any{"data": data}); err != nil {
		return sdk.Response{}, err
	}
	if err := m.setBundleFiles(ctx, strings.TrimSpace(in.Data.NewCode), b.Files); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Bundle %s nach %s kopiert (%d Dateien)", b.Code, strings.TrimSpace(in.Data.NewCode), len(b.Files))}}, nil
}

func firstOf(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
