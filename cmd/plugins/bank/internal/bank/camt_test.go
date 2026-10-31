package bank

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// camtFile: camt.053.001.08 mit erfundenen Umsätzen (Faust-Bestand).
const camtFile = `<?xml version="1.0" encoding="UTF-8"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
 <BkToCstmrStmt><Stmt>
  <Acct><Id><IBAN>` + testIBAN + `</IBAN></Id></Acct>
  <Ntry><Amt Ccy="EUR">800.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
   <BookgDt><Dt>2026-02-01</Dt></BookgDt><ValDt><Dt>2026-02-01</Dt></ValDt><AddtlNtryInf>Gutschrift</AddtlNtryInf>
   <BkTxCd><Prtry><Cd>NTRF+166</Cd></Prtry></BkTxCd>
   <NtryDtls><TxDtls><Refs><EndToEndId>NOTPROVIDED</EndToEndId></Refs>
    <RltdPties><Dbtr><Pty><Nm>Margarete Schwerdtlein</Nm></Pty></Dbtr><DbtrAcct><Id><IBAN>DE02120300000000202051</IBAN></Id></DbtrAcct></RltdPties>
    <RmtInf><Ustrd>Miete MV-2026-0001</Ustrd><Ustrd>Februar</Ustrd></RmtInf></TxDtls></NtryDtls></Ntry>
  <Ntry><Amt Ccy="EUR">45.10</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
   <BookgDt><Dt>2026-02-03</Dt></BookgDt><AddtlNtryInf>Lastschrift</AddtlNtryInf>
   <NtryDtls><TxDtls><Refs><EndToEndId>E2E-WAGNER</EndToEndId><MndtId>WAG-0815</MndtId></Refs>
    <RltdPties><Cdtr><Pty><Nm>Wagner Stadtwerke</Nm></Pty></Cdtr><CdtrAcct><Id><IBAN>DE89370400440532013000</IBAN></Id></CdtrAcct></RltdPties>
    <CdtrSchmeId><Id><PrvtId><Othr><Id>DE98ZZZ09999999999</Id></Othr></PrvtId></Id></CdtrSchmeId>
    <RmtInf><Ustrd>Abschlag Strom</Ustrd></RmtInf></TxDtls></NtryDtls></Ntry>
  <Ntry><Amt Ccy="EUR">300.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
   <BookgDt><Dt>2026-02-04</Dt></BookgDt><AddtlNtryInf>Sammler</AddtlNtryInf>
   <NtryDtls>
    <TxDtls><Amt Ccy="EUR">100.00</Amt><RltdPties><Cdtr><Nm>Marthe Schwerdtlein</Nm></Cdtr></RltdPties><RmtInf><Ustrd>Kaution zurück</Ustrd></RmtInf></TxDtls>
    <TxDtls><Amt Ccy="EUR">200.00</Amt><RltdPties><Cdtr><Nm>Valentin</Nm></Cdtr></RltdPties><RmtInf><Ustrd>Reparatur</Ustrd></RmtInf></TxDtls>
   </NtryDtls></Ntry>
  <Ntry><Amt Ccy="EUR">9.99</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>PDNG</Cd></Sts><BookgDt><Dt>2026-02-05</Dt></BookgDt></Ntry>
 </Stmt></BkToCstmrStmt>
</Document>`

func TestCamtImport(t *testing.T) {
	e := setup(t)
	e.must(setupObject, "setupCompany", map[string]any{"company": 1000})
	e.create(accountObject, map[string]any{"company_code": "1000", "account_id": "giro", "designation": "Girokonto", "iban": testIBAN,
		"currency": "EUR", "gl_account": "1800", "format": "coba"})
	// erst CSV bis 01.02. (eine Miete), dann camt als ZIP: die Miete ist schon da
	e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": csvFile(
		row("01.02.2026", "Überweisung", "Margarete Schwerdtlein", "800", "Margarete Schwerdtlein", "", "Miete MV-2026-0001 Februar"))}})
	var zb bytes.Buffer
	zw := zip.NewWriter(&zb)
	w, _ := zw.Create("auszug_2026-02.xml")
	_, _ = w.Write([]byte(camtFile))
	_ = zw.Close()
	r := e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{
		"file": base64.StdEncoding.EncodeToString(zb.Bytes()), "file_name": "auszug.zip"}})
	if r["rows_read"] != 4 || r["rows_new"] != 3 || r["rows_duplicate"] != 1 {
		t.Fatalf("camt: %v", r)
	}
	if x := e.txn(2); x["amount"] != "-45.10" || x["counterparty_name"] != "Wagner Stadtwerke" || x["mandate_ref"] != "WAG-0815" ||
		x["creditor_id"] != "DE98ZZZ09999999999" || x["end_to_end_ref"] != "E2E-WAGNER" || x["purpose"] != "Abschlag Strom" {
		t.Errorf("Lastschrift: %v", x)
	}
	if x, y := e.txn(3), e.txn(4); x["amount"] != "-100.00" || x["counterparty_name"] != "Marthe Schwerdtlein" || y["amount"] != "-200.00" {
		t.Errorf("Sammler aufgeteilt: %v / %v", x, y)
	}
	// noch einmal als XML-Text: alles vorhanden
	r = e.must(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": camtFile}})
	if r["rows_new"] != 0 || !strings.Contains(r["message"].(string), "4 schon vorhanden") {
		t.Fatalf("doppelt: %v", r)
	}
	other := strings.ReplaceAll(camtFile, testIBAN, "DE02120300000000202051")
	_, err := e.call(accountObject, "importFile", map[string]any{"id": "1000|GIRO", "data": map[string]any{"file": other}})
	if err == nil || !strings.Contains(err.Error(), "gehört zum Konto") {
		t.Fatalf("anderes Konto: %v", err)
	}
}
