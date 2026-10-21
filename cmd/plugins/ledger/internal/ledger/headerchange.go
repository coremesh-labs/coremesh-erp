package ledger

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Belegkopf ändern (seit 0.31.0, analog FB02): Gebuchte Belege bleiben in Beträgen,
// Konten und Daten unveränderlich (Korrektur = Storno). Änderbar sind nur Kopffelder
// ohne Wirkung auf die Buchung – Referenz (z. B. Rechnungsnummer des Lieferanten) und
// Belegkopftext. Jede Änderung steht mit altem und neuem Wert, Grund, Zeitpunkt und
// Benutzer im Änderungsprotokoll (ledger__journal_change, Abschnitt „Änderungen“ am
// Beleg) – nachvollziehbar nach GoBD. Andere Module ändern ihre Belege über dieselbe
// Aktion (Daten new_reference, new_header_text, change_reason; fehlt ein Feld, bleibt es).

const (
	changeObject       = "JournalEntryChange"
	changeHeaderAction = "changeHeader"
)

// headerChangeFields: änderbare Kopffelder (Spalte → Bezeichnung im Protokoll).
var headerChangeFields = []metamodel.Option{{Value: "reference", Label: "Referenz"}, {Value: "header_text", Label: "Belegkopftext"}}

// journalChange: Änderungsprotokoll der Belegköpfe, nur lesbar.
func (m *Module) journalChange() *crud.Entity {
	return &crud.Entity{
		Object: changeObject, Title: "Änderungen an Belegen", Icon: "icon-clock", Table: "ledger__journal_change", Section: "Belege",
		Keys: []string{"company_code_id", "fiscal_year", "document_number", "change_no", "field_name"}, ReadOnly: true,
		Order:   "changed_at DESC, document_number, change_no, field_name",
		Filters: []string{"company_code_id", "fiscal_year", "document_number", "field_name", "changed_by"},
		Fields: []crud.Field{
			{Key: "document_number", Label: "Beleg", Type: tText, ReadOnly: true, Listable: true},
			{Key: "company_code_id", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true, Listable: true, Lookup: lookupCC},
			{Key: "fiscal_year", Label: "Geschäftsjahr", Short: "GJ", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "change_no", Label: "Änderung", Short: "Nr.", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "field_name", Label: "Feld", Type: tSel, ReadOnly: true, Listable: true, Options: headerChangeFields},
			{Key: "old_value", Label: "Alter Wert", Type: tText, ReadOnly: true, Listable: true},
			{Key: "new_value", Label: "Neuer Wert", Type: tText, ReadOnly: true, Listable: true},
			{Key: "reason", Label: "Grund", Type: tText, ReadOnly: true, Listable: true},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true, Listable: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Listable: true, Lookup: crud.LookupUser},
		},
		Access: readByCompany(entryObject),
	}
}

// changeHeaderAction: „Kopf ändern …“ – Referenz und Belegkopftext eines gebuchten Belegs.
func (m *Module) changeHeaderAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	d := dataOf(req.Payload)
	id := crud.Str(p["id"])
	key, err := parseDocID(id)
	if err != nil {
		return sdk.Response{}, err
	}
	reason := strings.TrimSpace(crud.Str(d["change_reason"]))
	var changed []string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, `SELECT reference, header_text, document_type FROM ledger__journal_header
			WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, key.CompanyCode, key.FiscalYear, key.Number)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, id)
		}
		if err := requireCompanyCode(ctx, entryObject, changeHeaderAction, key.CompanyCode); err != nil {
			return err
		}
		r := res.Rows[0]
		old := map[string]string{"reference": crud.Str(r[0]), "header_text": crud.Str(r[1])}
		next := map[string]string{}
		for _, f := range headerChangeFields {
			if v, ok := d["new_"+f.Value]; ok {
				if s := strings.TrimSpace(crud.Str(v)); s != old[f.Value] {
					next[f.Value] = s
				}
			}
		}
		if ref, ok := next["reference"]; ok && ref == "" {
			if dt, err := m.documentType(ctx, crud.Str(r[2])); err == nil && dt.ReferenceRequired {
				return crud.Invalid("Belegart %s verlangt eine Referenz", crud.Str(r[2]))
			}
		}
		if len(next) == 0 {
			return nil
		}
		no := toInt(m.text(ctx, `SELECT COALESCE(MAX(change_no), 0) + 1 FROM ledger__journal_change
			WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, key.CompanyCode, key.FiscalYear, key.Number))
		at, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
		for _, f := range headerChangeFields {
			v, ok := next[f.Value]
			if !ok {
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__journal_change (company_code_id, fiscal_year, document_number, change_no, field_name,
				old_value, new_value, reason, changed_at, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				key.CompanyCode, key.FiscalYear, key.Number, no, f.Value, nilIfEmpty(old[f.Value]), nilIfEmpty(v), nilIfEmpty(reason), at, user); err != nil {
				return err
			}
			// Spaltenname aus headerChangeFields (fest), Wert als Parameter.
			if _, err := m.db.Exec(ctx, `UPDATE ledger__journal_header SET `+f.Value+` = ?
				WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, nilIfEmpty(v), key.CompanyCode, key.FiscalYear, key.Number); err != nil {
				return err
			}
			changed = append(changed, f.Label)
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Beleg %s: keine Änderung", key.Number)
	if len(changed) > 0 {
		msg = fmt.Sprintf("Beleg %s: %s geändert (Änderungsprotokoll)", key.Number, strings.Join(changed, " und "))
		m.emit(ctx, events.Event{Object: entryObject, Action: changeHeaderAction, CompanyCode: key.CompanyCode, EntityID: id, Source: Name,
			Data: map[string]any{"document_number": key.Number, "fiscal_year": key.FiscalYear, "fields": changed}})
	}
	return sdk.Response{Payload: map[string]any{"id": id, "changed": len(changed), "message": msg}}, nil
}

// entryFormState: Maske „Kopf ändern …“ mit den aktuellen Werten vorbelegt.
func (m *Module) entryFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" || req.Action != changeHeaderAction || req.ID == "" {
		return st, nil
	}
	key, err := parseDocID(req.ID)
	if err != nil {
		return st, nil
	}
	res, err := m.db.Query(ctx, `SELECT reference, header_text FROM ledger__journal_header WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`,
		key.CompanyCode, key.FiscalYear, key.Number)
	if err != nil || len(res.Rows) == 0 {
		return st, nil
	}
	for i, f := range headerChangeFields {
		if _, ok := req.Values["new_"+f.Value]; !ok {
			st.Fields["new_"+f.Value] = metamodel.FieldState{Value: ptr(crud.Str(res.Rows[0][i]))}
		}
	}
	st.Message = "Nur Referenz und Belegkopftext sind änderbar; Beträge, Konten und Daten korrigiert ein Storno. Die Änderung kommt ins Änderungsprotokoll des Belegs."
	return st, nil
}
