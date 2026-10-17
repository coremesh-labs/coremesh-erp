package contract

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
)

// Konditionen: Gegenstand (was) und Konditionsart (Verhalten, seit 0.26.0), Betrag
// (kleinste Einheit der Währung), Berechnung (fest oder je Einheit einer Bemessung),
// Rhythmus, Fälligkeit, Sachkonto aus der Kontenfindung (Vertragsart × Gegenstand ×
// Konditionsart) – ohne gesichertes Sachkonto lässt sich keine Kondition speichern.
// Eine Anpassung beendet die Zeitscheibe und legt eine neue an.

var (
	calcOptions = []metamodel.Option{
		{Value: calcFixed, Label: "Menge × Einzelpreis", Short: "Menge × Preis"}, {Value: calcPerUnit, Label: "je Einheit der Bemessung (z. B. je m² Wohnfläche)", Short: "je Einheit"},
		{Value: calcDependent, Label: "nachgelagert (aus anderen Konditionen, z. B. Umsatzsteuer, Materialanteil)", Short: "nachgelagert"}}
	frequencyOptions = []metamodel.Option{
		{Value: "MONTHLY", Label: "monatlich"}, {Value: "BIMONTHLY", Label: "zweimonatlich"}, {Value: "QUARTERLY", Label: "vierteljährlich", Short: "viertelj."},
		{Value: "HALF_YEARLY", Label: "halbjährlich"},
		{Value: "YEARLY", Label: "jährlich"}, {Value: "ONCE", Label: "einmalig"}}
	paymentModeOptions = []metamodel.Option{
		{Value: "IN_ADVANCE", Label: "vorschüssig"}, {Value: "IN_ARREARS", Label: "nachschüssig"}}

	amountRe = regexp.MustCompile(`^-?\d+(\.\d+)?$`)
)

const (
	calcFixed     = "FIXED"
	calcPerUnit   = "PER_UNIT"
	calcDependent = "DEPENDENT"
	defaultUnit   = "STK"
)

func (m *Module) condition() *crud.Entity {
	return &crud.Entity{
		Object: "ContractCondition", Title: "Konditionen", Icon: "icon-coins", Table: "contract__condition", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "condition_type", "object_id", "position", "valid_from"}, TimeSlice: true,
		Order:   "company_code, contract_id, condition_type, object_id, position, valid_from",
		Filters: []string{"company_code", "contract_id", "condition_type", "object_id", "account_number"},
		Events:  true, CompanyCodeField: "company_code",
		EventFields: []string{"contract_id", "contract_type", "condition_type", "condition_kind", "object_id", "position", "amount", "quantity", "unit_price", "dependent_type", "frequency", "due_day", "period_start_month", "due_month", "due_date", "account_number", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "contract_type", Label: "Vertragsart", Type: tText, ReadOnly: true, Lookup: catalogLookup("ContractType")},
			crud.Field{Key: "condition_type", Label: "Konditionsgegenstand", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ConditionType")},
			crud.Field{Key: "condition_kind", Label: "Konditionsart (leer = Standard des Gegenstands)", Type: tText, Listable: true, Lookup: kindLookup},
			crud.Field{Key: "object_id", Label: "Objekt (leer = ganzer Vertrag)", Type: tText, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "ContractObject", ValueField: "object_id", LabelFields: []string{"object_id"},
					Filters: map[string]string{"company_code": "company_code", "contract_id": "contract_id"}}},
			crud.Field{Key: "position", Label: "Position (leer = nächste freie; derselbe Gegenstand mehrfach)", Type: tNum, Listable: true, Immutable: true},
			crud.Field{Key: "calc_method", Label: "Berechnung", Type: tSel, Required: true, Options: calcOptions, Group: "Betrag", Trigger: true},
			crud.Field{Key: "quantity", Label: "Menge (leer = 1)", Type: tText, Listable: true, Group: "Betrag",
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcFixed}}},
			crud.Field{Key: "unit", Label: "Mengeneinheit (leer = Stück)", Type: tText, Listable: true, Group: "Betrag",
				Lookup: &metamodel.Lookup{Object: "Unit", ValueField: "code", LabelFields: []string{"name"}},
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcFixed}}},
			crud.Field{Key: "unit_price", Label: "Einzelpreis (bzw. Preis je Einheit der Bemessung)", Type: tText, Listable: true, Group: "Betrag",
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcFixed, calcPerUnit}}},
			crud.Field{Key: "measurement_type", Label: "Bemessungsart (bei je Einheit, z. B. WFL)", Type: tText, Group: "Betrag",
				Lookup: &metamodel.Lookup{Object: "MeasurementType", ValueField: "code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code"}},
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcPerUnit}}},
			crud.Field{Key: "dependent_type", Label: "Nachgelagerte Konditionsart", Type: tText, Listable: true, Group: "Betrag",
				Lookup: &metamodel.Lookup{Object: "ContractDependentType", ValueField: "code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code"}},
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcDependent}}},
			crud.Field{Key: "scheme", Label: "Kalkulationsschema (abhängige Positionen in Rechnungen zum Vertrag, z. B. STROM)", Type: tText, Group: "Betrag",
				Lookup: &metamodel.Lookup{Object: "PricingScheme", ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}},
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcFixed, calcPerUnit}}},
			crud.Field{Key: "amount", Label: "Gesamtpreis je Periode (= Menge × Einzelpreis; leer = gerechnet)", Type: tText, Group: "Betrag",
				ShowIf: &metamodel.Condition{Field: "calc_method", Values: []string{calcFixed}}},
			crud.Field{Key: "total", Label: "Gesamtpreis je Periode", Type: tText, Listable: true, Virtual: true, Group: "Betrag"},
			crud.Field{Key: "rounding_rule", Label: "Rundungsregel (leer = Gegenstand bzw. Buchungskreis)", Type: tText, Group: "Betrag",
				Lookup: lookupRounding},
			crud.Field{Key: "frequency", Label: "Rhythmus", Type: tSel, Required: true, Listable: true, Options: frequencyOptions, Group: "Fälligkeit",
				Trigger: true},
			crud.Field{Key: "due_day", Label: "Fällig am … Tag", Type: tNum, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"MONTHLY", "BIMONTHLY", "QUARTERLY", "HALF_YEARLY", "YEARLY"}}},
			crud.Field{Key: "period_start_month", Label: "Periodenbeginn im Monat (1–12; leer = Kalender, z. B. 10 = Versicherungsjahr Okt–Sep)",
				Type: tNum, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"BIMONTHLY", "QUARTERLY", "HALF_YEARLY", "YEARLY"}}},
			crud.Field{Key: "due_month", Label: "Fällig im … Monat der Periode (vorschüssig; leer = erster, z. B. 3 = Jahresmiete fällig im März)",
				Type: tNum, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"BIMONTHLY", "QUARTERLY", "HALF_YEARLY", "YEARLY"}}},
			crud.Field{Key: "due_date", Label: "Fällig am (leer = Beginn)", Type: tDate, Group: "Fälligkeit",
				ShowIf: &metamodel.Condition{Field: "frequency", Values: []string{"ONCE"}}},
			crud.Field{Key: "installments", Label: "Monatsraten (leer = Standard der Konditionsart)", Type: tNum, Group: "Fälligkeit",
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
		Prepare:  prepareCondition,
		Validate: m.checkCondition,
		Decorate: m.decorateCondition,
		Routes:   map[string]module.HandlerFunc{"positions": m.positionsAction, "taxQuota": m.taxQuotaAction},
	}
}

// conditionTypeOf: Konditionsgegenstand (aktiv) – Name.
func (m *Module) conditionTypeOf(ctx context.Context, cc, code string) (name string, err error) {
	name, _, err = m.conditionTypeKind(ctx, cc, code)
	return name, err
}

// conditionTypeKind: Konditionsgegenstand (aktiv) – Name und Standard-Konditionsart.
func (m *Module) conditionTypeKind(ctx context.Context, cc, code string) (name, kind string, err error) {
	res, err := m.db.Query(ctx, "SELECT name, COALESCE(default_kind, '') FROM contract__condition_type WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return "", "", err
	}
	if len(res.Rows) == 0 {
		return "", "", crud.Invalid("Konditionsgegenstand %q gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
	}
	return crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), nil
}

// checkConditionKind: Konditionsart (leer = Standard des Gegenstands) passend zum
// Abrechnungsweg der Vertragsart – Sollstellung ohne Rechnungspositionen.
func (m *Module) checkConditionKind(ctx context.Context, rec crud.Record, c *contractRow, defKind string) (*kindRow, error) {
	code := trimUpper(rec["condition_kind"])
	if code == "" {
		code = defKind
	}
	if code == "" {
		return nil, crud.Invalid("Konditionsart angeben (der Gegenstand %s hat keine Standard-Konditionsart)", crud.Str(rec["condition_type"]))
	}
	k, err := m.conditionKindOf(ctx, c.CompanyCode, code)
	if err != nil {
		return nil, err
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return nil, err
	}
	if k.Form == formInvoice && ct.BillingMode == billPosting {
		return nil, crud.Invalid("Konditionsart %s ist eine Rechnungsposition – die Vertragsart %s rechnet über Sollstellungen ab (Abrechnungsweg an der Vertragsart)", code, c.Type)
	}
	rec["condition_kind"] = code
	return k, nil
}

func (m *Module) checkCondition(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	rec["contract_type"] = c.Type
	if payer := strings.TrimSpace(crud.Str(rec["payer_id"])); payer != "" {
		ct, err := m.contractTypeOf(ctx, cc, c.Type)
		if err != nil {
			return err
		}
		if ct.AccountRequired {
			if err := m.requirePartnerAccount(ctx, cc, payer, ct.MainRole, "Zahler"); err != nil {
				return err
			}
		}
	}
	if old == nil {
		rec["condition_type"] = trimUpper(rec["condition_type"])
		rec["object_id"] = strings.TrimSpace(crud.Str(rec["object_id"]))
		if err := m.conditionPosition(ctx, rec); err != nil {
			return err
		}
	}
	if err := within("Kondition", crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"]), c); err != nil {
		return err
	}
	_, defKind, err := m.conditionTypeKind(ctx, cc, crud.Str(rec["condition_type"]))
	if err != nil {
		return err
	}
	kind, err := m.checkConditionKind(ctx, rec, c, defKind)
	if err != nil {
		return err
	}
	// Monatsraten nur bei einmaligen Konditionen; leer = Standard der Konditionsart.
	if crud.Str(rec["frequency"]) != "ONCE" {
		rec["installments"] = nil
	} else if crud.Str(rec["installments"]) == "" {
		rec["installments"] = max(kind.Installments, 1)
	} else if n := toInt(rec["installments"]); n < 1 || n > 60 {
		return crud.Invalid("Monatsraten 1 bis 60")
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
	if err := m.conditionPrice(ctx, rec, old, c); err != nil {
		return err
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
	periodMonths := map[string]int64{"BIMONTHLY": 2, "QUARTERLY": 3, "HALF_YEARLY": 6, "YEARLY": 12}[crud.Str(rec["frequency"])]
	switch dm := toInt(rec["due_month"]); {
	case rec["due_month"] == nil || crud.Str(rec["due_month"]) == "" || periodMonths == 0 || dm == 1:
		rec["due_month"] = nil // erster Monat der Periode
	case dm < 1 || dm > periodMonths:
		return crud.Invalid("Fällig im Monat der Periode: 1 bis %d", periodMonths)
	}
	switch sm := toInt(rec["period_start_month"]); {
	case rec["period_start_month"] == nil || crud.Str(rec["period_start_month"]) == "":
		rec["period_start_month"] = nil
	case sm < 1 || sm > 12:
		return crud.Invalid("Periodenbeginn: Monat 1 bis 12")
	case crud.Str(rec["frequency"]) == "MONTHLY" || crud.Str(rec["frequency"]) == "ONCE" || sm == 1:
		rec["period_start_month"] = nil // Kalender
	}
	if crud.Str(rec["calc_method"]) == calcDependent {
		if err := m.dependentAccount(ctx, rec, c); err != nil {
			return err
		}
	} else if err := m.accountFor(ctx, rec, c); err != nil {
		return err
	}
	return m.conditionHook(ctx, rec, old, c)
}

// conditionPosition: Position einer neuen Kondition – angegeben (ab 1) oder die kleinste,
// die im Zeitraum für Gegenstand und Objekt frei ist (eine Folgezeitscheibe bekommt so
// dieselbe Position, ein zweites Entgelt im selben Zeitraum die nächste).
func (m *Module) conditionPosition(ctx context.Context, rec crud.Record) error {
	if p := strings.TrimSpace(crud.Str(rec["position"])); p != "" {
		if n := toInt(rec["position"]); n < 1 {
			return crud.Invalid("Position ab 1")
		}
		rec["position"] = toInt(rec["position"])
		return nil
	}
	to := crud.Str(rec["valid_to"])
	if to == "" {
		to = "9999-12-31"
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT position FROM contract__condition WHERE company_code = ? AND contract_id = ? AND condition_type = ?
		AND COALESCE(object_id, '') = ? AND valid_from <= ? AND valid_to >= ?`,
		rec["company_code"], rec["contract_id"], rec["condition_type"], crud.Str(rec["object_id"]), to, crud.Str(rec["valid_from"]))
	if err != nil {
		return err
	}
	used := map[int64]bool{}
	for _, r := range res.Rows {
		used[toInt(r[0])] = true
	}
	p := int64(1)
	for used[p] {
		p++
	}
	rec["position"] = p
	return nil
}

// conditionHook: Hook contract.condition (check) bei neuer Kondition bzw.
// geändertem Betrag oder Beginn – z. B. verlangt das DMS ein Dokument zur
// Anpassung des Hausgelds. E verhindert das Speichern, W kommt als Warnung.
func (m *Module) conditionHook(ctx context.Context, rec, old crud.Record, c *contractRow) error {
	from, _ := crud.ParseDate(rec["valid_from"])
	mode := "create"
	if old != nil {
		oldFrom, _ := crud.ParseDate(old["valid_from"])
		if toInt(old["amount"]) == toInt(rec["amount"]) && oldFrom == from {
			return nil
		}
		mode = "update"
	}
	prev, err := m.db.Query(ctx, `SELECT MAX(valid_from) FROM contract__condition WHERE company_code = ? AND contract_id = ? AND condition_type = ?
		AND COALESCE(object_id, '') = ? AND position = ? AND valid_from < ?`, c.CompanyCode, c.ID, rec["condition_type"], crud.Str(rec["object_id"]), toInt(rec["position"]), from)
	if err != nil {
		return err
	}
	prevFrom := ""
	if len(prev.Rows) > 0 && crud.Str(prev.Rows[0][0]) != "" {
		prevFrom, _ = crud.ParseDate(prev.Rows[0][0])
	}
	data := map[string]any{"mode": mode, "company_code": c.CompanyCode, "contract_id": c.ID, "contract_type": c.Type,
		"condition_type": crud.Str(rec["condition_type"]), "object_id": crud.Str(rec["object_id"]), "position": toInt(rec["position"]), "valid_from": from,
		"amount_minor": toInt(rec["amount"]), "previous_valid_from": prevFrom}
	res, err := hook.Call(ctx, m.services, hookCondition, hook.PhaseCheck, data)
	if err != nil {
		return err
	}
	if res.HasErrors() {
		return res.Err()
	}
	for _, w := range res.Filter(hook.TypeWarning) {
		crud.Warn(ctx, "%s", w.String())
	}
	return nil
}

// accountFor: Sachkonto aus der Kontenfindung (Vertragsart, Gegenstand, Konditionsart;
// Einträge zur Konditionsart gehen denen ohne vor). Ohne Eintrag lässt sich die Kondition
// nicht speichern; ein angegebenes Konto muss dazu gehören, leer = Standard bzw. einziges.
func (m *Module) accountFor(ctx context.Context, rec crud.Record, c *contractRow) error {
	typ, kind := crud.Str(rec["condition_type"]), crud.Str(rec["condition_kind"])
	res, err := m.db.Query(ctx, `SELECT account_number, is_default, COALESCE(condition_kind, '') FROM contract__account
		WHERE company_code = ? AND contract_type = ? AND condition_type = ? AND is_active = ? AND COALESCE(condition_kind, '') IN ('', ?)
		ORDER BY account_number`, c.CompanyCode, c.Type, typ, true, kind)
	if err != nil {
		return err
	}
	rows := res.Rows
	if slices.ContainsFunc(rows, func(r []any) bool { return crud.Str(r[2]) != "" }) {
		rows = slices.DeleteFunc(slices.Clone(rows), func(r []any) bool { return crud.Str(r[2]) == "" })
	}
	if len(rows) == 0 {
		return crud.Invalid("Keine Kontenfindung für Vertragsart %s, Gegenstand %s, Konditionsart %s – unter Einstellungen → Kontenfindung ein Sachkonto hinterlegen",
			c.Type, typ, kind)
	}
	acc := strings.TrimSpace(crud.Str(rec["account_number"]))
	var allowed []string
	for _, r := range rows {
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
	return crud.Invalid("Sachkonto %s ist für %s/%s/%s nicht vorgesehen (Kontenfindung: %s)", acc, c.Type, typ, kind, strings.Join(allowed, ", "))
}

// decorateCondition: Betrag als Dezimaltext in der Vertragswährung.
func (m *Module) decorateCondition(ctx context.Context, rec crud.Record) error {
	// Rechnungsvorlage: keine Sollstellung (contract-billing überspringt sie).
	rec["is_template"] = m.isTemplate(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_type"]), crud.Str(rec["condition_kind"]))
	c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
	if err != nil {
		return nil
	}
	dec := m.currencyDecimals(ctx, c.Currency)
	rec["amount"] = formatAmount(toInt(rec["amount"]), dec)
	labels(rec)["amount"] = crud.Str(rec["amount"]) + " " + c.Currency
	if q := crud.Str(rec["quantity"]); q != "" {
		if v, err := pricing.ParseDecimal(q); err == nil {
			rec["quantity"] = pricing.FormatTrim(v, 6)
		}
	}
	rec["total"] = m.conditionTotal(ctx, rec, c, dec)
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

// templateSQL: Kondition k ist eine Rechnungsvorlage (keine Sollstellung) – Abrechnungsweg
// der Vertragsart Eingangsrechnung, bei „beides“ Konditionsart Rechnungsposition. Braucht
// die Joins templateJoins (Vertragsart tt, Konditionsart tk).
const (
	templateJoins = ` LEFT JOIN contract__contract_type tt ON tt.company_code = k.company_code AND tt.code = k.contract_type
		LEFT JOIN contract__condition_kind tk ON tk.company_code = k.company_code AND tk.code = k.condition_kind `
	templateSQL = `(COALESCE(tt.billing_mode, 'POSTING') = 'INVOICE' OR (tt.billing_mode = 'BOTH' AND COALESCE(tk.billing_form, '') = 'INVOICE'))`
)

// isTemplate: Kondition der Vertragsart mit dieser Konditionsart ist eine Rechnungsvorlage.
func (m *Module) isTemplate(ctx context.Context, cc, contractType, kind string) bool {
	res, err := m.db.Query(ctx, `SELECT COALESCE(t.billing_mode, ''), COALESCE(d.billing_form, '') FROM contract__contract_type t
		LEFT JOIN contract__condition_kind d ON d.company_code = t.company_code AND d.code = ?
		WHERE t.company_code = ? AND t.code = ?`, kind, cc, contractType)
	if err != nil || len(res.Rows) == 0 {
		return false
	}
	mode, form := crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1])
	return mode == billInvoice || (mode == billBoth && form == formInvoice)
}
