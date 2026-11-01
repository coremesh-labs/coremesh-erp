package process

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Schritte, Partner und Verlauf eines Vorgangs; Hinweise in der App und das
// Ereignisjournal.

const (
	stepObject    = "ProcessStep"
	partnerObject = "ProcessPartner"
	entryObject   = "ProcessEntry"
	noticeObject  = "ProcessNotice"
	eventObject   = "ProcessEvent"
)

// Zustand eines Schritts (technisch).
const (
	stepOpen    = "OPEN"
	stepDone    = "DONE"
	stepSkipped = "SKIPPED"
)

// Arten der Hinweise (technisch, erzeugt vom Modul).
const (
	noticeAssigned  = "ASSIGNED"
	noticeDue       = "DUE"
	noticeOverdue   = "OVERDUE"
	noticeEscalated = "ESCALATED"
	noticeIdle      = "IDLE"
)

var (
	stepStates  = []metamodel.Option{{Value: stepOpen, Label: "offen"}, {Value: stepDone, Label: "erledigt"}, {Value: stepSkipped, Label: "entfällt"}}
	noticeKinds = []metamodel.Option{{Value: noticeAssigned, Label: "zugewiesen"}, {Value: noticeDue, Label: "heute fällig"},
		{Value: noticeOverdue, Label: "überschritten"}, {Value: noticeEscalated, Label: "eskaliert"}, {Value: noticeIdle, Label: "ohne Bewegung"}}
)

// stepDue: Frist eines Schritts ("" = noch offen, z. B. ohne Stichtag).
func stepDue(basis string, days any, opened, event, prevDone string) string {
	n := 0
	if days != nil && crud.Str(days) != "" {
		n = toInt(days)
	}
	switch basis {
	case basisOpen:
		if opened != "" {
			return addDays(opened, n)
		}
	case basisEvent:
		if event != "" {
			return addDays(event, n)
		}
	case basisPrev:
		if prevDone != "" {
			return addDays(prevDone, n)
		}
	}
	return ""
}

// --- Schritte -----------------------------------------------------------------------

func (m *Module) step() *crud.Entity {
	return &crud.Entity{
		Object: stepObject, Title: "Schritte", Icon: "icon-list", Table: "process__step", Section: "Vorgänge",
		Keys: []string{"company_code", "process_id", "step_no"}, Order: "company_code, process_id, step_no", TitleField: "name",
		Filters: []string{"company_code", "process_id", "status"}, Search: []string{"name", "checklist", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Immutable: true, Lookup: lookupCC},
			{Key: "process_id", Label: "Vorgang", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupProcess},
			{Key: "step_no", Label: "Schritt (Nummer)", Short: "Schritt", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "status", Label: "Stand", Type: metamodel.TypeSelect, Listable: true, ReadOnly: true, Options: stepStates},
			{Key: "due_date", Label: "Frist (leer = aus der Grundlage)", Short: "Frist", Type: tDate, Listable: true},
			{Key: "due_basis", Label: "Frist ab", Type: metamodel.TypeSelect, Options: dueBases},
			{Key: "due_days", Label: "Frist in Tagen", Short: "Tage", Type: tNum},
			{Key: "required", Label: "Pflicht vor dem Abschluss", Short: "Pflicht", Type: tBool},
			{Key: "role_hint", Label: "Zuständig (Hinweis)", Short: "Zuständig", Type: tText},
			{Key: "checklist", Label: "Checkliste", Type: tArea},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "done_on", Label: "Erledigt am", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "done_by", Label: "Erledigt von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "step_note", Label: "Bemerkung zum Abschluss des Schritts", Type: tArea, ActionOnly: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "done", Label: "Erledigt …", Record: true, Fields: []string{"step_note"}}, Handle: m.stepAction(stepDone)},
			{ActionConfig: metamodel.ActionConfig{Name: "skip", Label: "Entfällt …", Record: true, Fields: []string{"step_note"}}, Handle: m.stepAction(stepSkipped)},
			{ActionConfig: metamodel.ActionConfig{Name: "reopen", Label: "Wieder öffnen", Record: true, Confirm: "Schritt wieder öffnen?"}, Handle: m.stepAction(stepOpen)},
		},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, processObject, action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkStep,
		AfterCreate: func(ctx context.Context, rec crud.Record) error {
			return m.afterStep(ctx, rec, "Schritt ergänzt: "+crud.Str(rec["name"]))
		},
		AfterUpdate: func(ctx context.Context, rec crud.Record) error { return m.afterStep(ctx, rec, "") },
	}
}

func (m *Module) checkStep(ctx context.Context, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
	if toInt(rec["step_no"]) <= 0 {
		return crud.Invalid("Schrittnummer muss größer 0 sein")
	}
	if crud.Str(rec["due_basis"]) == "" {
		rec["due_basis"] = basisOpen
	}
	if err := checkDue(rec); err != nil {
		return err
	}
	d, err := dateOrNil(rec["due_date"])
	if err != nil {
		return err
	}
	rec["due_date"] = d
	if old == nil {
		row, err := m.one(ctx, "SELECT opened_on, event_date FROM process__process WHERE company_code = ? AND process_id = ?", cc, id)
		if err != nil {
			return err
		}
		if row == nil {
			return crud.Invalid("Vorgang %s gibt es nicht", id)
		}
		rec["status"] = stepOpen
		if rec["due_date"] == nil {
			rec["due_date"] = nilIfEmpty(stepDue(crud.Str(rec["due_basis"]), rec["due_days"], dateStr(row[0]), dateStr(row[1]), m.today()))
		}
	}
	return nil
}

func (m *Module) afterStep(ctx context.Context, rec crud.Record, text string) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
	if text != "" {
		if err := m.systemEntry(ctx, cc, id, text); err != nil {
			return err
		}
	}
	if err := m.touch(ctx, cc, id); err != nil {
		return err
	}
	return m.refresh(ctx, cc, id)
}

// openRequiredSteps: offene Pflichtschritte (Nummer und Bezeichnung).
func (m *Module) openRequiredSteps(ctx context.Context, cc, id string) ([]string, error) {
	res, err := m.db.Query(ctx, "SELECT step_no, name FROM process__step WHERE company_code = ? AND process_id = ? AND status = ? AND required = ? ORDER BY step_no",
		cc, id, stepOpen, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res.Rows {
		out = append(out, fmt.Sprintf("%d %s", toInt(r[0]), crud.Str(r[1])))
	}
	return out, nil
}

// stepAction: ProcessStep.done / skip / reopen {id: "1000|WV-2026-0001|10", data: {step_note}}.
// Nach „erledigt“ bzw. „entfällt“ bekommt der nächste offene Schritt mit Frist
// „ab dem vorigen Schritt“ seine Frist.
func (m *Module) stepAction(state string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in recordInput
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		parts := strings.Split(in.ID, "|")
		if len(parts) != 3 {
			return sdk.Response{}, crud.Invalid("Schritt %q: erwartet Buchungskreis|Vorgang|Schritt", in.ID)
		}
		cc, id, no := parts[0], parts[1], toInt(parts[2])
		if err := requireWrite(ctx, processObject, "update", cc); err != nil {
			return sdk.Response{}, err
		}
		added := strings.TrimSpace(crud.Str(in.Data["step_note"]))
		note, msg := added, ""
		err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
			row, err := m.one(ctx, "SELECT name, status, note FROM process__step WHERE company_code = ? AND process_id = ? AND step_no = ?", cc, id, no)
			if err != nil {
				return err
			}
			if row == nil {
				return fmt.Errorf("%w: Schritt %d im Vorgang %s", sdk.ErrNotFound, no, id)
			}
			name, cur := crud.Str(row[0]), crud.Str(row[1])
			if prev := crud.Str(row[2]); note != "" && prev != "" {
				note = prev + "\n" + note
			} else if note == "" {
				note = prev
			}
			if cur == state {
				return crud.Invalid("Schritt %d %s ist schon %s", no, name, optionLabel(stepStates, state))
			}
			var doneOn, doneBy any
			if state != stepOpen {
				doneOn, doneBy = m.today(), nilIfEmpty(userOf(ctx))
			}
			if _, err := m.db.Exec(ctx, "UPDATE process__step SET status = ?, done_on = ?, done_by = ?, note = ? WHERE company_code = ? AND process_id = ? AND step_no = ?",
				state, doneOn, doneBy, nilIfEmpty(note), cc, id, no); err != nil {
				return err
			}
			if state != stepOpen {
				next, err := m.one(ctx, `SELECT step_no, due_basis, due_days FROM process__step WHERE company_code = ? AND process_id = ? AND step_no > ? AND status = ?
					ORDER BY step_no`, cc, id, no, stepOpen)
				if err != nil {
					return err
				}
				if next != nil && crud.Str(next[1]) == basisPrev {
					if _, err := m.db.Exec(ctx, "UPDATE process__step SET due_date = ? WHERE company_code = ? AND process_id = ? AND step_no = ?",
						stepDue(basisPrev, next[2], "", "", m.today()), cc, id, next[0]); err != nil {
						return err
					}
				}
			}
			msg = fmt.Sprintf("Schritt %d %s: %s", no, name, optionLabel(stepStates, state))
			text := msg
			if added != "" {
				text += " – " + added
			}
			if err := m.systemEntry(ctx, cc, id, text); err != nil {
				return err
			}
			event := map[string]string{stepDone: "STEP_DONE", stepSkipped: "STEP_SKIPPED", stepOpen: "STEP_REOPENED"}[state]
			if err := m.journal(ctx, cc, id, event, map[string]any{"step_no": no, "name": name}); err != nil {
				return err
			}
			if err := m.touch(ctx, cc, id); err != nil {
				return err
			}
			return m.refresh(ctx, cc, id)
		})
		if err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: map[string]any{"message": msg}}, nil
	}
}

func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}

// --- Partner ------------------------------------------------------------------------

func (m *Module) partner() *crud.Entity {
	return &crud.Entity{
		Object: partnerObject, Title: "Partner am Vorgang", Icon: "icon-users", Table: "process__partner", Section: "Vorgänge",
		Keys: []string{"company_code", "process_id", "role_code", "partner_id"}, Order: "company_code, process_id, role_code, partner_id",
		Filters: []string{"company_code", "process_id", "role_code", "partner_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Immutable: true, Lookup: lookupCC},
			{Key: "process_id", Label: "Vorgang", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupProcess},
			{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ProcessRole")},
			{Key: "partner_id", Label: "Partner", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupPartner},
			{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, processObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			rec["role_code"], rec["partner_id"] = trimUpper(rec["role_code"]), strings.TrimSpace(crud.Str(rec["partner_id"]))
			if old != nil {
				return nil
			}
			if err := m.mustExist(ctx, cc, crud.Str(rec["process_id"])); err != nil {
				return err
			}
			if ok, err := m.catalogHas(ctx, "role", cc, crud.Str(rec["role_code"])); err != nil {
				return err
			} else if !ok {
				return crud.Invalid("Rolle %s gibt es im Buchungskreis %s nicht (oder inaktiv)", rec["role_code"], cc)
			}
			return m.checkRefs(ctx, cc, crud.Record{"partner_id": rec["partner_id"]}, nil)
		},
		AfterCreate: func(ctx context.Context, rec crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
			label := strings.TrimSpace(crud.Str(rec["partner_id"]) + " " + m.partnerName(ctx, crud.Str(rec["partner_id"])))
			if err := m.systemEntry(ctx, cc, id, m.name(ctx, "role", cc, crud.Str(rec["role_code"]))+": "+label); err != nil {
				return err
			}
			return m.touch(ctx, cc, id)
		},
		Decorate: func(ctx context.Context, rec crud.Record) error {
			if name := m.partnerName(ctx, crud.Str(rec["partner_id"])); name != "" {
				setLabel(rec, "partner_id", name)
			}
			return nil
		},
	}
}

// --- Verlauf ------------------------------------------------------------------------

func (m *Module) entry() *crud.Entity {
	return &crud.Entity{
		Object: entryObject, Title: "Verlauf", Icon: "icon-message", Table: "process__entry", Section: "Vorgänge", CreateOnly: true,
		Keys: []string{"company_code", "process_id", "entry_no"}, Order: "company_code, process_id, entry_no DESC",
		Filters: []string{"company_code", "process_id", "kind", "user_id"}, Search: []string{"text"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Immutable: true, Lookup: lookupCC},
			{Key: "process_id", Label: "Vorgang", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupProcess},
			{Key: "entry_no", Label: "Nr.", Type: tNum, ReadOnly: true},
			{Key: "entry_at", Label: "Zeitpunkt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "kind", Label: "Art", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("ProcessEntryKind")},
			{Key: "text", Label: "Text", Type: tArea, Listable: true},
			{Key: "user_id", Label: "Benutzer", Type: tText, Listable: true, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, processObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
			if err := m.mustExist(ctx, cc, id); err != nil {
				return err
			}
			rec["kind"] = trimUpper(rec["kind"])
			if err := m.requireCatalog(ctx, "entry_kind", "Art des Eintrags", cc, rec["kind"]); err != nil {
				return err
			}
			no, err := m.nextEntryNo(ctx, cc, id)
			if err != nil {
				return err
			}
			rec["entry_no"], rec["entry_at"], rec["user_id"] = no, m.stamp(), nilIfEmpty(userOf(ctx))
			return nil
		},
		AfterCreate: func(ctx context.Context, rec crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["process_id"])
			if err := m.journal(ctx, cc, id, "ENTRY", map[string]any{"kind": rec["kind"], "text": rec["text"]}); err != nil {
				return err
			}
			return m.touch(ctx, cc, id)
		},
	}
}

func (m *Module) nextEntryNo(ctx context.Context, cc, id string) (int, error) {
	row, err := m.one(ctx, "SELECT COALESCE(MAX(entry_no), 0) FROM process__entry WHERE company_code = ? AND process_id = ?", cc, id)
	if err != nil {
		return 0, err
	}
	return toInt(row[0]) + 1, nil
}

// addEntry: Eintrag im Verlauf, letzte Bewegung und Journal (für Aktionen).
func (m *Module) addEntry(ctx context.Context, cc, id, kind, text, user string) error {
	no, err := m.nextEntryNo(ctx, cc, id)
	if err != nil {
		return err
	}
	if err := m.insertRow(ctx, "entry", map[string]any{"company_code": cc, "process_id": id, "entry_no": no, "kind": kind,
		"entry_at": m.stamp(), "text": nilIfEmpty(text), "user_id": nilIfEmpty(user)}); err != nil {
		return err
	}
	if err := m.journal(ctx, cc, id, "ENTRY", map[string]any{"kind": kind, "text": text}); err != nil {
		return err
	}
	return m.touch(ctx, cc, id)
}

// systemEntry: Systemeintrag im Verlauf (Art mit „vom System geschrieben“).
func (m *Module) systemEntry(ctx context.Context, cc, id, text string) error {
	row, err := m.one(ctx, "SELECT code FROM process__entry_kind WHERE company_code = ? AND is_system = ? AND is_active = ? ORDER BY sort_order, code", cc, true, true)
	if err != nil {
		return err
	}
	if row == nil {
		return crud.Invalid("Keine Art „vom System geschrieben“ im Buchungskreis %s – Vorgänge → Einstellungen → Arten der Verlaufseinträge", cc)
	}
	no, err := m.nextEntryNo(ctx, cc, id)
	if err != nil {
		return err
	}
	return m.insertRow(ctx, "entry", map[string]any{"company_code": cc, "process_id": id, "entry_no": no, "kind": crud.Str(row[0]),
		"entry_at": m.stamp(), "text": text, "user_id": nilIfEmpty(sdk.CallFromContext(ctx).UserID)})
}

// firstEntryKind: erste Art, die nicht vom System ist (Standard für Einträge).
func (m *Module) firstEntryKind(ctx context.Context, cc string) (string, error) {
	row, err := m.one(ctx, "SELECT code FROM process__entry_kind WHERE company_code = ? AND is_system = ? AND is_active = ? ORDER BY sort_order, code", cc, false, true)
	if err != nil {
		return "", err
	}
	if row == nil {
		return "", crud.Invalid("Keine Art der Verlaufseinträge im Buchungskreis %s", cc)
	}
	return crud.Str(row[0]), nil
}

// --- Hinweise in der App ------------------------------------------------------------

func (m *Module) notice() *crud.Entity {
	return &crud.Entity{
		Object: noticeObject, Title: "Meine Hinweise", Icon: "icon-bell", Table: "process__notice", Section: "Vorgänge", NoCreate: true,
		Keys: []string{"notice_id"}, Order: "notice_id DESC", StatusField: "is_open", EndLabel: "Gelesen", EndConfirm: "Hinweis als gelesen markieren?",
		NoReactivate: true, TitleField: "text",
		Filters: []string{"company_code", "kind", "process_id"}, Search: []string{"text"},
		Fields: []crud.Field{
			{Key: "notice_id", Label: "Nr.", Type: tNum, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true, Lookup: lookupCC},
			{Key: "created_at", Label: "Zeitpunkt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "kind", Label: "Art", Type: metamodel.TypeSelect, Listable: true, ReadOnly: true, Options: noticeKinds},
			{Key: "process_id", Label: "Vorgang", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupProcess},
			{Key: "text", Label: "Hinweis", Type: tText, Listable: true, ReadOnly: true},
			{Key: "user_id", Label: "Für", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "is_open", Label: "Ungelesen", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: access(),
		// Jeder sieht nur seine eigenen Hinweise.
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			u := userOf(ctx)
			if u == "" {
				return "", nil, false, nil // Systemanfrage (z. B. Konsole)
			}
			return "user_id = ?", []any{u}, false, nil
		},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			if u := userOf(ctx); u != "" && crud.Str(rec["user_id"]) != u {
				return fmt.Errorf("%w: Hinweis eines anderen Benutzers", sdk.ErrPermissionDenied)
			}
			return nil
		},
	}
}

// notify: Hinweis an einen Benutzer – nicht doppelt, solange derselbe ungelesen ist.
func (m *Module) notify(ctx context.Context, cc, user, id, kind, text string) error {
	if user == "" {
		return nil
	}
	row, err := m.one(ctx, "SELECT 1 FROM process__notice WHERE user_id = ? AND company_code = ? AND process_id = ? AND kind = ? AND is_open = ?",
		user, cc, id, kind, true)
	if err != nil || row != nil {
		return err
	}
	row, err = m.one(ctx, "SELECT COALESCE(MAX(notice_id), 0) FROM process__notice")
	if err != nil {
		return err
	}
	return m.insertRow(ctx, "notice", map[string]any{"notice_id": toInt(row[0]) + 1, "company_code": cc, "user_id": user,
		"process_id": nilIfEmpty(id), "kind": kind, "text": text, "created_at": m.stamp(), "is_open": true})
}

// --- Ereignisjournal ----------------------------------------------------------------

func (m *Module) event() *crud.Entity {
	return &crud.Entity{
		Object: eventObject, Title: "Ereignisjournal", Icon: "icon-activity", Table: "process__event", Section: "Schnittstellen", ReadOnly: true,
		Keys: []string{"seq"}, Order: "seq",
		Filters: []string{"company_code", "process_id", "event", "since"},
		FilterExpr: map[string]func(any) (string, []any){"since": func(v any) (string, []any) {
			return "seq > ?", []any{toInt(v)}
		}},
		Fields: []crud.Field{
			{Key: "since", Label: "Ab Nummer (ausschließlich)", Type: tNum, Virtual: true, FilterOnly: true},
			{Key: "seq", Label: "Nr.", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "occurred_at", Label: "Zeitpunkt", Type: tText, Listable: true, ReadOnly: true},
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "process_id", Label: "Vorgang", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupProcess},
			{Key: "event", Label: "Ereignis", Type: tText, Listable: true, ReadOnly: true},
			{Key: "user_id", Label: "Benutzer", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "data", Label: "Daten (JSON)", Type: tArea, ReadOnly: true},
			{Key: "pushed_at", Label: "An den Event-Dispatcher gemeldet", Short: "Gemeldet", Type: tText, ReadOnly: true},
		},
		Access: access(),
	}
}

// journal: Ereignis mit laufender Nummer (in der Transaktion der Änderung).
func (m *Module) journal(ctx context.Context, cc, id, event string, data map[string]any) error {
	row, err := m.one(ctx, "SELECT COALESCE(MAX(seq), 0) FROM process__event")
	if err != nil {
		return err
	}
	var payload any
	if len(data) > 0 {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		payload = string(b)
	}
	return m.insertRow(ctx, "event", map[string]any{"seq": toInt(row[0]) + 1, "company_code": cc, "process_id": nilIfEmpty(id), "event": event,
		"occurred_at": m.stamp(), "user_id": nilIfEmpty(userOf(ctx)), "data": payload})
}
