package contract

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
	"github.com/coremesh-labs/coremesh/pkg/sdk/numrange"
)

const (
	statusDraft      = "DRAFT"
	statusActive     = "ACTIVE"
	statusTerminated = "TERMINATED"
)

var (
	statusOptions = []metamodel.Option{
		{Value: statusDraft, Label: "Entwurf"}, {Value: statusActive, Label: "aktiv"}, {Value: statusTerminated, Label: "gekündigt"}}
	terminatedByOptions = []metamodel.Option{
		{Value: "PARTNER", Label: "Vertragspartner"}, {Value: "US", Label: "wir"}, {Value: "MUTUAL", Label: "einvernehmlich (Aufhebung)"}}
)

// --- Vertrag -------------------------------------------------------------------------

func (m *Module) contract() *crud.Entity {
	return &crud.Entity{
		Object: "Contract", Title: "Verträge", Icon: "icon-file", Table: "contract__contract", Section: "Verträge",
		Keys: []string{"company_code", "contract_id"}, Order: "company_code, contract_id DESC", TitleField: "designation",
		Filters: []string{"company_code", "contract_type", "status", "partner_id", "direction"},
		Search:  []string{"contract_id", "external_number", "designation"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"contract_type", "direction", "status", "partner_id", "valid_from", "valid_to"},
		Fields: []crud.Field{
			{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			{Key: "contract_type", Label: "Vertragsart", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("ContractType")},
			{Key: "contract_id", Label: "Vertragsnummer (intern, aus dem Nummernkreis)", Type: tText, Listable: true, ReadOnly: true},
			{Key: "external_number", Label: "Externe Vertragsnummer (leer = Vertragsart-Partner-Nummer)", Type: tText, Listable: true},
			{Key: "designation", Label: "Bezeichnung", Type: tText, Required: true, Listable: true},
			{Key: "partner_id", Label: "Vertragspartner", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"},
					Filters: map[string]string{"role": "contract_type.main_role"}}},
			{Key: "direction", Label: "Richtung", Type: tSel, Listable: true, ReadOnly: true, Options: directionOptions},
			{Key: "currency", Label: "Währung", Type: tText, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "Currency", ValueField: "code", LabelFields: []string{"name"}}},
			{Key: "status", Label: "Status", Type: tSel, Listable: true, ReadOnly: true, Options: statusOptions},
			{Key: "valid_from", Label: "Beginn", Type: tDate, Required: true, Listable: true, Group: "Laufzeit"},
			{Key: "valid_to", Label: "Ende (leer = unbefristet)", Type: tDate, Listable: true, Group: "Laufzeit"},
			{Key: "signed_date", Label: "Unterschrieben am", Type: tDate, Group: "Laufzeit"},
			{Key: "notice_received", Label: "Kündigung eingegangen am", Type: tDate, ReadOnly: true, Group: "Kündigung"},
			{Key: "terminated_by", Label: "Gekündigt von", Type: tSel, ReadOnly: true, Options: terminatedByOptions, Group: "Kündigung"},
			{Key: "termination_reason", Label: "Kündigungsgrund", Type: tText, ReadOnly: true, Group: "Kündigung"},
			{Key: "note", Label: "Bemerkung", Type: tArea},
			{Key: "changed_at", Label: "Geändert am", Type: tText, ReadOnly: true},
			{Key: "changed_by", Label: "Geändert von", Type: tText, ReadOnly: true},
			// nur im Formular „Buchen …“ (Sollstellung bis zu diesem Fälligkeitsdatum)
			{Key: "post_until", Label: "Buchen bis (Fälligkeit)", Type: tDate, ActionOnly: true},
		},
		Sections: []metamodel.SectionDefinition{
			{Key: "partner", Title: "Partner", Relation: &metamodel.Relation{Object: "ContractPartner", ForeignKey: "contract_id",
				Match: match(), Columns: []string{"role_code", "partner_id", "share", "valid_from", "valid_to"}}},
			{Key: "objekte", Title: "Objekte", Relation: &metamodel.Relation{Object: "ContractObject", ForeignKey: "contract_id",
				Match: match(), Columns: []string{"object_type", "object_id", "is_main", "valid_from", "valid_to"}}},
			{Key: "konditionen", Title: "Konditionen", Relation: &metamodel.Relation{Object: "ContractCondition", ForeignKey: "contract_id",
				Match: match("contract_type"), Columns: []string{"condition_type", "object_id", "amount", "frequency", "account_number", "valid_from", "valid_to"}}},
			{Key: "kuendigungsregeln", Title: "Kündigungsregeln", Collapsed: true, Relation: &metamodel.Relation{Object: "ContractNoticeTerm", ForeignKey: "contract_id",
				Match: match(), Columns: []string{"notice_period_months", "notice_deadline_day", "minimum_duration_months", "has_renewal_option", "valid_from", "valid_to"}}},
			{Key: "sollstellungen", Title: "Sollstellungen", Collapsed: true, Relation: &metamodel.Relation{Object: postingObject, ForeignKey: "contract_id",
				Match: match(), Columns: []string{"condition_type", "period_from", "period_to", "due_date", "amount", "status", "document_number"}}},
			{Key: "merkmale", Title: "Merkmale", Tags: true},
		},
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code", Fields: []string{"contract_type"}},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Contract", action, crud.Str(rec["company_code"]))
		},
		// Nummern vor der Transaktion ziehen (numrange vergibt in einer eigenen).
		Prepare:     func(ctx context.Context, rec crud.Record) error { return m.checkContract(ctx, rec, nil) },
		Validate:    m.checkContract,
		AfterCreate: m.afterCreateContract,
		FormState:   m.contractFormState,
		Actions: []crud.Action{
			{ActionConfig: metamodel.ActionConfig{Name: "activate", Label: "Aktivieren", Record: true,
				Confirm: "Vertrag aktivieren? Geprüft werden Vertragspartner, Objekte und Konditionen."}, Handle: m.activateAction},
			{ActionConfig: metamodel.ActionConfig{Name: "terminate", Label: "Kündigen …", Record: true,
				Fields: []string{"notice_received", "terminated_by", "termination_reason", "valid_to"}}, Handle: m.terminateAction},
			{ActionConfig: metamodel.ActionConfig{Name: "post", Label: "Buchen …", Record: true, Fields: []string{"post_until"}, FormState: true},
				Handle: m.postAction},
		},
	}
}

// match: Unter-Objekte hängen über Buchungskreis und Vertragsnummer am Vertrag
// (zusätzliche Felder werden mit übernommen, z. B. die Vertragsart).
func match(extra ...string) map[string]string {
	out := map[string]string{"company_code": "company_code", "contract_id": "contract_id"}
	for _, f := range extra {
		out[f] = f
	}
	return out
}

func (m *Module) checkContract(ctx context.Context, rec, old crud.Record) error {
	cc := crud.Str(rec["company_code"])
	if crud.Str(rec["valid_to"]) == "" {
		rec["valid_to"] = crud.DateMax
	}
	from, err := crud.ParseDate(rec["valid_from"])
	if err != nil {
		return err
	}
	to, err := crud.ParseDate(rec["valid_to"])
	if err != nil {
		return err
	}
	if to < from {
		return crud.Invalid("Ende (%s) liegt vor dem Beginn (%s)", to, from)
	}
	rec["valid_from"], rec["valid_to"] = from, to
	rec["external_number"] = strings.TrimSpace(crud.Str(rec["external_number"]))
	rec["changed_at"], rec["changed_by"] = now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID)
	if old != nil {
		if crud.Str(old["status"]) != statusDraft && (from != crud.Str(old["valid_from"])) {
			return crud.Invalid("Beginn eines aktiven Vertrags ist fest")
		}
		if crud.Str(old["status"]) != statusDraft && to != crud.Str(old["valid_to"]) {
			return crud.Invalid("Ende eines aktiven Vertrags über „Kündigen …“ setzen")
		}
		if rec["external_number"] == "" {
			return crud.Invalid("Externe Vertragsnummer ist Pflicht")
		}
		return nil
	}
	// Neuanlage: Vertragsart, Vertragspartner in der Hauptrolle, Nummern.
	rec["contract_type"] = trimUpper(rec["contract_type"])
	ct, err := m.contractTypeOf(ctx, cc, crud.Str(rec["contract_type"]))
	if err != nil {
		return err
	}
	role, err := m.roleSetting(ctx, cc, ct.MainRole)
	if err != nil {
		return err
	}
	partner := strings.TrimSpace(crud.Str(rec["partner_id"]))
	rec["partner_id"] = partner
	if err := m.requirePartnerRole(ctx, partner, ct.MainRole, role.Name, from, to); err != nil {
		return err
	}
	if ct.AccountRequired {
		if err := m.requirePartnerAccount(ctx, cc, partner, ct.MainRole, "Vertragspartner"); err != nil {
			return err
		}
	}
	rec["direction"], rec["status"] = ct.Direction, statusDraft
	if crud.Str(rec["currency"]) == "" {
		rec["currency"] = "EUR"
	}
	rec["currency"] = trimUpper(rec["currency"])
	if crud.Str(rec["contract_id"]) != "" {
		return nil // Nummern schon vergeben (Prepare, vor der Transaktion)
	}
	nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: rangeContract, CompanyCode: cc, Key: ct.RangeKey,
		Reference: crud.Str(rec["designation"])})
	if err != nil {
		return unavailable("Nummernkreis", err)
	}
	rec["contract_id"] = nr.Number
	if rec["external_number"] == "" {
		ext, err := m.externalNumber(ctx, cc, ct.Code, partner, nr.Number)
		if err != nil {
			return err
		}
		rec["external_number"] = ext
	}
	return nil
}

// externalNumber: <Vertragsart>-<3 Buchstaben des Partnernamens>-<nnn>, die
// laufende Nummer je Vertragsart und Kürzel aus dem Nummernkreis ContractExternal.
func (m *Module) externalNumber(ctx context.Context, cc, typ, partner, ref string) (string, error) {
	abbr := nameAbbr(m.partnerName(ctx, partner))
	key := typ + "_" + abbr
	if len(key) > 12 {
		key = key[:12]
	}
	nr, err := numrange.Next(ctx, m.services, numrange.Request{Object: rangeExternal, CompanyCode: cc, Key: key, Reference: ref})
	if err != nil {
		return "", unavailable("Nummernkreis", err)
	}
	return typ + "-" + abbr + "-" + nr.Number, nil
}

// nameAbbr: die ersten drei Buchstaben des Namens, groß, Umlaute ausgeschrieben
// ("Müller" → MUE); zu kurz: mit X aufgefüllt.
func nameAbbr(name string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss")
	var b strings.Builder
	for _, c := range r.Replace(name) {
		if c < unicode.MaxASCII && unicode.IsLetter(c) {
			b.WriteRune(unicode.ToUpper(c))
			if b.Len() == 3 {
				break
			}
		}
	}
	return (b.String() + "XXX")[:3]
}

// afterCreateContract: Der Vertragspartner steht in der Hauptrolle für die
// ganze Laufzeit im Abschnitt Partner.
func (m *Module) afterCreateContract(ctx context.Context, rec crud.Record) error {
	cc := crud.Str(rec["company_code"])
	ct, err := m.contractTypeOf(ctx, cc, crud.Str(rec["contract_type"]))
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `INSERT INTO contract__partner (company_code, contract_id, role_code, partner_id, valid_from, valid_to, share)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, cc, rec["contract_id"], ct.MainRole, rec["partner_id"], rec["valid_from"], rec["valid_to"], nil)
	return err
}

// contractRow: Kopf eines Vertrags.
type contractRow struct {
	CompanyCode, ID, Type, Status, Direction, Currency, Partner, ValidFrom, ValidTo, Designation string
}

func (m *Module) contractOf(ctx context.Context, cc, id string) (*contractRow, error) {
	res, err := m.db.Query(ctx, `SELECT contract_id, contract_type, status, direction, currency, partner_id, valid_from, valid_to, designation
		FROM contract__contract WHERE company_code = ? AND contract_id = ?`, cc, id)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Vertrag %s gibt es im Buchungskreis %s nicht", id, cc)
	}
	r := res.Rows[0]
	from, _ := crud.ParseDate(r[6])
	to, _ := crud.ParseDate(r[7])
	return &contractRow{CompanyCode: cc, ID: crud.Str(r[0]), Type: crud.Str(r[1]), Status: crud.Str(r[2]), Direction: crud.Str(r[3]),
		Currency: crud.Str(r[4]), Partner: crud.Str(r[5]), ValidFrom: from, ValidTo: to, Designation: crud.Str(r[8])}, nil
}

// contractOfID: Vertrag aus der Record-ID "<Buchungskreis>|<Vertragsnummer>".
func (m *Module) contractOfID(ctx context.Context, id string) (*contractRow, error) {
	cc, nr, ok := strings.Cut(id, "|")
	if !ok {
		return nil, crud.Invalid("Vertrag %q: <Buchungskreis>|<Vertragsnummer> erwartet", id)
	}
	return m.contractOf(ctx, cc, nr)
}

// --- Aktivieren ----------------------------------------------------------------------

// ActivateHookData: Daten des Hooks contract.activate.
type ActivateHookData struct {
	CompanyCode  string           `json:"company_code"`
	ContractID   string           `json:"contract_id"`
	ContractType string           `json:"contract_type"`
	Direction    string           `json:"direction"`
	ValidFrom    string           `json:"valid_from"`
	ValidTo      string           `json:"valid_to"`
	Partners     []map[string]any `json:"partners"`
	Objects      []map[string]any `json:"objects"`
}

func (m *Module) activateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "activate", c.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if c.Status != statusDraft {
		return sdk.Response{}, crud.Invalid("Vertrag %s ist nicht im Entwurf", c.ID)
	}
	ct, err := m.contractTypeOf(ctx, c.CompanyCode, c.Type)
	if err != nil {
		return sdk.Response{}, err
	}
	// Vertragspartner in der Hauptrolle lückenlos über die Laufzeit.
	partners, err := m.db.Query(ctx, `SELECT role_code, partner_id, valid_from, valid_to, share FROM contract__partner
		WHERE company_code = ? AND contract_id = ? ORDER BY valid_from`, c.CompanyCode, c.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	var main [][2]string
	data := ActivateHookData{CompanyCode: c.CompanyCode, ContractID: c.ID, ContractType: c.Type, Direction: c.Direction,
		ValidFrom: c.ValidFrom, ValidTo: c.ValidTo, Partners: []map[string]any{}, Objects: []map[string]any{}}
	for _, r := range partners.Rows {
		f, _ := crud.ParseDate(r[2])
		t, _ := crud.ParseDate(r[3])
		if crud.Str(r[0]) == ct.MainRole {
			main = append(main, [2]string{f, t})
		}
		data.Partners = append(data.Partners, map[string]any{"role_code": r[0], "partner_id": r[1], "valid_from": f, "valid_to": t, "share": r[4]})
	}
	if gap := coverageGap(main, c.ValidFrom, c.ValidTo); gap != "" {
		return sdk.Response{}, crud.Invalid("Vertragspartner in der Rolle %s fehlt ab %s", ct.MainRole, gap)
	}
	if ct.AccountRequired {
		if err := m.requireAccounts(ctx, c, ct); err != nil {
			return sdk.Response{}, err
		}
	}
	objects, err := m.db.Query(ctx, `SELECT object_type, object_id, is_main, valid_from, valid_to FROM contract__object
		WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if ct.NeedsObject && len(objects.Rows) == 0 {
		return sdk.Response{}, crud.Invalid("Vertragsart %s braucht ein Objekt (Abschnitt Objekte)", ct.Name)
	}
	for _, r := range objects.Rows {
		f, _ := crud.ParseDate(r[3])
		t, _ := crud.ParseDate(r[4])
		data.Objects = append(data.Objects, map[string]any{"object_type": r[0], "object_id": r[1], "is_main": crud.AsBool(r[2]), "valid_from": f, "valid_to": t})
	}
	conds, err := m.db.Query(ctx, `SELECT COUNT(*) FROM contract__condition WHERE company_code = ? AND contract_id = ?`, c.CompanyCode, c.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if toInt(conds.Rows[0][0]) == 0 {
		return sdk.Response{}, crud.Invalid("Vertrag %s hat keine Konditionen", c.ID)
	}
	res, err := hook.Call(ctx, m.services, hookActivate, hook.PhaseCheck, data)
	if err != nil {
		return sdk.Response{}, err
	}
	if res.HasErrors() {
		return sdk.Response{}, res.Err()
	}
	if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET status = ?, changed_at = ?, changed_by = ? WHERE company_code = ? AND contract_id = ?`,
		statusActive, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), c.CompanyCode, c.ID); err != nil {
		return sdk.Response{}, err
	}
	m.pushEvent(ctx, c, "activate", statusActive)
	commit, _ := hook.Call(ctx, m.services, hookActivate, hook.PhaseCommit, data)
	msg := fmt.Sprintf("Vertrag %s ist aktiv", c.ID)
	if w := messagesText(commit.Messages); w != "" {
		msg += " – " + w
	}
	return sdk.Response{Payload: map[string]any{"id": in.ID, "status": statusActive, "message": msg}}, nil
}

// coverageGap: erster Tag in [from, to], den keine der Zeitscheiben abdeckt ("" = lückenlos).
func coverageGap(slices [][2]string, from, to string) string {
	covered := from
	for {
		advanced := false
		for _, s := range slices {
			if s[0] <= covered && s[1] >= covered {
				if s[1] >= to {
					return ""
				}
				covered, advanced = nextDay(s[1]), true
			}
		}
		if !advanced {
			return covered
		}
	}
}

func messagesText(ms []hook.Message) string {
	var out []string
	for _, msg := range ms {
		out = append(out, msg.Text)
	}
	return strings.Join(out, "; ")
}

// --- Kündigen ------------------------------------------------------------------------

func (m *Module) terminateAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			NoticeReceived string `json:"notice_received"`
			TerminatedBy   string `json:"terminated_by"`
			Reason         string `json:"termination_reason"`
			ValidTo        string `json:"valid_to"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	c, err := m.contractOfID(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, "Contract", "terminate", c.CompanyCode); err != nil {
		return sdk.Response{}, err
	}
	if c.Status != statusActive {
		return sdk.Response{}, crud.Invalid("Nur aktive Verträge lassen sich kündigen (Status %s)", c.Status)
	}
	received, err := crud.ParseDate(in.Data.NoticeReceived)
	if err != nil || in.Data.NoticeReceived == "" {
		return sdk.Response{}, crud.Invalid("Eingang der Kündigung ist Pflicht")
	}
	by := strings.ToUpper(strings.TrimSpace(in.Data.TerminatedBy))
	if by != "PARTNER" && by != "US" && by != "MUTUAL" {
		return sdk.Response{}, crud.Invalid("Gekündigt von: Vertragspartner, wir oder einvernehmlich")
	}
	earliest, err := m.earliestEnd(ctx, c, received)
	if err != nil {
		return sdk.Response{}, err
	}
	end := earliest
	if strings.TrimSpace(in.Data.ValidTo) != "" {
		if end, err = crud.ParseDate(in.Data.ValidTo); err != nil {
			return sdk.Response{}, err
		}
	}
	switch {
	case end < c.ValidFrom:
		return sdk.Response{}, crud.Invalid("Ende %s liegt vor dem Vertragsbeginn %s", end, c.ValidFrom)
	case end > c.ValidTo:
		return sdk.Response{}, crud.Invalid("Ende %s liegt nach dem bisherigen Vertragsende %s", end, c.ValidTo)
	case end < earliest && by != "MUTUAL":
		return sdk.Response{}, crud.Invalid("Frühestes Ende nach den Kündigungsregeln: %s (bei Aufhebung „einvernehmlich“ wählen)", earliest)
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := m.db.Exec(ctx, `UPDATE contract__contract SET status = ?, valid_to = ?, notice_received = ?, terminated_by = ?, termination_reason = ?,
			changed_at = ?, changed_by = ? WHERE company_code = ? AND contract_id = ?`, statusTerminated, end, received, by,
			nilIfEmpty(strings.TrimSpace(in.Data.Reason)), now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), c.CompanyCode, c.ID); err != nil {
			return err
		}
		// Zeitscheiben über das neue Ende hinaus enden mit dem Vertrag.
		for _, t := range []string{"contract__partner", "contract__object", "contract__condition", "contract__notice_term"} {
			if _, err := m.db.Exec(ctx, "UPDATE "+t+" SET valid_to = ? WHERE company_code = ? AND contract_id = ? AND valid_to > ? AND valid_from <= ?",
				end, c.CompanyCode, c.ID, end, end); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	c.ValidTo = end
	m.pushEvent(ctx, c, "terminate", statusTerminated)
	return sdk.Response{Payload: map[string]any{"id": in.ID, "status": statusTerminated, "valid_to": end,
		"message": fmt.Sprintf("Vertrag %s gekündigt zum %s (frühestens möglich: %s)", c.ID, end, earliest)}}, nil
}

// earliestEnd: frühestes Vertragsende bei Eingang der Kündigung am received –
// Monatsende nach der Kündigungsfrist (Eingang nach dem Stichtag des Monats
// zählt ab dem Folgemonat), nicht vor Ablauf der Mindestlaufzeit.
func (m *Module) earliestEnd(ctx context.Context, c *contractRow, received string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT notice_period_months, notice_deadline_day, minimum_duration_months FROM contract__notice_term
		WHERE company_code = ? AND contract_id = ? AND valid_from <= ? AND valid_to >= ?`, c.CompanyCode, c.ID, received, received)
	if err != nil {
		return "", err
	}
	if len(res.Rows) == 0 {
		return received, nil // ohne Regeln: jederzeit
	}
	period, deadline, minimum := int(toInt(res.Rows[0][0])), int(toInt(res.Rows[0][1])), int(toInt(res.Rows[0][2]))
	r, _ := time.Parse(time.DateOnly, received)
	months := period
	if deadline > 0 && r.Day() > deadline {
		months++
	}
	end := time.Date(r.Year(), r.Month()+time.Month(months)+1, 0, 0, 0, 0, 0, time.UTC) // letzter Tag des Monats
	if minimum > 0 {
		start, _ := time.Parse(time.DateOnly, c.ValidFrom)
		if minEnd := start.AddDate(0, minimum, -1); minEnd.After(end) {
			end = minEnd
		}
	}
	return end.Format(time.DateOnly), nil
}

func nextDay(d string) string {
	t, err := time.Parse(time.DateOnly, d)
	if err != nil {
		return d
	}
	return t.AddDate(0, 0, 1).Format(time.DateOnly)
}

// pushEvent meldet Statuswechsel (eigene Actions laufen an crud vorbei).
func (m *Module) pushEvent(ctx context.Context, c *contractRow, action, status string) {
	err := events.Push(ctx, m.services, events.Event{Object: "Contract", Action: action, CompanyCode: c.CompanyCode,
		EntityID: c.CompanyCode + "|" + c.ID, Source: Name,
		Data: map[string]any{"id": c.CompanyCode + "|" + c.ID, "contract_type": c.Type, "direction": c.Direction, "status": status,
			"partner_id": c.Partner, "valid_from": c.ValidFrom, "valid_to": c.ValidTo}})
	if err != nil {
		m.log.WarnContext(ctx, "SystemEvent nicht gemeldet", "contract", c.ID, "err", err.Error())
	}
}

// --- Kündigungsregeln ----------------------------------------------------------------

func (m *Module) noticeTerm() *crud.Entity {
	return &crud.Entity{
		Object: "ContractNoticeTerm", Title: "Kündigungsregeln", Icon: "icon-calendar", Table: "contract__notice_term", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "valid_from"}, TimeSlice: true, Order: "company_code, contract_id, valid_from",
		Filters: []string{"company_code", "contract_id"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"contract_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "notice_period_months", Label: "Kündigungsfrist (Monate)", Type: tNum, Required: true, Listable: true},
			crud.Field{Key: "notice_deadline_day", Label: "Eingang bis zum … Tag des Monats (0 = egal)", Type: tNum, Listable: true},
			crud.Field{Key: "minimum_duration_months", Label: "Mindestlaufzeit (Monate)", Type: tNum, Listable: true},
			crud.Field{Key: "has_renewal_option", Label: "Verlängerungsoption", Type: tBool, Listable: true},
			crud.Field{Key: "renewal_months", Label: "Verlängerung um (Monate)", Type: tNum},
		),
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Contract", action, crud.Str(rec["company_code"]))
		},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
			if err != nil {
				return err
			}
			for k, def := range map[string]int64{"notice_period_months": 3, "notice_deadline_day": 3, "minimum_duration_months": 0, "renewal_months": 0} {
				if rec[k] == nil || crud.Str(rec[k]) == "" {
					rec[k] = def
				}
				if toInt(rec[k]) < 0 {
					return crud.Invalid("%s darf nicht negativ sein", k)
				}
			}
			if rec["has_renewal_option"] == nil {
				rec["has_renewal_option"] = false
			}
			if d := toInt(rec["notice_deadline_day"]); d > 31 {
				return crud.Invalid("Stichtag 0 bis 31")
			}
			return within("Kündigungsregel", crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"]), c)
		},
	}
}

// contractLookup: Auswahl eines Vertrags im Buchungskreis.
var contractLookup = &metamodel.Lookup{Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"},
	Filters: map[string]string{"company_code": "company_code"}}

// requireAccounts: Vertragspartner in der Rolle der Vertragsart und
// abweichende Zahler der Konditionen haben ein Partnerkonto im Buchungskreis.
func (m *Module) requireAccounts(ctx context.Context, c *contractRow, ct *typeRow) error {
	res, err := m.db.Query(ctx, `SELECT DISTINCT partner_id, 'Vertragspartner' FROM contract__partner
		WHERE company_code = ? AND contract_id = ? AND role_code = ?
		UNION SELECT DISTINCT payer_id, 'Zahler' FROM contract__condition
		WHERE company_code = ? AND contract_id = ? AND payer_id IS NOT NULL AND payer_id <> ''`,
		c.CompanyCode, c.ID, ct.MainRole, c.CompanyCode, c.ID)
	if err != nil {
		return err
	}
	for _, r := range res.Rows {
		if err := m.requirePartnerAccount(ctx, c.CompanyCode, crud.Str(r[0]), ct.MainRole, crud.Str(r[1])); err != nil {
			return err
		}
	}
	return nil
}
