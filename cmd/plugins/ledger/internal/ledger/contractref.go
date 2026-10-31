package ledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
)

// Vertrag nachtragen (seit 0.34.0): Bis 0.33.1 blendete die Feldstatusgruppe der
// Kreditoren den Vertrag aus; Zahlungen auf Eingangsrechnungen und die Rechnungen
// selbst trugen ihn nicht. Gebuchte Belege bleiben in Beträgen, Konten und Daten
// unveränderlich – ergänzt wird nur ein leeres Feld Vertrag an Positionen, deren
// Feldstatus es zulässt (keine Steuerpositionen). Jede Position steht mit neuem
// Wert, Grund, Zeitpunkt und Benutzer im Änderungsprotokoll. Ein Storno käme nicht
// in Frage: Es verschöbe die Reihenfolge der Belege.

const addContractAction = "addContract"

// fillContract: leere Vertragsfelder eines Belegs mit contract füllen (partnerOnly:
// nur Debitoren- und Kreditorenpositionen); Anzahl der Positionen.
func (m *Module) fillContract(ctx context.Context, key docKey, contract, reason string, partnerOnly bool) (int, error) {
	contract = strings.TrimSpace(contract)
	if contract == "" {
		return 0, crud.Invalid("Vertrag ist Pflicht")
	}
	res, err := m.db.Query(ctx, `SELECT i.ledger, i.line_item_number FROM ledger__journal_item i
		LEFT JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		LEFT JOIN ledger__field_status s ON s.group_id = COALESCE(c.field_status_group, ?) AND s.field_name = 'rent_contract_id'
		WHERE i.company_code_id = ? AND i.fiscal_year = ? AND i.document_number = ? AND COALESCE(i.rent_contract_id, '') = ''
		AND COALESCE(i.item_type, '') <> ? AND COALESCE(s.status, ?) <> ? AND (? = 0 OR i.account_kind IN ('D', 'K'))
		ORDER BY i.line_item_number, i.ledger`, defaultFSG, key.CompanyCode, key.FiscalYear, key.Number, itemTax, statusOptional, statusSuppress, partnerOnly)
	if err != nil || len(res.Rows) == 0 {
		return 0, err
	}
	no := toInt(m.text(ctx, `SELECT COALESCE(MAX(change_no), 0) FROM ledger__journal_change
		WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, key.CompanyCode, key.FiscalYear, key.Number))
	at, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	logged := map[int64]bool{}
	for _, r := range res.Rows {
		line := toInt(r[1])
		if _, err := m.db.Exec(ctx, `UPDATE ledger__journal_item SET rent_contract_id = ?
			WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ? AND ledger = ? AND line_item_number = ?`,
			contract, key.CompanyCode, key.FiscalYear, key.Number, r[0], line); err != nil {
			return 0, err
		}
		if logged[line] { // je Position einmal, auch bei mehreren Ledgern
			continue
		}
		logged[line] = true
		no++
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__journal_change (company_code_id, fiscal_year, document_number, change_no, field_name,
			old_value, new_value, reason, changed_at, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.CompanyCode, key.FiscalYear, key.Number, no, "rent_contract_id", nil, fmt.Sprintf("Pos. %d: %s", line, contract),
			nilIfEmpty(reason), at, user); err != nil {
			return 0, err
		}
	}
	return len(logged), nil
}

// addContractAction: „Vertrag nachtragen …“ – auch für andere Module (Daten
// new_contract, change_reason), z. B. Bank und Einkauf bei der Migration.
func (m *Module) addContractAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	d := dataOf(req.Payload)
	id := crud.Str(p["id"])
	key, err := parseDocID(id)
	if err != nil {
		return sdk.Response{}, err
	}
	contract := strings.TrimSpace(crud.Str(d["new_contract"]))
	if contract == "" {
		return sdk.Response{}, crud.Invalid("Vertrag ist Pflicht")
	}
	if err := requireCompanyCode(ctx, entryObject, addContractAction, key.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if m.text(ctx, `SELECT document_number FROM ledger__journal_header WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`,
		key.CompanyCode, key.FiscalYear, key.Number) == "" {
		return sdk.Response{}, fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, id)
	}
	if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": key.CompanyCode + "|" + contract}); errors.Is(err, sdk.ErrNotFound) {
		return sdk.Response{}, crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht", contract, key.CompanyCode)
	} else if err != nil {
		return sdk.Response{}, err
	}
	var n int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		n, err = m.fillContract(ctx, key, contract, strings.TrimSpace(crud.Str(d["change_reason"])), false)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Beleg %s: keine Position ohne Vertrag", key.Number)
	if n > 0 {
		msg = fmt.Sprintf("Beleg %s: Vertrag %s an %d Position(en) nachgetragen (Änderungsprotokoll)", key.Number, contract, n)
		m.emit(ctx, events.Event{Object: entryObject, Action: addContractAction, CompanyCode: key.CompanyCode, EntityID: id, Source: Name,
			Data: map[string]any{"document_number": key.Number, "fiscal_year": key.FiscalYear, "contract_id": contract, "lines": n}})
	}
	return sdk.Response{Payload: map[string]any{"id": id, "changed": n, "message": msg}}, nil
}

const contractMigrationReason = "Migration 0.34.0: Vertrag "

// migrateContracts (0.34.0): Vertrag an Positionen ohne Vertrag aus dem Hauptbuch
// selbst ableiten – wiederholbar, nur eindeutige Fälle:
//   - aus dem Beleg: alle Positionen mit Vertrag nennen denselben;
//
// Nachgetragen wird nur an Debitoren- und Kreditorenpositionen (dort fehlte er);
// Sachkontenpositionen ohne Vertrag gelten z. B. dem ganzen Haus.
//   - aus dem Ausgleich: alle mit einer Debitoren-/Kreditorenposition ausgeglichenen
//     Gegenposten nennen denselben Vertrag, und der Beleg hat keinen anderen.
//
// Nach jeder Runde können neue eindeutige Fälle entstehen (Zahlung → Sollstellung).
func (m *Module) migrateContracts(ctx context.Context) error {
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for round := 0; round < 5; round++ {
			found := map[docKey]string{}
			reasons := map[docKey]string{}
			// aus dem Beleg
			res, err := m.db.Query(ctx, `SELECT company_code_id, fiscal_year, document_number, MIN(rent_contract_id) FROM ledger__journal_item
				GROUP BY company_code_id, fiscal_year, document_number
				HAVING COUNT(DISTINCT NULLIF(rent_contract_id, '')) = 1
				AND SUM(CASE WHEN COALESCE(rent_contract_id, '') = '' AND account_kind IN ('D', 'K') THEN 1 ELSE 0 END) > 0`)
			if err != nil {
				return err
			}
			for _, r := range res.Rows {
				k := docKey{CompanyCode: crud.Str(r[0]), FiscalYear: int(toInt(r[1])), Number: crud.Str(r[2])}
				found[k], reasons[k] = crud.Str(r[3]), contractMigrationReason+"aus dem Beleg"
			}
			// aus dem Ausgleich offener Posten
			res, err = m.db.Query(ctx, `SELECT i.company_code_id, i.fiscal_year, i.document_number,
				COUNT(DISTINCT NULLIF(o.rent_contract_id, '')), MIN(NULLIF(o.rent_contract_id, '')),
				SUM(CASE WHEN COALESCE(o.rent_contract_id, '') = '' THEN 1 ELSE 0 END)
				FROM ledger__journal_item i
				JOIN ledger__clearing c ON c.company_code_id = i.company_code_id AND c.ledger = i.ledger
					AND ((c.debit_fiscal_year = i.fiscal_year AND c.debit_document_number = i.document_number AND c.debit_line = i.line_item_number)
					  OR (c.credit_fiscal_year = i.fiscal_year AND c.credit_document_number = i.document_number AND c.credit_line = i.line_item_number))
				JOIN ledger__journal_item o ON o.company_code_id = c.company_code_id AND o.ledger = c.ledger
					AND ((o.fiscal_year = c.debit_fiscal_year AND o.document_number = c.debit_document_number AND o.line_item_number = c.debit_line)
					  OR (o.fiscal_year = c.credit_fiscal_year AND o.document_number = c.credit_document_number AND o.line_item_number = c.credit_line))
					AND NOT (o.fiscal_year = i.fiscal_year AND o.document_number = i.document_number AND o.line_item_number = i.line_item_number)
				WHERE i.account_kind IN ('D', 'K') AND COALESCE(i.rent_contract_id, '') = ''
				GROUP BY i.company_code_id, i.fiscal_year, i.document_number`)
			if err != nil {
				return err
			}
			for _, r := range res.Rows {
				k := docKey{CompanyCode: crud.Str(r[0]), FiscalYear: int(toInt(r[1])), Number: crud.Str(r[2])}
				if toInt(r[3]) != 1 || toInt(r[5]) != 0 {
					continue
				}
				c := crud.Str(r[4])
				if other, ok := found[k]; ok && other != c {
					delete(found, k) // widersprüchlich: nichts ändern
					continue
				}
				// der Beleg darf keinen anderen Vertrag nennen
				if m.text(ctx, `SELECT rent_contract_id FROM ledger__journal_item WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?
					AND COALESCE(rent_contract_id, '') NOT IN ('', ?) LIMIT 1`, k.CompanyCode, k.FiscalYear, k.Number, c) != "" {
					continue
				}
				if _, ok := found[k]; !ok {
					found[k], reasons[k] = c, contractMigrationReason+"aus dem Ausgleich offener Posten"
				}
			}
			changed := 0
			for k, c := range found {
				n, err := m.fillContract(ctx, k, c, reasons[k], true)
				if err != nil {
					return err
				}
				changed += n
			}
			if changed > 0 {
				m.log.InfoContext(ctx, "Vertrag an Belegpositionen nachgetragen", "positions", changed, "round", round+1)
			}
			if changed == 0 {
				return nil
			}
		}
		return nil
	})
}
