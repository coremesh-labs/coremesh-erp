// Package bank ist das Bankmodul des ERP: Kontoumsätze einlesen, den
// Vorgängen zuordnen und im Hauptbuch buchen.
//
//   - Bankkonto (BankAccount): IBAN, Sachkonto, Belegarten, Importformat; je
//     Konto die Umsätze (BankTransaction) und die Einlesungen (BankImport).
//   - Importformat (BankImportFormat, BankImportColumn): Spalten einer
//     CSV-Datei den Feldern des Umsatzes zuweisen, mit Aufbereitung (Datum,
//     Betrag, Vorzeichen, Auszug per regulärem Ausdruck). „Kontoauszug
//     einlesen …“ am Bankkonto lädt die Datei über die Webseite hoch;
//     doppelte Umsätze erkennt ein Fingerabdruck.
//   - Zuordnung (BankAccount „Zuordnen“): bestätigte Regeln, Vertrags- bzw.
//     Partnernummer im Verwendungszweck, offene Eingangsrechnung, IBAN der
//     Gegenseite, Name der Gegenseite (nur Vorschlag). Von Hand zugeordnet
//     („Zuordnen …“ am Umsatz) lernt das System eine Regel (BankRule) – sie
//     wirkt erst, wenn sie bestätigt ist.
//   - Buchen (BankAccount „Buchen …“) in der Reihenfolge der Zahlungen
//     (Buchungstag, dann Reihenfolge der Datei): je Umsatz ein Beleg Bank an
//     Partnerkonto (Vertrag, Partner, Eingangsrechnung) bzw. Sachkonto. Der
//     Umsatz merkt sich Beleg und Buchungssätze (Rechenweg).
package bank

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
)

// Name ist der Namensraum des Moduls (/m/bank).
const Name = "bank"

const (
	accountObject = "BankAccount"
	formatObject  = "BankImportFormat"
	columnObject  = "BankImportColumn"
	importObject  = "BankImport"
	txnObject     = "BankTransaction"
	ruleObject    = "BankRule"
	setupObject   = "BankSetup"
)

// Status eines Umsatzes.
const (
	stOpen     = "OPEN"     // nicht zugeordnet
	stProposed = "PROPOSED" // Vorschlag (z. B. nach Name) – prüfen und übernehmen
	stMatched  = "MATCHED"  // zugeordnet, buchbar
	stPosted   = "POSTED"
	stIgnored  = "IGNORED"
)

// Ziel einer Zuordnung.
const (
	tgContract = "CONTRACT"
	tgPartner  = "PARTNER"
	tgInvoice  = "INVOICE"
	tgAccount  = "ACCOUNT"
)

// Module ist das Bankmodul.
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
	m.set = crud.NewSet(m.account(), m.format(), m.column(), m.importRun(), m.transaction(), m.rule())
	return m
}

func (m *Module) Descriptor() module.Descriptor {
	return module.Descriptor{Name: Name, Title: "Bank", Icon: "icon-bank",
		Description: "Kontoumsätze einlesen, zuordnen und buchen"}
}

func (m *Module) RegisterRoutes(r *module.Router) {
	m.registerChartChange(r)
	m.set.Register(r, "Bank")
	r.Object(setupObject).Handle("setupCompany", m.setupCompanyAction).Handle("import", m.importCommand)
	r.Command(metamodel.CommandDefinition{Name: "setup-company", Object: setupObject, Action: "setupCompany",
		Description: "Importformate eines Buchungskreises mit Vorschlagswerten anlegen (fehlende Einträge)",
		Params:      []metamodel.CommandParam{{Name: "company", Required: true}}})
	r.Command(metamodel.CommandDefinition{Name: "import", Object: setupObject, Action: "import",
		Description: "Kontoauszug (CSV) einlesen",
		Params: []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "account", Required: true},
			{Name: "format", Description: "Importformat (leer = Vorschlag des Bankkontos)"}, {Name: "file", Required: true, File: true}}})
}

func (m *Module) Initialize(ctx context.Context, env module.Env) error {
	m.db, m.services, m.log = env.DB, env.Services, env.Log
	ledgerapi.SubscribeChartChange(ctx, m.services, m.log, chartChangeCallback, "Bank")
	m.set.Bind(env.DB)
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
	tFile = metamodel.TypeFile
)

var (
	lookupCC       = &metamodel.Lookup{Object: "CompanyCode", ValueField: "code", LabelFields: []string{"description"}}
	lookupPartner  = &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"search_term", "name1"}}
	lookupContract = &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupInvoice = &metamodel.Lookup{Object: "SupplierInvoice", ValueField: "invoice_id", LabelFields: []string{"supplier_reference"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupAccount = &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
		Columns: []string{"account_number", "account_name", "account_type"}, Filters: map[string]string{"company_code_id": "company_code"}}
	lookupRole   = &metamodel.Lookup{Object: "PartnerRoleType", ValueField: "code", LabelFields: []string{"description"}}
	lookupFormat = &metamodel.Lookup{Object: formatObject, ValueField: "code", LabelFields: []string{"name"},
		Filters: map[string]string{"company_code": "company_code"}}
	lookupDocType = &metamodel.Lookup{Object: "DocumentType", ValueField: "code", LabelFields: []string{"name"}}
	amountRe      = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

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

// parseDecimal: Dezimaltext → kleinste Einheit; sep ist das Dezimaltrennzeichen
// ("," oder "."), das andere gilt als Tausendertrennzeichen.
func parseDecimal(s, sep string, decimals int) (int64, error) {
	t := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), " ", ""), "\u00a0", "")
	if sep == "," {
		t = strings.ReplaceAll(strings.ReplaceAll(t, ".", ""), ",", ".")
	} else {
		t = strings.ReplaceAll(t, ",", "")
	}
	if strings.HasPrefix(t, "+") {
		t = t[1:]
	}
	if strings.HasSuffix(t, "-") { // nachgestelltes Minus (manche Banken)
		t = "-" + strings.TrimSuffix(t, "-")
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

// glAccount: Sachkonto im Buchungskreis (Hauptbuch), nicht gesperrt – Nummer mit Kontenplan-Präfix.
func (m *Module) glAccount(ctx context.Context, cc, number string) (string, error) {
	resp, err := m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": cc, "account_number": number}})
	if err != nil {
		return "", unavailable("Hauptbuch", err)
	}
	var out struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
			IsBlocked     bool   `json:"is_blocked"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	for _, it := range out.Items {
		if sameAccount(it.AccountNumber, number) {
			if it.IsBlocked {
				return "", crud.Invalid("Sachkonto %s ist im Buchungskreis %s gesperrt", number, cc)
			}
			return it.AccountNumber, nil
		}
	}
	return "", crud.Invalid("Sachkonto %s gibt es im Buchungskreis %s nicht", number, cc)
}

// sameAccount: gleiche Nummer, mit oder ohne Kontenplan-Präfix (SKR25-6000 = 6000).
func sameAccount(a, b string) bool {
	a, b = strings.ToUpper(strings.TrimSpace(a)), strings.ToUpper(strings.TrimSpace(b))
	return a == b || strings.HasSuffix(a, "-"+b) || strings.HasSuffix(b, "-"+a)
}

// ibanValid: Prüfziffer nach ISO 13616 (mod 97 = 1).
func ibanValid(iban string) bool {
	if len(iban) < 15 {
		return false
	}
	var digits strings.Builder
	for _, r := range iban[4:] + iban[:4] {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

// companyOf: Buchungskreis aus der Konsole (company als Text oder Zahl) bzw. dem Formular.
func companyOf(payload any) string {
	var in struct {
		Company     any `json:"company"`
		CompanyCode any `json:"company_code"`
		Data        struct {
			CompanyCode any `json:"company_code"`
		} `json:"data"`
	}
	_ = sdk.Decode(payload, &in)
	for _, v := range []any{in.Company, in.CompanyCode, in.Data.CompanyCode} {
		if s := strings.TrimSpace(crud.Str(v)); s != "" && s != "<nil>" {
			return s
		}
	}
	return ""
}
