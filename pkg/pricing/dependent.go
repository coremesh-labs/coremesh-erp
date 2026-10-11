package pricing

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Nachgelagerte Konditionen: aus der eigentlichen Forderung (Basis) entsteht eine
// weitere – Steuer (Satz des Steuerkennzeichens) oder ein Prozentsatz; als Zuschlag
// (kommt zur Basis hinzu, z. B. Umsatzsteuer) oder als Anteil (wird aus der Basis
// herausgerechnet, z. B. Materialanteil eines Hausmeistervertrags).
const (
	MethodTax     = "TAX"
	MethodPercent = "PERCENT"

	EffectSurcharge = "SURCHARGE"
	EffectShare     = "SHARE"
)

// Dependent ist eine nachgelagerte Konditionsart.
type Dependent struct {
	Code, Name string
	Method     string   // TAX | PERCENT
	TaxCode    string   // TAX: festes Steuerkennzeichen; leer = das der Basisposition
	Percent    *big.Rat // PERCENT: Satz in Prozent
	Effect     string   // SURCHARGE | SHARE
	Base       []string // Arten der Basis (Konditionsart, Kostenart); leer = alle außer Steuer
	Account    string
	Rounding   string // Rundungsregel (leer = Standard)
	Sort       int64
	Default    bool // Rechnungen und Angebote ohne Vertrag bekommen sie (Standard)
}

// Line ist eine Basisposition.
type Line struct {
	Key     string   // Position (z. B. Positionsnummer)
	Kinds   []string // Arten der Position (Konditionsart, Kostenart)
	TaxCode string
	Amount  int64 // kleinste Einheit
}

// Result ist eine nachgelagerte Position.
type Result struct {
	Dependent Dependent
	TaxCode   string // Steuer: das Kennzeichen der Gruppe
	Rate      *big.Rat
	Base      int64 // Summe der Basis
	Amount    int64
	Shares    map[string]int64 // Anteil: je Basisposition herausgerechnet
}

// Text: „Umsatzsteuer 19 % auf 1.000,00“.
func (r Result) Text(decimals int) string {
	return fmt.Sprintf("%s %s %% auf %s", firstOf(r.Dependent.Name, r.Dependent.Code), FormatTrim(r.Rate, 4), FromMinor(r.Base, decimals).FloatString(decimals))
}

// RateFunc liefert den Satz eines Steuerkennzeichens (Prozent).
type RateFunc func(taxCode string) (*big.Rat, error)

// RoundFunc liefert die Rundungsregel (leer = Standard).
type RoundFunc func(code string) Rounding

// Apply rechnet die nachgelagerten Positionen der Reihe nach (Reihenfolge, Schlüssel);
// eine spätere Art sieht die früheren als Basis (z. B. Steuer auf den Materialanteil),
// außer Steuer – Steuer auf Steuer gibt es nicht. Zuschlag: je Gruppe (bei Steuer ohne
// festes Kennzeichen je Kennzeichen der Basis) auf die Summe; Anteil: je Basisposition.
func Apply(deps []Dependent, lines []Line, decimals int, rate RateFunc, round RoundFunc) ([]Result, error) {
	ds := append([]Dependent(nil), deps...)
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Sort != ds[j].Sort {
			return ds[i].Sort < ds[j].Sort
		}
		return ds[i].Code < ds[j].Code
	})
	type poolLine struct {
		Line
		tax bool // Ergebnis einer Steuer
	}
	var pool []poolLine
	for _, l := range lines {
		pool = append(pool, poolLine{Line: l})
	}
	var out []Result
	for _, d := range ds {
		var match []poolLine
		for _, l := range pool {
			if matches(d.Base, l.Kinds) && !(l.tax && (len(d.Base) == 0 || d.Method == MethodTax)) {
				match = append(match, l)
			}
		}
		if len(match) == 0 {
			continue
		}
		r := round(d.Rounding)
		groups := map[string][]poolLine{}
		var order []string
		for _, l := range match {
			// je Steuerkennzeichen der Basis: ein prozentualer Zuschlag trägt das Kennzeichen
			// seiner Basis weiter (Steuer auf den Zuschlag)
			g := l.TaxCode
			if d.Method == MethodTax {
				g = firstOf(d.TaxCode, l.TaxCode)
				if g == "" {
					continue // Basis ohne Steuerkennzeichen
				}
			}
			if _, ok := groups[g]; !ok {
				order = append(order, g)
			}
			groups[g] = append(groups[g], l)
		}
		sort.Strings(order)
		for _, g := range order {
			var pct *big.Rat
			if d.Method == MethodTax {
				p, err := rate(g)
				if err != nil {
					return nil, err
				}
				pct = p
			} else {
				if d.Percent == nil {
					return nil, fmt.Errorf("Nachgelagerte Kondition %s: Prozentsatz fehlt", d.Code)
				}
				pct = d.Percent
			}
			factor := new(big.Rat).Quo(pct, big.NewRat(100, 1))
			res := Result{Dependent: d, TaxCode: g, Rate: pct}
			if d.Effect == EffectShare {
				res.Shares = map[string]int64{}
				for _, l := range groups[g] {
					s := r.Minor(new(big.Rat).Mul(FromMinor(l.Amount, decimals), factor), decimals)
					res.Shares[l.Key] += s
					res.Amount += s
					res.Base += l.Amount
				}
			} else {
				for _, l := range groups[g] {
					res.Base += l.Amount
				}
				res.Amount = r.Minor(new(big.Rat).Mul(FromMinor(res.Base, decimals), factor), decimals)
			}
			if res.Amount == 0 {
				continue
			}
			out = append(out, res)
			if d.Effect != EffectShare { // ein Anteil ist schon in der Basis enthalten
				kinds := []string{d.Code}
				pool = append(pool, poolLine{Line: Line{Key: "dep:" + d.Code + ":" + g, Kinds: kinds, TaxCode: res.TaxCode, Amount: res.Amount},
					tax: d.Method == MethodTax})
			}
		}
	}
	return out, nil
}

// Matches: Position mit den Arten kinds gehört zur Basis base (leer = alle).
func Matches(base, kinds []string) bool { return matches(base, kinds) }

func matches(base, kinds []string) bool {
	if len(base) == 0 {
		return true
	}
	for _, b := range base {
		for _, k := range kinds {
			if k != "" && strings.EqualFold(b, k) {
				return true
			}
		}
	}
	return false
}

// Allocate verteilt total auf die Gewichte (größter Rest), z. B. nicht abziehbare
// Steuer auf die Aufwandskonten der Basis.
func Allocate(total int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	var sum int64
	for _, w := range weights {
		sum += abs(w)
	}
	if sum == 0 || len(weights) == 0 {
		if len(out) > 0 {
			out[0] = total
		}
		return out
	}
	type rest struct {
		i int
		r *big.Rat
	}
	var rests []rest
	var given int64
	for i, w := range weights {
		v := new(big.Rat).SetFrac(big.NewInt(total*abs(w)), big.NewInt(sum))
		n := new(big.Int).Quo(v.Num(), v.Denom()).Int64()
		out[i] = n
		given += n
		rests = append(rests, rest{i, new(big.Rat).Sub(v, new(big.Rat).SetInt64(n))})
	}
	sort.SliceStable(rests, func(a, b int) bool { return rests[a].r.Cmp(rests[b].r) > 0 })
	step := int64(1)
	if total-given < 0 {
		step = -1
		sort.SliceStable(rests, func(a, b int) bool { return rests[a].r.Cmp(rests[b].r) < 0 })
	}
	for k := 0; given != total && k < len(rests); k++ {
		out[rests[k].i] += step
		given += step
	}
	return out
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// --- Arten aus dem Vertragsmodul bzw. der eigenen Tabelle ---------------------------------

// DependentOf wandelt einen Datensatz (ContractDependentType bzw. die Tabelle der
// Beschaffung) in eine Dependent.
func DependentOf(rec map[string]any) Dependent {
	s := func(k string) string {
		if rec[k] == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(rec[k]))
	}
	d := Dependent{Code: s("code"), Name: s("name"), Method: s("method"), TaxCode: s("tax_code"), Effect: s("effect"),
		Account: s("account_number"), Rounding: s("rounding_rule"), Default: truthy(rec["invoice_default"])}
	if p, err := ParseDecimal(s("percent")); err == nil {
		d.Percent = p
	}
	for _, b := range strings.Split(s("base_types"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			d.Base = append(d.Base, strings.ToUpper(b))
		}
	}
	fmt.Sscan(s("sort_order"), &d.Sort)
	if d.Effect == "" {
		d.Effect = EffectSurcharge
	}
	return d
}

// Dependents: aktive Arten eines Buchungskreises aus object (z. B. ContractDependentType).
func (c Client) Dependents(ctx context.Context, object, cc string) ([]Dependent, error) {
	resp, err := c.s.Call(ctx, object, "list", map[string]any{"query": map[string]any{"company_code": cc}})
	if err != nil {
		return nil, err
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &l); err != nil {
		return nil, err
	}
	var out []Dependent
	for _, rec := range l.Items {
		if fmt.Sprint(rec["company_code"]) != cc || (rec["is_active"] != nil && !truthy(rec["is_active"])) {
			continue
		}
		out = append(out, DependentOf(rec))
	}
	return out, nil
}
