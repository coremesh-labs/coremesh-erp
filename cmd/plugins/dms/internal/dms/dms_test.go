package dms

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk/events"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func TestParseMindmap(t *testing.T) {
	title, entries, err := parseMindmap(testPlan)
	if err != nil {
		t.Fatal(err)
	}
	if title != "OBJEKTAKTE (TOP)" || len(entries) != 13 {
		t.Fatalf("%q %d %+v", title, len(entries), entries)
	}
	if e := entries[2]; e.Number != "3.0.0" || e.Name != "Einheitswertbescheide" || e.Parent != "3.0" {
		t.Fatalf("Eintrag: %+v", e)
	}
	if _, _, err := parseMindmap(`<map><node TEXT="TOP"><node TEXT="ohne Nummer"/></node></map>`); err == nil || !strings.Contains(err.Error(), "ohne Nummer") {
		t.Fatalf("Knoten ohne Nummer: %v", err)
	}
}

func TestImportPlanAndLeaf(t *testing.T) {
	e := setup(t)
	r := e.must(setupObject, "importPlan", map[string]any{"plan": "immo", "file": testPlan})
	if r["created"] != float64(13) {
		t.Fatalf("Import: %v", r)
	}
	acc := e.must(accountObject, "get", map[string]any{"id": "IMMO|3.0.1"})
	if acc["parent"] != "3.0" || acc["is_leaf"] != true || acc["name"] != "Grundsteuerbescheide (Jahresbescheide)" {
		t.Fatalf("Ablage: %v", acc)
	}
	if g := e.must(accountObject, "get", map[string]any{"id": "IMMO|3.0"}); g["is_leaf"] != false {
		t.Fatalf("Gruppe: %v", g)
	}
	// Erneut einlesen: Namen aktualisiert, nichts doppelt.
	r = e.must(setupObject, "importPlan", map[string]any{"plan": "IMMO", "file": strings.Replace(testPlan, "Einheitswertbescheide", "Einheitswerte", 1)})
	if r["created"] != float64(0) || r["updated"] != float64(1) {
		t.Fatalf("Zweiter Import: %v", r)
	}
	// Ohne übergeordnete Ablage geht nichts.
	_, err := e.call(accountObject, "create", map[string]any{"data": map[string]any{"plan": "IMMO", "number": "7.1.0", "name": "Mietvertrag"}})
	expect(t, err, sdk.ErrInvalidArgument, "fehlende übergeordnete Ablage")
	if sortKey("1.10") <= sortKey("1.9") {
		t.Fatal("Sortierung 1.10 nach 1.9")
	}
}

func TestBuildName(t *testing.T) {
	p := nameParts{"account": "3.0.1", "company": "VV", "object": "ROBro1", "date": "20250110", "doctype": "NOTICE", "partner": "Stadt Musterstadt"}
	if n := buildName(defaultPattern, p); n != "3.0.1:VV:ROBro1_20250110_NOTICE:Stadt-Musterstadt" {
		t.Fatalf("Name: %q", n)
	}
	delete(p, "object")
	delete(p, "partner")
	if n := buildName(defaultPattern, p); n != "3.0.1:VV:20250110_NOTICE" {
		t.Fatalf("ohne Objekt und Partner: %q", n)
	}
	if err := checkPattern("{company}_{date}"); err == nil {
		t.Fatal("Muster ohne {account}")
	}
	if err := checkPattern("{account}/{date}"); err == nil {
		t.Fatal("Muster mit Pfadtrenner")
	}
	if err := checkPattern("{account}:{gibtsnicht}"); err == nil {
		t.Fatal("unbekannter Platzhalter")
	}
}

func TestRetentionEnd(t *testing.T) {
	for _, c := range []struct {
		start         string
		years, months int
		date, want    string
	}{
		{"END_OF_YEAR", 8, 0, "2025-03-15", "2033-12-31"},
		{"DOC_DATE", 3, 6, "2025-03-15", "2028-09-15"},
		{"FOREVER", 0, 0, "2025-03-15", ""},
	} {
		got, err := retentionEnd(c.start, c.years, c.months, c.date)
		if err != nil || got != c.want {
			t.Errorf("%+v: %q %v", c, got, err)
		}
	}
}

// TestInboxWorkflowFile: Eingang mit Vorgaben, Hochladen, Standard-Workflow
// (Texterkennung, Datum, übersprungene Fragen, Titel, Bestätigung), Ablage
// im Verzeichnisbaum (nur lesbar), Verweis am Vertrag, Inhalt über content.
func TestInboxWorkflowFile(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "Stadt Musterstadt – Grundsteuerbescheid vom 10.01.2025 für das Grundstück Brockenstraße 1"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "musterstadt", "name": "Musterstadt", "path": e.in, "workflow": "STANDARD"})
	for _, p := range [][2]string{{"Building", "ROBro1"}, {docTypeObject, "notice"}, {accountObject, "3.0.1"}, {"BusinessPartner", "P1"},
		{"Contract", "GS-2026-0009"}} {
		e.create(inboxParamObject, map[string]any{"company_code": "2000", "inbox": "MUSTERSTADT", "object": p[0], "value": p[1]})
	}
	content := pdf("Grundsteuer")
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|MUSTERSTADT", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(content), "file_name": "scan 0001.pdf", "file_type": "application/pdf"}})
	id := up["id"].(string)
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["status"] != stInbox || doc["doc_date"] != "2025-01-10" || doc["doc_type"] != "NOTICE" || doc["file_account"] != "3.0.1" ||
		doc["wf_status"] != wfWaiting || doc["wf_step"] != "070" || doc["wf_question"] != "Schritt 070 · Titel: Titel des Dokuments?" {
		t.Fatalf("Nach dem Einlesen: %v", doc)
	}
	if doc["_hidden_sections"] != nil {
		t.Fatalf("Schritt wartet – Abschnitt sichtbar: %v", doc["_hidden_sections"])
	}
	if !strings.Contains(doc["wf_log"].(string), "030 Dokumentart wählen: Übersprungen") {
		t.Fatalf("Verlauf: %s", doc["wf_log"])
	}
	// Viewer liest aus dem Eingang.
	c := e.must(docObject, metamodel.ContentAction, map[string]any{"id": id})
	if b, _ := base64.StdEncoding.DecodeString(c["content"].(string)); !bytes.Equal(b, content) || c["content_type"] != "application/pdf" {
		t.Fatalf("Inhalt im Eingang: %v", c["content_type"])
	}
	// FormState fragt den Titel als Text.
	fs := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	if fs["message"] != "Schritt 070 · Titel: Titel des Dokuments?" {
		t.Fatalf("FormState: %v", fs)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_text": "Grundsteuerbescheid 2025"}})
	// Bestätigen: Ja/Nein als Knöpfe.
	fs = e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	if b, _ := fs["buttons"].([]any); len(b) != 1 || b[0].(map[string]any)["label"] != "Ablegen" {
		t.Fatalf("Knöpfe: %v", fs["buttons"])
	}
	r := e.must(docObject, "continue", map[string]any{"id": id, "button": oYes, "data": map[string]any{}})
	if !strings.Contains(r["message"].(string), "abgelegt als 30.1:VV:ROBro1_20250110_BS:StadtMusterstadt.pdf") {
		t.Fatalf("Ablegen: %v", r)
	}
	doc = e.must(docObject, "get", map[string]any{"id": id})
	want := filepath.Join(e.root, "3 Steuern Lasten Abgaben", "3.0 Grundsteuer", "3.0.1 Grundsteuerbescheide (Jahresbescheide)",
		"ROBro1 Brockenstraße 1", "30.1:VV:ROBro1_20250110_BS:StadtMusterstadt.pdf")
	if doc["status"] != stFiled || doc["wf_status"] != wfDone || doc["retain_until"] != nil || doc["link"] != "/file/DmsDocument/"+id ||
		fmt.Sprint(doc["_hidden_sections"]) != "[schritt]" {
		t.Fatalf("Abgelegt: %v", doc)
	}
	fi, err := os.Stat(want)
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("Datei im Archiv: %v %v", fi, err)
	}
	// Verzeichnisse gesperrt: die Datei lässt sich nicht löschen (außer als root).
	if di, _ := os.Stat(filepath.Dir(want)); di.Mode().Perm() != 0o555 {
		t.Fatalf("Verzeichnis: %v", di.Mode())
	}
	if os.Geteuid() != 0 {
		if err := os.Remove(want); err == nil {
			t.Fatal("Datei im Archiv ließ sich löschen")
		}
	}
	if _, err := os.Stat(filepath.Join(e.in, "scan 0001.pdf")); !os.IsNotExist(err) {
		t.Fatalf("Datei im Eingang sollte weg sein: %v", err)
	}
	c = e.must(docObject, metamodel.ContentAction, map[string]any{"id": "30.1:VV:ROBro1_20250110_BS:StadtMusterstadt.pdf"})
	if b, _ := base64.StdEncoding.DecodeString(c["content"].(string)); !bytes.Equal(b, content) {
		t.Fatal("Inhalt aus dem Archiv (über den Dokumentnamen)")
	}
	// Verweise am Gebäude, am Partner und am Vertrag – mit Dokumentname und Link.
	if len(e.h.attached) != 3 || e.h.attached[0].EntityType != "Building" || e.h.attached[0].EntityID != "2000|ROBro1" ||
		e.h.attached[1].EntityType != "BusinessPartner" || e.h.attached[2].EntityID != "2000|GS-2026-0009" ||
		e.h.attached[2].Document.Link != "/file/DmsDocument/"+id || e.h.attached[2].Document.Location != "30.1:VV:ROBro1_20250110_BS:StadtMusterstadt.pdf" ||
		e.h.attached[2].Document.DocType != "NOTICE" || e.h.attached[2].Document.Title != "Grundsteuerbescheid 2025" {
		t.Fatalf("Verweise: %+v", e.h.attached)
	}
	// Kopf fest, Notiz änderbar.
	_, err = e.call(docObject, "update", map[string]any{"id": id, "data": map[string]any{"title": "anders"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kopf nach Ablage")
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"note": "Original im Ordner"}})
	// Gleicher Inhalt erneut: schon bekannt.
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|MUSTERSTADT", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(content), "file_name": "nochmal.pdf"}})
	if up["duplicate"] != true || up["id"] != id {
		t.Fatalf("Doppelt: %v", up)
	}
	// Dauerhaft aufzubewahren: Löschen verweigert.
	_, err = e.call(docObject, "remove", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Löschen ohne Fristablauf")
	// Volltextsuche im erkannten Text.
	if l := items(e.must(docObject, "list", map[string]any{"query": map[string]any{"q": "brockenstraße"}})); len(l) != 1 {
		t.Fatalf("Suche: %v", l)
	}
	if l := items(e.must(docObject, "list", map[string]any{"query": map[string]any{"ref_object": "Contract", "ref_key": "2000|GS-2026-0009"}})); len(l) != 1 {
		t.Fatalf("Filter Bezug: %v", l)
	}
}

// TestScanAndManualFile: Einlesen aus dem Verzeichnis ohne Workflow, Ablegen
// von Hand; Gruppe statt unterster Ebene wird abgelehnt; Löschen nach Frist.
func TestScanAndManualFile(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "post", "name": "Post", "path": e.in})
	if err := os.WriteFile(filepath.Join(e.in, "rechnung.pdf"), pdf("Rechnung"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.must(scanObject, "scan", map[string]any{"company": "2000", "inbox": "post"})
	if r["new"] != float64(1) {
		t.Fatalf("Einlesen: %v", r)
	}
	if r = e.must(scanObject, "scan", map[string]any{"company": "2000", "inbox": "post"}); r["duplicate"] != float64(1) {
		t.Fatalf("Zweites Einlesen: %v", r)
	}
	if l := items(e.must(docObject, "list", map[string]any{"query": map[string]any{"received_via": viaScan, "received_inbox": "POST"}})); len(l) != 1 {
		t.Fatalf("Eingangsweg eingelesen: %v", l)
	}
	id := items(e.must(docObject, "list", map[string]any{"query": map[string]any{"inbox": "POST"}}))[0]["id"].(string)
	_, err := e.call(docObject, "file", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Ablegen ohne Ablage")
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"title": "Rechnung Hausmeister", "doc_type": "INVOICE",
		"file_account": "4.2", "doc_date": "2020-03-01", "retention": "Y3"}})
	_, err = e.call(docObject, "file", map[string]any{"id": id})
	if err == nil || !strings.Contains(err.Error(), "Unterablagen") {
		t.Fatalf("Gruppe: %v", err)
	}
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"file_account": "4.2.0"}})
	e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": "SupplierInvoice", "ref_key": "ER-00007"}})
	r = e.must(docObject, "file", map[string]any{"id": id})
	if r["name"] != "42.0:VV:Allgemein_ER-00007_20200301_RE.pdf" {
		t.Fatalf("Name: %v", r)
	}
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["retain_until"] != "2023-12-31" {
		t.Fatalf("Frist: %v", doc["retain_until"])
	}
	// Unterablage unter eine Ablage mit Dokumenten: abgelehnt.
	_, err = e.call(accountObject, "create", map[string]any{"data": map[string]any{"plan": "IMMO", "number": "4.2.0.1", "name": "Teil"}})
	expect(t, err, sdk.ErrInvalidArgument, "Unterablage unter gefüllter Ablage")
	// Frist abgelaufen: löschen.
	r = e.must(docObject, "remove", map[string]any{"id": id})
	if !strings.Contains(r["message"].(string), "Gelöscht") {
		t.Fatalf("Löschen: %v", r)
	}
	if _, err := os.Stat(filepath.Join(e.root, "4 Finanzen Bankwesen", "4.2 Buchhaltung", "4.2.0 Eingangsrechnungen (Kreditorisch)", "Allgemein", "42.0:VV:Allgemein_ER-00007_20200301_RE.pdf")); !os.IsNotExist(err) {
		t.Fatalf("Datei sollte gelöscht sein: %v", err)
	}
	if len(e.h.detached) != 1 {
		t.Fatalf("Verweis entfernt: %v", e.h.detached)
	}
}

// fileDoc legt ein Dokument mit Bezug direkt ab (Hilfe für die Prüfungen).
func (e *env) fileDoc(account, docType, date string, refObject, refKey string) string {
	e.t.Helper()
	os.MkdirAll(e.in, 0o755)
	if _, err := e.call(inboxObject, "get", map[string]any{"id": "2000|HAND"}); err != nil {
		e.create(inboxObject, map[string]any{"company_code": "2000", "code": "hand", "name": "Von Hand", "path": e.in})
	}
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|HAND", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf(account + date + refKey)), "file_name": "d.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"doc_type": docType, "file_account": account, "doc_date": date}})
	e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": refObject, "ref_key": refKey}})
	e.must(docObject, "file", map[string]any{"id": id})
	return id
}

func (e *env) hook(name string, data any) hook.Response {
	e.t.Helper()
	cb := cbPosting
	if name != hookPosting {
		cb = cbContract
	}
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: cb, Action: hook.CallbackAction, Payload: hook.Request{Hook: name, Action: hook.PhaseCheck, Data: data}})
	if err != nil {
		e.t.Fatal(err)
	}
	var out hook.Response
	_ = sdk.Decode(resp.Payload, &out)
	return out
}

func msgTypes(r hook.Response) string {
	var s []string
	for _, m := range r.Messages {
		s = append(s, m.Type)
	}
	return strings.Join(s, ",")
}

// TestPostingCheck: Belegart KR braucht ein abgelegtes Dokument in 4.2 – an
// der Vorerfassung oder an der Eingangsrechnung, die auf sie zeigt.
func TestPostingCheck(t *testing.T) {
	e := setup(t)
	e.company()
	data := map[string]any{"request": map[string]any{"company_code": "2000", "document_type": "KR"}, "draft_id": "D1"}
	if r := e.hook(hookPosting, data); len(r.Messages) != 0 {
		t.Fatalf("Prüfung aus: %v", r.Messages)
	}
	e.must(companyObject, "update", map[string]any{"id": "2000", "data": map[string]any{"check_postings": true}})
	e.create(postRuleObject, map[string]any{"company_code": "2000", "rule_no": 10, "document_type": "kr", "file_accounts": "4.2"})
	if r := e.hook(hookPosting, data); msgTypes(r) != "E" || !strings.Contains(r.Messages[0].Text, "kein abgelegtes Dokument") {
		t.Fatalf("ohne Dokument: %v", r.Messages)
	}
	sim := map[string]any{"request": data["request"], "draft_id": "D1", "simulate": true}
	if r := e.hook(hookPosting, sim); msgTypes(r) != "W" {
		t.Fatalf("Simulation: %v", r.Messages)
	}
	if r := e.hook(hookPosting, map[string]any{"request": map[string]any{"company_code": "2000", "document_type": "DZ"}, "draft_id": "D1"}); len(r.Messages) != 0 {
		t.Fatalf("andere Belegart: %v", r.Messages)
	}
	// Dokument an der Eingangsrechnung, die auf die Vorerfassung zeigt.
	e.fileDoc("4.2.0", "INVOICE", "2026-02-01", "SupplierInvoice", "ER-00007")
	e.h.invoices = []map[string]any{{"company_code": "2000", "invoice_id": "ER-00007", "draft_id": "D1"}}
	if r := e.hook(hookPosting, data); len(r.Messages) != 0 {
		t.Fatalf("mit Dokument: %v", r.Messages)
	}
	// Dokument in falscher Ablage zählt nicht.
	e.fileDoc("3.0.1", "NOTICE", "2026-02-01", "JournalDraft", "D2")
	if r := e.hook(hookPosting, map[string]any{"request": data["request"], "draft_id": "D2"}); msgTypes(r) != "E" ||
		!strings.Contains(r.Messages[0].Text, "anderen Ablagen") {
		t.Fatalf("falsche Ablage: %v", r.Messages)
	}
}

// TestContractCheck: Hausgeld (HG) am WEG-Vertrag nur mit neuem Dokument.
func TestContractCheck(t *testing.T) {
	e := setup(t)
	e.company()
	e.must(companyObject, "update", map[string]any{"id": "2000", "data": map[string]any{"check_contracts": true}})
	e.create(contractRuleObj, map[string]any{"company_code": "2000", "rule_no": 1, "event": "CONDITION", "contract_type": "WH",
		"condition_types": "hg", "file_accounts": "5.2.3.2"})
	cond := map[string]any{"company_code": "2000", "contract_id": "WH-2026-0001", "contract_type": "WH", "condition_type": "HG",
		"valid_from": "2026-01-01", "previous_valid_from": "2025-01-01"}
	if r := e.hook(hookCondition, cond); msgTypes(r) != "E" {
		t.Fatalf("ohne Dokument: %v", r.Messages)
	}
	e.fileDoc("5.2.3.2", "STATEMENT", "2024-12-01", "Contract", "WH-2026-0001")
	if r := e.hook(hookCondition, cond); msgTypes(r) != "E" || !strings.Contains(r.Messages[0].Text, "nach 2025-01-01") {
		t.Fatalf("altes Dokument: %v", r.Messages)
	}
	e.fileDoc("5.2.3.2", "STATEMENT", "2025-11-20", "Contract", "WH-2026-0001")
	if r := e.hook(hookCondition, cond); len(r.Messages) != 0 {
		t.Fatalf("neues Dokument: %v", r.Messages)
	}
	cond["condition_type"] = "MA"
	if r := e.hook(hookCondition, cond); len(r.Messages) != 0 {
		t.Fatalf("andere Konditionsart: %v", r.Messages)
	}
	if r := e.hook(hookActivate, cond); len(r.Messages) != 0 {
		t.Fatalf("Aktivieren ohne Regel: %v", r.Messages)
	}
}

// TestAIAndCopyWorkflow: eigener Workflow mit KI-Schritt (Dokumentart aus
// einer Liste), kopiert; Prüfen meldet fehlendes Ablegen.
func TestAIAndCopyWorkflow(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "KI", "name": "KI-Test"})
	e.create(stepObject, map[string]any{"workflow": "KI", "step": "10", "name": "Art per KI", "kind": kAI, "target": "doc_type",
		"ai_request": "DOC_KIND", "context": "Welche Art?", "source_kind": srcList, "apply": applySet})
	for _, v := range [][2]string{{"INVOICE", "Rechnung"}, {"NOTICE", "Bescheid"}} {
		e.create(mappingObject, map[string]any{"workflow": "KI", "step": "10", "role": mapValue, "field": v[0], "value": v[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "KI", "step": "20", "name": "Rechnung?", "kind": kCheck})
	e.create(conditionObject, map[string]any{"workflow": "KI", "step": "20", "role": condCheck, "left_expr": "{doc.doc_type}", "operator": opEq, "right_expr": "INVOICE"})
	e.create(optionObject, map[string]any{"workflow": "KI", "step": "10", "outcome": oSuccess, "next_step": "20"})
	r := e.must(workflowObject, "check", map[string]any{"id": "KI"})
	if !strings.Contains(r["message"].(string), "2 Hinweise") { // keine Option „nicht erfolgreich“, kein Ablegen
		t.Fatalf("Prüfen: %v", r)
	}
	e.must(workflowObject, "copy", map[string]any{"id": "KI", "data": map[string]any{"copy_code": "ki2"}})
	if n := e.one(`SELECT COUNT(*) FROM dms__step_mapping WHERE workflow = 'KI2'`); n != int64(2) {
		t.Fatalf("Kopie Zuordnungen: %v", n)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__step_condition WHERE workflow = 'KI2'`); n != int64(1) {
		t.Fatalf("Kopie Bedingungen: %v", n)
	}
	e.run.text = "Rechnung Nr. 4711 über Wartung"
	e.h.aiText = "INVOICE"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "ki", "name": "KI", "path": e.in, "workflow": "KI2"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|KI", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ki")), "file_name": "r.pdf"}})
	doc := e.must(docObject, "get", map[string]any{"id": up["id"]})
	if doc["doc_type"] != "INVOICE" || doc["wf_status"] != wfDone {
		t.Fatalf("KI: %v", doc)
	}
	var last sdk.Request
	for _, c := range e.h.aiCalls {
		if c.Action == "run" {
			last = c
		}
	}
	if ctxText := fmt.Sprint(last.Payload.(map[string]any)["context"]); !strings.Contains(ctxText, "Welche Art?") || !strings.Contains(ctxText, "INVOICE – Rechnung") {
		t.Fatalf("Kontext: %s", ctxText)
	}
	// Gleiche Anfrage beim Neustart: Antwort aus dem Zwischenspeicher.
	n := len(e.h.aiCalls)
	e.must(docObject, "startOver", map[string]any{"id": up["id"]})
	if len(e.h.aiCalls) != n || !strings.Contains(fmt.Sprint(e.must(docObject, "get", map[string]any{"id": up["id"]})["wf_log"]), "gespeicherte Antwort") {
		t.Fatalf("Zwischenspeicher: %d Aufrufe statt %d", len(e.h.aiCalls), n)
	}
	// Antwort außerhalb der Auswahl: Schritt nicht erfolgreich, Workflow hält an.
	e.h.aiText = "UNSINN"
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|KI", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ki2")), "file_name": "s.pdf"}})
	if doc = e.must(docObject, "get", map[string]any{"id": up["id"]}); doc["wf_status"] != wfFailed {
		t.Fatalf("KI falsch: %v", doc)
	}
}

func TestArchiveStaysInRoot(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { unlockTree(root) })
	a := fileArchive{root: root}
	if _, err := a.Load("../../etc/passwd"); err == nil {
		t.Fatal("Pfad außerhalb des Archivs")
	}
	if _, err := a.Store(StoreRequest{Content: []byte("x"), Name: "../x.pdf"}); err == nil {
		t.Fatal("Name mit Pfad")
	}
	loc, err := a.Store(StoreRequest{Content: []byte("x"), Name: "1:a.pdf", Path: []Level{{"1", "Akte/Teil"}}})
	if err != nil || loc != "1 Akte-Teil/1:a.pdf" {
		t.Fatalf("%q %v", loc, err)
	}
	if _, err := a.Store(StoreRequest{Content: []byte("y"), Name: "1:a.pdf", Path: []Level{{"1", "Akte/Teil"}}}); err == nil {
		t.Fatal("überschreiben")
	}
}

// TestDiscardInbox: Eingang „Vorentscheidung verwerfen“ – kein Workflow; endgültig
// verwerfen löscht die Datei, das Dokument bleibt; erneut eingelesen: bekannt.
func TestDiscardInbox(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in + "/pdf", "workflow": "STANDARD"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "unwichtig", "name": "Unwichtig", "path": e.in + "/unwichtig",
		"workflow": "STANDARD", "discard": true})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("werbung")), "file_name": "werbung.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "handover", map[string]any{"id": id, "data": map[string]any{"handover_inbox": "UNWICHTIG"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["wf_code"] != nil || d["wf_status"] != nil || !strings.Contains(d["wf_log"].(string), "Zum Verwerfen vorgemerkt") {
		t.Fatalf("vorgemerkt: %v", d)
	}
	if d["received_via"] != viaUpload || d["received_inbox"] != "PDF" || d["inbox"] != "UNWICHTIG" {
		t.Fatalf("Eingangsweg nach der Übergabe: %v", d)
	}
	file := d["inbox_file"].(string)
	e.must(docObject, "discard", map[string]any{"id": id})
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["status"] != stDiscarded || d["inbox_file"] != nil {
		t.Fatalf("verworfen: %v", d)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("Datei sollte weg sein: %v", err)
	}
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("werbung")), "file_name": "nochmal.pdf"}})
	if up["duplicate"] != true || up["id"] != id {
		t.Fatalf("erneut: %v", up)
	}
	_, err := e.call(docObject, "discard", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "zweimal verwerfen")

	// wieder aufnehmen: ohne Datei im Eingang nicht, mit Datei – Workflow von vorn
	_, err = e.call(docObject, "reopen", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Datei")
	again := filepath.Join(e.in, "pdf", "wieder.pdf")
	if err := os.WriteFile(again, pdf("werbung"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.must(docObject, "reopen", map[string]any{"id": id})
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["status"] != stInbox || d["inbox_file"] != again || d["inbox"] != "PDF" || d["wf_code"] != "STANDARD" {
		t.Fatalf("wieder aufgenommen: %v", d)
	}
	_, err = e.call(docObject, "reopen", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "nicht verworfen")
}

// TestForward: fehlgeleitetes Dokument weiterleiten – die Datei zieht in das
// Weiterleitungsziel um (nie überschreibend), das Dokument bleibt „weitergeleitet“;
// erneut eingelesen: bekannt; mit der Datei im Eingang wieder aufnehmbar.
func TestForward(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in + "/pdf", "workflow": "STANDARD"})
	_, err := e.call(forwardObject, "create", map[string]any{"company_code": "2000", "code": "privat", "name": "Private Belege", "path": "privat"})
	expect(t, err, sdk.ErrInvalidArgument, "relativer Pfad")
	dir := filepath.Join(e.in, "privat")
	e.create(forwardObject, map[string]any{"company_code": "2000", "code": "privat", "name": "Private Belege", "path": dir})
	if err := os.WriteFile(filepath.Join(dir, "beleg.pdf"), []byte("vorhanden"), 0o644); err != nil {
		t.Fatal(err)
	}
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("privat")), "file_name": "beleg.pdf"}})
	id := up["id"].(string)
	file := e.must(docObject, "get", map[string]any{"id": id})["inbox_file"].(string)
	_, err = e.call(docObject, "forward", map[string]any{"id": id, "data": map[string]any{"forward_target": "GIBTSNICHT"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Ziel")
	e.must(docObject, "forward", map[string]any{"id": id, "data": map[string]any{"forward_target": "privat"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	want := filepath.Join(dir, "beleg_2.pdf")
	if d["status"] != stForwarded || d["inbox_file"] != nil || d["forwarded_to"] != want || !strings.Contains(d["wf_log"].(string), "Weitergeleitet an PRIVAT") {
		t.Fatalf("weitergeleitet: %v", d)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != string(pdf("privat")) {
		t.Fatalf("Datei im Ziel: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "beleg.pdf")); string(b) != "vorhanden" {
		t.Fatal("vorhandene Datei überschrieben")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("Datei im Eingang sollte weg sein: %v", err)
	}
	_, err = e.call(docObject, "forward", map[string]any{"id": id, "data": map[string]any{"forward_target": "privat"}})
	expect(t, err, sdk.ErrInvalidArgument, "zweimal weiterleiten")
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("privat")), "file_name": "nochmal.pdf"}})
	if up["duplicate"] != true || up["id"] != id {
		t.Fatalf("erneut: %v", up)
	}
	if err := os.Rename(want, filepath.Join(e.in, "pdf", "zurueck.pdf")); err != nil {
		t.Fatal(err)
	}
	e.must(docObject, "reopen", map[string]any{"id": id})
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["status"] != stInbox || d["forwarded_to"] != nil {
		t.Fatalf("wieder aufgenommen: %v", d)
	}
}

// unlockTree gibt gesperrte Verzeichnisse vor dem Aufräumen frei.
func unlockTree(dir string) {
	filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
}

// TestSealArchive: Seal sperrt vorhandene Verzeichnisse und Dateien; Ablegen
// und Löschen nach Frist öffnen nur kurz.
func TestSealArchive(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { unlockTree(root) })
	os.MkdirAll(root+"/1 Akte/1.0 Teil", 0o755)
	os.WriteFile(root+"/1 Akte/1.0 Teil/alt.pdf", []byte("x"), 0o644)
	a := fileArchive{root: root}
	if err := a.Seal(); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{root + "/1 Akte": 0o555, root + "/1 Akte/1.0 Teil": 0o555, root + "/1 Akte/1.0 Teil/alt.pdf": 0o444, root: 0o700} {
		fi, err := os.Stat(p)
		if err != nil || (p != root && fi.Mode().Perm() != want) || (p == root && fi.Mode().Perm()&0o200 == 0) {
			t.Fatalf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	loc, err := a.Store(StoreRequest{Content: []byte("neu"), Name: "1.0:neu.pdf", Path: []Level{{"1", "Akte"}, {"1.0", "Teil"}, {"", "ROBro1"}}})
	if err != nil || loc != "1 Akte/1.0 Teil/ROBro1/1.0:neu.pdf" {
		t.Fatalf("Store: %q %v", loc, err)
	}
	if fi, _ := os.Stat(root + "/1 Akte/1.0 Teil/ROBro1"); fi.Mode().Perm() != 0o555 {
		t.Fatalf("neues Verzeichnis gesperrt: %v", fi.Mode())
	}
	if err := a.Remove(loc); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(root + "/1 Akte/1.0 Teil"); fi.Mode().Perm() != 0o555 {
		t.Fatalf("nach Löschen wieder gesperrt: %v", fi.Mode())
	}
}

// Suchtext der Bezüge (0.15.0): Freitextsuche findet Dokumente über Texte der Eingangsrechnung
// (auch Klartexte) und den Namen des Geschäftspartners; ohne den Bezug nicht mehr.
func TestSearchReferencedTexts(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	e.h.invoices = []map[string]any{{"id": "2000|ER-2026-00001", "company_code": "2000", "invoice_id": "ER-2026-00001",
		"header_text": "Winterdienst Am Stadttor 3", "supplier_reference": "100042", "draft_id": "516edc21ad7024a0d62800df64899039",
		"_labels": map[string]any{"cost_category": "Gartenpflege"}}}
	e.run.text = "Rechnung ohne Absender"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("such")), "file_name": "such.pdf"}})
	id := up["id"].(string)
	search := func(q string) int {
		return len(items(e.must(docObject, "list", map[string]any{"query": map[string]any{"q": q}})))
	}
	if search("winterdienst") != 0 {
		t.Fatal("ohne Bezug gefunden")
	}
	e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": "SupplierInvoice", "ref_key": "ER-2026-00001"}})
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"partner_id": "P3"}})
	for _, q := range []string{"winterdienst", "100042", "gartenpflege", "grundstücksservice", "ER-2026-00001"} {
		if search(q) != 1 {
			t.Errorf("Suche %q: nicht gefunden (%v)", q, e.one(`SELECT text FROM dms__document_search WHERE document_id = ?`, id))
		}
	}
	if search("516edc21") != 0 {
		t.Error("technische ID im Suchtext")
	}
	e.must(refObject, "deactivate", map[string]any{"id": id + "|1"})
	if search("winterdienst") != 0 || search("grundstücksservice") != 1 {
		t.Fatalf("nach Entfernen des Bezugs: %v", e.one(`SELECT text FROM dms__document_search WHERE document_id = ?`, id))
	}
	// „Suchtexte aktualisieren“ am Buchungskreis
	e.h.db.Exec(`DELETE FROM dms__document_search`)
	if r := e.must(companyObject, "refreshSearch", map[string]any{"id": "2000"}); r["refreshed"] != float64(1) && r["refreshed"] != 1 {
		t.Fatalf("aktualisieren: %v", r)
	}
	if search("grundstücksservice") != 1 {
		t.Fatal("nach dem Aktualisieren")
	}
}

// Suchtext per SystemEvent (0.16.0): Änderung an einer Rechnungsposition (Schlüssel unter
// dem des Bezugs) bzw. am Geschäftspartner baut den Suchtext neu; fremde Datensätze nicht.
func TestSearchEvents(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	e.h.invoices = []map[string]any{{"id": "2000|ER-2026-00001", "company_code": "2000", "invoice_id": "ER-2026-00001", "header_text": "Winterdienst"}}
	e.run.text = "Rechnung"
	id := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ev")), "file_name": "ev.pdf"}})["id"].(string)
	e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": "SupplierInvoice", "ref_key": "ER-2026-00001"}})
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"partner_id": "P2"}})
	text := func() string {
		return fmt.Sprint(e.one(`SELECT text FROM dms__document_search WHERE document_id = ?`, id))
	}
	event := func(object, entity string, data map[string]any) any {
		return e.must(searchEventObject, events.CallbackAction, map[string]any{"event": events.Event{Object: object, Action: "update", CompanyCode: "2000", EntityID: entity, Data: data}})["refreshed"]
	}
	e.h.invoices[0]["header_text"] = "Streudienst Januar"
	if n := event("SupplierInvoice", "2000|ER-2026-000011", nil); fmt.Sprint(n) != "0" || strings.Contains(text(), "Streudienst") {
		t.Fatalf("fremder Schlüssel mit gleichem Anfang: %v %s", n, text())
	}
	if n := event("SupplierInvoiceItem", "2000|ER-2026-00001|1", nil); fmt.Sprint(n) != "1" || !strings.Contains(text(), "Streudienst Januar") {
		t.Fatalf("Position geändert: %v %s", n, text())
	}
	testPartners[1]["name1"] = "Wagner Hausmeisterdienste"
	defer func() { testPartners[1]["name1"] = "Wagner" }()
	if n := event(partnerObject, "P2|2026-01-01", map[string]any{"id": "P2"}); fmt.Sprint(n) != "1" || !strings.Contains(text(), "Hausmeisterdienste") {
		t.Fatalf("Partner geändert: %v %s", n, text())
	}
}

// Verweis am Ziel (0.16.1): Bezug am abgelegten Dokument (z. B. „Objekt mit KI anlegen …“
// nach dem Ablegen) bekommt gleich seinen Verweis; fehlende trägt die Migration nach.
func TestAttachRefAfterFiling(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	e.run.text = "Prüfbericht"
	id := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("pb")), "file_name": "pb.pdf"}})["id"].(string)
	e.h.db.Exec(`UPDATE dms__document SET status = ?, name = 'pb.pdf', doc_type = 'REPORT' WHERE id = ?`, stFiled, id)
	d, err := e.m.loadDoc(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.m.insertRef(e.ctx, d, "BusinessEntity", "2000|Bro"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.h.attached); n != 1 || e.h.attached[0].EntityType != "BusinessEntity" || e.h.attached[0].EntityID != "2000|Bro" {
		t.Fatalf("Verweis: %v", e.h.attached)
	}
	if v := e.one(`SELECT doc_ref_id FROM dms__document_ref WHERE document_id = ? AND object = 'BusinessEntity'`, id); v != "R1" {
		t.Fatalf("doc_ref_id: %v", v)
	}
	// Bezug ohne Verweis (vor 0.16.1): Migration trägt nach
	e.h.db.Exec(`INSERT INTO dms__document_ref (document_id, line_no, company_code, object, object_key, is_active) VALUES (?, 9, '2000', 'Building', '2000|ROBro1', 1)`, id)
	if err := e.m.attachMissingRefs(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT doc_ref_id FROM dms__document_ref WHERE document_id = ? AND line_no = 9`, id); v != "R2" {
		t.Fatalf("nachgetragen: %v (%v)", v, e.h.attached)
	}
}
