package ledger

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Unterkonto erzeugen (Aktion „Unterkonto erzeugen …“ am Sachkonto, seit 0.18.0),
// z. B. je Bank ein eigenes Konto im SKR04:
//
//   - gleiche Länge (SAME_LENGTH): nächste freie Nummer, 1800 → 1801; das neue
//     Konto steht neben dem alten (gleiches übergeordnetes Konto);
//   - verlängert (EXTEND): zwei Stellen angehängt, 1800 → 180001, 180002 …; das neue
//     Konto hängt unter dem alten, das alte wird Kontengruppe (nur ohne Buchungen;
//     seine Zuordnungen in den Buchungskreisen werden gesperrt).
//
// Eine eingegebene Nummer (z. B. 1821) geht vor dem Vorschlag. Das neue Konto
// übernimmt Kontotyp, Kontoart, Kontengruppe und in jedem Buchungskreis des
// alten Kontos dessen Einstellungen (Währung, Abstimmkonto, Steuerkategorie,
// Feldstatusgruppe).

const (
	subSameLength = "SAME_LENGTH"
	subExtend     = "EXTEND"
)

var subModeOptions = []metamodel.Option{
	{Value: subSameLength, Label: "gleiche Länge – nächste freie Nummer (1800 → 1801)"},
	{Value: subExtend, Label: "verlängert – zwei Stellen anhängen (1800 → 180001), altes Konto wird Gruppe"}}

// subAccountFields: Formularfelder der Aktion (nur in der Aktion).
var subAccountFields = []crud.Field{
	{Key: "sub_mode", Label: "Nummer", Type: tSel, ActionOnly: true, Options: subModeOptions},
	{Key: "sub_number", Label: "Neue Kontonummer (leer = Vorschlag)", Type: tText, ActionOnly: true},
	{Key: "sub_name", Label: "Bezeichnung des neuen Kontos", Type: tText, ActionOnly: true},
}

func (m *Module) subAccountAction() crud.Action {
	return crud.Action{ActionConfig: metamodel.ActionConfig{Name: "createSubAccount", Label: "Unterkonto erzeugen …", Record: true,
		Fields: []string{"sub_mode", "sub_number", "sub_name"}}, Handle: m.createSubAccount}
}

func (m *Module) createSubAccount(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Mode   string `json:"sub_mode"`
			Number string `json:"sub_number"`
			Name   string `json:"sub_name"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if err := m.migrate(ctx); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity("GLAccount").ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	chart, src := crud.Str(key["chart_of_accounts_id"]), crud.Str(key["account_number"])
	res, err := m.db.Query(ctx, `SELECT name, account_type, account_kind, account_group, parent_number, is_group FROM ledger__account_master
		WHERE chart_of_accounts_id = ? AND account_number = ?`, chart, src)
	if err != nil {
		return sdk.Response{}, err
	}
	if len(res.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Konto %s gibt es im Kontenplan %s nicht", src, chart)
	}
	r := res.Rows[0]
	mode := strings.ToUpper(strings.TrimSpace(in.Data.Mode))
	if mode == "" {
		mode = subSameLength
	}
	if mode != subSameLength && mode != subExtend {
		return sdk.Response{}, crud.Invalid("Nummer: %s oder %s", subSameLength, subExtend)
	}
	if crud.AsBool(r[5]) && mode == subSameLength {
		return sdk.Response{}, crud.Invalid("Konto %s ist eine Kontengruppe – Unterkonten „verlängert“ anlegen", src)
	}
	name := strings.TrimSpace(in.Data.Name)
	if name == "" {
		name = crud.Str(r[0])
	}
	number := strings.TrimSpace(in.Data.Number)
	if number != "" {
		if number, err = m.accountKey(ctx, chart, number); err != nil {
			return sdk.Response{}, err
		}
	} else if number, err = m.proposeSubNumber(ctx, chart, src, mode); err != nil {
		return sdk.Response{}, err
	}
	if m.accountExists(ctx, chart, number) {
		return sdk.Response{}, crud.Invalid("Konto %s gibt es im Kontenplan %s schon", number, chart)
	}
	parent := r[4]
	if mode == subExtend {
		parent = src
		if n, err := m.postedItems(ctx, chart, src); err != nil || n > 0 {
			if err != nil {
				return sdk.Response{}, err
			}
			return sdk.Response{}, crud.Invalid("Konto %s hat %d Buchungen – es kann nicht zur Kontengruppe werden; „gleiche Länge“ wählen", src, n)
		}
	}
	var companies int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `INSERT INTO ledger__account_master (chart_of_accounts_id, account_number, name, account_type, account_kind,
			account_group, is_active, parent_number, is_group) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			chart, number, name, r[1], r[2], r[3], true, parent, false); err != nil {
			return err
		}
		if mode == subExtend {
			if _, err := m.db.Exec(ctx, "UPDATE ledger__account_master SET is_group = ? WHERE chart_of_accounts_id = ? AND account_number = ?",
				true, chart, src); err != nil {
				return err
			}
		}
		cc, err := m.db.Query(ctx, `SELECT company_code_id, currency, reconciliation_type, tax_category, field_status_group FROM ledger__account_company
			WHERE chart_of_accounts_id = ? AND account_number = ?`, chart, src)
		if err != nil {
			return err
		}
		for _, c := range cc.Rows {
			if _, err := m.db.Exec(ctx, `INSERT INTO ledger__account_company (id, company_code_id, chart_of_accounts_id, account_number, currency,
				reconciliation_type, tax_category, field_status_group, is_blocked) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				crud.NewID(), c[0], chart, number, c[1], c[2], c[3], c[4], false); err != nil {
				return err
			}
			companies++
		}
		if mode == subExtend { // Gruppen sind nicht bebuchbar: Zuordnungen des alten Kontos sperren
			_, err = m.db.Exec(ctx, "UPDATE ledger__account_company SET is_blocked = ? WHERE chart_of_accounts_id = ? AND account_number = ?",
				true, chart, src)
		}
		return err
	})
	if err != nil {
		return sdk.Response{}, err
	}
	msg := fmt.Sprintf("Konto %s „%s“ angelegt (in %d Buchungskreisen)", number, name, companies)
	if mode == subExtend {
		msg += fmt.Sprintf("; %s ist jetzt Kontengruppe", src)
	}
	return sdk.Response{Payload: map[string]any{"message": msg, "account_number": number}}, nil
}

// proposeSubNumber: nächste freie Nummer gleicher Länge bzw. Nummer + 01, 02 …
func (m *Module) proposeSubNumber(ctx context.Context, chart, src, mode string) (string, error) {
	if mode == subExtend {
		for i := 1; i <= 99; i++ {
			if n := fmt.Sprintf("%s%02d", src, i); !m.accountExists(ctx, chart, n) {
				return n, nil
			}
		}
		return "", crud.Invalid("unter %s sind alle Nummern %s01–%s99 vergeben", src, src, src)
	}
	v, err := strconv.ParseUint(src, 10, 64)
	if err != nil {
		return "", crud.Invalid("Konto %s ist nicht numerisch – neue Nummer bitte eingeben", src)
	}
	width := len(src)
	for n := v + 1; len(strconv.FormatUint(n, 10)) <= width; n++ {
		cand := fmt.Sprintf("%0*d", width, n)
		if !m.accountExists(ctx, chart, cand) {
			return cand, nil
		}
	}
	return "", crud.Invalid("nach %s ist keine Nummer gleicher Länge frei – „verlängert“ wählen", src)
}

func (m *Module) accountExists(ctx context.Context, chart, number string) bool {
	res, err := m.db.Query(ctx, "SELECT 1 FROM ledger__account_master WHERE chart_of_accounts_id = ? AND account_number = ?", chart, number)
	return err != nil || len(res.Rows) > 0
}

// postedItems: Einzelposten und offene Vorerfassung auf dem Konto (alle Buchungskreise des Kontenplans).
func (m *Module) postedItems(ctx context.Context, chart, number string) (int64, error) {
	res, err := m.db.Query(ctx, `SELECT (SELECT COUNT(*) FROM ledger__journal_item WHERE chart_of_accounts_id = ? AND account_number = ?)
		+ (SELECT COUNT(*) FROM ledger__draft_item i JOIN ledger__draft_header h ON h.id = i.draft_id
		   JOIN ledger__company_config c ON c.company_code_id = h.company_code_id
		   WHERE c.chart_of_accounts_id = ? AND i.account_number = ? AND h.status = ?)`, chart, number, chart, number, draftOpen)
	if err != nil {
		return 0, err
	}
	return toInt(res.Rows[0][0]), nil
}
