package pricing

import (
	"math/big"
	"testing"
)

func rat(s string) *big.Rat { v, _ := ParseDecimal(s); return v }

func TestRounding(t *testing.T) {
	for _, c := range []struct {
		mode, inc, v string
		want         int64
	}{
		{HalfUp, "0.01", "2.345", 235}, {HalfUp, "0.01", "-2.345", -235}, {HalfEven, "0.01", "2.345", 234},
		{HalfEven, "0.01", "2.355", 236}, {Up, "0.01", "2.341", 235}, {Down, "0.01", "2.349", 234},
		{HalfUp, "0.05", "2.37", 235}, {HalfUp, "0.05", "2.38", 240},
	} {
		r := Rounding{Mode: c.mode, Increment: rat(c.inc)}
		if got := r.Minor(rat(c.v), 2); got != c.want {
			t.Errorf("%s %s %s: %d, erwartet %d", c.mode, c.inc, c.v, got, c.want)
		}
	}
	r := Rounding{Code: "KFM", Mode: HalfUp, Increment: rat("0.01"), PriceDecimals: 2}
	if v, err := r.Total(rat("3"), rat("4,99"), 2); err != nil || v != 1497 {
		t.Fatalf("Total: %d %v", v, err)
	}
	if _, err := r.Total(rat("1"), rat("0.1234"), 2); err == nil {
		t.Fatal("4 Stellen im Einzelpreis bei Regel mit 2")
	}
	r.PriceDecimals = 4
	if v, _ := r.Total(rat("1000"), rat("0.3456"), 2); v != 34560 {
		t.Fatalf("Energie: %d", v)
	}
}

func TestApply(t *testing.T) {
	kfm := func(string) Rounding { return Rounding{Mode: HalfUp, Increment: rat("0.01"), PriceDecimals: 2} }
	rates := func(c string) (*big.Rat, error) { return map[string]*big.Rat{"U19": rat("19"), "U7": rat("7")}[c], nil }
	deps := []Dependent{
		{Code: "MWST", Method: MethodTax, Effect: EffectSurcharge, Sort: 90},
		{Code: "MAT", Method: MethodPercent, Percent: rat("10"), Effect: EffectShare, Base: []string{"HAUS"}, Sort: 10},
		{Code: "ZUS", Method: MethodPercent, Percent: rat("5"), Effect: EffectSurcharge, Base: []string{"HAUS"}, Sort: 20},
	}
	lines := []Line{{Key: "1", Kinds: []string{"HAUS"}, TaxCode: "U19", Amount: 50000}, {Key: "2", Kinds: []string{"BUCH"}, TaxCode: "U7", Amount: 1001}}
	res, err := Apply(deps, lines, 2, rates, kfm)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, r := range res {
		got[r.Dependent.Code+"/"+r.TaxCode] = r.Amount
	}
	// MAT 10 % von 500 = 50 (Anteil, nicht in der Basis); ZUS 5 % = 25; MWST 19 % auf 500 + 25, 7 % auf 10,01
	want := map[string]int64{"MAT/U19": 5000, "ZUS/U19": 2500, "MWST/U19": 9975, "MWST/U7": 70}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %d, erwartet %d (%v)", k, got[k], v, got)
		}
	}
	if a := Allocate(100, []int64{1, 1, 1}); a[0]+a[1]+a[2] != 100 || a[0] != 34 {
		t.Errorf("Allocate: %v", a)
	}
	if a := Allocate(-5, []int64{300, 100}); a[0]+a[1] != -5 {
		t.Errorf("Allocate negativ: %v", a)
	}
}

func TestFormatDEAndText(t *testing.T) {
	for in, want := range map[string]string{"201.73": "201,73", "1234567.5": "1.234.567,5", "-1000.00": "-1.000,00", "19": "19", "0.25": "0,25"} {
		if got := FormatDE(in); got != want {
			t.Errorf("FormatDE(%q) = %q, want %q", in, got, want)
		}
	}
	r := Result{Dependent: Dependent{Name: "Umsatzsteuer"}, Rate: big.NewRat(15, 2), Base: 123456}
	if got := r.Text(2); got != "Umsatzsteuer 7,5 % auf 1.234,56" {
		t.Errorf("Text = %q", got)
	}
}
