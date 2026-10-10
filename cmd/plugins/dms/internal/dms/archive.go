package dms

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Archive ist die Schnittstelle zu einem Archiv. Das DMS übergibt das
// Dokument mit allem, was ein Archiv zum Ablegen braucht; das Archiv liefert
// den Ort (Location), unter dem es das Dokument wiederfindet. Weitere Arten
// (z. B. ein Dokumentenserver über HTTP) implementieren dieselbe
// Schnittstelle und melden sich in openArchive an.
type Archive interface {
	// Store legt das Dokument ab und liefert den Ort im Archiv. Ein vorhandenes
	// Dokument wird nie überschrieben.
	Store(req StoreRequest) (location string, err error)
	// Load liest ein abgelegtes Dokument.
	Load(location string) ([]byte, error)
	// Remove löscht ein Dokument (nur nach Ablauf der Frist).
	Remove(location string) error
	// Check prüft, ob das Archiv erreichbar und beschreibbar ist.
	Check() error
	// Describe beschreibt das Archiv für Meldungen.
	Describe() string
}

// StoreRequest: Dokument (Inhalt als Bytes) mit Ablage, Bezug, Dokumentdatum,
// Ablagefrist, Dokumentart und Geschäftspartner.
type StoreRequest struct {
	Content     []byte
	Name        string  // Dokumentname (Dateiname)
	FileAccount string  // Nummer der Ablage
	Path        []Level // Ablage mit allen übergeordneten Ebenen (oberste zuerst)
	References  []string
	DocDate     string
	RetainUntil string // leer = dauerhaft
	DocType     string
	Partner     string
}

// Level ist eine Ebene des Ablageplans.
type Level struct{ Number, Name string }

func openArchive(kind, root string) (Archive, error) {
	switch kind {
	case "FILE", "":
		return fileArchive{root: filepath.Clean(root)}, nil
	}
	return nil, crud.Invalid("Archivart %q gibt es nicht", kind)
}

func (m *Module) archiveByCode(ctx context.Context, code string) (Archive, error) {
	res, err := m.db.Query(ctx, `SELECT kind, root, is_active FROM dms__archive WHERE code = ?`, code)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Archiv %s gibt es nicht", code)
	}
	if !crud.AsBool(res.Rows[0][2]) {
		return nil, crud.Invalid("Archiv %s ist inaktiv", code)
	}
	return openArchive(crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]))
}

// fileArchive: Verzeichnisbaum unter root – je Ebene des Ablageplans ein
// Verzeichnis „Nummer Name“, darin die Datei unter ihrem Dokumentnamen. Die
// Verzeichnisse bleiben beschreibbar, die Datei ist nach dem Ablegen nur
// lesbar (Ändern und Löschen erschwert).
type fileArchive struct{ root string }

const (
	dirMode  fs.FileMode = 0o755
	fileMode fs.FileMode = 0o444
)

func (a fileArchive) Describe() string { return a.root }

func (a fileArchive) Check() error {
	if err := os.MkdirAll(a.root, dirMode); err != nil {
		return crud.Invalid("Archiv %s: Verzeichnis nicht anlegbar: %v", a.root, err)
	}
	probe, err := os.CreateTemp(a.root, ".dms-probe-*")
	if err != nil {
		return crud.Invalid("Archiv %s ist nicht beschreibbar: %v", a.root, err)
	}
	probe.Close()
	return os.Remove(probe.Name())
}

func (a fileArchive) Store(req StoreRequest) (string, error) {
	if len(req.Content) == 0 {
		return "", crud.Invalid("Dokument %s ist leer", req.Name)
	}
	name := safeFileName(req.Name)
	if name == "" || name != req.Name {
		return "", crud.Invalid("Dokumentname %q ist als Dateiname nicht erlaubt", req.Name)
	}
	parts := []string{a.root}
	for _, l := range req.Path {
		parts = append(parts, DirName(l))
	}
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return "", fmt.Errorf("Archiv: Verzeichnis %s: %w", dir, err)
	}
	// Jedes Verzeichnis der Ablage beschreibbar (für weitere Dokumente).
	for i := 2; i <= len(parts); i++ {
		d := filepath.Join(parts[:i]...)
		if fi, err := os.Stat(d); err == nil && fi.Mode().Perm()&0o200 == 0 {
			if err := os.Chmod(d, fi.Mode().Perm()|0o700); err != nil {
				return "", fmt.Errorf("Archiv: Verzeichnis %s nicht beschreibbar: %w", d, err)
			}
		}
	}
	target := filepath.Join(dir, name)
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "", crud.Invalid("Im Archiv liegt schon ein Dokument %s", name)
	}
	if err != nil {
		return "", fmt.Errorf("Archiv: %w", err)
	}
	_, werr := f.Write(req.Content)
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(target)
		return "", fmt.Errorf("Archiv: Schreiben von %s: %w", name, err)
	}
	if err := os.Chmod(target, fileMode); err != nil {
		return "", fmt.Errorf("Archiv: nur lesbar setzen: %w", err)
	}
	back, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(back, req.Content) {
		return "", fmt.Errorf("Archiv: %s nach dem Schreiben nicht gleich gelesen (%v)", name, err)
	}
	rel, err := filepath.Rel(a.root, target)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// path: Ort im Archiv → Datei; nie außerhalb von root.
func (a fileArchive) path(location string) (string, error) {
	p := filepath.Join(a.root, filepath.FromSlash(location))
	if rel, err := filepath.Rel(a.root, p); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", crud.Invalid("Ort %q liegt nicht im Archiv", location)
	}
	return p, nil
}

func (a fileArchive) Load(location string) ([]byte, error) {
	p, err := a.path(location)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, crud.Invalid("Dokument fehlt im Archiv %s: %s", a.root, location)
	}
	return b, err
}

func (a fileArchive) Remove(location string) error {
	p, err := a.path(location)
	if err != nil {
		return err
	}
	if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// DirName: Verzeichnis einer Ebene, „Nummer Name“ (z. B. „3.0.1 Grundsteuerbescheide (Jahresbescheide)“).
func DirName(l Level) string {
	return safeFileName(strings.TrimSpace(l.Number + " " + l.Name))
}

// safeFileName: ohne Pfadtrenner und Steuerzeichen, ohne führende Punkte.
func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '/' || r == '\\' || r == 0 || r < 0x20 || r == 0x7f:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(strings.TrimSpace(b.String()), ".")
}
