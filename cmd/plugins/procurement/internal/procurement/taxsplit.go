package procurement

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Steueranteile (seit 0.11.0):
//
//   - nicht abziehbare Vorsteuer bleibt im Kostenbetrag der Basisposition und steht
//     zusätzlich in nondeductible_tax; beim Buchen geht sie auf das „Konto für nicht
//     abziehbare Vorsteuer“ des Sachkontos (Hauptbuch), sonst mit auf das Sachkonto;
//   - aufzuteilende Vorsteuer (Steuerkennzeichen der Art PARTIAL): je Basisposition
//     Quote der steuerpflichtig genutzten Mietobjekte des Objekts (Verträge ->
//     ContractCondition.taxQuota) zum Leistungsdatum (Ende des Leistungszeitraums,
//     sonst Rechnungs-, sonst Buchungsdatum); der abziehbare Teil geht auf das
//     Steuerkonto des Kennzeichens, der Rest wie nicht abziehbare Vorsteuer.

const taxPartial = "PARTIAL"

type quotaInfo struct {
	rate       *big.Rat
	text, date string
}

// quotaCache: Vorsteuerquote je Objekt, Stichtag und Bemessungsart (einmal je Beleg).
type quotaCache struct {
	m  *Module
	cc string
	q  map[string]quotaInfo
}

func newQuotaCache(m *Module, cc string) *quotaCache {
	return &quotaCache{m: m, cc: cc, q: map[string]quotaInfo{}}
}

func (c *quotaCache) get(ctx context.Context, objType, objID, date, measure string) (quotaInfo, error) {
	key := objType + "|" + objID + "|" + date + "|" + measure
	if q, ok := c.q[key]; ok {
		return q, nil
	}
	if strings.TrimSpace(objID) == "" {
		return quotaInfo{}, crud.Invalid("aufzuteilende Vorsteuer: Position ohne Objekt – Mietobjekt, Gebäude oder Wirtschaftseinheit angeben")
	}
	resp, err := c.m.services.Call(ctx, "ContractCondition", "taxQuota", map[string]any{"company_code": c.cc,
		"object_type": objType, "object_id": objID, "date": date, "measurement_type": measure})
	if err != nil {
		return quotaInfo{}, crud.Invalid("Vorsteuerquote %s %s am %s: %v", objType, objID, date, err)
	}
	var out struct {
		Quota string `json:"quota"`
		Text  string `json:"text"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return quotaInfo{}, err
	}
	r, ok := new(big.Rat).SetString(out.Quota)
	if !ok || r.Sign() < 0 || r.Cmp(big.NewRat(1, 1)) > 0 {
		return quotaInfo{}, fmt.Errorf("Vorsteuerquote %q ungültig", out.Quota)
	}
	q := quotaInfo{rate: r, date: date, text: fmt.Sprintf("%s %% (%s, %s %s am %s)", percent(r), out.Text, objType, objID, date)}
	c.q[key] = q
	return q, nil
}

// percent: Anteil als Prozent mit zwei Nachkommastellen (deutsch).
func percent(r *big.Rat) string {
	return strings.Replace(new(big.Rat).Mul(r, big.NewRat(100, 1)).FloatString(2), ".", ",", 1)
}

// scaleRat: Betrag × Anteil, kaufmännisch gerundet.
func scaleRat(amount int64, f *big.Rat) int64 {
	v := new(big.Rat).Mul(big.NewRat(amount, 1), f)
	n, d := v.Num(), v.Denom()
	q, r := new(big.Int).QuoRem(n, d, new(big.Int))
	if new(big.Int).Mul(new(big.Int).Abs(r), big.NewInt(2)).Cmp(d) >= 0 {
		if n.Sign() < 0 {
			q.Sub(q, big.NewInt(1))
		} else {
			q.Add(q, big.NewInt(1))
		}
	}
	return q.Int64()
}

// serviceDate: Stichtag der Vorsteuerquote einer Position.
func serviceDate(serviceTo, serviceFrom, invoiceDate, postingDate any) string {
	for _, v := range []any{serviceTo, serviceFrom, invoiceDate, postingDate} {
		if d, err := crud.ParseDate(v); err == nil && d != "" {
			return d
		}
	}
	return ""
}

// splitMeasurement: Bemessungsart der Aufteilung am Steuerkennzeichen ("" = Anzahl Mietobjekte).
func (m *Module) splitMeasurement(ctx context.Context, code string) string {
	resp, err := m.services.Call(ctx, "TaxCode", "get", map[string]any{"id": code})
	if err != nil {
		return ""
	}
	var t struct {
		Split string `json:"split_measurement"`
	}
	_ = sdk.Decode(resp.Payload, &t)
	return strings.TrimSpace(t.Split)
}

// taxAccounts: „Konto für nicht abziehbare Vorsteuer“ je Sachkonto (Hauptbuch, einmal je Beleg).
type taxAccounts struct {
	m   *Module
	cc  string
	acc map[string]string
}

func (t *taxAccounts) of(ctx context.Context, account string) (string, error) {
	if a, ok := t.acc[account]; ok {
		return a, nil
	}
	resp, err := t.m.services.Call(ctx, "GLAccountCompany", "list", map[string]any{"query": map[string]any{"company_code_id": t.cc, "account_number": account}})
	if err != nil {
		return "", unavailable("Hauptbuch", err)
	}
	var out struct {
		Items []struct {
			AccountNumber string `json:"account_number"`
			TaxAccount    string `json:"nondeductible_tax_account"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return "", err
	}
	sub := ""
	for _, it := range out.Items {
		if sameAccount(it.AccountNumber, account) && strings.TrimSpace(it.TaxAccount) != "" {
			if sub, err = t.m.glAccount(ctx, t.cc, strings.TrimSpace(it.TaxAccount)); err != nil {
				return "", err
			}
		}
	}
	t.acc[account] = sub
	return sub, nil
}

// setTaxParts: Steueranteile und Vorsteuerquote einer Basisposition (leer = keine).
func (m *Module) setTaxParts(ctx context.Context, cc, id string, line, nonded, ded int64, q quotaInfo) error {
	var n, d, rate, text, date any
	if nonded != 0 {
		n = nonded
	}
	if q.rate != nil {
		d, rate, text, date = ded, q.rate.FloatString(8), q.text, q.date
	}
	_, err := m.db.Exec(ctx, `UPDATE procurement__invoice_item SET nondeductible_tax = ?, deductible_tax = ?, tax_quota = ?, tax_quota_text = ?, tax_quota_date = ?
		WHERE company_code = ? AND invoice_id = ? AND line_no = ?`, n, d, rate, text, date, cc, id, line)
	return err
}
