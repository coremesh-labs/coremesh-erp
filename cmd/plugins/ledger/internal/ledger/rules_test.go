package ledger

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
)

// formState ruft die FormState-Action eines Objects auf.
func (e *env) formState(object string, values map[string]string) metamodel.FormState {
	e.t.Helper()
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: object, Action: "formState",
		Payload: metamodel.FormStateRequest{Mode: "create", Values: values}})
	if err != nil {
		e.t.Fatalf("formState %s: %v", object, err)
	}
	return resp.Payload.(metamodel.FormState)
}

func visible(st metamodel.FormState, f string) bool {
	fs, ok := st.Fields[f]
	return !ok || fs.Visible == nil || *fs.Visible
}

func required(st metamodel.FormState, f string) bool {
	fs := st.Fields[f]
	return fs.Required != nil && *fs.Required
}

func (e *env) draft(docType string) string {
	e.t.Helper()
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-10-05",
		"currency": "EUR", "header_text": "Test", "document_type": docType, "reference": "R-1"}})
	return d["id"].(string)
}

// TestFieldStatusAndItemTypes: Feldstatusgruppe des Kontos und Belegart steuern
// Maske, Speichern der Vorerfassung und Buchen gleichermaßen.
func TestFieldStatusAndItemTypes(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	acc := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": "6000"}}))[0]
	if acc["field_status_group"] != "RENT_REVENUE" {
		t.Fatalf("Feldstatusgruppe aus der Kontenrahmen-Datei: %v", acc["field_status_group"])
	}
	if a := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"account_number": "2800"}}))[0]; a["field_status_group"] != "BALANCE" {
		t.Fatalf("abgeleitete Feldstatusgruppe: %v", a["field_status_group"])
	}

	// Maske der Position: ohne Konto keine Kontierung; Mieterlöse: Objekt und Vertrag Pflicht.
	sa := e.draft("SA")
	st := e.formState("JournalDraftItem", map[string]string{"draft_id": sa})
	if visible(st, "rent_object_id") || !strings.Contains(st.Message, "Konto wählen") {
		t.Fatalf("ohne Konto: %+v", st)
	}
	if opts := st.Fields["item_type"].Options; len(opts) != 2 || opts[0].Value != itemGL {
		t.Fatalf("Positionsarten SA: %+v", opts)
	}
	st = e.formState("JournalDraftItem", map[string]string{"draft_id": sa, "account_number": "6000"})
	if !required(st, "rent_object_id") || !required(st, "rent_contract_id") || visible(st, "cost_center") || visible(st, "sd_sales_org") {
		t.Fatalf("Mieterlöse: %+v", st.Fields)
	}
	if !strings.Contains(st.Message, "RENT_REVENUE") || *st.Fields["item_type"].Value != itemGL {
		t.Fatalf("Hinweis: %s", st.Message)
	}
	// Bank: keine Kontierung.
	st = e.formState("JournalDraftItem", map[string]string{"draft_id": sa, "account_number": "1700"})
	for _, f := range dimColumns {
		if visible(st, f) {
			t.Fatalf("Bank: %s sichtbar", f)
		}
	}
	// Debitorenkonto in SA: nicht erlaubt; in DR: Positionsart Debitor.
	st = e.formState("JournalDraftItem", map[string]string{"draft_id": sa, "account_number": "1200"})
	if !strings.Contains(st.Message, "erlaubt keine Positionen der Art CUSTOMER") {
		t.Fatalf("Debitor in SA: %s", st.Message)
	}
	dr := e.draft("DR")
	st = e.formState("JournalDraftItem", map[string]string{"draft_id": dr, "account_number": "1200"})
	if *st.Fields["item_type"].Value != itemCustomer || !strings.Contains(st.Message, "Kunde oder Mietvertrag") {
		t.Fatalf("Debitor in DR: %+v %s", st.Fields["item_type"], st.Message)
	}
	// Kopf: Referenz Pflicht bei DR.
	if hs := e.formState("JournalDraft", map[string]string{"document_type": "DR"}); !required(hs, "reference") {
		t.Fatalf("Referenz DR: %+v", hs)
	}
	if hs := e.formState("JournalDraft", map[string]string{"document_type": "SA"}); required(hs, "reference") {
		t.Fatalf("Referenz SA: %+v", hs)
	}

	// Speichern der Position prüft dieselben Regeln.
	add := func(draft string, data map[string]any) error {
		data["draft_id"] = draft
		_, err := e.call("JournalDraftItem", "create", map[string]any{"data": data})
		return err
	}
	expect(t, add(sa, map[string]any{"account_number": "6000", "shkzg": "H", "amount": "1", "rent_object_id": "WE-1"}), sdk.ErrInvalidArgument, "Vertrag fehlt")
	expect(t, add(sa, map[string]any{"account_number": "1700", "shkzg": "S", "amount": "1", "cost_center": "K1"}), sdk.ErrInvalidArgument, "ausgeblendet")
	expect(t, add(sa, map[string]any{"account_number": "1200", "shkzg": "S", "amount": "1", "rent_contract_id": "MV"}), sdk.ErrInvalidArgument, "Debitor in SA")
	if err := add(dr, map[string]any{"account_number": "1200", "shkzg": "S", "amount": "1", "rent_contract_id": "MV"}); err != nil {
		t.Fatalf("Debitor in DR: %v", err)
	}
	pos := items(e.must("JournalDraftItem", "list", map[string]any{"query": map[string]any{"draft_id": dr}}))
	if pos[0]["item_type"] != itemCustomer {
		t.Fatalf("Positionsart gespeichert: %v", pos[0])
	}

	// Buchen: Positionsart im Universal Journal, Feldstatus auch für Module.
	res, err := e.gl.Post(e.ctx, rentInvoice("R-FS"))
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res.ID}})) {
		want := map[string]string{"1200": itemCustomer, "6000": itemGL, "2800": itemGL}[l["account_number"].(string)]
		if l["item_type"] != want {
			t.Fatalf("item_type %v: %v", l["account_number"], l["item_type"])
		}
	}
	bad := rentInvoice("")
	bad.Items[1].CostCenter = "K-1" // Kostenstelle bei Mieterlösen ausgeblendet
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "ausgeblendetes Feld beim Buchen")
	bad = rentInvoice("")
	bad.Reference = ""
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "Referenz bei DR")
	bad = rentInvoice("")
	bad.DocumentType = "KR"
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "Debitor in KR")
}

// TestLockUnlockAccounts: Sperren/Entsperren im Kontenplan und im Buchungskreis.
func TestLockUnlockAccounts(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	g := e.must("GLAccount", "get", map[string]any{"id": "SKR25|6000"})
	if h := g["_hidden_actions"].([]any); len(h) != 1 || h[0] != "unlock" {
		t.Fatalf("aktives Konto: %v", g["_hidden_actions"])
	}
	g = e.must("GLAccount", "lock", map[string]any{"id": "SKR25|6000"})
	if g["is_active"] != false || g["_hidden_actions"].([]any)[0] != "lock" || !strings.Contains(g["message"].(string), "gesperrt") {
		t.Fatalf("gesperrt: %v", g)
	}
	_, err := e.gl.Post(e.ctx, rentInvoice("L-1"))
	expect(t, err, sdk.ErrInvalidArgument, "Konto im Kontenplan gesperrt")
	e.must("GLAccount", "unlock", map[string]any{"id": "SKR25|6000"})
	if _, err := e.gl.Post(e.ctx, rentInvoice("L-2")); err != nil {
		t.Fatalf("nach Entsperren: %v", err)
	}
	acc := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"account_number": "2800"}}))[0]
	e.must("GLAccountCompany", "lock", map[string]any{"id": acc["id"]})
	// Ja/Nein-Filter als Text (Formular, URL, Lookup-Filter "=true").
	if n := len(items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"is_blocked": "true"}}))); n != 1 {
		t.Fatalf("Filter is_blocked=true: %d", n)
	}
	if n := len(items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"is_blocked": "false"}}))); n < 20 {
		t.Fatalf("Filter is_blocked=false: %d", n)
	}
	_, err = e.gl.Post(e.ctx, rentInvoice("L-3"))
	expect(t, err, sdk.ErrInvalidArgument, "Konto im Buchungskreis gesperrt")
	// Ohne Recht im Buchungskreis: nicht sperren.
	e.h.granted = map[string][]string{"GLAccountCompany.update": {"2000"}}
	_, err = e.call("GLAccountCompany", "unlock", map[string]any{"id": acc["id"]})
	expect(t, err, sdk.ErrPermissionDenied, "Entsperren ohne Recht")
}

// TestPeriodAccountLocks: Kontensperren je Periode als Ausnahme zum Periodenstatus.
func TestPeriodAccountLocks(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	// Oktober offen, aber Mietforderungen 1200 gesperrt.
	r := e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "to": 10, "status": "CLOSED",
		"accounts": "1200-1299", "reason": "Mahnlauf"})
	if !strings.Contains(r["message"].(string), "gesperrt") {
		t.Fatalf("Kontensperre: %v", r)
	}
	_, err := e.gl.Post(e.ctx, rentInvoice("P-1"))
	if err == nil || !strings.Contains(err.Error(), "Konto 1200 ist in Periode 10/2026 gesperrt") || !strings.Contains(err.Error(), "Mahnlauf") {
		t.Fatalf("gesperrt erwartet: %v", err)
	}
	// Andere Konten buchbar; im November gilt die Sperre nicht.
	nov := rentInvoice("P-2")
	nov.PostingDate = "2026-11-01"
	if _, err := e.gl.Post(e.ctx, nov); err != nil {
		t.Fatalf("November: %v", err)
	}
	// Periode 13 gesperrt, aber Konten 2800–2999 als Ausnahme buchbar.
	e.must("PeriodAccountLock", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "fiscal_year": 2026,
		"period_from": 13, "account_from": "2800", "account_to": "2999", "status": "OPEN", "reason": "Abschluss"}})
	closing := ledgerapi.PostRequest{SourceModule: "MANUAL", CompanyCode: "1000", PostingDate: "2026-12-31", PostingPeriod: 13, Currency: "EUR",
		HeaderText: "Abgrenzung BK", Items: []ledgerapi.Item{
			{Account: "2800", Side: ledgerapi.Debit, Amount: "100"},
			{Account: "2850", Side: ledgerapi.Credit, Amount: "100"},
		}}
	if res, err := e.gl.Post(e.ctx, closing); err != nil || res.PostingPeriod != 13 {
		t.Fatalf("Ausnahme in gesperrter Periode: %+v %v", res, err)
	}
	closing.Items[1].Account = "1700"
	_, err = e.gl.Post(e.ctx, closing)
	expect(t, err, sdk.ErrInvalidArgument, "Konto außerhalb der Ausnahme")
	// Widerspruch: CLOSED geht vor.
	lock := e.must("PeriodAccountLock", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "fiscal_year": 2026,
		"period_from": 13, "account_from": "2850", "status": "CLOSED"}})
	closing.Items[1].Account = "2850"
	_, err = e.gl.Post(e.ctx, closing)
	expect(t, err, sdk.ErrInvalidArgument, "CLOSED vor OPEN")
	// Aufheben (deactivate) beendet die Sperre.
	e.must("PeriodAccountLock", "deactivate", map[string]any{"id": lock["id"]})
	if _, err := e.gl.Post(e.ctx, closing); err != nil {
		t.Fatalf("nach Aufheben: %v", err)
	}
	for name, d := range map[string]map[string]any{
		"Kontenbereich": {"company_code_id": "1000", "fiscal_year": 2026, "period_from": 1, "account_from": "9", "account_to": "1", "status": "OPEN"},
		"Perioden":      {"company_code_id": "1000", "fiscal_year": 2026, "period_from": 5, "period_to": 2, "account_from": "1", "status": "OPEN"},
		"Status":        {"company_code_id": "1000", "fiscal_year": 2026, "period_from": 1, "account_from": "1", "status": "HALB"},
	} {
		_, err := e.call("PeriodAccountLock", "create", map[string]any{"data": d})
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
}

// TestImportOfficialExport: Spalten wie im DATEV-Export, Kontoart aus der
// Kontenklasse, führende Nullen ergänzt.
func TestImportOfficialExport(t *testing.T) {
	e := setup(t)
	rows := []any{
		map[string]any{"Konto": "135", "Beschriftung": "EDV-Software"},           // 0135, Klasse 0
		map[string]any{"Konto": "4400", "Beschriftung": "Erlöse 19 % USt"},       // Klasse 4 → REVENUE
		map[string]any{"Konto": "6805", "Beschriftung": "Telefon"},               // Klasse 6 → PRIMARY_COST
		map[string]any{"Kontonummer": "7300", "Bezeichnung": "Zinsaufwendungen"}, // Klasse 7 → NON_OPERATING
	}
	r := e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04", "file": rows})
	if r["inserted"].(int) != 4 {
		t.Fatalf("Import: %v", r)
	}
	for id, typ := range map[string]string{"SKR04|0135": "BALANCE_SHEET", "SKR04|4400": "REVENUE", "SKR04|6805": "PRIMARY_COST", "SKR04|7300": "NON_OPERATING"} {
		if a := e.must("GLAccount", "get", map[string]any{"id": id}); a["account_type"] != typ {
			t.Fatalf("%s: %v", id, a["account_type"])
		}
	}
	// Klasse ohne Zuordnung (SKR04 kennt 8 nicht): Kontoart nötig.
	_, err := e.call(loaderObject, "loadCoa", map[string]any{"chart": "SKR04", "file": []any{map[string]any{"Konto": "8000", "Beschriftung": "x"}}})
	expect(t, err, sdk.ErrInvalidArgument, "Klasse 8")
}

// TestBackdateFrom: „Rückwirkend buchen ab“ im Buchungskreis – davor abgelehnt, ab dem Tag gebucht.
func TestBackdateFrom(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	e.must("LedgerCompanyConfig", "update", map[string]any{"id": "1000", "data": map[string]any{"backdate_from": "2026-01-01"}})
	inv := rentInvoice("SOLL-ALT")
	inv.PostingDate = "2025-12-31"
	if _, err := e.gl.Post(e.ctx, inv); err == nil || !strings.Contains(err.Error(), "rückwirkendes Buchen") {
		t.Fatalf("vor der Grenze: %v", err)
	}
	inv = rentInvoice("SOLL-NEU")
	inv.PostingDate = "2026-01-01"
	if _, err := e.gl.Post(e.ctx, inv); err != nil {
		t.Fatalf("ab der Grenze: %v", err)
	}
}
