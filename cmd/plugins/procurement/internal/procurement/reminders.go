package procurement

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Zahlungserinnerungen (seit 0.18.0) für gebuchte, nicht bezahlte Eingangsrechnungen:
//
//   - Regel an der Rechnungsart: Beschreibung (leer = keine Erinnerung), erste
//     Erinnerung … Tage vor der Fälligkeit (negativ = danach), danach alle … Tage,
//     höchstens … mal (leer = bis bezahlt), nur Rechnungen fällig ab „Erinnern ab“.
//   - Bezahlt ist eine Rechnung, wenn ihr Beleg im Hauptbuch keinen offenen Posten
//     mehr hat (Ausgleich mit der Zahlung); offene Erinnerungen entfallen dann,
//     ebenso bei Storno, geänderter Fälligkeit oder Regel.
//   - Jede Erinnerung eine Zeile ab ihrem Tag; die neuere setzt die ältere auf
//     „wiederholt“. „Erledigt …“ beendet die Erinnerungen dieser Fälligkeit.
//
// Ermittelt wird beim Öffnen der Liste (ohne Hauptbuch keine neuen Erinnerungen).

const (
	reminderObject = "InvoiceReminder"

	remOpen     = "OPEN"
	remDone     = "DONE"
	remRepeated = "REPEATED"
	remObsolete = "OBSOLETE"
)

var reminderStatusOptions = []metamodel.Option{
	{Value: remOpen, Label: "offen"}, {Value: remRepeated, Label: "wiederholt (neuere Erinnerung offen)", Short: "wiederholt"},
	{Value: remDone, Label: "erledigt"}, {Value: remObsolete, Label: "entfallen"}}

// reminderTypeFields: Regel der Zahlungserinnerung an der Rechnungsart.
func reminderTypeFields() []crud.Field {
	const g = "Zahlungserinnerung"
	return []crud.Field{
		{Key: "remind_text", Label: "Beschreibung der Zahlungserinnerung (leer = keine Erinnerung)", Short: "Erinnerung", Type: tText, Group: g},
		{Key: "remind_lead_days", Label: "Erste Erinnerung … Tage vor der Fälligkeit (negativ = danach)", Short: "Tage vorher", Type: tNum, Group: g},
		{Key: "remind_repeat_days", Label: "Danach alle … Tage (leer = einmal)", Short: "Abstand", Type: tNum, Group: g},
		{Key: "remind_max_count", Label: "Höchstens … Erinnerungen (leer = bis bezahlt)", Short: "Anzahl", Type: tNum, Group: g},
		{Key: "remind_from", Label: "Erinnern für Rechnungen fällig ab (leer beim Eintragen = heute)", Short: "Erinnern ab", Type: tDate, Group: g},
	}
}

// checkReminderType: Regel normalisieren.
func checkReminderType(rec crud.Record) error {
	rec["remind_text"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["remind_text"])))
	if rec["remind_text"] == nil {
		return nil
	}
	if toInt(rec["remind_repeat_days"]) < 0 || toInt(rec["remind_max_count"]) < 0 {
		return crud.Invalid("Abstand und Anzahl der Zahlungserinnerung sind nicht negativ")
	}
	if crud.Str(rec["remind_from"]) == "" {
		rec["remind_from"] = time.Now().Format(time.DateOnly)
	}
	return optDate(rec, "remind_from")
}

func (m *Module) reminder() *crud.Entity {
	return &crud.Entity{
		Object: reminderObject, Title: "Zahlungserinnerungen", Icon: "icon-bell", Table: "procurement__invoice_reminder", Section: "Beschaffung",
		Keys:     []string{"company_code", "invoice_id", "due_date", "seq"},
		ReadOnly: true, TitleField: "description",
		Order:   "CASE status WHEN 'OPEN' THEN 0 WHEN 'REPEATED' THEN 1 ELSE 2 END, remind_date DESC, invoice_id",
		Filters: []string{"company_code", "invoice_id", "supplier_id", "invoice_type", "status"},
		Search:  []string{"invoice_id", "description", "detail", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "remind_date", Label: "Erinnern am", Type: tDate, Listable: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, Options: reminderStatusOptions},
			{Key: "invoice_id", Label: "Eingangsrechnung", Short: "Rechnung", Type: tText, Listable: true,
				Lookup: &metamodel.Lookup{Object: invoiceObject, ValueField: "invoice_id", LabelFields: []string{"supplier_reference"}}},
			{Key: "invoice_type", Label: "Rechnungsart", Type: tText, Lookup: catalogLookup("InvoiceType")},
			{Key: "supplier_id", Label: "Lieferant", Type: tText, Listable: true, Lookup: lookupPartner},
			{Key: "due_date", Label: "Fällig am", Short: "Fällig", Type: tDate, Listable: true},
			{Key: "seq", Label: "Erinnerung Nr.", Short: "Nr.", Type: tNum, Listable: true},
			{Key: "description", Label: "Beschreibung (aus der Rechnungsart)", Short: "Beschreibung", Type: tText, Listable: true},
			{Key: "detail", Label: "Einzelheiten", Type: tText, Listable: true},
			{Key: "reason", Label: "Entfallen, weil", Type: tText},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "done_at", Label: "Erledigt am", Type: tText},
			{Key: "done_by", Label: "Erledigt von", Type: tText, Lookup: crud.LookupUser},
		},
		Access: &crud.Access{Object: invoiceObject, Records: true, CompanyCode: "company_code"},
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			m.refreshReminders(ctx)
			return "", nil, false, nil
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "done", Label: "Erledigt …", Record: true, Fields: []string{"note"}}, Handle: m.reminderDoneAction},
			{ActionConfig: metamodel.ActionConfig{Name: "reopen", Label: "Wieder öffnen", Record: true,
				Confirm: "Erinnerung wieder öffnen? Weitere Erinnerungen zu dieser Rechnung folgen dann wieder nach der Regel."}, Handle: m.reminderReopenAction},
		},
	}
}

// reminderDates: Tage der Erinnerungen bis heute (count 0 = ohne Grenze, bis bezahlt).
func reminderDates(due string, lead, repeat, count int64, today string) []string {
	d, err := time.Parse(time.DateOnly, due)
	if err != nil {
		return nil
	}
	var out []string
	for i, t := int64(0), d.AddDate(0, 0, -int(lead)); count == 0 || i < count; i++ {
		s := t.Format(time.DateOnly)
		if s > today {
			break
		}
		out = append(out, s)
		if repeat <= 0 {
			break
		}
		t = t.AddDate(0, 0, int(repeat))
	}
	return out
}

// remKey: Erinnerungen einer Rechnung zu einer Fälligkeit.
type remKey struct{ CC, Invoice, Due string }

const remWhere = `company_code = ? AND invoice_id = ? AND due_date = ?`

func remArgs(k remKey, before ...any) []any { return append(before, k.CC, k.Invoice, k.Due) }

type remCand struct {
	key                 remKey
	typ, supplier, desc string
	lead, repeat, count int64
	detail, reason      string
}

var refreshMu sync.Mutex

// refreshReminders: Fehler landen im Log (die Liste bleibt lesbar).
func (m *Module) refreshReminders(ctx context.Context) {
	if !refreshMu.TryLock() {
		return
	}
	defer refreshMu.Unlock()
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error { return m.refreshRemindersTx(ctx, time.Now().Format(time.DateOnly)) }); err != nil {
		m.log.WarnContext(ctx, "Zahlungserinnerungen nicht ermittelt", "err", err.Error())
	}
}

// openDocs: offener Betrag je Beleg (Jahr|Beleg) auf den Abstimmkonten eines Buchungskreises.
func (m *Module) openDocs(ctx context.Context, cc string) (map[string]int64, error) {
	resp, err := m.services.Call(ctx, "OpenItem", "list", map[string]any{"query": map[string]any{"company_code": cc, "open_only": true}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []struct {
			FY   any    `json:"fiscal_year"`
			Doc  string `json:"document_number"`
			Open any    `json:"open_minor"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	open := map[string]int64{}
	for _, it := range out.Items {
		open[fmt.Sprintf("%d|%s", toInt(it.FY), it.Doc)] += toInt(it.Open)
	}
	return open, nil
}

// reminderCandidates: gebuchte Rechnungen der Rechnungsarten mit Regel.
func (m *Module) reminderCandidates(ctx context.Context) ([]remCand, error) {
	res, err := m.db.Query(ctx, `SELECT i.company_code, i.invoice_id, i.invoice_type, i.supplier_id, i.due_date, i.status, COALESCE(i.document_id, ''),
			COALESCE(i.invoice_amount, 0), i.currency, COALESCE(i.supplier_reference, ''),
			t.remind_text, COALESCE(t.remind_lead_days, 0), COALESCE(t.remind_repeat_days, 0), COALESCE(t.remind_max_count, 0), t.remind_from
		FROM procurement__invoice i JOIN procurement__invoice_type t ON t.company_code = i.company_code AND t.code = i.invoice_type
		WHERE t.remind_text IS NOT NULL AND t.remind_text <> '' AND i.due_date IS NOT NULL AND i.due_date >= t.remind_from
		ORDER BY i.company_code, i.invoice_id`)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	opens := map[string]map[string]int64{}
	decs := map[string]int{}
	var out []remCand
	for _, r := range res.Rows {
		cc, status, doc := crud.Str(r[0]), crud.Str(r[5]), crud.Str(r[6])
		c := remCand{key: remKey{cc, crud.Str(r[1]), dateOf(r[4])}, typ: crud.Str(r[2]), supplier: crud.Str(r[3]),
			desc: crud.Str(r[10]), lead: toInt(r[11]), repeat: toInt(r[12]), count: toInt(r[13])}
		if status != invoicePosted {
			continue // nicht (mehr) gebucht: offene Erinnerungen entfallen
		}
		open, ok := opens[cc]
		if !ok {
			if open, err = m.openDocs(ctx, cc); err != nil {
				return nil, fmt.Errorf("offene Posten: %w", err)
			}
			opens[cc] = open
		}
		p := strings.Split(doc, "|") // <Buchungskreis>|<Jahr>|<Beleg>
		left, isOpen := int64(0), false
		if len(p) == 3 {
			left, isOpen = open[p[1]+"|"+p[2]]
		}
		if !isOpen || left == 0 {
			c.reason = "bezahlt (kein offener Posten im Hauptbuch)"
		}
		cur := crud.Str(r[8])
		d, ok := decs[cur]
		if !ok {
			d = m.currencyDecimals(ctx, cur)
			decs[cur] = d
		}
		if left < 0 {
			left = -left
		}
		c.detail = fmt.Sprintf("fällig %s, offen %s %s", germanDate(c.key.Due), germanAmount(formatAmount(left, d)), cur)
		if total := toInt(r[7]); total != left && total != -left {
			c.detail += fmt.Sprintf(" von %s %s", germanAmount(formatAmount(total, d)), cur)
		}
		if ref := crud.Str(r[9]); ref != "" {
			c.detail += ", Rechnung " + ref
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *Module) refreshRemindersTx(ctx context.Context, today string) error {
	cands, err := m.reminderCandidates(ctx)
	if err != nil {
		return err
	}
	res, err := m.db.Query(ctx, `SELECT company_code, invoice_id, due_date, seq, status FROM procurement__invoice_reminder`)
	if err != nil {
		return err
	}
	type state struct {
		seqs map[int64]string
		done bool
	}
	have := map[remKey]*state{}
	for _, r := range res.Rows {
		k := remKey{crud.Str(r[0]), crud.Str(r[1]), dateOf(r[2])}
		s := have[k]
		if s == nil {
			s = &state{seqs: map[int64]string{}}
			have[k] = s
		}
		s.seqs[toInt(r[3])] = crud.Str(r[4])
		s.done = s.done || crud.Str(r[4]) == remDone
	}
	valid := map[remKey]bool{}
	for _, c := range cands {
		s := have[c.key]
		if c.reason != "" {
			valid[c.key] = true
			if s != nil {
				if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET status = ?, reason = ? WHERE `+remWhere+` AND status IN (?, ?)`,
					append(remArgs(c.key, remObsolete, c.reason), remOpen, remRepeated)...); err != nil {
					return err
				}
			}
			continue
		}
		valid[c.key] = true
		if s != nil && s.done {
			continue
		}
		var last int64
		for i, d := range reminderDates(c.key.Due, c.lead, c.repeat, c.count, today) {
			seq := int64(i + 1)
			last = seq
			if s != nil && s.seqs[seq] != "" {
				// Einzelheiten nachführen (offener Betrag nach Teilzahlung)
				if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET detail = ? WHERE `+remWhere+` AND seq = ? AND status = ?`,
					append(remArgs(c.key, c.detail), seq, remOpen)...); err != nil {
					return err
				}
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO procurement__invoice_reminder (company_code, invoice_id, due_date, seq, remind_date, invoice_type,
				supplier_id, description, detail, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				c.key.CC, c.key.Invoice, c.key.Due, seq, d, c.typ, nilIfEmpty(c.supplier), c.desc, nilIfEmpty(c.detail), remOpen, now()); err != nil {
				return err
			}
		}
		if last > 1 {
			if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET status = ? WHERE `+remWhere+` AND status = ? AND seq < ?`,
				append(remArgs(c.key, remRepeated), remOpen, last)...); err != nil {
				return err
			}
		}
	}
	// Storno, geänderte Fälligkeit, Regel entfernt: offene Erinnerungen entfallen.
	for k, s := range have {
		if valid[k] {
			continue
		}
		for _, st := range s.seqs {
			if st == remOpen || st == remRepeated {
				if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET status = ?, reason = COALESCE(reason, ?) WHERE `+remWhere+` AND status IN (?, ?)`,
					append(remArgs(k, remObsolete, "Rechnung storniert, Fälligkeit oder Regel geändert"), remOpen, remRepeated)...); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (m *Module) reminderOfID(ctx context.Context, id string) (remKey, int64, string, error) {
	key, err := m.set.Entity(reminderObject).ParseID(id)
	if err != nil {
		return remKey{}, 0, "", err
	}
	k := remKey{crud.Str(key["company_code"]), crud.Str(key["invoice_id"]), crud.Str(key["due_date"])}
	seq := toInt(key["seq"])
	res, err := m.db.Query(ctx, `SELECT status FROM procurement__invoice_reminder WHERE `+remWhere+` AND seq = ?`, append(remArgs(k), seq)...)
	if err != nil {
		return remKey{}, 0, "", err
	}
	if len(res.Rows) == 0 {
		return remKey{}, 0, "", crud.Invalid("Zahlungserinnerung %s gibt es nicht", id)
	}
	if err := requireWrite(ctx, invoiceObject, "update", k.CC); err != nil {
		return remKey{}, 0, "", err
	}
	return k, seq, crud.Str(res.Rows[0][0]), nil
}

func (m *Module) reminderDoneAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Note string `json:"note"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	k, seq, status, err := m.reminderOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != remOpen && status != remRepeated {
		return sdk.Response{}, crud.Invalid("Nur offene Erinnerungen lassen sich erledigen")
	}
	_, err = m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET status = ?, done_at = ?, done_by = ?, note = COALESCE(?, note)
		WHERE `+remWhere+` AND (seq = ? OR status IN (?, ?))`,
		append(remArgs(k, remDone, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), nilIfEmpty(strings.TrimSpace(in.Data.Note))),
			seq, remOpen, remRepeated)...)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Erinnerung erledigt – zu dieser Rechnung wird nicht mehr erinnert"}}, nil
}

func (m *Module) reminderReopenAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	k, seq, status, err := m.reminderOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if status != remDone {
		return sdk.Response{}, crud.Invalid("Nur erledigte Erinnerungen lassen sich wieder öffnen")
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE procurement__invoice_reminder SET status = CASE WHEN seq = ? THEN ? ELSE ? END, done_at = NULL, done_by = NULL
			WHERE `+remWhere+` AND status = ?`, append(remArgs(k, seq, remOpen, remRepeated), remDone)...); err != nil {
			return err
		}
		return m.refreshRemindersTx(ctx, time.Now().Format(time.DateOnly))
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Erinnerung wieder offen"}}, nil
}

// germanAmount: "1234.50" → "1.234,50".
func germanAmount(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if hasFrac {
		return sign + b.String() + "," + frac
	}
	return sign + b.String()
}
