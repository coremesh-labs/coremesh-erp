package inspection

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh-erp/pkg/reportapi"
	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Verwendung der Vorlage und Datenformat des Protokolldrucks.
const (
	usageSheet  = "PROTOCOL_SHEET"
	sheetFormat = "inspection.protocol/1"
)

type recordInput struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"data"`
}

type protocolRec struct {
	cc, id, typ, object, contract, partner, day, title, place, note, status, document, process string
}

func (m *Module) loadProtocol(ctx context.Context, key string) (*protocolRec, *protocolType, error) {
	cc, id, ok := strings.Cut(key, "|")
	if !ok {
		return nil, nil, crud.Invalid("Protokoll: id <BuKr>|<Nummer>")
	}
	res, err := m.db.Query(ctx, `SELECT protocol_type, object_id, COALESCE(contract_id, ''), COALESCE(partner_id, ''), protocol_date, COALESCE(title, ''),
		COALESCE(place, ''), COALESCE(note, ''), status, COALESCE(document, ''), COALESCE(process_id, '')
		FROM inspection__protocol WHERE company_code = ? AND protocol_id = ?`, cc, id)
	if err != nil {
		return nil, nil, err
	}
	if len(res.Rows) == 0 {
		return nil, nil, fmt.Errorf("%w: Protokoll %s", sdk.ErrNotFound, key)
	}
	r := res.Rows[0]
	s := func(i int) string { return crud.Str(r[i]) }
	p := &protocolRec{cc: cc, id: id, typ: s(0), object: s(1), contract: s(2), partner: s(3), day: date(r[4]), title: s(5), place: s(6),
		note: s(7), status: s(8), document: s(9), process: s(10)}
	t, err := m.protocolType(ctx, cc, p.typ)
	return p, t, err
}

func (m *Module) count(ctx context.Context, table string, p *protocolRec) (int64, error) {
	v, err := m.one(ctx, "SELECT COUNT(*) FROM inspection__"+table+" WHERE company_code = ? AND protocol_id = ?", p.cc, p.id)
	return toInt(v), err
}

// --- Übernehmen -----------------------------------------------------------------------

type itemLine struct {
	roomNo, room, compType, comp, desc string
	sort                               int64
}

type meterLine struct {
	point, register, designation, equipment, number, location, unit, lastOn string
	last                                                                    any
}

// prepareAction: InspectionProtocol.prepare {id} – Räume und Bauteile aus dem
// Raumbuch, Zähler aus den technischen Objekten, Teilnehmer aus Vertrag und
// Buchungskreis, Prüfliste aus dem Katalog. Was schon erfasst ist, bleibt: Räume,
// Zähler und Teilnehmer nur, solange das Protokoll noch keine hat; Prüfpunkte
// kommen hinzu, soweit sie fehlen.
func (m *Module) prepareAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	p, t, err := m.loadProtocol(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "InspectionProtocol", "update", p.cc); err != nil {
		return sdk.Response{}, err
	}
	if err := m.openProtocol(ctx, p.cc, p.id); err != nil {
		return sdk.Response{}, err
	}
	var notes []string
	// Daten der anderen Module vor der Transaktion holen
	var items []itemLine
	if n, err := m.count(ctx, "item", p); err != nil {
		return sdk.Response{}, err
	} else if t.withRooms && n == 0 {
		if items, err = m.roomItems(ctx, p); err != nil {
			notes = append(notes, "Raumbuch nicht lesbar: "+err.Error())
		} else if len(items) == 0 {
			notes = append(notes, "Das Raumbuch des Mietobjekts ist leer – Räume am Mietobjekt im Abschnitt „Räume“ anlegen und erneut übernehmen")
		}
	}
	var meters []meterLine
	if n, err := m.count(ctx, "meter", p); err != nil {
		return sdk.Response{}, err
	} else if t.withMeters && n == 0 {
		if meters, err = m.meterLines(ctx, p); err != nil {
			notes = append(notes, "Zähler nicht lesbar: "+err.Error())
		} else if len(meters) == 0 {
			notes = append(notes, "Am Mietobjekt misst kein Zähler (technische Objekte: Messpunkt mit Verknüpfung „misst für“ zum Mietobjekt)")
		}
	}
	type person struct{ role, partner, name string }
	var people []person
	if n, err := m.count(ctx, "participant", p); err != nil {
		return sdk.Response{}, err
	} else if n == 0 {
		roles, err := m.db.Query(ctx, "SELECT code, COALESCE(source, '') FROM inspection__participant_role WHERE company_code = ? AND is_active = ? AND source IS NOT NULL ORDER BY sort_order, code", p.cc, true)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, r := range roles.Rows {
			var partner string
			switch crud.Str(r[1]) {
			case "CONTRACT":
				partner = p.partner
			case "COMPANY":
				if pp, ok := reportapi.Company(ctx, m.services, p.cc)["partner"].(map[string]any); ok {
					partner = crud.Str(pp["id"])
				}
			}
			if partner == "" {
				continue
			}
			if name := m.partnerName(ctx, partner); name != "" {
				people = append(people, person{crud.Str(r[0]), partner, name})
			}
		}
	}
	var added [4]int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for i, it := range items {
			if _, err := m.db.Exec(ctx, `INSERT INTO inspection__item (company_code, protocol_id, line_no, room_no, room_name, component_type, component_name, description, sort_order, is_defect)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, p.cc, p.id, i+1, nilIfEmpty(it.roomNo), it.room, nilIfEmpty(it.compType), nilIfEmpty(it.comp), nilIfEmpty(it.desc), it.sort, false); err != nil {
				return err
			}
			added[0]++
		}
		for i, mt := range meters {
			if _, err := m.db.Exec(ctx, `INSERT INTO inspection__meter (company_code, protocol_id, point_id, register_code, designation, equipment_id, meter_number, location, unit, last_value, last_read_on, sort_order, reading_written)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, p.cc, p.id, mt.point, mt.register, mt.designation, nilIfEmpty(mt.equipment), nilIfEmpty(mt.number),
				nilIfEmpty(mt.location), nilIfEmpty(mt.unit), mt.last, nilIfEmpty(mt.lastOn), (i+1)*10, false); err != nil {
				return err
			}
			added[1]++
		}
		for i, pp := range people {
			if _, err := m.db.Exec(ctx, `INSERT INTO inspection__participant (company_code, protocol_id, line_no, role, partner_id, name, present, signs)
				SELECT ?, ?, ?, ?, ?, ?, ?, signs FROM inspection__participant_role WHERE company_code = ? AND code = ?`,
				p.cc, p.id, i+1, pp.role, pp.partner, pp.name, true, p.cc, pp.role); err != nil {
				return err
			}
			added[2]++
		}
		checks, err := m.db.Query(ctx, "SELECT code, name, kind, COALESCE(protocol_types, ''), COALESCE(hint, ''), sort_order FROM inspection__checklist_item WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code", p.cc, true)
		if err != nil {
			return err
		}
		for _, c := range checks.Rows {
			if types := splitCodes(crud.Str(c[3])); len(types) > 0 && !containsCode(types, p.typ) {
				continue
			}
			res, err := m.db.Exec(ctx, `INSERT INTO inspection__check (company_code, protocol_id, item_code, name, kind, hint, sort_order) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
				p.cc, p.id, c[0], c[1], c[2], nilIfEmpty(crud.Str(c[4])), c[5])
			if err != nil {
				return err
			}
			added[3] += int(res.RowsAffected)
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Protokoll %s: %d Räume/Bauteile, %d Zählwerke, %d Teilnehmer und %d Prüfpunkte übernommen", p.id, added[0], added[1], added[2], added[3])
	if len(notes) > 0 {
		msg += ". " + strings.Join(notes, ". ")
	}
	return sdk.Response{Payload: map[string]any{"message": msg}}, nil
}

// roomItems: je Raum (Reihenfolge des Rundgangs) die Bauteile aus dem Raumbuch;
// ein Raum ohne Bauteile bekommt eine Zeile „Raum allgemein“.
func (m *Module) roomItems(ctx context.Context, p *protocolRec) ([]itemLine, error) {
	q := map[string]any{"company_code": p.cc, "object_id": p.object}
	rooms, err := m.list(ctx, "Room", q)
	if err != nil {
		return nil, err
	}
	comps, err := m.list(ctx, "RoomComponent", q)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	if types, err := m.list(ctx, "ComponentType", map[string]any{"company_code": p.cc}); err == nil {
		for _, t := range types {
			names[crud.Str(t["code"])] = crud.Str(t["name"])
		}
	}
	byRoom := map[string][]map[string]any{}
	for _, c := range comps {
		byRoom[crud.Str(c["room_no"])] = append(byRoom[crud.Str(c["room_no"])], c)
	}
	sort.SliceStable(rooms, func(i, j int) bool {
		a, b := toInt(rooms[i]["sort_order"]), toInt(rooms[j]["sort_order"])
		if a != b {
			return a < b
		}
		return crud.Str(rooms[i]["room_no"]) < crud.Str(rooms[j]["room_no"])
	})
	var out []itemLine
	for ri, r := range rooms {
		if v, ok := r["is_active"]; ok && v != nil && !crud.AsBool(v) {
			continue
		}
		no, name := crud.Str(r["room_no"]), strings.TrimSpace(crud.Str(r["designation"]))
		base := int64(ri+1) * 1000
		cs := byRoom[no]
		if len(cs) == 0 {
			out = append(out, itemLine{roomNo: no, room: name, sort: base})
			continue
		}
		// Reihenfolge der Bauteilart im Katalog ist nicht bekannt: wie im Raumbuch
		for ci, c := range cs {
			code := crud.Str(c["component_type"])
			label := names[code]
			if label == "" {
				label = code
			}
			desc := strings.TrimSpace(crud.Str(c["description"]))
			if q := toFloat(c["quantity"]); q > 0 {
				desc = strings.TrimSpace(strconv.FormatFloat(q, 'f', -1, 64) + " × " + desc)
			}
			out = append(out, itemLine{roomNo: no, room: name, compType: code, comp: label, desc: desc, sort: base + int64(ci+1)*10})
		}
	}
	return out, nil
}

// meterLines: Zählwerke der Messpunkte, die am Tag für das Mietobjekt messen.
func (m *Module) meterLines(ctx context.Context, p *protocolRec) ([]meterLine, error) {
	resp, err := m.services.Call(ctx, "TechObjectSetup", "pointsFor", map[string]any{"company_code": p.cc, "object": "RentObject", "id": p.object, "date": p.day})
	if err != nil {
		return nil, err
	}
	var res struct {
		Items []struct {
			PointID     string `json:"point_id"`
			Designation string `json:"designation"`
			Location    string `json:"location"`
			EquipmentID string `json:"equipment_id"`
			MeterNumber string `json:"meter_number"`
			Numbers     string `json:"numbers"`
			Registers   []struct {
				Code        string `json:"register_code"`
				Designation string `json:"designation"`
				Unit        string `json:"unit"`
				LastOn      string `json:"last_read_on"`
				LastValue   any    `json:"last_value"`
			} `json:"registers"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &res); err != nil {
		return nil, err
	}
	var out []meterLine
	for _, it := range res.Items {
		number := it.MeterNumber
		if number == "" {
			number = it.Numbers
		}
		for _, r := range it.Registers {
			d := it.Designation
			if len(it.Registers) > 1 && r.Designation != "" {
				d += " – " + r.Designation
			}
			out = append(out, meterLine{point: it.PointID, register: r.Code, designation: d, equipment: it.EquipmentID, number: number,
				location: it.Location, unit: r.Unit, lastOn: r.LastOn, last: r.LastValue})
		}
	}
	return out, nil
}

// --- Drucken und Abschließen -------------------------------------------------------------

// printAction: InspectionProtocol.print {id, data: {pr_mode}} – BLANK: Formular
// für die Begehung, PREVIEW (Standard): Vorschau, FINAL: Abschluss.
func (m *Module) printAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	p, t, err := m.loadProtocol(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	mode := trimUpper(in.Data["pr_mode"])
	if mode == "" {
		mode = "PREVIEW"
	}
	final := mode == "FINAL"
	if final {
		if err := requireWrite(ctx, "InspectionProtocol", "update", p.cc); err != nil {
			return sdk.Response{}, err
		}
		if p.status == statusDone {
			return sdk.Response{}, crud.Invalid("Protokoll %s ist bereits abgeschlossen (%s)", p.id, p.document)
		}
	}
	data, defects, err := m.sheetData(ctx, p, t, mode == "BLANK")
	if err != nil {
		return sdk.Response{}, err
	}
	if final {
		if missing := data["missing"].([]string); len(missing) > 0 {
			return sdk.Response{}, crud.Invalid("Zählerstand fehlt: %s – eintragen oder die Zeile löschen", strings.Join(missing, ", "))
		}
	}
	tmpl := t.template
	if tmpl == "" {
		if opts := reportapi.Templates(ctx, m.services, usageSheet); len(opts) > 0 {
			tmpl = opts[0].Value
		}
	}
	if tmpl == "" {
		return sdk.Response{}, crud.Invalid("Keine Vorlage mit der Verwendung %s (Vorlagen → „Beispiele anlegen …“ legt PROTOCOL_SHEET an)", usageSheet)
	}
	if !strings.Contains(tmpl, "|") {
		tmpl += "|de"
	}
	out, err := reportapi.Render(ctx, m.services, p.cc, tmpl, "Protokoll "+p.id, data)
	if err != nil {
		return sdk.Response{}, err
	}
	if !final {
		return sdk.Response{Payload: out}, nil
	}
	file, ok := out["download"].(metamodel.Content)
	if !ok || file.Content == "" {
		return sdk.Response{}, crud.Invalid("Protokoll %s: PDF nicht fertig – %s", p.id, crud.Str(out["message"]))
	}
	var done []string
	// 1. Zählerstände als Ablesung (je Zeile vermerkt, ein zweiter Lauf bucht nur den Rest)
	if t.readingType != "" {
		n, err := m.writeReadings(ctx, p, t)
		if err != nil {
			return sdk.Response{}, err
		}
		if n > 0 {
			done = append(done, fmt.Sprintf("%d Zählerstände als Ablesung gebucht", n))
		}
	}
	// 2. PDF ins DMS
	document := p.document
	if t.docType != "" && document == "" {
		refs := []any{map[string]any{"object": "InspectionProtocol", "key": p.cc + "|" + p.id}, map[string]any{"object": "RentObject", "key": p.cc + "|" + p.object}}
		if p.contract != "" {
			refs = append(refs, map[string]any{"object": "Contract", "key": p.cc + "|" + p.contract})
		}
		resp, err := m.services.Call(ctx, "DmsDocument", "store", map[string]any{"company_code": p.cc, "content": file.Content,
			"file_name": p.id + ".pdf", "content_type": "application/pdf", "doc_type": t.docType, "doc_date": p.day,
			"title": p.title, "partner_id": p.partner, "refs": refs})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Ablage im DMS: %v", err)
		}
		var d struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		_ = sdk.Decode(resp.Payload, &d)
		document = d.Name
		if document == "" {
			document = d.ID
		}
		done = append(done, "im DMS abgelegt als "+document)
	}
	// 3. Mängel als Vorgang
	process := p.process
	if t.defectType != "" && len(defects) > 0 && process == "" {
		desc := "Mängel laut " + t.name + " " + p.id + " vom " + germanDate(p.day) + ":\n- " + strings.Join(defects, "\n- ")
		resp, err := m.services.Call(ctx, "Process", "create", map[string]any{"data": map[string]any{"company_code": p.cc, "process_type": t.defectType,
			"subject": fmt.Sprintf("Mängel aus %s %s", t.name, p.id), "description": desc, "object_id": p.object,
			"contract_id": nilIfEmpty(p.contract), "partner_id": nilIfEmpty(p.partner), "event_date": p.day}})
		if err != nil {
			return sdk.Response{}, crud.Invalid("Vorgang zu den Mängeln: %v", err)
		}
		var pr struct {
			ID string `json:"process_id"`
		}
		_ = sdk.Decode(resp.Payload, &pr)
		process = pr.ID
		done = append(done, fmt.Sprintf("%d Mängel als Vorgang %s", len(defects), process))
	}
	if _, err := m.db.Exec(ctx, `UPDATE inspection__protocol SET status = ?, document = ?, process_id = ?, done_at = ?, done_by = ?, changed_at = ?, changed_by = ?
		WHERE company_code = ? AND protocol_id = ?`, statusDone, nilIfEmpty(document), nilIfEmpty(process), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID),
		now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), p.cc, p.id); err != nil {
		return sdk.Response{}, err
	}
	msg := "Protokoll " + p.id + " abgeschlossen"
	if len(done) > 0 {
		msg += ": " + strings.Join(done, ", ")
	}
	out["message"] = msg
	return sdk.Response{Payload: out}, nil
}

// writeReadings: Stände als Ablesung (techobj MeterReading.create) mit der
// Ableseart der Protokollart; gebuchte Zeilen werden vermerkt.
func (m *Module) writeReadings(ctx context.Context, p *protocolRec, t *protocolType) (int, error) {
	res, err := m.db.Query(ctx, `SELECT point_id, register_code, COALESCE(equipment_id, ''), value, designation FROM inspection__meter
		WHERE company_code = ? AND protocol_id = ? AND value IS NOT NULL AND reading_written = ? ORDER BY sort_order`, p.cc, p.id, false)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res.Rows {
		if crud.Str(r[2]) == "" {
			continue
		}
		_, err := m.services.Call(ctx, "MeterReading", "create", map[string]any{"data": map[string]any{"company_code": p.cc, "equipment_id": r[2],
			"register_code": r[1], "read_on": p.day, "reading_type": t.readingType, "value": r[3], "point_id": r[0],
			"reader": t.name, "source": "Protokoll " + p.id}})
		if err != nil && !strings.Contains(err.Error(), "already exists") && !strings.Contains(err.Error(), "gibt es bereits") {
			return n, crud.Invalid("Ablesung %s: %v", crud.Str(r[4]), err)
		}
		if _, err := m.db.Exec(ctx, "UPDATE inspection__meter SET reading_written = ? WHERE company_code = ? AND protocol_id = ? AND point_id = ? AND register_code = ?",
			true, p.cc, p.id, r[0], r[1]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// sheetData: Daten inspection.protocol/1 und die Mängel als Text. blank: Formular
// ohne Zustand, Feststellungen, Stände und Ergebnisse.
func (m *Module) sheetData(ctx context.Context, p *protocolRec, t *protocolType, blank bool) (map[string]any, []string, error) {
	obj, _ := m.rentObject(ctx, p.cc, p.object)
	data := map[string]any{"format": sheetFormat, "protocol_id": p.id, "type": t.code, "type_name": t.name, "title": p.title,
		"date": p.day, "place": p.place, "note": p.note, "intro": t.intro, "closing": t.closing, "blank": blank,
		"company": reportapi.Company(ctx, m.services, p.cc), "object": map[string]any{"id": p.object, "label": obj}}
	if p.contract != "" {
		c := map[string]any{"id": p.contract}
		if ci, err := m.contract(ctx, p.cc, p.contract); err == nil {
			c["designation"] = ci.Designation
		}
		if p.partner != "" {
			c["tenant"] = m.partnerName(ctx, p.partner)
		}
		data["contract"] = c
	}
	parts, err := m.db.Query(ctx, `SELECT r.name, x.name, x.present, x.signs, COALESCE(x.note, '') FROM inspection__participant x
		LEFT JOIN inspection__participant_role r ON r.company_code = x.company_code AND r.code = x.role
		WHERE x.company_code = ? AND x.protocol_id = ? ORDER BY x.line_no`, p.cc, p.id)
	if err != nil {
		return nil, nil, err
	}
	var participants, signers []any
	for _, r := range parts.Rows {
		e := map[string]any{"role": crud.Str(r[0]), "name": crud.Str(r[1]), "present": crud.AsBool(r[2]), "note": crud.Str(r[4])}
		participants = append(participants, e)
		if crud.AsBool(r[3]) {
			signers = append(signers, e)
		}
	}
	data["participants"], data["signers"] = participants, signers
	// Räume
	items, err := m.db.Query(ctx, `SELECT i.room_name, COALESCE(i.component_name, ''), COALESCE(i.description, ''), COALESCE(c.name, ''), COALESCE(i.remark, ''), i.is_defect
		FROM inspection__item i LEFT JOIN inspection__condition c ON c.company_code = i.company_code AND c.code = i.condition
		WHERE i.company_code = ? AND i.protocol_id = ? ORDER BY i.sort_order, i.line_no`, p.cc, p.id)
	if err != nil {
		return nil, nil, err
	}
	var rooms []any
	var cur map[string]any
	var defects []string
	var defectRows []any
	for _, r := range items.Rows {
		room := crud.Str(r[0])
		if cur == nil || cur["name"] != room {
			cur = map[string]any{"name": room, "items": []any{}}
			rooms = append(rooms, cur)
		}
		it := map[string]any{"component": crud.Str(r[1]), "description": crud.Str(r[2])}
		if !blank {
			it["condition"], it["remark"], it["defect"] = crud.Str(r[3]), crud.Str(r[4]), crud.AsBool(r[5])
			if crud.AsBool(r[5]) {
				what := room
				if c := crud.Str(r[1]); c != "" {
					what += ", " + c
				}
				text := what + ": " + crud.Str(r[3])
				if rm := crud.Str(r[4]); rm != "" {
					text += " – " + rm
				}
				defects = append(defects, text)
				defectRows = append(defectRows, map[string]any{"room": room, "component": crud.Str(r[1]), "condition": crud.Str(r[3]), "remark": crud.Str(r[4])})
			}
		}
		cur["items"] = append(cur["items"].([]any), it)
	}
	data["rooms"], data["defects"] = rooms, defectRows
	// Zähler
	ms, err := m.db.Query(ctx, `SELECT designation, COALESCE(meter_number, ''), COALESCE(location, ''), COALESCE(unit, ''), last_value, COALESCE(last_read_on, ''), value, COALESCE(note, '')
		FROM inspection__meter WHERE company_code = ? AND protocol_id = ? ORDER BY sort_order, point_id, register_code`, p.cc, p.id)
	if err != nil {
		return nil, nil, err
	}
	var meters []any
	missing := []string{}
	for _, r := range ms.Rows {
		e := map[string]any{"designation": crud.Str(r[0]), "meter_number": crud.Str(r[1]), "location": crud.Str(r[2]), "unit": crud.Str(r[3])}
		if r[4] != nil {
			e["last_value"], e["last_read_on"] = numberDE(toFloat(r[4])), date(r[5])
		}
		if !blank {
			if r[6] != nil {
				e["value"] = numberDE(toFloat(r[6]))
			} else {
				missing = append(missing, crud.Str(r[0]))
			}
			e["note"] = crud.Str(r[7])
		}
		meters = append(meters, e)
	}
	data["meters"], data["missing"] = meters, missing
	// Prüfliste
	cs, err := m.db.Query(ctx, `SELECT name, kind, COALESCE(value, ''), COALESCE(hint, ''), COALESCE(note, '') FROM inspection__check
		WHERE company_code = ? AND protocol_id = ? ORDER BY sort_order, item_code`, p.cc, p.id)
	if err != nil {
		return nil, nil, err
	}
	var checks []any
	for _, r := range cs.Rows {
		e := map[string]any{"name": crud.Str(r[0]), "kind": crud.Str(r[1]), "hint": crud.Str(r[3])}
		if !blank {
			e["value"], e["note"] = crud.Str(r[2]), crud.Str(r[4])
		}
		checks = append(checks, e)
	}
	data["checks"] = checks
	if blank {
		data["note"] = ""
	}
	return data, defects, nil
}

// numberDE: Zahl deutsch ohne überflüssige Nachkommastellen (1.234,5).
func numberDE(v float64) string {
	s := strconv.FormatFloat(v, 'f', -1, 64)
	intPart, frac, _ := strings.Cut(s, ".")
	neg := strings.HasPrefix(intPart, "-")
	intPart = strings.TrimPrefix(intPart, "-")
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
