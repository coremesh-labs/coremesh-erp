package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Anbindung an die Immobilienverwaltung: Abonnent des Hooks realestate.partners
// (Phase modify). Die wirksamen Partner eines Objekts bekommen die Partner der
// Verträge (aktiv oder gekündigt, nicht im Entwurf), denen das Objekt am
// Stichtag zugeordnet ist – direkt oder über ein Vertragsobjekt, zu dem es
// gehört. So erscheint der Mieter am Mietobjekt, ohne dass realestate die
// Verträge kennt.

const (
	partnersHook     = "realestate.partners"
	partnersCallback = "ContractObjectPartners"
)

func registerHooks(r *module.Router, m *Module) {
	hook.Handle(r, partnersCallback, m.onObjectPartners)
}

func (m *Module) subscribeHooks(ctx context.Context) {
	if err := hook.Subscribe(ctx, m.services, hook.Subscription{Hook: partnersHook, Phase: hook.PhaseModify, Callback: partnersCallback,
		Priority: 50, Description: "Partner aus Verträgen (z. B. Mieter) an Mietobjekten, Gebäuden und Wirtschaftseinheiten"}); err != nil {
		m.log.WarnContext(ctx, "Hook nicht abonniert", "hook", partnersHook, "err", err.Error())
	}
}

// partnersData: Daten von realestate.partners (Partner bleiben unverändert erhalten).
type partnersData struct {
	CompanyCode string           `json:"company_code"`
	ObjectID    string           `json:"object_id"`
	Lineage     []string         `json:"lineage"`
	Date        string           `json:"date"`
	Partners    []map[string]any `json:"partners"`
}

func (m *Module) onObjectPartners(ctx context.Context, req hook.Request) (hook.Response, error) {
	var d partnersData
	if err := req.DecodeData(&d); err != nil {
		return hook.Response{}, err
	}
	if d.Partners == nil {
		d.Partners = []map[string]any{}
	}
	ids := []string{d.ObjectID}
	if len(d.Lineage) == 3 { // Mietobjekt: auch Vertragsobjekte, zu denen es gehört
		related, err := m.compositeRelated(ctx, d.CompanyCode, d.ObjectID, "", d.Date, d.Date)
		if err != nil {
			return hook.Response{}, err
		}
		ids = append(ids, related...)
	}
	for _, id := range ids {
		res, err := m.db.Query(ctx, `SELECT p.contract_id, p.role_code, p.partner_id, p.share, p.valid_from, p.valid_to, r.name
			FROM contract__object o
			JOIN contract__contract k ON k.company_code = o.company_code AND k.contract_id = o.contract_id
			JOIN contract__partner p ON p.company_code = k.company_code AND p.contract_id = k.contract_id
			LEFT JOIN contract__partner_role r ON r.company_code = p.company_code AND r.role_code = p.role_code
			WHERE o.company_code = ? AND o.object_id = ? AND k.status <> ?
			AND o.valid_from <= ? AND o.valid_to >= ? AND p.valid_from <= ? AND p.valid_to >= ?
			ORDER BY p.contract_id, p.role_code, p.partner_id`, d.CompanyCode, id, statusDraft, d.Date, d.Date, d.Date, d.Date)
		if err != nil {
			return hook.Response{}, err
		}
		for _, r := range res.Rows {
			f, _ := crud.ParseDate(r[4])
			t, _ := crud.ParseDate(r[5])
			p := map[string]any{"role_code": crud.Str(r[1]), "role_name": crud.Str(r[6]), "partner_id": crud.Str(r[2]),
				"partner_name": m.partnerName(ctx, crud.Str(r[2])), "from_object": id, "from_level": "CONTRACT",
				"valid_from": f, "valid_to": t, "contract_id": crud.Str(r[0])}
			if r[3] != nil {
				p["share"] = toFloat(r[3])
			}
			d.Partners = append(d.Partners, p)
		}
	}
	return hook.Reply(d), nil
}
