package procurement

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Offene Aufträge – Bestellobligo (seit 0.17.0): erteilte Aufträge zum Stichtag mit
// Auftragssumme, schon berechnet (Eingangsrechnungen zum Auftrag bis zum Stichtag, ohne
// stornierte und verworfene) und offen; Zahlung erwartet zum Ende der geplanten Ausführung.
// Eine Auswertung, keine Buchung – der Auftrag ist ein schwebendes Geschäft. Beträge brutto
// wie die Auftragssumme. Gliederung je Auftrag oder zusammengefasst; Ausgabe als Tabelle und CSV.

const obligoObject = "PurchaseOrderObligo"

// Gliederungen der Auswertung.
const (
	obligoByOrder    = "ORDER"
	obligoByObject   = "OBJECT"
	obligoByCategory = "COST_CATEGORY"
	obligoBySupplier = "SUPPLIER"
	obligoByMonth    = "MONTH"
)

var obligoGroups = []metamodel.Option{
	{Value: obligoByOrder, Label: "je Auftrag"},
	{Value: obligoByObject, Label: "je Objekt"},
	{Value: obligoByCategory, Label: "je Kostenart"},
	{Value: obligoBySupplier, Label: "je Lieferant"},
	{Value: obligoByMonth, Label: "je Monat der erwarteten Zahlung"},
}

var obligoDef = metamodel.ObjectDefinition{
	Name: obligoObject, Title: "Offene Aufträge (Obligo)", Icon: "icon-chart", FormState: "formState",
	Lifecycle: metamodel.Lifecycle{Type: metamodel.LifecycleImmutable},
	Fields: []metamodel.FieldDefinition{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Editable: true, ActionOnly: true, Lookup: lookupCC},
		{Key: "key_date", Label: "Stichtag", Type: tDate, Required: true, Editable: true, ActionOnly: true},
		{Key: "group_by", Label: "Gliederung", Type: tSel, Required: true, Editable: true, ActionOnly: true, Options: obligoGroups},
		{Key: "cost_category", Label: "Kostenart (leer = alle)", Type: tText, Editable: true, ActionOnly: true, Lookup: catalogLookup("CostCategory")},
		{Key: "object_id", Label: "Objekt (leer = alle)", Type: tText, Editable: true, ActionOnly: true},
		{Key: "with_drafts", Label: "Entwürfe einbeziehen (noch nicht erteilt)", Type: tBool, Editable: true, ActionOnly: true},
		{Key: "only_open", Label: "Nur Aufträge mit offenem Betrag", Type: tBool, Editable: true, ActionOnly: true},
	},
	Actions: []metamodel.ActionConfig{{Name: "run", Kind: metamodel.KindCustom, Label: "Auswerten", FormState: true,
		Fields: []string{"company_code", "key_date", "group_by", "cost_category", "object_id", "with_drafts", "only_open"}}},
}

func (m *Module) registerObligo(r *module.Router) {
	r.Object(obligoObject).Section("Auswertungen").Describe(obligoDef).
		Handle("run", m.obligoAction).
		Handle("formState", m.obligoFormState)
}

func (m *Module) obligoFormState(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in metamodel.FormStateRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	set := func(k, v string) {
		if strings.TrimSpace(in.Values[k]) == "" {
			st.Fields[k] = metamodel.FieldState{Value: &v}
		}
	}
	set("key_date", time.Now().Format(time.DateOnly))
	set("group_by", obligoByOrder)
	set("only_open", "true")
	return sdk.Response{Payload: st}, nil
}

// obligoRow: ein Auftrag der Auswertung (Beträge in der kleinsten Einheit).
type obligoRow struct {
	ID, Date, Supplier, Description, ObjectType, Object, Category, Due, Status string
	Ordered, Billed                                                            int64
}

func (r obligoRow) open() int64 { return max(r.Ordered-r.Billed, 0) }

func (m *Module) obligoAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			CC       string `json:"company_code"`
			KeyDate  string `json:"key_date"`
			GroupBy  string `json:"group_by"`
			Category string `json:"cost_category"`
			Object   string `json:"object_id"`
			Drafts   any    `json:"with_drafts"`
			OnlyOpen any    `json:"only_open"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	d := in.Data
	cc := strings.TrimSpace(d.CC)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis angeben")
	}
	if ok, err := sdk.CheckAccess(ctx, orderObject, "read", cc); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%w: keine Berechtigung für %s.read im Buchungskreis %s", sdk.ErrPermissionDenied, orderObject, cc)
		}
		return sdk.Response{}, err
	}
	keyDate, err := crud.ParseDate(d.KeyDate)
	if err != nil || keyDate == "" {
		return sdk.Response{}, crud.Invalid("Stichtag: Datum fehlt")
	}
	group := firstNonEmpty(strings.ToUpper(strings.TrimSpace(d.GroupBy)), obligoByOrder)
	if optionLabel(obligoGroups, group) == "" {
		return sdk.Response{}, crud.Invalid("Gliederung %q unbekannt", d.GroupBy)
	}
	rows, err := m.obligoRows(ctx, cc, keyDate, trimUpper(d.Category), strings.TrimSpace(d.Object), crud.AsBool(d.Drafts))
	if err != nil {
		return sdk.Response{}, err
	}
	if crud.AsBool(d.OnlyOpen) {
		kept := rows[:0]
		for _, r := range rows {
			if r.open() > 0 {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	dec := m.currencyDecimals(ctx, m.text(ctx, `SELECT currency FROM procurement__order WHERE company_code = ? AND currency IS NOT NULL LIMIT 1`, cc))
	columns, table := obligoTable(rows, group, dec)
	var total int64
	for _, r := range rows {
		total += r.open()
	}
	msg := fmt.Sprintf("%d Aufträge zum %s, offen %s", len(rows), germanDate(keyDate), formatDE(total, dec))
	return sdk.Response{Payload: map[string]any{"message": msg,
		"table": map[string]any{"columns": columns, "rows": table},
		"download": metamodel.Content{Content: base64.StdEncoding.EncodeToString(csvOf(columns, table)), ContentType: "text/csv; charset=utf-8",
			FileName: fmt.Sprintf("Obligo_%s_%s.csv", cc, keyDate)}}}, nil
}

// obligoRows: Aufträge zum Stichtag mit den Rechnungen bis zum Stichtag.
func (m *Module) obligoRows(ctx context.Context, cc, keyDate, category, object string, drafts bool) ([]obligoRow, error) {
	statuses := []any{orderIssued}
	if drafts {
		statuses = append(statuses, orderDraft)
	}
	q := `SELECT o.order_id, o.order_date, o.supplier_id, o.description, o.object_type, o.object_id, o.cost_category,
		COALESCE(o.planned_to, o.planned_from), o.status, o.amount,
		(SELECT COALESCE(SUM(COALESCE(i.invoice_amount, (SELECT SUM(t.amount) FROM procurement__invoice_item t
			WHERE t.company_code = i.company_code AND t.invoice_id = i.invoice_id AND t.is_active = ?))), 0)
		 FROM procurement__invoice i WHERE i.company_code = o.company_code AND i.order_id = o.order_id
		 AND i.status NOT IN (?, ?) AND i.invoice_date <= ?)
		FROM procurement__order o WHERE o.company_code = ? AND o.order_date <= ? AND o.status IN (` +
		strings.TrimSuffix(strings.Repeat("?, ", len(statuses)), ", ") + `)`
	args := append([]any{true, invoiceCancelled, invoiceDiscarded, keyDate, cc, keyDate}, statuses...)
	if category != "" {
		q += ` AND o.cost_category = ?`
		args = append(args, category)
	}
	if object != "" {
		q += ` AND o.object_id = ?`
		args = append(args, object)
	}
	res, err := m.db.Query(ctx, q+` ORDER BY COALESCE(o.planned_to, o.planned_from, '9999'), o.order_id`, args...)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	name := func(key string, f func() string) string {
		if v, ok := names[key]; ok {
			return v
		}
		names[key] = f()
		return names[key]
	}
	var out []obligoRow
	for _, r := range res.Rows {
		row := obligoRow{ID: crud.Str(r[0]), Date: dateOf(r[1]), Description: crud.Str(r[3]), ObjectType: crud.Str(r[4]),
			Due: dateOf(r[7]), Status: optionLabel(orderStatusOptions, crud.Str(r[8])), Ordered: toInt(r[9]), Billed: toInt(r[10])}
		sup := crud.Str(r[2])
		row.Supplier = withName(sup, name("BP|"+sup, func() string { return m.partnerName(ctx, sup) }))
		if obj := crud.Str(r[5]); obj != "" {
			row.Object = withName(obj, name(row.ObjectType+"|"+obj, func() string { return m.objectDesignation(ctx, cc, row.ObjectType, obj) }))
		}
		if cat := crud.Str(r[6]); cat != "" {
			row.Category = withName(cat, name("CC|"+cat, func() string { return m.costCategoryName(ctx, cc, cat) }))
		}
		out = append(out, row)
	}
	return out, nil
}

// objectDesignation: Bezeichnung des Objekts ("" bei Fehler).
func (m *Module) objectDesignation(ctx context.Context, cc, typ, id string) string {
	if typ == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, typ, "get", map[string]any{"id": cc + "|" + id})
	if err != nil {
		return ""
	}
	return field(resp.Payload, "designation")
}

// withName: „Schlüssel Bezeichnung“ (ohne Bezeichnung nur der Schlüssel).
func withName(key, name string) string {
	if name == "" || name == key {
		return key
	}
	return key + " " + name
}

// obligoTable: Zeilen je Auftrag oder zusammengefasst, mit Summe.
func obligoTable(rows []obligoRow, group string, dec int) ([]string, [][]string) {
	money := func(v int64) string { return formatDE(v, dec) }
	var ordered, billed, open int64
	for _, r := range rows {
		ordered, billed, open = ordered+r.Ordered, billed+r.Billed, open+r.open()
	}
	if group == obligoByOrder {
		columns := []string{"Auftrag", "Datum", "Lieferant", "Leistung", "Objekt", "Kostenart", "Zahlung erwartet", "Status", "Auftragssumme", "Berechnet", "Offen"}
		var out [][]string
		for _, r := range rows {
			status := r.Status
			if r.Billed > r.Ordered {
				status += ", überschritten"
			}
			out = append(out, []string{r.ID, germanDate(r.Date), r.Supplier, r.Description, r.Object, r.Category, germanDate(r.Due), status,
				money(r.Ordered), money(r.Billed), money(r.open())})
		}
		out = append(out, []string{"Summe", "", "", "", "", "", "", "", money(ordered), money(billed), money(open)})
		return columns, out
	}
	keyOf := map[string]func(obligoRow) string{
		obligoByObject:   func(r obligoRow) string { return firstNonEmpty(r.Object, "ohne Objekt") },
		obligoByCategory: func(r obligoRow) string { return firstNonEmpty(r.Category, "ohne Kostenart") },
		obligoBySupplier: func(r obligoRow) string { return r.Supplier },
		obligoByMonth: func(r obligoRow) string {
			if len(r.Due) < 7 {
				return "ohne Termin"
			}
			return r.Due[:7]
		},
	}[group]
	type sum struct {
		n                     int
		ordered, billed, open int64
	}
	sums := map[string]*sum{}
	var keys []string
	for _, r := range rows {
		k := keyOf(r)
		s := sums[k]
		if s == nil {
			s = &sum{}
			sums[k] = s
			keys = append(keys, k)
		}
		s.n, s.ordered, s.billed, s.open = s.n+1, s.ordered+r.Ordered, s.billed+r.Billed, s.open+r.open()
	}
	sort.Strings(keys) // Monat JJJJ-MM: zeitlich; „ohne …“ nach den Ziffern
	out := make([][]string, 0, len(keys)+1)
	for _, k := range keys {
		s, label := sums[k], k
		if group == obligoByMonth && len(k) == 7 && k[4] == '-' {
			label = k[5:] + "/" + k[:4]
		}
		out = append(out, []string{label, fmt.Sprint(s.n), money(s.ordered), money(s.billed), money(s.open)})
	}
	out = append(out, []string{"Summe", fmt.Sprint(len(rows)), money(ordered), money(billed), money(open)})
	head := map[string]string{obligoByObject: "Objekt", obligoByCategory: "Kostenart", obligoBySupplier: "Lieferant", obligoByMonth: "Zahlung erwartet (Monat)"}[group]
	return []string{head, "Aufträge", "Auftragssumme", "Berechnet", "Offen"}, out
}

// csvOf: Tabelle als CSV für die Tabellenkalkulation (Semikolon, UTF-8 mit BOM).
func csvOf(columns []string, rows [][]string) []byte {
	var b strings.Builder
	b.WriteString("\uFEFF")
	line := func(cells []string) {
		for i, c := range cells {
			if i > 0 {
				b.WriteByte(';')
			}
			if strings.ContainsAny(c, ";\"\n") {
				c = `"` + strings.ReplaceAll(c, `"`, `""`) + `"`
			}
			b.WriteString(c)
		}
		b.WriteString("\r\n")
	}
	line(columns)
	for _, r := range rows {
		line(r)
	}
	return []byte(b.String())
}
