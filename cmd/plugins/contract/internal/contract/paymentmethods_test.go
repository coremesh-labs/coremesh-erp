package contract

import (
	"fmt"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// TestPaymentMethods: Zahlwege aus dem Katalog; das Merkmal „mit Mandat“
// blendet die Mandatsfelder ein, auch für einen selbst angelegten Zahlweg.
func TestPaymentMethods(t *testing.T) {
	e := setup(t)
	e.basics()
	if n, err := e.m.seedPaymentMethods(e.ctx, "1000"); err != nil || n != 0 {
		t.Fatalf("Vorschlag schon beim Einrichten: %d %v", n, err)
	}
	e.create(paymentMethodObject, map[string]any{"company_code": "1000", "code": "FIRMENLS", "name": "Firmenlastschrift", "uses_mandate": true})
	id := e.rentContract()
	_, err := e.call("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"payment_method": "SCHECK"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Zahlweg")
	e.must("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"payment_method": "firmenls", "mandate_ref": " M-1 ", "creditor_id": "de98 zzz 09999999999"}})
	c := e.must("Contract", "get", map[string]any{"id": "1000|" + id})
	if c["payment_method"] != "FIRMENLS" || c["mandate_ref"] != "M-1" || c["creditor_id"] != "DE98ZZZ09999999999" {
		t.Fatalf("Vertrag: %v", c)
	}
	visible := func(method string) bool {
		t.Helper()
		st, err := e.m.contractFormState(e.ctx, metamodel.FormStateRequest{Mode: "edit", ID: "1000|" + id, Values: map[string]string{"payment_method": method}})
		if err != nil {
			t.Fatal(err)
		}
		return *st.Fields["mandate_ref"].Visible
	}
	if !visible("FIRMENLS") || !visible("DIRECT_DEBIT") || visible("STANDING_ORDER") || visible("") {
		t.Fatal("Mandatsfelder nach Merkmal")
	}
	_, err = e.call(reminderRuleObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "contract_type": "MV", "condition_kind": "FO",
		"occasion": "AMOUNT_CHANGE", "description": "x", "lead_days": 10, "payment_methods": "SCHECK"}})
	expect(t, err, sdk.ErrInvalidArgument, "Regel mit unbekanntem Zahlweg")
}

// TestPaymentDue: „Zahlung fällig“ nur bei Zahlwegen mit Merkmal „selbst
// überweisen“; je Vertrag und Fälligkeit eine Erinnerung mit Summe; bezahlt → entfallen.
func TestPaymentDue(t *testing.T) {
	e := setup(t)
	e.basics()
	e.partner("WEG", "WEG Brunnenstraße", "CREDITOR")
	mk := func(method string) string {
		id := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "WH", "designation": "Hausgeld",
			"partner_id": "WEG", "valid_from": "2026-01-01", "payment_method": method})["contract_id"].(string)
		e.create("ContractObject", map[string]any{"company_code": "1000", "contract_id": id, "object_type": "RentObject",
			"object_id": "LpzBrn1WG001", "is_main": true, "valid_from": "2026-01-01"})
		for typ, amt := range map[string]string{"HV": "244,00", "RZ": "51,00"} {
			e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": id, "condition_type": typ,
				"calc_method": "FIXED", "unit_price": amt, "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
			if _, err := e.m.db.Exec(e.ctx, `INSERT INTO contract__posting (company_code, contract_id, condition_type, object_id, position, period_from,
				sequence, kind, period_to, due_date, amount, currency, account_number, status, draft_id, document_id, document_number, recorded_at)
				VALUES ('1000', ?, ?, '', 1, '2026-12-01', 0, 'ORIGINAL', '2026-12-31', '2026-12-01', ?, 'EUR', '6000', 'POSTED', 'D', ?, '100', 'x')`,
				id, typ, map[string]int64{"HV": 24400, "RZ": 5100}[typ], "1000|2026|100"); err != nil {
				t.Fatal(err)
			}
		}
		e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
		return id
	}
	self, standing := mk("TRANSFER"), mk("STANDING_ORDER")
	e.create(reminderRuleObject, map[string]any{"company_code": "1000", "contract_type": "WH", "condition_kind": "VO", "occasion": "PAYMENT_DUE",
		"description": "Hausgeld überweisen", "lead_days": 5, "method_flag": "SELF_TRANSFER", "active_from": "2026-01-01"})
	e.h.openItems = []any{map[string]any{"fiscal_year": 2026, "document_number": "100", "open_minor": -29500}}
	rows := func(today string) string {
		t.Helper()
		if err := e.m.refreshRemindersTx(e.ctx, today); err != nil {
			t.Fatal(err)
		}
		res, _ := e.m.db.Query(e.ctx, `SELECT contract_id, remind_date, status, detail FROM contract__reminder WHERE occasion = 'PAYMENT_DUE' ORDER BY contract_id`)
		for _, r := range res.Rows {
			r[1] = dateOf(r[1])
		}
		return fmt.Sprint(res.Rows)
	}
	if got, want := rows("2026-11-27"), "[["+self+" 2026-11-26 OPEN fällig 01.12.2026: 295,00 EUR (HV 244,00, RZ 51,00)]]"; got != want {
		t.Fatalf("fällig: %s, erwartet %s (nicht %s)", got, want, standing)
	}
	e.h.openItems = nil
	if got := rows("2026-11-28"); got != "[["+self+" 2026-11-26 OBSOLETE fällig 01.12.2026: 295,00 EUR (HV 244,00, RZ 51,00)]]" {
		t.Fatalf("bezahlt: %s", got)
	}
	_, err := e.call(paymentMethodObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "BEIDES", "name": "x",
		"uses_mandate": true, "self_transfer": true}})
	expect(t, err, sdk.ErrInvalidArgument, "zwei Merkmale")
}
