package ledger

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Steuerungstabellen: Belegarten, Feldstatusgruppen, Kontensperren je Periode,
// sowie Sperren/Entsperren von Konten.

var (
	itemTypes = []metamodel.Option{
		{Value: itemGL, Label: "Sachkonto"},
		{Value: itemCustomer, Label: "Debitor"},
		{Value: itemSupplier, Label: "Kreditor"},
		{Value: itemTax, Label: "Steuer"},
		{Value: itemAsset, Label: "Anlage"},
	}
	fieldStatuses = []metamodel.Option{
		{Value: statusSuppress, Label: "Ausblenden"},
		{Value: statusOptional, Label: "Kann-Eingabe"},
		{Value: statusRequired, Label: "Muss-Eingabe"},
	}
	refDocType = &crud.Ref{Table: "ledger__document_type", Column: "code", Label: "Belegart", ActiveField: "is_active",
		Object: "DocumentType", LabelFields: []string{"name"}}
	refFSG = &crud.Ref{Table: "ledger__field_status_group", Column: "id", Label: "Feldstatusgruppe", ActiveField: "is_active",
		Object: "FieldStatusGroup", LabelFields: []string{"name"}}
)

// DocumentType: Belegart (analog T003).
func (m *Module) documentTypeEntity() *crud.Entity {
	return &crud.Entity{
		Object: "DocumentType", Title: "Belegarten", Icon: "icon-tag", Table: "ledger__document_type", Section: "Einstellungen",
		Keys: []string{"code"}, Order: "code", StatusField: "is_active", TitleField: "name", Search: []string{"code", "name"},
		Authorization: documentTypeAuthorization,
		Fields: []crud.Field{
			{Key: "code", Label: "Belegart", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "allowed_item_types", Label: "Erlaubte Positionsarten (GL, CUSTOMER, SUPPLIER, TAX)", Type: tText, Required: true, Listable: true},
			{Key: "reference_required", Label: "Referenz Pflicht", Type: tBool, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["code"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["code"])))
				if !regexpCode2.MatchString(crud.Str(rec["code"])) {
					return crud.Invalid("Belegart: zwei Zeichen, z. B. SA")
				}
			}
			types := splitList(crud.Str(rec["allowed_item_types"]))
			if len(types) == 0 {
				return crud.Invalid("mindestens eine Positionsart")
			}
			for _, t := range types {
				if !slices.ContainsFunc(itemTypes, func(o metamodel.Option) bool { return o.Value == t }) {
					return crud.Invalid("Positionsart %q – erlaubt: GL, CUSTOMER, SUPPLIER, TAX, ASSET", t)
				}
			}
			rec["allowed_item_types"] = strings.Join(types, ",")
			return nil
		},
	}
}

// FieldStatusGroup: Feldstatusgruppe (analog T004F) mit Status je Feld.
func (m *Module) fieldStatusGroup() *crud.Entity {
	return &crud.Entity{
		Object: "FieldStatusGroup", Title: "Feldstatusgruppen", Icon: "icon-list", Table: "ledger__field_status_group", Section: "Einstellungen",
		Keys: []string{"id"}, Order: "id", StatusField: "is_active", TitleField: "name", Search: []string{"id", "name"},
		Fields: []crud.Field{
			{Key: "id", Label: "Feldstatusgruppe", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "description", Label: "Beschreibung", Type: tArea},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "kopf", Title: "Feldstatusgruppe", Fields: []string{"id", "name", "description", "is_active"}},
			{Key: "felder", Title: "Feldstatus", Relation: &metamodel.Relation{Object: "FieldStatus", ForeignKey: "group_id",
				Columns: []string{"field_name", "status"}}},
		},
		Validate: func(_ context.Context, rec, old crud.Record) error {
			if old == nil {
				rec["id"] = strings.ToUpper(strings.TrimSpace(crud.Str(rec["id"])))
				if !codeRe.MatchString(crud.Str(rec["id"])) {
					return crud.Invalid("Feldstatusgruppe %q: Großbuchstaben, Ziffern, _ -", crud.Str(rec["id"]))
				}
			}
			return nil
		},
		// Neue Gruppen erhalten für jedes Feld den Status Kann-Eingabe.
		AfterCreate: func(ctx context.Context, rec crud.Record) error {
			for _, f := range statusFields {
				if _, err := m.db.Exec(ctx, "INSERT INTO ledger__field_status (group_id, field_name, status) VALUES (?, ?, ?)",
					rec["id"], f, statusOptional); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func (m *Module) fieldStatus() *crud.Entity {
	opts := []metamodel.Option{}
	labels := map[string]string{"item_text": "Positionstext"}
	for _, f := range dimFields(false) {
		labels[f.Key] = f.Label
	}
	for _, f := range statusFields {
		opts = append(opts, metamodel.Option{Value: f, Label: labels[f]})
	}
	return &crud.Entity{
		Object: "FieldStatus", Title: "Feldstatus", Icon: "icon-list", Table: "ledger__field_status", Section: "Einstellungen",
		Keys: []string{"group_id", "field_name"}, Order: "group_id, field_name", Filters: []string{"group_id", "status"},
		Fields: []crud.Field{
			{Key: "group_id", Label: "Feldstatusgruppe", Type: tText, Required: true, Listable: true, Immutable: true, Ref: refFSG},
			{Key: "field_name", Label: "Feld", Type: tSel, Required: true, Listable: true, Immutable: true, Options: opts},
			{Key: "status", Label: "Status", Type: tSel, Required: true, Listable: true, Options: fieldStatuses},
		},
	}
}

// PeriodAccountLock: Kontensperre je Periode – Ausnahme zum Periodenstatus für
// einen Kontenbereich. "Aufheben" (deactivate) beendet die Sperre.
func (m *Module) periodAccountLock() *crud.Entity {
	return &crud.Entity{
		Object: "PeriodAccountLock", Title: "Kontensperren (Perioden)", Icon: "icon-lock", Table: "ledger__period_account_lock", Section: "Einstellungen",
		Keys: []string{"id"}, Surrogate: true, StatusField: "is_active",
		Order:   "company_code_id, fiscal_year DESC, period_from, account_from",
		Filters: []string{"company_code_id", "ledger", "fiscal_year", "status", "is_active"},
		Fields: []crud.Field{
			{Key: "id", Label: "ID", Type: tText, ReadOnly: true},
			{Key: "company_code_id", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "ledger", Label: "Ledger", Type: tText, Listable: true, Immutable: true, Ref: refLedger},
			{Key: "fiscal_year", Label: "Geschäftsjahr", Type: tNum, Required: true, Listable: true, Immutable: true},
			{Key: "period_from", Label: "Von Periode", Type: tNum, Required: true, Listable: true},
			{Key: "period_to", Label: "Bis Periode", Type: tNum, Listable: true},
			{Key: "account_from", Label: "Von Konto", Type: tText, Required: true, Listable: true},
			{Key: "account_to", Label: "Bis Konto", Type: tText, Listable: true},
			{Key: "status", Label: "Konten in diesen Perioden", Type: tSel, Required: true, Listable: true,
				Options: []metamodel.Option{{Value: "CLOSED", Label: "Gesperrt"}, {Value: "OPEN", Label: "Buchbar"}}},
			{Key: "reason", Label: "Grund", Type: tText, Listable: true},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true},
		},
		Access: readByCompany(""),
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "FiscalPeriod", action, crud.Str(rec["company_code_id"]))
		},
		Validate: func(ctx context.Context, rec, old crud.Record) error {
			return m.checkLock(ctx, rec, old == nil)
		},
	}
}

func (m *Module) checkLock(ctx context.Context, rec crud.Record, isNew bool) error {
	if isNew && crud.Str(rec["ledger"]) == "" {
		cfg, err := m.config(ctx, crud.Str(rec["company_code_id"]))
		if err != nil {
			return err
		}
		rec["ledger"] = cfg.Ledger
	}
	from, to := toInt(rec["period_from"]), toInt(rec["period_to"])
	if to == 0 {
		to = from
	}
	if from < 1 || to > 16 || from > to {
		return crud.Invalid("Perioden %d–%d: 1–16, von ≤ bis", from, to)
	}
	rec["period_from"], rec["period_to"] = from, to
	if y := toInt(rec["fiscal_year"]); y < 1900 || y > 2999 {
		return crud.Invalid("Geschäftsjahr %d ungültig", y)
	}
	chart, err := m.companyChart(ctx, crud.Str(rec["company_code_id"]))
	if err != nil {
		return err
	}
	af, err := m.accountKey(ctx, chart, crud.Str(rec["account_from"]))
	if err != nil {
		return err
	}
	at, err := m.accountKey(ctx, chart, crud.Str(rec["account_to"]))
	if err != nil {
		return err
	}
	if at == "" {
		at = af
	}
	if af == "" || af > at {
		return crud.Invalid("Kontenbereich %s–%s: von ≤ bis", af, at)
	}
	rec["account_from"], rec["account_to"] = af, at
	if s := crud.Str(rec["status"]); s != "OPEN" && s != "CLOSED" {
		return crud.Invalid("Status OPEN oder CLOSED")
	}
	rec["changed_at"], rec["changed_by"] = time.Now().UTC().Format(time.RFC3339), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	return nil
}

// flagAction setzt ein boolesches Feld eines Datensatzes (Sperren/Entsperren)
// mit den Prüfungen von update.
func (m *Module) flagAction(object, field string, value bool, msg string) module.HandlerFunc {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		e := m.set.Entity(object)
		p, _ := req.Payload.(map[string]any)
		key, err := e.ParseID(crud.Str(p["id"]))
		if err != nil {
			return sdk.Response{}, err
		}
		rec, err := e.Load(ctx, key)
		if err != nil {
			return sdk.Response{}, err
		}
		if e.CheckRecord != nil {
			if err := e.CheckRecord(ctx, "update", rec); err != nil {
				return sdk.Response{}, err
			}
		}
		var where []string
		args := []any{value}
		for _, k := range e.Keys {
			where, args = append(where, k+" = ?"), append(args, rec[k])
		}
		if _, err := m.db.Exec(ctx, "UPDATE "+e.Table+" SET "+field+" = ? WHERE "+strings.Join(where, " AND "), args...); err != nil {
			return sdk.Response{}, err
		}
		resp, err := e.Get(ctx, map[string]any{"id": e.RecordID(rec)})
		if err != nil {
			return sdk.Response{}, err
		}
		if out, ok := resp.Payload.(crud.Record); ok {
			out["message"] = fmt.Sprintf(msg, e.BusinessKey(rec))
		}
		return resp, nil
	}
}

// lockActions: Sperren/Entsperren als Aktionen je Datensatz.
func (m *Module) lockActions(object, field string, blockedValue bool, what string) []crud.Action {
	return []crud.Action{
		{ActionConfig: metamodel.ActionConfig{Name: "lock", Label: "Sperren", Record: true, Confirm: what + " sperren? Es kann dann nicht mehr bebucht werden."},
			Handle: m.flagAction(object, field, blockedValue, what+" %s gesperrt")},
		{ActionConfig: metamodel.ActionConfig{Name: "unlock", Label: "Entsperren", Record: true, Confirm: what + " wieder zum Buchen öffnen?"},
			Handle: m.flagAction(object, field, !blockedValue, what+" %s entsperrt")},
	}
}

// hideLockAction: je nach Zustand nur Sperren oder nur Entsperren anbieten.
func hideLockAction(rec crud.Record, blocked bool) {
	if blocked {
		rec["_hidden_actions"] = []any{"lock"}
	} else {
		rec["_hidden_actions"] = []any{"unlock"}
	}
}

var regexpCode2 = regexp.MustCompile(`^[0-9A-Z]{2}$`)
