// Package opcost sind die Betriebskosten des ERP: die Stammdaten, die
// Vertragsabrechnungen (Vertragsmodul), Eingangsrechnungen (Beschaffung) und
// der Nebenkostenrechner gemeinsam nutzen.
//
//   - Kostenarten (CostCategory): Art – umlagefähig (Betriebskosten nach § 2
//     BetrKV), nicht umlagefähig (Verwaltung, Instandhaltung …) oder
//     Zuführung zur Erhaltungsrücklage –, Vorschlag Sachkonto, Nr. nach BetrKV.
//   - Verteilerschlüssel (AllocationKey): Grundlage Bemessung der
//     Immobilienverwaltung (MEA, Wohnfläche …), Anzahl Einheiten, Personen,
//     Verbrauch oder direkte Zuordnung.
//
// Andere Module lesen beides über die Objects (get/list) – sie haben keine
// eigene Kopie der Kostenarten.
package opcost

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"log/slog"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/opcost).
const Name = "opcost"

const setupObject = "OpCostSetup"

const (
	costAllocable    = "ALLOCABLE"
	costNonAllocable = "NON_ALLOCABLE"
	costReserve      = "RESERVE"
)

var (
	tText = metamodel.TypeText
	tNum  = metamodel.TypeNumber
	tBool = metamodel.TypeBoolean
	tSel  = metamodel.TypeSelect

	lookupCC      = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupAccount = &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
		Columns: []string{"account_number", "account_name", "account_type"}, Filters: map[string]string{"company_code_id": "company_code"}}
	costTypeOptions = []metamodel.Option{
		{Value: costAllocable, Label: "umlagefähig (Betriebskosten)"}, {Value: costNonAllocable, Label: "nicht umlagefähig"},
		{Value: costReserve, Label: "Zuführung Erhaltungsrücklage (Bilanz)"}}
	basisOptions = []metamodel.Option{
		{Value: "MEASUREMENT", Label: "Bemessung des Objekts (z. B. MEA, Wohnfläche)"}, {Value: "UNITS", Label: "Anzahl Einheiten"},
		{Value: "PERSONS", Label: "Personen"}, {Value: "CONSUMPTION", Label: "Verbrauch (Zähler)"}, {Value: "DIRECT", Label: "direkte Zuordnung"}}
)

// Module sind die Betriebskosten.
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

func New() *Module {
	m := &Module{}
	m.set = crud.NewSet(m.costCategory(), m.allocationKey(), m.definition(), m.rule(), m.run(), m.journal(), m.tenant())
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Betriebskosten", Icon: "icon-coins",
		Description: "Kostenarten und Verteilerschlüssel für Abrechnungen"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.registerChartChange(r)
	m.set.Register(r, "Einstellungen")
	m.registerPosting(r)
	r.Object(setupObject).Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kostenarten und Verteilerschlüssel eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	ledgerapi.SubscribeChartChange(ctx, m.services, m.log, chartChangeCallback, "Betriebskosten")
	m.set.Bind(env.DB)
	m.subscribePosting(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL} }

//go:embed i18n/*.json
var i18nFiles embed.FS

var translations = module.MustLoadTranslations(i18nFiles, "i18n")

func (m *Module) Translations() metamodel.Translations { return translations }

func catalogFields(extra ...crud.Field) []crud.Field {
	fs := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	fs = append(fs, extra...)
	return append(fs,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
}

func (m *Module) catalog(object, title, table string, fields []crud.Field, check func(context.Context, crud.Record) error) *crud.Entity {
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-tag", Table: table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: fields,
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			if rec["sort_order"] == nil || crud.Str(rec["sort_order"]) == "" {
				rec["sort_order"] = 0
			}
			return check(ctx, rec)
		},
	}
}

// costCategory: Kostenart. „allocable“ (umlagefähig) liefert die Anzeige
// zusätzlich, abgeleitet aus der Art.
func (m *Module) costCategory() *crud.Entity {
	e := m.catalog("CostCategory", "Kostenarten", "opcost__cost_category", catalogFields(
		crud.Field{Key: "cost_type", Label: "Art", Type: tSel, Required: true, Listable: true, Options: costTypeOptions},
		crud.Field{Key: "account_number", Label: "Sachkonto (Vorschlag; bei Rücklage das Bilanzkonto)", Type: tText, Listable: true, Lookup: lookupAccount},
		crud.Field{Key: "betrkv_no", Label: "Nr. nach § 2 BetrKV", Type: tText, Listable: true},
		crud.Field{Key: "allocable", Label: "Umlagefähig", Type: tBool, ReadOnly: true, Virtual: true},
	), func(ctx context.Context, rec crud.Record) error {
		if acc := strings.TrimSpace(crud.Str(rec["account_number"])); acc != "" {
			nr, err := m.glAccount(ctx, crud.Str(rec["company_code"]), acc)
			if err != nil {
				return err
			}
			rec["account_number"] = nr
		} else {
			rec["account_number"] = nil
		}
		rec["betrkv_no"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["betrkv_no"])))
		return nil
	})
	e.Filters = []string{"company_code", "cost_type"}
	e.Decorate = func(_ context.Context, rec crud.Record) error {
		rec["allocable"] = crud.Str(rec["cost_type"]) == costAllocable
		return nil
	}
	e.Actions = []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "setup", Label: "Buchungskreis einrichten …",
		Fields: []string{"company_code"}}, Handle: m.setupCompanyAction}}
	return e
}

// allocationKey: Verteilerschlüssel.
func (m *Module) allocationKey() *crud.Entity {
	return m.catalog("AllocationKey", "Verteilerschlüssel", "opcost__allocation_key", catalogFields(
		crud.Field{Key: "basis", Label: "Grundlage", Type: tSel, Required: true, Listable: true, Options: basisOptions},
		crud.Field{Key: "measurement_type", Label: "Bemessungsart (bei Bemessung, z. B. MEA, WFL)", Type: tText, Listable: true,
			Lookup: &metamodel.Lookup{Object: "MeasurementType", ValueField: "code", LabelFields: []string{"name"},
				Filters: map[string]string{"company_code": "company_code"}}},
		crud.Field{Key: "unit", Label: "Einheit (Anzeige, z. B. ‰, m²)", Type: tText},
	), func(_ context.Context, rec crud.Record) error {
		mt := strings.ToUpper(strings.TrimSpace(crud.Str(rec["measurement_type"])))
		if crud.Str(rec["basis"]) == "MEASUREMENT" && mt == "" {
			return crud.Invalid("Grundlage Bemessung braucht die Bemessungsart, z. B. MEA")
		}
		if crud.Str(rec["basis"]) != "MEASUREMENT" {
			mt = ""
		}
		rec["measurement_type"] = nilIfEmpty(mt)
		return nil
	})
}

// --- Vorschlagswerte ----------------------------------------------------------------

var defaultCostCategories = []struct{ code, name, typ, betrkv string }{
	{"GRST", "Grundsteuer", costAllocable, "1"},
	{"WASSER", "Wasserversorgung", costAllocable, "2"},
	{"ABWASSER", "Entwässerung", costAllocable, "3"},
	{"HEIZUNG", "Heizung", costAllocable, "4"},
	{"WARMW", "Warmwasser", costAllocable, "5"},
	{"AUFZUG", "Aufzug", costAllocable, "7"},
	{"STRREIN", "Straßenreinigung und Müllbeseitigung", costAllocable, "8"},
	{"GEBREIN", "Gebäudereinigung und Ungezieferbekämpfung", costAllocable, "9"},
	{"GARTEN", "Gartenpflege", costAllocable, "10"},
	{"BELEUCHT", "Beleuchtung", costAllocable, "11"},
	{"SCHORNST", "Schornsteinreinigung", costAllocable, "12"},
	{"VERSICH", "Sach- und Haftpflichtversicherung", costAllocable, "13"},
	{"HAUSWART", "Hauswart", costAllocable, "14"},
	{"ANTENNE", "Gemeinschaftsantenne / Breitbandnetz", costAllocable, "15"},
	{"WAESCHE", "Einrichtungen der Wäschepflege", costAllocable, "16"},
	{"SONSTBK", "Sonstige Betriebskosten", costAllocable, "17"},
	{"VERWALT", "Verwaltungskosten (WEG-Verwalter)", costNonAllocable, ""},
	{"INSTAND", "Instandhaltung / Reparaturen", costNonAllocable, ""},
	{"KONTO", "Kontoführung, Sonstiges (nicht umlagefähig)", costNonAllocable, ""},
	{"RUECKL", "Zuführung Erhaltungsrücklage", costReserve, ""},
}

var defaultAllocationKeys = []struct{ code, name, basis, mt, unit string }{
	{"MEA", "Miteigentumsanteile", "MEASUREMENT", "MEA", "‰"},
	{"WFL", "Wohnfläche", "MEASUREMENT", "WFL", "m²"},
	{"HZF", "Heizfläche", "MEASUREMENT", "HZF", "m²"},
	{"EINH", "Anzahl Einheiten", "UNITS", "", "Einh."},
	{"PERS", "Personen", "PERSONS", "", "Pers."},
	{"VERBR", "Verbrauch", "CONSUMPTION", "", ""},
	{"DIREKT", "direkte Zuordnung", "DIRECT", "", ""},
}

func (m *Module) setupCompany(ctx context.Context, cc string) (cats, keys int, err error) {
	has := func(table, code string) (bool, error) {
		res, err := m.db.Query(ctx, "SELECT 1 FROM "+table+" WHERE company_code = ? AND code = ?", cc, code)
		return err == nil && len(res.Rows) > 0, err
	}
	for i, c := range defaultCostCategories {
		if ok, err := has("opcost__cost_category", c.code); err != nil || ok {
			if err != nil {
				return cats, keys, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO opcost__cost_category (company_code, code, name, cost_type, betrkv_no, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, c.code, c.name, c.typ, nilIfEmpty(c.betrkv), (i+1)*10, true); err != nil {
			return cats, keys, err
		}
		cats++
	}
	for i, k := range defaultAllocationKeys {
		if ok, err := has("opcost__allocation_key", k.code); err != nil || ok {
			if err != nil {
				return cats, keys, err
			}
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO opcost__allocation_key (company_code, code, name, basis, measurement_type, unit, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, cc, k.code, k.name, k.basis, nilIfEmpty(k.mt), nilIfEmpty(k.unit), (i+1)*10, true); err != nil {
			return cats, keys, err
		}
		keys++
	}
	return cats, keys, nil
}

func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     any    `json:"company"`
		CompanyCode string `json:"company_code"`
		Data        struct {
			CompanyCode string `json:"company_code"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.CompanyCode + in.Data.CompanyCode)
	if in.Company != nil { // Konsole: --param company=1000 kommt als Zahl
		cc = strings.TrimSpace(crud.Str(in.Company))
	}
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	ok, err := sdk.CheckAccess(ctx, "CostCategory", "create", cc)
	if err != nil {
		return sdk.Response{}, err
	}
	if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung für CostCategory.create im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	var cats, keys int
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		cats, keys, err = m.setupCompany(ctx, cc)
		return err
	}); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "cost_categories": cats, "allocation_keys": keys,
		"message": fmt.Sprintf("Buchungskreis %s: %d Kostenarten, %d Verteilerschlüssel angelegt", cc, cats, keys)}}, nil
}

// --- Hilfsfunktionen ----------------------------------------------------------------

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// glAccount: Sachkonto im Buchungskreis (Hauptbuch) – Nummer mit Kontenplan-Präfix.
func (m *Module) glAccount(ctx context.Context, cc, number string) (string, error) {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable) {
		return "", crud.Invalid("Hauptbuch nicht verfügbar: %v", err)
	}
	if err != nil {
		return "", err
	}
	var out struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		a, b := strings.ToUpper(it.AccountNumber), strings.ToUpper(number)
		if a == b || strings.HasSuffix(a, "-"+b) {
			return it.AccountNumber, nil
		}
	}
	return "", crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht", number, cc)
}
