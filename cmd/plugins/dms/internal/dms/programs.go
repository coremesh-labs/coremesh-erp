package dms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Programmschritte: Wert setzen, Texterkennung, Muster im Text, KI-Anfrage, Ablegen.

// runner führt ein Programm aus (Texterkennung).
type runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	path, err := lookProgram(name)
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// fallbackPath: Verzeichnisse für Programme, wenn das Plugin ohne PATH läuft
// (der Host vererbt seine Umgebung nicht; PATH kommt aus env der Plugin-Konfiguration).
var fallbackPath = []string{"/usr/local/bin", "/usr/bin", "/bin"}

// lookProgram: absoluter Pfad bleibt, sonst Suche im PATH, sonst in fallbackPath.
func lookProgram(name string) (string, error) {
	if filepath.IsAbs(name) {
		return name, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, d := range fallbackPath {
		p := filepath.Join(d, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: Programm nicht gefunden (PATH %q, gesucht auch in %s)", name, os.Getenv("PATH"), strings.Join(fallbackPath, ", "))
}

// httpDoer ruft einen KI-Dienst auf.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func defaultHTTP() httpDoer { return &http.Client{Timeout: 2 * time.Minute} }

func envLookup(k string) string { return os.Getenv(k) }

// execute führt einen Programmschritt aus: Ergebnis, gesetzter Wert, Hinweis.
func (m *Module) execute(ctx context.Context, d *docRow, s *stepDef) (outcome, value, info string) {
	fail := func(err error) (string, string, string) { return oFailed, "", err.Error() }
	switch s.Kind {
	case kSet:
		v, err := m.setTarget(ctx, d, s, m.expand(ctx, d, s.param(ctx, "value")))
		if err != nil {
			return fail(err)
		}
		return oSuccess, v, ""
	case kOCR:
		n, prepared, note, err := m.recognizeNote(ctx, d, s)
		if err != nil {
			return fail(err)
		}
		done := fmt.Sprintf("%d Zeichen erkannt", n)
		if note != "" {
			done += " (" + note + ")"
		}
		if prepared {
			done = "Scan aufbereitet (Text-Ebene eingefügt, Inhalt ersetzt), " + done
		}
		min := 20
		if v, err := strconv.Atoi(s.param(ctx, "min_chars")); err == nil {
			min = v
		}
		if n < min {
			return oFailed, "", strings.Replace(done, fmt.Sprintf("%d Zeichen", n), fmt.Sprintf("nur %d Zeichen", n), 1)
		}
		return oSuccess, "", done
	case kExtract:
		v, err := m.extract(ctx, d, s)
		if err != nil {
			return fail(err)
		}
		return oSuccess, v, ""
	case kAI:
		v, err := m.askAI(ctx, d, s)
		if err != nil {
			return fail(err)
		}
		return oSuccess, v, ""
	case kIdentify:
		info, err := m.identifyStep(ctx, d, s)
		if err != nil {
			return fail(err)
		}
		return oSuccess, "", info
	case kTakeover:
		info, err := m.takeover(ctx, d)
		if err != nil {
			return fail(err)
		}
		return oSuccess, "", info
	case kFile:
		name, warnings, err := m.fileDocument(ctx, d.ID)
		if err != nil {
			return fail(err)
		}
		return oSuccess, "", strings.Join(append([]string{"abgelegt als " + name}, warnings...), "; ")
	}
	return oFailed, "", "Art " + s.Kind + " ist kein Programmschritt"
}

// --- Texterkennung ---------------------------------------------------------------------

// Befehle der Texterkennung (Vorschlag; Parameter des Schritts überschreiben
// sie). Je Zeile ein Programmaufruf ohne Shell, Platzhalter {in} (Datei),
// {work} (Arbeitsverzeichnis), {lang} (Sprachen), {out} (aufbereitete Datei);
// der Text ist die Ausgabe der letzten Zeile.
//
//   - command: zuerst (PDF mit Text-Ebene: fertig, Datei bleibt unverändert).
//   - Weniger als min_chars Zeichen = Scan ohne Text-Ebene:
//   - Eingangsverzeichnis mit „Eigene Scans“: prepare_command (PDF) bzw.
//     prepare_image_command (Bild) schreiben {out}; das ersetzt den Inhalt
//     (PDF mit Text-Ebene), danach command auf die neue Datei.
//   - sonst (zugeschickte Scans): fallback_command nur für den Text, die Datei
//     bleibt unverändert.
//   - Bilder ohne „Eigene Scans“: image_command.
const (
	defaultCommand             = "pdftotext -layout {in} -"
	defaultFallbackCommand     = "ocrmypdf --skip-text -l {lang} {in} {work}/ocr.pdf\npdftotext -layout {work}/ocr.pdf -"
	defaultImageCommand        = "tesseract {in} - -l {lang}"
	defaultPrepareCommand      = "ocrmypdf --skip-text --output-type pdfa -l {lang} {in} {out}"
	defaultPrepareImageCommand = "ocrmypdf --image-dpi 300 --output-type pdfa -l {lang} {in} {out}"
)

// defaultPrograms: erlaubte Programme, wenn die Einstellung allowed_programs fehlt.
var defaultPrograms = []string{"pdftotext", "ocrmypdf", "tesseract"}

// recognize: Text der Datei erkennen und speichern; liefert die Zahl der
// Zeichen ohne Leerraum und ob der Inhalt aufbereitet (ersetzt) wurde.
func (m *Module) recognize(ctx context.Context, d *docRow, s *stepDef) (int, bool, error) {
	n, prepared, _, err := m.recognizeNote(ctx, d, s)
	return n, prepared, err
}

// recognizeNote wie recognize, mit Hinweis (z. B. fehlende Sprachdaten).
func (m *Module) recognizeNote(ctx context.Context, d *docRow, s *stepDef) (int, bool, string, error) {
	n, prepared, err := m.recognizeWith(ctx, d, s)
	return n, prepared, m.takeLangNote(d.ID), err
}

func (m *Module) recognizeWith(ctx context.Context, d *docRow, s *stepDef) (int, bool, error) {
	content, err := m.content(ctx, d)
	if err != nil {
		return 0, false, err
	}
	param := func(key, def string) string {
		if s == nil {
			return def
		}
		return firstOf(s.param(ctx, key), def)
	}
	langs := m.usableLangs(ctx, d.ID, param("languages", "deu+eng"))
	minChars := 20
	if v, err := strconv.Atoi(param("min_chars", "")); err == nil {
		minChars = v
	}
	dir, err := os.MkdirTemp("", "dms-ocr-*")
	if err != nil {
		return 0, false, err
	}
	defer os.RemoveAll(dir)
	ext := extOf(d.FileName)
	in := filepath.Join(dir, "in"+ext)
	if err := os.WriteFile(in, content, 0o600); err != nil {
		return 0, false, err
	}
	out := filepath.Join(dir, "out.pdf")
	vars := map[string]string{"{in}": in, "{work}": dir, "{lang}": langs, "{out}": out}
	ownScans := d.Status == stInbox && m.ownScans(ctx, d)
	// prepare: Scan aufbereiten, Inhalt ersetzen, Text aus der neuen Datei.
	prepare := func(cmd string) (string, error) {
		if _, err := m.runCommands(ctx, cmd, vars); err != nil {
			return "", err
		}
		b, err := os.ReadFile(out)
		if err != nil || len(b) == 0 {
			return "", crud.Invalid("Aufbereitung hat keine Datei {out} geschrieben")
		}
		if err := m.replaceContent(ctx, d, b); err != nil {
			return "", err
		}
		return m.runCommands(ctx, param("command", defaultCommand), map[string]string{"{in}": out, "{work}": dir, "{lang}": langs, "{out}": out})
	}
	var text string
	prepared := false
	switch {
	case strings.Contains(d.ContentType, "pdf") || ext == ".pdf":
		if text, err = m.runCommands(ctx, param("command", defaultCommand), vars); err != nil {
			return 0, false, err
		}
		if countChars(text) < minChars {
			if ownScans {
				text, err = prepare(param("prepare_command", defaultPrepareCommand))
				prepared = err == nil
			} else {
				text, err = m.runCommands(ctx, param("fallback_command", defaultFallbackCommand), vars)
			}
			if err != nil {
				return 0, false, err
			}
		}
	case strings.HasPrefix(d.ContentType, "image/"):
		if ownScans {
			text, err = prepare(param("prepare_image_command", defaultPrepareImageCommand))
			prepared = err == nil
		} else {
			text, err = m.runCommands(ctx, param("image_command", defaultImageCommand), vars)
		}
		if err != nil {
			return 0, false, err
		}
	case strings.HasPrefix(d.ContentType, "text/"):
		text = string(content)
	default:
		return 0, false, crud.Invalid("Texterkennung für %s nicht möglich", d.ContentType)
	}
	text = strings.ToValidUTF8(text, "")
	if err := m.saveText(ctx, d.ID, text); err != nil {
		return 0, false, err
	}
	return countChars(text), prepared, nil
}

// ownScans: Dokument aus einem Eingangsverzeichnis mit „Eigene Scans“.
func (m *Module) ownScans(ctx context.Context, d *docRow) bool {
	if d.Inbox == "" {
		return false
	}
	ib, err := m.loadInbox(ctx, d.CC, d.Inbox)
	return err == nil && ib.OwnScans
}

// replaceContent: aufbereitete Datei (PDF) ersetzt die Datei im Eingang;
// Prüfsumme neu, die der eingelesenen Datei bleibt (doppelte Scans).
func (m *Module) replaceContent(ctx context.Context, d *docRow, b []byte) error {
	if d.Status != stInbox || d.InboxFile == "" {
		return crud.Invalid("Nur Dokumente im Eingang lassen sich aufbereiten")
	}
	// Original bis zur Ablage aufheben – bei einer Übergabe an einen anderen
	// Eingang (z. B. zugeschickter Scan im Scanner-Eingang) kommt es zurück.
	if b, _ := original(d); b == nil {
		if err := os.MkdirAll(filepath.Dir(originalPath(d)), dirMode); err != nil {
			return err
		}
		if err := copyFile(d.InboxFile, originalPath(d)); err != nil {
			return fmt.Errorf("Eingang: Original nicht gesichert: %w", err)
		}
	}
	name := strings.TrimSuffix(filepath.Base(d.InboxFile), filepath.Ext(d.InboxFile)) + ".pdf"
	var p string
	if target := filepath.Join(filepath.Dir(d.InboxFile), name); target == d.InboxFile {
		// gleicher Name (PDF): über eine Zwischendatei ersetzen
		tmp, err := writeNew(filepath.Dir(d.InboxFile), "."+name+".neu", b)
		if err != nil {
			return err
		}
		if err := os.Rename(tmp, target); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("Eingang: %w", err)
		}
		p = target
	} else {
		var err error
		if p, err = writeNew(filepath.Dir(d.InboxFile), name, b); err != nil {
			return err
		}
	}
	sum := sha256.Sum256(b)
	fileName := strings.TrimSuffix(d.FileName, filepath.Ext(d.FileName)) + ".pdf"
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET inbox_file = ?, file_name = ?, content_type = ?, size = ?, sha256 = ? WHERE id = ?`,
		p, fileName, "application/pdf", len(b), hex.EncodeToString(sum[:]), d.ID); err != nil {
		os.Remove(p)
		return err
	}
	if p != d.InboxFile {
		if err := os.Remove(d.InboxFile); err != nil && !os.IsNotExist(err) {
			m.log.WarnContext(ctx, "Datei im Eingang nicht entfernt", "file", d.InboxFile, "err", err.Error())
		}
	}
	d.InboxFile, d.FileName, d.ContentType, d.Size = p, fileName, "application/pdf", int64(len(b))
	return nil
}

// runCommands führt die Zeilen nacheinander aus (ohne Shell) und liefert die
// Ausgabe der letzten. Nur Programme aus allowed_programs.
func (m *Module) runCommands(ctx context.Context, lines string, vars map[string]string) (string, error) {
	var out []byte
	n := 0
	for _, line := range strings.Split(lines, "\n") {
		args, err := splitCommand(line)
		if err != nil {
			return "", err
		}
		if len(args) == 0 {
			continue
		}
		if !m.programAllowed(args[0]) {
			return "", crud.Invalid("Programm %q ist nicht erlaubt (Einstellung allowed_programs des Plugins dms: %s)",
				args[0], strings.Join(m.allowedPrograms(), ", "))
		}
		for i := range args {
			for k, v := range vars {
				args[i] = strings.ReplaceAll(args[i], k, v)
			}
		}
		if out, err = m.run.Run(ctx, args[0], args[1:]...); err != nil {
			return "", err
		}
		n++
	}
	if n == 0 {
		return "", crud.Invalid("Kein Befehl für die Texterkennung")
	}
	return string(out), nil
}

func (m *Module) allowedPrograms() []string {
	if len(m.settings.AllowedPrograms) > 0 {
		return m.settings.AllowedPrograms
	}
	return defaultPrograms
}

func (m *Module) programAllowed(name string) bool {
	return contains(m.allowedPrograms(), name)
}

// splitCommand: Befehlszeile in Argumente – Leerzeichen trennen, "…" und '…' fassen zusammen.
func splitCommand(line string) ([]string, error) {
	var args []string
	var cur strings.Builder
	quote, inArg := rune(0), false
	for _, r := range strings.TrimSpace(line) {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			cur.WriteRune(r)
		case r == '"' || r == '\'':
			quote, inArg = r, true
		case r == ' ' || r == '\t':
			if inArg {
				args, inArg = append(args, cur.String()), false
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, crud.Invalid("Befehl %q: Anführungszeichen nicht geschlossen", line)
	}
	if inArg {
		args = append(args, cur.String())
	}
	return args, nil
}

func countChars(s string) int {
	n := 0
	for _, r := range s {
		if r > ' ' {
			n++
		}
	}
	return n
}

func (m *Module) saveText(ctx context.Context, id, text string) error {
	if _, err := m.db.Exec(ctx, `DELETE FROM dms__document_text WHERE document_id = ?`, id); err != nil {
		return err
	}
	_, err := m.db.Exec(ctx, `INSERT INTO dms__document_text (document_id, text) VALUES (?, ?)`, id, text)
	return err
}

func (m *Module) recognizeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	n, prepared, note, err := m.recognizeNote(ctx, d, nil)
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("%d Zeichen erkannt", n)
	if note != "" {
		msg += " (" + note + ")"
	}
	if prepared {
		msg = "Scan aufbereitet (Text-Ebene eingefügt), " + msg
	}
	return sdk.Response{Payload: map[string]any{"chars": n, "prepared": prepared, "message": msg}}, nil
}

// --- Muster im Text ------------------------------------------------------------------------

func (m *Module) extract(ctx context.Context, d *docRow, s *stepDef) (string, error) {
	pattern := s.param(ctx, "pattern")
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", crud.Invalid("Muster %q: %v", pattern, err)
	}
	text := m.docText(ctx, d.ID)
	if text == "" {
		return "", crud.Invalid("kein erkannter Text – vorher Texterkennung")
	}
	g := re.FindStringSubmatch(text)
	if g == nil {
		return "", crud.Invalid("Muster nicht gefunden")
	}
	v := g[0]
	if len(g) > 1 {
		v = g[1]
	}
	return m.setTarget(ctx, d, s, v)
}

// --- KI-Anfrage ----------------------------------------------------------------------------

// askAI fragt einen OpenAI-kompatiblen Dienst (Chat Completions). Parameter:
// prompt (je Sprache; Platzhalter wie bei Vorlagen, {text} gekürzt auf
// max_text Zeichen, {options} die erlaubten Werte aus source bzw. options),
// endpoint, model, api_key_env (Umgebungsvariable des Plugins mit dem Schlüssel).
func (m *Module) askAI(ctx context.Context, d *docRow, s *stepDef) (string, error) {
	keyEnv := firstOf(s.param(ctx, "api_key_env"), "OPENAI_API_KEY")
	key := m.getenv(keyEnv)
	if key == "" {
		return "", crud.Invalid("KI: Umgebungsvariable %s des Plugins dms ist leer (API-Schlüssel)", keyEnv)
	}
	maxText := 6000
	if v, err := strconv.Atoi(s.param(ctx, "max_text")); err == nil && v > 0 {
		maxText = v
	}
	text := []rune(m.docText(ctx, d.ID))
	if len(text) > maxText {
		text = text[:maxText]
	}
	var allowed []choice
	if s.param(ctx, "source") != "" || s.param(ctx, "options") != "" {
		cs, err := m.choices(ctx, d, s, "")
		if err != nil {
			return "", err
		}
		for _, c := range cs {
			if c.Value != "" {
				allowed = append(allowed, c)
			}
		}
	}
	var opts strings.Builder
	for _, c := range allowed {
		fmt.Fprintf(&opts, "%s\n", c.Label)
	}
	prompt := strings.ReplaceAll(s.param(ctx, "prompt"), "{text}", string(text))
	prompt = strings.ReplaceAll(prompt, "{options}", opts.String())
	prompt = m.expand(ctx, d, prompt)
	body, _ := json.Marshal(map[string]any{
		"model": firstOf(s.param(ctx, "model"), "gpt-4o-mini"), "temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": "Antworte nur mit dem gesuchten Wert, ohne Erklärung. Bei einer Auswahl nur mit dem Schlüssel vor dem Gedankenstrich."},
			{"role": "user", "content": prompt},
		},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, firstOf(s.param(ctx, "endpoint"), "https://api.openai.com/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("KI: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("KI: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return "", fmt.Errorf("KI: Antwort nicht lesbar")
	}
	answer := strings.Trim(strings.TrimSpace(out.Choices[0].Message.Content), "\"'`. ")
	if v, _, ok := strings.Cut(answer, " – "); ok {
		answer = strings.TrimSpace(v)
	}
	if len(allowed) > 0 && !containsChoice(allowed, answer) {
		return "", crud.Invalid("KI-Antwort %q steht nicht zur Auswahl", answer)
	}
	return m.setTarget(ctx, d, s, answer)
}

// originalPath: Original eines aufbereiteten Dokuments bis zur Ablage
// (<Eingang>/.original/<id>__<Dateiname>; versteckt, „Einlesen“ übergeht es).
// Vor der Aufbereitung aufgerufen: FileName ist noch der eingelesene Name.
func originalPath(d *docRow) string {
	return filepath.Join(filepath.Dir(d.InboxFile), ".original", d.ID+"__"+safeFileName(d.FileName))
}

// original: aufgehobenes Original (Inhalt, Dateiname) oder nil.
func original(d *docRow) ([]byte, string) {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(d.InboxFile), ".original", d.ID+"__*"))
	if len(matches) == 0 {
		return nil, ""
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, ""
	}
	return b, strings.TrimPrefix(filepath.Base(matches[0]), d.ID+"__")
}

// dropOriginal: Original nach Ablage oder Übergabe entfernen.
func dropOriginal(d *docRow) {
	if d.InboxFile == "" {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(d.InboxFile), ".original", d.ID+"__*"))
	for _, p := range matches {
		os.Remove(p)
	}
}

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o640)
}

// --- Sprachdaten der Texterkennung ------------------------------------------------------

// installedLangs: Sprachen von Tesseract (tesseract --list-langs, bei jedem
// Lauf – nachinstallierte Sprachdaten gelten sofort). nil = unbekannt
// (Programm fehlt oder nicht erlaubt) – dann bleibt die Vorgabe.
func (m *Module) installedLangs(ctx context.Context) []string {
	if !m.programAllowed("tesseract") {
		return nil
	}
	out, err := m.run.Run(ctx, "tesseract", "--list-langs")
	if err != nil {
		return nil
	}
	var langs []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.Contains(l, " ") || l == "osd" {
			continue // Kopfzeile „List of available languages …“, Skripterkennung
		}
		langs = append(langs, l)
	}
	return langs
}

// usableLangs: gewünschte Sprachen, soweit installiert (fehlende im Hinweis);
// keine installiert: eng bzw. die erste vorhandene – Text gibt es trotzdem.
func (m *Module) usableLangs(ctx context.Context, docID, wanted string) string {
	have := m.installedLangs(ctx)
	if len(have) == 0 {
		return wanted
	}
	var use, missing []string
	for _, l := range strings.Split(wanted, "+") {
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		if contains(have, l) {
			use = append(use, l)
		} else {
			missing = append(missing, l)
		}
	}
	if len(use) == 0 {
		use = []string{have[0]}
		if contains(have, "eng") {
			use = []string{"eng"}
		}
	}
	if len(missing) > 0 {
		m.langMu.Lock()
		if m.langNotes == nil {
			m.langNotes = map[string]string{}
		}
		m.langNotes[docID] = "Sprachdaten " + strings.Join(missing, ", ") + " fehlen – erkannt mit " + strings.Join(use, "+") +
			" (installieren z. B. tesseract-ocr-" + missing[0] + ")"
		m.langMu.Unlock()
	}
	return strings.Join(use, "+")
}

func (m *Module) takeLangNote(docID string) string {
	m.langMu.Lock()
	defer m.langMu.Unlock()
	n := m.langNotes[docID]
	delete(m.langNotes, docID)
	return n
}
