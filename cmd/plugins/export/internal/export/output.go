package export

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Platzhalter in Ordner- und Dateimustern.
var placeholders = map[string]bool{"lfd": true, "belegdatum": true, "jahr": true, "monat": true, "dokumentart": true,
	"kategorie": true, "partner": true, "titel": true, "ablage": true, "regel": true, "name": true}

var placeRe = regexp.MustCompile(`\{([^{}]*)\}`)

// checkPattern: nur bekannte Platzhalter, Klammern paarweise.
func checkPattern(p string) error {
	if p == "" {
		return nil
	}
	for _, mm := range placeRe.FindAllStringSubmatch(p, -1) {
		if !placeholders[mm[1]] {
			return crud.Invalid("Platzhalter {%s} gibt es nicht – %s", mm[1], patternHelp)
		}
	}
	if rest := placeRe.ReplaceAllString(p, ""); strings.ContainsAny(rest, "{}") {
		return crud.Invalid("Muster %q: Klammern { } nicht paarweise", p)
	}
	return nil
}

// namer: Bezeichnungen (Dokumentart, Kategorie, Partner) für Namen und Inhaltsverzeichnis.
type namer struct {
	ctx      context.Context
	m        *Module
	cc       string
	cache    map[string]string
	partners map[string]string
}

func (m *Module) newNamer(ctx context.Context, cc string) *namer {
	return &namer{ctx: ctx, m: m, cc: cc, cache: map[string]string{}, partners: map[string]string{}}
}

func (n *namer) label(object, id string, fields ...string) string {
	if id == "" {
		return ""
	}
	k := object + "|" + id
	if v, ok := n.cache[k]; ok {
		return v
	}
	name := id
	if resp, err := n.m.services.Call(n.ctx, object, "get", map[string]any{"id": id}); err == nil {
		var r map[string]any
		_ = sdk.Decode(resp.Payload, &r)
		var parts []string
		for _, f := range fields {
			if s := strings.TrimSpace(crud.Str(r[f])); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			name = strings.Join(parts, " ")
		}
	}
	n.cache[k] = name
	return name
}

func (n *namer) docType(code string) string  { return n.label("DmsDocType", code, "name") }
func (n *namer) category(code string) string { return n.label("DmsDocCategory", code, "name") }
func (n *namer) partner(id string) string    { return n.label("BusinessPartner", id, "name1", "name2") }

// fill setzt die Platzhalter ein; „/“ in einem Wert (z. B. „Bericht / Protokoll“) gliedert nicht.
func (n *namer) fill(p string, d doc, no int) string {
	return placeRe.ReplaceAllStringFunc(p, func(s string) string {
		return strings.ReplaceAll(n.value(s[1:len(s)-1], d, no), "/", "-")
	})
}

func (n *namer) value(key string, d doc, no int) string {
	{
		switch key {
		case "lfd":
			return fmt.Sprintf("%03d", no)
		case "belegdatum":
			return d.Date
		case "jahr":
			if len(d.Date) >= 4 {
				return d.Date[:4]
			}
		case "monat":
			if len(d.Date) >= 7 {
				return d.Date[5:7]
			}
		case "dokumentart":
			return n.docType(d.DocType)
		case "kategorie":
			return n.category(d.Category)
		case "partner":
			return n.partner(d.Partner)
		case "titel":
			return d.Title
		case "ablage":
			return d.Account
		case "regel":
			return d.ruleName
		case "name":
			return d.Name
		}
		return ""
	}
}

var badChars = regexp.MustCompile(`[\\/:*?"<>|\x00-\x1f]+`)
var spaces = regexp.MustCompile(`\s+`)
var doubleSep = regexp.MustCompile(`_{2,}`)

// clean: Teil eines Pfads für alle Dateisysteme (Windows eingeschlossen).
func clean(s string, max int) string {
	s = spaces.ReplaceAllString(badChars.ReplaceAllString(s, "-"), " ")
	s = doubleSep.ReplaceAllString(s, "_") // leere Platzhalter
	s = strings.Trim(s, " .-_")
	if r := []rune(s); len(r) > max {
		s = strings.TrimRight(string(r[:max]), " .-_")
	}
	return s
}

// place: Ordner und eindeutiger Dateiname eines Dokuments; füllt partnerName.
func (n *namer) place(filePattern string, d *doc, no int, used map[string]bool) (string, string) {
	d.partnerName = n.partner(d.Partner)
	var segs []string
	for _, s := range strings.Split(n.fill(d.folderPattern, *d, no), "/") {
		if s = clean(s, 80); s != "" {
			segs = append(segs, s)
		}
	}
	folder := strings.Join(segs, "/")
	if filePattern == "" {
		filePattern = "{lfd}_{titel}"
	}
	ext := strings.ToLower(path.Ext(d.FileName))
	if ext == "" {
		ext = strings.ToLower(path.Ext(d.Name))
	}
	if ext == "" || len(ext) > 6 {
		ext = ".pdf"
	}
	base := clean(n.fill(filePattern, *d, no), 120)
	if base == "" {
		base = fmt.Sprintf("%03d", no)
	}
	file := base + ext
	for i := 2; used[strings.ToLower(folder+"/"+file)]; i++ {
		file = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	used[strings.ToLower(folder+"/"+file)] = true
	return folder, file
}

// --- Ausgabe ---------------------------------------------------------------------------

type outItem struct {
	No       int    `json:"nr"`
	Folder   string `json:"ordner,omitempty"`
	File     string `json:"datei"`
	Date     string `json:"belegdatum,omitempty"`
	DocType  string `json:"dokumentart,omitempty"`
	Category string `json:"kategorie,omitempty"`
	Partner  string `json:"partner,omitempty"`
	Account  string `json:"ablage,omitempty"`
	Title    string `json:"titel,omitempty"`
	SHA      string `json:"sha256"`
	Size     int64  `json:"groesse"`
	Document string `json:"dokument_id"`
	Rule     int    `json:"regel,omitempty"`
	Added    bool   `json:"hinzugenommen,omitempty"`
	Reason   string `json:"bemerkung,omitempty"`
	// seit 0.3.0 (Datasets)
	New        bool   `json:"neu,omitempty"`
	Mark       string `json:"kennzeichen,omitempty"`
	ReplacedBy string `json:"ersetzt_durch,omitempty"`
	// seit 0.4.0 für den Index der Ablage (nicht im Manifest)
	DocTypeCode string `json:"-"`
	PartnerID   string `json:"-"`
	MarkCode    string `json:"-"`
	MarkNote    string `json:"-"`
	MarkOn      string `json:"-"`
}

func (o outItem) rel() string {
	if o.Folder == "" {
		return o.File
	}
	return o.Folder + "/" + o.File
}

type manifest struct {
	Run      string    `json:"lauf"`
	Name     string    `json:"name"`
	View     string    `json:"sicht"`
	ViewType string    `json:"sichtart"`
	Company  string    `json:"buchungskreis"`
	KeyDate  string    `json:"stichtag"`
	Params   any       `json:"parameter,omitempty"`
	Created  string    `json:"erstellt"`
	By       string    `json:"von,omitempty"`
	Items    []outItem `json:"dokumente"`
	Excluded []outItem `json:"ausgeschlossen,omitempty"`
	// seit 0.3.0: Dataset und Stand; Gone = gekennzeichnet ohne Datei (im DMS gelöscht …)
	Dataset     string    `json:"dataset,omitempty"`
	DatasetName string    `json:"dataset_name,omitempty"`
	State       int       `json:"stand,omitempty"`
	Gone        []outItem `json:"entfallen,omitempty"`
}

// files: Name im Ziel → Inhalt (Dokumente einzeln nachgeladen über write).
type sink interface {
	write(name string, b []byte) error
	close() error
}

type dirSink struct{ root string }

func (s *dirSink) write(name string, b []byte) error {
	p := filepath.Join(s.root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o640)
}
func (s *dirSink) close() error { return nil }

type zipSink struct {
	buf  bytes.Buffer
	zw   *zip.Writer
	stem string
	max  int64
}

func (s *zipSink) write(name string, b []byte) error {
	w, err := s.zw.Create(s.stem + "/" + name)
	if err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	if s.max > 0 && int64(s.buf.Len()) > s.max {
		return crud.Invalid("zip größer als %s – bitte ein Ziel mit Pfad (Datei auf dem Server) wählen", germanSize(s.max))
	}
	return nil
}
func (s *zipSink) close() error { return s.zw.Close() }

// csvRow: Semikolon, Anführungszeichen nur bei Bedarf (Excel, deutsch).
func csvRow(cols ...string) string {
	for i, c := range cols {
		if strings.ContainsAny(c, ";\"\n\r") {
			cols[i] = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
		}
	}
	return strings.Join(cols, ";") + "\r\n"
}

func tocCSV(mf manifest) []byte {
	var b strings.Builder
	b.WriteString("\ufeff")
	b.WriteString(csvRow("Nr", "Ordner", "Datei", "Belegdatum", "Dokumentart", "Kategorie", "Partner", "Ablage", "Titel", "Größe", "SHA-256", "Dokument", "Bemerkung",
		"Neu", "Kennzeichen"))
	neu := func(it outItem) string {
		if it.New {
			return "neu"
		}
		return ""
	}
	mark := func(it outItem) string {
		if it.ReplacedBy != "" {
			return it.Mark + " durch " + it.ReplacedBy
		}
		return it.Mark
	}
	for _, it := range mf.Items {
		note := it.Reason
		if it.Added {
			note = strings.TrimSpace("hinzugenommen " + note)
		}
		b.WriteString(csvRow(fmt.Sprint(it.No), it.Folder, it.File, germanDate(it.Date), it.DocType, it.Category, it.Partner, it.Account, it.Title,
			germanSize(it.Size), it.SHA, it.Document, note, neu(it), mark(it)))
	}
	for _, it := range mf.Gone {
		b.WriteString(csvRow(fmt.Sprint(it.No), "", "", germanDate(it.Date), it.DocType, it.Category, it.Partner, it.Account, it.Title, "", it.SHA, it.Document,
			"ohne Datei", neu(it), mark(it)))
	}
	for _, it := range mf.Excluded {
		b.WriteString(csvRow("", "", "", germanDate(it.Date), it.DocType, it.Category, it.Partner, it.Account, it.Title, "", it.SHA, it.Document,
			"ausgeschlossen: "+it.Reason, neu(it), mark(it)))
	}
	return []byte(b.String())
}

func germanDate(d string) string {
	if len(d) == 10 {
		return d[8:10] + "." + d[5:7] + "." + d[:4]
	}
	return d
}

// zipMax: größter zip-Download; darüber nur als Datei auf dem Server (gRPC-Grenze 64 MiB, base64).
const zipMax = 40 << 20

// writeRun schreibt die Dokumente des Laufs ins Ziel; liefert Pfad bzw. Download und Prüfsumme des Manifests.
func (m *Module) writeRun(ctx context.Context, mf manifest, kind, dir string) (where string, download *metamodel.Content, sum string, err error) {
	stem := clean(mf.Run+" "+mf.Name, 120)
	var s sink
	var zs *zipSink
	switch kind {
	case targetDir:
		if dir == "" {
			return "", nil, "", crud.Invalid("Ziel Verzeichnis ohne Pfad")
		}
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			return "", nil, "", crud.Invalid("Verzeichnis %s gibt es auf dem Server nicht", dir)
		}
		where = filepath.Join(dir, stem)
		if _, err := os.Stat(where); err == nil {
			return "", nil, "", crud.Invalid("%s gibt es schon – ein Lauf wird nicht überschrieben", where)
		}
		s = &dirSink{root: where}
	case targetZip:
		zs = &zipSink{stem: stem}
		zs.zw = zip.NewWriter(&zs.buf)
		if dir == "" {
			zs.max = zipMax
		} else {
			if st, err := os.Stat(dir); err != nil || !st.IsDir() {
				return "", nil, "", crud.Invalid("Verzeichnis %s gibt es auf dem Server nicht", dir)
			}
			where = filepath.Join(dir, stem+".zip")
			if _, err := os.Stat(where); err == nil {
				return "", nil, "", crud.Invalid("%s gibt es schon – ein Lauf wird nicht überschrieben", where)
			}
		}
		s = zs
	default:
		return "", nil, "", crud.Invalid("Zielart %s wird noch nicht unterstützt", kind)
	}
	var sums strings.Builder
	for _, it := range mf.Items {
		resp, err := m.services.Call(ctx, "DmsDocument", "content", map[string]any{"id": it.Document})
		if err != nil {
			return "", nil, "", fmt.Errorf("Dokument %d %s: %w", it.No, it.Title, err)
		}
		var c metamodel.Content
		if err := sdk.Decode(resp.Payload, &c); err != nil {
			return "", nil, "", err
		}
		b, err := base64.StdEncoding.DecodeString(c.Content)
		if err != nil {
			return "", nil, "", fmt.Errorf("Dokument %d: %w", it.No, err)
		}
		h := sha256.Sum256(b)
		if got := hex.EncodeToString(h[:]); it.SHA != "" && got != it.SHA {
			return "", nil, "", crud.Invalid("Dokument %d %s: Prüfsumme weicht vom DMS ab – Lauf neu zusammenstellen", it.No, it.Title)
		}
		if err := s.write(it.rel(), b); err != nil {
			return "", nil, "", err
		}
		fmt.Fprintf(&sums, "%s  %s\n", it.SHA, it.rel())
	}
	mj, _ := json.MarshalIndent(mf, "", "  ")
	h := sha256.Sum256(mj)
	sum = hex.EncodeToString(h[:])
	for _, f := range []struct {
		name string
		b    []byte
	}{{"Inhaltsverzeichnis.csv", tocCSV(mf)}, {"MANIFEST.json", mj}, {"SHA256SUMS", []byte(sums.String())}} {
		if err := s.write(f.name, f.b); err != nil {
			return "", nil, "", err
		}
	}
	if err := s.close(); err != nil {
		return "", nil, "", err
	}
	if zs != nil {
		if where != "" {
			if err := os.WriteFile(where, zs.buf.Bytes(), 0o640); err != nil {
				return "", nil, "", err
			}
		} else {
			download = &metamodel.Content{Content: base64.StdEncoding.EncodeToString(zs.buf.Bytes()), ContentType: "application/zip", FileName: stem + ".zip"}
		}
	}
	return where, download, sum, nil
}
