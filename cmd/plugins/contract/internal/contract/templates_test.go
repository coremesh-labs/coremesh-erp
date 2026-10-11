package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Rahmenvertrag: Vorlagen sind Konditionen der Konditionsart RT; „Eingangsrechnung
// erfassen …“ reicht die gewählte Vorlage an die Beschaffung weiter.
func TestInvoiceFromTemplate(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "RV", "designation": "Rahmenvertrag Allianz",
		"partner_id": "INS", "valid_from": "2026-01-01"})
	id := c["contract_id"].(string)
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "RT",
		"calc_method": "FIXED", "amount": "120,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01",
		"note": "Wartung"})
	ts, err := e.m.templates(e.ctx, "1000", id)
	if err != nil || len(ts) != 1 || ts[0].Value != "RT" {
		t.Fatalf("Vorlagen: %v %+v", err, ts)
	}
	key := "1000|" + id
	_, err = e.call("Contract", "invoiceFromTemplate", map[string]any{"id": key, "data": map[string]any{"inv_template": "XX", "inv_reference": "R1"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Vorlage")
	e.must("Contract", "invoiceFromTemplate", map[string]any{"id": key, "data": map[string]any{"inv_template": "RT", "inv_reference": " R-77 ", "inv_date": "2026-03-02"}})
	if f := e.h.fromTemplate; f["template"] != "RT" || f["supplier_reference"] != "R-77" || f["contract_id"] != id {
		t.Fatalf("fromTemplate: %v", f)
	}
}
