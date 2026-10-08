package bank

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

func (m *Module) account() *crud.Entity {
	match := map[string]string{"company_code": "company_code", "account_id": "account_id"}
	return &crud.Entity{
		Object: accountObject, Title: "Bankkonten", Icon: "icon-bank", Table: "bank__account", Section: "Bank",
		Keys: []string{"company_code", "account_id"}, Order: "company_code, account_id", StatusField: "is_active", TitleField: "designation",
		Filters: []string{"company_code"}, Search: []string{"account_id", "designation", "iban"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "account_id", Label: "Kurzzeichen", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "iban", Label: "IBAN", Type: tText, Required: true, Listable: true},
			{Key: "bic", Label: "BIC", Type: tText},
			{Key: "bank_name", Label: "Bank", Type: tText},
			{Key: "currency", Label: "Währung", Type: tText, Required: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "contract_id", Label: "Vertrag Bankkonto (optional)", Type: tText, Lookup: lookupContract},
			{Key: "gl_account", Label: "Sachkonto Bank", Type: tText, Required: true, Group: "Buchung", Lookup: lookupAccount},
			{Key: "document_type_in", Label: "Belegart Debitorenzahlung (Mieter, Kunden – Ein- und Auszahlung)", Type: tText, Group: "Buchung", Lookup: lookupDocType},
			{Key: "document_type_out", Label: "Belegart Kreditorenzahlung (Lieferanten – Aus- und Einzahlung)", Type: tText, Group: "Buchung", Lookup: lookupDocType},
			{Key: "document_type_gl", Label: "Belegart an Sachkonto (Entgelte, Zinsen)", Type: tText, Group: "Buchung", Lookup: lookupDocType},
			{Key: "post_out_of_order", Label: "Nicht zugeordnete Umsätze überspringen (sonst hält die Buchung dort an)", Type: tBool, Group: "Buchung"},
			{Key: "format", Label: "Importformat (Vorschlag)", Type: tText, Group: "Einlesen", Lookup: lookupFormat},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			// Eingaben der Aktion „Kontoauszug einlesen …“
			{Key: "import_format", Label: "Importformat (leer = Vorschlag des Bankkontos)", Type: tText, ActionOnly: true, Lookup: lookupFormat},
			{Key: "file", Label: "Datei (CSV)", Type: tFile, Required: true, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "umsaetze", Title: "Umsätze", Relation: &metamodel.Relation{Object: txnObject, ForeignKey: "account_id", Match: match,
				Columns: []string{"txn_no", "booking_date", "amount", "counterparty_name", "purpose", "status", "target_type", "contract_id", "partner_id", "document_number"}}},
			{Key: "einlesungen", Title: "Einlesungen", Collapsed: true, Relation: &metamodel.Relation{Object: importObject, ForeignKey: "account_id", Match: match,
				Columns: []string{"import_id", "file_name", "imported_at", "rows_read", "rows_new", "rows_duplicate", "date_from", "date_to"}}},
			{Key: "dokumente", Title: "Dokumente", Collapsed: true, Documents: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkAccount,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "importFile", Label: "Kontoauszug einlesen …", Record: true,
				Fields: []string{"import_format", "file"}}, Handle: m.importAction},
			{ActionConfig: metamodel.ActionConfig{Name: "match", Label: "Zuordnen", Record: true,
				Confirm: "Offene Umsätze maschinell zuordnen (Regeln, Verwendungszweck, Rechnungen, IBAN, Name)?"}, Handle: m.matchAction},
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true,
				Confirm: "Zugeordnete Umsätze in der Reihenfolge der Zahlungen buchen?"}, Handle: m.postAction},
		},
	}
}

func (m *Module) checkAccount(ctx context.Context, rec, _ crud.Record) error {
	cc := crud.Str(rec["company_code"])
	rec["account_id"] = trimUpper(rec["account_id"])
	iban := strings.ReplaceAll(trimUpper(rec["iban"]), " ", "")
	if !ibanValid(iban) || !ibanLengthOK(iban) {
		return crud.Invalid("IBAN %s ist ungültig (Prüfziffer oder Länge)", iban)
	}
	rec["iban"] = iban
	res, err := m.db.Query(ctx, `SELECT account_id FROM bank__account WHERE company_code = ? AND iban = ? AND account_id <> ?`, cc, iban, rec["account_id"])
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		return crud.Invalid("IBAN %s gehört schon zum Bankkonto %s", iban, crud.Str(res.Rows[0][0]))
	}
	rec["currency"] = trimUpper(rec["currency"])
	rec["bic"] = nilIfEmpty(strings.ReplaceAll(trimUpper(rec["bic"]), " ", ""))
	defaults(rec, map[string]any{"document_type_in": "DZ", "document_type_out": "KZ", "document_type_gl": "SA",
		"post_out_of_order": false})
	for _, k := range []string{"document_type_in", "document_type_out", "document_type_gl"} {
		rec[k] = trimUpper(rec[k])
	}
	if rec["gl_account"], err = m.glAccount(ctx, cc, crud.Str(rec["gl_account"])); err != nil {
		return err
	}
	if c := strings.TrimSpace(crud.Str(rec["contract_id"])); c != "" {
		if _, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + c}); err != nil {
			return crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht", c, cc)
		}
		rec["contract_id"] = c
	} else {
		rec["contract_id"] = nil
	}
	if f := trimUpper(rec["format"]); f != "" {
		if _, err := m.loadFormat(ctx, cc, f); err != nil {
			return err
		}
		rec["format"] = f
	} else {
		rec["format"] = nil
	}
	return nil
}

// accountRow: Kopf eines Bankkontos.
type accountRow struct {
	CC, ID, IBAN, Currency, GLAccount, Format, DocIn, DocOut, DocGL string
	OutOfOrder                                                      bool
}

func (m *Module) accountOf(ctx context.Context, cc, id string) (*accountRow, error) {
	res, err := m.db.Query(ctx, `SELECT account_id, iban, currency, gl_account, format, document_type_in, document_type_out, document_type_gl,
		post_out_of_order FROM bank__account WHERE company_code = ? AND account_id = ?`, cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Bankkonto %s gibt es im Buchungskreis %s nicht", id, cc)
	}
	r := res.Rows[0]
	return &accountRow{CC: cc, ID: crud.Str(r[0]), IBAN: crud.Str(r[1]), Currency: crud.Str(r[2]), GLAccount: crud.Str(r[3]), Format: crud.Str(r[4]),
		DocIn: crud.Str(r[5]), DocOut: crud.Str(r[6]), DocGL: crud.Str(r[7]), OutOfOrder: crud.AsBool(r[8])}, nil
}

func (m *Module) accountOfRequest(ctx context.Context, payload any) (*accountRow, map[string]any, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(payload, &in); err != nil {
		return nil, nil, err
	}
	key, err := m.set.Entity(accountObject).ParseID(in.ID)
	if err != nil {
		return nil, nil, err
	}
	a, err := m.accountOf(ctx, crud.Str(key["company_code"]), crud.Str(key["account_id"]))
	return a, in.Data, err
}

// --- Einlesen ----------------------------------------------------------------------

func (m *Module) importAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	a, data, err := m.accountOfRequest(ctx, req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	return m.importFile(ctx, a, trimUpper(data["import_format"]), crud.Str(data["file_name"]), crud.Str(data["file"]))
}

// importCommand: console bank:import --company --account [--format] --file.
func (m *Module) importCommand(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		Account string `json:"account"`
		Format  string `json:"format"`
		File    any    `json:"file"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	a, err := m.accountOf(ctx, companyOf(req.Payload), strings.ToUpper(strings.TrimSpace(in.Account)))
	if err != nil {
		return sdk.Response{}, err
	}
	formatCode := strings.ToUpper(strings.TrimSpace(in.Format))
	text, _ := in.File.(string)
	if rows, ok := in.File.([]any); ok {
		// Die Konsole liest .csv selbst (Zeilen: Spaltenname → Wert) – wieder als CSV
		code := formatCode
		if code == "" {
			code = a.Format
		}
		f, err := m.loadFormat(ctx, a.CC, code)
		if err != nil {
			return sdk.Response{}, err
		}
		if text, err = rowsToCSV(rows, f.Delimiter); err != nil {
			return sdk.Response{}, err
		}
	}
	if text == "" {
		return sdk.Response{}, crud.Invalid("Datei (--file) ist Pflicht: Text (CSV)")
	}
	return m.importFile(ctx, a, formatCode, "", text)
}

func (m *Module) importFile(ctx context.Context, a *accountRow, formatCode, fileName, text string) (sdk.Response, error) {
	if err := requireWrite(ctx, accountObject, "importFile", a.CC); err != nil {
		return sdk.Response{}, err
	}
	if formatCode == "" {
		formatCode = a.Format
	}
	if formatCode == "" {
		return sdk.Response{}, crud.Invalid("Importformat ist Pflicht (oder am Bankkonto als Vorschlag hinterlegen)")
	}
	if strings.TrimSpace(text) == "" {
		return sdk.Response{}, crud.Invalid("Datei ist leer")
	}
	f, err := m.loadFormat(ctx, a.CC, formatCode)
	if err != nil {
		return sdk.Response{}, err
	}
	rows, rowErrs, err := f.parse(text, m.currencyDecimals(ctx, a.Currency))
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range rows {
		if own := strings.ReplaceAll(strings.ToUpper(r.Fields["own_iban"]), " ", ""); own != "" && own != a.IBAN {
			return sdk.Response{}, crud.Invalid("Datei gehört zum Konto %s, nicht zum Bankkonto %s (%s)", own, a.ID, a.IBAN)
		}
		if cur := strings.ToUpper(strings.TrimSpace(r.Fields["currency"])); cur != "" && cur != a.Currency {
			rowErrs = append(rowErrs, fmt.Sprintf("Zeile %d: Währung %s statt %s", r.Line, cur, a.Currency))
		}
	}
	if len(rowErrs) > 0 {
		return sdk.Response{}, crud.Invalid("Datei nicht eingelesen – %d Fehler: %s", len(rowErrs), strings.Join(first(rowErrs, 5), "; "))
	}
	existing := map[string]bool{}
	res, err := m.db.Query(ctx, `SELECT fingerprint FROM bank__transaction WHERE company_code = ? AND account_id = ?`, a.CC, a.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range res.Rows {
		existing[crud.Str(r[0])] = true
	}
	var newRows []parsedRow
	var prints []string
	seen := map[string]int{}
	for _, r := range rows {
		fp := fingerprint(r)
		seen[fp]++
		fp = fmt.Sprintf("%s#%d", fp, seen[fp]) // gleiche Umsätze am selben Tag (z. B. zwei Mieten)
		if existing[fp] {
			continue
		}
		newRows, prints = append(newRows, r), append(prints, fp)
	}
	importID := ""
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		n, err := m.db.Query(ctx, `SELECT COUNT(*) FROM bank__import WHERE company_code = ?`, a.CC)
		if err != nil {
			return err
		}
		importID = fmt.Sprintf("%s-%04d", a.ID, toInt(n.Rows[0][0])+1)
		max, err := m.db.Query(ctx, `SELECT COALESCE(MAX(txn_no), 0) FROM bank__transaction WHERE company_code = ? AND account_id = ?`, a.CC, a.ID)
		if err != nil {
			return err
		}
		no := toInt(max.Rows[0][0])
		for i, r := range newRows {
			no++
			f := r.Fields
			cur := strings.ToUpper(strings.TrimSpace(f["currency"]))
			if cur == "" {
				cur = a.Currency
			}
			cp := strings.ReplaceAll(strings.ToUpper(f["counterparty_iban"]), " ", "")
			if cp == a.IBAN {
				cp = "" // eigene IBAN im Text ist nicht die Gegenseite
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO bank__transaction (company_code, account_id, txn_no, booking_date, value_date, amount, currency,
				counterparty_name, counterparty_iban, counterparty_bic, purpose, booking_text, transaction_type, end_to_end_ref, mandate_ref, creditor_id,
				customer_ref, import_id, fingerprint, status, changed_at, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				a.CC, a.ID, no, r.BookingDate, nilIfEmpty(r.ValueDate), r.Amount, cur, nilIfEmpty(f["counterparty_name"]),
				nilIfEmpty(cp), nilIfEmpty(f["counterparty_bic"]),
				nilIfEmpty(f["purpose"]), nilIfEmpty(f["booking_text"]), nilIfEmpty(f["transaction_type"]), nilIfEmpty(f["end_to_end_ref"]),
				nilIfEmpty(f["mandate_ref"]), nilIfEmpty(f["creditor_id"]), nilIfEmpty(f["customer_ref"]), importID, prints[i], stOpen, now(),
				nilIfEmpty(sdk.CallFromContext(ctx).UserID)); err != nil {
				return err
			}
		}
		var from, to any
		if len(rows) > 0 {
			from, to = rows[0].BookingDate, rows[len(rows)-1].BookingDate
		}
		msg := fmt.Sprintf("%d Umsätze gelesen, %d neu, %d schon vorhanden", len(rows), len(newRows), len(rows)-len(newRows))
		_, err = m.db.Exec(ctx, `INSERT INTO bank__import (company_code, import_id, account_id, format, file_name, imported_at, imported_by, rows_read,
			rows_new, rows_duplicate, date_from, date_to, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.CC, importID, a.ID, f.Code,
			nilIfEmpty(fileName), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), len(rows), len(newRows), len(rows)-len(newRows), from, to, msg)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	matched, err := m.matchAccount(ctx, a)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"import_id": importID, "rows_read": len(rows), "rows_new": len(newRows),
		"rows_duplicate": len(rows) - len(newRows),
		"message": fmt.Sprintf("Einlesung %s: %d Umsätze gelesen, %d neu, %d schon vorhanden. %s", importID, len(rows), len(newRows),
			len(rows)-len(newRows), matched.text())}}, nil
}

// rowsToCSV: Zeilen (Spaltenname → Wert) als CSV mit Kopfzeile; die Spalten
// sortiert – das Importformat findet sie über den Namen.
func rowsToCSV(rows []any, delimiter string) (string, error) {
	var cols []string
	seen := map[string]bool{}
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			return "", crud.Invalid("Datei: Zeilen mit Spaltennamen erwartet")
		}
		for k := range m {
			if !seen[k] {
				seen[k], cols = true, append(cols, k)
			}
		}
	}
	sort.Strings(cols)
	var b strings.Builder
	w := csv.NewWriter(&b)
	w.Comma = []rune(delimiter)[0]
	_ = w.Write(cols)
	for _, r := range rows {
		m := r.(map[string]any)
		rec := make([]string, len(cols))
		for i, c := range cols {
			if m[c] != nil {
				rec[i] = crud.Str(m[c])
			}
		}
		_ = w.Write(rec)
	}
	w.Flush()
	return b.String(), w.Error()
}

// fingerprint: Umsatz ohne Rücksicht auf Leerzeichen und Groß-/Kleinschreibung.
func fingerprint(r parsedRow) string {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	h := sha256.Sum256([]byte(strings.Join([]string{r.BookingDate, r.ValueDate, fmt.Sprint(r.Amount), norm(r.Fields["counterparty_name"]),
		norm(r.Fields["purpose"]), norm(r.Fields["booking_text"]), norm(r.Fields["end_to_end_ref"])}, "|")))
	return hex.EncodeToString(h[:16])
}

func first(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("… %d weitere", len(s)-n))
	}
	return s
}

// --- Einrichtung -------------------------------------------------------------------

// defaultFormats: Vorschläge für Importformate (Kontoumsätze als CSV).
var defaultFormats = []struct {
	code, name, delimiter, dateFormat, decimal string
	newestFirst                                bool
	columns                                    []columnDef
	patterns                                   map[string]string
}{
	{code: "COBA", name: "Commerzbank – Umsätze als CSV", delimiter: ";", dateFormat: "DD.MM.YYYY", decimal: ",", newestFirst: true,
		columns: []columnDef{
			{Target: "booking_date", Source: "Buchungstag"}, {Target: "value_date", Source: "Wertstellung"},
			{Target: "transaction_type", Source: "Umsatzart"}, {Target: "booking_text", Source: "Buchungstext", Transform: "TRIM"},
			{Target: "amount", Source: "Betrag"}, {Target: "currency", Source: "Währung"}, {Target: "own_iban", Source: "IBAN Kontoinhaber"},
			{Target: "counterparty_name", Source: "Sender", Transform: "TRIM"},
			{Target: "counterparty_name", Source: "Empfänger", Transform: "TRIM", Fallback: true},
			{Target: "purpose", Source: "Verwendungszweck", Transform: "TRIM"},
			{Target: "counterparty_iban", Source: "Buchungstext", Transform: "IBAN"},
			{Target: "end_to_end_ref", Source: "Buchungstext", Transform: "EXTRACT"},
			{Target: "mandate_ref", Source: "Buchungstext", Transform: "EXTRACT"},
			{Target: "creditor_id", Source: "Buchungstext", Transform: "EXTRACT"},
			{Target: "customer_ref", Source: "Buchungstext", Transform: "EXTRACT"},
		},
		patterns: map[string]string{
			"end_to_end_ref": `End-to-End-Ref\.:\s*(\S+)`, "mandate_ref": `Mandatsref\.?:\s*(\S+)`,
			"creditor_id": `Gläubiger-ID:\s*(\S+)`, "customer_ref": `Kundenreferenz:\s*(\S+)`,
		}},
}

func (m *Module) setupCompanyAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	cc := companyOf(req.Payload)
	if cc == "" {
		return sdk.Response{}, crud.Invalid("Buchungskreis (company) ist Pflicht")
	}
	if err := requireWrite(ctx, formatObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	n := 0
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, f := range defaultFormats {
			res, err := m.db.Query(ctx, `SELECT 1 FROM bank__format WHERE company_code = ? AND code = ?`, cc, f.code)
			if err != nil {
				return err
			}
			if len(res.Rows) > 0 {
				continue
			}
			if _, err := m.db.Exec(ctx, `INSERT INTO bank__format (company_code, code, name, delimiter, skip_lines, no_header, date_format, decimal_separator,
				newest_first, is_active) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, cc, f.code, f.name, f.delimiter, 0, false, f.dateFormat, f.decimal,
				f.newestFirst, true); err != nil {
				return err
			}
			for i, c := range f.columns {
				transform := c.Transform
				if transform == "" {
					transform = "NONE"
				}
				var pattern any
				if transform == "EXTRACT" {
					pattern = f.patterns[c.Target]
				}
				if _, err := m.db.Exec(ctx, `INSERT INTO bank__format_column (company_code, format, line_no, target, source, transform, pattern, fallback)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, cc, f.code, (i+1)*10, c.Target, c.Source, transform, pattern, c.Fallback); err != nil {
					return err
				}
			}
			n++
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"company_code": cc, "formats": n,
		"message": fmt.Sprintf("Buchungskreis %s: %d Importformate angelegt", cc, n)}}, nil
}
