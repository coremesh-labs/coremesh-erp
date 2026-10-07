package ledger

import (
	"strings"
	"testing"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
	"github.com/camel/coremesh_erp/pkg/ledgerapi"
)

func periods(ranges ...sdk.ValueRange) []sdk.GrantRule {
	return []sdk.GrantRule{{CompanyCodes: []string{"1000"}, Fields: map[string][]sdk.ValueRange{"posting_period": ranges}}}
}

// TestPostingAuthorization: Beim Buchen zählen Periode (FiscalPeriod.post) und
// Belegart (DocumentType.post) – auf jedem Weg: Post, Simulate, Reverse.
func TestPostingAuthorization(t *testing.T) {
	e := setup(t)
	e.rentCompany()

	// Normale Perioden 1–11, Dezember nicht.
	e.h.rules = map[string][]sdk.GrantRule{"FiscalPeriod.post": periods(sdk.ValueRange{Low: "1", High: "11"})}
	oct, err := e.gl.Post(e.ctx, rentInvoice("A-10"))
	if err != nil {
		t.Fatalf("Oktober: %v", err)
	}
	dec := rentInvoice("A-12")
	dec.PostingDate = "2026-12-01"
	_, err = e.gl.Post(e.ctx, dec)
	expect(t, err, sdk.ErrPermissionDenied, "Periode 12")
	if err != nil && !strings.Contains(err.Error(), "Periode 12/2026") {
		t.Errorf("Meldung: %v", err)
	}
	err = e.gl.Simulate(e.ctx, dec)
	expect(t, err, sdk.ErrPermissionDenied, "Simulation Periode 12")

	// Anderer Buchungskreis in der Regel → kein Recht in 1000.
	e.h.rules["FiscalPeriod.post"] = []sdk.GrantRule{{CompanyCodes: []string{"2000"}}}
	_, err = e.gl.Post(e.ctx, rentInvoice("A-10b"))
	expect(t, err, sdk.ErrPermissionDenied, "nur 2000")

	// Belegart: nur Kreditorenrechnungen (KR) → DR (Debitorenrechnung) nicht.
	e.h.rules = map[string][]sdk.GrantRule{"DocumentType.post": {{CompanyCodes: []string{"*"}, Fields: map[string][]sdk.ValueRange{"code": {{Low: "KR"}}}}}}
	_, err = e.gl.Post(e.ctx, rentInvoice("A-11"))
	expect(t, err, sdk.ErrPermissionDenied, "Belegart DR")
	// Muster: D* erlaubt DR.
	e.h.rules["DocumentType.post"][0].Fields["code"] = append(e.h.rules["DocumentType.post"][0].Fields["code"], sdk.ValueRange{Low: "D*"})
	if _, err := e.gl.Post(e.ctx, rentInvoice("A-11")); err != nil {
		t.Fatalf("D*: %v", err)
	}

	// Storno bucht in die Periode des Stornodatums.
	e.h.rules = map[string][]sdk.GrantRule{"FiscalPeriod.post": periods(sdk.ValueRange{Low: "1", High: "9"})}
	_, err = e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: oct.ID, PostingDate: "2026-10-15"})
	expect(t, err, sdk.ErrPermissionDenied, "Storno in Periode 10")
}

// TestDraftSpecialPeriod: Die Vorerfassung bietet Sonderperioden nur im
// Dezember und nur mit Berechtigung an; gebucht wird in die gewählte Periode.
func TestDraftSpecialPeriod(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	formState := func(values map[string]string) metamodel.FormState {
		t.Helper()
		resp, err := e.p.Handle(e.ctx, sdk.Request{Object: "JournalDraft", Action: "formState",
			Payload: metamodel.FormStateRequest{Mode: "create", Values: values}})
		if err != nil {
			t.Fatal(err)
		}
		var st metamodel.FormState
		if err := sdk.Decode(resp.Payload, &st); err != nil {
			t.Fatal(err)
		}
		return st
	}
	visible := func(st metamodel.FormState) []string {
		fs := st.Fields["special_period"]
		if fs.Visible == nil || !*fs.Visible {
			return nil
		}
		var out []string
		for _, o := range fs.Options {
			out = append(out, o.Value)
		}
		return out
	}
	values := map[string]string{"company_code_id": "1000", "document_type": "SA", "posting_date": "2026-12-31"}

	e.h.rules = map[string][]sdk.GrantRule{"FiscalPeriod.post": periods(sdk.ValueRange{Low: "1", High: "12"})}
	if got := visible(formState(values)); got != nil {
		t.Fatalf("ohne Recht auf 13–16: %v", got)
	}
	e.h.rules["FiscalPeriod.post"] = periods(sdk.ValueRange{Low: "1", High: "12"}, sdk.ValueRange{Low: "13"}, sdk.ValueRange{Low: "15"})
	if got := strings.Join(visible(formState(values)), ","); got != "13,15" {
		t.Fatalf("Sonderperioden: %q", got)
	}
	values["posting_date"] = "2026-11-30"
	if got := visible(formState(values)); got != nil {
		t.Fatalf("November: %v", got)
	}
	// Hinweis bei nicht erlaubter Belegart.
	e.h.rules["DocumentType.post"] = []sdk.GrantRule{{CompanyCodes: []string{"*"}, Fields: map[string][]sdk.ValueRange{"code": {{Low: "KR"}}}}}
	if st := formState(values); !strings.Contains(st.Message, "Keine Berechtigung, Belegart SA") {
		t.Fatalf("Hinweis: %q", st.Message)
	}
	delete(e.h.rules, "DocumentType.post")

	// Periode 13 mit Kontenausnahme öffnen (wie im Abschluss) und buchen.
	e.must("PeriodAccountLock", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "fiscal_year": 2026,
		"period_from": 13, "account_from": "2800", "account_to": "2999", "status": "OPEN", "reason": "Abschluss"}})
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "document_type": "SA",
		"posting_date": "2026-12-31", "special_period": "13", "currency": "EUR", "header_text": "Abgrenzung"}})
	for _, it := range []map[string]any{
		{"account_number": "2800", "shkzg": "S", "amount": "100"},
		{"account_number": "2850", "shkzg": "H", "amount": "100"},
	} {
		it["draft_id"] = d["id"]
		e.must("JournalDraftItem", "create", map[string]any{"data": it})
	}
	res := e.must("JournalDraft", "post", map[string]any{"id": d["id"]})
	if toInt(res["posting_period"]) != 13 {
		t.Fatalf("gebucht in %v", res)
	}
	// Ohne Recht auf 13 scheitert dieselbe Buchung.
	e.h.rules["FiscalPeriod.post"] = periods(sdk.ValueRange{Low: "1", High: "12"})
	d2 := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "document_type": "SA",
		"posting_date": "2026-12-31", "special_period": "13", "currency": "EUR", "header_text": "Abgrenzung 2"}})
	for _, it := range []map[string]any{
		{"account_number": "2800", "shkzg": "S", "amount": "50"},
		{"account_number": "2850", "shkzg": "H", "amount": "50"},
	} {
		it["draft_id"] = d2["id"]
		e.must("JournalDraftItem", "create", map[string]any{"data": it})
	}
	_, err := e.call("JournalDraft", "post", map[string]any{"id": d2["id"]})
	expect(t, err, sdk.ErrPermissionDenied, "Periode 13 ohne Recht")
}
