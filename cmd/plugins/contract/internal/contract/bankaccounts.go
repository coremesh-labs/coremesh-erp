package contract

import (
	"context"
	"math/big"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Bankkonto und Kreditkarte als Vertrag (z. B. Vertragsarten BK und KK, Partner
// Bank als Kreditor): Entgelte und Gebühren sind Konditionen wie bei jedem
// Vertrag; die Kontodaten trägt ContractBankAccount (Zeitscheiben, z. B. neuer
// Dispositionsrahmen, neue Karte):
//
//   - Art: Bankkonto oder Kreditkarte;
//   - Bankkonto: IBAN (Prüfziffer), BIC, Kontoinhaber, Dispositionsrahmen;
//   - Kreditkarte: nur die letzten vier Ziffern der Kartennummer (die volle
//     Nummer wird nicht gespeichert), gültig bis, Limit, Abrechnungstag,
//     Belastung über einen Bankkonto-Vertrag (Vorschlag: der Bezugsvertrag);
//   - Sachkonto im Hauptbuch (Bank bzw. Kreditkartenverrechnung) – je Zeitraum
//     höchstens ein Vertrag je Sachkonto.

const bankAccountObject = "ContractBankAccount"

const (
	bankKindAccount = "ACCOUNT"
	bankKindCard    = "CARD"
)

var (
	bankKindOptions = []metamodel.Option{{Value: bankKindAccount, Label: "Bankkonto"}, {Value: bankKindCard, Label: "Kreditkarte"}}
	ibanRe          = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$`)
	bicRe           = regexp.MustCompile(`^[A-Z]{6}[A-Z0-9]{2}([A-Z0-9]{3})?$`)
	last4Re         = regexp.MustCompile(`^[0-9]{4}$`)
	expiryRe        = regexp.MustCompile(`^(0[1-9]|1[0-2])/[0-9]{4}$`)

	bankAccountSection = metamodel.SectionDefinition{Key: "bankkonto", Title: "Bankkonto / Kreditkarte", Collapsed: true,
		Relation: &metamodel.Relation{Object: bankAccountObject, ForeignKey: "contract_id", Match: match(),
			Columns: []string{"kind", "iban", "card_last4", "gl_account", "credit_limit", "valid_from", "valid_to"}}}
)

func (m *Module) bankAccount() *crud.Entity {
	card := &metamodel.Condition{Field: "kind", Values: []string{bankKindCard}}
	acct := &metamodel.Condition{Field: "kind", Values: []string{bankKindAccount}}
	return &crud.Entity{
		Object: bankAccountObject, Title: "Bankkonten und Kreditkarten", Icon: "icon-coins", Table: "contract__bank_account", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "valid_from"}, TimeSlice: true, Order: "company_code, contract_id, valid_from",
		Filters: []string{"company_code", "contract_id", "kind", "gl_account"}, Search: []string{"iban", "account_holder"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "kind", Label: "Art", Type: tSel, Required: true, Listable: true, Options: bankKindOptions},
			crud.Field{Key: "account_holder", Label: "Kontoinhaber bzw. Karteninhaber", Type: tText, Listable: true},
			crud.Field{Key: "iban", Label: "IBAN", Type: tText, Listable: true, Group: "Bankkonto", ShowIf: acct},
			crud.Field{Key: "bic", Label: "BIC", Type: tText, Group: "Bankkonto", ShowIf: acct},
			crud.Field{Key: "card_last4", Label: "Kartennummer (nur die letzten 4 Ziffern)", Type: tText, Listable: true, Group: "Kreditkarte", ShowIf: card},
			crud.Field{Key: "card_expiry", Label: "Gültig bis (MM/JJJJ)", Type: tText, Group: "Kreditkarte", ShowIf: card},
			crud.Field{Key: "settlement_day", Label: "Abrechnungstag", Type: tNum, Group: "Kreditkarte", ShowIf: card},
			crud.Field{Key: "debit_contract_id", Label: "Belastung über Bankkonto (Vertrag)", Type: tText, Group: "Kreditkarte", ShowIf: card,
				Lookup: contractLookup},
			crud.Field{Key: "credit_limit", Label: "Dispositionsrahmen bzw. Kartenlimit", Type: tText, Listable: true, Group: "Rahmen"},
			crud.Field{Key: "debit_rate", Label: "Sollzins % p. a.", Type: tText, Group: "Rahmen"},
			crud.Field{Key: "credit_rate", Label: "Habenzins % p. a.", Type: tText, Group: "Rahmen"},
			crud.Field{Key: "gl_account", Label: "Sachkonto (Bank bzw. Kreditkartenverrechnung)", Type: tText, Required: true, Listable: true,
				Group: "Buchung", Lookup: glLookup},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkBankAccount,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "credit_limit")
		},
	}
}

func (m *Module) checkBankAccount(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	c, err := m.contractOf(ctx, cc, crud.Str(rec["contract_id"]))
	if err != nil {
		return err
	}
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])
	if err := within("Kontodaten", from, to, c); err != nil {
		return err
	}
	clean := func(k string) string {
		v := strings.ReplaceAll(trimUpper(rec[k]), " ", "")
		rec[k] = nilIfEmpty(v)
		return v
	}
	iban, bic := clean("iban"), clean("bic")
	last4, expiry := clean("card_last4"), clean("card_expiry")
	switch crud.Str(rec["kind"]) {
	case bankKindAccount:
		if iban == "" {
			return crud.Invalid("Bankkonto: IBAN ist Pflicht")
		}
		rec["card_last4"], rec["card_expiry"], rec["settlement_day"], rec["debit_contract_id"] = nil, nil, nil, nil
	case bankKindCard:
		if len(strings.Trim(last4, "0123456789")) == 0 && len(last4) > 4 {
			return crud.Invalid("Kreditkarte: nur die letzten 4 Ziffern erfassen – die volle Kartennummer wird nicht gespeichert")
		}
		if !last4Re.MatchString(last4) {
			return crud.Invalid("Kreditkarte: letzte 4 Ziffern der Kartennummer erwartet")
		}
		if expiry != "" && !expiryRe.MatchString(expiry) {
			return crud.Invalid("Gültig bis: MM/JJJJ, z. B. 09/2029")
		}
		if d := toInt(rec["settlement_day"]); rec["settlement_day"] != nil && crud.Str(rec["settlement_day"]) != "" && (d < 1 || d > 31) {
			return crud.Invalid("Abrechnungstag: 1 bis 31")
		}
		dc := strings.TrimSpace(crud.Str(rec["debit_contract_id"]))
		if dc == "" { // Vorschlag: der Bezugsvertrag (z. B. Kreditkarte → Bankkonto)
			res, err := m.db.Query(ctx, `SELECT parent_contract_id FROM contract__contract WHERE company_code = ? AND contract_id = ?`, cc, c.ID)
			if err != nil {
				return err
			}
			if len(res.Rows) > 0 {
				dc = crud.Str(res.Rows[0][0])
			}
		}
		if dc != "" {
			if dc == c.ID {
				return crud.Invalid("Belastung über Bankkonto: anderer Vertrag erwartet")
			}
			res, err := m.db.Query(ctx, `SELECT 1 FROM contract__bank_account WHERE company_code = ? AND contract_id = ? AND kind = ?`, cc, dc, bankKindAccount)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				return crud.Invalid("Belastung über Bankkonto: Vertrag %s hat kein Bankkonto", dc)
			}
			rec["debit_contract_id"] = dc
		} else {
			rec["debit_contract_id"] = nil
		}
		rec["bic"] = nilIfEmpty(bic)
	default:
		return crud.Invalid("Art: Bankkonto oder Kreditkarte")
	}
	if iban != "" {
		if !ibanRe.MatchString(iban) || !ibanValid(iban) {
			return crud.Invalid("IBAN %s ist ungültig (Prüfziffer)", iban)
		}
	}
	if bic != "" && !bicRe.MatchString(bic) {
		return crud.Invalid("BIC %s ist ungültig (8 oder 11 Zeichen)", bic)
	}
	if v := strings.TrimSpace(crud.Str(rec["credit_limit"])); v != "" {
		minor, err := parseAmount(v, m.currencyDecimals(ctx, c.Currency))
		if err != nil {
			return crud.Invalid("Rahmen: %v", err)
		}
		if minor < 0 {
			return crud.Invalid("Rahmen: 0 oder mehr")
		}
		rec["credit_limit"] = minor
	} else {
		rec["credit_limit"] = nil
	}
	for _, k := range []string{"debit_rate", "credit_rate"} {
		v := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec[k])), ",", ".")
		if v != "" && !rateRe.MatchString(v) {
			return crud.Invalid("Zinssatz in %%, z. B. 9,75")
		}
		rec[k] = nilIfEmpty(v)
	}
	nr, _, err := m.glAccount(ctx, cc, crud.Str(rec["gl_account"]))
	if err != nil {
		return err
	}
	rec["gl_account"] = nr
	// Ein Sachkonto gehört im selben Zeitraum zu höchstens einem Vertrag.
	res, err := m.db.Query(ctx, `SELECT contract_id FROM contract__bank_account WHERE company_code = ? AND gl_account = ? AND contract_id <> ?
		AND valid_from <= ? AND valid_to >= ?`, cc, nr, c.ID, to, from)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		return crud.Invalid("Sachkonto %s gehört schon zum Vertrag %s", nr, crud.Str(res.Rows[0][0]))
	}
	return nil
}

// ibanValid: Prüfziffer nach ISO 13616 (mod 97 = 1).
func ibanValid(iban string) bool {
	s := iban[4:] + iban[:4]
	var digits strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			digits.WriteString(big.NewInt(int64(r - 'A' + 10)).String())
		default:
			return false
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}
