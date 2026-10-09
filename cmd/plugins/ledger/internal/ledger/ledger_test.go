package ledger

import (
	"os"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
)

func TestAmounts(t *testing.T) {
	for in, want := range map[string]int64{"1500": 150000, "1500.5": 150050, "1'500.50": 150050, "0.05": 5, "12,3": 1230} {
		if got, err := parseAmount(in, 2); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "-5", "1.234", "abc", "1e3"} {
		if _, err := parseAmount(in, 2); err == nil {
			t.Errorf("%q angenommen", in)
		}
	}
	if _, err := parseAmount("10.5", 0); err == nil {
		t.Error("JPY mit Nachkommastellen angenommen")
	}
	if formatAmount(-150005, 2) != "-1500.05" || formatAmount(7, 2) != "0.07" || formatAmount(1500, 0) != "1500" {
		t.Fatal("formatAmount")
	}
}

func TestLoadChartOfAccountsIdempotent(t *testing.T) {
	e := setup(t)
	r := e.must(loaderObject, "loadCoa", map[string]any{"chart": "skr04"})
	if r["inserted"].(int) < 10 || r["updated"].(int) != 0 {
		t.Fatalf("erster Lauf: %v", r)
	}
	n := r["inserted"].(int)
	r = e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04"})
	if r["inserted"].(int) != 0 || r["unchanged"].(int) != n {
		t.Fatalf("zweiter Lauf nicht idempotent: %v", r)
	}
	// Eigene Datei (wie von der CLI aus CSV gelesen): eine Änderung, ein neues Konto.
	rows := []any{
		map[string]any{"account_number": "1800", "name": "Bank (Hausbank)", "account_type": "BALANCE_SHEET"},
		map[string]any{"account_number": "4401", "name": "Erlöse 7 % USt", "account_type": "revenue"},
	}
	r = e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04", "file": rows})
	if r["inserted"].(int) != 1 || r["updated"].(int) != 1 {
		t.Fatalf("Datei: %v", r)
	}
	if a := e.must("GLAccount", "get", map[string]any{"id": "SKR04|1800"}); a["name"] != "Bank (Hausbank)" {
		t.Fatalf("Upsert: %v", a)
	}
	for name, p := range map[string]map[string]any{
		"Kontoart":        {"chart": "SKR04", "file": []any{map[string]any{"account_number": "9", "name": "x", "account_type": "ASSET"}}},
		"doppelt":         {"chart": "SKR04", "file": []any{map[string]any{"account_number": "9", "name": "x", "account_type": "REVENUE"}, map[string]any{"account_number": "9", "name": "y", "account_type": "REVENUE"}}},
		"falscher Plan":   {"chart": "SKR04", "file": map[string]any{"chart_of_accounts_id": "SKR25", "accounts": []any{}}},
		"unbekannt":       {"chart": "XYZ"},
		"ohne Kontenplan": {},
	} {
		_, err := e.call(loaderObject, "loadCoa", p)
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
}

func TestSetupCompany(t *testing.T) {
	e := setup(t)
	_, err := e.call(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "SKR25", "currency": "EUR"})
	expect(t, err, sdk.ErrInvalidArgument, "Kontenplan ohne Konten")
	e.rentCompany()
	acc := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": "1200"}}))
	if len(acc) != 1 || acc[0]["reconciliation_type"] != "CUSTOMER" || acc[0]["currency"] != "EUR" || acc[0]["account_name"] != "Mietforderungen" {
		t.Fatalf("SKB1 mit Vorschlag Abstimmkonto: %v", acc)
	}
	periods := items(e.must("FiscalPeriod", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "fiscal_year": "2026"}}))
	if len(periods) != 12 || periods[0]["is_open"] != true {
		t.Fatalf("Perioden: %d %v", len(periods), periods)
	}
	r := e.must(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "SKR25", "currency": "EUR"})
	if r["accounts_assigned"].(int) != 0 || r["created"].(bool) {
		t.Fatalf("idempotent: %v", r)
	}
	_, err = e.call(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "SKR25", "currency": "CHF"})
	expect(t, err, sdk.ErrInvalidArgument, "andere Währung")
	_, err = e.call(loaderObject, "setupCompany", map[string]any{"company": "9999", "chart": "SKR25", "currency": "EUR"})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Buchungskreis")
}

func TestModulePosting(t *testing.T) {
	e := setup(t)
	e.rentCompany()

	res, err := e.gl.Post(e.ctx, rentInvoice("SOLL-2026-10-MV-0007"))
	if err != nil {
		t.Fatal(err)
	}
	if res.DocumentNumber != "1000000001" || res.FiscalYear != 2026 || res.PostingPeriod != 10 || res.Duplicate {
		t.Fatalf("Beleg: %+v", res)
	}
	// Mapping RENT: contract → rent_contract_id, object → rent_object_id; Soll +, Haben −.
	lines := items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res.ID}}))
	if len(lines) != 3 {
		t.Fatalf("Positionen: %v", lines)
	}
	byAcc := map[string]map[string]any{}
	for _, l := range lines {
		byAcc[l["account_number"].(string)] = l
	}
	if l := byAcc["1200"]; l["rent_contract_id"] != "MV-0007" || l["rent_object_id"] != "WE-0001-0003" || l["shkzg"] != "S" ||
		l["amount_document_curr"] != int64(125000) || l["debit"] != "1250.00" || l["ledger"] != "0L" {
		t.Fatalf("1200: %v", l)
	}
	if l := byAcc["2800"]; l["amount_local_curr"] != int64(-25000) || l["credit"] != "250.00" || l["item_text"] != "BK-Vorauszahlung" {
		t.Fatalf("2800: %v", l)
	}
	head := e.must("JournalEntry", "get", map[string]any{"id": res.ID})
	if head["source_module"] != "RENT" || head["total"] != "1250.00 EUR" || head["exchange_rate"] != "1" || head["document_type"] != "DR" {
		t.Fatalf("Kopf: %v", head)
	}

	// Idempotenz: gleiche Referenz → vorhandener Beleg.
	dup, err := e.gl.Post(e.ctx, rentInvoice("SOLL-2026-10-MV-0007"))
	if err != nil || !dup.Duplicate || dup.ID != res.ID {
		t.Fatalf("Duplikat: %+v %v", dup, err)
	}

	bad := func(name string, change func(*ledgerapi.PostRequest)) {
		t.Helper()
		r := rentInvoice("")
		change(&r)
		_, err := e.gl.Post(e.ctx, r)
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
	bad("Soll ≠ Haben", func(r *ledgerapi.PostRequest) { r.Items[0].Amount = "1249.99" })
	bad("Periode gesperrt", func(r *ledgerapi.PostRequest) { r.PostingDate = "2027-01-05" })
	bad("Sonderperiode außerhalb Dezember", func(r *ledgerapi.PostRequest) { r.PostingPeriod = 13 })
	bad("unbekannte Kontierung", func(r *ledgerapi.PostRequest) { r.Items[1].Assignments = map[string]string{"room": "3"} })
	bad("Abstimmkonto ohne Partner", func(r *ledgerapi.PostRequest) { r.Items[0].Assignments = map[string]string{"object": "WE-1"} })
	bad("Konto nicht im Kontenplan", func(r *ledgerapi.PostRequest) { r.Items[1].Account = "4711" })
	bad("Modul ohne Mapping", func(r *ledgerapi.PostRequest) { r.SourceModule = "PAYROLL" })
	bad("Währung unbekannt", func(r *ledgerapi.PostRequest) { r.Currency = "XAU" })
	bad("Fremdwährung ohne Kurs", func(r *ledgerapi.PostRequest) { r.Currency = "CHF" })
	bad("Buchungskreis nicht eingerichtet", func(r *ledgerapi.PostRequest) { r.CompanyCode = "2000" })

	// Gesperrtes Konto im Buchungskreis.
	acc := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": "6000"}}))[0]
	e.must("GLAccountCompany", "lock", map[string]any{"id": acc["id"]})
	bad("Konto gesperrt", func(*ledgerapi.PostRequest) {})

	// Eigenes Mapping: RENT schreibt "unit" statt "object" nach rent_object_id.
	e.must("LedgerCompanyConfig", "update", map[string]any{"id": "1000", "data": map[string]any{
		"module_field_mapping": `{"RENT": {"unit": "rent_object_id", "contract": "rent_contract_id"}}`}})
	_, err = e.call("LedgerCompanyConfig", "update", map[string]any{"id": "1000", "data": map[string]any{
		"module_field_mapping": `{"RENT": {"unit": "rent_room"}}`}})
	expect(t, err, sdk.ErrInvalidArgument, "Mapping auf unbekannte Spalte")
	e.must("GLAccountCompany", "unlock", map[string]any{"id": acc["id"]})
	r := rentInvoice("")
	for i := range r.Items {
		r.Items[i].Assignments = map[string]string{"contract": "MV-0008", "unit": "WE-0002-0001"}
	}
	r.Items[1].Account = "6010"
	if _, err := e.gl.Post(e.ctx, r); err != nil {
		t.Fatalf("eigenes Mapping: %v", err)
	}
	bad("altes Mapping-Feld", func(*ledgerapi.PostRequest) {})
}

func TestForeignCurrencyAndRounding(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	r := e.must(loaderObject, "loadRates", map[string]any{"file": []any{
		map[string]any{"rate_type": "M", "from_currency": "CHF", "to_currency": "EUR", "valid_from": "2026-09-30", "rate": "1,0650"},
		map[string]any{"from_currency": "JPY", "to_currency": "EUR", "valid_from": "2026-09-30", "rate": "0.6250", "from_factor": "100"},
	}})
	if r["inserted"].(int) != 2 {
		t.Fatalf("Kurse: %v", r)
	}
	if r := e.must(loaderObject, "loadRates", map[string]any{"file": map[string]any{"rates": []any{
		map[string]any{"rate_type": "M", "from_currency": "CHF", "to_currency": "EUR", "valid_from": "2026-09-30", "rate": "1.065"}}}}); r["unchanged"].(int) != 1 {
		t.Fatalf("Kurse idempotent: %v", r)
	}
	// 100 CHF in drei Teilen: Umrechnung einzeln gerundet, Differenz auf die größte Position.
	req := ledgerapi.PostRequest{SourceModule: "PROCUREMENT", CompanyCode: "1000", PostingDate: "2026-10-02", Currency: "CHF",
		DocumentType: "KR", Reference: "RE-77-2026-118", HeaderText: "Rechnung Hauswart (CHF)", Items: []ledgerapi.Item{
			{Account: "7100", Side: ledgerapi.Debit, Amount: "100.00", CostCenter: "HAUS-1", Assignments: map[string]string{"object": "WE-0001"}},
			{Account: "2900", Side: ledgerapi.Credit, Amount: "33.33", Assignments: map[string]string{"supplier": "K-77"}},
			{Account: "2900", Side: ledgerapi.Credit, Amount: "33.33", Assignments: map[string]string{"supplier": "K-77"}},
			{Account: "2900", Side: ledgerapi.Credit, Amount: "33.34", Assignments: map[string]string{"supplier": "K-77"}},
		}}
	res, err := e.gl.Post(e.ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	lines := items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res.ID}}))
	var sum int64
	for _, l := range lines {
		sum += l["amount_local_curr"].(int64)
		if l["account_number"] == "7100" && (l["amount_local_curr"] != int64(10651) || l["cost_center"] != "HAUS-1") {
			t.Fatalf("Rundungsdifferenz: %v", l)
		}
		if l["account_number"] == "2900" && l["supplier_id"] != "K-77" {
			t.Fatalf("Lieferant: %v", l)
		}
	}
	if sum != 0 {
		t.Fatalf("Hauswährung nicht ausgeglichen: %d", sum)
	}
	if h := e.must("JournalEntry", "get", map[string]any{"id": res.ID}); h["exchange_rate"] != "1.065" || h["local_currency"] != "EUR" {
		t.Fatalf("Kurs im Kopf: %v", h)
	}
	// Umrechnung: direkt, Kehrwert, Faktor 100 bei JPY.
	for _, c := range []struct{ amount, from, to, want string }{
		{"100", "CHF", "EUR", "106.50"}, {"106.50", "EUR", "CHF", "100.00"}, {"10000", "JPY", "EUR", "62.50"}, {"62.50", "EUR", "JPY", "10000"},
	} {
		got := e.must(conversionObject, "convert", map[string]any{"amount": c.amount, "from": c.from, "to": c.to, "date": "2026-10-01"})
		if got["converted"] != c.want {
			t.Errorf("%s %s → %s: %v, erwartet %s", c.amount, c.from, c.to, got["converted"], c.want)
		}
	}
	_, err = e.call(conversionObject, "convert", map[string]any{"amount": "1", "from": "CHF", "to": "EUR", "date": "2026-09-01"})
	expect(t, err, sdk.ErrInvalidArgument, "kein Kurs vor valid_from")
}

func TestDraftSaveAndPost(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	// Speichern: Kopf und Positionen, änderbar.
	d := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-10-05",
		"currency": "eur", "header_text": "Einlage Eigenkapital"}})
	id := d["id"].(string)
	if d["status"] != draftOpen || d["currency"] != "EUR" {
		t.Fatalf("Vorerfassung: %v", d)
	}
	i1 := e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": id, "account_number": "1700", "shkzg": "S", "amount": "5000"}})
	e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": id, "account_number": "2000", "shkzg": "H", "amount": "4900",
		"rent_object_id": "WE-0001"}})
	if i1["line_item_number"] != int64(1) || i1["amount"] != "5000.00" || i1["account_name"] != "Bankguthaben" {
		t.Fatalf("Position: %v", i1)
	}
	if g := e.must("JournalDraft", "get", map[string]any{"id": id}); g["balance"] != "100.00 EUR" {
		t.Fatalf("Saldo: %v", g["balance"])
	}
	// Buchen scheitert, solange Soll ≠ Haben – die Vorerfassung bleibt offen.
	_, err := e.call("JournalDraft", "post", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "unausgeglichen")
	e.must("JournalDraftItem", "update", map[string]any{"id": i1["id"], "data": map[string]any{"amount": "4900"}})
	if s := e.must("JournalDraft", "simulate", map[string]any{"id": id}); !strings.HasPrefix(s["message"].(string), "Prüfung erfolgreich") {
		t.Fatalf("Prüfen: %v", s)
	}

	// Buchen: Beleg mit Verweis auf die Vorerfassung, Vorerfassung gesperrt.
	res := e.must("JournalDraft", "post", map[string]any{"id": id})
	g := e.must("JournalDraft", "get", map[string]any{"id": id})
	if g["status"] != draftPosted || g["posted_document_id"] != res["id"] {
		t.Fatalf("nach dem Buchen: %v", g)
	}
	head := e.must("JournalEntry", "get", map[string]any{"id": res["id"]})
	if head["draft_id"] != id || head["source_module"] != moduleManual || head["source_reference"] != "DRAFT-"+id {
		t.Fatalf("Beleg: %v", head)
	}
	lines := items(e.must("JournalEntryItem", "list", map[string]any{"query": map[string]any{"header_id": res["id"]}}))
	if len(lines) != 2 || (lines[0]["rent_object_id"] != "WE-0001" && lines[1]["rent_object_id"] != "WE-0001") {
		t.Fatalf("direkte Kontierung (MANUAL): %v", lines)
	}
	for name, f := range map[string]func() error{
		"Kopf ändern": func() error {
			_, err := e.call("JournalDraft", "update", map[string]any{"id": id, "data": map[string]any{"header_text": "x"}})
			return err
		},
		"Position anlegen": func() error {
			_, err := e.call("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": id, "account_number": "1700", "shkzg": "S", "amount": "1"}})
			return err
		},
		"Position ändern": func() error {
			_, err := e.call("JournalDraftItem", "update", map[string]any{"id": i1["id"], "data": map[string]any{"amount": "1"}})
			return err
		},
		"Position entfernen": func() error {
			_, err := e.call("JournalDraftItem", "remove", map[string]any{"id": i1["id"]})
			return err
		},
		"erneut buchen": func() error { _, err := e.call("JournalDraft", "post", map[string]any{"id": id}); return err },
		"verwerfen":     func() error { _, err := e.call("JournalDraft", "deactivate", map[string]any{"id": id}); return err },
	} {
		expect(t, f(), sdk.ErrInvalidArgument, name)
	}

	// Offene Vorerfassung: Position entfernen, verwerfen.
	d2 := e.must("JournalDraft", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "posting_date": "2026-10-06",
		"currency": "EUR", "header_text": "Test"}})
	it := e.must("JournalDraftItem", "create", map[string]any{"data": map[string]any{"draft_id": d2["id"], "account_number": "1700", "shkzg": "S", "amount": "1"}})
	e.must("JournalDraftItem", "remove", map[string]any{"id": it["id"]})
	if n := len(items(e.must("JournalDraftItem", "list", map[string]any{"query": map[string]any{"draft_id": d2["id"]}}))); n != 0 {
		t.Fatalf("entfernt: %d", n)
	}
	e.must("JournalDraft", "deactivate", map[string]any{"id": d2["id"]})
	if g := e.must("JournalDraft", "get", map[string]any{"id": d2["id"]}); g["status"] != draftDiscarded {
		t.Fatalf("verworfen: %v", g)
	}
}

func TestReverse(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	res, err := e.gl.Post(e.ctx, rentInvoice("SOLL-1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: res.ID, PostingDate: "2026-09-30"})
	expect(t, err, sdk.ErrInvalidArgument, "Storno vor dem Beleg")
	rev, err := e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: res.ID, PostingDate: "2026-10-15"})
	if err != nil {
		t.Fatal(err)
	}
	orig := e.must("JournalEntry", "get", map[string]any{"id": res.ID})
	st := e.must("JournalEntry", "get", map[string]any{"id": rev.ID})
	if orig["reversal_document_id"] != rev.ID || st["reversed_document_id"] != res.ID || st["reversal_flag"] != true ||
		st["header_text"] != "Storno zu 1000000001" || st["source_module"] != "RENT" {
		t.Fatalf("Verweise: orig=%v storno=%v", orig, st)
	}
	_, err = e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: res.ID})
	expect(t, err, sdk.ErrInvalidArgument, "doppelt")
	_, err = e.gl.Reverse(e.ctx, ledgerapi.ReverseRequest{ID: rev.ID})
	expect(t, err, sdk.ErrInvalidArgument, "Storno vom Storno")
	// Salden je Mietobjekt nach Storno: alles 0.
	bal := e.must(balanceObject, "list", map[string]any{"company_code": "1000", "rent_object_id": "WE-0001-0003"})
	if rows := bal["items"].([]balanceRow); len(rows) != 3 {
		t.Fatalf("Salden: %+v", rows)
	} else {
		for _, r := range rows {
			if r.BalanceMin != 0 {
				t.Fatalf("Saldo nach Storno: %+v", r)
			}
		}
	}
}

func TestBalancesAndRights(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	if _, err := e.gl.Post(e.ctx, rentInvoice("A")); err != nil {
		t.Fatal(err)
	}
	r := rentInvoice("B")
	for i := range r.Items {
		r.Items[i].Assignments = map[string]string{"contract": "MV-0009", "object": "WE-0002-0001"}
	}
	if _, err := e.gl.Post(e.ctx, r); err != nil {
		t.Fatal(err)
	}
	all := e.must(balanceObject, "list", map[string]any{"company_code": "1000", "fiscal_year": 2026})["items"].([]balanceRow)
	obj := e.must(balanceObject, "list", map[string]any{"company_code": "1000", "rent_object_id": "WE-0002-0001"})["items"].([]balanceRow)
	find := func(rows []balanceRow, acc string) balanceRow {
		for _, x := range rows {
			if x.Account == acc {
				return x
			}
		}
		return balanceRow{}
	}
	if find(all, "6000").Balance != "-2000.00" || find(obj, "6000").Balance != "-1000.00" || find(all, "1200").Name != "Mietforderungen" {
		t.Fatalf("Salden: alle=%+v objekt=%+v", all, obj)
	}
	// Rechte: Buchen nur in 2000 erlaubt, Belege nur aus 2000 sichtbar.
	e.h.granted = map[string][]string{"JournalEntry.post": {"2000"}, "JournalEntry.read": {"2000"}}
	_, err := e.gl.Post(e.ctx, rentInvoice("C"))
	expect(t, err, sdk.ErrPermissionDenied, "Buchen in 1000")
	if n := len(items(e.must("JournalEntry", "list", nil))); n != 0 {
		t.Fatalf("%d Belege sichtbar", n)
	}
	_, err = e.call(balanceObject, "list", map[string]any{"company_code": "1000"})
	expect(t, err, sdk.ErrPermissionDenied, "Salden 1000")
}

func TestPeriodsCommand(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	r := e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "to": 16, "status": "CLOSED"})
	if r["changed"].(int) != 3 { // offen waren nur 10–12; 13–16 stehen nicht in der Liste
		t.Fatalf("Perioden: %v", r)
	}
	_, err := e.gl.Post(e.ctx, rentInvoice(""))
	expect(t, err, sdk.ErrInvalidArgument, "Oktober gesperrt")
	if r := e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 10, "to": 10, "status": "OPEN"}); r["changed"].(int) != 1 {
		t.Fatalf("öffnen: %v", r)
	}
	if _, err := e.gl.Post(e.ctx, rentInvoice("")); err != nil {
		t.Fatal(err)
	}
	// Sonderperiode 13 (Dezember, Abschlussbuchung) erst nach dem Öffnen.
	dec := rentInvoice("")
	dec.PostingDate, dec.PostingPeriod = "2026-12-31", 13
	_, err = e.gl.Post(e.ctx, dec)
	expect(t, err, sdk.ErrInvalidArgument, "Periode 13 gesperrt")
	e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 13, "status": "OPEN"})
	if res, err := e.gl.Post(e.ctx, dec); err != nil || res.PostingPeriod != 13 {
		t.Fatalf("Sonderperiode: %+v %v", res, err)
	}
	for name, p := range map[string]map[string]any{
		"Status":  {"company": "1000", "year": 2026, "from": 1, "to": 2, "status": "HALB"},
		"Bereich": {"company": "1000", "year": 2026, "from": 5, "to": 2, "status": "OPEN"},
		"Jahr":    {"company": "1000", "year": 26, "from": 1, "status": "OPEN"},
	} {
		_, err := e.call(loaderObject, "setPeriods", p)
		expect(t, err, sdk.ErrInvalidArgument, name)
	}
}

// TestSchemaDDLInSync: docs/ledger_schema_postgres.sql beschreibt jede Tabelle und
// Spalte des maßgeblichen Atlas-Schemas.
func TestSchemaDDLInSync(t *testing.T) {
	b, err := os.ReadFile("../../../../../docs/ledger_schema_postgres.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(b)
	for _, tbl := range strings.Split(schemaHCL, "\ntable \"")[1:] {
		name := tbl[:strings.Index(tbl, "\"")]
		start := strings.Index(ddl, "CREATE TABLE "+name+" (")
		if start < 0 {
			t.Errorf("DDL: Tabelle %s fehlt", name)
			continue
		}
		body := ddl[start : start+strings.Index(ddl[start:], ");")]
		for _, line := range strings.Split(tbl, "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "column \"") {
				continue
			}
			col := strings.Split(line, "\"")[1]
			if !strings.Contains(body, "\n    "+col+" ") {
				t.Errorf("DDL: %s.%s fehlt", name, col)
			}
		}
	}
}

// TestSetupCompanyOwnChart: eigener Kontenplan aus Datei – Abstimmkonto aus der
// Kontoart des Kontenplans, Feldstatus aus den Vorschlägen der Datei.
func TestSetupCompanyOwnChart(t *testing.T) {
	e := setup(t)
	file := map[string]any{"accounts": []any{
		map[string]any{"account_number": "2000", "name": "Mietenkontokorrent", "account_type": "BALANCE_SHEET", "reconciliation_type": "CUSTOMER"},
		map[string]any{"account_number": "2740", "name": "Bank", "account_type": "BALANCE_SHEET", "field_status_group": "BANK"},
		map[string]any{"account_number": "6000", "name": "Sollmieten", "account_type": "REVENUE"},
	}}
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "OWN", "file": file})
	e.must(loaderObject, "setupCompany", map[string]any{"company": "2000", "chart": "OWN", "currency": "EUR", "file": file})
	got := map[string]map[string]any{}
	for _, a := range items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "2000"}})) {
		got[a["account_number"].(string)] = a
	}
	if got["2000"]["reconciliation_type"] != "CUSTOMER" || got["2000"]["field_status_group"] != "CUSTOMER" {
		t.Fatalf("Abstimmkonto: %v", got["2000"])
	}
	if got["2740"]["field_status_group"] != "BANK" || got["6000"]["field_status_group"] != "REVENUE" {
		t.Fatalf("Feldstatus: %v / %v", got["2740"], got["6000"])
	}
	// ohne Datei: Abstimmkonto aus der Kontoart (D), Feldstatus aus der Kontoart
	e.must(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "OWN", "currency": "EUR"})
	for _, a := range items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000"}})) {
		got[a["account_number"].(string)] = a
	}
	if got["2000"]["reconciliation_type"] != "CUSTOMER" || got["2740"]["field_status_group"] != "BALANCE" {
		t.Fatalf("ohne Datei: %v / %v", got["2000"], got["2740"])
	}
}

// TestChartHierarchy: Kontengruppen im Kontenplan – übergeordnetes Konto muss
// eine Gruppe sein; Gruppen werden keinem Buchungskreis zugeordnet und sind
// nicht bebuchbar.
func TestChartHierarchy(t *testing.T) {
	e := setup(t)
	file := map[string]any{"accounts": []any{
		map[string]any{"account_number": "2", "name": "Umlaufvermögen", "account_type": "BALANCE_SHEET", "is_group": true},
		map[string]any{"account_number": "20", "name": "Mietforderungen", "account_type": "BALANCE_SHEET", "is_group": true, "parent_account": "2"},
		map[string]any{"account_number": "2000", "name": "Mietenkontokorrent", "account_type": "BALANCE_SHEET", "parent_account": "20", "reconciliation_type": "CUSTOMER"},
	}}
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "HIER", "file": file})
	if a := e.must("GLAccount", "get", map[string]any{"id": "HIER|2000"}); a["parent_number"] != "20" || a["is_group"] != false {
		t.Fatalf("Konto: %v", a)
	}
	e.must(loaderObject, "setupCompany", map[string]any{"company": "2000", "chart": "HIER", "currency": "EUR"})
	if l := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "2000"}})); len(l) != 1 {
		t.Fatalf("nur bebuchbare Konten zugeordnet: %v", l)
	}
	_, err := e.call("GLAccountCompany", "create", map[string]any{"data": map[string]any{"company_code_id": "2000", "account_number": "20"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gruppe dem Buchungskreis zuordnen")
	bad := map[string]any{"accounts": []any{map[string]any{"account_number": "2001", "name": "x", "account_type": "BALANCE_SHEET", "parent_account": "2000"}}}
	_, err = e.call(loaderObject, "loadCoa", map[string]any{"chart": "HIER", "file": bad})
	expect(t, err, sdk.ErrInvalidArgument, "übergeordnetes Konto keine Gruppe")
}

// TestChartGroupNoPadding: Mit den Regeln des mitgelieferten SKR04 (4 Stellen) werden
// Konten aufgefüllt, Kontengruppen der Datei (Klasse „1“) und Verweise darauf nicht.
func TestChartGroupNoPadding(t *testing.T) {
	e := setup(t)
	file := map[string]any{"accounts": []any{
		map[string]any{"account_number": "1", "name": "Umlaufvermögen", "account_type": "BALANCE_SHEET", "is_group": true},
		map[string]any{"account_number": "1200", "name": "Forderungen aus Lieferungen und Leistungen", "account_type": "BALANCE_SHEET", "parent_account": "1"},
		map[string]any{"account_number": "135", "name": "EDV-Software", "account_type": "BALANCE_SHEET"},
	}}
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "SKR04", "file": file})
	if a := e.must("GLAccount", "get", map[string]any{"id": "SKR04|1200"}); a["parent_number"] != "1" {
		t.Fatalf("Eltern: %v", a["parent_number"])
	}
	e.must("GLAccount", "get", map[string]any{"id": "SKR04|1"})
	e.must("GLAccount", "get", map[string]any{"id": "SKR04|0135"}) // Konto aufgefüllt
}
