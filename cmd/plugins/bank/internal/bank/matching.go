package bank

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Maschinelle Zuordnung offener Umsätze (und maschineller Vorschläge), in
// dieser Reihenfolge:
//
//  1. bestätigte Regel                                   → zugeordnet
//  2. Vertragsnummer (intern/extern) bzw. BP-Nummer im
//     Verwendungszweck oder Buchungstext                 → zugeordnet
//  3. Ausgang: Rechnungsnummer des Lieferanten im Text   → zugeordnet;
//     gleicher Betrag wie eine offene Rechnung des
//     Partners mit passendem Namen                        → Vorschlag
//  4. IBAN der Gegenseite bei genau einem Partner        → zugeordnet
//  5. Name der Gegenseite bei genau einem Partner        → Vorschlag
//
// Bei einem Partner wählt die Zuordnung den Vertrag, wenn genau einer passt
// (Partner oder Mitpartner, Richtung, Laufzeit am Buchungstag), sonst den
// Partner in seiner einzigen Finanzrolle.

type txnRow struct {
	Account                                        string
	No, Amount                                     int64
	Date, Counterparty, IBAN, Purpose, BookingText string
}

type contractRef struct {
	ID, External, Partner, Type, Status, From, To string
}

type partnerRef struct {
	ID, Name1, Name2 string
}

type invoiceRef struct {
	ID, Supplier, Reference string
	Total                   int64
}

// refData: Stammdaten einer Zuordnung (einmal je Lauf geladen).
type refData struct {
	contracts   []contractRef
	directions  map[string]string   // Vertragsart → RECEIVABLE | PAYABLE
	coPartners  map[string][]string // Partner → Verträge (Mitpartner)
	partners    []partnerRef
	byIBAN      map[string][]string // IBAN → Partner
	invoices    []invoiceRef
	finance     map[string][]string // Partner → Finanzrollen im Buchungskreis
	debitor     map[string]bool     // Rolle → Debitor (sonst Kreditor)
	invoiceUsed map[string]bool
}

type matchResult struct{ Matched, Proposed, Open int }

func (r matchResult) text() string {
	return fmt.Sprintf("Zuordnung: %d zugeordnet, %d Vorschläge, %d offen", r.Matched, r.Proposed, r.Open)
}

func (m *Module) matchAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	a, _, err := m.accountOfRequest(ctx, req.Payload)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, accountObject, "match", a.CC); err != nil {
		return sdk.Response{}, err
	}
	r, err := m.matchAccount(ctx, a)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"matched": r.Matched, "proposed": r.Proposed, "open": r.Open, "message": r.text()}}, nil
}

// matchAccount ordnet offene Umsätze und maschinelle Vorschläge (neu) zu.
func (m *Module) matchAccount(ctx context.Context, a *accountRow) (matchResult, error) {
	var out matchResult
	res, err := m.db.Query(ctx, `SELECT txn_no, amount, booking_date, counterparty_name, counterparty_iban, purpose, booking_text FROM bank__transaction
		WHERE company_code = ? AND account_id = ? AND (status = ? OR (status = ? AND match_source <> 'MANUAL')) ORDER BY booking_date, txn_no`,
		a.CC, a.ID, stOpen, stProposed)
	if err != nil {
		return out, err
	}
	if len(res.Rows) == 0 {
		return out, nil
	}
	rules, err := m.confirmedRules(ctx, a.CC)
	if err != nil {
		return out, err
	}
	ref, err := m.loadRefData(ctx, a.CC)
	if err != nil {
		return out, err
	}
	hits := map[int64]int{}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, r := range res.Rows {
			t := &txnRow{Account: a.ID, No: toInt(r[0]), Amount: toInt(r[1]), Counterparty: crud.Str(r[3]), IBAN: crud.Str(r[4]),
				Purpose: crud.Str(r[5]), BookingText: crud.Str(r[6])}
			t.Date, _ = crud.ParseDate(r[2])
			tg, status, source, note, rule := m.matchOne(t, rules, ref)
			k := txnKey{CC: a.CC, Account: a.ID, No: t.No}
			switch status {
			case stMatched:
				out.Matched++
			case stProposed:
				out.Proposed++
			default:
				out.Open++
			}
			if rule > 0 {
				hits[rule]++
			}
			var ruleNo any
			if rule > 0 {
				ruleNo = rule
			}
			if tg.Invoice != "" {
				ref.invoiceUsed[tg.Invoice] = true
			}
			if err := m.setTarget(ctx, k, tg, status, source, note, ruleNo); err != nil {
				return err
			}
		}
		for no, n := range hits {
			if _, err := m.db.Exec(ctx, `UPDATE bank__rule SET hits = hits + ? WHERE company_code = ? AND rule_no = ?`, n, a.CC, no); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

// matchOne: Ziel, Status, Quelle, Begründung, Regel.
func (m *Module) matchOne(t *txnRow, rules []ruleDef, ref *refData) (target, string, string, string, int64) {
	// 1. Regel – die genaueste
	var best *ruleDef
	for i := range rules {
		if rules[i].matches(t) && (best == nil || rules[i].conditions() > best.conditions()) {
			best = &rules[i]
		}
	}
	if best != nil {
		return best.Target, stMatched, "RULE", fmt.Sprintf("Regel %d", best.No), best.No
	}
	text := strings.ToUpper(t.Purpose + " " + t.BookingText)
	// 2. Vertrags- bzw. Partnernummer im Text
	var cs []contractRef
	for _, c := range ref.contracts {
		if containsToken(text, c.ID) || (c.External != "" && containsToken(text, c.External)) {
			cs = append(cs, c)
		}
	}
	if len(cs) == 1 {
		return target{Type: tgContract, Contract: cs[0].ID, Partner: cs[0].Partner}, stMatched, "REFERENCE",
			"Vertrag " + cs[0].ID + " im Verwendungszweck", 0
	}
	var ps []string
	for _, p := range ref.partners {
		if len(p.ID) >= 6 && containsToken(text, strings.ToUpper(p.ID)) {
			ps = append(ps, p.ID)
		}
	}
	if len(ps) == 1 {
		if tg, ok := ref.forPartner(ps[0], t); ok {
			return tg, stMatched, "REFERENCE", "Partner " + ps[0] + " im Verwendungszweck", 0
		}
	}
	// 3. Eingangsrechnung (Ausgang)
	if t.Amount < 0 {
		var byRef, byAmount []invoiceRef
		for _, inv := range ref.invoices {
			if ref.invoiceUsed[inv.ID] {
				continue
			}
			if len(inv.Reference) >= 4 && containsToken(text, strings.ToUpper(inv.Reference)) {
				byRef = append(byRef, inv)
			} else if inv.Total == -t.Amount && ref.nameMatches(inv.Supplier, t.Counterparty) {
				byAmount = append(byAmount, inv)
			}
		}
		if len(byRef) == 1 {
			return target{Type: tgInvoice, Invoice: byRef[0].ID, Partner: byRef[0].Supplier}, stMatched, "INVOICE",
				"Rechnungsnummer " + byRef[0].Reference + " im Verwendungszweck", 0
		}
		if len(byAmount) == 1 {
			return target{Type: tgInvoice, Invoice: byAmount[0].ID, Partner: byAmount[0].Supplier}, stProposed, "INVOICE",
				"offene Rechnung " + byAmount[0].ID + " mit gleichem Betrag", 0
		}
	}
	// 4. IBAN – sicher nur, wenn die Richtung zur Rolle passt (Eingang ↔ Debitor, Ausgang ↔ Kreditor)
	if ids := ref.byIBAN[t.IBAN]; t.IBAN != "" && len(ids) == 1 {
		if tg, ok := ref.forPartner(ids[0], t); ok {
			if ref.directionFits(tg, t) {
				return tg, stMatched, "IBAN", "IBAN der Gegenseite bei Partner " + ids[0], 0
			}
			return tg, stProposed, "IBAN", "IBAN bei Partner " + ids[0] + " – Richtung passt nicht zur Rolle (z. B. Auszahlung an Mieter): prüfen", 0
		}
		return target{Type: tgPartner, Partner: ids[0]}, stProposed, "IBAN", "IBAN bei Partner " + ids[0] + " – Vertrag bzw. Rolle wählen", 0
	}
	// 5. Name
	if t.Counterparty != "" {
		if ids := ref.byName(t.Counterparty); len(ids) == 1 {
			if tg, ok := ref.forPartner(ids[0], t); ok {
				return tg, stProposed, "NAME", "Name der Gegenseite passt zu Partner " + ids[0], 0
			}
			return target{Type: tgPartner, Partner: ids[0]}, stProposed, "NAME", "Name passt zu Partner " + ids[0] + " – Vertrag bzw. Rolle wählen", 0
		} else if len(ids) > 1 {
			return target{}, stOpen, "", "mehrere Partner passen zum Namen: " + strings.Join(first(ids, 4), ", "), 0
		}
	}
	return target{}, stOpen, "", "", 0
}

// forPartner: Vertrag (genau einer passt) bzw. Partner in seiner einzigen Finanzrolle.
func (r *refData) forPartner(partner string, t *txnRow) (target, bool) {
	want := "RECEIVABLE"
	if t.Amount < 0 {
		want = "PAYABLE"
	}
	ids := map[string]bool{}
	for _, c := range r.contracts {
		if c.Partner == partner {
			ids[c.ID] = true
		}
	}
	for _, id := range r.coPartners[partner] {
		ids[id] = true
	}
	var hit []contractRef
	for _, c := range r.contracts {
		if ids[c.ID] && r.directions[c.Type] == want && (c.Status == "ACTIVE" || c.Status == "TERMINATED") &&
			c.From <= t.Date && (c.To == "" || t.Date <= c.To) {
			hit = append(hit, c)
		}
	}
	if len(hit) == 1 {
		return target{Type: tgContract, Contract: hit[0].ID, Partner: hit[0].Partner}, true
	}
	if roles := r.finance[partner]; len(roles) == 1 {
		return target{Type: tgPartner, Partner: partner, Role: roles[0]}, true
	}
	return target{}, false
}

// directionFits: Eingang von einem Debitor bzw. Ausgang an einen Kreditor
// (Vertrag: Richtung der Vertragsart).
func (r *refData) directionFits(tg target, t *txnRow) bool {
	if tg.Contract != "" {
		return true // forPartner wählt nur Verträge passender Richtung
	}
	deb, ok := r.debitor[tg.Role]
	return ok && deb == (t.Amount > 0)
}

// byName: Partner, deren Name zur Gegenseite passt – Nachname/Firma und
// Vorname; nur, wenn keiner beides trifft, genügt der Nachname.
func (r *refData) byName(counterparty string) []string {
	tokens := map[string]bool{}
	for _, w := range strings.Fields(normName(counterparty)) {
		tokens[w] = true
	}
	var full, last []string
	for _, p := range r.partners {
		n1 := strings.Fields(normName(p.Name1))
		if len(n1) == 0 || !allIn(n1, tokens) {
			continue
		}
		n2 := strings.Fields(normName(p.Name2))
		if len(n2) > 0 && tokens[n2[0]] {
			full = append(full, p.ID)
		} else {
			last = append(last, p.ID)
		}
	}
	if len(full) > 0 {
		return full
	}
	return last
}

func (r *refData) nameMatches(partner, counterparty string) bool {
	for _, id := range r.byName(counterparty) {
		if id == partner {
			return true
		}
	}
	return false
}

func allIn(words []string, set map[string]bool) bool {
	for _, w := range words {
		if !set[w] {
			return false
		}
	}
	return true
}

// containsToken: s kommt in text als ganzes Wort vor (Grenzen: kein Buchstabe, keine Ziffer).
func containsToken(text, s string) bool {
	if s == "" {
		return false
	}
	for i := 0; ; {
		j := strings.Index(text[i:], s)
		if j < 0 {
			return false
		}
		j += i
		before, after := j == 0 || !isAlnum(text[j-1]), j+len(s) == len(text) || !isAlnum(text[j+len(s)])
		if before && after {
			return true
		}
		i = j + 1
	}
}

func isAlnum(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

// --- Stammdaten laden --------------------------------------------------------------

func (m *Module) list(ctx context.Context, object string, query map[string]any) ([]map[string]any, error) {
	resp, err := m.services.Call(ctx, object, "list", map[string]any{"query": query, "limit": 100000})
	if err != nil {
		return nil, unavailable(object, err)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

func (m *Module) loadRefData(ctx context.Context, cc string) (*refData, error) {
	r := &refData{directions: map[string]string{}, coPartners: map[string][]string{}, byIBAN: map[string][]string{},
		finance: map[string][]string{}, debitor: map[string]bool{}, invoiceUsed: map[string]bool{}}
	s := func(rec map[string]any, k string) string { return strings.TrimSpace(crud.Str(rec[k])) }
	date := func(rec map[string]any, k string) string {
		d, _ := crud.ParseDate(rec[k])
		if d == "9999-12-31" {
			return ""
		}
		return d
	}
	optional := func(items []map[string]any, err error) ([]map[string]any, error) {
		if err != nil && strings.Contains(err.Error(), "nicht verfügbar") {
			return nil, nil // Modul nicht gestartet: Schritt entfällt
		}
		return items, err
	}
	cs, err := optional(m.list(ctx, "Contract", map[string]any{"company_code": cc}))
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		r.contracts = append(r.contracts, contractRef{ID: s(c, "contract_id"), External: strings.ToUpper(s(c, "external_number")),
			Partner: s(c, "partner_id"), Type: s(c, "contract_type"), Status: s(c, "status"), From: date(c, "valid_from"), To: date(c, "valid_to")})
	}
	types, err := optional(m.list(ctx, "ContractType", map[string]any{"company_code": cc}))
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		r.directions[s(t, "code")] = s(t, "direction")
	}
	cps, err := optional(m.list(ctx, "ContractPartner", map[string]any{"company_code": cc}))
	if err != nil {
		return nil, err
	}
	for _, p := range cps {
		r.coPartners[s(p, "partner_id")] = append(r.coPartners[s(p, "partner_id")], s(p, "contract_id"))
	}
	ps, err := m.list(ctx, "BusinessPartner", map[string]any{})
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		r.partners = append(r.partners, partnerRef{ID: s(p, "id"), Name1: s(p, "name1"), Name2: s(p, "name2")})
	}
	bank, err := m.list(ctx, "PartnerBankDetail", map[string]any{})
	if err != nil {
		return nil, err
	}
	for _, b := range bank {
		iban := strings.ReplaceAll(strings.ToUpper(s(b, "iban")), " ", "")
		if iban != "" && !contains(r.byIBAN[iban], s(b, "bp_id")) {
			r.byIBAN[iban] = append(r.byIBAN[iban], s(b, "bp_id"))
		}
	}
	fin, err := m.list(ctx, "PartnerCompanyCode", map[string]any{"company_code": cc})
	if err != nil {
		return nil, err
	}
	for _, f := range fin {
		r.finance[s(f, "bp_id")] = append(r.finance[s(f, "bp_id")], s(f, "role_code"))
	}
	roles, err := m.list(ctx, "PartnerRoleType", map[string]any{})
	if err != nil {
		return nil, err
	}
	for _, rt := range roles {
		r.debitor[s(rt, "code")] = crud.AsBool(rt["is_debitor"])
	}
	invs, err := optional(m.list(ctx, "SupplierInvoice", map[string]any{"company_code": cc, "status": "POSTED"}))
	if err != nil {
		return nil, err
	}
	for _, inv := range invs {
		if s(inv, "status") != "POSTED" {
			continue
		}
		total, err := parseDecimal(s(inv, "total"), ".", 2)
		if err != nil {
			continue
		}
		r.invoices = append(r.invoices, invoiceRef{ID: s(inv, "invoice_id"), Supplier: s(inv, "supplier_id"),
			Reference: strings.TrimSpace(s(inv, "supplier_reference")), Total: total})
	}
	used, err := m.db.Query(ctx, `SELECT DISTINCT invoice_id FROM bank__transaction WHERE company_code = ? AND invoice_id IS NOT NULL
		AND status IN (?, ?)`, cc, stMatched, stPosted)
	if err != nil {
		return nil, err
	}
	for _, u := range used.Rows {
		r.invoiceUsed[crud.Str(u[0])] = true
	}
	sort.Slice(r.contracts, func(i, j int) bool { return r.contracts[i].ID < r.contracts[j].ID })
	return r, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
