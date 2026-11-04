package inspection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Status eines Protokolls: offen (wird erfasst) oder abgeschlossen (gedruckt,
// abgelegt, Zählerstände gebucht – danach nicht mehr änderbar).
const (
	statusOpen = "OPEN"
	statusDone = "DONE"
)

var statusOptions = []metamodel.Option{{Value: statusOpen, Label: "offen"}, {Value: statusDone, Label: "abgeschlossen"}}

var printModes = []metamodel.Option{
	{Value: "BLANK", Label: "Formular für die Begehung (ohne Zustand und Stände)"},
	{Value: "PREVIEW", Label: "Vorschau mit den erfassten Angaben"},
	{Value: "FINAL", Label: "Abschließen: Zählerstände buchen, im DMS ablegen, Mängel als Vorgang"},
}

func (m *Module) protocol() *crud.Entity {
	return &crud.Entity{
		Object: "InspectionProtocol", Title: "Protokolle", Icon: "icon-clipboard", Table: "inspection__protocol", Section: "Protokolle",
		Keys: []string{"company_code", "protocol_id"}, Order: "company_code, protocol_date DESC, protocol_id DESC", TitleField: "title",
		Filters: []string{"company_code", "protocol_type", "object_id", "contract_id", "status"}, Search: []string{"protocol_id", "title", "note"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"protocol_type", "object_id", "status"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "protocol_type", Label: "Protokollart", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("InspectionType")},
			{Key: "protocol_id", Label: "Protokoll", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
			{Key: "object_id", Label: "Mietobjekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: rentObjectLookup},
			{Key: "protocol_date", Label: "Datum der Übergabe bzw. Begehung (leer = heute)", Short: "Datum", Type: tDate, Listable: true},
			{Key: "contract_id", Label: "Mietvertrag (leer = der am Tag laufende bzw. beginnende Vertrag der Vertragsarten)", Short: "Vertrag", Type: tText, Listable: true, Lookup: contractLookup},
			{Key: "partner_id", Label: "Mieter (aus dem Vertrag)", Short: "Mieter", Type: tText, ReadOnly: true, Listable: true, Lookup: partnerLookup},
			{Key: "title", Label: "Bezeichnung (leer = Art und Mietobjekt)", Short: "Bezeichnung", Type: tText, Listable: true, Wide: true},
			{Key: "place", Label: "Ort der Unterschrift", Short: "Ort", Type: tText},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: statusOptions},
			{Key: "note", Label: "Bemerkungen und Vereinbarungen (im Protokoll vor den Unterschriften)", Short: "Bemerkungen", Type: tArea},
			{Key: "document", Label: "Protokoll im DMS", Short: "DMS", Type: tText, ReadOnly: true, Group: "Abschluss"},
			{Key: "process_id", Label: "Vorgang zu den Mängeln", Short: "Vorgang", Type: tText, ReadOnly: true, Group: "Abschluss"},
			{Key: "done_at", Label: "Abgeschlossen am", Type: tText, ReadOnly: true, Group: "Abschluss"},
			{Key: "done_by", Label: "Abgeschlossen von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser, Group: "Abschluss"},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "pr_mode", Label: "Druck", Type: tSel, ActionOnly: true, Options: printModes},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "teilnehmer", Title: "Teilnehmer", Relation: &metamodel.Relation{Object: "InspectionParticipant", ForeignKey: "protocol_id",
				Match: match(), Columns: []string{"role", "name", "present", "signs"}}},
			{Key: "raeume", Title: "Räume und Bauteile", Relation: &metamodel.Relation{Object: "InspectionItem", ForeignKey: "protocol_id",
				Match: match(), Columns: []string{"room_name", "component_name", "description", "condition", "remark"}}},
			{Key: "zaehler", Title: "Zählerstände", Relation: &metamodel.Relation{Object: "InspectionMeter", ForeignKey: "protocol_id",
				Match: match(), Columns: []string{"designation", "meter_number", "location", "last_value", "value"}}},
			{Key: "pruefliste", Title: "Schlüssel und Prüfliste", Relation: &metamodel.Relation{Object: "InspectionCheck", ForeignKey: "protocol_id",
				Match: match(), Columns: []string{"name", "value", "note"}}},
			{Key: "dokumente", Title: "Fotos und Dokumente", Documents: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "prepare", Label: "Räume, Zähler und Teilnehmer übernehmen", Icon: "icon-inbox", Record: true}, Handle: m.prepareAction},
			{ActionConfig: metamodel.ActionConfig{Name: "print", Label: "Protokoll drucken …", Icon: "icon-printer", Record: true, Fields: []string{"pr_mode"}}, Handle: m.printAction},
			{ActionConfig: metamodel.ActionConfig{Name: "delete", Label: "Löschen", Record: true,
				Confirm: "Offenes Protokoll mit allen Zeilen endgültig löschen?"}, Handle: m.deleteProtocol},
		},
		Access:      access(),
		CheckRecord: checkWrite("InspectionProtocol"),
		Prepare:     m.prepareProtocol,
		Validate:    m.checkProtocol,
	}
}

func match() map[string]string {
	return map[string]string{"company_code": "company_code", "protocol_id": "protocol_id"}
}

// prepareProtocol (vor der Transaktion): Kataloge, Nummer, Vertrag und Mieter.
func (m *Module) prepareProtocol(ctx context.Context, rec crud.Record) error {
	cc := strings.TrimSpace(crud.Str(rec["company_code"]))
	if cc == "" {
		return nil // Pflichtprüfung meldet es
	}
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		_, err := m.setupCatalogs(ctx, cc)
		return err
	}); err != nil {
		return err
	}
	rec["protocol_type"], rec["object_id"] = trimUpper(rec["protocol_type"]), strings.TrimSpace(crud.Str(rec["object_id"]))
	t, err := m.protocolType(ctx, cc, crud.Str(rec["protocol_type"]))
	if err != nil {
		return err
	}
	day := strings.TrimSpace(crud.Str(rec["protocol_date"]))
	if day == "" {
		day = crud.Today()
	}
	if day, err = crud.ParseDate(day); err != nil {
		return err
	}
	rec["protocol_date"] = day
	obj, err := m.rentObject(ctx, cc, crud.Str(rec["object_id"]))
	if err != nil {
		return err
	}
	contract := strings.TrimSpace(crud.Str(rec["contract_id"]))
	if contract == "" && len(t.contractTypes) > 0 {
		if contract, err = m.findContract(ctx, cc, crud.Str(rec["object_id"]), day, t.contractTypes); err != nil {
			return err
		}
	}
	if contract != "" {
		c, err := m.contract(ctx, cc, contract)
		if err != nil {
			return err
		}
		if len(t.contractTypes) > 0 && !containsCode(t.contractTypes, c.Type) {
			return crud.Invalid("Vertrag %s hat die Vertragsart %s – %s erwartet %s", contract, c.Type, t.name, strings.Join(t.contractTypes, ", "))
		}
		rec["contract_id"], rec["partner_id"] = contract, nilIfEmpty(c.PartnerID)
	} else {
		rec["contract_id"], rec["partner_id"] = nil, nil
	}
	if strings.TrimSpace(crud.Str(rec["title"])) == "" {
		title := t.name + " " + obj
		if name := m.partnerName(ctx, crud.Str(rec["partner_id"])); name != "" {
			title += " – " + name
		}
		rec["title"] = title
	}
	year := day[:4]
	prefix := t.prefix + "-" + year + "-"
	last, err := m.one(ctx, "SELECT MAX(protocol_id) FROM inspection__protocol WHERE company_code = ? AND protocol_id LIKE ?", cc, prefix+"%")
	if err != nil {
		return err
	}
	n := 0
	if last != "" {
		fmt.Sscanf(strings.TrimPrefix(last, prefix), "%d", &n)
	}
	rec["protocol_id"], rec["status"] = fmt.Sprintf("%s%04d", prefix, n+1), statusOpen
	return nil
}

func (m *Module) checkProtocol(ctx context.Context, rec, old crud.Record) error {
	if old != nil {
		if crud.Str(old["status"]) == statusDone {
			return crud.Invalid("Protokoll %s ist abgeschlossen und kann nicht mehr geändert werden", crud.Str(old["protocol_id"]))
		}
		if d := strings.TrimSpace(crud.Str(rec["protocol_date"])); d != "" {
			var err error
			if rec["protocol_date"], err = crud.ParseDate(d); err != nil {
				return err
			}
		}
	}
	if crud.Str(rec["protocol_date"]) == "" {
		return crud.Invalid("Datum fehlt")
	}
	m.stamp(ctx, rec)
	return nil
}

// --- Protokollart und Bezüge -------------------------------------------------------

type protocolType struct {
	code, name, prefix, readingType, template, docType, defectType, intro, closing string
	contractTypes                                                                  []string
	withRooms, withMeters                                                          bool
}

func (m *Module) protocolType(ctx context.Context, cc, code string) (*protocolType, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, id_prefix, COALESCE(reading_type, ''), COALESCE(template, ''), COALESCE(doc_type, ''),
		COALESCE(defect_process_type, ''), COALESCE(intro, ''), COALESCE(closing, ''), COALESCE(contract_types, ''), with_rooms, with_meters
		FROM inspection__protocol_type WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Protokollart %q gibt es im Buchungskreis %s nicht (oder inaktiv) – Begehungen und Protokolle → Einstellungen → Protokollarten", code, cc)
	}
	r := res.Rows[0]
	s := func(i int) string { return crud.Str(r[i]) }
	return &protocolType{code: s(0), name: s(1), prefix: s(2), readingType: s(3), template: s(4), docType: s(5), defectType: s(6),
		intro: s(7), closing: s(8), contractTypes: splitCodes(s(9)), withRooms: crud.AsBool(r[10]), withMeters: crud.AsBool(r[11])}, nil
}

// rentObject: Bezeichnung des Mietobjekts (Modul realestate).
func (m *Module) rentObject(ctx context.Context, cc, id string) (string, error) {
	resp, err := m.services.Call(ctx, "RentObject", "get", map[string]any{"id": cc + "|" + id})
	if err != nil {
		return "", crud.Invalid("Mietobjekt %s im Buchungskreis %s: %v", id, cc, err)
	}
	var o struct {
		Designation string `json:"designation"`
	}
	_ = sdk.Decode(resp.Payload, &o)
	return strings.TrimSpace(id + " " + o.Designation), nil
}

type contractInfo struct {
	ID, Type, PartnerID, Designation, From, To string
}

func (m *Module) contract(ctx context.Context, cc, id string) (*contractInfo, error) {
	resp, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + id})
	if err != nil {
		return nil, crud.Invalid("Vertrag %s im Buchungskreis %s: %v", id, cc, err)
	}
	var c struct {
		ID          string `json:"contract_id"`
		Type        string `json:"contract_type"`
		PartnerID   string `json:"partner_id"`
		Designation string `json:"designation"`
	}
	if err := sdk.Decode(resp.Payload, &c); err != nil {
		return nil, err
	}
	return &contractInfo{ID: id, Type: c.Type, PartnerID: c.PartnerID, Designation: c.Designation}, nil
}

// findContract: Vertrag der Vertragsarten am Mietobjekt, der am Tag läuft; sonst
// der nächste, der danach beginnt (Einzug), sonst der zuletzt beendete (Auszug).
func (m *Module) findContract(ctx context.Context, cc, object, day string, types []string) (string, error) {
	items, err := m.list(ctx, "ContractObject", map[string]any{"company_code": cc, "object_type": "RentObject", "object_id": object, "includeHistory": "true"})
	if err != nil {
		return "", nil // ohne Vertragsmodul: von Hand
	}
	var running, next, ended *contractInfo
	for _, it := range items {
		id := crud.Str(it["contract_id"])
		from, to := date(it["valid_from"]), date(it["valid_to"])
		if to == "" {
			to = crud.DateMax
		}
		c, err := m.contract(ctx, cc, id)
		if err != nil || !containsCode(types, c.Type) {
			continue
		}
		c.From, c.To = from, to
		switch {
		case from <= day && to >= day:
			if running == nil || c.From > running.From {
				running = c
			}
		case from > day:
			if next == nil || c.From < next.From {
				next = c
			}
		default:
			if ended == nil || c.To > ended.To {
				ended = c
			}
		}
	}
	for _, c := range []*contractInfo{running, next, ended} {
		if c != nil {
			return c.ID, nil
		}
	}
	return "", nil
}

// openProtocol: Protokoll muss offen sein (für Zeilen und Aktionen).
func (m *Module) openProtocol(ctx context.Context, cc, id string) error {
	st, err := m.one(ctx, "SELECT status FROM inspection__protocol WHERE company_code = ? AND protocol_id = ?", cc, id)
	if err != nil {
		return err
	}
	switch st {
	case "":
		return crud.Invalid("Protokoll %s gibt es im Buchungskreis %s nicht", id, cc)
	case statusDone:
		return crud.Invalid("Protokoll %s ist abgeschlossen und kann nicht mehr geändert werden", id)
	}
	return nil
}

// --- Zeilen ---------------------------------------------------------------------------

// lineEntity: gemeinsame Teile der Zeilen eines Protokolls.
func (m *Module) lineEntity(object, title, table string, keys []string, order string, fields []crud.Field, validate func(ctx context.Context, rec, old crud.Record) error) *crud.Entity {
	head := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "protocol_id", Label: "Protokoll", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: protocolLookup},
	}
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-list", Table: "inspection__" + table, Section: "Protokolle",
		Keys: append([]string{"company_code", "protocol_id"}, keys...), Order: order,
		Filters: []string{"company_code", "protocol_id"},
		Fields:  append(head, fields...),
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Entfernen", Record: true,
			Confirm: "Zeile aus dem Protokoll entfernen?"}, Handle: m.removeLine(table, keys)}},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "InspectionProtocol", "update", crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			if err := m.openProtocol(ctx, crud.Str(rec["company_code"]), crud.Str(rec["protocol_id"])); err != nil {
				return err
			}
			return validate(ctx, rec, old)
		},
	}
}

// nextLine: nächste Zeilennummer im Protokoll.
func (m *Module) nextLine(ctx context.Context, table, cc, id string) (int64, error) {
	v, err := m.one(ctx, "SELECT COALESCE(MAX(line_no), 0) FROM inspection__"+table+" WHERE company_code = ? AND protocol_id = ?", cc, id)
	return toInt(v) + 1, err
}

func (m *Module) participant() *crud.Entity {
	return m.lineEntity("InspectionParticipant", "Teilnehmer", "participant", []string{"line_no"}, "company_code, protocol_id, line_no", []crud.Field{
		{Key: "line_no", Label: "Zeile", Type: tNum, ReadOnly: true},
		{Key: "role", Label: "Rolle", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("InspectionRole")},
		{Key: "partner_id", Label: "Geschäftspartner", Short: "Partner", Type: tText, Lookup: partnerLookup},
		{Key: "name", Label: "Name (leer = Name des Partners)", Short: "Name", Type: tText, Listable: true},
		{Key: "present", Label: "Anwesend", Type: tBool, Listable: true},
		{Key: "signs", Label: "Unterschreibt (leer = laut Rolle)", Short: "Unterschreibt", Type: tBool, Listable: true},
		{Key: "note", Label: "Bemerkung (z. B. Vollmacht)", Short: "Bemerkung", Type: tText},
	}, func(ctx context.Context, rec, old crud.Record) error {
		cc, id := crud.Str(rec["company_code"]), crud.Str(rec["protocol_id"])
		rec["role"] = trimUpper(rec["role"])
		res, err := m.db.Query(ctx, "SELECT signs FROM inspection__participant_role WHERE company_code = ? AND code = ? AND is_active = ?", cc, rec["role"], true)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Rolle %q gibt es nicht – Einstellungen → Rollen der Teilnehmer", crud.Str(rec["role"]))
		}
		if old == nil {
			n, err := m.nextLine(ctx, "participant", cc, id)
			if err != nil {
				return err
			}
			rec["line_no"] = n
			if rec["signs"] == nil || crud.Str(rec["signs"]) == "" {
				rec["signs"] = crud.AsBool(res.Rows[0][0])
			}
			if rec["present"] == nil || crud.Str(rec["present"]) == "" {
				rec["present"] = true
			}
		}
		rec["partner_id"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["partner_id"])))
		if strings.TrimSpace(crud.Str(rec["name"])) == "" {
			rec["name"] = m.partnerName(ctx, crud.Str(rec["partner_id"]))
		}
		if strings.TrimSpace(crud.Str(rec["name"])) == "" {
			return crud.Invalid("Name oder Geschäftspartner fehlt")
		}
		return nil
	})
}

func (m *Module) item() *crud.Entity {
	return m.lineEntity("InspectionItem", "Räume und Bauteile", "item", []string{"line_no"}, "company_code, protocol_id, sort_order, line_no", []crud.Field{
		{Key: "line_no", Label: "Zeile", Type: tNum, ReadOnly: true},
		{Key: "room_name", Label: "Raum", Type: tText, Required: true, Listable: true},
		{Key: "room_no", Label: "Raum im Raumbuch", Short: "Raum-Nr.", Type: tText},
		{Key: "component_name", Label: "Bauteil / Ausstattung (leer = Raum allgemein)", Short: "Bauteil", Type: tText, Listable: true},
		{Key: "component_type", Label: "Bauteilart im Raumbuch", Short: "Bauteilart", Type: tText},
		{Key: "description", Label: "Beschreibung (aus dem Raumbuch)", Short: "Beschreibung", Type: tText, Listable: true},
		{Key: "condition", Label: "Zustand", Type: tText, Listable: true, Lookup: catalogLookup("InspectionCondition")},
		{Key: "remark", Label: "Feststellung (z. B. Kratzer an der Tür, Dübellöcher)", Short: "Feststellung", Type: tText, Listable: true, Wide: true},
		{Key: "is_defect", Label: "Mangel", Type: tBool, Listable: true, ReadOnly: true},
		{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
	}, func(ctx context.Context, rec, old crud.Record) error {
		cc, id := crud.Str(rec["company_code"]), crud.Str(rec["protocol_id"])
		if old == nil {
			n, err := m.nextLine(ctx, "item", cc, id)
			if err != nil {
				return err
			}
			rec["line_no"] = n
			if toInt(rec["sort_order"]) == 0 {
				rec["sort_order"] = n * 10
			}
		}
		rec["condition"] = nilIfEmpty(trimUpper(rec["condition"]))
		rec["is_defect"] = false
		if c := crud.Str(rec["condition"]); c != "" {
			res, err := m.db.Query(ctx, "SELECT is_defect FROM inspection__condition WHERE company_code = ? AND code = ? AND is_active = ?", cc, c, true)
			if err != nil {
				return err
			}
			if len(res.Rows) == 0 {
				return crud.Invalid("Zustand %q gibt es nicht – Einstellungen → Zustände", c)
			}
			rec["is_defect"] = crud.AsBool(res.Rows[0][0])
		}
		return nil
	})
}

func (m *Module) check() *crud.Entity {
	return m.lineEntity("InspectionCheck", "Schlüssel und Prüfliste", "check", []string{"item_code"}, "company_code, protocol_id, sort_order, item_code", []crud.Field{
		{Key: "item_code", Label: "Prüfpunkt (Schlüssel)", Short: "Punkt", Type: tText, Required: true, Immutable: true},
		{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
		{Key: "kind", Label: "Erfassung", Type: tSel, Options: kindOptions},
		{Key: "value", Label: "Ergebnis (ja/nein, Anzahl oder Text)", Short: "Ergebnis", Type: tText, Listable: true},
		{Key: "hint", Label: "Hinweis", Type: tText, ReadOnly: true},
		{Key: "note", Label: "Bemerkung", Type: tText, Listable: true},
		{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
	}, func(ctx context.Context, rec, old crud.Record) error {
		rec["item_code"] = trimUpper(rec["item_code"])
		if crud.Str(rec["kind"]) == "" {
			rec["kind"] = "CHECK"
		}
		v := strings.TrimSpace(crud.Str(rec["value"]))
		switch crud.Str(rec["kind"]) {
		case "CHECK":
			switch strings.ToLower(v) {
			case "":
			case "ja", "j", "x", "true", "1", "yes":
				v = "ja"
			case "nein", "n", "false", "0", "no":
				v = "nein"
			default:
				return crud.Invalid("%s: ja oder nein", crud.Str(rec["name"]))
			}
		case "COUNT":
			if v != "" {
				var n int
				if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 0 {
					return crud.Invalid("%s: Anzahl (ganze Zahl)", crud.Str(rec["name"]))
				}
				v = fmt.Sprint(n)
			}
		}
		rec["value"] = nilIfEmpty(v)
		return nil
	})
}

func (m *Module) meter() *crud.Entity {
	return m.lineEntity("InspectionMeter", "Zählerstände", "meter", []string{"point_id", "register_code"}, "company_code, protocol_id, sort_order, point_id, register_code", []crud.Field{
		{Key: "point_id", Label: "Messpunkt", Type: tText, Required: true, Listable: true, Immutable: true},
		{Key: "register_code", Label: "Zählwerk", Type: tText, Required: true, Immutable: true},
		{Key: "designation", Label: "Zähler", Type: tText, Required: true, Listable: true},
		{Key: "equipment_id", Label: "Gerät (technisches Objekt)", Short: "Gerät", Type: tText},
		{Key: "meter_number", Label: "Zählernummer", Short: "Zähler-Nr.", Type: tText, Listable: true},
		{Key: "location", Label: "Lage", Type: tText},
		{Key: "unit", Label: "Einheit", Type: tText},
		{Key: "last_value", Label: "Letzter bekannter Stand", Short: "Letzter Stand", Type: tNum, Listable: true, ReadOnly: true},
		{Key: "last_read_on", Label: "Letzte Ablesung am", Short: "Letzte am", Type: tDate, ReadOnly: true},
		{Key: "value", Label: "Stand bei der Übergabe", Short: "Stand", Type: tNum, Listable: true},
		{Key: "note", Label: "Bemerkung", Type: tText},
		{Key: "reading_written", Label: "Als Ablesung gebucht", Short: "Gebucht", Type: tBool, ReadOnly: true, Listable: true},
		{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
	}, func(ctx context.Context, rec, old crud.Record) error {
		if old != nil && crud.AsBool(old["reading_written"]) && toFloat(rec["value"]) != toFloat(old["value"]) {
			return crud.Invalid("Stand ist schon als Ablesung gebucht – Korrektur in den technischen Objekten")
		}
		if v := crud.Str(rec["value"]); v != "" && toFloat(v) < 0 {
			return crud.Invalid("Stand darf nicht negativ sein")
		}
		return nil
	})
}

// removeLine: Zeile eines offenen Protokolls entfernen (id <BuKr>|<Protokoll>|<Schlüssel …>).
func (m *Module) removeLine(table string, keys []string) func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in recordInput
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		parts := strings.Split(in.ID, "|")
		if len(parts) != 2+len(keys) {
			return sdk.Response{}, crud.Invalid("Zeile: id <BuKr>|<Protokoll>|%s", strings.Join(keys, "|"))
		}
		cc, id := parts[0], parts[1]
		if err := requireWrite(ctx, "InspectionProtocol", "update", cc); err != nil {
			return sdk.Response{}, err
		}
		if err := m.openProtocol(ctx, cc, id); err != nil {
			return sdk.Response{}, err
		}
		where := "company_code = ? AND protocol_id = ?"
		args := []any{cc, id}
		for i, k := range keys {
			where += " AND " + k + " = ?"
			args = append(args, parts[2+i])
		}
		res, err := m.db.Exec(ctx, "DELETE FROM inspection__"+table+" WHERE "+where, args...)
		if err != nil {
			return sdk.Response{}, err
		}
		if res.RowsAffected == 0 {
			return sdk.Response{}, fmt.Errorf("%w: Zeile %s", sdk.ErrNotFound, in.ID)
		}
		return sdk.Response{Payload: map[string]any{"message": "Zeile entfernt"}}, nil
	}
}

// deleteProtocol: offenes Protokoll mit allen Zeilen löschen.
func (m *Module) deleteProtocol(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, ok := strings.Cut(in.ID, "|")
	if !ok {
		return sdk.Response{}, crud.Invalid("Protokoll: id <BuKr>|<Nummer>")
	}
	if err := requireWrite(ctx, "InspectionProtocol", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	if err := m.openProtocol(ctx, cc, id); err != nil {
		return sdk.Response{}, err
	}
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, t := range []string{"participant", "item", "check", "meter", "protocol"} {
			if _, err := m.db.Exec(ctx, "DELETE FROM inspection__"+t+" WHERE company_code = ? AND protocol_id = ?", cc, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Protokoll " + id + " gelöscht"}}, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }
