package dms

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Auswahl mit „Anlegen“ → Schritt AI_OBJECT: KI füllt vor, Maske zeigt die
// Felder, „Weiter“ legt den Partner an und setzt ihn am Dokument; Variable aus
// einer Auswahl als Platzhalter im Kontext.
func TestAIObjectStep(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "AIW", "name": "KI-Anlage"})
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "10", "name": "Art", "kind": kSelect, "target": "var"})
	for _, p := range [][2]string{{"var", "kind"}, {"options", "DL=Dienstleistung; VS=Versicherung"}} {
		e.create(paramObject, map[string]any{"workflow": "AIW", "step": "10", "param": p[0], "value": p[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "20", "name": "Partner", "kind": kSelect, "target": "partner_id"})
	for _, p := range [][2]string{{"source", "BusinessPartner.list"}, {"value_field", "id"}, {"label_fields", "name1"}} {
		e.create(paramObject, map[string]any{"workflow": "AIW", "step": "20", "param": p[0], "value": p[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "25", "name": "Partner anlegen", "kind": kAIObject})
	for _, p := range [][2]string{{"requests", "BusinessPartner=BUSINESS_PARTNER"}, {"bind", "partner"},
		{"context", "Vertragsart: {var.kind} {var.kind_label}"}, {"defaults", "partner.type=ORGANIZATION"}} {
		e.create(paramObject, map[string]any{"workflow": "AIW", "step": "25", "param": p[0], "value": p[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "AIW", "step": "30", "name": "Titel", "kind": kText, "target": "title"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "10", "outcome": oSelected, "next_step": "20"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "20", "outcome": oSelected, "next_step": "30"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "20", "outcome": oCreate, "next_step": "25"})
	e.create(optionObject, map[string]any{"workflow": "AIW", "step": "25", "outcome": oSelected, "next_step": "30"})
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "aiw", "name": "KI", "path": e.in, "workflow": "AIW"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|AIW", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("aiw")), "file_name": "a.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "DL"}})

	// Partnerauswahl: Knopf „Anlegen“ sichtbar, Link nicht
	st := e.formState(id)
	if contains(st.HideButtons, btnCreate) {
		t.Fatalf("Knopf Anlegen ausgeblendet: %+v", st)
	}
	e.must(docObject, btnCreate, map[string]any{"id": id})

	// KI-Schritt: Vorbelegung mit Kontext, Maske mit den Feldern
	run := e.h.aiCalls[0].Payload.(map[string]any)
	if run["request"] != "BUSINESS_PARTNER" || !strings.HasPrefix(fmt.Sprint(run["context"]), "Vertragsart: DL ") {
		t.Fatalf("Ai.run: %v", run)
	}
	st = e.formState(id)
	if len(st.Extra) != 1 || st.Extra[0].Key != "f__partner__name1" || *st.Fields["f__partner__name1"].Value != "Neue GmbH" ||
		!contains(st.HideButtons, btnCreate) || !strings.Contains(st.Message, "von der KI vorbelegt") {
		t.Fatalf("Maske: %+v", st)
	}
	r := e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"f__partner__name1": "Neue Muster GmbH"}})
	if r["status"] == "FAILED" {
		t.Fatalf("weiter: %v", r)
	}
	var create, collect map[string]any
	for _, c := range e.h.aiCalls {
		switch c.Action {
		case "create":
			create = c.Payload.(map[string]any)
		case "collect":
			collect = c.Payload.(map[string]any)
		}
	}
	if collect["values"].(map[string]any)["f__partner__name1"] != "Neue Muster GmbH" ||
		create["defaults"].(map[string]any)["partner.type"] != "ORGANIZATION" {
		t.Fatalf("collect/create: %v %v", collect, create)
	}
	if doc := e.must(docObject, "get", map[string]any{"id": id}); doc["partner_id"] != "BPNEW" {
		t.Fatalf("Partner am Dokument: %v", doc["partner_id"])
	}

	// nachträglich (Workflow fertig): Auswahl aus den KI-Schritten, Maske, Anlegen
	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: docObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "aiCreateLater", "id": id}})
	var st0 metamodel.FormState
	if err != nil || sdk.Decode(resp.Payload, &st0) != nil || len(st0.Fields["ai_choice"].Options) != 1 || st0.Fields["ai_choice"].Options[0].Value != "25|BusinessPartner|BUSINESS_PARTNER" {
		t.Fatalf("Auswahl: %v %+v", err, st0)
	}
	resp, _ = e.p.Handle(e.ctx, sdk.Request{Object: docObject, Action: "formState", Payload: map[string]any{"mode": "action", "action": "aiCreateLater", "id": id,
		"values": map[string]any{"ai_choice": "25|BusinessPartner|BUSINESS_PARTNER"}}})
	_ = sdk.Decode(resp.Payload, &st0)
	if len(st0.Extra) != 1 {
		t.Fatalf("Maske nachträglich: %+v", st0)
	}
	creates := 0
	for _, c := range e.h.aiCalls {
		if c.Action == "create" {
			creates++
		}
	}
	r = e.must(docObject, "aiCreateLater", map[string]any{"id": id, "data": map[string]any{"ai_choice": "25|BusinessPartner|BUSINESS_PARTNER", "f__partner__name1": "Nachtrag GmbH"}})
	n := 0
	for _, c := range e.h.aiCalls {
		if c.Action == "create" {
			n++
		}
	}
	if n != creates+1 || !strings.Contains(fmt.Sprint(r["message"]), "BPNEW") {
		t.Fatalf("nachträglich angelegt: %v (%d → %d)", r, creates, n)
	}
}

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

// Rechnung zum Vertrag: Frage „Vertragspositionen übernehmen?“ – Ja: KI nur Kopf
// (requests_prefill), Positionen aus dem Vertrag; Nein: KI liest alles, nur der Kopf
// kommt aus dem Vertrag.
func TestContractItemsPrefill(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(workflowObject, map[string]any{"code": "PFW", "name": "Vertrag"})
	e.create(stepObject, map[string]any{"workflow": "PFW", "step": "10", "name": "Frage", "kind": kSelect, "target": "var"})
	for _, p := range [][2]string{{"var", "contract_items"}, {"options", "YES=Ja; NO=Nein"}, {"when", "ref:Contract & no_ref:SupplierInvoice"}} {
		e.create(paramObject, map[string]any{"workflow": "PFW", "step": "10", "param": p[0], "value": p[1]})
	}
	e.create(stepObject, map[string]any{"workflow": "PFW", "step": "20", "name": "Rechnung", "kind": kAIObject})
	for _, p := range [][2]string{{"requests", "SupplierInvoice=SUPPLIER_INVOICE"}, {"prefill", "SupplierInvoice=SupplierInvoice.contractDraft"},
		{"prefill_when", "var:contract_items=YES"}, {"requests_prefill", "SupplierInvoice=SUPPLIER_INVOICE_HEAD"}} {
		e.create(paramObject, map[string]any{"workflow": "PFW", "step": "20", "param": p[0], "value": p[1]})
	}
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
	lastRun := func() string {
		for i := len(e.h.aiCalls) - 1; i >= 0; i-- {
			if e.h.aiCalls[i].Action == "run" {
				return fmt.Sprint(e.h.aiCalls[i].Payload.(map[string]any)["request"])
			}
		}
		return ""
	}
	_ = e.m.setWfVar(e.ctx, d.ID, "var.contract_items", "YES")
	e.m.aiPrefill(e.ctx, d, s)
	st := e.m.aiStateOf(e.ctx, d, s)
	if lastRun() != "SUPPLIER_INVOICE_HEAD" || st.Request != "SUPPLIER_INVOICE" || len(st.Result["items"].([]any)) != 1 {
		t.Fatalf("Ja: %s %+v", lastRun(), st)
	}
	_ = e.m.setWfVar(e.ctx, d.ID, "var.contract_items", "NO")
	e.m.aiPrefill(e.ctx, d, s)
	st = e.m.aiStateOf(e.ctx, d, s)
	if inv, _ := st.Result["invoice"].(map[string]any); lastRun() != "SUPPLIER_INVOICE" || st.Result["items"] != nil || inv["framework_contract"] != "DL-1" {
		t.Fatalf("Nein: %s %+v", lastRun(), st)
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

	// 060: Knopf „Anlegen“ mit Hinweis
	st := e.formState(id)
	if contains(st.HideButtons, btnCreate) || !contains(st.HideButtons, btnFile) || !strings.Contains(st.Message, "angelegt wird erst nach der Vorschau") {
		t.Fatalf("060: %+v", st)
	}
	e.must(docObject, btnCreate, map[string]any{"id": id})

	// 070: KI mit Text (ohne Datei), Kontext mit Hinweisen (eigenes Konto nicht)
	var run map[string]any
	for _, c := range e.h.aiCalls {
		if c.Action == "run" {
			run = c.Payload.(map[string]any)
		}
	}
	ctx := fmt.Sprint(run["context"])
	if run["input"] != nil || !strings.Contains(ctx, "Zahlungen an die IBAN DE89370400440532013000 gingen laut Kontoauszug an: Sanitär Beaker") ||
		!strings.Contains(ctx, "Steuernummer des Absenders: 123/456/78901") {
		t.Fatalf("Ai.run: %v", run)
	}
	vars := fmt.Sprint(e.one(`SELECT wf_vars FROM dms__document WHERE id = ?`, id))
	if !strings.Contains(vars, `"banks":[{"account_holder":"Sanitär Beaker","iban":"DE89370400440532013000"}]`) || !strings.Contains(vars, `"name1":"Neue GmbH"`) {
		t.Fatalf("Vorbelegung: %s", vars)
	}
	st = e.formState(id)
	if contains(st.HideButtons, btnFile) || !strings.Contains(st.Message, "„KI liest die Datei“") || !strings.Contains(st.Message, "„Weiter“ zeigt die Vorschau") ||
		!strings.Contains(st.Message, "aus Dokument und Kontoauszug: banks.iban, banks.account_holder") {
		t.Fatalf("Maske: %+v", st)
	}

	// Datei an die KI auf Nachfrage
	e.must(docObject, btnFile, map[string]any{"id": id})
	run = e.h.aiCalls[len(e.h.aiCalls)-1].Payload.(map[string]any)
	for i := len(e.h.aiCalls) - 1; i >= 0; i-- {
		if e.h.aiCalls[i].Action == "run" {
			run = e.h.aiCalls[i].Payload.(map[string]any)
			break
		}
	}
	if run["content_type"] != "application/pdf" || run["input"] != base64.StdEncoding.EncodeToString(pdf("gerlach")) || run["text"] == nil {
		t.Fatalf("Datei an die KI: %v", run)
	}
	if st = e.formState(id); !contains(st.HideButtons, btnFile) {
		t.Fatalf("Knopf nach dem Lesen: %+v", st.HideButtons)
	}

	creates := func() int {
		n := 0
		for _, c := range e.h.aiCalls {
			if c.Action == "create" {
				n++
			}
		}
		return n
	}
	// Weiter → Vorschau, nichts angelegt; Nein → Maske mit den Eingaben
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"f__partner__name1": "Sanitär Beaker"}})
	st = e.formState(id)
	if creates() != 0 || !strings.Contains(st.Message, "Vorschau") || len(st.Fields["wf_choice"].Options) != 2 ||
		st.Fields["f__partner__name1"].ReadOnly == nil || !*st.Fields["f__partner__name1"].ReadOnly {
		t.Fatalf("Vorschau: %d %+v", creates(), st)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": oNo}})
	if st = e.formState(id); creates() != 0 || st.Fields["wf_choice"].Options != nil || strings.Contains(st.Message, "Vorschau:") {
		t.Fatalf("Nein: %+v", st)
	}
	// Zurück aus der Vorschau: Maske
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"f__partner__name1": "Sanitär Beaker"}})
	e.must(docObject, "back", map[string]any{"id": id})
	if d := e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "070" || creates() != 0 {
		t.Fatalf("Zurück aus der Vorschau: %v", d["wf_step"])
	}
	// Ja → angelegt, Rückmeldung über dem nächsten Schritt
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"f__partner__name1": "Sanitär Beaker"}})
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": oYes}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if creates() != 1 || d["partner_id"] != "BPNEW" || !strings.Contains(fmt.Sprint(d["wf_log"]), "Geschäftspartner BPNEW (BPNEW) angelegt") {
		t.Fatalf("angelegt: %d %v\n%s", creates(), d["partner_id"], d["wf_log"])
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
	if d["partner_id"] != "P2" || !strings.Contains(fmt.Sprint(d["wf_log"]), "IBAN laut Kontoauszug") {
		t.Fatalf("erkannt: %v\n%s", d["partner_id"], d["wf_log"])
	}
}

// Neustart verwirft den KI-Stand; ein KI-Fehler wird beim nächsten Betreten wiederholt.
func TestAIStateResetOnRestart(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	e.run.text = "Rechnung vom 12.02.2026, IBAN DE89370400440532013000"
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("neu")), "file_name": "n.pdf"}})
	id := fmt.Sprint(up["id"])
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})
	e.must(docObject, btnCreate, map[string]any{"id": id})
	e.must(docObject, btnFile, map[string]any{"id": id})
	if v := fmt.Sprint(e.one(`SELECT wf_vars FROM dms__document WHERE id = ?`, id)); !strings.Contains(v, `"file":true`) {
		t.Fatalf("KI-Stand: %s", v)
	}
	e.must(docObject, "restart", map[string]any{"id": id, "data": map[string]any{}})
	if v := e.one(`SELECT COALESCE(wf_vars, '') FROM dms__document WHERE id = ?`, id); strings.Contains(fmt.Sprint(v), "ai.070") {
		t.Fatalf("nach Neustart: %v", v)
	}
	// Fehler der KI: beim nächsten Betreten neu gefragt
	_ = e.m.setWfVar(e.ctx, id, "ai.070", aiState{Object: "BusinessPartner", Request: "BUSINESS_PARTNER", Status: "ERROR", Message: "HTTP 401"})
	runs := func() int {
		n := 0
		for _, c := range e.h.aiCalls {
			if c.Action == "run" {
				n++
			}
		}
		return n
	}
	before := runs()
	e.must(docObject, btnCreate, map[string]any{"id": id})
	if runs() != before+1 {
		t.Fatalf("KI nach Fehler nicht neu gefragt: %d → %d", before, runs())
	}
}
