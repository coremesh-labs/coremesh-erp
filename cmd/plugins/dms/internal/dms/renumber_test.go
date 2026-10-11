package dms

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Neu nummerieren: Schritte 010, 020 …; Optionen, undo_to, Startschritt und
// Dokumente im Workflow (Schritt, Verlauf für „Zurück“) mit.
func TestRenumberWorkflow(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "RN", "name": "Krumm"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "005", "name": "Titel", "kind": kText, "target": "note"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "007", "name": "Ok?", "kind": kConfirm})
	e.create(paramObject, map[string]any{"workflow": "RN", "step": "007", "param": "undo_to", "value": "005"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "012", "name": "Notiz", "kind": kText, "target": "title"})
	e.create(optionObject, map[string]any{"workflow": "RN", "step": "005", "outcome": oEntered, "next_step": "007"})
	e.create(optionObject, map[string]any{"workflow": "RN", "step": "007", "outcome": oYes, "next_step": "012"})
	e.create(optionObject, map[string]any{"workflow": "RN", "step": "007", "outcome": oNo, "next_step": "012"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "rn", "name": "RN", "path": e.in, "workflow": "RN"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|RN", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ren")), "file_name": "r.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_text": "eins"}})
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "007" {
		t.Fatalf("vorher: %v", d["wf_step"])
	}
	r := e.must(workflowObject, "renumber", map[string]any{"id": "RN"})
	if !strings.Contains(r["message"].(string), "3 Schritte neu nummeriert") {
		t.Fatalf("renumber: %v", r)
	}
	if s := e.one(`SELECT GROUP_CONCAT(step, ',') FROM (SELECT step FROM dms__step WHERE workflow = 'RN' ORDER BY step)`); s != "010,020,030" {
		t.Errorf("Schritte: %v", s)
	}
	if s := e.one(`SELECT GROUP_CONCAT(step || '>' || next_step, ',') FROM (SELECT step, next_step FROM dms__step_option WHERE workflow = 'RN' ORDER BY step, outcome)`); s != "010>020,020>030,020>030" {
		t.Errorf("Optionen: %v", s)
	}
	if v := e.one(`SELECT value FROM dms__step_param WHERE workflow = 'RN' AND param = 'undo_to'`); v != "010" {
		t.Errorf("undo_to: %v", v)
	}
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "020" {
		t.Errorf("Dokument: %v", d["wf_step"])
	}
	// „Zurück“ findet den umnummerierten Verlauf
	e.must(docObject, "back", map[string]any{"id": id})
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "010" {
		t.Errorf("zurück: %v", d["wf_step"])
	}
}
