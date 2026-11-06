package cm1

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

// Prüfdateien (Spezifikation 7): testdata/ablage/… ist eine kleine Ablage mit
// festen Schlüsseln (testdata/keys.json) und erwarteten Ergebnissen
// (testdata/expect.json). Neu erzeugen: CM1_UPDATE=1 go test ./pkg/cm1/
// Die PWA prüft sich gegen dieselben Dateien.

type testKeys struct {
	Note   string            `json:"hinweis"`
	Age    map[string]string `json:"age"`    // Schlüsselname → AGE-SECRET-KEY-1…
	Public map[string]string `json:"public"` // Schlüsselname → age1…
	Sig    map[string]struct {
		Seed   string `json:"seed"`
		Public string `json:"public"`
	} `json:"sig"`
}

type expectation struct {
	Path        string `json:"pfad"`
	Valid       bool   `json:"gueltig"`
	Error       string `json:"fehler,omitempty"`
	ContentSHA  string `json:"inhalt_sha256,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	// Prüfungen der Anwendung: erwarteter Content-Type (aus dem Index), kleinste Fassung
	WantType   string `json:"erwartet_content_type,omitempty"`
	MinVersion int    `json:"fassung_mindestens,omitempty"`
	Note       string `json:"fall"`
}

func TestRoundTrip(t *testing.T) {
	sec, pub, _ := NewAgeKey()
	r, _ := Recipient(pub)
	id, _ := Identity(sec)
	spub, spriv, _ := ed25519.GenerateKey(nil)
	signer := &Signer{Name: "srv-sig:1", Key: spriv}
	data := []byte(strings.Repeat("Faust und Gretchen ", 100))
	b, err := Seal(Header{Kind: KindDataset, Path: "i/x", Key: "ds:x:1", ContentType: TypeDataset, Packing: PackGzip}, data, r, signer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	h, got, err := Open("i/x", b, map[string]ed25519.PublicKey{"srv-sig:1": spub}, map[string]age.Identity{"ds:x:1": id})
	if err != nil || string(got) != string(data) || h.Size != int64(len(data)) {
		t.Fatalf("Open: %v", err)
	}
	if _, _, err := Open("i/y", b, map[string]ed25519.PublicKey{"srv-sig:1": spub}, nil); !errors.Is(err, ErrPath) {
		t.Fatalf("Pfad: %v", err)
	}
	link := SetupLink{Ablage: "https://a/", Start: "s", Name: "Gretchen", Key: LinkKey{"user:u:1", sec}, Sig: LinkSigner{"srv-sig:1", B64(spub)}}
	l2, err := ParseSetupLink(link.URL("https://pwa/"))
	if err != nil || l2.Key.Secret != sec || l2.V != 1 {
		t.Fatalf("Link: %v", err)
	}
	if len(NewID()) != 26 {
		t.Fatal("ID")
	}
}

func TestVectors(t *testing.T) {
	dir := "testdata"
	if os.Getenv("CM1_UPDATE") == "1" {
		writeVectors(t, dir)
	}
	var keys testKeys
	readJSON(t, filepath.Join(dir, "keys.json"), &keys)
	var exp []expectation
	readJSON(t, filepath.Join(dir, "expect.json"), &exp)
	ids := map[string]age.Identity{}
	for n, s := range keys.Age {
		id, err := Identity(s)
		if err != nil {
			t.Fatal(err)
		}
		ids[n] = id
	}
	sig := map[string]ed25519.PublicKey{}
	for n, s := range keys.Sig {
		p, _ := UnB64(s.Public)
		sig[n] = p
	}
	for _, e := range exp {
		b, err := os.ReadFile(filepath.Join(dir, "ablage", e.Path))
		if err != nil {
			t.Fatal(err)
		}
		h, data, err := Open(e.Path, b, sig, ids)
		if err == nil && e.WantType != "" && h.ContentType != e.WantType {
			err = ErrType
		}
		if err == nil && e.MinVersion > 0 {
			var v struct {
				Fassung int `json:"fassung"`
			}
			json.Unmarshal(data, &v)
			if v.Fassung < e.MinVersion {
				err = ErrOld
			}
		}
		code := ""
		if err != nil {
			code = strings.SplitN(err.Error(), ":", 2)[0]
		}
		if e.Valid != (err == nil) || code != e.Error {
			t.Errorf("%s (%s): Fehler %q, erwartet %q", e.Path, e.Note, code, e.Error)
			continue
		}
		if e.Valid && (Hash(data) != e.ContentSHA || h.ContentType != e.ContentType) {
			t.Errorf("%s: Inhalt bzw. Typ abweichend", e.Path)
		}
	}
}

func readJSON(t *testing.T, p string, v any) {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func writeVectors(t *testing.T, dir string) {
	os.RemoveAll(filepath.Join(dir, "ablage"))
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	keys := testKeys{Note: "Nur zum Testen – diese Schlüssel sind öffentlich.", Age: map[string]string{}, Public: map[string]string{}}
	keys.Sig = map[string]struct {
		Seed   string `json:"seed"`
		Public string `json:"public"`
	}{}
	rcpt := map[string]age.Recipient{}
	for _, n := range []string{"user:gretchen:1", "ds:steuer2026:1", "ds:steuer2026:2", "ds:fremd:1"} {
		s, p, _ := NewAgeKey()
		if n != "ds:fremd:1" {
			keys.Age[n], keys.Public[n] = s, p
		}
		rcpt[n], _ = Recipient(p)
	}
	signers := map[string]*Signer{}
	for i, n := range []string{"srv-sig:1", "srv-sig:2"} {
		seed := make([]byte, ed25519.SeedSize)
		seed[0] = byte(i + 1)
		k := ed25519.NewKeyFromSeed(seed)
		signers[n] = &Signer{Name: n, Key: k}
		keys.Sig[n] = struct {
			Seed   string `json:"seed"`
			Public string `json:"public"`
		}{B64(seed), B64(k.Public().(ed25519.PublicKey))}
	}
	var exp []expectation
	put := func(path string, b []byte, e expectation) {
		p := filepath.Join(dir, "ablage", filepath.FromSlash(path))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		e.Path = path
		exp = append(exp, e)
	}
	seal := func(h Header, data []byte, key string, s *Signer) []byte {
		h.Key = key
		var r age.Recipient
		if key != "" {
			r = rcpt[key]
		}
		b, err := Seal(h, data, r, s, now)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ok := func(note string, data []byte, ct string) expectation {
		return expectation{Valid: true, ContentSHA: Hash(data), ContentType: ct, Note: note}
	}
	bad := func(note string, code error) expectation {
		return expectation{Error: code.Error(), Note: note}
	}
	// Datendateien
	pdf := []byte("%PDF-1.4\n% Rechnung Stadtwerke Auerbach – Prüfdatei\n%%EOF\n")
	b := seal(Header{Kind: KindData, ContentType: "application/pdf", Packing: PackNone, Name: "2026-03-01_Stadtwerke.pdf"}, pdf, "ds:steuer2026:1", nil)
	pdfRef := "o/" + Hash(b)
	put(pdfRef, b, ok("Datendatei PDF, none, Fassung 1", pdf, "application/pdf"))
	js := []byte(`{"id":"AF-2026-0001","titel":"Leckwasser Whg. 16","ereignisse":[{"datum":"2026-10-01T09:00:00Z","text":"Meldung von Gretchen"}]}`)
	b = seal(Header{Kind: KindData, ContentType: TypeProcess, Packing: PackGzip, Name: "AF-2026-0001.json"}, js, "ds:steuer2026:2", nil)
	jsRef := "o/" + Hash(b)
	put(jsRef, b, ok("Datendatei JSON, gzip, Fassung 2", js, TypeProcess))
	// Index
	idx := []byte(`{"id":"steuer2026","name":"Steuerunterlagen 2026","fassung":2,"stand":2,"elemente":[{"nr":1,"key":"ds:steuer2026:1","ref":"` + pdfRef + `"},{"nr":2,"key":"ds:steuer2026:2","ref":"` + jsRef + `"}]}`)
	b = seal(Header{Kind: KindDataset, Path: "i/steuer2026", ContentType: TypeDataset, Packing: PackGzip}, idx, "ds:steuer2026:2", signers["srv-sig:1"])
	put("i/steuer2026", b, ok("Dataset-Index, signiert", idx, TypeDataset))
	put("i/anderer-pfad", b, bad("Index unter fremdem Pfad", ErrPath))
	// Startdatei, neuer Signaturschlüssel
	sig2 := keys.Sig["srv-sig:2"]
	sigOfSig2 := ed25519.Sign(signers["srv-sig:1"].Key, SigText("srv-sig:2", sig2.Public, "2027-01-01"))
	start := []byte(`{"fassung":2,"teilnehmer":{"id":"gretchen","name":"Gretchen"},"server":{"signatur":[{"key":"srv-sig:1","public":"` + keys.Sig["srv-sig:1"].Public +
		`","gueltig_ab":"2026-11-06"},{"key":"srv-sig:2","public":"` + sig2.Public + `","gueltig_ab":"2027-01-01","signiert_von":"srv-sig:1","signatur":"` + B64(sigOfSig2) +
		`"}]},"schluessel":[{"key":"ds:steuer2026:1","secret":"` + keys.Age["ds:steuer2026:1"] + `","dataset":"steuer2026"},{"key":"ds:steuer2026:2","secret":"` +
		keys.Age["ds:steuer2026:2"] + `","dataset":"steuer2026"}],"datasets":[{"id":"steuer2026","name":"Steuerunterlagen 2026","keys":["ds:steuer2026:1","ds:steuer2026:2"],"fassung":2,"modul":"explorer"}],"modules":["explorer"]}`)
	b = seal(Header{Kind: KindStart, Path: "i/gretchen-start", ContentType: TypeStart, Packing: PackGzip}, start, "user:gretchen:1", signers["srv-sig:1"])
	e := ok("Startdatei mit zweitem Signaturschlüssel (signiert_von srv-sig:1)", start, TypeStart)
	e.MinVersion = 2
	put("i/gretchen-start", b, e)
	e = bad("ältere Startdatei (Fassung 1, gespeichert ist 2)", ErrOld)
	e.MinVersion = 2
	put("i/gretchen-start-alt", seal(Header{Kind: KindStart, Path: "i/gretchen-start-alt", ContentType: TypeStart, Packing: PackGzip}, []byte(`{"fassung":1}`), "user:gretchen:1", signers["srv-sig:1"]), e)
	gesperrt := []byte(`{"fassung":3,"teilnehmer":{"id":"gretchen","name":"Gretchen"},"schluessel":[],"datasets":[],"modules":[]}`)
	put("i/gretchen-gesperrt", seal(Header{Kind: KindStart, Path: "i/gretchen-gesperrt", ContentType: TypeStart, Packing: PackGzip}, gesperrt, "user:gretchen:1", signers["srv-sig:2"]),
		ok("gesperrt: leere Listen, signiert mit srv-sig:2", gesperrt, TypeStart))
	// Modulliste (unverschlüsselt, signiert)
	mods := []byte(`{"fassung":1,"module":[{"id":"explorer","name":"Dateien","version":"1.0.0","ref":"","content_types":["application/vnd.coremesh.dataset+json"],"sichtarten":["*"],"rueckkanal":[]}]}`)
	put("i/modules", seal(Header{Kind: KindModules, Path: "i/modules", ContentType: TypeModules, Packing: PackNone}, mods, "", signers["srv-sig:1"]), ok("Modulliste", mods, TypeModules))
	// Fehlerfälle
	put("o/"+strings.Repeat("0", 64), seal(Header{Kind: KindData, ContentType: "application/pdf", Packing: PackNone}, pdf, "ds:steuer2026:1", nil), bad("falscher Hash", ErrHash))
	sb := seal(Header{Kind: KindDataset, Path: "i/falsche-signatur", ContentType: TypeDataset, Packing: PackGzip}, idx, "ds:steuer2026:2", signers["srv-sig:1"])
	sb[len(sb)-70] ^= 1 // ein Bit im Inhalt
	put("i/falsche-signatur", sb, bad("Inhalt nach dem Signieren verändert", ErrSig))
	fb := seal(Header{Kind: KindData, ContentType: "application/pdf", Packing: PackNone}, pdf, "ds:fremd:1", nil)
	put("o/"+Hash(fb), fb, bad("Schlüssel nicht im Bund", ErrKey))
	tb := seal(Header{Kind: KindData, ContentType: "application/pdf", Packing: PackNone}, pdf, "ds:steuer2026:1", nil)
	tb = tb[:len(tb)-10]
	put("o/"+Hash(tb), tb, bad("abgeschnitten", ErrFormat))
	xb := append(seal(Header{Kind: KindData, ContentType: "application/pdf", Packing: PackNone}, pdf, "ds:steuer2026:1", nil), 0)
	put("o/"+Hash(xb), xb, bad("Bytes nach dem Inhalt", ErrFormat))
	yb := seal(Header{Kind: KindData, ContentType: "text/plain", Packing: PackNone}, pdf, "ds:steuer2026:1", nil)
	e = bad("Content-Type anders als im Index", ErrType)
	e.WantType = "application/pdf"
	put("o/"+Hash(yb), yb, e)

	sort.SliceStable(exp, func(i, j int) bool { return exp[i].Path < exp[j].Path })
	for name, v := range map[string]any{"keys.json": keys, "expect.json": exp} {
		b, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name), append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
