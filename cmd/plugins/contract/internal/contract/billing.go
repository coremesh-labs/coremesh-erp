package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Sollstellung: Rechnen und Buchen übernimmt das Plugin contract-billing
// (Haskell, Object ContractBilling) – ohne eigene Daten. Dieses Modul hält
// Läufe und gebuchte Fälligkeiten und stellt die Oberfläche:
//
//   - Buchungsläufe (ContractPostingRun): „Buchungslauf …“ bucht im
//     Buchungskreis alle Fälligkeiten bis zum Stichtag, die noch nicht vermerkt
//     sind, und rechnet schon vermerkte Perioden nach (Nachberechnung:
//     rückwirkende Änderungen, Minderungen); „Vorschau …“ plant nur. Das
//     Formular schlägt heute vor und zeigt den letzten Lauf.
//   - Sollstellungen (ContractPosting): je Fälligkeit Vorerfassung und Status
//     (vorerfasst, gebucht); auch als Abschnitt am Vertrag und am Lauf.
//   - „Buchen …“ am Vertrag: ein Lauf für einen Vertrag.
//   - ContractPostingService.record: contract-billing vermerkt die
//     Fälligkeiten eines Belegs – in seiner Transaktion, zusammen mit
//     Vorerfassung und Buchung.
//   - ContractPostingService.onEvent: Vorerfassungen, die im Hauptbuch gebucht
//     oder verworfen werden (SystemEvent JournalDraft.post/deactivate).
//     Verworfene Fälligkeiten werden wieder offen. Jeder Lauf gleicht vorher
//     die offenen Vorerfassungen ab (Events gehen höchstens einmal zu).

const (
	billingObject = "ContractBilling"
	runObject     = "ContractPostingRun"
	postingObject = "ContractPosting"
	serviceObject = "ContractPostingService"

	postingDraft  = "DRAFT"
	postingPosted = "POSTED"

	kindOriginal   = "ORIGINAL"
	kindCorrection = "CORRECTION"
)

var (
	runStatusOptions = []metamodel.Option{
		{Value: "RUNNING", Label: "läuft"}, {Value: "DONE", Label: "ausgeführt"}, {Value: "PARTIAL", Label: "teilweise ausgeführt (Meldungen)"},
		{Value: "FAILED", Label: "nicht ausgeführt (Meldungen)"}, {Value: "EMPTY", Label: "nichts fällig"}}
	postingStatusOptions = []metamodel.Option{{Value: postingDraft, Label: "vorerfasst"}, {Value: postingPosted, Label: "gebucht"}}
	postingKindOptions   = []metamodel.Option{{Value: kindOriginal, Label: "Sollstellung"}, {Value: kindCorrection, Label: "Nachberechnung"}}
	lookupRun            = &metamodel.Lookup{Object: runObject, ValueField: "id", LabelFields: []string{"message"}}
)

func (m *Module) postingRun() *crud.Entity {
	return &crud.Entity{
		Object: runObject, Title: "Buchungsläufe", Icon: "icon-coins", Table: "contract__posting_run", Section: "Buchung",
		Keys: []string{"id"}, Surrogate: true, ReadOnly: true, Order: "started_at DESC", TitleField: "message",
		Filters: []string{"company_code", "status"},
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Lookup: lookupCC},
			{Key: "to_date", Label: "Buchen bis (Fälligkeit)", Type: tDate, Required: true, Listable: true},
			{Key: "contract_id", Label: "Nur Vertrag", Type: tText, Listable: true, ReadOnly: true, Lookup: contractLookup},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: runStatusOptions},
			{Key: "documents", Label: "Belege", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "drafts", Label: "davon nur vorerfasst", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "items", Label: "Fälligkeiten", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "errors", Label: "Meldungen", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "corrections", Label: "davon Nachberechnungen", Type: tNum, ReadOnly: true},
			{Key: "message", Label: "Ergebnis", Type: tText, ReadOnly: true},
			{Key: "messages", Label: "Meldungen je Vertrag", Type: tArea, ReadOnly: true},
			{Key: "started_at", Label: "Gestartet am", Type: tText, Listable: true, ReadOnly: true},
			{Key: "started_by", Label: "Gestartet von", Type: tText, ReadOnly: true},
			{Key: "finished_at", Label: "Beendet am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "lauf", Title: "Buchungslauf", Fields: []string{"company_code", "to_date", "contract_id", "status", "message", "documents", "drafts",
				"items", "corrections", "errors", "messages", "started_at", "started_by", "finished_at", "id"}},
			{Key: "sollstellungen", Title: "Sollstellungen", Relation: &metamodel.Relation{Object: postingObject, ForeignKey: "run_id",
				Columns: []string{"contract_id", "condition_type", "kind", "period_from", "period_to", "due_date", "amount", "status", "document_number"}}},
		},
		Access:    &crud.Access{Records: true, CompanyCode: "company_code"},
		FormState: m.runFormState,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "execute", Label: "Buchungslauf …", Fields: []string{"company_code", "to_date"}, FormState: true},
				Handle: m.executeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "preview", Label: "Vorschau …", Fields: []string{"company_code", "to_date"}, FormState: true},
				Handle: m.previewAction},
		},
	}
}

// runFormState: „Buchungslauf …“ und „Vorschau …“ – Buchen bis = heute,
// Buchungskreis und Hinweis aus dem letzten Lauf.
func (m *Module) runFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" {
		return st, nil
	}
	today := time.Now().Format(time.DateOnly)
	st.Fields["to_date"] = metamodel.FieldState{Value: &today}
	res, err := m.db.Query(ctx, `SELECT company_code, to_date, started_at, message FROM contract__posting_run
		WHERE contract_id IS NULL AND status <> 'RUNNING' ORDER BY started_at DESC LIMIT 1`)
	if err != nil || len(res.Rows) == 0 {
		st.Message = "Noch kein Buchungslauf. Gebucht wird alles, was bis „Buchen bis“ fällig und noch nicht gebucht ist."
		return st, err
	}
	r := res.Rows[0]
	cc := crud.Str(r[0])
	st.Fields["company_code"] = metamodel.FieldState{Value: &cc}
	st.Message = fmt.Sprintf("Letzter Lauf (Buchungskreis %s) am %s, gebucht bis %s: %s", cc, germanDate(crud.Str(r[2])),
		germanDate(crud.Str(r[1])), crud.Str(r[3]))
	return st, nil
}

// contractFormState: „Buchen …“ am Vertrag – Buchen bis = heute, Hinweis auf
// die letzte Sollstellung des Vertrags.
func (m *Module) contractFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	if req.Mode != "action" || req.Action != "post" {
		return st, nil
	}
	today := time.Now().Format(time.DateOnly)
	st.Fields["post_until"] = metamodel.FieldState{Value: &today}
	key, err := m.set.Entity("Contract").ParseID(req.ID)
	if err != nil {
		return st, nil
	}
	res, err := m.db.Query(ctx, `SELECT due_date, document_number FROM contract__posting WHERE company_code = ? AND contract_id = ?
		AND kind = ? ORDER BY due_date DESC LIMIT 1`, key["company_code"], key["contract_id"], kindOriginal)
	if err != nil {
		return st, err
	}
	if len(res.Rows) == 0 {
		st.Message = "Noch keine Sollstellung für diesen Vertrag."
	} else if doc := crud.Str(res.Rows[0][1]); doc != "" {
		st.Message = fmt.Sprintf("Zuletzt gebucht: Fälligkeit %s (Beleg %s)", germanDate(crud.Str(res.Rows[0][0])), doc)
	} else {
		st.Message = fmt.Sprintf("Zuletzt vorerfasst: Fälligkeit %s", germanDate(crud.Str(res.Rows[0][0])))
	}
	return st, nil
}

// germanDate: "2026-10-08…" → "08.10.2026".
func germanDate(s string) string {
	if len(s) < 10 || s[4] != '-' || s[7] != '-' {
		return s
	}
	return s[8:10] + "." + s[5:7] + "." + s[0:4]
}

func (m *Module) posting() *crud.Entity {
	return &crud.Entity{
		Object: postingObject, Title: "Sollstellungen", Icon: "icon-list", Table: "contract__posting", Section: "Buchung",
		Keys:     []string{"company_code", "contract_id", "condition_type", "object_id", "period_from", "sequence"},
		ReadOnly: true, Order: "company_code, contract_id, due_date, condition_type, sequence",
		Filters: []string{"company_code", "contract_id", "condition_type", "kind", "status", "run_id", "draft_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "condition_type", Label: "Konditionsart", Type: tText, Listable: true, Lookup: catalogLookup("ConditionType")},
			{Key: "object_id", Label: "Objekt der Kondition", Type: tText},
			{Key: "period_from", Label: "Von", Type: tDate, Listable: true},
			{Key: "sequence", Label: "Lfd. Nr. (0 = erste Sollstellung)", Type: tNum},
			{Key: "kind", Label: "Art", Type: tSel, Listable: true, Options: postingKindOptions},
			{Key: "billing_period", Label: "Periode ab", Type: tDate},
			{Key: "period_to", Label: "Bis", Type: tDate, Listable: true},
			{Key: "due_date", Label: "Fällig am", Type: tDate, Listable: true},
			{Key: "amount", Label: "Betrag", Type: tNum, Listable: true},
			{Key: "currency", Label: "Währung", Type: tText},
			{Key: "account_number", Label: "Sachkonto", Type: tText},
			{Key: "rent_object_id", Label: "Mietobjekt", Type: tText,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}}},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, Options: postingStatusOptions},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText,
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true},
			{Key: "document_id", Label: "Beleg (ID)", Type: tText,
				Lookup: &metamodel.Lookup{Object: "JournalEntry", ValueField: "id", LabelFields: []string{"document_number"}}},
			{Key: "run_id", Label: "Buchungslauf", Type: tText, Lookup: lookupRun},
			{Key: "recorded_at", Label: "Vermerkt am", Type: tText},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Decorate: m.decoratePosting,
	}
}

// decoratePosting: Betrag als Dezimaltext in der Währung.
func (m *Module) decoratePosting(ctx context.Context, rec crud.Record) error {
	cur := crud.Str(rec["currency"])
	rec["amount"] = formatAmount(toInt(rec["amount"]), m.currencyDecimals(ctx, cur))
	labels(rec)["amount"] = crud.Str(rec["amount"]) + " " + cur
	return nil
}

func (m *Module) registerBilling(r *module.Router) {
	r.Object(serviceObject).
		Handle("record", m.recordAction).
		Handle("recordSettlement", m.recordSettlementAction).
		Handle(events.CallbackAction, m.onDraftEvent)
}

// subscribeBilling: Vorerfassungen, die im Hauptbuch gebucht oder verworfen werden.
func (m *Module) subscribeBilling(ctx context.Context) {
	for _, a := range []string{"post", "deactivate"} {
		if err := events.Register(ctx, m.services, events.Subscription{Object: "JournalDraft", Action: a, CompanyCode: events.All,
			Callback: serviceObject}); err != nil {
			m.log.WarnContext(ctx, "Event nicht abonniert", "object", "JournalDraft", "action", a, "err", err.Error())
		}
	}
}

// billingCall leitet an das Plugin contract-billing weiter; fehlt es, eine klare Meldung.
func (m *Module) billingCall(ctx context.Context, action string, payload map[string]any) (sdk.Response, error) {
	resp, err := m.services.Call(ctx, billingObject, action, payload)
	if errors.Is(err, sdk.ErrUnimplemented) {
		return resp, fmt.Errorf("%w: Vertragsbuchung nicht verfügbar – Plugin contract-billing ist nicht gestartet", sdk.ErrUnavailable)
	}
	return resp, err
}

type runInput struct {
	ID   string `json:"id"`
	Data struct {
		CompanyCode string `json:"company_code"`
		ToDate      string `json:"to_date"`
		PostUntil   string `json:"post_until"`
	} `json:"data"`
	CompanyCode string `json:"company_code"`
	ToDate      string `json:"to_date"`
}

func decodeRun(payload any) (cc, to string, err error) {
	var in runInput
	if err := sdk.Decode(payload, &in); err != nil {
		return "", "", err
	}
	cc, to = strings.TrimSpace(in.Data.CompanyCode+in.CompanyCode), strings.TrimSpace(in.Data.ToDate+in.ToDate)
	if cc == "" || to == "" {
		return "", "", crud.Invalid("Buchungskreis und „Buchen bis“ sind Pflicht")
	}
	return cc, to, nil
}

// previewAction: „Vorschau …“ – plant nur, nichts wird angelegt.
func (m *Module) previewAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc, to, err := decodeRun(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, runObject, "preview", cc); err != nil {
		return sdk.Response{}, err
	}
	return m.billingCall(ctx, "run", map[string]any{"company_code": cc, "to_date": to, "simulate": true})
}

// executeAction: „Buchungslauf …“ für einen Buchungskreis.
func (m *Module) executeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc, to, err := decodeRun(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, runObject, "execute", cc); err != nil {
		return sdk.Response{}, err
	}
	return m.execute(ctx, cc, to, "")
}

// postAction: „Buchen …“ am Vertrag – alle Fälligkeiten des Vertrags bis zum Stichtag.
func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in runInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	if strings.TrimSpace(in.Data.PostUntil) == "" {
		return sdk.Response{}, crud.Invalid("„Buchen bis“ ist Pflicht")
	}
	if err := requireWrite(ctx, "Contract", "post", cc); err != nil {
		return sdk.Response{}, err
	}
	return m.execute(ctx, cc, strings.TrimSpace(in.Data.PostUntil), id)
}

// execute: offene Vorerfassungen abgleichen, Lauf anlegen, contract-billing
// rechnen und buchen lassen, Ergebnis am Lauf vermerken.
func (m *Module) execute(ctx context.Context, cc, to, contractID string) (sdk.Response, error) {
	if _, err := crud.ParseDate(to); err != nil {
		return sdk.Response{}, err
	}
	if err := m.reconcileDrafts(ctx, cc); err != nil {
		return sdk.Response{}, err
	}
	runID := crud.NewID()
	if _, err := m.db.Exec(ctx, `INSERT INTO contract__posting_run (id, company_code, to_date, contract_id, status, started_at, started_by)
		VALUES (?, ?, ?, ?, 'RUNNING', ?, ?)`, runID, cc, to, nilIfEmpty(contractID), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID)); err != nil {
		return sdk.Response{}, err
	}
	p := map[string]any{"company_code": cc, "to_date": to, "run_id": runID}
	if contractID != "" {
		p["contract_id"] = contractID
	}
	resp, err := m.billingCall(ctx, "run", p)
	var out struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Documents   int    `json:"documents"`
		Drafts      int    `json:"drafts"`
		Items       int    `json:"items"`
		Corrections int    `json:"corrections"`
		Errors      int    `json:"errors"`
		Messages    []struct {
			ContractID string `json:"contract_id"`
			Message    string `json:"message"`
		} `json:"messages"`
	}
	if err == nil {
		err = sdk.Decode(resp.Payload, &out)
	}
	if err != nil {
		out.Status, out.Message, out.Errors = "FAILED", err.Error(), 1
	}
	var lines []string
	for _, msg := range out.Messages {
		lines = append(lines, msg.ContractID+": "+msg.Message)
	}
	if _, uerr := m.db.Exec(ctx, `UPDATE contract__posting_run SET status = ?, documents = ?, drafts = ?, items = ?, corrections = ?, errors = ?,
		message = ?, messages = ?, finished_at = ? WHERE id = ?`, out.Status, out.Documents, out.Drafts, out.Items, out.Corrections, out.Errors, out.Message,
		nilIfEmpty(strings.Join(lines, "\n")), now(), runID); uerr != nil {
		return sdk.Response{}, uerr
	}
	if err != nil {
		return sdk.Response{}, err
	}
	run, err := m.set.Entity(runObject).Load(ctx, crud.Record{"id": runID})
	if err != nil {
		return sdk.Response{}, err
	}
	run["_id"] = runID
	return sdk.Response{Payload: run}, nil
}

// recordAction: contract-billing vermerkt die Fälligkeiten eines Belegs (in
// dessen Transaktion). Payload: company_code, run_id, draft_id, status
// (DRAFT|POSTED), document_id, document_number, currency, entries[].
func (m *Module) recordAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode    string `json:"company_code"`
		RunID          string `json:"run_id"`
		DraftID        string `json:"draft_id"`
		Status         string `json:"status"`
		DocumentID     string `json:"document_id"`
		DocumentNumber string `json:"document_number"`
		Currency       string `json:"currency"`
		Entries        []struct {
			ContractID    string `json:"contract_id"`
			ConditionType string `json:"condition_type"`
			ObjectID      string `json:"object_id"`
			PeriodFrom    string `json:"period_from"`
			PeriodTo      string `json:"period_to"`
			BillingPeriod string `json:"billing_period"`
			Kind          string `json:"kind"`
			DueDate       string `json:"due_date"`
			Amount        int64  `json:"amount"`
			AccountNumber string `json:"account_number"`
			RentObjectID  string `json:"rent_object_id"`
		} `json:"entries"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if in.Status != postingDraft && in.Status != postingPosted {
		return sdk.Response{}, crud.Invalid("Status %q: DRAFT oder POSTED", in.Status)
	}
	if in.CompanyCode == "" || in.DraftID == "" || len(in.Entries) == 0 {
		return sdk.Response{}, crud.Invalid("company_code, draft_id und entries sind Pflicht")
	}
	if err := requireWrite(ctx, runObject, "execute", in.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	stamp := now()
	for _, e := range in.Entries {
		kind := e.Kind
		if kind == "" {
			kind = kindOriginal
		}
		if kind != kindOriginal && kind != kindCorrection {
			return sdk.Response{}, crud.Invalid("Art %q: ORIGINAL oder CORRECTION", kind)
		}
		// Laufende Nummer je Schlüssel: 0 = erster Vermerk, weitere = Nachberechnungen.
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__posting (company_code, contract_id, condition_type, object_id, period_from, sequence,
			kind, billing_period, period_to, due_date, amount, currency, account_number, rent_object_id, status, draft_id, document_id,
			document_number, run_id, recorded_at)
			VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sequence) + 1, 0) FROM contract__posting WHERE company_code = ? AND contract_id = ?
			AND condition_type = ? AND object_id = ? AND period_from = ?), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.CompanyCode, e.ContractID, e.ConditionType, e.ObjectID, e.PeriodFrom,
			in.CompanyCode, e.ContractID, e.ConditionType, e.ObjectID, e.PeriodFrom,
			kind, nilIfEmpty(e.BillingPeriod), e.PeriodTo, e.DueDate, e.Amount, in.Currency,
			e.AccountNumber, nilIfEmpty(e.RentObjectID), in.Status, in.DraftID, nilIfEmpty(in.DocumentID), nilIfEmpty(in.DocumentNumber),
			nilIfEmpty(in.RunID), stamp); err != nil {
			return sdk.Response{}, err
		}
	}
	return sdk.Response{Payload: map[string]any{"recorded": len(in.Entries)}}, nil
}

// onDraftEvent: Vorerfassung im Hauptbuch gebucht (Belegnummer übernehmen)
// oder verworfen (Fälligkeiten wieder offen).
func (m *Module) onDraftEvent(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	ev, err := events.Decode(req.Payload)
	if err != nil || ev.Object != "JournalDraft" || ev.EntityID == "" {
		return sdk.Response{}, err
	}
	switch ev.Action {
	case "post":
		_, err = m.db.Exec(ctx, `UPDATE contract__posting SET status = ?, document_id = ?, document_number = ? WHERE draft_id = ?`,
			postingPosted, nilIfEmpty(crud.Str(ev.Data["posted_document_id"])), nilIfEmpty(crud.Str(ev.Data["document_number"])), ev.EntityID)
		if err == nil {
			_, err = m.db.Exec(ctx, `UPDATE contract__settlement SET status = ?, document_id = ?, document_number = ? WHERE draft_id = ? AND status = ?`,
				settlementPosted, nilIfEmpty(crud.Str(ev.Data["posted_document_id"])), nilIfEmpty(crud.Str(ev.Data["document_number"])), ev.EntityID, settlementDraft)
		}
	case "deactivate":
		_, err = m.db.Exec(ctx, `DELETE FROM contract__posting WHERE draft_id = ? AND status = ?`, ev.EntityID, postingDraft)
		if err == nil { // verworfene Vorerfassung: Abrechnung wieder erfasst
			_, err = m.db.Exec(ctx, `UPDATE contract__settlement SET status = ?, draft_id = NULL WHERE draft_id = ? AND status = ?`,
				settlementOpen, ev.EntityID, settlementDraft)
		}
	}
	return sdk.Response{}, err
}

// reconcileDrafts gleicht offene Vorerfassungen des Buchungskreises mit dem
// Hauptbuch ab – falls ein Event verloren ging. Dazu gebuchte Sollstellungen,
// deren Beleg noch die GUID aus ledger < 0.13.0 trägt (neu: Buchungskreis|Jahr|Nummer).
func (m *Module) reconcileDrafts(ctx context.Context, cc string) error {
	res, err := m.db.Query(ctx, `SELECT DISTINCT draft_id FROM contract__posting WHERE company_code = ?
		AND (status = ? OR (status = ? AND draft_id IS NOT NULL AND document_id IS NOT NULL AND document_id NOT LIKE '%|%'))`,
		cc, postingDraft, postingPosted)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		id := crud.Str(r[0])
		resp, err := m.services.Call(ctx, "JournalDraft", "get", map[string]any{"id": id})
		var d struct {
			Status          string `json:"status"`
			PostedDocument  string `json:"posted_document_id"`
			DocumentNumbers string `json:"document_number"`
		}
		switch {
		case errors.Is(err, sdk.ErrNotFound):
			d.Status = "DISCARDED"
		case err != nil:
			return unavailable("Hauptbuch", err)
		default:
			if err := sdk.Decode(resp.Payload, &d); err != nil {
				return err
			}
		}
		ev := events.Event{Object: "JournalDraft", EntityID: id, Data: map[string]any{"posted_document_id": d.PostedDocument}}
		switch d.Status {
		case "POSTED":
			ev.Action = "post"
			if d.PostedDocument != "" {
				if e, err := m.services.Call(ctx, "JournalEntry", "get", map[string]any{"id": d.PostedDocument}); err == nil {
					if rec, ok := e.Payload.(map[string]any); ok {
						ev.Data["document_number"] = rec["document_number"]
					}
				}
			}
		case "DISCARDED":
			ev.Action = "deactivate"
		default:
			continue
		}
		if _, err := m.onDraftEvent(ctx, sdk.Request{Payload: map[string]any{"event": ev}}); err != nil {
			return err
		}
	}
	return nil
}
