package contract

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Zahlwege (seit 0.47.0, vorher feste Liste seit 0.45.0) je Buchungskreis:
// Katalog mit Merkmalen, die das Verhalten steuern –
//
//   - „Lastschrift mit Mandat“ (uses_mandate): Mandatsfelder am Vertrag; das
//     Bankmodul ordnet Abbuchungen über das Mandat zu und prüft sie dagegen.
//
// Die Erinnerungsregeln wählen ihre Zahlwege aus diesem Katalog. Vorschlag beim
// Einrichten bzw. Update (Buchungskreis ohne Zahlwege): Dauerauftrag,
// Lastschrift, Einzelüberweisung.

const paymentMethodObject = "PaymentMethod"

var defaultPaymentMethods = []struct {
	code, name string
	mandate    bool
}{
	{"STANDING_ORDER", "Dauerauftrag", false},
	{"DIRECT_DEBIT", "Lastschrift", true},
	{"TRANSFER", "Einzelüberweisung", false},
}

var paymentMethodLookup = &metamodel.Lookup{Object: paymentMethodObject, ValueField: "code", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code"}}

func (m *Module) paymentMethod() *crud.Entity {
	return m.catalog(paymentMethodObject, "Zahlwege", "contract__payment_method", catalogFields(
		crud.Field{Key: "uses_mandate", Label: "Lastschrift mit Mandat (Mandatsfelder am Vertrag, Prüfung der Abbuchungen im Bankmodul)",
			Short: "Mandat", Type: tBool, Listable: true},
		crud.Field{Key: "note", Label: "Bemerkung", Type: tArea},
	), func(_ context.Context, rec crud.Record) error {
		defaults(rec, map[string]any{"uses_mandate": false, "sort_order": 0})
		return nil
	})
}

// seedPaymentMethods: Vorschlag für einen Buchungskreis ohne Zahlwege.
func (m *Module) seedPaymentMethods(ctx context.Context, cc string) (int, error) {
	res, err := m.db.Query(ctx, `SELECT 1 FROM contract__payment_method WHERE company_code = ? LIMIT 1`, cc)
	if err != nil || len(res.Rows) > 0 {
		return 0, err
	}
	for i, p := range defaultPaymentMethods {
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__payment_method (company_code, code, name, uses_mandate, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?)`, cc, p.code, p.name, p.mandate, (i+1)*10, true); err != nil {
			return i, err
		}
	}
	return len(defaultPaymentMethods), nil
}

// seedAllPaymentMethods (Update): jeder Buchungskreis mit Vertragsarten bekommt den Vorschlag.
func (m *Module) seedAllPaymentMethods(ctx context.Context) {
	res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM contract__contract_type`)
	if err == nil {
		for _, r := range res.Rows {
			if _, err = m.seedPaymentMethods(ctx, crud.Str(r[0])); err != nil {
				break
			}
		}
	}
	if err != nil {
		m.log.WarnContext(ctx, "Zahlwege nicht vorgeschlagen", "err", err.Error())
	}
}

// paymentMethods: aktive Zahlwege eines Buchungskreises (Code → mit Mandat) und Auswahl.
func (m *Module) paymentMethods(ctx context.Context, cc string) (map[string]bool, []metamodel.Option, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, uses_mandate FROM contract__payment_method WHERE company_code = ? AND is_active = ?
		ORDER BY sort_order, code`, cc, true)
	if err != nil {
		return nil, nil, err
	}
	flags := map[string]bool{}
	var opts []metamodel.Option
	for _, r := range res.Rows {
		code := crud.Str(r[0])
		flags[code] = crud.AsBool(r[2])
		opts = append(opts, metamodel.Option{Value: code, Label: crud.Str(r[1])})
	}
	return flags, opts, nil
}

// checkPaymentMethod: Zahlweg des Vertrags aus dem Katalog (nur geprüft, wenn geändert).
func (m *Module) checkPaymentMethod(ctx context.Context, rec, old crud.Record) error {
	code := trimUpper(rec["payment_method"])
	rec["payment_method"] = nilIfEmpty(code)
	if code == "" || (old != nil && code == crud.Str(old["payment_method"])) {
		return nil
	}
	flags, _, err := m.paymentMethods(ctx, crud.Str(rec["company_code"]))
	if err != nil {
		return err
	}
	if _, ok := flags[code]; !ok {
		return crud.Invalid("Zahlweg %s gibt es im Buchungskreis %s nicht (Einstellungen → Zahlwege)", code, crud.Str(rec["company_code"]))
	}
	return nil
}

// mandateFormState: Mandatsfelder nur bei einem Zahlweg mit Mandat.
func (m *Module) mandateFormState(ctx context.Context, req metamodel.FormStateRequest, st *metamodel.FormState) error {
	cc := req.Values["company_code"]
	if cc == "" && req.ID != "" {
		if key, err := m.set.Entity("Contract").ParseID(req.ID); err == nil {
			cc = crud.Str(key["company_code"])
		}
	}
	flags, _, err := m.paymentMethods(ctx, cc)
	if err != nil {
		return err
	}
	show := flags[strings.ToUpper(strings.TrimSpace(req.Values["payment_method"]))]
	for _, k := range []string{"mandate_ref", "creditor_id", "mandate_date"} {
		st.Fields[k] = metamodel.FieldState{Visible: &show}
	}
	return nil
}

// checkMandate: Mandatsreferenz (SEPA: bis 35 Zeichen) und Gläubiger-ID
// (ohne Leerzeichen, groß) normalisieren.
func checkMandate(rec crud.Record) error {
	ref := strings.TrimSpace(crud.Str(rec["mandate_ref"]))
	if len(ref) > 35 {
		return crud.Invalid("Mandatsreferenz hat %d Zeichen (höchstens 35)", len(ref))
	}
	cred := strings.ToUpper(strings.ReplaceAll(crud.Str(rec["creditor_id"]), " ", ""))
	if cred != "" && (len(cred) < 8 || len(cred) > 35) {
		return crud.Invalid("Gläubiger-ID %q hat nicht die Form DE98ZZZ09999999999", cred)
	}
	rec["mandate_ref"], rec["creditor_id"] = nilIfEmpty(ref), nilIfEmpty(cred)
	if d := crud.Str(rec["mandate_date"]); d != "" {
		v, err := crud.ParseDate(d)
		if err != nil {
			return err
		}
		rec["mandate_date"] = v
	} else {
		rec["mandate_date"] = nil
	}
	return nil
}
