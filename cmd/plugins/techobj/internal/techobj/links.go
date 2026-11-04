package techobj

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Brücke zu Objekten anderer Module ------------------------------------------------

// Die Brücke verbindet einen Messpunkt, eine Lage oder ein technisches Objekt
// mit einem Objekt eines anderen Moduls in einer Rolle, mit Zeitscheibe. Welche
// Objekte verknüpfbar sind (Object des Dienstes) und welche Rollen es gibt,
// steht in den Katalogen; geprüft wird über <Object>.get des zuständigen Moduls –
// ohne Fremdschlüssel, das Modul kennt kein anderes fest.

// sourceTable: Tabelle und ID-Spalte eines Objects dieses Moduls mit eigener ID
// (Schlüssel Buchungskreis + ID) – aus der Beschreibung des Objects, nicht fest.
func (m *Module) sourceTable(object string) ([2]string, bool) {
	e := m.set.Entity(object)
	if e == nil || len(e.Keys) != 2 || e.Keys[0] != "company_code" || e.TimeSlice {
		return [2]string{}, false
	}
	return [2]string{e.Table, e.Keys[1]}, true
}

func (m *Module) link() *crud.Entity {
	return &crud.Entity{
		Object: "TechLink", Title: "Verknüpfungen", Icon: "icon-link", Table: "techobj__link", Section: "Bestand",
		Keys: []string{"company_code", "source_kind", "source_id", "role_code", "object_type", "object_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, source_kind, source_id, role_code, valid_from",
		Filters: []string{"company_code", "source_kind", "source_id", "role_code", "object_type", "object_id", "object"},
		FilterExpr: map[string]func(v any) (string, []any){
			// Verknüpfungen auf ein Object eines anderen Moduls (seit 0.5.0) – das andere
			// Modul kennt die Schlüssel des Katalogs „Verknüpfbare Objekte“ nicht.
			"object": func(v any) (string, []any) {
				return `object_type IN (SELECT o.code FROM techobj__link_object o WHERE o.company_code = techobj__link.company_code AND o.service_object = ?)`,
					[]any{strings.TrimSpace(crud.Str(v))}
			},
		},
		Events: true, CompanyCodeField: "company_code",
		EventFields: []string{"source_kind", "source_id", "role_code", "object_type", "object_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "source_kind", Label: "Verknüpft wird", Short: "Quelle", Type: tSel, Required: true, Listable: true, Immutable: true, Trigger: true},
			crud.Field{Key: "source_id", Label: "Messpunkt / Lage / technisches Objekt", Short: "Quelle-ID", Type: tSel, Required: true, Listable: true, Immutable: true,
				Lookup: sourceLookup()},
			crud.Field{Key: "source_info", Label: "Gerät, Nummer und letzter Stand", Short: "Gerät / Stand", Type: tText, Virtual: true, ReadOnly: true, Listable: true},
			crud.Field{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechLinkRole")},
			crud.Field{Key: "object_type", Label: "Objekt (Art)", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechLinkObject"), Trigger: true},
			crud.Field{Key: "object_id", Label: "Objekt", Type: tSel, Required: true, Listable: true, Immutable: true, Lookup: targetLookup()},
			crud.Field{Key: "object", Label: "Object des anderen Moduls (z. B. RentObject)", Type: tText, FilterOnly: true},
			crud.Field{Key: "note", Label: "Bemerkung (z. B. Kundennummer)", Short: "Bemerkung", Type: tText, Listable: true},
		),
		Access:      access(),
		CheckRecord: checkWrite("TechLink"),
		Validate:    m.checkLink,
		FormState:   m.linkFormState,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			cc := crud.Str(rec["company_code"])
			if l := m.targetLabel(ctx, cc, crud.Str(rec["object_type"]), crud.Str(rec["object_id"])); l != "" {
				setLabel(rec, "object_id", l)
			}
			if n, _ := m.one(ctx, "SELECT name FROM techobj__link_source WHERE company_code = ? AND source_object = ?", cc, rec["source_kind"]); n != "" {
				setLabel(rec, "source_kind", n)
			}
			if t, ok := m.sourceTable(crud.Str(rec["source_kind"])); ok {
				if d, _ := m.one(ctx, "SELECT designation FROM "+t[0]+" WHERE company_code = ? AND "+t[1]+" = ?", cc, rec["source_id"]); d != "" {
					setLabel(rec, "source_id", d)
				}
			}
			info, err := m.sourceInfo(ctx, cc, crud.Str(rec["source_kind"]), crud.Str(rec["source_id"]))
			if err != nil {
				return err
			}
			rec["source_info"] = info
			return nil
		},
	}
}

func (m *Module) checkLink(ctx context.Context, rec, _ crud.Record) error {
	cc, kind := crud.Str(rec["company_code"]), strings.TrimSpace(crud.Str(rec["source_kind"]))
	src, obj := strings.TrimSpace(crud.Str(rec["source_id"])), strings.TrimSpace(crud.Str(rec["object_id"]))
	role, typ := trimUpper(rec["role_code"]), trimUpper(rec["object_type"])
	rec["source_kind"], rec["source_id"], rec["role_code"], rec["object_type"], rec["object_id"] = kind, src, role, typ, obj
	from, to := date(rec["valid_from"]), date(rec["valid_to"])
	if v, err := m.one(ctx, "SELECT 1 FROM techobj__link_source WHERE company_code = ? AND source_object = ? AND is_active = ?", cc, kind, true); err != nil {
		return err
	} else if v == "" {
		return crud.Invalid("%s ist nicht verknüpfbar – Einstellungen → Verknüpfbar aus diesem Modul", kind)
	}
	t, ok := m.sourceTable(kind)
	if !ok {
		return crud.Invalid("%s ist kein Object dieses Moduls mit eigener ID", kind)
	}
	if v, err := m.one(ctx, "SELECT 1 FROM "+t[0]+" WHERE company_code = ? AND "+t[1]+" = ?", cc, src); err != nil {
		return err
	} else if v == "" {
		return crud.Invalid("%s gibt es im Buchungskreis %s nicht", src, cc)
	}
	res, err := m.db.Query(ctx, "SELECT name, COALESCE(object_types, ''), is_exclusive FROM techobj__link_role WHERE company_code = ? AND code = ? AND is_active = ?", cc, role, true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Rolle %s gibt es im Buchungskreis %s nicht (oder inaktiv) – Einstellungen → Rollen der Verknüpfung", role, cc)
	}
	roleName, allowed, exclusive := crud.Str(res.Rows[0][0]), splitCodes(crud.Str(res.Rows[0][1])), crud.AsBool(res.Rows[0][2])
	if len(allowed) > 0 && !slices.Contains(allowed, typ) {
		return crud.Invalid("Rolle %s erlaubt nur %s", roleName, strings.Join(allowed, ", "))
	}
	if _, err := m.target(ctx, cc, typ, obj); err != nil {
		return err
	}
	if exclusive {
		r, err := m.overlap(ctx, "techobj__link", "object_id", `company_code = ? AND source_kind = ? AND source_id = ? AND role_code = ?
			AND NOT (object_type = ? AND object_id = ? AND valid_from = ?)`, from, to, cc, kind, src, role, typ, obj, from)
		if err != nil {
			return err
		}
		if r != nil {
			return crud.Invalid("%s an %s ist %s–%s bereits %s – dort erst beenden (Wechsel = neue Zeitscheibe)", roleName, src,
				germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
		}
	}
	return nil
}

// target liest das verknüpfte Objekt über <Object>.get des zuständigen Moduls.
func (m *Module) target(ctx context.Context, cc, typ, id string) (map[string]any, error) {
	res, err := m.db.Query(ctx, "SELECT name, service_object, company_scoped FROM techobj__link_object WHERE company_code = ? AND code = ? AND is_active = ?", cc, typ, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Verknüpfbares Objekt %s gibt es im Buchungskreis %s nicht (oder inaktiv) – Einstellungen → Verknüpfbare Objekte", typ, cc)
	}
	name, object, scoped := crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), crud.AsBool(res.Rows[0][2])
	key := id
	if scoped {
		key = cc + "|" + id
	}
	resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": key})
	if err != nil {
		if errors.Is(err, sdk.ErrNotFound) {
			return nil, crud.Invalid("%s %s gibt es im Buchungskreis %s nicht", name, id, cc)
		}
		return nil, crud.Invalid("%s %s: %v", name, id, err)
	}
	var out map[string]any
	_ = sdk.Decode(resp.Payload, &out)
	return out, nil
}

// targetLabel: Bezeichnung des verknüpften Objekts (leer, wenn nicht lesbar).
func (m *Module) targetLabel(ctx context.Context, cc, typ, id string) string {
	o, err := m.target(ctx, cc, typ, id)
	if err != nil || o == nil {
		return ""
	}
	for _, k := range []string{"designation", "description"} {
		if s := strings.TrimSpace(crud.Str(o[k])); s != "" {
			return s
		}
	}
	return strings.TrimSpace(crud.Str(o["name1"]) + " " + crud.Str(o["name2"]))
}

// linkText: verknüpfte Objekte einer Rolle am Tag, z. B. „Mietobjekt 1000-01
// Whg. EG links“; beim Mietobjekt mit dem Mieter aus dem laufenden Vertrag.
func (m *Module) linkText(ctx context.Context, cc, kind, id, role, day string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT l.object_type, l.object_id, COALESCE(o.name, l.object_type), COALESCE(o.service_object, ''),
		COALESCE(o.tenant_contract_types, '') FROM techobj__link l
		LEFT JOIN techobj__link_object o ON o.company_code = l.company_code AND o.code = l.object_type
		WHERE l.company_code = ? AND l.source_kind = ? AND l.source_id = ? AND l.role_code = ? AND l.valid_from <= ? AND l.valid_to >= ?
		ORDER BY l.object_type, l.object_id`, cc, kind, id, role, day, day)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, r := range res.Rows {
		typ, obj := crud.Str(r[0]), crud.Str(r[1])
		text := crud.Str(r[2]) + " " + obj
		if l := m.targetLabel(ctx, cc, typ, obj); l != "" {
			text += " " + l
		}
		if types := splitCodes(crud.Str(r[4])); len(types) > 0 {
			if t := m.tenantOf(ctx, cc, crud.Str(r[3]), obj, day, types); t != "" {
				text += " (" + t + ")"
			}
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; "), nil
}

// tenantOf: Vertrag und Partner des am Tag laufenden Vertrags einer der
// Vertragsarten types am Objekt (Vertragsmodul: ContractObject, Contract);
// leer, wenn es das Modul nicht gibt.
func (m *Module) tenantOf(ctx context.Context, cc, objectType, object, day string, types []string) string {
	resp, err := m.services.Call(ctx, "ContractObject", "list", map[string]any{"query": map[string]any{
		"company_code": cc, "object_type": objectType, "object_id": object, "includeHistory": "true"}})
	if err != nil {
		return ""
	}
	var l struct {
		Items []struct {
			Contract string `json:"contract_id"`
			From     string `json:"valid_from"`
			To       string `json:"valid_to"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return ""
	}
	for _, it := range l.Items {
		if date(it.From) > day || date(it.To) < day {
			continue
		}
		c, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + it.Contract})
		if err != nil {
			continue
		}
		var k struct {
			Partner string `json:"partner_id"`
			Status  string `json:"status"`
			Type    string `json:"contract_type"`
		}
		if sdk.Decode(c.Payload, &k) != nil || k.Partner == "" || !slices.Contains(types, k.Type) {
			continue
		}
		return strings.TrimSpace(it.Contract + " " + m.partnerName(ctx, k.Partner))
	}
	return ""
}

// userText: verknüpfte Nutzer („misst für“: Rollen mit Kennzeichen Nutzer) am Tag.
func (m *Module) userText(ctx context.Context, cc, point, day string) (string, error) {
	res, err := m.db.Query(ctx, "SELECT code FROM techobj__link_role WHERE company_code = ? AND is_user = ? AND is_active = ? ORDER BY sort_order, code", cc, true, true)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, r := range res.Rows {
		t, err := m.linkText(ctx, cc, "MeterPoint", point, crud.Str(r[0]), day)
		if err != nil {
			return "", err
		}
		if t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "; "), nil
}

// linkFormState: Auswahl im Formular aus den Katalogen – verknüpfbare Objects
// dieses Moduls und ihre Datensätze, Objekte des anderen Moduls über dessen
// Liste (Schlüssel- und Bezeichnungsfelder aus „Verknüpfbare Objekte“).
func (m *Module) linkFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	v := func(k string) string { return strings.TrimSpace(req.Values[k]) }
	cc := v("company_code")
	if cc == "" {
		return st, nil
	}
	res, err := m.db.Query(ctx, "SELECT source_object, name FROM techobj__link_source WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code", cc, true)
	if err != nil {
		return st, err
	}
	var kinds []metamodel.Option
	for _, r := range res.Rows {
		kinds = append(kinds, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[1])})
	}
	st.Fields["source_kind"] = metamodel.FieldState{Options: kinds}
	if t, ok := m.sourceTable(v("source_kind")); ok {
		res, err := m.db.Query(ctx, "SELECT "+t[1]+", designation FROM "+t[0]+" WHERE company_code = ? ORDER BY "+t[1], cc)
		if err != nil {
			return st, err
		}
		var ids []metamodel.Option
		for _, r := range res.Rows {
			ids = append(ids, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[0]) + " " + crud.Str(r[1])})
		}
		st.Fields["source_id"] = metamodel.FieldState{Options: ids}
	}
	if typ := strings.ToUpper(v("object_type")); typ != "" {
		opts, err := m.targetOptions(ctx, cc, typ)
		if err != nil {
			return st, err
		}
		st.Fields["object_id"] = metamodel.FieldState{Options: opts}
	}
	return st, nil
}

// targetOptions: Objekte des anderen Moduls zur Auswahl (<Object>.list des Buchungskreises).
func (m *Module) targetOptions(ctx context.Context, cc, typ string) ([]metamodel.Option, error) {
	res, err := m.db.Query(ctx, `SELECT service_object, company_scoped, COALESCE(value_field, ''), COALESCE(label_fields, '')
		FROM techobj__link_object WHERE company_code = ? AND code = ? AND is_active = ?`, cc, typ, true)
	if err != nil || len(res.Rows) == 0 {
		return nil, err
	}
	object, scoped, field := crud.Str(res.Rows[0][0]), crud.AsBool(res.Rows[0][1]), crud.Str(res.Rows[0][2])
	labels := strings.Split(crud.Str(res.Rows[0][3]), ",")
	if field == "" {
		return nil, nil
	}
	q := map[string]any{"limit": 1000}
	if scoped {
		q["company_code"] = cc
	}
	resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": q})
	if err != nil {
		return nil, nil // anderes Modul nicht da oder nicht lesbar: freie Eingabe bleibt geprüft
	}
	var l struct {
		Items []map[string]any `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return nil, nil
	}
	var out []metamodel.Option
	for _, it := range l.Items {
		id := crud.Str(it[field])
		if id == "" {
			continue
		}
		var parts []string
		for _, f := range labels {
			if t := strings.TrimSpace(crud.Str(it[strings.TrimSpace(f)])); t != "" {
				parts = append(parts, t)
			}
		}
		out = append(out, metamodel.Option{Value: id, Label: strings.TrimSpace(id + " " + strings.Join(parts, " "))})
	}
	return out, nil
}

// sourceLookup (seit 0.5.0): Verweis der Quelle je Art – Messpunkt, Lage oder
// technisches Objekt dieses Moduls (Kopfdaten und Sprung in der Oberfläche).
func sourceLookup() *metamodel.Lookup {
	return &metamodel.Lookup{By: "source_kind", Variants: map[string]metamodel.Lookup{
		"MeterPoint": *pointLookup, "TechLocation": *locationLookup, "TechObject": *equipmentLookup}}
}

// targetLookup (seit 0.5.0): Verweis auf das Objekt des anderen Moduls je Objektart –
// aus den Vorschlagswerten des Katalogs „Verknüpfbare Objekte“ (das Metamodell gilt
// für alle Buchungskreise; eigene Einträge eines Buchungskreises zeigen den Text ohne Sprung).
func targetLookup() *metamodel.Lookup {
	v := map[string]metamodel.Lookup{}
	for _, row := range catalogDefaults["link_object"] {
		var labels []string
		for _, f := range strings.Split(crud.Str(row["label_fields"]), ",") {
			if f = strings.TrimSpace(f); f != "" {
				labels = append(labels, f)
			}
		}
		l := metamodel.Lookup{Object: crud.Str(row["service_object"]), ValueField: crud.Str(row["value_field"]), LabelFields: labels}
		if b, _ := row["company_scoped"].(bool); b {
			l.Filters = ccFilter
		}
		v[crud.Str(row["code"])] = l
	}
	return &metamodel.Lookup{By: "object_type", Variants: v}
}

// sourceInfo: Kurzinfo zur Quelle – beim Messpunkt das eingebaute Gerät mit
// Nummer und der letzte Stand des Hauptzählwerks, beim technischen Objekt die Nummern.
func (m *Module) sourceInfo(ctx context.Context, cc, kind, id string) (string, error) {
	switch kind {
	case "MeterPoint":
		eq, err := m.installedID(ctx, cc, id, crud.KeyDate(ctx))
		if err != nil || eq == "" {
			return "", err
		}
		out, nums, err := m.installedAt(ctx, cc, id, crud.KeyDate(ctx))
		if err != nil {
			return "", err
		}
		if nums != "" {
			out += " – " + nums
		}
		res, err := m.db.Query(ctx, `SELECT r.read_on, r.value, COALESCE(NULLIF(u.symbol, ''), u.name, g.unit, '') FROM techobj__reading r
			JOIN techobj__register g ON g.company_code = r.company_code AND g.equipment_id = r.equipment_id AND g.register_code = r.register_code
			LEFT JOIN techobj__unit u ON u.company_code = g.company_code AND u.code = g.unit
			WHERE r.company_code = ? AND r.equipment_id = ? AND NOT EXISTS (SELECT 1 FROM techobj__reading_status s WHERE s.company_code = r.company_code AND s.code = r.status AND s.is_rejected)
			ORDER BY r.read_on DESC, r.register_code LIMIT 1`, cc, eq)
		if err != nil {
			return "", err
		}
		if len(res.Rows) > 0 {
			r := res.Rows[0]
			out += ", Stand " + strings.TrimSpace(fmtNum(toFloat(r[1]))+" "+crud.Str(r[2])) + " am " + germanDate(date(r[0]))
		}
		return out, nil
	case "TechObject":
		return m.numbersText(ctx, cc, id)
	}
	return "", nil
}
