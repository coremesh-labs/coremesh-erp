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

// Vertrag korrigieren (seit 0.39.0): Die Migration 0.34.0 übernahm den Vertrag
// einer Zahlung aus dem damaligen Ausgleich – der lief noch über Verträge
// hinweg; manche Zahlungen tragen so den Vertrag eines anderen Darlehens oder
// Objekts. Korrigiert wird nur der Vertrag an Debitoren- und Kreditorenpositionen
// (Beträge, Konten und Daten bleiben), mit altem und neuem Wert im
// Änderungsprotokoll; danach rechnet der automatische Ausgleich des Partners neu
// (von Hand zugeordnete bleiben). Positionen mit Ausgleich von Hand werden nicht
// geändert. derived: nur Werte, die eine Migration oder der Ausgleich gesetzt hat
// (für die Migrationen anderer Module, die den Vertrag besser kennen).

const correctContractAction = "correctContract"

func (m *Module) correctContractAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	d := dataOf(req.Payload)
	id := crud.Str(p["id"])
	key, err := parseDocID(id)
	if err != nil {
		return sdk.Response{}, err
	}
	contract := strings.TrimSpace(crud.Str(d["fix_contract"]))
	reason := strings.TrimSpace(crud.Str(d["change_reason"]))
	if contract == "" {
		return sdk.Response{}, crud.Invalid("Richtiger Vertrag ist Pflicht")
	}
	if reason == "" {
		return sdk.Response{}, crud.Invalid("Grund der Änderung ist Pflicht")
	}
	if err := requireCompanyCode(ctx, entryObject, correctContractAction, key.CompanyCode); err != nil {
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
	var manual []int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		n, manual, err = m.replaceContract(ctx, key, contract, reason, toInt(d["new_line"]), isTrue(d["derived"]))
		if err != nil || n == 0 {
			return err
		}
		_, err = m.clearPartners(ctx, key.CompanyCode, &key, "", true)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Beleg %s: keine Position zu korrigieren", key.Number)
	if n > 0 {
		msg = fmt.Sprintf("Beleg %s: Vertrag %s an %d Position(en) korrigiert, Ausgleich neu gerechnet (Änderungsprotokoll)", key.Number, contract, n)
		m.emit(ctx, events.Event{Object: entryObject, Action: correctContractAction, CompanyCode: key.CompanyCode, EntityID: id, Source: Name,
			Data: map[string]any{"document_number": key.Number, "fiscal_year": key.FiscalYear, "contract_id": contract, "lines": n}})
	}
	if len(manual) > 0 {
		msg += fmt.Sprintf("; Position(en) %v mit Ausgleich von Hand nicht geändert – erst den Ausgleich aufheben", manual)
	}
	return sdk.Response{Payload: map[string]any{"id": id, "changed": n, "skipped_manual": len(manual), "message": msg}}, nil
}

// replaceContract: Vertrag an Debitoren-/Kreditorenpositionen ersetzen, deren
// Vertrag gesetzt ist und abweicht; Anzahl geänderter Positionen und die wegen
// eines Ausgleichs von Hand übergangenen.
func (m *Module) replaceContract(ctx context.Context, key docKey, contract, reason string, line int64, derived bool) (int, []int64, error) {
	res, err := m.db.Query(ctx, `SELECT i.ledger, i.line_item_number, i.rent_contract_id,
		(SELECT COUNT(*) FROM ledger__clearing l WHERE l.company_code_id = i.company_code_id AND l.ledger = i.ledger AND l.method <> ?
		  AND ((l.debit_fiscal_year = i.fiscal_year AND l.debit_document_number = i.document_number AND l.debit_line = i.line_item_number)
		    OR (l.credit_fiscal_year = i.fiscal_year AND l.credit_document_number = i.document_number AND l.credit_line = i.line_item_number)))
		FROM ledger__journal_item i
		WHERE i.company_code_id = ? AND i.fiscal_year = ? AND i.document_number = ? AND i.account_kind IN ('D', 'K')
		AND COALESCE(i.rent_contract_id, '') NOT IN ('', ?) AND (? = 0 OR i.line_item_number = ?)
		ORDER BY i.line_item_number, i.ledger`, clearAuto, key.CompanyCode, key.FiscalYear, key.Number, contract, line, line)
	if err != nil || len(res.Rows) == 0 {
		return 0, nil, err
	}
	no := toInt(m.text(ctx, `SELECT COALESCE(MAX(change_no), 0) FROM ledger__journal_change
		WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, key.CompanyCode, key.FiscalYear, key.Number))
	at, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	logged, skipped := map[int64]bool{}, map[int64]bool{}
	var manual []int64
	for _, r := range res.Rows {
		ln, old := toInt(r[1]), crud.Str(r[2])
		if skipped[ln] {
			continue
		}
		if toInt(r[3]) > 0 {
			skipped[ln] = true
			manual = append(manual, ln)
			continue
		}
		if derived && !logged[ln] && !m.derivedContract(ctx, key, ln, old) {
			skipped[ln] = true
			continue
		}
		if _, err := m.db.Exec(ctx, `UPDATE ledger__journal_item SET rent_contract_id = ?
			WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ? AND ledger = ? AND line_item_number = ?`,
			contract, key.CompanyCode, key.FiscalYear, key.Number, r[0], ln); err != nil {
			return 0, nil, err
		}
		if logged[ln] { // je Position einmal, auch bei mehreren Ledgern
			continue
		}
		logged[ln] = true
		no++
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__journal_change (company_code_id, fiscal_year, document_number, change_no, field_name,
			old_value, new_value, reason, changed_at, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.CompanyCode, key.FiscalYear, key.Number, no, "rent_contract_id", fmt.Sprintf("Pos. %d: %s", ln, old), fmt.Sprintf("Pos. %d: %s", ln, contract),
			reason, at, user); err != nil {
			return 0, nil, err
		}
	}
	return len(logged), manual, nil
}

// derivedContract: Der jetzige Vertrag der Position kam per Nachtrag (Migration
// oder Ausgleich) in einen leeren Wert, nicht bei der Buchung oder von Hand.
func (m *Module) derivedContract(ctx context.Context, key docKey, line int64, contract string) bool {
	res, err := m.db.Query(ctx, `SELECT COALESCE(old_value, ''), COALESCE(reason, '') FROM ledger__journal_change
		WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ? AND field_name = 'rent_contract_id' AND new_value = ?
		ORDER BY change_no DESC LIMIT 1`, key.CompanyCode, key.FiscalYear, key.Number, fmt.Sprintf("Pos. %d: %s", line, contract))
	if err != nil || len(res.Rows) == 0 {
		return false
	}
	r := crud.Str(res.Rows[0][1])
	return crud.Str(res.Rows[0][0]) == "" && (strings.HasPrefix(r, "Migration ") || strings.Contains(r, "Ausgleich"))
}
