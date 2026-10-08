package contract

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestBankAccount: Bankkonto (IBAN mit Prüfziffer, Sachkonto je Zeitraum nur
// einmal) und Kreditkarte (nur die letzten 4 Ziffern, Belastung über das Bankkonto).
func TestBankAccount(t *testing.T) {
	e := setup(t)
	e.basics()
	e.h.accounts["1000|SKR25-1800"] = "Bank"
	e.h.accounts["1000|SKR25-1810"] = "Kreditkarte"
	bk := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "BK", "designation": "Girokonto Sparkasse",
		"partner_id": "INS", "external_number": "GIRO-1", "valid_from": "2026-01-01"})["contract_id"].(string)
	kk := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "KK", "designation": "Firmenkreditkarte",
		"partner_id": "INS", "external_number": "KK-1", "valid_from": "2026-01-01", "parent_contract_id": bk})["contract_id"].(string)
	acc := map[string]any{"company_code": "1000", "contract_id": bk, "valid_from": "2026-01-01", "kind": "ACCOUNT",
		"iban": "DE89 3704 0044 0532 0130 01", "gl_account": "1800"}
	expect(t, e.try(bankAccountObject, acc), sdk.ErrInvalidArgument, "IBAN mit falscher Prüfziffer")
	acc["iban"], acc["bic"], acc["credit_limit"] = "de89 3704 0044 0532 0130 00", "COBADEFFXXX", "5000"
	a := e.create(bankAccountObject, acc)
	if a["iban"] != "DE89370400440532013000" || a["gl_account"] != "SKR25-1800" || a["credit_limit"] != "5000.00" {
		t.Fatalf("Bankkonto: %v", a)
	}
	card := map[string]any{"company_code": "1000", "contract_id": kk, "valid_from": "2026-01-01", "kind": "CARD",
		"card_last4": "4111 1111 1111 1111", "gl_account": "1800"}
	expect(t, e.try(bankAccountObject, card), sdk.ErrInvalidArgument, "volle Kartennummer")
	card["card_last4"] = "1111"
	expect(t, e.try(bankAccountObject, card), sdk.ErrInvalidArgument, "Sachkonto schon beim Bankkonto")
	card["gl_account"], card["debit_contract_id"], card["card_expiry"] = "1810", kk, "09/2029"
	expect(t, e.try(bankAccountObject, card), sdk.ErrInvalidArgument, "Belastung über sich selbst")
	card["debit_contract_id"], card["card_expiry"] = nil, "13/2029"
	expect(t, e.try(bankAccountObject, card), sdk.ErrInvalidArgument, "Gültig bis")
	card["card_expiry"], card["settlement_day"] = "09/2029", 15
	if c := e.create(bankAccountObject, card); c["card_last4"] != "1111" || c["debit_contract_id"] != bk || c["iban"] != nil {
		t.Fatalf("Kreditkarte: %v", c)
	}
	expect(t, e.try(bankAccountObject, map[string]any{"company_code": "1000", "contract_id": bk, "valid_from": "2025-01-01", "kind": "ACCOUNT",
		"iban": "DE89370400440532013000", "gl_account": "1800"}), sdk.ErrInvalidArgument, "vor Vertragsbeginn")
}
