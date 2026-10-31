package contract

import (
	"slices"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func TestReminderDates(t *testing.T) {
	for _, c := range []struct {
		lead, repeat, count int64
		want                []string
	}{
		{60, 0, 0, []string{"2027-01-30"}},
		{60, 14, 3, []string{"2027-01-30", "2027-02-13", "2027-02-27"}},
		{20, 10, 0, []string{"2027-03-11", "2027-03-21", "2027-03-31"}},
		{0, 0, 0, []string{"2027-03-31"}},
	} {
		if got := reminderDates("2027-03-31", c.lead, c.repeat, c.count); !slices.Equal(got, c.want) {
			t.Errorf("%d/%d/%d: %v, erwartet %v", c.lead, c.repeat, c.count, got, c.want)
		}
	}
}

// TestReminders: Regel je Vertragsart × Konditionsart, Wiederholung, Erledigt,
// Folgevertrag und geänderte Garantie.
func TestReminders(t *testing.T) {
	e := setup(t)
	e.basics()
	id := e.rentContract()
	e.must("Contract", "activate", map[string]any{"id": "1000|" + id})
	condID := "1000|" + id + "|KM||1|2026-01-01"
	e.must("ContractCondition", "update", map[string]any{"id": condID, "data": map[string]any{"guaranteed_until": "2027-03-31"}})

	err := e.try(reminderRuleObject, map[string]any{"company_code": "1000", "contract_type": "MV", "condition_kind": "FO", "lead_days": 60})
	expect(t, err, sdk.ErrInvalidArgument, "Beschreibung Pflicht")
	e.create(reminderRuleObject, map[string]any{"company_code": "1000", "contract_type": "MV", "condition_kind": "FO",
		"description": "Garantie läuft ab", "lead_days": 60, "repeat_days": 14, "max_count": 3})

	rows := func(today string) map[int64]string {
		t.Helper()
		if err := e.m.refreshRemindersTx(e.ctx, today); err != nil {
			t.Fatal(err)
		}
		res, err := e.m.db.Query(e.ctx, `SELECT seq, status, due_date FROM contract__reminder WHERE contract_id = ? ORDER BY due_date, seq`, id)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int64]string{}
		for _, r := range res.Rows {
			out[toInt(r[0])] = dateOf(r[2]) + " " + toStr(r[1])
		}
		return out
	}
	if r := rows("2027-01-29"); len(r) != 0 {
		t.Fatalf("zu früh: %v", r)
	}
	if r := rows("2027-02-14"); r[1] != "2027-03-31 REPEATED" || r[2] != "2027-03-31 OPEN" || len(r) != 2 {
		t.Fatalf("zwei Erinnerungen: %v", r)
	}
	e.must(reminderObject, "done", map[string]any{"id": "1000|" + id + "|KM||1|2027-03-31|2", "data": map[string]any{"note": "Angebot angefragt"}})
	if r := rows("2027-03-20"); r[1] != "2027-03-31 DONE" || r[2] != "2027-03-31 DONE" || len(r) != 2 {
		t.Fatalf("erledigt, keine dritte: %v", r)
	}
	e.must(reminderObject, "reopen", map[string]any{"id": "1000|" + id + "|KM||1|2027-03-31|2"})
	if r := rows("2027-03-20"); r[2] != "2027-03-31 REPEATED" || r[3] != "2027-03-31 OPEN" {
		t.Fatalf("wieder offen, dritte Erinnerung: %v", r)
	}

	// Folgevertrag: offene Erinnerungen entfallen, keine neuen.
	next := e.create("Contract", map[string]any{"company_code": "1000", "contract_type": "MV", "designation": "Neuer Vertrag",
		"partner_id": "BP1", "valid_from": "2027-04-01"})["contract_id"].(string)
	e.must("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"successor_contract_id": next}})
	if r := rows("2027-03-30"); r[3] != "2027-03-31 OBSOLETE" || r[1] != "2027-03-31 REPEATED" && r[1] != "2027-03-31 OBSOLETE" {
		t.Fatalf("Folgevertrag: %v", r)
	}
	_, err = e.call("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"successor_contract_id": id}})
	expect(t, err, sdk.ErrInvalidArgument, "Folgevertrag = dieser")

	// ohne Folgevertrag, neue Garantie: alte Erinnerungen bleiben entfallen, neue beginnen.
	e.must("Contract", "update", map[string]any{"id": "1000|" + id, "data": map[string]any{"successor_contract_id": ""}})
	e.must("ContractCondition", "update", map[string]any{"id": condID, "data": map[string]any{"guaranteed_until": "2028-03-31"}})
	res, _ := e.m.db.Query(e.ctx, `SELECT COUNT(*) FROM contract__reminder WHERE contract_id = ? AND due_date = '2027-03-31' AND status IN ('OPEN', 'REPEATED')`, id)
	if r := rows("2028-01-31"); r[1] != "2028-03-31 OPEN" || toInt(res.Rows[0][0]) != 0 {
		t.Fatalf("neue Garantie: %v", r)
	}
	// Liste ermittelt selbst (heute) und bleibt lesbar.
	if l := e.must(reminderObject, "list", map[string]any{"filters": map[string]any{"contract_id": id}}); len(items(l)) == 0 {
		t.Fatal("Liste leer")
	}
}

func toStr(v any) string { s, _ := v.(string); return s }
