// Package heatcost rechnet Heiz- und Warmwasserkosten nach der HeizkostenV ab –
// allgemein, für jede Liegenschaft:
//
//   - Abrechnungseinheit (HeatCostUnit): Liegenschaft mit Messdienst,
//     Nutzergruppen (z. B. Doppelhäuser) und Nutzern (Mietobjekte mit
//     Nutzernummer), Grundkostenanteil und Schlüssel für Heizung und Warmwasser,
//   - Kataloge: Kostengruppen, Kostenarten (Aufteilung: nach Warmwasseranteil,
//     nur Heizung, nur Warmwasser, nach eigenem Schlüssel, direkt an einen Nutzer)
//     und Verteilerschlüssel (Herkunft: Fläche, Verbrauch einer Sparte, Zahl der
//     Geräte oder Eingabe),
//   - Abrechnung (HeatCostRun) eines Zeitraums: die angefallenen Kosten
//     (Kostenaufstellung), Verteilungsgrößen der Liegenschaft, Werte der Nutzer
//     („Werte übernehmen“ aus Zählern und Flächen) und „Rechnen“: Warmwasseranteil,
//     Grund- und Verbrauchskosten, Verteilung je Nutzer mit Rechenweg. Korrektur =
//     neue Abrechnung mit Bezug auf die alte.
//
// Alle Daten sind je Buchungskreis sichtbar (crud.Access, Recht HeatCostRun.read).
package heatcost

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/heatcost, console heatcost:…).
const Name = "heatcost"

const setupObject = "HeatCostSetup"

// Module verwaltet Abrechnungseinheiten und Abrechnungen.
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
	es := []*crud.Entity{m.unit(), m.userGroup(), m.user(), m.run(), m.costLine(), m.basis(), m.value(), m.result(), m.step(), m.co2Result()}
	for _, e := range es {
		m.withLabels(e)
	}
	return append(append(es, m.catalogEntities()...), m.co2Stage(), m.degreeDay())
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Heizkostenabrechnung", Icon: "icon-flame",
		Description: "Heiz- und Warmwasserkosten nach HeizkostenV verteilen: Kosten, Schlüssel aus Zählern und Flächen, Ergebnis je Nutzer"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Heizkosten")
	r.Object(setupObject).Customizing().Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kataloge der Heizkostenabrechnung eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	m.set.Bind(env.DB)
	m.set.Events(env.Services, Name)
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
	tArea = metamodel.TypeTextarea
	tSel  = metamodel.TypeSelect
)

var lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

var ccFilter = map[string]string{"company_code": "company_code"}

func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"}, Filters: ccFilter}
}

var (
	partnerLookup    = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}
	rentObjectLookup = &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	entityLookup     = &metamodel.Lookup{Object: "BusinessEntity", ValueField: "entity_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	buildingLookup   = &metamodel.Lookup{Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	unitLookup       = &metamodel.Lookup{Object: "HeatCostUnit", ValueField: "unit_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	runLookup        = &metamodel.Lookup{Object: "HeatCostRun", ValueField: "run_id", LabelFields: []string{"title"}, Filters: ccFilter}
)

// access: Lesen je Buchungskreis (Recht HeatCostRun.read).
func access() *crud.Access {
	return &crud.Access{Object: "HeatCostRun", Records: true, CompanyCode: "company_code"}
}

func checkWrite(object string) func(ctx context.Context, action string, rec crud.Record) error {
	return func(ctx context.Context, action string, rec crud.Record) error {
		return requireWrite(ctx, object, action, crud.Str(rec["company_code"]))
	}
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

func (m *Module) stamp(ctx context.Context, rec crud.Record) {
	rec["changed_at"], rec["changed_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

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

func toInt(v any) int64 { return int64(toFloat(v)) }

// isSet: Zahl eingegeben (nicht leer).
func isSet(v any) bool { return v != nil && strings.TrimSpace(crud.Str(v)) != "" }

// round2: kaufmännisch auf Cent (Abstand zur Gleitkomma-Darstellung ausgeglichen).
func round2(x float64) float64 {
	if x < 0 {
		return -round2(-x)
	}
	return math.Floor(x*100+0.5+1e-7) / 100
}

// round: kaufmännisch auf d Nachkommastellen.
func round(x float64, d int) float64 {
	if x < 0 {
		return -round(-x, d)
	}
	f := math.Pow10(d)
	return math.Floor(x*f+0.5+1e-7) / f
}

// one: erste Spalte der ersten Zeile als Text ("" ohne Treffer).
func (m *Module) one(ctx context.Context, q string, args ...any) (string, error) {
	res, err := m.db.Query(ctx, q, args...)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", nil
	}
	return crud.Str(res.Rows[0][0]), nil
}

func date(v any) string {
	d, _ := crud.ParseDate(v)
	return d
}

// germanDate: 2026-11-04 → 04.11.2026.
func germanDate(s string) string {
	if len(s) < 10 {
		return s
	}
	return s[8:10] + "." + s[5:7] + "." + s[0:4]
}

// money: 1234.5 → „1.234,50“.
func money(v float64) string { return germanNumber(v, 2) }

func germanNumber(v float64, dec int) string {
	s := fmt.Sprintf("%.*f", dec, v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if dec > 0 {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}

func splitCodes(s string) []string {
	var out []string
	for _, c := range strings.Split(s, ",") {
		if c = strings.ToUpper(strings.TrimSpace(c)); c != "" {
			out = append(out, c)
		}
	}
	return out
}

func containsCode(list []string, code string) bool {
	for _, c := range list {
		if c == code {
			return true
		}
	}
	return false
}

func validOption(opts []metamodel.Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}

func setLabel(rec crud.Record, key, label string) {
	labels, _ := rec["_labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
		rec["_labels"] = labels
	}
	labels[key] = label
}

// partnerName: Name des Partners; leer, wenn das Partnermodul fehlt oder der
// Benutzer den Partner nicht lesen darf.
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Type  string `json:"type"`
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	if bp.Type == "PERSON" {
		return strings.TrimSpace(bp.Name2 + " " + bp.Name1)
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// refTables: Lookup-Object → Tabelle, Schlüsselspalte, Bezeichnung (Anzeige der
// Bezeichnung statt des Schlüssels).
func refTables() map[string][3]string {
	out := map[string][3]string{}
	for _, c := range catalogTables {
		out[c.Object] = [3]string{"heatcost__" + c.Table, "code", "name"}
	}
	out["HeatCostUnit"] = [3]string{"heatcost__unit", "unit_id", "designation"}
	out["HeatCostRun"] = [3]string{"heatcost__run", "run_id", "title"}
	return out
}

// withLabels: Felder mit Lookup auf Kataloge zeigen die Bezeichnung, Partner den Namen.
func (m *Module) withLabels(e *crud.Entity) {
	tables := refTables()
	type ref struct {
		key     string
		t       [3]string
		partner bool
	}
	var refs []ref
	for _, f := range e.Fields {
		if f.Lookup == nil || f.ActionOnly {
			continue
		}
		if t, ok := tables[f.Lookup.Object]; ok {
			refs = append(refs, ref{key: f.Key, t: t})
		} else if f.Lookup.Object == "BusinessPartner" {
			refs = append(refs, ref{key: f.Key, partner: true})
		}
	}
	if len(refs) == 0 {
		return
	}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		cc := crud.Str(rec["company_code"])
		for _, r := range refs {
			v := crud.Str(rec[r.key])
			if v == "" {
				continue
			}
			if r.partner {
				if name := m.partnerName(ctx, v); name != "" {
					setLabel(rec, r.key, name)
				}
				continue
			}
			label, err := m.one(ctx, "SELECT "+r.t[2]+" FROM "+r.t[0]+" WHERE company_code = ? AND "+r.t[1]+" = ?", cc, v)
			if err != nil {
				return err
			}
			if label != "" {
				setLabel(rec, r.key, label)
			}
		}
		if decorate != nil {
			return decorate(ctx, rec)
		}
		return nil
	}
}

// list: Einträge eines Objects eines anderen Moduls (Dienst list mit query).
func (m *Module) list(ctx context.Context, object string, query map[string]any) ([]map[string]any, error) {
	resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": query, "limit": 1000})
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

type recordInput struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"data"`
}
