package contract

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Vertragsart ändern (Aktion „Vertragsart ändern …“, seit 0.30.0), z. B. von der
// allgemeinen Dienstleistung DL zur Heizungswartung DLHZ. Richtung, Rolle des
// Vertragspartners und Abrechnungsweg bleiben gleich; erlaubte Objektarten muss
// die neue Art auch zulassen. Jede Kondition braucht eine Kontenfindung der neuen
// Vertragsart: Ihr Sachkonto bleibt, wenn es dort erlaubt ist, sonst gilt der
// Standard. Fehlt etwas, ändert sich nichts und die Meldung nennt alles.

func (m *Module) changeTypeAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Type string `json:"new_contract_type"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "update", c.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	code := trimUpper(in.Data.Type)
	if code == "" {
		return sdk.Response{}, crud.Invalid("Neue Vertragsart fehlt")
	}
	if code == c.Type {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat schon die Vertragsart %s", c.ID, code)
	}
	old, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return sdk.Response{}, err
	}
	nt, err := m.contractTypeOf(ctx, c.CompanyCode, code)
	if err != nil {
		return sdk.Response{}, err
	}
	var problems []string
	if nt.Direction != old.Direction || nt.MainRole != old.MainRole || nt.BillingMode != old.BillingMode {
		problems = append(problems, fmt.Sprintf("Richtung, Rolle des Vertragspartners oder Abrechnungsweg weichen ab (%s/%s/%s statt %s/%s/%s)",
			nt.Direction, nt.MainRole, nt.BillingMode, old.Direction, old.MainRole, old.BillingMode))
	}
	if len(nt.ObjectTypes) > 0 {
		res, err := m.db.Query(ctx, `SELECT DISTINCT object_type FROM contract__object WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, r := range res.Rows {
			if t := crud.Str(r[0]); !slices.ContainsFunc(nt.ObjectTypes, func(x string) bool { return strings.EqualFold(x, t) }) {
				problems = append(problems, fmt.Sprintf("Objektart %s ist in %s nicht erlaubt", t, code))
			}
		}
	}
	res, err := m.db.Query(ctx, `SELECT condition_type, COALESCE(condition_kind, ''), COALESCE(object_id, ''), position, valid_from, COALESCE(account_number, '')
		FROM contract__condition WHERE company_code = ? AND contract_id = ? ORDER BY condition_type, position, valid_from`, c.CompanyCode, c.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	target := *c
	target.Type = code
	type change struct {
		typ, obj, account string
		pos, from         any
	}
	var changes []change
	moved := 0
	for _, r := range res.Rows {
		rec := crud.Record{"condition_type": crud.Str(r[0]), "condition_kind": crud.Str(r[1]), "account_number": crud.Str(r[5])}
		if err := m.accountFor(ctx, rec, &target); err != nil {
			rec["account_number"] = "" // bisheriges Konto dort nicht vorgesehen: Standard
			if err := m.accountFor(ctx, rec, &target); err != nil {
				problems = append(problems, fmt.Sprintf("Kondition %s: %s", crud.Str(r[0]), strings.TrimPrefix(err.Error(), "invalid argument: ")))
				continue
			}
		}
		acc := crud.Str(rec["account_number"])
		if acc != crud.Str(r[5]) {
			moved++
		}
		vf, _ := crud.ParseDate(r[4]) // DATE-Spalte kommt je nach Treiber als time.Time
		changes = append(changes, change{typ: crud.Str(r[0]), obj: crud.Str(r[2]), pos: r[3], from: vf, account: acc})
	}
	if len(problems) > 0 {
		return sdk.Response{}, crud.Invalid("Vertragsart von %s nicht geändert: %s", c.ID, strings.Join(problems, "; "))
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET contract_type = ? WHERE company_code = ? AND contract_id = ?`,
			code, c.CompanyCode, c.ID); err != nil {
			return err
		}
		for _, ch := range changes {
			if _, err := m.db.Exec(ctx, `UPDATE contract__condition SET contract_type = ?, account_number = ?
				WHERE company_code = ? AND contract_id = ? AND condition_type = ? AND COALESCE(object_id, '') = ? AND position = ? AND valid_from = ?`,
				code, ch.account, c.CompanyCode, c.ID, ch.typ, ch.obj, ch.pos, ch.from); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "contract_type": code,
		"message": fmt.Sprintf("Vertrag %s: Vertragsart %s → %s, %d Konditionen, davon %d mit neuem Sachkonto", c.ID, c.Type, code, len(changes), moved)}}, nil
}
