// Package accounting ist das fachliche Modul Finanzbuchhaltung:
//
//   - Kontenplan (Account) mit Kontoart (Aktiv, Passiv, Eigenkapital, Ertrag,
//     Aufwand) und Status ACTIVE/LOCKED,
//   - Buchungsbelege (JournalEntry) mit Positionen (JournalLine) nach dem
//     Prinzip der doppelten Buchführung: Soll = Haben, je Buchungskreis,
//   - Salden je Konto (Service AccountBalance, Summen- und Saldenliste).
//
// Buchungen sind unveränderlich (GoBD, OR 957 ff.): Es gibt weder update noch
// delete. Ein Fehler wird durch eine Stornobuchung korrigiert (reverse), die
// den Originalbeleg mit umgekehrten Seiten ausgleicht.
package accounting

import (
	"context"
	"embed"
	"log/slog"

	"github.com/camel/coremesh/pkg/sdk/crud"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
	"github.com/camel/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/accounting, /api/v1/accounting).
const Name = "accounting"

// Module ist die Finanzbuchhaltung.
type Module struct {
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{}
	m.set = crud.NewSet(m.entities()...)
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Finanzbuchhaltung", Icon: "icon-book",
		Description: "Kontenplan, Buchungen nach doppelter Buchführung und Salden je Buchungskreis"}
}

// RegisterRoutes: Kontenplan und Belege (crud) sowie der Saldo-Service.
func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Buchhaltung")
	r.Object(balanceObject).Handle("list", m.balances)
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	m.set.Bind(env.DB)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL, Seed: seeds} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }
