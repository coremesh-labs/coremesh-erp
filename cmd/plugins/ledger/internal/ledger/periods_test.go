package ledger

import (
	"testing"

	"github.com/camel/coremesh/pkg/sdk"
)

func openPeriods(e *env, query map[string]any) []map[string]any {
	return items(e.must("FiscalPeriod", "list", map[string]any{"query": query}))
}

// TestOpenPeriodList: Die Liste enthält nur offene Perioden; Schließen nimmt
// sie heraus (Verlauf bleibt), Öffnen legt eine neue Zeile an. Zum
// Jahreswechsel sind Perioden beider Jahre offen.
func TestOpenPeriodList(t *testing.T) {
	e := setup(t)
	e.rentCompany() // 1–12/2026 offen

	// Jahreswechsel: 1/2027 dazu, 2026 bleibt offen.
	e.must("FiscalPeriod", "openRange", map[string]any{"data": map[string]any{
		"company_code_id": "1000", "ledger": "0L", "fiscal_year": 2027, "posting_period": 1}})
	jan := rentInvoice("Y-1")
	jan.PostingDate = "2027-01-05"
	if _, err := e.gl.Post(e.ctx, jan); err != nil {
		t.Fatalf("Januar 2027: %v", err)
	}
	dec := rentInvoice("Y-2")
	dec.PostingDate = "2026-12-30"
	if _, err := e.gl.Post(e.ctx, dec); err != nil {
		t.Fatalf("Dezember 2026: %v", err)
	}

	// Perioden 1–9/2026 schließen: Sie verschwinden aus der Liste.
	r := e.must("FiscalPeriod", "closeRange", map[string]any{"data": map[string]any{
		"company_code_id": "1000", "ledger": "0L", "fiscal_year": 2026, "posting_period": 1, "period_to": 9}})
	if r["changed"].(int) != 9 {
		t.Fatalf("schließen: %v", r)
	}
	if n := len(openPeriods(e, map[string]any{"company_code_id": "1000"})); n != 4 { // 10–12/2026, 1/2027
		t.Fatalf("%d offene Perioden", n)
	}
	// Verlauf: geschlossen am/von.
	all := openPeriods(e, map[string]any{"company_code_id": "1000", "fiscal_year": "2026", "includeHistory": "true"})
	var closed map[string]any
	for _, p := range all {
		if toInt(p["posting_period"]) == 5 {
			closed = p
		}
	}
	if closed == nil || closed["is_open"] != false || closed["closed_at"] == nil || closed["closed_by"] != "tester" {
		t.Fatalf("Verlauf: %v", closed)
	}
	may := rentInvoice("Y-3")
	may.PostingDate = "2026-05-02"
	_, err := e.gl.Post(e.ctx, may)
	expect(t, err, sdk.ErrInvalidArgument, "Mai geschlossen")

	// Einzelne Periode schließen (deactivate) und wieder öffnen (neue Zeile).
	oct := openPeriods(e, map[string]any{"company_code_id": "1000", "fiscal_year": "2026", "posting_period": "10"})
	if len(oct) != 1 {
		t.Fatalf("Oktober: %v", oct)
	}
	e.must("FiscalPeriod", "deactivate", map[string]any{"id": oct[0]["id"]})
	if len(openPeriods(e, map[string]any{"company_code_id": "1000", "fiscal_year": "2026", "posting_period": "10"})) != 0 {
		t.Fatal("Oktober noch offen")
	}
	open := map[string]any{"data": map[string]any{"company_code_id": "1000", "ledger": "0L", "fiscal_year": 2026, "posting_period": 10}}
	e.must("FiscalPeriod", "create", open)
	_, err = e.call("FiscalPeriod", "create", open)
	expect(t, err, sdk.ErrInvalidArgument, "doppelt öffnen")
	_, err = e.call("FiscalPeriod", "create", map[string]any{"data": map[string]any{"company_code_id": "1000", "ledger": "0L", "fiscal_year": 2026, "posting_period": 17}})
	expect(t, err, sdk.ErrInvalidArgument, "Periode 17 nicht definiert")
}

// TestPeriodDefinition: Sonderperioden und Monate kommen aus der
// Periodendefinition – nicht aus festen Nummern.
func TestPeriodDefinition(t *testing.T) {
	e := setup(t)
	e.rentCompany()
	// Periode 14 wird Sonderperiode für Juni (Halbjahresabschluss).
	e.must("PostingPeriod", "update", map[string]any{"id": "1000|14", "data": map[string]any{"name": "Halbjahr", "calendar_month": 6}})
	e.must(loaderObject, "setPeriods", map[string]any{"company": "1000", "year": 2026, "from": 14, "status": "OPEN"})
	half := rentInvoice("D-1")
	half.PostingDate, half.PostingPeriod = "2026-06-30", 14
	res, err := e.gl.Post(e.ctx, half)
	if err != nil || res.PostingPeriod != 14 {
		t.Fatalf("Periode 14 im Juni: %+v %v", res, err)
	}
	// 13 gehört weiter zum Dezember.
	bad := rentInvoice("D-2")
	bad.PostingDate, bad.PostingPeriod = "2026-06-30", 13
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "13 im Juni")
	// Normale Periode ist keine Sonderperiode.
	bad.PostingPeriod = 6
	_, err = e.gl.Post(e.ctx, bad)
	expect(t, err, sdk.ErrInvalidArgument, "6 als Sonderperiode")
}

// TestPeriodMigration: offene Perioden aus 0.7.0 werden einmalig übernommen.
func TestPeriodMigration(t *testing.T) {
	e := setup(t)
	for _, p := range []int{11, 12} {
		if _, err := e.h.db.Exec(`INSERT INTO ledger__fiscal_period_status (company_code_id, ledger, fiscal_year, posting_period, status) VALUES ('1000', '0L', 2026, ?, 'OPEN')`, p); err != nil {
			t.Fatal(err)
		}
	}
	e.h.db.Exec(`INSERT INTO ledger__fiscal_period_status (company_code_id, ledger, fiscal_year, posting_period, status) VALUES ('1000', '0L', 2026, 10, 'CLOSED')`)
	if got := openPeriods(e, map[string]any{"company_code_id": "1000"}); len(got) != 2 {
		t.Fatalf("übernommen: %v", got)
	}
}
