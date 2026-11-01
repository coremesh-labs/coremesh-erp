package process

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Kataloge je Buchungskreis. Jeder Buchungskreis prägt sie selbst aus; beim
// ersten Vorgang im Buchungskreis (oder mit process:setup-company) entstehen die
// Vorschlagswerte (catalogDefaults, stepDefaults, viewDefaults), soweit sie fehlen.

var (
	codeRe   = regexp.MustCompile(`^[A-Z0-9_]{1,16}$`)
	prefixRe = regexp.MustCompile(`^[A-Za-z]{1,4}$`)
)

// Arten der Status (technisch, steuern Pflichtfelder und Zeitgeber).
const (
	kindOpen    = "OPEN"
	kindWaiting = "WAITING"
	kindClosed  = "CLOSED"
)

// Grundlage der Frist eines Schritts.
const (
	basisOpen  = "OPEN"  // ab Eröffnung
	basisPrev  = "PREV"  // ab Erledigung des vorigen Schritts
	basisEvent = "EVENT" // zum Stichtag des Vorgangs (Tage vorher negativ)
)

var (
	statusKinds = []metamodel.Option{{Value: kindOpen, Label: "offen"}, {Value: kindWaiting, Label: "wartet (Erinnerungsdatum)"},
		{Value: kindClosed, Label: "abgeschlossen"}}
	dueBases = []metamodel.Option{{Value: basisOpen, Label: "ab Eröffnung"}, {Value: basisPrev, Label: "ab dem vorigen Schritt"},
		{Value: basisEvent, Label: "zum Stichtag (Tage vorher negativ)"}}
)

func (m *Module) catalogEntities() []*crud.Entity {
	out := make([]*crud.Entity, 0, len(catalogTables))
	for _, c := range catalogTables {
		out = append(out, m.catalogEntity(c))
	}
	return out
}

func (m *Module) catalogEntity(c catalogTable) *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
	}
	var sections []metamodel.SectionDefinition
	switch c.Table {
	case "type":
		fields = append(fields,
			crud.Field{Key: "id_prefix", Label: "Kürzel der Vorgangsnummer (z. B. WV)", Short: "Kürzel", Type: tText, Required: true, Listable: true},
			crud.Field{Key: "perspective", Label: "Perspektive", Type: tText, Listable: true, Lookup: catalogLookup("ProcessPerspective")},
			crud.Field{Key: "children_first", Label: "Abschluss erst, wenn alle Teilvorgänge abgeschlossen sind", Short: "Teilvorgänge zuerst", Type: tBool},
			crud.Field{Key: "event_label", Label: "Bedeutung des Stichtags (z. B. Auszugstermin, Versammlungstermin)", Short: "Stichtag", Type: tText},
			crud.Field{Key: "follow_up_days", Label: "Wiedervorlage nach Tagen (Vorschlag beim Eröffnen)", Short: "Wiedervorlage", Type: tNum, Group: "Fristen"},
			crud.Field{Key: "ack_days", Label: "Eingangsbestätigung binnen Tagen (leer = keine)", Short: "Bestätigung", Type: tNum, Group: "Fristen"},
			crud.Field{Key: "answer_days", Label: "Antwort bzw. Erledigung binnen Tagen (leer = keine)", Short: "Antwort", Type: tNum, Group: "Fristen"},
			crud.Field{Key: "idle_days", Label: "Hinweis ohne Bewegung nach Tagen (leer = keiner)", Short: "Ohne Bewegung", Type: tNum, Group: "Fristen"},
			crud.Field{Key: "escalate_days", Label: "Eskalation nach Tagen über der Frist (leer = keine)", Short: "Eskalation", Type: tNum, Group: "Fristen"},
			crud.Field{Key: "escalate_to", Label: "Eskalation an (Benutzer, sonst der Vertreter des Vorgangs)", Short: "Eskalation an", Type: tText,
				Group: "Fristen", Lookup: crud.LookupUser},
			crud.Field{Key: "keywords", Label: "Stichworte (für die spätere Zuordnung aus DMS und Mail, Komma)", Short: "Stichworte", Type: tText},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tArea})
		sections = []metamodel.SectionDefinition{{Key: "schritte", Title: "Schritte", Relation: &metamodel.Relation{Object: typeStepObject,
			ForeignKey: "process_type", Match: match("process_type", "code"), Columns: []string{"step_no", "name", "due_basis", "due_days", "required"}}}}
	case "status":
		fields = append(fields, crud.Field{Key: "kind", Label: "Art (offen, wartet, abgeschlossen)", Short: "Art", Type: metamodel.TypeSelect,
			Required: true, Listable: true, Options: statusKinds})
	case "entry_kind":
		fields = append(fields, crud.Field{Key: "is_system", Label: "Vom System geschrieben", Short: "System", Type: tBool, Listable: true})
	}
	fields = append(fields,
		crud.Field{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
		crud.Field{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true})
	return &crud.Entity{
		Object: c.Object, Customizing: true, Title: c.Title, Icon: "icon-tag", Table: "process__" + c.Table, Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: fields, Sections: sections,
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, c.Object, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 16 Zeichen)", crud.Str(rec["code"]))
				}
			}
			if c.Table == "type" {
				return m.checkType(ctx, rec)
			}
			return nil
		},
	}
}

// checkType: Kürzel eindeutig im Buchungskreis, Perspektive aus dem Katalog, Tage ≥ 0.
func (m *Module) checkType(ctx context.Context, rec crud.Record) error {
	cc, code := crud.Str(rec["company_code"]), crud.Str(rec["code"])
	prefix := strings.TrimSpace(crud.Str(rec["id_prefix"]))
	if !prefixRe.MatchString(prefix) {
		return crud.Invalid("Kürzel %q: 1–4 Buchstaben, z. B. WV", prefix)
	}
	rec["id_prefix"] = strings.ToUpper(prefix)
	row, err := m.one(ctx, `SELECT code FROM process__type WHERE company_code = ? AND UPPER(id_prefix) = ? AND code <> ?`, cc, rec["id_prefix"], code)
	if err != nil {
		return err
	}
	if row != nil {
		return crud.Invalid("Kürzel %s ist schon bei Vorgangsart %s vergeben", rec["id_prefix"], crud.Str(row[0]))
	}
	rec["perspective"] = nilIfEmpty(trimUpper(rec["perspective"]))
	if err := m.requireCatalog(ctx, "perspective", "Perspektive", cc, rec["perspective"]); err != nil {
		return err
	}
	for _, k := range []string{"follow_up_days", "ack_days", "answer_days", "idle_days", "escalate_days"} {
		rec[k] = intOrNil(rec[k])
		if rec[k] != nil && toInt(rec[k]) < 0 {
			return crud.Invalid("Tage dürfen nicht negativ sein")
		}
	}
	rec["escalate_to"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["escalate_to"])))
	return nil
}

// --- Schrittvorlagen ----------------------------------------------------------------

const typeStepObject = "ProcessTypeStep"

func (m *Module) typeStep() *crud.Entity {
	return &crud.Entity{
		Object: typeStepObject, Customizing: true, Title: "Schrittvorlagen", Icon: "icon-list", Table: "process__type_step", Section: "Einstellungen",
		Keys: []string{"company_code", "process_type", "step_no"}, Order: "company_code, process_type, step_no", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "process_type"}, Search: []string{"name", "checklist"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "process_type", Label: "Vorgangsart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ProcessType")},
			{Key: "step_no", Label: "Schritt (Nummer, z. B. 10, 20 …)", Short: "Schritt", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "checklist", Label: "Checkliste (eine Zeile je Punkt)", Short: "Checkliste", Type: tArea},
			{Key: "role_hint", Label: "Zuständig (Hinweis, z. B. Sachbearbeiter, Technik)", Short: "Zuständig", Type: tText, Listable: true},
			{Key: "due_basis", Label: "Frist ab", Type: metamodel.TypeSelect, Required: true, Listable: true, Options: dueBases},
			{Key: "due_days", Label: "Frist in Tagen (zum Stichtag: vorher negativ)", Short: "Tage", Type: tNum, Listable: true},
			{Key: "required", Label: "Pflicht vor dem Abschluss", Short: "Pflicht", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, typeStepObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			rec["process_type"] = trimUpper(rec["process_type"])
			if old == nil {
				if ok, err := m.exists(ctx, "type", crud.Str(rec["company_code"]), crud.Str(rec["process_type"])); err != nil {
					return err
				} else if !ok {
					return crud.Invalid("Vorgangsart %s gibt es nicht", rec["process_type"])
				}
			}
			if toInt(rec["step_no"]) <= 0 {
				return crud.Invalid("Schrittnummer muss größer 0 sein")
			}
			return checkDue(rec)
		},
	}
}

// checkDue: Grundlage und Tage einer Frist (Vorlage und Schritt).
func checkDue(rec crud.Record) error {
	rec["due_basis"] = trimUpper(rec["due_basis"])
	if !slices.ContainsFunc(dueBases, func(o metamodel.Option) bool { return o.Value == rec["due_basis"] }) {
		return crud.Invalid("Frist ab: OPEN, PREV oder EVENT")
	}
	rec["due_days"] = intOrNil(rec["due_days"])
	if rec["due_basis"] != basisEvent && rec["due_days"] != nil && toInt(rec["due_days"]) < 0 {
		return crud.Invalid("Negative Tage nur zum Stichtag")
	}
	return nil
}

// --- Sichten ------------------------------------------------------------------------

const viewObject = "ProcessView"

func (m *Module) view() *crud.Entity {
	return &crud.Entity{
		Object: viewObject, Customizing: true, Title: "Sichten", Icon: "icon-filter", Table: "process__view", Section: "Einstellungen",
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "only_mine", Label: "Nur meine (verantwortlich oder Vertreter)", Short: "Meine", Type: tBool, Listable: true},
			{Key: "status_kinds", Label: "Status-Arten (Komma: OPEN, WAITING, CLOSED; leer = nicht abgeschlossene)", Short: "Status", Type: tText, Listable: true},
			{Key: "types", Label: "Vorgangsarten (Komma, leer = alle)", Short: "Arten", Type: tText},
			{Key: "perspective", Label: "Perspektive (leer = alle)", Type: tText, Lookup: catalogLookup("ProcessPerspective")},
			{Key: "due_days", Label: "Nächste Frist in höchstens … Tagen (0 = heute und überfällig)", Short: "Fällig in", Type: tNum, Listable: true},
			{Key: "overdue_only", Label: "Nur überfällige", Short: "Überfällig", Type: tBool},
			{Key: "escalated_only", Label: "Nur eskalierte", Short: "Eskaliert", Type: tBool},
			{Key: "idle_days", Label: "Ohne Bewegung seit mindestens … Tagen", Short: "Ohne Bewegung", Type: tNum},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, viewObject, action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = trimUpper(rec["code"])
				if !codeRe.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Schlüssel %q: A–Z, 0–9, _ (höchstens 16 Zeichen)", crud.Str(rec["code"]))
				}
			}
			kinds := splitCodes(crud.Str(rec["status_kinds"]))
			for _, k := range kinds {
				if k != kindOpen && k != kindWaiting && k != kindClosed {
					return crud.Invalid("Status-Art %s: OPEN, WAITING oder CLOSED", k)
				}
			}
			rec["status_kinds"] = nilIfEmpty(strings.Join(kinds, ","))
			rec["types"] = nilIfEmpty(strings.Join(splitCodes(crud.Str(rec["types"])), ","))
			rec["perspective"] = nilIfEmpty(trimUpper(rec["perspective"]))
			rec["due_days"], rec["idle_days"] = intOrNil(rec["due_days"]), intOrNil(rec["idle_days"])
			return nil
		},
	}
}

// viewFilter: Bedingung der Sicht code für die Vorgangsliste (FilterCtx).
func (m *Module) viewFilter(ctx context.Context, v any) (string, []any, error) {
	code := trimUpper(v)
	row, err := m.one(ctx, `SELECT company_code, only_mine, status_kinds, types, perspective, due_days, overdue_only, escalated_only, idle_days
		FROM process__view WHERE code = ? AND is_active = ? ORDER BY company_code`, code, true)
	if err != nil {
		return "", nil, err
	}
	if row == nil {
		return "", nil, crud.Invalid("Sicht %s gibt es nicht (Vorgänge → Einstellungen → Sichten)", code)
	}
	var where []string
	var args []any
	add := func(w string, a ...any) { where, args = append(where, w), append(args, a...) }
	if crud.AsBool(row[1]) {
		u := userOf(ctx)
		add("(assignee = ? OR deputy = ?)", u, u)
	}
	kinds := splitCodes(crud.Str(row[2]))
	if len(kinds) == 0 {
		kinds = []string{kindOpen, kindWaiting}
	}
	add("status IN (SELECT s.code FROM process__status s WHERE s.company_code = process__process.company_code AND s.kind IN ("+
		marks(len(kinds))+"))", anys(kinds)...)
	if types := splitCodes(crud.Str(row[3])); len(types) > 0 {
		add("process_type IN ("+marks(len(types))+")", anys(types)...)
	}
	if p := crud.Str(row[4]); p != "" {
		add("perspective = ?", p)
	}
	today := m.today()
	if row[5] != nil && crud.Str(row[5]) != "" {
		add("next_due IS NOT NULL AND next_due <= ?", addDays(today, toInt(row[5])))
	}
	if crud.AsBool(row[6]) {
		add("next_due IS NOT NULL AND next_due < ?", today)
	}
	if crud.AsBool(row[7]) {
		add("escalation >= ?", levelEscalate)
	}
	if row[8] != nil && crud.Str(row[8]) != "" {
		add("(last_activity IS NULL OR last_activity < ?)", addDays(today, -toInt(row[8])))
	}
	return strings.Join(where, " AND "), args, nil
}

func marks(n int) string { return strings.TrimSuffix(strings.Repeat("?, ", n), ", ") }

func anys(s []string) []any {
	out := make([]any, len(s))
	for i, v := range s {
		out[i] = v
	}
	return out
}

// --- Katalogzugriff -----------------------------------------------------------------

// catalogHas: Gibt es den aktiven Eintrag code im Katalog des Buchungskreises?
func (m *Module) catalogHas(ctx context.Context, table, cc, code string) (bool, error) {
	row, err := m.one(ctx, "SELECT 1 FROM process__"+table+" WHERE company_code = ? AND code = ? AND is_active = ?", cc, code, true)
	return row != nil, err
}

// requireCatalog: leerer Wert ist erlaubt, sonst muss er im Katalog stehen.
func (m *Module) requireCatalog(ctx context.Context, table, label, cc string, v any) error {
	code := crud.Str(v)
	if code == "" {
		return nil
	}
	ok, err := m.catalogHas(ctx, table, cc, code)
	if err != nil {
		return err
	}
	if !ok {
		return crud.Invalid("%s %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Vorgänge → Einstellungen", label, code, cc)
	}
	return nil
}

// exists: Eintrag code im Katalog (auch inaktiv).
func (m *Module) exists(ctx context.Context, table, cc, code string) (bool, error) {
	row, err := m.one(ctx, "SELECT 1 FROM process__"+table+" WHERE company_code = ? AND code = ?", cc, code)
	return row != nil, err
}

// withCatalogLabels: Felder mit Katalog-Lookup zeigen die Bezeichnung aus dem
// Katalog des Buchungskreises (statt des Schlüssels).
func (m *Module) withCatalogLabels(e *crud.Entity) {
	tables := map[string]string{}
	for _, c := range catalogTables {
		tables[c.Object] = c.Table
	}
	tables[viewObject] = "view"
	fields := map[string]string{} // Feld → Katalogtabelle
	for _, f := range e.Fields {
		if f.Lookup != nil && tables[f.Lookup.Object] != "" && !f.Virtual {
			fields[f.Key] = tables[f.Lookup.Object]
		}
	}
	if len(fields) == 0 {
		return
	}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		if decorate != nil {
			if err := decorate(ctx, rec); err != nil {
				return err
			}
		}
		cc := crud.Str(rec["company_code"])
		for key, table := range fields {
			code := crud.Str(rec[key])
			if code == "" {
				continue
			}
			row, err := m.one(ctx, "SELECT name FROM process__"+table+" WHERE company_code = ? AND code = ?", cc, code)
			if err != nil {
				return err
			}
			if row != nil {
				setLabel(rec, key, crud.Str(row[0]))
			}
		}
		return nil
	}
}

// --- Vorschlagswerte ----------------------------------------------------------------

// catalogDefaults: Vorschlagswerte je Katalog. Fristen sind Vorschläge, kein Recht:
// jeder Buchungskreis prüft und ändert sie selbst.
var catalogDefaults = map[string][]map[string]any{
	"perspective": {
		{"code": "VERWALTUNG", "name": "Verwaltung (wir verwalten)"},
		{"code": "EIGENTUEMER", "name": "Eigentümer (wir sind Eigentümer)"},
		{"code": "PRIVAT", "name": "Privat"},
	},
	"type": {
		{"code": "WIEDERVERM", "name": "Wiedervermietung", "id_prefix": "WV", "perspective": "VERWALTUNG", "event_label": "Auszugstermin",
			"follow_up_days": 7, "ack_days": 7, "idle_days": 14, "escalate_days": 7, "keywords": "Kündigung, Auszug, Wohnungsübergabe"},
		{"code": "SCHADEN", "name": "Schadensmeldung / Reparatur", "id_prefix": "SM", "perspective": "VERWALTUNG",
			"follow_up_days": 3, "ack_days": 2, "answer_days": 14, "idle_days": 7, "escalate_days": 3, "keywords": "Schaden, defekt, Reparatur, Wasserschaden, Heizung"},
		{"code": "MIETERANFR", "name": "Mieteranfrage", "id_prefix": "MA", "perspective": "VERWALTUNG",
			"follow_up_days": 7, "ack_days": 3, "answer_days": 14, "idle_days": 14, "escalate_days": 7},
		{"code": "EIGENTANFR", "name": "Eigentümeranfrage", "id_prefix": "EA", "perspective": "VERWALTUNG",
			"follow_up_days": 7, "ack_days": 3, "answer_days": 14, "idle_days": 14, "escalate_days": 7},
		{"code": "ANFRAGE", "name": "Anfrage mit Antwortfrist", "id_prefix": "AF",
			"follow_up_days": 7, "answer_days": 14, "idle_days": 10, "escalate_days": 3},
		{"code": "ETV_VERW", "name": "Eigentümerversammlung (Verwaltung)", "id_prefix": "EV", "perspective": "VERWALTUNG",
			"event_label": "Versammlungstermin", "follow_up_days": 14, "idle_days": 21, "escalate_days": 3},
		{"code": "ETV_EIG", "name": "Eigentümerversammlung (Eigentümer)", "id_prefix": "EE", "perspective": "EIGENTUEMER",
			"event_label": "Versammlungstermin", "follow_up_days": 7, "idle_days": 21, "escalate_days": 3, "keywords": "Einladung, Eigentümerversammlung, Tagesordnung"},
	},
	"status": {
		{"code": "NEW", "name": "neu", "kind": kindOpen},
		{"code": "IN_WORK", "name": "in Bearbeitung", "kind": kindOpen},
		{"code": "WAIT_PARTNER", "name": "wartet auf Rückmeldung", "kind": kindWaiting},
		{"code": "WAIT_CRAFT", "name": "wartet auf Handwerker", "kind": kindWaiting},
		{"code": "WAIT_OWNER", "name": "wartet auf Eigentümer / Beirat", "kind": kindWaiting},
		{"code": "WAIT_ADMIN", "name": "wartet auf Verwaltung", "kind": kindWaiting},
		{"code": "DONE", "name": "erledigt", "kind": kindClosed},
		{"code": "CANCELLED", "name": "abgebrochen", "kind": kindClosed},
	},
	"priority": {
		{"code": "LOW", "name": "niedrig"}, {"code": "NORMAL", "name": "normal"},
		{"code": "HIGH", "name": "hoch"}, {"code": "URGENT", "name": "dringend"},
	},
	"role": {
		{"code": "REQUESTER", "name": "Anfragender"}, {"code": "TENANT", "name": "Mieter"},
		{"code": "OWNER", "name": "Eigentümer"}, {"code": "PROSPECT", "name": "Interessent"},
		{"code": "CRAFTSMAN", "name": "Handwerker"}, {"code": "BOARD", "name": "Beirat"},
		{"code": "ADMIN", "name": "Verwaltung"}, {"code": "INSURER", "name": "Versicherung"},
	},
	"resolution": {
		{"code": "SOLVED", "name": "erledigt"}, {"code": "ANSWERED", "name": "beantwortet"},
		{"code": "REJECTED", "name": "abgelehnt"}, {"code": "WITHDRAWN", "name": "zurückgezogen"},
		{"code": "FORWARDED", "name": "weitergegeben"}, {"code": "DUPLICATE", "name": "doppelt"},
	},
	"entry_kind": {
		{"code": "NOTE", "name": "Notiz"}, {"code": "CALL", "name": "Telefonat"},
		{"code": "MAIL", "name": "E-Mail"}, {"code": "LETTER", "name": "Brief"},
		{"code": "MEETING", "name": "Termin / Besuch"},
		{"code": "SYSTEM", "name": "Systemeintrag", "is_system": true},
	},
}

// stepDefaults: Schrittvorlagen je Vorgangsart (Schritt, Bezeichnung, Frist ab, Tage, Pflicht, Checkliste).
var stepDefaults = map[string][]map[string]any{
	"WIEDERVERM": {
		{"step_no": 10, "name": "Kündigung prüfen und bestätigen", "due_basis": basisOpen, "due_days": 7,
			"checklist": "Frist und Form der Kündigung prüfen\nAuszugstermin als Stichtag eintragen\nBestätigung an den Mieter"},
		{"step_no": 20, "name": "Vorabnahme vereinbaren", "due_basis": basisEvent, "due_days": -30, "required": false,
			"checklist": "Schönheitsreparaturen und Mängel feststellen\nRückbau von Einbauten klären"},
		{"step_no": 30, "name": "Inserat und Interessenten", "due_basis": basisOpen, "due_days": 14, "checklist": "Miete prüfen\nExposé und Fotos\nInserat schalten"},
		{"step_no": 40, "name": "Besichtigungen", "due_basis": basisEvent, "due_days": -14},
		{"step_no": 50, "name": "Mieter auswählen", "due_basis": basisEvent, "due_days": -10, "checklist": "Selbstauskunft\nBonität\nEinkommensnachweis"},
		{"step_no": 60, "name": "Mietvertrag mit dem neuen Mieter", "due_basis": basisEvent, "due_days": -7},
		{"step_no": 70, "name": "Wohnungsübergabe Auszug", "due_basis": basisEvent, "due_days": 0,
			"checklist": "Übergabeprotokoll\nZählerstände\nSchlüssel"},
		{"step_no": 80, "name": "Wohnungsübergabe Einzug", "due_basis": basisEvent, "due_days": 1,
			"checklist": "Übergabeprotokoll\nZählerstände an die Versorger\nSchlüssel"},
		{"step_no": 90, "name": "Kaution des neuen Mieters", "due_basis": basisEvent, "due_days": 14},
		{"step_no": 100, "name": "Kautionsabrechnung des alten Mieters", "due_basis": basisEvent, "due_days": 180},
		{"step_no": 110, "name": "Nebenkosten-Endabrechnung vormerken", "due_basis": basisEvent, "due_days": 365, "required": false},
	},
	"SCHADEN": {
		{"step_no": 10, "name": "Schaden aufnehmen", "due_basis": basisOpen, "due_days": 1, "checklist": "Ort und Umfang\nFotos\nDringlichkeit\nGefahr im Verzug?"},
		{"step_no": 20, "name": "Handwerker beauftragen", "due_basis": basisOpen, "due_days": 3},
		{"step_no": 30, "name": "Termin mit dem Mieter abstimmen", "due_basis": basisPrev, "due_days": 3},
		{"step_no": 40, "name": "Ausführung kontrollieren", "due_basis": basisPrev, "due_days": 14},
		{"step_no": 50, "name": "Rechnung prüfen, Weiterbelastung klären", "due_basis": basisPrev, "due_days": 14},
		{"step_no": 60, "name": "Versicherung melden", "due_basis": basisOpen, "due_days": 7, "required": false},
	},
	"MIETERANFR": {
		{"step_no": 10, "name": "Anfrage prüfen, Eingang bestätigen", "due_basis": basisOpen, "due_days": 3},
		{"step_no": 20, "name": "Antwort an den Mieter", "due_basis": basisOpen, "due_days": 14},
	},
	"EIGENTANFR": {
		{"step_no": 10, "name": "Anfrage prüfen, Eingang bestätigen", "due_basis": basisOpen, "due_days": 3},
		{"step_no": 20, "name": "Unterlagen bzw. Auskunft zusammenstellen", "due_basis": basisOpen, "due_days": 10},
		{"step_no": 30, "name": "Antwort an den Eigentümer", "due_basis": basisOpen, "due_days": 14},
	},
	"ANFRAGE": {
		{"step_no": 10, "name": "Zuständigkeit klären", "due_basis": basisOpen, "due_days": 2},
		{"step_no": 20, "name": "Antwort", "due_basis": basisOpen, "due_days": 14},
	},
	"ETV_VERW": {
		{"step_no": 10, "name": "Termin und Raum festlegen", "due_basis": basisEvent, "due_days": -56},
		{"step_no": 20, "name": "Anträge der Eigentümer sammeln", "due_basis": basisEvent, "due_days": -35},
		{"step_no": 30, "name": "Tagesordnung mit dem Beirat abstimmen", "due_basis": basisEvent, "due_days": -28},
		{"step_no": 40, "name": "Einladung versenden (Einberufungsfrist)", "due_basis": basisEvent, "due_days": -21,
			"checklist": "Tagesordnung\nBeschlussvorlagen\nVollmachtsformular\nWirtschaftsplan / Abrechnung"},
		{"step_no": 50, "name": "Vollmachten erfassen", "due_basis": basisEvent, "due_days": -1, "required": false},
		{"step_no": 60, "name": "Versammlung durchführen", "due_basis": basisEvent, "due_days": 0, "checklist": "Anwesenheit\nBeschlussfähigkeit\nAbstimmungen"},
		{"step_no": 70, "name": "Beschlusssammlung eintragen", "due_basis": basisEvent, "due_days": 3},
		{"step_no": 80, "name": "Protokoll versenden", "due_basis": basisEvent, "due_days": 14},
		{"step_no": 90, "name": "Anfechtungsfrist beobachten", "due_basis": basisEvent, "due_days": 31},
		{"step_no": 100, "name": "Beschlüsse umsetzen (Folgevorgänge)", "due_basis": basisEvent, "due_days": 35},
	},
	"ETV_EIG": {
		{"step_no": 10, "name": "Einladung und Tagesordnung prüfen", "due_basis": basisOpen, "due_days": 3},
		{"step_no": 20, "name": "Eigene Anträge bzw. Fragen an die Verwaltung", "due_basis": basisOpen, "due_days": 7, "required": false},
		{"step_no": 30, "name": "Teilnahme oder Vollmacht", "due_basis": basisEvent, "due_days": -3},
		{"step_no": 40, "name": "Versammlung", "due_basis": basisEvent, "due_days": 0},
		{"step_no": 50, "name": "Protokoll prüfen", "due_basis": basisEvent, "due_days": 21},
		{"step_no": 60, "name": "Anfechtung prüfen (Frist ab Beschluss)", "due_basis": basisEvent, "due_days": 25},
		{"step_no": 70, "name": "Folgen umsetzen (Sonderumlage, Hausgeld, Wirtschaftsplan)", "due_basis": basisEvent, "due_days": 30},
	},
}

// viewDefaults: Sichten für Verwalter und Sachbearbeiter.
var viewDefaults = []map[string]any{
	{"code": "MEINE_HEUTE", "name": "Meine – heute fällig und überfällig", "only_mine": true, "due_days": 0},
	{"code": "MEINE", "name": "Meine offenen Vorgänge", "only_mine": true},
	{"code": "MEINE_WARTEN", "name": "Meine – wartet auf …", "only_mine": true, "status_kinds": kindWaiting},
	{"code": "WOCHE", "name": "Alle – fällig in 7 Tagen", "due_days": 7},
	{"code": "UEBERFAELLIG", "name": "Alle – überfällig", "overdue_only": true},
	{"code": "ESKALIERT", "name": "Alle – eskaliert", "escalated_only": true},
	{"code": "OHNE_BEWEGUNG", "name": "Alle – ohne Bewegung seit 14 Tagen", "idle_days": 14},
	{"code": "OFFEN", "name": "Alle offenen Vorgänge"},
	{"code": "ABGESCHLOSSEN", "name": "Abgeschlossene Vorgänge", "status_kinds": kindClosed},
}

// insertRow: fehlende Zeile mit den angegebenen Spalten.
func (m *Module) insertRow(ctx context.Context, table string, row map[string]any) error {
	cols, args := make([]string, 0, len(row)), make([]any, 0, len(row))
	for _, k := range sortedKeys(row) {
		cols, args = append(cols, k), append(args, row[k])
	}
	_, err := m.db.Exec(ctx, "INSERT INTO process__"+table+" ("+strings.Join(cols, ", ")+") VALUES ("+marks(len(cols))+")", args...)
	return err
}

func sortedKeys(row map[string]any) []string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// setupCatalogs legt fehlende Vorschlagswerte im Buchungskreis an und liefert
// die Zahl der neuen Einträge. Schrittvorlagen nur für neu angelegte Arten.
func (m *Module) setupCatalogs(ctx context.Context, cc string) (int, error) {
	n := 0
	for _, table := range []string{"perspective", "type", "status", "priority", "role", "resolution", "entry_kind"} {
		for i, def := range catalogDefaults[table] {
			code := crud.Str(def["code"])
			ok, err := m.exists(ctx, table, cc, code)
			if err != nil {
				return 0, err
			}
			if ok {
				continue
			}
			row := map[string]any{"company_code": cc, "sort_order": (i + 1) * 10, "is_active": true}
			for k, v := range def {
				row[k] = v
			}
			if err := m.insertRow(ctx, table, row); err != nil {
				return 0, err
			}
			n++
			if table != "type" {
				continue
			}
			for _, st := range stepDefaults[code] {
				row := map[string]any{"company_code": cc, "process_type": code, "required": true, "is_active": true}
				for k, v := range st {
					row[k] = v
				}
				if err := m.insertRow(ctx, "type_step", row); err != nil {
					return 0, err
				}
			}
		}
	}
	for i, def := range viewDefaults {
		row, err := m.one(ctx, "SELECT 1 FROM process__view WHERE company_code = ? AND code = ?", cc, def["code"])
		if err != nil {
			return 0, err
		}
		if row != nil {
			continue
		}
		rec := map[string]any{"company_code": cc, "sort_order": (i + 1) * 10, "is_active": true}
		for k, v := range def {
			rec[k] = v
		}
		if err := m.insertRow(ctx, "view", rec); err != nil {
			return 0, err
		}
		n++
	}
	return n, nil
}

// setupCompanyAction: ProcessSetup.setupCompany {company} (Konsole process:setup-company).
func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
		Data        struct {
			CompanyCode string `json:"company_code"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Company + in.CompanyCode + in.Data.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, "ProcessType", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	var n int
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		n, err = m.setupCatalogs(ctx, cc)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	t, err := m.setupTexts(ctx, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "created": n, "texts": t,
		"message": fmt.Sprintf("Buchungskreis %s: %d Katalogeinträge der Vorgänge und %d Textkategorien bzw. Zuordnungen angelegt", cc, n, t)}}, nil
}

// textDefaults: Vorschlag der Textkategorien am Vorgang (Texte → Einstellungen;
// änderbar, mehrfach je Vorgang).
var textDefaults = []map[string]any{
	{"code": "NOTE", "name": "Anmerkung"},
	{"code": "FEEDBACK", "name": "Rückmeldung"},
	{"code": "INQUIRY", "name": "Anfrage"},
	{"code": "REPLY", "name": "Antwort"},
	{"code": "EMAIL", "name": "E-Mail"},
	{"code": "PHONE_NOTE", "name": "Telefonnotiz"},
	{"code": "LETTER", "name": "Brief"},
	{"code": "RESULT", "name": "Ergebnis / Prüfvermerk"},
}

// setupTexts: fehlende Textkategorien und ihre Zuordnung zum Vorgang im
// Buchungskreis (Modul Texte). Fehlt das Modul, bleibt es bei 0.
func (m *Module) setupTexts(ctx context.Context, cc string) (int, error) {
	n := 0
	ensure := func(object, id string, data map[string]any) error {
		_, err := m.services.Call(ctx, object, "get", map[string]any{"id": id})
		if err == nil {
			return nil
		}
		if !errors.Is(err, sdk.ErrNotFound) {
			return err
		}
		if _, err := m.services.Call(ctx, object, "create", map[string]any{"data": data}); err != nil {
			return fmt.Errorf("%s %s: %w", object, id, err)
		}
		n++
		return nil
	}
	if _, err := m.services.Call(ctx, "TextCategory", "list", map[string]any{}); err != nil {
		if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrNotFound) || errors.Is(err, sdk.ErrUnavailable) {
			return 0, nil // Modul Texte nicht installiert
		}
		return 0, err
	}
	for i, def := range textDefaults {
		code := crud.Str(def["code"])
		if err := ensure("TextCategory", code, map[string]any{"code": code, "name": def["name"], "cardinality": "MULTIPLE", "sort_order": (i + 1) * 10}); err != nil {
			return n, err
		}
		if err := ensure("TextAssignment", processObject+"|"+cc+"|"+code,
			map[string]any{"entity_type": processObject, "company_code": cc, "category": code, "sort_order": (i + 1) * 10}); err != nil {
			return n, err
		}
	}
	return n, nil
}
