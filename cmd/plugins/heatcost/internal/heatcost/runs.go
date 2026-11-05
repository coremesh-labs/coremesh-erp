package heatcost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Status einer Abrechnung: Entwurf (Kosten und Werte erfassen), gerechnet
// (Ergebnis aktuell), festgeschrieben (unveränderlich; Korrektur = neue Abrechnung).
const (
	runDraft    = "DRAFT"
	runComputed = "COMPUTED"
	runFinal    = "FINAL"
)

var runStatusOptions = []metamodel.Option{
	{Value: runDraft, Label: "Entwurf"}, {Value: runComputed, Label: "gerechnet"}, {Value: runFinal, Label: "festgeschrieben"},
}

var valueSourceOptions = []metamodel.Option{
	{Value: srcManual, Label: "Eingabe"}, {Value: srcArea, Label: "Fläche"}, {Value: "METER", Label: "Zähler"},
}

func runMatch() map[string]string {
	return map[string]string{"company_code": "company_code", "run_id": "run_id"}
}

func (m *Module) run() *crud.Entity {
	fields := []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "run_id", Label: "Abrechnung", Short: "Nr.", Type: tText, Listable: true, ReadOnly: true},
		{Key: "unit_id", Label: "Abrechnungseinheit", Short: "Einheit", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: unitLookup},
		{Key: "title", Label: "Bezeichnung (leer = Einheit und Zeitraum)", Short: "Bezeichnung", Type: tText, Listable: true, Wide: true},
		{Key: "period_from", Label: "Abrechnungszeitraum von", Short: "von", Type: tDate, Required: true, Listable: true},
		{Key: "period_to", Label: "Abrechnungszeitraum bis", Short: "bis", Type: tDate, Required: true, Listable: true},
		{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: runStatusOptions},
		{Key: "predecessor", Label: "Korrektur der Abrechnung", Short: "Korrektur von", Type: tText, Listable: true, ReadOnly: true, Lookup: runLookup},
		{Key: "ww_energy", Label: "Energie für Warmwasser (z. B. kWh der Wärmezähler Warmwasser)", Short: "Energie WW", Type: tNum, Group: "Warmwasser"},
		{Key: "total_energy", Label: "Gesamtenergie (z. B. kWh laut Brennstoffrechnung)", Short: "Energie gesamt", Type: tNum, Group: "Warmwasser"},
		{Key: "ww_share", Label: "Warmwasseranteil in % (gerechnet)", Short: "WW-Anteil %", Type: tNum, ReadOnly: true, Group: "Warmwasser"},
		{Key: "note", Label: "Bemerkung", Type: tArea},
	}
	fields = append(fields, blockFields()...)
	fields = append(fields,
		crud.Field{Key: "total_cost", Label: "Gesamtkosten", Short: "Kosten", Type: tNum, Listable: true, ReadOnly: true, Group: "Ergebnis"},
		crud.Field{Key: "heat_cost", Label: "davon Heizung", Type: tNum, ReadOnly: true, Group: "Ergebnis"},
		crud.Field{Key: "ww_cost", Label: "davon Warmwasser", Type: tNum, ReadOnly: true, Group: "Ergebnis"},
		crud.Field{Key: "message", Label: "Ergebnis der letzten Rechnung", Short: "Ergebnis", Type: tArea, ReadOnly: true, Group: "Ergebnis"},
		crud.Field{Key: "computed_at", Label: "Gerechnet am", Type: tText, ReadOnly: true, Group: "Ergebnis"},
		crud.Field{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
		crud.Field{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
		crud.Field{Key: "st_user", Label: "Nutzer (Nutzernummer; leer = alle)", Short: "Nutzer", Type: tText, ActionOnly: true},
		crud.Field{Key: "co_title", Label: "Bezeichnung der Korrektur (leer = „Korrektur …“)", Short: "Bezeichnung", Type: tText, ActionOnly: true})
	return &crud.Entity{
		Object: "HeatCostRun", Title: "Abrechnungen", Icon: "icon-file-text", Table: "heatcost__run", Section: "Heizkosten",
		Keys: []string{"company_code", "run_id"}, Order: "company_code, period_to DESC, run_id DESC", TitleField: "title",
		Filters: []string{"company_code", "unit_id", "status"}, Search: []string{"run_id", "title", "note"},
		Events: true, CompanyCodeField: "company_code", EventFields: []string{"unit_id", "status"},
		Fields: fields,
		Sections: []metamodel.SectionDefinition{
			{Key: "kosten", Title: "Kostenaufstellung", Relation: &metamodel.Relation{Object: "HeatCostLine", ForeignKey: "run_id", Match: runMatch(),
				Columns: []string{"line_no", "cost_group", "text", "amount", "allocation", "key_code", "scope", "ww_percent", "user_no"}}},
			{Key: "basis", Title: "Verteilungsgrößen der Liegenschaft", Relation: &metamodel.Relation{Object: "HeatCostBasis", ForeignKey: "run_id", Match: runMatch(),
				Columns: []string{"key_code", "scope", "total", "note"}}},
			{Key: "werte", Title: "Werte der Nutzer", Relation: &metamodel.Relation{Object: "HeatCostValue", ForeignKey: "run_id", Match: runMatch(),
				Columns: []string{"user_no", "key_code", "value", "source", "note"}}},
			{Key: "rechenweg", Title: "Verteilung der Gesamtkosten (Rechenweg)", Relation: &metamodel.Relation{Object: "HeatCostStep", ForeignKey: "run_id", Match: runMatch(),
				Columns: []string{"step_no", "text", "amount", "basis", "unit", "price"}}},
			{Key: "ergebnis", Title: "Ergebnis je Nutzer", Relation: &metamodel.Relation{Object: "HeatCostResult", ForeignKey: "run_id", Match: runMatch(),
				Columns: []string{"user_no", "text", "price", "value", "unit", "share", "amount"}}},
			{Key: "dokumente", Title: "Belege und Dokumente", Documents: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "fetch", Label: "Werte übernehmen (Zähler, Flächen)", Icon: "icon-inbox", Record: true}, Handle: m.fetchAction},
			{ActionConfig: metamodel.ActionConfig{Name: "compute", Label: "Rechnen", Icon: "icon-sliders", Record: true}, Handle: m.computeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "statement", Label: "Einzelabrechnung anzeigen …", Icon: "icon-eye", Record: true, Fields: []string{"st_user"}}, Handle: m.statementAction},
			{ActionConfig: metamodel.ActionConfig{Name: "correct", Label: "Korrektur anlegen …", Icon: "icon-edit", Record: true, Fields: []string{"co_title"}}, Handle: m.correctAction},
			{ActionConfig: metamodel.ActionConfig{Name: "finalize", Label: "Festschreiben", Record: true,
				Confirm: "Abrechnung festschreiben? Danach sind Kosten und Werte nicht mehr änderbar; Korrekturen gehen nur als neue Abrechnung."}, Handle: m.finalizeAction},
			{ActionConfig: metamodel.ActionConfig{Name: "delete", Label: "Löschen", Record: true,
				Confirm: "Abrechnung mit Kosten, Werten und Ergebnis endgültig löschen?"}, Handle: m.deleteAction},
		},
		Access:      access(),
		CheckRecord: checkWrite("HeatCostRun"),
		Prepare:     m.prepareRun,
		Validate:    m.checkRun,
	}
}

// prepareRun (vor der Transaktion): Nummer HK-<Jahr>-0001.
func (m *Module) prepareRun(ctx context.Context, rec crud.Record) error {
	cc := strings.TrimSpace(crud.Str(rec["company_code"]))
	to, err := crud.ParseDate(rec["period_to"])
	if cc == "" || err != nil || to == "" {
		return nil // Pflichtprüfung meldet es
	}
	prefix := "HK-" + to[:4] + "-"
	last, err := m.one(ctx, "SELECT MAX(run_id) FROM heatcost__run WHERE company_code = ? AND run_id LIKE ?", cc, prefix+"%")
	if err != nil {
		return err
	}
	n := 0
	if last != "" {
		fmt.Sscanf(strings.TrimPrefix(last, prefix), "%d", &n)
	}
	rec["run_id"], rec["status"] = fmt.Sprintf("%s%04d", prefix, n+1), runDraft
	return nil
}

func (m *Module) checkRun(ctx context.Context, rec, old crud.Record) error {
	cc, unit := crud.Str(rec["company_code"]), crud.Str(rec["unit_id"])
	if old != nil && crud.Str(old["status"]) == runFinal {
		return crud.Invalid("Abrechnung %s ist festgeschrieben – Korrektur anlegen", crud.Str(old["run_id"]))
	}
	var err error
	for _, k := range []string{"period_from", "period_to"} {
		if rec[k], err = crud.ParseDate(rec[k]); err != nil {
			return err
		}
	}
	if crud.Str(rec["period_from"]) > crud.Str(rec["period_to"]) {
		return crud.Invalid("Abrechnungszeitraum: von %s liegt nach bis %s", germanDate(crud.Str(rec["period_from"])), germanDate(crud.Str(rec["period_to"])))
	}
	res, err := m.db.Query(ctx, "SELECT designation, "+strings.Join(blockKeys, ", ")+" FROM heatcost__unit WHERE company_code = ? AND unit_id = ?", cc, unit)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Abrechnungseinheit %s gibt es im Buchungskreis %s nicht", unit, cc)
	}
	if old == nil { // Einstellungen der Einheit übernehmen, soweit nicht angegeben
		for i, k := range blockKeys {
			if !isSet(rec[k]) {
				rec[k] = res.Rows[0][i+1]
			}
		}
	}
	if strings.TrimSpace(crud.Str(rec["title"])) == "" {
		rec["title"] = fmt.Sprintf("Heizkostenabrechnung %s–%s %s", germanDate(crud.Str(rec["period_from"])), germanDate(crud.Str(rec["period_to"])), crud.Str(res.Rows[0][0]))
	}
	for _, k := range []string{"ww_energy", "total_energy"} {
		if !isSet(rec[k]) {
			rec[k] = nil
		} else if toFloat(rec[k]) < 0 {
			return crud.Invalid("Energie darf nicht negativ sein")
		}
	}
	if err := m.checkBlocks(ctx, cc, unit, rec); err != nil {
		return err
	}
	if old != nil && crud.Str(old["status"]) == runComputed {
		rec["status"] = runDraft
	}
	m.stamp(ctx, rec)
	return nil
}

// runHead: Kopf einer Zeile der Abrechnung.
func runHead() []crud.Field {
	return []crud.Field{
		{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
		{Key: "run_id", Label: "Abrechnung", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: runLookup},
	}
}

// openRun: Abrechnung vorhanden und nicht festgeschrieben; die Einheit.
func (m *Module) openRun(ctx context.Context, cc, id string) (string, error) {
	res, err := m.db.Query(ctx, "SELECT status, unit_id FROM heatcost__run WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return "", crud.Invalid("Abrechnung %s gibt es nicht", id)
	}
	if crud.Str(res.Rows[0][0]) == runFinal {
		return "", crud.Invalid("Abrechnung %s ist festgeschrieben – Korrektur anlegen", id)
	}
	return crud.Str(res.Rows[0][1]), nil
}

// touch: Änderung an Kosten oder Werten – die Abrechnung muss neu gerechnet werden.
func (m *Module) touch(ctx context.Context, cc, id string) error {
	_, err := m.db.Exec(ctx, "UPDATE heatcost__run SET status = ? WHERE company_code = ? AND run_id = ? AND status = ?", runDraft, cc, id, runComputed)
	return err
}

// lineEntity: änderbare Zeile einer offenen Abrechnung.
func (m *Module) lineEntity(object, title, table string, keys []string, order string, columns []string, fields []crud.Field,
	validate func(ctx context.Context, unit string, rec, old crud.Record) error) *crud.Entity {
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-list", Table: "heatcost__" + table, Section: "Heizkosten",
		Keys: append([]string{"company_code", "run_id"}, keys...), Order: order,
		Filters: append([]string{"company_code", "run_id"}, columns...),
		Fields:  append(runHead(), fields...),
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "remove", Label: "Entfernen", Record: true,
			Confirm: "Zeile aus der Abrechnung entfernen?"}, Handle: m.removeLine(table, keys)}},
		Access: access(),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "HeatCostRun", "update", crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			cc, id := crud.Str(rec["company_code"]), crud.Str(rec["run_id"])
			unit, err := m.openRun(ctx, cc, id)
			if err != nil {
				return err
			}
			if err := validate(ctx, unit, rec, old); err != nil {
				return err
			}
			return m.touch(ctx, cc, id)
		},
	}
}

// removeLine: Zeile entfernen (id = BuKr|Abrechnung|Schlüssel…).
func (m *Module) removeLine(table string, keys []string) func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in recordInput
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		parts := strings.Split(in.ID, "|")
		if len(parts) != 2+len(keys) {
			return sdk.Response{}, crud.Invalid("Zeile: id <BuKr>|<Abrechnung>|%s", strings.Join(keys, "|"))
		}
		cc, id := parts[0], parts[1]
		if err := requireWrite(ctx, "HeatCostRun", "update", cc); err != nil {
			return sdk.Response{}, err
		}
		err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
			if _, err := m.openRun(ctx, cc, id); err != nil {
				return err
			}
			where := "company_code = ? AND run_id = ?"
			args := []any{cc, id}
			for i, k := range keys {
				where += " AND " + k + " = ?"
				args = append(args, parts[2+i])
			}
			if _, err := m.db.Exec(ctx, "DELETE FROM heatcost__"+table+" WHERE "+where, args...); err != nil {
				return err
			}
			return m.touch(ctx, cc, id)
		})
		if err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: map[string]any{"message": "Zeile entfernt"}}, nil
	}
}

func (m *Module) costLine() *crud.Entity {
	return m.lineEntity("HeatCostLine", "Kosten", "cost", []string{"line_no"}, "company_code, run_id, sort_order, line_no",
		[]string{"cost_group", "allocation"}, []crud.Field{
			{Key: "line_no", Label: "Zeile", Type: tNum, ReadOnly: true, Listable: true},
			{Key: "cost_kind", Label: "Kostenart", Short: "Art", Type: tText, Required: true, Listable: true, Lookup: catalogLookup("HeatCostKind")},
			{Key: "text", Label: "Ausweistext (leer = Text der Kostenart)", Short: "Text", Type: tText, Listable: true, Wide: true},
			{Key: "amount", Label: "Betrag in € (Gutschrift negativ)", Short: "Betrag", Type: tNum, Required: true, Listable: true},
			{Key: "cost_group", Label: "Kostengruppe (leer = laut Kostenart)", Short: "Gruppe", Type: tText, Listable: true, Lookup: catalogLookup("HeatCostGroup")},
			{Key: "allocation", Label: "Aufteilung (leer = laut Kostenart)", Short: "Aufteilung", Type: tSel, Listable: true, Options: allocationOptions},
			{Key: "key_code", Label: "Schlüssel (leer = laut Kostenart)", Short: "Schlüssel", Type: tText, Listable: true, Lookup: catalogLookup("HeatCostKey")},
			{Key: "scope", Label: "Nur Nutzergruppe (leer = ganze Liegenschaft)", Short: "Gruppe/Bereich", Type: tText, Listable: true},
			{Key: "ww_percent", Label: "Warmwasseranteil in % (leer = laut Kostenart bzw. Abrechnung)", Short: "WW %", Type: tNum, Listable: true},
			{Key: "user_no", Label: "Nutzer (bei direkter Zuordnung)", Short: "Nutzer", Type: tText, Listable: true},
			{Key: "invoice_ref", Label: "Beleg (z. B. Rechnung vom 31.12.2025, 377.702 kWh)", Short: "Beleg", Type: tText},
			{Key: "sort_order", Label: "Reihenfolge", Short: "Reihenf.", Type: tNum},
			{Key: "note", Label: "Bemerkung", Type: tText},
		}, m.checkCostLine)
}

func (m *Module) checkCostLine(ctx context.Context, unit string, rec, old crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["run_id"])
	rec["cost_kind"] = trimUpper(rec["cost_kind"])
	res, err := m.db.Query(ctx, `SELECT name, COALESCE(text, ''), COALESCE(cost_group, ''), allocation, COALESCE(key_code, ''), ww_percent
		FROM heatcost__cost_kind WHERE company_code = ? AND code = ? AND is_active = ?`, cc, rec["cost_kind"], true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Kostenart %q gibt es nicht – Einstellungen → Kostenarten", crud.Str(rec["cost_kind"]))
	}
	k := res.Rows[0]
	if strings.TrimSpace(crud.Str(rec["text"])) == "" {
		if t := crud.Str(k[1]); t != "" {
			rec["text"] = t
		} else {
			rec["text"] = k[0]
		}
	}
	for field, i := range map[string]int{"cost_group": 2, "allocation": 3, "key_code": 4} {
		if v := trimUpper(rec[field]); v != "" {
			rec[field] = v
		} else {
			rec[field] = nilIfEmpty(crud.Str(k[i]))
		}
	}
	if !isSet(rec["ww_percent"]) {
		rec["ww_percent"] = k[5]
	}
	alloc := crud.Str(rec["allocation"])
	if !validOption(allocationOptions, alloc) {
		return crud.Invalid("Aufteilung %q: SPLIT, HEATING, HOTWATER, KEY oder DIRECT", alloc)
	}
	if g := crud.Str(rec["cost_group"]); g != "" {
		if ok, err := m.exists(ctx, "cost_group", cc, g); err != nil {
			return err
		} else if !ok {
			return crud.Invalid("Kostengruppe %s gibt es nicht", g)
		}
	}
	if alloc == allocKey {
		key := crud.Str(rec["key_code"])
		if key == "" {
			return crud.Invalid("Aufteilung nach eigenem Schlüssel: Schlüssel fehlt")
		}
		if ok, err := m.exists(ctx, "key", cc, key); err != nil {
			return err
		} else if !ok {
			return crud.Invalid("Schlüssel %s gibt es nicht", key)
		}
	} else {
		rec["key_code"] = nil
	}
	rec["scope"] = nilIfEmpty(trimUpper(rec["scope"]))
	if s := crud.Str(rec["scope"]); s != "" {
		if alloc != allocKey {
			return crud.Invalid("Nur Nutzergruppe: nur bei Aufteilung nach eigenem Schlüssel (Heizung und Warmwasser verteilen die Einstellungen der Abrechnung)")
		}
		if err := m.checkGroup(ctx, cc, unit, s); err != nil {
			return err
		}
	}
	rec["user_no"] = nilIfEmpty(strings.TrimSpace(crud.Str(rec["user_no"])))
	if alloc == allocDirect {
		u := crud.Str(rec["user_no"])
		if u == "" {
			return crud.Invalid("Direkte Zuordnung: Nutzer fehlt")
		}
		if v, err := m.one(ctx, "SELECT 1 FROM heatcost__user WHERE company_code = ? AND unit_id = ? AND user_no = ?", cc, unit, u); err != nil {
			return err
		} else if v == "" {
			return crud.Invalid("Nutzer %s gibt es in der Abrechnungseinheit %s nicht", u, unit)
		}
	} else {
		rec["user_no"] = nil
	}
	if isSet(rec["ww_percent"]) {
		if p := toFloat(rec["ww_percent"]); p < 0 || p > 100 {
			return crud.Invalid("Warmwasseranteil %v %%: 0 bis 100", p)
		}
	} else {
		rec["ww_percent"] = nil
	}
	rec["amount"] = round2(toFloat(rec["amount"]))
	if old == nil {
		v, err := m.one(ctx, "SELECT COALESCE(MAX(line_no), 0) FROM heatcost__cost WHERE company_code = ? AND run_id = ?", cc, id)
		if err != nil {
			return err
		}
		rec["line_no"] = toInt(v) + 1
		if !isSet(rec["sort_order"]) {
			rec["sort_order"] = (toInt(v) + 1) * 10
		}
	}
	return nil
}

func (m *Module) checkGroup(ctx context.Context, cc, unit, g string) error {
	if v, err := m.one(ctx, "SELECT 1 FROM heatcost__user_group WHERE company_code = ? AND unit_id = ? AND code = ?", cc, unit, g); err != nil {
		return err
	} else if v == "" {
		return crud.Invalid("Nutzergruppe %s gibt es in der Abrechnungseinheit %s nicht", g, unit)
	}
	return nil
}

func (m *Module) checkKey(ctx context.Context, cc string, rec crud.Record) error {
	rec["key_code"] = trimUpper(rec["key_code"])
	if ok, err := m.exists(ctx, "key", cc, crud.Str(rec["key_code"])); err != nil {
		return err
	} else if !ok {
		return crud.Invalid("Schlüssel %q gibt es nicht – Einstellungen → Verteilerschlüssel", crud.Str(rec["key_code"]))
	}
	return nil
}

func (m *Module) basis() *crud.Entity {
	return m.lineEntity("HeatCostBasis", "Verteilungsgrößen", "basis", []string{"key_code", "scope"}, "company_code, run_id, key_code, scope",
		[]string{"key_code"}, []crud.Field{
			{Key: "key_code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("HeatCostKey")},
			{Key: "scope", Label: "Nutzergruppe (* bzw. leer = ganze Liegenschaft)", Short: "Gruppe", Type: tText, Listable: true, Immutable: true},
			{Key: "total", Label: "Summe aller Nutzer (laut Messdienst bzw. Liegenschaft; ohne Eintrag = Summe der erfassten Nutzer)", Short: "Summe", Type: tNum, Required: true, Listable: true},
			{Key: "note", Label: "Bemerkung (Herkunft)", Short: "Bemerkung", Type: tText, Listable: true},
		}, func(ctx context.Context, unit string, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if err := m.checkKey(ctx, cc, rec); err != nil {
				return err
			}
			s := trimUpper(rec["scope"])
			if s == "" {
				s = "*"
			}
			rec["scope"] = s
			if s != "*" {
				if err := m.checkGroup(ctx, cc, unit, s); err != nil {
					return err
				}
			}
			if toFloat(rec["total"]) < 0 {
				return crud.Invalid("Summe darf nicht negativ sein")
			}
			return nil
		})
}

func (m *Module) value() *crud.Entity {
	return m.lineEntity("HeatCostValue", "Werte der Nutzer", "value", []string{"user_no", "key_code"}, "company_code, run_id, user_no, key_code",
		[]string{"user_no", "key_code", "source"}, []crud.Field{
			{Key: "user_no", Label: "Nutzer", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "key_code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("HeatCostKey")},
			{Key: "value", Label: "Wert", Type: tNum, Required: true, Listable: true},
			{Key: "source", Label: "Herkunft (Eingabe bleibt beim Übernehmen erhalten)", Short: "Herkunft", Type: tSel, Listable: true, ReadOnly: true, Options: valueSourceOptions},
			{Key: "note", Label: "Bemerkung (z. B. Zähler, Stände)", Short: "Bemerkung", Type: tText, Listable: true},
		}, func(ctx context.Context, unit string, rec, old crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if err := m.checkKey(ctx, cc, rec); err != nil {
				return err
			}
			rec["user_no"] = strings.TrimSpace(crud.Str(rec["user_no"]))
			if v, err := m.one(ctx, "SELECT 1 FROM heatcost__user WHERE company_code = ? AND unit_id = ? AND user_no = ?", cc, unit, rec["user_no"]); err != nil {
				return err
			} else if v == "" {
				return crud.Invalid("Nutzer %s gibt es in der Abrechnungseinheit %s nicht", crud.Str(rec["user_no"]), unit)
			}
			rec["source"] = srcManual // von Hand erfasst bzw. geändert
			return nil
		})
}

// readOnlyLine: Rechenweg und Ergebnis – schreibt nur „Rechnen“.
func (m *Module) readOnlyLine(object, title, table string, keys []string, order string, filters []string, fields []crud.Field) *crud.Entity {
	return &crud.Entity{
		Object: object, Title: title, Icon: "icon-list", Table: "heatcost__" + table, Section: "Heizkosten", ReadOnly: true,
		Keys: append([]string{"company_code", "run_id"}, keys...), Order: order,
		Filters: append([]string{"company_code", "run_id"}, filters...),
		Fields:  append(runHead(), fields...),
		Access:  access(),
	}
}

func (m *Module) step() *crud.Entity {
	return m.readOnlyLine("HeatCostStep", "Rechenweg", "step", []string{"step_no"}, "company_code, run_id, step_no", []string{"block"}, []crud.Field{
		{Key: "step_no", Label: "Schritt", Type: tNum, Listable: true},
		{Key: "block", Label: "Block", Type: tText, Listable: true},
		{Key: "text", Label: "Text", Type: tText, Listable: true, Wide: true},
		{Key: "amount", Label: "Betrag", Type: tNum, Listable: true},
		{Key: "key_code", Label: "Schlüssel", Type: tText, Lookup: catalogLookup("HeatCostKey")},
		{Key: "scope", Label: "Gruppe", Type: tText},
		{Key: "basis", Label: "Verteilungsgröße", Short: "Größe", Type: tNum, Listable: true},
		{Key: "unit", Label: "Einheit", Type: tText, Listable: true},
		{Key: "price", Label: "Preis je Einheit", Short: "je Einheit", Type: tNum, Listable: true},
	})
}

func (m *Module) result() *crud.Entity {
	return m.readOnlyLine("HeatCostResult", "Ergebnis je Nutzer", "result", []string{"user_no", "line_no"}, "company_code, run_id, user_no, line_no",
		[]string{"user_no", "block", "cost_group"}, []crud.Field{
			{Key: "user_no", Label: "Nutzer", Type: tText, Listable: true},
			{Key: "line_no", Label: "Zeile", Type: tNum},
			{Key: "block", Label: "Block", Type: tText, Listable: true},
			{Key: "cost_group", Label: "Kostengruppe", Short: "Gruppe", Type: tText, Lookup: catalogLookup("HeatCostGroup")},
			{Key: "text", Label: "Text", Type: tText, Listable: true, Wide: true},
			{Key: "total", Label: "Gesamtbetrag", Short: "Gesamt", Type: tNum},
			{Key: "key_code", Label: "Schlüssel", Type: tText, Lookup: catalogLookup("HeatCostKey")},
			{Key: "scope", Label: "Gruppe", Type: tText},
			{Key: "basis", Label: "Verteilungsgröße", Short: "Größe", Type: tNum},
			{Key: "price", Label: "Preis je Einheit", Short: "je Einheit", Type: tNum, Listable: true},
			{Key: "value", Label: "Wert des Nutzers", Short: "Wert", Type: tNum, Listable: true},
			{Key: "unit", Label: "Einheit", Type: tText, Listable: true},
			{Key: "share", Label: "Anteil in %", Short: "Anteil %", Type: tNum, Listable: true},
			{Key: "amount", Label: "Betrag", Type: tNum, Listable: true},
			{Key: "apportionable", Label: "Auf Mieter umlegbar", Short: "Umlegbar", Type: tBool},
		})
}

// --- Actions der Abrechnung --------------------------------------------------------

func splitID(key string) (string, string, error) {
	cc, id, ok := strings.Cut(key, "|")
	if !ok {
		return "", "", crud.Invalid("Abrechnung: id <BuKr>|<Nummer>")
	}
	return cc, id, nil
}

func (m *Module) finalizeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostRun", "update", cc); err != nil {
		return sdk.Response{}, err
	}
	st, err := m.one(ctx, "SELECT status FROM heatcost__run WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	switch st {
	case "":
		return sdk.Response{}, fmt.Errorf("%w: Abrechnung %s", sdk.ErrNotFound, in.ID)
	case runFinal:
		return sdk.Response{}, crud.Invalid("Abrechnung %s ist schon festgeschrieben", id)
	case runDraft:
		return sdk.Response{}, crud.Invalid("Abrechnung %s ist nach der letzten Änderung nicht gerechnet – zuerst „Rechnen“", id)
	}
	if _, err := m.db.Exec(ctx, "UPDATE heatcost__run SET status = ? WHERE company_code = ? AND run_id = ?", runFinal, cc, id); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Abrechnung " + id + " festgeschrieben"}}, nil
}

func (m *Module) deleteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostRun", "delete", cc); err != nil {
		return sdk.Response{}, err
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.openRun(ctx, cc, id); err != nil {
			return err
		}
		if v, err := m.one(ctx, "SELECT run_id FROM heatcost__run WHERE company_code = ? AND predecessor = ?", cc, id); err != nil {
			return err
		} else if v != "" {
			return crud.Invalid("Abrechnung %s hat die Korrektur %s – zuerst diese löschen", id, v)
		}
		for _, t := range []string{"result", "step", "value", "basis", "cost", "run"} {
			if _, err := m.db.Exec(ctx, "DELETE FROM heatcost__"+t+" WHERE company_code = ? AND run_id = ?", cc, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Abrechnung " + id + " gelöscht"}}, nil
}

// correctAction: neue Abrechnung mit Bezug auf die alte – Kopf, Kosten,
// Verteilungsgrößen und Werte kopiert; die alte bleibt unverändert.
func (m *Module) correctAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "HeatCostRun", "create", cc); err != nil {
		return sdk.Response{}, err
	}
	head, err := m.db.Query(ctx, "SELECT period_to, title FROM heatcost__run WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(head.Rows) == 0 {
		return sdk.Response{}, fmt.Errorf("%w: Abrechnung %s", sdk.ErrNotFound, in.ID)
	}
	rec := crud.Record{"company_code": cc, "period_to": date(head.Rows[0][0])}
	if err := m.prepareRun(ctx, rec); err != nil {
		return sdk.Response{}, err
	}
	nid := crud.Str(rec["run_id"])
	title := strings.TrimSpace(crud.Str(in.Data["co_title"]))
	if title == "" {
		title = "Korrektur " + crud.Str(head.Rows[0][1])
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		cols := "unit_id, period_from, period_to, ww_energy, total_energy, note, " + strings.Join(blockKeys, ", ")
		if _, err := m.db.Exec(ctx, "INSERT INTO heatcost__run (company_code, run_id, status, title, predecessor, changed_at, changed_by, "+cols+
			") SELECT company_code, ?, ?, ?, run_id, ?, ?, "+cols+" FROM heatcost__run WHERE company_code = ? AND run_id = ?",
			nid, runDraft, title, time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID), cc, id); err != nil {
			return err
		}
		for t, c := range map[string]string{
			"cost":  "line_no, cost_kind, text, amount, allocation, cost_group, key_code, scope, user_no, invoice_ref, note, ww_percent, sort_order",
			"basis": "key_code, scope, total, note",
			"value": "user_no, key_code, value, source, note",
		} {
			if _, err := m.db.Exec(ctx, "INSERT INTO heatcost__"+t+" (company_code, run_id, "+c+") SELECT company_code, ?, "+c+
				" FROM heatcost__"+t+" WHERE company_code = ? AND run_id = ?", nid, cc, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"run_id": nid, "id": cc + "|" + nid,
		"message": fmt.Sprintf("Korrektur %s zu %s angelegt – Kosten (z. B. Gutschriften) und Werte anpassen, dann „Rechnen“", nid, id)}}, nil
}
