package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
//     Buchungskreis alle Fälligkeiten bis zum Stichtag seit der letzten
//     Buchung, „Vorschau …“ plant nur.
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
)

var (
	runStatusOptions = []metamodel.Option{
		{Value: "RUNNING", Label: "läuft"}, {Value: "DONE", Label: "ausgeführt"}, {Value: "PARTIAL", Label: "teilweise ausgeführt (Meldungen)"},
		{Value: "FAILED", Label: "nicht ausgeführt (Meldungen)"}, {Value: "EMPTY", Label: "nichts fällig"}}
	postingStatusOptions = []metamodel.Option{{Value: postingDraft, Label: "vorerfasst"}, {Value: postingPosted, Label: "gebucht"}}
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
			{Key: "message", Label: "Ergebnis", Type: tText, ReadOnly: true},
			{Key: "messages", Label: "Meldungen je Vertrag", Type: tArea, ReadOnly: true},
			{Key: "started_at", Label: "Gestartet am", Type: tText, Listable: true, ReadOnly: true},
			{Key: "started_by", Label: "Gestartet von", Type: tText, ReadOnly: true},
			{Key: "finished_at", Label: "Beendet am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "lauf", Title: "Buchungslauf", Fields: []string{"company_code", "to_date", "contract_id", "status", "message", "documents", "drafts",
				"items", "errors", "messages", "started_at", "started_by", "finished_at", "id"}},
			{Key: "sollstellungen", Title: "Sollstellungen", Relation: &metamodel.Relation{Object: postingObject, ForeignKey: "run_id",
				Columns: []string{"contract_id", "condition_type", "period_from", "period_to", "due_date", "amount", "status", "document_number"}}},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "execute", Label: "Buchungslauf …", Fields: []string{"company_code", "to_date"}},
				Handle: m.executeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "preview", Label: "Vorschau …", Fields: []string{"company_code", "to_date"}},
				Handle: m.previewAction},
		},
	}
}

func (m *Module) posting() *crud.Entity {
	return &crud.Entity{
		Object: postingObject, Title: "Sollstellungen", Icon: "icon-list", Table: "contract__posting", Section: "Buchung",
		Keys:    []string{"company_code", "contract_id", "condition_type", "object_id", "period_from"},
		ReadOnly: true, Order: "company_code, contract_id, due_date, condition_type",
		Filters: []string{"company_code", "contract_id", "condition_type", "status", "run_id", "draft_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "condition_type", Label: "Konditionsart", Type: tText, Listable: true, Lookup: catalogLookup("ConditionType")},
			{Key: "object_id", Label: "Objekt der Kondition", Type: tText},
			{Key: "period_from", Label: "Von", Type: tDate, Listable: true},
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
		Status    string `json:"status"`
		Message   string `json:"message"`
		Documents int    `json:"documents"`
		Drafts    int    `json:"drafts"`
		Items     int    `json:"items"`
		Errors    int    `json:"errors"`
		Messages  []struct {
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
	if _, uerr := m.db.Exec(ctx, `UPDATE contract__posting_run SET status = ?, documents = ?, drafts = ?, items = ?, errors = ?, message = ?,
		messages = ?, finished_at = ? WHERE id = ?`, out.Status, out.Documents, out.Drafts, out.Items, out.Errors, out.Message,
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
		if _, err := m.db.Exec(ctx, `INSERT INTO contract__posting (company_code, contract_id, condition_type, object_id, period_from, period_to,
			due_date, amount, currency, account_number, rent_object_id, status, draft_id, document_id, document_number, run_id, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			in.CompanyCode, e.ContractID, e.ConditionType, e.ObjectID, e.PeriodFrom, e.PeriodTo, e.DueDate, e.Amount, in.Currency,
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
	case "deactivate":
		_, err = m.db.Exec(ctx, `DELETE FROM contract__posting WHERE draft_id = ? AND status = ?`, ev.EntityID, postingDraft)
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
