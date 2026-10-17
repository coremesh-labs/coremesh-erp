package ledger

import "testing"

// TestBalanceDimensions: Bestandskonten erlauben Gebäude und Wirtschaftseinheit; die
// Umstellung älterer Daten läuft einmal (Beschreibung als Merker).
func TestBalanceDimensions(t *testing.T) {
	e := setup(t)
	db := e.h.db
	db.Exec(`UPDATE ledger__field_status SET status = 'SUPPRESS' WHERE group_id = 'BALANCE' AND field_name LIKE 'dimension_custom_%'`)
	db.Exec(`UPDATE ledger__field_status_group SET description = NULL WHERE id = 'BALANCE'`)
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ledger__field_status WHERE group_id = 'BALANCE' AND field_name LIKE 'dimension_custom_%' AND status = 'OPTIONAL'`).Scan(&n)
	if n != 2 {
		t.Fatalf("optional: %d", n)
	}
	// vom Verwalter wieder ausgeblendet: bleibt so
	db.Exec(`UPDATE ledger__field_status SET status = 'SUPPRESS' WHERE group_id = 'BALANCE' AND field_name = 'dimension_custom_2'`)
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	var st string
	db.QueryRow(`SELECT status FROM ledger__field_status WHERE group_id = 'BALANCE' AND field_name = 'dimension_custom_2'`).Scan(&st)
	if st != "SUPPRESS" {
		t.Fatalf("Einstellung überschrieben: %s", st)
	}
}
