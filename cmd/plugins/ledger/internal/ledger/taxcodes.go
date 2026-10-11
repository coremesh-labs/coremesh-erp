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
// z. B. Vermietung nach § 4 Nr. 12 UStG), ohne Steuer, Umsatzsteuer. Für alle
// Buchungskreise; das Steuerkonto muss im buchenden Buchungskreis aktiv sein.
// Eingangsrechnungen (Beschaffung) wählen es je Position; Vorschlag aus
// Rechnungsvorlage, Vertragsart oder Rechnungskopf.

var taxKinds = []metamodel.Option{
	{Value: "INPUT", Label: "Vorsteuer abziehbar"},
	{Value: "NONDEDUCTIBLE", Label: "Vorsteuer nicht abziehbar (brutto in den Aufwand)"},
	{Value: "NONE", Label: "ohne Steuer"},
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
			{Key: "account_number", Label: "Steuerkonto (Vorsteuer bzw. Umsatzsteuer; nur bei abziehbarer Vorsteuer und Umsatzsteuer)", Type: tText, Listable: true},
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
			case "INPUT", "OUTPUT":
				if acc == "" && v > 0 {
					return crud.Invalid("Steuerkonto ist Pflicht (abziehbare Vorsteuer bzw. Umsatzsteuer)")
				}
			default:
				acc = ""
			}
			rec["account_number"] = nilIfEmpty(acc)
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
	{"code": "V0", "name": "ohne Steuer", "kind": "NONE", "rate": "0"},
	{"code": "U19", "name": "Umsatzsteuer 19 %", "kind": "OUTPUT", "rate": "19", "account_number": "3806"},
	{"code": "U7", "name": "Umsatzsteuer 7 %", "kind": "OUTPUT", "rate": "7", "account_number": "3801"},
}
