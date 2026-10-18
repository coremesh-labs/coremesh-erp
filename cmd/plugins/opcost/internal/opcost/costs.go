package opcost

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Kostenaufstellung (seit 0.6.0): Aktion „Kostenaufstellung …“ am Lauf eines
// Regelwerks der Art COSTS (sammeln ohne Verteilung, z. B. Unterlagen für den
// Messdienst). Die Daten gehen im Format opcost.costs/1 an eine Vorlage des
// Moduls template mit der Verwendung OPCOST_COSTS – ein Schreiben an den
// Empfänger des Regelwerks mit den Kosten je Ausweisgruppe und Konto, den
// Belegen und den Rechnungen aus dem DMS als Anlagen. Keine festen Texte in
// den Daten. Aufbau:
//
//	format, company, date, files [{name, content}]
//	sender (Partner wie beim Abrechnungsschreiben), recipient {persons, street, zip, city, country} (leer = ohne Empfänger)
//	settlement: definition {code, name, mode}, period {from, to}, unit {type, id, designation, street, zip, city}
//	groups [{name, amount, accounts [{code, name, amount, heating, items [{ref, kind, document, date, supplier,
//	    supplier_reference, text, amount, attachment}]}]}]
//	total, attachments [{file, title, document}], missing [{document, reason}]
//
// kind: LEDGER (Einzelposten), INVOICE (Rechnungsposition), INPUT (Eingabe am Lauf).

const (
	usageCosts  = "OPCOST_COSTS"
	costsFormat = "opcost.costs/1"
	// Rechnungen über dieser Größe gehen nicht als Anlage mit (Grenze des Moduls template je Datei).
	maxAttachment = 2 << 20
)

var dateDE = regexp.MustCompile(`\b(\d{2})\.(\d{2})\.(\d{4})\b`)

// costSheetFormState: Vorlage (Verwendung OPCOST_COSTS; Vorschlag: Vorlage
// „Kostenaufstellung“ des Regelwerks), Datum heute.
func (m *Module) costSheetFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	tmpls := reportapi.Templates(ctx, m.services, usageCosts)
	tf := metamodel.FieldState{Options: tmpls}
	if strings.TrimSpace(req.Values["cost_template"]) == "" && len(tmpls) > 0 {
		v := tmpls[0].Value
		if k, err := m.runKeyOf(map[string]any{"id": req.ID}); err == nil {
			if pref := m.text(ctx, `SELECT template FROM opcost__template WHERE company_code = ? AND definition = ? AND usage = 'COST_SHEET' AND is_active = ?
				ORDER BY code`, k.CC, k.Definition, true); pref != "" {
				for _, t := range tmpls {
					if t.Value == pref || strings.HasPrefix(t.Value, strings.TrimSpace(pref)+"|") {
						v = t.Value
						break
					}
				}
			}
		}
		tf.Value = &v
	}
	st.Fields["cost_template"] = tf
	if strings.TrimSpace(req.Values["cost_date"]) == "" {
		today := crud.KeyDate(ctx)
		st.Fields["cost_date"] = metamodel.FieldState{Value: &today}
	}
	if len(tmpls) == 0 {
		st.Message = "Keine Vorlage mit der Verwendung " + usageCosts + " (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt COST_SHEET an)."
	} else {
		st.Message = "Ein Schreiben an den Empfänger des Regelwerks: Kosten je Gruppe, Belege und die Rechnungen aus dem DMS als Anlagen."
	}
	return st, nil
}

func (m *Module) costSheetAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Template string `json:"cost_template"`
			Date     string `json:"cost_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	k, err := m.runKeyOf(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireAccess(ctx, runObject, "costSheet", k.CC); err != nil {
		return sdk.Response{}, err
	}
	data, err := m.costSheetData(ctx, k)
	if err != nil {
		return sdk.Response{}, err
	}
	if strings.TrimSpace(in.Data.Date) != "" {
		d, err := crud.ParseDate(in.Data.Date)
		if err != nil {
			return sdk.Response{}, crud.Invalid("Datum: %v", err)
		}
		data["date"] = d
	}
	out, err := reportapi.Render(ctx, m.services, k.CC, in.Data.Template, "Kostenaufstellung "+k.Definition, data)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

// costItem: ein Beleg eines Kontos.
type costItem struct {
	pool, ref, kind, text string
	amount                float64
}

// costSheetData: Daten opcost.costs/1 eines gerechneten Laufs.
func (m *Module) costSheetData(ctx context.Context, k runKey) (map[string]any, error) {
	def, err := m.set.Entity(definitionObject).Load(ctx, crud.Record{"company_code": k.CC, "code": k.Definition})
	if err != nil {
		return nil, err
	}
	if crud.Str(def["mode"]) != modeCosts {
		return nil, crud.Invalid("Regelwerk %s ist keine Kostenaufstellung (Art der Abrechnung)", k.Definition)
	}
	run, err := m.query(ctx, `SELECT period_to, pool_totals, computed_at, scale FROM opcost__run WHERE company_code = ? AND definition = ? AND period_from = ?`,
		[]string{"period_to", "pool_totals", "computed_at", "scale"}, k.CC, k.Definition, k.From)
	if err != nil {
		return nil, err
	}
	if len(run) == 0 || crud.Str(run[0]["computed_at"]) == "" {
		return nil, crud.Invalid("Lauf ist noch nicht gerechnet")
	}
	to, _ := crud.ParseDate(run[0]["period_to"])
	scale := toInt(run[0]["scale"])
	if scale <= 0 {
		scale = 1
	}
	pools := map[string]int64{}
	if s := crud.Str(run[0]["pool_totals"]); s != "" {
		_ = json.Unmarshal([]byte(s), &pools)
	}
	accs, err := m.query(ctx, `SELECT code, name, display_group, sort_order, heating FROM opcost__account
		WHERE company_code = ? AND definition = ? AND is_active = ? AND on_statement = ? ORDER BY sort_order, code`,
		[]string{"code", "name", "group", "sort", "heating"}, k.CC, k.Definition, true, true)
	if err != nil {
		return nil, err
	}
	journal, err := m.query(ctx, `SELECT debit_account, credit_account, amount, formula, source_ref FROM opcost__journal
		WHERE company_code = ? AND definition = ? AND period_from = ? AND credit_account LIKE 'Q:%' ORDER BY line_no`,
		[]string{"debit", "credit", "amount", "formula", "ref"}, k.CC, k.Definition, k.From)
	if err != nil {
		return nil, err
	}
	var items []costItem
	for _, j := range journal {
		d := crud.Str(j["debit"])
		pool := d[strings.LastIndex(d, ":")+1:]
		kind := strings.SplitN(strings.TrimPrefix(crud.Str(j["credit"]), "Q:"), ":", 2)[0]
		items = append(items, costItem{pool: pool, ref: crud.Str(j["ref"]), kind: kind, text: crud.Str(j["formula"]),
			amount: math.Round(float64(toInt(j["amount"]))/float64(scale)) / 100})
	}

	lb := &letterBuilder{m: m, cc: k.CC, date: to, roles: splitCodes(crud.Str(def["sender_roles"])), sigType: crud.Str(def["signature_doc_type"]),
		partners: map[string]map[string]any{}, files: map[string]reportapi.File{}, objects: map[string]map[string]any{}}
	unitType, unitID := crud.Str(def["unit_type"]), crud.Str(def["unit_id"])
	unit := lb.place(ctx, unitType, unitID)
	unit["type"] = unitType
	company := reportapi.Company(ctx, m.services, k.CC)
	sender := lb.sender(ctx, "", unitID, company)
	var recipient map[string]any
	if id := crud.Str(def["cost_recipient"]); id != "" {
		if p := lb.partner(ctx, id); p != nil {
			recipient = map[string]any{"persons": []any{p}, "street": p["street"], "zip": p["zip"], "city": p["city"]}
			if c := crud.Str(p["country"]); c != "" && c != crud.Str(sender["country"]) {
				recipient["country"] = c
			}
		}
	}

	src := &costSources{m: m, cc: k.CC, from: k.From, to: to, invoices: map[string]map[string]any{}, docInvoice: map[string]string{},
		attached: map[string]string{}, files: lb.files}
	type acc struct {
		code, name string
		heating    bool
		amount     float64
		items      []any
	}
	var groups []string
	byGroup := map[string][]*acc{}
	var total float64
	for _, a := range accs {
		c := crud.Str(a["code"])
		x := &acc{code: c, name: crud.Str(a["name"]), heating: crud.AsBool(a["heating"]), amount: euro(pools[c])}
		for _, it := range items {
			if it.pool == c {
				x.items = append(x.items, src.item(ctx, it))
			}
		}
		g := crud.Str(a["group"])
		if _, ok := byGroup[g]; !ok {
			groups = append(groups, g)
		}
		byGroup[g] = append(byGroup[g], x)
		total += x.amount
	}
	var gl []any
	for _, g := range groups {
		var al []any
		var sum float64
		for _, a := range byGroup[g] {
			al = append(al, map[string]any{"code": a.code, "name": a.name, "heating": a.heating, "amount": a.amount, "items": a.items})
			sum += a.amount
		}
		gl = append(gl, map[string]any{"name": g, "amount": math.Round(sum*100) / 100, "accounts": al})
	}
	var files []any
	for _, f := range lb.files {
		files = append(files, map[string]any{"name": f.Name, "content": f.Content})
	}
	sort.Slice(files, func(i, j int) bool {
		return crud.Str(files[i].(map[string]any)["name"]) < crud.Str(files[j].(map[string]any)["name"])
	})
	return map[string]any{
		"format":    costsFormat,
		"company":   company,
		"files":     files,
		"sender":    sender,
		"recipient": recipient,
		"settlement": map[string]any{
			"definition": map[string]any{"code": k.Definition, "name": crud.Str(def["name"]), "mode": crud.Str(def["mode"])},
			"period":     map[string]any{"from": k.From, "to": to},
			"unit":       unit,
		},
		"groups":      gl,
		"total":       math.Round(total*100) / 100,
		"attachments": src.attachments,
		"missing":     src.missing,
	}, nil
}

// costSources: Belege der Quellen – Einzelposten und Rechnungspositionen mit
// Rechnung, Lieferant und den Rechnungen aus dem DMS (je Rechnung eine Anlage).
type costSources struct {
	m           *Module
	cc          string
	from, to    string
	invoices    map[string]map[string]any // Rechnungsnummer → Kopf
	docInvoice  map[string]string         // Belegnummer im Hauptbuch → Rechnungsnummer
	loaded      bool
	attached    map[string]string // Beleg (Rechnung bzw. Hauptbuch) → Dateiname der Anlage ("" = keine)
	files       map[string]reportapi.File
	attachments []any
	missing     []any
}

func (s *costSources) loadInvoices(ctx context.Context) {
	if s.loaded {
		return
	}
	s.loaded = true
	resp, err := s.m.services.Call(ctx, "SupplierInvoice", "list", map[string]any{"query": map[string]any{"company_code": s.cc}, "limit": 100000})
	if err != nil {
		return
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return
	}
	for _, h := range l.Items {
		id := crud.Str(h["invoice_id"])
		s.invoices[id] = h
		if d := crud.Str(h["document_number"]); d != "" {
			s.docInvoice[d] = id
		}
	}
}

// item: Beleg einer Journalzeile (ref JE:<Beleg>/<Pos.>, INV:<Rechnung>/<Pos.>).
func (s *costSources) item(ctx context.Context, it costItem) map[string]any {
	out := map[string]any{"ref": it.ref, "kind": it.kind, "text": it.text, "amount": it.amount}
	ref := it.ref
	pos := ""
	if i := strings.LastIndex(ref, "/"); i > 0 {
		ref, pos = ref[:i], ref[i+1:]
	}
	var invoice, docNo string
	switch {
	case strings.HasPrefix(ref, "INV:"):
		invoice = strings.TrimPrefix(ref, "INV:")
	case strings.HasPrefix(ref, "JE:"):
		docNo = strings.TrimPrefix(ref, "JE:")
		s.loadInvoices(ctx)
		invoice = s.docInvoice[docNo]
	}
	if m := dateDE.FindStringSubmatch(it.text); m != nil {
		out["date"] = m[3] + "-" + m[2] + "-" + m[1]
	}
	if invoice != "" {
		s.loadInvoices(ctx)
		if h := s.invoices[invoice]; h != nil {
			out["document"] = invoice
			out["date"] = firstNonEmpty(crud.Str(h["invoice_date"]), crud.Str(h["posting_date"]), crud.Str(out["date"]))
			out["supplier"] = reportapi.DisplayName(ctx, s.m.services, crud.Str(h["supplier_id"]))
			out["supplier_reference"] = crud.Str(h["supplier_reference"])
			if t := s.invoiceItemText(ctx, invoice, pos, docNo != ""); t != "" {
				out["text"] = t
			}
			out["attachment"] = s.attach(ctx, invoice, [2]string{"SupplierInvoice", s.cc + "|" + invoice})
			return out
		}
	}
	if docNo != "" {
		out["document"] = docNo
		if t, d := s.ledgerItem(ctx, docNo, pos); t != "" || d != "" {
			out["text"], out["date"] = firstNonEmpty(t, it.text), firstNonEmpty(d, crud.Str(out["date"]))
		}
		out["attachment"] = s.attachJournal(ctx, docNo, crud.Str(out["date"]))
	}
	return out
}

// invoiceItemText: Text der Rechnungsposition; ledger = pos ist die Position im Hauptbuch (Text des Einzelpostens).
func (s *costSources) invoiceItemText(ctx context.Context, invoice, pos string, ledger bool) string {
	if ledger {
		return ""
	}
	resp, err := s.m.services.Call(ctx, "SupplierInvoiceItem", "list", map[string]any{"query": map[string]any{"company_code": s.cc, "invoice_id": invoice}})
	if err != nil {
		return ""
	}
	var l struct {
		Items []struct {
			Line any    `json:"line_no"`
			Text string `json:"item_text"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return ""
	}
	for _, x := range l.Items {
		if strconv.FormatInt(toInt(x.Line), 10) == pos {
			return x.Text
		}
	}
	return ""
}

// ledgerItem: Text und Buchungsdatum eines Einzelpostens.
func (s *costSources) ledgerItem(ctx context.Context, docNo, pos string) (string, string) {
	resp, err := s.m.services.Call(ctx, "JournalEntryItem", "list", map[string]any{"query": map[string]any{"company_code_id": s.cc, "document_number": docNo}})
	if err != nil {
		return "", ""
	}
	var l struct {
		Items []struct {
			Line any    `json:"line_item_number"`
			Text string `json:"item_text"`
			Date string `json:"posting_date"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return "", ""
	}
	for _, x := range l.Items {
		if strconv.FormatInt(toInt(x.Line), 10) == pos {
			d, _ := crud.ParseDate(x.Date)
			return x.Text, d
		}
	}
	return "", ""
}

// attachJournal: Beleg im Hauptbuch ohne Rechnung – DMS-Dokument zum Buchungsbeleg (Geschäftsjahr des Datums bzw. des Zeitraums).
func (s *costSources) attachJournal(ctx context.Context, docNo, date string) string {
	years := []string{}
	if len(date) >= 4 {
		years = append(years, date[:4])
	}
	for _, d := range []string{s.from, s.to} {
		if len(d) >= 4 && !contains(years, d[:4]) {
			years = append(years, d[:4])
		}
	}
	var refs [][2]string
	for _, y := range years {
		refs = append(refs, [2]string{"JournalEntry", s.cc + "|" + y + "|" + docNo})
	}
	return s.attach(ctx, "Beleg "+docNo, refs...)
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// attach: erstes PDF im DMS zu einem der Bezüge (Object, Schlüssel) als Anlage – einmal je Beleg.
func (s *costSources) attach(ctx context.Context, document string, refs ...[2]string) string {
	if f, ok := s.attached[document]; ok {
		return f
	}
	s.attached[document] = ""
	reason := "kein PDF im DMS"
	for _, ref := range refs {
		f, why, err := s.attachRef(ctx, document, ref[0], ref[1])
		if err != nil {
			return "" // ohne DMS: keine Anlagen
		}
		if f != "" {
			s.attached[document] = f
			return f
		}
		if why != "" {
			reason = why
		}
	}
	s.missing = append(s.missing, map[string]any{"document": document, "reason": reason})
	return ""
}

func (s *costSources) attachRef(ctx context.Context, document, object, key string) (file, reason string, err error) {
	resp, err := s.m.services.Call(ctx, "DmsDocument", "list", map[string]any{"query": map[string]any{"company_code": s.cc, "ref_object": object, "ref_key": key}})
	if err != nil {
		return "", "", err
	}
	var l struct {
		Items []struct {
			ID     string `json:"id"`
			Title  string `json:"title"`
			Name   string `json:"file_name"`
			Type   string `json:"content_type"`
			Status string `json:"status"`
			Size   any    `json:"size"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return "", "", nil
	}
	for _, d := range l.Items {
		if d.Status == "REMOVED" || d.Status == "DISCARDED" || d.Type != "application/pdf" {
			continue
		}
		if toInt(d.Size) > maxAttachment {
			reason = fmt.Sprintf("PDF im DMS größer als %d MB", maxAttachment>>20)
			continue
		}
		r, err := s.m.services.Call(ctx, "DmsDocument", "content", map[string]any{"id": d.ID})
		if err != nil {
			continue
		}
		var c struct {
			Content string `json:"content"`
		}
		if sdk.Decode(r.Payload, &c) != nil || c.Content == "" {
			continue
		}
		name := fmt.Sprintf("anlage-%02d.pdf", len(s.attachments)+1)
		s.files[name] = reportapi.File{Name: name, Content: c.Content}
		s.attachments = append(s.attachments, map[string]any{"file": name, "title": firstNonEmpty(d.Title, d.Name), "document": document})
		return name, "", nil
	}
	return "", reason, nil
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}
