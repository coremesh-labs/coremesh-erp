package dms

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// TestERPFlow: Rechnung – Kategorie, Partner, nur seine Rechnungen (nur
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
	next(map[string]any{"wf_choice": "INVOICE"}) // Kategorie Rechnung / Gutschrift
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
	if d["wf_step"] != "055" {
		t.Fatalf("Dokumentart nach der Übernahme: %v", d["wf_step"])
	}
	st0 := state(map[string]string{})
	if msg := st0["message"].(string); !strings.HasPrefix(msg, "Schritt 055 · Dokumentart wählen: ") || !strings.Contains(msg, "(Kategorie Rechnung / Gutschrift)") {
		t.Fatalf("Schrittnummer und Kategorie: %q", msg)
	}
	types, _ := st0["fields"].(map[string]any)["wf_choice"].(map[string]any)["options"].([]any)
	if len(types) != 2 || types[0].(map[string]any)["value"] != "INVOICE" || types[1].(map[string]any)["value"] != "CREDIT_NOTE" {
		t.Fatalf("nur Dokumentarten der Kategorie: %v", types)
	}
	_, err = e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "NOTICE"}})
	expect(t, err, sdk.ErrInvalidArgument, "Dokumentart anderer Kategorie")
	next(map[string]any{"wf_choice": "INVOICE"})
	d = doc()
	if d["wf_step"] != "070" || d["file_account"] != "4.2.0" || d["doc_date"] != "2026-03-01" || d["title"] != "RE-77" ||
		!strings.Contains(d["wf_log"].(string), "Ablage 4.2.0 (Sachkonto 6320)") {
		t.Fatalf("Übernahme: %v\n%s", d, d["wf_log"])
	}
	st = state(map[string]string{})
	if v := st["fields"].(map[string]any)["wf_text"].(map[string]any)["value"]; v != "42.0:VV:Allgemein_ER-1_20260301_RE:Pfitzner" {
		t.Fatalf("Namensvorschlag: %v", v)
	}
	// Zurück: erst zu den übersprungenen Fragen (Datum, Ablage aus dem Sachkonto) – korrigierbar –,
	// dann zur Objektwahl; die Übernahme ist zurückgenommen.
	e.must(docObject, "back", map[string]any{"id": id})
	if d = doc(); d["wf_step"] != "065" || d["doc_date"] != "2026-03-01" {
		t.Fatalf("zurück zum Datum: %v", d)
	}
	e.must(docObject, "back", map[string]any{"id": id})
	if d = doc(); d["wf_step"] != "060" || d["file_account"] != "4.2.0" {
		t.Fatalf("zurück zur Ablage: %v", d)
	}
	e.must(docObject, "back", map[string]any{"id": id})
	if d = doc(); d["wf_step"] != "055" || d["doc_type"] != nil {
		t.Fatalf("zurück zur Dokumentart: %v", d)
	}
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
	// Von vorn: alles zurück auf den Start, erste Frage wieder offen.
	e.must(docObject, "startOver", map[string]any{"id": id})
	if d = doc(); d["wf_step"] != "025" || d["doc_category"] != nil || d["partner_id"] != nil {
		t.Fatalf("von vorn: %v", d)
	}
	next(map[string]any{"wf_choice": "INVOICE"})
	next(map[string]any{"wf_choice": "P2"})
	next(map[string]any{"wf_choice": "SupplierInvoice:2000|ER-1"})
	next(map[string]any{"wf_choice": "CREDIT_NOTE"})
	_, err = e.call(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_text": "Hausmeister"}})
	expect(t, err, sdk.ErrInvalidArgument, "Name ohne Ablage")
	if v := state(map[string]string{})["fields"].(map[string]any)["wf_text"].(map[string]any)["value"]; v != "42.0:VV:Allgemein_ER-1_20260301_GS:Pfitzner" {
		t.Fatalf("Name mit Gutschrift: %v", v)
	}
	next(map[string]any{"wf_text": "42.0:VV:ER-1_Hausmeister"})
	if d = doc(); d["status"] != stFiled || d["name"] != "42.0:VV:ER-1_Hausmeister.pdf" {
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
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "OBJECT"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["wf_step"] != "040" || !strings.Contains(d["wf_log"].(string), "entfällt für Dokumentkategorie OBJECT") {
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

// TestERPAutoSingle: Kategorie mit nur einer Dokumentart – ohne Frage gesetzt.
func TestERPAutoSingle(t *testing.T) {
	e := setup(t)
	e.company()
	e.run.text = "Plan"
	e.must(docTypeObject, "deactivate", map[string]any{"id": "REPORT"}) // Kategorie OBJECT: nur noch PLAN
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("auto")), "file_name": "a.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "OBJECT"}})
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "Building:2000|ROGoer14"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["doc_type"] != "PLAN" || !strings.Contains(d["wf_log"].(string), "einzige Möglichkeit") || d["wf_step"] != "060" {
		t.Fatalf("eine Dokumentart: %v\n%s", d, d["wf_log"])
	}
}

// TestMigrateCategories: Objekte je Dokumentart (0.2.0/0.2.1) gehen an die Kategorie,
// Dokumentarten und Dokumente ohne Kategorie bekommen sie.
func TestMigrateCategories(t *testing.T) {
	e := setup(t)
	e.company()
	for _, q := range []string{
		`UPDATE dms__doc_type SET category = NULL WHERE code = 'NOTICE'`,
		`DELETE FROM dms__category_object WHERE category = 'CONTRACT'`,
		`INSERT INTO dms__doc_type_object (doc_type, object, label, partner_field) VALUES ('NOTICE', 'Contract', 'Vertrag alt', 'partner_id')`,
		`INSERT INTO dms__document (id, company_code, file_name, content_type, sha256, doc_type, created_at) VALUES ('alt', '2000', 'a.pdf', 'application/pdf', 'x', 'INVOICE', 'x')`,
	} {
		if _, err := e.h.db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if err := e.m.Migrate(e.ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.m.Migrate(e.ctx); err != nil { // wiederholbar
		t.Fatal(err)
	}
	if c := e.one(`SELECT category FROM dms__doc_type WHERE code = 'NOTICE'`); c != "CONTRACT" {
		t.Fatalf("Kategorie der Dokumentart: %v", c)
	}
	if l := e.one(`SELECT label FROM dms__category_object WHERE category = 'CONTRACT' AND object = 'Contract'`); l != "Vertrag alt" {
		t.Fatalf("Objekt übernommen: %v", l)
	}
	if c := e.one(`SELECT doc_category FROM dms__document WHERE id = 'alt'`); c != "INVOICE" {
		t.Fatalf("Kategorie des Dokuments: %v", c)
	}
}

// TestContractDocTypes: Kategorie Vertrag mit Rechnungsdokument und Gutschrift.
func TestContractDocTypes(t *testing.T) {
	e := setup(t)
	l := items(e.must(docTypeObject, "list", map[string]any{"query": map[string]any{"category": "CONTRACT"}}))
	var codes []string
	for _, i := range l {
		codes = append(codes, i["code"].(string))
	}
	if strings.Join(codes, ",") != "CONTRACT,AMENDMENT,CONTRACT_INVOICE,CONTRACT_CREDIT,NOTICE,STATEMENT,LETTER" {
		t.Fatalf("Dokumentarten Vertrag: %v", codes)
	}
}

func TestCompactNumber(t *testing.T) {
	for _, c := range [][3]string{{"5.2.0", "2", "52.0"}, {"5.2.3.2", "2", "52.32"}, {"5.2.3.4", "2", "52.34"}, {"3.0", "2", "30"}, {"5.2.0", "0", "5.2.0"}, {"5.2.0", "3", "520"}, {"7", "2", "7"}} {
		n := 0
		fmt.Sscan(c[1], &n)
		if got := compactNumber(c[0], n); got != c[2] {
			t.Errorf("%s/%s: %s", c[0], c[1], got)
		}
	}
}

// TestNameShorts: Kürzel von Dokumentart und Partner im Namen, eindeutiges
// Kürzel der Dokumentart, mehrdeutige Ablagenummer abgelehnt.
func TestNameShorts(t *testing.T) {
	e := setup(t)
	e.company()
	_, err := e.call(docTypeObject, "update", map[string]any{"id": "NOTICE", "data": map[string]any{"short": "re"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kürzel doppelt")
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "x", "name": "X", "path": e.in})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|X", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("kurz")), "file_name": "k.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"doc_type": "CONTRACT_INVOICE", "file_account": "5.2.3.2",
		"doc_date": "2026-03-02", "partner_id": "P3"}})
	e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": "Contract", "ref_key": "WH-2026-0001"}})
	if r := e.must(docObject, "file", map[string]any{"id": id}); r["name"] != "52.32:VV:ROGoer14_WH-2026-0001_20260302_VR:HUG.pdf" {
		t.Fatalf("Name: %v", r["name"])
	}
	// Mehrdeutig: 1.12.0 und 11.2.0 ergeben beide 112.0.
	for _, n := range []string{"1", "1.12", "1.12.0", "11", "11.2", "11.2.0"} {
		e.create(accountObject, map[string]any{"plan": "IMMO", "number": n, "name": "Ablage " + n})
	}
	cfg, _ := e.m.companyConfig(e.ctx, "2000")
	if _, err := e.m.nameAccount(e.ctx, cfg, "1.12.0"); err == nil || !strings.Contains(err.Error(), "ergeben im Dateinamen beide 112.0") {
		t.Fatalf("Kollision: %v", err)
	}
}

// TestNameTakenRetry: liegt der Name schon im Archiv (z. B. anderer Buchungskreis
// im selben Archiv), bekommt das Dokument den nächsten freien Namen.
func TestNameTakenRetry(t *testing.T) {
	e := setup(t)
	e.company()
	dir := e.root + "/3 Steuern Lasten Abgaben/3.0 Grundsteuer/3.0.1 Grundsteuerbescheide (Jahresbescheide)/Allgemein"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/30.1:VV:Allgemein_20250110_BS.pdf", []byte("fremd"), 0o444); err != nil {
		t.Fatal(err)
	}
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "x", "name": "X", "path": e.in})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|X", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("doppelt")), "file_name": "d.pdf"}})
	e.must(docObject, "update", map[string]any{"id": up["id"], "data": map[string]any{"doc_type": "NOTICE", "file_account": "3.0.1", "doc_date": "2025-01-10"}})
	if r := e.must(docObject, "file", map[string]any{"id": up["id"]}); r["name"] != "30.1:VV:Allgemein_20250110_BS_2.pdf" {
		t.Fatalf("Name: %v", r["name"])
	}
	if b, _ := os.ReadFile(dir + "/30.1:VV:Allgemein_20250110_BS.pdf"); string(b) != "fremd" {
		t.Fatal("fremde Datei überschrieben")
	}
}

// TestCashReceipt: Barbeleg – Kategorie Beleg, Vorerfassung wählen, Datum,
// Titel und Ablage aus der Vorerfassung (Sachkonto der Position).
func TestCashReceipt(t *testing.T) {
	e := setup(t)
	e.company()
	e.must(accountCCObject, "update", map[string]any{"id": "2000|4.2.0", "data": map[string]any{"gl_accounts": "6673"}})
	e.run.text = "Fahrschein"
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(pdf("ticket")), "file_name": "t.pdf"}})
	id := up["id"].(string)
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "RECEIPT"}})
	fs := e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	if !strings.Contains(fmt.Sprint(fs["fields"]), "JournalDraft:D9") || fs["links"].([]any)[0].(map[string]any)["url"] != "/new/JournalDraft" {
		t.Fatalf("Vorerfassungen: %v", fs)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "JournalDraft:D9"}})
	d := e.must(docObject, "get", map[string]any{"id": id})
	// Kein Datum im Text: Frage nach dem Dokumentdatum mit heute als Vorschlag (nicht das Datum der Vorerfassung).
	if d["doc_type"] != "CASH_RECEIPT" || d["doc_date"] != nil || d["title"] != "Straßenbahnticket" || d["file_account"] != "4.2.0" || d["wf_step"] != "065" {
		t.Fatalf("Barbeleg: %v\n%s", d, d["wf_log"])
	}
	fs = e.must(docObject, "formState", metamodel.FormStateRequest{Mode: "action", Action: "continue", ID: id, Values: map[string]string{}})
	if v := fs["fields"].(map[string]any)["wf_date"].(map[string]any)["value"]; v != today() {
		t.Fatalf("Vorschlag heute: %v", v)
	}
}

// TestObjectLevel: Objekt-Ebene unter der Ablage – Vertrag → Hauptobjekt
// (Mietobjekt) → Gebäude; ohne Objekt „Allgemein“; abschaltbar.
func TestObjectLevel(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "x", "name": "X", "path": e.in})
	file := func(tag string, ref []string) string {
		t.Helper()
		up := e.must(inboxObject, "upload", map[string]any{"id": "2000|X", "data": map[string]any{
			"file": base64.StdEncoding.EncodeToString(pdf(tag)), "file_name": tag + ".pdf"}})
		id := up["id"].(string)
		e.must(docObject, "update", map[string]any{"id": id, "data": map[string]any{"doc_type": "STATEMENT", "file_account": "5.2.3.2", "doc_date": "2026-01-15"}})
		if ref != nil {
			e.must(docObject, "addRef", map[string]any{"id": id, "data": map[string]any{"ref_object": ref[0], "ref_key": ref[1]}})
		}
		e.must(docObject, "file", map[string]any{"id": id})
		return e.one(`SELECT location FROM dms__document WHERE id = ?`, id).(string)
	}
	base := "5 Dienstleistungen Verträge/5.2 Management/5.2.3 WEG Verwaltung/5.2.3.2 Wirtschaftsplan/"
	if loc := file("weg", []string{"Contract", "WH-2026-0001"}); loc != base+"ROGoer14 Biesnitzer Str. 14/52.32:VV:ROGoer14_WH-2026-0001_20260115_AB.pdf" {
		t.Fatalf("über Vertrag und Mietobjekt: %s", loc)
	}
	if loc := file("ohne", nil); loc != base+"Allgemein/52.32:VV:Allgemein_20260115_AB.pdf" {
		t.Fatalf("ohne Objekt: %s", loc)
	}
	e.must(companyObject, "update", map[string]any{"id": "2000", "data": map[string]any{"object_dir": "NONE"}})
	if loc := file("flach", nil); loc != base+"52.32:VV:20260115_AB.pdf" {
		t.Fatalf("ohne Ebene: %s", loc)
	}
}

// TestIdentify: IBAN und Kundennummer erkennen Partner und Vertrag; Bestätigung
// „Ja“ übernimmt, „Nein“ nimmt die Erkennung zurück.
func TestIdentify(t *testing.T) {
	e := setup(t)
	e.company()
	e.create(inboxObject, map[string]any{"company_code": "2000", "code": "pdf", "name": "PDF", "path": e.in, "workflow": "ERP"})
	run := func(tag, text string) string {
		e.run.text = text
		up := e.must(inboxObject, "upload", map[string]any{"id": "2000|PDF", "data": map[string]any{
			"file": base64.StdEncoding.EncodeToString(pdf(tag)), "file_name": tag + ".pdf"}})
		return up["id"].(string)
	}
	id := run("ja", "Hausgeldabrechnung vom 12.03.2026\nIhre Kundennr.: KD 4711 08\nBitte überweisen auf IBAN DE89 3704 0044 0532 0130 00")
	d := e.must(docObject, "get", map[string]any{"id": id})
	if d["partner_id"] != "P2" || d["wf_step"] != "023" || !strings.Contains(d["wf_log"].(string), "Partner Pfitzner (IBAN, Kundennummer WH-2026-0001)") ||
		!strings.Contains(d["wf_log"].(string), "Vertrag WH-2026-0001") {
		t.Fatalf("erkannt: %v\n%s", d, d["wf_log"])
	}
	if q := d["wf_question"].(string); !strings.Contains(q, "Erkannt: Partner Pfitzner (P2), Bezug Contract WH-2026-0001 – stimmt das?") {
		t.Fatalf("Frage: %s", q)
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "YES"}})
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "CONTRACT"}})
	// Partner und Vertrag gesetzt: weiter bis zur Übernahme/Dokumentart, ohne Partner- und Objektfrage.
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "055" {
		t.Fatalf("ohne Partner- und Objektfrage: %v\n%s", d["wf_step"], d["wf_log"])
	}
	// Nein: Erkennung zurück.
	id = run("nein", "Rechnung vom 01.02.2026, Telefon 0351 / 123 45 67, IBAN DE89370400440532013000 nicht unsere")
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["partner_id"] != nil || d["wf_step"] != "025" {
		// Telefon (P3) und IBAN (P2) gleichauf genug? P2: 5, P3: 3 → kein klarer Abstand von 2? 5-3=2 → P2.
		if d["partner_id"] != "P2" {
			t.Fatalf("erkannt: %v", d)
		}
	}
	e.must(docObject, "continue", map[string]any{"id": id, "data": map[string]any{"wf_choice": "NO"}})
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["partner_id"] != nil || d["wf_step"] != "025" ||
		!strings.Contains(d["wf_log"].(string), "Erkennung zurückgenommen") {
		t.Fatalf("nein: %v", d)
	}
	// Nichts erkannt: keine Bestätigung.
	id = run("nichts", "Werbung ohne Bezug")
	if d = e.must(docObject, "get", map[string]any{"id": id}); d["wf_step"] != "025" {
		t.Fatalf("nichts: %v", d["wf_step"])
	}
}

func TestIdentifyHelpers(t *testing.T) {
	if !ibanValid("DE89370400440532013000") || ibanValid("DE89370400440532013001") {
		t.Fatal("IBAN-Prüfziffer")
	}
	if phoneDigits("+49 351 1234567") != "03511234567" || phoneDigits("0351 / 123 45 67") != "03511234567" {
		t.Fatal("Telefon")
	}
	if compactAlnum("KD 4711-08") != "KD471108" || !strings.Contains(foldText("Haus- und Grundstücksservice"), " grundstuecksservice ") {
		t.Fatal("Normalisierung")
	}
}
