package dms

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeywordWorkflow (0.27.0): Eingang mit Unterverzeichnissen; Stichworte ergeben
// Dokumentart und Bezug (der längere Begriff mit mehr Punkten gewinnt), die Ablage
// folgt aus Objektart × Dokumentart; ein Stichwort mit Weiterleitungsziel leitet weiter,
// seit 0.28.0 nur, wenn es mindestens so viele Punkte hat wie der Bezug ({var.score_…}).
func TestKeywordWorkflow(t *testing.T) {
	e := setup(t)
	e.company()
	fwd := filepath.Join(t.TempDir(), "vuv")
	e.create(forwardObject, map[string]any{"company_code": "2000", "code": "VUV", "name": "VuV", "path": fwd})
	e.must(accountCCObject, "update", map[string]any{"id": "2000|3.0.1", "data": map[string]any{"company_code": "2000", "number": "3.0.1",
		"asset_types": "PERS_STEUER", "doc_types": "NOTICE"}})
	kw := func(code, phrase, docType, ref, forward string, points int) {
		data := map[string]any{"company_code": "2000", "code": code, "phrase": phrase, "doc_type": docType, "forward": forward, "points": points}
		if ref != "" {
			data["ref_object"], data["ref_key"] = "Asset", ref
		}
		e.create(keywordObject, data)
	}
	kw("ESTB", "Einkommensteuerbescheid", "NOTICE", "PS001", "", 7)
	kw("STEUER", "Steuer", "OTHER", "", "", 3)
	kw("HAUSGELD", "Hausgeldabrechnung", "", "", "VUV", 5)

	w := "PRIV"
	e.create(workflowObject, map[string]any{"code": w, "name": "Privat"})
	step := func(no, kind string, data map[string]any) {
		data["workflow"], data["step"], data["name"], data["kind"] = w, no, no, kind
		e.create(stepObject, data)
	}
	opt := func(no, outcome, next string) {
		e.create(optionObject, map[string]any{"workflow": w, "step": no, "outcome": outcome, "next_step": next})
	}
	step("10", kCommand, map[string]any{"command": "PDFTEXT"})
	step("20", kRecognize, map[string]any{"rules": "doc_date, kw_doc_type, kw_ref, kw_forward", "apply": applySet, "min_score": 3})
	step("30", kCheck, map[string]any{})
	e.create(conditionObject, map[string]any{"workflow": w, "step": "30", "role": condCheck, "left_expr": "{var.forward}", "operator": "NOT_EMPTY"})
	e.create(conditionObject, map[string]any{"workflow": w, "step": "30", "line_no": 20, "role": condCheck, "left_expr": "{var.score_forward}", "operator": "GE",
		"right_expr": "{var.score_ref}"})
	step("40", kAction, map[string]any{"action_object": docObject, "action_name": "forward"})
	e.create(mappingObject, map[string]any{"workflow": w, "step": "40", "role": mapParam, "field": "forward_target", "value": "{var.forward}"})
	step("50", kAction, map[string]any{"action_object": docObject, "action_name": "takeover"})
	step("60", kAction, map[string]any{"action_object": docObject, "action_name": "file"})
	for _, o := range [][3]string{{"10", oSuccess, "20"}, {"20", oSuccess, "30"}, {"20", oFailed, "30"}, {"30", oYes, "40"}, {"30", oNo, "50"},
		{"50", oSuccess, "60"}, {"50", oFailed, "60"}} {
		opt(o[0], o[1], o[2])
	}
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "PRIV", "name": "Privat", "path": e.in, "workflow": w, "subdirs": true})
	sub := filepath.Join(e.in, "Belege", "2025")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sub, "estb.pdf"), pdf("estb"), 0o644)
	os.MkdirAll(filepath.Join(e.in, ".versteckt"), 0o755)
	os.WriteFile(filepath.Join(e.in, ".versteckt", "x.pdf"), pdf("x"), 0o644)
	e.run.text = "Finanzamt Leipzig\nEinkommensteuerbescheid für 2024 vom 30.06.2025 – Steuer festgesetzt, Anlage V: Hausgeldabrechnung"
	r := e.must(inboxObject, "scan", map[string]any{"id": "2000|PRIV"})
	if !strings.Contains(r["message"].(string), "1 neue") {
		t.Fatalf("Scan: %v", r)
	}
	id := e.one(`SELECT id FROM dms__document WHERE file_name = 'estb.pdf'`)
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["status"] != stFiled || d["doc_type"] != "NOTICE" || d["file_account"] != "3.0.1" || !strings.Contains(fmt.Sprint(d["wf_log"]), `{var.score_forward} = "5"`) {
		t.Fatalf("Bescheid: %v %v %v\n%s", d["status"], d["doc_type"], d["file_account"], d["wf_log"])
	}
	if _, err := os.Stat(filepath.Join(sub, "estb.pdf")); !os.IsNotExist(err) {
		t.Fatal("Datei im Eingang nach dem Ablegen noch vorhanden")
	}

	os.WriteFile(filepath.Join(e.in, "hg.pdf"), pdf("hg"), 0o644)
	e.run.text = "Hausgeldabrechnung 2025 der WEG"
	e.must(inboxObject, "scan", map[string]any{"id": "2000|PRIV"})
	d = e.must(docObject, "get", map[string]any{"id": e.one(`SELECT id FROM dms__document WHERE file_name = 'hg.pdf'`)})
	if d["status"] != stForwarded {
		t.Fatalf("Hausgeld: %v\n%s", d["status"], d["wf_log"])
	}
	if _, err := os.Stat(filepath.Join(fwd, "hg.pdf")); err != nil {
		t.Fatalf("weitergeleitet: %v", err)
	}
}
