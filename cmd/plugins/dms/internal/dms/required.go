package dms

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Pflichtunterlagen (seit 0.33.0): Je Buchungskreis legt der Katalog fest, welche
// Unterlagen zu einem Objekt (Gebäude bzw. Wirtschaftseinheit) vorliegen sollen –
// Teilungserklärung, Grundbuchauszug, Energieausweis, Wirtschaftsplan,
// Prüfberichte … Erkannt wird eine Unterlage an der Ablage (Nummer oder Anfang)
// und ggf. der Dokumentart der abgelegten Dokumente des Objekts; mit Gültigkeit in
// Monaten ab Dokumentdatum (0 = unbegrenzt, z. B. Teilungserklärung; 12–15 für
// Jahresunterlagen; 120 für den Energieausweis). Arten der Wirtschaftseinheit
// grenzen ein (z. B. Beschlusssammlung nur bei Sondereigentum in einer WEG).
//
// „Unterlagenstatus“ zeigt je Objekt und Unterlage: vorhanden, bald fällig
// (Vorwarnzeit), abgelaufen, fehlt oder entfällt. Ausnahme („entfällt“, z. B. kein
// Öltank), Papierunterlage mit Datum und Vermerk pflegt der Benutzer; der Stand
// wird beim Anzeigen neu ermittelt (höchstens alle 30 Sekunden je Buchungskreis)
// und mit „Stand neu ermitteln“.

const (
	requiredObject = "DmsRequiredDoc"
	reqStatusObj   = "DmsRequiredStatus"

	rqOK      = "OK"
	rqDue     = "DUE"
	rqExpired = "EXPIRED"
	rqMissing = "MISSING"
	rqNA      = "NA"
)

var reqStatusOptions = []metamodel.Option{
	{Value: rqOK, Label: "vorhanden", Tone: metamodel.ToneSuccess},
	{Value: rqDue, Label: "bald fällig", Tone: metamodel.ToneAttention},
	{Value: rqExpired, Label: "abgelaufen", Tone: metamodel.ToneDanger},
	{Value: rqMissing, Label: "fehlt", Tone: metamodel.ToneDanger},
	{Value: rqNA, Label: "entfällt", Tone: metamodel.ToneMuted},
}

func (m *Module) requiredDoc() *crud.Entity {
	return &crud.Entity{
		Object: requiredObject, Title: "Pflichtunterlagen", Icon: "icon-check", Table: "dms__required_doc", Section: secSettings,
		Keys: []string{"company_code", "code"}, Order: "company_code, sort_order, code", StatusField: "is_active", TitleField: "name",
		Filters: []string{"company_code", "doc_group"}, Search: []string{"code", "name", "doc_group", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "code", Label: "Schlüssel", Type: tText, Required: true, Listable: true, Immutable: true},
			{Key: "name", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "doc_group", Label: "Gruppe (z. B. Recht & Immobilie, Laufender Betrieb)", Short: "Gruppe", Type: tText, Listable: true},
			{Key: "file_accounts", Label: "Ablagen (Nummern oder Anfänge, durch Komma)", Short: "Ablagen", Type: tText, Required: true, Listable: true},
			{Key: "doc_types", Label: "Dokumentarten (durch Komma; leer = jede)", Short: "Dokumentarten", Type: tText},
			{Key: "entity_types", Label: "Nur Arten der Wirtschaftseinheit (durch Komma, z. B. SEV, ETW; leer = jedes Objekt)", Short: "Objektarten", Type: tText, Listable: true},
			{Key: "valid_months", Label: "Gültig (Monate ab Dokumentdatum; 0 = unbegrenzt)", Short: "Gültig (Mon.)", Type: tNum, Listable: true},
			{Key: "warn_days", Label: "Vorwarnzeit (Tage vor Ablauf)", Short: "Vorwarnung", Type: tNum},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "sort_order", Label: "Reihenfolge", Type: tNum},
			{Key: "is_active", Label: "Aktiv", Type: tBool, Listable: true, ReadOnly: true},
		},
		Access:   &crud.Access{Records: true, CompanyCode: "company_code"},
		Validate: m.checkRequired,
		Actions:  []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "refresh", Label: "Stand neu ermitteln"}, Handle: m.refreshAction}},
	}
}

func (m *Module) checkRequired(ctx context.Context, rec, _ crud.Record) error {
	if err := checkCode(rec, "code", "Schlüssel"); err != nil {
		return err
	}
	if _, err := m.companyConfig(ctx, crud.Str(rec["company_code"])); err != nil {
		return err
	}
	accounts := splitList(crud.Str(rec["file_accounts"]))
	if len(accounts) == 0 {
		return crud.Invalid("Ablagen: mindestens eine")
	}
	for _, a := range accounts {
		if !numberRe.MatchString(a) {
			return crud.Invalid("Ablage %q: Nummer wie 1.1.0 oder Anfang wie 6.3", a)
		}
	}
	rec["file_accounts"] = strings.Join(accounts, ", ")
	var types []string
	for _, t := range splitList(crud.Str(rec["doc_types"])) {
		t = strings.ToUpper(t)
		if _, err := m.docTypeOf(ctx, t); err != nil {
			return err
		}
		types = append(types, t)
	}
	rec["doc_types"] = strings.Join(types, ", ")
	var ets []string
	for _, t := range splitList(crud.Str(rec["entity_types"])) {
		ets = append(ets, strings.ToUpper(t))
	}
	rec["entity_types"] = strings.Join(ets, ", ")
	if toInt(rec["valid_months"]) < 0 || toInt(rec["warn_days"]) < 0 {
		return crud.Invalid("Gültigkeit und Vorwarnzeit: nicht negativ")
	}
	if crud.Str(rec["warn_days"]) == "" {
		rec["warn_days"] = 60
	}
	if crud.Str(rec["valid_months"]) == "" {
		rec["valid_months"] = 0
	}
	return nil
}

func (m *Module) requiredStatus() *crud.Entity {
	return &crud.Entity{
		Object: reqStatusObj, Title: "Unterlagenstatus", Icon: "icon-check", Table: "dms__required_status", Section: secDocs,
		Keys: []string{"company_code", "property", "required"}, Order: "company_code, property, required",
		Filters: []string{"company_code", "property", "required", "status"}, Search: []string{"property", "required", "note"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Listable: true, ReadOnly: true, Lookup: lookupCC},
			{Key: "property", Label: "Objekt", Type: tText, Listable: true, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: propertyObject, ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "required", Label: "Unterlage", Type: tText, Listable: true, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: requiredObject, ValueField: "code", LabelFields: []string{"name"}, Filters: map[string]string{"company_code": "company_code"}}},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: reqStatusOptions},
			{Key: "last_date", Label: "Letztes Dokument vom", Short: "Stand vom", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "valid_until", Label: "Gültig bis", Type: tDate, Listable: true, ReadOnly: true},
			{Key: "doc_count", Label: "Dokumente", Type: tNum, Listable: true, ReadOnly: true},
			{Key: "last_document", Label: "Letztes Dokument", Type: tText, ReadOnly: true,
				Lookup: &metamodel.Lookup{Object: docObject, ValueField: "id", LabelFields: []string{"title"}}},
			{Key: "not_required", Label: "Entfällt für dieses Objekt", Short: "entfällt", Type: tBool},
			{Key: "paper_date", Label: "Liegt auf Papier vor, Stand vom", Short: "Papier", Type: tDate},
			{Key: "note", Label: "Vermerk", Type: tText, Listable: true},
			{Key: "checked_at", Label: "Ermittelt am", Type: tText, ReadOnly: true},
		},
		Access: &crud.Access{Records: true, CompanyCode: "company_code"},
		ListScope: func(ctx context.Context) (string, []any, bool, error) {
			m.refreshThrottled(ctx)
			return "", nil, false, nil
		},
		// Benutzerangaben gelten sofort: „entfällt“ hier, Papierdatum beim nächsten Anzeigen
		AfterUpdate: func(ctx context.Context, rec crud.Record) error {
			refreshMu.Lock()
			delete(refreshedAt, crud.Str(rec["company_code"]))
			refreshMu.Unlock()
			if !crud.AsBool(rec["not_required"]) {
				return nil
			}
			_, err := m.db.Exec(ctx, `UPDATE dms__required_status SET status = ? WHERE company_code = ? AND property = ? AND required = ?`,
				rqNA, rec["company_code"], rec["property"], rec["required"])
			return err
		},
		Actions: []crud.Action{{ActionConfig: metamodel.ActionConfig{Name: "refresh", Label: "Stand neu ermitteln"}, Handle: m.refreshAction}},
	}
}

var (
	refreshMu   sync.Mutex
	refreshedAt = map[string]time.Time{}
)

// refreshThrottled: Stand aller Buchungskreise mit Katalog, höchstens alle 30 s.
func (m *Module) refreshThrottled(ctx context.Context) {
	res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM dms__required_doc WHERE is_active = ?`, true)
	if err != nil {
		return
	}
	for _, r := range res.Rows {
		cc := crud.Str(r[0])
		refreshMu.Lock()
		due := time.Since(refreshedAt[cc]) > 30*time.Second
		if due {
			refreshedAt[cc] = time.Now()
		}
		refreshMu.Unlock()
		if due {
			if _, err := m.refreshRequired(ctx, cc); err != nil {
				m.log.WarnContext(ctx, "Unterlagenstatus nicht ermittelt", "company_code", cc, "err", err.Error())
			}
		}
	}
}

func (m *Module) refreshAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	res, err := m.db.Query(ctx, `SELECT DISTINCT company_code FROM dms__required_doc WHERE is_active = ?`, true)
	if err != nil {
		return sdk.Response{}, err
	}
	n := 0
	for _, r := range res.Rows {
		cc := crud.Str(r[0])
		if ok, err := sdk.CheckAccess(ctx, reqStatusObj, "refresh", cc); err != nil || !ok {
			continue
		}
		c, err := m.refreshRequired(ctx, cc)
		if err != nil {
			return sdk.Response{}, err
		}
		n += c
	}
	return sdk.Response{Payload: map[string]any{"rows": n, "message": fmt.Sprintf("Unterlagenstatus ermittelt: %d Zeilen", n)}}, nil
}

type requiredRow struct {
	Code, Name             string
	Accounts, Types, Kinds []string
	ValidMonths, WarnDays  int64
}

type reqDoc struct{ ID, Account, Type, Date string }

// refreshRequired: Stand je Objekt und Pflichtunterlage neu ermitteln; Zeilen,
// die nicht mehr gelten und keine Benutzerangaben tragen, entfallen.
func (m *Module) refreshRequired(ctx context.Context, cc string) (int, error) {
	res, err := m.db.Query(ctx, `SELECT code, name, file_accounts, doc_types, entity_types, valid_months, warn_days FROM dms__required_doc
		WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`, cc, true)
	if err != nil {
		return 0, err
	}
	var reqs []requiredRow
	for _, r := range res.Rows {
		var kinds []string
		for _, k := range splitList(crud.Str(r[4])) {
			kinds = append(kinds, strings.ToUpper(k))
		}
		reqs = append(reqs, requiredRow{Code: crud.Str(r[0]), Name: crud.Str(r[1]), Accounts: splitList(crud.Str(r[2])),
			Types: splitList(strings.ToUpper(crud.Str(r[3]))), Kinds: kinds, ValidMonths: toInt(r[5]), WarnDays: toInt(r[6])})
	}
	props, err := m.requiredProperties(ctx, cc)
	if err != nil {
		return 0, err
	}
	docs := map[string][]reqDoc{}
	res, err = m.db.Query(ctx, `SELECT id, property, COALESCE(file_account, ''), COALESCE(doc_type, ''), COALESCE(doc_date, '') FROM dms__document
		WHERE company_code = ? AND status = ? AND COALESCE(property, '') <> '' ORDER BY doc_date DESC, id`, cc, stFiled)
	if err != nil {
		return 0, err
	}
	for _, r := range res.Rows {
		p := crud.Str(r[1])
		docs[p] = append(docs[p], reqDoc{ID: crud.Str(r[0]), Account: crud.Str(r[2]), Type: crud.Str(r[3]), Date: dateOnly(crud.Str(r[4]))})
	}
	type user struct {
		NA          bool
		Paper, Note string
	}
	users := map[[2]string]user{}
	res, err = m.db.Query(ctx, `SELECT property, required, not_required, COALESCE(paper_date, ''), COALESCE(note, '') FROM dms__required_status WHERE company_code = ?`, cc)
	if err != nil {
		return 0, err
	}
	for _, r := range res.Rows {
		users[[2]string{crud.Str(r[0]), crud.Str(r[1])}] = user{NA: crud.AsBool(r[2]), Paper: dateOnly(crud.Str(r[3])), Note: crud.Str(r[4])}
	}
	today := time.Now()
	at := today.UTC().Format(time.RFC3339)
	keep := map[[2]string]bool{}
	n := 0
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		for _, p := range props {
			for _, q := range reqs {
				if len(q.Kinds) > 0 && !slices.Contains(q.Kinds, p.Kind) {
					continue
				}
				k := [2]string{p.Code, q.Code}
				keep[k] = true
				u := users[k]
				var last *reqDoc
				count := 0
				for i, d := range docs[p.Code] {
					if !accountMatches(d.Account, q.Accounts) || len(q.Types) > 0 && !slices.Contains(q.Types, d.Type) {
						continue
					}
					count++
					if last == nil || d.Date > last.Date {
						last = &docs[p.Code][i]
					}
				}
				date, docID := "", ""
				if last != nil {
					date, docID = last.Date, last.ID
				}
				if u.Paper > date {
					date = u.Paper
				}
				status, until := evalRequired(date, q.ValidMonths, q.WarnDays, today)
				if u.NA {
					status = rqNA
				}
				if _, err := m.db.Exec(ctx, `INSERT INTO dms__required_status (company_code, property, required, status, doc_count, last_document, last_date,
					valid_until, checked_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
					ON CONFLICT (company_code, property, required) DO UPDATE SET status = excluded.status, doc_count = excluded.doc_count,
					last_document = excluded.last_document, last_date = excluded.last_date, valid_until = excluded.valid_until, checked_at = excluded.checked_at`,
					cc, p.Code, q.Code, status, count, nilIfEmpty(docID), nilIfEmpty(date), nilIfEmpty(until), at); err != nil {
					return err
				}
				n++
			}
		}
		for k, u := range users {
			if keep[k] {
				continue
			}
			if !u.NA && u.Paper == "" && u.Note == "" {
				if _, err := m.db.Exec(ctx, `DELETE FROM dms__required_status WHERE company_code = ? AND property = ? AND required = ?`, cc, k[0], k[1]); err != nil {
					return err
				}
				continue
			}
			if _, err := m.db.Exec(ctx, `UPDATE dms__required_status SET status = ?, checked_at = ? WHERE company_code = ? AND property = ? AND required = ?`,
				rqNA, at, cc, k[0], k[1]); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// evalRequired: Status und Ablaufdatum zum Stand date (leer = keine Unterlage).
func evalRequired(date string, validMonths, warnDays int64, today time.Time) (string, string) {
	if date == "" {
		return rqMissing, ""
	}
	if validMonths <= 0 {
		return rqOK, ""
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return rqMissing, ""
	}
	until := d.AddDate(0, int(validMonths), 0)
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case until.Before(day):
		return rqExpired, until.Format("2006-01-02")
	case !until.After(day.AddDate(0, 0, int(warnDays))):
		return rqDue, until.Format("2006-01-02")
	}
	return rqOK, until.Format("2006-01-02")
}

func dateOnly(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

type reqProperty struct{ Code, Kind string }

// requiredProperties: Objekte des Buchungskreises – aktive Gebäude (Plugin
// realestate) und Objekte mit Dokumenten – mit der Art ihrer Wirtschaftseinheit.
func (m *Module) requiredProperties(ctx context.Context, cc string) ([]reqProperty, error) {
	seen := map[string]bool{}
	var out []reqProperty
	kinds := map[string]string{} // Wirtschaftseinheit → Art
	entityKind := func(id string) string {
		if id == "" {
			return ""
		}
		if k, ok := kinds[id]; ok {
			return k
		}
		k := ""
		if resp, err := m.services.Call(ctx, "BusinessEntity", "get", map[string]any{"id": cc + "|" + id}); err == nil {
			var rec map[string]any
			if sdk.Decode(resp.Payload, &rec) == nil {
				k = strings.ToUpper(crud.Str(rec["entity_type"]))
			}
		}
		kinds[id] = k
		return k
	}
	buildings := map[string]string{} // Gebäude → Wirtschaftseinheit
	if resp, err := m.services.Call(ctx, "Building", "list", map[string]any{"query": map[string]any{"company_code": cc}, "limit": 1000}); err == nil {
		var list struct {
			Items []map[string]any `json:"items"`
		}
		if sdk.Decode(resp.Payload, &list) == nil {
			for _, b := range list.Items {
				if crud.Str(b["company_code"]) != "" && crud.Str(b["company_code"]) != cc {
					continue
				}
				id := crud.Str(b["building_id"])
				buildings[id] = crud.Str(b["entity_id"])
				if st := strings.ToUpper(crud.Str(b["status"])); id == "" || st != "" && st != "ACTIVE" {
					continue
				}
				if !seen[id] {
					seen[id] = true
					out = append(out, reqProperty{Code: id, Kind: entityKind(buildings[id])})
					if _, err := m.db.Exec(ctx, `INSERT INTO dms__property (company_code, code, name) VALUES (?, ?, ?) ON CONFLICT (company_code, code) DO NOTHING`,
						cc, id, nilIfEmpty(crud.Str(b["designation"]))); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	res, err := m.db.Query(ctx, `SELECT code FROM dms__property WHERE company_code = ? ORDER BY code`, cc)
	if err != nil {
		return nil, err
	}
	for _, r := range res.Rows {
		code := crud.Str(r[0])
		if seen[code] {
			continue
		}
		seen[code] = true
		kind := ""
		if e, ok := buildings[code]; ok {
			kind = entityKind(e)
		} else {
			kind = entityKind(code) // Objekt ist selbst eine Wirtschaftseinheit
		}
		out = append(out, reqProperty{Code: code, Kind: kind})
	}
	return out, nil
}
