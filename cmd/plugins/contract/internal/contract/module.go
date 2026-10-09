// Package contract ist die allgemeine Vertragsverwaltung des ERP (in
// Anlehnung an SAP RE-FX / RE-CN): Verträge, Vertragspartner, Objekte und
// Konditionen sind entkoppelt und haben Zeitscheiben.
//
//   - Vertragsart (Katalog je Buchungskreis): Richtung (wir erhalten / wir
//     zahlen), Hauptrolle des Vertragspartners, Nummernkreis, Objektpflicht,
//     erlaubte Objekte, exklusive Objektnutzung (Leerstand).
//   - Vertrag: interne Nummer aus dem Nummernkreis (numrange), externe Nummer
//     (Pflicht; ohne Eingabe <Vertragsart>-<3 Buchstaben des Partners>-<nnn>),
//     Vertragspartner aus dem Partnermodul (Pflicht), Laufzeit, Status
//     (Entwurf → aktiv → gekündigt).
//   - Partner (Rollen aus dem Partnermodul, je Buchungskreis aktiviert),
//     Objekte (Mietobjekte, Gebäude, Wirtschaftseinheiten), Konditionen
//     (Konditionsart mit Haupt-/Nebenforderung, Betrag, Rhythmus, Sachkonto
//     aus der Kontenfindung) und Kündigungsregeln – jeweils mit Zeitscheibe.
//
// Sollstellung und Buchung übernimmt ein eigenes Plugin; dieses Modul liefert
// dafür die Konditionen samt Sachkonto und meldet jede Änderung (Events).
package contract

import (
	"context"
	"embed"
	"fmt"
	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"log/slog"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// Name ist der Namensraum des Moduls (/m/contract, console contract:…).
const Name = "contract"

// setupObject: Service für das Einrichten eines Buchungskreises.
const setupObject = "ContractSetup"

// Nummernkreise (numrange) und Hook.
const (
	rangeContract = "Contract"         // interne Vertragsnummer je Vertragsart
	rangeExternal = "ContractExternal" // laufende Nummer der erzeugten externen Nummer
	hookActivate  = "contract.activate"
)

// Module ist die Vertragsverwaltung.
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

func (m *Module) entities() []*crud.Entity {
	es := []*crud.Entity{
		m.contract(), m.contractPartner(), m.contractObject(), m.condition(), m.noticeTerm(), m.loan(), m.loanPayment(),
		m.settlement(), m.settlementItem(), m.persons(), m.bankAccount(),
		m.contractType(), m.conditionType(), m.partnerRole(), m.account(),
		m.postingRun(), m.posting(),
	}
	for _, e := range es {
		m.withLabels(e)
	}
	return es
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Verträge", Icon: "icon-file",
		Description: "Verträge mit Partnern, Objekten, Konditionen und Kündigungsregeln"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.registerChartChange(r)
	m.set.Register(r, "Verträge")
	m.registerBilling(r)
	registerHooks(r, m)
	r.Object(setupObject).Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Vertragsarten, Konditionsarten und Partnerrollen eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	ledgerapi.SubscribeChartChange(ctx, m.services, m.log, chartChangeCallback, "Verträge")
	m.set.Bind(env.DB)
	m.set.Events(env.Services, Name)
	for _, d := range []numrange.Definition{
		{Object: rangeContract, Owner: Name, Description: "Vertragsnummern je Vertragsart (Intervallschlüssel = Nummernkreis der Vertragsart)",
			PerCompanyCode: true, PerYear: true, Pattern: "{KEY}-{YYYY}-{N}", Width: 4},
		{Object: rangeExternal, Owner: Name, Description: "Laufende Nummer erzeugter externer Vertragsnummern (<Vertragsart>-<Partner>-<nnn>)",
			PerCompanyCode: true, Pattern: "{N}", Width: 3},
	} {
		if err := numrange.Define(ctx, env.Services, d); err != nil {
			m.log.WarnContext(ctx, "Nummernkreis nicht angemeldet", "object", d.Object, "err", err.Error())
		}
	}
	if err := hook.Define(ctx, env.Services, hook.Definition{Name: hookActivate, Owner: Name, Phases: []string{hook.PhaseCheck, hook.PhaseCommit},
		Description: "Aktivieren eines Vertrags: check vor dem Statuswechsel (Meldung E verhindert ihn), commit danach",
		Data:        "ActivateHookData: company_code, contract_id, contract_type, direction, valid_from, valid_to, partners[], objects[]"}); err != nil {
		m.log.WarnContext(ctx, "Hook nicht angemeldet", "hook", hookActivate, "err", err.Error())
	}
	m.subscribeHooks(ctx)
	m.subscribeBilling(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }

// --- gemeinsame Hilfsfunktionen ----------------------------------------------------

const (
	tText = metamodel.TypeText
	tNum  = metamodel.TypeNumber
	tDate = metamodel.TypeDate
	tBool = metamodel.TypeBoolean
	tSel  = metamodel.TypeSelect
	tArea = metamodel.TypeTextarea
)

var lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

// catalogLookup: Auswahl aus einem Katalog des Buchungskreises.
func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
}

// requireWrite: Ändern im Buchungskreis (object.action); Lesen prüft crud.
func requireWrite(ctx context.Context, object, action, cc string) error {
	if action == "get" || action == "list" {
		return nil
	}
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
	}
	return nil
}

// within: Zeitraum [from, to] liegt in der Laufzeit des Vertrags.
func within(what, from, to string, c *contractRow) error {
	if from < c.ValidFrom || to > c.ValidTo {
		return crud.Invalid("%s %s–%s liegt außerhalb der Vertragslaufzeit %s–%s", what, from, to, c.ValidFrom, c.ValidTo)
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var i int64
		fmt.Sscan(strings.TrimSpace(n), &i)
		return i
	}
	return 0
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	case string:
		var f float64
		fmt.Sscan(strings.ReplaceAll(strings.TrimSpace(n), ",", "."), &f)
		return f
	}
	return 0
}

func formatNum(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

// labels: "_labels" eines Datensatzes (angelegt, wenn nötig).
func labels(rec crud.Record) map[string]any {
	l, _ := rec["_labels"].(map[string]any)
	if l == nil {
		l = map[string]any{}
		rec["_labels"] = l
	}
	return l
}

// defaults setzt leere Felder auf Standardwerte (crud schreibt sonst NULL in
// Spalten mit NOT NULL), z. B. nicht angekreuzte Ja/Nein-Felder.
func defaults(rec map[string]any, values map[string]any) {
	for k, v := range values {
		if rec[k] == nil || crud.Str(rec[k]) == "" {
			rec[k] = v
		}
	}
}
