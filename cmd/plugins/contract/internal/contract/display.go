package contract

import (
	"context"
	"slices"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// Standard-Darstellungsregeln der Vertragsmaske (seit 0.15.0; iam,
// Administration → Darstellung): Je Vertragsart blendet eine Regel die
// Abschnitte aus, die dort keinen Sinn haben – Darlehen und Sondertilgungen
// nur bei DA/DV, Abschreibung nur bei KV usw. setup-company legt fehlende Regeln
// an (Name als Kennung); danach gehören sie dem Administrator. Eigene
// Vertragsarten zeigen alle Abschnitte, bis man eine Regel für sie anlegt.

// sectionUse: optionaler Abschnitt → Vertragsarten, bei denen er erscheint.
var sectionUse = map[string][]string{
	"darlehen":          {"DA", "DV"},
	"sondertilgungen":   {"DA", "DV"},
	"abschreibung":      {"KV"},
	"bankkonto":         {"BK", "KK"},
	"personen":          {"MV"},
	"wirtschaftsplaene": {"WH", "HG"},
	"abrechnungen":      {"WH", "HG", "VE", "GS", "DL", "VS"},
}

// typeRules: je Standard-Vertragsart die auszublendenden Abschnitte.
func typeRules() map[string][]string {
	out := map[string][]string{}
	for _, t := range defaultTypes {
		var hide []string
		for sec, types := range sectionUse {
			if !slices.Contains(types, t.code) {
				hide = append(hide, sec)
			}
		}
		slices.Sort(hide)
		out[t.code] = hide
	}
	return out
}

// setupDisplayRules legt die fehlenden Regeln an; Fehler (z. B. ohne Recht
// auf die Administration) liefern einen Hinweis statt abzubrechen.
func (m *Module) setupDisplayRules(ctx context.Context) (int, string) {
	resp, err := m.services.Call(ctx, "DisplayRule", "list", map[string]any{"query": map[string]any{"object": "Contract", "includeHistory": "true"}})
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
	rules := typeRules()
	for _, t := range defaultTypes {
		name := "Vertrag: " + t.code + " " + t.name
		if exists[name] || len(rules[t.code]) == 0 {
			continue
		}
		if err := m.createTypeRule(ctx, name, t.code, rules[t.code]); err != nil {
			return n, "Darstellungsregel „" + name + "“ nicht angelegt: " + err.Error()
		}
		n++
	}
	return n, ""
}

func (m *Module) createTypeRule(ctx context.Context, name, code string, sections []string) error {
	resp, err := m.services.Call(ctx, "DisplayRule", "create", map[string]any{"data": map[string]any{"name": name, "object": "Contract", "roles": ""}})
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
	if err := add("DisplayRuleCondition", map[string]any{"field": "contract_type", "field_values": code}); err != nil {
		return err
	}
	for _, s := range sections {
		if err := add("DisplayRuleField", map[string]any{"field": s, "mode": "section"}); err != nil {
			return err
		}
	}
	return nil
}
