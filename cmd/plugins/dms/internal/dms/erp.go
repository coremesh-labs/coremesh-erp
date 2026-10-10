package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablauf mit angeschlossenem ERP (Workflow ERP):
//
//  1. Dokumentart wählen.
//  2. Geschäftspartner – nur, wenn die Objekte der Dokumentart einen haben
//     (Parameter when = doctype_partner), z. B. bei Rechnungen.
//  3. Kaufmännisches Objekt wählen (Parameter objects = doctype): nur die
//     Objects der Dokumentart (DmsDocTypeObject), beim Partner nur seine;
//     fehlt es, „… anlegen“ im neuen Fenster (noch nicht buchen), dann suchen.
//  4. Übernehmen (Art TAKEOVER): Partner, Datum, Titel aus dem Objekt; die
//     Ablage aus dem Sachkonto des Objekts (Sachkonten der Ablage im
//     Buchungskreis).
//  5. Ablage – nur, wenn sie noch fehlt (Ebene für Ebene).
//  6. Dokumentname bestätigen (Art NAME), dann ablegen.
//
// „Zurück“ führt zur vorigen Frage und nimmt zurück, was sie und die
// Programmschritte danach gesetzt haben (Stand je Schritt in wf_trail).

const (
	kTakeover = "TAKEOVER" // Programm: Daten aus dem kaufmännischen Objekt übernehmen
	kName     = "NAME"     // Benutzer: Dokumentname bestätigen
)

// dtObject: kaufmännisches Object einer Dokumentart.
type dtObject struct {
	Object, Label, Action, Filters, PartnerField, DateField, TitleField string
	AccountObject, AccountLink, AccountField                            string
	CanCreate                                                           bool
}

func (m *Module) docTypeObjects(ctx context.Context, docType string) ([]dtObject, error) {
	res, err := m.db.Query(ctx, `SELECT object, label, list_action, filters, partner_field, date_field, title_field, account_object, account_link,
		account_field, can_create FROM dms__doc_type_object WHERE doc_type = ? AND is_active = ? ORDER BY sort_order, object`, docType, true)
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

// byDocType: Schritt wählt die Objects der Dokumentart (Parameter objects = doctype).
func (s *stepDef) byDocType(ctx context.Context) bool { return s.param(ctx, "objects") == "doctype" }

// refObjects: Objects eines Bezug-Schritts – ref_object, die der Dokumentart oder die Quellen.
func (m *Module) refObjects(ctx context.Context, d *docRow, s *stepDef) []string {
	if s.byDocType(ctx) {
		objs, _ := m.docTypeObjects(ctx, d.DocType)
		out := make([]string, len(objs))
		for i, o := range objs {
			out[i] = o.Object
		}
		return out
	}
	return refObjectsOf(ctx, s)
}

// objectChoices: kaufmännische Objekte der Dokumentart – beim gewählten
// Partner nur seine, mit den Filtern der Dokumentart und der Suche.
func (m *Module) objectChoices(ctx context.Context, d *docRow, search string) ([]choice, error) {
	objs, err := m.docTypeObjects(ctx, d.DocType)
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, crud.Invalid("Für die Dokumentart %s sind keine kaufmännischen Objekte eingerichtet (Dokumentarten → Kaufmännische Objekte)", d.DocType)
	}
	out := []choice{{"", "– überspringen –"}}
	for _, o := range objs {
		query := map[string]any{"company_code": d.CC}
		for _, f := range strings.Split(o.Filters, ";") {
			if k, v, ok := strings.Cut(strings.TrimSpace(f), "="); ok {
				query[strings.TrimSpace(k)] = m.expand(ctx, d, strings.TrimSpace(v))
			}
		}
		if o.PartnerField != "" && d.Partner != "" {
			query[o.PartnerField] = d.Partner
		}
		if q := strings.TrimSpace(search); q != "" {
			query["q"] = q
		}
		resp, err := m.services.Call(ctx, o.Object, o.Action, map[string]any{"query": query})
		if err != nil {
			return nil, crud.Invalid("Auswahl aus %s: %v", o.Label, err)
		}
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if err := sdk.Decode(resp.Payload, &list); err != nil {
			return nil, err
		}
		for _, it := range list.Items {
			id := crud.Str(it["id"])
			if id == "" || (o.PartnerField != "" && d.Partner != "" && crud.Str(it[o.PartnerField]) != d.Partner) {
				continue
			}
			label := o.Label + ": " + shortKey(id, d.CC)
			var parts []string
			for _, f := range []string{o.TitleField, "designation", o.DateField} {
				if v := crud.Str(it[f]); f != "" && v != "" && !contains(parts, v) {
					parts = append(parts, v)
				}
			}
			if len(parts) > 0 {
				label += " – " + strings.Join(parts, " · ")
			}
			out = append(out, choice{o.Object + ":" + id, label})
		}
	}
	if len(out) > 501 {
		out = out[:501]
	}
	return out, nil
}

// createLinks: „… anlegen“ – Objects der Dokumentart mit can_create und der Parameter create.
func (m *Module) createLinks(ctx context.Context, d *docRow, s *stepDef) []metamodel.FormLink {
	var out []metamodel.FormLink
	if s.byDocType(ctx) {
		objs, _ := m.docTypeObjects(ctx, d.DocType)
		for _, o := range objs {
			if o.CanCreate {
				out = append(out, metamodel.FormLink{Label: o.Label + " anlegen", URL: "/new/" + o.Object})
			}
		}
	}
	return append(out, createLinks(ctx, s)...)
}

// stepApplies: Bedingung when des Schritts – doctype_partner: nur, wenn ein
// Object der Dokumentart einen Geschäftspartner hat.
func (m *Module) stepApplies(ctx context.Context, d *docRow, s *stepDef) (bool, string) {
	if s.byDocType(ctx) {
		if objs, _ := m.docTypeObjects(ctx, d.DocType); len(objs) == 0 {
			return false, "keine kaufmännischen Objekte für Dokumentart " + d.DocType
		}
	}
	switch s.param(ctx, "when") {
	case "doctype_partner":
		objs, _ := m.docTypeObjects(ctx, d.DocType)
		for _, o := range objs {
			if o.PartnerField != "" {
				return true, ""
			}
		}
		return false, "entfällt für Dokumentart " + d.DocType
	}
	return true, ""
}

// --- Übernahme aus dem kaufmännischen Objekt ----------------------------------------------

// takeover: Partner, Datum und Titel aus dem gewählten Objekt, die Ablage aus
// seinen Sachkonten. Liefert, was übernommen wurde.
func (m *Module) takeover(ctx context.Context, d *docRow) (string, error) {
	objs, err := m.docTypeObjects(ctx, d.DocType)
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
	if v := dateOf(rec[o.DateField]); o.DateField != "" && v != "" && v != d.DocDate {
		set["doc_date"] = v
		notes = append(notes, "Datum "+v)
	}
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
	link := crud.Str(rec[o.AccountLink])
	if link == "" {
		return "", nil, nil
	}
	resp, err := m.services.Call(ctx, o.AccountObject, "list", map[string]any{"query": map[string]any{"company_code": cc, o.AccountLink: link}})
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
		if crud.Str(it[o.AccountLink]) != link {
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
	parts := nameParts{"account": d.Account, "company": firstOf(cfg.ShortName, cfg.CC), "date": strings.ReplaceAll(d.DocDate, "-", ""),
		"year": d.DocDate[:4], "doctype": d.DocType, "doctype_name": dt.Name, "partner": m.partnerName(ctx, d.Partner), "title": d.Title}
	if len(refs) > 0 {
		parts["object"] = shortKey(refs[0].Key, d.CC)
	}
	return buildName(cfg.Pattern, parts), nil
}

// setName: bestätigter Dokumentname (ohne Endung) – enthält die Ablage, frei im Buchungskreis.
func (m *Module) setName(ctx context.Context, d *docRow, value string) (string, error) {
	v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), extOf(d.FileName)))
	if v == "" || safeFileName(v) != v {
		return "", crud.Invalid("Dokumentname %q: ohne / und \\, nicht leer", value)
	}
	if !strings.HasPrefix(v, d.Account) {
		return "", crud.Invalid("Dokumentname muss mit der Ablage %s beginnen (er beschreibt den Ort im Archiv)", d.Account)
	}
	res, err := m.db.Query(ctx, `SELECT 1 FROM dms__document WHERE company_code = ? AND name = ?`, d.CC, v+extOf(d.FileName))
	if err != nil {
		return "", err
	}
	if len(res.Rows) > 0 {
		return "", crud.Invalid("Dokumentname %s gibt es schon", v+extOf(d.FileName))
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document SET name_override = ? WHERE id = ?`, v, d.ID)
	return v, err
}

// --- Zurück --------------------------------------------------------------------------------

// trailEntry: Stand nach einem Schritt.
type trailEntry struct {
	Step  string            `json:"step"`
	User  bool              `json:"user"` // Frage beantwortet (nicht übersprungen)
	State map[string]string `json:"state"`
	Refs  []string          `json:"refs"` // Object|Schlüssel
}

// stateFields: Felder, die Schritte setzen (und „zurück“ wiederherstellt).
var stateFields = []string{"title", "doc_type", "file_account", "partner_id", "doc_date", "retention", "note", "name_override"}

func (m *Module) snapshot(ctx context.Context, docID, step string, user bool) (trailEntry, error) {
	e := trailEntry{Step: step, User: user, State: map[string]string{}}
	res, err := m.db.Query(ctx, `SELECT `+strings.Join(stateFields, ", ")+` FROM dms__document WHERE id = ?`, docID)
	if err != nil || len(res.Rows) == 0 {
		return e, err
	}
	for i, f := range stateFields {
		e.State[f] = crud.Str(res.Rows[0][i])
		if f == "doc_date" {
			e.State[f] = dateOf(res.Rows[0][i])
		}
	}
	refs, err := m.activeRefs(ctx, docID)
	if err != nil {
		return e, err
	}
	for _, r := range refs {
		e.Refs = append(e.Refs, r.Object+"|"+r.Key)
	}
	return e, nil
}

func (m *Module) trail(ctx context.Context, docID string) []trailEntry {
	res, err := m.db.Query(ctx, `SELECT wf_trail FROM dms__document WHERE id = ?`, docID)
	var out []trailEntry
	if err == nil && len(res.Rows) > 0 {
		_ = json.Unmarshal([]byte(crud.Str(res.Rows[0][0])), &out)
	}
	return out
}

func (m *Module) saveTrail(ctx context.Context, docID string, t []trailEntry) error {
	b, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document SET wf_trail = ? WHERE id = ?`, string(b), docID)
	return err
}

// pushTrail: Stand nach dem Schritt anhängen (Anfang: step leer).
func (m *Module) pushTrail(ctx context.Context, docID, step string, user bool) {
	e, err := m.snapshot(ctx, docID, step, user)
	if err == nil {
		t := m.trail(ctx, docID)
		if step == "" {
			t = nil
		}
		err = m.saveTrail(ctx, docID, append(t, e))
	}
	if err != nil {
		m.log.WarnContext(ctx, "Workflow-Stand nicht gespeichert", "document", docID, "err", err.Error())
	}
}

// backAction: zur vorigen beantworteten Frage; deren Antwort und alles danach zurücknehmen.
func (m *Module) backAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	d, err := m.writableDoc(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if d.Status != stInbox {
		return sdk.Response{}, crud.Invalid("Dokument ist schon abgelegt – zurück nicht möglich")
	}
	t := m.trail(ctx, d.ID)
	i := len(t) - 1
	for ; i > 0 && !t[i].User; i-- {
	}
	if i <= 0 {
		return sdk.Response{}, crud.Invalid("Keine vorige Frage – das ist der erste Schritt")
	}
	target, before := t[i], t[i-1]
	sets := make([]string, len(stateFields))
	args := make([]any, 0, len(stateFields)+1)
	for j, f := range stateFields {
		sets[j] = f + " = ?"
		args = append(args, nilIfEmpty(before.State[f]))
	}
	if _, err := m.db.Exec(ctx, `UPDATE dms__document SET `+strings.Join(sets, ", ")+` WHERE id = ?`, append(args, d.ID)...); err != nil {
		return sdk.Response{}, err
	}
	refs, err := m.activeRefs(ctx, d.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range refs {
		if !contains(before.Refs, r.Object+"|"+r.Key) {
			if _, err := m.db.Exec(ctx, `UPDATE dms__document_ref SET is_active = ? WHERE document_id = ? AND line_no = ?`, false, d.ID, r.Line); err != nil {
				return sdk.Response{}, err
			}
		}
	}
	if err := m.saveTrail(ctx, d.ID, t[:i]); err != nil {
		return sdk.Response{}, err
	}
	if err := m.setWorkflow(ctx, d.ID, target.Step, wfWaiting); err != nil {
		return sdk.Response{}, err
	}
	m.appendLog(ctx, d.ID, "Zurück zu Schritt "+target.Step)
	return m.workflowState(ctx, d.ID)
}
