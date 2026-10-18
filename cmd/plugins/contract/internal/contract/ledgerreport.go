package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Auswertungen aus dem Hauptbuch am Vertrag (seit 0.32.0) – gerechnet im
// GuV-Modul (profit-loss, Object LedgerReport), nichts wird gespeichert:
//
//   - „Vertragskonto …“: Partnerposten (Debitoren/Kreditoren) der Belege zum
//     Vertrag mit laufendem Saldo – Sollstellungen, Rechnungen, Zahlungen.
//   - „Darlehensstand …“: Restschuld laut Buchhaltung (Darlehenskonto der
//     Kondition am Stichtag) gegen den Tilgungsplan (contract-billing), mit Differenz.

const ledgerReportObject = "LedgerReport"

func (m *Module) ledgerCall(ctx context.Context, action string, payload map[string]any) (sdk.Response, error) {
	resp, err := m.services.Call(ctx, ledgerReportObject, action, payload)
	if errors.Is(err, sdk.ErrUnimplemented) {
		return resp, fmt.Errorf("%w: Auswertung nicht verfügbar – Plugin profit-loss (GuV) ist nicht gestartet", sdk.ErrUnavailable)
	}
	return resp, err
}

// optDate: leeres Datum bleibt leer, sonst JJJJ-MM-TT.
func optDate(s, label string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", nil
	}
	d, err := crud.ParseDate(s)
	if err != nil {
		return "", crud.Invalid("%s: %v", label, err)
	}
	return d, nil
}

// contractAccountAction: „Vertragskonto …“ – Tabelle mit laufendem Saldo, CSV zum Speichern.
func (m *Module) contractAccountAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			From string `json:"statement_from"`
			To   string `json:"statement_to"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	call := map[string]any{"company_code": key["company_code"], "contract_id": key["contract_id"]}
	for k, v := range map[string][2]string{"period_from": {in.Data.From, "Von"}, "period_to": {in.Data.To, "Bis"}} {
		d, err := optDate(v[0], v[1])
		if err != nil {
			return sdk.Response{}, err
		}
		if d != "" {
			call[k] = d
		}
	}
	return m.ledgerCall(ctx, "contractAccount", call)
}

// loanTerm: Darlehenskondition eines Vertrags am Stichtag.
type loanTerm struct {
	Account   string
	Principal int64
}

func (m *Module) loanTermAt(ctx context.Context, cc, id, date string) (*loanTerm, bool, error) {
	res, err := m.db.Query(ctx, `SELECT loan_account, principal FROM contract__loan
		WHERE company_code = ? AND contract_id = ? AND valid_from <= ? ORDER BY valid_from DESC`, cc, id, date)
	if err != nil {
		return nil, false, err
	}
	if len(res.Rows) == 0 {
		return nil, false, crud.Invalid("Vertrag %s hat am %s keine Darlehenskondition", id, germanDate(date))
	}
	t := &loanTerm{Account: crud.Str(res.Rows[0][0]), Principal: toInt(res.Rows[0][1])}
	// Teilen sich Darlehen ein Konto, zählen nur die Posten mit diesem Vertrag.
	shared, err := m.db.Query(ctx, `SELECT COUNT(DISTINCT contract_id) FROM contract__loan WHERE company_code = ? AND loan_account = ?`, cc, t.Account)
	if err != nil {
		return nil, false, err
	}
	return t, len(shared.Rows) > 0 && toInt(shared.Rows[0][0]) > 1, nil
}

// planBalance: Restschuld laut Tilgungsplan nach der letzten Fälligkeit bis zum Stichtag
// (noch keine Fälligkeit: Darlehensbetrag).
func planBalance(rows [][]string, date string, principal int64, decimals int) (int64, string) {
	bal, due := principal, ""
	for _, r := range rows {
		if len(r) < 7 || isoOfGerman(r[0]) > date {
			continue
		}
		if v, err := parseAmount(r[6], decimals); err == nil {
			bal, due = v, r[0]
		}
	}
	return bal, due
}

// isoOfGerman: "08.10.2026" → "2026-10-08" (anderes bleibt).
func isoOfGerman(s string) string {
	if len(s) != 10 || s[2] != '.' || s[5] != '.' {
		return s
	}
	return s[6:10] + "-" + s[3:5] + "-" + s[0:2]
}

// loanStatusAction: „Darlehensstand …“ – gebuchte Restschuld gegen Tilgungsplan am Stichtag.
func (m *Module) loanStatusAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Date string `json:"status_date"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("Contract").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["contract_id"])
	date, err := optDate(in.Data.Date, "Stichtag")
	if err != nil {
		return sdk.Response{}, err
	}
	if date == "" {
		date = crud.KeyDate(ctx)[:10]
	}
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return sdk.Response{}, err
	}
	term, shared, err := m.loanTermAt(ctx, cc, id, date)
	if err != nil {
		return sdk.Response{}, err
	}
	decimals := m.currencyDecimals(ctx, c.Currency)
	call := map[string]any{"company_code": cc, "account_number": term.Account, "date": date}
	if shared {
		call["contract_id"] = id
	}
	resp, err := m.ledgerCall(ctx, "accountBalance", call)
	if err != nil {
		return sdk.Response{}, err
	}
	var booked struct {
		Balance int64 `json:"balance"`
		Items   int   `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &booked); err != nil {
		return sdk.Response{}, err
	}
	resp, err = m.billingCall(ctx, "loanSchedule", map[string]any{"company_code": cc, "contract_id": id, "until": date})
	if err != nil {
		return sdk.Response{}, err
	}
	var plan struct {
		Table struct {
			Rows [][]string `json:"rows"`
		} `json:"table"`
	}
	if err := sdk.Decode(resp.Payload, &plan); err != nil {
		return sdk.Response{}, err
	}
	planned, due := planBalance(plan.Table.Rows, date, term.Principal, decimals)
	// Darlehen erhalten: Haben-Saldo = Restschuld; gewährt (Forderung): Soll-Saldo.
	actual := -booked.Balance
	if booked.Balance > 0 {
		actual = booked.Balance
	}
	diff := actual - planned
	amt := func(v int64) string { return germanAmount(formatAmount(v, decimals)) + " " + c.Currency }
	scope := "Konto " + term.Account
	if shared {
		scope += ", nur Posten mit Vertrag " + id
	}
	status := "stimmt mit dem Tilgungsplan überein"
	if diff != 0 {
		status = "Differenz " + amt(diff) + " – Raten oder Sondertilgungen prüfen (Kontoblatt " + term.Account + ")"
	}
	rows := [][]string{
		{"Restschuld laut Buchhaltung", scope + fmt.Sprintf(" (%d Posten)", booked.Items), amt(actual)},
		{"Restschuld laut Tilgungsplan", "letzte Fälligkeit " + firstNonEmpty(due, "– (Darlehensbetrag)"), amt(planned)},
		{"Differenz (Buchhaltung − Plan)", "", amt(diff)},
	}
	return sdk.Response{Payload: map[string]any{
		"message": fmt.Sprintf("Darlehensstand %s am %s: Restschuld %s, %s", id, germanDate(date), amt(actual), status),
		"table":   map[string]any{"columns": []string{"Position", "Grundlage", "Betrag"}, "rows": rows},
	}}, nil
}

// germanAmount: "-1234.50" → "-1.234,50".
func germanAmount(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(r)
	}
	if hasFrac {
		return sign + b.String() + "," + frac
	}
	return sign + b.String()
}
