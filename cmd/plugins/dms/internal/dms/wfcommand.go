package dms

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Befehle (Katalog DmsCommand, Schrittart COMMAND): Programmaufrufe ohne Shell,
// je Zeile einer, Platzhalter {in} (Datei), {work} (Arbeitsverzeichnis), {lang}
// (Sprachen des Schritts, soweit installiert), {out} (neue Datei). Ergebnis:
// TEXT – die Ausgabe der letzten Zeile ist der erkannte Text; FILE – {out}
// ersetzt den Inhalt im Eingang (z. B. Scan mit Text-Ebene); VAR – die Ausgabe
// kommt in die Variable des Schritts. Der Pseudo-Befehl „@read {in}“ liest die
// Datei als Text (Textdateien).
//
// Sicherheit: Welche Programme überhaupt laufen dürfen, steht in der
// Plugin-Konfiguration (settings allowed_programs) – nicht in der Datenbank;
// den Katalog pflegt, wer DmsCommand ändern darf. Workflows wählen nur einen
// Befehl aus dem Katalog, eine freie Befehlszeile gibt es dort nicht.

const (
	cmdText = "TEXT"
	cmdFile = "FILE"
	cmdVar  = "VAR"
)

var cmdResultOptions = []metamodel.Option{
	{Value: cmdText, Label: "Erkannter Text (Ausgabe der letzten Zeile)"},
	{Value: cmdFile, Label: "Datei ersetzen ({out}, z. B. Scan mit Text-Ebene)"},
	{Value: cmdVar, Label: "Variable des Schritts (Ausgabe)"},
}

func (m *Module) command() *crud.Entity {
	return &crud.Entity{
		Object: commandObject, Customizing: true, Title: "Befehle", Icon: "icon-terminal", Table: "dms__command", Section: secSettings,
		Keys: []string{"code"}, Order: "sort_order, code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "lines", Label: "Befehlszeilen (je Zeile ein Programm; {in}, {work}, {lang}, {out}; @read {in} liest die Datei)", Type: tArea, Required: true},
			{Key: "content_types", Label: "Nur für Dateien (Komma-Liste, z. B. application/pdf, image/*; leer = alle)", Type: tText, Listable: true},
			{Key: "result", Label: "Ergebnis", Type: tSel, Required: true, Listable: true, Options: cmdResultOptions},
			{Key: "sort_order", Label: "Reihenfolge (Aktion „Text erkennen“: Befehle mit Ergebnis Text der Reihe nach)", Type: tNum, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: m.checkCommand,
	}
}

func (m *Module) checkCommand(_ context.Context, rec, _ crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	n := 0
	for _, line := range strings.Split(crud.Str(rec["lines"]), "\n") {
		args, err := splitCommand(line)
		if err != nil {
			return err
		}
		if len(args) == 0 {
			continue
		}
		n++
		if args[0] != pseudoRead && !m.programAllowed(args[0]) {
			return crud.Invalid("Programm %q ist nicht erlaubt (Einstellung allowed_programs des Plugins dms: %s)", args[0], strings.Join(m.allowedPrograms(), ", "))
		}
	}
	if n == 0 {
		return crud.Invalid("Mindestens eine Befehlszeile angeben")
	}
	if crud.Str(rec["result"]) == cmdFile && !strings.Contains(crud.Str(rec["lines"]), "{out}") {
		return crud.Invalid("Ergebnis „Datei ersetzen“: eine Zeile muss {out} schreiben")
	}
	return nil
}

// commandDef ist ein Befehl des Katalogs.
type commandDef struct {
	Code, Name, Lines, ContentTypes, Result string
}

func (m *Module) loadCommand(ctx context.Context, code string) (*commandDef, error) {
	res, err := m.db.Query(ctx, `SELECT name, lines, content_types, result, is_active FROM dms__command WHERE code = ?`, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Befehl %s gibt es nicht (Einstellungen → Befehle)", code)
	}
	r := res.Rows[0]
	if !crud.AsBool(r[4]) {
		return nil, crud.Invalid("Befehl %s ist inaktiv", code)
	}
	return &commandDef{Code: code, Name: crud.Str(r[0]), Lines: crud.Str(r[1]), ContentTypes: crud.Str(r[2]), Result: crud.Str(r[3])}, nil
}

// fits: Befehl passt zum Inhaltstyp der Datei.
func (c *commandDef) fits(contentType string) bool {
	types := splitList(c.ContentTypes)
	return len(types) == 0 || evalOp(opIn, contentType, c.ContentTypes)
}

// runCommandStep: Programmschritt Befehl.
func (m *Module) runCommandStep(ctx context.Context, d *docRow, s *stepDef) (string, string, string) {
	c, err := m.loadCommand(ctx, s.Command)
	if err != nil {
		return oFailed, "", err.Error()
	}
	if !c.fits(d.ContentType) {
		return oSkipped, "", fmt.Sprintf("Befehl %s passt nicht zur Datei (%s)", c.Code, d.ContentType)
	}
	if c.Result == cmdFile && dryRunOf(ctx) != nil {
		return oSkipped, "", "Probelauf: Datei wird nicht ersetzt"
	}
	out, note, err := m.runCatalogCommand(ctx, d, c, s.Languages)
	if err != nil {
		return oFailed, "", err.Error()
	}
	switch c.Result {
	case cmdFile:
		return oSuccess, "", "Datei ersetzt" + note
	case cmdVar:
		if s.VarName == "" {
			return oFailed, "", "Ergebnis Variable: Name der Variablen am Schritt fehlt"
		}
		v := strings.TrimSpace(out)
		if err := m.setWfVar(ctx, d.ID, "var."+s.VarName, v); err != nil {
			return oFailed, "", err.Error()
		}
		return oSuccess, v, ""
	}
	return oSuccess, "", fmt.Sprintf("%d Zeichen erkannt%s", countChars(out), note)
}

// runCatalogCommand führt den Befehl auf die Datei des Dokuments aus; Ergebnis Text
// speichert den Text, Datei ersetzt den Inhalt. Liefert die Ausgabe und einen Hinweis
// (z. B. fehlende Sprachdaten).
func (m *Module) runCatalogCommand(ctx context.Context, d *docRow, c *commandDef, wantLangs string) (string, string, error) {
	content, err := m.content(ctx, d)
	if err != nil {
		return "", "", err
	}
	dir, err := os.MkdirTemp("", "dms-cmd-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "in"+extOf(d.FileName))
	if err := os.WriteFile(in, content, 0o600); err != nil {
		return "", "", err
	}
	out := filepath.Join(dir, "out.pdf")
	langs := ""
	if strings.Contains(c.Lines, "{lang}") {
		langs = m.usableLangs(ctx, d.ID, firstOf(wantLangs, "deu+eng"))
	}
	text, err := m.runCommands(ctx, c.Lines, map[string]string{"{in}": in, "{work}": dir, "{lang}": langs, "{out}": out})
	note := ""
	if n := m.takeLangNote(d.ID); n != "" {
		note = " (" + n + ")"
	}
	if err != nil {
		return "", note, err
	}
	switch c.Result {
	case cmdFile:
		b, err := os.ReadFile(out)
		if err != nil || len(b) == 0 {
			return "", note, crud.Invalid("Befehl %s hat keine Datei {out} geschrieben", c.Code)
		}
		return "", note, m.replaceContent(ctx, d, b)
	case cmdText:
		text = strings.ToValidUTF8(text, "")
		return text, note, m.saveText(ctx, d.ID, text)
	}
	return text, note, nil
}

// recognizeAction: Aktion „Text erkennen“ – die aktiven Befehle mit Ergebnis Text,
// die zur Datei passen, der Reihe nach, bis Text da ist.
func (m *Module) recognizeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT code FROM dms__command WHERE result = ? AND is_active = ? ORDER BY sort_order, code`, cmdText, true)
	if err != nil {
		return sdk.Response{}, err
	}
	var tried []string
	var last error
	for _, r := range res.Rows {
		c, err := m.loadCommand(ctx, crud.Str(r[0]))
		if err != nil || !c.fits(d.ContentType) {
			continue
		}
		tried = append(tried, c.Code)
		text, note, err := m.runCatalogCommand(ctx, d, c, "")
		if err != nil {
			last = err
			continue
		}
		if n := countChars(text); n >= 20 {
			return sdk.Response{Payload: map[string]any{"chars": n, "message": fmt.Sprintf("%d Zeichen erkannt (%s)%s", n, c.Name, note)}}, nil
		}
	}
	if last != nil {
		return sdk.Response{}, last
	}
	if len(tried) == 0 {
		return sdk.Response{}, crud.Invalid("Kein Befehl mit Ergebnis Text passt zu %s (Einstellungen → Befehle)", d.ContentType)
	}
	return sdk.Response{Payload: map[string]any{"chars": 0, "message": "Kein Text erkannt (" + strings.Join(tried, ", ") + ")"}}, nil
}

// --- Programme ausführen ------------------------------------------------------------------

// runner führt ein Programm aus; in Tests ersetzbar.
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

// defaultPrograms: erlaubte Programme, wenn die Einstellung allowed_programs fehlt.
var defaultPrograms = []string{"pdftotext", "ocrmypdf", "tesseract"}

// pseudoRead: liest die Datei als Text (ohne Programm).
const pseudoRead = "@read"

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
		for i := range args {
			for k, v := range vars {
				args[i] = strings.ReplaceAll(args[i], k, v)
			}
		}
		n++
		if args[0] == pseudoRead {
			if len(args) != 2 {
				return "", crud.Invalid("%s: genau eine Datei angeben", pseudoRead)
			}
			if out, err = os.ReadFile(args[1]); err != nil {
				return "", err
			}
			continue
		}
		if !m.programAllowed(args[0]) {
			return "", crud.Invalid("Programm %q ist nicht erlaubt (Einstellung allowed_programs des Plugins dms: %s)",
				args[0], strings.Join(m.allowedPrograms(), ", "))
		}
		if out, err = m.run.Run(ctx, args[0], args[1:]...); err != nil {
			return "", err
		}
	}
	if n == 0 {
		return "", crud.Invalid("Befehl ohne Zeilen")
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

func (m *Module) docText(ctx context.Context, id string) string {
	return m.text(ctx, `SELECT text FROM dms__document_text WHERE document_id = ?`, id)
}

// ownScans: Dokument aus einem Eingangsverzeichnis mit „Eigene Scans“.
func (m *Module) ownScans(ctx context.Context, d *docRow) bool {
	if d.Inbox == "" || d.Status != stInbox {
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
