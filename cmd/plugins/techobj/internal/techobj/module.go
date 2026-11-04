// Package techobj verwaltet technische Objekte – Zähler, Heizung, Aufzug,
// Rauchwarnmelder … – in drei getrennten Sichten:
//
//   - Lagebaum (TechLocation): wo man etwas findet, mit Wegbeschreibung, Zugang,
//     Ansprechpartner und Fotos (DMS),
//   - Messpunkte (MeterPoint) mit Lage (Zeitscheibe) und Versorgungsbaum
//     (MeterPointRelation): was gemessen wird und wer an wem hängt – unabhängig
//     vom eingebauten Gerät,
//   - technische Objekte (TechObject) mit Gerätetyp (TechObjectType: Hersteller,
//     Modell, Vorlage der Zählwerke, Unterlagen für alle Geräte des Typs),
//     Nummern (Seriennummer, Zählernummer, Funk-ID …) und Zählwerken; der Einbau
//     (MeterInstallation) setzt ein Gerät mit Zeitscheibe in einen Messpunkt,
//   - Ablesungen (MeterReading) je Zählwerk und Tag – neutrale Fakten, ohne
//     Tarif oder Preis; eine spätere Abrechnung setzt hinten an.
//
// Objekte anderer Module (Mietobjekt, Gebäude, Objekt, Vertrag, Partner …)
// hängen nur über die Brücke (TechLink) daran: welche verknüpft werden dürfen
// und in welchen Rollen, steht im Katalog, geprüft wird über den Dienst des
// zuständigen Moduls. Den Mieter eines Zählers liefert so der Mietvertrag am
// Mietobjekt – ohne doppelte Pflege.
//
// Alle Daten sind je Buchungskreis sichtbar (crud.Access, Recht TechObject.read).
package techobj

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

// Name ist der Namensraum des Moduls (/m/techobj, console techobj:…).
const Name = "techobj"

const setupObject = "TechObjectSetup"

// Module verwaltet die technischen Objekte.
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
	es := []*crud.Entity{m.location(), m.equipmentType(), m.typeRegister(), m.equipment(), m.number(), m.register(),
		m.point(), m.pointLocation(), m.pointRelation(), m.installation(), m.reading(), m.link()}
	for _, e := range es {
		m.withLabels(e)
	}
	return append(es, m.catalogEntities()...)
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Technische Objekte", Icon: "icon-gauge",
		Description: "Zähler, Heizung, Aufzug … mit Lage, Messpunkten, Einbau, Ablesungen und Dokumenten"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.set.Register(r, "Bestand")
	r.Object(setupObject).Customizing().Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Kataloge der technischen Objekte eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
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

// catalogLookup: Auswahl aus einem Katalog des Buchungskreises.
func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"}, Filters: ccFilter}
}

var (
	locationLookup  = &metamodel.Lookup{Object: "TechLocation", ValueField: "location_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	typeLookup      = &metamodel.Lookup{Object: "TechObjectType", ValueField: "type_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	equipmentLookup = &metamodel.Lookup{Object: "TechObject", ValueField: "equipment_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	pointLookup     = &metamodel.Lookup{Object: "MeterPoint", ValueField: "point_id", LabelFields: []string{"designation"}, Filters: ccFilter}
	partnerLookup   = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}
)

// access: Lesen je Buchungskreis (Recht TechObject.read).
func access() *crud.Access {
	return &crud.Access{Object: "TechObject", Records: true, CompanyCode: "company_code"}
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

// checkValidity: Standardwerte (ab Stichtag des Benutzers bzw. heute, offen bis
// 9999-12-31, Status = Vorschlag des Katalogs, wenn das Objekt einen Status hat), von ≤ bis,
// Änderungsvermerk (wenn die Tabelle ihn führt).
func (m *Module) checkValidity(ctx context.Context, rec crud.Record, withStatus, stamp bool) error {
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
	if withStatus {
		if err := m.defaultStatus(ctx, rec, cc); err != nil {
			return err
		}
	}
	if stamp {
		m.stamp(ctx, rec)
	}
	return nil
}

func (m *Module) defaultStatus(ctx context.Context, rec crud.Record, cc string) error {
	if crud.Str(rec["status"]) == "" {
		def, err := m.one(ctx, "SELECT code FROM techobj__status WHERE company_code = ? AND is_default = ? AND is_active = ?", cc, true, true)
		if err != nil {
			return err
		}
		rec["status"] = nilIfEmpty(def)
	}
	return m.requireCatalog(ctx, "status", "Status", cc, rec["status"])
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

// match: Abschnitt mit Unter-Objekten desselben Buchungskreises.
func match(childField, masterField string) map[string]string {
	return map[string]string{"company_code": "company_code", childField: masterField}
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

// nextID: <Kürzel><nnnn> mit der kleinsten freien Nummer in table.column.
func (m *Module) nextID(ctx context.Context, table, column, cc, prefix string) (string, error) {
	res, err := m.db.Query(ctx, "SELECT "+column+" FROM "+table+" WHERE company_code = ? AND UPPER("+column+") LIKE ?", cc, strings.ToUpper(prefix)+"%")
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, r := range res.Rows {
		taken[strings.ToUpper(crud.Str(r[0]))] = true
	}
	for n := 1; n < 100000; n++ {
		id := fmt.Sprintf("%s%04d", prefix, n)
		if !taken[strings.ToUpper(id)] {
			return id, nil
		}
	}
	return "", crud.Invalid("keine freie ID für %s", prefix)
}

// newID: eingegebene ID prüfen (Zeichen, frei) oder die nächste freie vergeben.
// prefix ist das Kürzel selbst oder „@<Schlüssel>“ im Katalog „Kürzel der IDs“.
func (m *Module) newID(ctx context.Context, rec crud.Record, key, table, cc, prefix, label string) error {
	id := strings.TrimSpace(crud.Str(rec[key]))
	if id == "" {
		if code, ok := strings.CutPrefix(prefix, "@"); ok {
			p, err := m.prefixOf(ctx, cc, code)
			if err != nil {
				return err
			}
			prefix = p
		}
		next, err := m.nextID(ctx, table, key, cc, prefix)
		if err != nil {
			return err
		}
		rec[key] = next
		return nil
	}
	if !idRe.MatchString(id) {
		return crud.Invalid("%s %q: 2–30 Buchstaben, Ziffern, - und _", label, id)
	}
	taken, err := m.one(ctx, "SELECT 1 FROM "+table+" WHERE company_code = ? AND LOWER("+key+") = LOWER(?)", cc, id)
	if err != nil {
		return err
	}
	if taken != "" {
		return fmt.Errorf("%w: %s %s ist im Buchungskreis %s bereits vergeben", sdk.ErrAlreadyExists, label, id, cc)
	}
	rec[key] = id
	return nil
}

// overlap: gibt es in table eine Zeitscheibe mit where (ohne den Datensatz
// selbst, self), die from–to überschneidet? Liefert die erste als Text.
func (m *Module) overlap(ctx context.Context, table, cols, where string, from, to string, args ...any) ([]any, error) {
	q := "SELECT " + cols + ", valid_from, valid_to FROM " + table + " WHERE " + where + " AND valid_from <= ? AND valid_to >= ?"
	res, err := m.db.Query(ctx, q, append(args, to, from)...)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil
	}
	return res.Rows[0], nil
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
	if s[:10] == crud.DateMax {
		return "offen"
	}
	return s[8:10] + "." + s[5:7] + "." + s[0:4]
}

func setLabel(rec crud.Record, key, label string) {
	labels, _ := rec["_labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
		rec["_labels"] = labels
	}
	labels[key] = label
}

// partnerName: Name 1 und 2 des Partners; leer, wenn das Partnermodul fehlt
// oder der Benutzer den Partner nicht lesen darf.
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// create: Datensatz eines eigenen Objects mit allen Prüfungen anlegen (in einer
// laufenden Transaktion), ohne Umweg über den Host.
func (m *Module) create(ctx context.Context, object string, payload any) (sdk.Response, error) {
	return m.set.Entity(object).Create(ctx, payload)
}
