package contract

import (
	"context"
	"regexp"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Darlehen: Ein Vertrag (z. B. Vertragsart DA „Darlehen aufgenommen“, Partner
// Bank als Kreditor, oder DV „Darlehen vergeben“) trägt Darlehenskonditionen
// statt – oder neben – Konditionen:
//
//   - ContractLoan (Zeitscheiben, z. B. neue Zinsbindung): Betrag und
//     Auszahlung (oder Übernahme mit Anfangsbestand), Tilgungsart (Annuität, Ratentilgung, endfällig), Rhythmus,
//     Fälligkeitstag, Zinsmethode, Zinssatz, Rate, Konten und die
//     Konditionsarten, unter denen Zins, Tilgung, Sondertilgung und Auszahlung
//     als Sollstellung vermerkt werden.
//   - ContractLoanPayment: Sondertilgungen (Datum, Betrag).
//
// Den Tilgungsplan rechnet contract-billing (Haskell, Billing.Loan); daraus
// entstehen die Sollstellungen wie bei Konditionen (Zins → Zinskonto, Tilgung
// und Auszahlung → Darlehenskonto, jeweils gegen das Partnerkonto).

const (
	loanObject        = "ContractLoan"
	loanPaymentObject = "ContractLoanPayment"
)

var (
	repaymentOptions = []metamodel.Option{
		{Value: "ANNUITY", Label: "Annuität (gleichbleibende Rate aus Zins und Tilgung)"},
		{Value: "INSTALLMENT", Label: "Ratentilgung (gleichbleibende Tilgung, Zins dazu)"},
		{Value: "BULLET", Label: "endfällig (nur Zinsen, Tilgung am Ende)"}}
	dayCountOptions = []metamodel.Option{
		{Value: "30/360", Label: "30/360 (deutsche Zinsmethode, Monatsende = 30.)"},
		{Value: "ACT/360", Label: "act/360 (Eurozinsmethode)"},
		{Value: "ACT/365", Label: "act/365 (englische Methode)"}}
	loanFrequencyOptions = []metamodel.Option{
		{Value: "MONTHLY", Label: "monatlich"}, {Value: "BIMONTHLY", Label: "zweimonatlich"}, {Value: "QUARTERLY", Label: "vierteljährlich", Short: "viertelj."},
		{Value: "HALF_YEARLY", Label: "halbjährlich"}, {Value: "YEARLY", Label: "jährlich"}}
	rateRe = regexp.MustCompile(`^\d{1,3}([.,]\d{1,6})?$`)

	glLookup = &metamodel.Lookup{Object: "GLAccountCompany", ValueField: "account_number", LabelFields: []string{"account_name"},
		Columns: []string{"account_number", "account_name", "reconciliation_type"}, Filters: map[string]string{"company_code_id": "company_code"}}
)

func (m *Module) loan() *crud.Entity {
	return &crud.Entity{
		Object: loanObject, Title: "Darlehenskonditionen", Icon: "icon-coins", Table: "contract__loan", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "valid_from"}, TimeSlice: true, Order: "company_code, contract_id, valid_from",
		Filters: []string{"company_code", "contract_id"},
		Events:  true, CompanyCodeField: "company_code", // contract-billing hält eine Kopie
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "principal", Label: "Darlehensbetrag", Type: tText, Required: true, Listable: true, Group: "Darlehen"},
			crud.Field{Key: "disbursement_date", Label: "Auszahlung am bzw. Übernahme zum (Zinsbeginn)", Type: tDate, Required: true, Group: "Darlehen"},
			crud.Field{Key: "takeover", Label: "Übernahme mit Anfangsbestand (Betrag = Restschuld am Datum, Auszahlung nicht buchen)", Type: tBool, Group: "Darlehen"},
			crud.Field{Key: "repayment_type", Label: "Tilgungsart", Short: "Tilgung", Type: tSel, Required: true, Listable: true, Options: repaymentOptions, Group: "Darlehen"},
			crud.Field{Key: "interest_rate", Label: "Sollzins % p. a.", Type: tText, Required: true, Listable: true, Group: "Zins und Rate"},
			crud.Field{Key: "installment", Label: "Rate (Annuität bzw. Tilgung je Periode; endfällig leer)", Type: tText, Listable: true, Group: "Zins und Rate"},
			crud.Field{Key: "frequency", Label: "Rhythmus", Type: tSel, Required: true, Options: loanFrequencyOptions, Group: "Zins und Rate"},
			crud.Field{Key: "due_day", Label: "Fällig am … Tag", Type: tNum, Group: "Zins und Rate"},
			crud.Field{Key: "day_count", Label: "Zinsmethode", Type: tSel, Required: true, Options: dayCountOptions, Group: "Zins und Rate"},
			crud.Field{Key: "fixed_until", Label: "Zinsbindung bis", Type: tDate, Group: "Zins und Rate"},
			crud.Field{Key: "loan_account", Label: "Darlehenskonto (Bilanz)", Type: tText, Required: true, Group: "Buchung", Lookup: glLookup},
			crud.Field{Key: "interest_account", Label: "Zinskonto (Aufwand bzw. Ertrag)", Type: tText, Required: true, Group: "Buchung", Lookup: glLookup},
			crud.Field{Key: "interest_type", Label: "Konditionsart Zins (Vermerk)", Type: tText, Group: "Buchung", Lookup: catalogLookup("ConditionType")},
			crud.Field{Key: "principal_type", Label: "Konditionsart Tilgung", Type: tText, Group: "Buchung", Lookup: catalogLookup("ConditionType")},
			crud.Field{Key: "special_type", Label: "Konditionsart Sondertilgung", Type: tText, Group: "Buchung", Lookup: catalogLookup("ConditionType")},
			crud.Field{Key: "disbursement_type", Label: "Konditionsart Auszahlung", Type: tText, Group: "Buchung", Lookup: catalogLookup("ConditionType")},
		),
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkLoan,
		Decorate: m.decorateLoan,
	}
}

func (m *Module) loanPayment() *crud.Entity {
	return &crud.Entity{
		Object: loanPaymentObject, Title: "Sondertilgungen", Icon: "icon-coins", Table: "contract__loan_payment", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "payment_date"}, Order: "company_code, contract_id, payment_date",
		Filters: []string{"company_code", "contract_id"},
		Events:  true, CompanyCodeField: "company_code",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "payment_date", Label: "Am", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "amount", Label: "Betrag", Type: tText, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
		},
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
			if err != nil {
				return err
			}
			d, err := crud.ParseDate(rec["payment_date"])
			if err != nil {
				return err
			}
			rec["payment_date"] = d
			minor, err := parseAmount(crud.Str(rec["amount"]), m.currencyDecimals(ctx, c.Currency))
			if err != nil {
				return crud.Invalid("%v", err)
			}
			if minor <= 0 {
				return crud.Invalid("Sondertilgung: Betrag größer 0")
			}
			rec["amount"] = minor
			return nil
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "amount")
		},
	}
}

// checkLoan: Beträge in der kleinsten Einheit, Zinssatz als Dezimaltext,
// Rate passend zur Tilgungsart, Konten im Buchungskreis, Vorschläge für die
// Konditionsarten (DZ, DT, DS, AZ).
func (m *Module) checkLoan(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	c, err := m.contractOf(ctx, cc, crud.Str(rec["contract_id"]))
	if err != nil {
		return err
	}
	if err := within("Darlehenskondition", crud.Str(rec["valid_from"]), endAtContract(rec, c), c); err != nil {
		return err
	}
	decimals := m.currencyDecimals(ctx, c.Currency)
	principal, err := parseAmount(crud.Str(rec["principal"]), decimals)
	if err != nil {
		return crud.Invalid("Darlehensbetrag: %v", err)
	}
	if principal <= 0 {
		return crud.Invalid("Darlehensbetrag größer 0")
	}
	rec["principal"] = principal
	rate := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec["interest_rate"])), ",", ".")
	if !rateRe.MatchString(rate) {
		return crud.Invalid("Sollzins: Prozent p. a., z. B. 3,45")
	}
	rec["interest_rate"] = rate
	switch crud.Str(rec["repayment_type"]) {
	case "BULLET":
		rec["installment"] = nil
	default:
		inst, err := parseAmount(crud.Str(rec["installment"]), decimals)
		if err != nil || inst <= 0 {
			return crud.Invalid("Rate größer 0 ist Pflicht (Annuität bzw. Tilgung je Periode)")
		}
		rec["installment"] = inst
	}
	if d := toInt(rec["due_day"]); crud.Str(rec["due_day"]) == "" {
		rec["due_day"] = 30
	} else if d < 1 || d > 31 {
		return crud.Invalid("Fälligkeitstag 1 bis 31")
	}
	for _, k := range []string{"disbursement_date", "fixed_until"} {
		if crud.Str(rec[k]) == "" {
			rec[k] = nil
			continue
		}
		if rec[k], err = crud.ParseDate(rec[k]); err != nil {
			return err
		}
	}
	for _, k := range []string{"loan_account", "interest_account"} {
		nr, _, err := m.glAccount(ctx, cc, crud.Str(rec[k]))
		if err != nil {
			return err
		}
		rec[k] = nr
	}
	for k, def := range map[string]string{"interest_type": "DZ", "principal_type": "DT", "special_type": "DS", "disbursement_type": "AZ"} {
		code := trimUpper(rec[k])
		if code == "" {
			code = def
		}
		if _, err := m.conditionTypeOf(ctx, cc, code); err != nil {
			return err
		}
		rec[k] = code
	}
	defaults(rec, map[string]any{"takeover": false})
	return nil
}

func (m *Module) decorateLoan(ctx context.Context, rec crud.Record) error {
	return m.decorateAmounts(ctx, rec, "principal", "installment")
}

// decorateAmounts: Beträge (kleinste Einheit) als Dezimaltext in der Vertragswährung.
func (m *Module) decorateAmounts(ctx context.Context, rec crud.Record, keys ...string) error {
	c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
	if err != nil {
		return nil
	}
	d := m.currencyDecimals(ctx, c.Currency)
	for _, k := range keys {
		if rec[k] != nil && crud.Str(rec[k]) != "" {
			rec[k] = formatAmount(toInt(rec[k]), d)
			labels(rec)[k] = crud.Str(rec[k]) + " " + c.Currency
		}
	}
	return nil
}

// loanScheduleAction: „Tilgungsplan …“ am Vertrag – rechnet contract-billing; ohne
// Vorlage als Tabelle, mit Vorlage (Verwendung LOAN_SCHEDULE, z. B. TILGUNGSPLAN) als Datei.
func (m *Module) loanScheduleAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Template string `json:"loan_template"`
			Until    string `json:"loan_until"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	call := map[string]any{"company_code": cc, "contract_id": id}
	if u := strings.TrimSpace(in.Data.Until); u != "" {
		d, err := crud.ParseDate(u)
		if err != nil {
			return sdk.Response{}, crud.Invalid("Plan bis: %v", err)
		}
		call["until"] = d
	}
	resp, err := m.billingCall(ctx, "loanSchedule", call)
	if err != nil || strings.TrimSpace(in.Data.Template) == "" {
		return resp, err
	}
	var plan struct {
		Message string `json:"message"`
		Table   struct {
			Rows [][]string `json:"rows"`
		} `json:"table"`
	}
	if err := sdk.Decode(resp.Payload, &plan); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	num := func(s string) float64 {
		v, _ := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
		return v
	}
	var rows []any
	var interest, principal, payments float64
	count, last := 0, ""
	for _, r := range plan.Table.Rows {
		if len(r) < 7 {
			continue
		}
		row := map[string]any{"due": r[0], "kind": r[1], "period": r[2], "interest": num(r[3]), "principal": num(r[4]), "payment": num(r[5]), "balance": num(r[6])}
		rows = append(rows, row)
		interest += num(r[3])
		if r[1] != "Auszahlung" {
			principal += num(r[4])
			payments += num(r[5])
		}
		if r[1] == "Rate" {
			count++
			last = r[0]
		}
	}
	data := map[string]any{"title": "Tilgungsplan", "subtitle": "Buchungskreis " + cc + " · Stand " + germanDate(crud.KeyDate(ctx)),
		"company": reportapi.Company(ctx, m.services, cc), "currency": c.Currency,
		"contract": map[string]any{"id": c.ID, "designation": c.Designation, "partner": firstNonEmpty(reportapi.DisplayName(ctx, m.services, c.Partner), c.Partner),
			"valid_from": dateOf(c.ValidFrom), "valid_to": dateOf(c.ValidTo), "open_end": dateOf(c.ValidTo) >= "9999-12-31"},
		"count": count, "last_due": firstNonEmpty(last, "–"), "interest": round2(interest), "principal": round2(principal), "payments": round2(payments), "rows": rows}
	out, err := reportapi.Render(ctx, m.services, cc, in.Data.Template, "Tilgungsplan "+c.ID, data)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}
