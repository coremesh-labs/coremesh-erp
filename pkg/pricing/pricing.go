// Package pricing rechnet Positionen für Vertrag, Angebot und Rechnung:
// Menge × Einzelpreis = Gesamtpreis nach einer Rundungsregel (Plugin ledger,
// Object RoundingRule) und nachgelagerte Positionen (z. B. Umsatzsteuer aus dem
// Steuerkennzeichen, Materialanteil in Prozent) aus ihrer Basis.
//
// Beträge sind ganze Zahlen in der kleinsten Einheit der Währung; Mengen und
// Einzelpreise Dezimalzahlen (*big.Rat), damit nichts unterwegs rundet.
package pricing

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Rundungsarten.
const (
	HalfUp   = "HALF_UP"   // kaufmännisch: ab 5 vom Betrag weg aufrunden
	HalfEven = "HALF_EVEN" // Bankrundung
	Up       = "UP"        // vom Betrag weg
	Down     = "DOWN"      // zum Betrag hin
)

// Rounding ist eine Rundungsregel.
type Rounding struct {
	Code          string
	Mode          string
	Increment     *big.Rat // Schrittweite in der Währung, z. B. 1/100; 5/100 = Rappenrundung
	PriceDecimals int      // Nachkommastellen des Einzelpreises
}

// Builtin: kaufmännisch auf die kleinste Einheit der Währung (ohne Eintrag im Hauptbuch).
func Builtin(decimals int) Rounding {
	return Rounding{Mode: HalfUp, Increment: new(big.Rat).SetFrac(big.NewInt(1), pow10(decimals)), PriceDecimals: max(decimals, 2)}
}

// Round rundet v auf die Schrittweite.
func (r Rounding) Round(v *big.Rat) *big.Rat {
	inc := r.Increment
	if inc == nil || inc.Sign() <= 0 {
		inc = big.NewRat(1, 100)
	}
	q := new(big.Rat).Quo(v, inc)
	n := roundInt(q, r.Mode)
	return new(big.Rat).Mul(new(big.Rat).SetInt(n), inc)
}

// Minor: v gerundet in der kleinsten Einheit der Währung (decimals Nachkommastellen).
func (r Rounding) Minor(v *big.Rat, decimals int) int64 {
	x := new(big.Rat).Mul(r.Round(v), new(big.Rat).SetInt(pow10(decimals)))
	return roundInt(x, HalfUp).Int64() // Schrittweite feiner als die Währung: auf die kleinste Einheit
}

// roundInt rundet q nach der Art auf eine ganze Zahl.
func roundInt(q *big.Rat, mode string) *big.Int {
	num, den := new(big.Int).Set(q.Num()), q.Denom()
	neg := num.Sign() < 0
	num.Abs(num)
	whole, rest := new(big.Int).QuoRem(num, den, new(big.Int))
	if rest.Sign() != 0 {
		twice := new(big.Int).Mul(rest, big.NewInt(2))
		switch mode {
		case Up:
			whole.Add(whole, big.NewInt(1))
		case Down:
		case HalfEven:
			if c := twice.Cmp(den); c > 0 || (c == 0 && whole.Bit(0) == 1) {
				whole.Add(whole, big.NewInt(1))
			}
		default: // HalfUp
			if twice.Cmp(den) >= 0 {
				whole.Add(whole, big.NewInt(1))
			}
		}
	}
	if neg {
		whole.Neg(whole)
	}
	return whole
}

func pow10(n int) *big.Int { return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil) }

// ParseDecimal: "1.250,5" (deutsch), "1250.5" oder "1250" → Dezimalzahl.
func ParseDecimal(s string) (*big.Rat, error) {
	t := strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	if strings.Contains(t, ",") {
		t = strings.ReplaceAll(strings.ReplaceAll(t, ".", ""), ",", ".")
	}
	if t == "" {
		return nil, fmt.Errorf("Zahl fehlt")
	}
	v, ok := new(big.Rat).SetString(t)
	if !ok || strings.ContainsAny(t, "/eE") {
		return nil, fmt.Errorf("%q ist keine Dezimalzahl", s)
	}
	return v, nil
}

// Decimals: Nachkommastellen von v (höchstens 12).
func Decimals(v *big.Rat) int {
	x := new(big.Rat).Set(v)
	for d := 0; d <= 12; d++ {
		if x.IsInt() {
			return d
		}
		x.Mul(x, big.NewRat(10, 1))
	}
	return 13
}

// Format: v mit genau decimals Nachkommastellen (Punkt), kaufmännisch gerundet.
func Format(v *big.Rat, decimals int) string { return v.FloatString(decimals) }

// FormatTrim: v mit höchstens decimals Nachkommastellen, ohne Nullen am Ende ("1.5", "3").
func FormatTrim(v *big.Rat, decimals int) string {
	s := v.FloatString(decimals)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

// FormatDE: Dezimalzahl mit Punkt ("1234.5", "-0.25") deutsch mit Tausenderpunkten
// und Komma ("1.234,5", "-0,25") – für Texte wie „Umsatzsteuer 19 % auf 1.000,00“.
func FormatDE(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if hasFrac {
		b.WriteString("," + frac)
	}
	return sign + b.String()
}

// FromMinor: Betrag in der kleinsten Einheit → Dezimalzahl.
func FromMinor(minor int64, decimals int) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(minor), pow10(decimals))
}

// Total: Menge × Einzelpreis, gerundet in der kleinsten Einheit; prüft die
// Nachkommastellen des Einzelpreises gegen die Regel.
func (r Rounding) Total(quantity, price *big.Rat, decimals int) (int64, error) {
	if price == nil {
		return 0, fmt.Errorf("Einzelpreis fehlt")
	}
	if d := Decimals(price); d > max(r.PriceDecimals, decimals) {
		return 0, fmt.Errorf("Einzelpreis %s: höchstens %d Nachkommastellen (Rundungsregel %s)", FormatTrim(price, 12), max(r.PriceDecimals, decimals), firstOf(r.Code, "Standard"))
	}
	if quantity == nil {
		quantity = big.NewRat(1, 1)
	}
	return r.Minor(new(big.Rat).Mul(quantity, price), decimals), nil
}

// --- Rundungsregel aus dem Hauptbuch ------------------------------------------------------

// Client liest Rundungsregeln und Steuersätze aus dem Hauptbuch.
type Client struct{ s module.Services }

// New erzeugt den Client (env.Services des Moduls).
func New(services module.Services) Client { return Client{services} }

// Rounding: Regel code; leer = Regel des Buchungskreises (LedgerCompanyConfig),
// sonst die Standardregel, sonst kaufmännisch auf die kleinste Einheit.
func (c Client) Rounding(ctx context.Context, cc, code string, decimals int) Rounding {
	if code = strings.TrimSpace(code); code == "" && cc != "" {
		if resp, err := c.s.Call(ctx, "LedgerCompanyConfig", "get", map[string]any{"id": cc}); err == nil {
			var cfg struct {
				Rule string `json:"rounding_rule"`
			}
			if sdk.Decode(resp.Payload, &cfg) == nil {
				code = strings.TrimSpace(cfg.Rule)
			}
		}
	}
	if code != "" {
		if resp, err := c.s.Call(ctx, "RoundingRule", "get", map[string]any{"id": code}); err == nil {
			var rec map[string]any
			if sdk.Decode(resp.Payload, &rec) == nil {
				if r, ok := ruleOf(rec); ok {
					return r
				}
			}
		}
	}
	if resp, err := c.s.Call(ctx, "RoundingRule", "list", map[string]any{"query": map[string]any{}}); err == nil {
		var l struct {
			Items []map[string]any `json:"items"`
		}
		_ = sdk.Decode(resp.Payload, &l)
		sort.Slice(l.Items, func(i, j int) bool { return fmt.Sprint(l.Items[i]["code"]) < fmt.Sprint(l.Items[j]["code"]) })
		for _, rec := range l.Items {
			if truthy(rec["is_default"]) && truthy(rec["is_active"]) {
				if r, ok := ruleOf(rec); ok {
					return r
				}
			}
		}
	}
	return Builtin(decimals)
}

func ruleOf(rec map[string]any) (Rounding, bool) {
	inc, err := ParseDecimal(fmt.Sprint(rec["increment"]))
	if err != nil || inc.Sign() <= 0 {
		return Rounding{}, false
	}
	pd, _ := strconv.Atoi(fmt.Sprint(rec["price_decimals"]))
	return Rounding{Code: fmt.Sprint(rec["code"]), Mode: fmt.Sprint(rec["mode"]), Increment: inc, PriceDecimals: pd}, true
}

// TaxRate: Satz des Steuerkennzeichens in Prozent, Art und Steuerkonto.
func (c Client) TaxRate(ctx context.Context, code string) (rate *big.Rat, kind, account string, err error) {
	resp, err := c.s.Call(ctx, "TaxCode", "get", map[string]any{"id": code})
	if err != nil {
		return nil, "", "", fmt.Errorf("Steuerkennzeichen %s: %w", code, err)
	}
	var t struct {
		Rate    any    `json:"rate"`
		Kind    string `json:"kind"`
		Account string `json:"account_number"`
	}
	if err := sdk.Decode(resp.Payload, &t); err != nil {
		return nil, "", "", err
	}
	r, err := ParseDecimal(fmt.Sprint(t.Rate))
	if err != nil {
		return nil, "", "", fmt.Errorf("Steuerkennzeichen %s: Satz %v", code, t.Rate)
	}
	return r, t.Kind, t.Account, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case nil:
		return false
	}
	s := strings.ToLower(fmt.Sprint(v))
	return s == "true" || s == "1" || s == "t"
}

func firstOf(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
