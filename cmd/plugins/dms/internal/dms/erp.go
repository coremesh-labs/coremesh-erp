package dms

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Kaufmännische Objekte der Dokumentkategorie (DmsCategoryObject): Auswahl im
// Workflow (Quelle CATEGORY), Übernahme von Partner, Titel und Ablage aus dem
// gewählten Objekt (Aktion DmsDocument.takeover) und der Dokumentname nach dem
// Muster des Buchungskreises.

// dtObject: kaufmännisches Object einer Dokumentart.
type dtObject struct {
	Object, Label, Action, Filters, PartnerField, DateField, TitleField string
	AccountObject, AccountLink, AccountField                            string
	CanCreate                                                           bool
}

// categoryObjects: kaufmännische Objects einer Dokumentkategorie.
func (m *Module) categoryObjects(ctx context.Context, category string) ([]dtObject, error) {
	res, err := m.db.Query(ctx, `SELECT object, label, list_action, filters, partner_field, date_field, title_field, account_object, account_link,
		account_field, can_create FROM dms__category_object WHERE category = ? AND is_active = ? ORDER BY sort_order, object`, category, true)
	if err != nil {
		return nil, err
	}
	out := make([]dtObject, 0, len(res.Rows))
	for _, r := range res.Rows {
		s := func(i int) string { return crud.Str(r[i]) }
		out = append(out, dtObject{Object: s(0), Label: s(1), Action: firstOf(s(2), "list"), Filters: s(3), PartnerField: s(4), DateField: s(5),
			TitleField: s(6), AccountObject: s(7), AccountLink: s(8), AccountField: s(9), CanCreate: crud.AsBool(r[10])})
	}
	return out, nil
}

// categoryOf: Dokumentkategorie des Dokuments – gewählt oder die der Dokumentart.
func (m *Module) categoryOf(ctx context.Context, d *docRow) string {
	if d.Category != "" || d.DocType == "" {
		return d.Category
	}
	res, err := m.db.Query(ctx, `SELECT category FROM dms__doc_type WHERE code = ?`, d.DocType)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}

// docObjects: Objects der Kategorie des Dokuments.
func (m *Module) docObjects(ctx context.Context, d *docRow) ([]dtObject, error) {
	return m.categoryObjects(ctx, m.categoryOf(ctx, d))
}

// --- Übernahme aus dem kaufmännischen Objekt ----------------------------------------------

// takeover: Partner und Titel aus dem gewählten Objekt, die Ablage aus
// seinen Sachkonten. Liefert, was übernommen wurde.
func (m *Module) takeover(ctx context.Context, d *docRow) (string, error) {
	objs, err := m.docObjects(ctx, d)
	if err != nil {
		return "", err
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return "", err
	}
	var o *dtObject
	var key string
	for _, r := range refs {
		for i := range objs {
			if objs[i].Object == r.Object && o == nil {
				o, key = &objs[i], r.Key
			}
		}
	}
	if o == nil {
		return "", crud.Invalid("kein kaufmännisches Objekt gewählt")
	}
	resp, err := m.services.Call(ctx, o.Object, "get", map[string]any{"id": key})
	if err != nil {
		return "", crud.Invalid("%s %s nicht lesbar: %v", o.Label, key, err)
	}
	var rec map[string]any
	if err := sdk.Decode(resp.Payload, &rec); err != nil {
		return "", err
	}
	set := map[string]string{}
	var notes []string
	if v := crud.Str(rec[o.PartnerField]); o.PartnerField != "" && v != "" && v != d.Partner {
		set["partner_id"] = v
		notes = append(notes, "Partner "+m.partnerName(ctx, v))
	}
	// Das Dokumentdatum kommt nur aus dem Dokument (Texterkennung bzw. Frage mit
	// Tagesdatum als Vorschlag), nie aus dem Objekt.
	if v := crud.Str(rec[o.TitleField]); o.TitleField != "" && v != "" && d.Title == "" {
		set["title"] = v
		notes = append(notes, "Titel")
	}
	if o.AccountObject != "" && d.Account == "" {
		folder, accounts, err := m.folderFromObject(ctx, d.CC, o, rec)
		switch {
		case err != nil:
			notes = append(notes, "Ablage nicht ermittelt: "+err.Error())
		case folder != "":
			set["file_account"] = folder
			notes = append(notes, "Ablage "+folder+" (Sachkonto "+strings.Join(accounts, ", ")+")")
		case len(accounts) > 0:
			notes = append(notes, "keine Ablage zu Sachkonto "+strings.Join(accounts, ", ")+" – Ablage wählen")
		}
	}
	for k, v := range set {
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+k+` = ? WHERE id = ?`, v, d.ID); err != nil {
			return "", err
		}
	}
	if len(notes) == 0 {
		return "nichts zu übernehmen", nil
	}
	return strings.Join(notes, ", "), nil
}

// folderFromObject: Ablage über die Sachkonten des Objekts – eindeutig oder keine.
func (m *Module) folderFromObject(ctx context.Context, cc string, o *dtObject, rec map[string]any) (string, []string, error) {
	// Verbindungsfeld: „feld“ (gleich in beiden) oder „feld der Positionen=feld des Objekts“ (draft_id=id).
	itemField, objField, ok := strings.Cut(o.AccountLink, "=")
	if !ok {
		objField = itemField
	}
	itemField, objField = strings.TrimSpace(itemField), strings.TrimSpace(objField)
	link := crud.Str(rec[objField])
	if link == "" {
		return "", nil, nil
	}
	resp, err := m.services.Call(ctx, o.AccountObject, "list", map[string]any{"query": map[string]any{"company_code": cc, itemField: link}})
	if err != nil {
		return "", nil, err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &list); err != nil {
		return "", nil, err
	}
	var accounts []string
	folders := map[string]bool{}
	for _, it := range list.Items {
		if crud.Str(it[itemField]) != link {
			continue
		}
		a := crud.Str(it[o.AccountField])
		if a == "" || contains(accounts, a) {
			continue
		}
		accounts = append(accounts, a)
		f, err := m.folderOfAccount(ctx, cc, a)
		if err != nil {
			return "", nil, err
		}
		if f != "" {
			folders[f] = true
		}
	}
	if len(folders) == 1 {
		for f := range folders {
			return f, accounts, nil
		}
	}
	if len(folders) > 1 {
		var fs []string
		for f := range folders {
			fs = append(fs, f)
		}
		sort.Strings(fs)
		return "", accounts, fmt.Errorf("Sachkonten %s gehören zu mehreren Ablagen (%s)", strings.Join(accounts, ", "), strings.Join(fs, ", "))
	}
	return "", accounts, nil
}

// --- Dokumentname bestätigen ------------------------------------------------------------

// proposeName: Dokumentname nach dem Muster (ohne Endung) – wie beim Ablegen.
func (m *Module) proposeName(ctx context.Context, d *docRow) (string, error) {
	var missing []string
	for _, f := range [][2]string{{d.Account, "Ablage"}, {d.DocType, "Dokumentart"}, {d.DocDate, "Dokumentdatum"}} {
		if f[0] == "" {
			missing = append(missing, f[1])
		}
	}
	if len(missing) > 0 {
		return "", crud.Invalid("Für den Dokumentnamen fehlt: %s", strings.Join(missing, ", "))
	}
	cfg, err := m.companyConfig(ctx, d.CC)
	if err != nil {
		return "", err
	}
	dt, err := m.docTypeOf(ctx, d.DocType)
	if err != nil {
		return "", err
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return "", err
	}
	account, err := m.nameAccount(ctx, cfg, d.Account)
	if err != nil {
		return "", err
	}
	parts := nameParts{"account": account, "company": firstOf(cfg.ShortName, cfg.CC), "date": strings.ReplaceAll(d.DocDate, "-", ""),
		"year": d.DocDate[:4], "doctype": dt.Short, "doctype_name": dt.Name, "partner": m.partnerFilePart(ctx, d.Partner), "title": d.Title,
		"category": m.categoryOf(ctx, d)}
	parts["property"] = m.propertyPart(ctx, cfg, d)
	if len(refs) > 0 {
		parts["object"] = shortKey(refs[0].Key, d.CC)
	}
	if parts["object"] == parts["property"] {
		delete(parts, "object") // Bezug ist das Objekt selbst: nicht doppelt
	}
	return buildName(cfg.Pattern, parts), nil
}

// setName: bestätigter Dokumentname (ohne Endung) – enthält die Ablage, frei im Buchungskreis.
// checkName: Dokumentname (ohne Endung) – enthält die Ablage, frei im Buchungskreis.
func (m *Module) checkName(ctx context.Context, d *docRow, value string) (string, error) {
	v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), extOf(d.FileName)))
	if v == "" || safeFileName(v) != v {
		return "", crud.Invalid("Dokumentname %q: ohne / und \\, nicht leer", value)
	}
	cfg, err := m.companyConfig(ctx, d.CC)
	if err != nil {
		return "", err
	}
	account, err := m.nameAccount(ctx, cfg, d.Account)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(v, account) {
		return "", crud.Invalid("Dokumentname muss mit der Ablage %s beginnen (er beschreibt den Ort im Archiv)", account)
	}
	res, err := m.db.Query(ctx, `SELECT 1 FROM dms__document WHERE company_code = ? AND name = ?`, d.CC, v+extOf(d.FileName))
	if err != nil {
		return "", err
	}
	if len(res.Rows) > 0 {
		return "", crud.Invalid("Dokumentname %s gibt es schon", v+extOf(d.FileName))
	}
	return v, nil
}
