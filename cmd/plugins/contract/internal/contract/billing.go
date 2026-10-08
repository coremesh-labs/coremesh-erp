package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Vertragsbuchung (Sollstellung): Oberfläche im Modul Verträge. Daten und
// Logik liegen im Plugin contract-billing (Haskell, Object ContractBilling);
// hier wird nur weitergeleitet und je Buchungskreis berechtigt.
//
//   - Buchungsläufe (ContractPostingRun): „Neu“ bucht alle Fälligkeiten bis zum
//     Stichtag seit der letzten Buchung, „Vorschau …“ plant nur.
//   - Sollstellungen (ContractPosting): gebuchte Fälligkeiten mit Beleg; auch
//     als Abschnitt am Vertrag und am Buchungslauf.
//   - „Buchen …“ am Vertrag: dasselbe für einen Vertrag.

const (
	billingObject = "ContractBilling"
	runObject     = "ContractPostingRun"
	postingObject = "ContractPosting"
)

var runStatusOptions = []metamodel.Option{
	{Value: "RUNNING", Label: "läuft"}, {Value: "DONE", Label: "gebucht"}, {Value: "PARTIAL", Label: "teilweise gebucht (Meldungen)"},
	{Value: "FAILED", Label: "nicht gebucht (Meldungen)"}, {Value: "EMPTY", Label: "nichts fällig"}}

func (m *Module) runDefinition() metamodel.ObjectDefinition {
	return metamodel.ObjectDefinition{
		Name: runObject, Title: "Buchungsläufe", Icon: "icon-coins", TitleField: "message",
		Fields: []metamodel.FieldDefinition{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Editable: true, Lookup: lookupCC},
			{Key: "to_date", Label: "Buchen bis (Fälligkeit)", Type: tDate, Required: true, Listable: true, Editable: true},
			{Key: "contract_id", Label: "Nur Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, Options: runStatusOptions},
			{Key: "documents", Label: "Belege", Type: tNum, Listable: true},
			{Key: "items", Label: "Fälligkeiten", Type: tNum, Listable: true},
			{Key: "errors", Label: "Meldungen", Type: tNum, Listable: true},
			{Key: "message", Label: "Ergebnis", Type: tText},
			{Key: "messages_text", Label: "Meldungen je Vertrag", Type: tArea},
			{Key: "started_at", Label: "Gestartet am", Type: tText, Listable: true},
			{Key: "started_by", Label: "Gestartet von", Type: tText},
			{Key: "finished_at", Label: "Beendet am", Type: tText},
			{Key: "id", Label: "ID", Type: tText},
		},
		Actions: []metamodel.ActionConfig{
			{Name: "list", Kind: metamodel.KindList, Label: "Übersicht"},
			{Name: "get", Kind: metamodel.KindItem, Label: "Anzeigen"},
			{Name: "create", Kind: metamodel.KindCreate, Label: "Buchungslauf"},
			{Name: "preview", Kind: metamodel.KindCustom, Label: "Vorschau …", Fields: []string{"company_code", "to_date"}},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "lauf", Title: "Buchungslauf", Fields: []string{"company_code", "to_date", "contract_id", "status", "message", "documents", "items", "errors",
				"messages_text", "started_at", "started_by", "finished_at", "id"}},
			{Key: "sollstellungen", Title: "Sollstellungen", Relation: &metamodel.Relation{Object: postingObject, ForeignKey: "run_id",
				Columns: []string{"contract_id", "condition_type", "period_from", "period_to", "due_date", "amount", "document_number"}}},
		},
		Filters: []string{"company_code"},
	}
}

func (m *Module) postingDefinition() metamodel.ObjectDefinition {
	return metamodel.ObjectDefinition{
		Name: postingObject, Title: "Sollstellungen", Icon: "icon-list", TitleField: "document_number",
		Fields: []metamodel.FieldDefinition{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "condition_type", Label: "Konditionsart", Type: tText, Listable: true, Lookup: catalogLookup("ConditionType")},
			{Key: "object_id", Label: "Objekt der Kondition", Type: tText},
			{Key: "period_from", Label: "Von", Type: tDate, Listable: true},
			{Key: "period_to", Label: "Bis", Type: tDate, Listable: true},
			{Key: "due_date", Label: "Fällig am", Type: tDate, Listable: true},
			{Key: "amount", Label: "Betrag", Type: tText, Listable: true},
			{Key: "account_number", Label: "Sachkonto", Type: tText},
			{Key: "rent_object_id", Label: "Mietobjekt", Type: tText,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}}},
			{Key: "document_number", Label: "Beleg", Type: tText, Listable: true},
			{Key: "draft_id", Label: "Vorerfassung", Type: tText,
				Lookup: &metamodel.Lookup{Object: "JournalDraft", ValueField: "id", LabelFields: []string{"header_text"}}},
			{Key: "run_id", Label: "Buchungslauf", Type: tText,
				Lookup: &metamodel.Lookup{Object: runObject, ValueField: "id", LabelFields: []string{"message"}}},
			{Key: "posted_at", Label: "Gebucht am", Type: tText},
		},
		Actions: []metamodel.ActionConfig{
			{Name: "list", Kind: metamodel.KindList, Label: "Übersicht"},
			{Name: "get", Kind: metamodel.KindItem, Label: "Anzeigen"},
		},
		Filters: []string{"company_code", "contract_id", "condition_type"},
	}
}

func (m *Module) registerBilling(r *module.Router) {
	r.Object(runObject).Section("Buchung").Describe(m.runDefinition()).
		Handle("list", m.billingList("runs")).
		Handle("get", m.billingForward("getRun")).
		Handle("create", m.runCreate(false)).
		Handle("preview", m.runCreate(true))
	r.Object(postingObject).Section("Buchung").Describe(m.postingDefinition()).
		Handle("list", m.billingList("postings")).
		Handle("get", m.billingForward("getPosting"))
}

// billingCall leitet an das Plugin contract-billing weiter; fehlt es, eine klare Meldung.
func (m *Module) billingCall(ctx context.Context, action string, payload map[string]any) (sdk.Response, error) {
	resp, err := m.services.Call(ctx, billingObject, action, payload)
	if err != nil && strings.Contains(err.Error(), "kein Plugin für "+billingObject) {
		return resp, fmt.Errorf("%w: Vertragsbuchung nicht verfügbar – Plugin contract-billing ist nicht gestartet", sdk.ErrUnavailable)
	}
	return resp, err
}

// visible: Buchungskreise, in denen der Benutzer Verträge sehen darf (nil = alle).
func visible(ctx context.Context) ([]string, error) {
	if sdk.CallFromContext(ctx).UserID == "" {
		return nil, nil
	}
	g, err := sdk.GrantedCompanyCodes(ctx, "Contract", metamodel.ActionRead)
	if err != nil {
		return nil, err
	}
	if g.All {
		return nil, nil
	}
	if len(g.CompanyCodes) == 0 {
		return []string{"-"}, nil // nichts sichtbar
	}
	return g.CompanyCodes, nil
}

func billingQuery(payload any) map[string]any {
	p, _ := payload.(map[string]any)
	if q, ok := p["query"].(map[string]any); ok {
		return q
	}
	if p == nil {
		return map[string]any{}
	}
	return p
}

func (m *Module) billingList(action string) module.HandlerFunc {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		cc, err := visible(ctx)
		if err != nil {
			return sdk.Response{}, err
		}
		p := map[string]any{}
		for k, v := range billingQuery(req.Payload) {
			if s := crud.Str(v); s != "" {
				p[k] = s
			}
		}
		if cc != nil {
			p["company_codes"] = cc
		}
		return m.billingCall(ctx, action, p)
	}
}

func (m *Module) billingForward(action string) module.HandlerFunc {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		p, _ := req.Payload.(map[string]any)
		resp, err := m.billingCall(ctx, action, map[string]any{"id": p["id"]})
		if err != nil {
			return resp, err
		}
		rec, _ := resp.Payload.(map[string]any)
		if cc, err := visible(ctx); err != nil {
			return sdk.Response{}, err
		} else if cc != nil && !slices.Contains(cc, crud.Str(rec["company_code"])) {
			return sdk.Response{}, fmt.Errorf("%w: %v", sdk.ErrNotFound, p["id"])
		}
		return resp, nil
	}
}

// runCreate: Buchungslauf („Neu“) bzw. Vorschau für einen Buchungskreis.
func (m *Module) runCreate(simulate bool) module.HandlerFunc {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in struct {
			Data struct {
				CompanyCode string `json:"company_code"`
				ToDate      string `json:"to_date"`
			} `json:"data"`
			CompanyCode string `json:"company_code"`
			ToDate      string `json:"to_date"`
		}
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		cc, to := strings.TrimSpace(in.Data.CompanyCode+in.CompanyCode), strings.TrimSpace(in.Data.ToDate+in.ToDate)
		if cc == "" || to == "" {
			return sdk.Response{}, crud.Invalid("Buchungskreis und „Buchen bis“ sind Pflicht")
		}
		action := "create"
		if simulate {
			action = "preview"
		}
		if err := requireWrite(ctx, runObject, action, cc); err != nil {
			return sdk.Response{}, err
		}
		return m.billingCall(ctx, "run", map[string]any{"company_code": cc, "to_date": to, "simulate": simulate})
	}
}

// postAction: „Buchen …“ am Vertrag – alle Fälligkeiten des Vertrags bis zum Stichtag.
func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			PostUntil string `json:"post_until"`
		} `json:"data"`
	}
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
	return m.billingCall(ctx, "run", map[string]any{"company_code": cc, "contract_id": id, "to_date": in.Data.PostUntil})
}
