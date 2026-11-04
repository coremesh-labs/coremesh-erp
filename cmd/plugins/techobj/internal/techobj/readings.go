package techobj

import (
	"context"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Ablesungen ------------------------------------------------------------------------

// Eine Ablesung ist ein neutrales Faktum je Zählwerk und Tag: welcher Stand,
// welche Ableseart, wer, woher, an wen gemeldet. Der fachliche Schlüssel
// (Gerät, Zählwerk, Tag, Ableseart) macht erneutes Senden – etwa aus einer
// Offline-Erfassung am Tablet – unschädlich: es gibt keine Dubletten.

func (m *Module) reading() *crud.Entity {
	return &crud.Entity{
		Object: "MeterReading", Title: "Ablesungen", Icon: "icon-edit", Table: "techobj__reading", Section: "Bestand",
		Keys:    []string{"company_code", "equipment_id", "register_code", "read_on", "reading_type"},
		Order:   "company_code, equipment_id, register_code, read_on",
		Filters: []string{"company_code", "equipment_id", "register_code", "point_id", "reading_type", "status"},
		Search:  []string{"reader", "source", "reported_to", "note"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"equipment_id", "register_code", "read_on", "reading_type", "point_id", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "equipment_id", Label: "Gerät (technisches Objekt)", Short: "Gerät", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: equipmentLookup},
			{Key: "register_code", Label: "Zählwerk", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "read_on", Label: "Abgelesen am", Short: "Datum", Type: tDate, Required: true, Listable: true, Immutable: true},
			{Key: "reading_type", Label: "Ableseart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechReadingType")},
			{Key: "value", Label: "Stand", Type: tNum, Required: true, Listable: true},
			{Key: "consumption", Label: "Verbrauch seit voriger Ablesung (mit Faktor des Zählwerks)", Short: "Verbrauch", Type: tNum, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "unit", Label: "Einheit", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			{Key: "read_time", Label: "Uhrzeit (optional, HH:MM)", Short: "Uhrzeit", Type: tText},
			{Key: "point_id", Label: "Messpunkt (leer = aus dem Einbau am Tag)", Short: "Messpunkt", Type: tText, Listable: true, Lookup: pointLookup},
			{Key: "reader", Label: "Abgelesen von (Person, Versorger, Messdienst)", Short: "Ableser", Type: tText, Listable: true},
			{Key: "source", Label: "Herkunft (z. B. Foto, Abrechnung 2024, Tabelle Ablesewerte.ods)", Short: "Herkunft", Type: tText},
			{Key: "reported_to", Label: "Gemeldet an (Versorger, Kunden-/Vertragskontonummer)", Short: "Gemeldet an", Type: tText},
			{Key: "reported_on", Label: "Gemeldet am", Type: tDate, Listable: true},
			{Key: "report_document", Label: "Mitteilung im DMS", Short: "Mitteilung", Type: tText, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tText, Listable: true, Lookup: catalogLookup("TechReadingStatus")},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "dokumente", Title: "Foto / Beleg", Documents: true},
		},
		Access:      access(),
		CheckRecord: checkWrite("MeterReading"),
		Validate:    m.checkReading,
		Decorate:    m.decorateReading,
	}
}

func (m *Module) checkReading(ctx context.Context, rec, old crud.Record) error {
	cc, eq := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["equipment_id"]))
	reg := trimUpper(rec["register_code"])
	rec["equipment_id"], rec["register_code"], rec["reading_type"] = eq, reg, trimUpper(rec["reading_type"])
	day, err := crud.ParseDate(rec["read_on"])
	if err != nil {
		return err
	}
	rec["read_on"] = day
	if day > time.Now().Format("2006-01-02") {
		return crud.Invalid("Ablesedatum %s liegt in der Zukunft", germanDate(day))
	}
	res, err := m.db.Query(ctx, "SELECT overflow FROM techobj__register WHERE company_code = ? AND equipment_id = ? AND register_code = ?", cc, eq, reg)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Zählwerk %s gibt es bei %s nicht (Abschnitt „Zählwerke“ am Gerät)", reg, eq)
	}
	overflow := toFloat(res.Rows[0][0])
	if err := m.requireCatalog(ctx, "reading_type", "Ableseart", cc, rec["reading_type"]); err != nil {
		return err
	}
	value := toFloat(rec["value"])
	if value < 0 {
		return crud.Invalid("Stand darf nicht negativ sein")
	}
	if overflow > 0 && value >= overflow {
		return crud.Invalid("Stand %s erreicht den Überlauf %s des Zählwerks", fmtNum(value), fmtNum(overflow))
	}
	if t := strings.TrimSpace(crud.Str(rec["read_time"])); t != "" {
		if _, err := time.Parse("15:04", t); err != nil {
			return crud.Invalid("Uhrzeit %q: HH:MM", t)
		}
		rec["read_time"] = t
	} else {
		rec["read_time"] = nil
	}
	point := strings.TrimSpace(crud.Str(rec["point_id"]))
	if point == "" {
		if point, err = m.one(ctx, `SELECT point_id FROM techobj__installation WHERE company_code = ? AND equipment_id = ? AND valid_from <= ? AND valid_to >= ?`,
			cc, eq, day, day); err != nil {
			return err
		}
	}
	rec["point_id"] = nilIfEmpty(point)
	rec["status"] = trimUpper(rec["status"])
	if crud.Str(rec["status"]) == "" {
		def, err := m.one(ctx, "SELECT code FROM techobj__reading_status WHERE company_code = ? AND is_default = ? AND is_active = ?", cc, true, true)
		if err != nil {
			return err
		}
		rec["status"] = nilIfEmpty(def)
	}
	if err := m.requireCatalog(ctx, "reading_status", "Status der Ablesung", cc, rec["status"]); err != nil {
		return err
	}
	rejected, err := m.one(ctx, "SELECT is_rejected FROM techobj__reading_status WHERE company_code = ? AND code = ?", cc, rec["status"])
	if err != nil {
		return err
	}
	if d := strings.TrimSpace(crud.Str(rec["reported_on"])); d != "" {
		if rec["reported_on"], err = crud.ParseDate(d); err != nil {
			return err
		}
	} else {
		rec["reported_on"] = nil
	}
	for _, k := range []string{"reader", "source", "reported_to"} {
		rec[k] = nilIfEmpty(strings.TrimSpace(crud.Str(rec[k])))
	}
	if !crud.AsBool(rejected) {
		if err := m.checkMonotonic(ctx, cc, eq, reg, day, crud.Str(rec["reading_type"]), value, overflow); err != nil {
			return err
		}
	}
	m.stamp(ctx, rec)
	return nil
}

// checkMonotonic: Ein Zählerstand fällt nicht – außer über den Überlauf. Am
// selben Tag (z. B. Versorgerwechsel und turnusmäßig) gelten beide Richtungen.
func (m *Module) checkMonotonic(ctx context.Context, cc, eq, reg, day, typ string, value, overflow float64) error {
	prev, err := m.db.Query(ctx, `SELECT read_on, value FROM techobj__reading WHERE company_code = ? AND equipment_id = ? AND register_code = ?
		AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = techobj__reading.company_code AND s.code = techobj__reading.status AND s.is_rejected) AND (read_on < ? OR (read_on = ? AND reading_type <> ?)) ORDER BY read_on DESC LIMIT 1`, cc, eq, reg, day, day, typ)
	if err != nil {
		return err
	}
	if len(prev.Rows) > 0 {
		if pv := toFloat(prev.Rows[0][1]); value < pv {
			if overflow > 0 {
				crud.Warn(ctx, "Stand %s ist kleiner als %s vom %s – Überlauf bei %s angenommen", fmtNum(value), fmtNum(pv), germanDate(date(prev.Rows[0][0])), fmtNum(overflow))
			} else {
				return crud.Invalid("Stand %s ist kleiner als %s vom %s – Zählerstände fallen nicht (Tausch? Dann „Zählertausch …“ am Messpunkt; Fehlablesung? Status mit „verworfen“)",
					fmtNum(value), fmtNum(pv), germanDate(date(prev.Rows[0][0])))
			}
		}
	}
	next, err := m.db.Query(ctx, `SELECT read_on, value FROM techobj__reading WHERE company_code = ? AND equipment_id = ? AND register_code = ?
		AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = techobj__reading.company_code AND s.code = techobj__reading.status AND s.is_rejected) AND read_on > ? ORDER BY read_on LIMIT 1`, cc, eq, reg, day)
	if err != nil {
		return err
	}
	if len(next.Rows) > 0 && overflow == 0 {
		if nv := toFloat(next.Rows[0][1]); value > nv {
			return crud.Invalid("Stand %s ist größer als %s vom %s", fmtNum(value), fmtNum(nv), germanDate(date(next.Rows[0][0])))
		}
	}
	return nil
}

func (m *Module) decorateReading(ctx context.Context, rec crud.Record) error {
	cc, eq, reg := crud.Str(rec["company_code"]), crud.Str(rec["equipment_id"]), crud.Str(rec["register_code"])
	g, err := m.db.Query(ctx, `SELECT COALESCE(g.overflow, 0), COALESCE(g.factor, 1), COALESCE(g.decimals, 0), COALESCE(NULLIF(u.symbol, ''), u.name, g.unit, '')
		FROM techobj__register g LEFT JOIN techobj__unit u ON u.company_code = g.company_code AND u.code = g.unit
		WHERE g.company_code = ? AND g.equipment_id = ? AND g.register_code = ?`, cc, eq, reg)
	if err != nil || len(g.Rows) == 0 {
		return err
	}
	overflow, factor, decimals := toFloat(g.Rows[0][0]), toFloat(g.Rows[0][1]), toInt(g.Rows[0][2])
	rec["unit"] = crud.Str(g.Rows[0][3])
	res, err := m.db.Query(ctx, `SELECT r.value FROM techobj__reading r
		WHERE r.company_code = ? AND r.equipment_id = ? AND r.register_code = ? AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = r.company_code AND s.code = r.status AND s.is_rejected) AND r.read_on < ?
		ORDER BY r.read_on DESC LIMIT 1`, cc, eq, reg, date(rec["read_on"]))
	if err != nil || len(res.Rows) == 0 {
		return err
	}
	d := toFloat(rec["value"]) - toFloat(res.Rows[0][0])
	if d < 0 && overflow > 0 {
		d += overflow
	}
	if factor == 0 {
		factor = 1
	}
	p := math.Pow(10, float64(decimals))
	rec["consumption"] = math.Round(d*factor*p) / p
	return nil
}

func fmtNum(f float64) string {
	return strings.Replace(strconv.FormatFloat(f, 'f', -1, 64), ".", ",", 1)
}

// dayBefore: 2026-03-01 → 2026-02-28.
func dayBefore(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.AddDate(0, 0, -1).Format("2006-01-02")
}
