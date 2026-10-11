package templating

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/cmd/plugins/template/internal/tectonic"
)

// PDF: Das Template liefert LaTeX; Tectonic (XeTeX: UTF-8, Systemschriften)
// übersetzt es in einem temporären Verzeichnis mit den Dateien des Bundles mit
// Binärdateien. Tectonic lädt fehlende TeX-Pakete beim ersten Bedarf aus dem
// Netz in den Cache (settings.cache_dir); scheitert das, wird der Lauf
// wiederholt (settings.retries). Bei Fehlern – mit Debug immer – kommen
// Quelltext, Protokoll und Hilfsdateien an den Auftrag.

// texRunner führt Tectonic aus (in Tests ersetzbar).
type texRunner func(ctx context.Context, dir, bin string, args, env []string) ([]byte, error)

func execTex(ctx context.Context, dir, bin string, args, env []string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	return cmd.CombinedOutput()
}

// netErrRe: Ausgabe von Tectonic, wenn Pakete nicht geladen werden konnten.
var netErrRe = regexp.MustCompile(`(?i)(failed to download|download failed|error sending request|network|connection (refused|reset|closed)|timed? ?out|dns|failed to resolve|tls|certificate)`)

func tectonicInfo() string {
	if v := tectonic.Version(); v != "" {
		return "eingebettet " + v
	}
	return "nicht eingebettet (tectonic_path oder Suchpfad)"
}

// tectonicBin: Programm – settings.tectonic_path, sonst eingebettet (einmal
// in ein temporäres Verzeichnis entpackt), sonst tectonic im Suchpfad.
func (m *Module) tectonicBin() (string, error) {
	m.tecOnce.Do(func() {
		if p := strings.TrimSpace(m.settings.TectonicPath); p != "" {
			m.tecPath = p
			return
		}
		if gz, name := tectonic.Binary(); gz != nil {
			dir, err := os.MkdirTemp("", "coremesh-tectonic-")
			if err != nil {
				m.tecErr = err
				return
			}
			m.tecTmp = dir
			zr, err := gzip.NewReader(bytes.NewReader(gz))
			if err != nil {
				m.tecErr = fmt.Errorf("eingebettetes Tectonic: %w", err)
				return
			}
			p := filepath.Join(dir, name)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if err == nil {
				_, err = io.Copy(f, zr)
				if cerr := f.Close(); err == nil {
					err = cerr
				}
			}
			if err != nil {
				m.tecErr = fmt.Errorf("Tectonic entpacken: %w", err)
				return
			}
			m.tecPath = p
			return
		}
		p, err := exec.LookPath("tectonic")
		if err != nil {
			m.tecErr = errors.New("Tectonic fehlt: nicht eingebettet (tools/fetch-tectonic.sh vor dem Bauen), kein tectonic_path, nicht im Suchpfad")
			return
		}
		m.tecPath = p
	})
	return m.tecPath, m.tecErr
}

// pdfResult: PDF und angehängte Dateien.
type pdfResult struct {
	PDF   []byte
	Files []zipFile
}

// renderPDF übersetzt den LaTeX-Quelltext; binaries sind die Dateien des Bundles mit Binärdateien.
func (m *Module) renderPDF(ctx context.Context, tex string, binaries []zipFile, debug bool) (*pdfResult, error) {
	bin, err := m.tectonicBin()
	if err != nil {
		return &pdfResult{Files: []zipFile{{Name: "main.tex", Data: []byte(tex)}}}, err
	}
	dir, err := os.MkdirTemp("", "coremesh-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	own := map[string]bool{}
	if err := exportFiles(dir, binaries); err != nil {
		return nil, err
	}
	for _, f := range binaries {
		own[f.Name] = true
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tex"), []byte(tex), 0o644); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(m.settings.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("Cache %s: %w", m.settings.CacheDir, err)
	}
	env := []string{"TECTONIC_CACHE_DIR=" + m.settings.CacheDir, "HOME=" + dir}
	for _, k := range []string{"PATH", "SSL_CERT_FILE", "SSL_CERT_DIR", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	args := []string{"--keep-logs", "--keep-intermediates", "--untrusted", "--chatter", "minimal", "main.tex"}

	m.texMu.Lock()
	var out []byte
	var runErr error
	var log bytes.Buffer
	for attempt := 1; attempt <= m.settings.Retries; attempt++ {
		rctx, cancel := context.WithTimeout(ctx, m.settings.timeout)
		out, runErr = m.tex(rctx, dir, bin, args, env)
		cancel()
		fmt.Fprintf(&log, "--- Versuch %d (%s) ---\n%s\n", attempt, time.Now().Format(time.RFC3339), out)
		if runErr == nil || !netErrRe.Match(out) || attempt == m.settings.Retries {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(m.settings.retryWait):
		}
	}
	m.texMu.Unlock()

	res := &pdfResult{}
	pdf, perr := os.ReadFile(filepath.Join(dir, "main.pdf"))
	failed := runErr != nil || perr != nil || len(pdf) == 0
	if failed || debug {
		res.Files = append(res.Files, zipFile{Name: "tectonic.txt", Data: log.Bytes()})
		entries, _ := os.ReadDir(dir)
		var names []string
		for _, e := range entries {
			if !e.IsDir() && !own[e.Name()] && e.Name() != "main.pdf" {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for _, n := range names {
			if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
				res.Files = append(res.Files, zipFile{Name: n, Data: b})
			}
		}
	}
	if failed {
		msg := lastLines(string(out), 6)
		if runErr != nil {
			return res, fmt.Errorf("Tectonic: %v – %s", runErr, msg)
		}
		return res, fmt.Errorf("Tectonic: kein PDF – %s", msg)
	}
	res.PDF = pdf
	return res, nil
}

// lastLines: die letzten n nicht leeren Zeilen (Fehlermeldung von TeX).
func lastLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
