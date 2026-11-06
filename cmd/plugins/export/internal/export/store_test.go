package export

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// device: was die App nach dem Einrichten weiß (Link) und ihr Schlüsselbund.
type device struct {
	t    *testing.T
	dir  string
	link cm1.SetupLink
	sig  map[string]ed25519.PublicKey
	ids  map[string]age.Identity
}

func newDevice(t *testing.T, dir string, payload map[string]any) *device {
	t.Helper()
	copies := payload["copy"].([]map[string]any)
	l, err := cm1.ParseSetupLink(copies[0]["value"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(copies[0]["value"].(string), "https://faust.local:8443/app/#cm1:") || l.Ablage != "https://faust.local:8443/files/ablage/" {
		t.Fatalf("Link: %v", copies[0]["value"])
	}
	pub, _ := cm1.UnB64(l.Sig.Public)
	id, err := cm1.Identity(l.Key.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return &device{t: t, dir: dir, link: l, sig: map[string]ed25519.PublicKey{l.Sig.Key: pub}, ids: map[string]age.Identity{l.Key.Name: id}}
}

func (d *device) read(p string, v any) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(d.dir, filepath.FromSlash(p)))
	if err != nil {
		return nil, err
	}
	_, data, err := cm1.Open(p, b, d.sig, d.ids)
	if err == nil && v != nil {
		err = json.Unmarshal(data, v)
	}
	return data, err
}

// start: Startdatei lesen und den Schlüsselbund danach richten (Ablauf 1.7).
func (d *device) start() startJSON {
	d.t.Helper()
	var st startJSON
	if _, err := d.read("i/"+d.link.Start, &st); err != nil {
		d.t.Fatalf("Startdatei: %v", err)
	}
	ids := map[string]age.Identity{d.link.Key.Name: d.ids[d.link.Key.Name]}
	for _, k := range st.Schluessel {
		id, err := cm1.Identity(k["secret"].(string))
		if err != nil {
			d.t.Fatal(err)
		}
		ids[k["key"].(string)] = id
	}
	d.ids = ids
	return st
}

func TestAblage(t *testing.T) {
	e := faust(t)
	dir := filepath.Join(t.TempDir(), "ablage")
	e.m.cfg.KeyFile = filepath.Join(t.TempDir(), "keys", "master.key")
	e.at("2026-01-10")
	if _, err := e.call(targetObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "APP", "name": "App", "kind": targetStore,
		"path": dir, "public_url": "http://faust.local/files/"}}); err == nil {
		t.Fatal("Ablage ohne https")
	}
	e.create(targetObject, map[string]any{"company_code": "1000", "code": "APP", "name": "Ablage der App", "kind": targetStore, "path": dir,
		"public_url": "https://faust.local:8443/files/ablage", "pwa_url": "https://faust.local:8443/app/"})
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "STB", "name": "Steuerbüro", "view_type": "STEUERBUERO", "target": "APP"})
	for _, p := range []struct{ name, partner, role string }{{"Wagner", "", "STB"}, {"Gretchen", "4712", "MIETER"}, {"Faust", "", "STB"}} {
		r := e.create(participantObject, map[string]any{"company_code": "1000", "name": p.name, "partner_id": p.partner, "target": "APP"})
		e.create(partRoleObject, map[string]any{"company_code": "1000", "participant_id": r["participant_id"], "role_no": 1, "role": p.role})
	}
	wagner := newDevice(t, dir, e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-0001"}))
	gretchen := newDevice(t, dir, e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-0002"}))
	faustD := newDevice(t, dir, e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-0003"}))
	if st := wagner.start(); len(st.Datasets) != 0 || st.Fassung != 1 || st.Teilnehmer["name"] != "Wagner" {
		t.Fatalf("vor der Lieferung: %+v", st)
	}
	// Hauptschlüssel liegt außerhalb der Datenbank, Geheimnisse nur verschlüsselt
	if fi, err := os.Stat(e.m.cfg.KeyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("Hauptschlüssel: %v", err)
	}
	if r, _ := e.m.one(e.ctx, "SELECT COUNT(*) FROM export__key WHERE secret IS NOT NULL AND secret NOT LIKE 'x1:%'"); toInt(r[0]) != 0 {
		t.Fatal("Geheimnis unverschlüsselt in der Datenbank")
	}

	// liefern: Steuerunterlagen 2025
	e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	r := e.must(runObject, "export", map[string]any{"id": "1000|EX-2026-0001", "data": map[string]any{"export_target": "APP"}})
	if !strings.Contains(r["message"].(string), "Startdateien geschrieben") {
		t.Fatalf("Lieferung: %v", r)
	}
	st := wagner.start()
	if len(st.Datasets) != 1 || st.Fassung != 2 || st.Datasets[0]["name"] != "Steuerunterlagen 2025" || st.Datasets[0]["berechtigt_ueber"] != "STB" {
		t.Fatalf("Startdatei Wagner: %+v", st.Datasets)
	}
	var idx dsIndex
	if _, err := wagner.read("i/"+st.Datasets[0]["id"].(string), &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Elemente) != 4 || idx.Fassung != 1 || idx.Sichtart.Code != "STEUERBUERO" {
		t.Fatalf("Index: %+v", idx)
	}
	for _, el := range idx.Elemente {
		data, err := wagner.read(el.Ref, nil)
		if err != nil || !strings.HasPrefix(string(data), "%PDF-1.4 D") || el.Key != "ds:"+idx.ID+":1" || el.ContentType != "application/pdf" {
			t.Fatalf("Element %d: %v %q", el.Nr, err, data)
		}
	}
	// Gretchen ist Mieterin – die Steuerunterlagen sieht sie nicht und kann sie nicht lesen
	if st := gretchen.start(); len(st.Datasets) != 0 {
		t.Fatalf("Gretchen sieht %v", st.Datasets)
	}
	if _, err := gretchen.read("i/"+idx.ID, nil); err == nil {
		t.Fatal("Gretchen liest den Index")
	}
	// ohne Änderung keine neue Startdatei
	e.must(targetObject, "publish", map[string]any{"id": "1000|APP"})
	if st := wagner.start(); st.Fassung != 2 {
		t.Fatalf("ohne Änderung Fassung %d", st.Fassung)
	}

	// Faust wird gesperrt: seine Startdatei ist leer, neuer Dataset-Schlüssel für die übrigen
	faustD.start()
	e.must(participantObject, "lock", map[string]any{"id": "1000|TN-2026-0003"})
	if st := faustD.start(); len(st.Datasets) != 0 || len(st.Schluessel) != 0 {
		t.Fatalf("gesperrt: %+v", st)
	}
	st = wagner.start()
	if keys := st.Datasets[0]["keys"].([]any); len(keys) != 2 {
		t.Fatalf("Wagner nach dem Schlüsselwechsel: %v", keys)
	}
	if _, err := faustD.read("i/"+idx.ID, nil); err == nil {
		t.Fatal("Gesperrter liest den neuen Index")
	}
	var idx2 dsIndex
	if _, err := wagner.read("i/"+idx.ID, &idx2); err != nil || idx2.Fassung != 2 {
		t.Fatalf("Index nach dem Schlüsselwechsel: %v %d", err, idx2.Fassung)
	}

	// neuer Beleg: neuer Stand, neue Datei mit der neuen Fassung; alte Dateien bleiben
	e.at("2026-02-01")
	e.addDoc("D9", "INVOICE", "2025-12-20", "4711", "Nachzügler")
	e.h.docs[len(e.h.docs)-1]["filed_at"] = "2026-01-20T09:00:00Z"
	e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	e.must(runObject, "export", map[string]any{"id": "1000|EX-2026-0002", "data": map[string]any{"export_target": "APP"}})
	wagner.start()
	var idx3 dsIndex
	wagner.read("i/"+idx.ID, &idx3)
	if len(idx3.Elemente) != 5 || idx3.Elemente[4].Key != "ds:"+idx.ID+":2" || !idx3.Elemente[4].Neu || idx3.Elemente[0].Ref != idx.Elemente[0].Ref {
		t.Fatalf("zweiter Stand: %+v", idx3.Elemente)
	}
	if n, _ := e.m.one(e.ctx, "SELECT COUNT(*) FROM export__store_file WHERE kind = ?", fileData); toInt(n[0]) != 5 {
		t.Fatalf("Datendateien: %v", n)
	}

	// Wagner richtet ein neues Gerät ein: das alte sieht nichts mehr
	old := wagner
	wagner = newDevice(t, dir, e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-0001"}))
	if st := old.start(); len(st.Datasets) != 0 {
		t.Fatal("altes Gerät sieht noch Datasets")
	}
	if st := wagner.start(); len(st.Datasets) != 1 {
		t.Fatal("neues Gerät")
	}

	// Aufräumen: nach der Aufbewahrung verschwinden die alten Startdateien
	e.at("2026-03-01")
	e.must(targetObject, "publish", map[string]any{"id": "1000|APP"})
	if _, err := os.Stat(filepath.Join(dir, "i", faustD.link.Start)); !os.IsNotExist(err) {
		t.Fatal("Startdatei des Gesperrten nicht gelöscht")
	}
	if _, err := os.Stat(filepath.Join(dir, "i", "modules")); err != nil {
		t.Fatal("Modulliste fehlt")
	}
	// Hauptschlüssel anzeigen (Sichern)
	if r := e.must(targetObject, "masterKey", map[string]any{"id": "1000|APP"}); len(r["copy"].([]map[string]any)) != 1 {
		t.Fatal("Hauptschlüssel")
	}
	// Zeitgeber: offene Stände der Sicht gehen von selbst in die Ablage
	e.addDoc("D10", "INVOICE", "2025-11-20", "4711", "Noch einer")
	e.h.docs[len(e.h.docs)-1]["filed_at"] = "2026-02-20T09:00:00Z"
	e.must(viewObject, "compose", map[string]any{"id": "1000|STB", "data": map[string]any{}})
	sys := sdk.WithCall(e.ctx, sdk.CallContext{RequestID: "timer"})
	e.m.mu.Lock()
	e.m.storeTick(sys)
	e.m.mu.Unlock()
	var idx4 dsIndex
	wagner.start()
	wagner.read("i/"+idx.ID, &idx4)
	if len(idx4.Elemente) != 6 {
		t.Fatalf("Zeitgeber hat nicht geliefert: %d Elemente", len(idx4.Elemente))
	}
}
