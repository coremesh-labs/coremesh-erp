package ledger

import (
	"context"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"sync"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Beträge und Währungsumrechnung.
//
// Beträge werden als ganze Zahl in der kleinsten Einheit der Währung gespeichert
// (ledger__currency.decimals: EUR 2 → Cent, JPY 0 → Yen). Rechnen mit Kursen
// erfolgt exakt mit big.Rat; gerundet wird kaufmännisch (halb weg von null).
//
// Kurse (analog SAP TCURR) gelten ab valid_from bis zum nächsten Kurs:
//
//	from_factor × from_currency = rate × to_factor × to_currency
//	z. B. 100 JPY = 0.5841 CHF  → from_factor 100, rate 0.5841, to_factor 1
//
// Fehlt der direkte Kurs, gilt der Kehrwert des umgekehrten Kurses.

var (
	amountRe   = regexp.MustCompile(`^\d{1,15}(\.\d+)?$`)
	rateRe     = regexp.MustCompile(`^\d{1,12}(\.\d{1,10})?$`)
	currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
)

// normalizeDecimal: "1'250,50" → "1250.50".
func normalizeDecimal(s string) string {
	return strings.NewReplacer("'", "", "’", "", " ", "", ",", ".").Replace(strings.TrimSpace(s))
}

// parseAmount wandelt einen positiven Dezimalbetrag in die kleinste Einheit.
func parseAmount(s string, decimals int) (int64, error) {
	t := normalizeDecimal(s)
	if !amountRe.MatchString(t) {
		return 0, fmt.Errorf("Betrag %q: positive Dezimalzahl erwartet", s)
	}
	whole, frac, _ := strings.Cut(t, ".")
	if len(frac) > decimals {
		return 0, fmt.Errorf("Betrag %q: höchstens %d Nachkommastellen", s, decimals)
	}
	frac += strings.Repeat("0", decimals-len(frac))
	n, ok := new(big.Int).SetString(whole+frac, 10)
	if !ok || !n.IsInt64() {
		return 0, fmt.Errorf("Betrag %q ist zu groß", s)
	}
	if n.Sign() == 0 {
		return 0, fmt.Errorf("Betrag darf nicht 0 sein")
	}
	return n.Int64(), nil
}

// formatAmount: kleinste Einheit → Dezimaltext ("-1250.50"; bei 0 Nachkommastellen "1250").
func formatAmount(minor int64, decimals int) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	s := fmt.Sprintf("%0*d", decimals+1, minor)
	if decimals == 0 {
		return sign + s
	}
	return sign + s[:len(s)-decimals] + "." + s[len(s)-decimals:]
}

// rateOf parst einen Kurs (> 0).
func rateOf(s string) (*big.Rat, error) {
	t := normalizeDecimal(s)
	r, ok := new(big.Rat).SetString(t)
	if !rateRe.MatchString(t) || !ok || r.Sign() <= 0 {
		return nil, fmt.Errorf("Kurs %q: positive Dezimalzahl erwartet (höchstens 10 Nachkommastellen)", s)
	}
	return r, nil
}

// convertMinor rechnet minor (Einheit von fromDec) mit dem Faktor factor
// (1 from = factor to) in die kleinste Einheit von toDec um.
func convertMinor(minor int64, fromDec, toDec int, factor *big.Rat) int64 {
	v := new(big.Rat).SetInt64(minor)
	v.Mul(v, factor)
	v.Mul(v, new(big.Rat).SetFrac(pow10(toDec), pow10(fromDec)))
	return roundHalfAway(v)
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

func roundHalfAway(v *big.Rat) int64 {
	num, den := new(big.Int).Abs(v.Num()), v.Denom()
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if new(big.Int).Mul(r, big.NewInt(2)).Cmp(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if v.Sign() < 0 {
		q.Neg(q)
	}
	return q.Int64()
}

// currencies hält die Nachkommastellen je Währung (decimals ist nach dem
// Anlegen unveränderlich, daher genügt ein einfacher Cache).
type currencies struct {
	m  *Module
	mu sync.Mutex
	d  map[string]int
}

func (c *currencies) decimals(ctx context.Context, code string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d, ok := c.d[code]; ok {
		return d, nil
	}
	res, err := c.m.db.Query(ctx, "SELECT decimals, is_active FROM ledger__currency WHERE code = ?", code)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, crud.Invalid("Währung %q gibt es nicht", code)
	}
	if c.d == nil {
		c.d = map[string]int{}
	}
	c.d[code] = int(toInt(res.Rows[0][0]))
	return c.d[code], nil
}

// factor liefert den Umrechnungsfaktor 1 from = factor to am Stichtag date.
func (m *Module) factor(ctx context.Context, rateType, from, to, date string) (*big.Rat, error) {
	if from == to {
		return big.NewRat(1, 1), nil
	}
	q := `SELECT rate, from_factor, to_factor FROM ledger__exchange_rate
		WHERE rate_type = ? AND from_currency = ? AND to_currency = ? AND valid_from <= ?
		ORDER BY valid_from DESC LIMIT 1`
	for _, inverse := range []bool{false, true} {
		a, b := from, to
		if inverse {
			a, b = to, from
		}
		res, err := m.db.Query(ctx, q, rateType, a, b, date)
		if err != nil {
			return nil, err
		}
		if len(res.Rows) == 0 {
			continue
		}
		r, err := rateOf(crud.Str(res.Rows[0][0]))
		if err != nil {
			return nil, err
		}
		// a_factor × a = rate × b_factor × b  →  1 a = rate × b_factor / a_factor b
		f := new(big.Rat).Mul(r, new(big.Rat).SetFrac64(toInt(res.Rows[0][2]), max(toInt(res.Rows[0][1]), 1)))
		if inverse {
			f.Inv(f)
		}
		return f, nil
	}
	return nil, crud.Invalid("kein Kurs %s → %s (Kurstyp %s) am %s – Kurs pflegen oder ledger:load-rates", from, to, rateType, date)
}

func toInt(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		var i int64
		fmt.Sscan(n, &i)
		return i
	}
	return 0
}
