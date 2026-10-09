package realestate

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Geschäftspartner an Objekten (Plugin partner):
//
//   - Die Rollen (Eigentümer, Hausmeister, WEG-Verwalter, Verwalter
//     Sondereigentum …) pflegt das Partnermodul (PartnerRoleType); dort hat
//     auch jeder Partner seine Rollen mit Zeitraum (PartnerRole).
//   - RentPartnerRole aktiviert eine Rolle je Buchungskreis für die Immobilien
//     und ergänzt, was nur hier zählt: erlaubte Ebenen, exklusiv (ein Partner je
//     Stichtag), Anteil (Summe ≤ 100 %), Vererbung an untergeordnete Objekte.
//   - RentObjectPartner ordnet Partner in einer Rolle mit Zeitscheibe einer
//     Wirtschaftseinheit, einem Gebäude oder einem Mietobjekt zu. Auswählbar
//     sind nur Partner, die die Rolle im Partnermodul haben.
//   - Wirksame Partner (Action partners): Was am Objekt selbst fehlt, kommt vom
//     Gebäude, dann von der Wirtschaftseinheit – sofern die Rolle vererbt.
//
// Mieter gehören nicht hierher: Sie ergeben sich aus dem Mietvertrag.

const (
	partnerRoleObject   = "RentPartnerRole"
	objectPartnerObject = "RentObjectPartner"
	levelEntity         = "ENTITY"
	levelBuilding       = "BUILDING"
)

// allLevels: Ebenen in der Reihenfolge der Vererbung (oben → unten).
var allLevels = []string{levelEntity, levelBuilding, kindUnit, kindSpace, kindPool, kindComposite}

// defaultPartnerRoles: Vorschlag für setup-company – aktiviert, wenn das
// Partnermodul die Rolle kennt (Code, Ebenen, exklusiv, Anteil).
var defaultPartnerRoles = []struct {
	code, levels      string
	exclusive, shares bool
}{
	{"OWNER", "ENTITY,BUILDING,UNIT,SPACE,POOL,COMPOSITE", false, true},
	{"JANITOR", "ENTITY,BUILDING", true, false},
	{"WEGADM", "ENTITY", true, false},
	{"SEADM", "UNIT,COMPOSITE", true, false},
}

// --- Aktivierung der Rollen ----------------------------------------------------------

func (m *Module) partnerRole() *crud.Entity {
	return &crud.Entity{
		Object: partnerRoleObject, Title: "Partnerrollen", Icon: "icon-users", Table: "realestate__partner_role", Section: "Einstellungen",
		Keys: []string{"company_code", "role_code"}, Order: "company_code, sort_order, role_code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code"}, Search: []string{"role_code", "name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "role_code", Label: "Rolle (aus dem Partnermodul)", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "PartnerRoleType", ValueField: "code", LabelFields: []string{"description"}}},
			{Key: "name", Label: "Bezeichnung (leer = aus dem Partnermodul)", Type: tText, Listable: true},
			{Key: "levels", Label: "Ebenen (ENTITY, BUILDING, UNIT, SPACE, POOL, COMPOSITE; leer = alle)", Type: tText, Listable: true},
			{Key: "is_exclusive", Label: "Exklusiv (ein Partner je Stichtag)", Type: tBool, Listable: true},
			{Key: "with_share", Label: "Mit Anteil in % (Summe ≤ 100)", Type: tBool, Listable: true},
			{Key: "inherits", Label: "Gilt für untergeordnete Objekte", Type: tBool, Listable: true},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, partnerRoleObject, action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkPartnerRole,
	}
}

func (m *Module) checkPartnerRole(ctx context.Context, rec, old crud.Record) error {
	if old == nil {
		rec["role_code"] = trimUpper(rec["role_code"])
		if rec["inherits"] == nil {
			rec["inherits"] = true
		}
	}
	types, err := m.partnerRoleTypes(ctx)
	if err != nil {
		return err
	}
	desc, ok := types[crud.Str(rec["role_code"])]
	if !ok {
		return crud.Invalid("Rolle %s gibt es im Partnermodul nicht (Geschäftspartner → Kataloge → Rollentypen)", crud.Str(rec["role_code"]))
	}
	if strings.TrimSpace(crud.Str(rec["name"])) == "" {
		rec["name"] = desc
	}
	levels, err := parseLevels(crud.Str(rec["levels"]))
	if err != nil {
		return err
	}
	rec["levels"] = strings.Join(levels, ",")
	return nil
}

// parseLevels: "unit, building" → [BUILDING UNIT] (Reihenfolge der Ebenen); leer = alle.
func parseLevels(text string) ([]string, error) {
	var out []string
	for _, l := range strings.FieldsFunc(strings.ToUpper(text), func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if !slices.Contains(allLevels, l) {
			return nil, crud.Invalid("Ebene %q – erlaubt: %s", l, strings.Join(allLevels, ", "))
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return slices.Clone(allLevels), nil
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(allLevels, a) - slices.Index(allLevels, b) })
	return slices.Compact(out), nil
}

// partnerRoleTypes: heute gültige Rollentypen des Partnermoduls (Code → Text).
func (m *Module) partnerRoleTypes(ctx context.Context) (map[string]string, error) {
	resp, err := m.services.Call(ctx, "PartnerRoleType", "list", map[string]any{"query": map[string]any{}})
	if err != nil {
		return nil, partnerUnavailable(err)
	}
	var out struct {
		Items []struct {
			Code        string `json:"code"`
			Description string `json:"description"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	types := map[string]string{}
	for _, it := range out.Items {
		types[it.Code] = it.Description
	}
	return types, nil
}

func partnerUnavailable(err error) error {
	if errors.Is(err, sdk.ErrUnimplemented) || errors.Is(err, sdk.ErrUnavailable) {
		return crud.Invalid("Partnermodul nicht verfügbar: %v", err)
	}
	return err
}

// setupPartnerRoles aktiviert die vorgeschlagenen Rollen, die das Partnermodul
// kennt und die im Buchungskreis noch fehlen.
func (m *Module) setupPartnerRoles(ctx context.Context, cc string) (int, error) {
	types, err := m.partnerRoleTypes(ctx)
	if err != nil {
		return 0, nil // ohne Partnermodul: nichts vorschlagen
	}
	n := 0
	for i, d := range defaultPartnerRoles {
		desc, ok := types[d.code]
		if !ok {
			continue
		}
		res, err := m.db.Query(ctx, "SELECT 1 FROM realestate__partner_role WHERE company_code = ? AND role_code = ?", cc, d.code)
		if err != nil {
			return n, err
		}
		if len(res.Rows) > 0 {
			continue
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO realestate__partner_role (company_code, role_code, name, levels, is_exclusive, with_share, inherits, sort_order, is_active)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, d.code, desc, d.levels, d.exclusive, d.shares, true, (i+1)*10, true); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- Zuordnung -----------------------------------------------------------------------

func (m *Module) objectPartner() *crud.Entity {
	return &crud.Entity{
		Object: objectPartnerObject, Title: "Partnerzuordnungen", Icon: "icon-users", Table: "realestate__object_partner", Section: "Bestand",
		Keys: []string{"company_code", "object_id", "role_code", "partner_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, object_id, role_code, valid_from, partner_id",
		Filters: []string{"company_code", "object_id", "object_level", "role_code", "partner_id"},
		Events:  true, CompanyCodeField: "company_code",
		EventFields: []string{"object_id", "object_level", "role_code", "partner_id", "share", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "object_id", Label: "Objekt (Wirtschaftseinheit, Gebäude oder Mietobjekt)", Type: tText, Required: true, Listable: true, Immutable: true},
			crud.Field{Key: "object_level", Label: "Ebene", Type: tSel, ReadOnly: true, Listable: true, Options: levelOptions},
			crud.Field{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: partnerRoleObject, ValueField: "role_code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code"}}},
			crud.Field{Key: "partner_id", Label: "Geschäftspartner", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"},
					Filters: map[string]string{"role": "role_code"}}},
			crud.Field{Key: "share", Label: "Anteil in %", Type: tNum, Listable: true},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access: &crud.Access{Object: objectPartnerObject, Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, objectPartnerObject, action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkObjectPartner,
		Decorate: m.partnerLabels,
	}
}

// roleSetting: Eigenschaften einer aktivierten Rolle.
type roleSetting struct {
	code, name                  string
	levels                      []string
	exclusive, shares, inherits bool
}

func (m *Module) roleSettings(ctx context.Context, cc, code string) ([]roleSetting, error) {
	sql := "SELECT role_code, name, levels, is_exclusive, with_share, inherits FROM realestate__partner_role WHERE company_code = ? AND is_active = ?"
	args := []any{cc, true}
	if code != "" {
		sql, args = sql+" AND role_code = ?", append(args, code)
	}
	res, err := m.db.Query(ctx, sql+" ORDER BY sort_order, role_code", args...)
	if err != nil {
		return nil, err
	}
	out := make([]roleSetting, len(res.Rows))
	for i, r := range res.Rows {
		levels, _ := parseLevels(crud.Str(r[2]))
		out[i] = roleSetting{code: crud.Str(r[0]), name: crud.Str(r[1]), levels: levels,
			exclusive: crud.AsBool(r[3]), shares: crud.AsBool(r[4]), inherits: crud.AsBool(r[5])}
	}
	return out, nil
}

func (m *Module) checkObjectPartner(ctx context.Context, rec, old crud.Record) error {
	cc, obj := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["object_id"]))
	role, partner := trimUpper(rec["role_code"]), strings.TrimSpace(crud.Str(rec["partner_id"]))
	rec["object_id"], rec["role_code"], rec["partner_id"] = obj, role, partner
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])

	level, err := m.objectLevel(ctx, cc, obj)
	if err != nil {
		return err
	}
	if level == "" {
		return crud.Invalid("Objekt %s gibt es im Buchungskreis %s nicht", obj, cc)
	}
	rec["object_level"] = level
	settings, err := m.roleSettings(ctx, cc, role)
	if err != nil {
		return err
	}
	if len(settings) == 0 {
		return crud.Invalid("Rolle %s ist im Buchungskreis %s nicht aktiviert (Immobilien → Einstellungen → Partnerrollen)", role, cc)
	}
	rs := settings[0]
	if !slices.Contains(rs.levels, level) {
		return crud.Invalid("%s ist an %s nicht vorgesehen (nur %s)", rs.name, levelLabel(level), levelLabels(rs.levels))
	}
	if err := m.requirePartnerRole(ctx, partner, role, rs.name, from, to); err != nil {
		return err
	}

	// Andere Zuordnungen derselben Rolle am Objekt, die sich zeitlich überschneiden
	// (ohne den Datensatz selbst).
	res, err := m.db.Query(ctx, `SELECT partner_id, valid_from, valid_to, share FROM realestate__object_partner
		WHERE company_code = ? AND object_id = ? AND role_code = ? AND valid_from <= ? AND valid_to >= ?
		AND NOT (partner_id = ? AND valid_from = ?)`, cc, obj, role, to, from, partner, from)
	if err != nil {
		return err
	}
	if rs.exclusive {
		for _, r := range res.Rows {
			if crud.Str(r[0]) != partner {
				f, _ := crud.ParseDate(r[1])
				t, _ := crud.ParseDate(r[2])
				return crud.Invalid("%s an %s ist %s–%s bereits Partner %s – erst dort beenden", rs.name, obj, f, t, crud.Str(r[0]))
			}
		}
	}
	if !rs.shares {
		rec["share"] = nil
		return nil
	}
	share := toFloat(rec["share"])
	if rec["share"] == nil || share <= 0 || share > 100 {
		return crud.Invalid("%s: Anteil in %% (größer 0, höchstens 100) ist Pflicht", rs.name)
	}
	// Summe der Anteile an jedem Stichtag im Zeitraum (Beginn und Beginn der
	// überschneidenden Zuordnungen) ≤ 100.
	dates := []string{from}
	for _, r := range res.Rows {
		if d, _ := crud.ParseDate(r[1]); d > from {
			dates = append(dates, d)
		}
	}
	for _, d := range dates {
		sum := share
		for _, r := range res.Rows {
			f, _ := crud.ParseDate(r[1])
			t, _ := crud.ParseDate(r[2])
			if f <= d && t >= d {
				sum += toFloat(r[3])
			}
		}
		if sum > 100+1e-9 {
			return crud.Invalid("%s an %s: Anteile ergeben am %s %s %% – mehr als 100 %%", rs.name, obj, d, formatNum(sum))
		}
	}
	return nil
}

// requirePartnerRole: Der Partner hat die Rolle im Partnermodul für den ganzen
// Zeitraum (Zeitscheiben dürfen aneinander anschließen).
func (m *Module) requirePartnerRole(ctx context.Context, partner, role, roleName, from, to string) error {
	resp, err := m.services.Call(ctx, "PartnerRole", "list", map[string]any{"query": map[string]any{
		"bp_id": partner, "role_code": role, "includeHistory": "true"}})
	if err != nil {
		return partnerUnavailable(err)
	}
	var out struct {
		Items []struct {
			ValidFrom string `json:"valid_from"`
			ValidTo   string `json:"valid_to"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return err
	}
	sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].ValidFrom < out.Items[j].ValidFrom })
	covered := from // bis einschließlich Vortag von covered abgedeckt
	for _, it := range out.Items {
		f, _ := crud.ParseDate(it.ValidFrom)
		t, _ := crud.ParseDate(it.ValidTo)
		if f > covered {
			break
		}
		if t >= covered {
			if t >= to {
				return nil
			}
			covered = nextDay(t)
		}
	}
	return crud.Invalid("Partner %s hat die Rolle %s im Partnermodul nicht für den ganzen Zeitraum %s–%s", partner, roleName, from, to)
}

// partnerLabels: Text der Rolle (Immobilien) und Name des Partners.
func (m *Module) partnerLabels(ctx context.Context, rec crud.Record) error {
	labels, _ := rec["_labels"].(map[string]any)
	if labels == nil {
		labels = map[string]any{}
		rec["_labels"] = labels
	}
	if res, err := m.db.Query(ctx, "SELECT name FROM realestate__partner_role WHERE company_code = ? AND role_code = ?",
		rec["company_code"], rec["role_code"]); err == nil && len(res.Rows) > 0 {
		labels["role_code"] = crud.Str(res.Rows[0][0])
	}
	if name := m.partnerName(ctx, crud.Str(rec["partner_id"])); name != "" {
		labels["partner_id"] = name
	}
	return nil
}

// partnerName: Name 1 und 2 des Partners (heute gültige Zeitscheibe); leer, wenn
// das Partnermodul fehlt oder der Benutzer den Partner nicht lesen darf.
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

func levelLabel(level string) string {
	for _, o := range levelOptions {
		if o.Value == level {
			return o.Label
		}
	}
	return level
}

func levelLabels(levels []string) string {
	out := make([]string, len(levels))
	for i, l := range levels {
		out[i] = levelLabel(l)
	}
	return strings.Join(out, ", ")
}

// --- Wirksame Partner (mit Vererbung) -------------------------------------------------

// EffectivePartner: Partner einer Rolle am Objekt zum Stichtag; From ist das
// Objekt, an dem die Zuordnung steht (das Objekt selbst oder übergeordnet).
type EffectivePartner struct {
	Role        string  `json:"role_code"`
	RoleName    string  `json:"role_name"`
	Partner     string  `json:"partner_id"`
	PartnerName string  `json:"partner_name,omitempty"`
	Share       float64 `json:"share,omitempty"`
	From        string  `json:"from_object"`
	FromLevel   string  `json:"from_level"`
	ValidFrom   string  `json:"valid_from"`
	ValidTo     string  `json:"valid_to"`
	Contract    string  `json:"contract_id,omitempty"` // aus einem Vertrag (Hook realestate.partners)
}

// lineage: das Objekt und seine übergeordneten Objekte (unten → oben).
func (m *Module) lineage(ctx context.Context, cc, id string) ([][2]string, error) {
	level, err := m.objectLevel(ctx, cc, id)
	if err != nil || level == "" {
		return nil, err
	}
	out := [][2]string{{id, level}}
	switch level {
	case levelEntity:
		return out, nil
	case levelBuilding:
		res, err := m.db.Query(ctx, "SELECT entity_id FROM realestate__building WHERE company_code = ? AND building_id = ?", cc, id)
		if err != nil {
			return nil, err
		}
		return append(out, [2]string{crud.Str(res.Rows[0][0]), levelEntity}), nil
	}
	res, err := m.db.Query(ctx, "SELECT building_id, entity_id FROM realestate__rent_object WHERE company_code = ? AND object_id = ?", cc, id)
	if err != nil {
		return nil, err
	}
	return append(out, [2]string{crud.Str(res.Rows[0][0]), levelBuilding}, [2]string{crud.Str(res.Rows[0][1]), levelEntity}), nil
}

// EffectivePartners: je aktivierter Rolle die Partner am Objekt zum Stichtag –
// vom Objekt selbst, sonst vom nächsten übergeordneten Objekt (Rolle vererbt).
func (m *Module) EffectivePartners(ctx context.Context, cc, id, date string) ([]EffectivePartner, error) {
	chain, err := m.lineage(ctx, cc, id)
	if err != nil {
		return nil, err
	}
	if chain == nil {
		return nil, fmt.Errorf("%w: Objekt %s im Buchungskreis %s", sdk.ErrNotFound, id, cc)
	}
	settings, err := m.roleSettings(ctx, cc, "")
	if err != nil {
		return nil, err
	}
	var out []EffectivePartner
	for _, rs := range settings {
		for i, link := range chain {
			if i > 0 && !rs.inherits {
				break
			}
			res, err := m.db.Query(ctx, `SELECT partner_id, share, valid_from, valid_to FROM realestate__object_partner
				WHERE company_code = ? AND object_id = ? AND role_code = ? AND valid_from <= ? AND valid_to >= ? ORDER BY partner_id`,
				cc, link[0], rs.code, date, date)
			if err != nil {
				return nil, err
			}
			for _, r := range res.Rows {
				f, _ := crud.ParseDate(r[2])
				t, _ := crud.ParseDate(r[3])
				out = append(out, EffectivePartner{Role: rs.code, RoleName: rs.name, Partner: crud.Str(r[0]),
					PartnerName: m.partnerName(ctx, crud.Str(r[0])), Share: toFloat(r[1]),
					From: link[0], FromLevel: link[1], ValidFrom: f, ValidTo: t})
			}
			if len(res.Rows) > 0 {
				break // tiefere Ebene übersteuert die höhere
			}
		}
	}
	return m.partnersFromHook(ctx, cc, id, date, chain, out)
}

// PartnersHookData: Daten des Hooks realestate.partners (Phase modify). Abonnenten
// (z. B. die Vertragsverwaltung mit den Mietern) liefern die ergänzte Liste zurück.
type PartnersHookData struct {
	CompanyCode string             `json:"company_code"`
	ObjectID    string             `json:"object_id"`
	Lineage     []string           `json:"lineage"` // Objekt, Gebäude, Wirtschaftseinheit
	Date        string             `json:"date"`
	Partners    []EffectivePartner `json:"partners"`
}

// partnersFromHook ruft realestate.partners (modify) – ohne Abonnenten bleibt die Liste.
func (m *Module) partnersFromHook(ctx context.Context, cc, id, date string, chain [][2]string, ps []EffectivePartner) ([]EffectivePartner, error) {
	data := PartnersHookData{CompanyCode: cc, ObjectID: id, Date: date, Partners: ps}
	if data.Partners == nil {
		data.Partners = []EffectivePartner{}
	}
	for _, l := range chain {
		data.Lineage = append(data.Lineage, l[0])
	}
	res, err := hook.Call(ctx, m.services, partnersHook, hook.PhaseModify, data)
	if err != nil {
		return ps, nil // Erweiterung nicht erreichbar: eigene Zuordnungen genügen
	}
	var out PartnersHookData
	if err := sdk.Decode(res.Data, &out); err != nil {
		return ps, nil
	}
	return out.Partners, nil
}

// partnersAction: RentObject.partners, Building.partners, BusinessEntity.partners
// (Detailansicht) und RealEstateSetup.partners (Konsole: partners)
// – Payload {id, date} (Detailansicht) oder {company, object, date} (Konsole).
func (m *Module) partnersAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID          string `json:"id"`
		Company     string `json:"company"`
		CompanyCode string `json:"company_code"`
		Object      string `json:"object"`
		Date        string `json:"date"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id := strings.TrimSpace(in.Company+in.CompanyCode), strings.TrimSpace(in.Object)
	if c, o, ok := strings.Cut(in.ID, "|"); ok {
		cc, id = c, o
	}
	if cc == "" || id == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis und Objekt sind Pflicht")
	}
	if ok, err := sdk.CheckAccess(ctx, "RentObject", "read", cc); err != nil || !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung für RentObject.read im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	date := crud.KeyDate(ctx)
	if strings.TrimSpace(in.Date) != "" {
		d, err := crud.ParseDate(in.Date)
		if err != nil {
			return sdk.Response{}, err
		}
		date = d
	}
	ps, err := m.EffectivePartners(ctx, cc, id, date)
	if err != nil {
		return sdk.Response{}, err
	}
	var parts []string
	for _, p := range ps {
		s := p.RoleName + ": " + p.PartnerName
		if p.PartnerName == "" {
			s = p.RoleName + ": " + p.Partner
		}
		if p.Share > 0 {
			s += " (" + formatNum(p.Share) + " %)"
		}
		if p.Contract != "" {
			s += " – Vertrag " + p.Contract
		} else if p.From != id {
			s += " – von " + levelLabel(p.FromLevel) + " " + p.From
		}
		parts = append(parts, s)
	}
	msg := fmt.Sprintf("%s am %s: keine Partner", id, date)
	if len(parts) > 0 {
		msg = fmt.Sprintf("%s am %s: %s", id, date, strings.Join(parts, " · "))
	}
	if ps == nil {
		ps = []EffectivePartner{}
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "object_id": id, "date": date, "partners": ps, "message": msg}}, nil
}

// partnersActionConfig: Action „Wirksame Partner“ in der Detailansicht.
func (m *Module) partnersActionConfig() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "partners", Label: "Wirksame Partner", Record: true,
		Confirm: "Wirksame Partner heute anzeigen (einschließlich der vom Gebäude bzw. der Wirtschaftseinheit geerbten)?"},
		Handle: m.partnersAction}
}

// nextDay: Folgetag eines Datums (YYYY-MM-DD).
func nextDay(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, 1).Format(time.DateOnly)
}
