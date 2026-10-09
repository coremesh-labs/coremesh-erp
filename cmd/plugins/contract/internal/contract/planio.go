package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Wirtschaftspläne exportieren und importieren (seit 0.14.0):
//
//	console contract:export-plans --company=2000 [--contract=WH-2026-0001] [--from=2026-01-01] --out=plaene.yaml
//	console contract:import-plans --company=2000 --file=plaene.yaml [--dry-run]
//
// Das Dateiformat (YAML; JSON geht beim Import auch) beschreibt die
// Fachobjekte, nicht die Tabellen (format coremesh.contract-plans, version 1)
// – es übersteht Releasewechsel:
//
//	format: coremesh.contract-plans
//	version: 1
//	company_code: "2000"
//	plans:
//	  - contract_id: WH-2026-0001
//	    object_id: RUP0101
//	    period_from: "2026-01-01"
//	    period_to: "2026-12-31"
//	    reference: Rühl WJ 2026
//	    items:
//	      - description: Hausmeister
//	        cost_category: HAUSWART
//	        total_cost: "3300.00"
//	        allocation_key: WFL
//	        key_total: "516.83"
//	        key_share: "52.74"
//	        amount: "336.75"
//
// Beim Import gilt der Vertrag contract_id; gibt es ihn im Buchungskreis nicht,
// der Vertrag mit dem Hauptobjekt object_id zu Beginn des Wirtschaftsjahres
// (contract_type optional). Ein Plan mit Positionen bleibt unverändert, ein
// leerer Plan erhält die Positionen. Geprüft wird wie bei der Erfassung in der
// Oberfläche; Warnungen (z. B. Anteil weicht vom Schlüssel ab) werden gemeldet.

const (
	planFormat        = "coremesh.contract-plans"
	planFormatVersion = 1
)

type planFile struct {
	Format      string     `json:"format" yaml:"format"`
	Version     int        `json:"version" yaml:"version"`
	CompanyCode string     `json:"company_code,omitempty" yaml:"company_code,omitempty"`
	Plans       []planData `json:"plans" yaml:"plans"`
}

type planData struct {
	ContractID   string         `json:"contract_id,omitempty" yaml:"contract_id,omitempty"`
	ContractType string         `json:"contract_type,omitempty" yaml:"contract_type,omitempty"`
	ObjectID     string         `json:"object_id,omitempty" yaml:"object_id,omitempty"`
	PeriodFrom   string         `json:"period_from" yaml:"period_from"`
	PeriodTo     string         `json:"period_to" yaml:"period_to"`
	PlanDate     string         `json:"plan_date,omitempty" yaml:"plan_date,omitempty"`
	Reference    string         `json:"reference,omitempty" yaml:"reference,omitempty"`
	Note         string         `json:"note,omitempty" yaml:"note,omitempty"`
	Items        []planItemData `json:"items" yaml:"items"`
}

type planItemData struct {
	Description   string `json:"description,omitempty" yaml:"description,omitempty"`
	CostCategory  string `json:"cost_category" yaml:"cost_category"`
	TotalCost     string `json:"total_cost,omitempty" yaml:"total_cost,omitempty"`
	AllocationKey string `json:"allocation_key,omitempty" yaml:"allocation_key,omitempty"`
	KeyTotal      string `json:"key_total,omitempty" yaml:"key_total,omitempty"`
	KeyShare      string `json:"key_share,omitempty" yaml:"key_share,omitempty"`
	Amount        string `json:"amount" yaml:"amount"`
	Note          string `json:"note,omitempty" yaml:"note,omitempty"`
}

func (m *Module) registerPlanIO(r interface {
	Command(metamodel.CommandDefinition)
}) {
	r.Command(metamodel.CommandDefinition{Name: "export-plans", Object: setupObject, Action: "exportPlans",
		Description: "Wirtschaftspläne mit allen Positionen als Datei ausgeben (YAML, format coremesh.contract-plans), z. B. --out=plaene.yaml",
		Params: []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "contract", Description: "nur dieser Vertrag"},
			{Name: "from", Description: "nur Wirtschaftsjahre ab diesem Datum (JJJJ-MM-TT)"}}})
	r.Command(metamodel.CommandDefinition{Name: "import-plans", Object: setupObject, Action: "importPlans",
		Description: "Wirtschaftspläne aus einer Datei von export-plans anlegen (vorhandene Pläne mit Positionen bleiben unverändert)",
		Params: []metamodel.CommandParam{{Name: "company", Required: true}, {Name: "file", File: true, Required: true},
			{Name: "dry-run", Type: metamodel.ParamBoolean, Description: "nur prüfen, nichts anlegen"}}})
}

func (m *Module) exportPlansAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company  string `json:"company" yaml:"company"`
		Contract string `json:"contract" yaml:"contract"`
		From     string `json:"from" yaml:"from"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company)
	if err := requireWrite(ctx, "Contract", "list", cc); err != nil {
		return sdk.Response{}, err
	}
	from := "0001-01-01"
	if strings.TrimSpace(in.From) != "" {
		d, err := crud.ParseDate(in.From)
		if err != nil {
			return sdk.Response{}, err
		}
		from = d
	}
	res, err := m.db.Query(ctx, `SELECT p.contract_id, p.period_from, p.period_to, p.plan_date, p.reference, p.note, c.contract_type, c.currency
		FROM contract__plan p JOIN contract__contract c ON c.company_code = p.company_code AND c.contract_id = p.contract_id
		WHERE p.company_code = ? AND (? = '' OR p.contract_id = ?) AND p.period_from >= ? ORDER BY p.contract_id, p.period_from`,
		cc, in.Contract, in.Contract, from)
	if err != nil {
		return sdk.Response{}, err
	}
	out := planFile{Format: planFormat, Version: planFormatVersion, CompanyCode: cc, Plans: []planData{}}
	for _, r := range res.Rows {
		pf, _ := crud.ParseDate(r[1])
		pt, _ := crud.ParseDate(r[2])
		pd, _ := crud.ParseDate(r[3])
		p := planData{ContractID: crud.Str(r[0]), ContractType: crud.Str(r[6]), PeriodFrom: pf, PeriodTo: pt, PlanDate: pd,
			Reference: crud.Str(r[4]), Note: crud.Str(r[5]), Items: []planItemData{}}
		c := &contractRow{CompanyCode: cc, ID: p.ContractID}
		if obj, err := m.mainObject(ctx, c, pf); err == nil {
			p.ObjectID = obj
		}
		dec := m.currencyDecimals(ctx, crud.Str(r[7]))
		amount := func(v any) string {
			if v == nil || crud.Str(v) == "" {
				return ""
			}
			return formatAmount(toInt(v), dec)
		}
		items, err := m.db.Query(ctx, `SELECT description, cost_category, total_cost, allocation_key, key_total, key_share, amount, note
			FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ? AND is_active = ? ORDER BY line_no`,
			cc, p.ContractID, pf, true)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, it := range items.Rows {
			p.Items = append(p.Items, planItemData{Description: crud.Str(it[0]), CostCategory: crud.Str(it[1]), TotalCost: amount(it[2]),
				AllocationKey: crud.Str(it[3]), KeyTotal: crud.Str(it[4]), KeyShare: crud.Str(it[5]), Amount: amount(it[6]), Note: crud.Str(it[7])})
		}
		out.Plans = append(out.Plans, p)
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return sdk.Response{}, err
	}
	n := 0
	for _, p := range out.Plans {
		n += len(p.Items)
	}
	head := fmt.Sprintf("# Wirtschaftspläne Buchungskreis %s – console contract:import-plans --company=%s --file=<datei>\n", cc, cc)
	return sdk.Response{Payload: map[string]any{"file_content": head + string(body),
		"message": fmt.Sprintf("%d Wirtschaftspläne mit %d Positionen", len(out.Plans), n)}}, nil
}

func (m *Module) importPlansAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company string `json:"company" yaml:"company"`
		File    any    `json:"file" yaml:"file"`
		DryRun  any    `json:"dry_run" yaml:"dry_run"`
		DryRun2 any    `json:"dry-run"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company)
	if err := requireWrite(ctx, "Contract", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	dry := strings.EqualFold(fmt.Sprint(in.DryRun), "true") || strings.EqualFold(fmt.Sprint(in.DryRun2), "true")
	var f planFile
	raw, ok := in.File.(string)
	if !ok {
		b, err := json.Marshal(in.File)
		if err != nil {
			return sdk.Response{}, err
		}
		raw = string(b)
	}
	if err := yaml.Unmarshal([]byte(raw), &f); err != nil { // YAML, JSON ist darin enthalten
		return sdk.Response{}, crud.Invalid("Datei: kein YAML im Format %s (%v)", planFormat, err)
	}
	if f.Format != planFormat {
		return sdk.Response{}, crud.Invalid("Datei: format %q erwartet, gefunden %q", planFormat, f.Format)
	}
	if f.Version < 1 || f.Version > planFormatVersion {
		return sdk.Response{}, crud.Invalid("Datei: Version %d wird nicht unterstützt (bis %d) – neueres Release verwenden", f.Version, planFormatVersion)
	}
	counts := map[string]int{"Pläne neu": 0, "Pläne ergänzt": 0, "Pläne vorhanden": 0, "Positionen": 0, "Warnungen": 0}
	var notes []string
	planE, itemE := m.set.Entity(planObject), m.set.Entity(planItemObject)
	for i, p := range f.Plans {
		where := fmt.Sprintf("Plan %d (%s %s)", i+1, orText(p.ContractID, p.ObjectID), p.PeriodFrom)
		id, err := m.planContract(ctx, cc, p)
		if err != nil {
			return sdk.Response{}, crud.Invalid("%s: %v", where, strings.TrimPrefix(err.Error(), sdk.ErrInvalidArgument.Error()+": "))
		}
		from, err := crud.ParseDate(p.PeriodFrom)
		if err != nil {
			return sdk.Response{}, crud.Invalid("%s: period_from: %v", where, err)
		}
		exists := m.text(ctx, `SELECT 1 FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, id, from) != ""
		if exists && m.text(ctx, `SELECT 1 FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, id, from) != "" {
			counts["Pläne vorhanden"]++
			continue
		}
		if dry {
			counts[map[bool]string{true: "Pläne ergänzt", false: "Pläne neu"}[exists]]++
			counts["Positionen"] += len(p.Items)
			continue
		}
		err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
			if !exists {
				data := map[string]any{"company_code": cc, "contract_id": id, "period_from": from, "period_to": p.PeriodTo,
					"plan_date": p.PlanDate, "reference": p.Reference, "note": p.Note}
				if _, err := planE.Create(ctx, map[string]any{"data": data}); err != nil {
					return err
				}
			}
			for j, it := range p.Items {
				data := map[string]any{"company_code": cc, "contract_id": id, "period_from": from, "description": it.Description,
					"cost_category": it.CostCategory, "total_cost": it.TotalCost, "allocation_key": it.AllocationKey,
					"key_total": it.KeyTotal, "key_share": it.KeyShare, "amount": it.Amount, "note": it.Note}
				resp, err := itemE.Create(ctx, map[string]any{"data": data})
				if err != nil {
					return fmt.Errorf("Position %d (%s): %w", j+1, it.CostCategory, err)
				}
				if rec, _ := resp.Payload.(crud.Record); rec != nil {
					if w, _ := rec[crud.WarningsField].([]string); len(w) > 0 {
						counts["Warnungen"]++
						notes = append(notes, fmt.Sprintf("%s, Position %d: %s", where, j+1, w[0]))
					}
				}
			}
			return nil
		})
		if err != nil {
			return sdk.Response{}, crud.Invalid("%s: %v", where, strings.TrimPrefix(err.Error(), sdk.ErrInvalidArgument.Error()+": "))
		}
		counts[map[bool]string{true: "Pläne ergänzt", false: "Pläne neu"}[exists]]++
		counts["Positionen"] += len(p.Items)
	}
	what := "Import"
	if dry {
		what = "Prüfung (nichts angelegt)"
	}
	msg := fmt.Sprintf("%s: %d Pläne neu, %d ergänzt, %d vorhanden (unverändert), %d Positionen, %d Warnungen", what,
		counts["Pläne neu"], counts["Pläne ergänzt"], counts["Pläne vorhanden"], counts["Positionen"], counts["Warnungen"])
	return sdk.Response{Payload: map[string]any{"message": msg, "counts": counts, "warnings": notes}}, nil
}

// planContract: Vertrag des Plans – contract_id, sonst der Vertrag mit dem
// Hauptobjekt object_id zu Beginn des Wirtschaftsjahres.
func (m *Module) planContract(ctx context.Context, cc string, p planData) (string, error) {
	if p.ContractID != "" {
		if _, err := m.contractOf(ctx, cc, p.ContractID); err == nil {
			return p.ContractID, nil
		}
	}
	if p.ObjectID == "" {
		return "", crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht (und kein object_id)", p.ContractID, cc)
	}
	from, err := crud.ParseDate(p.PeriodFrom)
	if err != nil {
		return "", err
	}
	res, err := m.db.Query(ctx, `SELECT o.contract_id FROM contract__object o JOIN contract__contract c ON c.company_code = o.company_code AND c.contract_id = o.contract_id
		WHERE o.company_code = ? AND o.object_id = ? AND o.is_main = ? AND o.valid_from <= ? AND o.valid_to >= ? AND (? = '' OR c.contract_type = ?)
		ORDER BY o.contract_id`, cc, p.ObjectID, true, from, from, p.ContractType, p.ContractType)
	if err != nil {
		return "", err
	}
	switch len(res.Rows) {
	case 0:
		return "", crud.Invalid("kein Vertrag mit Hauptobjekt %s am %s", p.ObjectID, from)
	case 1:
		return crud.Str(res.Rows[0][0]), nil
	}
	return "", crud.Invalid("mehrere Verträge mit Hauptobjekt %s am %s – contract_type oder contract_id angeben", p.ObjectID, from)
}

func orText(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
