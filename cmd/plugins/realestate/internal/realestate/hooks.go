package realestate

import (
	"context"
	"fmt"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
	"github.com/coremesh-labs/coremesh/pkg/sdk/module"
)

// Anbindung an das Hauptbuch: Abonnent des Hooks ledger.posting (Phase check).
// Jede Position mit Kontierung rent_object_id muss auf ein Mietobjekt des
// Buchungskreises zeigen, das zum Buchungsdatum gültig ist.

const postingCheckCallback = "RentObjectPostingCheck"

// partnersHook: Erweiterungspunkt der wirksamen Partner (Phase modify), z. B. Mieter
// aus der Vertragsverwaltung.
const partnersHook = "realestate.partners"

func registerHooks(r *module.Router, m *Module) {
	hook.Handle(r, postingCheckCallback, m.onPostingCheck)
}

func (m *Module) subscribeHooks(ctx context.Context) {
	if err := hook.Define(ctx, m.services, hook.Definition{Name: partnersHook, Owner: Name, Phases: []string{hook.PhaseModify},
		Description: "Wirksame Partner eines Objekts: Abonnenten ergänzen die Liste, z. B. um Mieter aus Verträgen",
		Data:        "PartnersHookData: company_code, object_id, lineage[], date, partners[] (EffectivePartner)"}); err != nil {
		m.log.WarnContext(ctx, "Hook nicht angemeldet", "hook", partnersHook, "err", err.Error())
	}
	err := hook.Subscribe(ctx, m.services, hook.Subscription{Hook: "ledger.posting", Phase: hook.PhaseCheck,
		Callback: postingCheckCallback, Priority: 50, Description: "Mietobjekt der Kontierung prüfen (Buchungskreis, Gültigkeit)"})
	if err != nil {
		m.log.WarnContext(ctx, "Hook ledger.posting nicht abonniert", "err", err.Error())
	}
}

// postingData: der Teil der Hook-Daten von ledger.posting, den das Modul braucht.
type postingData struct {
	Request struct {
		CompanyCode string `json:"company_code"`
		PostingDate string `json:"posting_date"`
	} `json:"request"`
	Lines []struct {
		Line int               `json:"line"`
		Dims map[string]string `json:"dims"`
	} `json:"lines"`
}

func (m *Module) onPostingCheck(ctx context.Context, req hook.Request) (hook.Response, error) {
	var d postingData
	if err := req.DecodeData(&d); err != nil {
		return hook.Response{}, err
	}
	date, err := crud.ParseDate(d.Request.PostingDate)
	if err != nil {
		date = crud.KeyDate(ctx)
	}
	var msgs []hook.Message
	for _, l := range d.Lines {
		id := l.Dims["rent_object_id"]
		if id == "" {
			continue
		}
		res, err := m.db.Query(ctx, `SELECT valid_from, valid_to FROM realestate__rent_object WHERE company_code = ? AND object_id = ?`,
			d.Request.CompanyCode, id)
		if err != nil {
			return hook.Response{}, err
		}
		if len(res.Rows) == 0 {
			msgs = append(msgs, hook.Error("RE-001", fmt.Sprintf("Position %d: Mietobjekt %s gibt es im Buchungskreis %s nicht", l.Line, id, d.Request.CompanyCode)).OnField("rent_object_id"))
			continue
		}
		from, _ := crud.ParseDate(res.Rows[0][0])
		to, _ := crud.ParseDate(res.Rows[0][1])
		if date < from || date > to {
			msgs = append(msgs, hook.Error("RE-002", fmt.Sprintf("Position %d: Mietobjekt %s ist am %s nicht gültig (%s–%s)", l.Line, id, date, from, to)).OnField("rent_object_id"))
		}
	}
	return hook.Reply(nil, msgs...), nil
}
