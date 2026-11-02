package banktransfer

import "testing"

func TestFill(t *testing.T) {
	v := map[string]string{"reference": "R1", "customer": "", "project": "P9"}
	for tpl, want := range map[string]string{
		"RE {reference}[ KD {customer}][ PRJ {project}]": "RE R1 PRJ P9",
		"[{customer}/{project}] {reference} {x}":         " R1 {x}",
		"offen [ohne Ende":                               "offen [ohne Ende",
	} {
		if got := Fill(tpl, v); got != want {
			t.Errorf("%q: %q, erwartet %q", tpl, got, want)
		}
	}
}

func TestGroupIBAN(t *testing.T) {
	if g := GroupIBAN("DE89370400440532013000"); g != "DE89 3704 0044 0532 0130 00" {
		t.Fatal(g)
	}
}
