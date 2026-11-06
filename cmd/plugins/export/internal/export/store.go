package export

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablage für die PWA (Konzept Ablage_Dateiformat_Konzept.txt 7.4–7.6, Format
// Ablage_Format_Spezifikation.txt): Ziel der Art OBJECTSTORE, Anbieter LOCAL
// (Verzeichnis, z. B. das Ablageverzeichnis des Testservers); R2 folgt.
//
// Lieferung eines Stands: Datendateien o/<sha256> (gleicher Inhalt mit gleichem
// Schlüssel wird nicht neu geschrieben), dann der Index i/<ablage-id>, dann die
// Startdateien i/<start-id> der Teilnehmer (publish). Was kein Index mehr braucht,
// wird nach der Aufbewahrung (keep_days) gelöscht.

const (
	targetStore   = "OBJECTSTORE"
	providerLocal = "LOCAL"

	fileData    = "data"
	fileDataset = "dataset"
	fileStart   = "start"
	fileModules = "modules"

	defaultKeepDays = 7
	lockedHash      = "locked"
)

var providerOptions = []metamodel.Option{{Value: providerLocal, Label: "Verzeichnis auf dem Server (z. B. Ablage des Testservers)"}}

type storeTgt struct {
	cc, code, name, provider, path, publicURL, pwaURL, appPath, appSHA string
	keepDays                                                           int
}

func (m *Module) storeTarget(ctx context.Context, cc, code string) (*storeTgt, error) {
	r, err := m.one(ctx, `SELECT name, kind, COALESCE(provider, ''), COALESCE(path, ''), COALESCE(public_url, ''), COALESCE(pwa_url, ''), keep_days,
		COALESCE(app_path, ''), COALESCE(app_sha, '') FROM export__target WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, crud.Invalid("Ablage %s gibt es im Buchungskreis %s nicht", code, cc)
	}
	if crud.Str(r[1]) != targetStore {
		return nil, crud.Invalid("Ziel %s ist kein Objektspeicher (Ablage für die App)", code)
	}
	t := &storeTgt{cc: cc, code: code, name: crud.Str(r[0]), provider: crud.Str(r[2]), path: crud.Str(r[3]), publicURL: crud.Str(r[4]), pwaURL: crud.Str(r[5]),
		keepDays: defaultKeepDays, appPath: crud.Str(r[7]), appSHA: crud.Str(r[8])}
	if r[6] != nil {
		t.keepDays = toInt(r[6])
	}
	return t, nil
}

// --- Schreiben --------------------------------------------------------------------------

type storeWriter interface {
	put(p string, b []byte) error
	remove(p string) error
}

type localWriter struct{ root string }

func (t *storeTgt) writer() (storeWriter, error) {
	switch t.provider {
	case providerLocal:
		if t.path == "" {
			return nil, crud.Invalid("Ablage %s: Verzeichnis fehlt", t.code)
		}
		if err := os.MkdirAll(t.path, 0o755); err != nil {
			return nil, crud.Invalid("Ablage %s: Verzeichnis %s nicht anlegbar – %v", t.code, t.path, err)
		}
		return &localWriter{root: t.path}, nil
	}
	return nil, crud.Invalid("Ablage %s: Anbieter %q wird noch nicht unterstützt", t.code, t.provider)
}

func (w *localWriter) file(p string) (string, error) {
	if p == "" || strings.Contains(p, "..") || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("Pfad %q in der Ablage ungültig", p)
	}
	return filepath.Join(w.root, filepath.FromSlash(p)), nil
}

// put schreibt atomar (Hilfsdatei, dann umbenennen) – die App liest nie halbe Dateien.
func (w *localWriter) put(p string, b []byte) error {
	f, err := w.file(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		return err
	}
	tmp := f + ".schreiben"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f)
}

func (w *localWriter) remove(p string) error {
	f, err := w.file(p)
	if err != nil {
		return err
	}
	if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// recordFile: Belegung (welche Datei liegt in der Ablage, wozu).
func (m *Module) recordFile(ctx context.Context, t *storeTgt, p, kind, dsID, docID, srcSHA, key, ct, name string, size int64) error {
	res, err := m.db.Exec(ctx, `UPDATE export__store_file SET kind = ?, dataset_id = ?, document_id = ?, source_sha = ?, key_name = ?, content_type = ?, file_name = ?,
		size = ?, written_at = ?, unused_since = NULL, deleted_at = NULL WHERE company_code = ? AND target = ? AND path = ?`,
		kind, nilIfEmpty(dsID), nilIfEmpty(docID), nilIfEmpty(srcSHA), nilIfEmpty(key), nilIfEmpty(ct), nilIfEmpty(name), size, m.stamp(), t.cc, t.code, p)
	if err != nil || res.RowsAffected > 0 {
		return err
	}
	_, err = m.db.Exec(ctx, `INSERT INTO export__store_file (company_code, target, path, kind, dataset_id, document_id, source_sha, key_name, content_type, file_name, size, written_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.cc, t.code, p, kind, nilIfEmpty(dsID), nilIfEmpty(docID), nilIfEmpty(srcSHA), nilIfEmpty(key), nilIfEmpty(ct),
		nilIfEmpty(name), size, m.stamp())
	return err
}

// --- Index eines Datasets ------------------------------------------------------------------

type codeName struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type idName struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type markJSON struct {
	Code  string `json:"code"`
	Name  string `json:"name"`
	Notiz string `json:"notiz,omitempty"`
	Am    string `json:"am,omitempty"`
}

type idxElem struct {
	Nr           int       `json:"nr"`
	Ordner       string    `json:"ordner"`
	Name         string    `json:"name"`
	Titel        string    `json:"titel"`
	Datum        string    `json:"datum,omitempty"`
	Art          *codeName `json:"art,omitempty"`
	Partner      *idName   `json:"partner,omitempty"`
	ContentType  string    `json:"content_type"`
	Groesse      int64     `json:"groesse"`
	Key          string    `json:"key"`
	Neu          bool      `json:"neu"`
	Kennzeichen  *markJSON `json:"kennzeichen,omitempty"`
	ErsetztDurch *int      `json:"ersetzt_durch,omitempty"`
	Ref          string    `json:"ref"`
}

type idxFolder struct {
	Ordner string `json:"ordner"`
	Name   string `json:"name"`
}

type period struct {
	Von string `json:"von,omitempty"`
	Bis string `json:"bis,omitempty"`
}

type dsIndex struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Fassung    int         `json:"fassung"`
	Stand      int         `json:"stand"`
	Sichtart   codeName    `json:"sichtart"`
	Zeitraum   *period     `json:"zeitraum,omitempty"`
	Geliefert  string      `json:"geliefert"`
	Gliederung []idxFolder `json:"gliederung,omitempty"`
	Elemente   []idxElem   `json:"elemente"`
	Teile      []string    `json:"teile,omitempty"`
	Modul      string      `json:"modul"`
}

// writeStore liefert einen Stand in die Ablage (statt writeRun bei Zielen der Art
// OBJECTSTORE): Datendateien, Index; danach publish für die Startdateien.
func (m *Module) writeStore(ctx context.Context, mf manifest, t *storeTgt) (where, sum string, err error) {
	cc := mf.Company
	if mf.Dataset == "" {
		return "", "", crud.Invalid("In die Ablage der App gehen nur Stände eines Datasets")
	}
	r, err := m.one(ctx, `SELECT COALESCE(store_id, ''), COALESCE(store_target, ''), COALESCE(store_version, 0), name, COALESCE(view_type, ''), period_from, period_to,
		COALESCE(state_count, 0) FROM export__dataset WHERE company_code = ? AND dataset_id = ?`, cc, mf.Dataset)
	if err != nil {
		return "", "", err
	}
	if r == nil {
		return "", "", fmt.Errorf("%w: Dataset %s", sdk.ErrNotFound, mf.Dataset)
	}
	storeID, storeTarget, version := crud.Str(r[0]), crud.Str(r[1]), toInt(r[2])
	if storeTarget != "" && storeTarget != t.code {
		return "", "", crud.Invalid("Dataset %s liegt in der Ablage %s – es geht immer in dieselbe", mf.Dataset, storeTarget)
	}
	if storeID == "" {
		storeID = cm1.NewID()
		if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET store_id = ?, store_target = ? WHERE company_code = ? AND dataset_id = ?", storeID, t.code, cc, mf.Dataset); err != nil {
			return "", "", err
		}
	}
	w, err := t.writer()
	if err != nil {
		return "", "", err
	}
	sk, err := m.serverKeys(ctx)
	if err != nil {
		return "", "", err
	}
	key, err := m.currentDatasetKey(ctx, cc, mf.Dataset, storeID)
	if err != nil {
		return "", "", err
	}
	rcpt, err := cm1.Recipient(key.public)
	if err != nil {
		return "", "", err
	}
	all := append(append([]outItem{}, mf.Items...), mf.Gone...)
	sort.Slice(all, func(i, j int) bool { return all[i].No < all[j].No })
	noOf := map[string]int{}
	for _, it := range all {
		noOf[it.Document] = it.No
	}
	idx := dsIndex{ID: storeID, Name: firstNonEmpty(mf.DatasetName, crud.Str(r[3])), Fassung: version + 1, Stand: mf.State,
		Sichtart: codeName{mf.ViewType, m.viewTypeName(ctx, cc, mf.ViewType)}, Geliefert: m.stamp(), Teile: []string{}, Modul: defaultModule}
	if from, to := dateStr(r[5]), dateStr(r[6]); from != "" || to != "" {
		idx.Zeitraum = &period{from, to}
	}
	seen := map[string]bool{}
	var used []string
	written := 0
	for _, it := range all {
		if !seen[it.Folder] {
			seen[it.Folder] = true
			idx.Gliederung = append(idx.Gliederung, idxFolder{Ordner: it.Folder, Name: firstNonEmpty(it.Folder, "(ohne Ordner)")})
		}
		e := idxElem{Nr: it.No, Ordner: it.Folder, Name: it.File, Titel: it.Title, Datum: it.Date, Groesse: it.Size, Neu: it.New}
		if it.DocTypeCode != "" {
			e.Art = &codeName{it.DocTypeCode, it.DocType}
		}
		if it.Partner != "" || it.PartnerID != "" {
			e.Partner = &idName{it.PartnerID, it.Partner}
		}
		if it.MarkCode != "" {
			e.Kennzeichen = &markJSON{Code: it.MarkCode, Name: it.Mark, Notiz: it.MarkNote, Am: it.MarkOn}
		}
		if n, ok := noOf[it.ReplacedBy]; ok && it.ReplacedBy != "" {
			e.ErsetztDurch = &n
		}
		if it.MarkCode != "" && !markHasFile(it.MarkCode) {
			idx.Elemente = append(idx.Elemente, e) // ohne Datei: nur im Verzeichnis
			continue
		}
		ref, keyName, ct, isNew, err := m.storeDocument(ctx, t, w, mf.Dataset, it, key, rcpt)
		if err != nil {
			return "", "", err
		}
		if isNew {
			written++
		}
		e.Ref, e.Key, e.ContentType = ref, keyName, ct
		used = append(used, ref)
		idx.Elemente = append(idx.Elemente, e)
	}
	if idx.Gliederung == nil {
		idx.Gliederung = []idxFolder{}
	}
	ij, err := json.Marshal(idx)
	if err != nil {
		return "", "", err
	}
	ip := "i/" + storeID
	b, err := cm1.Seal(cm1.Header{Kind: cm1.KindDataset, Path: ip, Key: key.name, ContentType: cm1.TypeDataset, Packing: cm1.PackGzip}, ij, rcpt, sk.signer, m.now())
	if err != nil {
		return "", "", err
	}
	if err := w.put(ip, b); err != nil {
		return "", "", err
	}
	if err := m.recordFile(ctx, t, ip, fileDataset, mf.Dataset, "", "", key.name, cm1.TypeDataset, "", int64(len(b))); err != nil {
		return "", "", err
	}
	// Datendateien, die kein Element mehr braucht (z. B. im DMS gelöscht): Aufbewahrung beginnt
	q := `UPDATE export__store_file SET unused_since = ? WHERE company_code = ? AND target = ? AND dataset_id = ? AND kind = ? AND unused_since IS NULL AND deleted_at IS NULL`
	args := []any{m.stamp(), cc, t.code, mf.Dataset, fileData}
	if len(used) > 0 {
		q += " AND path NOT IN (" + strings.TrimSuffix(strings.Repeat("?, ", len(used)), ", ") + ")"
		for _, u := range used {
			args = append(args, u)
		}
	}
	if _, err := m.db.Exec(ctx, q, args...); err != nil {
		return "", "", err
	}
	if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET store_version = ?, store_at = ?, store_index = ? WHERE company_code = ? AND dataset_id = ?",
		idx.Fassung, idx.Geliefert, string(ij), cc, mf.Dataset); err != nil {
		return "", "", err
	}
	where = fmt.Sprintf("Ablage %s: %s (%d Dateien neu, Index Fassung %d)", t.code, ip, written, idx.Fassung)
	return where, cm1.Hash(b), nil
}

// storeDocument: Datendatei eines Elements – vorhandene (gleiche Quelle) oder neu geschrieben.
func (m *Module) storeDocument(ctx context.Context, t *storeTgt, w storeWriter, dsID string, it outItem, key dsKeyVer, rcpt age.Recipient) (ref, keyName, ct string, isNew bool, err error) {
	r, err := m.one(ctx, `SELECT path, COALESCE(key_name, ''), COALESCE(content_type, '') FROM export__store_file WHERE company_code = ? AND target = ? AND dataset_id = ?
		AND document_id = ? AND kind = ? AND COALESCE(source_sha, '') = ? AND deleted_at IS NULL ORDER BY written_at DESC`, t.cc, t.code, dsID, it.Document, fileData, it.SHA)
	if err != nil {
		return "", "", "", false, err
	}
	if r != nil {
		if _, err := m.db.Exec(ctx, "UPDATE export__store_file SET unused_since = NULL WHERE company_code = ? AND target = ? AND path = ?", t.cc, t.code, crud.Str(r[0])); err != nil {
			return "", "", "", false, err
		}
		return crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), false, nil
	}
	resp, err := m.services.Call(ctx, "DmsDocument", "content", map[string]any{"id": it.Document})
	if err != nil {
		return "", "", "", false, fmt.Errorf("Dokument %d %s: %w", it.No, it.Title, err)
	}
	var c metamodel.Content
	if err := sdk.Decode(resp.Payload, &c); err != nil {
		return "", "", "", false, err
	}
	data, err := base64.StdEncoding.DecodeString(c.Content)
	if err != nil {
		return "", "", "", false, fmt.Errorf("Dokument %d: %w", it.No, err)
	}
	h := sha256.Sum256(data)
	if got := hex.EncodeToString(h[:]); it.SHA != "" && got != it.SHA {
		return "", "", "", false, crud.Invalid("Dokument %d %s: Prüfsumme weicht vom DMS ab – Dataset erneuern", it.No, it.Title)
	}
	ct = contentTypeOf(c.ContentType, firstNonEmpty(c.FileName, it.File))
	b, err := cm1.Seal(cm1.Header{Kind: cm1.KindData, Key: key.name, ContentType: ct, Packing: m.packingFor(ctx, t.cc, ct), Name: it.File}, data, rcpt, nil, m.now())
	if err != nil {
		return "", "", "", false, err
	}
	ref = "o/" + cm1.Hash(b)
	if err := w.put(ref, b); err != nil {
		return "", "", "", false, err
	}
	if err := m.recordFile(ctx, t, ref, fileData, dsID, it.Document, it.SHA, key.name, ct, it.File, int64(len(b))); err != nil {
		return "", "", "", false, err
	}
	return ref, key.name, ct, true, nil
}

func (m *Module) viewTypeName(ctx context.Context, cc, code string) string {
	if r, err := m.one(ctx, "SELECT name FROM export__view_type WHERE company_code = ? AND code = ?", cc, code); err == nil && r != nil {
		return crud.Str(r[0])
	}
	return code
}

// --- Startdateien und Zugriffe -----------------------------------------------------------

type grantRow struct{ role, rule, module, validTo string }

// grantsOf: Freigaben der Sicht; ohne eigene der Vorschlag der Sichtart.
func (m *Module) grantsOf(ctx context.Context, cc, viewID, today string) ([]grantRow, error) {
	res, err := m.db.Query(ctx, `SELECT role, rule, COALESCE(module, ''), valid_to FROM export__grant WHERE company_code = ? AND view_id = ? AND is_active = ?
		ORDER BY grant_no`, cc, viewID, true)
	if err != nil {
		return nil, err
	}
	var out []grantRow
	for _, r := range res.Rows {
		g := grantRow{crud.Str(r[0]), crud.Str(r[1]), firstNonEmpty(crud.Str(r[2]), defaultModule), dateStr(r[3])}
		if g.validTo == "" || g.validTo >= today {
			out = append(out, g)
		}
	}
	if len(res.Rows) > 0 {
		return out, nil
	}
	r, err := m.one(ctx, `SELECT COALESCE(t.grant_role, ''), COALESCE(t.grant_rule, '') FROM export__view v JOIN export__view_type t ON t.company_code = v.company_code
		AND t.code = v.view_type WHERE v.company_code = ? AND v.view_id = ?`, cc, viewID)
	if err != nil || r == nil || crud.Str(r[0]) == "" {
		return nil, err
	}
	return []grantRow{{role: crud.Str(r[0]), rule: firstNonEmpty(crud.Str(r[1]), ruleAll), module: defaultModule}}, nil
}

type pubDataset struct {
	id, storeID, name, view, viewName, viewType, partner, deleteAfter, storeAt, since string
	version, states                                                                   int
}

type want struct {
	role, module, until string
}

type startJSON struct {
	Fassung    int              `json:"fassung"`
	Erstellt   string           `json:"erstellt"`
	Teilnehmer map[string]any   `json:"teilnehmer"`
	Server     map[string]any   `json:"server"`
	Schluessel []map[string]any `json:"schluessel"`
	Datasets   []map[string]any `json:"datasets"`
	Modules    []string         `json:"modules"`
	Hinweise   []any            `json:"hinweise"`
}

// publish: Zugriffe nach Rollen und Freigaben bestimmen, entzogene Berechtigungen mit
// neuer Fassung des Dataset-Schlüssels beantworten, Startdateien schreiben (nur bei
// Änderung, Fassung +1). Aufrufer halten m.mu.
func (m *Module) publish(ctx context.Context, cc, code string) (string, error) {
	t, err := m.storeTarget(ctx, cc, code)
	if err != nil {
		return "", err
	}
	w, err := t.writer()
	if err != nil {
		return "", err
	}
	sk, err := m.serverKeys(ctx)
	if err != nil {
		return "", err
	}
	today, now := m.today(), m.stamp()
	// Datasets in dieser Ablage
	res, err := m.db.Query(ctx, `SELECT d.dataset_id, d.store_id, d.name, d.view_id, COALESCE(v.name, d.view_id), COALESCE(d.view_type, ''), COALESCE(d.partner_id, ''),
		d.delete_after, COALESCE(d.store_at, ''), COALESCE(r.created_at, ''), COALESCE(d.store_version, 0), COALESCE(d.state_count, 0)
		FROM export__dataset d LEFT JOIN export__view v ON v.company_code = d.company_code AND v.view_id = d.view_id
		LEFT JOIN export__run r ON r.company_code = d.company_code AND r.run_id = d.last_delivery
		WHERE d.company_code = ? AND d.store_target = ? AND COALESCE(d.store_version, 0) > 0 AND d.status <> ? ORDER BY d.dataset_id`, cc, code, dsDeleted)
	if err != nil {
		return "", err
	}
	var dss []pubDataset
	byView := map[string][]int{}
	for i, r := range res.Rows {
		dss = append(dss, pubDataset{id: crud.Str(r[0]), storeID: crud.Str(r[1]), name: crud.Str(r[2]), view: crud.Str(r[3]), viewName: crud.Str(r[4]),
			viewType: crud.Str(r[5]), partner: crud.Str(r[6]), deleteAfter: dateStr(r[7]), storeAt: crud.Str(r[8]), since: crud.Str(r[9]),
			version: toInt(r[10]), states: toInt(r[11])})
		byView[dss[i].view] = append(byView[dss[i].view], i)
	}
	grants := map[string][]grantRow{}
	for v := range byView {
		if grants[v], err = m.grantsOf(ctx, cc, v, today); err != nil {
			return "", err
		}
	}
	// Teilnehmer
	pres, err := m.db.Query(ctx, `SELECT participant_id, name, status, COALESCE(partner_id, ''), COALESCE(uid, ''), COALESCE(user_key, ''), COALESCE(user_public, ''),
		COALESCE(start_id, ''), COALESCE(start_version, 0), COALESCE(start_hash, '') FROM export__participant WHERE company_code = ? AND target = ? AND status IN (?, ?)
		ORDER BY participant_id`, cc, code, tnActive, tnLocked)
	if err != nil {
		return "", err
	}
	type part struct {
		id, name, status, partner, uid, key, public, start, hash string
		version                                                  int
		roles                                                    []map[string]any
		roleCodes                                                []string
		pin                                                      bool
		want                                                     map[string]want
		mods                                                     map[string]bool
	}
	var parts []*part
	for _, r := range pres.Rows {
		p := &part{id: crud.Str(r[0]), name: crud.Str(r[1]), status: crud.Str(r[2]), partner: crud.Str(r[3]), uid: crud.Str(r[4]), key: crud.Str(r[5]),
			public: crud.Str(r[6]), start: crud.Str(r[7]), version: toInt(r[8]), hash: crud.Str(r[9]), want: map[string]want{}}
		parts = append(parts, p)
		if p.status != tnActive {
			continue
		}
		rr, err := m.db.Query(ctx, `SELECT pr.role, COALESCE(pr.partner_id, ''), pr.valid_to, COALESCE(ro.name, pr.role), COALESCE(ro.pin_required, ?)
			FROM export__participant_role pr LEFT JOIN export__role ro ON ro.company_code = pr.company_code AND ro.code = pr.role
			WHERE pr.company_code = ? AND pr.participant_id = ? AND (pr.valid_from IS NULL OR pr.valid_from <= ?) AND (pr.valid_to IS NULL OR pr.valid_to >= ?)
			AND COALESCE(ro.is_active, ?) = ? ORDER BY pr.role_no`, false, cc, p.id, today, today, true, true)
		if err != nil {
			return "", err
		}
		for _, x := range rr.Rows {
			role, rp, roleTo := crud.Str(x[0]), firstNonEmpty(crud.Str(x[1]), p.partner), dateStr(x[2])
			p.pin = p.pin || crud.AsBool(x[4])
			rj := map[string]any{"rolle": role, "name": crud.Str(x[3])}
			if rp != "" {
				rj["bezug"] = map[string]any{"art": "PARTNER", "id": rp, "name": m.newNamer(ctx, cc).partner(rp)}
			}
			p.roles = append(p.roles, rj)
			p.roleCodes = append(p.roleCodes, role)
			for v, idxs := range byView {
				for _, g := range grants[v] {
					if g.role != role {
						continue
					}
					for _, i := range idxs {
						d := dss[i]
						if g.rule == rulePartner && (rp == "" || d.partner != rp) {
							continue
						}
						if _, ok := p.want[d.id]; ok {
							continue
						}
						p.want[d.id] = want{role: role, module: g.module, until: minDate(d.deleteAfter, g.validTo, roleTo)}
					}
				}
			}
		}
	}
	// Zugriffe abgleichen
	dsIdx := map[string]int{}
	for i, d := range dss {
		dsIdx[d.id] = i
	}
	rotate := map[string]bool{}
	granted, revoked := 0, 0
	for _, p := range parts {
		cur, err := m.db.Query(ctx, "SELECT dataset_id FROM export__access WHERE company_code = ? AND participant_id = ? AND until IS NULL", cc, p.id)
		if err != nil {
			return "", err
		}
		have := map[string]bool{}
		for _, r := range cur.Rows {
			id := crud.Str(r[0])
			have[id] = true
			if _, ok := p.want[id]; ok {
				continue
			}
			if _, err := m.db.Exec(ctx, "UPDATE export__access SET until = ? WHERE company_code = ? AND participant_id = ? AND dataset_id = ?", now, cc, p.id, id); err != nil {
				return "", err
			}
			revoked++
			if _, ok := dsIdx[id]; ok { // gelöschte Datasets brauchen keinen neuen Schlüssel
				rotate[id] = true
			}
		}
		for id, wv := range p.want {
			if have[id] {
				continue
			}
			res, err := m.db.Exec(ctx, "UPDATE export__access SET role = ?, since = ?, until = NULL WHERE company_code = ? AND participant_id = ? AND dataset_id = ?", wv.role, now, cc, p.id, id)
			if err != nil {
				return "", err
			}
			if res.RowsAffected == 0 {
				if _, err := m.db.Exec(ctx, "INSERT INTO export__access (company_code, participant_id, dataset_id, role, since) VALUES (?, ?, ?, ?, ?)", cc, p.id, id, wv.role, now); err != nil {
					return "", err
				}
			}
			granted++
		}
	}
	// Module: wer welches bekommt; wer eins verliert, bekommt es mit neuem Schlüssel nicht mehr
	mods, err := m.activeModules(ctx, cc)
	if err != nil {
		return "", err
	}
	modByID := map[string]*modRow{}
	for _, mr := range mods {
		modByID[mr.id] = mr
	}
	modRotate := map[string]bool{}
	for _, p := range parts {
		p.mods = map[string]bool{}
		if p.status == tnActive {
			for _, mr := range mods {
				if mr.gets(p.roleCodes) {
					p.mods[mr.id] = true
				}
			}
		}
		lost, err := m.syncModuleAccess(ctx, cc, p.id, p.mods, now)
		if err != nil {
			return "", err
		}
		for _, id := range lost {
			modRotate[id] = true
		}
	}
	if err := m.placeModules(ctx, t, w, mods, modRotate); err != nil {
		return "", err
	}
	// Schlüsselwechsel: Index mit der neuen Fassung neu schreiben
	for id := range rotate {
		d := &dss[dsIdx[id]]
		if err := m.reindex(ctx, t, w, sk, id, d.storeID); err != nil {
			return "", err
		}
		d.version++
	}
	// Startdateien
	server := map[string]any{"rueckkanal": map[string]any{"key": sk.boxName, "public": sk.boxPub}}
	var sigs []map[string]any
	for _, s := range sk.sigs {
		sigs = append(sigs, map[string]any{"key": s.name, "public": s.public, "gueltig_ab": s.since})
	}
	server["signatur"] = sigs
	starts, noModule := 0, 0
	for _, p := range parts {
		if p.start == "" || p.public == "" {
			continue
		}
		if p.status == tnLocked {
			if p.hash != lockedHash {
				if err := m.writeEmptyStart(ctx, cc, p.id, t, sk); err != nil {
					return "", err
				}
				starts++
			}
			continue
		}
		st := startJSON{Teilnehmer: map[string]any{"id": p.uid, "name": p.name, "rollen": orEmpty(p.roles), "pin_pflicht": p.pin}, Server: server,
			Schluessel: []map[string]any{}, Datasets: []map[string]any{}, Modules: []string{}, Hinweise: []any{}}
		ids := make([]string, 0, len(p.want))
		for id := range p.want {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, mr := range mods {
			if !p.mods[mr.id] {
				continue
			}
			sec, err := m.moduleSecret(ctx, mr.key.name)
			if err != nil {
				return "", err
			}
			st.Schluessel = append(st.Schluessel, map[string]any{"key": mr.key.name, "secret": sec, "modul": mr.id, "gueltig_ab": mr.key.since})
			st.Modules = append(st.Modules, mr.id)
		}
		for _, id := range ids {
			wv, d := p.want[id], dss[dsIdx[id]]
			ks, err := m.datasetKeys(ctx, cc, id, true)
			if err != nil {
				return "", err
			}
			var names []string
			for _, k := range ks {
				st.Schluessel = append(st.Schluessel, map[string]any{"key": k.name, "secret": k.secret, "dataset": d.storeID, "gueltig_ab": k.since})
				names = append(names, k.name)
			}
			e := map[string]any{"id": d.storeID, "name": d.name, "sichtart": codeName{d.viewType, m.viewTypeName(ctx, cc, d.viewType)},
				"sicht": idName{d.view, d.viewName}, "berechtigt_ueber": wv.role, "keys": names, "fassung": d.version, "stand": d.states,
				"geliefert": d.storeAt, "neu_seit": d.since, "modul": wv.module}
			if wv.until != "" {
				e["freigabe_bis"] = wv.until
			}
			st.Datasets = append(st.Datasets, e)
			if !p.mods[wv.module] {
				noModule++
			}
		}
		cj, _ := json.Marshal(st) // ohne Fassung und Zeitpunkt: erkennt „nichts geändert“
		h := sha256.Sum256(cj)
		hash := hex.EncodeToString(h[:])
		if hash == p.hash {
			continue
		}
		st.Fassung, st.Erstellt = p.version+1, now
		if err := m.writeStart(ctx, t, w, sk, p.start, p.key, p.public, st); err != nil {
			return "", err
		}
		if _, err := m.db.Exec(ctx, "UPDATE export__participant SET start_version = ?, start_hash = ?, start_at = ? WHERE company_code = ? AND participant_id = ?",
			st.Fassung, hash, now, cc, p.id); err != nil {
			return "", err
		}
		starts++
	}
	listNew, err := m.writeModuleList(ctx, t, w, sk, mods)
	if err != nil {
		return "", err
	}
	appNew, err := m.deployApp(ctx, t)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Ablage %s: %d Startdateien geschrieben", t.code, starts)
	if listNew {
		msg += fmt.Sprintf(", Modulliste neu (%d Module)", len(mods))
	}
	if len(modRotate) > 0 {
		msg += fmt.Sprintf(", %d Modulschlüssel erneuert", len(modRotate))
	}
	if appNew {
		msg += ", App nach " + t.appPath + " entpackt"
	}
	if noModule > 0 {
		msg += fmt.Sprintf(" – Achtung: %d Datasets zeigen ein Modul, das der Teilnehmer nicht bekommt (Module der App → Rollen)", noModule)
	}
	if granted+revoked > 0 {
		msg += fmt.Sprintf(", %d Zugriffe neu, %d entzogen", granted, revoked)
	}
	if len(rotate) > 0 {
		msg += fmt.Sprintf(", %d Dataset-Schlüssel erneuert", len(rotate))
	}
	return msg, nil
}

func orEmpty(l []map[string]any) []map[string]any {
	if l == nil {
		return []map[string]any{}
	}
	return l
}

// minDate: frühestes nicht leeres Datum.
func minDate(ds ...string) string {
	out := ""
	for _, d := range ds {
		if d != "" && (out == "" || d < out) {
			out = d
		}
	}
	return out
}

func (m *Module) writeStart(ctx context.Context, t *storeTgt, w storeWriter, sk *serverKeys, startID, keyName, public string, st startJSON) error {
	rcpt, err := cm1.Recipient(public)
	if err != nil {
		return err
	}
	j, err := json.Marshal(st)
	if err != nil {
		return err
	}
	p := "i/" + startID
	b, err := cm1.Seal(cm1.Header{Kind: cm1.KindStart, Path: p, Key: keyName, ContentType: cm1.TypeStart, Packing: cm1.PackGzip}, j, rcpt, sk.signer, m.now())
	if err != nil {
		return err
	}
	if err := w.put(p, b); err != nil {
		return err
	}
	return m.recordFile(ctx, t, p, fileStart, "", "", "", keyName, cm1.TypeStart, "", int64(len(b)))
}

// writeEmptyStart: Startdatei mit leeren Listen (gesperrt bzw. Gerät neu eingerichtet) –
// die App löscht dann alles; die Datei wird nach der Aufbewahrung gelöscht.
func (m *Module) writeEmptyStart(ctx context.Context, cc, pid string, t *storeTgt, sk *serverKeys) error {
	r, err := m.one(ctx, `SELECT name, COALESCE(uid, ''), COALESCE(user_key, ''), COALESCE(user_public, ''), COALESCE(start_id, ''), COALESCE(start_version, 0)
		FROM export__participant WHERE company_code = ? AND participant_id = ?`, cc, pid)
	if err != nil || r == nil || crud.Str(r[4]) == "" || crud.Str(r[3]) == "" {
		return err
	}
	w, err := t.writer()
	if err != nil {
		return err
	}
	st := startJSON{Fassung: toInt(r[5]) + 1, Erstellt: m.stamp(), Teilnehmer: map[string]any{"id": crud.Str(r[1]), "name": crud.Str(r[0])},
		Server: map[string]any{}, Schluessel: []map[string]any{}, Datasets: []map[string]any{}, Modules: []string{}, Hinweise: []any{}}
	if err := m.writeStart(ctx, t, w, sk, crud.Str(r[4]), crud.Str(r[2]), crud.Str(r[3]), st); err != nil {
		return err
	}
	if _, err := m.db.Exec(ctx, "UPDATE export__participant SET start_version = ?, start_hash = ?, start_at = ? WHERE company_code = ? AND participant_id = ?",
		st.Fassung, lockedHash, m.stamp(), cc, pid); err != nil {
		return err
	}
	if _, err := m.db.Exec(ctx, "UPDATE export__access SET until = ? WHERE company_code = ? AND participant_id = ? AND until IS NULL", m.stamp(), cc, pid); err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, "UPDATE export__store_file SET unused_since = ? WHERE company_code = ? AND target = ? AND path = ? AND unused_since IS NULL",
		m.stamp(), cc, t.code, "i/"+crud.Str(r[4]))
	return err
}

// reindex: letzten Index mit der neuesten Fassung des Dataset-Schlüssels neu schreiben
// (nach einem Schlüsselwechsel); die Datendateien bleiben.
func (m *Module) reindex(ctx context.Context, t *storeTgt, w storeWriter, sk *serverKeys, dsID, storeID string) error {
	r, err := m.one(ctx, "SELECT COALESCE(store_index, '') FROM export__dataset WHERE company_code = ? AND dataset_id = ?", t.cc, dsID)
	if err != nil || r == nil || crud.Str(r[0]) == "" {
		return err
	}
	var idx dsIndex
	if err := json.Unmarshal([]byte(crud.Str(r[0])), &idx); err != nil {
		return err
	}
	key, err := m.rotateDatasetKey(ctx, t.cc, dsID, storeID)
	if err != nil {
		return err
	}
	rcpt, err := cm1.Recipient(key.public)
	if err != nil {
		return err
	}
	idx.Fassung++
	ij, _ := json.Marshal(idx)
	ip := "i/" + storeID
	b, err := cm1.Seal(cm1.Header{Kind: cm1.KindDataset, Path: ip, Key: key.name, ContentType: cm1.TypeDataset, Packing: cm1.PackGzip}, ij, rcpt, sk.signer, m.now())
	if err != nil {
		return err
	}
	if err := w.put(ip, b); err != nil {
		return err
	}
	if err := m.recordFile(ctx, t, ip, fileDataset, dsID, "", "", key.name, cm1.TypeDataset, "", int64(len(b))); err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, "UPDATE export__dataset SET store_version = ?, store_index = ? WHERE company_code = ? AND dataset_id = ?", idx.Fassung, string(ij), t.cc, dsID)
	return err
}

// --- Aufräumen -------------------------------------------------------------------------------

// cleanStore: Dateien gelöschter Datasets freigeben; was länger als keep_days unbenutzt
// ist, aus der Ablage löschen.
func (m *Module) cleanStore(ctx context.Context, cc, code string) (int, error) {
	t, err := m.storeTarget(ctx, cc, code)
	if err != nil {
		return 0, err
	}
	if _, err := m.db.Exec(ctx, `UPDATE export__store_file SET unused_since = ? WHERE company_code = ? AND target = ? AND unused_since IS NULL AND deleted_at IS NULL
		AND dataset_id IN (SELECT dataset_id FROM export__dataset WHERE company_code = ? AND status = ?)`, m.stamp(), cc, code, cc, dsDeleted); err != nil {
		return 0, err
	}
	limit := m.now().UTC().Add(-time.Duration(t.keepDays) * 24 * time.Hour).Format(time.RFC3339)
	res, err := m.db.Query(ctx, "SELECT path FROM export__store_file WHERE company_code = ? AND target = ? AND deleted_at IS NULL AND unused_since IS NOT NULL AND unused_since <= ?",
		cc, code, limit)
	if err != nil || len(res.Rows) == 0 {
		return 0, err
	}
	w, err := t.writer()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res.Rows {
		p := crud.Str(r[0])
		if err := w.remove(p); err != nil {
			return n, err
		}
		if _, err := m.db.Exec(ctx, "UPDATE export__store_file SET deleted_at = ? WHERE company_code = ? AND target = ? AND path = ?", m.stamp(), cc, code, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// storeTick: offene Stände der Sichten mit Ablageziel liefern, Startdateien, Aufräumen.
func (m *Module) storeTick(ctx context.Context) {
	res, err := m.db.Query(ctx, "SELECT company_code, code FROM export__target WHERE kind = ? AND is_active = ? ORDER BY company_code, code", targetStore, true)
	if err != nil {
		m.log.Warn("Ablagen nicht gelesen", "error", err)
		return
	}
	for _, r := range res.Rows {
		cc, code := crud.Str(r[0]), crud.Str(r[1])
		open, err := m.db.Query(ctx, `SELECT d.last_run FROM export__dataset d JOIN export__view v ON v.company_code = d.company_code AND v.view_id = d.view_id
			JOIN export__run r ON r.company_code = d.company_code AND r.run_id = d.last_run
			WHERE d.company_code = ? AND v.target = ? AND d.status <> ? AND r.status = ? AND COALESCE(r.item_count, 0) > 0`, cc, code, dsDeleted, runOpen)
		if err != nil {
			m.log.Warn("Ablage: offene Stände nicht gelesen", "target", cc+"|"+code, "error", err)
			continue
		}
		for _, x := range open.Rows {
			if _, err := m.exportAction(ctx, sdk.Request{Payload: map[string]any{"id": cc + "|" + crud.Str(x[0]), "data": map[string]any{"export_target": code}}}); err != nil {
				m.log.Warn("Ablage: Stand nicht geliefert", "run", cc+"|"+crud.Str(x[0]), "error", err)
			}
		}
		if out, err := m.publish(ctx, cc, code); err != nil {
			m.log.Warn("Ablage: Startdateien nicht geschrieben", "target", cc+"|"+code, "error", err)
		} else if !strings.Contains(out, ": 0 Startdateien") {
			m.log.Info(out, "target", cc+"|"+code)
		}
		if n, err := m.cleanStore(ctx, cc, code); err != nil {
			m.log.Warn("Ablage nicht aufgeräumt", "target", cc+"|"+code, "error", err)
		} else if n > 0 {
			m.log.Info(fmt.Sprintf("Ablage %s: %d Dateien gelöscht", code, n), "target", cc+"|"+code)
		}
	}
}

// --- Aktionen am Ziel ----------------------------------------------------------------------

// targetPublishAction: ExportTarget.publish – Startdateien neu bestimmen und aufräumen.
func (m *Module) targetPublishAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, code, err := splitKey(in.ID, "Ziel")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, targetObject, "publish", cc); err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	msg, err := m.publish(ctx, cc, code)
	if err != nil {
		return sdk.Response{}, err
	}
	n, err := m.cleanStore(ctx, cc, code)
	if err != nil {
		return sdk.Response{}, err
	}
	if n > 0 {
		msg += fmt.Sprintf(", %d nicht mehr gebrauchte Dateien gelöscht", n)
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

// targetMasterAction: ExportTarget.masterKey – Hauptschlüssel zum Sichern anzeigen.
func (m *Module) targetMasterAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, _, err := splitKey(in.ID, "Ziel")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, targetObject, "masterKey", cc); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.serverKeys(ctx); err != nil { // legt Hauptschlüssel und Serverschlüssel an
		return sdk.Response{}, err
	}
	k, err := m.master(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	text := base64.StdEncoding.EncodeToString(k)
	res, err := m.db.Exec(ctx, "UPDATE export__key SET created_at = ?, note = ? WHERE key_name = ?", m.stamp(), nilIfEmpty(userOf(ctx)), "master")
	if err == nil && res.RowsAffected == 0 {
		_, err = m.db.Exec(ctx, "INSERT INTO export__key (key_name, kind, created_at, note) VALUES (?, ?, ?, ?)", "master", keyMaster, m.stamp(), nilIfEmpty(userOf(ctx)))
	}
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{
		"message": fmt.Sprintf("Hauptschlüssel der Ablage (Datei %s). Ausdrucken bzw. getrennt von der Datenbank aufbewahren – ohne ihn sind alle Schlüssel verloren und jeder Teilnehmer ist neu einzurichten.",
			firstNonEmpty(m.cfg.KeyFile, defaultKeyFile)),
		"qr":   []map[string]any{{"text": "cm-master:" + text, "caption": "Hauptschlüssel der Ablage"}},
		"copy": []map[string]any{{"label": "Hauptschlüssel", "value": text}},
	}}, nil
}

// contentTypeOf: Typ aus dem DMS, sonst nach der Endung.
func contentTypeOf(ct, name string) string {
	if ct = strings.TrimSpace(ct); ct != "" {
		return ct
	}
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(name))); t != "" {
		return strings.SplitN(t, ";", 2)[0]
	}
	return "application/octet-stream"
}

var fileKinds = []metamodel.Option{{Value: fileData, Label: "Datendatei"}, {Value: fileDataset, Label: "Index eines Datasets"},
	{Value: fileStart, Label: "Startdatei"}, {Value: fileModules, Label: "Modulliste"}}

// storeFile: Belegung der Ablage (welche Datei wozu, unbenutzt seit, gelöscht am).
func (m *Module) storeFile() *crud.Entity {
	return &crud.Entity{
		Object: storeFileObject, Title: "Dateien in der Ablage", Icon: "icon-file", Table: "export__store_file", Section: "Einstellungen", ReadOnly: true,
		Keys: []string{"company_code", "target", "path"}, Order: "company_code, target, written_at DESC", TitleField: "path",
		Filters: []string{"company_code", "target", "kind", "dataset_id"}, Search: []string{"path", "file_name", "dataset_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "target", Label: "Ablage", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogLookup(targetObject)},
			{Key: "path", Label: "Pfad", Type: tText, Listable: true, ReadOnly: true},
			{Key: "kind", Label: "Art", Type: tSel, Listable: true, ReadOnly: true, Options: fileKinds},
			{Key: "dataset_id", Label: "Dataset", Type: tText, Listable: true, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: datasetObject, ValueField: "dataset_id", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "document_id", Label: "Dokument", Type: tText, ReadOnly: true, Lookup: lookupDocument},
			{Key: "file_name", Label: "Datei", Type: tText, Listable: true, ReadOnly: true},
			{Key: "content_type", Label: "Content-Type", Type: tText, ReadOnly: true},
			{Key: "key_name", Label: "Schlüssel", Type: tText, ReadOnly: true},
			{Key: "source_sha", Label: "Prüfsumme der Quelle (SHA-256)", Type: tText, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "written_at", Label: "Geschrieben am", Type: tText, Listable: true, ReadOnly: true},
			{Key: "unused_since", Label: "Nicht mehr gebraucht seit", Short: "unbenutzt seit", Type: tText, Listable: true, ReadOnly: true},
			{Key: "deleted_at", Label: "Gelöscht am", Type: tText, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Object: targetObject, Records: true, CompanyCode: "company_code"},
	}
}
