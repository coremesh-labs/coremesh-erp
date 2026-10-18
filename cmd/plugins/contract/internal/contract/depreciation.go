package contract

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Abschreibung (AfA) von Häusern und Wohnungen am Vertrag (seit 0.11.0), z. B.
// Vertragsart KV „Kaufvertrag Immobilie“: Vertragspartner ist der Verkäufer,
// Objekt das Haus, die Wohnung oder die Wirtschaftseinheit.
//
//   - ContractDepreciation (Zeitscheiben): Bemessungsgrundlage Gebäude
//     (kumuliert – eine neue Zeitscheibe mit höherem Wert sind nachträgliche
//     Herstellungskosten), Anteil Grund und Boden (zur Information, wird nicht
//     abgeschrieben), Methode und Satz, Beginn, Restbuchwert bei Übernahme,
//     Anlage- und Abschreibungskonto, Konditionsart des Vermerks (AF).
//
// Den Plan rechnet contract-billing (Billing.Depreciation): jährlich zum
// 31.12. bzw. Vertragsende, monatsgenau; gebucht wird Abschreibungskonto an
// Anlagekonto ohne Partnerzeile (Belegart SA). Aktion „Abschreibungsplan“.

const depreciationObject = "ContractDepreciation"

var depMethodOptions = []metamodel.Option{
	{Value: "LINEAR", Label: "linear (Bemessungsgrundlage × Satz)"},
	{Value: "DEGRESSIVE", Label: "degressiv (Buchwert × Satz)"}}

func (m *Module) depreciation() *crud.Entity {
	return &crud.Entity{
		Object: depreciationObject, Title: "Abschreibung", Icon: "icon-trending-down", Table: "contract__depreciation", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "valid_from"}, TimeSlice: true, Order: "company_code, contract_id, valid_from",
		Filters: []string{"company_code", "contract_id"},
		Events:  true, CompanyCodeField: "company_code", // contract-billing hält eine Kopie
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "base_amount", Label: "Bemessungsgrundlage Gebäude (Anschaffungs-/Herstellungskosten, kumuliert)", Type: tText,
				Required: true, Listable: true, Group: "Abschreibung"},
			crud.Field{Key: "land_amount", Label: "Anteil Grund und Boden (wird nicht abgeschrieben)", Type: tText, Listable: true, Group: "Abschreibung"},
			crud.Field{Key: "method", Label: "Methode", Type: tSel, Required: true, Listable: true, Options: depMethodOptions, Group: "Abschreibung"},
			crud.Field{Key: "rate", Label: "Satz % p. a. (z. B. 2, 2,5, 3)", Type: tText, Required: true, Listable: true, Group: "Abschreibung"},
			crud.Field{Key: "start_date", Label: "Beginn der Abschreibung (leer = Gültig ab)", Type: tDate, Group: "Abschreibung"},
			crud.Field{Key: "opening_value", Label: "Restbuchwert zum Beginn (Übernahme; leer = Bemessungsgrundlage)", Type: tText, Group: "Abschreibung"},
			crud.Field{Key: "asset_account", Label: "Anlagekonto (Gebäude)", Type: tText, Required: true, Group: "Buchung", Lookup: glLookup},
			crud.Field{Key: "expense_account", Label: "Abschreibungskonto", Type: tText, Required: true, Group: "Buchung", Lookup: glLookup},
			crud.Field{Key: "condition_type", Label: "Konditionsart (Vermerk)", Type: tText, Group: "Buchung", Lookup: catalogLookup("ConditionType")},
		),
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkDepreciation,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "base_amount", "land_amount", "opening_value")
		},
	}
}

// checkDepreciation: Beträge in der kleinsten Einheit, Satz als Dezimaltext,
// Konten im Buchungskreis, Konditionsart (Vorschlag AF).
func (m *Module) checkDepreciation(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	c, err := m.contractOf(ctx, cc, crud.Str(rec["contract_id"]))
	if err != nil {
		return err
	}
	if err := within("Abschreibung", crud.Str(rec["valid_from"]), endAtContract(rec, c), c); err != nil {
		return err
	}
	decimals := m.currencyDecimals(ctx, c.Currency)
	for _, k := range []string{"base_amount", "land_amount", "opening_value"} {
		if crud.Str(rec[k]) == "" {
			if k == "base_amount" {
				return crud.Invalid("Bemessungsgrundlage ist Pflicht")
			}
			rec[k] = nil
			continue
		}
		v, err := parseAmount(crud.Str(rec[k]), decimals)
		if err != nil {
			return crud.Invalid("%s: %v", k, err)
		}
		if v < 0 || (k == "base_amount" && v == 0) {
			return crud.Invalid("%s: Betrag größer 0", k)
		}
		rec[k] = v
	}
	rate := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["rate"])), ",", ".")
	if !rateRe.MatchString(rate) {
		return crud.Invalid("Satz: Prozent p. a., z. B. 2 oder 2,5")
	}
	rec["rate"] = rate
	if crud.Str(rec["start_date"]) == "" {
		rec["start_date"] = nil
	} else if rec["start_date"], err = crud.ParseDate(rec["start_date"]); err != nil {
		return err
	}
	for _, k := range []string{"asset_account", "expense_account"} {
		nr, _, err := m.glAccount(ctx, cc, crud.Str(rec[k]))
		if err != nil {
			return err
		}
		rec[k] = nr
	}
	code := trimUpper(rec["condition_type"])
	if code == "" {
		code = "AF"
	}
	if _, err := m.conditionTypeOf(ctx, cc, code); err != nil {
		return err
	}
	rec["condition_type"] = code
	return nil
}

// depreciationScheduleAction: „Abschreibungsplan“ am Vertrag – rechnet contract-billing.
func (m *Module) depreciationScheduleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	return m.billingCall(ctx, "depreciationSchedule", map[string]any{"company_code": key["company_code"], "contract_id": key["contract_id"]})
}
