package dms

import (
	"encoding/base64"
	"strings"
	"testing"
)

// Neu nummerieren: Schritte 010, 020 …; Optionen, Bedingungen, Startschritt und
// Dokumente im Workflow (Schritt, Ablauf, Verlauf für „Zurück“) mit.
func TestRenumberWorkflow(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "RN", "name": "Krumm"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "005", "name": "Titel", "kind": kInput, "input_type": inText, "target": "note"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "007", "name": "Ok?", "kind": kInput, "input_type": inYesNo})
	e.create(conditionObject, map[string]any{"workflow": "RN", "step": "007", "role": condApplies, "left_expr": "{note}", "operator": opNotEmpty})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "012", "name": "Notiz", "kind": kInput, "input_type": inText, "target": "title"})
	e.create(stepObject, map[string]any{"workflow": "RN", "step": "006", "name": "Alt", "kind": kInput, "input_type": inText, "target": "title"})
	e.must(stepObject, "deactivate", map[string]any{"id": "RN|006"})
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
	if !strings.Contains(r["message"].(string), "4 Schritte neu nummeriert") {
		t.Fatalf("renumber: %v", r)
	}
	if s := e.one(`SELECT GROUP_CONCAT(step, ',') FROM (SELECT step FROM dms__step WHERE workflow = 'RN' ORDER BY step)`); s != "010,020,030,040" { // inaktiver Schritt 006 am Ende
		t.Errorf("Schritte: %v", s)
	}
	if s := e.one(`SELECT GROUP_CONCAT(step || '>' || next_step, ',') FROM (SELECT step, next_step FROM dms__step_option WHERE workflow = 'RN' ORDER BY step, outcome)`); s != "010>020,020>030,020>030" {
		t.Errorf("Optionen: %v", s)
	}
	if v := e.one(`SELECT step FROM dms__step_condition WHERE workflow = 'RN'`); v != "020" {
		t.Errorf("Bedingung: %v", v)
	}
	if v := e.one(`SELECT GROUP_CONCAT(step || ':' || status, ',') FROM (SELECT step, status FROM dms__document_step WHERE document_id = ? ORDER BY step)`, id); v != "010:DONE,020:WAITING,030:OPEN" {
		t.Errorf("Ablauf: %v", v)
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
