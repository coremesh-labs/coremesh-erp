package export

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

var runStatus = []metamodel.Option{{Value: runOpen, Label: "offen (nachbessern)"}, {Value: runExported, Label: "exportiert"}}

var lookupDocument = &metamodel.Lookup{Object: "DmsDocument", ValueField: "id", LabelFields: []string{"title", "doc_date"},
	Filters: map[string]string{"company_code": "company_code"}}

// run: Lauf einer Sicht (entsteht über „Zusammenstellen“).
func (m *Module) run() *crud.Entity {
	return &crud.Entity{
		Object: runObject, Title: "Läufe", Icon: "icon-list", Table: "export__run", Section: "Sichten", ReadOnly: true,
		Keys: []string{"company_code", "run_id"}, Order: "company_code, run_id DESC", TitleField: "name",
		Filters: []string{"company_code", "view_id", "status"}, Search: []string{"run_id", "name", "view_id"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "run_id", Label: "Lauf", Type: tText, Listable: true, ReadOnly: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Listable: true, ReadOnly: true},
			{Key: "view_id", Label: "Sicht", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogViewLookup},
			{Key: "view_type", Label: "Sichtart", Type: tText, ReadOnly: true, Lookup: catalogLookup(viewTypeObject)},
			{Key: "key_date", Label: "Stichtag", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: runStatus},
			{Key: "item_count", Label: "Dokumente", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "excluded_count", Label: "Ausgeschlossen", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "total_size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true},
			{Key: "params", Label: "Angaben der Sicht (beim Zusammenstellen)", Type: tArea, ReadOnly: true},
			{Key: "message", Label: "Hinweise", Type: tArea, ReadOnly: true},
			{Key: "created_at", Label: "Zusammengestellt am", Type: tText, ReadOnly: true},
			{Key: "created_by", Label: "Zusammengestellt von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "target", Label: "Ziel", Type: tText, Listable: true, ReadOnly: true, Lookup: catalogLookup(targetObject)},
			{Key: "exported_at", Label: "Exportiert am", Type: tText, Listable: true, ReadOnly: true},
			{Key: "exported_by", Label: "Exportiert von", Type: tText, ReadOnly: true, Lookup: crud.LookupUser},
			{Key: "export_path", Label: "Ausgegeben nach", Type: tText, ReadOnly: true},
			{Key: "manifest_sha256", Label: "Prüfsumme des Manifests (SHA-256)", Type: tText, ReadOnly: true},
			{Key: "log", Label: "Protokoll", Type: tArea, ReadOnly: true},
			// Formularfelder der Aktionen
			{Key: "export_target", Label: "Ziel", Type: tText, ActionOnly: true, Required: true, Lookup: catalogLookup(targetObject)},
			{Key: "add_document", Label: "Dokument (abgelegt)", Type: tText, ActionOnly: true, Lookup: lookupDocument},
			{Key: "add_reason", Label: "Bemerkung (z. B. warum zusätzlich)", Type: tText, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{{Key: "dokumente", Title: "Dokumente", Relation: &metamodel.Relation{Object: itemObject, ForeignKey: "run_id",
			Match: match("run_id", "run_id"), Columns: []string{"item_no", "folder", "file_name", "doc_date", "partner_name", "excluded", "reason"}}}},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "addDocument", Label: "Dokument hinzunehmen …", Record: true, Fields: []string{"add_document", "add_reason"}},
				Handle: m.addDocumentAction},
			{ActionConfig: metamodel.ActionConfig{Name: "export", Label: "Exportieren …", Icon: "icon-download", Record: true, Fields: []string{"export_target"}, FormState: true},
				Handle: m.exportAction},
		},
		FormState: m.runFormState,
		Access:    &crud.Access{Object: runObject, Records: true, CompanyCode: "company_code"},
	}
}

var catalogViewLookup = &metamodel.Lookup{Object: viewObject, ValueField: "view_id", LabelFields: []string{"name"},
	Filters: map[string]string{"company_code": "company_code"}}

// item: Dokument eines Laufs.
func (m *Module) item() *crud.Entity {
	return &crud.Entity{
		Object: itemObject, Title: "Dokumente eines Laufs", Icon: "icon-file", Table: "export__item", Section: "Sichten", ReadOnly: true,
		Keys: []string{"company_code", "run_id", "item_no"}, Order: "company_code, run_id, item_no", TitleField: "file_name",
		Filters: []string{"company_code", "run_id", "excluded"}, Search: []string{"file_name", "title", "partner_name"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, ReadOnly: true, Lookup: lookupCC},
			{Key: "run_id", Label: "Lauf", Type: tText, Listable: true, ReadOnly: true},
			{Key: "item_no", Label: "Nr.", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "folder", Label: "Ordner", Type: tText, Listable: true, ReadOnly: true},
			{Key: "file_name", Label: "Datei", Type: tText, Listable: true, ReadOnly: true},
			{Key: "title", Label: "Titel", Type: tText, ReadOnly: true},
			{Key: "doc_date", Label: "Belegdatum", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "doc_type", Label: "Dokumentart", Type: tText, ReadOnly: true, Lookup: lookupDocType},
			{Key: "doc_category", Label: "Kategorie", Type: tText, ReadOnly: true, Lookup: lookupCategory},
			{Key: "partner_id", Label: "Partner", Type: tText, ReadOnly: true, Lookup: lookupPartner},
			{Key: "partner_name", Label: "Partner (Name)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "file_account", Label: "Ablage", Type: tText, ReadOnly: true, Lookup: lookupFileAccount},
			{Key: "document_id", Label: "Dokument", Type: tText, ReadOnly: true, Lookup: lookupDocument},
			{Key: "rule_no", Label: "Regel", Type: tNum, ReadOnly: true},
			{Key: "size", Label: "Größe (Bytes)", Type: tNum, ReadOnly: true},
			{Key: "sha256", Label: "Prüfsumme (SHA-256)", Type: tText, ReadOnly: true},
			{Key: "added", Label: "Hinzugenommen", Type: tBool, ReadOnly: true},
			{Key: "excluded", Label: "Ausgeschlossen", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "reason", Label: "Bemerkung / Grund", Type: tText, Listable: true, ReadOnly: true},
			{Key: "exclude_reason", Label: "Grund (steht im Inhaltsverzeichnis)", Type: tText, ActionOnly: true, Required: true},
		},
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "exclude", Label: "Ausschließen …", Record: true, Fields: []string{"exclude_reason"}}, Handle: m.excludeAction(true)},
			{ActionConfig: metamodel.ActionConfig{Name: "include", Label: "Wieder aufnehmen", Record: true}, Handle: m.excludeAction(false)},
		},
		Access: &crud.Access{Object: itemObject, Records: true, CompanyCode: "company_code"},
	}
}

// openRun: Lauf zum Ändern (Status offen).
func (m *Module) openRun(ctx context.Context, cc, id string) (status string, err error) {
	r, err := m.one(ctx, "SELECT status FROM export__run WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return "", err
	}
	if r == nil {
		return "", fmt.Errorf("%w: Lauf %s|%s", sdk.ErrNotFound, cc, id)
	}
	return crud.Str(r[0]), nil
}

func (m *Module) recount(ctx context.Context, cc, id string) error {
	_, err := m.db.Exec(ctx, `UPDATE export__run SET
		item_count = (SELECT COUNT(*) FROM export__item i WHERE i.company_code = export__run.company_code AND i.run_id = export__run.run_id AND i.excluded = ?),
		excluded_count = (SELECT COUNT(*) FROM export__item i WHERE i.company_code = export__run.company_code AND i.run_id = export__run.run_id AND i.excluded = ?),
		total_size = (SELECT COALESCE(SUM(size), 0) FROM export__item i WHERE i.company_code = export__run.company_code AND i.run_id = export__run.run_id AND i.excluded = ?)
		WHERE company_code = ? AND run_id = ?`, false, true, false, cc, id)
	return err
}

func (m *Module) excludeAction(exclude bool) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		var in recordInput
		if err := sdk.Decode(req.Payload, &in); err != nil {
			return sdk.Response{}, err
		}
		parts := strings.Split(in.ID, "|")
		if len(parts) != 3 {
			return sdk.Response{}, crud.Invalid("Dokument eines Laufs %q: Buchungskreis|Lauf|Nr. erwartet", in.ID)
		}
		cc, id := parts[0], parts[1]
		no, _ := strconv.Atoi(parts[2])
		if err := requireWrite(ctx, itemObject, "update", cc); err != nil {
			return sdk.Response{}, err
		}
		reason := strings.TrimSpace(crud.Str(in.Data["exclude_reason"]))
		if exclude && reason == "" {
			return sdk.Response{}, crud.Invalid("Bitte einen Grund angeben – er steht im Inhaltsverzeichnis")
		}
		st, err := m.openRun(ctx, cc, id)
		if err != nil {
			return sdk.Response{}, err
		}
		if st != runOpen {
			return sdk.Response{}, crud.Invalid("Lauf %s ist exportiert – für Änderungen die Sicht neu zusammenstellen", id)
		}
		err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
			q := "UPDATE export__item SET excluded = ?, reason = ? WHERE company_code = ? AND run_id = ? AND item_no = ?"
			args := []any{exclude, nilIfEmpty(reason), cc, id, no}
			if !exclude {
				q = "UPDATE export__item SET excluded = ?, reason = CASE WHEN added THEN reason ELSE NULL END WHERE company_code = ? AND run_id = ? AND item_no = ?"
				args = []any{false, cc, id, no}
			}
			res, err := m.db.Exec(ctx, q, args...)
			if err != nil {
				return err
			}
			if res.RowsAffected == 0 {
				return fmt.Errorf("%w: Dokument %d im Lauf %s", sdk.ErrNotFound, no, id)
			}
			return m.recount(ctx, cc, id)
		})
		if err != nil {
			return sdk.Response{}, err
		}
		msg := fmt.Sprintf("Dokument %d wieder aufgenommen", no)
		if exclude {
			msg = fmt.Sprintf("Dokument %d ausgeschlossen: %s", no, reason)
		}
		return sdk.Response{Payload: map[string]any{"message": msg}}, nil
	}
}

// addDocumentAction: ExportRun.addDocument {id, data: {add_document, add_reason}}.
func (m *Module) addDocumentAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Lauf")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, itemObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	docID := strings.TrimSpace(crud.Str(in.Data["add_document"]))
	if docID == "" {
		return sdk.Response{}, crud.Invalid("Bitte ein Dokument wählen")
	}
	st, err := m.openRun(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if st != runOpen {
		return sdk.Response{}, crud.Invalid("Lauf %s ist exportiert – für Änderungen die Sicht neu zusammenstellen", id)
	}
	resp, err := m.services.Call(ctx, "DmsDocument", "get", map[string]any{"id": docID})
	if err != nil {
		return sdk.Response{}, err
	}
	var r map[string]any
	_ = sdk.Decode(resp.Payload, &r)
	d := docOf(r)
	if crud.Str(r["company_code"]) != cc {
		return sdk.Response{}, crud.Invalid("Dokument %s gehört zum Buchungskreis %s, nicht %s", d.Title, crud.Str(r["company_code"]), cc)
	}
	if crud.Str(r["status"]) != "FILED" {
		return sdk.Response{}, crud.Invalid("Dokument %s ist noch nicht abgelegt", d.Title)
	}
	if x, err := m.one(ctx, "SELECT item_no, excluded FROM export__item WHERE company_code = ? AND run_id = ? AND document_id = ?", cc, id, d.ID); err != nil {
		return sdk.Response{}, err
	} else if x != nil {
		if crud.AsBool(x[1]) {
			return sdk.Response{}, crud.Invalid("Das Dokument steht als Nr. %d ausgeschlossen im Lauf – dort „Wieder aufnehmen“", toInt(x[0]))
		}
		return sdk.Response{}, crud.Invalid("Das Dokument steht schon als Nr. %d im Lauf", toInt(x[0]))
	}
	t, err := m.one(ctx, `SELECT COALESCE(t.folder_pattern, ''), COALESCE(t.file_pattern, '') FROM export__run r
		JOIN export__view_type t ON t.company_code = r.company_code AND t.code = r.view_type WHERE r.company_code = ? AND r.run_id = ?`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	var folderP, fileP string
	if t != nil {
		folderP, fileP = crud.Str(t[0]), crud.Str(t[1])
	}
	res, err := m.db.Query(ctx, "SELECT item_no, COALESCE(folder, ''), file_name FROM export__item WHERE company_code = ? AND run_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	used, next := map[string]bool{}, 1
	for _, x := range res.Rows {
		used[strings.ToLower(crud.Str(x[1])+"/"+crud.Str(x[2]))] = true
		if n := toInt(x[0]); n >= next {
			next = n + 1
		}
	}
	d.folderPattern, d.ruleName = folderP, "hinzugenommen"
	folder, file := m.newNamer(ctx, cc).place(fileP, &d, next, used)
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.insertItem(ctx, cc, id, next, d, folder, file, true, strings.TrimSpace(crud.Str(in.Data["add_reason"]))); err != nil {
			return err
		}
		return m.recount(ctx, cc, id)
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Als Nr. %d hinzugenommen: %s", next, file)}}, nil
}

// runFormState: Ziel aus der Sicht vorschlagen.
func (m *Module) runFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	cc, id, err := splitKey(req.ID, "Lauf")
	if err != nil || req.Action != "export" {
		return st, nil
	}
	r, err := m.one(ctx, `SELECT COALESCE(v.target, ''), r.item_count, r.total_size FROM export__run r
		LEFT JOIN export__view v ON v.company_code = r.company_code AND v.view_id = r.view_id WHERE r.company_code = ? AND r.run_id = ?`, cc, id)
	if err != nil || r == nil {
		return st, err
	}
	if t := crud.Str(r[0]); t != "" {
		st.Fields["export_target"] = metamodel.FieldState{Value: &t}
	}
	st.Message = fmt.Sprintf("%d Dokumente, %s", toInt(r[1]), germanSize(int64(toInt(r[2]))))
	return st, nil
}

// exportAction: ExportRun.export {id, data: {export_target}}.
func (m *Module) exportAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := splitKey(in.ID, "Lauf")
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, runObject, "export", cc); err != nil {
		return sdk.Response{}, err
	}
	tgt := trimUpper(in.Data["export_target"])
	t, err := m.one(ctx, "SELECT name, kind, COALESCE(path, '') FROM export__target WHERE company_code = ? AND code = ? AND is_active = ?", cc, tgt, true)
	if err != nil {
		return sdk.Response{}, err
	}
	if t == nil {
		return sdk.Response{}, crud.Invalid("Ziel %s gibt es im Buchungskreis %s nicht", tgt, cc)
	}
	r, err := m.one(ctx, `SELECT name, view_id, COALESCE(view_type, ''), key_date, COALESCE(params, ''), created_at, COALESCE(created_by, ''), COALESCE(log, '')
		FROM export__run WHERE company_code = ? AND run_id = ?`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if r == nil {
		return sdk.Response{}, fmt.Errorf("%w: Lauf %s|%s", sdk.ErrNotFound, cc, id)
	}
	mf := manifest{Run: id, Name: crud.Str(r[0]), View: crud.Str(r[1]), ViewType: crud.Str(r[2]), Company: cc, KeyDate: dateStr(r[3]),
		Created: crud.Str(r[5]), By: crud.Str(r[6])}
	var params map[string]string
	if json.Unmarshal([]byte(crud.Str(r[4])), &params) == nil {
		for k, v := range params {
			if v == "" {
				delete(params, k)
			}
		}
		mf.Params = params
	}
	res, err := m.db.Query(ctx, `SELECT item_no, COALESCE(folder, ''), file_name, doc_date, COALESCE(doc_type, ''), COALESCE(doc_category, ''),
		COALESCE(partner_name, ''), COALESCE(file_account, ''), COALESCE(title, ''), COALESCE(sha256, ''), COALESCE(size, 0), document_id,
		COALESCE(rule_no, 0), added, COALESCE(reason, ''), excluded FROM export__item WHERE company_code = ? AND run_id = ? ORDER BY item_no`, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	nm := m.newNamer(ctx, cc)
	for _, x := range res.Rows {
		it := outItem{No: toInt(x[0]), Folder: crud.Str(x[1]), File: crud.Str(x[2]), Date: dateStr(x[3]), DocType: nm.docType(crud.Str(x[4])),
			Category: nm.category(crud.Str(x[5])), Partner: crud.Str(x[6]), Account: crud.Str(x[7]), Title: crud.Str(x[8]), SHA: crud.Str(x[9]),
			Size: int64(toInt(x[10])), Document: crud.Str(x[11]), Rule: toInt(x[12]), Added: crud.AsBool(x[13]), Reason: crud.Str(x[14])}
		if crud.AsBool(x[15]) {
			mf.Excluded = append(mf.Excluded, it)
		} else {
			mf.Items = append(mf.Items, it)
		}
	}
	if len(mf.Items) == 0 {
		return sdk.Response{}, crud.Invalid("Lauf %s hat keine Dokumente", id)
	}
	where, download, sum, err := m.writeRun(ctx, mf, crud.Str(t[1]), crud.Str(t[2]))
	if err != nil {
		return sdk.Response{}, err
	}
	shown := where
	if download != nil {
		shown = "Download " + download.FileName
	}
	line := fmt.Sprintf("%s %s: %d Dokumente → %s (%s), Manifest %s", m.stamp(), userOf(ctx), len(mf.Items), crud.Str(t[0]), shown, sum[:12])
	log := strings.TrimSpace(crud.Str(r[7]) + "\n" + line)
	if _, err := m.db.Exec(ctx, `UPDATE export__run SET status = ?, exported_at = ?, exported_by = ?, target = ?, export_path = ?, manifest_sha256 = ?, log = ?
		WHERE company_code = ? AND run_id = ?`, runExported, m.stamp(), nilIfEmpty(userOf(ctx)), tgt, nilIfEmpty(where), sum, log, cc, id); err != nil {
		return sdk.Response{}, err
	}
	out := map[string]any{"message": fmt.Sprintf("%d Dokumente exportiert – %s", len(mf.Items), shown)}
	if download != nil {
		out["download"] = *download
	}
	return sdk.Response{Payload: out}, nil
}
