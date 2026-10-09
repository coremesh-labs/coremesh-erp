package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestCopyConditions: Fortschreiben ins nächste Wirtschaftsjahr (alte Scheibe endet)
// und Kopie auf einen anderen Vertrag mit Anteil nach Wohnfläche.
func TestCopyConditions(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract() // KM 850,00 ab 2026-01-01 auf LpzBrn1WG001
	// Fortschreiben: ab 2027-01-01
	r := e.must("Contract", "copyConditions", map[string]any{"id": "1000|" + id, "data": map[string]any{
		"source_date": "2026-06-01", "copy_from": "2027-01-01"}})
	if r["count"] != int64(1) && r["count"] != 1 {
		t.Fatalf("fortgeschrieben: %v", r)
	}
	conds := items(e.must("ContractCondition", "list", map[string]any{"query": map[string]any{"contract_id": id, "includeHistory": "true"}}))
	if len(conds) != 2 || conds[0]["valid_to"] != "2026-12-31" || conds[1]["valid_from"] != "2027-01-01" || conds[1]["amount"] != "850.00" {
		t.Fatalf("Zeitscheiben: %v", conds)
	}
	_, err := e.call("Contract", "copyConditions", map[string]any{"id": "1000|" + id, "data": map[string]any{
		"source_date": "2027-06-01", "copy_from": "2027-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Fortschreibung nicht nach dem Stichtag")

	// Kopie auf den Stellplatz-Vertrag, Anteil nach WFL (100 → 50 m²)
	c := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Zweiter Vertrag",
		"partner_id": "BP1", "valid_from": "2026-01-01"})
	id2 := c["contract_id"].(string)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id2, "object_type": "RentObject",
		"object_id": "LpzBrn1SP001", "is_main": true, "valid_from": "2026-01-01"})
	e.h.measurements = map[string]string{"LpzBrn1WG001|WFL": "100", "LpzBrn1SP001|WFL": "50"}
	e.must("Contract", "copyConditions", map[string]any{"id": "1000|" + id, "data": map[string]any{
		"target_contract_id": id2, "source_date": "2026-06-01", "copy_from": "2026-01-01", "rescale_by": "wfl"}})
	got := items(e.must("ContractCondition", "list", map[string]any{"query": map[string]any{"contract_id": id2}}))
	if len(got) != 1 || got[0]["amount"] != "425.00" || got[0]["condition_type"] != "KM" {
		t.Fatalf("Kopie mit Anteil: %v", got)
	}
	// Quelle unverändert
	if n := len(items(e.must("ContractCondition", "list", map[string]any{"query": map[string]any{"contract_id": id, "includeHistory": "true"}}))); n != 2 {
		t.Fatalf("Quelle: %d Scheiben", n)
	}
}
