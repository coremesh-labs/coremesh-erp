package ledger

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh-erp/pkg/ledgerapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

// TestStarterAccounts: Buchungskreis mit Grundausstattung einrichten, ein
// weiteres Konto bei Bedarf aktivieren (Hinweis in der Fehlermeldung), nicht
// benötigte Konten entfernen – gebuchte, Grundausstattung und in Fachmodulen
// verwendete bleiben.
func TestStarterAccounts(t *testing.T) {
	e := setup(t)
	yes := true
	file := map[string]any{"accounts": []any{
		map[string]any{"account_number": "1", "name": "Umlaufvermögen", "account_type": "BALANCE_SHEET", "is_group": true, "is_starter": yes},
		map[string]any{"account_number": "1200", "name": "Mietforderungen", "account_type": "BALANCE_SHEET", "parent_account": "1",
			"reconciliation_type": "CUSTOMER", "is_starter": yes},
		map[string]any{"account_number": "1300", "name": "Forderungen gegen Geschäftsführer", "account_type": "BALANCE_SHEET", "parent_account": "1"},
		map[string]any{"account_number": "1800", "name": "Bank", "account_type": "BALANCE_SHEET", "parent_account": "1", "is_starter": yes},
		map[string]any{"account_number": "3300", "name": "Verbindlichkeiten", "account_type": "BALANCE_SHEET", "reconciliation_type": "SUPPLIER"},
		map[string]any{"account_number": "4861", "name": "Mieterlöse", "account_type": "REVENUE", "is_starter": yes},
		map[string]any{"account_number": "6000", "name": "Reparaturen", "account_type": "PRIMARY_COST"},
		map[string]any{"account_number": "6300", "name": "Versicherungen", "account_type": "PRIMARY_COST"},
	}}
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "KLEIN", "file": file})
	if g := e.must("GLAccount", "get", map[string]any{"id": "KLEIN|1"}); g["is_starter"] != false {
		t.Fatalf("Gruppe nie Grundausstattung: %v", g["is_starter"])
	}
	r := e.must(loaderObject, "setupCompany", map[string]any{"company": "2000", "chart": "KLEIN", "currency": "EUR", "year": 2026})
	accounts := func() []string {
		var out []string
		for _, a := range items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "2000"}})) {
			out = append(out, fmt.Sprint(a["account_number"]))
		}
		slices.Sort(out)
		return out
	}
	if got := accounts(); fmt.Sprint(got) != "[1200 1800 4861]" || toInt(r["accounts_assigned"]) != 3 {
		t.Fatalf("Grundausstattung: %v %v", got, r)
	}
	if len(e.h.events) == 0 || e.h.events[len(e.h.events)-1].Object != "GLAccountCompany" {
		t.Fatalf("Event für contract-billing fehlt: %v", e.h.events)
	}

	// Buchen auf ein nicht aktiviertes Konto: Hinweis auf die Aktion
	_, err := e.gl.Post(e.ctx, ledgerapi.PostRequest{SourceModule: "MANUAL", SourceReference: "GF-1", CompanyCode: "2000", PostingDate: "2026-03-01",
		Currency: "EUR", DocumentType: "SA", Items: []ledgerapi.Item{
			{Account: "1300", Side: ledgerapi.Debit, Amount: "500.00"}, {Account: "1800", Side: ledgerapi.Credit, Amount: "500.00"}}})
	if err == nil || !strings.Contains(err.Error(), "Im Buchungskreis aktivieren") {
		t.Fatalf("Hinweis erwartet: %v", err)
	}
	r = e.must("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|1300", "data": map[string]any{"activate_company": "2000"}})
	if !strings.Contains(fmt.Sprint(r["message"]), "Forderungen gegen Geschäftsführer") {
		t.Fatalf("aktiviert: %v", r)
	}
	_, err = e.call("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|1300", "data": map[string]any{"activate_company": "2000"}})
	expect(t, err, sdk.ErrInvalidArgument, "schon aktiv")
	_, err = e.call("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|1", "data": map[string]any{"activate_company": "2000"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gruppe")
	e.must("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|3300", "data": map[string]any{"activate_company": "2000"}})
	sup := items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "2000", "account_number": "3300"}}))
	if len(sup) != 1 || sup[0]["reconciliation_type"] != "SUPPLIER" {
		t.Fatalf("Abstimmkonto aus der Kontoart: %v", sup)
	}
	for _, a := range []string{"6000", "6300"} {
		e.must("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|" + a, "data": map[string]any{"activate_company": "2000"}})
	}
	// 1300 wird gebucht, 6300 verwendet ein Fachmodul (Vertrag), 3300 und 6000 nicht
	if _, err := e.gl.Post(e.ctx, ledgerapi.PostRequest{SourceModule: "MANUAL", SourceReference: "GF-2", CompanyCode: "2000", PostingDate: "2026-03-01",
		Currency: "EUR", DocumentType: "SA", Items: []ledgerapi.Item{
			{Account: "1300", Side: ledgerapi.Debit, Amount: "500.00"}, {Account: "1800", Side: ledgerapi.Credit, Amount: "500.00"}}}); err != nil {
		t.Fatal(err)
	}
	e.h.hook = func(req hook.Request) hook.Result {
		var in ledgerapi.ChartChange
		_ = sdk.Decode(req.Data, &in)
		if req.Hook != ledgerapi.HookChartChange || req.Action != hook.PhaseCheck || in.Mapping["1800"] != "1800" {
			t.Errorf("Hook: %v %v", req.Hook, in)
		}
		if _, ok := in.Mapping["6300"]; !ok {
			return hook.Result{Messages: []hook.Message{hook.Error("CHART-001", "Verträge: Konten ohne Zuordnung in Buchungskreis 2000: 6300")}}
		}
		return hook.Result{}
	}
	r = e.must("LedgerCompanyConfig", "removeUnusedAccounts", map[string]any{"id": "2000", "data": map[string]any{"cleanup_check_only": true}})
	if toInt(r["removable"]) != 2 || fmt.Sprint(r["accounts"]) != "[3300 6000]" || len(accounts()) != 7 {
		t.Fatalf("Prüfung: %v", r)
	}
	r = e.must("LedgerCompanyConfig", "removeUnusedAccounts", map[string]any{"id": "2000", "data": map[string]any{}})
	if toInt(r["removed"]) != 2 || fmt.Sprint(accounts()) != "[1200 1300 1800 4861 6300]" ||
		!strings.Contains(fmt.Sprint(r["message"]), "5 bleiben (Grundausstattung 3, gebucht 1, in Fachmodulen verwendet 1)") {
		t.Fatalf("entfernt: %v %v", r, accounts())
	}
	if a := e.must("GLAccount", "get", map[string]any{"id": "KLEIN|6000"}); a["is_active"] != true {
		t.Fatalf("im Kontenplan bleibt es: %v", a)
	}
	// Meldung ohne Kontenliste: lieber nichts entfernen
	e.must("GLAccount", "activateInCompany", map[string]any{"id": "KLEIN|6000", "data": map[string]any{"activate_company": "2000"}})
	e.h.hook = func(hook.Request) hook.Result {
		return hook.Result{Messages: []hook.Message{hook.Error("X-1", "unbekannter Fehler")}}
	}
	_, err = e.call("LedgerCompanyConfig", "removeUnusedAccounts", map[string]any{"id": "2000", "data": map[string]any{}})
	expect(t, err, sdk.ErrInvalidArgument, "Meldung ohne Liste")
	if len(accounts()) != 6 {
		t.Fatalf("nichts entfernt: %v", accounts())
	}

	// ohne Grundausstattung im Kontenplan (SKR25) und mit accounts=ALL: alle Konten
	e.rentCompany()
	if n := len(items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000"}}))); n < 20 {
		t.Fatalf("SKR25 ohne Grundausstattung: alle Konten, %d", n)
	}
}

// TestHouseholdChart: mitgelieferter Kontenrahmen HAUSHALT (Haushaltsbuch der
// Privat-Edition) lässt sich laden und einem Buchungskreis zuordnen.
func TestHouseholdChart(t *testing.T) {
	e := setup(t)
	e.must(loaderObject, "loadCoa", map[string]any{"chart": "HAUSHALT"})
	e.must(loaderObject, "setupCompany", map[string]any{"company": "1000", "chart": "HAUSHALT", "currency": "EUR", "year": 2026})
	if n := len(items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000"}}))); n != 66 {
		t.Fatalf("Konten im Buchungskreis: %d", n)
	}
	if a := e.must("GLAccount", "get", map[string]any{"id": "HAUSHALT|5000"}); a["name"] != "Lebensmittel" || a["account_type"] != "PRIMARY_COST" {
		t.Fatalf("Lebensmittel: %v", a)
	}
	// 0.32.3: Ausgaben ohne Kostenstelle (Feldstatusgruppe Standard)
	for _, c := range items(e.must("GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": "1000", "account_number": "5000"}})) {
		if c["field_status_group"] != "STD" {
			t.Fatalf("Feldstatusgruppe Lebensmittel: %v", c["field_status_group"])
		}
	}
}
