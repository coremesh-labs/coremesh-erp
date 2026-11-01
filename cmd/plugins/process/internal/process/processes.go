package process

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Vorgangsnummer: <Kürzel der Art>-<Jahr der Eröffnung>-<laufend, 4-stellig>,
// z. B. WV-2026-0001. Eskalationsstufe: 0 keine, 1 heute fällig gemeldet,
// 2 überschritten gemeldet, 3 eskaliert (an Vertreter bzw. „Eskalation an“).

const processObject = "Process"

const (
	levelDue      = 1
	levelOverdue  = 2
	levelEscalate = 3
)

func (m *Module) process() *crud.Entity {
	return &crud.Entity{
		Object: processObject, Title: "Vorgänge", Icon: "icon-inbox", Table: "process__process", Section: "Vorgänge",
		Keys: []string{"company_code", "process_id"}, TitleField: "subject",
		Order:     "next_due IS NULL, next_due, process_id",
		Filters:   []string{"company_code", "view", "process_type", "status", "assignee", "perspective", "partner_id", "contract_id", "object_id", "entity_id", "asset_id"},
		FilterCtx: map[string]func(context.Context, any) (string, []any, error){"view": m.viewFilter},
		Search:    []string{"process_id", "subject", "description", "waiting_for"},
		Events:    true, CompanyCodeField: "company_code", EventFields: []string{"process_type", "status", "assignee", "next_due"},
		Fields: []crud.Field{
			{Key: "view", Label: "Sicht", Type: tText, Virtual: true, FilterOnly: true, Lookup: catalogLookup(viewObject)},
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Immutable: true, Lookup: lookupCC},
			{Key: "process_type", Label: "Vorgangsart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ProcessType")},
			{Key: "process_id", Label: "Vorgang", Type: tText, Listable: true, ReadOnly: true},
			{Key: "subject", Label: "Betreff", Type: tText, Required: true, Listable: true, Wide: true},
			{Key: "signal", Label: "Frist", Type: tText, Listable: true, ReadOnly: true, Virtual: true},
			{Key: "next_due", Label: "Nächste Frist", Short: "Fällig", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "next_step", Label: "Nächster Schritt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("ProcessStatus")},
			{Key: "priority", Label: "Priorität", Type: tText, Lookup: catalogLookup("ProcessPriority")},
			{Key: "assignee", Label: "Verantwortlich (leer = ich)", Short: "Verantwortlich", Type: tText, Listable: true, Lookup: crud.LookupUser},
			{Key: "deputy", Label: "Vertreter", Type: tText, Lookup: crud.LookupUser},
			{Key: "follow_up", Label: "Wiedervorlage bzw. Erinnerung (Pflicht, solange offen)", Short: "Wiedervorlage", Type: tDate},
			{Key: "waiting_for", Label: "Wartet auf (wer bzw. was; Pflicht bei „wartet …“)", Short: "Wartet auf", Type: tText},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "partner_id", Label: "Anfragender / Partner", Short: "Partner", Type: tText, Listable: true, Group: "Bezug", Lookup: lookupPartner},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Group: "Bezug", Lookup: lookupContract},
			{Key: "object_id", Label: "Mietobjekt / Einheit", Short: "Mietobjekt", Type: tText, Group: "Bezug", Lookup: lookupRentObject},
			{Key: "entity_id", Label: "Wirtschaftseinheit / WEG", Short: "WE", Type: tText, Group: "Bezug", Lookup: lookupEntity},
			{Key: "asset_id", Label: "Objekt (Wohnung, Auto, Person …)", Short: "Objekt", Type: tText, Group: "Bezug", Lookup: lookupAsset},
			{Key: "event_date", Label: "Stichtag (Bedeutung laut Vorgangsart, z. B. Auszug, Versammlung)", Short: "Stichtag", Type: tDate, Group: "Fristen"},
			{Key: "opened_on", Label: "Eröffnet am (leer = heute)", Short: "Eröffnet", Type: tDate, Group: "Fristen"},
			{Key: "ack_due", Label: "Eingangsbestätigung bis", Short: "Bestätigung bis", Type: tDate, Group: "Fristen"},
			{Key: "acknowledged_on", Label: "Eingang bestätigt am", Short: "Bestätigt", Type: tDate, Group: "Fristen"},
			{Key: "answer_due", Label: "Antwort bzw. Erledigung bis", Short: "Antwort bis", Type: tDate, Group: "Fristen"},
			{Key: "escalation", Label: "Eskalationsstufe (1 fällig, 2 überschritten, 3 eskaliert)", Short: "Stufe", Type: tNum, ReadOnly: true, Group: "Fristen"},
			{Key: "perspective", Label: "Perspektive", Type: tText, ReadOnly: true, Lookup: catalogLookup("ProcessPerspective")},
			{Key: "resolution", Label: "Erledigungsart (Pflicht beim Abschluss)", Short: "Erledigung", Type: tText, Group: "Abschluss", Lookup: catalogLookup("ProcessResolution")},
			{Key: "closed_on", Label: "Abgeschlossen am", Type: tDate, ReadOnly: true, Group: "Abschluss"},
			{Key: "closed_by", Label: "Abgeschlossen von", Type: tText, ReadOnly: true, Group: "Abschluss", Lookup: crud.LookupUser},
			{Key: "last_activity", Label: "Letzte Bewegung", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Eröffnet von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "entry_kind", Label: "Art des Eintrags", Type: tText, ActionOnly: true, Lookup: catalogLookup("ProcessEntryKind")},
			{Key: "entry_text", Label: "Text", Type: tArea, ActionOnly: true},
			{Key: "new_follow_up", Label: "Neue Wiedervorlage (leer = unverändert)", Type: tDate, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "schritte", Title: "Schritte", Relation: &metamodel.Relation{Object: stepObject, ForeignKey: "process_id",
				Match: match("process_id", "process_id"), Columns: []string{"step_no", "name", "due_date", "status", "done_on", "role_hint"}}},
			{Key: "partner", Title: "Partner", Relation: &metamodel.Relation{Object: partnerObject, ForeignKey: "process_id",
				Match: match("process_id", "process_id"), Columns: []string{"role_code", "partner_id", "note"}}},
			{Key: "verlauf", Title: "Verlauf", Relation: &metamodel.Relation{Object: entryObject, ForeignKey: "process_id",
				Match: match("process_id", "process_id"), Columns: []string{"entry_at", "kind", "text", "user_id"}}},
			{Key: "dokumente", Title: "Dokumente", Documents: true},
			{Key: "texte", Title: "Texte", Collapsed: true, Texts: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "note", Label: "Eintrag …", Record: true, Fields: []string{"entry_kind", "entry_text", "new_follow_up"}},
				Handle: m.noteAction},
			{ActionConfig: metamodel.ActionConfig{Name: "acknowledge", Label: "Eingang bestätigt", Record: true,
				Confirm: "Eingang heute bestätigt? Die Frist der Eingangsbestätigung entfällt."}, Handle: m.acknowledgeAction},
		},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, processObject, action, crud.Str(rec["company_code"]))
		},
		Validate:    m.checkProcess,
		AfterCreate: m.afterCreate,
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			return m.refresh(ctx, crud.Str(rec["company_code"]), crud.Str(rec["process_id"]))
		},
		Decorate: m.decorateProcess,
	}
}

// typeRow: was eine Vorgangsart für den Vorgang festlegt.
type typeRow struct {
	Name, Prefix, Perspective, EscalateTo string
	FollowUp, Ack, Answer, Idle, Escalate *int
}

func (m *Module) typeOf(ctx context.Context, cc, code string) (*typeRow, error) {
	row, err := m.one(ctx, `SELECT name, id_prefix, perspective, escalate_to, follow_up_days, ack_days, answer_days, idle_days, escalate_days, is_active
		FROM process__type WHERE company_code = ? AND code = ?`, cc, code)
	if err != nil {
		return nil, err
	}
	if row == nil || !crud.AsBool(row[9]) {
		return nil, crud.Invalid("Vorgangsart %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Vorgänge → Einstellungen → Vorgangsarten", code, cc)
	}
	opt := func(v any) *int {
		if v == nil || crud.Str(v) == "" {
			return nil
		}
		n := toInt(v)
		return &n
	}
	return &typeRow{Name: crud.Str(row[0]), Prefix: crud.Str(row[1]), Perspective: crud.Str(row[2]), EscalateTo: crud.Str(row[3]),
		FollowUp: opt(row[4]), Ack: opt(row[5]), Answer: opt(row[6]), Idle: opt(row[7]), Escalate: opt(row[8])}, nil
}

// statusKind: Art des Status aus dem Katalog (OPEN, WAITING, CLOSED).
func (m *Module) statusKind(ctx context.Context, cc, code string) (string, error) {
	row, err := m.one(ctx, "SELECT kind FROM process__status WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", crud.Invalid("Status %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Vorgänge → Einstellungen → Status", code, cc)
	}
	return crud.Str(row[0]), nil
}

// firstStatus: erster offener Status im Katalog (Standard beim Eröffnen).
func (m *Module) firstStatus(ctx context.Context, cc string) (string, error) {
	row, err := m.one(ctx, "SELECT code FROM process__status WHERE company_code = ? AND kind = ? AND is_active = ? ORDER BY sort_order, code", cc, kindOpen, true)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", crud.Invalid("Kein offener Status im Buchungskreis %s – Vorgänge → Einstellungen → Status", cc)
	}
	return crud.Str(row[0]), nil
}

func (m *Module) checkProcess(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	for _, k := range []string{"event_date", "opened_on", "ack_due", "acknowledged_on", "answer_due", "follow_up"} {
		d, err := dateOrNil(rec[k])
		if err != nil {
			return err
		}
		rec[k] = d
	}
	var typ *typeRow
	var err error
	if old == nil {
		// Erster Vorgang im Buchungskreis: Kataloge mit Vorschlagswerten.
		if _, err := m.setupCatalogs(ctx, cc); err != nil {
			return err
		}
		rec["process_type"] = trimUpper(rec["process_type"])
		if typ, err = m.typeOf(ctx, cc, crud.Str(rec["process_type"])); err != nil {
			return err
		}
		if rec["opened_on"] == nil {
			rec["opened_on"] = m.today()
		}
		opened := crud.Str(rec["opened_on"])
		id, err := m.nextID(ctx, cc, typ.Prefix, opened[:4])
		if err != nil {
			return err
		}
		rec["process_id"], rec["perspective"] = id, nilIfEmpty(typ.Perspective)
		if crud.Str(rec["status"]) == "" {
			if rec["status"], err = m.firstStatus(ctx, cc); err != nil {
				return err
			}
		}
		if crud.Str(rec["assignee"]) == "" {
			rec["assignee"] = nilIfEmpty(userOf(ctx))
		}
		days := func(n *int) any {
			if n == nil {
				return nil
			}
			return addDays(opened, *n)
		}
		if rec["follow_up"] == nil {
			if rec["follow_up"] = days(typ.FollowUp); rec["follow_up"] == nil {
				rec["follow_up"] = opened
			}
		}
		if rec["ack_due"] == nil {
			rec["ack_due"] = days(typ.Ack)
		}
		if rec["answer_due"] == nil {
			rec["answer_due"] = days(typ.Answer)
		}
		rec["created_by"], rec["escalation"] = nilIfEmpty(userOf(ctx)), 0
	}
	rec["status"] = trimUpper(rec["status"])
	kind, err := m.statusKind(ctx, cc, crud.Str(rec["status"]))
	if err != nil {
		return err
	}
	for _, c := range []struct{ field, table, label string }{{"priority", "priority", "Priorität"}, {"resolution", "resolution", "Erledigungsart"}} {
		rec[c.field] = nilIfEmpty(trimUpper(rec[c.field]))
		if err := m.requireCatalog(ctx, c.table, c.label, cc, rec[c.field]); err != nil {
			return err
		}
	}
	for _, k := range []string{"assignee", "deputy", "waiting_for", "partner_id", "contract_id", "object_id", "entity_id", "asset_id"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	if err := m.checkRefs(ctx, cc, rec, old); err != nil {
		return err
	}
	switch kind {
	case kindClosed:
		if rec["resolution"] == nil {
			return crud.Invalid("Erledigungsart ist beim Abschluss Pflicht")
		}
		if old != nil {
			if open, err := m.openRequiredSteps(ctx, cc, crud.Str(rec["process_id"])); err != nil {
				return err
			} else if len(open) > 0 {
				return crud.Invalid("Offene Pflichtschritte: %s – erst erledigen oder als „entfällt“ markieren", strings.Join(open, ", "))
			}
		}
		if rec["closed_on"] == nil || crud.Str(rec["closed_on"]) == "" {
			rec["closed_on"], rec["closed_by"] = m.today(), nilIfEmpty(userOf(ctx))
		}
	default:
		if rec["assignee"] == nil {
			return crud.Invalid("Verantwortlich ist Pflicht, solange der Vorgang offen ist")
		}
		if rec["follow_up"] == nil {
			return crud.Invalid("Wiedervorlage ist Pflicht, solange der Vorgang offen ist – damit nichts untergeht")
		}
		if kind == kindWaiting && rec["waiting_for"] == nil {
			return crud.Invalid("„Wartet auf“ ist Pflicht bei Status %s (wer bzw. was, mit Erinnerungsdatum in der Wiedervorlage)", rec["status"])
		}
		rec["closed_on"], rec["closed_by"] = nil, nil
	}
	rec["last_activity"], rec["changed_at"], rec["changed_by"] = m.stamp(), m.stamp(), nilIfEmpty(userOf(ctx))
	if old == nil {
		return nil
	}
	return m.logChanges(ctx, cc, rec, old, kind)
}

// checkRefs: Partner und Bezüge, soweit das zuständige Plugin sie kennt (ein
// fehlendes Plugin verhindert die Eingabe nicht).
func (m *Module) checkRefs(ctx context.Context, cc string, rec, old crud.Record) error {
	refs := []struct{ field, object, label string }{
		{"partner_id", "BusinessPartner", "Partner"}, {"contract_id", "Contract", "Vertrag"}, {"object_id", "RentObject", "Mietobjekt"},
		{"entity_id", "BusinessEntity", "Wirtschaftseinheit"}, {"asset_id", "Asset", "Objekt"},
	}
	for _, r := range refs {
		v := crud.Str(rec[r.field])
		if v == "" || (old != nil && v == crud.Str(old[r.field])) {
			continue
		}
		id := cc + "|" + v
		if r.object == "BusinessPartner" {
			id = v
		}
		if _, err := m.services.Call(ctx, r.object, "get", map[string]any{"id": id}); errors.Is(err, sdk.ErrNotFound) {
			return crud.Invalid("%s %s gibt es nicht", r.label, v)
		}
	}
	return nil
}

// nextID: <Kürzel>-<Jahr>-<nnnn> mit der nächsten Nummer des Jahres.
func (m *Module) nextID(ctx context.Context, cc, prefix, year string) (string, error) {
	stem := prefix + "-" + year + "-"
	res, err := m.db.Query(ctx, "SELECT process_id FROM process__process WHERE company_code = ? AND process_id LIKE ?", cc, stem+"%")
	if err != nil {
		return "", err
	}
	n := 0
	for _, r := range res.Rows {
		if k, err := strconv.Atoi(strings.TrimPrefix(crud.Str(r[0]), stem)); err == nil && k > n {
			n = k
		}
	}
	return fmt.Sprintf("%s%04d", stem, n+1), nil
}

// afterCreate: Schritte aus der Vorlage, Verlauf, Hinweis an den Verantwortlichen,
// Journal, nächste Frist.
func (m *Module) afterCreate(ctx context.Context, rec crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
	res, err := m.db.Query(ctx, `SELECT step_no, name, checklist, role_hint, due_basis, due_days, required FROM process__type_step
		WHERE company_code = ? AND process_type = ? AND is_active = ? ORDER BY step_no`, cc, rec["process_type"], true)
	if err != nil {
		return err
	}
	opened, event := crud.Str(rec["opened_on"]), dateStr(rec["event_date"])
	for i, r := range res.Rows {
		basis := crud.Str(r[4])
		due := stepDue(basis, r[5], opened, event, "")
		if basis == basisPrev && i == 0 {
			due = stepDue(basisOpen, r[5], opened, event, "")
		}
		if err := m.insertRow(ctx, "step", map[string]any{"company_code": cc, "process_id": id, "step_no": r[0], "name": r[1],
			"checklist": r[2], "role_hint": r[3], "due_basis": basis, "due_days": r[5], "required": crud.AsBool(r[6]),
			"due_date": nilIfEmpty(due), "status": stepOpen}); err != nil {
			return err
		}
	}
	if err := m.systemEntry(ctx, cc, id, "Vorgang eröffnet"); err != nil {
		return err
	}
	if err := m.journal(ctx, cc, id, "CREATED", map[string]any{"process_type": rec["process_type"], "subject": rec["subject"],
		"assignee": rec["assignee"], "status": rec["status"]}); err != nil {
		return err
	}
	if a := crud.Str(rec["assignee"]); a != "" && a != userOf(ctx) {
		if err := m.notify(ctx, cc, a, id, noticeAssigned, "Neuer Vorgang für Sie: "+crud.Str(rec["subject"])); err != nil {
			return err
		}
	}
	return m.refresh(ctx, cc, id)
}

// logChanges: Status, Verantwortlicher und Stichtag im Verlauf und im Journal.
func (m *Module) logChanges(ctx context.Context, cc string, rec, old crud.Record, kind string) error {
	id := crud.Str(rec["process_id"])
	if s, o := crud.Str(rec["status"]), crud.Str(old["status"]); s != o {
		oldKind, _ := m.statusKind(ctx, cc, o)
		event := "STATUS"
		switch {
		case kind == kindClosed:
			event = "CLOSED"
		case oldKind == kindClosed:
			event = "REOPENED"
		}
		if err := m.systemEntry(ctx, cc, id, fmt.Sprintf("Status %s → %s", m.name(ctx, "status", cc, o), m.name(ctx, "status", cc, s))); err != nil {
			return err
		}
		if err := m.journal(ctx, cc, id, event, map[string]any{"from": o, "to": s, "resolution": rec["resolution"]}); err != nil {
			return err
		}
	}
	if a, o := crud.Str(rec["assignee"]), crud.Str(old["assignee"]); a != o && a != "" {
		if err := m.systemEntry(ctx, cc, id, "Verantwortlich: "+a); err != nil {
			return err
		}
		if err := m.journal(ctx, cc, id, "ASSIGNED", map[string]any{"from": o, "to": a}); err != nil {
			return err
		}
		if a != userOf(ctx) {
			if err := m.notify(ctx, cc, a, id, noticeAssigned, "Vorgang an Sie übergeben: "+crud.Str(rec["subject"])); err != nil {
				return err
			}
		}
	}
	if e, o := dateStr(rec["event_date"]), dateStr(old["event_date"]); e != o {
		res, err := m.db.Query(ctx, "SELECT step_no, due_days FROM process__step WHERE company_code = ? AND process_id = ? AND due_basis = ? AND status = ?",
			cc, id, basisEvent, stepOpen)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			if _, err := m.db.Exec(ctx, "UPDATE process__step SET due_date = ? WHERE company_code = ? AND process_id = ? AND step_no = ?",
				nilIfEmpty(stepDue(basisEvent, r[1], "", e, "")), cc, id, r[0]); err != nil {
				return err
			}
		}
		if err := m.systemEntry(ctx, cc, id, "Stichtag: "+orDash(e)); err != nil {
			return err
		}
	}
	return m.journal(ctx, cc, id, "UPDATED", nil)
}

func orDash(s string) string {
	if s == "" {
		return "–"
	}
	return s
}

// name: Bezeichnung eines Katalogeintrags (sonst der Schlüssel).
func (m *Module) name(ctx context.Context, table, cc, code string) string {
	if row, err := m.one(ctx, "SELECT name FROM process__"+table+" WHERE company_code = ? AND code = ?", cc, code); err == nil && row != nil {
		return crud.Str(row[0])
	}
	return code
}

// refresh: nächste Frist und nächster Schritt; die Eskalationsstufe fällt zurück,
// sobald die Frist wieder in der Zukunft liegt.
func (m *Module) refresh(ctx context.Context, cc, id string) error {
	row, err := m.one(ctx, `SELECT p.follow_up, p.ack_due, p.acknowledged_on, p.answer_due, s.kind, p.next_due, p.escalation
		FROM process__process p LEFT JOIN process__status s ON s.company_code = p.company_code AND s.code = p.status
		WHERE p.company_code = ? AND p.process_id = ?`, cc, id)
	if err != nil || row == nil {
		return err
	}
	next, step := "", ""
	if crud.Str(row[4]) != kindClosed {
		consider := func(d string) {
			if d != "" && (next == "" || d < next) {
				next = d
			}
		}
		consider(dateStr(row[0]))
		if dateStr(row[2]) == "" {
			consider(dateStr(row[1]))
		}
		consider(dateStr(row[3]))
		s, err := m.one(ctx, `SELECT name, due_date FROM process__step WHERE company_code = ? AND process_id = ? AND status = ?
			ORDER BY due_date IS NULL, due_date, step_no`, cc, id, stepOpen)
		if err != nil {
			return err
		}
		if s != nil {
			step = crud.Str(s[0])
			consider(dateStr(s[1]))
		}
	}
	level := toInt(row[6])
	if today := m.today(); next == "" || next > today || (next != dateStr(row[5]) && next >= today) {
		level = 0
	}
	_, err = m.db.Exec(ctx, "UPDATE process__process SET next_due = ?, next_step = ?, escalation = ? WHERE company_code = ? AND process_id = ?",
		nilIfEmpty(next), nilIfEmpty(step), level, cc, id)
	return err
}

// touch: letzte Bewegung jetzt.
func (m *Module) touch(ctx context.Context, cc, id string) error {
	_, err := m.db.Exec(ctx, "UPDATE process__process SET last_activity = ? WHERE company_code = ? AND process_id = ?", m.stamp(), cc, id)
	return err
}

func (m *Module) decorateProcess(ctx context.Context, rec crud.Record) error {
	due, today := dateStr(rec["next_due"]), m.today()
	switch {
	case due == "":
		rec["signal"] = ""
	case due < today:
		rec["signal"] = "überfällig"
	case due == today:
		rec["signal"] = "heute"
	default:
		rec["signal"] = ""
	}
	if toInt(rec["escalation"]) >= levelEscalate {
		rec["signal"] = "eskaliert"
	}
	if name := m.partnerName(ctx, crud.Str(rec["partner_id"])); name != "" {
		setLabel(rec, "partner_id", name)
	}
	return nil
}

// partnerName: Name 1 und 2 des Partners; leer, wenn das Partnermodul fehlt
// oder der Benutzer den Partner nicht lesen darf.
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// --- Aktionen am Vorgang ------------------------------------------------------------

type recordInput struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"data"`
}

// processKey: Buchungskreis und Vorgang aus der ID „1000|WV-2026-0001“.
func processKey(id string) (string, string, error) {
	cc, pid, ok := strings.Cut(id, "|")
	if !ok || cc == "" || pid == "" {
		return "", "", crud.Invalid("Vorgang %q: erwartet Buchungskreis|Vorgang", id)
	}
	return cc, pid, nil
}

// noteAction: Process.note {id, data: {entry_kind, entry_text, new_follow_up}}.
func (m *Module) noteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, processObject, "note", cc); err != nil {
		return sdk.Response{}, err
	}
	text := strings.TrimSpace(crud.Str(in.Data["entry_text"]))
	follow, err := dateOrNil(in.Data["new_follow_up"])
	if err != nil {
		return sdk.Response{}, err
	}
	if text == "" && follow == nil {
		return sdk.Response{}, crud.Invalid("Text oder neue Wiedervorlage angeben")
	}
	kind := trimUpper(in.Data["entry_kind"])
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.mustExist(ctx, cc, id); err != nil {
			return err
		}
		if kind == "" {
			if kind, err = m.firstEntryKind(ctx, cc); err != nil {
				return err
			}
		}
		if follow != nil {
			if _, err := m.db.Exec(ctx, "UPDATE process__process SET follow_up = ? WHERE company_code = ? AND process_id = ?", follow, cc, id); err != nil {
				return err
			}
			if text == "" {
				text = "Wiedervorlage " + crud.Str(follow)
			} else {
				text += " (Wiedervorlage " + crud.Str(follow) + ")"
			}
		}
		if err := m.addEntry(ctx, cc, id, kind, text, userOf(ctx)); err != nil {
			return err
		}
		return m.refresh(ctx, cc, id)
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Eintrag gespeichert"}}, nil
}

// acknowledgeAction: Process.acknowledge {id} – Eingang heute bestätigt.
func (m *Module) acknowledgeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, processObject, "acknowledge", cc); err != nil {
		return sdk.Response{}, err
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.mustExist(ctx, cc, id); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, "UPDATE process__process SET acknowledged_on = ?, last_activity = ? WHERE company_code = ? AND process_id = ?",
			m.today(), m.stamp(), cc, id); err != nil {
			return err
		}
		if err := m.systemEntry(ctx, cc, id, "Eingang bestätigt"); err != nil {
			return err
		}
		if err := m.journal(ctx, cc, id, "ACKNOWLEDGED", nil); err != nil {
			return err
		}
		return m.refresh(ctx, cc, id)
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Eingang bestätigt"}}, nil
}

func (m *Module) mustExist(ctx context.Context, cc, id string) error {
	row, err := m.one(ctx, "SELECT 1 FROM process__process WHERE company_code = ? AND process_id = ?", cc, id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%w: Vorgang %s im Buchungskreis %s", sdk.ErrNotFound, id, cc)
	}
	return nil
}
