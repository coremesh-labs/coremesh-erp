package techobj

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Wartung und Prüfung (seit 0.4.0) ---------------------------------------------------
//
// Welche Wartungen und Prüfungen eine Anlage braucht, steht im Katalog
// „Wartungs- und Prüfarten“ (Objektklassen, Abstand, Vorlauf, gesetzlich,
// Vorgangsart). Am technischen Objekt wird jede durchgeführte Wartung mit
// Datum, Ausführendem, Ergebnis und Prüfbericht (DMS) nachgewiesen; die
// nächste Fälligkeit ergibt sich aus dem Abstand, wenn sie nicht eingetragen
// ist. „Wartung beauftragen …“ legt einen Vorgang im Modul Vorgänge an.

func (m *Module) maintenance() *crud.Entity {
	return &crud.Entity{
		Object: "TechMaintenance", Title: "Wartungen und Prüfungen", Icon: "icon-tool", Table: "techobj__maintenance", Section: "Bestand",
		Keys:    []string{"company_code", "equipment_id", "maintenance_type", "done_on"},
		Order:   "company_code, equipment_id, maintenance_type, done_on DESC",
		Filters: []string{"company_code", "equipment_id", "maintenance_type"},
		Search:  []string{"performed_by", "result", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "equipment_id", Label: "Technisches Objekt", Short: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: equipmentLookup},
			{Key: "maintenance_type", Label: "Wartungs- bzw. Prüfart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechMaintenanceType")},
			{Key: "done_on", Label: "Durchgeführt am", Short: "Am", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "performed_by", Label: "Durchgeführt von (Firma, Prüfer)", Short: "Von", Type: tText, Listable: true},
			{Key: "partner_id", Label: "Geschäftspartner (Firma)", Short: "Partner", Type: tText,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}}},
			{Key: "result", Label: "Ergebnis (z. B. ohne Mängel, Mängel behoben, Nachprüfung)", Short: "Ergebnis", Type: tText, Listable: true},
			{Key: "next_due", Label: "Nächste Fälligkeit (leer = nach dem Abstand der Art)", Short: "Nächste", Type: tDate, Listable: true},
			{Key: "process_id", Label: "Vorgang", Type: tText},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "dokumente", Title: "Prüfbericht / Rechnung", Documents: true},
		},
		Access:      access(),
		CheckRecord: checkWrite("TechObject"),
		Validate:    m.checkMaintenance,
	}
}

func (m *Module) checkMaintenance(ctx context.Context, rec, _ crud.Record) error {
	cc, eq := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["equipment_id"]))
	rec["equipment_id"], rec["maintenance_type"] = eq, trimUpper(rec["maintenance_type"])
	day, err := crud.ParseDate(rec["done_on"])
	if err != nil {
		return err
	}
	rec["done_on"] = day
	class, err := m.one(ctx, "SELECT object_class FROM techobj__equipment WHERE company_code = ? AND equipment_id = ?", cc, eq)
	if err != nil {
		return err
	}
	if class == "" {
		return crud.Invalid("Technisches Objekt %s gibt es im Buchungskreis %s nicht", eq, cc)
	}
	res, err := m.db.Query(ctx, "SELECT object_classes, interval_months, name FROM techobj__maintenance_type WHERE company_code = ? AND code = ? AND is_active = ?",
		cc, rec["maintenance_type"], true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Wartungsart %s gibt es im Buchungskreis %s nicht (oder inaktiv)", crud.Str(rec["maintenance_type"]), cc)
	}
	if !containsCode(splitCodes(crud.Str(res.Rows[0][0])), class) {
		return crud.Invalid("%s gilt nicht für die Objektklasse %s (Katalog Wartungs- und Prüfarten)", crud.Str(res.Rows[0][2]), class)
	}
	if d := strings.TrimSpace(crud.Str(rec["next_due"])); d != "" {
		if rec["next_due"], err = crud.ParseDate(d); err != nil {
			return err
		}
	} else {
		rec["next_due"] = parseDay(day).AddDate(0, int(toInt(res.Rows[0][1])), 0).Format("2006-01-02")
	}
	for _, k := range []string{"performed_by", "partner_id", "result", "process_id"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	m.stamp(ctx, rec)
	return nil
}

func containsCode(list []string, code string) bool {
	for _, c := range list {
		if c == code {
			return true
		}
	}
	return false
}

// dueItem: eine fällige Wartung (Übersicht, Gerät).
type dueItem struct {
	equipment, designation, typeCode, typeName, due, last, processType string
	legal                                                              bool
	lead                                                               int
}

// dueMaintenance: je Gerät (heute gültig) und passender Wartungsart die
// nächste Fälligkeit; ohne Nachweis = heute fällig („noch nie erfasst“).
func (m *Module) dueMaintenance(ctx context.Context, cc, equipment string) ([]dueItem, error) {
	day := crud.Today()
	q := `SELECT e.equipment_id, e.designation, e.object_class FROM techobj__equipment e WHERE e.company_code = ? AND e.valid_from <= ? AND e.valid_to >= ?`
	args := []any{cc, day, day}
	if equipment != "" {
		q += " AND e.equipment_id = ?"
		args = append(args, equipment)
	}
	eqs, err := m.db.Query(ctx, q+" ORDER BY e.equipment_id", args...)
	if err != nil {
		return nil, err
	}
	types, err := m.db.Query(ctx, `SELECT code, name, object_classes, lead_days, is_legal, COALESCE(process_type, '') FROM techobj__maintenance_type
		WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`, cc, true)
	if err != nil {
		return nil, err
	}
	var out []dueItem
	for _, e := range eqs.Rows {
		for _, t := range types.Rows {
			if !containsCode(splitCodes(crud.Str(t[2])), crud.Str(e[2])) {
				continue
			}
			last, err := m.db.Query(ctx, `SELECT done_on, COALESCE(next_due, '') FROM techobj__maintenance WHERE company_code = ? AND equipment_id = ? AND maintenance_type = ?
				ORDER BY done_on DESC LIMIT 1`, cc, e[0], t[0])
			if err != nil {
				return nil, err
			}
			it := dueItem{equipment: crud.Str(e[0]), designation: crud.Str(e[1]), typeCode: crud.Str(t[0]), typeName: crud.Str(t[1]),
				lead: int(toInt(t[3])), legal: crud.AsBool(t[4]), processType: crud.Str(t[5]), due: day}
			if len(last.Rows) > 0 {
				it.last, it.due = date(last.Rows[0][0]), date(last.Rows[0][1])
			}
			out = append(out, it)
		}
	}
	return out, nil
}

func (d dueItem) text() string {
	s := d.typeName
	if d.legal {
		s += " (Pflicht)"
	}
	if d.last == "" {
		return s + ": noch nie erfasst"
	}
	status := "fällig " + germanDate(d.due)
	if d.due < crud.Today() {
		status = "überfällig seit " + germanDate(d.due)
	}
	return s + ": " + status + " (zuletzt " + germanDate(d.last) + ")"
}

// maintenanceText: Fälligkeiten eines Geräts für die Anzeige am Gerät.
func (m *Module) maintenanceText(ctx context.Context, cc, eq string) (string, error) {
	items, err := m.dueMaintenance(ctx, cc, eq)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, it := range items {
		parts = append(parts, it.text())
	}
	return strings.Join(parts, "; "), nil
}

var orderFields = []crud.Field{
	{Key: "mo_type", Label: "Wartungs- bzw. Prüfart (leer = die nächste fällige)", Type: tText, ActionOnly: true, Lookup: catalogLookup("TechMaintenanceType")},
	{Key: "mo_note", Label: "Hinweis für den Vorgang (z. B. Termin mit Mieter abstimmen)", Type: tArea, ActionOnly: true},
}

func (m *Module) orderMaintenanceAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "orderMaintenance", Label: "Wartung beauftragen …", Icon: "icon-tool", Record: true,
		Fields: []string{"mo_type", "mo_note"}}, Handle: m.orderMaintenance}
}

// orderMaintenance: Vorgang (Modul process) zur fälligen Wartung mit Gerät, Lage,
// Nummern und Fälligkeit; Vorgangsart aus der Wartungsart.
func (m *Module) orderMaintenance(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Type string `json:"mo_type"`
			Note string `json:"mo_note"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, eq, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Technisches Objekt: id <BuKr>|<ID>")
	}
	if err := requireWrite(ctx, "TechObject", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	items, err := m.dueMaintenance(ctx, cc, eq)
	if err != nil {
		return sdk.Response{}, err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].due < items[j].due })
	var it *dueItem
	for i := range items {
		if want := trimUpper(in.Data.Type); want == "" || items[i].typeCode == want {
			it = &items[i]
			break
		}
	}
	if it == nil {
		return sdk.Response{}, crud.Invalid("Für %s ist keine passende Wartungsart hinterlegt (Katalog Wartungs- und Prüfarten, Objektklasse)", eq)
	}
	if it.processType == "" {
		return sdk.Response{}, crud.Invalid("%s: keine Vorgangsart an der Wartungsart – Einstellungen → Wartungs- und Prüfarten", it.typeName)
	}
	var lines []string
	lines = append(lines, it.text())
	if nums, err := m.numbersText(ctx, cc, eq); err == nil && nums != "" {
		lines = append(lines, "Nummern: "+nums)
	}
	if loc, err := m.one(ctx, "SELECT COALESCE(location_id, '') FROM techobj__equipment WHERE company_code = ? AND equipment_id = ?", cc, eq); err == nil && loc != "" {
		if info, err := m.locationInfo(ctx, cc, loc); err == nil {
			lines = append(lines, "Lage: "+info.Path)
			if info.Access != "" {
				lines = append(lines, "Zugang: "+info.Access)
			}
		}
	}
	if n := strings.TrimSpace(in.Data.Note); n != "" {
		lines = append(lines, n)
	}
	data := map[string]any{"company_code": cc, "process_type": it.processType, "subject": it.typeName + ": " + eq + " " + it.designation,
		"description": strings.Join(lines, "\n"), "event_date": it.due}
	resp, err := m.services.Call(ctx, "Process", "create", map[string]any{"data": data})
	if err != nil {
		return sdk.Response{}, crud.Invalid("Vorgang nicht angelegt: %v", err)
	}
	var p struct {
		ID string `json:"process_id"`
	}
	_ = sdk.Decode(resp.Payload, &p)
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Vorgang %s angelegt: %s – nach der Wartung den Nachweis unter „Wartungen“ eintragen", p.ID, data["subject"])}}, nil
}

// --- Übersicht ----------------------------------------------------------------------

var overviewFields = []crud.Field{
	{Key: "ov_company", Label: "Buchungskreis", Type: tText, ActionOnly: true, Lookup: lookupCC},
	{Key: "ov_days", Label: "Vorschau in Tagen (leer = 90)", Type: tNum, ActionOnly: true},
}

func (m *Module) overviewAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "overview", Label: "Fälligkeiten und Auffälligkeiten …", Icon: "icon-bell",
		Fields: []string{"ov_company", "ov_days"}}, Handle: m.overview}
}

// overview: alles, was zu tun ist – Wartungen, Eichfristen, Ablesungen,
// noch nicht gemeldete Stände und Auffälligkeiten im Verbrauch.
func (m *Module) overview(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Data struct {
			Company string `json:"ov_company"`
			Days    any    `json:"ov_days"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.Data.Company)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis fehlt")
	}
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	horizon := int(toInt(in.Data.Days))
	if horizon <= 0 {
		horizon = 90
	}
	today := time.Now()
	day := today.Format("2006-01-02")
	until := today.AddDate(0, 0, horizon).Format("2006-01-02")
	type row struct{ sort, kind, object, text string }
	var rows []row
	add := func(sortKey, kind, object, text string) { rows = append(rows, row{sortKey, kind, object, text}) }

	// Wartungen
	items, err := m.dueMaintenance(ctx, cc, "")
	if err != nil {
		return sdk.Response{}, err
	}
	for _, it := range items {
		lead := today.AddDate(0, 0, max(it.lead, horizon)).Format("2006-01-02")
		if it.due <= lead {
			add(it.due, "Wartung", it.equipment+" "+it.designation, it.text())
		}
	}
	// Eichfristen eingebauter Zähler (Ende des Eichjahres + Frist)
	eqs, err := m.db.Query(ctx, `SELECT e.equipment_id, e.designation, COALESCE(e.type_id, ''), e.calibration_year, i.point_id FROM techobj__equipment e
		JOIN techobj__installation i ON i.company_code = e.company_code AND i.equipment_id = e.equipment_id AND i.valid_from <= ? AND i.valid_to >= ?
		WHERE e.company_code = ? AND e.calibration_year IS NOT NULL AND e.calibration_year > 0`, day, day, cc)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, e := range eqs.Rows {
		years, err := m.calibrationYears(ctx, cc, crud.Str(e[2]))
		if err != nil {
			return sdk.Response{}, err
		}
		if years <= 0 {
			continue
		}
		end := fmt.Sprintf("%d-12-31", toInt(e[3])+years)
		if end <= until {
			text := "Eichung gültig bis " + germanDate(end)
			if end < day {
				text = "Eichung abgelaufen am " + germanDate(end) + " – Tausch veranlassen"
			}
			add(end, "Eichfrist", crud.Str(e[0])+" "+crud.Str(e[1])+" (in "+crud.Str(e[4])+")", text)
		}
	}
	// Ablesungen, Mitteilungen und Auffälligkeiten je Messpunkt
	pts, err := m.db.Query(ctx, `SELECT p.point_id, p.designation, COALESCE(md.reading_days, 0) FROM techobj__point p
		LEFT JOIN techobj__medium md ON md.company_code = p.company_code AND md.code = p.medium
		WHERE p.company_code = ? AND p.valid_from <= ? AND p.valid_to >= ? ORDER BY p.point_id`, cc, day, day)
	if err != nil {
		return sdk.Response{}, err
	}
	noticeRoles, err := m.db.Query(ctx, "SELECT DISTINCT link_role FROM techobj__notice_type WHERE company_code = ? AND is_active = ?", cc, true)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, p := range pts.Rows {
		id, name, every := crud.Str(p[0]), crud.Str(p[1]), int(toInt(p[2]))
		if eq, err := m.installedID(ctx, cc, id, day); err != nil {
			return sdk.Response{}, err
		} else if eq == "" {
			add(day, "Messpunkt", id+" "+name, "kein Gerät eingebaut")
			continue
		}
		last := date(firstOr(m.one(ctx, "SELECT MAX(read_on) FROM techobj__reading WHERE company_code = ? AND point_id = ?", cc, id)))
		if every > 0 {
			if last == "" {
				add(day, "Ablesung", id+" "+name, "noch keine Ablesung")
			} else if due := parseDay(last).AddDate(0, 0, every).Format("2006-01-02"); due <= until {
				add(due, "Ablesung", id+" "+name, "fällig "+germanDate(due)+" (letzte "+germanDate(last)+")")
			}
		}
		// letzte Stände noch nicht gemeldet (nur, wenn ein Empfänger verknüpft ist)
		if last != "" && (every == 0 || parseDay(last).AddDate(0, 0, every).Format("2006-01-02") >= day) {
			for _, r := range noticeRoles.Rows {
				has, err := m.one(ctx, `SELECT 1 FROM techobj__link WHERE company_code = ? AND source_kind = ? AND source_id = ? AND role_code = ? AND valid_from <= ? AND valid_to >= ?`,
					cc, "MeterPoint", id, r[0], last, last)
				if err != nil {
					return sdk.Response{}, err
				}
				if has == "" {
					continue
				}
				open, err := m.one(ctx, `SELECT COUNT(*) FROM techobj__reading r
					LEFT JOIN techobj__reading_type t ON t.company_code = r.company_code AND t.code = r.reading_type
					WHERE r.company_code = ? AND r.point_id = ? AND r.read_on = ? AND r.reported_on IS NULL AND COALESCE(t.skip_notice, ?) = ?`, cc, id, last, false, false)
				if err != nil {
					return sdk.Response{}, err
				}
				if toInt(open) > 0 {
					add(last, "Mitteilung", id+" "+name, "Stand vom "+germanDate(last)+" noch nicht gemeldet – „Ablesemitteilung …“")
					break
				}
			}
		}
		a, err := m.analyze(ctx, cc, id)
		if err != nil {
			return sdk.Response{}, err
		}
		recent := today.AddDate(-1, 0, 0).Format("2006-01-02")
		for _, n := range a.notes {
			if !n.info && n.to >= recent {
				add(n.to, "Auffälligkeit", id+" "+name, n.text)
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].sort < rows[j].sort })
	out := make([][]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, []any{germanDate(r.sort), r.kind, r.object, r.text})
	}
	msg := fmt.Sprintf("Buchungskreis %s: %d Punkte bis %s", cc, len(rows), germanDate(until))
	if len(rows) == 0 {
		msg = fmt.Sprintf("Buchungskreis %s: bis %s ist nichts fällig und nichts auffällig", cc, germanDate(until))
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "table": map[string]any{"columns": []string{"Datum", "Art", "Objekt", "Hinweis"}, "rows": out}}}, nil
}

func firstOr(s string, _ error) string { return s }

// --- Dienst für andere Module: Messpunkte eines Objekts -----------------------------------

// pointsFor: TechObjectSetup.pointsFor {company_code, object, id, date} – Messpunkte,
// die am Tag für ein Objekt eines anderen Moduls messen (Rolle „Nutzer“, z. B. das
// Mietobjekt einer Übergabe), mit eingebautem Gerät, Nummern und Zählwerken.
// object ist das Object des anderen Moduls (z. B. RentObject), wie es im Katalog
// „Verknüpfbare Objekte“ steht.
func (m *Module) pointsFor(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		Object      string `json:"object"`
		ID          string `json:"id"`
		Date        string `json:"date"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.CompanyCode)
	if ok, err := sdk.CheckAccess(ctx, "TechObject", "read", cc); err != nil {
		return sdk.Response{}, err
	} else if !ok {
		return sdk.Response{}, fmt.Errorf("%w: keine Berechtigung im Buchungskreis %s", sdk.ErrPermissionDenied, cc)
	}
	day := crud.Today()
	if strings.TrimSpace(in.Date) != "" {
		var err error
		if day, err = crud.ParseDate(in.Date); err != nil {
			return sdk.Response{}, err
		}
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT l.source_id, p.designation, p.medium, COALESCE(md.name, p.medium), COALESCE(p.market_location, '') FROM techobj__link l
		JOIN techobj__link_object o ON o.company_code = l.company_code AND o.code = l.object_type AND o.service_object = ?
		JOIN techobj__link_role r ON r.company_code = l.company_code AND r.code = l.role_code AND r.is_user = ?
		JOIN techobj__point p ON p.company_code = l.company_code AND p.point_id = l.source_id
		LEFT JOIN techobj__medium md ON md.company_code = p.company_code AND md.code = p.medium
		WHERE l.company_code = ? AND l.source_kind = ? AND l.object_id = ? AND l.valid_from <= ? AND l.valid_to >= ?
		ORDER BY l.source_id`, strings.TrimSpace(in.Object), true, cc, "MeterPoint", strings.TrimSpace(in.ID), day, day)
	if err != nil {
		return sdk.Response{}, err
	}
	var items []any
	for _, r := range res.Rows {
		point := crud.Str(r[0])
		it := map[string]any{"point_id": point, "designation": r[1], "medium": r[2], "medium_name": r[3], "market_location": r[4]}
		if loc, pos, err := m.pointLocationAt(ctx, cc, point, day); err == nil && loc != "" {
			if info, err := m.locationInfo(ctx, cc, loc); err == nil {
				it["location"] = strings.TrimSpace(info.Path + " " + pos)
			}
		}
		eq, err := m.installedID(ctx, cc, point, day)
		if err != nil {
			return sdk.Response{}, err
		}
		if eq != "" {
			it["equipment_id"] = eq
			it["numbers"], _ = m.numbersText(ctx, cc, eq)
			it["meter_number"], _ = m.meterNumbers(ctx, cc, eq, "METER")
			regs, err := m.db.Query(ctx, `SELECT g.register_code, g.designation, COALESCE(u.name, g.unit), g.decimals FROM techobj__register g
				LEFT JOIN techobj__unit u ON u.company_code = g.company_code AND u.code = g.unit
				WHERE g.company_code = ? AND g.equipment_id = ? ORDER BY g.register_code`, cc, eq)
			if err != nil {
				return sdk.Response{}, err
			}
			var rs []any
			for _, g := range regs.Rows {
				reg := map[string]any{"register_code": g[0], "designation": g[1], "unit": g[2], "decimals": g[3]}
				last, err := m.db.Query(ctx, `SELECT read_on, value FROM techobj__reading WHERE company_code = ? AND equipment_id = ? AND register_code = ? AND read_on <= ?
					ORDER BY read_on DESC LIMIT 1`, cc, eq, g[0], day)
				if err == nil && len(last.Rows) > 0 {
					reg["last_read_on"], reg["last_value"] = date(last.Rows[0][0]), last.Rows[0][1]
				}
				rs = append(rs, reg)
			}
			it["registers"] = rs
		}
		items = append(items, it)
	}
	return sdk.Response{Payload: map[string]any{"items": items}}, nil
}
