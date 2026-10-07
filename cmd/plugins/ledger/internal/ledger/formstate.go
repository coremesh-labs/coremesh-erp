package ledger

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
)

// Masken der Vorerfassung (FormState): Der WebServer fragt bei jeder Änderung
// von Belegart, Positionsart oder Konto nach, welche Felder gelten. Grundlage
// sind dieselben Regeln wie beim Speichern und Buchen (rules.go).

func ptr[T any](v T) *T { return &v }

func trimInvalid(err error) string {
	return strings.TrimPrefix(err.Error(), sdk.ErrInvalidArgument.Error()+": ")
}

// draftFormState: Buchungskopf – Referenz Pflicht je Belegart.
func (m *Module) draftFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	code := strings.ToUpper(req.Values["document_type"])
	if code == "" {
		code = "SA"
		st.Fields["document_type"] = metamodel.FieldState{Value: ptr(code)}
	}
	dt, err := m.documentType(ctx, code)
	if err != nil {
		st.Message = trimInvalid(err)
		return st, nil
	}
	st.Fields["reference"] = metamodel.FieldState{Required: ptr(dt.ReferenceRequired)}
	var names []string
	for _, t := range dt.Allowed {
		names = append(names, itemTypeLabel(t))
	}
	st.Message = fmt.Sprintf("%s %s: Positionen %s", dt.Code, dt.Name, strings.Join(names, ", "))
	if dt.ReferenceRequired {
		st.Message += " · Referenz (z. B. Rechnungsnummer) Pflicht"
	}
	return st, nil
}

func itemTypeLabel(code string) string {
	for _, o := range itemTypes {
		if o.Value == code {
			return o.Label
		}
	}
	return code
}

// draftItemFormState: Position – Positionsart aus der Belegart, Kontierungsfelder
// aus der Feldstatusgruppe des Kontos, Hinweis auf gesperrte Konten/Perioden.
func (m *Module) draftItemFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	hideAll := func() {
		for _, f := range dimColumns {
			st.Fields[f] = metamodel.FieldState{Visible: ptr(false)}
		}
	}
	d, err := m.draftHeader(ctx, req.Values["draft_id"])
	if err != nil {
		hideAll()
		return st, nil
	}
	dt, err := m.documentType(ctx, d.DocumentType)
	if err != nil {
		hideAll()
		st.Message = trimInvalid(err)
		return st, nil
	}
	var opts []metamodel.Option
	for _, o := range itemTypes {
		if slices.Contains(dt.Allowed, o.Value) {
			opts = append(opts, o)
		}
	}
	itemState := metamodel.FieldState{Options: opts}
	if len(opts) == 1 {
		itemState.Value, itemState.ReadOnly = ptr(opts[0].Value), ptr(true)
	}
	st.Fields["item_type"] = itemState

	account := strings.ToUpper(strings.TrimSpace(req.Values["account_number"]))
	if account == "" {
		hideAll()
		st.Message = "Konto wählen – die Kontierungsfelder richten sich nach dessen Feldstatusgruppe."
		return st, nil
	}
	rule, err := m.rule(ctx, d.CompanyCode, d.DocumentType, account, "")
	if err != nil {
		hideAll()
		st.Message = trimInvalid(err)
		return st, nil
	}
	st.Fields["item_type"] = metamodel.FieldState{Options: opts, Value: ptr(rule.ItemType), ReadOnly: ptr(true)}
	for _, f := range statusFields {
		switch rule.Status[f] {
		case statusSuppress:
			st.Fields[f] = metamodel.FieldState{Visible: ptr(false)}
		case statusRequired:
			st.Fields[f] = metamodel.FieldState{Visible: ptr(true), Required: ptr(true)}
		default:
			st.Fields[f] = metamodel.FieldState{Visible: ptr(true), Required: ptr(false)}
		}
	}
	msg := []string{fmt.Sprintf("%s · Feldstatusgruppe %s – %s", itemTypeLabel(rule.ItemType), rule.Group, rule.GroupName)}
	switch rule.ItemType {
	case itemCustomer:
		msg = append(msg, "Kunde oder Mietvertrag angeben")
	case itemSupplier:
		msg = append(msg, "Lieferant angeben")
	}
	if cfg, err := m.config(ctx, d.CompanyCode); err == nil {
		if t, err := time.Parse(time.DateOnly, d.PostingDate); err == nil {
			if err := m.accountOpen(ctx, d.CompanyCode, cfg.Ledger, t.Year(), int(t.Month()), account); err != nil {
				msg = append(msg, "Achtung: "+trimInvalid(err))
			}
		}
	}
	st.Message = strings.Join(msg, " · ")
	return st, nil
}
