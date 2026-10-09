package contract

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestLoanTerms: Darlehenskonditionen am Darlehensvertrag – Beträge,
// Zinssatz, Rate je Tilgungsart, Konten und Konditionsarten (Vorschläge).
func TestLoanTerms(t *testing.T) {
	e := setup(t)
	e.basics()
	e.h.accounts["1000|3150"] = "Verbindlichkeiten gegenüber Kreditinstituten"
	e.h.accounts["1000|7310"] = "Zinsaufwand"
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "DA", "designation": "Baufinanzierung",
		"partner_id": "INS", "valid_from": "2026-01-01"})
	id := c["contract_id"].(string)
	loan := map[string]any{"company_code": "1000", "contract_id": id, "valid_from": "2026-01-01", "valid_to": "2030-12-31", "principal": "200.000,00",
		"disbursement_date": "2026-01-15", "repayment_type": "ANNUITY", "interest_rate": "3,45", "installment": "1.000,00",
		"frequency": "MONTHLY", "day_count": "30/360", "loan_account": "3150", "interest_account": "7310"}
	l := e.create(loanObject, loan)
	if l["principal"] != "200000.00" || l["interest_rate"] != "3.45" || l["installment"] != "1000.00" || l["interest_type"] != "DZ" ||
		l["principal_type"] != "DT" || l["special_type"] != "DS" || l["disbursement_type"] != "AZ" || l["loan_account"] != "3150" ||
		l["takeover"] != false || toInt(l["due_day"]) != 30 {
		t.Fatalf("Darlehen: %v", l)
	}
	bad := func(k string, v any, what string) {
		t.Helper()
		d := map[string]any{}
		for kk, vv := range loan {
			d[kk] = vv
		}
		d["valid_from"], d["valid_to"], d[k] = "2031-01-01", "", v
		err := e.try(loanObject, d)
		expect(t, err, sdk.ErrInvalidArgument, what)
		if strings.Contains(err.Error(), "Zeitscheibe") {
			t.Fatalf("%s: falscher Grund: %v", what, err)
		}
	}
	bad("interest_rate", "drei", "Zinssatz")
	bad("installment", "", "Annuität ohne Rate")
	bad("loan_account", "9998", "unbekanntes Konto")
	bad("principal", "0", "Betrag 0")
	// Endfällig: ohne Rate
	d := map[string]any{}
	for k, v := range loan {
		d[k] = v
	}
	d["valid_from"], d["valid_to"], d["repayment_type"], d["installment"] = "2031-01-01", "", "BULLET", "500"
	if l2 := e.create(loanObject, d); l2["installment"] != nil {
		t.Fatalf("endfällig mit Rate: %v", l2)
	}
	// Aktivieren: Darlehenskonditionen genügen
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	// Sondertilgung
	p := e.create(loanPaymentObject, map[string]any{"company_code": "1000", "contract_id": id, "payment_date": "2027-03-01", "amount": "10.000,00"})
	if p["amount"] != "10000.00" {
		t.Fatalf("Sondertilgung: %v", p)
	}
	expect(t, e.try(loanPaymentObject, map[string]any{"company_code": "1000", "contract_id": id, "payment_date": "2027-04-01", "amount": "-1"}),
		sdk.ErrInvalidArgument, "Sondertilgung negativ")
}
