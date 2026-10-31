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

// TestAssetDimMigration (0.33.1): das in 0.33.0 ausgeblendete Objekt wird an Erlösen,
// Bestands- und Kostenstellenkonten einmal Kann-Eingabe; spätere Einstellungen bleiben.
func TestAssetDimMigration(t *testing.T) {
	e := setup(t)
	db := e.h.db
	db.Exec(`UPDATE ledger__field_status SET status = 'SUPPRESS' WHERE field_name = 'asset_id' AND group_id IN ('REVENUE', 'BALANCE', 'COST')`)
	db.Exec(`UPDATE ledger__field_status_group SET description = NULL WHERE id = 'COST_OBJECT'`)
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM ledger__field_status WHERE field_name = 'asset_id' AND group_id IN ('REVENUE', 'BALANCE', 'COST') AND status = 'OPTIONAL'`).Scan(&n)
	if n != 3 {
		t.Fatalf("optional: %d", n)
	}
	db.Exec(`UPDATE ledger__field_status SET status = 'SUPPRESS' WHERE group_id = 'REVENUE' AND field_name = 'asset_id'`)
	if err := e.m.migrateData(e.ctx); err != nil {
		t.Fatal(err)
	}
	var st string
	db.QueryRow(`SELECT status FROM ledger__field_status WHERE group_id = 'REVENUE' AND field_name = 'asset_id'`).Scan(&st)
	if st != "SUPPRESS" {
		t.Fatalf("Einstellung überschrieben: %s", st)
	}
}
