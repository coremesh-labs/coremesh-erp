package ledger

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Journal (seit 0.23.0): alle Belege eines Buchungskreises und Ledgers im
// Zeitraum mit ihren Positionen (Soll, Haben), in der gewählten zeitlichen
// Reihenfolge – Buchungsdatum, Belegdatum, Erfassungszeitpunkt – oder nach
// Belegnummer; der Zeitraum gilt für das gewählte Datum. Je Monat die Tage,
// darunter die Belege des Tages (das Datum steht einmal); Summen je Monat und gesamt. Ausgegeben wird über eine Vorlage des Moduls template mit der
// Verwendung JOURNAL (z. B. PDF quer, CSV).

const (
	journalObject = "JournalReport"
	usageJournal  = "JOURNAL"
	maxJournal    = 20000 // Positionen je Ausgabe
)

// Reihenfolgen: Spalte für Zeitraum und Sortierung.
var journalOrders = []metamodel.Option{
	{Value: "POSTING_DATE", Label: "Buchungsdatum"},
	{Value: "DOCUMENT_DATE", Label: "Belegdatum"},
	{Value: "ENTRY", Label: "Erfassungszeitpunkt"},
	{Value: "NUMBER", Label: "Belegnummer (Zeitraum: Buchungsdatum)"},
}

var journalDef = metamodel.ObjectDefinition{
	Name: journalObject, Title: "Journal", Icon: "icon-printer", FormState: "formState",
	Lifecycle: metamodel.Lifecycle{Type: metamodel.LifecycleImmutable},
	Fields: []metamodel.FieldDefinition{
		{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Editable: true, ActionOnly: true, Lookup: lookupCC},
		{Key: "ledger", Label: "Ledger", Type: tText, Required: true, Editable: true, ActionOnly: true,
			Lookup: &metamodel.Lookup{Object: "Ledger", ValueField: "id", LabelFields: []string{"name"}}},
		{Key: "period_from", Label: "Zeitraum von", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "period_to", Label: "Zeitraum bis", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "order", Label: "Reihenfolge", Type: tSel, Required: true, Editable: true, ActionOnly: true, Options: journalOrders},
		{Key: "template", Label: "Vorlage (Ausgabe)", Type: tSel, Required: true, Editable: true, ActionOnly: true},
	},
	Actions: []metamodel.ActionConfig{{Name: "run", Kind: metamodel.KindCustom, Label: "Journal erzeugen", FormState: true,
		Fields: []string{"company_code", "ledger", "period_from", "period_to", "order", "template"}}},
}

func (m *Module) registerJournal(r *module.Router) {
	r.Object(journalObject).Section("Auswertungen").Describe(journalDef).
		Handle("run", m.beforeAction(m.journalAction)).
		Handle("formState", m.journalFormState)
}

func (m *Module) journalFormState(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in metamodel.FormStateRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	set := func(k, v string, opts []metamodel.Option) {
		fs := metamodel.FieldState{Options: opts}
		if strings.TrimSpace(in.Values[k]) == "" && v != "" {
			fs.Value = &v
		}
		if fs.Value != nil || opts != nil {
			st.Fields[k] = fs
		}
	}
	now := time.Now()
	set("period_from", fmt.Sprintf("%d-01-01", now.Year()), nil)
	set("period_to", now.Format(time.DateOnly), nil)
	set("ledger", m.text(ctx, `SELECT id FROM ledger__ledger WHERE is_leading = ? AND is_active = ?`, true, true), nil)
	set("order", "POSTING_DATE", nil)
	tmpls := reportapi.Templates(ctx, m.services, usageJournal)
	first := ""
	if len(tmpls) > 0 {
		first = tmpls[0].Value
		st.Message = "Belege mit Positionen in der gewählten Reihenfolge; der Zeitraum gilt für das gewählte Datum (Erfassung: Tag der Erfassung)."
	} else {
		st.Message = "Keine Vorlage mit der Verwendung JOURNAL (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt PDF und CSV an)."
	}
	set("template", first, tmpls)
	return sdk.Response{Payload: st}, nil
}

func (m *Module) text(ctx context.Context, q string, args ...any) string {
	res, err := m.db.Query(ctx, q, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

type journalFilter struct{ CC, Ledger, From, To, Order string }

// journalData: Belege mit Positionen, Monatsgruppen und Summen.
func (m *Module) journalData(ctx context.Context, f journalFilter) (map[string]any, int, error) {
	dateCol, order := "h.posting_date", "h.posting_date, h.fiscal_year, h.document_number"
	switch f.Order {
	case "DOCUMENT_DATE":
		dateCol, order = "h.document_date", "h.document_date, h.posting_date, h.fiscal_year, h.document_number"
	case "ENTRY":
		dateCol, order = "substr(h.created_at, 1, 10)", "h.created_at, h.fiscal_year, h.document_number"
	case "NUMBER":
		order = "h.fiscal_year, h.document_number"
	}
	res, err := m.db.Query(ctx, `SELECT h.fiscal_year, h.document_number, h.posting_date, h.document_date, h.created_at, h.document_type, h.reference,
		h.header_text, h.created_by, i.line_item_number, i.account_number, i.shkzg, i.amount_local_curr, i.local_currency, i.item_text,
		COALESCE(am.name, ''), i.rent_object_id, i.supplier_id, i.sd_customer_id
		FROM ledger__journal_header h
		JOIN ledger__journal_item i ON i.company_code_id = h.company_code_id AND i.fiscal_year = h.fiscal_year AND i.document_number = h.document_number
		LEFT JOIN ledger__account_master am ON am.chart_of_accounts_id = i.chart_of_accounts_id AND am.account_number = i.account_number
		WHERE h.company_code_id = ? AND i.ledger = ? AND `+dateCol+` >= ? AND `+dateCol+` <= ?
		ORDER BY `+order+`, i.shkzg DESC, i.line_item_number`, f.CC, f.Ledger, f.From, f.To)
	if err != nil {
		return nil, 0, err
	}
	if len(res.Rows) > maxJournal {
		return nil, 0, crud.Invalid("%d Positionen im Zeitraum – bitte den Zeitraum verkürzen (höchstens %d)", len(res.Rows), maxJournal)
	}
	label := map[string]string{}
	for _, o := range journalOrders {
		label[o.Value] = o.Label
	}
	type day struct {
		date          string
		debit, credit float64
		entries       []any
	}
	type group struct {
		key, label    string
		debit, credit float64
		entries       []any
		days          []*day
	}
	var groups []*group
	var entry map[string]any
	var lines []any
	var eDebit, eCredit, tDebit, tCredit float64
	docs := 0
	decimals := map[string]int{}
	major := func(minor int64, cur string) float64 {
		d, ok := decimals[cur]
		if !ok {
			d, _ = m.cur.decimals(ctx, cur)
			decimals[cur] = d
		}
		return float64(minor) / math.Pow10(d)
	}
	flush := func() {
		if entry == nil {
			return
		}
		entry["lines"], entry["debit"], entry["credit"] = lines, round2(eDebit), round2(eCredit)
		g := groups[len(groups)-1]
		g.entries = append(g.entries, entry)
		g.debit, g.credit = g.debit+eDebit, g.credit+eCredit
		if d := entry["date"].(string); len(g.days) == 0 || g.days[len(g.days)-1].date != d {
			g.days = append(g.days, &day{date: d})
		}
		dd := g.days[len(g.days)-1]
		dd.entries = append(dd.entries, entry)
		dd.debit, dd.credit = dd.debit+eDebit, dd.credit+eCredit
		tDebit, tCredit = tDebit+eDebit, tCredit+eCredit
		entry, lines, eDebit, eCredit = nil, nil, 0, 0
	}
	lastDoc := ""
	for _, r := range res.Rows {
		doc := crud.Str(r[0]) + "/" + crud.Str(r[1])
		if doc != lastDoc {
			flush()
			lastDoc = doc
			docs++
			posting, document := dateText(r[2]), dateText(r[3])
			created := crud.Str(r[4])
			day := posting
			switch f.Order {
			case "DOCUMENT_DATE":
				day = document
			case "ENTRY":
				if len(created) >= 10 {
					day = created[:10]
				}
			}
			gk, gl := "", "alle Belege"
			if f.Order != "NUMBER" && len(day) >= 7 {
				gk, gl = day[:7], monthLabel(day)
			}
			if len(groups) == 0 || groups[len(groups)-1].key != gk {
				groups = append(groups, &group{key: gk, label: gl})
			}
			entry = map[string]any{"fiscal_year": crud.Str(r[0]), "document_number": crud.Str(r[1]), "posting_date": posting, "document_date": document,
				"created_at": entryTime(created), "date": day, "document_type": crud.Str(r[5]), "reference": crud.Str(r[6]), "text": crud.Str(r[7]),
				"created_by": crud.Str(r[8])}
		}
		amt := major(toInt(r[12]), crud.Str(r[13]))
		debit, credit := 0.0, 0.0
		if crud.Str(r[11]) == "H" {
			credit = math.Abs(amt)
		} else {
			debit = math.Abs(amt)
		}
		eDebit, eCredit = eDebit+debit, eCredit+credit
		lines = append(lines, map[string]any{"line": crud.Str(r[9]), "account": crud.Str(r[10]), "account_name": crud.Str(r[15]), "side": crud.Str(r[11]),
			"debit": round2(debit), "credit": round2(credit), "text": crud.Str(r[14]), "rent_object_id": crud.Str(r[16]),
			"partner": firstNonEmpty(crud.Str(r[17]), crud.Str(r[18]))})
	}
	flush()
	var gs []any
	for _, g := range groups {
		var ds []any
		for _, d := range g.days {
			ds = append(ds, map[string]any{"date": d.date, "debit": round2(d.debit), "credit": round2(d.credit), "entries": d.entries})
		}
		gs = append(gs, map[string]any{"key": g.key, "label": g.label, "debit": round2(g.debit), "credit": round2(g.credit), "entries": g.entries, "days": ds})
	}
	subtitle := fmt.Sprintf("Buchungskreis %s · Ledger %s · %s %s – %s · Reihenfolge: %s", f.CC, f.Ledger, map[bool]string{true: "Buchungsdatum", false: label[f.Order]}[f.Order == "NUMBER"],
		germanDay(f.From), germanDay(f.To), label[f.Order])
	return map[string]any{"title": "Journal", "subtitle": subtitle, "company": reportapi.Company(ctx, m.services, f.CC),
		"filter": map[string]any{"company_code": f.CC, "ledger": f.Ledger, "from": f.From, "to": f.To, "order": f.Order, "order_label": label[f.Order]},
		"groups": gs, "documents": docs, "lines": len(res.Rows), "debit": round2(tDebit), "credit": round2(tCredit)}, docs, nil
}

func (m *Module) journalAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			CC       string `json:"company_code"`
			Ledger   string `json:"ledger"`
			From     string `json:"period_from"`
			To       string `json:"period_to"`
			Order    string `json:"order"`
			Template string `json:"template"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d := in.Data
	f := journalFilter{CC: strings.TrimSpace(d.CC), Ledger: strings.ToUpper(strings.TrimSpace(d.Ledger)), Order: strings.TrimSpace(d.Order)}
	if f.CC == "" || f.Ledger == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Ledger angeben")
	}
	if f.Order == "" {
		f.Order = "POSTING_DATE"
	}
	if label := optionLabel(journalOrders, f.Order); label == "" {
		return sdk.Response{}, crud.Invalid("Reihenfolge %q unbekannt", f.Order)
	}
	var err error
	if f.From, err = crud.ParseDate(d.From); err != nil || f.From == "" {
		return sdk.Response{}, crud.Invalid("Zeitraum von: Datum fehlt")
	}
	if f.To, err = crud.ParseDate(d.To); err != nil || f.To == "" {
		return sdk.Response{}, crud.Invalid("Zeitraum bis: Datum fehlt")
	}
	if f.To < f.From {
		return sdk.Response{}, crud.Invalid("Zeitraum: „bis“ liegt vor „von“")
	}
	if err := requireRead(ctx, entryObject, f.CC); err != nil {
		return sdk.Response{}, err
	}
	data, docs, err := m.journalData(ctx, f)
	if err != nil {
		return sdk.Response{}, err
	}
	out, err := reportapi.Render(ctx, m.services, f.CC, d.Template, fmt.Sprintf("Journal: %d Belege", docs), data)
	if err != nil {
		return sdk.Response{}, err
	}
	out["table"] = map[string]any{"columns": []string{"Monat", "Belege", "Soll", "Haben"}, "rows": journalRows(data)}
	return sdk.Response{Payload: out}, nil
}

func journalRows(data map[string]any) [][]string {
	var rows [][]string
	gs, _ := data["groups"].([]any)
	for _, g := range gs {
		gm := g.(map[string]any)
		es, _ := gm["entries"].([]any)
		rows = append(rows, []string{crud.Str(gm["label"]), fmt.Sprint(len(es)), reportapi.MoneyDE(gm["debit"].(float64)), reportapi.MoneyDE(gm["credit"].(float64))})
	}
	rows = append(rows, []string{"Summe", fmt.Sprint(data["documents"]), reportapi.MoneyDE(data["debit"].(float64)), reportapi.MoneyDE(data["credit"].(float64))})
	return rows
}

func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return ""
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// dateText: Datum einer Spalte als JJJJ-MM-TT (Treiber liefern Text oder Zeit).
func dateText(v any) string {
	d, _ := crud.ParseDate(v)
	return d
}

// entryTime: Erfassungszeitpunkt (RFC 3339) → „02.01.2006 15:04“ in Ortszeit.
func entryTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("02.01.2006 15:04")
}

func germanDay(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.Format("02.01.2006")
}

var monthNames = []string{"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"}

func monthLabel(day string) string {
	t, err := time.Parse(time.DateOnly, day[:10])
	if err != nil {
		return day[:7]
	}
	return fmt.Sprintf("%s %d", monthNames[t.Month()-1], t.Year())
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
