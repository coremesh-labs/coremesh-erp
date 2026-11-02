package contract

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh-erp/pkg/banktransfer"
	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Überweisung / Dauerauftrag (seit 0.52.0): an Verträgen, die wir bezahlen (Richtung
// PAYABLE), ohne dass Rechnungen kommen – Grundsteuer, Abschläge, Hausgeld, Versicherung.
// Das Fenster zeigt wie an der Eingangsrechnung Empfänger, IBAN, BIC, Betrag und
// Verwendungszweck mit Kopier-Zeichen (Paket banktransfer), dazu Rhythmus, Ausführungstag,
// erste Ausführung und – wenn der Betrag sich ändert – ab wann und auf welchen Betrag; so
// lässt sich ein Dauerauftrag einrichten oder anpassen. In Euro dazu ein GiroCode.
//
// Betrag: die am Stichtag (leer = heute) gültigen Konditionen mit Rhythmus (nicht einmalig)
// samt nachgelagerten Zuschlägen (z. B. Umsatzsteuer), je Rhythmus und Fälligkeit summiert.
// Verwendungszweck nach der Vorlage der Vertragsart (transfer_purpose; Platzhalter
// {external} Vertragsnummer des Partners bzw. Kassenzeichen, {customer} Kundennummer,
// {contract} eigene Vertragsnummer, {designation} Bezeichnung, {mandate} Mandatsreferenz;
// [Text] entfällt, wenn ein Platzhalter darin leer ist; leer = defaultContractPurpose).

const (
	transferAction         = "transfer"
	defaultContractPurpose = "{external}[ Kunde {customer}]"
)

var monthsPer = map[string]int{"MONTHLY": 1, "BIMONTHLY": 2, "QUARTERLY": 3, "HALF_YEARLY": 6, "YEARLY": 12}

// payGroup: Zahlungen gleichen Rhythmus und gleicher Fälligkeit.
type payGroup struct {
	Frequency, Mode              string
	DueDay, DueMonth, StartMonth int
	Amount                       int64
	ValidFrom, ValidTo           string // gemeinsame Gültigkeit der Konditionen
}

func (g payGroup) key() string {
	return fmt.Sprint(g.Frequency, g.DueDay, g.DueMonth, g.StartMonth, g.Mode)
}

func (m *Module) transferAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	c, err := m.contractOfID(ctx, crud.Str(p["id"]))
	if err != nil {
		return sdk.Response{}, err
	}
	if c.Direction != dirPayable {
		return sdk.Response{}, crud.Invalid("Vertrag %s: Überweisung nur an Verträgen, die wir bezahlen (Richtung „zu zahlen“)", c.ID)
	}
	data, _ := p["data"].(map[string]any)
	day, _ := crud.ParseDate(data["transfer_date"])
	if day == "" {
		day = time.Now().Format(time.DateOnly)
	}
	if c.ValidTo != "" && day > c.ValidTo {
		return sdk.Response{}, crud.Invalid("Vertrag %s endet am %s – nichts mehr zu überweisen", c.ID, germanDate(c.ValidTo))
	}
	if c.ValidFrom > day {
		day = c.ValidFrom
	}
	groups, dec, err := m.payGroups(ctx, c, day)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(groups) == 0 {
		return sdk.Response{}, crud.Invalid("Vertrag %s: am %s keine laufende Zahlung (Konditionen mit Rhythmus)", c.ID, germanDate(day))
	}
	bank, err := banktransfer.PartnerBank(ctx, m.services, c.Partner, day, "Vertragspartner")
	if err != nil {
		return sdk.Response{}, err
	}
	payee := firstNonEmpty(bank.Holder, banktransfer.PayeeName(ctx, m.services, c.Partner))
	cur := firstNonEmpty(c.Currency, "EUR")
	g := groups[0]
	extra := []banktransfer.Field{{Label: "Rhythmus", Value: frequencyText(g.Frequency)}}
	if d := dueDayText(g); d != "" {
		extra = append(extra, banktransfer.Field{Label: "Ausführung", Value: d})
	}
	first := nextDue(g, day)
	extra = append(extra, banktransfer.Field{Label: "Erste Ausführung", Value: germanDate(first)})
	if c.ValidTo != "" && c.ValidTo < "9999-01-01" {
		extra = append(extra, banktransfer.Field{Label: "Letzte Ausführung bis", Value: germanDate(c.ValidTo)})
	}
	// Betrag ändert sich: Folgebetrag ab dem Tag nach dem Ende der Konditionen
	if g.ValidTo != "" && g.ValidTo < "9999-01-01" && (c.ValidTo == "" || g.ValidTo < c.ValidTo) {
		next := addDays(g.ValidTo, 1)
		if ng, _, err := m.payGroups(ctx, c, next); err == nil {
			amount := int64(0)
			for _, x := range ng {
				if x.key() == g.key() {
					amount = x.Amount
				}
			}
			extra = append(extra, banktransfer.Field{Label: "Ab " + germanDate(next),
				Value: pricing.FormatDE(formatAmount(amount, dec)) + " " + cur + " (Dauerauftrag anpassen)", Copy: strings.ReplaceAll(formatAmount(amount, dec), ".", ",")})
		}
	}
	for _, o := range groups[1:] {
		extra = append(extra, banktransfer.Field{Label: "Weitere Zahlung (" + frequencyText(o.Frequency) + ")",
			Value: pricing.FormatDE(formatAmount(o.Amount, dec)) + " " + cur + ", erste am " + germanDate(nextDue(o, day)), Copy: strings.ReplaceAll(formatAmount(o.Amount, dec), ".", ",")})
	}
	msg := fmt.Sprintf("Vertrag %s (%s) – %s %s, Stand %s. Werte für die Überweisung bzw. den Dauerauftrag kopieren; der GiroCode füllt eine Überweisung, den Rhythmus in der Banking-App einstellen.",
		c.ID, c.Designation, frequencyText(g.Frequency), pricing.FormatDE(formatAmount(g.Amount, dec))+" "+cur, germanDate(day))
	if pm := m.text(ctx, `SELECT w.name FROM contract__contract k JOIN contract__payment_method w ON w.company_code = k.company_code AND w.code = k.payment_method
		WHERE k.company_code = ? AND k.contract_id = ? AND w.uses_mandate`, c.CompanyCode, c.ID); pm != "" {
		msg = "Hinweis: Zahlweg „" + pm + "“ – der Partner bucht selbst ab. " + msg
	}
	return sdk.Response{Payload: banktransfer.Payload(payee, bank, formatAmount(g.Amount, dec), cur, m.contractPurpose(ctx, c), msg, extra...)}, nil
}

// payGroups: laufende Zahlungen am Stichtag, je Rhythmus und Fälligkeit (häufigste zuerst).
func (m *Module) payGroups(ctx context.Context, c *contractRow, day string) ([]payGroup, int, error) {
	pos, deps, dec, err := m.positions(ctx, c, day)
	if err != nil {
		return nil, dec, err
	}
	res, err := m.db.Query(ctx, `SELECT condition_type, COALESCE(object_id, ''), position, frequency, due_day, due_month, period_start_month, payment_mode, valid_from, valid_to
		FROM contract__condition WHERE company_code = ? AND contract_id = ? AND valid_from <= ? AND valid_to >= ?`, c.CompanyCode, c.ID, day, day)
	if err != nil {
		return nil, dec, err
	}
	type cond struct {
		g        payGroup
		from, to string
	}
	byKey := map[string]cond{}
	for _, r := range res.Rows {
		from, _ := crud.ParseDate(r[8])
		to, _ := crud.ParseDate(r[9])
		byKey[fmt.Sprint(crud.Str(r[0]), "|", crud.Str(r[1]), "|", toInt(r[2]))] = cond{g: payGroup{Frequency: crud.Str(r[3]), DueDay: int(toInt(r[4])),
			DueMonth: int(toInt(r[5])), StartMonth: int(toInt(r[6])), Mode: crud.Str(r[7])}, from: from, to: to}
	}
	groups := map[string]*payGroup{}
	var order []string
	lineGroup := map[string]string{}
	for i, p := range pos {
		k, ok := byKey[fmt.Sprint(p.ConditionType, "|", p.Object, "|", p.Position)]
		if !ok || monthsPer[k.g.Frequency] == 0 {
			continue // einmalig
		}
		gk := k.g.key()
		g := groups[gk]
		if g == nil {
			ng := k.g
			ng.ValidFrom, ng.ValidTo = k.from, k.to
			g = &ng
			groups[gk] = g
			order = append(order, gk)
		}
		g.Amount += p.Amount
		if k.from > g.ValidFrom {
			g.ValidFrom = k.from
		}
		if k.to < g.ValidTo {
			g.ValidTo = k.to
		}
		lineGroup[fmt.Sprint(i)] = gk
	}
	// nachgelagerte Zuschläge (z. B. Umsatzsteuer) zur Gruppe ihrer Basis
	for _, r := range deps {
		if r.Dependent.Effect == "SHARE" {
			continue // im Betrag enthalten
		}
		if len(r.Shares) > 0 {
			for line, a := range r.Shares {
				if g := groups[lineGroup[line]]; g != nil {
					g.Amount += a
				}
			}
		} else if len(order) > 0 {
			groups[order[0]].Amount += r.Amount
		}
	}
	var out []payGroup
	for _, k := range order {
		if groups[k].Amount > 0 {
			out = append(out, *groups[k])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return monthsPer[out[i].Frequency] < monthsPer[out[j].Frequency] })
	return out, dec, nil
}

// nextDue: erste Fälligkeit am oder nach day (Monat der Periode laut due_month bzw.
// nachschüssig der letzte; Tag höchstens Monatsende).
func nextDue(g payGroup, day string) string {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return day
	}
	per := monthsPer[g.Frequency]
	start := max(g.StartMonth, 1)
	off := max(g.DueMonth, 1) - 1
	if g.Mode == "IN_ARREARS" && g.DueMonth == 0 {
		off = per - 1
	}
	if from, err := time.Parse(time.DateOnly, g.ValidFrom); err == nil && from.After(d) {
		d = from
	}
	for i := 0; i < 400; i++ {
		mo := time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, i, 0)
		if ((int(mo.Month())-start)%per+per)%per != off {
			continue
		}
		last := mo.AddDate(0, 1, -1).Day()
		due := time.Date(mo.Year(), mo.Month(), min(max(g.DueDay, 1), last), 0, 0, 0, 0, time.UTC)
		if !due.Before(d) {
			return due.Format(time.DateOnly)
		}
	}
	return day
}

func dueDayText(g payGroup) string {
	day := max(g.DueDay, 1)
	if monthsPer[g.Frequency] == 1 {
		return fmt.Sprintf("am %d. jedes Monats", day)
	}
	return fmt.Sprintf("am %d. des Fälligkeitsmonats", day)
}

func frequencyText(f string) string {
	for _, o := range frequencyOptions {
		if o.Value == f {
			return o.Label
		}
	}
	return f
}

func addDays(day string, n int) string {
	d, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return day
	}
	return d.AddDate(0, 0, n).Format(time.DateOnly)
}

// contractPurpose: Verwendungszweck nach der Vorlage der Vertragsart (leer = Bezeichnung).
func (m *Module) contractPurpose(ctx context.Context, c *contractRow) string {
	tpl := firstNonEmpty(strings.TrimSpace(m.text(ctx, `SELECT transfer_purpose FROM contract__contract_type WHERE company_code = ? AND code = ?`,
		c.CompanyCode, c.Type)), defaultContractPurpose)
	res, err := m.db.Query(ctx, `SELECT external_number, customer_number, mandate_ref FROM contract__contract WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID)
	vals := map[string]string{"contract": c.ID, "designation": c.Designation, "external": "", "customer": "", "mandate": ""}
	if err == nil && len(res.Rows) > 0 {
		vals["external"], vals["customer"], vals["mandate"] = strings.TrimSpace(crud.Str(res.Rows[0][0])), strings.TrimSpace(crud.Str(res.Rows[0][1])), strings.TrimSpace(crud.Str(res.Rows[0][2]))
	}
	return firstNonEmpty(banktransfer.Purpose(tpl, vals), c.Designation)
}
