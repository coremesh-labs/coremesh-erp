package dms

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// transferAction (seit 0.29.0): DmsDocument.transfer – abgelegte Dokumente eines
// Bezugs aus der Ablage mit Ordner je Bezug (z. B. dem Ordner eines Vorgangs) in
// ihre endgültige Ablage übernehmen. Name und Verzeichnis folgen dabei der neuen
// Ablage (wie „Neu benennen …“), der Bezug bleibt.
//
//	{company_code, object, key,
//	 account   Ziel-Ablage (leer = Vorschlags-Ablage der Dokumentart),
//	 doc_type  nur Dokumente dieser Art (leer = alle),
//	 refs      weitere Bezüge für die übernommenen Dokumente, z. B. die WEG des Vorgangs: [{object, key}]}
func (m *Module) transferAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CC      string `json:"company_code"`
		Object  string `json:"object"`
		Key     string `json:"key"`
		Account string `json:"account"`
		DocType string `json:"doc_type"`
		Refs    []struct {
			Object string `json:"object"`
			Key    string `json:"key"`
		} `json:"refs"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	in.Object, in.Key, in.Account, in.DocType = strings.TrimSpace(in.Object), strings.TrimSpace(in.Key), strings.TrimSpace(in.Account), strings.TrimSpace(in.DocType)
	if in.CC == "" || in.Object == "" || in.Key == "" {
		return sdk.Response{}, crud.Invalid("company_code, object und key sind Pflicht")
	}
	if err := requireWrite(ctx, docObject, "update", in.CC); err != nil {
		return sdk.Response{}, err
	}
	cfg, err := m.companyConfig(ctx, in.CC)
	if err != nil {
		return sdk.Response{}, err
	}
	if in.Account != "" {
		if _, err := m.companyAccount(ctx, cfg, in.Account); err != nil {
			return sdk.Response{}, err
		}
	}
	res, err := m.db.Query(ctx, `SELECT DISTINCT d.id FROM dms__document d JOIN dms__document_ref r ON r.document_id = d.id AND r.is_active = ?
		WHERE d.company_code = ? AND d.status = ? AND r.object = ? AND r.object_key = ? ORDER BY d.id`, true, in.CC, stFiled, in.Object, in.Key)
	if err != nil {
		return sdk.Response{}, err
	}
	var moved, skipped []string
	for _, row := range res.Rows {
		d, err := m.loadDoc(ctx, crud.Str(row[0]))
		if err != nil {
			return sdk.Response{}, err
		}
		if info, err := m.accountPath(ctx, cfg.Plan, d.Account); err != nil || info.FolderObject != in.Object {
			continue // schon in einer anderen Ablage
		}
		if in.DocType != "" && d.DocType != in.DocType {
			continue
		}
		target := in.Account
		if target == "" {
			if r, err := m.db.Query(ctx, `SELECT file_account FROM dms__doc_type WHERE code = ?`, d.DocType); err == nil && len(r.Rows) > 0 {
				target = crud.Str(r.Rows[0][0])
			}
		}
		if target == "" {
			skipped = append(skipped, d.Name+": Dokumentart ohne Vorschlags-Ablage – Ablage angeben")
			continue
		}
		for _, r := range in.Refs {
			if strings.TrimSpace(r.Object) != "" && strings.TrimSpace(r.Key) != "" {
				if err := m.insertRef(ctx, d, r.Object, r.Key); err != nil {
					return sdk.Response{}, err
				}
			}
		}
		if _, err := m.renameAction(ctx, sdk.Request{Payload: map[string]any{"id": d.ID, "data": map[string]any{"rename_account": target}}}); err != nil {
			skipped = append(skipped, d.Name+": "+err.Error())
			continue
		}
		after, err := m.loadDoc(ctx, d.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		if after.Account != target {
			skipped = append(skipped, d.Name+": nicht verschoben")
			continue
		}
		moved = append(moved, after.Name+" → "+target)
	}
	msg := fmt.Sprintf("%d Dokumente übernommen", len(moved))
	if len(skipped) > 0 {
		msg += fmt.Sprintf(", %d nicht: %s", len(skipped), strings.Join(skipped, "; "))
	}
	return sdk.Response{Payload: map[string]any{"moved": moved, "skipped": skipped, "message": msg}}, nil
}
