// Package asset verwaltet Objekte – alles, woran Verträge, Kosten, Dokumente
// und Personen hängen, ohne ein Mietobjekt zu sein:
//
//   - Objekt (Asset) mit Objektart aus dem Katalog je Buchungskreis (Wohnung,
//     Auto, Reifensatz, Handy, Reise, Gerät …); die Objektart legt das Kürzel
//     der sprechenden ID fest und welche Arten Bestandteil sein dürfen,
//   - Bestandteile mit Zeitscheibe (AssetPart): Auto ← Sommer-/Winterreifen,
//   - Personen in Rollen mit Zeitscheibe (AssetPerson): Eigentümer, Nutzer,
//     Halter, Fahrer; Rollen im Katalog (AssetRole), exklusiv oder mit Anteil,
//   - Merkmale über Tags, Dokumente aus dem DMS und Texte (Abschnitte der Maske),
//   - Verträge hängen über ihre Vertragsobjekte der Art „Objekt“ (Plugin
//     contract, object_type Asset) am Objekt.
//
// Alle Daten sind je Buchungskreis sichtbar (crud.Access, Recht Asset.read).
package asset

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

// Name ist der Namensraum des Moduls (/m/asset, console asset:…).
const Name = "asset"

// setupObject: Service für das Einrichten eines Buchungskreises.
const setupObject = "AssetSetup"

// Module verwaltet die Objekte.
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
	es := []*crud.Entity{m.asset(), m.part(), m.person()}
	for _, e := range es {
		m.withCatalogLabels(e)
	}
	return append(es, m.catalogEntities()...)
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Objekte", Icon: "icon-package",
		Description: "Wohnung, Auto, Handy, Reise … mit Bestandteilen, Personen, Verträgen und Dokumenten"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Bestand")
	r.Object(setupObject).Customizing().Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kataloge der Objekte eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
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
)

var lookupCC = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}

// catalogLookup: Auswahl aus einem Katalog des Buchungskreises.
func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
}

// assetLookup: Auswahl eines Objekts des Buchungskreises.
var assetLookup = &metamodel.Lookup{Object: "Asset", ValueField: "asset_id", LabelFields: []string{"designation"},
	Filters: map[string]string{"company_code": "company_code"}}

// access: Lesen je Buchungskreis (Recht Asset.read).
func access() *crud.Access {
	return &crud.Access{Object: "Asset", Records: true, CompanyCode: "company_code"}
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

// checkValidity: Standardwerte (ab Stichtag des Benutzers bzw. heute, offen bis
// 9999-12-31, Status ACTIVE wenn vorhanden), Status aus dem Katalog, von ≤ bis,
// Änderungsvermerk.
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
		if ok, _ := m.catalogHas(ctx, "asset_status", cc, "ACTIVE"); ok {
			rec["status"] = "ACTIVE"
		}
	}
	if err := m.requireCatalog(ctx, "asset_status", "Status", cc, rec["status"]); err != nil {
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

// match: Abschnitt mit Unter-Objekten desselben Buchungskreises.
func match(childField, masterField string) map[string]string {
	return map[string]string{"company_code": "company_code", childField: masterField}
}
