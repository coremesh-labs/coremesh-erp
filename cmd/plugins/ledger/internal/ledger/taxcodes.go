package ledger

import (
	"context"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Steuerkennzeichen (seit 0.22.0): Satz und Art der Steuer einer Position –
// abziehbare Vorsteuer (Netto auf das Aufwandskonto, Steuer auf das
// Vorsteuerkonto), nicht abziehbare Vorsteuer (brutto auf das Aufwandskonto,
// z. B. Vermietung nach § 4 Nr. 12 UStG), aufzuteilende Vorsteuer (seit 0.26.0:
// abziehbar im Anteil der steuerpflichtig genutzten Mietobjekte des Objekts der
// Buchungszeile, § 15 Abs. 4 UStG; Anteil nach Bemessungsart oder Anzahl), ohne
// Steuer, Umsatzsteuer. Für alle
// Buchungskreise; das Steuerkonto muss im buchenden Buchungskreis aktiv sein.
// Eingangsrechnungen (Beschaffung) wählen es je Position; Vorschlag aus
// Rechnungsvorlage, Vertragsart oder Rechnungskopf.

var taxKinds = []metamodel.Option{
	{Value: "INPUT", Label: "Vorsteuer abziehbar", Short: "VSt abziehbar"},
	{Value: "NONDEDUCTIBLE", Label: "Vorsteuer nicht abziehbar (brutto in den Aufwand)", Short: "VSt n. abz."},
	{Value: "PARTIAL", Label: "Vorsteuer aufzuteilen (abziehbar im Anteil steuerpflichtig genutzter Mietobjekte)", Short: "VSt aufzut."},
	{Value: "NONE", Label: "ohne Steuer", Short: "kein"},
	{Value: "OUTPUT", Label: "Umsatzsteuer"},
}

func (m *Module) taxCode() *crud.Entity {
	return &crud.Entity{
		Object: "TaxCode", Title: "Steuerkennzeichen", Icon: "icon-percent", Table: "ledger__tax_code", Section: "Einstellungen",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: taxKinds},
			{Key: "rate", Label: "Steuersatz in % (z. B. 19)", Type: tText, Required: true, Listable: true},
			{Key: "account_number", Label: "Steuerkonto (Vorsteuer bzw. Umsatzsteuer; nur bei abziehbarer, aufzuteilender Vorsteuer und Umsatzsteuer)", Type: tText, Listable: true},
			{Key: "split_measurement", Label: "Aufteilung nach Bemessungsart (nur aufzuteilende Vorsteuer; leer = Anzahl Mietobjekte)", Type: tText,
				Lookup: &metamodel.Lookup{Object: "MeasurementType", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			if rec["code"] == "" {
				return crud.Invalid("Schlüssel fehlt")
			}
			r := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["rate"])), ",", ".")
			v, err := strconv.ParseFloat(r, 64)
			if err != nil || v < 0 || v > 100 {
				return crud.Invalid("Steuersatz %q: Zahl von 0 bis 100", crud.Str(rec["rate"]))
			}
			rec["rate"] = r
			acc := strings.TrimSpace(crud.Str(rec["account_number"]))
			switch crud.Str(rec["kind"]) {
			case "INPUT", "OUTPUT", "PARTIAL":
				if acc == "" && v > 0 {
					return crud.Invalid("Steuerkonto ist Pflicht (abziehbare bzw. aufzuteilende Vorsteuer, Umsatzsteuer)")
				}
			default:
				acc = ""
			}
			rec["account_number"] = nilIfEmpty(acc)
			split := strings.ToUpper(strings.TrimSpace(crud.Str(rec["split_measurement"])))
			if crud.Str(rec["kind"]) != "PARTIAL" {
				split = ""
			}
			rec["split_measurement"] = nilIfEmpty(split)
			return nil
		},
	}
}

// Vorschlagswerte (SKR04: 1406 Vorsteuer 19 %, 1401 Vorsteuer 7 %, 3806/3801 Umsatzsteuer).
var seedTaxCodes = []map[string]any{
	{"code": "V19", "name": "Vorsteuer 19 % (abziehbar)", "kind": "INPUT", "rate": "19", "account_number": "1406"},
	{"code": "V7", "name": "Vorsteuer 7 % (abziehbar)", "kind": "INPUT", "rate": "7", "account_number": "1401"},
	{"code": "VN19", "name": "Vorsteuer 19 % nicht abziehbar (brutto in den Aufwand)", "kind": "NONDEDUCTIBLE", "rate": "19"},
	{"code": "VN7", "name": "Vorsteuer 7 % nicht abziehbar (brutto in den Aufwand)", "kind": "NONDEDUCTIBLE", "rate": "7"},
	{"code": "VA19", "name": "Vorsteuer 19 % aufzuteilen (abziehbar im Anteil steuerpflichtig genutzter Mietobjekte)", "kind": "PARTIAL", "rate": "19", "account_number": "1406"},
	{"code": "VA7", "name": "Vorsteuer 7 % aufzuteilen (abziehbar im Anteil steuerpflichtig genutzter Mietobjekte)", "kind": "PARTIAL", "rate": "7", "account_number": "1401"},
	{"code": "V0", "name": "ohne Steuer", "kind": "NONE", "rate": "0"},
	{"code": "U19", "name": "Umsatzsteuer 19 %", "kind": "OUTPUT", "rate": "19", "account_number": "3806"},
	{"code": "U7", "name": "Umsatzsteuer 7 %", "kind": "OUTPUT", "rate": "7", "account_number": "3801"},
}

// Mengeneinheiten (seit 0.25.0): Einheit der Menge einer Position (Vertrag, Angebot,
// Rechnung) mit Nachkommastellen der Menge. Für alle Buchungskreise.
func (m *Module) unitEntity() *crud.Entity {
	return &crud.Entity{
		Object: "Unit", Title: "Mengeneinheiten", Icon: "icon-ruler", Table: "ledger__unit", Section: "Einstellungen",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "decimals", Label: "Nachkommastellen der Menge", Short: "Dez.", Type: tNum, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			if rec["code"] == "" {
				return crud.Invalid("Schlüssel fehlt")
			}
			if d, err := strconv.Atoi(firstNonBlank(crud.Str(rec["decimals"]), "2")); err != nil || d < 0 || d > 6 {
				return crud.Invalid("Nachkommastellen: 0 bis 6")
			} else {
				rec["decimals"] = d
			}
			return nil
		},
	}
}

func firstNonBlank(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

var seedUnits = []map[string]any{
	{"code": "STK", "name": "Stück", "decimals": 0}, {"code": "PAU", "name": "pauschal", "decimals": 0},
	{"code": "H", "name": "Stunde", "decimals": 2}, {"code": "MON", "name": "Monat", "decimals": 0},
	{"code": "M", "name": "Meter", "decimals": 2}, {"code": "M2", "name": "Quadratmeter", "decimals": 2},
	{"code": "M3", "name": "Kubikmeter", "decimals": 3}, {"code": "KG", "name": "Kilogramm", "decimals": 3},
	{"code": "L", "name": "Liter", "decimals": 2}, {"code": "KWH", "name": "Kilowattstunde", "decimals": 0},
	{"code": "PCT", "name": "Prozent", "decimals": 2},
}

var roundingModes = []metamodel.Option{
	{Value: "HALF_UP", Label: "kaufmännisch (ab 5 aufrunden)"}, {Value: "HALF_EVEN", Label: "Bankrundung (auf gerade Ziffer)"},
	{Value: "UP", Label: "aufrunden"}, {Value: "DOWN", Label: "abrunden"},
}

// Rundungsregeln (seit 0.25.0): wie Gesamtpreise (Menge × Einzelpreis), nachgelagerte
// Konditionen (z. B. Steuer) und Beträge je Bemessung gerundet werden – Art,
// Schrittweite (0.01; 0.05 = Rappenrundung) und Nachkommastellen des Einzelpreises
// (z. B. 4 für Energie). Standard: Regel des Buchungskreises, sonst die mit „Standard“;
// Konditionsarten, Konditionen und Positionen können eine eigene wählen.
func (m *Module) roundingRule() *crud.Entity {
	return &crud.Entity{
		Object: "RoundingRule", Title: "Rundungsregeln", Icon: "icon-sliders", Table: "ledger__rounding_rule", Section: "Einstellungen",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "mode", Label: "Art", Type: tSel, Required: true, Listable: true, Options: roundingModes},
			{Key: "increment", Label: "Schrittweite (z. B. 0.01; 0.05 = Rappenrundung)", Type: tText, Required: true, Listable: true},
			{Key: "price_decimals", Label: "Nachkommastellen des Einzelpreises", Short: "Dez. Preis", Type: tNum, Listable: true},
			{Key: "country", Label: "Land (ISO-2, zur Information)", Type: tText, Listable: true},
			{Key: "is_default", Label: "Standard (ohne Regel am Buchungskreis)", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, _ crud.Record) error {
			rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
			if rec["code"] == "" {
				return crud.Invalid("Schlüssel fehlt")
			}
			inc := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["increment"])), ",", ".")
			if v, err := strconv.ParseFloat(inc, 64); err != nil || v <= 0 || v > 1000 {
				return crud.Invalid("Schrittweite %q: Zahl größer 0", crud.Str(rec["increment"]))
			}
			rec["increment"] = inc
			if d, err := strconv.Atoi(firstNonBlank(crud.Str(rec["price_decimals"]), "2")); err != nil || d < 0 || d > 6 {
				return crud.Invalid("Nachkommastellen des Einzelpreises: 0 bis 6")
			} else {
				rec["price_decimals"] = d
			}
			rec["country"] = nilIfEmpty(strings.ToUpper(strings.TrimSpace(crud.Str(rec["country"]))))
			if rec["is_default"] == nil {
				rec["is_default"] = false
			}
			return nil
		},
	}
}

// Vorschläge: kaufmännisch auf den Cent (DE: Rechnung und Umsatzsteuer, § 14 UStG,
// Abschn. 14.5 UStAE), mit 4 Stellen im Einzelpreis (Energie, Kraftstoff),
// Rappenrundung (CH), Bankrundung, Abrunden.
var seedRoundingRules = []map[string]any{
	{"code": "KFM", "name": "kaufmännisch auf 0,01 (Rechnung, Umsatzsteuer)", "mode": "HALF_UP", "increment": "0.01", "price_decimals": 2, "country": "DE", "is_default": true},
	{"code": "KFM4", "name": "kaufmännisch, Einzelpreis mit 4 Stellen (Energie, Kraftstoff)", "mode": "HALF_UP", "increment": "0.01", "price_decimals": 4, "country": "DE", "is_default": false},
	{"code": "CH05", "name": "Rappenrundung auf 0,05 (Schweiz)", "mode": "HALF_UP", "increment": "0.05", "price_decimals": 2, "country": "CH", "is_default": false},
	{"code": "BANK", "name": "Bankrundung auf 0,01", "mode": "HALF_EVEN", "increment": "0.01", "price_decimals": 2, "is_default": false},
	{"code": "AB", "name": "abrunden auf 0,01", "mode": "DOWN", "increment": "0.01", "price_decimals": 2, "is_default": false},
}
