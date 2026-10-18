package contract

import "testing"

func TestPlanBalance(t *testing.T) {
	rows := [][]string{
		{"30.01.2026", "Rate", "", "10.00", "100.00", "110.00", "900.00"},
		{"28.02.2026", "Rate", "", "9.00", "101.00", "110.00", "799.00"},
		{"30.03.2026", "Rate", "", "8.00", "102.00", "110.00", "697.00"},
	}
	if bal, due := planBalance(rows, "2026-03-01", 100000, 2); bal != 79900 || due != "28.02.2026" {
		t.Fatalf("Plan am 01.03.: %d %s", bal, due)
	}
	if bal, due := planBalance(rows, "2026-01-15", 100000, 2); bal != 100000 || due != "" {
		t.Fatalf("vor der ersten Rate: %d %s", bal, due)
	}
	if got := germanAmount("-124482.87"); got != "-124.482,87" {
		t.Fatalf("germanAmount: %s", got)
	}
}
