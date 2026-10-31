package contract

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Erinnerungen (seit 0.44.0) vor dem Ablauf einer Garantie (z. B. Preisgarantie):
//
//   - Kondition: „Garantie bis“ (guaranteed_until). Maßgeblich ist die jüngste
//     Zeitscheibe der Kondition, und nur wenn sie vor dem Ablauf beginnt – eine
//     Folgekondition (neue Zeitscheibe nach der Garantie) beendet die Erinnerung.
//   - Erinnerungsregeln (Einstellungen) je Vertragsart × Konditionsart: erste
//     Erinnerung … Tage vor Ablauf, danach alle … Tage, höchstens … mal, mit
//     Beschreibung (z. B. „Neues Angebot einholen, ggf. kündigen“).
//   - Erinnerungen: jede Erinnerung eine Zeile ab ihrem Tag; die neuere setzt die
//     ältere auf „wiederholt“. „Erledigt …“ beendet die Erinnerungen zu diesem Ablauf.
//     Ein Folgevertrag am Vertrag (oder ein Entwurf) erinnert nicht; offene
//     Erinnerungen entfallen dann, ebenso bei geänderter Garantie oder Regel.
//
// Ermittelt wird beim Öffnen der Erinnerungen (Liste oder Abschnitt am Vertrag).

const (
	reminderRuleObject = "ContractReminderRule"
	reminderObject     = "ContractReminder"

	remOpen     = "OPEN"
	remDone     = "DONE"
	remRepeated = "REPEATED"
	remObsolete = "OBSOLETE"
)

var reminderStatusOptions = []metamodel.Option{
	{Value: remOpen, Label: "offen"}, {Value: remRepeated, Label: "wiederholt (neuere Erinnerung offen)", Short: "wiederholt"},
	{Value: remDone, Label: "erledigt"}, {Value: remObsolete, Label: "entfallen"}}

// --- Erinnerungsregeln ---------------------------------------------------------------

func (m *Module) reminderRule() *crud.Entity {
	return &crud.Entity{
		Object: reminderRuleObject, Customizing: true, Title: "Erinnerungsregeln", Icon: "icon-bell", Table: "contract__reminder_rule", Section: "Einstellungen",
		Keys: []string{"company_code", "contract_type", "condition_kind"}, Order: "company_code, contract_type, condition_kind",
		StatusField: "is_active", TitleField: "description", Filters: []string{"company_code", "contract_type", "condition_kind"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ContractType")},
			{Key: "condition_kind", Label: "Konditionsart", Short: "Kond.-Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: kindLookup},
			{Key: "description", Label: "Beschreibung (Text der Erinnerung, z. B. „Preisgarantie läuft ab – neues Angebot einholen, ggf. kündigen“)",
				Short: "Beschreibung", Type: tArea, Required: true, Listable: true},
			{Key: "lead_days", Label: "Erste Erinnerung … Tage vor Ablauf der Garantie", Short: "Vorlauf (Tage)", Type: tNum, Required: true, Listable: true},
			{Key: "repeat_days", Label: "Danach alle … Tage erneut (leer = einmal)", Short: "Abstand (Tage)", Type: tNum, Listable: true},
			{Key: "max_count", Label: "Höchstens … Erinnerungen (leer = bis zum Ablauf)", Short: "Anzahl", Type: tNum, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, reminderRuleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkReminderRule,
	}
}

func (m *Module) checkReminderRule(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if old == nil {
		rec["contract_type"], rec["condition_kind"] = trimUpper(rec["contract_type"]), trimUpper(rec["condition_kind"])
		if _, err := m.contractTypeOf(ctx, cc, crud.Str(rec["contract_type"])); err != nil {
			return err
		}
		if _, err := m.conditionKindOf(ctx, cc, crud.Str(rec["condition_kind"])); err != nil {
			return err
		}
	}
	if strings.TrimSpace(crud.Str(rec["description"])) == "" {
		return crud.Invalid("Beschreibung ist Pflicht")
	}
	if n := toInt(rec["lead_days"]); n < 0 || n > 3660 {
		return crud.Invalid("Vorlauf 0 bis 3660 Tage")
	}
	for _, k := range []string{"repeat_days", "max_count"} {
		if crud.Str(rec[k]) == "" {
			rec[k] = nil
		} else if toInt(rec[k]) < 1 {
			return crud.Invalid("Abstand und Anzahl mindestens 1 (oder leer)")
		}
	}
	return nil
}

// --- Erinnerungen --------------------------------------------------------------------

func (m *Module) reminder() *crud.Entity {
	return &crud.Entity{
		Object: reminderObject, Title: "Erinnerungen", Icon: "icon-bell", Table: "contract__reminder", Section: "Verträge",
		Keys:     []string{"company_code", "contract_id", "condition_type", "object_id", "position", "due_date", "seq"},
		ReadOnly: true, TitleField: "description",
		Order:   "CASE status WHEN 'OPEN' THEN 0 WHEN 'REPEATED' THEN 1 ELSE 2 END, remind_date DESC, contract_id",
		Filters: []string{"company_code", "contract_id", "contract_type", "condition_kind", "status"},
		Search:  []string{"contract_id", "description", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, Lookup: lookupCC},
			{Key: "remind_date", Label: "Erinnern am", Type: tDate, Listable: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, Options: reminderStatusOptions},
			{Key: "contract_id", Label: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Lookup: catalogLookup("ContractType")},
			{Key: "condition_type", Label: "Konditionsgegenstand", Short: "Gegenstand", Type: tText, Listable: true, Lookup: catalogLookup("ConditionType")},
			{Key: "condition_kind", Label: "Konditionsart", Short: "Kond.-Art", Type: tText, Lookup: kindLookup},
			{Key: "object_id", Label: "Objekt der Kondition", Short: "Objekt", Type: tText},
			{Key: "position", Label: "Position der Kondition", Type: tNum},
			{Key: "due_date", Label: "Garantie bis", Type: tDate, Listable: true},
			{Key: "seq", Label: "Erinnerung Nr.", Short: "Nr.", Type: tNum, Listable: true},
			{Key: "description", Label: "Beschreibung (aus der Erinnerungsregel)", Short: "Beschreibung", Type: tText, Listable: true},
			{Key: "reason", Label: "Entfallen, weil", Type: tText},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "done_at", Label: "Erledigt am", Type: tText},
			{Key: "done_by", Label: "Erledigt von", Type: tText, Lookup: crud.LookupUser},
		},
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code", Fields: []string{"contract_type"}},
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			m.refreshReminders(ctx)
			return "", nil, false, nil
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "done", Label: "Erledigt …", Record: true, Fields: []string{"note"}}, Handle: m.reminderDoneAction},
			{ActionConfig: metamodel.ActionConfig{Name: "reopen", Label: "Wieder öffnen", Record: true,
				Confirm: "Erinnerung wieder öffnen? Weitere Erinnerungen zu diesem Ablauf folgen dann wieder nach der Regel."}, Handle: m.reminderReopenAction},
		},
	}
}

// reminderDates: Tage der Erinnerungen vor dem Ablauf (höchstens bis zum Ablauf).
func reminderDates(due string, lead, repeat, count int64) []string {
	d, err := time.Parse(time.DateOnly, due)
	if err != nil {
		return nil
	}
	if count <= 0 {
		count = 1
		if repeat > 0 {
			count = 1 << 20 // bis zum Ablauf
		}
	}
	var out []string
	for i, t := int64(0), d.AddDate(0, 0, -int(lead)); i < count && !t.After(d); i++ {
		out = append(out, t.Format(time.DateOnly))
		if repeat <= 0 {
			break
		}
		t = t.AddDate(0, 0, int(repeat))
	}
	return out
}

// remKey: Erinnerungen einer Kondition zu einem Ablauf.
type remKey struct {
	CC, Contract, Type, Object string
	Pos                        int64
	Due                        string
}

var refreshMu sync.Mutex

// refreshReminders legt fällige Erinnerungen an und setzt überholte auf
// „wiederholt“ bzw. „entfallen“. Fehler landen im Log (die Liste bleibt lesbar).
func (m *Module) refreshReminders(ctx context.Context) {
	if !refreshMu.TryLock() {
		return
	}
	defer refreshMu.Unlock()
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error { return m.refreshRemindersTx(ctx, time.Now().Format(time.DateOnly)) }); err != nil {
		m.log.WarnContext(ctx, "Erinnerungen nicht ermittelt", "err", err.Error())
	}
}

func (m *Module) refreshRemindersTx(ctx context.Context, today string) error {
	res, err := m.db.Query(ctx, `SELECT k.company_code, k.contract_id, k.condition_type, k.object_id, k.position, k.guaranteed_until, k.valid_from,
			k.contract_type, k.condition_kind, c.status, COALESCE(c.successor_contract_id, ''), r.lead_days, r.repeat_days, r.max_count, r.description
		FROM contract__condition k
		JOIN contract__contract c ON c.company_code = k.company_code AND c.contract_id = k.contract_id
		JOIN contract__reminder_rule r ON r.company_code = k.company_code AND r.contract_type = k.contract_type AND r.condition_kind = k.condition_kind
			AND r.is_active = ?
		WHERE k.guaranteed_until IS NOT NULL AND k.valid_from = (SELECT MAX(x.valid_from) FROM contract__condition x
			WHERE x.company_code = k.company_code AND x.contract_id = k.contract_id AND x.condition_type = k.condition_type
			AND x.object_id = k.object_id AND x.position = k.position)`, true)
	if err != nil {
		return err
	}
	type cand struct {
		key                remKey
		kind, ctype, desc  string
		lead, repeat, max_ int64
		reason             string // leer = erinnern
	}
	var cands []cand
	for _, r := range res.Rows {
		c := cand{key: remKey{crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), toInt(r[4]), dateOf(r[5])},
			ctype: crud.Str(r[7]), kind: crud.Str(r[8]), lead: toInt(r[11]), repeat: toInt(r[12]), max_: toInt(r[13]), desc: crud.Str(r[14])}
		switch {
		case crud.Str(r[10]) != "":
			c.reason = "Folgevertrag " + crud.Str(r[10])
		case crud.Str(r[9]) == statusDraft:
			c.reason = "Vertrag im Entwurf"
		case dateOf(r[6]) > c.key.Due:
			c.reason = "Folgekondition ab " + dateOf(r[6])
		}
		cands = append(cands, c)
	}
	// bisherige Erinnerungen je Kondition und Ablauf
	res, err = m.db.Query(ctx, `SELECT company_code, contract_id, condition_type, object_id, position, due_date, seq, status FROM contract__reminder`)
	if err != nil {
		return err
	}
	type state struct {
		seqs map[int64]string
		done bool
	}
	have := map[remKey]*state{}
	for _, r := range res.Rows {
		k := remKey{crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), toInt(r[4]), dateOf(r[5])}
		s := have[k]
		if s == nil {
			s = &state{seqs: map[int64]string{}}
			have[k] = s
		}
		s.seqs[toInt(r[6])] = crud.Str(r[7])
		s.done = s.done || crud.Str(r[7]) == remDone
	}
	where := `company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ? AND position = ? AND due_date = ?`
	args := func(k remKey, extra ...any) []any {
		return append(extra, k.CC, k.Contract, k.Type, k.Object, k.Pos, k.Due)
	}
	valid := map[remKey]bool{}
	for _, c := range cands {
		s := have[c.key]
		if c.reason != "" {
			if s != nil {
				if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, reason = ? WHERE `+where+` AND status IN (?, ?)`,
					args(c.key, remObsolete, c.reason, remOpen, remRepeated)...); err != nil {
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
		for i, d := range reminderDates(c.key.Due, c.lead, c.repeat, c.max_) {
			if d > today {
				break
			}
			seq := int64(i + 1)
			last = seq
			if s != nil && s.seqs[seq] != "" {
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO contract__reminder (company_code, contract_id, condition_type, object_id, position, due_date, seq,
				remind_date, contract_type, condition_kind, description, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, c.key.CC, c.key.Contract, c.key.Type, c.key.Object, c.key.Pos, c.key.Due, seq,
				d, c.ctype, c.kind, c.desc, remOpen, now()); err != nil {
				return err
			}
		}
		if last > 1 {
			if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ? WHERE `+where+` AND status = ? AND seq < ?`,
				append(args(c.key, remRepeated), remOpen, last)...); err != nil {
				return err
			}
		}
	}
	// Garantie geändert oder entfernt, Regel inaktiv: offene Erinnerungen entfallen.
	for k, s := range have {
		if valid[k] {
			continue
		}
		for _, st := range s.seqs {
			if st == remOpen || st == remRepeated {
				if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, reason = COALESCE(reason, ?) WHERE `+where+` AND status IN (?, ?)`,
					append(append([]any{remObsolete, "Garantie oder Erinnerungsregel geändert"}, args(k)...), remOpen, remRepeated)...); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// reminderOfID: Erinnerung aus der Record-ID (Schlüsselfelder, „|“-getrennt).
func (m *Module) reminderOfID(ctx context.Context, id string) (remKey, int64, string, error) {
	key, err := m.set.Entity(reminderObject).ParseID(id)
	if err != nil {
		return remKey{}, 0, "", err
	}
	k := remKey{crud.Str(key["company_code"]), crud.Str(key["contract_id"]), crud.Str(key["condition_type"]), crud.Str(key["object_id"]),
		toInt(key["position"]), crud.Str(key["due_date"])}
	seq := toInt(key["seq"])
	res, err := m.db.Query(ctx, `SELECT status FROM contract__reminder WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ?
		AND position = ? AND due_date = ? AND seq = ?`, k.CC, k.Contract, k.Type, k.Object, k.Pos, k.Due, seq)
	if err != nil {
		return remKey{}, 0, "", err
	}
	if len(res.Rows) == 0 {
		return remKey{}, 0, "", crud.Invalid("Erinnerung %s gibt es nicht", id)
	}
	if err := requireWrite(ctx, "Contract", "update", k.CC); err != nil {
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
	// Erledigt gilt für den Ablauf: auch ältere, wiederholte Erinnerungen.
	_, err = m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, done_at = ?, done_by = ?, note = COALESCE(?, note)
		WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ? AND position = ? AND due_date = ?
		AND (seq = ? OR status IN (?, ?))`, remDone, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), nilIfEmpty(strings.TrimSpace(in.Data.Note)),
		k.CC, k.Contract, k.Type, k.Object, k.Pos, k.Due, seq, remOpen, remRepeated)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Erinnerung erledigt – zu diesem Ablauf wird nicht mehr erinnert"}}, nil
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
	// Die jüngste wird offen, die übrigen dieses Ablaufs wiederholt.
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = CASE WHEN seq = ? THEN ? ELSE ? END, done_at = NULL, done_by = NULL
			WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ? AND position = ? AND due_date = ? AND status = ?`,
			seq, remOpen, remRepeated, k.CC, k.Contract, k.Type, k.Object, k.Pos, k.Due, remDone); err != nil {
			return err
		}
		return m.refreshRemindersTx(ctx, time.Now().Format(time.DateOnly))
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Erinnerung wieder offen"}}, nil
}
