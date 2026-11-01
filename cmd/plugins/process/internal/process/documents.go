package process

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Dokumente des Vorgangs im DMS: Ein Bezug auf den Vorgang im Eingang legt das
// Dokument in die Ablage mit Ordner je Vorgang (DmsFileAccount.folder_object =
// Process, Ordner „Nummer Kurzbezeichnung“). „Dokumente übernehmen …“ legt sie in
// die endgültige Ablage um (DmsDocument.transfer) und gibt ihnen die Bezüge des
// Vorgangs (Vertrag, Mietobjekt, Wirtschaftseinheit, Objekt) mit.

// shortNameLen: Länge der Kurzbezeichnung aus dem Betreff (Ordnername im DMS).
const shortNameLen = 40

// shortName: Kurzbezeichnung aus dem Betreff – höchstens shortNameLen Zeichen,
// an einer Wortgrenze gekürzt.
func shortName(subject string) string {
	s := strings.Join(strings.Fields(subject), " ")
	if utf8.RuneCountInString(s) <= shortNameLen {
		return s
	}
	cut := string([]rune(s)[:shortNameLen])
	if i := strings.LastIndex(cut, " "); i > 0 {
		return cut[:i]
	}
	return cut
}

// Migrate (0.3.0): Vorgänge ohne Kurzbezeichnung bekommen die aus dem Betreff.
func (m *Module) Migrate(ctx context.Context) error {
	res, err := m.db.Query(ctx, "SELECT company_code, process_id, subject FROM process__process WHERE short_name IS NULL OR short_name = ''")
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		if _, err := m.db.Exec(ctx, "UPDATE process__process SET short_name = ? WHERE company_code = ? AND process_id = ?",
			nilIfEmpty(shortName(crud.Str(r[2]))), r[0], r[1]); err != nil {
			return err
		}
	}
	return nil
}

// transferDocsAction: Process.transferDocs {id, data: {transfer_account, transfer_doc_type}} –
// Dokumente aus dem Ordner des Vorgangs in ihre endgültige Ablage übernehmen.
func (m *Module) transferDocsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in recordInput
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc, id, err := processKey(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, processObject, "transferDocs", cc); err != nil {
		return sdk.Response{}, err
	}
	row, err := m.one(ctx, "SELECT contract_id, object_id, entity_id, asset_id FROM process__process WHERE company_code = ? AND process_id = ?", cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	if row == nil {
		return sdk.Response{}, m.mustExist(ctx, cc, id)
	}
	var refs []map[string]any
	for i, object := range []string{"Contract", "RentObject", "BusinessEntity", "Asset"} {
		if v := crud.Str(row[i]); v != "" {
			refs = append(refs, map[string]any{"object": object, "key": cc + "|" + v})
		}
	}
	resp, err := m.services.Call(ctx, "DmsDocument", "transfer", map[string]any{"company_code": cc, "object": processObject, "key": cc + "|" + id,
		"account": strings.TrimSpace(crud.Str(in.Data["transfer_account"])), "doc_type": strings.TrimSpace(crud.Str(in.Data["transfer_doc_type"])), "refs": refs})
	if err != nil {
		return sdk.Response{}, err
	}
	var out struct {
		Moved   []string `json:"moved"`
		Skipped []string `json:"skipped"`
		Message string   `json:"message"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return sdk.Response{}, err
	}
	if len(out.Moved) > 0 {
		err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
			if err := m.systemEntry(ctx, cc, id, "Dokumente übernommen: "+strings.Join(out.Moved, "; ")); err != nil {
				return err
			}
			return m.journal(ctx, cc, id, "DOCS_TRANSFERRED", map[string]any{"moved": out.Moved})
		})
		if err != nil {
			return sdk.Response{}, err
		}
	}
	return sdk.Response{Payload: map[string]any{"moved": out.Moved, "skipped": out.Skipped, "message": out.Message}}, nil
}
