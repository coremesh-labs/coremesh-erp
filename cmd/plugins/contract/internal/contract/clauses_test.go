package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestContractClauses: Klauselset mit Bedingungen, Kopie mit lückenloser Nummer und
// Verweisen, Hinweis bei geändertem Set, Erneuern, Vertragsdokument, Löschen.
func TestContractClauses(t *testing.T) {
	e := setup(t)
	e.basics()
	c := e.rentContract()
	id := "1000|" + c
	e.create("ContractCondition", map[string]any{"company_code": "1000", "contract_id": c, "condition_type": "NK",
		"calc_method": "FIXED", "amount": "150,00", "frequency": "MONTHLY", "payment_mode": "IN_ADVANCE", "valid_from": "2026-01-01"})
	e.h.entityTags = map[string][]any{"BusinessPartner|BP1": {map[string]any{"tag": "HUND", "value": map[string]any{"option": "JA"}}}}
	e.h.entityTexts = map[string]map[string]any{}
	e.h.filed = map[string]bool{}

	e.create(clauseSetObject, map[string]any{"company_code": "1000", "code": "WOHNUNG", "name": "Wohnung", "contract_types": "mv, gm",
		"title": "Mietvertrag", "template": "vertraege", "own_label": "Vermieter", "partner_label": "Mieter"})
	cl := func(key string, p, s int, title, text string, extra ...string) {
		d := map[string]any{"company_code": "1000", "set_code": "WOHNUNG", "clause_key": key, "paragraph": p, "subsection": s, "title": title, "text": text}
		for i := 0; i+1 < len(extra); i += 2 {
			d[extra[i]] = extra[i+1]
		}
		e.create(clauseObject, d)
	}
	cl("mietraeume", 10, 0, "Mieträume", "Vermietet wird {objekt:designation}.")
	cl("AUSSTATTUNG", 10, 1, "", "Ausstattung laut Übergabeprotokoll.")
	cl("KAUTION_BUERG", 20, 0, "Mietsicherheit", "Bankbürgschaft.", "cond_source", condContractTag, "cond_name", "KAUTION", "cond_values", "BUERGSCHAFT")
	cl("KAUTION_BAR", 20, 0, "Mietsicherheit", "Barkaution über {kondition:KM}.", "cond_source", condContractTag, "cond_name", "KAUTION",
		"cond_values", "BUERGSCHAFT", "cond_negate", "true")
	cl("MIETE", 30, 0, "Miete", "Miete {kondition:KM+NK} EUR an {partei:name} (vgl. {ref:ausstattung}).")
	cl("HEIZ", 30, 5, "", "Heizkosten.", "cond_source", condCondition, "cond_name", "HK")
	cl("SONST", 40, 0, "Sonstiges", "{text:RENT_CLAUSE}")
	cl("TIERE", 50, 0, "Tierhaltung", "Ein Hund ist erlaubt.", "cond_source", condPartnerTag, "cond_name", "HUND", "rating", "DISPUTED")
	if err := e.try(clauseObject, map[string]any{"company_code": "1000", "set_code": "WOHNUNG", "clause_key": "X", "paragraph": 0, "text": "x"}); err == nil {
		t.Fatal("Paragraph 0 angenommen")
	}

	// Ohne „Vertragsklauseln“ an der Vertragsart: Aktionen ausgeblendet und abgewiesen.
	if h := fmt.Sprint(e.must("Contract", "get", map[string]any{"id": id})["_hidden_actions"]); h != "[createClauses renewClauses clauseDocument deleteClauses]" {
		t.Fatalf("ohne Kennzeichen: %s", h)
	}
	if _, err := e.call("Contract", "createClauses", map[string]any{"id": id, "data": map[string]any{"cl_set": "WOHNUNG"}}); err == nil || !strings.Contains(err.Error(), "keine Vertragsklauseln") {
		t.Fatalf("ohne Kennzeichen erstellt: %v", err)
	}
	e.must("ContractType", "update", map[string]any{"id": "1000|MV", "data": map[string]any{"clauses_enabled": true}})
	if h := fmt.Sprint(e.must("Contract", "get", map[string]any{"id": id})["_hidden_actions"]); h != "[renewClauses clauseDocument deleteClauses]" {
		t.Fatalf("ohne Kopie: %s", h)
	}

	resp, err := e.p.Handle(e.ctx, sdk.Request{Object: "Contract", Action: "formState", Payload: map[string]any{"mode": "action", "action": "createClauses", "id": id}})
	var st map[string]any
	if err != nil || sdk.Decode(resp.Payload, &st) != nil || !strings.Contains(fmt.Sprint(st), "value:WOHNUNG") {
		t.Fatalf("Formular: %v %v", st, err)
	}
	resp, err = e.p.Handle(e.ctx, sdk.Request{Object: "Contract", Action: "formState", Payload: map[string]any{"mode": "action", "action": "clauseDocument", "id": id}})
	if err != nil || sdk.Decode(resp.Payload, &st) != nil || !strings.Contains(fmt.Sprint(st), "value:VERTRAEGE|de") {
		t.Fatalf("Formular PDF: %v %v", st, err)
	}
	r := e.must("Contract", "createClauses", map[string]any{"id": id, "data": map[string]any{"cl_set": "WOHNUNG"}})
	msg := r["message"].(string)
	if h := fmt.Sprint(e.must("Contract", "get", map[string]any{"id": id})["_hidden_actions"]); h != "[createClauses]" {
		t.Fatalf("mit Kopie: %s", h)
	}
	for _, want := range []string{"5 Klauseln in 4 Paragraphen", "umstritten: TIERE (§ 4)", "HEIZ", "SONST (Vertragstext RENT_CLAUSE leer)", "KAUTION_BUERG"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("Meldung ohne %q: %s", want, msg)
		}
	}
	res, err := e.m.db.Query(e.ctx, `SELECT clause_key, paragraph_no, subsection_no, text FROM contract__clause_copy WHERE contract_id = ? ORDER BY seq`, c)
	if err != nil || len(res.Rows) != 5 {
		t.Fatalf("Kopie: %v %v", res, err)
	}
	var got []string
	for _, row := range res.Rows {
		got = append(got, row[0].(string)+":"+clauseNumber(toInt(row[1]), toInt(row[2])))
	}
	if strings.Join(got, ",") != "MIETRAEUME:§ 1,AUSSTATTUNG:§ 1 Abs. 1,KAUTION_BAR:§ 2,MIETE:§ 3,TIERE:§ 4" {
		t.Fatalf("Nummern: %v", got)
	}
	if !strings.Contains(res.Rows[3][3].(string), "(vgl. § 1 Abs. 1)") {
		t.Fatalf("Verweis: %v", res.Rows[3][3])
	}
	if _, err := e.call("Contract", "createClauses", map[string]any{"id": id, "data": map[string]any{"cl_set": "WOHNUNG"}}); err == nil {
		t.Fatal("zweimal erstellt")
	}
	if s := e.must("Contract", "get", map[string]any{"id": id})["clause_state"]; !strings.Contains(s.(string), "entspricht") {
		t.Fatalf("Stand: %v", s)
	}

	// Set ändern → Hinweis; Erneuern übernimmt den Vertragstext.
	e.h.entityTexts["Contract|"+id] = map[string]any{"RENT_CLAUSE": "Rauchen ist im Haus nicht gestattet."}
	e.must(clauseObject, "update", map[string]any{"id": "1000|WOHNUNG|MIETE", "data": map[string]any{"title": "Miete und Betriebskosten"}})
	if s := e.must("Contract", "get", map[string]any{"id": id})["clause_state"]; !strings.Contains(s.(string), "geändert") {
		t.Fatalf("Stand nach Änderung: %v", s)
	}
	r = e.must("Contract", "renewClauses", map[string]any{"id": id})
	if !strings.Contains(r["message"].(string), "6 Klauseln in 5 Paragraphen") {
		t.Fatalf("Erneuern: %v", r)
	}

	// Vertragsdokument
	r = e.must("Contract", "clauseDocument", map[string]any{"id": id, "data": map[string]any{"cl_template": "VERTRAEGE|de"}})
	if r["download"] == nil || e.h.rendered["template"] != "VERTRAEGE" {
		t.Fatalf("PDF: %v %v", r, e.h.rendered)
	}
	d := e.h.rendered["data"].(map[string]any)
	paras := d["paragraphs"].([]any)
	if d["title"] != "Mietvertrag" || len(paras) != 5 {
		t.Fatalf("Daten: %v", d)
	}
	p1, p3, p4 := paras[0].(map[string]any), paras[2].(map[string]any), paras[3].(map[string]any)
	if p1["text"] != "Vermietet wird Wohnung 1. OG links." || len(p1["items"].([]any)) != 1 {
		t.Fatalf("§ 1: %v", p1)
	}
	if p3["title"] != "Miete und Betriebskosten" || !strings.Contains(p3["text"].(string), "Miete 1.000,00 EUR an Allianz") {
		t.Fatalf("§ 3: %v", p3)
	}
	if p4["text"] != "Rauchen ist im Haus nicht gestattet." {
		t.Fatalf("§ 4: %v", p4)
	}
	if ps := d["partners"].(map[string]any); ps["label"] != "Mieter" || len(ps["items"].([]any)) != 1 {
		t.Fatalf("Partner: %v", ps)
	}

	// Unbekannter Platzhalter ist ein Fehler.
	e.must(clauseObject, "update", map[string]any{"id": "1000|WOHNUNG|MIETRAEUME", "data": map[string]any{"text": "{objekt:gibtsnicht}"}})
	e.must("Contract", "renewClauses", map[string]any{"id": id})
	if _, err := e.call("Contract", "clauseDocument", map[string]any{"id": id, "data": map[string]any{"cl_template": "VERTRAEGE|de"}}); err == nil ||
		!strings.Contains(err.Error(), "{objekt:gibtsnicht}") {
		t.Fatalf("unbekannter Platzhalter: %v", err)
	}

	// Löschen: unterschrieben nur mit abgelegtem Dokument, nicht zustande gekommen nur im Entwurf.
	if _, err := e.call("Contract", "deleteClauses", map[string]any{"id": id, "data": map[string]any{"cl_reason": clauseDelSigned}}); err == nil {
		t.Fatal("ohne Dokument gelöscht")
	}
	e.h.filed["Contract|"+id] = true
	e.must("Contract", "deleteClauses", map[string]any{"id": id, "data": map[string]any{"cl_reason": clauseDelSigned}})
	if g := e.must("Contract", "get", map[string]any{"id": id}); g["clause_set"] != nil {
		t.Fatalf("nach Löschen: %v", g["clause_set"])
	}
	e.must("Contract", "createClauses", map[string]any{"id": id, "data": map[string]any{"cl_set": "WOHNUNG"}})
	e.must("Contract", "deleteClauses", map[string]any{"id": id, "data": map[string]any{"cl_reason": clauseDelNotConcluded}})
}
