package contract

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Konditionen: Betrag (kleinste Einheit der Währung), Berechnung (fest oder je
// Einheit einer Bemessung), Rhythmus, Fälligkeit, Sachkonto aus der
// Kontenfindung der Vertragsart. Eine Anpassung beendet die Zeitscheibe und
// legt eine neue an.

var (
	calcOptions = []metamodel.Option{
		{Value: "FIXED", Label: "fester Betrag"}, {Value: "PER_UNIT", Label: "je Einheit der Bemessung (z. B. je m² Wohnfläche)"}}
	frequencyOptions = []metamodel.Option{
		{Value: "MONTHLY", Label: "monatlich"}, {Value: "QUARTERLY", Label: "vierteljährlich"}, {Value: "HALF_YEARLY", Label: "halbjährlich"},
		{Value: "YEARLY", Label: "jährlich"}, {Value: "ONCE", Label: "einmalig"}}
	paymentModeOptions = []metamodel.Option{
		{Value: "IN_ADVANCE", Label: "vorschüssig"}, {Value: "IN_ARREARS", Label: "nachschüssig"}}

	amountRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

func (m *Module) condition() *crud.Entity {
	return &crud.Entity{
		Object: "ContractCondition", Title: "Konditionen", Icon: "icon-coins", Table: "contract__condition", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "condition_type", "object_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, contract_id, condition_type, object_id, valid_from",
		Filters: []string{"company_code", "contract_id", "condition_type", "object_id", "account_number"},
		Events:  true, CompanyCodeField: "company_code",
		EventFields: []string{"contract_id", "contract_type", "condition_type", "object_id", "amount", "frequency", "due_day", "due_date", "account_number", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "contract_type", Label: "Vertragsart", Type: tText, ReadOnly: true, Lookup: catalogLookup("ContractType")},
			crud.Field{Key: "condition_type", Label: "Konditionsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ConditionType")},
			crud.Field{Key: "object_id", Label: "Objekt (leer = ganzer Vertrag)", Type: tText, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "ContractObject", ValueField: "object_id", LabelFields: []string{"object_id"},
					Filters: map[string]string{"company_code": "company_code", "contract_id": "contract_id"}}},
			crud.Field{Key: "calc_method", Label: "Berechnung", Type: tSel, Required: true, Options: calcOptions, Group: "Betrag"},
			crud.Field{Key: "amount", Label: "Betrag bzw. Preis je Einheit", Type: tText, Required: true, Listable: true, Group: "Betrag"},
			crud.Field{Key: "measurement_type", Label: "Bemessungsart (bei je Einheit, z. B. WFL)", Type: tText, Group: "Betrag",
				Lookup: &metamodel.Lookup{Object: "MeasurementType", ValueField: "code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code"}}},
			crud.Field{Key: "frequency", Label: "Rhythmus", Type: tSel, Required: true, Listable: true, Options: frequencyOptions, Group: "Fälligkeit",
				Trigger: true},
			crud.Field{Key: "due_day", Label: "Fällig am … Tag", Type: tNum, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"MONTHLY", "QUARTERLY", "HALF_YEARLY", "YEARLY"}}},
			crud.Field{Key: "due_date", Label: "Fällig am (leer = Beginn)", Type: tDate, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"ONCE"}}},
			crud.Field{Key: "payment_mode", Label: "Zahlungsweise", Type: tSel, Required: true, Options: paymentModeOptions, Group: "Fälligkeit"},
			crud.Field{Key: "account_number", Label: "Sachkonto (leer = Standard der Kontenfindung)", Type: tText, Listable: true, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "ContractAccount", ValueField: "account_number", LabelFields: []string{"account_name"},
					Filters: map[string]string{"company_code": "company_code", "contract_type": "contract_type", "condition_type": "condition_type"}}},
			crud.Field{Key: "payer_id", Label: "Abweichender Zahler", Type: tText, Group: "Buchung",
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code", Fields: []string{"contract_type"}},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Contract", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkCondition,
		Decorate: m.decorateCondition,
	}
}

// conditionTypeOf: Konditionsart (aktiv) – Name und Forderungsklasse.
func (m *Module) conditionTypeOf(ctx context.Context, cc, code string) (name string, err error) {
	res, err := m.db.Query(ctx, "SELECT name FROM contract__condition_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Konditionsart %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	return crud.Str(res.Rows[0][0]), nil
}

func (m *Module) checkCondition(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	rec["contract_type"] = c.Type
	if old == nil {
		rec["condition_type"] = trimUpper(rec["condition_type"])
		rec["object_id"] = strings.TrimSpace(crud.Str(rec["object_id"]))
	}
	if err := within("Kondition", crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"]), c); err != nil {
		return err
	}
	typ := crud.Str(rec["condition_type"])
	if _, err := m.conditionTypeOf(ctx, cc, typ); err != nil {
		return err
	}
	if obj := crud.Str(rec["object_id"]); obj != "" {
		res, err := m.db.Query(ctx, `SELECT 1 FROM contract__object WHERE company_code = ? AND contract_id = ? AND object_id = ?
			AND valid_from <= ? AND valid_to >= ?`, cc, id, obj, rec["valid_to"], rec["valid_from"])
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Objekt %s gehört im Zeitraum nicht zum Vertrag %s", obj, id)
		}
	}
	// Betrag in der kleinsten Einheit der Vertragswährung.
	decimals := m.currencyDecimals(ctx, c.Currency)
	minor, err := parseAmount(crud.Str(rec["amount"]), decimals)
	if err != nil {
		return crud.Invalid("%v", err)
	}
	rec["amount"] = minor
	switch crud.Str(rec["calc_method"]) {
	case "PER_UNIT":
		if crud.Str(rec["object_id"]) == "" {
			return crud.Invalid("Berechnung je Einheit braucht ein Objekt (dessen Bemessung zählt)")
		}
		if crud.Str(rec["measurement_type"]) == "" {
			return crud.Invalid("Berechnung je Einheit braucht die Bemessungsart, z. B. WFL")
		}
		rec["measurement_type"] = trimUpper(rec["measurement_type"])
	default:
		rec["calc_method"], rec["measurement_type"] = "FIXED", nil
	}
	// Fälligkeitsdatum nur bei einmaligen Konditionen, nicht vor dem Beginn.
	if crud.Str(rec["frequency"]) != "ONCE" || crud.Str(rec["due_date"]) == "" {
		rec["due_date"] = nil
	} else if due, err := crud.ParseDate(rec["due_date"]); err != nil {
		return err
	} else if from, err := crud.ParseDate(rec["valid_from"]); err == nil && due < from {
		return crud.Invalid("Fällig am %s liegt vor dem Beginn der Kondition", due)
	} else {
		rec["due_date"] = due
	}
	if d := toInt(rec["due_day"]); rec["due_day"] == nil || crud.Str(rec["due_day"]) == "" {
		rec["due_day"] = 1
	} else if d < 1 || d > 31 {
		return crud.Invalid("Fälligkeitstag 1 bis 31")
	}
	return m.accountFor(ctx, rec, c)
}

// accountFor: Sachkonto aus der Kontenfindung (Vertragsart, Konditionsart).
// Ist dort etwas gepflegt, muss das Konto dazu gehören; leer = Standard.
func (m *Module) accountFor(ctx context.Context, rec crud.Record, c *contractRow) error {
	res, err := m.db.Query(ctx, `SELECT account_number, is_default FROM contract__account
		WHERE company_code = ? AND contract_type = ? AND condition_type = ? AND is_active = ? ORDER BY account_number`,
		c.CompanyCode, c.Type, rec["condition_type"], true)
	if err != nil {
		return err
	}
	acc := strings.TrimSpace(crud.Str(rec["account_number"]))
	if len(res.Rows) == 0 {
		if acc != "" {
			nr, _, err := m.glAccount(ctx, c.CompanyCode, acc)
			if err != nil {
				return err
			}
			acc = nr
		}
		rec["account_number"] = nilIfEmpty(acc)
		return nil
	}
	var allowed []string
	for _, r := range res.Rows {
		a := crud.Str(r[0])
		allowed = append(allowed, a)
		if acc == "" && crud.AsBool(r[1]) {
			acc = a
		}
	}
	if acc == "" && len(allowed) == 1 {
		acc = allowed[0]
	}
	if acc == "" {
		return crud.Invalid("Sachkonto wählen (kein Standard in der Kontenfindung): %s", strings.Join(allowed, ", "))
	}
	for _, a := range allowed {
		if sameAccount(a, acc) {
			rec["account_number"] = a
			return nil
		}
	}
	return crud.Invalid("Sachkonto %s ist für %s/%s nicht vorgesehen (Kontenfindung: %s)", acc, c.Type, crud.Str(rec["condition_type"]), strings.Join(allowed, ", "))
}

// decorateCondition: Betrag als Dezimaltext in der Vertragswährung.
func (m *Module) decorateCondition(ctx context.Context, rec crud.Record) error {
	c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
	if err != nil {
		return nil
	}
	rec["amount"] = formatAmount(toInt(rec["amount"]), m.currencyDecimals(ctx, c.Currency))
	labels(rec)["amount"] = crud.Str(rec["amount"]) + " " + c.Currency
	return nil
}

// currencyDecimals: Nachkommastellen der Währung aus dem Hauptbuch (Standard 2).
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

// parseAmount: "1.250,50" / "1250.5" → kleinste Einheit; höchstens decimals
// Nachkommastellen.
func parseAmount(s string, decimals int) (int64, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if strings.Contains(t, ",") { // deutsches Format: Punkt = Tausender, Komma = Dezimal
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
