package dms

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func (e *env) formState(id string) metamodel.FormState {
	e.t.Helper()
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: docObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "continue", "id": id}})
	if err != nil {
		e.t.Fatal(err)
	}
	var st metamodel.FormState
	if err := sdk.Decode(resp.Payload, &st); err != nil {
		e.t.Fatal(err)
	}
	return st
}

// press: Knopf des wartenden Schritts.
func (e *env) press(id, button string, data map[string]any) map[string]any {
	e.t.Helper()
	if data == nil {
		data = map[string]any{}
	}
	return e.must(docObject, "continue", map[string]any{"id": id, "button": button, "data": data})
}

func buttonValues(st metamodel.FormState) []string {
	var out []string
	for _, b := range st.Buttons {
		out = append(out, b.Value+"="+b.Label)
	}
	return out
}

func (e *env) aiCount(action string) int {
	n := 0
	for _, c := range e.h.aiCalls {
		if c.Action == action {
			n++
		}
	}
	return n
}

func (e *env) lastAI(action string) map[string]any {
	for i := len(e.h.aiCalls) - 1; i >= 0; i-- {
		if e.h.aiCalls[i].Action == action {
			return e.h.aiCalls[i].Payload.(map[string]any)
		}
	}
	return nil
}

// Auswahl mit Knopf „Partner anlegen“ → Maske: KI füllt vor (Antwort im
// Zwischenspeicher), Maske zeigt die Felder, „Anlegen“ legt den Partner an und
// setzt ihn am Dokument; Variable aus einer Auswahl als Platzhalter im Kontext.
func TestFormStep(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "AIW", "name": "KI-Anlage"})
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "10", "name": "Art", "kind": kSelect, "target": "var", "var_name": "kind", "source_kind": srcList})
	for _, v := range [][2]string{{"DL", "Dienstleistung"}, {"VS", "Versicherung"}} {
		e.create(mappingObject, map[string]any{"workflow": "AIW", "step": "10", "role": mapValue, "field": v[0], "value": v[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "20", "name": "Partner", "kind": kSelect, "target": "partner_id",
		"source_kind": srcObject, "source_object": "BusinessPartner", "value_field": "id", "label_fields": "name1"})
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "25", "name": "Partner anlegen", "kind": kForm, "target": "partner_id",
		"context": "Vertragsart: {var.kind} {var.kind_label}"})
	e.create(mappingObject, map[string]any{"workflow": "AIW", "step": "25", "role": mapRequest, "field": "BusinessPartner", "value": "BUSINESS_PARTNER"})
	e.create(mappingObject, map[string]any{"workflow": "AIW", "step": "25", "role": mapDefault, "field": "partner.type", "value": "ORGANIZATION"})
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "30", "name": "Titel", "kind": kInput, "target": "title", "input_type": inText})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "10", "outcome": oSelected, "next_step": "20"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "20", "outcome": oSelected, "next_step": "30"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "20", "outcome": oCreate, "next_step": "25", "label": "Partner anlegen"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "25", "outcome": oCreated, "next_step": "30"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "aiw", "name": "KI", "path": e.in, "workflow": "AIW"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|AIW", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("aiw")), "file_name": "a.pdf"}})
	id := fmt.Sprint(up["id"])
	// keine Vorauswahl: Pflichtauswahl ohne Wert
	if st := e.formState(id); st.Fields["wf_choice"].Value != nil {
		t.Fatalf("vorausgewählt: %v", *st.Fields["wf_choice"].Value)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "DL"}})

	// Partnerauswahl: Knöpfe Weiter und „Partner anlegen“
	st := e.formState(id)
	if b := strings.Join(buttonValues(st), ";"); b != "SELECTED=Weiter;CREATE=Partner anlegen" {
		t.Fatalf("Knöpfe: %s", b)
	}
	e.press(id, oCreate, nil)

	// Maske: Vorbelegung mit Kontext, Felder, Knöpfe
	run := e.lastAI("run")
	if run["request"] != "BUSINESS_PARTNER" || !strings.HasPrefix(fmt.Sprint(run["context"]), "Vertragsart: DL Dienstleistung") {
		t.Fatalf("Ai.run: %v", run)
	}
	st = e.formState(id)
	if len(st.Extra) != 1 || st.Extra[0].Key != "f__partner__name1" || *st.Fields["f__partner__name1"].Value != "Neue GmbH" ||
		!strings.Contains(st.Message, "von der KI vorbelegt") || strings.Join(buttonValues(st), ";") != "CREATED=Anlegen;_ai_refresh=KI neu fragen" {
		t.Fatalf("Maske: %+v", st)
	}
	// zurück und wieder hin: Antwort aus dem Zwischenspeicher; „KI neu fragen“ fragt erneut
	runs := e.aiCount("run")
	e.must(docObject, "back", map[string]any{"id": id})
	e.press(id, oCreate, nil)
	if e.aiCount("run") != runs {
		t.Fatalf("Zwischenspeicher: %d → %d", runs, e.aiCount("run"))
	}
	e.press(id, btnAIRefresh, nil)
	if e.aiCount("run") != runs+1 {
		t.Fatalf("neu fragen: %d → %d", runs, e.aiCount("run"))
	}
	r := e.press(id, oCreated, map[string]any{"f__partner__name1": "Neue Muster GmbH"})
	if r["status"] == "FAILED" {
		t.Fatalf("anlegen: %v", r)
	}
	if e.lastAI("collect")["values"].(map[string]any)["f__partner__name1"] != "Neue Muster GmbH" ||
		e.lastAI("create")["defaults"].(map[string]any)["partner.type"] != "ORGANIZATION" {
		t.Fatalf("collect/create: %v %v", e.lastAI("collect"), e.lastAI("create"))
	}
	if doc := e.must(docObject, "get", map[string]any{"id": id}); doc["partner_id"] != "BPNEW" || doc["wf_step"] != "30" {
		t.Fatalf("Partner am Dokument: %v %v", doc["partner_id"], doc["wf_step"])
	}

	// nachträglich: Auswahl aus den Masken, Maske, Anlegen
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: docObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "aiCreateLater", "id": id}})
	var st0 metamodel.FormState
	if err != nil || sdk.Decode(resp.Payload, &st0) != nil || len(st0.Fields["ai_choice"].Options) != 1 || st0.Fields["ai_choice"].Options[0].Value != "25|BusinessPartner|BUSINESS_PARTNER" {
		t.Fatalf("Auswahl: %v %+v", err, st0)
	}
	resp, _ = e.p.Handle(e.ctx, sdk.Request{Object: docObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "aiCreateLater", "id": id,
		"values": map[string]any{"ai_choice": "25|BusinessPartner|BUSINESS_PARTNER"}}})
	_ = sdk.Decode(resp.Payload, &st0)
	if len(st0.Extra) != 1 || st0.Buttons != nil {
		t.Fatalf("Maske nachträglich: %+v", st0)
	}
	creates := e.aiCount("create")
	r = e.must(docObject, "aiCreateLater", map[string]any{"id": id, "data": map[string]any{"ai_choice": "25|BusinessPartner|BUSINESS_PARTNER", "f__partner__name1": "Nachtrag GmbH"}})
	if e.aiCount("create") != creates+1 || !strings.Contains(fmt.Sprint(r["message"]), "BPNEW") {
		t.Fatalf("nachträglich angelegt: %v", r)
	}
}

// Rechnung zum Vertrag: Frage „Vertragspositionen übernehmen?“ – Ja: KI nur Kopf
// (Anfrage bei Positionen aus der Vorbelegung), Positionen aus dem Vertrag; Nein: KI
// liest alles (Antwort vom Betreten der Maske gespeichert), nur der Kopf kommt aus dem Vertrag.
func TestContractItemsPrefill(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "PFW", "name": "Vertrag"})
	e.create(stepObject, map[string]any{"workflow": "PFW", "step": "10", "name": "Frage", "kind": kSelect, "target": "var", "var_name": "contract_items", "source_kind": srcList})
	for _, v := range [][2]string{{"YES", "Ja"}, {"NO", "Nein"}} {
		e.create(mappingObject, map[string]any{"workflow": "PFW", "step": "10", "role": mapValue, "field": v[0], "value": v[1]})
	}
	e.create(conditionObject, map[string]any{"workflow": "PFW", "step": "10", "role": condApplies, "left_expr": "{ref.Contract}", "operator": opNotEmpty})
	e.create(conditionObject, map[string]any{"workflow": "PFW", "step": "10", "role": condApplies, "left_expr": "{ref.SupplierInvoice}", "operator": opEmpty})
	e.create(stepObject, map[string]any{"workflow": "PFW", "step": "20", "name": "Rechnung", "kind": kForm, "target": "ref"})
	for _, p := range [][3]string{{mapRequest, "SupplierInvoice", "SUPPLIER_INVOICE"}, {mapPrefill, "SupplierInvoice", "SupplierInvoice.contractDraft"},
		{mapRequestPrefill, "SupplierInvoice", "SUPPLIER_INVOICE_HEAD"}} {
		e.create(mappingObject, map[string]any{"workflow": "PFW", "step": "20", "role": p[0], "field": p[1], "value": p[2]})
	}
	e.create(conditionObject, map[string]any{"workflow": "PFW", "step": "20", "role": condPrefill, "left_expr": "{var.contract_items}", "operator": opEq, "right_expr": "YES"})
	e.create(optionObject, map[string]any{"workflow": "PFW", "step": "10", "outcome": oSelected, "next_step": "20"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pfw", "name": "V", "path": e.in, "workflow": "PFW"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PFW", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("pfw")), "file_name": "v.pdf"}})
	d, err := e.m.loadDoc(e.ctx, fmt.Sprint(up["id"]))
	if err != nil {
		t.Fatal(err)
	}
	q, _ := e.m.loadStep(e.ctx, "PFW", "10")
	if ok, _ := e.m.stepApplies(e.ctx, d, q); ok {
		t.Fatal("Frage ohne Vertrag")
	}
	if err := e.m.insertRef(e.ctx, d, "Contract", "2000|DL-1"); err != nil {
		t.Fatal(err)
	}
	if ok, why := e.m.stepApplies(e.ctx, d, q); !ok {
		t.Fatalf("Frage mit Vertrag: %s", why)
	}
	s, _ := e.m.loadStep(e.ctx, "PFW", "20")
	_ = e.m.setWfVar(e.ctx, d.ID, "var.contract_items", "YES")
	e.m.aiPrefill(e.ctx, d, s)
	st := e.m.aiStateOf(e.ctx, d, s)
	if e.lastAI("run")["request"] != "SUPPLIER_INVOICE_HEAD" || st.Request != "SUPPLIER_INVOICE" || len(st.Result["items"].([]any)) != 1 {
		t.Fatalf("Ja: %v %+v", e.lastAI("run")["request"], st)
	}
	_ = e.m.setWfVar(e.ctx, d.ID, "var.contract_items", "NO")
	e.m.aiPrefill(e.ctx, d, s)
	st = e.m.aiStateOf(e.ctx, d, s)
	if inv, _ := st.Result["invoice"].(map[string]any); st.RunRequest != "" || st.Result["items"] != nil || inv["framework_contract"] != "DL-1" {
		t.Fatalf("Nein: %v %+v", e.lastAI("run")["request"], st)
	}
}

// Geschäftspartner anlegen im Workflow ERP (Rechnung mit Briefkopf als Bild):
// Hinweise aus Dokument und Kontoauszug im Kontext und in der Maske, Datei an die
// KI nur auf Knopfdruck, Vorschau mit Ja/Nein, Rückmeldung über dem nächsten Schritt.
func TestPartnerCreateWithPreview(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	e.run.text = "Herr Lehmann\nR E C H N U N G - Nr.: 250526  Datum: 07.05.2026\n123/456/78901\nIBAN DE12 5001 0517 0648 4898 90\nIBAN DE89370400440532013000"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("gerlach")), "file_name": "g.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})

	// 060: Knöpfe Partner übernehmen, GP anlegen, ohne Partner weiter
	st := e.formState(id)
	if b := strings.Join(buttonValues(st), ";"); b != "SELECTED=Partner übernehmen;CREATE=GP anlegen;SKIPPED=Ohne Partner weiter" {
		t.Fatalf("060: %s", b)
	}
	e.press(id, oCreate, nil)

	// 070: KI mit Text (ohne Datei), Kontext mit Hinweisen (eigenes Konto nicht)
	run := e.lastAI("run")
	ctx := fmt.Sprint(run["context"])
	if run["input"] != nil || !strings.Contains(ctx, "Zahlungen an die IBAN DE89370400440532013000 gingen laut Kontoauszug an: Sanitär Altmayer") ||
		!strings.Contains(ctx, "Steuernummer des Absenders: 123/456/78901") {
		t.Fatalf("Ai.run: %v", run)
	}
	vars := fmt.Sprint(e.one(`SELECT wf_vars FROM dms__document WHERE id = ?`, id))
	if !strings.Contains(vars, `"banks":[{"account_holder":"Sanitär Altmayer","iban":"DE89370400440532013000"}]`) || !strings.Contains(vars, `"name1":"Neue GmbH"`) {
		t.Fatalf("Vorbelegung: %s", vars)
	}
	st = e.formState(id)
	if b := strings.Join(buttonValues(st), ";"); b != "CREATED=Vorschau;_ai_file=KI liest die Datei;_ai_refresh=KI neu fragen" ||
		!strings.Contains(st.Message, "„KI liest die Datei“") || !strings.Contains(st.Message, "„Vorschau“ zeigt") ||
		!strings.Contains(st.Message, "aus dem Dokument: banks.iban, banks.account_holder") {
		t.Fatalf("Maske: %s – %s", b, st.Message)
	}

	// Datei an die KI auf Nachfrage
	e.press(id, btnAIFile, nil)
	if run = e.lastAI("run"); run["content_type"] != "application/pdf" || run["input"] != base64.StdEncoding.EncodeToString(pdf("gerlach")) || run["text"] == nil {
		t.Fatalf("Datei an die KI: %v", run)
	}
	if st = e.formState(id); strings.Contains(strings.Join(buttonValues(st), ";"), btnAIFile) {
		t.Fatalf("Knopf nach dem Lesen: %v", buttonValues(st))
	}

	// Vorschau, nichts angelegt; Nein → Maske mit den Eingaben
	e.press(id, oCreated, map[string]any{"f__partner__name1": "Sanitär Altmayer"})
	st = e.formState(id)
	if e.aiCount("create") != 0 || !strings.Contains(st.Message, "Vorschau") || strings.Join(buttonValues(st), ";") != "_yes=Ja, so anlegen;_no=Nein, zurück zur Bearbeitung" ||
		st.Fields["f__partner__name1"].ReadOnly == nil || !*st.Fields["f__partner__name1"].ReadOnly {
		t.Fatalf("Vorschau: %d %+v", e.aiCount("create"), st)
	}
	e.press(id, btnPreviewNo, nil)
	if st = e.formState(id); e.aiCount("create") != 0 || strings.Contains(st.Message, "Vorschau:") {
		t.Fatalf("Nein: %+v", st)
	}
	// Zurück aus der Vorschau: Maske
	e.press(id, oCreated, map[string]any{"f__partner__name1": "Sanitär Altmayer"})
	e.must(docObject, "back", map[string]any{"id": id})
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "070" || e.aiCount("create") != 0 {
		t.Fatalf("Zurück aus der Vorschau: %v", d["wf_step"])
	}
	// Ja → angelegt, Rückmeldung über dem nächsten Schritt
	e.press(id, oCreated, map[string]any{"f__partner__name1": "Sanitär Altmayer"})
	e.press(id, btnPreviewOK, nil)
	d := e.must(docObject, "get", map[string]any{"id": id})
	if e.aiCount("create") != 1 || d["partner_id"] != "BPNEW" || !strings.Contains(fmt.Sprint(d["wf_log"]), "Geschäftspartner BPNEW (BPNEW) angelegt") {
		t.Fatalf("angelegt: %d %v\n%s", e.aiCount("create"), d["partner_id"], d["wf_log"])
	}
	if st = e.formState(id); !strings.HasPrefix(st.Message, "✔ Geschäftspartner BPNEW (BPNEW) angelegt und am Dokument eingetragen – ") {
		t.Fatalf("Rückmeldung: %s", st.Message)
	}
}

// Erkennung über den Kontoauszug: Umsatz mit der IBAN und zugeordnetem Partner.
func TestIdentifyByBankTransaction(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	e.run.text = "Rechnung vom 01.02.2026, bitte auf DE75 5121 0800 1245 1261 99"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("bank")), "file_name": "b.pdf"}})
	d := e.must(docObject, "get", map[string]any{"id": up["id"]})
	if p := e.m.proposal(e.ctx, fmt.Sprint(up["id"]), "partner_id"); p == nil || p.Value != "P2" ||
		!strings.Contains(fmt.Sprint(d["wf_log"]), "Wagner (P2) (Name laut Kontoauszug, Partnername im Kontoauszug; 9 Punkte)") {
		t.Fatalf("erkannt: %v\n%s", p, d["wf_log"])
	}
}

// Neustart verwirft den Stand der Maske, die Antwort der KI bleibt im Zwischenspeicher.
func TestAIStateResetOnRestart(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	e.run.text = "Rechnung vom 12.02.2026, IBAN DE89370400440532013000"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("neu")), "file_name": "n.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})
	e.press(id, oCreate, nil)
	e.press(id, btnAIFile, nil)
	if v := fmt.Sprint(e.one(`SELECT wf_vars FROM dms__document WHERE id = ?`, id)); !strings.Contains(v, `"file":true`) {
		t.Fatalf("KI-Stand: %s", v)
	}
	e.must(docObject, "restart", map[string]any{"id": id, "data": map[string]any{}})
	if v := e.one(`SELECT COALESCE(wf_vars, '') FROM dms__document WHERE id = ?`, id); strings.Contains(fmt.Sprint(v), "ai.070") {
		t.Fatalf("nach Neustart: %v", v)
	}
	runs := e.aiCount("run")
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})
	e.press(id, oCreate, nil)
	if e.aiCount("run") != runs || !strings.Contains(e.formState(id).Message, "gespeicherte Antwort") {
		t.Fatalf("KI nach Neustart erneut gefragt: %d → %d", runs, e.aiCount("run"))
	}
}

// Neustart und „Von vorn“: Partner, Kategorie und Bezüge wie beim Einlesen; Partnerauswahl bei
// Rechnungen nur mit Kreditor-Rolle (Filter je Kategorie).
func TestRestartResetsAndPartnerFilter(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	e.run.text = "Rechnung vom 12.02.2026"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("filter")), "file_name": "f.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "INVOICE"}})
	st := e.formState(id)
	opts := st.Fields["wf_choice"].Options
	if len(opts) != 2 || opts[0].Value != "P2" || opts[1].Value != "P3" || !strings.Contains(st.Message, "nur Partner mit Kreditor-Rolle") {
		t.Fatalf("Auswahl Rechnung: %+v – %s", opts, st.Message)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "P3"}})
	for _, action := range []string{"restart", "startOver"} {
		e.must(docObject, action, map[string]any{"id": id, "data": map[string]any{}})
		d := e.must(docObject, "get", map[string]any{"id": id})
		if d["partner_id"] != nil || d["doc_category"] != nil || d["wf_step"] != "050" {
			t.Fatalf("%s: %v %v %v", action, d["partner_id"], d["doc_category"], d["wf_step"])
		}
		e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})
		// Vertrag: keine Einschränkung
		if st = e.formState(id); len(st.Fields["wf_choice"].Options) != len(testPartners) || strings.Contains(st.Message, "eingeschränkt") {
			t.Fatalf("Auswahl Vertrag: %+v", st.Fields["wf_choice"].Options)
		}
		e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "P1"}})
	}
}

// Konfiguration statt fester Regeln: Hinweis aus dem Katalog abschalten, Mindestpunkte
// am Schritt, Felder, die ein Neustart behält.
func TestConfigurableRules(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	upload := func(tag, text string) string {
		e.run.text = text
		up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
			"file": base64.StdEncoding.EncodeToString(pdf(tag)), "file_name": tag + ".pdf"}})
		return fmt.Sprint(up["id"])
	}
	// Erkennung über den Kontoauszug abgeschaltet: Hinweis payee inaktiv
	e.must(hintObject, "deactivate", map[string]any{"id": "payee"})
	id := upload("aus", "Rechnung vom 01.02.2026, bitte auf DE75 5121 0800 1245 1261 99")
	if p := e.m.proposal(e.ctx, id, "partner_id"); p != nil {
		t.Fatalf("Hinweis inaktiv, trotzdem erkannt: %v", p)
	}
	e.h.db.Exec(`UPDATE dms__hint SET is_active = 1 WHERE code = 'payee'`)
	// Mindestpunkte am Schritt: 9 Punkte reichen nicht für 10
	e.h.db.Exec(`UPDATE dms__step SET min_score = 10 WHERE workflow = 'ERP' AND step = '030'`)
	id = upload("wenig", "Rechnung vom 02.02.2026, bitte auf DE75 5121 0800 1245 1261 99")
	if p := e.m.proposal(e.ctx, id, "partner_id"); p != nil {
		t.Fatalf("min_score=10, trotzdem erkannt: %v", p)
	}
	// Neustart behält laut Workflow auch den Titel
	e.h.db.Exec(`UPDATE dms__document SET title = 'Mein Titel', note = 'Notiz' WHERE id = ?`, id)
	e.h.db.Exec(`UPDATE dms__workflow SET restart_keep = 'note, title' WHERE code = 'ERP'`)
	e.must(docObject, "restart", map[string]any{"id": id, "data": map[string]any{}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["title"] != "Mein Titel" || d["note"] != "Notiz" {
		t.Fatalf("restart_keep: %v %v", d["title"], d["note"])
	}
}
