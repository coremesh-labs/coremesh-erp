package contract

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Wirtschaftsplan am Vertrag (seit 0.13.0), z. B. am Hausgeld-Vertrag der WEG:
//
//   - ContractPlan (Kopf): Wirtschaftsjahr (Zeitraum), Beschluss vom, Nummer;
//     Summen (gesamt, umlagefähig, nicht umlagefähig, Rücklage, monatlich)
//     rechnet das Modul aus den Positionen.
//   - ContractPlanItem: jede Zeile des Plans – Bezeichnung laut Plan, Kostenart
//     (Modul Betriebskosten; deren Art entscheidet umlagefähig, nicht
//     umlagefähig, Rücklage), Gesamtkosten, Verteilerschlüssel mit Gesamt- und
//     Anteilswert, Ihr Anteil. Weicht der Anteil um mehr als die Toleranz der
//     Vertragsart von Gesamtkosten × Anteil / Gesamt ab, gibt es eine Warnung.
//   - „Positionen aus dem Vorjahr übernehmen“: Zeilen des letzten früheren
//     Plans ohne Beträge (Bezeichnung, Kostenart, Schlüssel bleiben).
//
// Die Konditionen (Hausgeld, Rücklage) bleiben die Zahlungsbedingungen; die
// Abrechnung desselben Zeitraums zeigt je Position den Planwert der Kostenart
// (Plan und Ist nebeneinander).

const (
	planObject     = "ContractPlan"
	planItemObject = "ContractPlanItem"

	costAllocable    = "ALLOCABLE"
	costNonAllocable = "NON_ALLOCABLE"
	costReserve      = "RESERVE"
)

var costTypeOptions = []metamodel.Option{{Value: costAllocable, Label: "umlagefähig"}, {Value: costNonAllocable, Label: "nicht umlagefähig"},
	{Value: costReserve, Label: "Rücklage"}}

// planSums: Summenfelder des Kopfs (nicht in der Tabelle).
var planSums = []string{"total_amount", "allocable_amount", "non_allocable_amount", "reserve_amount", "monthly_amount"}

func (m *Module) plan() *crud.Entity {
	match := map[string]string{"company_code": "company_code", "contract_id": "contract_id", "period_from": "period_from"}
	return &crud.Entity{
		Object: planObject, Title: "Wirtschaftspläne", Icon: "icon-calc", Table: "contract__plan", Section: "Abrechnung",
		Keys: []string{"company_code", "contract_id", "period_from"}, Order: "company_code, contract_id, period_from DESC",
		Filters: []string{"company_code", "contract_id"}, TitleField: "reference",
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "period_from", Label: "Wirtschaftsjahr von", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "period_to", Label: "Wirtschaftsjahr bis", Type: tDate, Required: true, Listable: true},
			{Key: "plan_date", Label: "Beschlossen am", Type: tDate, Listable: true},
			{Key: "reference", Label: "Bezeichnung bzw. Nummer des Plans", Type: tText, Listable: true},
			{Key: "total_amount", Label: "Ihr Anteil gesamt", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Group: "Summen"},
			{Key: "allocable_amount", Label: "davon umlagefähig", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Group: "Summen"},
			{Key: "non_allocable_amount", Label: "davon nicht umlagefähig", Type: tText, Virtual: true, ReadOnly: true, Group: "Summen"},
			{Key: "reserve_amount", Label: "davon Erhaltungsrücklage", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Group: "Summen"},
			{Key: "monthly_amount", Label: "monatlich (gesamt / Monate des Wirtschaftsjahres)", Type: tText, Virtual: true, ReadOnly: true, Listable: true, Group: "Summen"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "positionen", Title: "Positionen", Relation: &metamodel.Relation{Object: planItemObject, ForeignKey: "contract_id", Match: match,
				Columns: []string{"line_no", "description", "cost_category", "cost_type", "total_cost", "allocation_key", "key_total", "key_share", "amount"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: m.checkPlan,
		Decorate: m.decoratePlan,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "copyPrevious", Label: "Positionen aus dem Vorjahr übernehmen", Record: true,
				Confirm: "Bezeichnungen, Kostenarten und Schlüssel des letzten Wirtschaftsplans übernehmen (Beträge leer)?"}, Handle: m.copyPreviousPlanAction},
		},
	}
}

func (m *Module) planItem() *crud.Entity {
	return &crud.Entity{
		Object: planItemObject, Title: "Wirtschaftsplanpositionen", Icon: "icon-list", Table: "contract__plan_item", Section: "Abrechnung",
		Keys: []string{"company_code", "contract_id", "period_from", "line_no"}, Order: "company_code, contract_id, period_from, line_no",
		StatusField: "is_active", Filters: []string{"company_code", "contract_id", "period_from", "cost_category", "cost_type"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			{Key: "period_from", Label: "Wirtschaftsplan (Wirtschaftsjahr von)", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "line_no", Label: "Position", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "description", Label: "Bezeichnung laut Plan", Type: tText, Listable: true},
			{Key: "cost_category", Label: "Kostenart", Type: tText, Required: true, Listable: true, Lookup: lookupCostCategory},
			{Key: "cost_type", Label: "umlagefähig (aus der Kostenart)", Type: tSel, ReadOnly: true, Listable: true, Options: costTypeOptions},
			{Key: "total_cost", Label: "Gesamtkosten (z. B. der WEG)", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "allocation_key", Label: "Verteilerschlüssel", Type: tText, Listable: true, Group: "Verteilung", Lookup: lookupAllocationKey},
			{Key: "key_total", Label: "Schlüssel gesamt", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "key_share", Label: "Schlüssel Anteil", Type: tText, Listable: true, Group: "Verteilung"},
			{Key: "amount", Label: "Ihr Anteil", Type: tText, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "is_active", Label: "Aktiv", Type: tBool, ReadOnly: true},
		},
		Access:   &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Prepare:  m.preparePlanItem,
		Validate: m.checkPlanItem,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			return m.decorateAmounts(ctx, rec, "total_cost", "amount")
		},
	}
}

func (m *Module) checkPlan(ctx context.Context, rec, _ crud.Record) error {
	if _, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])); err != nil {
		return err
	}
	for _, k := range []string{"period_from", "period_to", "plan_date"} {
		if k == "plan_date" && crud.Str(rec[k]) == "" {
			rec[k] = nil
			continue
		}
		d, err := crud.ParseDate(rec[k])
		if err != nil {
			return err
		}
		rec[k] = d
	}
	if crud.Str(rec["period_to"]) < crud.Str(rec["period_from"]) {
		return crud.Invalid("Wirtschaftsjahr: bis liegt vor von")
	}
	rec["changed_at"] = now()
	return nil
}

// decoratePlan: Summen der aktiven Positionen nach Art der Kostenart.
func (m *Module) decoratePlan(ctx context.Context, rec crud.Record) error {
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return nil
	}
	res, err := m.db.Query(ctx, `SELECT cost_type, SUM(amount) FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ?
		AND is_active = ? GROUP BY cost_type`, rec["company_code"], rec["contract_id"], from, true)
	if err != nil {
		return nil
	}
	var total int64
	sums := map[string]int64{}
	for _, r := range res.Rows {
		sums[crud.Str(r[0])] = toInt(r[1])
		total += toInt(r[1])
	}
	rec["total_amount"], rec["allocable_amount"] = total, sums[costAllocable]
	rec["non_allocable_amount"], rec["reserve_amount"] = sums[costNonAllocable], sums[costReserve]
	if n := months(crud.Str(rec["period_from"]), crud.Str(rec["period_to"])); n > 0 {
		rec["monthly_amount"] = scale(total, big.NewRat(1, int64(n)))
	}
	return m.decorateAmounts(ctx, rec, planSums...)
}

// months: angefangene Kalendermonate von from bis to (beide einschließlich).
func months(from, to string) int {
	a, err1 := time.Parse(time.DateOnly, from)
	b, err2 := time.Parse(time.DateOnly, to)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return (b.Year()-a.Year())*12 + int(b.Month()) - int(a.Month()) + 1
}

func (m *Module) preparePlanItem(ctx context.Context, rec crud.Record) error {
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return err
	}
	rec["period_from"] = from
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1 FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
		rec["company_code"], rec["contract_id"], from)
	if err != nil {
		return err
	}
	rec["line_no"] = toInt(res.Rows[0][0])
	return nil
}

func (m *Module) checkPlanItem(ctx context.Context, rec, _ crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	from, err := crud.ParseDate(rec["period_from"])
	if err != nil {
		return err
	}
	rec["period_from"] = from
	if m.text(ctx, `SELECT 1 FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, id, from) == "" {
		return crud.Invalid("Wirtschaftsplan %s ab %s gibt es nicht", id, from)
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	cat := trimUpper(rec["cost_category"])
	_, typ, err := m.costCategory(ctx, cc, cat)
	if err != nil {
		return err
	}
	rec["cost_category"], rec["cost_type"] = cat, typ
	decimals := m.currencyDecimals(ctx, c.Currency)
	if err := minorField(rec, "amount", decimals, true); err != nil {
		return err
	}
	if err := minorField(rec, "total_cost", decimals, false); err != nil {
		return err
	}
	for _, k := range []string{"key_total", "key_share"} {
		v := strings.ReplaceAll(strings.TrimSpace(crud.Str(rec[k])), ",", ".")
		if v != "" && (!decimalRe.MatchString(v) || strings.HasPrefix(v, "-")) {
			return crud.Invalid("%s: Zahl ≥ 0 erwartet (z. B. 85,32)", k)
		}
		rec[k] = nilIfEmpty(v)
	}
	if rec["key_total"] != nil && strings.Trim(crud.Str(rec["key_total"]), "0.") == "" {
		return crud.Invalid("Schlüssel gesamt darf nicht 0 sein")
	}
	rec["allocation_key"] = nilIfEmpty(trimUpper(rec["allocation_key"]))
	rec["description"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["description"])))
	defaults(rec, map[string]any{"is_active": true})
	m.checkPlanShare(ctx, rec, c, decimals)
	return nil
}

// checkPlanShare warnt, wenn Ihr Anteil von Gesamtkosten × Anteil / Gesamt um
// mehr als die Toleranz der Vertragsart abweicht.
func (m *Module) checkPlanShare(ctx context.Context, rec crud.Record, c *contractRow, decimals int) {
	if rec["total_cost"] == nil || rec["key_total"] == nil || rec["key_share"] == nil {
		return
	}
	kt, ok1 := new(big.Rat).SetString(crud.Str(rec["key_total"]))
	ks, ok2 := new(big.Rat).SetString(crud.Str(rec["key_share"]))
	if !ok1 || !ok2 || kt.Sign() == 0 {
		return
	}
	want := scale(toInt(rec["total_cost"]), new(big.Rat).Quo(ks, kt))
	tol, _ := parseAmount(m.text(ctx, `SELECT settlement_tolerance FROM contract__contract_type WHERE company_code = ? AND code = ?`,
		c.CompanyCode, c.Type), decimals)
	if d := want - toInt(rec["amount"]); d > tol || -d > tol {
		crud.Warn(ctx, "Ihr Anteil %s weicht vom Schlüssel ab: Gesamtkosten × %s / %s = %s", formatAmount(toInt(rec["amount"]), decimals),
			crud.Str(rec["key_share"]), crud.Str(rec["key_total"]), formatAmount(want, decimals))
	}
}

// copyPreviousPlanAction: Positionen des letzten früheren Plans des Vertrags
// übernehmen – ohne Beträge (Gesamtkosten und Anteil 0).
func (m *Module) copyPreviousPlanAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(planObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	from, _ := crud.ParseDate(key["period_from"])
	if err := requireWrite(ctx, "Contract", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	if m.text(ctx, `SELECT 1 FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from = ?`, cc, id, from) == "" {
		return sdk.Response{}, crud.Invalid("Wirtschaftsplan %s ab %s gibt es nicht", id, from)
	}
	prev := m.text(ctx, `SELECT MAX(period_from) FROM contract__plan WHERE company_code = ? AND contract_id = ? AND period_from < ?`, cc, id, from)
	if prev == "" {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keinen früheren Wirtschaftsplan", id)
	}
	prev, _ = crud.ParseDate(prev)
	var n int64
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		base := toInt(m.text(ctx, `SELECT COALESCE(MAX(line_no), 0) FROM contract__plan_item WHERE company_code = ? AND contract_id = ? AND period_from = ?`,
			cc, id, from))
		res, err := m.db.Exec(ctx, `INSERT INTO contract__plan_item (company_code, contract_id, period_from, line_no, description, cost_category,
			cost_type, total_cost, allocation_key, key_total, key_share, amount, note, is_active)
			SELECT company_code, contract_id, ?, line_no + ?, description, cost_category, cost_type, 0, allocation_key, key_total, key_share, 0,
			note, is_active FROM contract__plan_item
			WHERE company_code = ? AND contract_id = ? AND period_from = ? AND is_active = ?`, from, base, cc, id, prev, true)
		if err != nil {
			return err
		}
		n = res.RowsAffected
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "copied": n,
		"message": fmt.Sprintf("%d Positionen aus dem Wirtschaftsplan ab %s übernommen – Gesamtkosten und Anteile eintragen", n, germanDate(prev))}}, nil
}

// planAmount: Planwert (Ihr Anteil) einer Kostenart (leer = aller Positionen)
// im Wirtschaftsplan, der am Beginn des Abrechnungszeitraums beginnt; ok =
// false ohne Plan.
func (m *Module) planAmount(ctx context.Context, cc, id, from, category string) (amount int64, ok bool) {
	res, err := m.db.Query(ctx, `SELECT SUM(i.amount), COUNT(p.period_from) FROM contract__plan p
		LEFT JOIN contract__plan_item i ON i.company_code = p.company_code AND i.contract_id = p.contract_id AND i.period_from = p.period_from
		  AND (i.cost_category = ? OR ? = '') AND i.is_active = ?
		WHERE p.company_code = ? AND p.contract_id = ? AND p.period_from = ?`, category, category, true, cc, id, from)
	if err != nil || len(res.Rows) == 0 || toInt(res.Rows[0][1]) == 0 {
		return 0, false
	}
	return toInt(res.Rows[0][0]), true
}
