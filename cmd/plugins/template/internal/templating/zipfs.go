package templating

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Grenzen für Bundles (Schutz vor zu großen oder präparierten Zip-Dateien).
const (
	maxFileSize   = 50 << 20  // je Datei
	maxBundleSize = 200 << 20 // alle Dateien entpackt
	maxFiles      = 5000
)

// zipFile ist eine Datei eines Bundles (Name mit / getrennt, relativ).
type zipFile struct {
	Name string
	Data []byte
}

// cleanName: relativer, sicherer Name (kein absoluter Pfad, kein .., keine
// versteckten Dateien wie __MACOSX/ oder .DS_Store); "" = überspringen.
func cleanName(name string) (string, error) {
	n := strings.ReplaceAll(name, "\\", "/")
	if strings.HasSuffix(n, "/") {
		return "", nil // Verzeichnis
	}
	if strings.HasPrefix(n, "/") || strings.Contains(n, ":") {
		return "", crud.Invalid("Datei %q: absoluter Pfad", name)
	}
	n = path.Clean(n)
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", crud.Invalid("Datei %q: Pfad mit ..", name)
		}
		if strings.HasPrefix(part, ".") || part == "__MACOSX" {
			return "", nil
		}
	}
	return n, nil
}

// readZip liest die Dateien einer Zip-Datei, nach Namen sortiert.
func readZip(b []byte) ([]zipFile, error) {
	if len(b) == 0 {
		return nil, nil
	}
	r, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, crud.Invalid("keine gültige Zip-Datei: %v", err)
	}
	var out []zipFile
	var total int64
	for _, f := range r.File {
		name, err := cleanName(f.Name)
		if err != nil {
			return nil, err
		}
		if name == "" || f.FileInfo().IsDir() {
			continue
		}
		if f.UncompressedSize64 > maxFileSize {
			return nil, crud.Invalid("Datei %s: größer als %d MB", name, maxFileSize>>20)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, crud.Invalid("Datei %s: %v", name, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxFileSize+1))
		rc.Close()
		if err != nil {
			return nil, crud.Invalid("Datei %s: %v", name, err)
		}
		if len(data) > maxFileSize {
			return nil, crud.Invalid("Datei %s: größer als %d MB", name, maxFileSize>>20)
		}
		total += int64(len(data))
		if total > maxBundleSize || len(out) >= maxFiles {
			return nil, crud.Invalid("Bundle zu groß (höchstens %d MB, %d Dateien)", maxBundleSize>>20, maxFiles)
		}
		out = append(out, zipFile{Name: name, Data: data})
	}
	sortFiles(out)
	return out, nil
}

func sortFiles(fs []zipFile) { sort.Slice(fs, func(i, j int) bool { return fs[i].Name < fs[j].Name }) }

// writeZip schreibt die Dateien als Zip (sortiert, feste Zeit für gleichen Inhalt).
func writeZip(files []zipFile) ([]byte, error) {
	sortFiles(files)
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		fw, err := w.CreateHeader(&zip.FileHeader{Name: f.Name, Method: zip.Deflate, Modified: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// mergeFiles: Dateien aus add ersetzen gleichnamige in old, neue kommen hinzu.
func mergeFiles(old, add []zipFile) []zipFile {
	byName := map[string]int{}
	out := append([]zipFile(nil), old...)
	for i, f := range out {
		byName[f.Name] = i
	}
	for _, f := range add {
		if i, ok := byName[f.Name]; ok {
			out[i] = f
		} else {
			byName[f.Name] = len(out)
			out = append(out, f)
		}
	}
	sortFiles(out)
	return out
}

// --- Verzeichnisse auf dem Server --------------------------------------------------------

// allowedDir: absoluter Pfad unter einer der freigegebenen Wurzeln (settings.allowed_dirs).
func (m *Module) allowedDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", crud.Invalid("Verzeichnis fehlt")
	}
	if len(m.settings.roots) == 0 {
		return "", crud.Invalid("keine Verzeichnisse freigegeben (Konfiguration: settings.modules.template.allowed_dirs)")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", crud.Invalid("Verzeichnis %q: %v", dir, err)
	}
	abs = filepath.Clean(abs)
	real := abs
	if r, err := evalExisting(abs); err == nil {
		real = r
	}
	for _, root := range m.settings.roots {
		rr := root
		if r, err := evalExisting(root); err == nil {
			rr = r
		}
		if rel, err := filepath.Rel(rr, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return abs, nil
		}
	}
	return "", crud.Invalid("Verzeichnis %s liegt nicht unter den freigegebenen Verzeichnissen (%s)", abs, strings.Join(m.settings.roots, ", "))
}

// evalExisting: Symlinks des längsten vorhandenen Teils auflösen (das Ziel darf noch fehlen).
func evalExisting(p string) (string, error) {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", fmt.Errorf("kein vorhandener Teil")
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// exportFiles schreibt die Dateien in das Verzeichnis (vorhandene gleichen Namens werden überschrieben).
func exportFiles(dir string, files []zipFile) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return crud.Invalid("Verzeichnis %s: %v", dir, err)
	}
	for _, f := range files {
		name, err := cleanName(f.Name)
		if err != nil || name == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return crud.Invalid("Verzeichnis %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return crud.Invalid("Datei %s: %v", p, err)
		}
	}
	return nil
}

// importFiles liest alle Dateien des Verzeichnisses (mit Unterverzeichnissen,
// ohne versteckte).
func importFiles(dir string) ([]zipFile, error) {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return nil, crud.Invalid("Verzeichnis %s gibt es nicht", dir)
	}
	var out []zipFile
	var total int64
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p != dir && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileSize {
			return crud.Invalid("Datei %s: größer als %d MB", rel, maxFileSize>>20)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxBundleSize || len(out) >= maxFiles {
			return crud.Invalid("Verzeichnis zu groß (höchstens %d MB, %d Dateien)", maxBundleSize>>20, maxFiles)
		}
		out = append(out, zipFile{Name: filepath.ToSlash(rel), Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortFiles(out)
	return out, nil
}
