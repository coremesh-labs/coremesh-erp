package contract

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Vertragspartner -----------------------------------------------------------------

// contractPartner: Partner eines Vertrags in Rollen (Hauptmieter, Mitmieter,
// Bürge, Zahler …) mit Zeitscheibe. Auswählbar sind nur Partner, die die Rolle
// im Partnermodul haben.
func (m *Module) contractPartner() *crud.Entity {
	return &crud.Entity{
		Object: "ContractPartner", Title: "Vertragspartner", Icon: "icon-users", Table: "contract__partner", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "role_code", "partner_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, contract_id, role_code, valid_from",
		Filters: []string{"company_code", "contract_id", "role_code", "partner_id"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"contract_id", "role_code", "partner_id", "share", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "ContractPartnerRole", ValueField: "role_code", LabelFields: []string{"name"},
					Filters: map[string]string{"company_code": "company_code"}}},
			crud.Field{Key: "partner_id", Label: "Geschäftspartner", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"},
					Filters: map[string]string{"role": "role_code"}}},
			crud.Field{Key: "share", Label: "Anteil in %", Type: tNum, Listable: true},
		),
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Contract", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkContractPartner,
	}
}

func (m *Module) checkContractPartner(ctx context.Context, rec, _ crud.Record) error {
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	role, partner := trimUpper(rec["role_code"]), strings.TrimSpace(crud.Str(rec["partner_id"]))
	rec["role_code"], rec["partner_id"] = role, partner
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	if err := within("Partner", from, to, c); err != nil {
		return err
	}
	rs, err := m.roleSetting(ctx, cc, role)
	if err != nil {
		return err
	}
	if err := m.requirePartnerRole(ctx, partner, role, rs.Name, from, to); err != nil {
		return err
	}
	if ct, err := m.contractTypeOf(ctx, cc, c.Type); err != nil {
		return err
	} else if ct.AccountRequired && role == ct.MainRole {
		if err := m.requirePartnerAccount(ctx, cc, partner, role, "Vertragspartner"); err != nil {
			return err
		}
	}
	others, err := m.db.Query(ctx, `SELECT partner_id, valid_from, valid_to, share FROM contract__partner
		WHERE company_code = ? AND contract_id = ? AND role_code = ? AND valid_from <= ? AND valid_to >= ?
		AND NOT (partner_id = ? AND valid_from = ?)`, cc, id, role, to, from, partner, from)
	if err != nil {
		return err
	}
	if rs.Exclusive {
		for _, r := range others.Rows {
			if crud.Str(r[0]) != partner {
				f, _ := crud.ParseDate(r[1])
				t, _ := crud.ParseDate(r[2])
				return crud.Invalid("%s ist %s–%s bereits Partner %s – erst dort beenden", rs.Name, f, t, crud.Str(r[0]))
			}
		}
	}
	if !rs.Shares {
		rec["share"] = nil
		return nil
	}
	share := toFloat(rec["share"])
	if rec["share"] == nil || share <= 0 || share > 100 {
		return crud.Invalid("%s: Anteil in %% (größer 0, höchstens 100) ist Pflicht", rs.Name)
	}
	dates := []string{from}
	for _, r := range others.Rows {
		if d, _ := crud.ParseDate(r[1]); d > from {
			dates = append(dates, d)
		}
	}
	for _, d := range dates {
		sum := share
		for _, r := range others.Rows {
			f, _ := crud.ParseDate(r[1])
			t, _ := crud.ParseDate(r[2])
			if f <= d && t >= d {
				sum += toFloat(r[3])
			}
		}
		if sum > 100+1e-9 {
			return crud.Invalid("%s: Anteile ergeben am %s %s %% – mehr als 100 %%", rs.Name, d, formatNum(sum))
		}
	}
	return nil
}

// requirePartnerRole: Der Partner hat die Rolle im Partnermodul für den ganzen
// Zeitraum (Zeitscheiben dürfen aneinander anschließen).
func (m *Module) requirePartnerRole(ctx context.Context, partner, role, roleName, from, to string) error {
	if partner == "" {
		return crud.Invalid("Geschäftspartner ist Pflicht")
	}
	resp, err := m.services.Call(ctx, "PartnerRole", "list", map[string]any{"query": map[string]any{
		"bp_id": partner, "role_code": role, "includeHistory": "true"}})
	if err != nil {
		return unavailable("Partnermodul", err)
	}
	var out struct {
		Items []struct {
			ValidFrom string `json:"valid_from"`
			ValidTo   string `json:"valid_to"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return err
	}
	var ranges [][2]string
	for _, it := range out.Items {
		f, _ := crud.ParseDate(it.ValidFrom)
		t, _ := crud.ParseDate(it.ValidTo)
		ranges = append(ranges, [2]string{f, t})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })
	if gap := coverageGap(ranges, from, to); gap != "" {
		return crud.Invalid("Partner %s hat die Rolle %s im Partnermodul nicht ab %s (Vertragszeitraum %s–%s)", partner, roleName, gap, from, to)
	}
	return nil
}

// partnerName: Name 1 und 2 des Partners (heute gültige Zeitscheibe); leer,
// wenn das Partnermodul fehlt oder der Partner nicht lesbar ist.
func (m *Module) partnerName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	resp, err := m.services.Call(ctx, "BusinessPartner", "get", map[string]any{"id": id})
	if err != nil {
		return ""
	}
	var bp struct {
		Name1 string `json:"name1"`
		Name2 string `json:"name2"`
	}
	if sdk.Decode(resp.Payload, &bp) != nil {
		return ""
	}
	return strings.TrimSpace(bp.Name1 + " " + bp.Name2)
}

// --- Objekte -------------------------------------------------------------------------

// contractObject: Objekte des Vertrags (Mietobjekt, Gebäude, Wirtschaftseinheit)
// mit Zeitscheibe – z. B. Wohnung als Hauptobjekt und ein später angemieteter
// Stellplatz.
func (m *Module) contractObject() *crud.Entity {
	return &crud.Entity{
		Object: "ContractObject", Title: "Vertragsobjekte", Icon: "icon-door", Table: "contract__object", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "object_type", "object_id", "valid_from"}, TimeSlice: true,
		Order:   "company_code, contract_id, valid_from, object_id",
		Filters: []string{"company_code", "contract_id", "object_type", "object_id"},
		Events:  true, CompanyCodeField: "company_code", EventFields: []string{"contract_id", "object_type", "object_id", "is_main", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "object_type", Label: "Objektart", Type: tSel, Required: true, Listable: true, Immutable: true, Options: objectTypeOptions},
			crud.Field{Key: "object_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true,
				Lookup: &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"},
					Filters: map[string]string{"company_code": "company_code"}}},
			crud.Field{Key: "is_main", Label: "Hauptobjekt", Type: tBool, Listable: true},
		),
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		CheckRecord: func(ctx context.Context, action string, rec crud.Record) error {
			return requireWrite(ctx, "Contract", action, crud.Str(rec["company_code"]))
		},
		Validate: m.checkContractObject,
	}
}

func (m *Module) checkContractObject(ctx context.Context, rec, _ crud.Record) error {
	defaults(rec, map[string]any{"is_main": false})
	cc, id := crud.Str(rec["company_code"]), crud.Str(rec["contract_id"])
	typ, obj := crud.Str(rec["object_type"]), strings.TrimSpace(crud.Str(rec["object_id"]))
	rec["object_id"] = obj
	from, to := crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"])
	c, err := m.contractOf(ctx, cc, id)
	if err != nil {
		return err
	}
	if err := within("Objekt", from, to, c); err != nil {
		return err
	}
	ct, err := m.contractTypeOf(ctx, cc, c.Type)
	if err != nil {
		return err
	}
	if len(ct.ObjectTypes) > 0 && !slices.Contains(ct.ObjectTypes, typ) {
		return crud.Invalid("Vertragsart %s erlaubt nur %s", ct.Name, strings.Join(ct.ObjectTypes, ", "))
	}
	o, err := m.objectOf(ctx, cc, typ, obj)
	if err != nil {
		return err
	}
	if o.ValidFrom > from || o.ValidTo < to {
		return crud.Invalid("Objekt %s ist nur %s–%s gültig", obj, o.ValidFrom, o.ValidTo)
	}
	if crud.AsBool(rec["is_main"]) {
		res, err := m.db.Query(ctx, `SELECT object_id FROM contract__object WHERE company_code = ? AND contract_id = ? AND is_main = ?
			AND valid_from <= ? AND valid_to >= ? AND NOT (object_type = ? AND object_id = ? AND valid_from = ?)`,
			cc, id, true, to, from, typ, obj, from)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			return crud.Invalid("Hauptobjekt ist im Zeitraum bereits %s", crud.Str(res.Rows[0][0]))
		}
	}
	if ct.Exclusive {
		return m.checkVacancy(ctx, c, typ, obj, o.Kind, from, to)
	}
	return nil
}

// objectRow: Objekt aus der Immobilienverwaltung.
type objectRow struct {
	Designation, Kind, ValidFrom, ValidTo string
}

// objectOf liest das Objekt über <Objektart>.get (Existenz und Leserecht).
func (m *Module) objectOf(ctx context.Context, cc, typ, id string) (*objectRow, error) {
	resp, err := m.services.Call(ctx, typ, "get", map[string]any{"id": cc + "|" + id})
	if err != nil {
		if errors.Is(err, sdk.ErrNotFound) {
			return nil, crud.Invalid("%s %s gibt es im Buchungskreis %s nicht", typ, id, cc)
		}
		return nil, unavailable("Immobilienverwaltung", err)
	}
	var o struct {
		Designation string `json:"designation"`
		Kind        string `json:"kind"`
		ValidFrom   string `json:"valid_from"`
		ValidTo     string `json:"valid_to"`
	}
	if err := sdk.Decode(resp.Payload, &o); err != nil {
		return nil, err
	}
	out := &objectRow{Designation: o.Designation, Kind: o.Kind, ValidFrom: "0001-01-01", ValidTo: crud.DateMax}
	if o.ValidFrom != "" {
		out.ValidFrom, _ = crud.ParseDate(o.ValidFrom)
	}
	if o.ValidTo != "" {
		out.ValidTo, _ = crud.ParseDate(o.ValidTo)
	}
	return out, nil
}

// checkVacancy: Ein Mietobjekt gehört zu einem Zeitpunkt höchstens einem
// Vertrag exklusiver Vertragsarten – auch über Vertragsobjekte (Bestandteile)
// hinweg: ein vermietetes Vertragsobjekt sperrt seine Bestandteile und umgekehrt.
func (m *Module) checkVacancy(ctx context.Context, c *contractRow, typ, obj, kind, from, to string) error {
	ids := []string{obj}
	if typ == "RentObject" {
		related, err := m.compositeRelated(ctx, c.CompanyCode, obj, kind, from, to)
		if err != nil {
			return err
		}
		ids = append(ids, related...)
	}
	for _, id := range ids {
		res, err := m.db.Query(ctx, `SELECT o.contract_id, o.valid_from, o.valid_to FROM contract__object o
			JOIN contract__contract k ON k.company_code = o.company_code AND k.contract_id = o.contract_id
			JOIN contract__contract_type t ON t.company_code = k.company_code AND t.code = k.contract_type
			WHERE o.company_code = ? AND o.object_type = ? AND o.object_id = ? AND o.contract_id <> ? AND t.exclusive_objects = ?
			AND o.valid_from <= ? AND o.valid_to >= ?`, c.CompanyCode, typ, id, c.ID, true, to, from)
		if err != nil {
			return err
		}
		if len(res.Rows) > 0 {
			f, _ := crud.ParseDate(res.Rows[0][1])
			t, _ := crud.ParseDate(res.Rows[0][2])
			via := ""
			if id != obj {
				via = " (über " + id + ")"
			}
			return crud.Invalid("%s ist %s–%s bereits im Vertrag %s vergeben%s", obj, f, t, crud.Str(res.Rows[0][0]), via)
		}
	}
	return nil
}

// compositeRelated: Bestandteile eines Vertragsobjekts bzw. die Vertragsobjekte,
// zu denen ein Objekt im Zeitraum gehört (Immobilienverwaltung, CompositeItem).
func (m *Module) compositeRelated(ctx context.Context, cc, obj, kind, from, to string) ([]string, error) {
	field, other := "object_id", "composite_id"
	if kind == "COMPOSITE" {
		field, other = "composite_id", "object_id"
	}
	resp, err := m.services.Call(ctx, "CompositeItem", "list", map[string]any{"query": map[string]any{
		"company_code": cc, field: obj, "includeHistory": "true"}})
	if err != nil {
		return nil, nil // ohne Immobilienverwaltung: nur das Objekt selbst
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &out); err != nil {
		return nil, err
	}
	var ids []string
	for _, it := range out.Items {
		f, _ := crud.ParseDate(it["valid_from"])
		t, _ := crud.ParseDate(it["valid_to"])
		if f <= to && t >= from {
			ids = append(ids, crud.Str(it[other]))
		}
	}
	return ids, nil
}
