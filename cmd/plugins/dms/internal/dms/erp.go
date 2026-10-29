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
	CostCategoryField, AllocableField, FileAccountField                 string
	CanCreate                                                           bool
}

// categoryObjects: kaufmännische Objects einer Dokumentkategorie.
func (m *Module) categoryObjects(ctx context.Context, category string) ([]dtObject, error) {
	res, err := m.db.Query(ctx, `SELECT object, label, list_action, filters, partner_field, date_field, title_field, account_object, account_link,
		account_field, can_create, cost_category_field, allocable_field, file_account_field FROM dms__category_object WHERE category = ? AND is_active = ? ORDER BY sort_order, object`, category, true)
	if err != nil {
		return nil, err
	}
	out := make([]dtObject, 0, len(res.Rows))
	for _, r := range res.Rows {
		s := func(i int) string { return crud.Str(r[i]) }
		out = append(out, dtObject{Object: s(0), Label: s(1), Action: firstOf(s(2), "list"), Filters: s(3), PartnerField: s(4), DateField: s(5),
			TitleField: s(6), AccountObject: s(7), AccountLink: s(8), AccountField: s(9), CanCreate: crud.AsBool(r[10]),
			CostCategoryField: s(11), AllocableField: s(12), FileAccountField: s(13)})
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

// takeover: Partner und Titel aus dem gewählten Objekt, die Ablage aus seinen
// Positionen (Ablagefindung: Sachkonto, Kostenart, umlagefähig). Liefert, was übernommen wurde.
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
	// Partner nur, wenn das Dokument noch keinen hat (seit 0.17.1: der gewählte Partner kann
	// weiterer Vertragspartner sein, z. B. Mitmieter – er bleibt).
	if v := crud.Str(rec[o.PartnerField]); o.PartnerField != "" && v != "" && d.Partner == "" {
		set["partner_id"] = v
		notes = append(notes, "Partner "+m.partnerName(ctx, v))
	}
	// Das Dokumentdatum kommt nur aus dem Dokument (Texterkennung bzw. Frage mit
	// Tagesdatum als Vorschlag), nie aus dem Objekt.
	if v := crud.Str(rec[o.TitleField]); o.TitleField != "" && v != "" && d.Title == "" {
		set["title"] = v
		notes = append(notes, "Titel")
	}
	// seit 0.20.0: am Objekt gewählte Ablage (z. B. am Angebot) vor der Ablagefindung
	if v := crud.Str(rec[o.FileAccountField]); o.FileAccountField != "" && v != "" && d.Account == "" {
		set["file_account"] = v
		notes = append(notes, "Ablage "+v+" (am "+o.Label+" gewählt)")
	} else if o.AccountObject != "" && d.Account == "" {
		folder, why, err := m.folderFromObject(ctx, d.CC, o, rec)
		switch {
		case err != nil:
			notes = append(notes, "Ablage nicht ermittelt: "+err.Error())
		case folder != "":
			set["file_account"] = folder
			notes = append(notes, "Ablage "+folder+" ("+why+")")
		case why != "":
			notes = append(notes, "keine Ablage zu "+why+" – Ablage wählen")
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

// folderFromObject: Ablage über die Positionen des Objekts – je Position die genaueste
// Ablagefindung; Positionen ohne Treffer (z. B. Vorsteuer) zählen nicht. Mehrere Ablagen:
// die genaueste, bei Gleichstand Fehler. Liefert die Ablage und die Begründung.
func (m *Module) folderFromObject(ctx context.Context, cc string, o *dtObject, rec map[string]any) (string, string, error) {
	// Verbindungsfeld: „feld“ (gleich in beiden) oder „feld der Positionen=feld des Objekts“ (draft_id=id).
	itemField, objField, ok := strings.Cut(o.AccountLink, "=")
	if !ok {
		objField = itemField
	}
	itemField, objField = strings.TrimSpace(itemField), strings.TrimSpace(objField)
	link := crud.Str(rec[objField])
	if link == "" {
		return "", "", nil
	}
	resp, err := m.services.Call(ctx, o.AccountObject, "list", map[string]any{"query": map[string]any{"company_code": cc, itemField: link}})
	if err != nil {
		return "", "", err
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &list); err != nil {
		return "", "", err
	}
	rules, err := m.folderRules(ctx, cc)
	if err != nil {
		return "", "", err
	}
	type hit struct {
		rule  folderRule
		score int
	}
	hits := map[string]hit{}
	var seen []string
	for _, it := range list.Items {
		if crud.Str(it[itemField]) != link {
			continue
		}
		acc := crud.Str(it[o.AccountField])
		cat := ""
		if o.CostCategoryField != "" {
			cat = crud.Str(it[o.CostCategoryField])
		}
		var alloc *bool
		if o.AllocableField != "" && it[o.AllocableField] != nil {
			v := crud.AsBool(it[o.AllocableField])
			alloc = &v
		}
		if acc == "" && cat == "" {
			continue
		}
		desc := strings.Trim(strings.Join([]string{acc, cat}, " "), " ")
		if !contains(seen, desc) {
			seen = append(seen, desc)
		}
		r, n, tied := bestFolder(rules, acc, cat, alloc)
		if len(tied) > 1 {
			sort.Strings(tied)
			return "", "", fmt.Errorf("Position %s passt gleich gut zu mehreren Ablagen (%s)", desc, strings.Join(tied, ", "))
		}
		if n > 0 && n > hits[r.Folder].score {
			hits[r.Folder] = hit{r, n}
		}
	}
	if len(hits) == 0 {
		if len(seen) == 0 {
			return "", "", nil
		}
		return "", "Position " + strings.Join(seen, "; "), nil
	}
	var best []hit
	for _, h := range hits {
		switch {
		case len(best) == 0 || h.score > best[0].score:
			best = []hit{h}
		case h.score == best[0].score:
			best = append(best, h)
		}
	}
	if len(best) > 1 {
		var fs []string
		for _, h := range best {
			fs = append(fs, h.rule.Folder+" ("+h.rule.text()+")")
		}
		sort.Strings(fs)
		return "", "", fmt.Errorf("Positionen gehören zu mehreren Ablagen: %s", strings.Join(fs, ", "))
	}
	return best[0].rule.Folder, best[0].rule.text(), nil
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
