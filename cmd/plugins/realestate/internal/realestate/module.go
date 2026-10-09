// Package realestate ist die Immobilienverwaltung (Mietobjekte) des ERP nach
// dem Vorbild von SAP RE-FX:
//
//   - Wirtschaftseinheit (BusinessEntity) → Gebäude (Building) → Mietobjekte,
//   - Mietobjekte (RentObject, realestate__rent_object) in einer Maske mit den
//     Arten Mieteinheit, Fläche, Pool und Vertragsobjekt; Felder und Abschnitte
//     je Art steuern Darstellungsregeln (Vorschlag über setup-company),
//   - Vertragsobjekt = Zusammenfassung von Einheiten, Stellplätzen und Flächen
//     mit Zeitscheibe (CompositeItem),
//   - Bemessungen (Measurement) mit Zeitscheibe für jedes Objekt,
//   - Geschäftspartner in Rollen (RentObjectPartner) mit Zeitscheibe an
//     Wirtschaftseinheit, Gebäude und Mietobjekt; die Rollen pflegt das
//     Partnermodul, hier werden sie je Buchungskreis aktiviert (RentPartnerRole),
//   - Kataloge je Buchungskreis (Nutzungsart, Bemessungsart, Maßeinheit,
//     Status, Art der Wirtschaftseinheit, Gebäudeart, Geschoss, Lage).
//
// Sprechende IDs: Gebäude = Wirtschaftseinheit + Nummer (LpzBrn → LpzBrn1),
// Mietobjekt = Gebäude + Kürzel der Nutzungsart + laufende Nummer
// (LpzBrn1WG001). Ohne Eingabe vergibt das Modul die nächste freie ID.
//
// Alle Daten sind je Buchungskreis sichtbar (crud.Access, Recht
// RentObject.read mit den Feldern entity_id und building_id).
package realestate

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Name ist der Namensraum des Moduls (/m/realestate, console realestate:…).
const Name = "realestate"

// setupObject: Service für das Einrichten eines Buchungskreises.
const setupObject = "RealEstateSetup"

// Module ist die Immobilienverwaltung.
type Module struct {
	db       module.DB
	services module.Services
	log      *slog.Logger
	set      *crud.Set
	settings settings // settings.modules.realestate
}

// settings: Einstellungen des Moduls.
type settings struct {
	// IDPrefixRequired: eingegebene IDs müssen mit der ID der übergeordneten
	// Ebene beginnen (Gebäude mit der Wirtschaftseinheit, Mietobjekt mit dem
	// Gebäude). Standard true; false erlaubt freie IDs, z. B. übernommene
	// Schlüssel. Vorgeschlagene IDs sind immer hierarchisch.
	IDPrefixRequired *bool `json:"id_prefix_required"`
}

func (s settings) idPrefixRequired() bool { return s.IDPrefixRequired == nil || *s.IDPrefixRequired }

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
		m.businessEntity(), m.building(),
		m.rentObject(), m.compositeItem(), m.measurement(),
		m.partnerRole(), m.objectPartner(),
	}
	for _, e := range es {
		m.withCatalogLabels(e)
	}
	return append(es, m.catalogEntities()...)
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Immobilien", Icon: "icon-building",
		Description: "Wirtschaftseinheiten, Gebäude und Mietobjekte mit Bemessungen"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Bestand")
	registerHooks(r, m)
	r.Object(setupObject).Handle("setupCompany", m.setupCompanyAction).Handle("partners", m.partnersAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kataloge eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
	r.Command(metamodel.CommandDefinition{Name: "partners", Object: setupObject, Action: "partners",
		Description: "Wirksame Partner eines Objekts zum Stichtag (mit Vererbung von Gebäude und Wirtschaftseinheit)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "object", Required: true}, {Name: "date"}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	if err := env.Config(&m.settings); err != nil {
		return err
	}
	m.set.Bind(env.DB)
	m.set.Events(env.Services, Name)
	m.subscribeHooks(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL + partnerHCL} }

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

// access: Lesen je Buchungskreis (Recht RentObject.read); fields sind die
// Berechtigungsfelder, die die Tabelle als Spalten hat.
func access(fields ...string) *crud.Access {
	return &crud.Access{Object: "RentObject", Records: true, CompanyCode: "company_code", Fields: fields}
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

// validityFields: Gültigkeit und Status eines Objekts (ohne Versionierung).
func validityFields() []crud.Field {
	return []crud.Field{
		{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("ObjectStatus"), Group: "Gültigkeit"},
		{Key: "valid_from", Label: "Gültig ab", Type: tDate, Listable: true, Group: "Gültigkeit"},
		{Key: "valid_to", Label: "Gültig bis", Type: tDate, Group: "Gültigkeit"},
		{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true},
	}
}

// checkValidity: Standardwerte (ab Stichtag des Benutzers bzw. heute, offen bis 9999-12-31, Status ACTIVE
// wenn vorhanden), Status aus dem Katalog, von ≤ bis, Änderungsvermerk.
func (m *Module) checkValidity(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if crud.Str(rec["valid_from"]) == "" {
		rec["valid_from"] = crud.KeyDate(ctx)
	}
	if crud.Str(rec["valid_to"]) == "" {
		rec["valid_to"] = crud.DateMax
	}
	from, err := crud.ParseDate(rec["valid_from"])
	if err != nil {
		return err
	}
	to, err := crud.ParseDate(rec["valid_to"])
	if err != nil {
		return err
	}
	if to < from {
		return crud.Invalid("Gültig bis (%s) liegt vor Gültig ab (%s)", to, from)
	}
	rec["valid_from"], rec["valid_to"] = from, to
	if crud.Str(rec["status"]) == "" {
		if ok, _ := m.catalogHas(ctx, "object_status", cc, "ACTIVE"); ok {
			rec["status"] = "ACTIVE"
		}
	}
	if err := m.requireCatalog(ctx, "object_status", "Status", cc, rec["status"]); err != nil {
		return err
	}
	rec["changed_at"], rec["changed_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func trimUpper(v any) string { return strings.ToUpper(strings.TrimSpace(crud.Str(v))) }

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
		fmt.Sscan(n, &i)
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
		fmt.Sscan(n, &f)
		return f
	}
	return 0
}
