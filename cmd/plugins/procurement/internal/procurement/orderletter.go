package procurement

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math/big"
	"mime"
	"mime/multipart"
	"net/textproto"
	"regexp"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/pricing"
	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// Auftragsschreiben (Datenformat procurement.order/1) über eine Vorlage der
// Verwendung PURCHASE_ORDER; Texte in Markdown (die Vorlage setzt sie mit mdtex),
// Platzhalter vorher eingesetzt:
//
//	{auftrag:feld}    Feld des Auftrags (Datum TT.MM.JJJJ), number = Auftragsnummer
//	{angebote}        Angebote des Lieferanten, z. B. „AN-2026-00003 und AN-2026-00004 vom 07.10.2026“
//	{angebot:feld}    Feld des ersten Angebots (supplier_reference, quote_date, description)
//	{objekt:feld}     text (Objektangabe), designation, street, zip, city, building, floor, location
//	{lieferant:feld}  name, street, zip, city, Kommunikationskategorie (z. B. EMAIL)
//	{absender:feld}   wie lieferant, für den Absender
//	{summe:netto}     netto, steuer, brutto (deutsch, mit Nachkommastellen der Währung)
//	{kontakt:feld}    nur im Satz einer Kontaktrolle: name, person, addition, street, zip, city, Kategorie
//
// Unbekannte Quellen sind beim Speichern ein Fehler, fehlende Werte beim Erzeugen –
// das Schreiben bleibt sonst unvollständig, ohne dass es auffällt.

const orderFormat = "procurement.order/1"

var placeholderRe = regexp.MustCompile(`\{([a-z]+)(?::([^{}]+))?\}`)

var placeholderSources = map[string]bool{"auftrag": true, "angebote": true, "angebot": true, "objekt": true, "lieferant": true, "absender": true, "summe": true}

// checkPlaceholders: nur bekannte Quellen ({kontakt:…} nur im Satz einer Kontaktrolle).
func checkPlaceholders(s string, contact bool) error {
	for _, p := range placeholderRe.FindAllStringSubmatch(s, -1) {
		if placeholderSources[p[1]] || (contact && p[1] == "kontakt") {
			if p[1] != "angebote" && strings.TrimSpace(p[2]) == "" {
				return crud.Invalid("Platzhalter %s: Feld fehlt (z. B. {%s:name})", p[0], p[1])
			}
			continue
		}
		known := "auftrag, angebote, angebot, objekt, lieferant, absender, summe"
		if contact {
			known += ", kontakt"
		}
		return crud.Invalid("Platzhalter %s: Quelle unbekannt (%s)", p[0], known)
	}
	return nil
}

// orderLetter: gesammelte Daten eines Auftrags für Platzhalter und Vorlage.
type orderLetter struct {
	m        *Module
	cc, id   string
	rec      crud.Record
	typ      crud.Record
	dec      int
	currency string
	supplier map[string]any
	sender   map[string]any
	object   map[string]any
	quotes   []map[string]any
	net, tax int64
	files    []reportapi.File
	contact  map[string]any // Kontakt beim Einsetzen des Satzes einer Rolle
}

func (m *Module) newOrderLetter(ctx context.Context, cc, id string) (*orderLetter, error) {
	rec, err := m.set.Entity(orderObject).Load(ctx, crud.Record{"company_code": cc, "order_id": id})
	if err != nil {
		return nil, err
	}
	typ, err := m.set.Entity(orderTypeObject).Load(ctx, crud.Record{"company_code": cc, "code": crud.Str(rec["order_type"])})
	if err != nil {
		return nil, crud.Invalid("Auftragsart %s: %v", crud.Str(rec["order_type"]), err)
	}
	l := &orderLetter{m: m, cc: cc, id: id, rec: rec, typ: typ, currency: crud.Str(rec["currency"])}
	l.dec = m.currencyDecimals(ctx, l.currency)
	l.supplier = reportapi.Partner(ctx, m.services, crud.Str(rec["supplier_id"]))
	if l.supplier == nil {
		return nil, crud.Invalid("Lieferant %s nicht lesbar", crud.Str(rec["supplier_id"]))
	}
	l.object = m.orderObjectData(ctx, cc, crud.Str(rec["object_type"]), crud.Str(rec["object_id"]), crud.Str(rec["object_text"]))
	res, err := m.db.Query(ctx, `SELECT q.quote_id, q.supplier_reference, q.quote_date, q.description FROM procurement__order_quote o
		JOIN procurement__quote q ON q.company_code = o.company_code AND q.quote_id = o.quote_id
		WHERE o.company_code = ? AND o.order_id = ? ORDER BY q.quote_date, q.quote_id`, cc, id)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		l.quotes = append(l.quotes, map[string]any{"id": crud.Str(r[0]), "reference": firstNonEmpty(crud.Str(r[1]), crud.Str(r[0])),
			"supplier_reference": crud.Str(r[1]), "date": dateOf(r[2]), "quote_date": dateOf(r[2]), "description": crud.Str(r[3])})
	}
	sums, err := m.db.Query(ctx, `SELECT COALESCE(SUM(CASE WHEN dependent_type IS NULL THEN amount ELSE 0 END), 0) FROM procurement__order_item
		WHERE company_code = ? AND order_id = ? AND is_active = ?`, cc, id, true)
	if err != nil {
		return nil, err
	}
	l.net = toInt(sums.Rows[0][0])
	if crud.AsBool(rec["net_amounts"]) {
		l.tax = toInt(rec["amount"]) - l.net
	} else {
		l.net = toInt(rec["amount"])
	}
	l.sender = m.orderSender(ctx, l)
	return l, nil
}

// orderSender: erster Partner des Objekts in den Rollen der Auftragsart, sonst der des
// Buchungskreises; Unterschrift aus dem DMS (Dokumentart der Auftragsart).
func (m *Module) orderSender(ctx context.Context, l *orderLetter) map[string]any {
	company := reportapi.Company(ctx, m.services, l.cc)
	id := ""
	if roles := splitCodes(crud.Str(l.typ["sender_roles"])); len(roles) > 0 {
		id = reportapi.ObjectPartner(ctx, m.services, l.cc, crud.Str(l.rec["object_id"]), crud.Str(l.rec["order_date"]), roles)
	}
	if id == "" {
		if p, ok := company["partner"].(map[string]any); ok {
			id = crud.Str(p["id"])
		}
	}
	p := reportapi.Partner(ctx, m.services, id)
	if p == nil {
		return map[string]any{"name": crud.Str(company["name"])}
	}
	if f, ok := reportapi.Signature(ctx, m.services, l.cc, id, crud.Str(l.typ["signature_doc_type"]), "signature-"+id); ok {
		l.files = append(l.files, f)
		p["signature"] = f.Name
	}
	return p
}

// orderObjectData: Objekt für das Schreiben – beim Mietobjekt die Anschrift des
// Gebäudes und Bezeichnung, Geschoss, Lage der Einheit; text = Objektangabe.
func (m *Module) orderObjectData(ctx context.Context, cc, typ, id, text string) map[string]any {
	o := map[string]any{"type": typ, "id": id}
	get := func(object, key string) map[string]any {
		resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": cc + "|" + key})
		if err != nil {
			return nil
		}
		var rec map[string]any
		if sdk.Decode(resp.Payload, &rec) != nil {
			return nil
		}
		return rec
	}
	if id != "" {
		rec := get(typ, id)
		if rec != nil {
			o["designation"] = crud.Str(rec["designation"])
			building := rec
			if typ == "RentObject" {
				o["unit"] = crud.Str(rec["designation"])
				for _, k := range []string{"floor", "location"} {
					o[k] = m.catalogName(ctx, k, cc, crud.Str(rec[k]))
				}
				building = get("Building", crud.Str(rec["building_id"]))
			}
			if building != nil {
				o["building"] = crud.Str(building["designation"])
				for _, k := range []string{"street", "zip", "city"} {
					o[k] = crud.Str(building[k])
				}
			}
		}
	}
	if text == "" {
		addr := strings.TrimSpace(crud.Str(o["street"]))
		if z := strings.TrimSpace(crud.Str(o["zip"]) + " " + crud.Str(o["city"])); z != "" {
			addr = strings.TrimSpace(addr + " – " + z)
		}
		parts := []string{}
		if addr != "" {
			parts = append(parts, addr)
		} else if b := crud.Str(o["building"]); b != "" {
			parts = append(parts, b)
		}
		if u := strings.TrimSpace(crud.Str(o["unit"]) + " " + firstNonEmpty(crud.Str(o["floor"]), crud.Str(o["location"]))); u != "" {
			parts = append(parts, u)
		}
		text = strings.Join(parts, " – ")
	}
	o["text"] = text
	return o
}

// catalogName: Bezeichnung eines Katalogwerts der Immobilienverwaltung (Geschoss, Lage).
func (m *Module) catalogName(ctx context.Context, key, cc, code string) string {
	if code == "" {
		return ""
	}
	object := map[string]string{"floor": "Floor", "location": "Location"}[key]
	resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": cc + "|" + code})
	if err != nil {
		return code
	}
	return firstNonEmpty(field(resp.Payload, "name"), code)
}

// quotesText: „AN-2026-00003 und AN-2026-00004 vom 07.10.2026“ bzw. je Angebot mit Datum.
func (l *orderLetter) quotesText() string {
	if len(l.quotes) == 0 {
		return ""
	}
	sameDate := true
	for _, q := range l.quotes {
		if q["date"] != l.quotes[0]["date"] {
			sameDate = false
		}
	}
	var parts []string
	for _, q := range l.quotes {
		s := crud.Str(q["reference"])
		if !sameDate {
			s += " vom " + germanDate(crud.Str(q["date"]))
		}
		parts = append(parts, s)
	}
	s := parts[0]
	if len(parts) > 1 {
		s = strings.Join(parts[:len(parts)-1], ", ") + " und " + parts[len(parts)-1]
	}
	if sameDate {
		s += " vom " + germanDate(crud.Str(l.quotes[0]["date"]))
	}
	return s
}

// fill: Platzhalter eines Textes einsetzen; fehlende Werte sammelt missing.
func (l *orderLetter) fill(s string, missing *[]string) string {
	return placeholderRe.ReplaceAllStringFunc(s, func(t string) string {
		p := placeholderRe.FindStringSubmatch(t)
		v, err := l.value(p[1], strings.TrimSpace(p[2]))
		if err != nil {
			*missing = append(*missing, t+": "+err.Error())
			return t
		}
		return v
	})
}

func (l *orderLetter) value(src, name string) (string, error) {
	shown := func(v any) string {
		s := strings.TrimSpace(crud.Str(v))
		if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
			return germanDate(s[:10])
		}
		return s
	}
	partner := func(p map[string]any, what string) (string, error) {
		if p == nil {
			return "", fmt.Errorf("nicht lesbar")
		}
		if v := shown(p[name]); v != "" {
			return v, nil
		}
		if c, ok := p["contact"].(map[string]any); ok {
			if v := shown(c[strings.ToUpper(name)]); v != "" {
				return v, nil
			}
		}
		return "", fmt.Errorf("fehlt beim %s", what)
	}
	switch src {
	case "auftrag":
		if name == "number" {
			return l.id, nil
		}
		v, ok := l.rec[name]
		if !ok {
			return "", fmt.Errorf("Feld gibt es am Auftrag nicht")
		}
		if s := shown(v); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("leer")
	case "angebote":
		if s := l.quotesText(); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("Auftrag ohne Angebot")
	case "angebot":
		if len(l.quotes) == 0 {
			return "", fmt.Errorf("Auftrag ohne Angebot")
		}
		if s := shown(l.quotes[0][name]); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("leer oder unbekannt")
	case "objekt":
		if s := shown(l.object[name]); s != "" {
			return s, nil
		}
		return "", fmt.Errorf("leer oder unbekannt")
	case "lieferant":
		return partner(l.supplier, "Lieferanten")
	case "absender":
		return partner(l.sender, "Absender")
	case "summe":
		v, ok := map[string]int64{"netto": l.net, "steuer": l.tax, "brutto": l.net + l.tax}[name]
		if !ok {
			return "", fmt.Errorf("netto, steuer oder brutto")
		}
		return formatDE(v, l.dec), nil
	case "kontakt":
		if l.contact == nil {
			return "", fmt.Errorf("nur im Satz einer Kontaktrolle")
		}
		return partner(l.contact, "Kontakt")
	}
	return "", fmt.Errorf("Quelle unbekannt")
}

// formatDE: kleinste Einheit → „1.234,56“.
func formatDE(minor int64, dec int) string {
	return pricing.FormatDE(new(big.Rat).SetFrac(big.NewInt(minor), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(dec)), nil)).FloatString(dec))
}

func germanDate(iso string) string {
	if len(iso) < 10 {
		return iso
	}
	return iso[8:10] + "." + iso[5:7] + "." + iso[:4]
}

func dateOf(v any) string {
	s := crud.Str(v)
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// minorFloat: kleinste Einheit → Zahl für die Vorlage (fmtNumber).
func (l *orderLetter) minorFloat(v int64) float64 {
	f, _ := new(big.Rat).SetFrac(big.NewInt(v), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(l.dec)), nil)).Float64()
	return f
}

// data: Daten procurement.order/1 für die Vorlage.
func (l *orderLetter) data(ctx context.Context) (map[string]any, error) {
	var missing []string
	m := l.m
	texts, err := m.db.Query(ctx, `SELECT section, text, seq FROM procurement__order_text WHERE company_code = ? AND order_id = ? AND is_active = ?
		ORDER BY seq, line_no`, l.cc, l.id, true)
	if err != nil {
		return nil, err
	}
	type agreement struct {
		seq  int64
		item map[string]any
	}
	var intro, closing []any
	var agreements []agreement
	for _, r := range texts.Rows {
		t := strings.TrimSpace(l.fill(crud.Str(r[1]), &missing))
		if t == "" {
			continue
		}
		switch crud.Str(r[0]) {
		case sectionIntro:
			intro = append(intro, t)
		case sectionClosing:
			closing = append(closing, t)
		case sectionAgreement:
			agreements = append(agreements, agreement{toInt(r[2]), map[string]any{"text": t}})
		}
	}
	contacts, err := m.db.Query(ctx, `SELECT c.partner_id, c.person, c.addition, COALESCE(c.categories, r.categories), c.show_address,
		COALESCE(c.text, r.text), c.seq, r.name FROM procurement__order_contact c
		LEFT JOIN procurement__contact_role r ON r.company_code = c.company_code AND r.code = c.role
		WHERE c.company_code = ? AND c.order_id = ? AND c.is_active = ? ORDER BY c.seq, c.line_no`, l.cc, l.id, true)
	if err != nil {
		return nil, err
	}
	for _, r := range contacts.Rows {
		p := reportapi.Partner(ctx, m.services, crud.Str(r[0]))
		if p == nil {
			missing = append(missing, "Kontakt "+crud.Str(r[0])+": Geschäftspartner nicht lesbar")
			continue
		}
		p["person"], p["addition"] = crud.Str(r[1]), crud.Str(r[2])
		block := m.contactBlock(ctx, p, splitCodes(crud.Str(r[3])), crud.AsBool(r[4]))
		l.contact = p
		text := strings.TrimSpace(l.fill(crud.Str(r[5]), &missing))
		l.contact = nil
		if text == "" {
			text = crud.Str(r[7]) + ":"
		}
		agreements = append(agreements, agreement{toInt(r[6]), map[string]any{"text": text, "contact": block}})
	}
	// Vereinbarungen nach Reihenfolge (Texte und Kontakte gemischt), bei Gleichstand wie gelesen
	for i := 1; i < len(agreements); i++ {
		for j := i; j > 0 && agreements[j].seq < agreements[j-1].seq; j-- {
			agreements[j], agreements[j-1] = agreements[j-1], agreements[j]
		}
	}
	var ag []any
	for _, a := range agreements {
		ag = append(ag, a.item)
	}
	subject := l.fill(firstNonEmpty(crud.Str(l.rec["subject"]), crud.Str(l.typ["subject"]), "{auftrag:description}"), &missing)
	if len(missing) > 0 {
		return nil, crud.Invalid("Platzhalter ohne Wert: %s", strings.Join(missing, "; "))
	}
	recipient := map[string]any{"persons": []any{l.supplier}}
	for _, k := range []string{"street", "zip", "city", "country"} {
		recipient[k] = l.supplier[k]
	}
	if c := crud.Str(recipient["country"]); c == "" || c == crud.Str(l.sender["country"]) {
		delete(recipient, "country")
	}
	if s := crud.Str(l.rec["salutation"]); s != "" {
		recipient["salutation"] = s
	}
	var quotes []any
	for _, q := range l.quotes {
		quotes = append(quotes, q)
	}
	order := map[string]any{"id": l.id, "number": l.id, "type": crud.Str(l.rec["order_type"]), "description": crud.Str(l.rec["description"]),
		"date": dateOf(l.rec["order_date"]), "object": l.object, "quotes": quotes, "quotes_text": l.quotesText(),
		"planned_from": dateOf(l.rec["planned_from"]), "planned_to": dateOf(l.rec["planned_to"]), "currency": l.currency,
		"net_amounts": crud.AsBool(l.rec["net_amounts"]), "net": l.minorFloat(l.net), "tax": l.minorFloat(l.tax), "gross": l.minorFloat(l.net + l.tax)}
	out := map[string]any{"format": orderFormat, "company": reportapi.Company(ctx, m.services, l.cc), "sender": l.sender, "recipient": recipient,
		"date": dateOf(l.rec["order_date"]), "subject": subject, "our_ref": firstNonEmpty(crud.Str(l.rec["our_reference"]), l.id),
		"order": order, "intro": intro, "agreements": ag, "outro": closing, "show_items": crud.AsBool(l.typ["show_items"])}
	if len(l.quotes) > 0 {
		dates := map[string]bool{}
		var ds []string
		for _, q := range l.quotes {
			if d := crud.Str(q["date"]); !dates[d] {
				dates[d] = true
				ds = append(ds, d)
			}
		}
		out["your_mail"] = ds
	}
	if crud.AsBool(l.typ["show_items"]) {
		items, err := l.items(ctx)
		if err != nil {
			return nil, err
		}
		out["items"] = items
	}
	var files []any
	for _, f := range l.files {
		files = append(files, map[string]any{"name": f.Name, "content": f.Content})
	}
	out["files"] = files
	return out, nil
}

// items: Hauptpositionen mit Positionsart (Kürzel) für die Tabelle im Schreiben.
func (l *orderLetter) items(ctx context.Context) ([]any, error) {
	res, err := l.m.db.Query(ctx, `SELECT i.supplier_pos, i.item_text, i.quantity, i.unit, i.unit_price, i.amount, k.name, k.short, i.line_no
		FROM procurement__order_item i LEFT JOIN procurement__item_kind k ON k.company_code = i.company_code AND k.code = i.item_kind
		WHERE i.company_code = ? AND i.order_id = ? AND i.is_active = ? AND i.dependent_type IS NULL ORDER BY i.line_no`, l.cc, l.id, true)
	if err != nil {
		return nil, err
	}
	var out []any
	for _, r := range res.Rows {
		q := ""
		if v, err := pricing.ParseDecimal(crud.Str(r[2])); err == nil {
			q = pricing.FormatTrim(v, 6)
		}
		price := 0.0
		if v, err := pricing.ParseDecimal(crud.Str(r[4])); err == nil {
			price, _ = v.Float64()
		}
		out = append(out, map[string]any{"pos": firstNonEmpty(crud.Str(r[0]), fmt.Sprint(toInt(r[8]))), "text": crud.Str(r[1]), "quantity": q,
			"unit": crud.Str(r[3]), "unit_price": price, "amount": l.minorFloat(toInt(r[5])), "kind": crud.Str(r[6]), "kind_short": crud.Str(r[7])})
	}
	return out, nil
}

// contactBlock: Name, Ansprechpartner, Zusatz, Anschrift und gewählte Kommunikationsdaten.
func (m *Module) contactBlock(ctx context.Context, p map[string]any, categories []string, address bool) map[string]any {
	b := map[string]any{"name": p["name"], "person": p["person"], "addition": p["addition"]}
	if address {
		for _, k := range []string{"street", "zip", "city"} {
			b[k] = p[k]
		}
	}
	want := map[string]bool{}
	for _, c := range categories {
		want[c] = true
	}
	labels := map[string]string{}
	var lines []any
	cs, _ := p["contacts"].([]map[string]any)
	for _, c := range cs {
		cat := crud.Str(c["category"])
		if len(want) > 0 && !want[cat] {
			continue
		}
		if _, ok := labels[cat]; !ok {
			labels[cat] = cat
			if resp, err := m.services.Call(ctx, "PartnerCommCategory", "get", map[string]any{"id": cat}); err == nil {
				labels[cat] = firstNonEmpty(field(resp.Payload, "description"), cat)
			}
		}
		lines = append(lines, map[string]any{"category": cat, "label": labels[cat], "value": crud.Str(c["value"])})
	}
	b["lines"] = lines
	return b
}

// template: Vorlage der Auftragsart bzw. die erste der Verwendung (Code|Sprache).
func (l *orderLetter) template(ctx context.Context) (string, error) {
	t := strings.TrimSpace(crud.Str(l.typ["template"]))
	if t == "" {
		if opts := reportapi.Templates(ctx, l.m.services, usageOrder); len(opts) > 0 {
			t = opts[0].Value
		}
	}
	if t == "" {
		return "", crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt ORDER_LETTER an)", usageOrder)
	}
	if !strings.Contains(t, "|") {
		t += "|de"
	}
	return t, nil
}

// render: Schreiben erzeugen – Ergebnis für die Oberfläche (download, message).
func (m *Module) renderOrder(ctx context.Context, o *orderRow) (*orderLetter, map[string]any, error) {
	l, err := m.newOrderLetter(ctx, o.CC, o.ID)
	if err != nil {
		return nil, nil, err
	}
	data, err := l.data(ctx)
	if err != nil {
		return nil, nil, err
	}
	tmpl, err := l.template(ctx)
	if err != nil {
		return nil, nil, err
	}
	out, err := reportapi.Render(ctx, m.services, o.CC, tmpl, "Auftragsschreiben "+o.ID, data)
	return l, out, err
}

func (m *Module) orderPreviewAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, _, err := m.orderAction(ctx, req.Payload, "get")
	if err != nil {
		return sdk.Response{}, err
	}
	_, out, err := m.renderOrder(ctx, o)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: out}, nil
}

// orderIssueAction: Schreiben erzeugen, im DMS ablegen (Dokumentart der Auftragsart,
// Bezüge Auftrag, Objekt, Angebote), Auftrag erteilt.
func (m *Module) orderIssueAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, _, err := m.orderAction(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if o.Status != orderDraft {
		return sdk.Response{}, crud.Invalid("Auftrag %s ist %s", o.ID, optionLabel(orderStatusOptions, o.Status))
	}
	if !m.hasItems(ctx, orderDoc, o.CC, o.ID) {
		return sdk.Response{}, crud.Invalid("Auftrag %s hat keine Positionen", o.ID)
	}
	l, out, err := m.renderOrder(ctx, o)
	if err != nil {
		return sdk.Response{}, err
	}
	var file metamodel.Content
	if d, ok := out["download"]; !ok || sdk.Decode(d, &file) != nil || file.Content == "" {
		return sdk.Response{}, crud.Invalid("Auftragsschreiben %s: kein Ergebnis (%s)", o.ID, crud.Str(out["message"]))
	}
	docID, docName, note := "", "", ""
	if dt := crud.Str(l.typ["doc_type"]); dt != "" {
		refs := []any{map[string]any{"object": orderObject, "key": o.CC + "|" + o.ID}}
		if typ, id := crud.Str(l.rec["object_type"]), crud.Str(l.rec["object_id"]); id != "" {
			refs = append(refs, map[string]any{"object": typ, "key": o.CC + "|" + id})
		}
		for _, q := range l.quotes {
			refs = append(refs, map[string]any{"object": quoteObject, "key": o.CC + "|" + crud.Str(q["id"])})
		}
		resp, err := m.services.Call(ctx, "DmsDocument", "store", map[string]any{"company_code": o.CC, "content": file.Content,
			"file_name": firstNonEmpty(file.FileName, o.ID+".pdf"), "content_type": firstNonEmpty(file.ContentType, "application/pdf"),
			"doc_type": dt, "doc_date": dateOf(l.rec["order_date"]), "title": "Auftrag " + o.ID + " " + crud.Str(l.rec["description"]),
			"partner_id": crud.Str(l.rec["supplier_id"]), "refs": refs})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Auftragsschreiben nicht im DMS abgelegt: %v", unavailable("DMS", err))
		}
		var d struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := sdk.Decode(resp.Payload, &d); err != nil {
			return sdk.Response{}, err
		}
		docID, docName, note = d.ID, d.Name, " – im DMS: "+d.Name
	} else {
		note = " – nicht im DMS abgelegt (Dokumentart an der Auftragsart fehlt)"
	}
	if _, err := m.db.Exec(ctx, `UPDATE procurement__order SET status = ?, issued_at = ?, issued_by = ?, document_id = ?, document_name = ?, changed_at = ?
		WHERE company_code = ? AND order_id = ? AND status = ?`, orderIssued, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), nilIfEmpty(docID),
		nilIfEmpty(docName), now(), o.CC, o.ID, orderDraft); err != nil {
		return sdk.Response{}, err
	}
	if docName != "" {
		file.FileName = docName + extOfName(file.FileName)
	}
	return sdk.Response{Payload: map[string]any{"id": o.CC + "|" + o.ID, "status": orderIssued, "document_id": docID, "document_name": docName,
		"download": file, "message": "Auftrag " + o.ID + " erteilt" + note}}, nil
}

func extOfName(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[i:]
	}
	return ".pdf"
}

// orderDocument: abgelegtes Auftragsschreiben aus dem DMS.
func (m *Module) orderDocument(ctx context.Context, o *orderRow) (metamodel.Content, error) {
	var c metamodel.Content
	if o.DocumentID == "" {
		return c, crud.Invalid("Auftrag %s hat kein abgelegtes Schreiben – erst „Auftrag erteilen …“", o.ID)
	}
	resp, err := m.services.Call(ctx, "DmsDocument", "content", map[string]any{"id": o.DocumentID})
	if err != nil {
		return c, crud.Invalid("Auftragsschreiben %s: %v", o.DocumentName, unavailable("DMS", err))
	}
	if err := sdk.Decode(resp.Payload, &c); err != nil {
		return c, err
	}
	if c.FileName == "" {
		c.FileName = firstNonEmpty(o.DocumentName, o.ID) + ".pdf"
	} else if !strings.Contains(c.FileName, ".") {
		c.FileName += ".pdf"
	}
	return c, nil
}

func (m *Module) orderPDFAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, _, err := m.orderAction(ctx, req.Payload, "get")
	if err != nil {
		return sdk.Response{}, err
	}
	c, err := m.orderDocument(ctx, o)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"download": c, "message": "Auftragsschreiben " + c.FileName}}, nil
}

// orderMailAction: E-Mail-Entwurf (.eml, X-Unsent) mit Empfänger, Betreff, Text und
// dem abgelegten Schreiben als Anhang – der Mailclient öffnet ihn zum Prüfen und Senden.
func (m *Module) orderMailAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, _, err := m.orderAction(ctx, req.Payload, "get")
	if err != nil {
		return sdk.Response{}, err
	}
	pdf, err := m.orderDocument(ctx, o)
	if err != nil {
		return sdk.Response{}, err
	}
	l, err := m.newOrderLetter(ctx, o.CC, o.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	var missing []string
	subject := l.fill(firstNonEmpty(crud.Str(l.typ["mail_subject"]), crud.Str(l.rec["subject"]), crud.Str(l.typ["subject"]), "{auftrag:description}"), &missing)
	body := ""
	if b := crud.Str(l.typ["mail_block"]); b != "" {
		body = l.fill(m.text(ctx, `SELECT text FROM procurement__text_block WHERE company_code = ? AND code = ?`, o.CC, b), &missing)
	}
	if len(missing) > 0 {
		return sdk.Response{}, crud.Invalid("Platzhalter ohne Wert: %s", strings.Join(missing, "; "))
	}
	to := ""
	if cat := crud.Str(l.typ["mail_category"]); cat != "" {
		if c, ok := l.supplier["contact"].(map[string]any); ok {
			to = crud.Str(c[cat])
		}
	}
	from := ""
	if cat := crud.Str(l.typ["mail_category"]); cat != "" {
		if c, ok := l.sender["contact"].(map[string]any); ok {
			from = crud.Str(c[cat])
		}
	}
	raw, err := base64.StdEncoding.DecodeString(pdf.Content)
	if err != nil {
		return sdk.Response{}, err
	}
	eml, err := buildEML(from, to, subject, body, pdf.FileName, firstNonEmpty(pdf.ContentType, "application/pdf"), raw, time.Now())
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "E-Mail-Entwurf an " + firstNonEmpty(to, "(Empfänger eintragen)") + " – im Mailclient öffnen, prüfen und senden"
	return sdk.Response{Payload: map[string]any{"download": metamodel.Content{Content: base64.StdEncoding.EncodeToString(eml), ContentType: "message/rfc822",
		FileName: strings.TrimSuffix(pdf.FileName, extOfName(pdf.FileName)) + ".eml"}, "message": msg}}, nil
}

// buildEML: Nachricht im Format RFC 5322 mit Anhang; X-Unsent: 1 öffnet sie in Outlook
// als Entwurf (Thunderbird: „Als neu bearbeiten“).
func buildEML(from, to, subject, body, fileName, contentType string, attachment []byte, at time.Time) ([]byte, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	head := func(k, v string) { fmt.Fprintf(&buf, "%s: %s\r\n", k, v) }
	head("X-Unsent", "1")
	if from != "" {
		head("From", from)
	}
	if to != "" {
		head("To", to)
	}
	head("Subject", mime.QEncoding.Encode("utf-8", subject))
	head("Date", at.Format(time.RFC1123Z))
	head("MIME-Version", "1.0")
	head("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": w.Boundary()}))
	buf.WriteString("\r\n")
	text, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {"text/plain; charset=utf-8"}, "Content-Transfer-Encoding": {"base64"}})
	if err != nil {
		return nil, err
	}
	if _, err := text.Write([]byte(wrap76(base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(body, "\n", "\r\n")))))); err != nil {
		return nil, err
	}
	part, err := w.CreatePart(textproto.MIMEHeader{"Content-Type": {mime.FormatMediaType(contentType, map[string]string{"name": fileName})},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": fileName})},
		"Content-Transfer-Encoding": {"base64"}})
	if err != nil {
		return nil, err
	}
	if _, err := part.Write([]byte(wrap76(base64.StdEncoding.EncodeToString(attachment)))); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wrap76: Base64 in Zeilen zu 76 Zeichen (RFC 2045).
func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76] + "\r\n")
		s = s[76:]
	}
	b.WriteString(s + "\r\n")
	return b.String()
}
