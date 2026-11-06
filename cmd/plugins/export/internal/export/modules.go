package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Module der App (Konzept Ablage_Dateiformat_Konzept.txt 7.10, Spezifikation 5.3/5.4)
//
//   - Katalog Module je Buchungskreis: Paket (zip mit manifest.json, Einstieg .js und
//     <stem>_bg.wasm) hochladen oder mitgeliefert (bundled/: der File Explorer aus
//     coremesh-pwa, Makefile-Ziel pwa-bundle).
//   - Je Modul ein eigener Schlüssel mod:<BuKr>.<modul>:<n> (age). Das Paket liegt
//     verschlüsselt unter m/<sha256>; nur wer das Modul bekommt, hat den Schlüssel in
//     seiner Startdatei. Wer es nicht mehr bekommt: neue Fassung des Schlüssels, Paket neu
//     verschlüsselt (neuer Hash), Modulliste neu.
//   - Wer ein Modul bekommt: „für alle Teilnehmer“ oder die Rollen am Modul.
//   - Die Modulliste i/modules ist signiert, aber nicht verschlüsselt (Namen und Hashes).
//   - Die App selbst (bundled/app.zip) wird beim Veröffentlichen in das App-Verzeichnis
//     des Ziels entpackt (Anbieter Verzeichnis) und kann heruntergeladen werden.

//go:embed bundled/explorer.zip
var bundledExplorer []byte

//go:embed bundled/app.zip
var bundledApp []byte

const (
	moduleObject       = "ExportModule"
	moduleRoleObject   = "ExportModuleRole"
	moduleAccessObject = "ExportModuleAccess"

	keyModule  = "MODULE"
	fileModule = "module"

	originBundled = "BUNDLED"
	originUpload  = "UPLOAD"

	moduleAPI = 1 // Schnittstelle der Hülle, die die mitgelieferte App kann
)

var moduleOrigins = []metamodel.Option{{Value: originBundled, Label: "mitgeliefert"}, {Value: originUpload, Label: "hochgeladen"}}

var moduleIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

var lookupModule = &metamodel.Lookup{Object: moduleObject, ValueField: "module_id", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code"}}

// modManifest: manifest.json im Modulpaket.
type modManifest struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Einstieg     string   `json:"einstieg"`
	ContentTypes []string `json:"content_types"`
	Sichtarten   []string `json:"sichtarten"`
	Rueckkanal   []string `json:"rueckkanal"`
	API          int      `json:"api"`
}

// parseModule prüft ein Modulpaket und liefert sein Manifest.
func parseModule(pkg []byte) (modManifest, error) {
	var mf modManifest
	zr, err := zip.NewReader(bytes.NewReader(pkg), int64(len(pkg)))
	if err != nil {
		return mf, crud.Invalid("Modulpaket: keine zip-Datei (%v)", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	read := func(name string) ([]byte, error) {
		f, ok := files[name]
		if !ok {
			return nil, crud.Invalid("Modulpaket: %s fehlt", name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 64<<20))
	}
	b, err := read("manifest.json")
	if err != nil {
		return mf, err
	}
	if err := json.Unmarshal(b, &mf); err != nil {
		return mf, crud.Invalid("Modulpaket: manifest.json unlesbar (%v)", err)
	}
	switch {
	case !moduleIDPattern.MatchString(mf.ID):
		return mf, crud.Invalid("Modulpaket: id %q ungültig (klein, a–z, 0–9, - und _)", mf.ID)
	case strings.TrimSpace(mf.Name) == "" || strings.TrimSpace(mf.Version) == "":
		return mf, crud.Invalid("Modulpaket: name und version sind Pflicht")
	case mf.API < 1 || mf.API > moduleAPI:
		return mf, crud.Invalid("Modulpaket: Schnittstelle api %d – die App kann %d", mf.API, moduleAPI)
	case !strings.HasSuffix(mf.Einstieg, ".js") || strings.Contains(mf.Einstieg, "/"):
		return mf, crud.Invalid("Modulpaket: einstieg %q – eine .js-Datei im Paket erwartet", mf.Einstieg)
	}
	for _, n := range []string{mf.Einstieg, strings.TrimSuffix(mf.Einstieg, ".js") + "_bg.wasm"} {
		if _, ok := files[n]; !ok {
			return mf, crud.Invalid("Modulpaket: %s fehlt", n)
		}
	}
	return mf, nil
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// --- Kataloge -------------------------------------------------------------------------------

func (m *Module) module() *crud.Entity {
	return &crud.Entity{
		Object: moduleObject, Customizing: true, Title: "Module der App", Icon: "icon-package", Table: "export__module", Section: "Einstellungen",
		Keys: []string{"company_code", "module_id"}, Order: "company_code, sort_order, module_id", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "origin"}, Search: []string{"module_id", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "module_id", Label: "Modul (id im Manifest, z. B. explorer)", Short: "Modul", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung (aus dem Manifest)", Short: "Bezeichnung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "all_roles", Label: "Für alle Teilnehmer (sonst nur die Rollen unten)", Short: "alle", Type: tBool, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "version", Label: "Fassung", Type: tText, Listable: true, ReadOnly: true, Group: "Paket"},
			{Key: "origin", Label: "Herkunft", Type: tSel, Listable: true, ReadOnly: true, Options: moduleOrigins, Group: "Paket"},
			{Key: "api", Label: "Schnittstelle der App (api)", Type: tNum, ReadOnly: true, Group: "Paket"},
			{Key: "entry", Label: "Einstieg", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "content_types", Label: "Zeigt Content-Types", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "view_types", Label: "Für Sichtarten", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "back_channel", Label: "Nachrichten an den Server (Rückkanal)", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true, Group: "Paket"},
			{Key: "package_sha", Label: "Prüfsumme des Pakets (SHA-256)", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "uploaded_at", Label: "Hochgeladen am", Type: tText, ReadOnly: true, Group: "Paket"},
			{Key: "uploaded_by", Label: "Hochgeladen von", Type: tText, ReadOnly: true, Group: "Paket", Lookup: crud.LookupUser},
			{Key: "upload_file", Label: "Modulpaket (zip mit manifest.json)", Type: tFile, Required: true, ActionOnly: true, Binary: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "rollen", Title: "Rollen, die das Modul bekommen", Relation: &metamodel.Relation{Object: moduleRoleObject, ForeignKey: "module_id",
				Match: match("module_id", "module_id"), Columns: []string{"role", "note"}}},
			{Key: "zugriffe", Title: "Teilnehmer mit dem Modul", Collapsed: true, Relation: &metamodel.Relation{Object: moduleAccessObject, ForeignKey: "module_id",
				Match: match("module_id", "module_id"), Columns: []string{"participant_id", "since", "until"}}},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "upload", Label: "Paket hochladen …", Icon: "icon-upload", Record: true, Fields: []string{"upload_file"}},
				Handle: m.moduleUploadAction},
			{ActionConfig: metamodel.ActionConfig{Name: "download", Label: "Paket herunterladen", Icon: "icon-download", Record: true}, Handle: m.moduleDownloadAction},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, moduleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				id := strings.ToLower(strings.TrimSpace(crud.Str(rec["module_id"])))
				if !moduleIDPattern.MatchString(id) {
					return crud.Invalid("Modul %q: klein, a–z, 0–9, - und _ (wie id im Manifest)", id)
				}
				rec["module_id"] = id
				if strings.TrimSpace(crud.Str(rec["name"])) == "" {
					rec["name"] = id
				}
			}
			rec["note"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["note"])))
			return nil
		},
	}
}

func (m *Module) moduleRole() *crud.Entity {
	return &crud.Entity{
		Object: moduleRoleObject, Title: "Rollen der Module", Icon: "icon-users", Table: "export__module_role", Section: "Einstellungen",
		Keys: []string{"company_code", "module_id", "role"}, Order: "company_code, module_id, role",
		Filters: []string{"company_code", "module_id", "role"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "module_id", Label: "Modul", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupModule},
			{Key: "role", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup(roleObject)},
			{Key: "note", Label: "Bemerkung", Type: tText},
		},
		Access: &crud.Access{Object: moduleObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, moduleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old != nil {
				return nil
			}
			cc := crud.Str(rec["company_code"])
			rec["role"], rec["module_id"] = trimUpper(rec["role"]), strings.ToLower(strings.TrimSpace(crud.Str(rec["module_id"])))
			if r, err := m.one(ctx, "SELECT 1 FROM export__role WHERE company_code = ? AND code = ? AND is_active = ?", cc, rec["role"], true); err != nil {
				return err
			} else if r == nil {
				return crud.Invalid("Rolle %s gibt es nicht (Katalog Rollen der Teilnehmer)", crud.Str(rec["role"]))
			}
			if r, err := m.one(ctx, "SELECT 1 FROM export__module WHERE company_code = ? AND module_id = ?", cc, rec["module_id"]); err != nil {
				return err
			} else if r == nil {
				return crud.Invalid("Modul %s gibt es nicht (Katalog Module der App)", crud.Str(rec["module_id"]))
			}
			return nil
		},
	}
}

func (m *Module) moduleAccess() *crud.Entity {
	return &crud.Entity{
		Object: moduleAccessObject, Title: "Module der Teilnehmer", Icon: "icon-eye", Table: "export__module_access", Section: "Sichten", ReadOnly: true,
		Keys: []string{"company_code", "participant_id", "module_id"}, Order: "company_code, participant_id, module_id",
		Filters: []string{"company_code", "participant_id", "module_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "participant_id", Label: "Teilnehmer", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupParticipant},
			{Key: "module_id", Label: "Modul", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupModule},
			{Key: "since", Label: "Hat es seit", Short: "seit", Type: tText, Listable: true, ReadOnly: true},
			{Key: "until", Label: "Bis (Berechtigung entzogen)", Short: "bis", Type: tText, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Object: participantObject, Records: true, CompanyCode: "company_code"},
	}
}

// --- Aktionen ------------------------------------------------------------------------------

// moduleUploadAction: ExportModule.upload {id, data: {upload_file}} – Paket prüfen und übernehmen.
func (m *Module) moduleUploadAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Modul")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, moduleObject, "upload", cc); err != nil {
		return sdk.Response{}, err
	}
	pkg, err := base64.StdEncoding.DecodeString(crud.Str(in.Data["upload_file"]))
	if err != nil || len(pkg) == 0 {
		return sdk.Response{}, crud.Invalid("Modulpaket fehlt bzw. ist unlesbar")
	}
	mf, err := parseModule(pkg)
	if err != nil {
		return sdk.Response{}, err
	}
	if mf.ID != id {
		return sdk.Response{}, crud.Invalid("Das Paket ist das Modul %q, nicht %q", mf.ID, id)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.storeModulePackage(ctx, cc, mf, pkg, originUpload, userOf(ctx)); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Modul %s (%s) Fassung %s übernommen – geht mit dem nächsten Veröffentlichen in die Ablagen",
		mf.ID, mf.Name, mf.Version)}}, nil
}

// storeModulePackage: Paket und Angaben aus dem Manifest in den Katalog.
func (m *Module) storeModulePackage(ctx context.Context, cc string, mf modManifest, pkg []byte, origin, user string) error {
	_, err := m.db.Exec(ctx, `UPDATE export__module SET name = ?, version = ?, api = ?, entry = ?, content_types = ?, view_types = ?, back_channel = ?, size = ?,
		package = ?, package_sha = ?, origin = ?, uploaded_at = ?, uploaded_by = ? WHERE company_code = ? AND module_id = ?`,
		mf.Name, mf.Version, mf.API, mf.Einstieg, nilIfEmpty(strings.Join(mf.ContentTypes, ", ")), nilIfEmpty(strings.Join(mf.Sichtarten, ", ")),
		nilIfEmpty(strings.Join(mf.Rueckkanal, ", ")), len(pkg), base64.StdEncoding.EncodeToString(pkg), sha256Hex(pkg), origin, m.stamp(), nilIfEmpty(user), cc, mf.ID)
	return err
}

func (m *Module) moduleDownloadAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Modul")
	if err != nil {
		return sdk.Response{}, err
	}
	r, err := m.one(ctx, "SELECT COALESCE(package, ''), COALESCE(version, '') FROM export__module WHERE company_code = ? AND module_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if r == nil || crud.Str(r[0]) == "" {
		return sdk.Response{}, crud.Invalid("Modul %s hat noch kein Paket", id)
	}
	return sdk.Response{Payload: map[string]any{"message": "Modulpaket " + id,
		"download": metamodel.Content{Content: crud.Str(r[0]), ContentType: "application/zip", FileName: fmt.Sprintf("%s-%s.zip", id, crud.Str(r[1]))}}}, nil
}

// targetAppAction: ExportTarget.app – die mitgelieferte App als zip herunterladen.
func (m *Module) targetAppAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	return sdk.Response{Payload: map[string]any{"message": "App der Ablage (auf den Webserver unter der Adresse der App entpacken)",
		"download": metamodel.Content{Content: base64.StdEncoding.EncodeToString(bundledApp), ContentType: "application/zip",
			FileName: "ablage-app-" + sha256Hex(bundledApp)[:8] + ".zip"}}}, nil
}

// --- Mitgeliefert ----------------------------------------------------------------------------

// ensureBundledModules: mitgelieferte Module in den Katalog (fehlt eins: anlegen, für alle
// Teilnehmer; mitgeliefert und älter: Paket erneuern). Hochgeladene bleiben unberührt.
func (m *Module) ensureBundledModules(ctx context.Context, cc string) (int, error) {
	n := 0
	for i, pkg := range [][]byte{bundledExplorer} {
		mf, err := parseModule(pkg)
		if err != nil {
			return n, fmt.Errorf("mitgeliefertes Modul: %w", err)
		}
		r, err := m.one(ctx, "SELECT COALESCE(origin, ''), COALESCE(package_sha, '') FROM export__module WHERE company_code = ? AND module_id = ?", cc, mf.ID)
		if err != nil {
			return n, err
		}
		if r == nil {
			if _, err := m.db.Exec(ctx, "INSERT INTO export__module (company_code, module_id, name, all_roles, sort_order, is_active) VALUES (?, ?, ?, ?, ?, ?)",
				cc, mf.ID, mf.Name, true, (i+1)*10, true); err != nil {
				return n, err
			}
			n++
		} else if crud.Str(r[0]) != originBundled || crud.Str(r[1]) == sha256Hex(pkg) {
			continue
		}
		if err := m.storeModulePackage(ctx, cc, mf, pkg, originBundled, ""); err != nil {
			return n, err
		}
	}
	return n, nil
}

// --- Veröffentlichen ------------------------------------------------------------------------

type modRow struct {
	id, name, version, sha, pkg string
	allRoles                    bool
	roles                       map[string]bool
	ct, views, back             []string
	key                         dsKeyVer
	ref                         string
}

// activeModules: Module mit Paket und ihren Rollen.
func (m *Module) activeModules(ctx context.Context, cc string) ([]*modRow, error) {
	res, err := m.db.Query(ctx, `SELECT module_id, COALESCE(name, module_id), COALESCE(version, ''), COALESCE(package_sha, ''), COALESCE(all_roles, ?),
		COALESCE(content_types, ''), COALESCE(view_types, ''), COALESCE(back_channel, '') FROM export__module
		WHERE company_code = ? AND is_active = ? AND package IS NOT NULL ORDER BY sort_order, module_id`, false, cc, true)
	if err != nil {
		return nil, err
	}
	var out []*modRow
	for _, r := range res.Rows {
		mr := &modRow{id: crud.Str(r[0]), name: crud.Str(r[1]), version: crud.Str(r[2]), sha: crud.Str(r[3]), allRoles: crud.AsBool(r[4]),
			ct: splitList(crud.Str(r[5])), views: splitList(crud.Str(r[6])), back: splitList(crud.Str(r[7])), roles: map[string]bool{}}
		rr, err := m.db.Query(ctx, "SELECT role FROM export__module_role WHERE company_code = ? AND module_id = ?", cc, mr.id)
		if err != nil {
			return nil, err
		}
		for _, x := range rr.Rows {
			mr.roles[crud.Str(x[0])] = true
		}
		out = append(out, mr)
	}
	return out, nil
}

func splitList(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// gets: bekommt ein Teilnehmer mit diesen Rollen das Modul?
func (mr *modRow) gets(roles []string) bool {
	if mr.allRoles {
		return true
	}
	for _, r := range roles {
		if mr.roles[r] {
			return true
		}
	}
	return false
}

// syncModuleAccess: wer welches Modul hat (seit/bis); liefert die Module, die jemand verloren hat.
func (m *Module) syncModuleAccess(ctx context.Context, cc, pid string, want map[string]bool, now string) (lost []string, err error) {
	cur, err := m.db.Query(ctx, "SELECT module_id FROM export__module_access WHERE company_code = ? AND participant_id = ? AND until IS NULL", cc, pid)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range cur.Rows {
		id := crud.Str(r[0])
		have[id] = true
		if want[id] {
			continue
		}
		if _, err := m.db.Exec(ctx, "UPDATE export__module_access SET until = ? WHERE company_code = ? AND participant_id = ? AND module_id = ?", now, cc, pid, id); err != nil {
			return nil, err
		}
		lost = append(lost, id)
	}
	for id := range want {
		if have[id] {
			continue
		}
		res, err := m.db.Exec(ctx, "UPDATE export__module_access SET since = ?, until = NULL WHERE company_code = ? AND participant_id = ? AND module_id = ?", now, cc, pid, id)
		if err != nil {
			return nil, err
		}
		if res.RowsAffected == 0 {
			if _, err := m.db.Exec(ctx, "INSERT INTO export__module_access (company_code, participant_id, module_id, since) VALUES (?, ?, ?, ?)", cc, pid, id, now); err != nil {
				return nil, err
			}
		}
	}
	return lost, nil
}

// moduleKey: neueste Fassung des Modulschlüssels (beim ersten Mal bzw. rotate: neue Fassung).
func (m *Module) moduleKey(ctx context.Context, cc, modID string, rotate bool) (dsKeyVer, error) {
	res, err := m.db.Query(ctx, `SELECT key_name, COALESCE(public, ''), COALESCE(version, 0), created_at FROM export__key
		WHERE kind = ? AND company_code = ? AND module_id = ? ORDER BY version`, keyModule, cc, modID)
	if err != nil {
		return dsKeyVer{}, err
	}
	n := len(res.Rows)
	if n > 0 && !rotate {
		r := res.Rows[n-1]
		return dsKeyVer{name: crud.Str(r[0]), public: crud.Str(r[1]), version: toInt(r[2]), since: dateOf(crud.Str(r[3]))}, nil
	}
	s, p, err := cm1.NewAgeKey()
	if err != nil {
		return dsKeyVer{}, err
	}
	sec, err := m.seal(ctx, s)
	if err != nil {
		return dsKeyVer{}, err
	}
	name := fmt.Sprintf("mod:%s.%s:%d", cc, modID, n+1)
	if err := m.insertKey(ctx, name, keyModule, n+1, cc, "", "", p, sec); err != nil {
		return dsKeyVer{}, err
	}
	if _, err := m.db.Exec(ctx, "UPDATE export__key SET module_id = ? WHERE key_name = ?", modID, name); err != nil {
		return dsKeyVer{}, err
	}
	return dsKeyVer{name: name, public: p, secret: s, version: n + 1, since: m.today()}, nil
}

// moduleSecret: geheimer Teil eines Modulschlüssels (für die Startdatei).
func (m *Module) moduleSecret(ctx context.Context, name string) (string, error) {
	r, err := m.one(ctx, "SELECT COALESCE(secret, '') FROM export__key WHERE key_name = ?", name)
	if err != nil || r == nil {
		return "", err
	}
	return m.unseal(ctx, crud.Str(r[0]))
}

// placeModules: je Modul das Paket mit seinem aktuellen Schlüssel unter m/<sha256>;
// ältere Pakete dieses Ziels gelten als nicht mehr gebraucht.
func (m *Module) placeModules(ctx context.Context, t *storeTgt, w storeWriter, mods []*modRow, rotate map[string]bool) error {
	keep := map[string]bool{}
	for _, mr := range mods {
		k, err := m.moduleKey(ctx, t.cc, mr.id, rotate[mr.id])
		if err != nil {
			return err
		}
		mr.key = k
		r, err := m.one(ctx, `SELECT path FROM export__store_file WHERE company_code = ? AND target = ? AND kind = ? AND document_id = ? AND source_sha = ? AND key_name = ?
			AND deleted_at IS NULL ORDER BY written_at DESC`, t.cc, t.code, fileModule, mr.id, mr.sha, k.name)
		if err != nil {
			return err
		}
		if r != nil {
			mr.ref = crud.Str(r[0])
		} else {
			pr, err := m.one(ctx, "SELECT package FROM export__module WHERE company_code = ? AND module_id = ?", t.cc, mr.id)
			if err != nil {
				return err
			}
			pkg, err := base64.StdEncoding.DecodeString(crud.Str(pr[0]))
			if err != nil {
				return fmt.Errorf("Modul %s: Paket beschädigt", mr.id)
			}
			rcpt, err := cm1.Recipient(k.public)
			if err != nil {
				return err
			}
			b, err := cm1.Seal(cm1.Header{Kind: cm1.KindModule, Key: k.name, ContentType: cm1.TypeModule, Packing: cm1.PackNone, Name: mr.id + ".zip"}, pkg, rcpt, nil, m.now())
			if err != nil {
				return err
			}
			mr.ref = "m/" + cm1.Hash(b)
			if err := w.put(mr.ref, b); err != nil {
				return err
			}
			if err := m.recordFile(ctx, t, mr.ref, fileModule, "", mr.id, mr.sha, k.name, cm1.TypeModule, mr.id+".zip", int64(len(b))); err != nil {
				return err
			}
		}
		keep[mr.ref] = true
		if _, err := m.db.Exec(ctx, "UPDATE export__store_file SET unused_since = NULL WHERE company_code = ? AND target = ? AND path = ?", t.cc, t.code, mr.ref); err != nil {
			return err
		}
	}
	res, err := m.db.Query(ctx, "SELECT path FROM export__store_file WHERE company_code = ? AND target = ? AND kind = ? AND unused_since IS NULL AND deleted_at IS NULL",
		t.cc, t.code, fileModule)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		if p := crud.Str(r[0]); !keep[p] {
			if _, err := m.db.Exec(ctx, "UPDATE export__store_file SET unused_since = ? WHERE company_code = ? AND target = ? AND path = ?", m.stamp(), t.cc, t.code, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeModuleList: i/modules (signiert, nicht verschlüsselt), nur bei Änderung (Fassung +1).
func (m *Module) writeModuleList(ctx context.Context, t *storeTgt, w storeWriter, sk *serverKeys, mods []*modRow) (bool, error) {
	list := []map[string]any{}
	for _, mr := range mods {
		list = append(list, map[string]any{"id": mr.id, "name": mr.name, "version": mr.version, "ref": mr.ref, "key": mr.key.name,
			"content_types": mr.ct, "sichtarten": mr.views, "rueckkanal": mr.back})
	}
	cj, _ := json.Marshal(list)
	hash := sha256Hex(cj)
	r, err := m.one(ctx, "SELECT COALESCE(modules_hash, ''), COALESCE(modules_version, 0) FROM export__target WHERE company_code = ? AND code = ?", t.cc, t.code)
	if err != nil {
		return false, err
	}
	if r != nil && crud.Str(r[0]) == hash {
		return false, nil
	}
	ver := 1
	if r != nil {
		ver = toInt(r[1]) + 1
	}
	j, _ := json.Marshal(map[string]any{"fassung": ver, "erstellt": m.stamp(), "module": list})
	b, err := cm1.Seal(cm1.Header{Kind: cm1.KindModules, Path: "i/modules", ContentType: cm1.TypeModules, Packing: cm1.PackNone}, j, nil, sk.signer, m.now())
	if err != nil {
		return false, err
	}
	if err := w.put("i/modules", b); err != nil {
		return false, err
	}
	if err := m.recordFile(ctx, t, "i/modules", fileModules, "", "", "", "", cm1.TypeModules, "", int64(len(b))); err != nil {
		return false, err
	}
	_, err = m.db.Exec(ctx, "UPDATE export__target SET modules_hash = ?, modules_version = ? WHERE company_code = ? AND code = ?", hash, ver, t.cc, t.code)
	return err == nil, err
}

// deployApp: die mitgelieferte App in das App-Verzeichnis des Ziels entpacken (nur bei
// neuer Fassung). Liefert true, wenn neu entpackt wurde.
func (m *Module) deployApp(ctx context.Context, t *storeTgt) (bool, error) {
	if t.appPath == "" || t.provider != providerLocal {
		return false, nil
	}
	sha := sha256Hex(bundledApp)
	if t.appSHA == sha {
		if _, err := os.Stat(filepath.Join(t.appPath, "index.html")); err == nil {
			return false, nil
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(bundledApp), int64(len(bundledApp)))
	if err != nil {
		return false, err
	}
	w := &localWriter{root: t.appPath}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return false, err
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return false, err
		}
		if err := w.put(f.Name, b); err != nil {
			return false, fmt.Errorf("App nach %s: %w", t.appPath, err)
		}
	}
	_, err = m.db.Exec(ctx, "UPDATE export__target SET app_sha = ?, app_at = ? WHERE company_code = ? AND code = ?", sha, m.stamp(), t.cc, t.code)
	return err == nil, err
}
