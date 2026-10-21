package dms

import "testing"

// TestMatchesFilters: Komma-Liste im Filter = einer der Werte (seit 0.17.1).
func TestMatchesFilters(t *testing.T) {
	q := map[string]any{"status": "OPEN,DRAFT,POSTED", "company_code": "2000"}
	if !matchesFilters(map[string]any{"status": "POSTED", "company_code": "2000"}, q) {
		t.Fatal("POSTED sollte passen")
	}
	if matchesFilters(map[string]any{"status": "CANCELLED", "company_code": "2000"}, q) {
		t.Fatal("CANCELLED sollte nicht passen")
	}
}
