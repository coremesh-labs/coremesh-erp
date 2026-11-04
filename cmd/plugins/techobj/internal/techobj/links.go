package techobj

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- Brücke zu Objekten anderer Module ------------------------------------------------

// Die Brücke verbindet einen Messpunkt, eine Lage oder ein technisches Objekt
// mit einem Objekt eines anderen Moduls in einer Rolle, mit Zeitscheibe. Welche
// Objekte verknüpfbar sind (Object des Dienstes) und welche Rollen es gibt,
// steht in den Katalogen; geprüft wird über <Object>.get des zuständigen Moduls –
// ohne Fremdschlüssel, das Modul kennt kein anderes fest.

var sourceKindOptions = []metamodel.Option{
	{Value: "POINT", Label: "Messpunkt"}, {Value: "LOCATION", Label: "Lage"}, {Value: "EQUIPMENT", Label: "Technisches Objekt"},
}

// sourceTables: Art der Quelle → Tabelle und Schlüsselspalte.
var sourceTables = map[string][2]string{
	"POINT":     {"techobj__point", "point_id"},
	"LOCATION":  {"techobj__location", "location_id"},
	"EQUIPMENT": {"techobj__equipment", "equipment_id"},
}

// targetLookup: Auswahl im Formular je verknüpfbarem Objekt der Vorschlagswerte;
// für selbst angelegte Einträge bleibt die Eingabe frei (geprüft wird trotzdem).
func targetLookup() *metamodel.Lookup {
	return &metamodel.Lookup{Object: "RentObject", ValueField: "object_id", LabelFields: []string{"designation"}, Filters: ccFilter,
		By: "object_type", Variants: map[string]metamodel.Lookup{
			"BUILDING": {Object: "Building", ValueField: "building_id", LabelFields: []string{"designation"}, Filters: ccFilter},
			"ENTITY":   {Object: "BusinessEntity", ValueField: "entity_id", LabelFields: []string{"designation"}, Filters: ccFilter},
			"ASSET":    {Object: "Asset", ValueField: "asset_id", LabelFields: []string{"designation"}, Filters: ccFilter},
			"CONTRACT": {Object: "Contract", ValueField: "contract_id", LabelFields: []string{"designation"}, Filters: ccFilter},
			"PARTNER":  {Object: "BusinessPartner", ValueField: "id", LabelFields: []string{"name1", "name2"}},
		}}
}

func sourceLookup() *metamodel.Lookup {
	return &metamodel.Lookup{Object: "MeterPoint", ValueField: "point_id", LabelFields: []string{"designation"}, Filters: ccFilter,
		By: "source_kind", Variants: map[string]metamodel.Lookup{
			"LOCATION":  *locationLookup,
			"EQUIPMENT": *equipmentLookup,
		}}
}

func (m *Module) link() *crud.Entity {
	return &crud.Entity{
		Object: "TechLink", Title: "Verknüpfungen", Icon: "icon-link", Table: "techobj__link", Section: "Bestand",
		Keys:  []string{"company_code", "source_kind", "source_id", "role_code", "object_type", "object_id", "valid_from"}, TimeSlice: true,
		Order: "company_code, source_kind, source_id, role_code, valid_from",
		Filters: []string{"company_code", "source_kind", "source_id", "role_code", "object_type", "object_id"},
		Events:  true, CompanyCodeField: "company_code",
		EventFields: []string{"source_kind", "source_id", "role_code", "object_type", "object_id", "valid_from", "valid_to"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Short: "BuKr", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "source_kind", Label: "Verknüpft wird", Short: "Quelle", Type: tSel, Required: true, Listable: true, Immutable: true, Options: sourceKindOptions},
			crud.Field{Key: "source_id", Label: "Messpunkt / Lage / technisches Objekt", Short: "Quelle-ID", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: sourceLookup()},
			crud.Field{Key: "role_code", Label: "Rolle", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechLinkRole")},
			crud.Field{Key: "object_type", Label: "Objekt (Art)", Short: "Art", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: catalogLookup("TechLinkObject")},
			crud.Field{Key: "object_id", Label: "Objekt", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: targetLookup()},
			crud.Field{Key: "note", Label: "Bemerkung (z. B. Kundennummer)", Short: "Bemerkung", Type: tText, Listable: true},
		),
		Access:      access(),
		CheckRecord: checkWrite("TechLink"),
		Validate:    m.checkLink,
		Decorate: func(ctx context.Context, rec crud.Record) error {
			if l := m.targetLabel(ctx, crud.Str(rec["company_code"]), crud.Str(rec["object_type"]), crud.Str(rec["object_id"])); l != "" {
				setLabel(rec, "object_id", l)
			}
			return nil
		},
	}
}

func (m *Module) checkLink(ctx context.Context, rec, _ crud.Record) error {
	cc, kind := crud.Str(rec["company_code"]), trimUpper(rec["source_kind"])
	src, obj := strings.TrimSpace(crud.Str(rec["source_id"])), strings.TrimSpace(crud.Str(rec["object_id"]))
	role, typ := trimUpper(rec["role_code"]), trimUpper(rec["object_type"])
	rec["source_kind"], rec["source_id"], rec["role_code"], rec["object_type"], rec["object_id"] = kind, src, role, typ, obj
	from, to := date(rec["valid_from"]), date(rec["valid_to"])
	t, ok := sourceTables[kind]
	if !ok {
		return crud.Invalid("Quelle %q: POINT, LOCATION oder EQUIPMENT", kind)
	}
	if v, err := m.one(ctx, "SELECT 1 FROM "+t[0]+" WHERE company_code = ? AND "+t[1]+" = ?", cc, src); err != nil {
		return err
	} else if v == "" {
		return crud.Invalid("%s gibt es im Buchungskreis %s nicht", src, cc)
	}
	res, err := m.db.Query(ctx, "SELECT name, COALESCE(object_types, ''), is_exclusive FROM techobj__link_role WHERE company_code = ? AND code = ? AND is_active = ?", cc, role, true)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return crud.Invalid("Rolle %s gibt es im Buchungskreis %s nicht (oder inaktiv) – Einstellungen → Rollen der Verknüpfung", role, cc)
	}
	roleName, allowed, exclusive := crud.Str(res.Rows[0][0]), splitCodes(crud.Str(res.Rows[0][1])), crud.AsBool(res.Rows[0][2])
	if len(allowed) > 0 && !slices.Contains(allowed, typ) {
		return crud.Invalid("Rolle %s erlaubt nur %s", roleName, strings.Join(allowed, ", "))
	}
	if _, err := m.target(ctx, cc, typ, obj); err != nil {
		return err
	}
	if exclusive {
		r, err := m.overlap(ctx, "techobj__link", "object_id", `company_code = ? AND source_kind = ? AND source_id = ? AND role_code = ?
			AND NOT (object_type = ? AND object_id = ? AND valid_from = ?)`, from, to, cc, kind, src, role, typ, obj, from)
		if err != nil {
			return err
		}
		if r != nil {
			return crud.Invalid("%s an %s ist %s–%s bereits %s – dort erst beenden (Wechsel = neue Zeitscheibe)", roleName, src,
				germanDate(date(r[1])), germanDate(date(r[2])), crud.Str(r[0]))
		}
	}
	return nil
}

// target liest das verknüpfte Objekt über <Object>.get des zuständigen Moduls.
func (m *Module) target(ctx context.Context, cc, typ, id string) (map[string]any, error) {
	res, err := m.db.Query(ctx, "SELECT name, service_object, company_scoped FROM techobj__link_object WHERE company_code = ? AND code = ? AND is_active = ?", cc, typ, true)
	if err != nil {
		return nil, err
	}
	if len(res.Rows) == 0 {
		return nil, crud.Invalid("Verknüpfbares Objekt %s gibt es im Buchungskreis %s nicht (oder inaktiv) – Einstellungen → Verknüpfbare Objekte", typ, cc)
	}
	name, object, scoped := crud.Str(res.Rows[0][0]), crud.Str(res.Rows[0][1]), crud.AsBool(res.Rows[0][2])
	key := id
	if scoped {
		key = cc + "|" + id
	}
	resp, err := m.services.Call(ctx, object, "get", map[string]any{"id": key})
	if err != nil {
		if errors.Is(err, sdk.ErrNotFound) {
			return nil, crud.Invalid("%s %s gibt es im Buchungskreis %s nicht", name, id, cc)
		}
		return nil, crud.Invalid("%s %s: %v", name, id, err)
	}
	var out map[string]any
	_ = sdk.Decode(resp.Payload, &out)
	return out, nil
}

// targetLabel: Bezeichnung des verknüpften Objekts (leer, wenn nicht lesbar).
func (m *Module) targetLabel(ctx context.Context, cc, typ, id string) string {
	o, err := m.target(ctx, cc, typ, id)
	if err != nil || o == nil {
		return ""
	}
	for _, k := range []string{"designation", "description"} {
		if s := strings.TrimSpace(crud.Str(o[k])); s != "" {
			return s
		}
	}
	return strings.TrimSpace(crud.Str(o["name1"]) + " " + crud.Str(o["name2"]))
}

// linkText: verknüpfte Objekte einer Rolle am Tag, z. B. „Mietobjekt 1000-01
// Whg. EG links“; beim Mietobjekt mit dem Mieter aus dem laufenden Vertrag.
func (m *Module) linkText(ctx context.Context, cc, kind, id, role, day string) (string, error) {
	res, err := m.db.Query(ctx, `SELECT l.object_type, l.object_id, COALESCE(o.name, l.object_type), COALESCE(o.service_object, ''),
		COALESCE(o.tenant_contract_types, '') FROM techobj__link l
		LEFT JOIN techobj__link_object o ON o.company_code = l.company_code AND o.code = l.object_type
		WHERE l.company_code = ? AND l.source_kind = ? AND l.source_id = ? AND l.role_code = ? AND l.valid_from <= ? AND l.valid_to >= ?
		ORDER BY l.object_type, l.object_id`, cc, kind, id, role, day, day)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, r := range res.Rows {
		typ, obj := crud.Str(r[0]), crud.Str(r[1])
		text := crud.Str(r[2]) + " " + obj
		if l := m.targetLabel(ctx, cc, typ, obj); l != "" {
			text += " " + l
		}
		if types := splitCodes(crud.Str(r[4])); len(types) > 0 {
			if t := m.tenantOf(ctx, cc, crud.Str(r[3]), obj, day, types); t != "" {
				text += " (" + t + ")"
			}
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; "), nil
}

// tenantOf: Vertrag und Partner des am Tag laufenden Vertrags einer der
// Vertragsarten types am Objekt (Vertragsmodul: ContractObject, Contract);
// leer, wenn es das Modul nicht gibt.
func (m *Module) tenantOf(ctx context.Context, cc, objectType, object, day string, types []string) string {
	resp, err := m.services.Call(ctx, "ContractObject", "list", map[string]any{"query": map[string]any{
		"company_code": cc, "object_type": objectType, "object_id": object, "includeHistory": "true"}})
	if err != nil {
		return ""
	}
	var l struct {
		Items []struct {
			Contract string `json:"contract_id"`
			From     string `json:"valid_from"`
			To       string `json:"valid_to"`
		} `json:"items"`
	}
	if sdk.Decode(resp.Payload, &l) != nil {
		return ""
	}
	for _, it := range l.Items {
		if date(it.From) > day || date(it.To) < day {
			continue
		}
		c, err := m.services.Call(ctx, "Contract", "get", map[string]any{"id": cc + "|" + it.Contract})
		if err != nil {
			continue
		}
		var k struct {
			Partner string `json:"partner_id"`
			Status  string `json:"status"`
			Type    string `json:"contract_type"`
		}
		if sdk.Decode(c.Payload, &k) != nil || k.Partner == "" || !slices.Contains(types, k.Type) {
			continue
		}
		return strings.TrimSpace(it.Contract + " " + m.partnerName(ctx, k.Partner))
	}
	return ""
}
