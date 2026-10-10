package dms

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	p := nameParts{"account": "3.0.1", "company": "VV", "object": "ROGoer14", "date": "20250110", "doctype": "NOTICE", "partner": "Stadt Görlitz"}
	if n := buildName(defaultPattern, p); n != "3.0.1:VV:ROGoer14_20250110_NOTICE:Stadt-Görlitz" {
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
	e.run.text = "Stadt Görlitz – Grundsteuerbescheid vom 10.01.2025 für das Grundstück Biesnitzer Str. 14"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "goerlitz", "name": "Görlitz", "path": e.in, "workflow": "STANDARD"})
	for _, p := range [][2]string{{"Building", "ROGoer14"}, {docTypeObject, "notice"}, {accountObject, "3.0.1"}, {"BusinessPartner", "P1"},
		{"Contract", "GS-2026-0009"}} {
		e.create(inboxParamObject, map[string]any{"company_code": "2000", "inbox": "GOERLITZ", "object": p[0], "value": p[1]})
	}
	content := pdf("Grundsteuer")
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|GOERLITZ", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(content), "file_name": "scan 0001.pdf", "file_type": "application/pdf"}})
	id := up["id"].(string)
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["status"] != stInbox || doc["doc_date"] != "2025-01-10" || doc["doc_type"] != "NOTICE" || doc["file_account"] != "3.0.1" ||
		doc["wf_status"] != wfWaiting || doc["wf_step"] != "070" || doc["wf_question"] != "Titel des Dokuments?" {
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
	if fs["message"] != "Titel des Dokuments?" {
		t.Fatalf("FormState: %v", fs)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_text": "Grundsteuerbescheid 2025"}})
	// Bestätigen: Ja/Nein als Auswahl.
	fs = e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	if opts := fs["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any); len(opts) != 2 {
		t.Fatalf("Ja/Nein: %v", fs)
	}
	r := e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "YES"}})
	if !strings.Contains(r["message"].(string), "abgelegt als 3.0.1:VV:ROGoer14_20250110_NOTICE:Stadt-Görlitz.pdf") {
		t.Fatalf("Ablegen: %v", r)
	}
	doc = e.must(docObject, "get", map[string]any{"id": id})
	want := filepath.Join(e.root, "3 Steuern Lasten Abgaben", "3.0 Grundsteuer", "3.0.1 Grundsteuerbescheide (Jahresbescheide)",
		"3.0.1:VV:ROGoer14_20250110_NOTICE:Stadt-Görlitz.pdf")
	if doc["status"] != stFiled || doc["wf_status"] != wfDone || doc["retain_until"] != nil || doc["link"] != "/file/DmsDocument/"+id ||
		fmt.Sprint(doc["_hidden_sections"]) != "[schritt]" {
		t.Fatalf("Abgelegt: %v", doc)
	}
	fi, err := os.Stat(want)
	if err != nil || fi.Mode().Perm() != 0o444 {
		t.Fatalf("Datei im Archiv: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(e.in, "scan 0001.pdf")); !os.IsNotExist(err) {
		t.Fatalf("Datei im Eingang sollte weg sein: %v", err)
	}
	c = e.must(docObject, metamodel.ContentAction, map[string]any{"id": "3.0.1:VV:ROGoer14_20250110_NOTICE:Stadt-Görlitz.pdf"})
	if b, _ := base64.StdEncoding.DecodeString(c["content"].(string)); !bytes.Equal(b, content) {
		t.Fatal("Inhalt aus dem Archiv (über den Dokumentnamen)")
	}
	// Verweise am Gebäude, am Partner und am Vertrag – mit Dokumentname und Link.
	if len(e.h.attached) != 3 || e.h.attached[0].EntityType != "Building" || e.h.attached[0].EntityID != "2000|ROGoer14" ||
		e.h.attached[1].EntityType != "BusinessPartner" || e.h.attached[2].EntityID != "2000|GS-2026-0009" ||
		e.h.attached[2].Document.Link != "/file/DmsDocument/"+id || e.h.attached[2].Document.Location != "3.0.1:VV:ROGoer14_20250110_NOTICE:Stadt-Görlitz.pdf" ||
		e.h.attached[2].Document.DocType != "NOTICE" || e.h.attached[2].Document.Title != "Grundsteuerbescheid 2025" {
		t.Fatalf("Verweise: %+v", e.h.attached)
	}
	// Kopf fest, Notiz änderbar.
	_, err = e.call(docObject, "update", map[string]any{"id": id, "data": map[string]any{"title": "anders"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kopf nach Ablage")
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"note": "Original im Ordner"}})
	// Gleicher Inhalt erneut: schon bekannt.
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|GOERLITZ", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(content), "file_name": "nochmal.pdf"}})
	if up["duplicate"] != true || up["id"] != id {
		t.Fatalf("Doppelt: %v", up)
	}
	// Dauerhaft aufzubewahren: Löschen verweigert.
	_, err = e.call(docObject, "remove", map[string]any{"id": id})
	expect(t, err, sdk.ErrInvalidArgument, "Löschen ohne Fristablauf")
	// Volltextsuche im erkannten Text.
	if l := items(e.must(docObject, "list", map[string]any{"query": map[string]any{"q": "biesnitzer"}})); len(l) != 1 {
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
	r := e.must(setupObject, "scan", map[string]any{"company": "2000", "inbox": "post"})
	if r["new"] != float64(1) {
		t.Fatalf("Einlesen: %v", r)
	}
	if r = e.must(setupObject, "scan", map[string]any{"company": "2000", "inbox": "post"}); r["duplicate"] != float64(1) {
		t.Fatalf("Zweites Einlesen: %v", r)
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
	if r["name"] != "4.2.0:VV:ER-00007_20200301_INVOICE.pdf" {
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
	if _, err := os.Stat(filepath.Join(e.root, "4 Finanzen Bankwesen", "4.2 Buchhaltung", "4.2.0 Eingangsrechnungen (Kreditorisch)", "4.2.0:VV:ER-00007_20200301_INVOICE.pdf")); !os.IsNotExist(err) {
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
	e.create(stepObject, map[string]any{"workflow": "KI", "step": "10", "name": "Art per KI", "kind": kAI, "target": "doc_type"})
	for _, p := range [][3]string{{"prompt", "de", "Welche Art? {options}\n{text}"}, {"options", "", "INVOICE=Rechnung; NOTICE=Bescheid"}} {
		e.create(paramObject, map[string]any{"workflow": "KI", "step": "10", "param": p[0], "locale": p[1], "value": p[2]})
	}
	e.create(stepObject, map[string]any{"workflow": "KI", "step": "20", "name": "Notiz", "kind": kSet, "target": "note"})
	e.create(paramObject, map[string]any{"workflow": "KI", "step": "20", "param": "value", "value": "aus {file_name}"})
	e.create(optionObject, map[string]any{"workflow": "KI", "step": "10", "outcome": oSuccess, "next_step": "20"})
	r := e.must(workflowObject, "check", map[string]any{"id": "KI"})
	if !strings.Contains(r["message"].(string), "1 Hinweise") {
		t.Fatalf("Prüfen: %v", r)
	}
	e.must(workflowObject, "copy", map[string]any{"id": "KI", "data": map[string]any{"copy_code": "ki2"}})
	if n := e.one(`SELECT COUNT(*) FROM dms__step_param WHERE workflow = 'KI2'`); n != int64(3) {
		t.Fatalf("Kopie Parameter: %v", n)
	}
	e.ai.answer = "INVOICE"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "ki", "name": "KI", "path": e.in, "workflow": "KI2"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|KI", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ki")), "file_name": "r.pdf"}})
	doc := e.must(docObject, "get", map[string]any{"id": up["id"]})
	if doc["doc_type"] != "INVOICE" || doc["note"] != "aus r.pdf" || doc["wf_status"] != wfDone {
		t.Fatalf("KI: %v", doc)
	}
	if !strings.Contains(e.ai.body, "Rechnung") || !strings.Contains(e.ai.body, "Welche Art?") {
		t.Fatalf("Prompt: %s", e.ai.body)
	}
	// Antwort außerhalb der Auswahl: Schritt nicht erfolgreich, Workflow hält an.
	e.ai.answer = "UNSINN"
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|KI", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ki2")), "file_name": "s.pdf"}})
	if doc = e.must(docObject, "get", map[string]any{"id": up["id"]}); doc["wf_status"] != wfFailed {
		t.Fatalf("KI falsch: %v", doc)
	}
}

func TestArchiveStaysInRoot(t *testing.T) {
	a := fileArchive{root: t.TempDir()}
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
