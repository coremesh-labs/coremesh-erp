package bank

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// camt-Import (seit 0.4.0): Kontoauszüge im ISO-20022-Format camt.053
// (Tagesauszug) und camt.052 (Umsätze untertags), als XML-Datei oder ZIP mit
// mehreren Dateien – so liefern Online-Banking und FinTS (HKCAZ) die Umsätze.
// Erkannt am Inhalt, ohne Importformat (Format CAMT in der Einlesung).
//
// Je gebuchtem Eintrag (Ntry, Status BOOK) ein Umsatz; ein Sammelposten mit
// mehreren Einzelumsätzen (TxDtls mit eigenem Betrag) wird aufgeteilt.
// Gegenseite ist bei Belastung der Zahlungsempfänger (Cdtr), bei Gutschrift
// der Zahler (Dbtr). Vorgemerkte Umsätze (PDNG) entfallen.

const formatCamt = "CAMT"

// fileText: Inhalt eines hochgeladenen Dateifelds – Base64 (Binary) oder Text;
// ZIP wird entpackt (alle XML-Dateien), sonst UTF-8 bzw. Latin-1.
func fileText(b []byte) ([]string, error) {
	if bytes.HasPrefix(b, []byte("PK\x03\x04")) {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, crud.Invalid("ZIP-Datei: %v", err)
		}
		var out []string
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, crud.Invalid("ZIP-Datei %s: %v", f.Name, err)
			}
			data, err := io.ReadAll(io.LimitReader(rc, 64<<20))
			rc.Close()
			if err != nil {
				return nil, crud.Invalid("ZIP-Datei %s: %v", f.Name, err)
			}
			out = append(out, string(data))
		}
		if len(out) == 0 {
			return nil, crud.Invalid("ZIP-Datei ohne XML-Dateien")
		}
		return out, nil
	}
	if utf8.Valid(b) {
		return []string{string(b)}, nil
	}
	r := make([]rune, len(b)) // Latin-1 (z. B. CSV-Exporte)
	for i, c := range b {
		r[i] = rune(c)
	}
	return []string{string(r)}, nil
}

// isCamt: Inhalt ist ein camt-Dokument (XML mit BkToCstmrStmt bzw. BkToCstmrAcctRpt).
func isCamt(text string) bool {
	t := strings.TrimSpace(strings.TrimPrefix(text, "\ufeff"))
	return strings.HasPrefix(t, "<") && (strings.Contains(t, "BkToCstmrStmt") || strings.Contains(t, "BkToCstmrAcctRpt"))
}

type camtDoc struct {
	Stmts []camtStmt `xml:"BkToCstmrStmt>Stmt"`
	Rpts  []camtStmt `xml:"BkToCstmrAcctRpt>Rpt"`
}

type camtStmt struct {
	IBAN    string      `xml:"Acct>Id>IBAN"`
	Entries []camtEntry `xml:"Ntry"`
}

type camtAmount struct {
	Value string `xml:",chardata"`
	Ccy   string `xml:"Ccy,attr"`
}

type camtDate struct {
	Dt   string `xml:"Dt"`
	DtTm string `xml:"DtTm"`
}

func (d camtDate) date() string {
	if d.Dt != "" {
		return strings.TrimSpace(d.Dt)
	}
	if len(strings.TrimSpace(d.DtTm)) >= 10 {
		return strings.TrimSpace(d.DtTm)[:10]
	}
	return ""
}

type camtParty struct {
	Nm    string `xml:"Nm"`
	PtyNm string `xml:"Pty>Nm"` // ab camt.053.001.08
	ID    string `xml:"Id>PrvtId>Othr>Id"`
	PtyID string `xml:"Pty>Id>PrvtId>Othr>Id"`
}

func (p camtParty) name() string { return strings.TrimSpace(firstNonEmpty(p.Nm, p.PtyNm)) }

// camtStatus: bis Version 07 Text (BOOK), ab 08 Element Cd.
type camtStatus struct {
	Text string `xml:",chardata"`
	Cd   string `xml:"Cd"`
}

type camtEntry struct {
	Amt      camtAmount `xml:"Amt"`
	CdtDbt   string     `xml:"CdtDbtInd"`
	Sts      camtStatus `xml:"Sts"`
	Bookg    camtDate   `xml:"BookgDt"`
	Val      camtDate   `xml:"ValDt"`
	SvcrRef  string     `xml:"AcctSvcrRef"`
	Info     string     `xml:"AddtlNtryInf"`
	Prtry    string     `xml:"BkTxCd>Prtry>Cd"`
	Txs      []camtTx   `xml:"NtryDtls>TxDtls"`
	Reversal string     `xml:"RvslInd"`
}

type camtTx struct {
	Amt       camtAmount `xml:"Amt"`
	TxAmt     camtAmount `xml:"AmtDtls>TxAmt>Amt"`
	EndToEnd  string     `xml:"Refs>EndToEndId"`
	Mandate   string     `xml:"Refs>MndtId"`
	InstrID   string     `xml:"Refs>InstrId"`
	Cdtr      camtParty  `xml:"RltdPties>Cdtr"`
	CdtrIBAN  string     `xml:"RltdPties>CdtrAcct>Id>IBAN"`
	Dbtr      camtParty  `xml:"RltdPties>Dbtr"`
	DbtrIBAN  string     `xml:"RltdPties>DbtrAcct>Id>IBAN"`
	CdtrBIC   string     `xml:"RltdAgts>CdtrAgt>FinInstnId>BIC"`
	CdtrBICFI string     `xml:"RltdAgts>CdtrAgt>FinInstnId>BICFI"`
	DbtrBIC   string     `xml:"RltdAgts>DbtrAgt>FinInstnId>BIC"`
	DbtrBICFI string     `xml:"RltdAgts>DbtrAgt>FinInstnId>BICFI"`
	Ustrd     []string   `xml:"RmtInf>Ustrd"`
	SchemeID  string     `xml:"CdtrSchmeId>Id>PrvtId>Othr>Id"`
	AddtlInfo string     `xml:"AddtlTxInf"`
	Purpose   string     `xml:"Purp>Cd"`
}

// parseCamt: gebuchte Umsätze aller Auszüge; ownIBAN je Auszug zur Prüfung.
func parseCamt(texts []string, decimals int) ([]parsedRow, []string, error) {
	var rows []parsedRow
	var errs []string
	line := 0
	for fi, text := range texts {
		var doc camtDoc
		if err := xml.Unmarshal([]byte(strings.TrimPrefix(text, "\ufeff")), &doc); err != nil {
			return nil, nil, crud.Invalid("camt-Datei %d: %v", fi+1, err)
		}
		stmts := append(doc.Stmts, doc.Rpts...)
		if len(stmts) == 0 {
			return nil, nil, crud.Invalid("camt-Datei %d: kein Auszug (BkToCstmrStmt/BkToCstmrAcctRpt)", fi+1)
		}
		for _, st := range stmts {
			for _, e := range st.Entries {
				line++
				if s := strings.ToUpper(strings.TrimSpace(firstNonEmpty(e.Sts.Cd, e.Sts.Text))); s != "" && s != "BOOK" {
					continue // vorgemerkt bzw. Information
				}
				debit := strings.TrimSpace(e.CdtDbt) == "DBIT"
				book := e.Bookg.date()
				if book == "" {
					errs = append(errs, fmt.Sprintf("Eintrag %d: Buchungstag fehlt", line))
					continue
				}
				total, err := parseDecimal(e.Amt.Value, ".", decimals)
				if err != nil {
					errs = append(errs, fmt.Sprintf("Eintrag %d: %v", line, err))
					continue
				}
				txs := e.Txs
				split := len(txs) > 1
				if split {
					var sum int64
					for _, tx := range txs {
						a, err := parseDecimal(firstNonEmpty(tx.Amt.Value, tx.TxAmt.Value), ".", decimals)
						if err != nil {
							split = false
							break
						}
						sum += a
					}
					split = split && sum == total
				}
				if !split {
					txs = txs[:min(len(txs), 1)]
					if len(txs) == 0 {
						txs = []camtTx{{}}
					}
				}
				for _, tx := range txs {
					amt := total
					if split {
						amt, _ = parseDecimal(firstNonEmpty(tx.Amt.Value, tx.TxAmt.Value), ".", decimals)
					}
					if debit {
						amt = -amt
					}
					party, iban, bic := tx.Dbtr, tx.DbtrIBAN, firstNonEmpty(tx.DbtrBICFI, tx.DbtrBIC)
					if debit {
						party, iban, bic = tx.Cdtr, tx.CdtrIBAN, firstNonEmpty(tx.CdtrBICFI, tx.CdtrBIC)
					}
					f := map[string]string{
						"currency": strings.TrimSpace(e.Amt.Ccy), "counterparty_name": party.name(),
						"counterparty_iban": strings.ReplaceAll(strings.TrimSpace(iban), " ", ""), "counterparty_bic": strings.TrimSpace(bic),
						"purpose":          strings.TrimSpace(strings.Join(trimAll(tx.Ustrd), " ")),
						"booking_text":     strings.TrimSpace(firstNonEmpty(e.Info, tx.AddtlInfo)),
						"transaction_type": strings.TrimSpace(e.Prtry),
						"end_to_end_ref":   notProvided(tx.EndToEnd), "mandate_ref": strings.TrimSpace(tx.Mandate),
						"creditor_id":  strings.TrimSpace(firstNonEmpty(tx.SchemeID, tx.Cdtr.ID, tx.Cdtr.PtyID)),
						"customer_ref": notProvided(tx.InstrID), "own_iban": strings.ReplaceAll(strings.TrimSpace(st.IBAN), " ", ""),
					}
					for k, v := range f {
						if v == "" {
							delete(f, k)
						}
					}
					rows = append(rows, parsedRow{Line: line, BookingDate: book, ValueDate: e.Val.date(), Amount: amt, Fields: f})
				}
			}
		}
	}
	stableSortByDate(rows)
	return rows, errs, nil
}

func trimAll(l []string) []string {
	out := make([]string, 0, len(l))
	for _, s := range l {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// notProvided: „NOTPROVIDED“ der SEPA-Referenzen ist keine Referenz.
func notProvided(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "NOTPROVIDED") {
		return ""
	}
	return s
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
