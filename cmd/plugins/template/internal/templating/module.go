// Package templating ist das Vorlagen-Modul des ERP (Modul template).
//
//   - Bundles (TemplateBundle): Zip-Dateien, Art TEMPLATE (Go-Templates; Kette
//     über parent vom konkreten zum allgemeinen Bundle, zusammengebaut in der
//     anderen Richtung – Blöcke des allgemeinen überschreibt das konkrete) oder
//     BINARY (Bilder u. a. für PDF). Hochladen (ergänzt), Herunterladen,
//     in ein Verzeichnis entpacken, aus einem Verzeichnis einlesen, kopieren.
//   - Vorlagen (TemplateDefinition) je Code und Sprache, für alle Buchungskreise:
//     Ausgabeart (HTML, XML, CSV, PDF), Template-Bundle, für PDF ein Bundle mit
//     Binärdateien, Debug.
//   - Aufträge (TemplateJob, TemplateJobFile): Template.render legt den Auftrag
//     an (Nummer, Eingang als YAML), rechnet im Hintergrund und meldet den
//     Status als SystemEvent (TemplateJob.status); Template.get liefert das
//     Ergebnis. Nach der Frist des Buchungskreises (TemplateCompany, Erfolg und
//     Fehler getrennt) werden Aufträge gelöscht, außer „Nicht löschen“.
//
// Für HTML gilt html/template mit den .html-Dateien der Bundles, für XML
// text/template mit den .xml-Dateien, für CSV und PDF text/template mit den
// .text-Dateien (PDF: LaTeX, übersetzt mit Tectonic/XeTeX). Andere Dateien
// eines Template-Bundles werden ignoriert; die Reihenfolge ist die der Namen.
package templating

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Name ist der Namensraum des Moduls (/m/template).
const Name = "template"

const (
	bundleObject  = "TemplateBundle"
	defObject     = "TemplateDefinition"
	companyObject = "TemplateCompany"
	jobObject     = "TemplateJob"
	jobFileObject = "TemplateJobFile"
	// serviceObject: Schnittstelle für andere Plugins (render, get), ohne Oberfläche.
	serviceObject = "Template"
	setupObject   = "TemplateSetup"
)

// Arten der Bundles.
const (
	kindTemplate = "TEMPLATE"
	kindBinary   = "BINARY"
)

// Ausgabearten.
const (
	targetHTML = "HTML"
	targetXML  = "XML"
	targetCSV  = "CSV"
	targetPDF  = "PDF"
)

// Status der Aufträge.
const (
	stNew     = "NEW"
	stRunning = "RUNNING"
	stDone    = "DONE"
	stError   = "ERROR"
)

// Module sind die Vorlagen.
type Module struct {
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
	settings settings
	// tex führt Tectonic aus; in Tests ersetzbar.
	tex texRunner
	// now: Uhr (Tests).
	now func() time.Time

	tecOnce sync.Once
	tecPath string
	tecTmp  string
	tecErr  error
	texMu   sync.Mutex // ein Tectonic-Lauf zur Zeit (gemeinsamer Paket-Cache)

	cleanMu   sync.Mutex
	lastClean time.Time
	// wg: laufende Aufträge (Tests warten, Shutdown).
	wg sync.WaitGroup
}

// settings: settings.modules.template der Plugin-Konfiguration – nicht in der
// Datenbank: Wer Vorlagen pflegt, soll nicht beliebige Verzeichnisse oder
// Programme auf dem Server erreichen.
type settings struct {
	// AllowedDirs: Wurzeln, unter denen Bundles entpackt und eingelesen werden
	// dürfen (relativ zum Arbeitsverzeichnis des Hosts oder absolut).
	AllowedDirs []string `json:"allowed_dirs"`
	// TectonicPath: eigenes Tectonic statt des eingebetteten.
	TectonicPath string `json:"tectonic_path"`
	// CacheDir: Paket-Cache von Tectonic (TeX-Pakete werden beim ersten Bedarf geladen).
	CacheDir string `json:"cache_dir"`
	// Retries: Versuche eines Tectonic-Laufs, wenn Pakete nicht geladen werden konnten.
	Retries int `json:"retries"`
	// RetryWait: Pause zwischen den Versuchen, z. B. "10s".
	RetryWait string `json:"retry_wait"`
	// Timeout eines Tectonic-Laufs, z. B. "10m".
	Timeout string `json:"timeout"`
	// Fristen ohne Eintrag des Buchungskreises (Tage).
	KeepSuccessDays *int `json:"keep_success_days"`
	KeepErrorDays   *int `json:"keep_error_days"`
	// Sync: Aufträge im Aufruf rechnen statt im Hintergrund (Tests, Diagnose).
	Sync bool `json:"sync"`

	retryWait, timeout time.Duration
	roots              []string
}

func (s *settings) normalize() error {
	if s.CacheDir == "" {
		s.CacheDir = filepath.Join("data", "tectonic-cache")
	}
	if abs, err := filepath.Abs(s.CacheDir); err == nil {
		s.CacheDir = abs
	}
	if s.Retries <= 0 {
		s.Retries = 3
	}
	var err error
	if s.retryWait, err = parseDuration(s.RetryWait, 10*time.Second); err != nil {
		return fmt.Errorf("retry_wait: %w", err)
	}
	if s.timeout, err = parseDuration(s.Timeout, 10*time.Minute); err != nil {
		return fmt.Errorf("timeout: %w", err)
	}
	s.roots = nil
	for _, d := range s.AllowedDirs {
		if strings.TrimSpace(d) == "" {
			continue
		}
		abs, err := filepath.Abs(strings.TrimSpace(d))
		if err != nil {
			return fmt.Errorf("allowed_dirs %q: %w", d, err)
		}
		s.roots = append(s.roots, filepath.Clean(abs))
	}
	return nil
}

func (s settings) keepDays() (success, failure int) {
	success, failure = 7, 30
	if s.KeepSuccessDays != nil {
		success = *s.KeepSuccessDays
	}
	if s.KeepErrorDays != nil {
		failure = *s.KeepErrorDays
	}
	return success, failure
}

func parseDuration(s string, def time.Duration) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return def, nil
	}
	return time.ParseDuration(strings.TrimSpace(s))
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{tex: execTex, now: time.Now}
	m.set = crud.NewSet(m.job(), m.jobFile(), m.definition(), m.bundle(), m.company())
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Vorlagen", Icon: "icon-file-text",
		Description: "Vorlagen für Briefe, Dokumente, HTML, XML und CSV; Aufträge mit Ergebnis"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Vorlagen")
	r.Object(serviceObject).Handle("render", m.renderAction).Handle("get", m.getAction)
	r.Object(setupObject).Customizing().Handle("examples", m.examplesCommand)
	r.Command(metamodel.CommandDefinition{Name: "setup-examples", Object: setupObject, Action: "examples",
		Description: "Beispiele anlegen: Bundles, Vorlagen (Brief, Dokument, HTML) und je einen Auftrag mit „Nicht löschen“",
		Params: []metamodel.CommandParam{{Name: "company", Required: true, Description: "Buchungskreis der Beispielaufträge"},
			{Name: "update", Description: "true: vorhandene Beispiel-Bundles auf den Stand dieser Version bringen"}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	if err := env.Config(&m.settings); err != nil {
		return err
	}
	if err := m.settings.normalize(); err != nil {
		return err
	}
	m.set.Bind(env.DB)
	if err := numrange.Define(ctx, env.Services, numrange.Definition{Object: jobObject, Owner: Name,
		Description: "Auftragsnummern der Vorlagen je Buchungskreis und Jahr", PerCompanyCode: true, PerYear: true,
		Pattern: "{CC}-{YYYY}-{N}", Width: 6}); err != nil {
		m.log.WarnContext(ctx, "Nummernkreis nicht angemeldet", "object", jobObject, "err", err.Error())
	}
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name(), "tectonic", tectonicInfo(), "verzeichnisse", strings.Join(m.settings.roots, ", "))
	return nil
}

func (m *Module) Shutdown(context.Context) error {
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
	if m.tecTmp != "" {
		_ = os.RemoveAll(m.tecTmp)
	}
	return nil
}

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }

// --- gemeinsame Hilfsfunktionen ------------------------------------------------------

const (
	tText = metamodel.TypeText
	tNum  = metamodel.TypeNumber
	tBool = metamodel.TypeBoolean
	tSel  = metamodel.TypeSelect
	tArea = metamodel.TypeTextarea
	tFile = metamodel.TypeFile
)

var (
	lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

	languages = []metamodel.Option{{Value: metamodel.LocaleDE, Label: "Deutsch"}, {Value: metamodel.LocaleEN, Label: "Englisch"},
		{Value: metamodel.LocaleZH, Label: "Chinesisch"}}
	targets = []metamodel.Option{{Value: targetHTML, Label: "HTML (.html-Templates)"}, {Value: targetXML, Label: "XML (.xml-Templates)"},
		{Value: targetCSV, Label: "CSV (.text-Templates)"}, {Value: targetPDF, Label: "PDF über LaTeX (.text-Templates)"}}
	bundleKinds = []metamodel.Option{{Value: kindTemplate, Label: "Go-Templates"}, {Value: kindBinary, Label: "Binärdateien (Bilder für PDF)"}}
	jobStatus   = []metamodel.Option{{Value: stNew, Label: "Neu"}, {Value: stRunning, Label: "In Arbeit"}, {Value: stDone, Label: "Fertig"},
		{Value: stError, Label: "Fehler"}}
)

// requireWrite: Ändern (object.action), bei Aufträgen im Buchungskreis cc.
func requireWrite(ctx context.Context, object, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s.%s nicht erlaubt", sdk.ErrPermissionDenied, object, action)
	}
	return nil
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case nil:
		return 0
	}
	var n int64
	_, _ = fmt.Sscan(crud.Str(v), &n)
	return n
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func (m *Module) nowText() string { return m.now().UTC().Format(time.RFC3339) }

func (m *Module) text(ctx context.Context, q string, args ...any) string {
	res, err := m.db.Query(ctx, q, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}
