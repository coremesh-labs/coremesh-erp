package accounting

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Beträge: Eingabe als Dezimaltext ("1500", "1500.5", "1'500.50", "1500,50"),
// gespeichert in der kleinsten Einheit (2 Nachkommastellen). Währungen mit
// anderen Nachkommastellen (z. B. JPY) sind noch nicht vorgesehen.

var amountRe = regexp.MustCompile(`^\d{1,13}(\.\d{1,2})?$`)

// parseAmount wandelt einen positiven Betrag in Rappen/Cent.
func parseAmount(s string) (int64, error) {
	t := strings.NewReplacer("'", "", "’", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
	if !amountRe.MatchString(t) {
		return 0, fmt.Errorf("Betrag %q: positive Dezimalzahl mit höchstens 2 Nachkommastellen erwartet", s)
	}
	whole, frac, _ := strings.Cut(t, ".")
	frac = (frac + "00")[:2]
	n, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("Betrag %q: %v", s, err)
	}
	if n == 0 {
		return 0, fmt.Errorf("Betrag darf nicht 0 sein")
	}
	return n, nil
}

// formatAmount: Rappen/Cent → "1500.50" (negativ mit Minus).
func formatAmount(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	return fmt.Sprintf("%s%d.%02d", sign, minor/100, minor%100)
}

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
