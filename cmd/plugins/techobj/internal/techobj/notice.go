package techobj

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
)

// --- Ablesemitteilung ------------------------------------------------------------------
//
// Ein Brief an den Empfänger, der am Messpunkt in der Rolle der Mitteilungsart
// verknüpft ist (Liefervertrag → Vertragspartner, Netzbetreiber …). Alle
// Messpunkte mit demselben Empfängerobjekt am Ablesetag stehen im selben Brief
// (z. B. Wasser und Strom bei den Stadtwerken). Je Zähler: Messpunkt,
// Zählernummer, Zählpunkt/Netznummer, Nummer beim Versorger und die Stände
// des Tages je Zählwerk – am Tauschtag Endstand des alten und Anfangsstand des
// neuen Geräts. „Endgültig“ legt den Brief im DMS ab und vermerkt die Meldung
// an den Ablesungen (gemeldet an, gemeldet am, Dokument).

const usageLetter = "LETTER"

var noticePlaceholderRe = regexp.MustCompile(`\{([a-z]+)\}`)

var noticePlaceholders = map[string]bool{"datum": true, "staende": true, "kundennummer": true, "vertrag": true, "objekt": true}

func (m *Module) checkNoticeType(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["link_role"], rec["number_type"] = trimUpper(rec["link_role"]), nilIfEmpty(trimUpper(rec["number_type"]))
	if err := m.requireCatalog(ctx, "link_role", "Rolle", cc, rec["link_role"]); err != nil {
		return err
	}
	if err := m.requireCatalog(ctx, "number_type", "Nummernart", cc, rec["number_type"]); err != nil {
		return err
	}
	rec["template"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["template"])))
	rec["doc_type"] = nilIfEmpty(trimUpper(rec["doc_type"]))
	for _, k := range []string{"subject", "body"} {
		for _, p := range noticePlaceholderRe.FindAllStringSubmatch(crud.Str(rec[k]), -1) {
			if !noticePlaceholders[p[1]] {
				return crud.Invalid("Platzhalter %s gibt es nicht ({datum}, {staende}, {kundennummer}, {vertrag}, {objekt})", p[0])
			}
		}
	}
	return nil
}

// noticeFields: Eingaben der Aktion am Messpunkt.
var noticeFields = []crud.Field{
	{Key: "nt_type", Label: "Mitteilungsart (leer = erste)", Type: tText, ActionOnly: true, Lookup: catalogLookup("TechNoticeType")},
	{Key: "nt_date", Label: "Ablesedatum (leer = letzte Ablesung)", Type: tDate, ActionOnly: true},
	{Key: "nt_final", Label: "Endgültig: im DMS ablegen und an den Ablesungen vermerken (sonst nur Vorschau)", Type: tBool, ActionOnly: true},
}

func (m *Module) noticeAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "notice", Label: "Ablesemitteilung …", Icon: "icon-mail", Record: true,
		Fields: []string{"nt_type", "nt_date", "nt_final"}}, Handle: m.notice}
}

type noticeType struct {
	code, name, role, roleName, numberType, template, docType, subject, body string
}

func (m *Module) loadNoticeType(ctx context.Context, cc, code string) (*noticeType, error) {
	q := `SELECT n.code, n.name, n.link_role, COALESCE(r.name, n.link_role), COALESCE(n.number_type, ''), COALESCE(n.template, ''), COALESCE(n.doc_type, ''), n.subject, n.body
		FROM techobj__notice_type n LEFT JOIN techobj__link_role r ON r.company_code = n.company_code AND r.code = n.link_role
		WHERE n.company_code = ? AND n.is_active = ?`
	args := []any{cc, true}
	if code != "" {
		q += " AND n.code = ?"
		args = append(args, code)
	}
	res, err := m.db.Query(ctx, q+" ORDER BY n.sort_order, n.code LIMIT 1", args...)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		if code != "" {
			return nil, crud.Invalid("Mitteilungsart %s gibt es im Buchungskreis %s nicht (oder inaktiv)", code, cc)
		}
		return nil, crud.Invalid("Keine Mitteilungsart im Buchungskreis %s – Einstellungen → Mitteilungsarten („Kataloge einrichten“ legt Vorschläge an)", cc)
	}
	r := res.Rows[0]
	return &noticeType{code: crud.Str(r[0]), name: crud.Str(r[1]), role: crud.Str(r[2]), roleName: crud.Str(r[3]), numberType: crud.Str(r[4]),
		template: crud.Str(r[5]), docType: crud.Str(r[6]), subject: crud.Str(r[7]), body: crud.Str(r[8])}, nil
}

// noticeReading: ein Stand im Brief.
type noticeReading struct {
	equipment, register, regName, typeName, value, readingType string
}

type noticePoint struct {
	id, name, marketLocation, providerNumber string
	numbers                                  map[string]string // Gerät → Nummern
	readings                                 []noticeReading
}

func (m *Module) notice(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Type  string `json:"nt_type"`
			Date  string `json:"nt_date"`
			Final any    `json:"nt_final"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, point, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Messpunkt: id <BuKr>|<Messpunkt>")
	}
	final := crud.AsBool(in.Data.Final)
	action := "read"
	if final {
		action = "update"
	}
	if err := requireWrite(ctx, "MeterReading", action, cc); err != nil {
		return sdk.Response{}, err
	}
	if action == "read" {
		if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
			return sdk.Response{}, err
		} else if !ok {
			return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
		}
	}
	nt, err := m.loadNoticeType(ctx, cc, trimUpper(in.Data.Type))
	if err != nil {
		return sdk.Response{}, err
	}
	day := ""
	if strings.TrimSpace(in.Data.Date) != "" {
		if day, err = crud.ParseDate(in.Data.Date); err != nil {
			return sdk.Response{}, err
		}
	} else if day, err = m.one(ctx, `SELECT MAX(read_on) FROM techobj__reading WHERE company_code = ? AND point_id = ?`, cc, point); err != nil {
		return sdk.Response{}, err
	} else if day = date(day); day == "" {
		return sdk.Response{}, crud.Invalid("Messpunkt %s hat keine Ablesungen", point)
	}
	// Empfängerobjekt am Ablesetag
	link, err := m.db.Query(ctx, `SELECT object_type, object_id FROM techobj__link WHERE company_code = ? AND source_kind = ? AND source_id = ? AND role_code = ?
		AND valid_from <= ? AND valid_to >= ? ORDER BY object_type, object_id LIMIT 1`, cc, "MeterPoint", point, nt.role, day, day)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(link.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Messpunkt %s hat am %s keine Verknüpfung „%s“ – Abschnitt Verknüpfungen", point, germanDate(day), nt.roleName)
	}
	objType, objID := crud.Str(link.Rows[0][0]), crud.Str(link.Rows[0][1])
	target, err := m.target(ctx, cc, objType, objID)
	if err != nil {
		return sdk.Response{}, err
	}
	fields, err := m.db.Query(ctx, "SELECT COALESCE(partner_field, ''), COALESCE(reference_field, ''), name FROM techobj__link_object WHERE company_code = ? AND code = ?", cc, objType)
	if err != nil {
		return sdk.Response{}, err
	}
	partnerField, refField, objName := crud.Str(fields.Rows[0][0]), crud.Str(fields.Rows[0][1]), crud.Str(fields.Rows[0][2])
	partnerID := strings.TrimSpace(crud.Str(target[partnerField]))
	if partnerField == "" || partnerID == "" {
		return sdk.Response{}, crud.Invalid("%s %s: kein Empfänger – Feld mit dem Geschäftspartner unter Einstellungen → Verknüpfbare Objekte (%s) eintragen", objName, objID, objType)
	}
	reference := ""
	if refField != "" {
		reference = strings.TrimSpace(crud.Str(target[refField]))
	}
	recipient := reportapi.Partner(ctx, m.services, partnerID)
	if recipient == nil {
		return sdk.Response{}, crud.Invalid("Geschäftspartner %s nicht lesbar", partnerID)
	}
	// alle Messpunkte mit diesem Empfängerobjekt am Tag
	pts, err := m.db.Query(ctx, `SELECT l.source_id, p.designation, COALESCE(p.market_location, ''), COALESCE(p.provider_number, '') FROM techobj__link l
		JOIN techobj__point p ON p.company_code = l.company_code AND p.point_id = l.source_id
		WHERE l.company_code = ? AND l.source_kind = ? AND l.role_code = ? AND l.object_type = ? AND l.object_id = ? AND l.valid_from <= ? AND l.valid_to >= ?
		ORDER BY l.source_id`, cc, "MeterPoint", nt.role, objType, objID, day, day)
	if err != nil {
		return sdk.Response{}, err
	}
	var points []*noticePoint
	var skipped []string
	for _, r := range pts.Rows {
		p := &noticePoint{id: crud.Str(r[0]), name: crud.Str(r[1]), marketLocation: crud.Str(r[2]), providerNumber: crud.Str(r[3]), numbers: map[string]string{}}
		if err := m.noticeReadings(ctx, cc, p, day, nt.numberType); err != nil {
			return sdk.Response{}, err
		}
		if len(p.readings) == 0 {
			skipped = append(skipped, p.id+" "+p.name)
			continue
		}
		points = append(points, p)
	}
	if len(points) == 0 {
		return sdk.Response{}, crud.Invalid("Am %s gibt es an keinem Messpunkt von %s %s eine Ablesung", germanDate(day), objName, objID)
	}
	object, err := m.userLabels(ctx, cc, point, day)
	if err != nil {
		return sdk.Response{}, err
	}
	if object == "" {
		object = points[0].name
	}
	contract := strings.TrimSpace(objID + " " + m.targetLabel(ctx, cc, objType, objID))
	values := map[string]string{"datum": germanDate(day), "staende": noticeLines(points), "kundennummer": reference, "vertrag": contract, "objekt": object}
	fill := func(s string) string {
		return noticePlaceholderRe.ReplaceAllStringFunc(s, func(p string) string { return values[p[1:len(p)-1]] })
	}
	subject, body := fill(nt.subject), fill(nt.body)
	company := reportapi.Company(ctx, m.services, cc)
	sender := map[string]any{"name": crud.Str(company["name"])}
	if p, ok := company["partner"].(map[string]any); ok {
		if s := reportapi.Partner(ctx, m.services, crud.Str(p["id"])); s != nil {
			sender = s
		}
	}
	data := map[string]any{"format": "techobj.notice/1", "sender": sender, "recipient": letterRecipient(recipient), "subject": subject, "text": body,
		"date": crud.Today(), "notice": map[string]any{"date": day, "reference": reference, "contract": contract, "object": object, "points": noticeData(points)}}
	tmpl := nt.template
	if tmpl == "" {
		if opts := reportapi.Templates(ctx, m.services, usageLetter); len(opts) > 0 {
			tmpl = opts[0].Value
		}
	}
	if tmpl == "" {
		return sdk.Response{}, crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → Vorlagen; „Beispiele anlegen …“ legt LETTER an)", usageLetter)
	}
	if !strings.Contains(tmpl, "|") {
		tmpl += "|de"
	}
	name := crud.Str(recipient["name"])
	out, err := reportapi.Render(ctx, m.services, cc, tmpl, nt.name+" "+germanDate(day)+" an "+name, data)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(skipped) > 0 {
		out["message"] = crud.Str(out["message"]) + " – ohne Ablesung am Tag: " + strings.Join(skipped, ", ")
	}
	if !final {
		out["message"] = "Vorschau: " + crud.Str(out["message"])
		return sdk.Response{Payload: out}, nil
	}
	var file metamodel.Content
	if d, ok := out["download"]; !ok || sdk.Decode(d, &file) != nil || file.Content == "" {
		return sdk.Response{}, crud.Invalid("Ablesemitteilung: kein Ergebnis (%s)", crud.Str(out["message"]))
	}
	docName := ""
	if nt.docType != "" {
		refs := []any{}
		for _, p := range points {
			refs = append(refs, map[string]any{"object": "MeterPoint", "key": cc + "|" + p.id})
		}
		if svc, err := m.one(ctx, "SELECT service_object FROM techobj__link_object WHERE company_code = ? AND code = ?", cc, objType); err == nil && svc != "" && svc != "BusinessPartner" {
			refs = append(refs, map[string]any{"object": svc, "key": cc + "|" + objID})
		}
		resp, err := m.services.Call(ctx, "DmsDocument", "store", map[string]any{"company_code": cc, "content": file.Content,
			"file_name": file.FileName, "content_type": file.ContentType, "doc_type": nt.docType, "doc_date": crud.Today(),
			"title": strings.TrimSpace(subject), "partner_id": partnerID, "refs": refs})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Ablesemitteilung nicht im DMS abgelegt: %v", err)
		}
		var d struct {
			Name string `json:"name"`
		}
		_ = sdk.Decode(resp.Payload, &d)
		docName = d.Name
	}
	reported := strings.TrimSpace(name)
	if reference != "" {
		reported += ", " + reference
	}
	for _, p := range points {
		for _, r := range p.readings {
			if _, err := m.db.Exec(ctx, `UPDATE techobj__reading SET reported_to = ?, reported_on = ?, report_document = ? WHERE company_code = ? AND equipment_id = ?
				AND register_code = ? AND read_on = ? AND reading_type = ?`, reported, crud.Today(), nilIfEmpty(docName), cc, r.equipment, r.register, day, r.readingType); err != nil {
				return sdk.Response{}, err
			}
		}
	}
	msg := nt.name + " " + germanDate(day) + " an " + reported
	if docName != "" {
		msg += " – im DMS: " + docName
		file.FileName = docName + ".pdf"
	} else {
		msg += " – nicht im DMS abgelegt (Dokumentart an der Mitteilungsart fehlt)"
	}
	out["download"], out["message"] = file, msg+"; an den Ablesungen vermerkt"
	return sdk.Response{Payload: out}, nil
}

// noticeReadings: Stände des Tages an den Geräten, die am Tag im Messpunkt
// eingebaut sind oder am Vortag ausgebaut wurden (Tauschtag).
func (m *Module) noticeReadings(ctx context.Context, cc string, p *noticePoint, day, numberType string) error {
	res, err := m.db.Query(ctx, `SELECT r.equipment_id, r.register_code, COALESCE(g.designation, r.register_code), COALESCE(t.name, r.reading_type), r.value,
		COALESCE(g.decimals, 0), COALESCE(u.name, g.unit, ''), r.reading_type
		FROM techobj__reading r
		JOIN techobj__installation i ON i.company_code = r.company_code AND i.equipment_id = r.equipment_id AND i.point_id = ? AND i.valid_from <= ? AND i.valid_to >= ?
		LEFT JOIN techobj__register g ON g.company_code = r.company_code AND g.equipment_id = r.equipment_id AND g.register_code = r.register_code
		LEFT JOIN techobj__unit u ON u.company_code = g.company_code AND u.code = g.unit
		LEFT JOIN techobj__reading_type t ON t.company_code = r.company_code AND t.code = r.reading_type
		WHERE r.company_code = ? AND r.read_on = ?
		AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = r.company_code AND s.code = r.status AND s.is_rejected)
		ORDER BY i.valid_from, r.equipment_id, r.register_code`, p.id, day, dayBefore(day), cc, day)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		eq := crud.Str(r[0])
		if _, ok := p.numbers[eq]; !ok {
			if p.numbers[eq], err = m.meterNumbers(ctx, cc, eq, numberType); err != nil {
				return err
			}
		}
		v := numberDE(toFloat(r[4]), int(toInt(r[5])))
		if u := crud.Str(r[6]); u != "" {
			v += " " + u
		}
		p.readings = append(p.readings, noticeReading{equipment: eq, register: crud.Str(r[1]), regName: crud.Str(r[2]), typeName: crud.Str(r[3]), value: v, readingType: crud.Str(r[7])})
	}
	return nil
}

// meterNumbers: Nummern des Geräts der Nummernart (leer = alle, mit Art).
func (m *Module) meterNumbers(ctx context.Context, cc, eq, numberType string) (string, error) {
	if numberType == "" {
		return m.numbersText(ctx, cc, eq)
	}
	res, err := m.db.Query(ctx, "SELECT number FROM techobj__equipment_number WHERE company_code = ? AND equipment_id = ? AND number_type = ? ORDER BY number", cc, eq, numberType)
	if err != nil {
		return "", err
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, crud.Str(r[0]))
	}
	return strings.Join(out, ", "), nil
}

// noticeLines: {staende} – je Messpunkt eine Kopfzeile mit Zählpunkt/Netznummer,
// je Gerät die Zählernummer und je Zählwerk der Stand.
func noticeLines(points []*noticePoint) string {
	var b strings.Builder
	for i, p := range points {
		if i > 0 {
			b.WriteString("\n")
		}
		head := p.name
		var ids []string
		if p.marketLocation != "" {
			ids = append(ids, "Zählpunkt/Netznummer "+p.marketLocation)
		}
		if p.providerNumber != "" {
			ids = append(ids, "Nr. beim Versorger "+p.providerNumber)
		}
		if len(ids) > 0 {
			head += " (" + strings.Join(ids, ", ") + ")"
		}
		b.WriteString(head + "\n")
		eqs := map[string][]noticeReading{}
		var order []string
		for _, r := range p.readings {
			if _, ok := eqs[r.equipment]; !ok {
				order = append(order, r.equipment)
			}
			eqs[r.equipment] = append(eqs[r.equipment], r)
		}
		for _, eq := range order {
			num := p.numbers[eq]
			if num == "" {
				num = "ohne Nummer (" + eq + ")"
			}
			rs := eqs[eq]
			many := len(rs) > 1
			for _, r := range rs {
				line := "Zähler " + num
				if many {
					line += ", " + r.regName
				}
				line += ": " + r.value
				if multipleTypes(p.readings) {
					line += " (" + r.typeName + ")"
				}
				b.WriteString(line + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// multipleTypes: verschiedene Ablesearten am Tag (z. B. Ausbau und Einbau) – dann je Zeile genannt.
func multipleTypes(rs []noticeReading) bool {
	for _, r := range rs {
		if r.readingType != rs[0].readingType {
			return true
		}
	}
	return false
}

func noticeData(points []*noticePoint) []any {
	var out []any
	for _, p := range points {
		var rs []any
		for _, r := range p.readings {
			rs = append(rs, map[string]any{"equipment": r.equipment, "number": p.numbers[r.equipment], "register": r.register, "register_name": r.regName,
				"reading_type": r.readingType, "reading_type_name": r.typeName, "value": r.value})
		}
		out = append(out, map[string]any{"point": p.id, "name": p.name, "market_location": p.marketLocation, "provider_number": p.providerNumber, "readings": rs})
	}
	return out
}

// letterRecipient: Empfänger für die Briefvorlage (Personen mit Anrede, sonst Name).
func letterRecipient(p map[string]any) map[string]any {
	r := map[string]any{"street": p["street"], "zip": p["zip"], "city": p["city"]}
	if crud.Str(p["last_name"]) != "" {
		r["persons"] = []any{map[string]any{"title": p["title"], "first_name": p["first_name"], "last_name": p["last_name"], "letter_salutation": p["letter_salutation"]}}
	} else {
		r["name"] = p["name"]
		if s := crud.Str(p["letter_salutation"]); s != "" {
			r["salutation"] = s
		}
	}
	return r
}

// userLabels: Bezeichnungen der Nutzer-Objekte am Tag (z. B. „Biesnitzer Str. 14“) –
// für Briefe ohne Objektart und ID.
func (m *Module) userLabels(ctx context.Context, cc, point, day string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT l.object_type, l.object_id FROM techobj__link l
		JOIN techobj__link_role r ON r.company_code = l.company_code AND r.code = l.role_code AND r.is_user = ? AND r.is_active = ?
		WHERE l.company_code = ? AND l.source_kind = ? AND l.source_id = ? AND l.valid_from <= ? AND l.valid_to >= ?
		ORDER BY r.sort_order, l.object_type, l.object_id`, true, true, cc, "MeterPoint", point, day, day)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, r := range res.Rows {
		l := m.targetLabel(ctx, cc, crud.Str(r[0]), crud.Str(r[1]))
		if l == "" {
			l = crud.Str(r[1])
		}
		parts = append(parts, l)
	}
	return strings.Join(parts, "; "), nil
}

// numberDE: 12128.75 → „12.128,75“ (Nachkommastellen des Zählwerks).
func numberDE(v float64, dec int) string {
	s := fmt.Sprintf("%.*f", dec, v)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	intPart, frac, _ := strings.Cut(s, ".")
	var b strings.Builder
	for i, c := range intPart {
		if i > 0 && (len(intPart)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String()
	if frac != "" {
		out += "," + frac
	}
	if neg {
		out = "-" + out
	}
	return out
}
