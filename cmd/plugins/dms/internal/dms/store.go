package dms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Erzeugte Dokumente ablegen (seit 0.19.0, Service DmsDocument.store): ein Plugin
// übergibt ein Dokument, das es selbst erzeugt hat (z. B. das Auftragsschreiben der
// Beschaffung) – ohne Eingangsverzeichnis und Workflow, direkt ins Archiv.
//
//	{company_code, content (Base64), file_name, content_type, doc_type, doc_date,
//	 title, partner_id, file_account (leer = ermitteln), note, refs: [{object, key}]}
//
// Die Ablage: angegeben, sonst die Ablagefindung über das kaufmännische Objekt der
// Kategorie (erster passender Bezug, z. B. Kostenart der Auftragspositionen), sonst
// die Ablage der Dokumentart. Name nach dem Muster des Buchungskreises, Verweise an
// den Bezügen wie beim Ablegen aus dem Eingang. Gleicher Inhalt: das vorhandene Dokument.

type storeRequest struct {
	CompanyCode string `json:"company_code"`
	Content     string `json:"content"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	DocType     string `json:"doc_type"`
	DocDate     string `json:"doc_date"`
	Title       string `json:"title"`
	Partner     string `json:"partner_id"`
	FileAccount string `json:"file_account"`
	Note        string `json:"note"`
	Refs        []struct {
		Object string `json:"object"`
		Key    string `json:"key"`
	} `json:"refs"`
}

func (m *Module) storeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in storeRequest
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	cc := strings.TrimSpace(in.CompanyCode)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis fehlt")
	}
	if err := requireWrite(ctx, docObject, "file", cc); err != nil {
		return sdk.Response{}, err
	}
	content, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Inhalt nicht lesbar (Base64 erwartet): %v", err)
	}
	if len(content) == 0 {
		return sdk.Response{}, crud.Invalid("Dokument ist leer")
	}
	docDate, err := crud.ParseDate(in.DocDate)
	if err != nil {
		return sdk.Response{}, crud.Invalid("Dokumentdatum: %v", err)
	}
	dt := strings.ToUpper(strings.TrimSpace(in.DocType))
	res, err := m.db.Query(ctx, `SELECT category, file_account FROM dms__doc_type WHERE code = ? AND is_active = ?`, dt, true)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Dokumentart %q gibt es nicht", in.DocType)
	}
	category, typeAccount := crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1])
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	if dup, err := m.db.Query(ctx, `SELECT id, name FROM dms__document WHERE company_code = ? AND (sha256 = ? OR source_sha256 = ?) AND status <> ?`,
		cc, sha, sha, stRemoved); err != nil {
		return sdk.Response{}, err
	} else if len(dup.Rows) > 0 {
		name := crud.Str(dup.Rows[0][1])
		return sdk.Response{Payload: map[string]any{"id": crud.Str(dup.Rows[0][0]), "name": name, "duplicate": true,
			"message": "Gleiches Dokument schon vorhanden: " + name}}, nil
	}
	var id, name string
	var warnings []string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		id = crud.NewID()
		call := sdk.CallFromContext(ctx)
		if _, err := m.db.Exec(ctx, `INSERT INTO dms__document (id, company_code, title, doc_category, doc_type, partner_id, doc_date, note, file_name,
			content_type, size, sha256, source_sha256, status, received_via, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, cc, nilIfEmpty(strings.TrimSpace(in.Title)), nilIfEmpty(category), dt, nilIfEmpty(strings.TrimSpace(in.Partner)), docDate,
			nilIfEmpty(strings.TrimSpace(in.Note)), orDefault(strings.TrimSpace(in.FileName), "dokument"+extFor(in.ContentType)),
			orDefault(in.ContentType, "application/octet-stream"), len(content), sha, sha, stInbox, viaGenerated, now(), nilIfEmpty(call.UserID)); err != nil {
			return err
		}
		for _, r := range in.Refs {
			if _, err := m.set.Entity(refObject).Create(ctx, map[string]any{"data": map[string]any{"document_id": id, "object": r.Object,
				"object_key": r.Key}}); err != nil {
				return err
			}
		}
		account := strings.TrimSpace(in.FileAccount)
		if account == "" {
			d, err := m.loadDoc(ctx, id)
			if err != nil {
				return err
			}
			if note, err := m.takeover(ctx, d); err == nil {
				m.appendLog(ctx, id, "Übernahme: "+note)
			}
			if d, err = m.loadDoc(ctx, id); err != nil {
				return err
			}
			account = firstOf(d.Account, typeAccount)
		}
		if account == "" {
			return crud.Invalid("Ablage für Dokumentart %s nicht ermittelt – an der Dokumentart eine Ablage angeben (Dokumentenverwaltung → Dokumentarten) oder eine Ablagefindung einrichten", dt)
		}
		if _, err := m.db.Exec(ctx, `UPDATE dms__document SET file_account = ? WHERE id = ?`, account, id); err != nil {
			return err
		}
		var err error
		name, warnings, err = m.fileDocumentWith(ctx, id, content)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := "Abgelegt: " + name
	if len(warnings) > 0 {
		msg += " – " + strings.Join(warnings, "; ")
	}
	return sdk.Response{Payload: map[string]any{"id": id, "name": name, "message": msg}}, nil
}

// Ablagen zur Auswahl (seit 0.20.0, Service DmsDocument.fileAccounts): für ein kaufmännisches
// Objekt (z. B. PurchaseQuote) die Ablagen der Dokumentarten seiner Kategorien – Vorschlags-Ablage
// und „Ablagen zur Auswahl“ –, soweit im Buchungskreis vorhanden, nicht gesperrt und ohne Unterablagen.
//
//	{company_code, object} → {options: [{value, label}], default}
func (m *Module) fileAccountsAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		CompanyCode string `json:"company_code"`
		Object      string `json:"object"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	res, err := m.db.Query(ctx, `SELECT t.file_account, t.file_accounts FROM dms__doc_type t
		WHERE t.is_active = ? AND t.category IN (SELECT category FROM dms__category_object WHERE object = ? AND is_active = ?)
		ORDER BY t.sort_order, t.code`, true, in.Object, true)
	if err != nil {
		return sdk.Response{}, err
	}
	var numbers []string
	def := ""
	for _, r := range res.Rows {
		if def == "" {
			def = crud.Str(r[0])
		}
		for _, n := range append([]string{crud.Str(r[0])}, splitList(crud.Str(r[1]))...) {
			if n != "" && !contains(numbers, n) {
				numbers = append(numbers, n)
			}
		}
	}
	by, _, err := m.companyLevels(ctx, strings.TrimSpace(in.CompanyCode))
	if err != nil {
		return sdk.Response{}, err
	}
	options := []any{}
	found := false
	for _, n := range numbers {
		if l := by[n]; l != nil && l.Leaf {
			options = append(options, map[string]any{"value": n, "label": n + " " + l.Name})
			found = found || n == def
		}
	}
	if !found {
		def = ""
	}
	return sdk.Response{Payload: map[string]any{"options": options, "default": def}}, nil
}
