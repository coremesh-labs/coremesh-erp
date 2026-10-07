// Package ledger ist das zentrale Hauptbuch (General Ledger) des ERP nach dem
// Vorbild von SAP S/4HANA:
//
//   - Kontenplan zentral (ledger__account_master, analog SKA1) und je
//     Buchungskreis (ledger__account_company, analog SKB1),
//   - Steuerung je Buchungskreis mit Modul-Mapping (ledger__company_config),
//   - Belegkopf (ledger__journal_entry_header, analog BKPF) und Universal
//     Journal (ledger__journal_entry_item, analog ACDOCA) als einzige Quelle
//     aller Einzelposten – mehrdimensional (Kostenstelle, Profit-Center,
//     Segment, Vertrieb, Vermietung, Einkauf, freie Dimensionen),
//   - Buchungsperioden: Periodendefinition 01–16 und Liste der offenen Perioden (periods.go),
//   - Währungen und Tageskurse (ledger__currency, ledger__exchange_rate, analog TCURR),
//   - Vorerfassung für manuelle Buchungen (ledger__draft_header/_item, analog
//     VBKPF/VBSEG): speichern und ändern, dann buchen.
//
// Fachmodule buchen synchron über den Service LedgerPosting (Client
// github.com/coremesh-lab/coremesh-erp/pkg/ledgerapi). Gebuchte Belege sind
// unveränderlich; Korrekturen sind Stornobelege.
package ledger

import (
	"context"
	"embed"
	"log/slog"
	"sync/atomic"

	"github.com/coremesh-lab/coremesh/pkg/sdk/crud"
	"github.com/coremesh-lab/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-lab/coremesh/pkg/sdk/module"

	"github.com/coremesh-lab/coremesh-erp/pkg/ledgerapi"
)

// Name ist der Namensraum des Moduls (/m/ledger, /api/v1/ledger, console ledger:…).
const Name = "ledger"

// Module ist das Hauptbuch.
type Module struct {
	migrated atomic.Bool // Datenumstellungen älterer Versionen erledigt (migrations.go)
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
	cur      currencies
	posting  *PostingService
}

var (
	_ module.Module         = (*Module)(nil)
	_ module.SchemaProvider = (*Module)(nil)
	_ module.Translator     = (*Module)(nil)
)

// New erzeugt das Modul.
func New() *Module {
	m := &Module{}
	m.cur.m = m
	m.posting = &PostingService{m: m}
	m.set = crud.NewSet(m.entities()...)
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Hauptbuch", Icon: "icon-book",
		Description: "General Ledger: Kontenpläne, Universal Journal, Vorerfassung, Periodensperre und Währungen"}
}

// RegisterRoutes: Stammdaten und Belege (crud), Services und Konsolenbefehle.
func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Hauptbuch")
	r.Object(ledgerapi.Object).
		Handle(ledgerapi.ActionPost, m.beforeAction(m.postAction)).
		Handle(ledgerapi.ActionSimulate, m.beforeAction(m.simulateAction)).
		Handle(ledgerapi.ActionReverse, m.beforeAction(m.reverseAction))
	r.Object(balanceObject).Handle("list", m.beforeAction(m.balances))
	r.Object(conversionObject).Handle("convert", m.convertAction)
	// Ladevorgänge (Konsole / API): Kontenrahmen, Kurse, Buchungskreis, Perioden.
	r.Object(loaderObject).
		Handle("loadCoa", m.beforeAction(m.loadCoaAction)).
		Handle("loadRates", m.loadRatesAction).
		Handle("setupCompany", m.beforeAction(m.setupCompanyAction)).
		Handle("setPeriods", m.beforeAction(m.setPeriodsAction))

	r.Command(metamodel.CommandDefinition{Name: "load-coa", Object: loaderObject, Action: "loadCoa",
		Description: "Kontenrahmen laden (Upsert): mitgelieferter SKR04/SKR25 oder eigene Datei (JSON/CSV)",
		Params: []metamodel.CommandParam{
			{Name: "chart", Required: true, Description: "Kontenplan, z. B. SKR04 oder SKR25"},
			{Name: "file", File: true, Description: "optional: JSON {accounts:[…]} oder CSV account_number;name;account_type;…"},
		}})
	r.Command(metamodel.CommandDefinition{Name: "load-rates", Object: loaderObject, Action: "loadRates",
		Description: "Tageskurse laden (Upsert) aus JSON oder CSV",
		Params: []metamodel.CommandParam{
			{Name: "file", File: true, Required: true, Description: "rate_type;from_currency;to_currency;valid_from;rate[;from_factor;to_factor]"},
		}})
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: loaderObject, Action: "setupCompany",
		Description: "Buchungskreis einrichten: Steuerung anlegen, alle Konten des Kontenplans zuordnen, Perioden eines Jahres öffnen",
		Params: []metamodel.CommandParam{
			{Name: "company", Required: true}, {Name: "chart", Required: true}, {Name: "currency", Required: true},
			{Name: "year", Description: "Geschäftsjahr, dessen Perioden 1–12 geöffnet werden"},
		}})
	r.Command(metamodel.CommandDefinition{Name: "periods", Object: loaderObject, Action: "setPeriods",
		Description: "Buchungsperioden öffnen oder schließen (Liste der offenen Perioden) – ganz oder für einen Kontenbereich (--accounts)",
		Params: []metamodel.CommandParam{
			{Name: "company", Required: true}, {Name: "year", Required: true},
			{Name: "from", Required: true}, {Name: "to", Required: true},
			{Name: "status", Required: true, Description: "OPEN (öffnen) oder CLOSED (schließen)"},
			{Name: "ledger", Description: "Standard: führendes Ledger des Buchungskreises"},
			{Name: "kind", Description: "Kontoart (z. B. D, K, S); Standard + = alle (Hauptschalter)"},
			{Name: "accounts", Description: "Kontenbereich von-bis, z. B. 1000-1999: legt eine Kontensperre (CLOSED) bzw. Freigabe (OPEN) an statt die Periode zu öffnen oder zu schließen"},
			{Name: "reason", Description: "Grund der Kontensperre"},
		}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	m.set.Bind(env.DB)
	m.set.Events(env.Services, Name) // Vorerfassung: SystemEvents bei jeder Änderung
	m.defineHooks(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL, Seed: seeds} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }
