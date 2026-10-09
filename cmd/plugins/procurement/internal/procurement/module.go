// Package procurement ist die Beschaffung des ERP: Angebote und
// Eingangsrechnungen von Handwerkern und Dienstleistern, gebucht über die
// Vorerfassung des Hauptbuchs (Plugin ledger).
//
//   - Kataloge je Buchungskreis: Rechnungsarten (Belegart im Hauptbuch,
//     Lieferantenrolle, Nummernkreis, automatisch buchen) und Kontierung der
//     Objekte. Kostenarten (Sachkonto, umlagefähig) kommen aus dem Modul
//     Betriebskosten (Plugin opcost, Object CostCategory).
//   - Angebot (PurchaseQuote): Lieferant, Objekt, Betrag, gültig bis;
//     annehmen oder ablehnen.
//   - Eingangsrechnung (SupplierInvoice) mit Positionen (SupplierInvoiceItem):
//     Kostenart, Sachkonto, Betrag, Objekt, umlagefähig, Leistungszeitraum –
//     Grundlage der Nebenkostenabrechnung.
//   - „Buchen …“: Vorerfassung anlegen (Aufwand je Position an das
//     Kreditorenkonto des Lieferanten aus seinen Buchungskreisdaten), prüfen,
//     bei „automatisch buchen“ buchen. Gebucht oder verworfen im Hauptbuch:
//     SystemEvents JournalDraft.post/deactivate.
//   - „Stornieren“: erfasst → storniert, vorerfasst → Vorerfassung verwerfen,
//     gebucht → Storno im Hauptbuch.
//
// Beträge sind brutto in der kleinsten Einheit der Währung (Umsatzsteuer folgt).
package procurement

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"log/slog"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

// Name ist der Namensraum des Moduls (/m/procurement).
const Name = "procurement"

// Nummernkreise (numrange).
const (
	rangeQuote   = "PurchaseQuote"
	rangeInvoice = "SupplierInvoice"
	setupObject  = "ProcurementSetup"
)

// Module ist die Beschaffung.
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
	es := []*crud.Entity{m.quote(), m.invoice(), m.invoiceItem(), m.invoiceType(), m.objectPosting()}
	for _, e := range es {
		m.withLabels(e)
	}
	return es
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Beschaffung", Icon: "icon-cart",
		Description: "Angebote und Eingangsrechnungen von Handwerkern und Dienstleistern"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.registerChartChange(r)
	m.set.Register(r, "Beschaffung")
	m.registerPosting(r)
	r.Object(setupObject).Handle("setupCompany", m.setupCompanyAction)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Rechnungsarten und Kostenarten eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	ledgerapi.SubscribeChartChange(ctx, m.services, m.log, chartChangeCallback, "Beschaffung")
	m.set.Bind(env.DB)
	for _, d := range []numrange.Definition{
		{Object: rangeQuote, Owner: Name, Description: "Angebotsnummern", PerCompanyCode: true, PerYear: true,
			Pattern: "AN-{YYYY}-{N}", Width: 5},
		{Object: rangeInvoice, Owner: Name, Description: "Nummern der Eingangsrechnungen (Intervallschlüssel = Nummernkreis der Rechnungsart)",
			PerCompanyCode: true, PerYear: true, Pattern: "{KEY}-{YYYY}-{N}", Width: 5},
	} {
		if err := numrange.Define(ctx, env.Services, d); err != nil {
			m.log.WarnContext(ctx, "Nummernkreis nicht angemeldet", "object", d.Object, "err", err.Error())
		}
	}
	m.subscribePosting(ctx)
	m.log.InfoContext(ctx, "Modul bereit", "database", env.DB.Name())
	return nil
}

func (m *Module) Shutdown(context.Context) error { return nil }

func (m *Module) Schema() module.Schema { return module.Schema{HCL: schemaHCL, Seed: nil} }

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

var (
	lookupCC          = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupPartner     = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"search_term", "name1"}}
	objectTypeOptions = []metamodel.Option{
		{Value: "RentObject", Label: "Mietobjekt"}, {Value: "Building", Label: "Gebäude"}, {Value: "BusinessEntity", Label: "Wirtschaftseinheit"}}
	lookupAccount = &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
		Columns: []string{"account_number", "account_name", "account_type"}, Filters: map[string]string{"company_code_id": "company_code"}}
	amountRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

// catalogLookup: Auswahl aus einem Katalog des Buchungskreises.
func catalogLookup(object string) *metamodel.Lookup {
	return &metamodel.Lookup{Object: object, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
}

// requireWrite: Ändern im Buchungskreis (object.action); Lesen prüft crud.
func requireWrite(ctx context.Context, object, action, cc string) error {
	ok, err := sdk.CheckAccess(ctx, object, action, cc)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, object, action, cc)
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

// labels: "_labels" eines Datensatzes (angelegt, wenn nötig).
func labels(rec crud.Record) map[string]any {
	l, _ := rec["_labels"].(map[string]any)
	if l == nil {
		l = map[string]any{}
		rec["_labels"] = l
	}
	return l
}

// defaults setzt leere Felder auf Standardwerte.
func defaults(rec map[string]any, values map[string]any) {
	for k, v := range values {
		if rec[k] == nil || crud.Str(rec[k]) == "" {
			rec[k] = v
		}
	}
}

func unavailable(what string, err error) error {
	if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable) {
		return crud.Invalid("%s nicht verfügbar: %v", what, err)
	}
	return err
}

// currencyDecimals: Nachkommastellen der Währung (Hauptbuch; Standard 2).
func (m *Module) currencyDecimals(ctx context.Context, code string) int {
	resp, err := m.services.Call(ctx, "Currency", "get", map[string]any{"id": code})
	if err != nil {
		return 2
	}
	var cur struct {
		Decimals any `json:"decimals"`
	}
	if sdk.Decode(resp.Payload, &cur) != nil || cur.Decimals == nil {
		return 2
	}
	return int(toInt(cur.Decimals))
}

// parseAmount: "1.250,50" / "1250.5" → kleinste Einheit.
func parseAmount(s string, decimals int) (int64, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if strings.Contains(t, ",") {
		t = strings.ReplaceAll(strings.ReplaceAll(t, ".", ""), ",", ".")
	}
	if !amountRe.MatchString(t) {
		return 0, fmt.Errorf("Betrag %q: Dezimalzahl erwartet", s)
	}
	neg := strings.HasPrefix(t, "-")
	whole, frac, _ := strings.Cut(strings.TrimPrefix(t, "-"), ".")
	if len(frac) > decimals {
		return 0, fmt.Errorf("Betrag %q: höchstens %d Nachkommastellen", s, decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("Betrag %q ist zu groß", s)
	}
	if neg {
		return -n.Int64(), nil
	}
	return n.Int64(), nil
}

// formatAmount: kleinste Einheit → Dezimaltext ("1250.50").
func formatAmount(minor int64, decimals int) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	s := fmt.Sprintf("%0*d", decimals+1, minor)
	if decimals == 0 {
		return sign + s
	}
	return sign + s[:len(s)-decimals] + "." + s[len(s)-decimals:]
}

// date: Pflichtdatum normalisieren.
func date(rec crud.Record, key, label string) (string, error) {
	if crud.Str(rec[key]) == "" {
		return "", crud.Invalid("%s ist Pflicht", label)
	}
	d, err := crud.ParseDate(rec[key])
	if err != nil {
		return "", err
	}
	rec[key] = d
	return d, nil
}

// optDate: optionales Datum normalisieren (leer = NULL).
func optDate(rec crud.Record, key string) error {
	if crud.Str(rec[key]) == "" {
		rec[key] = nil
		return nil
	}
	d, err := crud.ParseDate(rec[key])
	if err != nil {
		return err
	}
	rec[key] = d
	return nil
}
