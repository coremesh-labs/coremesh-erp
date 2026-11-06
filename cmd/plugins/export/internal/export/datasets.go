package export

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Datasets (seit 0.3.0, Konzept Sichten_Export_Konzept.txt Abschnitt 8)
//
//   - Eine Sicht erzeugt je Schlüssel (Sichtart bzw. Sicht: key_by) ein dauerhaftes Dataset,
//     z. B. „Steuerunterlagen 2026“ (Jahr), „Energie-Monitor Gretchen 10/2026“ (Partner × Monat)
//     oder „Vorgang AF-2026-0001“ (Vorgang).
//   - Die Läufe sind die Stände des Datasets. Ein offener Stand sammelt weiter; nach dem
//     Export (Lieferung) beginnt die nächste Änderung einen neuen Stand mit allen Elementen.
//   - Ein Dataset verliert keine Elemente: storniert, ersetzt, im DMS gelöscht, passt nicht
//     mehr – das sind Kennzeichen. Gelöscht wird nur das Dataset als Ganzes (Lebensdauer,
//     Ende des Vorgangs); die Lieferungen bleiben als Protokoll.
//   - Änderungszeiger: Die Sicht merkt sich, bis wann sie nach neuen Schlüsseln gesucht hat
//     (pointer, bei Vorgängen die Nummer im Ereignisjournal), jedes Dataset, bis wann es
//     nachgeführt ist. Erneuert wird nach dem Rhythmus des Datasets (Zeitgeber).

const datasetPrefix = "DS" // DS-JJJJ-NNNN

const (
	keySicht        = "SICHT"
	keyYear         = "YEAR"
	keyMonth        = "MONTH"
	keyPartner      = "PARTNER"
	keyPartnerMonth = "PARTNER_MONTH"
	keyPartnerYear  = "PARTNER_YEAR"
	keyProcess      = "PROCESS"

	rhManual    = "MANUAL"
	rhChange    = "CHANGE"
	rhDaily     = "DAILY"
	rhWeekly    = "WEEKLY"
	rhMonthly   = "MONTHLY"
	rhQuarterly = "QUARTERLY"
	rhYearly    = "YEARLY"

	dsActive  = "ACTIVE"
	dsClosed  = "CLOSED"
	dsDeleted = "DELETED"

	markCancelled = "CANCELLED"
	markReplaced  = "REPLACED"
	markRemoved   = "REMOVED"
	markDiscarded = "DISCARDED"
	markForwarded = "FORWARDED"
	markNoMatch   = "NOMATCH"
)

var keyOptions = []metamodel.Option{
	{Value: keySicht, Label: "eins je Sicht"},
	{Value: keyYear, Label: "je Jahr (Belegdatum)"},
	{Value: keyMonth, Label: "je Monat (Belegdatum)"},
	{Value: keyPartner, Label: "je Partner"},
	{Value: keyPartnerMonth, Label: "je Partner und Monat"},
	{Value: keyPartnerYear, Label: "je Partner und Jahr"},
	{Value: keyProcess, Label: "je Vorgang (Ereignisse im Vorgang)"},
}

var rhythmOptions = []metamodel.Option{
	{Value: rhManual, Label: "nur von Hand"},
	{Value: rhChange, Label: "bei Änderung (jeder Lauf des Zeitgebers)"},
	{Value: rhDaily, Label: "täglich"},
	{Value: rhWeekly, Label: "wöchentlich"},
	{Value: rhMonthly, Label: "monatlich"},
	{Value: rhQuarterly, Label: "vierteljährlich"},
	{Value: rhYearly, Label: "jährlich"},
}

var datasetStatus = []metamodel.Option{
	{Value: dsActive, Label: "aktiv", Tone: metamodel.ToneSuccess},
	{Value: dsClosed, Label: "abgeschlossen (keine Erneuerung)", Tone: metamodel.ToneInfo},
	{Value: dsDeleted, Label: "gelöscht (nur Protokoll)", Tone: metamodel.ToneMuted},
}

var markOptions = []metamodel.Option{
	{Value: markCancelled, Label: "storniert"},
	{Value: markReplaced, Label: "ersetzt"},
	{Value: markRemoved, Label: "im DMS gelöscht (Frist)"},
	{Value: markDiscarded, Label: "im DMS verworfen"},
	{Value: markForwarded, Label: "im DMS weitergeleitet"},
	{Value: markNoMatch, Label: "passt nicht mehr zu den Regeln"},
}

// dmsMarks: Status im DMS → Kennzeichen (die Datei gibt es dann nicht mehr).
var dmsMarks = map[string]string{"REMOVED": markRemoved, "DISCARDED": markDiscarded, "FORWARDED": markForwarded}

func markHasFile(mark string) bool {
	return mark != markRemoved && mark != markDiscarded && mark != markForwarded
}

func optionLabel(opts []metamodel.Option, v string) string {
	for _, o := range opts {
		if o.Value == v {
			return o.Label
		}
	}
	return v
}

func validOption(opts []metamodel.Option, v string) bool {
	for _, o := range opts {
		if o.Value == v {
			return true
		}
	}
	return false
}

// Platzhalter im Namen eines Datasets.
var datasetPlaceholders = map[string]bool{"sicht": true, "jahr": true, "monat": true, "partner": true, "vorgang": true, "schluessel": true}

const datasetPatternHelp = "Platzhalter: {sicht} {jahr} {monat} {partner} {vorgang} {schluessel}"

func checkDatasetPattern(p string) error {
	for _, mm := range placeRe.FindAllStringSubmatch(p, -1) {
		if !datasetPlaceholders[mm[1]] {
			return crud.Invalid("Platzhalter {%s} gibt es im Namen des Datasets nicht – %s", mm[1], datasetPatternHelp)
		}
	}
	return nil
}

// periodKey: Schlüssel, die den Zeitraum mitbringen (die Sicht braucht dann keinen).
func periodKey(k string) bool {
	return k == keyYear || k == keyMonth || k == keyPartnerMonth || k == keyPartnerYear
}

func partnerKey(k string) bool { return k == keyPartner || k == keyPartnerMonth || k == keyPartnerYear }

// --- Einstellungen einer Sicht (wirksam: Sicht vor Sichtart) ------------------------------

type viewCfg struct {
	v                       *viewRow
	typ                     *typeRow
	rules                   []ruleRow
	keyBy, rhythm           string
	pattern, refreshCall    string
	endField                string
	lifetime, endDays       *int
	replaceSame             bool
	pointer, checkedAt      string
	eventSeq                int
	viewTarget, viewDateMin string
}

func intPtr(v any) *int {
	if v == nil || strings.TrimSpace(crud.Str(v)) == "" {
		return nil
	}
	n := toInt(v)
	return &n
}

func (m *Module) loadCfg(ctx context.Context, cc, id string) (*viewCfg, error) {
	v, typ, rules, err := m.loadView(ctx, cc, id)
	if err != nil {
		return nil, err
	}
	r, err := m.one(ctx, `SELECT COALESCE(v.key_by, ''), COALESCE(v.rhythm, ''), v.lifetime_months, COALESCE(v.pointer, ''), COALESCE(v.event_seq, 0),
		COALESCE(t.key_by, ''), COALESCE(t.rhythm, ''), COALESCE(t.dataset_pattern, ''), COALESCE(t.refresh_call, ''), COALESCE(t.end_field, ''),
		t.lifetime_months, t.end_days, t.replace_same, COALESCE(v.checked_at, '')
		FROM export__view v JOIN export__view_type t ON t.company_code = v.company_code AND t.code = v.view_type
		WHERE v.company_code = ? AND v.view_id = ?`, cc, id)
	if err != nil || r == nil {
		return nil, err
	}
	c := &viewCfg{v: v, typ: typ, rules: rules, keyBy: firstNonEmpty(crud.Str(r[0]), crud.Str(r[5]), keySicht),
		rhythm: firstNonEmpty(crud.Str(r[1]), crud.Str(r[6]), rhManual), pattern: crud.Str(r[7]), refreshCall: crud.Str(r[8]), endField: crud.Str(r[9]),
		lifetime: intPtr(r[2]), endDays: intPtr(r[11]), replaceSame: crud.AsBool(r[12]), pointer: crud.Str(r[3]), eventSeq: toInt(r[4]), checkedAt: crud.Str(r[13])}
	if c.lifetime == nil {
		c.lifetime = intPtr(r[10])
	}
	return c, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// --- Schlüssel ------------------------------------------------------------------------------

type dsKey struct {
	value, from, to, partner, process string
}

func monthEnd(ym string) string {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return ""
	}
	return t.AddDate(0, 1, -1).Format(time.DateOnly)
}

// keyFor: Schlüssel eines Dokuments (false: passt zu keinem Dataset, z. B. ohne Partner).
func keyFor(c *viewCfg, d doc) (dsKey, bool) {
	k := dsKey{from: c.v.params["date_from"], to: c.v.params["date_to"]}
	year, ym := "", ""
	if len(d.Date) >= 7 {
		year, ym = d.Date[:4], d.Date[:7]
	}
	if partnerKey(c.keyBy) {
		if d.Partner == "" {
			return k, false
		}
		k.partner = d.Partner
	}
	switch c.keyBy {
	case keySicht:
	case keyPartner:
		k.value = d.Partner
	case keyYear, keyPartnerYear:
		if year == "" {
			return k, false
		}
		k.value, k.from, k.to = year, year+"-01-01", year+"-12-31"
	case keyMonth, keyPartnerMonth:
		if ym == "" {
			return k, false
		}
		k.value, k.from, k.to = ym, ym+"-01", monthEnd(ym)
	default:
		return k, false
	}
	if c.keyBy == keyPartnerYear || c.keyBy == keyPartnerMonth {
		k.value = d.Partner + "|" + k.value
	}
	return k, true
}

// discoveryFrom: ohne Zeitraum an der Sicht beginnt die erste Suche nach Schlüsseln mit
// dem vorigen Zeitraum (Jahr bzw. Monat vor dem Stichtag) – nicht mit allen Altbeständen.
func discoveryFrom(keyBy, keyDate string) string {
	t, err := time.Parse(time.DateOnly, keyDate)
	if err != nil {
		return ""
	}
	switch keyBy {
	case keyYear, keyPartnerYear:
		return fmt.Sprintf("%d-01-01", t.Year()-1)
	case keyMonth, keyPartnerMonth:
		return time.Date(t.Year(), t.Month()-1, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)
	}
	return ""
}

func (m *Module) datasetName(ctx context.Context, c *viewCfg, k dsKey) string {
	p := c.pattern
	if p == "" {
		p = "{sicht}"
		if k.value != "" {
			p += " {schluessel}"
		}
	}
	nm := m.newNamer(ctx, c.v.cc)
	year, month := "", ""
	if len(k.from) >= 7 {
		year, month = k.from[:4], k.from[5:7]
	}
	shown := k.value
	if k.partner != "" {
		shown = strings.Replace(shown, k.partner, nm.partner(k.partner), 1)
	}
	if c.keyBy == keyMonth || c.keyBy == keyPartnerMonth {
		shown = strings.Replace(shown, year+"-"+month, month+"/"+year, 1)
	}
	shown = strings.ReplaceAll(shown, "|", " ")
	vals := map[string]string{"sicht": c.v.name, "jahr": year, "monat": month, "partner": nm.partner(k.partner), "vorgang": k.process, "schluessel": shown}
	if (c.keyBy == keyMonth || c.keyBy == keyPartnerMonth) && month != "" {
		vals["monat"] = month + "/" + year
	}
	out := placeRe.ReplaceAllStringFunc(p, func(s string) string { return vals[s[1:len(s)-1]] })
	return strings.TrimSpace(spaces.ReplaceAllString(out, " "))
}

// --- Fälligkeit und Lebensdauer ---------------------------------------------------------------

// nextDue: nächste Erneuerung nach dem Rhythmus (leer = nur von Hand).
func nextDue(rhythm, today string) any {
	t, err := time.Parse(time.DateOnly, today)
	if err != nil {
		return nil
	}
	var n time.Time
	switch rhythm {
	case rhChange:
		return today
	case rhDaily:
		n = t.AddDate(0, 0, 1)
	case rhWeekly:
		n = t.AddDate(0, 0, 7)
	case rhMonthly:
		n = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	case rhQuarterly:
		q := (int(t.Month())-1)/3*3 + 4
		n = time.Date(t.Year(), time.Month(q), 1, 0, 0, 0, 0, time.UTC)
	case rhYearly:
		n = time.Date(t.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)
	default:
		return nil
	}
	return n.Format(time.DateOnly)
}

func addMonths(date string, n int) any {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil
	}
	// Monatsende bleibt Monatsende (31.10. + 18 Monate = 30.04.)
	return t.AddDate(0, 0, 1).AddDate(0, n, 0).AddDate(0, 0, -1).Format(time.DateOnly)
}

func addDaysDate(date string, n int) any {
	t, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil
	}
	return t.AddDate(0, 0, n).Format(time.DateOnly)
}

// --- Datensatz Dataset ------------------------------------------------------------------------

type dsRow struct {
	cc, id, view, key, name, status, rhythm      string
	from, to, partner, process, pointer, lastRun string
	pending                                      bool
	states                                       int
}

func (m *Module) loadDatasets(ctx context.Context, where string, args ...any) ([]dsRow, error) {
	res, err := m.db.Query(ctx, `SELECT company_code, dataset_id, view_id, COALESCE(key_value, ''), name, status, COALESCE(rhythm, ''),
		period_from, period_to, COALESCE(partner_id, ''), COALESCE(process_id, ''), COALESCE(pointer, ''), COALESCE(last_run, ''), pending,
		COALESCE(state_count, 0) FROM export__dataset WHERE `+where+` ORDER BY company_code, dataset_id`, args...)
	if err != nil {
		return nil, err
	}
	var out []dsRow
	for _, r := range res.Rows {
		out = append(out, dsRow{cc: crud.Str(r[0]), id: crud.Str(r[1]), view: crud.Str(r[2]), key: crud.Str(r[3]), name: crud.Str(r[4]), status: crud.Str(r[5]),
			rhythm: crud.Str(r[6]), from: dateStr(r[7]), to: dateStr(r[8]), partner: crud.Str(r[9]), process: crud.Str(r[10]), pointer: crud.Str(r[11]),
			lastRun: crud.Str(r[12]), pending: crud.AsBool(r[13]), states: toInt(r[14])})
	}
	return out, nil
}

func (m *Module) nextDatasetID(ctx context.Context, cc, year string) (string, error) {
	stem := datasetPrefix + "-" + year + "-"
	res, err := m.db.Query(ctx, "SELECT dataset_id FROM export__dataset WHERE company_code = ? AND dataset_id LIKE ?", cc, stem+"%")
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

// createDataset: neues Dataset für einen Schlüssel (Name vorher, Bezeichnungen aus anderen Modulen).
func (m *Module) createDataset(ctx context.Context, c *viewCfg, k dsKey, name string) (dsRow, error) {
	cc := c.v.cc
	id, err := m.nextDatasetID(ctx, cc, m.today()[:4])
	if err != nil {
		return dsRow{}, err
	}
	var deleteAfter any
	if c.lifetime != nil && k.to != "" && c.keyBy != keyProcess {
		deleteAfter = addMonths(k.to, *c.lifetime)
	}
	_, err = m.db.Exec(ctx, `INSERT INTO export__dataset (company_code, dataset_id, view_id, name, status, created_at, key_value, view_type, key_by, rhythm,
		partner_id, process_id, created_by, period_from, period_to, next_due, delete_after, item_count, marked_count, state_count, pending)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		cc, id, c.v.id, name, dsActive, m.stamp(), k.value, c.v.typ, c.keyBy, c.rhythm, nilIfEmpty(k.partner), nilIfEmpty(k.process), nilIfEmpty(userOf(ctx)),
		nilIfEmpty(k.from), nilIfEmpty(k.to), m.today(), deleteAfter, 0, 0, 0, c.keyBy == keyProcess)
	if err != nil {
		return dsRow{}, err
	}
	return dsRow{cc: cc, id: id, view: c.v.id, key: k.value, name: name, status: dsActive, rhythm: c.rhythm, from: k.from, to: k.to,
		partner: k.partner, process: k.process, pending: c.keyBy == keyProcess}, nil
}

// --- Sammeln ----------------------------------------------------------------------------------

// scope: Einschränkung beim Sammeln (Zeitraum, Partner und Vorgang des Datasets).
type scope struct {
	from, to, partner, process string
	since                      string // Änderungszeiger: abgelegt ab (leer = alles)
}

// collect: Dokumente nach den Regeln der Sichtart; ein Dokument einmal (erste Regel).
func (m *Module) collect(ctx context.Context, c *viewCfg, sc scope) ([]doc, []string, error) {
	params := map[string]string{}
	for k, v := range c.v.params {
		params[k] = v
	}
	if sc.from != "" || sc.to != "" {
		params["date_from"], params["date_to"] = sc.from, sc.to
	}
	if sc.partner != "" {
		params["partner_id"] = sc.partner
	}
	if sc.process != "" {
		params["process_id"] = sc.process
	}
	extra := map[string]any{}
	if sc.since != "" {
		extra["filed_since"] = sc.since
	}
	if sc.partner != "" {
		extra["partner_id"] = sc.partner
	}
	var picked []doc
	index := map[string]int{}
	var notes []string
	for _, r := range c.rules {
		docs, skip, err := m.selectRule(ctx, c.v.cc, params, r, extra)
		if err != nil {
			return nil, nil, err
		}
		if skip != "" {
			notes = append(notes, fmt.Sprintf("Regel %d übersprungen: %s", r.no, skip))
			continue
		}
		for _, d := range docs {
			if r.exclude {
				if i, ok := index[d.ID]; ok {
					picked[i].ID = ""
					delete(index, d.ID)
				}
				continue
			}
			if _, ok := index[d.ID]; ok {
				continue
			}
			d.rule, d.ruleName, d.folderPattern = r.no, r.name, c.typ.folder
			if r.folder != "" {
				d.folderPattern = r.folder
			}
			index[d.ID] = len(picked)
			picked = append(picked, d)
		}
	}
	var out []doc
	for _, d := range picked {
		if d.ID != "" {
			out = append(out, d)
		}
	}
	return out, notes, nil
}

// --- Erneuern ---------------------------------------------------------------------------------

type refreshOpts struct {
	full    bool   // vollständig prüfen (auch: passt nicht mehr)
	keyDate string // Stichtag der Stände (leer = heute)
	all     bool   // alle aktiven Datasets der Sicht (von Hand), sonst nur fällige
}

type refreshResult struct {
	created, refreshed, states, added, marked, deleted int
	notes                                              []string
	lastRun                                            string
}

func (r refreshResult) message(prefix string) string {
	msg := fmt.Sprintf("%s: %d Datasets neu, %d erneuert, %d neue Stände – %d Dokumente neu, %d gekennzeichnet",
		prefix, r.created, r.refreshed, r.states, r.added, r.marked)
	if r.deleted > 0 {
		msg += fmt.Sprintf(", %d Datasets gelöscht (Lebensdauer)", r.deleted)
	}
	if len(r.notes) > 0 {
		msg += "\n" + strings.Join(r.notes, "\n")
	}
	return msg
}

// refreshView: neue Schlüssel suchen (Zeiger der Sicht) und die Datasets der Sicht erneuern.
func (m *Module) refreshView(ctx context.Context, cc, id string, o refreshOpts) (refreshResult, error) {
	var out refreshResult
	if err := m.migrateRuns(ctx); err != nil {
		return out, err
	}
	c, err := m.loadCfg(ctx, cc, id)
	if err != nil {
		return out, err
	}
	if c == nil {
		return out, fmt.Errorf("%w: Sicht %s|%s", sdk.ErrNotFound, cc, id)
	}
	pm := map[string]any{}
	for k, x := range c.v.params {
		pm[k] = x
	}
	if err := missingParams(effectiveParams(c.typ.params, c.keyBy), pm); err != nil {
		return out, err
	}
	if o.keyDate == "" {
		o.keyDate = m.today()
	}
	start := m.stamp()
	// 1. neue Schlüssel
	keys := map[string]dsKey{}
	var order []string
	add := func(k dsKey) {
		if _, ok := keys[k.value]; !ok {
			keys[k.value] = k
			order = append(order, k.value)
		}
	}
	changed := map[string]bool{} // Vorgänge mit neuen Ereignissen
	seq := c.eventSeq
	switch c.keyBy {
	case keySicht:
		add(dsKey{from: c.v.params["date_from"], to: c.v.params["date_to"]})
	case keyProcess:
		evs, max, err := m.processEvents(ctx, cc, c.eventSeq)
		if err != nil {
			out.notes = append(out.notes, "Ereignisse der Vorgänge nicht gelesen: "+err.Error())
		}
		seq = max
		if p := c.v.params["process_id"]; p != "" {
			evs = filterEqual(evs, p)
			if c.eventSeq == 0 {
				evs = append(evs, p)
			}
		}
		for _, p := range evs {
			changed[p] = true
			add(dsKey{value: p, process: p, from: c.v.params["date_from"], to: c.v.params["date_to"]})
		}
	default:
		sc := scope{from: c.v.params["date_from"], to: c.v.params["date_to"], since: c.pointer}
		if o.full {
			sc.since = ""
		}
		if sc.from == "" && sc.since == "" {
			sc.from = discoveryFrom(c.keyBy, o.keyDate)
		}
		docs, notes, err := m.collect(ctx, c, sc)
		if err != nil {
			return out, err
		}
		out.notes = append(out.notes, notes...)
		skipped := 0
		for _, d := range docs {
			k, ok := keyFor(c, d)
			if !ok {
				skipped++
				continue
			}
			add(k)
		}
		if skipped > 0 {
			out.notes = append(out.notes, fmt.Sprintf("%d Dokumente ohne %s – keinem Dataset zugeordnet", skipped, map[bool]string{true: "Partner", false: "Belegdatum"}[partnerKey(c.keyBy)]))
		}
	}
	existing, err := m.loadDatasets(ctx, "company_code = ? AND view_id = ?", cc, id)
	if err != nil {
		return out, err
	}
	byKey := map[string]*dsRow{}
	for i := range existing {
		byKey[existing[i].key] = &existing[i]
	}
	fresh := map[string]bool{}
	for _, kv := range order {
		k := keys[kv]
		if ds, ok := byKey[kv]; ok {
			if ds.status == dsActive && changed[kv] && !ds.pending {
				if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET pending = ? WHERE company_code = ? AND dataset_id = ?", true, cc, ds.id); err != nil {
					return out, err
				}
				ds.pending = true
			}
			continue
		}
		if c.keyBy == keyProcess && m.processEnded(ctx, c, cc, kv) != "" {
			continue // abgeschlossener Vorgang: kein neues Dataset
		}
		if c.lifetime != nil && k.to != "" && c.keyBy != keyProcess {
			if d, _ := addMonths(k.to, *c.lifetime).(string); d != "" && d < o.keyDate {
				continue // schon abgelaufen
			}
		}
		name := m.datasetName(ctx, c, k)
		ds, err := m.createDataset(ctx, c, k, name)
		if err != nil {
			return out, err
		}
		existing = append(existing, ds)
		byKey[kv] = &existing[len(existing)-1]
		fresh[ds.id] = true
		out.created++
	}
	if _, err := m.db.Exec(ctx, "UPDATE export__view SET pointer = ?, event_seq = ?, checked_at = ? WHERE company_code = ? AND view_id = ?",
		start, seq, start, cc, id); err != nil {
		return out, err
	}
	// 2. Datasets erneuern: neue, von Hand alle aktiven, sonst die fälligen
	due, err := m.loadDatasets(ctx, "company_code = ? AND view_id = ? AND status = ? AND (next_due IS NULL OR next_due <= ?)", cc, id, dsActive, o.keyDate)
	if err != nil {
		return out, err
	}
	isDue := map[string]bool{}
	for _, d := range due {
		if d.rhythm != rhManual && d.rhythm != "" {
			isDue[d.id] = true
		}
	}
	for _, ds := range existing {
		if ds.status != dsActive || !(fresh[ds.id] || o.all || isDue[ds.id]) {
			continue
		}
		r, err := m.refreshDataset(ctx, c, ds, o)
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("%s %s: %v", ds.id, ds.name, err))
			continue
		}
		out.refreshed++
		out.states += r.states
		out.added += r.added
		out.marked += r.marked
		out.notes = append(out.notes, r.notes...)
		if r.lastRun != "" {
			out.lastRun = r.lastRun
		}
	}
	// 3. Ende und Lebensdauer
	n, notes, err := m.expire(ctx, c, o.keyDate)
	if err != nil {
		return out, err
	}
	out.deleted += n
	out.notes = append(out.notes, notes...)
	if out.lastRun != "" {
		if _, err := m.db.Exec(ctx, "UPDATE export__view SET last_run = ? WHERE company_code = ? AND view_id = ?", out.lastRun, cc, id); err != nil {
			return out, err
		}
	}
	return out, nil
}

func filterEqual(list []string, v string) []string {
	var out []string
	for _, x := range list {
		if x == v {
			out = append(out, x)
		}
	}
	return out
}

// effectiveParams: Pflichtangaben ohne die, die der Schlüssel mitbringt.
func effectiveParams(params, keyBy string) string {
	var out []string
	for _, p := range splitCodes(params) {
		if (p == "period" && periodKey(keyBy)) || (p == "process" && keyBy == keyProcess) || (p == "partner" && partnerKey(keyBy)) {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ", ")
}

// processEvents: Vorgänge mit Ereignissen nach seq (Ereignisjournal des Moduls process).
func (m *Module) processEvents(ctx context.Context, cc string, since int) ([]string, int, error) {
	resp, err := m.services.Call(ctx, "ProcessEvent", "list", map[string]any{"query": map[string]any{"company_code": cc, "since": since}})
	if err != nil {
		if errorsIsUnimplemented(err) {
			return nil, since, nil
		}
		return nil, since, err
	}
	max := since
	seen := map[string]bool{}
	var out []string
	for _, it := range listItems(resp.Payload) {
		if n := toInt(it["seq"]); n > max {
			max = n
		}
		p := crud.Str(it["process_id"])
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, max, nil
}

// processEnded: Datum des Endes (Feld end_field am Vorgang), leer = läuft noch.
func (m *Module) processEnded(ctx context.Context, c *viewCfg, cc, pid string) string {
	if c.endField == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "Process", "get", map[string]any{"id": cc + "|" + pid})
	if err != nil {
		return ""
	}
	var r map[string]any
	_ = sdk.Decode(resp.Payload, &r)
	v := crud.Str(r[c.endField])
	if d := dateStr(v); d != "" {
		return d
	}
	if len(v) >= 10 {
		return v[:10]
	}
	return v
}

// item: Element im neuesten Stand.
type dsItem struct {
	no                                    int
	docID, mark, docType, partner, source string
	folder, file                          string
	excluded                              bool
}

type markSet struct {
	mark, note, by string
}

type dsChange struct {
	states, added, marked int
	notes                 []string
	lastRun               string
}

// refreshDataset: neue Dokumente seit dem Zeiger, Kennzeichen; neuer Stand nur bei Änderungen.
func (m *Module) refreshDataset(ctx context.Context, c *viewCfg, ds dsRow, o refreshOpts) (dsChange, error) {
	var out dsChange
	cc := ds.cc
	keyDate := o.keyDate
	if keyDate == "" {
		keyDate = m.today()
	}
	start := m.stamp()
	// Fachmodul erzeugt vorher (z. B. Process.storeSheet: Vorgang als PDF ins DMS)
	if ds.pending && c.refreshCall != "" {
		obj, act, ok := strings.Cut(c.refreshCall, ".")
		if ok {
			if _, err := m.services.Call(ctx, obj, act, map[string]any{"id": cc + "|" + ds.key, "data": map[string]any{}}); err != nil {
				out.notes = append(out.notes, fmt.Sprintf("%s: %s nicht ausgeführt – %v", ds.id, c.refreshCall, err))
			}
		}
	}
	sc := scope{from: ds.from, to: ds.to, partner: ds.partner, process: ds.process, since: ds.pointer}
	if !periodKey(c.keyBy) { // Zeitraum der Sicht (kann sich ändern)
		sc.from, sc.to = c.v.params["date_from"], c.v.params["date_to"]
	}
	if o.full || ds.states == 0 {
		sc.since = ""
	}
	docs, notes, err := m.collect(ctx, c, sc)
	if err != nil {
		return out, err
	}
	if ds.states == 0 {
		out.notes = append(out.notes, notes...)
	}
	// neuester Stand
	var items []dsItem
	lastStatus := ""
	if ds.lastRun != "" {
		if r, err := m.one(ctx, "SELECT status FROM export__run WHERE company_code = ? AND run_id = ?", cc, ds.lastRun); err != nil {
			return out, err
		} else if r != nil {
			lastStatus = crud.Str(r[0])
		}
		res, err := m.db.Query(ctx, `SELECT item_no, document_id, COALESCE(mark, ''), COALESCE(doc_type, ''), COALESCE(partner_id, ''), COALESCE(source_name, ''),
			COALESCE(folder, ''), file_name, excluded FROM export__item WHERE company_code = ? AND run_id = ? ORDER BY item_no`, cc, ds.lastRun)
		if err != nil {
			return out, err
		}
		for _, x := range res.Rows {
			items = append(items, dsItem{no: toInt(x[0]), docID: crud.Str(x[1]), mark: crud.Str(x[2]), docType: crud.Str(x[3]), partner: crud.Str(x[4]),
				source: crud.Str(x[5]), folder: crud.Str(x[6]), file: crud.Str(x[7]), excluded: crud.AsBool(x[8])})
		}
	}
	have := map[string]int{}
	for i, it := range items {
		have[it.docID] = i
	}
	var fresh []doc
	found := map[string]bool{}
	for _, d := range docs {
		found[d.ID] = true
		if _, ok := have[d.ID]; !ok {
			fresh = append(fresh, d)
			have[d.ID] = -1
		}
	}
	sort.SliceStable(fresh, func(i, j int) bool { return fresh[i].Filed < fresh[j].Filed })
	marks := map[string]markSet{} // Dokument → neues Kennzeichen
	unmarked := func(docID string) bool {
		if _, ok := marks[docID]; ok {
			return false
		}
		if i, ok := have[docID]; ok && i >= 0 {
			return items[i].mark == ""
		}
		return true
	}
	// a) Status im DMS (gelöscht, verworfen, weitergeleitet)
	if len(items) > 0 {
		for st, mk := range dmsMarks {
			q := map[string]any{"company_code": cc, "status": st}
			if sc.since != "" {
				q["changed_since"] = sc.since
			}
			resp, err := m.services.Call(ctx, "DmsDocument", "list", map[string]any{"query": q})
			if err != nil {
				return out, fmt.Errorf("Status im DMS lesen: %w", err)
			}
			for _, it := range listItems(resp.Payload) {
				id := crud.Str(it["id"])
				if i, ok := have[id]; ok && i >= 0 && unmarked(id) {
					marks[id] = markSet{mark: mk, note: crud.Str(it["removed_at"])}
				}
			}
		}
	}
	// b) Kennzeichenregeln (z. B. Rechnung storniert)
	cancelled, err := m.cancelledDocs(ctx, cc)
	if err != nil {
		out.notes = append(out.notes, err.Error())
	}
	for id, label := range cancelled {
		if _, ok := have[id]; ok && unmarked(id) {
			marks[id] = markSet{mark: markCancelled, note: label}
		}
	}
	// c) ersetzt: gleiche Dokumentart, gleicher Partner, gleiche Datei im DMS
	if c.replaceSame {
		latest := map[string]string{}
		sig := func(t, p, s string) string { return t + "\x00" + p + "\x00" + strings.ToLower(s) }
		for _, it := range items {
			if it.mark == "" && it.source != "" {
				latest[sig(it.docType, it.partner, it.source)] = it.docID
			}
		}
		for _, d := range fresh {
			if d.FileName == "" {
				continue
			}
			k := sig(d.DocType, d.Partner, d.FileName)
			if old, ok := latest[k]; ok && unmarked(old) {
				marks[old] = markSet{mark: markReplaced, by: d.ID}
			}
			latest[k] = d.ID
		}
	}
	// d) vollständig: passt nicht mehr
	if o.full {
		for _, it := range items {
			if it.mark == "" && !found[it.docID] && unmarked(it.docID) {
				if r, err := m.one(ctx, "SELECT added FROM export__item WHERE company_code = ? AND run_id = ? AND item_no = ?", cc, ds.lastRun, it.no); err == nil && r != nil && crud.AsBool(r[0]) {
					continue // von Hand hinzugenommen
				}
				marks[it.docID] = markSet{mark: markNoMatch}
			}
		}
	}
	now := m.today()
	upd := func() error {
		_, err := m.db.Exec(ctx, "UPDATE export__dataset SET pointer = ?, refreshed_at = ?, next_due = ?, pending = ? WHERE company_code = ? AND dataset_id = ?",
			start, start, nextDue(ds.rhythm, now), false, cc, ds.id)
		return err
	}
	if len(fresh) == 0 && len(marks) == 0 && ds.states > 0 {
		return out, upd()
	}
	// Namen vor der Transaktion
	nm := m.newNamer(ctx, cc)
	sortDocs(fresh, c.typ.sortBy, nm)
	used := map[string]bool{}
	next := 1
	for _, it := range items {
		used[strings.ToLower(it.folder+"/"+it.file)] = true
		if it.no >= next {
			next = it.no + 1
		}
	}
	type placed struct{ folder, file string }
	places := make([]placed, len(fresh))
	for i := range fresh {
		places[i].folder, places[i].file = nm.place(c.typ.file, &fresh[i], next+i, used)
	}
	runID := ds.lastRun
	newRun := lastStatus != runOpen
	if newRun {
		if runID, err = m.nextRunID(ctx, cc, keyDate[:4]); err != nil {
			return out, err
		}
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if newRun {
			if _, err := m.db.Exec(ctx, `INSERT INTO export__run (company_code, run_id, view_id, name, status, key_date, created_at, view_type, params, created_by,
				item_count, excluded_count, message, dataset_id, state_no) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, runID, ds.view, ds.name, runOpen, keyDate,
				start, c.v.typ, scopeJSON(ds), nilIfEmpty(userOf(ctx)), 0, 0, nilIfEmpty(strings.Join(notes, "\n")), ds.id, ds.states+1); err != nil {
				return err
			}
			if ds.lastRun != "" {
				// alle Elemente des vorigen Stands übernehmen (nicht mehr neu)
				if _, err := m.db.Exec(ctx, `INSERT INTO export__item (company_code, run_id, item_no, document_id, folder, file_name, title, doc_type, doc_category,
					partner_id, partner_name, file_account, sha256, reason, doc_date, rule_no, size, excluded, added, mark, mark_note, mark_on, replaced_by, first_run, source_name, is_new)
					SELECT company_code, ?, item_no, document_id, folder, file_name, title, doc_type, doc_category, partner_id, partner_name, file_account, sha256, reason,
					doc_date, rule_no, size, excluded, added, mark, mark_note, mark_on, replaced_by, first_run, source_name, ?
					FROM export__item WHERE company_code = ? AND run_id = ?`, runID, false, cc, ds.lastRun); err != nil {
					return err
				}
			}
		} else if _, err := m.db.Exec(ctx, "UPDATE export__run SET key_date = ? WHERE company_code = ? AND run_id = ?", keyDate, cc, runID); err != nil {
			return err
		}
		for i, d := range fresh {
			if err := m.insertItem(ctx, cc, runID, next+i, d, places[i].folder, places[i].file, false, ""); err != nil {
				return err
			}
			if _, err := m.db.Exec(ctx, "UPDATE export__item SET is_new = ?, first_run = ?, source_name = ? WHERE company_code = ? AND run_id = ? AND item_no = ?",
				true, runID, nilIfEmpty(d.FileName), cc, runID, next+i); err != nil {
				return err
			}
		}
		for docID, mk := range marks {
			if _, err := m.db.Exec(ctx, "UPDATE export__item SET mark = ?, mark_note = ?, mark_on = ?, replaced_by = ? WHERE company_code = ? AND run_id = ? AND document_id = ?",
				mk.mark, nilIfEmpty(mk.note), now, nilIfEmpty(mk.by), cc, runID, docID); err != nil {
				return err
			}
		}
		if err := m.recount(ctx, cc, runID); err != nil {
			return err
		}
		states := ds.states
		if newRun {
			states++
		}
		if _, err := m.db.Exec(ctx, `UPDATE export__dataset SET last_run = ?, state_count = ?,
			item_count = (SELECT item_count FROM export__run WHERE company_code = ? AND run_id = ?),
			marked_count = (SELECT marked_count FROM export__run WHERE company_code = ? AND run_id = ?)
			WHERE company_code = ? AND dataset_id = ?`, runID, states, cc, runID, cc, runID, cc, ds.id); err != nil {
			return err
		}
		return upd()
	})
	if err != nil {
		return out, err
	}
	if newRun {
		out.states = 1
	}
	out.added, out.marked, out.lastRun = len(fresh), len(marks), runID
	return out, nil
}

func scopeJSON(ds dsRow) string {
	var parts []string
	for _, kv := range [][2]string{{"schluessel", ds.key}, {"date_from", ds.from}, {"date_to", ds.to}, {"partner_id", ds.partner}, {"process_id", ds.process}} {
		if kv[1] != "" {
			parts = append(parts, fmt.Sprintf("%q:%q", kv[0], kv[1]))
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// cancelledDocs: Dokumente, deren verknüpftes Objekt nach einer Kennzeichenregel storniert ist.
func (m *Module) cancelledDocs(ctx context.Context, cc string) (map[string]string, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, COALESCE(ref_object, ''), COALESCE(key_field, ''), COALESCE(status_field, ''), COALESCE(statuses, ''),
		COALESCE(mark_label, '') FROM export__mark_rule WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`, cc, true)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	var errs []string
	for _, r := range res.Rows {
		obj, keyField, stField, label := crud.Str(r[2]), crud.Str(r[3]), crud.Str(r[4]), firstNonEmpty(crud.Str(r[6]), crud.Str(r[1]))
		for _, st := range splitCodes(crud.Str(r[5])) {
			resp, err := m.services.Call(ctx, obj, "list", map[string]any{"query": map[string]any{"company_code": cc, stField: st}})
			if err != nil {
				if !errorsIsUnimplemented(err) {
					errs = append(errs, fmt.Sprintf("Kennzeichenregel %s: %v", crud.Str(r[0]), err))
				}
				break
			}
			for _, it := range listItems(resp.Payload) {
				key := crud.Str(it[keyField])
				if key == "" {
					continue
				}
				for _, k := range []string{cc + "|" + key, key} {
					dr, err := m.services.Call(ctx, "DmsDocument", "list", map[string]any{"query": map[string]any{"company_code": cc, "ref": obj + "|" + k}})
					if err != nil {
						continue
					}
					for _, d := range listItems(dr.Payload) {
						out[crud.Str(d["id"])] = fmt.Sprintf("%s (%s %s)", label, obj, key)
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

// --- Ende, Lebensdauer, Löschen -----------------------------------------------------------------

// expire: Vorgang abgeschlossen → Dataset abgeschlossen (Frist end_days); abgelaufene löschen.
func (m *Module) expire(ctx context.Context, c *viewCfg, today string) (int, []string, error) {
	cc := c.v.cc
	var notes []string
	if c.keyBy == keyProcess && c.endField != "" {
		open, err := m.loadDatasets(ctx, "company_code = ? AND view_id = ? AND status = ?", cc, c.v.id, dsActive)
		if err != nil {
			return 0, nil, err
		}
		for _, ds := range open {
			end := m.processEnded(ctx, c, cc, ds.key)
			if end == "" {
				continue
			}
			var after any
			switch {
			case c.endDays != nil:
				after = addDaysDate(end, *c.endDays)
			case c.lifetime != nil:
				after = addMonths(end, *c.lifetime)
			}
			if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET status = ?, closed_at = ?, delete_after = ?, next_due = NULL WHERE company_code = ? AND dataset_id = ?",
				dsClosed, m.stamp(), after, cc, ds.id); err != nil {
				return 0, nil, err
			}
			notes = append(notes, fmt.Sprintf("%s %s: Vorgang beendet am %s – abgeschlossen", ds.id, ds.name, germanDate(end)))
		}
	}
	gone, err := m.loadDatasets(ctx, "company_code = ? AND view_id = ? AND status <> ? AND delete_after IS NOT NULL AND delete_after <= ?", cc, c.v.id, dsDeleted, today)
	if err != nil {
		return 0, nil, err
	}
	for _, ds := range gone {
		if err := m.deleteDataset(ctx, ds, "Lebensdauer abgelaufen"); err != nil {
			return 0, nil, err
		}
		notes = append(notes, fmt.Sprintf("%s %s: gelöscht (Lebensdauer)", ds.id, ds.name))
	}
	return len(gone), notes, nil
}

// deleteDataset: Elemente aller Stände entfernen; Stände und Lieferungen bleiben als Protokoll.
func (m *Module) deleteDataset(ctx context.Context, ds dsRow, reason string) error {
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, `SELECT run_id, COALESCE(exported_at, ''), COALESCE(target, ''), COALESCE(export_path, ''), COALESCE(manifest_sha256, ''),
			COALESCE(item_count, 0) FROM export__run WHERE company_code = ? AND dataset_id = ? ORDER BY run_id`, ds.cc, ds.id)
		if err != nil {
			return err
		}
		var lines []string
		for _, r := range res.Rows {
			if crud.Str(r[1]) == "" {
				lines = append(lines, fmt.Sprintf("Stand %s: %d Dokumente, nicht geliefert", crud.Str(r[0]), toInt(r[5])))
				continue
			}
			lines = append(lines, fmt.Sprintf("Lieferung %s am %s an %s %s, %d Dokumente, Manifest %s", crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]),
				crud.Str(r[3]), toInt(r[5]), crud.Str(r[4])))
		}
		lines = append(lines, fmt.Sprintf("%s gelöscht von %s: %s", m.stamp(), firstNonEmpty(userOf(ctx), "Zeitgeber"), reason))
		if _, err := m.db.Exec(ctx, `DELETE FROM export__item WHERE company_code = ? AND run_id IN (SELECT run_id FROM export__run WHERE company_code = ? AND dataset_id = ?)`,
			ds.cc, ds.cc, ds.id); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, "UPDATE export__run SET status = ? WHERE company_code = ? AND dataset_id = ?", runDeleted, ds.cc, ds.id); err != nil {
			return err
		}
		_, err = m.db.Exec(ctx, `UPDATE export__dataset SET status = ?, deleted_at = ?, deleted_by = ?, next_due = NULL, pending = ?,
			log = TRIM(COALESCE(log, '') || ? || ?) WHERE company_code = ? AND dataset_id = ?`,
			dsDeleted, m.stamp(), nilIfEmpty(userOf(ctx)), false, "\n", strings.Join(lines, "\n"), ds.cc, ds.id)
		return err
	})
}

// --- Übernahme der Läufe aus 0.2 -------------------------------------------------------------

// migrateRuns: exportierte Läufe ohne Dataset werden Stände eines Datasets ihrer Sicht
// (Schlüssel der Sichtart; Sichtarten ohne Schlüssel erst nach „Vorschläge ergänzen“).
func (m *Module) migrateRuns(ctx context.Context) error {
	res, err := m.db.Query(ctx, `SELECT r.company_code, r.view_id, r.run_id, r.key_date, r.created_at FROM export__run r
		JOIN export__view v ON v.company_code = r.company_code AND v.view_id = r.view_id
		JOIN export__view_type t ON t.company_code = v.company_code AND t.code = v.view_type
		WHERE r.dataset_id IS NULL AND r.status = ? AND COALESCE(v.key_by, t.key_by) IS NOT NULL ORDER BY r.company_code, r.view_id, r.run_id`, runExported)
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	for _, r := range res.Rows {
		cc, view, run := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2])
		c, err := m.loadCfg(ctx, cc, view)
		if err != nil || c == nil {
			continue
		}
		k := dsKey{from: c.v.params["date_from"], to: c.v.params["date_to"]}
		switch c.keyBy {
		case keySicht:
		case keyYear:
			y := firstNonEmpty(c.v.params["date_from"], dateStr(r[3]))[:4]
			k = dsKey{value: y, from: y + "-01-01", to: y + "-12-31"}
		default:
			continue // Läufe je Partner bzw. Vorgang lassen sich nicht zuordnen
		}
		ds, err := m.loadDatasets(ctx, "company_code = ? AND view_id = ? AND key_value = ?", cc, view, k.value)
		if err != nil {
			return err
		}
		var d dsRow
		if len(ds) > 0 {
			d = ds[0]
		} else if d, err = m.createDataset(ctx, c, k, m.datasetName(ctx, c, k)); err != nil {
			return err
		}
		err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
			if _, err := m.db.Exec(ctx, "UPDATE export__run SET dataset_id = ?, state_no = ? WHERE company_code = ? AND run_id = ?", d.id, d.states+1, cc, run); err != nil {
				return err
			}
			if _, err := m.db.Exec(ctx, `UPDATE export__item SET first_run = COALESCE(first_run, ?), is_new = ?, source_name = COALESCE(source_name, file_name)
				WHERE company_code = ? AND run_id = ?`, run, d.states == 0, cc, run); err != nil {
				return err
			}
			if err := m.recount(ctx, cc, run); err != nil {
				return err
			}
			// ohne Zeiger: der nächste Stand prüft alles (der Lauf kannte keinen Zeiger)
			_, err := m.db.Exec(ctx, `UPDATE export__dataset SET last_run = ?, last_delivery = ?, state_count = ?, pointer = NULL, refreshed_at = ?,
				next_due = ?, item_count = (SELECT item_count FROM export__run WHERE company_code = ? AND run_id = ?),
				log = TRIM(COALESCE(log, '') || ? || ?) WHERE company_code = ? AND dataset_id = ?`, run, run, d.states+1, crud.Str(r[4]),
				nextDue(d.rhythm, m.today()), cc, run, "\n", fmt.Sprintf("%s: Lauf %s aus 0.2 als Stand %d übernommen", m.stamp(), run, d.states+1), cc, d.id)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// --- Zeitgeber --------------------------------------------------------------------------------

func (m *Module) startTimer() {
	if m.cfg.CheckMinutes < 0 {
		return
	}
	every := time.Duration(m.cfg.CheckMinutes) * time.Minute
	if every == 0 {
		every = 15 * time.Minute
	}
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		first := time.NewTimer(2 * time.Minute) // nach dem Start (Schema angepasst)
		tick := time.NewTicker(every)
		defer first.Stop()
		defer tick.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-first.C:
				m.tick(context.Background())
			case <-tick.C:
				m.tick(context.Background())
			}
		}
	}()
}

// tick: alle aktiven Sichten mit Rhythmus erneuern; Lebensdauer auch bei „nur von Hand“.
func (m *Module) tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.db.Query(ctx, `SELECT v.company_code, v.view_id, COALESCE(v.rhythm, t.rhythm, '') FROM export__view v
		JOIN export__view_type t ON t.company_code = v.company_code AND t.code = v.view_type WHERE v.is_active = ? ORDER BY v.company_code, v.view_id`, true)
	if err != nil {
		m.log.Warn("Sichten nicht gelesen", "error", err)
		return
	}
	for _, r := range res.Rows {
		cc, id, rh := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2])
		if rh == "" || rh == rhManual {
			c, err := m.loadCfg(ctx, cc, id)
			if err == nil && c != nil {
				if _, _, err := m.expire(ctx, c, m.today()); err != nil {
					m.log.Warn("Lebensdauer nicht geprüft", "view", cc+"|"+id, "error", err)
				}
			}
			continue
		}
		out, err := m.refreshView(ctx, cc, id, refreshOpts{})
		if err != nil {
			m.log.Warn("Sicht nicht erneuert", "view", cc+"|"+id, "error", err)
			continue
		}
		if out.created+out.states+out.added+out.marked+out.deleted > 0 {
			m.log.Info(out.message("Sicht "+cc+"|"+id), "view", cc+"|"+id)
		}
	}
}

// --- Oberfläche -------------------------------------------------------------------------------

func (m *Module) dataset() *crud.Entity {
	return &crud.Entity{
		Object: datasetObject, Title: "Datasets", Icon: "icon-database", Table: "export__dataset", Section: "Sichten", NoCreate: true,
		Keys: []string{"company_code", "dataset_id"}, Order: "company_code, dataset_id DESC", TitleField: "name",
		Filters: []string{"company_code", "view_id", "status", "partner_id", "process_id"}, Search: []string{"dataset_id", "name", "key_value"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "dataset_id", Label: "Dataset", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Listable: true, Wide: true},
			{Key: "view_id", Label: "Sicht", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogViewLookup},
			{Key: "view_type", Label: "Sichtart", Type: tText, ReadOnly: true, Lookup: catalogLookup(viewTypeObject)},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: datasetStatus},
			{Key: "key_by", Label: "Schlüssel", Type: tSel, ReadOnly: true, Options: keyOptions},
			{Key: "key_value", Label: "Schlüsselwert", Short: "Schlüssel", Type: tText, Listable: true, ReadOnly: true},
			{Key: "period_from", Label: "Zeitraum von", Short: "von", Type: tDate, ReadOnly: true, Group: "Umfang"},
			{Key: "period_to", Label: "Zeitraum bis", Short: "bis", Type: tDate, ReadOnly: true, Group: "Umfang"},
			{Key: "partner_id", Label: "Partner", Type: tText, ReadOnly: true, Group: "Umfang", Lookup: lookupPartner},
			{Key: "process_id", Label: "Vorgang", Type: tText, ReadOnly: true, Group: "Umfang", Lookup: lookupProcess},
			{Key: "rhythm", Label: "Rhythmus der Erneuerung", Short: "Rhythmus", Type: tSel, Listable: true, Options: rhythmOptions, Group: "Erneuerung"},
			{Key: "next_due", Label: "Nächste Erneuerung", Short: "fällig", Type: tDate, Listable: true, Group: "Erneuerung"},
			{Key: "pointer", Label: "Änderungszeiger (nachgeführt bis)", Type: tText, ReadOnly: true, Group: "Erneuerung"},
			{Key: "refreshed_at", Label: "Zuletzt erneuert", Type: tText, ReadOnly: true, Group: "Erneuerung"},
			{Key: "pending", Label: "Änderung gemeldet (Erzeugen vor dem Sammeln)", Type: tBool, ReadOnly: true, Group: "Erneuerung"},
			{Key: "delete_after", Label: "Löschen ab (Lebensdauer; leer = bleibt)", Short: "löschen ab", Type: tDate, Listable: true, Group: "Erneuerung"},
			{Key: "state_count", Label: "Stände", Type: tNum, ReadOnly: true},
			{Key: "item_count", Label: "Dokumente", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "marked_count", Label: "Gekennzeichnet", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "last_run", Label: "Neuester Stand", Type: tText, ReadOnly: true},
			{Key: "last_delivery", Label: "Letzte Lieferung", Type: tText, ReadOnly: true},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "created_at", Label: "Angelegt am", Type: tText, ReadOnly: true},
			{Key: "closed_at", Label: "Abgeschlossen am", Type: tText, ReadOnly: true},
			{Key: "deleted_at", Label: "Gelöscht am", Type: tText, ReadOnly: true},
			{Key: "deleted_by", Label: "Gelöscht von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "log", Label: "Protokoll (Lieferungen, Übernahme, Löschen)", Type: tArea, ReadOnly: true},
			{Key: "refresh_full", Label: "Vollständig prüfen (auch Dokumente, die nicht mehr passen)", Type: tBool, ActionOnly: true},
			{Key: "delete_reason", Label: "Grund (steht im Protokoll)", Type: tText, ActionOnly: true, Required: true},
		},
		Sections: []metamodel.SectionDefinition{{Key: "staende", Title: "Stände", Relation: &metamodel.Relation{Object: runObject, ForeignKey: "dataset_id",
			Match: match("dataset_id", "dataset_id"), Columns: []string{"run_id", "state_no", "key_date", "status", "item_count", "new_count", "marked_count", "exported_at", "target"}}}},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "refresh", Label: "Erneuern …", Icon: "icon-refresh", Record: true, Fields: []string{"refresh_full"}}, Handle: m.datasetRefreshAction},
			{ActionConfig: metamodel.ActionConfig{Name: "close", Label: "Abschließen", Record: true}, Handle: m.datasetStatusAction(dsClosed)},
			{ActionConfig: metamodel.ActionConfig{Name: "reopen", Label: "Wieder aktiv", Record: true}, Handle: m.datasetStatusAction(dsActive)},
			{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Löschen …", Icon: "icon-trash", Record: true, Fields: []string{"delete_reason"}}, Handle: m.datasetDeleteAction},
		},
		Access: &crud.Access{Object: datasetObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, datasetObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old != nil && crud.Str(old["status"]) == dsDeleted {
				return crud.Invalid("Dataset %s ist gelöscht", crud.Str(old["dataset_id"]))
			}
			rec["rhythm"] = trimUpper(rec["rhythm"])
			if rec["rhythm"] == "" {
				rec["rhythm"] = rhManual
			}
			if !validOption(rhythmOptions, crud.Str(rec["rhythm"])) {
				return crud.Invalid("Rhythmus %q gibt es nicht", crud.Str(rec["rhythm"]))
			}
			for _, k := range []string{"next_due", "delete_after"} {
				d, err := dateOrNil(rec[k])
				if err != nil {
					return err
				}
				rec[k] = d
			}
			if rec["rhythm"] == rhManual {
				rec["next_due"] = nil
			}
			rec["note"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["note"])))
			return nil
		},
	}
}

func (m *Module) datasetOf(ctx context.Context, id string) (dsRow, error) {
	cc, key, err := splitKey(id, "Dataset")
	if err != nil {
		return dsRow{}, err
	}
	ds, err := m.loadDatasets(ctx, "company_code = ? AND dataset_id = ?", cc, key)
	if err != nil {
		return dsRow{}, err
	}
	if len(ds) == 0 {
		return dsRow{}, fmt.Errorf("%w: Dataset %s", sdk.ErrNotFound, id)
	}
	return ds[0], nil
}

// datasetRefreshAction: ExportDataset.refresh {id, data: {refresh_full}} – sofort erneuern.
func (m *Module) datasetRefreshAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ds, err := m.datasetOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, runObject, "create", ds.cc); err != nil {
		return sdk.Response{}, err
	}
	if ds.status != dsActive {
		return sdk.Response{}, crud.Invalid("Dataset %s ist %s – nur aktive werden erneuert", ds.id, optionLabel(datasetStatus, ds.status))
	}
	c, err := m.loadCfg(ctx, ds.cc, ds.view)
	if err != nil || c == nil {
		return sdk.Response{}, fmt.Errorf("Sicht %s: %w", ds.view, err)
	}
	if c.keyBy == keyProcess {
		ds.pending = true // von Hand: Vorgang neu ausgeben
	}
	r, err := m.refreshDataset(ctx, c, ds, refreshOpts{full: crud.AsBool(in.Data["refresh_full"])})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("%s: %d Dokumente neu, %d gekennzeichnet", ds.name, r.added, r.marked)
	switch {
	case r.states > 0:
		msg += " – neuer Stand " + r.lastRun
	case r.lastRun != "":
		msg += " – im offenen Stand " + r.lastRun
	default:
		msg += " – keine Änderung"
	}
	if len(r.notes) > 0 {
		msg += "\n" + strings.Join(r.notes, "\n")
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "run_id": r.lastRun, "added": r.added, "marked": r.marked}}, nil
}

func (m *Module) datasetStatusAction(status string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in recordInput
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		ds, err := m.datasetOf(ctx, in.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		if err := requireWrite(ctx, datasetObject, "update", ds.cc); err != nil {
			return sdk.Response{}, err
		}
		if ds.status == dsDeleted {
			return sdk.Response{}, crud.Invalid("Dataset %s ist gelöscht", ds.id)
		}
		var closed, due any
		if status == dsClosed {
			closed = m.stamp()
		} else {
			due = nextDue(ds.rhythm, m.today())
		}
		if _, err := m.db.Exec(ctx, "UPDATE export__dataset SET status = ?, closed_at = ?, next_due = ? WHERE company_code = ? AND dataset_id = ?",
			status, closed, due, ds.cc, ds.id); err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%s: %s", ds.name, optionLabel(datasetStatus, status))}}, nil
	}
}

// datasetDeleteAction: ExportDataset.remove {id, data: {delete_reason}}.
func (m *Module) datasetDeleteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	ds, err := m.datasetOf(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, datasetObject, "delete", ds.cc); err != nil {
		return sdk.Response{}, err
	}
	reason := strings.TrimSpace(crud.Str(in.Data["delete_reason"]))
	if reason == "" {
		return sdk.Response{}, crud.Invalid("Bitte einen Grund angeben – er steht im Protokoll")
	}
	if ds.status == dsDeleted {
		return sdk.Response{}, crud.Invalid("Dataset %s ist schon gelöscht", ds.id)
	}
	if err := m.deleteDataset(ctx, ds, reason); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("%s gelöscht – Stände und Lieferungen stehen im Protokoll, die Dokumente bleiben im DMS", ds.name)}}, nil
}

// markRule: Katalog Kennzeichenregeln (z. B. Rechnung storniert).
func (m *Module) markRule() *crud.Entity {
	return &crud.Entity{
		Object: markObject, Customizing: true, Title: "Kennzeichenregeln", Icon: "icon-flag", Table: "export__mark_rule", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "ref_object", Label: "Verknüpftes Object im DMS (z. B. SupplierInvoice)", Short: "Object", Type: tText, Required: true, Listable: true},
			{Key: "key_field", Label: "Schlüsselfeld des Objects (z. B. invoice_id)", Short: "Schlüssel", Type: tText, Required: true},
			{Key: "status_field", Label: "Statusfeld (z. B. status)", Short: "Statusfeld", Type: tText, Required: true},
			{Key: "statuses", Label: "Status, die kennzeichnen (Komma, z. B. CANCELLED)", Short: "Status", Type: tText, Required: true, Listable: true},
			{Key: "mark_label", Label: "Kennzeichen im Inhaltsverzeichnis (z. B. storniert)", Short: "Kennzeichen", Type: tText, Listable: true},
			{Key: "note", Label: "Bemerkung", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge in der Liste", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: catalogAccess(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, markObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if err := codeCheck(rec, old); err != nil {
				return err
			}
			for _, k := range []string{"ref_object", "key_field", "status_field", "mark_label", "note"} {
				rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
			}
			rec["statuses"] = strings.ToUpper(strings.Join(splitCodes(crud.Str(rec["statuses"])), ","))
			if rec["statuses"] == "" {
				return crud.Invalid("Mindestens ein Status")
			}
			return nil
		},
	}
}
