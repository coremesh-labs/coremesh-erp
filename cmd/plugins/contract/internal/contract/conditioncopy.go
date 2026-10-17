package contract

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Konditionen kopieren (Aktion „Konditionen kopieren …“ am Vertrag, seit 0.12.0):
//
//   - in dasselbe Wirtschaftsjahr weiter: Zielvertrag leer – die zum Stichtag
//     gültigen Konditionen enden am Tag vor „Gültig ab“ und beginnen dort neu
//     (gleiche Werte; danach die Beträge des neuen Wirtschaftsplans eintragen);
//   - auf einen anderen Vertrag (z. B. die nächste Wohnung derselben WEG): die
//     Konditionen werden dort ab „Gültig ab“ angelegt. Mit „Anteil neu rechnen
//     nach“ (Bemessungsart, z. B. WFL oder MEA) wird ein fester Betrag im
//     Verhältnis der Bemessungen der Hauptobjekte umgerechnet:
//     Betrag × Bemessung Ziel / Bemessung Quelle. Konditionen „je Einheit“
//     rechnen ohnehin mit der Bemessung des Zielobjekts.
//
// Konditionen mit eigenem Objekt (object_id) werden nur im selben Vertrag
// fortgeschrieben, auf einen anderen Vertrag ohne Objekt übernommen.

var copyFields = []string{"calc_method", "measurement_type", "frequency", "due_day", "period_start_month", "due_month", "due_date", "installments", "payment_mode",
	"account_number", "payer_id", "note", "quantity", "unit", "unit_price", "dependent_type", "rounding_rule", "scheme"}

type copyRequest struct {
	ID string `json:"id"`
	// Formulardaten: data.{target_contract_id, source_date, copy_from, copy_to, rescale_by}
	Data struct {
		Target    string `json:"target_contract_id"`
		Source    string `json:"source_date"`
		From      string `json:"copy_from"`
		To        string `json:"copy_to"`
		RescaleBy string `json:"rescale_by"`
	} `json:"data"`
}

func (m *Module) copyConditionsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in copyRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	src, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "update", src.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	from, err := crud.ParseDate(in.Data.From)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Gültig ab der Kopie ist Pflicht (JJJJ-MM-TT)")
	}
	to := ""
	if strings.TrimSpace(in.Data.To) != "" {
		if to, err = crud.ParseDate(in.Data.To); err != nil {
			return sdk.Response{}, err
		}
	}
	at := crud.KeyDate(ctx)
	if strings.TrimSpace(in.Data.Source) != "" {
		if at, err = crud.ParseDate(in.Data.Source); err != nil {
			return sdk.Response{}, err
		}
	}
	target := src
	sameContract := strings.TrimSpace(in.Data.Target) == "" || strings.TrimSpace(in.Data.Target) == src.ID
	if !sameContract {
		if target, err = m.contractOf(ctx, src.CompanyCode, strings.TrimSpace(in.Data.Target)); err != nil {
			return sdk.Response{}, err
		}
	}
	if sameContract && from <= at {
		return sdk.Response{}, crud.Invalid("Fortschreibung im selben Vertrag: Gültig ab (%s) muss nach dem Stichtag der Quelle (%s) liegen", from, at)
	}
	factor := big.NewRat(1, 1)
	rescale := trimUpper(in.Data.RescaleBy)
	if rescale != "" && !sameContract {
		if factor, err = m.shareFactor(ctx, src, target, rescale, at, from); err != nil {
			return sdk.Response{}, err
		}
	}
	res, err := m.db.Query(ctx, `SELECT condition_type, object_id, amount, valid_from, `+strings.Join(copyFields, ", ")+`, position
		FROM contract__condition WHERE company_code = ? AND contract_id = ? AND valid_from <= ? AND valid_to >= ? ORDER BY condition_type, object_id, position`,
		src.CompanyCode, src.ID, at, at)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat am %s keine Konditionen", src.ID, at)
	}
	dec := m.currencyDecimals(ctx, src.Currency)
	cond := m.set.Entity("ContractCondition")
	var done []string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, r := range res.Rows {
			typ, obj, pos := crud.Str(r[0]), crud.Str(r[1]), toInt(r[4+len(copyFields)])
			amount := toInt(r[2])
			data := map[string]any{"company_code": target.CompanyCode, "contract_id": target.ID, "condition_type": typ, "position": pos, "valid_from": from}
			if to != "" {
				data["valid_to"] = to
			}
			if sameContract && obj != "" {
				data["object_id"] = obj
			}
			for i, k := range copyFields {
				if v := r[4+i]; v != nil && crud.Str(v) != "" {
					data[k] = v
				}
			}
			if (crud.Str(data["calc_method"]) == "FIXED" || crud.Str(data["calc_method"]) == "") && factor != nil {
				amount = scale(amount, factor)
				delete(data, "unit_price") // neu aufgeteilt: Gesamtpreis als Einzelpreis, Menge 1
				delete(data, "quantity")
			}
			data["amount"] = formatAmount(amount, dec)
			if sameContract {
				vf, _ := crud.ParseDate(r[3]) // DATE-Spalte kommt je nach Treiber als time.Time
				id := cond.RecordID(crud.Record{"company_code": src.CompanyCode, "contract_id": src.ID, "condition_type": typ, "object_id": obj,
					"position": pos, "valid_from": vf})
				if _, err := cond.Expire(ctx, map[string]any{"id": id, "valid_to": dayBefore(from)}); err != nil {
					return fmt.Errorf("Kondition %s beenden: %w", typ, err)
				}
			}
			if _, err := cond.Create(ctx, map[string]any{"data": data}); err != nil {
				return fmt.Errorf("Kondition %s anlegen: %w", typ, err)
			}
			done = append(done, typ+" "+formatAmount(amount, dec))
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	what := "fortgeschrieben"
	if !sameContract {
		what = "kopiert nach " + target.ID
		if rescale != "" {
			what += fmt.Sprintf(" (Anteil nach %s: × %s)", rescale, factor.FloatString(4))
		}
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%d Konditionen ab %s %s: %s", len(done), from, what, strings.Join(done, ", ")),
		"count": len(done)}}, nil
}

// shareFactor: Bemessung des Hauptobjekts des Ziels / der Quelle (Bemessungsart typ).
func (m *Module) shareFactor(ctx context.Context, src, target *contractRow, typ, srcDate, targetDate string) (*big.Rat, error) {
	value := func(c *contractRow, date string) (*big.Rat, string, error) {
		obj, err := m.mainObject(ctx, c, date)
		if err != nil {
			return nil, "", err
		}
		v, err := m.measurementOf(ctx, c.CompanyCode, obj, typ, date)
		return v, obj, err
	}
	a, objA, err := value(src, srcDate)
	if err != nil {
		return nil, err
	}
	b, _, err := value(target, targetDate)
	if err != nil {
		return nil, err
	}
	if a.Sign() == 0 {
		return nil, crud.Invalid("Bemessung %s von %s ist 0", typ, objA)
	}
	return new(big.Rat).Quo(b, a), nil
}

func (m *Module) mainObject(ctx context.Context, c *contractRow, date string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT object_id FROM contract__object WHERE company_code = ? AND contract_id = ? AND valid_from <= ? AND valid_to >= ?
		ORDER BY is_main DESC, object_id LIMIT 1`, c.CompanyCode, c.ID, date, date)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Vertrag %s hat am %s kein Objekt – Anteil nicht berechenbar", c.ID, date)
	}
	return crud.Str(res.Rows[0][0]), nil
}

// measurementOf: Bemessung eines Objekts (Immobilien) zum Stichtag.
func (m *Module) measurementOf(ctx context.Context, cc, obj, typ, date string) (*big.Rat, error) {
	resp, err := m.services.Call(ctx, "Measurement", "list", map[string]any{"query": map[string]any{
		"company_code": cc, "object_id": obj, "measurement_type": typ, "includeHistory": "true"}})
	if err != nil {
		return nil, unavailable("Immobilien", err)
	}
	var out struct {
		Items []struct {
			Value     any    `json:"value"`
			ValidFrom string `json:"valid_from"`
			ValidTo   string `json:"valid_to"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	for _, it := range out.Items {
		if it.ValidFrom <= date && date <= it.ValidTo {
			r, ok := new(big.Rat).SetString(strings.ReplaceAll(fmt.Sprint(it.Value), ",", "."))
			if ok {
				return r, nil
			}
		}
	}
	return nil, crud.Invalid("Objekt %s hat am %s keine Bemessung %s", obj, date, typ)
}

// scale: Betrag (kleinste Einheit) × Faktor, kaufmännisch gerundet.
func scale(amount int64, f *big.Rat) int64 {
	if f.Cmp(big.NewRat(1, 1)) == 0 {
		return amount
	}
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

func dayBefore(date string) string {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return date
	}
	return t.AddDate(0, 0, -1).Format(time.DateOnly)
}
