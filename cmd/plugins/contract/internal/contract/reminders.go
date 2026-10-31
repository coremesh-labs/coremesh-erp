package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Erinnerungen (seit 0.44.0) zu einem Anlass an einer Kondition:
//
//   - Anlass „Garantie läuft ab“: „Garantie bis“ (guaranteed_until) der Kondition.
//     Maßgeblich ist die jüngste Zeitscheibe, und nur wenn sie vor dem Ablauf
//     beginnt – eine Folgekondition (neue Zeitscheibe nach der Garantie) beendet
//     die Erinnerung.
//   - Anlass „Betrag ändert sich“ (seit 0.45.0): eine Zeitscheibe beginnt mit einem
//     anderen Betrag als die vorige (z. B. Hausgeld laut neuem Wirtschaftsplan) –
//     etwa „Dauerauftrag anpassen“; Einzelheiten nennen alten und neuen Betrag.
//   - Erinnerungsregeln (Einstellungen) je Vertragsart × Konditionsart × Anlass:
//     erste Erinnerung … Tage vorher, danach alle … Tage, höchstens … mal, mit
//     Beschreibung; nur Anlässe ab „gilt ab“ und (leer = alle) Verträge mit
//     einem der Zahlwege (z. B. nur Dauerauftrag, nicht Lastschrift).
//   - Erinnerungen: jede Erinnerung eine Zeile ab ihrem Tag; die neuere setzt die
//     ältere auf „wiederholt“. „Erledigt …“ beendet die Erinnerungen zu diesem Anlass.
//     Ein Folgevertrag am Vertrag (oder ein Entwurf) erinnert nicht; offene
//     Erinnerungen entfallen dann, ebenso bei geändertem Anlass oder Regel.
//
// Ermittelt wird beim Öffnen der Erinnerungen (Liste oder Abschnitt am Vertrag).

const (
	reminderRuleObject = "ContractReminderRule"
	reminderObject     = "ContractReminder"

	remOpen     = "OPEN"
	remDone     = "DONE"
	remRepeated = "REPEATED"
	remObsolete = "OBSOLETE"

	occGuarantee = "GUARANTEE_END"
	occAmount    = "AMOUNT_CHANGE"
)

var (
	reminderStatusOptions = []metamodel.Option{
		{Value: remOpen, Label: "offen"}, {Value: remRepeated, Label: "wiederholt (neuere Erinnerung offen)", Short: "wiederholt"},
		{Value: remDone, Label: "erledigt"}, {Value: remObsolete, Label: "entfallen"}}
	occasionOptions = []metamodel.Option{
		{Value: occGuarantee, Label: "Garantie läuft ab (Garantie bis an der Kondition)", Short: "Garantie läuft ab"},
		{Value: occAmount, Label: "Betrag ändert sich (neue Zeitscheibe mit anderem Betrag)", Short: "Betrag ändert sich"}}
)

// --- Erinnerungsregeln ---------------------------------------------------------------

func (m *Module) reminderRule() *crud.Entity {
	return &crud.Entity{
		Object: reminderRuleObject, Customizing: true, Title: "Erinnerungsregeln", Icon: "icon-bell", Table: "contract__reminder_rule", Section: "Einstellungen",
		Keys: []string{"company_code", "contract_type", "condition_kind", "occasion"}, Order: "company_code, contract_type, condition_kind, occasion",
		StatusField: "is_active", TitleField: "description", Filters: []string{"company_code", "contract_type", "condition_kind", "occasion"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ContractType")},
			{Key: "condition_kind", Label: "Konditionsart", Short: "Kond.-Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: kindLookup},
			{Key: "occasion", Label: "Anlass", Type: tSel, Required: true, Listable: true, Immutable: true, Options: occasionOptions},
			{Key: "description", Label: "Beschreibung (Text der Erinnerung, z. B. „Preisgarantie läuft ab – neues Angebot einholen“, „Dauerauftrag anpassen“)",
				Short: "Beschreibung", Type: tArea, Required: true, Listable: true},
			{Key: "lead_days", Label: "Erste Erinnerung … Tage vor dem Anlass (Ablauf der Garantie bzw. Beginn des neuen Betrags)", Short: "Vorlauf (Tage)",
				Type: tNum, Required: true, Listable: true},
			{Key: "repeat_days", Label: "Danach alle … Tage erneut (leer = einmal)", Short: "Abstand (Tage)", Type: tNum, Listable: true},
			{Key: "max_count", Label: "Höchstens … Erinnerungen (leer = bis zum Anlass)", Short: "Anzahl", Type: tNum, Listable: true},
			{Key: "payment_methods", Label: "Nur bei Zahlweg des Vertrags (leer = alle)", Short: "Zahlweg", Type: tSel, Multiple: true, Listable: true,
				Options: paymentMethodOptions},
			{Key: "active_from", Label: "Gilt für Anlässe ab (leer beim Anlegen = heute; frühere bleiben ohne Erinnerung)", Short: "gilt ab", Type: tDate},
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
		if crud.Str(rec["active_from"]) == "" {
			rec["active_from"] = time.Now().Format(time.DateOnly)
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
	rec["payment_methods"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["payment_methods"])))
	if crud.Str(rec["active_from"]) == "" {
		rec["active_from"] = nil
	} else {
		d, err := crud.ParseDate(rec["active_from"])
		if err != nil {
			return err
		}
		rec["active_from"] = d
	}
	return nil
}

// --- Erinnerungen --------------------------------------------------------------------

func (m *Module) reminder() *crud.Entity {
	return &crud.Entity{
		Object: reminderObject, Title: "Erinnerungen", Icon: "icon-bell", Table: "contract__reminder", Section: "Verträge",
		Keys:     []string{"company_code", "contract_id", "condition_type", "object_id", "position", "occasion", "due_date", "seq"},
		ReadOnly: true, TitleField: "description",
		Order:   "CASE status WHEN 'OPEN' THEN 0 WHEN 'REPEATED' THEN 1 ELSE 2 END, remind_date DESC, contract_id",
		Filters: []string{"company_code", "contract_id", "contract_type", "condition_kind", "occasion", "status"},
		Search:  []string{"contract_id", "description", "detail", "note"},
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
			{Key: "occasion", Label: "Anlass", Type: tSel, Listable: true, Options: occasionOptions},
			{Key: "due_date", Label: "Am (Ablauf der Garantie bzw. Beginn des neuen Betrags)", Short: "Am", Type: tDate, Listable: true},
			{Key: "seq", Label: "Erinnerung Nr.", Short: "Nr.", Type: tNum, Listable: true},
			{Key: "description", Label: "Beschreibung (aus der Erinnerungsregel)", Short: "Beschreibung", Type: tText, Listable: true},
			{Key: "detail", Label: "Einzelheiten", Type: tText, Listable: true},
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
				Confirm: "Erinnerung wieder öffnen? Weitere Erinnerungen zu diesem Anlass folgen dann wieder nach der Regel."}, Handle: m.reminderReopenAction},
		},
	}
}

// reminderDates: Tage der Erinnerungen vor dem Anlass (höchstens bis zum Anlass).
func reminderDates(due string, lead, repeat, count int64) []string {
	d, err := time.Parse(time.DateOnly, due)
	if err != nil {
		return nil
	}
	if count <= 0 {
		count = 1
		if repeat > 0 {
			count = 1 << 20 // bis zum Anlass
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

// remKey: Erinnerungen einer Kondition zu einem Anlass.
type remKey struct {
	CC, Contract, Type, Object string
	Pos                        int64
	Occasion, Due              string
}

// remWhere: Bedingung auf die Erinnerungen eines remKey (Argumente aus remArgs).
const remWhere = `company_code = ? AND contract_id = ? AND condition_type = ? AND object_id = ? AND position = ? AND occasion = ? AND due_date = ?`

func remArgs(k remKey, before ...any) []any {
	return append(before, k.CC, k.Contract, k.Type, k.Object, k.Pos, k.Occasion, k.Due)
}

// remCand: ein Anlass mit Regel – reason leer = erinnern.
type remCand struct {
	key                 remKey
	ctype, kind, desc   string
	lead, repeat, count int64
	detail, reason      string
}

// remRule: aktive Erinnerungsregel.
type remRule struct {
	lead, repeat, count int64
	desc, from          string
	methods             []string
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

// reminderCandidates: Anlässe aller Konditionen mit aktiver Regel.
func (m *Module) reminderCandidates(ctx context.Context) ([]remCand, error) {
	res, err := m.db.Query(ctx, `SELECT company_code, contract_type, condition_kind, occasion, lead_days, repeat_days, max_count, description,
		active_from, COALESCE(payment_methods, '') FROM contract__reminder_rule WHERE is_active = ?`, true)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	rules := map[[4]string]remRule{}
	for _, r := range res.Rows {
		var methods []string
		for _, p := range strings.Split(crud.Str(r[9]), ",") {
			if p = strings.TrimSpace(p); p != "" {
				methods = append(methods, p)
			}
		}
		rules[[4]string{crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3])}] = remRule{lead: toInt(r[4]), repeat: toInt(r[5]),
			count: toInt(r[6]), desc: crud.Str(r[7]), from: dateOf(r[8]), methods: methods}
	}
	// alle Zeitscheiben der Konditionen mit Vertrag, je Schlüssel nach Beginn
	res, err = m.db.Query(ctx, `SELECT k.company_code, k.contract_id, k.condition_type, k.object_id, k.position, k.valid_from, k.amount,
			k.guaranteed_until, k.contract_type, COALESCE(k.condition_kind, ''), c.status, COALESCE(c.successor_contract_id, ''),
			COALESCE(c.payment_method, ''), c.currency, k.valid_to, COALESCE(k.calc_method, ''), COALESCE(k.frequency, '')
		FROM contract__condition k JOIN contract__contract c ON c.company_code = k.company_code AND c.contract_id = k.contract_id
		ORDER BY k.company_code, k.contract_id, k.condition_type, k.object_id, k.position, k.valid_from`)
	if err != nil {
		return nil, err
	}
	// Summe der Festbeträge eines Vertrags mit gleicher Periode an einem Tag
	byContract := map[[2]string][][]any{}
	for _, r := range res.Rows {
		k := [2]string{crud.Str(r[0]), crud.Str(r[1])}
		byContract[k] = append(byContract[k], r)
	}
	total := func(r []any, day string) (sum, n int64) {
		for _, o := range byContract[[2]string{crud.Str(r[0]), crud.Str(r[1])}] {
			if to := dateOf(o[14]); crud.Str(o[15]) == calcFixed && crud.Str(o[16]) == crud.Str(r[16]) &&
				dateOf(o[5]) <= day && (to == "" || to >= day) {
				sum, n = sum+toInt(o[6]), n+1
			}
		}
		return sum, n
	}
	decs := map[string]int{}
	var out []remCand
	for i, r := range res.Rows {
		cc, ctype, kind := crud.Str(r[0]), crud.Str(r[8]), crud.Str(r[9])
		same := func(j int) bool {
			o := res.Rows[j]
			return crud.Str(o[0]) == cc && crud.Str(o[1]) == crud.Str(r[1]) && crud.Str(o[2]) == crud.Str(r[2]) &&
				crud.Str(o[3]) == crud.Str(r[3]) && toInt(o[4]) == toInt(r[4])
		}
		last := i == len(res.Rows)-1 || !same(i+1)
		from := dateOf(r[5])
		reason := ""
		switch {
		case crud.Str(r[11]) != "":
			reason = "Folgevertrag " + crud.Str(r[11])
		case crud.Str(r[10]) == statusDraft:
			reason = "Vertrag im Entwurf"
		}
		add := func(occ, due, detail, why string) {
			rule, ok := rules[[4]string{cc, ctype, kind, occ}]
			if !ok || due < rule.from {
				return
			}
			if len(rule.methods) > 0 && !slices.Contains(rule.methods, crud.Str(r[12])) {
				return
			}
			c := remCand{key: remKey{cc, crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), toInt(r[4]), occ, due},
				ctype: ctype, kind: kind, desc: rule.desc, lead: rule.lead, repeat: rule.repeat, count: rule.count, detail: detail, reason: why}
			if c.reason == "" {
				c.reason = reason
			}
			out = append(out, c)
		}
		if g := dateOf(r[7]); g != "" && last {
			why := ""
			if from > g {
				why = "Folgekondition ab " + from
			}
			add(occGuarantee, g, "", why)
		}
		if i > 0 && same(i-1) && toInt(res.Rows[i-1][6]) != toInt(r[6]) {
			cur := crud.Str(r[13])
			d, ok := decs[cur]
			if !ok {
				d = m.currencyDecimals(ctx, cur)
				decs[cur] = d
			}
			amt := func(v int64) string { return germanAmount(formatAmount(v, d)) }
			detail := fmt.Sprintf("ab %s %s statt %s %s", germanDate(from), amt(toInt(r[6])), amt(toInt(res.Rows[i-1][6])), cur)
			if t, n := total(r, from); crud.Str(r[15]) == calcFixed && n > 1 {
				before, _ := total(r, dayBefore(from))
				detail += fmt.Sprintf("; Zahlung gesamt %s statt %s %s", amt(t), amt(before), cur)
			}
			add(occAmount, from, detail, "")
		}
	}
	return out, nil
}

func (m *Module) refreshRemindersTx(ctx context.Context, today string) error {
	cands, err := m.reminderCandidates(ctx)
	if err != nil {
		return err
	}
	// bisherige Erinnerungen je Kondition und Anlass
	res, err := m.db.Query(ctx, `SELECT company_code, contract_id, condition_type, object_id, position, occasion, due_date, seq, status FROM contract__reminder`)
	if err != nil {
		return err
	}
	type state struct {
		seqs map[int64]string
		done bool
	}
	have := map[remKey]*state{}
	for _, r := range res.Rows {
		k := remKey{crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), toInt(r[4]), crud.Str(r[5]), dateOf(r[6])}
		s := have[k]
		if s == nil {
			s = &state{seqs: map[int64]string{}}
			have[k] = s
		}
		s.seqs[toInt(r[7])] = crud.Str(r[8])
		s.done = s.done || crud.Str(r[8]) == remDone
	}
	valid := map[remKey]bool{}
	for _, c := range cands {
		s := have[c.key]
		if c.reason != "" {
			if s != nil {
				if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, reason = ? WHERE `+remWhere+` AND status IN (?, ?)`,
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
		for i, d := range reminderDates(c.key.Due, c.lead, c.repeat, c.count) {
			if d > today {
				break
			}
			seq := int64(i + 1)
			last = seq
			if s != nil && s.seqs[seq] != "" {
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO contract__reminder (company_code, contract_id, condition_type, object_id, position, occasion, due_date,
				seq, remind_date, contract_type, condition_kind, description, detail, status, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT DO NOTHING`, c.key.CC, c.key.Contract, c.key.Type, c.key.Object, c.key.Pos, c.key.Occasion, c.key.Due, seq,
				d, c.ctype, c.kind, c.desc, nilIfEmpty(c.detail), remOpen, now()); err != nil {
				return err
			}
		}
		if last > 1 {
			if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ? WHERE `+remWhere+` AND status = ? AND seq < ?`,
				append(remArgs(c.key, remRepeated), remOpen, last)...); err != nil {
				return err
			}
		}
	}
	// Anlass geändert oder entfallen, Regel inaktiv: offene Erinnerungen entfallen.
	for k, s := range have {
		if valid[k] {
			continue
		}
		for _, st := range s.seqs {
			if st == remOpen || st == remRepeated {
				if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, reason = COALESCE(reason, ?) WHERE `+remWhere+` AND status IN (?, ?)`,
					append(remArgs(k, remObsolete, "Anlass oder Erinnerungsregel geändert"), remOpen, remRepeated)...); err != nil {
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
		toInt(key["position"]), crud.Str(key["occasion"]), crud.Str(key["due_date"])}
	seq := toInt(key["seq"])
	res, err := m.db.Query(ctx, `SELECT status FROM contract__reminder WHERE `+remWhere+` AND seq = ?`, append(remArgs(k), seq)...)
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
	// Erledigt gilt für den Anlass: auch ältere, wiederholte Erinnerungen.
	_, err = m.db.Exec(ctx, `UPDATE contract__reminder SET status = ?, done_at = ?, done_by = ?, note = COALESCE(?, note)
		WHERE `+remWhere+` AND (seq = ? OR status IN (?, ?))`,
		append(remArgs(k, remDone, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), nilIfEmpty(strings.TrimSpace(in.Data.Note))),
			seq, remOpen, remRepeated)...)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Erinnerung erledigt – zu diesem Anlass wird nicht mehr erinnert"}}, nil
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
	// Die gewählte wird offen, die übrigen dieses Anlasses wiederholt.
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE contract__reminder SET status = CASE WHEN seq = ? THEN ? ELSE ? END, done_at = NULL, done_by = NULL
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
