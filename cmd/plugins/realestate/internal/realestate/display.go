package realestate

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Standard-Darstellungsregeln der Mietobjekt-Maske (iam, Administration →
// Darstellung): Alle Arten teilen sich die Maske RentObject; je Art blenden
// Regeln die Felder und Abschnitte aus, die dort keinen Sinn haben. setup-company
// legt fehlende Regeln an (Name als Kennung); danach gehören sie dem
// Administrator – geänderte oder inaktivierte Regeln bleiben unangetastet.
//
// Die Regeln gelten für alle Buchungskreise (Darstellung ist nicht je
// Buchungskreis). Die Prüfung leert unpassende Felder ohnehin (kindFields).

type kindRule struct {
	kind, name       string
	hidden, sections []string
}

var kindRules = []kindRule{
	{kindUnit, "Mietobjekt: Mieteinheit", []string{"pool_id", "area_type", "total_area"}, []string{"flaechen", "bestandteile"}},
	{kindSpace, "Mietobjekt: Fläche", []string{"area_type", "total_area"}, []string{"flaechen", "bestandteile"}},
	{kindPool, "Mietobjekt: Pool", []string{"floor", "location", "pool_id"}, []string{"bestandteile"}},
	{kindComposite, "Mietobjekt: Vertragsobjekt", []string{"floor", "location", "pool_id", "area_type", "total_area"}, []string{"flaechen"}},
}

// setupDisplayRules legt die fehlenden Standardregeln an. Fehler (z. B. ohne
// Recht auf die Administration) liefern einen Hinweis statt abzubrechen.
func (m *Module) setupDisplayRules(ctx context.Context) (int, string) {
	resp, err := m.services.Call(ctx, "DisplayRule", "list", map[string]any{"query": map[string]any{"object": "RentObject", "includeHistory": "true"}})
	if err != nil {
		return 0, "Darstellungsregeln nicht angelegt: " + err.Error()
	}
	var have struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := sdk.Decode(resp.Payload, &have); err != nil {
		return 0, "Darstellungsregeln nicht angelegt: " + err.Error()
	}
	exists := map[string]bool{}
	for _, it := range have.Items {
		exists[it.Name] = true
	}
	n := 0
	for _, kr := range kindRules {
		if exists[kr.name] {
			continue
		}
		if err := m.createKindRule(ctx, kr); err != nil {
			return n, "Darstellungsregel „" + kr.name + "“ nicht angelegt: " + err.Error()
		}
		n++
	}
	return n, ""
}

func (m *Module) createKindRule(ctx context.Context, kr kindRule) error {
	resp, err := m.services.Call(ctx, "DisplayRule", "create", map[string]any{"data": map[string]any{
		"name": kr.name, "object": "RentObject", "roles": ""}})
	if err != nil {
		return err
	}
	var rule struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(resp.Payload, &rule); err != nil {
		return err
	}
	add := func(object string, data map[string]any) error {
		data["rule_id"] = rule.ID
		_, err := m.services.Call(ctx, object, "create", map[string]any{"data": data})
		return err
	}
	if err := add("DisplayRuleCondition", map[string]any{"field": "kind", "field_values": kr.kind}); err != nil {
		return err
	}
	for _, f := range kr.hidden {
		if err := add("DisplayRuleField", map[string]any{"field": f, "mode": "hidden"}); err != nil {
			return err
		}
	}
	for _, s := range kr.sections {
		if err := add("DisplayRuleField", map[string]any{"field": s, "mode": "section"}); err != nil {
			return err
		}
	}
	return nil
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
