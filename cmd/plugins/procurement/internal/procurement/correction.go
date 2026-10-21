package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Referenz korrigieren (seit 0.13.0): Bei gebuchten Rechnungen sind Rechnungsnummer des
// Lieferanten und Text noch änderbar – sie wirken nicht auf die Buchung (Beträge, Konten,
// Steuer und Daten korrigiert nur ein Storno). Der Buchungsbeleg zieht Referenz und
// Belegkopftext über JournalEntry.changeHeader nach; das Hauptbuch führt das
// Änderungsprotokoll (alter und neuer Wert, Grund, Benutzer).

const correctAction = "correctReference"

// ledgerHeader: Belegkopftext des Buchungsbelegs – Rechnung und Text, sonst Lieferant.
func (m *Module) ledgerHeader(ctx context.Context, inv *invoiceRow) string {
	if inv.HeaderText != "" {
		return inv.ID + " " + inv.HeaderText
	}
	return strings.TrimSpace(inv.ID + " " + m.partnerName(ctx, inv.Supplier))
}

// correctReferenceAction: „Referenz korrigieren …“ an der gebuchten Rechnung.
func (m *Module) correctReferenceAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Reference  string `json:"new_supplier_reference"`
			HeaderText string `json:"new_invoice_text"`
			Reason     string `json:"change_reason"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	inv, err := m.invoiceOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, invoiceObject, correctAction, inv.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if inv.Status != invoicePosted {
		return sdk.Response{}, crud.Invalid("Rechnung %s ist %s – „Referenz korrigieren …“ gilt für gebuchte Rechnungen, sonst „Bearbeiten“",
			inv.ID, statusText(inv.Status))
	}
	ref, text := strings.TrimSpace(in.Data.Reference), strings.TrimSpace(in.Data.HeaderText)
	if ref == "" {
		return sdk.Response{}, crud.Invalid("Rechnungsnummer des Lieferanten ist Pflicht")
	}
	if ref == inv.Reference && text == inv.HeaderText {
		return sdk.Response{Payload: map[string]any{"id": in.ID, "message": fmt.Sprintf("Rechnung %s: keine Änderung", inv.ID)}}, nil
	}
	dup, err := m.db.Query(ctx, `SELECT invoice_id FROM procurement__invoice WHERE company_code = ? AND supplier_id = ? AND supplier_reference = ?
		AND status NOT IN (?, ?) AND invoice_id <> ?`, inv.CompanyCode, inv.Supplier, ref, invoiceCancelled, invoiceDiscarded, inv.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(dup.Rows) > 0 {
		return sdk.Response{}, crud.Invalid("Rechnung %s des Lieferanten ist bereits als %s erfasst", ref, crud.Str(dup.Rows[0][0]))
	}
	reason := "Rechnung " + inv.ID
	if r := strings.TrimSpace(in.Data.Reason); r != "" {
		reason += ": " + r
	}
	var msg string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		fixed := *inv
		fixed.Reference, fixed.HeaderText = ref, text
		if inv.DocumentID != "" {
			res, err := m.services.Call(ctx, "JournalEntry", "changeHeader", map[string]any{"id": inv.DocumentID, "data": map[string]any{
				"new_reference": ref, "new_header_text": m.ledgerHeader(ctx, &fixed), "change_reason": reason}})
			if err != nil {
				return unavailable("Hauptbuch", err)
			}
			msg = field(res.Payload, "message")
		}
		_, err := m.db.Exec(ctx, `UPDATE procurement__invoice SET supplier_reference = ?, header_text = ?, changed_at = ?, changed_by = ?
			WHERE company_code = ? AND invoice_id = ?`, ref, nilIfEmpty(text), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), inv.CompanyCode, inv.ID)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	id := inv.CompanyCode + "|" + inv.ID
	if err := events.Push(ctx, m.services, events.Event{Object: invoiceObject, Action: correctAction, CompanyCode: inv.CompanyCode,
		EntityID: id, Source: Name, Data: map[string]any{"supplier_reference": ref}}); err != nil {
		m.log.WarnContext(ctx, "SystemEvent nicht gemeldet", "object", invoiceObject, "action", correctAction, "err", err)
	}
	out := fmt.Sprintf("Rechnung %s: Rechnungsnummer %s, Text übernommen", inv.ID, ref)
	if msg != "" {
		out += "; " + msg
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "message": out}}, nil
}

// correctFormState: Maske „Referenz korrigieren …“ mit den Werten der Rechnung.
func (m *Module) correctFormState(ctx context.Context, req metamodel.FormStateRequest, st metamodel.FormState) metamodel.FormState {
	inv, err := m.invoiceOf(ctx, req.ID)
	if err != nil {
		return st
	}
	for k, v := range map[string]string{"new_supplier_reference": inv.Reference, "new_invoice_text": inv.HeaderText} {
		if _, ok := req.Values[k]; !ok {
			st.Fields[k] = metamodel.FieldState{Value: &v}
		}
	}
	st.Message = "Ändert nur Rechnungsnummer des Lieferanten und Text – auch im Buchungsbeleg " + inv.DocumentNumber +
		" (Referenz, Belegkopftext; mit Änderungsprotokoll). Beträge, Konten und Steuer korrigiert „Stornieren …“."
	if inv.Status != invoicePosted {
		st.Message = fmt.Sprintf("Rechnung %s ist %s – bitte „Bearbeiten“.", inv.ID, statusText(inv.Status))
	}
	return st
}
