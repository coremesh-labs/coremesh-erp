package ledger

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Verweise an Belegpositionen (seit 0.35.0): Der Beleg allein soll auswertbar
// sein – Rechnung, Vertrag, Objekt und Belastung an stehen an allen Positionen,
// deren Feldstatus sie zulässt. Fachmodule schreiben sie beim Buchen; fehlen sie
// an gebuchten Belegen, trägt sie JournalReference.fill nach (Migrationen der
// Module). Beim Ausgleich offener Posten übernimmt ein Posten die Verweise seiner
// Gegenposten (Katalog „Vererbte Kontierungen“), z. B. die Zahlung Rechnung,
// Vertrag und Objekt der Rechnung. Ergänzt werden nur leere Felder, nur eindeutige
// Werte, jede Position mit Eintrag im Änderungsprotokoll; Beträge, Konten und Daten
// bleiben unverändert.

const refObject = "JournalReference"

// inheritFieldSeeds: Felder, die der Ausgleich weitergibt (abschaltbar).
var inheritFieldSeeds = func() sdk.SchemaSeed {
	rows := []map[string]any{}
	for _, f := range []string{"invoice_id", "rent_contract_id", "rent_object_id", "dimension_custom_1", "dimension_custom_2", "asset_id", "charge_type", "charge_id"} {
		rows = append(rows, map[string]any{"field_name": f, "is_active": true})
	}
	return sdk.SchemaSeed{Table: "ledger__inherit_field", Rows: rows}
}()

const inheritReason = "Übernommen aus dem Ausgleich offener Posten"

// fillField: leere Felder field eines Belegs mit value füllen – nur wo der Feldstatus
// des Kontos es zulässt, keine Steuerpositionen; partnerOnly: nur Debitoren- und
// Kreditorenpositionen; line > 0: nur diese Position. Anzahl der Positionen.
func (m *Module) fillField(ctx context.Context, key docKey, field, value, reason string, partnerOnly bool, line int64) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	if !slices.Contains(dimColumns, field) {
		return 0, crud.Invalid("Feld %q ist keine Kontierung", field)
	}
	res, err := m.db.Query(ctx, `SELECT i.ledger, i.line_item_number FROM ledger__journal_item i
		LEFT JOIN ledger__account_company c ON c.company_code_id = i.company_code_id AND c.account_number = i.account_number
		LEFT JOIN ledger__field_status s ON s.group_id = COALESCE(c.field_status_group, ?) AND s.field_name = ?
		WHERE i.company_code_id = ? AND i.fiscal_year = ? AND i.document_number = ? AND COALESCE(i.`+field+`, '') = ''
		AND COALESCE(i.item_type, '') <> ? AND COALESCE(s.status, ?) <> ? AND (? = 0 OR i.account_kind IN ('D', 'K'))
		AND (? = 0 OR i.line_item_number = ?)
		ORDER BY i.line_item_number, i.ledger`, defaultFSG, field, key.CompanyCode, key.FiscalYear, key.Number, itemTax, statusOptional, statusSuppress,
		partnerOnly, line, line)
	if err != nil || len(res.Rows) == 0 {
		return 0, err
	}
	no := toInt(m.text(ctx, `SELECT COALESCE(MAX(change_no), 0) FROM ledger__journal_change
		WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`, key.CompanyCode, key.FiscalYear, key.Number))
	at, user := time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	logged := map[int64]bool{}
	for _, r := range res.Rows {
		ln := toInt(r[1])
		if _, err := m.db.Exec(ctx, `UPDATE ledger__journal_item SET `+field+` = ?
			WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ? AND ledger = ? AND line_item_number = ?`,
			value, key.CompanyCode, key.FiscalYear, key.Number, r[0], ln); err != nil {
			return 0, err
		}
		if logged[ln] { // je Position einmal, auch bei mehreren Ledgern
			continue
		}
		logged[ln] = true
		no++
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__journal_change (company_code_id, fiscal_year, document_number, change_no, field_name,
			old_value, new_value, reason, changed_at, changed_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			key.CompanyCode, key.FiscalYear, key.Number, no, field, nil, fmt.Sprintf("Pos. %d: %s", ln, value),
			nilIfEmpty(reason), at, user); err != nil {
			return 0, err
		}
	}
	return len(logged), nil
}

// inheritFields: aktive Felder des Katalogs „Vererbte Kontierungen“.
func (m *Module) inheritFields(ctx context.Context) ([]string, error) {
	res, err := m.db.Query(ctx, `SELECT field_name FROM ledger__inherit_field WHERE is_active = ? ORDER BY field_name`, true)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range res.Rows {
		if f := crud.Str(r[0]); slices.Contains(dimColumns, f) {
			out = append(out, f)
		}
	}
	return out, nil
}

// inheritRefs: Verweise über den Ausgleich weitergeben – an die Debitoren- und
// Kreditorenposten der Belege docs und ihrer Gegenbelege (docs nil: alle Belege
// mit Ausgleich). Ein leeres Feld übernimmt den Wert, wenn alle Gegenposten genau
// diesen einen nennen. Runden, bis nichts mehr dazukommt (Rechnung → Zahlung →
// Erstattung). Anzahl der ergänzten Felder.
func (m *Module) inheritRefs(ctx context.Context, cc string, docs []docKey) (int, error) {
	fields, err := m.inheritFields(ctx)
	if err != nil || len(fields) == 0 {
		return 0, err
	}
	cfg, err := m.config(ctx, cc)
	if err != nil {
		return 0, err
	}
	total := 0
	for round := 0; round < 5; round++ {
		var scope []docKey
		if docs != nil {
			if len(docs) == 0 {
				break
			}
			if scope, err = m.clearingNeighbours(ctx, cc, cfg.Ledger, docs); err != nil {
				return total, err
			}
		}
		changed := map[docKey]bool{}
		for _, f := range fields {
			q := `SELECT i.fiscal_year, i.document_number, i.line_item_number,
				COUNT(DISTINCT NULLIF(o.` + f + `, '')), MIN(NULLIF(o.` + f + `, '')), SUM(CASE WHEN COALESCE(o.` + f + `, '') = '' THEN 1 ELSE 0 END)
				FROM ledger__journal_item i
				JOIN ledger__clearing c ON c.company_code_id = i.company_code_id AND c.ledger = i.ledger
					AND ((c.debit_fiscal_year = i.fiscal_year AND c.debit_document_number = i.document_number AND c.debit_line = i.line_item_number)
					  OR (c.credit_fiscal_year = i.fiscal_year AND c.credit_document_number = i.document_number AND c.credit_line = i.line_item_number))
				JOIN ledger__journal_item o ON o.company_code_id = c.company_code_id AND o.ledger = c.ledger
					AND ((o.fiscal_year = c.debit_fiscal_year AND o.document_number = c.debit_document_number AND o.line_item_number = c.debit_line)
					  OR (o.fiscal_year = c.credit_fiscal_year AND o.document_number = c.credit_document_number AND o.line_item_number = c.credit_line))
					AND NOT (o.fiscal_year = i.fiscal_year AND o.document_number = i.document_number AND o.line_item_number = i.line_item_number)
				LEFT JOIN ledger__account_company ac ON ac.company_code_id = i.company_code_id AND ac.account_number = i.account_number
				LEFT JOIN ledger__field_status s ON s.group_id = COALESCE(ac.field_status_group, ?) AND s.field_name = ?
				WHERE i.company_code_id = ? AND i.ledger = ? AND i.account_kind IN ('D', 'K') AND COALESCE(i.` + f + `, '') = ''
				AND COALESCE(s.status, ?) <> ?`
			args := []any{defaultFSG, f, cc, cfg.Ledger, statusOptional, statusSuppress}
			if scope != nil {
				var or []string
				for _, d := range scope {
					or = append(or, "(i.fiscal_year = ? AND i.document_number = ?)")
					args = append(args, d.FiscalYear, d.Number)
				}
				q += " AND (" + strings.Join(or, " OR ") + ")"
			}
			q += " GROUP BY i.fiscal_year, i.document_number, i.line_item_number"
			res, err := m.db.Query(ctx, q, args...)
			if err != nil {
				return total, err
			}
			for _, r := range res.Rows {
				if toInt(r[3]) != 1 || toInt(r[5]) != 0 {
					continue
				}
				k := docKey{CompanyCode: cc, FiscalYear: int(toInt(r[0])), Number: crud.Str(r[1])}
				n, err := m.fillField(ctx, k, f, crud.Str(r[4]), inheritReason, true, toInt(r[2]))
				if err != nil {
					return total, err
				}
				if n > 0 {
					total += n
					changed[k] = true
				}
			}
		}
		if len(changed) == 0 {
			break
		}
		docs = docs[:0:0]
		for k := range changed {
			docs = append(docs, k)
		}
	}
	return total, nil
}

// clearingNeighbours: die Belege docs und alle mit ihnen ausgeglichenen Belege.
func (m *Module) clearingNeighbours(ctx context.Context, cc, ledger string, docs []docKey) ([]docKey, error) {
	seen := map[docKey]bool{}
	var out []docKey
	add := func(k docKey) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, d := range docs {
		add(d)
		res, err := m.db.Query(ctx, `SELECT credit_fiscal_year, credit_document_number FROM ledger__clearing
			WHERE company_code_id = ? AND ledger = ? AND debit_fiscal_year = ? AND debit_document_number = ?
			UNION SELECT debit_fiscal_year, debit_document_number FROM ledger__clearing
			WHERE company_code_id = ? AND ledger = ? AND credit_fiscal_year = ? AND credit_document_number = ?`,
			cc, ledger, d.FiscalYear, d.Number, cc, ledger, d.FiscalYear, d.Number)
		if err != nil {
			return nil, err
		}
		for _, r := range res.Rows {
			add(docKey{CompanyCode: cc, FiscalYear: int(toInt(r[0])), Number: crud.Str(r[1])})
		}
	}
	return out, nil
}

// fillReferences: JournalReference.fill {id, data: {fields: {Feld: Wert}, lines:
// {"<Pos.>": {Feld: Wert}}, partner_only, change_reason}} – Verweise an einem
// gebuchten Beleg nachtragen (nur leere Felder, Feldstatus, Protokoll), danach an
// die Gegenposten weitergeben. Antwort: Anzahl der ergänzten Felder.
func (m *Module) fillReferences(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Fields      map[string]string            `json:"fields"`
			Lines       map[string]map[string]string `json:"lines"`
			PartnerOnly bool                         `json:"partner_only"`
			Reason      string                       `json:"change_reason"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := parseDocID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireCompanyCode(ctx, entryObject, addContractAction, key.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if m.text(ctx, `SELECT document_number FROM ledger__journal_header WHERE company_code_id = ? AND fiscal_year = ? AND document_number = ?`,
		key.CompanyCode, key.FiscalYear, key.Number) == "" {
		return sdk.Response{}, fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, in.ID)
	}
	for f := range in.Data.Fields {
		if !slices.Contains(dimColumns, f) {
			return sdk.Response{}, crud.Invalid("Feld %q ist keine Kontierung", f)
		}
	}
	reason := strings.TrimSpace(in.Data.Reason)
	n := 0
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		// Positionen zuerst (genauer), dann der ganze Beleg
		for ln, fs := range in.Data.Lines {
			line := toInt(ln)
			if line <= 0 {
				return crud.Invalid("Position %q", ln)
			}
			for _, f := range sortedKeys(fs) {
				c, err := m.fillField(ctx, key, f, fs[f], reason, in.Data.PartnerOnly, line)
				if err != nil {
					return err
				}
				n += c
			}
		}
		for _, f := range sortedKeys(in.Data.Fields) {
			c, err := m.fillField(ctx, key, f, in.Data.Fields[f], reason, in.Data.PartnerOnly, 0)
			if err != nil {
				return err
			}
			n += c
		}
		if n > 0 {
			c, err := m.inheritRefs(ctx, key.CompanyCode, []docKey{key})
			n += c
			return err
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "changed": n}}, nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// migrateRefs (0.35.0, einmal): Objekt, Rechnung und Belastung an an Debitoren- und
// Kreditorenpositionen zulassen (Feldstatusgruppen CUSTOMER und SUPPLIER, nur noch
// ausgeblendete Felder); danach Verweise über alle Ausgleiche weitergeben.
func (m *Module) migrateRefs(ctx context.Context) error {
	return m.once(ctx, "partner_refs_0.35.0", func(ctx context.Context) error {
		args := []any{statusOptional, statusSuppress}
		ph := make([]string, len(partnerRefFields))
		for i, f := range partnerRefFields {
			ph[i] = "?"
			args = append(args, f)
		}
		if _, err := m.db.Exec(ctx, `UPDATE ledger__field_status SET status = ? WHERE status = ? AND group_id IN ('CUSTOMER', 'SUPPLIER')
			AND field_name IN (`+strings.Join(ph, ", ")+`)`, args...); err != nil {
			return err
		}
		res, err := m.db.Query(ctx, `SELECT DISTINCT company_code_id FROM ledger__clearing`)
		if err != nil {
			return err
		}
		for _, r := range res.Rows {
			n, err := m.inheritRefs(ctx, crud.Str(r[0]), nil)
			if err != nil {
				return err
			}
			if n > 0 {
				m.log.InfoContext(ctx, "Verweise aus dem Ausgleich übernommen", "company_code", crud.Str(r[0]), "fields", n)
			}
		}
		return nil
	})
}

// once: Datenumstellung name einmal ausführen (Merker in ledger__migration).
func (m *Module) once(ctx context.Context, name string, step func(context.Context) error) error {
	res, err := m.db.Query(ctx, `SELECT 1 FROM ledger__migration WHERE name = ?`, name)
	if err != nil || len(res.Rows) > 0 {
		return err
	}
	return m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := step(ctx); err != nil {
			return err
		}
		_, err := m.db.Exec(ctx, `INSERT INTO ledger__migration (name, done_at) VALUES (?, ?)`, name, time.Now().UTC().Format(time.RFC3339))
		return err
	})
}
