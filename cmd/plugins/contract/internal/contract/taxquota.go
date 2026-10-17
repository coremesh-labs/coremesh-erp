package contract

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Vorsteueraufteilung (seit 0.27.0, Route ContractCondition.taxQuota): Anteil
// der steuerpflichtig genutzten Mietobjekte eines Objekts zum Stichtag, für die
// aufzuteilende Vorsteuer (Steuerkennzeichen der Art PARTIAL, § 15 Abs. 4 UStG).
//
//   - Mietobjekte: das Mietobjekt selbst (Vertragsobjekt: seine Bestandteile),
//     beim Gebäude bzw. bei der Wirtschaftseinheit alle Mietobjekte darin – ohne
//     Pools und Vertragsobjekte, gültig am Stichtag;
//   - steuerpflichtig: ein aktiver oder gekündigter Vertrag des Buchungskreises
//     am Mietobjekt (oder am Vertragsobjekt, zu dem es gehört) berechnet am
//     Stichtag Umsatzsteuer (nachgelagerte Kondition mit Steuerkennzeichen der
//     Art Umsatzsteuer) – Mietverträge wie Hausgeldverträge optierender Eigentümer;
//   - Gewicht: Bemessung der Bemessungsart des Steuerkennzeichens, ohne
//     Bemessungsart 1 je Mietobjekt.

type quotaPart struct {
	Object string `json:"object_id"`
	Weight string `json:"weight"`
	Taxed  bool   `json:"taxed"`
	By     string `json:"contract_id,omitempty"`
}

type quota struct {
	Num, Den *big.Rat
	Parts    []quotaPart
}

func (q quota) text(measure string) string {
	basis := "Mietobjekte"
	if measure != "" {
		basis = measure
	}
	return fmt.Sprintf("%s von %s %s steuerpflichtig", q.Num.FloatString(4), q.Den.FloatString(4), basis)
}

func (m *Module) taxQuota(ctx context.Context, cc, objType, objID, date, measure string) (quota, error) {
	if strings.TrimSpace(objID) == "" {
		return quota{}, crud.Invalid("aufzuteilende Vorsteuer braucht ein Objekt (Mietobjekt, Gebäude oder Wirtschaftseinheit)")
	}
	list := func(object string) ([]map[string]any, error) {
		resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": map[string]any{"company_code": cc, "includeHistory": "true"}})
		if err != nil {
			return nil, unavailable("Immobilien", err)
		}
		var l struct {
			Items []map[string]any `json:"items"`
		}
		return l.Items, sdk.Decode(resp.Payload, &l)
	}
	valid := func(r map[string]any) bool {
		from, to := dateOf(r["valid_from"]), dateOf(r["valid_to"])
		return (from == "" || from <= date) && (to == "" || to >= date)
	}
	objects, err := list("RentObject")
	if err != nil {
		return quota{}, err
	}
	items, err := list("CompositeItem")
	if err != nil {
		return quota{}, err
	}
	composite := map[string]string{} // Bestandteil → Vertragsobjekt
	parts := map[string][]string{}   // Vertragsobjekt → Bestandteile
	for _, it := range items {
		if valid(it) {
			o, c := crud.Str(it["object_id"]), crud.Str(it["composite_id"])
			composite[o] = c
			parts[c] = append(parts[c], o)
		}
	}
	kinds := map[string]string{}
	var members []string
	for _, r := range objects {
		id, kind := crud.Str(r["object_id"]), crud.Str(r["kind"])
		kinds[id] = kind
		if !valid(r) || kind == "POOL" || kind == "COMPOSITE" {
			continue
		}
		switch objType {
		case "Building":
			if crud.Str(r["building_id"]) == objID {
				members = append(members, id)
			}
		case "BusinessEntity":
			if crud.Str(r["entity_id"]) == objID {
				members = append(members, id)
			}
		}
	}
	if objType == "RentObject" || objType == "" {
		if kinds[objID] == "COMPOSITE" {
			members = parts[objID]
		} else {
			members = []string{objID}
		}
	}
	sort.Strings(members)
	if len(members) == 0 {
		return quota{}, crud.Invalid("%s %s hat am %s keine Mietobjekte – Vorsteuer nicht aufteilbar", objType, objID, date)
	}
	taxed, err := m.taxedObjects(ctx, cc, date)
	if err != nil {
		return quota{}, err
	}
	q := quota{Num: new(big.Rat), Den: new(big.Rat)}
	for _, o := range members {
		w := big.NewRat(1, 1)
		if measure != "" {
			if w, err = m.measurementOf(ctx, cc, o, measure, date); err != nil {
				return quota{}, err
			}
		}
		by := firstNonEmpty(taxed[o], taxed[composite[o]])
		q.Den.Add(q.Den, w)
		if by != "" {
			q.Num.Add(q.Num, w)
		}
		q.Parts = append(q.Parts, quotaPart{Object: o, Weight: w.FloatString(4), Taxed: by != "", By: by})
	}
	if q.Den.Sign() == 0 {
		return quota{}, crud.Invalid("%s %s: Summe der Bemessung %s ist 0 – Vorsteuer nicht aufteilbar", objType, objID, measure)
	}
	return q, nil
}

// taxedObjects: Objekte mit einem Vertrag, der am Stichtag Umsatzsteuer berechnet → Vertrag.
func (m *Module) taxedObjects(ctx context.Context, cc, date string) (map[string]string, error) {
	res, err := m.db.Query(ctx, `SELECT DISTINCT o.object_id, c.contract_id FROM contract__object o
		JOIN contract__contract c ON c.company_code = o.company_code AND c.contract_id = o.contract_id
		WHERE o.company_code = ? AND o.valid_from <= ? AND o.valid_to >= ? AND c.status IN (?, ?)
		AND EXISTS (SELECT 1 FROM contract__condition k WHERE k.company_code = c.company_code AND k.contract_id = c.contract_id
			AND k.calc_method = ? AND k.valid_from <= ? AND k.valid_to >= ?)
		ORDER BY c.contract_id`, cc, date, date, statusActive, statusTerminated, calcDependent, date, date)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	checked := map[string]bool{}
	for _, r := range res.Rows {
		obj, id := crud.Str(r[0]), crud.Str(r[1])
		ok, seen := checked[id]
		if !seen {
			c, err := m.contractOf(ctx, cc, id)
			if err != nil {
				return nil, err
			}
			if ok, err = m.chargesOutputTax(ctx, c, date); err != nil {
				return nil, crud.Invalid("Vertrag %s: %v", id, err)
			}
			checked[id] = ok
		}
		if ok && out[obj] == "" {
			out[obj] = id
		}
	}
	return out, nil
}

func (m *Module) chargesOutputTax(ctx context.Context, c *contractRow, date string) (bool, error) {
	_, results, _, err := m.positions(ctx, c, date)
	if err != nil {
		return false, err
	}
	for _, r := range results {
		if r.Dependent.Method != pricing.MethodTax || r.Amount == 0 {
			continue
		}
		if _, kind, _, err := pricing.New(m.services).TaxRate(ctx, r.TaxCode); err == nil && kind == "OUTPUT" {
			return true, nil
		}
	}
	return false, nil
}

// taxQuotaAction: ContractCondition.taxQuota {company_code, object_type, object_id, date,
// measurement_type} → {numerator, denominator, quota, text, parts}.
func (m *Module) taxQuotaAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		ObjectType  string `json:"object_type"`
		ObjectID    string `json:"object_id"`
		Date        string `json:"date"`
		Measure     string `json:"measurement_type"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.CompanyCode)
	if ok, err := sdk.CheckAccess(ctx, "Contract", "get", cc); err != nil || !ok {
		return sdk.Response{}, fmt.Errorf("%w: Verträge %s lesen", sdk.ErrPermissionDenied, cc)
	}
	m.migrate(ctx)
	date := firstNonEmpty(strings.TrimSpace(in.Date), crud.KeyDate(ctx))
	if len(date) > 10 {
		date = date[:10]
	}
	measure := strings.ToUpper(strings.TrimSpace(in.Measure))
	q, err := m.taxQuota(ctx, cc, strings.TrimSpace(in.ObjectType), strings.TrimSpace(in.ObjectID), date, measure)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"numerator": q.Num.FloatString(6), "denominator": q.Den.FloatString(6),
		"quota": new(big.Rat).Quo(q.Num, q.Den).FloatString(8), "text": q.text(measure), "parts": q.Parts, "date": date}}, nil
}
