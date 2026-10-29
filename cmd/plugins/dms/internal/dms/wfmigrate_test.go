package dms

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// Übernahme in Form 2: eigener Workflow aus den Parametern (Text → Eingabe mit
// Bedingung, Bestätigen → Ja/Nein, Wert setzen → inaktiv mit Hinweis), wartende
// Dokumente bleiben am Schritt und bekommen ihren Ablauf; ERP: 040 → 060.
func TestMigrateLegacyWorkflow(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "OLD", "name": "Alt"})
	e.create(stepObject, map[string]any{"workflow": "OLD", "step": "010", "name": "Titel", "kind": kInput, "input_type": inText, "target": "title"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "old", "name": "Alt", "path": e.in, "workflow": "OLD"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|OLD", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("alt")), "file_name": "a.pdf"}})
	id := fmt.Sprint(up["id"])
	// Form bis 0.11.x
	for _, q := range []string{
		`UPDATE dms__workflow SET format = NULL WHERE code = 'OLD'`,
		`UPDATE dms__step SET kind = 'TEXT', input_type = NULL WHERE workflow = 'OLD'`,
		`INSERT INTO dms__step (workflow, step, name, kind, is_active) VALUES ('OLD', '020', 'Ok?', 'CONFIRM', 1), ('OLD', '030', 'Setzen', 'SET', 1)`,
		`INSERT INTO dms__step_param (workflow, step, param, locale, value, is_active) VALUES
			('OLD', '010', 'question', '', 'Titel?', 1), ('OLD', '010', 'question', 'en', 'Title?', 1),
			('OLD', '010', 'when', '', 'ref:Contract', 1), ('OLD', '020', 'question', '', 'Passt?', 1)`,
		`INSERT INTO dms__step_option (workflow, step, outcome, next_step, is_active) VALUES ('OLD', '010', 'ENTERED', '020', 1), ('OLD', '020', 'YES', '030', 1)`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// ERP: Dokument wartete bei der früheren Bestätigung der Erkennung
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "erp", "name": "ERP", "path": e.in + "/erp", "workflow": "ERP"})
	up = e.must(inboxObject, "upload", map[string]any{"id": "2000|ERP", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("erp")), "file_name": "e.pdf"}})
	erpID := fmt.Sprint(up["id"])
	e.h.db.Exec(`UPDATE dms__document SET wf_step = '040' WHERE id = ?`, erpID)
	e.h.db.Exec(`DELETE FROM dms__document_step`)
	e.h.db.Exec(`UPDATE dms__workflow SET format = NULL WHERE code = 'ERP'`)

	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	steps := e.one(`SELECT GROUP_CONCAT(step || ':' || kind || ':' || COALESCE(input_type, '') || ':' || is_active, ',') FROM (SELECT * FROM dms__step WHERE workflow = 'OLD' ORDER BY step)`)
	if steps != "010:INPUT:TEXT:1,020:INPUT:YESNO:1,030:CHECK::0" {
		t.Fatalf("Schritte: %v", steps)
	}
	if q := e.one(`SELECT question FROM dms__step WHERE workflow = 'OLD' AND step = '010'`); q != "Titel?" {
		t.Errorf("Frage: %v", q)
	}
	if v := e.one(`SELECT value FROM dms__step_text WHERE workflow = 'OLD' AND step = '010' AND locale = 'en'`); v != "Title?" {
		t.Errorf("Übersetzung: %v", v)
	}
	if c := e.one(`SELECT role || ' ' || left_expr || ' ' || operator FROM dms__step_condition WHERE workflow = 'OLD' AND step = '010'`); c != "APPLIES {ref.Contract} NOT_EMPTY" {
		t.Errorf("Bedingung: %v", c)
	}
	if d := e.one(`SELECT description FROM dms__step WHERE workflow = 'OLD' AND step = '030'`); !strings.HasPrefix(fmt.Sprint(d), legacyInactiveTag) {
		t.Errorf("inaktiv: %v", d)
	}
	if f := e.one(`SELECT COUNT(*) FROM dms__workflow WHERE format = ?`, wfFormat); f != int64(3) {
		t.Errorf("Form 2: %v", f)
	}
	doc := e.must(docObject, "get", map[string]any{"id": id})
	if doc["wf_step"] != "010" || doc["wf_status"] != wfWaiting || e.one(`SELECT status FROM dms__document_step WHERE document_id = ? AND step = '010'`, id) != stepWaiting {
		t.Fatalf("wartendes Dokument: %v %v", doc["wf_step"], doc["wf_status"])
	}
	doc = e.must(docObject, "get", map[string]any{"id": erpID})
	if doc["wf_step"] != "060" || doc["wf_status"] != wfWaiting || !strings.Contains(fmt.Sprint(doc["wf_log"]), "wartet bei Schritt 060") {
		t.Fatalf("ERP: %v %v", doc["wf_step"], doc["wf_status"])
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__document_step WHERE document_id = ? AND status = ?`, erpID, stepDone); n == int64(0) {
		t.Errorf("Ablauf ERP ohne erledigte Schritte")
	}
	// wiederholbar: nichts mehr zu tun
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__step WHERE workflow = 'OLD'`); n != int64(3) {
		t.Errorf("zweiter Lauf: %v", n)
	}
}

// Probelauf: Workflow läuft für die Dokumente im Eingang, bleibt beim ersten
// Benutzerschritt ohne Vorschlag stehen, nichts wird geändert.
func TestDryRun(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "STANDARD"})
	e.run.text = "Bescheid vom 15.02.2025"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("probe")), "file_name": "p.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "NOTICE"}})
	before := e.must(docObject, "get", map[string]any{"id": id})
	steps := e.one(`SELECT COUNT(*) FROM dms__document_step WHERE document_id = ?`, id)
	r := e.must(workflowObject, "dryRun", map[string]any{"id": "STANDARD", "data": map[string]any{}})
	msg := fmt.Sprint(r["message"])
	rows := fmt.Sprint(r["table"].(map[string]any)["rows"])
	if !strings.Contains(msg, "Probelauf STANDARD, 1 Dokumente (nichts geändert)") || !strings.Contains(msg, "Schritt 030 Dokumentart wählen wartet: kein Vorschlag") ||
		!strings.Contains(rows, "Datum im Text suchen") {
		t.Fatalf("Probelauf: %s\n%s", msg, rows)
	}
	after := e.must(docObject, "get", map[string]any{"id": id})
	for _, k := range []string{"wf_step", "wf_status", "wf_log", "doc_type", "doc_date"} {
		if fmt.Sprint(before[k]) != fmt.Sprint(after[k]) {
			t.Errorf("%s geändert: %v → %v", k, before[k], after[k])
		}
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__document_step WHERE document_id = ?`, id); n != steps {
		t.Errorf("Ablauf geändert: %v → %v", steps, n)
	}
	_, err := e.call(workflowObject, "dryRun", map[string]any{"id": "ERP", "data": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "Keine Dokumente im Eingang") {
		t.Fatalf("ohne Dokumente: %v", err)
	}
}

// TestMigrateFormat4: Workflow ERP in Form 3 bekommt Schritt 142 „Vorhandene Rechnung
// wählen“, 140 führt dorthin; geänderte Workflows bleiben, wie sie sind.
func TestMigrateFormat4(t *testing.T) {
	e := setup(t)
	if _, err := e.m.ensureWorkflow(e.ctx, erpWorkflow); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM dms__step_condition WHERE workflow = 'ERP' AND step = '142'`, `DELETE FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '142'`,
		`DELETE FROM dms__step_text WHERE workflow = 'ERP' AND step = '142'`, `DELETE FROM dms__step_option WHERE workflow = 'ERP' AND step = '142'`,
		`DELETE FROM dms__step_param WHERE workflow = 'ERP' AND step = '142'`, `DELETE FROM dms__step WHERE workflow = 'ERP' AND step = '142'`,
		`UPDATE dms__step_option SET next_step = '145' WHERE workflow = 'ERP' AND step = '140'`, `UPDATE dms__workflow SET format = 3`} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT next_step FROM dms__step_option WHERE workflow = 'ERP' AND step = '140' AND outcome = 'SELECTED'`); n != "142" {
		t.Fatalf("140 →: %v", n)
	}
	if o := e.one(`SELECT GROUP_CONCAT(outcome || '>' || next_step, ',') FROM (SELECT * FROM dms__step_option WHERE workflow = 'ERP' AND step = '142' ORDER BY outcome)`); o != "SELECTED>160,SKIPPED>145" {
		t.Fatalf("142: %v", o)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '142' AND role = 'FILTER'`); n != int64(3) {
		t.Fatalf("Filter: %v", n)
	}
	// zweiter Lauf ändert nichts
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.one(`SELECT COUNT(*) FROM dms__step WHERE workflow = 'ERP' AND step = '142'`); n != int64(1) {
		t.Fatalf("142 doppelt: %v", n)
	}
}

// Form 5 (0.14.0): ERP und STANDARD bekommen 025–027 Duplikat suchen, wenn 020 noch auf 030 führt.
func TestMigrateFormat5(t *testing.T) {
	e := setup(t)
	e.company()
	for _, q := range []string{
		`DELETE FROM dms__step_option WHERE step IN ('025', '026', '027')`,
		`DELETE FROM dms__step_text WHERE step IN ('025', '026', '027')`,
		`DELETE FROM dms__step WHERE step IN ('025', '026', '027')`,
		`UPDATE dms__step_option SET next_step = '030' WHERE step = '020'`,
		`UPDATE dms__workflow SET format = 4`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	// STANDARD von Hand geändert: 020 führt woandershin – bleibt
	e.h.db.Exec(`UPDATE dms__step_option SET next_step = '040' WHERE workflow = 'STANDARD' AND step = '020' AND outcome = 'FAILED'`)
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT GROUP_CONCAT(step || ':' || kind, ',') FROM (SELECT * FROM dms__step WHERE workflow = 'ERP' AND step IN ('025', '026', '027') ORDER BY step)`); v != "025:DUPLICATE,026:INPUT,027:ACTION" {
		t.Fatalf("ERP: %v", v)
	}
	if v := e.one(`SELECT GROUP_CONCAT(DISTINCT next_step) FROM dms__step_option WHERE workflow = 'ERP' AND step = '020'`); v != "025" {
		t.Fatalf("ERP 020: %v", v)
	}
	if v := e.one(`SELECT COUNT(*) FROM dms__step WHERE workflow = 'STANDARD' AND step = '025'`); fmt.Sprint(v) != "0" {
		t.Fatalf("STANDARD geändert, sollte bleiben: %v", v)
	}
	if v := e.one(`SELECT COUNT(*) FROM dms__workflow WHERE format <> ?`, wfFormat); fmt.Sprint(v) != "0" {
		t.Fatalf("Form: %v", v)
	}
}

// Form 6 (0.16.2): ERP bekommt 141 Titel, wenn 140 noch auf 142 führt.
func TestMigrateFormat6(t *testing.T) {
	e := setup(t)
	e.company()
	for _, q := range []string{
		`DELETE FROM dms__step_option WHERE workflow = 'ERP' AND step = '141'`,
		`DELETE FROM dms__step_text WHERE workflow = 'ERP' AND step = '141'`,
		`DELETE FROM dms__step_condition WHERE workflow = 'ERP' AND step = '141'`,
		`DELETE FROM dms__step WHERE workflow = 'ERP' AND step = '141'`,
		`UPDATE dms__step_option SET next_step = '142' WHERE workflow = 'ERP' AND step = '140'`,
		`UPDATE dms__workflow SET format = 5`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT kind || ':' || target || ':' || if_set FROM dms__step WHERE workflow = 'ERP' AND step = '141'`); v != "INPUT:title:ASK" {
		t.Fatalf("141: %v", v)
	}
	if v := e.one(`SELECT next_step FROM dms__step_option WHERE workflow = 'ERP' AND step = '140'`); v != "141" {
		t.Fatalf("140 → %v", v)
	}
}

// TestMigrateFormat7: Schritt 142 bietet auch gebuchte Rechnungen an.
func TestMigrateFormat7(t *testing.T) {
	e := setup(t)
	e.company()
	for _, q := range []string{
		`UPDATE dms__step_mapping SET value = 'OPEN' WHERE workflow = 'ERP' AND step = '142' AND field = 'status'`,
		`UPDATE dms__step SET question = 'Gibt es die Rechnung schon? Offene Rechnungen von {partner}' WHERE workflow = 'ERP' AND step = '142'`,
		`UPDATE dms__workflow SET format = 6`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT value FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '142' AND field = 'status'`); v != invoiceStatuses {
		t.Fatalf("Filter: %v", v)
	}
	if v := e.one(`SELECT label_fields FROM dms__step WHERE workflow = 'ERP' AND step = '142'`); v != invoiceLabels {
		t.Fatalf("Anzeige: %v", v)
	}
	if v := e.one(`SELECT value FROM dms__step_text WHERE workflow = 'ERP' AND step = '142' AND text_key = 'question' AND locale = 'en'`); v != invoiceQuestion[1] {
		t.Fatalf("Frage en: %v", v)
	}
}

// TestMigrateFormat8: „Vorgang mit KI anlegen“ (120) legt auch Angebote an.
func TestMigrateFormat8(t *testing.T) {
	e := setup(t)
	e.company()
	for _, q := range []string{
		`DELETE FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '120' AND (field = 'PurchaseQuote' OR field LIKE 'quote.%')`,
		`UPDATE dms__workflow SET format = 7`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT value FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '120' AND role = 'REQUEST' AND field = 'PurchaseQuote'`); v != aiPurchaseQuote {
		t.Fatalf("Anfrage: %v", v)
	}
	if v := e.one(`SELECT COUNT(*) FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '120' AND field LIKE 'quote.%'`); fmt.Sprint(v) != "2" {
		t.Fatalf("Vorgaben: %v", v)
	}
	if v := e.one(`SELECT format FROM dms__workflow WHERE code = 'ERP'`); fmt.Sprint(v) != "8" {
		t.Fatalf("Form: %v", v)
	}
	// wiederholt: nichts doppelt
	if err := e.m.migrateWorkflows(e.ctx); err != nil {
		t.Fatal(err)
	}
	if v := e.one(`SELECT COUNT(*) FROM dms__step_mapping WHERE workflow = 'ERP' AND step = '120' AND field = 'PurchaseQuote'`); fmt.Sprint(v) != "1" {
		t.Fatalf("doppelt: %v", v)
	}
}
