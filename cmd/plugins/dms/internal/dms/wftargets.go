package dms

import (
	"context"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ziele der Schritte: jedes änderbare Feld des Dokuments (aus seiner Definition,
// nicht aus einer festen Liste), ein Bezug (ref, Object fest oder aus der Auswahl
// „Object:Schlüssel“) oder eine Variable (var). Geprüft wird wie in der Maske
// des Dokuments: Verweise müssen existieren, Datum lesbar, Kopf nur im Eingang
// (checkDocument).

// docTargetFields: änderbare Felder des Dokuments (Ziele der Schritte).
func (m *Module) docTargetFields() []crud.Field {
	var out []crud.Field
	for _, f := range m.document().Fields {
		if f.ReadOnly || f.Virtual || f.ActionOnly {
			continue
		}
		out = append(out, f)
	}
	return out
}

func (m *Module) docField(key string) *crud.Field {
	for _, f := range m.docTargetFields() {
		if f.Key == key {
			return &f
		}
	}
	return nil
}

// targetOptions: Auswahl „Setzt Feld“ der Schritte.
func (m *Module) targetOptions() []metamodel.Option {
	var out []metamodel.Option
	for _, f := range m.docTargetFields() {
		out = append(out, metamodel.Option{Value: f.Key, Label: f.Label})
	}
	return append(out, metamodel.Option{Value: "ref", Label: "Bezug (Object fest oder aus der Auswahl)"},
		metamodel.Option{Value: "var", Label: "Variable für spätere Schritte ({var.<Name>})"})
}

// stateFields: Felder, die Schritte setzen und „zurück“ wiederherstellt.
func (m *Module) stateFields() []string {
	var out []string
	for _, f := range m.docTargetFields() {
		out = append(out, f.Key)
	}
	return out
}

// fieldObject: Object hinter einem Verweisfeld (Ablage, Dokumentart, Partner …) oder "".
func fieldObject(f *crud.Field) string {
	switch {
	case f.Ref != nil:
		return f.Ref.Object
	case f.Lookup != nil:
		return f.Lookup.Object
	}
	return ""
}

// targetValue: aktueller Wert des Ziels (Feld, Bezug, Variable); "" = leer.
func (m *Module) targetValue(ctx context.Context, d *docRow, key string) string {
	t, arg := splitTarget(key)
	switch t {
	case "":
		return ""
	case "var":
		return m.varText(ctx, d.ID, arg)
	case "ref":
		refs, _ := m.activeRefs(ctx, d.ID)
		for _, r := range refs {
			if arg == "" {
				if m.isCategoryObject(ctx, d, r.Object) {
					return r.Object + ":" + r.Key
				}
				continue
			}
			if r.Object == arg {
				return r.Key
			}
		}
		return ""
	}
	return m.docValues(ctx, d.ID)[t]
}

// isCategoryObject: Object gehört zur Kategorie des Dokuments (Bezug ohne festes Object).
func (m *Module) isCategoryObject(ctx context.Context, d *docRow, object string) bool {
	objs, _ := m.docObjects(ctx, d)
	for _, o := range objs {
		if o.Object == object {
			return true
		}
	}
	return false
}

// setTargetValue prüft den Wert und setzt das Ziel (Feld, Bezug, Variable mit Bezeichnung label).
// Liefert den gespeicherten Wert.
func (m *Module) setTargetValue(ctx context.Context, d *docRow, key, value, label string) (string, error) {
	value = strings.TrimSpace(value)
	t, arg := splitTarget(key)
	switch t {
	case "":
		return value, nil
	case "var":
		if arg == "" {
			return "", crud.Invalid("Variable ohne Namen")
		}
		if err := m.setWfVar(ctx, d.ID, "var."+arg, nilIfEmpty(value)); err != nil {
			return "", err
		}
		return value, m.setWfVar(ctx, d.ID, "var."+arg+"_label", nilIfEmpty(firstOf(label, value)))
	case "ref":
		object := arg
		if object == "" {
			o, k, ok := strings.Cut(value, ":")
			if !ok {
				return "", crud.Invalid("Bezug %q: Object:Schlüssel erwartet", value)
			}
			object, value = o, k
		}
		if value == "" {
			return "", crud.Invalid("Bezug %s: Schlüssel fehlt", object)
		}
		v, err := m.checkParamValue(ctx, d.CC, object, value)
		if err != nil {
			return "", err
		}
		return v, m.insertRef(ctx, d, object, v)
	}
	return m.setDocField(ctx, d, t, value)
}

// setDocField: Feld des Dokuments – geprüft wie in der Maske (Verweis, Datum, Kopf, Kategorie).
func (m *Module) setDocField(ctx context.Context, d *docRow, key, value string) (string, error) {
	f := m.docField(key)
	if f == nil {
		return "", crud.Invalid("Feld %q des Dokuments gibt es nicht oder ist nicht änderbar", key)
	}
	if value == "" {
		return "", crud.Invalid("%s: Wert fehlt", f.Label)
	}
	switch {
	case f.Type == tDate:
		v, err := parseDate(value, "")
		if err != nil {
			return "", err
		}
		value = v
	case f.Type == tNum:
		if _, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64); err != nil {
			return "", crud.Invalid("%s: Zahl erwartet", f.Label)
		}
	case fieldObject(f) != "":
		v, err := m.checkParamValue(ctx, d.CC, fieldObject(f), value)
		if err != nil {
			return "", err
		}
		value = v
	}
	rec := crud.Record{}
	for k, v := range m.docValues(ctx, d.ID) {
		rec[k] = v
	}
	old := crud.Record{}
	for k, v := range rec {
		old[k] = v
	}
	rec[key] = value
	if err := m.checkDocument(ctx, rec, old); err != nil {
		return "", err
	}
	value = crud.Str(rec[key])
	// checkDocument ergänzt z. B. die Kategorie der Dokumentart.
	sets, args := []string{key + " = ?"}, []any{value}
	if key == "doc_type" && crud.Str(rec["doc_category"]) != crud.Str(old["doc_category"]) {
		sets, args = append(sets, "doc_category = ?"), append(args, rec["doc_category"])
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, d.ID)...); err != nil {
		return "", err
	}
	return value, nil
}

// targetLabel: lesbare Bezeichnung des Ziels.
func (m *Module) targetLabel(key string) string {
	t, arg := splitTarget(key)
	switch t {
	case "ref":
		if arg == "" {
			return "Bezug"
		}
		return "Bezug " + arg
	case "var":
		return "Variable " + arg
	}
	if f := m.docField(t); f != nil {
		return f.Label
	}
	return key
}

// insertRef: Bezug anlegen (schon aktiv vorhanden: nichts).
func (m *Module) insertRef(ctx context.Context, d *docRow, object, key string) error {
	res, err := m.db.Query(ctx, `SELECT COALESCE(MAX(line_no), 0) + 1, SUM(CASE WHEN object = ? AND object_key = ? AND is_active = ? THEN 1 ELSE 0 END)
		FROM dms__document_ref WHERE document_id = ?`, object, key, true, d.ID)
	if err != nil {
		return err
	}
	if toInt(res.Rows[0][1]) > 0 {
		return nil
	}
	_, err = m.db.Exec(ctx, `INSERT INTO dms__document_ref (document_id, line_no, company_code, object, object_key, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, toInt(res.Rows[0][0]), d.CC, object, key, true)
	return err
}
