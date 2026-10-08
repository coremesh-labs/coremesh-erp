package bank

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

const testIBAN = "DE89370400440532013000"

// csvFile: Aufbau wie der CSV-Export der Commerzbank (neuester Umsatz zuerst) – erfundene Daten.
func csvFile(rows ...string) string {
	return "\ufeffBuchungstag;Wertstellung;Umsatzart;Buchungstext;Betrag;Währung;IBAN Kontoinhaber;Kategorie;Sender;Empfänger;Verwendungszweck\r\n" +
		strings.Join(rows, "\r\n")
}

func row(date, typ, text, amount, sender, receiver, purpose string) string {
	return strings.Join([]string{date, date, typ, text, amount, "EUR", testIBAN, "", sender, receiver, purpose}, ";")
}

var february = csvFile(
	row("10.02.2026", "Zinsen/Entgelte", "Kontoführung", "-9,90", "", "", "Entgelt Februar"),
	row("05.02.2026", "Überweisung", "Stadtwerke Musterstadt IBAN: DE02120300000000202051 End-to-End-Ref.: E2E-77", "-80", "", "Stadtwerke Musterstadt", "Rechnung RE-4711"),
	row("03.02.2026", "Dauerauftrag", "Max Beispiel End-to-End-Ref.: NOTPROVIDED", "200", "Max Beispiel", "", "Nebenkosten Februar"),
	row("01.02.2026", "Überweisung", "Anna Muster End-to-End-Ref.: A1", "800", "Anna Muster", "", "Miete MV-2026-0001 Februar"),
	row("01.02.2026", "Überweisung", "Anna Muster End-to-End-Ref.: A1", "800", "Anna Muster", "", "Miete MV-2026-0001 Februar"),
)

func (e *env) txn(no int) map[string]any {
	e.t.Helper()
	return e.must(txnObject, "get", map[string]any{"id": "1000|GIRO|" + string(rune('0'+no))})
}

func TestBankImportMatchPost(t *testing.T) {
	e := setup(t)
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": 1000}); r["formats"] != 1 {
		t.Fatalf("Einrichtung: %v", r)
	}
	expect(t, e.try(accountObject, map[string]any{"company_code": "1000", "account_id": "x", "designation": "x", "iban": "DE89370400440532013001",
		"currency": "EUR", "gl_account": "1800"}), sdk.ErrInvalidArgument, "IBAN mit falscher Prüfziffer")
	a := e.create(accountObject, map[string]any{"company_code": "1000", "account_id": "giro", "designation": "Girokonto", "iban": "de89 3704 0044 0532 0130 00",
		"currency": "EUR", "gl_account": "1800", "format": "coba"})
	if a["account_id"] != "GIRO" || a["iban"] != testIBAN || a["gl_account"] != "SKR25-1800" || a["document_type_in"] != "DZ" {
		t.Fatalf("Bankkonto: %v", a)
	}
	id := map[string]any{"id": "1000|GIRO"}
	other := strings.ReplaceAll(february, testIBAN, "DE02120300000000202051")
	_, err := e.call(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": other}})
	expect(t, err, sdk.ErrInvalidArgument, "Datei eines anderen Kontos")

	r := e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": february, "file_name": "feb.csv"}})
	if r["rows_new"] != 5 || !strings.Contains(r["message"].(string), "3 zugeordnet, 1 Vorschläge, 1 offen") {
		t.Fatalf("Einlesen: %v", r)
	}
	// Reihenfolge der Zahlungen: ältester zuerst; zwei gleiche Mieten am selben Tag bleiben zwei Umsätze
	want := []struct{ date, amount, status, source, target string }{
		{"2026-02-01", "800.00", stMatched, "REFERENCE", "MV-2026-0001"},
		{"2026-02-01", "800.00", stMatched, "REFERENCE", "MV-2026-0001"},
		{"2026-02-03", "200.00", stProposed, "NAME", "MV-2026-0001"}, // Mitmieter des Vertrags
		{"2026-02-05", "-80.00", stMatched, "INVOICE", ""},
		{"2026-02-10", "-9.90", stOpen, "", ""},
	}
	for i, w := range want {
		x := e.txn(i + 1)
		if x["booking_date"] != w.date || x["amount"] != w.amount || x["status"] != w.status || str(x["match_source"]) != w.source || str(x["contract_id"]) != w.target {
			t.Errorf("Umsatz %d: %v", i+1, x)
		}
	}
	if x := e.txn(4); x["invoice_id"] != "ER-2026-00001" || x["counterparty_iban"] != "DE02120300000000202051" || x["end_to_end_ref"] != "E2E-77" {
		t.Errorf("Rechnung/IBAN/Referenz: %v", x)
	}
	if r := e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": february}}); r["rows_new"] != 0 || r["rows_duplicate"] != 5 {
		t.Fatalf("doppelt eingelesen: %v", r)
	}

	// Buchen hält am Vorschlag an
	r = e.must(accountObject, "post", id)
	if r["posted"] != 2 || !strings.Contains(r["message"].(string), "angehalten bei Umsatz 3") {
		t.Fatalf("Buchen 1: %v", r)
	}
	d := e.h.drafts[0]
	if d["document_type"] != "DZ" || d["posting_date"] != "2026-02-01" || d["reference"] != "A1" {
		t.Fatalf("Beleg: %v", d)
	}
	lines := e.h.items["D1"]
	if lines[0]["account_number"] != "SKR25-1800" || lines[0]["shkzg"] != "S" || lines[1]["account_number"] != "SKR25-1200" || lines[1]["shkzg"] != "H" ||
		lines[1]["sd_customer_id"] != "P1" || lines[1]["rent_contract_id"] != "MV-2026-0001" || lines[1]["amount"] != "800.00" {
		t.Fatalf("Positionen: %v", lines)
	}
	if x := e.txn(1); x["status"] != stPosted || x["document_number"] != "150000001" || !strings.Contains(str(x["posting_trace"]), "S SKR25-1800 800.00 / H SKR25-1200") {
		t.Fatalf("Vermerk: %v", x)
	}
	_, err = e.call(txnObject, "reset", map[string]any{"id": "1000|GIRO|1"})
	expect(t, err, sdk.ErrInvalidArgument, "gebuchter Umsatz")

	e.must(txnObject, "accept", map[string]any{"id": "1000|GIRO|3"})
	if x := e.txn(3); x["status"] != stMatched || x["partner_id"] != "P1" || x["role_code"] != "TENANT" {
		t.Fatalf("übernommen: %v", x)
	}
	_, err = e.call(txnObject, "assign", map[string]any{"id": "1000|GIRO|5", "data": map[string]any{"target_type": "ACCOUNT", "gl_account": "9999"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Sachkonto")
	if r := e.must(txnObject, "assign", map[string]any{"id": "1000|GIRO|5", "data": map[string]any{"target_type": "ACCOUNT", "gl_account": "6855"}}); r["rule_no"] != int64(0) {
		t.Fatalf("Entgelt ohne Gegenseite lernt keine Regel: %v", r)
	}
	if r = e.must(accountObject, "post", id); r["posted"] != 3 {
		t.Fatalf("Buchen 2: %v", r)
	}
	if d := e.h.drafts[3]; d["document_type"] != "KZ" {
		t.Fatalf("Zahlungsausgang: %v", d)
	}
	if l := e.h.items["D4"]; l[0]["shkzg"] != "H" || l[1]["account_number"] != "SKR25-2900" || l[1]["shkzg"] != "S" || l[1]["supplier_id"] != "P3" {
		t.Fatalf("Rechnung bezahlt: %v", l)
	}
	if d, l := e.h.drafts[4], e.h.items["D5"]; d["document_type"] != "SA" || l[1]["account_number"] != "SKR25-6855" || l[1]["shkzg"] != "S" || l[1]["sd_customer_id"] != nil {
		t.Fatalf("Entgelt: %v %v", d, l)
	}

	// Lernen: von Hand zugeordnet → Regelvorschlag; erst bestätigt ordnet sie zu
	march := csvFile(row("12.02.2026", "Lastschrift", "Abschlag", "-45", "", "Stadtwerke Musterstadt", "Abschlag Strom"))
	r = e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": march}})
	if x := e.txn(6); x["status"] != stProposed || x["partner_id"] != "P3" || x["match_source"] != "NAME" {
		t.Fatalf("Name: %v", x)
	}
	a6 := e.must(txnObject, "assign", map[string]any{"id": "1000|GIRO|6", "data": map[string]any{"target_type": "PARTNER", "partner_id": "P3"}})
	no := a6["rule_no"].(int64)
	rule := e.must(ruleObject, "get", map[string]any{"id": "1000|" + str(no)})
	if rule["status"] != ruleProposed || rule["counterparty_name"] != "stadtwerke musterstadt" || rule["direction"] != "OUT" || rule["role_code"] != "CREDITOR" {
		t.Fatalf("gelernte Regel: %v", rule)
	}
	april := csvFile(row("12.03.2026", "Lastschrift", "Abschlag", "-45", "", "Stadtwerke Musterstadt", "Abschlag Strom"))
	e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": april}})
	if x := e.txn(7); x["match_source"] != "NAME" {
		t.Fatalf("Vorschlag wirkt nicht: %v", x)
	}
	e.must(ruleObject, "confirm", map[string]any{"id": "1000|" + str(no)})
	e.must(accountObject, "match", id)
	if x := e.txn(7); x["status"] != stMatched || x["match_source"] != "RULE" || toInt(x["rule_no"]) != no {
		t.Fatalf("bestätigte Regel: %v", x)
	}
	if r := e.must(ruleObject, "get", map[string]any{"id": "1000|" + str(no)}); toInt(r["hits"]) != 1 {
		t.Fatalf("Treffer: %v", r)
	}
	expect(t, e.try(ruleObject, map[string]any{"company_code": "1000", "target_type": "ACCOUNT", "gl_account": "6855"}),
		sdk.ErrInvalidArgument, "Regel ohne Bedingung")
	if r := e.create(ruleObject, map[string]any{"company_code": "1000", "purpose_contains": "Entgelt", "target_type": "ACCOUNT", "gl_account": "6855"}); r["status"] != ruleConfirmed {
		t.Fatalf("von Hand angelegte Regel: %v", r)
	}
}

func TestImportFormat(t *testing.T) {
	e := setup(t)
	e.create(formatObject, map[string]any{"company_code": "1000", "code": "simple", "name": "Einfach", "delimiter": ",", "decimal_separator": ".",
		"date_format": "YYYY-MM-DD", "debit_values": "D"})
	expect(t, e.try(columnObject, map[string]any{"company_code": "1000", "format": "SIMPLE", "target": "purpose", "source": "3", "transform": "EXTRACT",
		"pattern": "Ref"}), sdk.ErrInvalidArgument, "Auszug ohne Gruppe")
	for _, c := range []map[string]any{
		{"target": "booking_date", "source": "Date"}, {"target": "amount", "source": "Amount"}, {"target": "debit_credit", "source": "DC"},
		{"target": "counterparty_name", "source": "Name", "transform": "UPPER"},
	} {
		c["company_code"], c["format"] = "1000", "SIMPLE"
		e.create(columnObject, c)
	}
	f, err := e.m.loadFormat(e.ctx, "1000", "SIMPLE")
	if err != nil {
		t.Fatal(err)
	}
	rows, errs, err := f.parse("Date,Amount,DC,Name\n2026-03-02,\"1,250.50\",D,max beispiel\n2026-03-01,10,C,x\n2026-13-01,1,C,y\n", 2)
	if err != nil || len(errs) != 1 || !strings.Contains(errs[0], "Zeile 4") {
		t.Fatalf("Fehler: %v %v", err, errs)
	}
	if len(rows) != 2 || rows[0].BookingDate != "2026-03-01" || rows[1].Amount != -125050 || rows[1].Fields["counterparty_name"] != "MAX BEISPIEL" {
		t.Fatalf("Zeilen: %+v", rows)
	}
	if _, _, err := f.parse("Datum,Betrag\n", 2); err == nil {
		t.Fatal("fehlende Spalte nicht gemeldet")
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch n := v.(type) {
	case float64:
		return strings.TrimSuffix(strings.TrimRight(strings.TrimRight(sprintf(n), "0"), "."), ".")
	case int64:
		return sprintf(n)
	}
	return sprintf(v)
}

// TestImportCommandRows: Die Konsole schickt eine .csv als Zeilen (Spaltenname → Wert).
func TestImportCommandRows(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.create(accountObject, map[string]any{"company_code": "1000", "account_id": "GIRO", "designation": "Girokonto", "iban": testIBAN,
		"currency": "EUR", "gl_account": "1800", "format": "COBA"})
	rows := []any{map[string]any{"Buchungstag": "01.02.2026", "Wertstellung": "01.02.2026", "Umsatzart": "Überweisung", "Buchungstext": "x; y",
		"Betrag": "1.250,50", "Währung": "EUR", "IBAN Kontoinhaber": testIBAN, "Kategorie": "", "Sender": "Anna Muster", "Empfänger": "",
		"Verwendungszweck": "Miete"}}
	r := e.must(setupObject, "import", map[string]any{"company": 1000, "account": "giro", "file": rows})
	if r["rows_new"] != 1 {
		t.Fatalf("Einlesen: %v", r)
	}
	if x := e.txn(1); x["amount"] != "1250.50" || x["booking_text"] != "x; y" || x["counterparty_name"] != "Anna Muster" {
		t.Fatalf("Umsatz: %v", x)
	}
}

func TestFindIBAN(t *testing.T) {
	for text, want := range map[string]string{
		"Stadtwerke IBAN: DE02120300000000202051 End-to-End-Ref.: X": "DE02120300000000202051",
		"Ref DE02 1203 0000 0000 2020 51 Miete":                      "DE02120300000000202051",
		"End-to-End-Ref.: DE12345678901234":                          "", // zu kurz für DE
		"NL91ABNA0417164300 Zahlung":                                 "NL91ABNA0417164300",
	} {
		if got := findIBAN(text); got != want {
			t.Errorf("%q: %q, erwartet %q", text, got, want)
		}
	}
}

// TestRefundToTenant: Auszahlung an einen Mieter ist eine Debitorenzahlung (DZ).
func TestRefundToTenant(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.create(accountObject, map[string]any{"company_code": "1000", "account_id": "GIRO", "designation": "Girokonto", "iban": testIBAN,
		"currency": "EUR", "gl_account": "1800", "format": "COBA"})
	e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{
		"file": csvFile(row("02.03.2026", "Überweisung", "Rückzahlung", "-50", "", "Anna Muster", "Guthaben MV-2026-0001"))}})
	if r := e.must(accountObject, "post", map[string]any{"id": "1000|GIRO"}); r["posted"] != 1 {
		t.Fatalf("Buchen: %v", r)
	}
	if d, l := e.h.drafts[0], e.h.items["D1"]; d["document_type"] != "DZ" || l[1]["shkzg"] != "S" || l[1]["sd_customer_id"] != "P1" {
		t.Fatalf("Rückzahlung: %v %v", d, l)
	}
}

// TestIBANDirection: Auszahlung an die IBAN eines Mieters ist nur ein Vorschlag.
func TestIBANDirection(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	e.create(accountObject, map[string]any{"company_code": "1000", "account_id": "GIRO", "designation": "Girokonto", "iban": testIBAN,
		"currency": "EUR", "gl_account": "1800", "format": "COBA"})
	testRoles["P9"] = "TENANT"
	testPartners = append(testPartners, map[string]any{"id": "P9", "name1": "Neun", "name2": ""})
	t.Cleanup(func() { delete(testRoles, "P9"); testPartners = testPartners[:len(testPartners)-1] })
	e.h.bankIBAN = "DE75512108001245126199"
	e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": csvFile(
		row("02.03.2026", "Überweisung", "IBAN DE75512108001245126199", "-30", "", "Irgendwer", "Auslage"),
		row("01.03.2026", "Überweisung", "IBAN DE75512108001245126199", "30", "Irgendwer", "", "Miete"))}})
	if x := e.txn(1); x["status"] != stMatched || x["match_source"] != "IBAN" {
		t.Fatalf("Eingang vom Mieter: %v", x)
	}
	if x := e.txn(2); x["status"] != stProposed || !strings.Contains(str(x["match_note"]), "Richtung") {
		t.Fatalf("Auszahlung an Mieter: %v", x)
	}
}
