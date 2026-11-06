package export

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/coremesh-labs/coremesh-erp/pkg/cm1"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/qrcode"
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
	if err == nil && strings.HasPrefix(p, "i/") && strings.Contains(string(data), ":null") {
		d.t.Errorf("%s enthält null (Spezifikation: leere Felder weglassen): %s", p, data)
	}
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

	// Module: der mitgelieferte File Explorer, verschlüsselt mit eigenem Schlüssel, für alle
	var ml struct {
		Fassung int              `json:"fassung"`
		Module  []map[string]any `json:"module"`
	}
	if _, err := wagner.read("i/modules", &ml); err != nil || len(ml.Module) != 2 || ml.Module[0]["id"] != "explorer" || ml.Module[1]["id"] != "vorgang" {
		t.Fatalf("Modulliste: %v %+v", err, ml)
	}
	modRef := ml.Module[0]["ref"].(string)
	// Vorgänge: mitgeliefert für Mieter, Handwerker, Beirat, Eigentümer – das Steuerbüro hat den Schlüssel nicht
	if st := wagner.start(); hasKey(st, "mod:1000.vorgang:1") || len(st.Modules) != 1 {
		t.Fatalf("Vorgänge ohne Rolle: %+v", st.Modules)
	}
	if st := gretchen.start(); len(st.Modules) != 2 || !hasKey(st, "mod:1000.explorer:1") || !hasKey(st, "mod:1000.vorgang:1") {
		t.Fatalf("Gretchen mit Explorer und Vorgängen: %+v", st.Modules)
	}
	if pkg, err := wagner.read(modRef, nil); err != nil || sha256Hex(pkg) != sha256Hex(bundledExplorer) {
		t.Fatalf("Modulpaket: %v", err)
	}
	// nur noch für das Steuerbüro: Gretchen verliert es, neuer Schlüssel, neues Paket
	e.must(moduleObject, "update", map[string]any{"id": "1000|explorer", "data": map[string]any{"all_roles": false}})
	e.create(moduleRoleObject, map[string]any{"company_code": "1000", "module_id": "explorer", "role": "STB"})
	if r := e.must(targetObject, "publish", map[string]any{"id": "1000|APP"}); !strings.Contains(r["message"].(string), "1 Modulschlüssel erneuert") {
		t.Fatalf("Modul entzogen: %v", r["message"])
	}
	if st := gretchen.start(); len(st.Modules) != 1 || st.Modules[0] != "vorgang" || hasKey(st, "mod:1000.explorer:2") {
		t.Fatalf("Gretchen hat noch Module: %+v", st.Modules)
	}
	wagner.start()
	wagner.read("i/modules", &ml)
	if ml.Fassung != 2 || ml.Module[0]["ref"] == modRef || ml.Module[0]["key"] != "mod:1000.explorer:2" {
		t.Fatalf("Modulliste nach dem Entzug: %+v", ml)
	}
	if _, err := gretchen.read(ml.Module[0]["ref"].(string), nil); err == nil {
		t.Fatal("Gretchen liest das neue Paket")
	}
	if _, err := wagner.read(ml.Module[0]["ref"].(string), nil); err != nil {
		t.Fatalf("Wagner: %v", err)
	}
	// Paket hochladen: nur zum passenden Modul
	up := map[string]any{"upload_file": base64.StdEncoding.EncodeToString(bundledExplorer)}
	if _, err := e.call(moduleObject, "upload", map[string]any{"id": "1000|vorgang", "data": up}); err == nil {
		t.Fatal("fremdes Paket angenommen")
	}
	e.must(moduleObject, "upload", map[string]any{"id": "1000|explorer", "data": up})
	if r, _ := e.m.one(e.ctx, "SELECT origin FROM export__module WHERE company_code = ? AND module_id = ?", "1000", "explorer"); r[0] != originUpload {
		t.Fatalf("Herkunft: %v", r)
	}
	// App ins App-Verzeichnis des Ziels
	appDir := filepath.Join(t.TempDir(), "app")
	e.must(targetObject, "update", map[string]any{"id": "1000|APP", "data": map[string]any{"app_path": appDir}})
	if r := e.must(targetObject, "publish", map[string]any{"id": "1000|APP"}); !strings.Contains(r["message"].(string), "App nach") {
		t.Fatalf("App: %v", r["message"])
	}
	if _, err := os.Stat(filepath.Join(appDir, "pkg", "pwa", "pwa_bg.wasm")); err != nil {
		t.Fatal(err)
	}
	if r := e.must(targetObject, "publish", map[string]any{"id": "1000|APP"}); strings.Contains(r["message"].(string), "App nach") {
		t.Fatal("App ohne Änderung neu entpackt")
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

func hasKey(st startJSON, name string) bool {
	for _, k := range st.Schluessel {
		if k["key"] == name {
			return true
		}
	}
	return false
}

// TestPruefablage schreibt eine Prüfablage aus dem Testbestand (Faust) zum Ausprobieren
// mit der App, nur mit EXPORT_PRUEF_DIR (Verzeichnis der Ablage), EXPORT_PRUEF_URL (Adresse
// der Ablage), EXPORT_PRUEF_APP (Adresse der App) und optional EXPORT_PRUEF_APPDIR
// (Verzeichnis der App). Die Einrichtungslinks stehen danach in <dir>/einrichtungslinks.txt.
func TestPruefablage(t *testing.T) {
	dir, url, app := os.Getenv("EXPORT_PRUEF_DIR"), os.Getenv("EXPORT_PRUEF_URL"), os.Getenv("EXPORT_PRUEF_APP")
	if dir == "" || url == "" || app == "" {
		t.Skip("EXPORT_PRUEF_DIR, EXPORT_PRUEF_URL, EXPORT_PRUEF_APP nicht gesetzt")
	}
	e := faust(t)
	e.m.cfg.KeyFile = filepath.Join(t.TempDir(), "master.key")
	e.at("2026-01-10")
	// Gretchen: Mieterin mit Verbrauchsinformationen (Energie-Monitor, Vormonat)
	e.h.partners["4713"] = "Margarete (Gretchen)"
	e.addDoc("V1", "CONSUMPTION_INFO", "2025-12-31", "4713", "Verbrauchsinformation Dezember 2025")
	e.h.docs[len(e.h.docs)-1]["filed_at"] = "2026-01-05T10:00:00Z"
	// … und ein Vorgang, an dem sie beteiligt ist (Modul vorgang, Objektdaten process/1)
	e.h.partners["4714"] = "Valentin Sanitär"
	e.addDoc("P1", "LETTER", "2026-01-06", "4713", "Schadenmeldung Versicherung", "Process|1000|SM-2026-0001")
	e.h.processes["1000|SM-2026-0001"] = map[string]any{"process_id": "SM-2026-0001", "subject": "Wasserschaden Küche Whg 16, Leitung hinter der Spüle",
		"partner_id": "4713", "changed_at": "2026-01-08T13:30:00Z",
		"participants": []any{map[string]any{"rolle": "TENANT", "partner_id": "4713"}, map[string]any{"rolle": "CRAFTSMAN", "partner_id": "4714"}},
		"vorgang": map[string]any{"kurz": "Wasserschaden Küche", "art": map[string]any{"code": "SCHADEN", "name": "Schadensmeldung / Reparatur"},
			"status": map[string]any{"code": "WAIT_CRAFT", "name": "wartet auf Handwerker", "klasse": "WARTET"}, "ampel": "GELB",
			"wartet_auf": "Termin Valentin Sanitär", "naechster_schritt": "Ausführung kontrollieren",
			"beschreibung_md": "Am **5. Januar** Wasser unter der Spüle gemeldet. Die Wand hinter der Spüle ist feucht.",
			"zustaendig":      map[string]any{"name": "Mephisto"},
			"daten":           map[string]any{"eroeffnet": "2026-01-05", "stichtag": "2026-01-05", "naechste_frist": "2026-01-12"},
			"bezuege":         []any{map[string]any{"art": "PARTNER", "id": "4713", "name": "Margarete (Gretchen)"}},
			"beteiligte": []any{map[string]any{"rolle": map[string]any{"code": "TENANT", "name": "Mieter"}, "partner": map[string]any{"id": "4713", "name": "Margarete (Gretchen)"}, "ich": true},
				map[string]any{"rolle": map[string]any{"code": "CRAFTSMAN", "name": "Handwerker"}, "partner": map[string]any{"id": "4714", "name": "Valentin Sanitär"}}},
			"schritte": []any{map[string]any{"nr": 10, "name": "Schaden aufnehmen", "status": "ERLEDIGT", "erledigt": "2026-01-05", "pflicht": true},
				map[string]any{"nr": 20, "name": "Handwerker beauftragen", "status": "ERLEDIGT", "erledigt": "2026-01-06", "pflicht": true},
				map[string]any{"nr": 30, "name": "Termin mit dem Mieter abstimmen", "status": "OFFEN", "faellig": "2026-01-12", "pflicht": true},
				map[string]any{"nr": 60, "name": "Versicherung melden", "status": "ERLEDIGT", "erledigt": "2026-01-06"}},
			"texte": []any{map[string]any{"kategorie": map[string]any{"code": "INQUIRY", "name": "Anfrage"}, "position": 1,
				"inhalt_md": "Bitte um einen Termin **vormittags**.", "geaendert": "2026-01-07"}},
			"verlauf": []any{map[string]any{"nr": 1, "zeit": "2026-01-05T08:02:00Z", "art": map[string]any{"code": "SYSTEM", "name": "Systemeintrag", "system": true}, "text": "Vorgang eröffnet"},
				map[string]any{"nr": 4, "zeit": "2026-01-08T13:30:00Z", "art": map[string]any{"code": "MEETING", "name": "Termin / Besuch"}, "text": "Feuchte Wand bestätigt",
					"von": map[string]any{"name": "Mephisto"}}},
			"teilvorgaenge": []any{map[string]any{"nummer": "AF-2026-0001", "kurz": "Angebot Trocknung", "betreff": "Angebot Trocknung",
				"status": map[string]any{"code": "NEW", "name": "neu", "klasse": "OFFEN"}, "ampel": "GRUEN", "daten": map[string]any{"naechste_frist": "2026-01-15"}}}}}
	e.h.events = append(e.h.events, map[string]any{"seq": 1, "process_id": "SM-2026-0001", "event": "CREATED"})
	realPDFs(e)
	e.create(targetObject, map[string]any{"company_code": "1000", "code": "PRUEF", "name": "Prüfablage", "kind": targetStore, "path": dir,
		"public_url": url, "pwa_url": app, "app_path": os.Getenv("EXPORT_PRUEF_APPDIR"), "keep_days": 0})
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "STB", "name": "Steuerbüro", "view_type": "STEUERBUERO", "target": "PRUEF"})
	var links []string
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "EM", "name": "Energie-Monitor", "view_type": "VERBRAUCHSINFO", "target": "PRUEF"})
	for _, p := range []struct{ name, partner, role string }{{"Wagner", "", "STB"}, {"Gretchen", "4713", "MIETER"}} {
		r := e.create(participantObject, map[string]any{"company_code": "1000", "name": p.name, "partner_id": p.partner, "target": "PRUEF"})
		e.create(partRoleObject, map[string]any{"company_code": "1000", "participant_id": r["participant_id"], "role_no": 1, "role": p.role})
	}
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "VG", "name": "Vorgänge für Mieter", "view_type": "VORGANG", "target": "PRUEF"})
	for _, v := range []string{"1000|STB", "1000|EM", "1000|VG"} {
		e.must(viewObject, "compose", map[string]any{"id": v, "data": map[string]any{}})
	}
	sys := sdk.WithCall(e.ctx, sdk.CallContext{RequestID: "timer"})
	e.m.mu.Lock()
	e.m.storeTick(sys) // offene Stände beider Sichten in die Ablage
	e.m.mu.Unlock()
	var cards []string
	for i, p := range []struct{ file, name string }{{"wagner", "Wagner (Steuerbüro)"}, {"gretchen", "Gretchen (Mieterin)"}} {
		out := e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-000" + string(rune('1'+i))})
		link := out["copy"].([]map[string]any)[0]["value"].(string)
		links = append(links, p.name+":\n"+link+"\n")
		c, err := qrcode.Encode(link, qrcode.M)
		if err != nil {
			t.Fatal(err)
		}
		svg := c.SVG(4)
		if err := os.WriteFile(filepath.Join(dir, p.file+".svg"), []byte(svg), 0o644); err != nil {
			t.Fatal(err)
		}
		cards = append(cards, fmt.Sprintf("<div class=\"qr\"><h2>%s</h2>%s<p><a href=\"%s\">Link öffnen</a></p></div>", html.EscapeString(p.name), svg, html.EscapeString(link)))
	}
	if err := os.WriteFile(filepath.Join(dir, "einrichtungslinks.txt"), []byte(strings.Join(links, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	page := `<!doctype html><html lang="de"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Prüfablage – Einrichtung</title><style>body{font-family:system-ui,sans-serif;margin:16px;background:#fff;color:#111}
.qrs{display:flex;flex-wrap:wrap;gap:24px}.qr{width:min(360px,100%)}.qr svg{width:100%;height:auto;background:#fff}h2{font-size:1.1rem}</style></head><body>
<h1>Prüfablage – Einrichtungslinks</h1><p>Testbestand (Faust), nur zum Ausprobieren. Mit dem Handy scannen; jeder Code richtet die App für eine Person ein.</p>
<div class="qrs">` + strings.Join(cards, "\n") + `</div></body></html>`
	if err := os.WriteFile(filepath.Join(dir, "einrichtung.html"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log(strings.Join(links, "\n"))
}

// realPDFs: die Attrappen des Testbestands durch lesbare PDFs ersetzen (eine Seite mit
// Titel, Art, Datum und Partner), damit die App sie anzeigen kann.
func realPDFs(e *env) {
	for _, d := range e.h.docs {
		id := fmt.Sprint(d["id"])
		lines := []string{fmt.Sprint(d["title"]), "Art: " + fmt.Sprint(d["doc_type"]), "Datum: " + fmt.Sprint(d["doc_date"])}
		if p := fmt.Sprint(d["partner_id"]); p != "" && p != "<nil>" {
			lines = append(lines, "Partner: "+firstNonEmpty(e.h.partners[p], p))
		}
		lines = append(lines, "Testbestand (Faust) - Dokument "+id)
		body := minimalPDF(lines)
		e.h.content[id] = body
		d["size"], d["sha256"] = float64(len(body)), sha256Hex(body)
	}
}

// minimalPDF: PDF 1.4 mit einer A4-Seite Text (Helvetica; Umlaute werden umschrieben).
func minimalPDF(lines []string) []byte {
	esc := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`, "ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss", "–", "-")
	var text strings.Builder
	text.WriteString("BT /F1 18 Tf 72 770 Td 24 TL\n")
	for i, l := range lines {
		if i == 1 {
			text.WriteString("/F1 12 Tf\n")
		}
		fmt.Fprintf(&text, "(%s) Tj T*\n", esc.Replace(l))
	}
	text.WriteString("ET")
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", text.Len(), text.String()),
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offs := make([]int, len(objs))
	for i, o := range objs {
		offs[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

// TestAblageVorgang (seit 0.6.0): Vorgang als Objektdaten in der Ablage – Element process+json
// (verschlüsselt, Modul vorgang), Verweise auf die Datendateien der Dokumente, Bezugsregel
// BETEILIGTE (Mieterin am Vorgang sieht ihn, am anderen nicht; fällt sie heraus: Entzug).
func TestAblageVorgang(t *testing.T) {
	e := faust(t)
	dir := filepath.Join(t.TempDir(), "ablage")
	e.m.cfg.KeyFile = filepath.Join(t.TempDir(), "keys", "master.key")
	e.at("2026-11-06")
	e.create(targetObject, map[string]any{"company_code": "1000", "code": "APP", "name": "Ablage der App", "kind": targetStore, "path": dir,
		"public_url": "https://faust.local:8443/files/ablage", "pwa_url": "https://faust.local:8443/app/"})
	e.h.processes["1000|SM-2026-0001"] = map[string]any{"process_id": "SM-2026-0001", "subject": "Wasserschaden Küche", "partner_id": "4712",
		"participants": []any{map[string]any{"rolle": "TENANT", "partner_id": "4712"}, map[string]any{"rolle": "CRAFTSMAN", "partner_id": "4711"}}}
	e.h.processes["1000|SM-2026-0002"] = map[string]any{"process_id": "SM-2026-0002", "subject": "Keller Auerbach", "partner_id": "4711",
		"participants": []any{map[string]any{"rolle": "TENANT", "partner_id": "4711"}}}
	e.h.events = append(e.h.events, map[string]any{"seq": 1, "process_id": "SM-2026-0001", "event": "CREATED"},
		map[string]any{"seq": 2, "process_id": "SM-2026-0002", "event": "CREATED"})
	e.addDoc("P1", "INVOICE", "2026-11-02", "4711", "Trocknung", "Process|1000|SM-2026-0001")
	r := e.create(participantObject, map[string]any{"company_code": "1000", "name": "Gretchen", "partner_id": "4712", "target": "APP"})
	e.create(partRoleObject, map[string]any{"company_code": "1000", "participant_id": r["participant_id"], "role_no": 1, "role": "MIETER"})
	gretchen := newDevice(t, dir, e.must(participantObject, "setup", map[string]any{"id": "1000|TN-2026-0001"}))
	e.create(viewObject, map[string]any{"company_code": "1000", "view_id": "VG", "name": "Vorgänge für Mieter", "view_type": "VORGANG", "target": "APP"})
	e.must(viewObject, "compose", map[string]any{"id": "1000|VG", "data": map[string]any{}})
	e.m.storeTick(e.ctx)
	st := gretchen.start()
	if len(st.Datasets) != 1 || st.Datasets[0]["name"] != "Vorgang SM-2026-0001" || st.Datasets[0]["modul"] != "vorgang" || st.Datasets[0]["berechtigt_ueber"] != "MIETER" {
		t.Fatalf("Gretchen (beteiligt nur an SM-2026-0001): %+v", st.Datasets)
	}
	var idx dsIndex
	if _, err := gretchen.read("i/"+st.Datasets[0]["id"].(string), &idx); err != nil {
		t.Fatal(err)
	}
	if idx.Modul != "vorgang" || len(idx.Elemente) != 2 {
		t.Fatalf("Index: %+v", idx)
	}
	var pdf, obj idxElem
	for _, el := range idx.Elemente {
		if el.ContentType == "application/pdf" {
			pdf = el
		} else {
			obj = el
		}
	}
	if obj.ContentType != "application/vnd.coremesh.process+json;v=1" || obj.Name != "vorgang.json" || obj.Art == nil || obj.Art.Code != "VORGANG" || obj.Ordner != "" {
		t.Fatalf("Element der Objektdaten: %+v", obj)
	}
	var v struct {
		Schema  string
		Vorgang struct {
			Nummer    string
			Dokumente []struct {
				Ref, Titel  string
				ContentType string `json:"content_type"`
			}
		}
	}
	if _, err := gretchen.read(obj.Ref, &v); err != nil {
		t.Fatal(err)
	}
	if v.Schema != "process/1" || v.Vorgang.Nummer != "SM-2026-0001" || len(v.Vorgang.Dokumente) != 1 || v.Vorgang.Dokumente[0].Ref != pdf.Ref ||
		v.Vorgang.Dokumente[0].ContentType != "application/pdf" {
		t.Fatalf("Abbild: %+v (PDF %s)", v, pdf.Ref)
	}
	// unverändert geliefert: dieselbe Datei
	e.h.events = append(e.h.events, map[string]any{"seq": 3, "process_id": "SM-2026-0001", "event": "VIEWED"})
	e.m.tick(e.ctx)
	e.m.storeTick(e.ctx)
	st = gretchen.start()
	var idx2 dsIndex
	if _, err := gretchen.read("i/"+st.Datasets[0]["id"].(string), &idx2); err != nil || len(idx2.Elemente) != 2 {
		t.Fatalf("ohne Änderung: %v %+v", err, idx2)
	}
	// Gretchen fällt heraus (nur noch Handwerker): Entzug, neuer Dataset-Schlüssel
	e.h.processes["1000|SM-2026-0001"]["participants"] = []any{map[string]any{"rolle": "CRAFTSMAN", "partner_id": "4711"}}
	e.h.processes["1000|SM-2026-0001"]["partner_id"] = "4711"
	if r := e.must(targetObject, "publish", map[string]any{"id": "1000|APP"}); !strings.Contains(r["message"].(string), "1 entzogen") ||
		!strings.Contains(r["message"].(string), "1 Dataset-Schlüssel erneuert") {
		t.Fatalf("Entzug: %v", r["message"])
	}
	if st := gretchen.start(); len(st.Datasets) != 0 {
		t.Fatalf("Gretchen sieht noch %+v", st.Datasets)
	}
}
