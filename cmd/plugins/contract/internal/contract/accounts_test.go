package contract

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestPartnerAccount: Vertragsart mit „Partnerkonto Pflicht“ – Vertragspartner
// und abweichender Zahler brauchen Buchungskreisdaten der Rolle mit einem
// Abstimmkonto passender Art; abschaltbar je Vertragsart.
func TestPartnerAccount(t *testing.T) {
	e := setup(t)
	e.basics()
	contract := func(partner string) (map[string]any, error) {
		return e.call("Contract", "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "MV",
			"designation": "Mietvertrag", "partner_id": partner, "valid_from": "2026-01-01"}})
	}

	// Mieter ohne Buchungskreisdaten: kein Mietvertrag
	e.partner("BP9", "Neumann", "TENANT")
	delete(e.h.partnerCC, "BP9|1000|TENANT")
	_, err := contract("BP9")
	expect(t, err, sdk.ErrInvalidArgument, "Mieter ohne Konto")
	if !strings.Contains(err.Error(), "Neumann") || !strings.Contains(err.Error(), "Buchungskreisdaten") {
		t.Fatalf("Meldung: %v", err)
	}

	// Konto ist im Hauptbuch kein Debitoren-Abstimmkonto
	e.h.partnerCC["BP9|1000|TENANT"] = "1600"
	_, err = contract("BP9")
	expect(t, err, sdk.ErrInvalidArgument, "Kreditorenkonto beim Mieter")

	// Unbekanntes Konto
	e.h.partnerCC["BP9|1000|TENANT"] = "4711"
	_, err = contract("BP9")
	expect(t, err, sdk.ErrInvalidArgument, "Konto nicht im Hauptbuch")

	// Richtiges Konto: angelegt
	e.h.partnerCC["BP9|1000|TENANT"] = "1200"
	c, err := contract("BP9")
	if err != nil {
		t.Fatal(err)
	}
	id := c["contract_id"].(string)

	// Abweichender Zahler ohne Konto in der Rolle der Vertragsart: Kondition abgelehnt
	e.partner("JC", "Jobcenter", "TENANT")
	delete(e.h.partnerCC, "JC|1000|TENANT")
	err = e.try("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "KM",
		"calc_method": "FIXED", "amount": "500,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01",
		"payer_id": "JC"})
	expect(t, err, sdk.ErrInvalidArgument, "Zahler ohne Konto")

	// Aktivieren prüft erneut (Konto inzwischen entfernt)
	e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
		"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": "KM",
		"calc_method": "FIXED", "amount": "500,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	delete(e.h.partnerCC, "BP9|1000|TENANT")
	_, err = e.call("Contract", "activate", map[string]any{"id": "1000|" + id})
	expect(t, err, sdk.ErrInvalidArgument, "aktivieren ohne Konto")
	e.h.partnerCC["BP9|1000|TENANT"] = "1200"
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})

	// Abgeschaltet an der Vertragsart: Mieter ohne Konto erlaubt
	e.must("ContractType", "update", map[string]any{"id": "1000|MV", "data": map[string]any{"partner_account_required": false}})
	delete(e.h.partnerCC, "BP9|1000|TENANT")
	if _, err := contract("BP9"); err != nil {
		t.Fatalf("ohne Pflicht: %v", err)
	}

	// Pflicht an einer Vertragsart, deren Rolle keine Finanzrolle ist: abgelehnt
	_, err = e.call("ContractType", "update", map[string]any{"id": "1000|MV", "data": map[string]any{"partner_account_required": true,
		"main_role": "GUARANTOR"}})
	expect(t, err, sdk.ErrInvalidArgument, "Pflicht bei Nicht-Finanzrolle")
	if !strings.Contains(err.Error(), "keine Finanzrolle") {
		t.Fatalf("Meldung: %v", err)
	}
}

// TestMigratePartnerIDs: Verweise auf alte Partner-GUIDs werden auf die
// BP-Nummer umgestellt (Vertrag, Vertragspartner, Zahler).
func TestMigratePartnerIDs(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	if _, err := e.h.db.Exec(`UPDATE contract__condition SET payer_id = 'BP2' WHERE contract_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	e.h.rekeyed = map[string]string{"BP1": "100000", "BP2": "100001"}
	if err := e.m.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string]string{
		`SELECT partner_id FROM contract__contract WHERE contract_id = ?`: "100000",
		`SELECT partner_id FROM contract__partner WHERE contract_id = ?`:  "100000",
		`SELECT payer_id FROM contract__condition WHERE contract_id = ?`:  "100001",
	} {
		var got string
		if err := e.h.db.QueryRow(q, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s: %q %v", q, got, err)
		}
	}
	// Partnermodul nicht erreichbar: nichts geändert, kein Fehler
	e.h.rekeyed = nil
	if err := e.m.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
}
