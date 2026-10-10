package dms

import (
	"bytes"
	"context"
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
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %v %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
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
		n, err := m.recognize(ctx, d, s)
		if err != nil {
			return fail(err)
		}
		min := 20
		if v, err := strconv.Atoi(s.param(ctx, "min_chars")); err == nil {
			min = v
		}
		if n < min {
			return oFailed, "", fmt.Sprintf("nur %d Zeichen erkannt", n)
		}
		return oSuccess, "", fmt.Sprintf("%d Zeichen erkannt", n)
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
// {work} (Arbeitsverzeichnis), {lang} (Sprachen); der Text ist die Ausgabe
// der letzten Zeile. command: zuerst; fallback_command: wenn command weniger
// als min_chars Zeichen liefert (Scan ohne Textebene); image_command: Bilder.
const (
	defaultCommand         = "pdftotext -layout {in} -"
	defaultFallbackCommand = "ocrmypdf --skip-text -l {lang} {in} {work}/ocr.pdf\npdftotext -layout {work}/ocr.pdf -"
	defaultImageCommand    = "tesseract {in} - -l {lang}"
)

// defaultPrograms: erlaubte Programme, wenn die Einstellung allowed_programs fehlt.
var defaultPrograms = []string{"pdftotext", "ocrmypdf", "tesseract"}

// recognize: Text der Datei erkennen und speichern; liefert die Zahl der
// Zeichen ohne Leerraum. PDF: command, reicht das nicht, fallback_command;
// Bilder: image_command; Text: unverändert.
func (m *Module) recognize(ctx context.Context, d *docRow, s *stepDef) (int, error) {
	content, err := m.content(ctx, d)
	if err != nil {
		return 0, err
	}
	param := func(key, def string) string {
		if s == nil {
			return def
		}
		return firstOf(s.param(ctx, key), def)
	}
	langs := param("languages", "deu+eng")
	minChars := 20
	if v, err := strconv.Atoi(param("min_chars", "")); err == nil {
		minChars = v
	}
	dir, err := os.MkdirTemp("", "dms-ocr-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	ext := extOf(d.FileName)
	in := filepath.Join(dir, "in"+ext)
	if err := os.WriteFile(in, content, 0o600); err != nil {
		return 0, err
	}
	vars := map[string]string{"{in}": in, "{work}": dir, "{lang}": langs}
	var text string
	switch {
	case strings.Contains(d.ContentType, "pdf") || ext == ".pdf":
		if text, err = m.runCommands(ctx, param("command", defaultCommand), vars); err != nil {
			return 0, err
		}
		if countChars(text) < minChars {
			if text, err = m.runCommands(ctx, param("fallback_command", defaultFallbackCommand), vars); err != nil {
				return 0, err
			}
		}
	case strings.HasPrefix(d.ContentType, "image/"):
		if text, err = m.runCommands(ctx, param("image_command", defaultImageCommand), vars); err != nil {
			return 0, err
		}
	case strings.HasPrefix(d.ContentType, "text/"):
		text = string(content)
	default:
		return 0, crud.Invalid("Texterkennung für %s nicht möglich", d.ContentType)
	}
	text = strings.ToValidUTF8(text, "")
	if err := m.saveText(ctx, d.ID, text); err != nil {
		return 0, err
	}
	return countChars(text), nil
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
	n, err := m.recognize(ctx, d, nil)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"chars": n, "message": fmt.Sprintf("%d Zeichen erkannt", n)}}, nil
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
		cs, err := m.choices(ctx, d, s)
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
