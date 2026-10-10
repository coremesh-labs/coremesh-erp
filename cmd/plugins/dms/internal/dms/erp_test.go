package dms

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// TestERPFlow: Rechnung – Dokumentart, Partner, nur seine Rechnungen (nur
// „Eingangsrechnung anlegen“), Übernahme von Datum, Titel und Ablage über das
// Sachkonto, Name bestätigen, zurück, ablegen.
func TestERPFlow(t *testing.T) {
	e := setup(t)
	e.company()
	e.must(accountCCObject, "update", map[string]any{"id": "2000|4.2.0", "data": map[string]any{"gl_accounts": "6320"}})
	_, err := e.call(accountCCObject, "update", map[string]any{"id": "2000|3.0.1", "data": map[string]any{"gl_accounts": "6320"}})
	expect(t, err, sdk.ErrInvalidArgument, "Sachkonto in zwei Ablagen")
	e.h.invoices = []map[string]any{
		{"id": "2000|ER-1", "invoice_id": "ER-1", "company_code": "2000", "supplier_id": "P2", "supplier_reference": "RE-77", "invoice_date": "2026-03-05"},
		{"id": "2000|ER-2", "invoice_id": "ER-2", "company_code": "2000", "supplier_id": "P1", "supplier_reference": "GS-1", "invoice_date": "2026-02-01"},
	}
	e.h.items = []map[string]any{{"invoice_id": "ER-1", "account_number": "6320"}}
	e.run.text = "Rechnung vom 01.03.2026 Hausmeister"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("erp")), "file_name": "r.pdf"}})
	id := up["id"].(string)
	state := func(values map[string]string) map[string]any {
		return e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: values})
	}
	next := func(data map[string]any) {
		t.Helper()
		e.must(docObject, "continue", map[string]any{"id": id, "data": data})
	}
	doc := func() map[string]any { return e.must(docObject, "get", map[string]any{"id": id}) }
	next(map[string]any{"wf_choice": "INVOICE"})
	if d := doc(); d["wf_step"] != "035" {
		t.Fatalf("Partner zuerst: %v", d["wf_step"])
	}
	next(map[string]any{"wf_choice": "P2"})
	st := state(map[string]string{})
	opts := st["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any)
	links := st["links"].([]any)
	if len(opts) != 2 || opts[1].(map[string]any)["value"] != "SupplierInvoice:2000|ER-1" ||
		!strings.Contains(opts[1].(map[string]any)["label"].(string), "RE-77") || len(links) != 1 ||
		links[0].(map[string]any)["label"] != "Eingangsrechnung anlegen" {
		t.Fatalf("Objekte: %v %v", opts, links)
	}
	next(map[string]any{"wf_choice": "SupplierInvoice:2000|ER-1"})
	d := doc()
	if d["wf_step"] != "070" || d["file_account"] != "4.2.0" || d["doc_date"] != "2026-03-05" || d["title"] != "RE-77" ||
		!strings.Contains(d["wf_log"].(string), "Ablage 4.2.0 (Sachkonto 6320)") {
		t.Fatalf("Übernahme: %v\n%s", d, d["wf_log"])
	}
	st = state(map[string]string{})
	if v := st["fields"].(map[string]any)["wf_text"].(map[string]any)["value"]; v != "4.2.0:VV:ER-1_20260305_INVOICE:Pfitzner" {
		t.Fatalf("Namensvorschlag: %v", v)
	}
	// Zurück: zur Objektwahl, Übernahme zurückgenommen.
	e.must(docObject, "back", map[string]any{"id": id})
	d = doc()
	if d["wf_step"] != "040" || d["file_account"] != nil || d["doc_date"] != "2026-03-01" || d["title"] != nil {
		t.Fatalf("zurück: %v", d)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__document_ref WHERE document_id = ? AND is_active = 1`, id); n != int64(0) {
		t.Fatalf("Bezug zurückgenommen: %v", n)
	}
	e.must(docObject, "back", map[string]any{"id": id})
	if d = doc(); d["wf_step"] != "035" || d["partner_id"] != nil {
		t.Fatalf("zurück zum Partner: %v", d)
	}
	next(map[string]any{"wf_choice": "P2"})
	next(map[string]any{"wf_choice": "SupplierInvoice:2000|ER-1"})
	_, err = e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_text": "Hausmeister"}})
	expect(t, err, sdk.ErrInvalidArgument, "Name ohne Ablage")
	next(map[string]any{"wf_text": "4.2.0:VV:ER-1_Hausmeister"})
	if d = doc(); d["status"] != stFiled || d["name"] != "4.2.0:VV:ER-1_Hausmeister.pdf" {
		t.Fatalf("abgelegt: %v", d)
	}
	_, err = e.call(docObject, "back", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "zurück nach Ablage")
}

// TestERPWithoutPartner: Plan – kein Partner (Objekte ohne Partnerfeld),
// Gebäude oder Mietobjekt mit „anlegen“.
func TestERPWithoutPartner(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "Grundriss"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("plan")), "file_name": "p.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "PLAN"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["wf_step"] != "040" || !strings.Contains(d["wf_log"].(string), "entfällt für Dokumentart PLAN") {
		t.Fatalf("ohne Partner: %v", d)
	}
	st := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	links := st["links"].([]any)
	if len(links) != 2 || links[0].(map[string]any)["url"] != "/new/Building" || links[1].(map[string]any)["url"] != "/new/RentObject" {
		t.Fatalf("Links: %v", links)
	}
	if o := st["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any); !strings.Contains(o[1].(map[string]any)["value"].(string), "Building:") {
		t.Fatalf("nur Gebäude/Mietobjekte: %v", o)
	}
}
