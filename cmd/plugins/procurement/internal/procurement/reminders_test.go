package procurement

import (
	"fmt"
	"slices"
	"testing"
)

func TestReminderDates(t *testing.T) {
	for _, c := range []struct {
		lead, repeat, count int64
		today               string
		want                []string
	}{
		{3, 7, 0, "2026-11-20", []string{"2026-11-05", "2026-11-12", "2026-11-19"}}, // bis bezahlt
		{3, 7, 2, "2026-11-30", []string{"2026-11-05", "2026-11-12"}},
		{-7, 0, 0, "2026-11-30", []string{"2026-11-15"}}, // eine Woche nach Fälligkeit
		{3, 7, 0, "2026-11-04", nil},
	} {
		if got := reminderDates("2026-11-08", c.lead, c.repeat, c.count, c.today); !slices.Equal(got, c.want) {
			t.Errorf("%d/%d/%d: %v, erwartet %v", c.lead, c.repeat, c.count, got, c.want)
		}
	}
}

// TestInvoiceReminders: Regel an der Rechnungsart; offen laut Hauptbuch →
// Erinnerung mit offenem Betrag; ausgeglichen → entfallen (bezahlt).
func TestInvoiceReminders(t *testing.T) {
	e := setup(t)
	e.setupCompany()
	e.must("InvoiceType", "update", map[string]any{"id": "1000|ER", "data": map[string]any{"auto_post": true, "remind_text": "Rechnung bezahlen",
		"remind_lead_days": 3, "remind_repeat_days": 7, "remind_max_count": 3, "remind_from": "2026-01-01"}})
	id := e.create(invoiceObject, map[string]any{"company_code": "1000", "invoice_type": "ER", "supplier_id": "HW1", "supplier_reference": "766/2026",
		"invoice_date": "2026-10-30", "due_date": "2026-11-08", "object_type": "Building", "object_id": "LpzBrn1"})["invoice_id"].(string)
	e.create(itemObject, map[string]any{"company_code": "1000", "invoice_id": id, "cost_category": "INSTAND", "amount": "666,40"})
	e.must(invoiceObject, "post", map[string]any{"id": "1000|" + id})

	rows := func(today string) string {
		t.Helper()
		if err := e.m.refreshRemindersTx(e.ctx, today); err != nil {
			t.Fatal(err)
		}
		res, err := e.m.db.Query(e.ctx, `SELECT seq, remind_date, status, detail FROM procurement__invoice_reminder WHERE invoice_id = ? ORDER BY seq`, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range res.Rows {
			r[1] = dateOf(r[1])
		}
		return fmt.Sprint(res.Rows)
	}
	e.h.openItems = []any{map[string]any{"fiscal_year": 2026, "document_number": "1000000099", "open_minor": -66640}}
	if got := rows("2026-11-04"); got != "[]" {
		t.Fatalf("zu früh: %s", got)
	}
	if got := rows("2026-11-12"); got != "[[1 2026-11-05 REPEATED fällig 08.11.2026, offen 666,40 EUR, Rechnung 766/2026] [2 2026-11-12 OPEN fällig 08.11.2026, offen 666,40 EUR, Rechnung 766/2026]]" {
		t.Fatalf("offen: %s", got)
	}
	e.h.openItems = []any{map[string]any{"fiscal_year": 2026, "document_number": "1000000099", "open_minor": -16640}}
	if got := rows("2026-11-13"); got != "[[1 2026-11-05 REPEATED fällig 08.11.2026, offen 666,40 EUR, Rechnung 766/2026] [2 2026-11-12 OPEN fällig 08.11.2026, offen 166,40 EUR von 666,40 EUR, Rechnung 766/2026]]" {
		t.Fatalf("Teilzahlung: %s", got)
	}
	e.h.openItems = nil
	if got := rows("2026-11-20"); got != "[[1 2026-11-05 OBSOLETE fällig 08.11.2026, offen 666,40 EUR, Rechnung 766/2026] [2 2026-11-12 OBSOLETE fällig 08.11.2026, offen 166,40 EUR von 666,40 EUR, Rechnung 766/2026]]" {
		t.Fatalf("bezahlt: %s", got)
	}
	if l := e.must(reminderObject, "list", map[string]any{}); len(l["items"].([]any)) != 2 {
		t.Fatalf("Liste: %v", l)
	}
}
