package ledger

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/crud"
	"github.com/camel/coremesh/pkg/sdk/events"

	"github.com/camel/coremesh_erp/pkg/ledgerapi"
)

// PostingService verarbeitet Buchungsaufträge der Fachmodule (SD, RENT,
// PROCUREMENT …) und der Vorerfassung (MANUAL) synchron:
//
//  1. Kopf normalisieren, Recht JournalEntry.post im Buchungskreis prüfen
//  2. Steuerung des Buchungskreises lesen (ledger__company_config)
//  3. Geschäftsjahr/Periode bestimmen, Periodensperre (ledger__fiscal_period_status)
//     und Berechtigungen FiscalPeriod.post und DocumentType.post prüfen (authz.go)
//  4. Modul-Mapping auflösen (Kontierungen des Moduls → ACDOCA-Spalten)
//  5. Positionen prüfen: Konto (SKA1 aktiv, SKB1 vorhanden, nicht gesperrt),
//     Kontowährung, Abstimmkonten, Beträge
//  6. Soll = Haben in Belegwährung (Summe 0), Umrechnung in Hauswährung mit
//     Tageskurs, Rundungsdifferenz auf die größte Position
//  7. Belegnummer vergeben, Kopf (BKPF) und Einzelposten (ACDOCA) in einer
//     Transaktion schreiben
//
// Idempotenz: Gleiche source_reference (je Buchungskreis und Modul) liefert
// den vorhandenen Beleg statt einer Doppelbuchung.
type PostingService struct{ m *Module }

// plan ist ein vollständig geprüfter Beleg, bereit zum Schreiben.
type plan struct {
	req          ledgerapi.PostRequest
	cfg          *companyConfig
	fiscalYear   int
	period       int
	docDecimals  int
	locDecimals  int
	exchangeRate string
	items        []planItem
}

type planItem struct {
	line     int
	itemType string
	account  string
	side     string
	docMinor int64 // vorzeichenbehaftet: Soll +, Haben −
	locMinor int64
	text     string
	dims     map[string]string // ACDOCA-Spalte → Wert
}

// Post bucht einen Auftrag (eigene Transaktion).
func (s *PostingService) Post(ctx context.Context, req ledgerapi.PostRequest) (ledgerapi.PostResult, error) {
	var out ledgerapi.PostResult
	err := s.m.db.InTx(ctx, nil, func(ctx context.Context) error {
		var err error
		out, err = s.post(ctx, req, "")
		return err
	})
	return out, err
}

// post: Prüfen und Schreiben in der Transaktion des Aufrufers. draftID verknüpft
// den Beleg mit seiner Vorerfassung.
func (s *PostingService) post(ctx context.Context, req ledgerapi.PostRequest, draftID string) (ledgerapi.PostResult, error) {
	req = normalize(req)
	if dup, ok, err := s.existing(ctx, req); err != nil || ok {
		return dup, err
	}
	p, err := s.prepare(ctx, req)
	if err != nil {
		return ledgerapi.PostResult{}, err
	}
	return s.write(ctx, p, draftID)
}

// Simulate prüft einen Auftrag vollständig, ohne zu schreiben.
func (s *PostingService) Simulate(ctx context.Context, req ledgerapi.PostRequest) (*plan, error) {
	return s.prepare(ctx, normalize(req))
}

func normalize(req ledgerapi.PostRequest) ledgerapi.PostRequest {
	req.SourceModule = strings.ToUpper(strings.TrimSpace(req.SourceModule))
	req.CompanyCode = strings.TrimSpace(req.CompanyCode)
	req.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	req.DocumentType = strings.ToUpper(strings.TrimSpace(req.DocumentType))
	if req.DocumentType == "" {
		req.DocumentType = "SA"
	}
	req.SourceReference = strings.TrimSpace(req.SourceReference)
	return req
}

// existing: Beleg mit derselben source_reference (Idempotenz).
func (s *PostingService) existing(ctx context.Context, req ledgerapi.PostRequest) (ledgerapi.PostResult, bool, error) {
	if req.SourceReference == "" {
		return ledgerapi.PostResult{}, false, nil
	}
	res, err := s.m.db.Query(ctx, `SELECT id, document_number, fiscal_year, posting_period FROM ledger__journal_entry_header
		WHERE company_code_id = ? AND source_module = ? AND source_reference = ?`, req.CompanyCode, req.SourceModule, req.SourceReference)
	if err != nil || len(res.Rows) == 0 {
		return ledgerapi.PostResult{}, false, err
	}
	r := res.Rows[0]
	return ledgerapi.PostResult{ID: crud.Str(r[0]), DocumentNumber: crud.Str(r[1]), FiscalYear: int(toInt(r[2])),
		PostingPeriod: int(toInt(r[3])), Duplicate: true}, true, nil
}

func (s *PostingService) prepare(ctx context.Context, req ledgerapi.PostRequest) (*plan, error) {
	m := s.m
	// 1. Kopf und Recht
	switch {
	case !moduleKeyRe.MatchString(req.SourceModule):
		return nil, crud.Invalid("source_module %q: Modulkennung in Großbuchstaben, z. B. RENT", req.SourceModule)
	case req.CompanyCode == "":
		return nil, crud.Invalid("Buchungskreis (company_code) ist Pflicht")
	case !currencyRe.MatchString(req.Currency):
		return nil, crud.Invalid("Belegwährung %q: ISO-4217-Code erwartet", req.Currency)
	case len(req.Items) < 2:
		return nil, crud.Invalid("ein Beleg braucht mindestens zwei Positionen")
	case len(req.Items) > 999:
		return nil, crud.Invalid("höchstens 999 Positionen je Beleg")
	}
	if err := requireCompanyCode(ctx, entryObject, "post", req.CompanyCode); err != nil {
		return nil, err
	}
	postingDate, err := crud.ParseDate(req.PostingDate)
	if err != nil || strings.TrimSpace(req.PostingDate) == "" {
		return nil, crud.Invalid("Buchungsdatum (posting_date) JJJJ-MM-TT ist Pflicht")
	}
	req.PostingDate = postingDate
	if req.DocumentDate == "" {
		req.DocumentDate = postingDate
	} else if req.DocumentDate, err = crud.ParseDate(req.DocumentDate); err != nil {
		return nil, crud.Invalid("Belegdatum: %v", err)
	}

	// 2. Steuerung des Buchungskreises
	cfg, err := m.config(ctx, req.CompanyCode)
	if err != nil {
		return nil, err
	}
	p := &plan{req: req, cfg: cfg}

	// 3. Geschäftsjahr und Periode (Variante K4: Kalenderjahr), Periodensperre
	t, _ := time.Parse(time.DateOnly, postingDate)
	p.fiscalYear, p.period = t.Year(), int(t.Month())
	if req.PostingPeriod != 0 {
		if req.PostingPeriod < 13 || req.PostingPeriod > 16 || t.Month() != time.December {
			return nil, crud.Invalid("Sonderperiode %d: nur 13–16 und nur mit Buchungsdatum im Dezember", req.PostingPeriod)
		}
		p.period = req.PostingPeriod
	}
	// Belegart: erlaubte Positionsarten (je Position geprüft), Referenzpflicht.
	dt, err := m.documentType(ctx, req.DocumentType)
	if err != nil {
		return nil, err
	}
	if dt.ReferenceRequired && strings.TrimSpace(req.Reference) == "" {
		return nil, crud.Invalid("Belegart %s (%s): Referenz, z. B. Rechnungsnummer, ist Pflicht", dt.Code, dt.Name)
	}
	// Berechtigung bis auf Feldwerte: Periode und Belegart.
	if err := m.authorizePosting(ctx, req.CompanyCode, cfg.Ledger, p.fiscalYear, p.period, dt.Code); err != nil {
		return nil, err
	}
	// Die Periodensperre prüft jede Position (Kontensperren je Periode).

	// 4. Modul-Mapping
	mapping, err := cfg.mappingFor(req.SourceModule)
	if err != nil {
		return nil, err
	}

	// 6a. Währungen und Kurs
	if p.docDecimals, err = m.cur.decimals(ctx, req.Currency); err != nil {
		return nil, err
	}
	if p.locDecimals, err = m.cur.decimals(ctx, cfg.Currency); err != nil {
		return nil, err
	}
	factor, err := m.factor(ctx, cfg.RateType, req.Currency, cfg.Currency, postingDate)
	if err != nil {
		return nil, err
	}
	p.exchangeRate = strings.TrimRight(strings.TrimRight(factor.FloatString(10), "0"), ".")

	// 5. Positionen
	var docSum, locSum int64
	for i, it := range req.Items {
		pi, err := s.item(ctx, p, mapping, i+1, it)
		if err != nil {
			return nil, err
		}
		pi.locMinor = convertMinor(pi.docMinor, p.docDecimals, p.locDecimals, factor)
		docSum += pi.docMinor
		locSum += pi.locMinor
		p.items = append(p.items, pi)
	}

	// 6b. Soll = Haben
	if docSum != 0 {
		var debit, credit int64
		for _, it := range p.items {
			if it.docMinor > 0 {
				debit += it.docMinor
			} else {
				credit -= it.docMinor
			}
		}
		return nil, crud.Invalid("Soll (%s) und Haben (%s) sind nicht ausgeglichen – Differenz %s %s",
			formatAmount(debit, p.docDecimals), formatAmount(credit, p.docDecimals), formatAmount(docSum, p.docDecimals), req.Currency)
	}
	// Rundungsdifferenz der Umrechnung auf die betragsgrößte Position.
	if locSum != 0 {
		big := 0
		for i, it := range p.items {
			if abs(it.locMinor) > abs(p.items[big].locMinor) {
				big = i
			}
		}
		p.items[big].locMinor -= locSum
	}
	return p, nil
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// item prüft eine Position und mappt ihre Kontierungen.
func (s *PostingService) item(ctx context.Context, p *plan, mapping map[string]string, line int, it ledgerapi.Item) (planItem, error) {
	m := s.m
	pi := planItem{line: line, account: strings.ToUpper(strings.TrimSpace(it.Account)), text: strings.TrimSpace(it.Text), dims: map[string]string{}}
	fail := func(format string, args ...any) (planItem, error) {
		return planItem{}, crud.Invalid("Position %d: "+format, append([]any{line}, args...)...)
	}
	if pi.account == "" {
		return fail("Konto (account_number) ist Pflicht")
	}
	switch strings.ToUpper(string(it.Side)) {
	case sideDebit:
		pi.side = sideDebit
	case sideCredit:
		pi.side = sideCredit
	default:
		return fail("Soll/Haben (shkzg) muss S oder H sein")
	}
	n, err := parseAmount(it.Amount, p.docDecimals)
	if err != nil {
		return fail("%v", err)
	}
	pi.docMinor = n
	if pi.side == sideCredit {
		pi.docMinor = -n
	}

	// Konto: Kontenplan (aktiv) und Buchungskreis (zugeordnet, nicht gesperrt).
	if _, err := m.masterAccount(ctx, p.cfg.Chart, pi.account); err != nil {
		return fail("%v", strings.TrimPrefix(err.Error(), sdk.ErrInvalidArgument.Error()+": "))
	}
	res, err := m.db.Query(ctx, `SELECT currency, reconciliation_type, is_blocked FROM ledger__account_company
		WHERE company_code_id = ? AND account_number = ?`, p.req.CompanyCode, pi.account)
	if err != nil {
		return planItem{}, err
	}
	if len(res.Rows) == 0 {
		return fail("Konto %s ist dem Buchungskreis %s nicht zugeordnet", pi.account, p.req.CompanyCode)
	}
	accCur, _, blocked := crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), crud.AsBool(res.Rows[0][2])
	if blocked {
		return fail("Konto %s ist im Buchungskreis %s gesperrt", pi.account, p.req.CompanyCode)
	}
	if accCur != p.cfg.Currency && accCur != p.req.Currency {
		return fail("Konto %s wird in %s geführt – nur Belege in %s", pi.account, accCur, accCur)
	}

	// Kontierungen: allgemeine direkt, modulspezifische über das Mapping.
	for col, v := range map[string]string{"cost_center": it.CostCenter, "profit_center": it.ProfitCenter, "segment": it.Segment} {
		if v = strings.TrimSpace(v); v != "" {
			pi.dims[col] = v
		}
	}
	for key, v := range it.Assignments {
		col, ok := mapping[key]
		if !ok {
			return fail("Kontierung %q ist für Modul %s nicht zugeordnet (Modul-Mapping des Buchungskreises)", key, p.req.SourceModule)
		}
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		if prev, dup := pi.dims[col]; dup && prev != v {
			return fail("Spalte %s doppelt belegt (%q und %q)", col, prev, v)
		}
		pi.dims[col] = v
	}

	// Regeln: Positionsart (Belegart), Feldstatusgruppe des Kontos, Partner,
	// Kontensperre in der Periode.
	rule, err := m.rule(ctx, p.req.CompanyCode, p.req.DocumentType, pi.account, it.ItemType)
	if err != nil {
		return fail("%v", trimInvalid(err))
	}
	values := map[string]string{"item_text": pi.text}
	for k, v := range pi.dims {
		values[k] = v
	}
	if err := rule.check(pi.account, values); err != nil {
		return fail("%v", trimInvalid(err))
	}
	if err := m.accountOpen(ctx, p.req.CompanyCode, p.cfg.Ledger, p.fiscalYear, p.period, pi.account); err != nil {
		return fail("%v", trimInvalid(err))
	}
	pi.itemType = rule.ItemType
	return pi, nil
}

// write vergibt die Belegnummer und schreibt Kopf und Einzelposten.
func (s *PostingService) write(ctx context.Context, p *plan, draftID string) (ledgerapi.PostResult, error) {
	m := s.m
	no, err := m.nextNumber(ctx, p.req.CompanyCode, p.fiscalYear)
	if err != nil {
		return ledgerapi.PostResult{}, err
	}
	id := crud.NewID()
	user := nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	_, err = m.db.Exec(ctx, `INSERT INTO ledger__journal_entry_header (id, document_number, company_code_id, fiscal_year, posting_period,
		document_type, document_date, posting_date, currency, local_currency, exchange_rate, reference, header_text, source_module,
		source_reference, reversal_flag, draft_id, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, no, p.req.CompanyCode, p.fiscalYear, p.period, p.req.DocumentType, p.req.DocumentDate, p.req.PostingDate,
		p.req.Currency, p.cfg.Currency, p.exchangeRate, nilIfEmpty(p.req.Reference), nilIfEmpty(p.req.HeaderText), p.req.SourceModule,
		nilIfEmpty(p.req.SourceReference), false, nilIfEmpty(draftID), user, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return ledgerapi.PostResult{}, err
	}
	for _, it := range p.items {
		if err := m.insertItem(ctx, id, p, it); err != nil {
			return ledgerapi.PostResult{}, err
		}
	}
	return ledgerapi.PostResult{ID: id, DocumentNumber: no, FiscalYear: p.fiscalYear, PostingPeriod: p.period}, nil
}

func (m *Module) insertItem(ctx context.Context, headerID string, p *plan, it planItem) error {
	cols := []string{"id", "header_id", "line_item_number", "ledger", "company_code_id", "fiscal_year", "posting_period", "posting_date",
		"chart_of_accounts_id", "account_number", "shkzg", "item_type", "amount_document_curr", "amount_local_curr", "currency", "local_currency", "item_text", "source_module"}
	args := []any{crud.NewID(), headerID, it.line, p.cfg.Ledger, p.req.CompanyCode, p.fiscalYear, p.period, p.req.PostingDate,
		p.cfg.Chart, it.account, it.side, orDefault(it.itemType, itemGL), it.docMinor, it.locMinor, p.req.Currency, p.cfg.Currency, nilIfEmpty(it.text), nilIfEmpty(p.req.SourceModule)}
	for _, c := range dimColumns {
		cols, args = append(cols, c), append(args, nilIfEmpty(it.dims[c]))
	}
	_, err := m.db.Exec(ctx, "INSERT INTO ledger__journal_entry_item ("+strings.Join(cols, ", ")+") VALUES ("+
		strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")+")", args...)
	return err
}

// nextNumber: Belegnummernkreis je Buchungskreis und Geschäftsjahr (analog NRIV),
// zehnstellig ab 1000000001.
func (m *Module) nextNumber(ctx context.Context, cc string, year int) (string, error) {
	res, err := m.db.Query(ctx, "SELECT last_number FROM ledger__number_range WHERE company_code_id = ? AND fiscal_year = ?", cc, year)
	if err != nil {
		return "", err
	}
	next := int64(1000000001)
	if len(res.Rows) == 0 {
		_, err = m.db.Exec(ctx, "INSERT INTO ledger__number_range (company_code_id, fiscal_year, last_number) VALUES (?, ?, ?)", cc, year, next)
	} else {
		next = toInt(res.Rows[0][0]) + 1
		_, err = m.db.Exec(ctx, "UPDATE ledger__number_range SET last_number = ? WHERE company_code_id = ? AND fiscal_year = ? AND last_number = ?",
			next, cc, year, next-1)
	}
	return strconv.FormatInt(next, 10), err
}

// Reverse storniert einen Beleg: neuer Beleg mit getauschten Seiten und
// negierten Beträgen (Hauswährung unverändert, kein neuer Kurs). Beide Belege
// verweisen aufeinander (rekursive Fremdschlüssel im Belegkopf).
func (s *PostingService) Reverse(ctx context.Context, req ledgerapi.ReverseRequest) (ledgerapi.PostResult, error) {
	m := s.m
	var out ledgerapi.PostResult
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, `SELECT document_number, company_code_id, posting_date, currency, local_currency, exchange_rate, reference,
			source_module, reversal_flag, reversal_document_id, document_type FROM ledger__journal_entry_header WHERE id = ?`, req.ID)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return fmt.Errorf("%w: Beleg %s", sdk.ErrNotFound, req.ID)
		}
		r := res.Rows[0]
		docNo, cc := crud.Str(r[0]), crud.Str(r[1])
		orig, _ := crud.ParseDate(r[2])
		switch {
		case crud.AsBool(r[8]):
			return crud.Invalid("Beleg %s ist selbst ein Storno – bitte neu buchen", docNo)
		case r[9] != nil:
			return crud.Invalid("Beleg %s ist bereits storniert", docNo)
		}
		if err := requireCompanyCode(ctx, entryObject, "reverse", cc); err != nil {
			return err
		}
		date := strings.TrimSpace(req.PostingDate)
		if date == "" {
			date = crud.Today()
		}
		if date, err = crud.ParseDate(date); err != nil {
			return crud.Invalid("Buchungsdatum: %v", err)
		}
		if date < orig {
			return crud.Invalid("Storno am %s liegt vor dem Buchungsdatum des Belegs (%s)", date, orig)
		}
		cfg, err := m.config(ctx, cc)
		if err != nil {
			return err
		}
		t, _ := time.Parse(time.DateOnly, date)
		p := &plan{cfg: cfg, fiscalYear: t.Year(), period: int(t.Month()), exchangeRate: crud.Str(r[5]),
			req: ledgerapi.PostRequest{CompanyCode: cc, PostingDate: date, DocumentDate: date, Currency: crud.Str(r[3]),
				Reference: crud.Str(r[6]), SourceModule: crud.Str(r[7]), DocumentType: crud.Str(r[10]),
				HeaderText: strings.TrimSpace(req.HeaderText)}}
		if p.req.HeaderText == "" {
			p.req.HeaderText = "Storno zu " + docNo
		}
		if err := m.authorizePosting(ctx, cc, cfg.Ledger, p.fiscalYear, p.period, p.req.DocumentType); err != nil {
			return err
		}
		cols := "line_item_number, account_number, shkzg, amount_document_curr, amount_local_curr, item_text, item_type, " + strings.Join(dimColumns, ", ")
		items, err := m.db.Query(ctx, "SELECT "+cols+" FROM ledger__journal_entry_item WHERE header_id = ? AND ledger = ? ORDER BY line_item_number", req.ID, cfg.Ledger)
		if err != nil {
			return err
		}
		for _, it := range items.Rows {
			pi := planItem{line: int(toInt(it[0])), account: crud.Str(it[1]), docMinor: -toInt(it[3]), locMinor: -toInt(it[4]),
				text: crud.Str(it[5]), itemType: crud.Str(it[6]), dims: map[string]string{}, side: sideDebit}
			if err := m.accountOpen(ctx, cc, cfg.Ledger, p.fiscalYear, p.period, pi.account); err != nil {
				return err
			}
			if crud.Str(it[2]) == sideDebit {
				pi.side = sideCredit
			}
			for i, c := range dimColumns {
				pi.dims[c] = crud.Str(it[7+i])
			}
			p.items = append(p.items, pi)
		}
		if out, err = s.write(ctx, p, ""); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, "UPDATE ledger__journal_entry_header SET reversal_flag = ?, reversed_document_id = ? WHERE id = ?", true, req.ID, out.ID); err != nil {
			return err
		}
		upd, err := m.db.Exec(ctx, "UPDATE ledger__journal_entry_header SET reversal_document_id = ? WHERE id = ? AND reversal_document_id IS NULL", out.ID, req.ID)
		if err != nil {
			return err
		}
		if upd.RowsAffected != 1 {
			return crud.Invalid("Beleg %s wurde gleichzeitig storniert", docNo)
		}
		return nil
	})
	return out, err
}

// --- Actions -----------------------------------------------------------------------

// decodePost liest einen PostRequest aus {data: {...}} oder direkt.
func decodePost(payload any) (ledgerapi.PostRequest, error) {
	var req ledgerapi.PostRequest
	src := payload
	if m, ok := payload.(map[string]any); ok {
		if d, ok := m["data"]; ok {
			src = d
		}
	}
	if err := sdk.Decode(src, &req); err != nil {
		return req, crud.Invalid("Buchungsauftrag: %v", err)
	}
	return req, nil
}

func (m *Module) postAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	in, err := decodePost(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	res, err := m.posting.Post(ctx, in)
	if err != nil {
		return sdk.Response{}, err
	}
	m.emitEntry(ctx, "post", res)
	return sdk.Response{Payload: result(res)}, nil
}

func result(res ledgerapi.PostResult) map[string]any {
	msg := fmt.Sprintf("Beleg %s gebucht (%d/%d)", res.DocumentNumber, res.PostingPeriod, res.FiscalYear)
	if res.Duplicate {
		msg = fmt.Sprintf("Beleg %s war bereits gebucht (gleiche Referenz)", res.DocumentNumber)
	}
	return map[string]any{"id": res.ID, "document_number": res.DocumentNumber, "fiscal_year": res.FiscalYear,
		"posting_period": res.PostingPeriod, "duplicate": res.Duplicate, "message": msg}
}

func (m *Module) simulateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	in, err := decodePost(req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	p, err := m.posting.Simulate(ctx, in)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: p.summary()}, nil
}

// summary: Ergebnis einer Simulation (Positionen mit gemappten Spalten und Hauswährung).
func (p *plan) summary() map[string]any {
	items := []map[string]any{}
	for _, it := range p.items {
		row := map[string]any{"line_item_number": it.line, "account_number": it.account, "shkzg": it.side,
			"amount_document_curr": formatAmount(it.docMinor, p.docDecimals), "amount_local_curr": formatAmount(it.locMinor, p.locDecimals)}
		for c, v := range it.dims {
			row[c] = v
		}
		items = append(items, row)
	}
	return map[string]any{"message": fmt.Sprintf("Prüfung erfolgreich: Periode %d/%d offen, Soll = Haben, %d Positionen", p.period, p.fiscalYear, len(p.items)),
		"fiscal_year": p.fiscalYear, "posting_period": p.period, "ledger": p.cfg.Ledger, "currency": p.req.Currency,
		"local_currency": p.cfg.Currency, "exchange_rate": p.exchangeRate, "items": items}
}

func (m *Module) reverseAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	d := dataOf(req.Payload)
	in := ledgerapi.ReverseRequest{ID: crud.Str(p["id"]), PostingDate: crud.Str(d["posting_date"]), HeaderText: crud.Str(d["header_text"])}
	if in.ID == "" {
		return sdk.Response{}, crud.Invalid("id des Belegs fehlt")
	}
	res, err := m.posting.Reverse(ctx, in)
	if err != nil {
		return sdk.Response{}, err
	}
	m.emitEntry(ctx, "reverse", res)
	out := result(res)
	out["message"] = fmt.Sprintf("Stornobeleg %s gebucht", res.DocumentNumber)
	return sdk.Response{Payload: out}, nil
}

func dataOf(payload any) map[string]any {
	m, _ := payload.(map[string]any)
	if d, ok := m["data"].(map[string]any); ok {
		return d
	}
	if m == nil {
		return map[string]any{}
	}
	return m
}

// --- Vorerfassung: prüfen, buchen, Position entfernen ------------------------------

// draftRequest baut aus einer Vorerfassung den Buchungsauftrag (Modul MANUAL,
// Kontierungen direkt als Spalten).
func (m *Module) draftRequest(ctx context.Context, d *draftHead) (ledgerapi.PostRequest, error) {
	req := ledgerapi.PostRequest{SourceModule: moduleManual, SourceReference: "DRAFT-" + d.ID, CompanyCode: d.CompanyCode,
		DocumentType: d.DocumentType, PostingDate: d.PostingDate, DocumentDate: d.DocumentDate, Currency: d.Currency,
		HeaderText: d.HeaderText, Reference: d.Reference, PostingPeriod: d.SpecialPeriod}
	cols := "account_number, shkzg, amount, item_text, item_type, " + strings.Join(dimColumns, ", ")
	res, err := m.db.Query(ctx, "SELECT "+cols+" FROM ledger__draft_item WHERE draft_id = ? ORDER BY line_item_number", d.ID)
	if err != nil {
		return req, err
	}
	for _, r := range res.Rows {
		it := ledgerapi.Item{Account: crud.Str(r[0]), Side: ledgerapi.Side(crud.Str(r[1])), Amount: crud.Str(r[2]), Text: crud.Str(r[3]),
			ItemType: crud.Str(r[4]), Assignments: map[string]string{}}
		for i, c := range dimColumns {
			if v := crud.Str(r[5+i]); v != "" {
				it.Assignments[c] = v
			}
		}
		req.Items = append(req.Items, it)
	}
	return req, nil
}

func (m *Module) draftSimulateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	d, err := m.draftHeader(ctx, crud.Str(p["id"]))
	if err != nil {
		return sdk.Response{}, err
	}
	if err := d.editable(ctx); err != nil {
		return sdk.Response{}, err
	}
	pr, err := m.draftRequest(ctx, d)
	if err != nil {
		return sdk.Response{}, err
	}
	plan, err := m.posting.Simulate(ctx, pr)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: plan.summary()}, nil
}

// draftPostAction bucht die Vorerfassung: Beleg schreiben (draft_id), Vorerfassung
// auf POSTED setzen und mit dem Beleg verknüpfen (posted_document_id) – in einer
// Transaktion. Danach ist die Vorerfassung gesperrt.
func (m *Module) draftPostAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	var res ledgerapi.PostResult
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		d, err := m.draftHeader(ctx, crud.Str(p["id"]))
		if err != nil {
			return err
		}
		if err := d.editable(ctx); err != nil {
			return err
		}
		pr, err := m.draftRequest(ctx, d)
		if err != nil {
			return err
		}
		if res, err = m.posting.post(ctx, pr, d.ID); err != nil {
			return err
		}
		upd, err := m.db.Exec(ctx, `UPDATE ledger__draft_header SET status = ?, posted_document_id = ?, changed_at = ?, changed_by = ?
			WHERE id = ? AND status = ?`, draftPosted, res.ID, time.Now().UTC().Format(time.RFC3339),
			nilIfEmpty(sdk.CallFromContext(ctx).UserID), d.ID, draftOpen)
		if err != nil {
			return err
		}
		if upd.RowsAffected != 1 {
			return crud.Invalid("Vorerfassung wurde gleichzeitig geändert")
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.emitEntry(ctx, "post", res)
	if d, err := m.draftHeader(ctx, crud.Str(p["id"])); err == nil {
		m.emit(ctx, events.Event{Object: "JournalDraft", Action: "post", CompanyCode: d.CompanyCode, EntityID: d.ID, Source: Name,
			Data: map[string]any{"posted_document_id": res.ID, "document_number": res.DocumentNumber}})
	}
	return sdk.Response{Payload: result(res)}, nil
}

// draftItemRemoveAction entfernt eine Position einer offenen Vorerfassung.
// Die Vorerfassung ist Arbeitsbereich: Hier wird – anders als bei Belegen –
// physisch gelöscht.
func (m *Module) draftItemRemoveAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	p, _ := req.Payload.(map[string]any)
	id := crud.Str(p["id"])
	err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
		res, err := m.db.Query(ctx, "SELECT draft_id FROM ledger__draft_item WHERE id = ?", id)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return fmt.Errorf("%w: Position %s", sdk.ErrNotFound, id)
		}
		d, err := m.draftHeader(ctx, crud.Str(res.Rows[0][0]))
		if err != nil {
			return err
		}
		if err := d.editable(ctx); err != nil {
			return err
		}
		_, err = m.db.Exec(ctx, "DELETE FROM ledger__draft_item WHERE id = ?", id)
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	m.emit(ctx, events.Event{Object: "JournalDraftItem", Action: "remove", EntityID: id, Source: Name})
	return sdk.Response{Payload: map[string]any{"message": "Position entfernt"}}, nil
}

// --- SystemEvents ------------------------------------------------------------------

// emitEntry meldet einen gebuchten Beleg (Bewegungsdaten) an den Event-Dispatcher –
// nach dem Commit. Doppelte Aufrufe mit gleicher Referenz (Duplicate) melden nichts.
func (m *Module) emitEntry(ctx context.Context, action string, res ledgerapi.PostResult) {
	if res.Duplicate {
		return
	}
	r, err := m.db.Query(ctx, `SELECT company_code_id, source_module, source_reference, reversed_document_id, draft_id
		FROM ledger__journal_entry_header WHERE id = ?`, res.ID)
	if err != nil || len(r.Rows) == 0 {
		return
	}
	row := r.Rows[0]
	m.emit(ctx, events.Event{Object: entryObject, Action: action, CompanyCode: crud.Str(row[0]), EntityID: res.ID, Source: Name,
		Data: map[string]any{"document_number": res.DocumentNumber, "fiscal_year": res.FiscalYear, "posting_period": res.PostingPeriod,
			"source_module": crud.Str(row[1]), "source_reference": crud.Str(row[2]), "reversed_document_id": crud.Str(row[3]),
			"draft_id": crud.Str(row[4])}})
}

func (m *Module) emit(ctx context.Context, ev events.Event) {
	if err := events.Push(ctx, m.services, ev); err != nil {
		m.log.WarnContext(ctx, "SystemEvent nicht gemeldet", "object", ev.Object, "action", ev.Action, "err", err)
	}
}
