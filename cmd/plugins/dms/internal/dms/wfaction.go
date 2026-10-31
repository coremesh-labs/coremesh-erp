package dms

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Aktion (Schrittart ACTION): ruft Object.Action mit den Zuordnungen „Parameter“
// (Feld = Wert mit Platzhaltern; leere entfallen) und übernimmt Felder der
// Antwort (Zuordnungen „Ergebnis“, Feld der Antwort, auch a.b → Ziel). Aktionen
// des Dokuments selbst laufen hier: DmsDocument.file (Ablegen), .takeover
// (Partner, Titel, Ablage aus dem kaufmännischen Objekt), .recognize (Text erkennen).
// „Ändert Daten“: im Probelauf nicht ausgeführt.

// docActions: Aktionen des Dokuments, die ein Schritt aufrufen kann.
var docActions = map[string]string{"file": "Ablegen", "takeover": "Übernehmen aus dem kaufmännischen Objekt", "recognize": "Text erkennen",
	"discard": "Endgültig verwerfen (seit 0.14.0, z. B. nach „Duplikat?“)",
	"forward": "Weiterleiten (seit 0.27.0, Parameter forward_target, z. B. {var.forward} aus den Stichworten)"}

func (m *Module) runActionStep(ctx context.Context, d *docRow, s *stepDef) (string, string, string) {
	if s.ChangesData && dryRunOf(ctx) != nil {
		return oSkipped, "", "Probelauf: " + s.ActionObject + "." + s.ActionName + " ändert Daten – nicht ausgeführt"
	}
	params := map[string]any{}
	for _, p := range s.mappings(mapParam, m.categoryOf(ctx, d)) {
		if v := strings.TrimSpace(m.expand(ctx, d, p.Value)); p.Field != "" && v != "" {
			params[p.Field] = v
		}
	}
	var out map[string]any
	if s.ActionObject == docObject {
		res, err := m.docAction(ctx, d, s.ActionName, params)
		if err != nil {
			return oFailed, "", err.Error()
		}
		out = res
	} else {
		resp, err := m.services.Call(ctx, s.ActionObject, s.ActionName, params)
		if err != nil {
			return oFailed, "", fmt.Sprintf("%s.%s: %v", s.ActionObject, s.ActionName, err)
		}
		if err := sdk.Decode(resp.Payload, &out); err != nil {
			return oFailed, "", err.Error()
		}
	}
	var value string
	for _, p := range s.mappings(mapResult, m.categoryOf(ctx, d)) {
		v := strings.TrimSpace(crud.Str(pathValue(out, p.Field)))
		if v == "" {
			return oFailed, "", fmt.Sprintf("%s.%s: Antwort ohne %s", s.ActionObject, s.ActionName, p.Field)
		}
		set, err := m.applyResult(ctx, d, s, p.Target, &proposalRec{Value: v, Source: s.ActionObject + "." + s.ActionName + ", Schritt " + s.Step})
		if err != nil {
			return oFailed, "", err.Error()
		}
		value = firstOf(value, set, v)
	}
	return oSuccess, value, strings.TrimSpace(crud.Str(out["message"]))
}

// docAction: Aktion des Dokuments im Workflow.
func (m *Module) docAction(ctx context.Context, d *docRow, name string, params map[string]any) (map[string]any, error) {
	switch name {
	case "file":
		n, warnings, err := m.fileDocument(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		msg := "abgelegt als " + n
		if len(warnings) > 0 {
			msg += " – " + strings.Join(warnings, "; ")
		}
		return map[string]any{"name": n, "message": msg}, nil
	case "takeover":
		note, err := m.takeover(ctx, d)
		if err != nil {
			return nil, err
		}
		return map[string]any{"message": note}, nil
	case "discard":
		resp, err := m.discardAction(ctx, sdk.Request{Payload: map[string]any{"id": d.ID}})
		if err != nil {
			return nil, err
		}
		out, _ := resp.Payload.(map[string]any)
		return out, nil
	case "forward":
		resp, err := m.forwardAction(ctx, sdk.Request{Payload: map[string]any{"id": d.ID, "data": map[string]any{"forward_target": params["forward_target"]}}})
		if err != nil {
			return nil, err
		}
		out, _ := resp.Payload.(map[string]any)
		return out, nil
	case "recognize":
		resp, err := m.recognizeAction(ctx, sdk.Request{Payload: map[string]any{"id": d.ID}})
		if err != nil {
			return nil, err
		}
		out, _ := resp.Payload.(map[string]any)
		return out, nil
	}
	return nil, crud.Invalid("Aktion %s.%s gibt es im Workflow nicht (%s)", docObject, name, strings.Join(sortedKeys(func() map[string]bool {
		out := map[string]bool{}
		for k := range docActions {
			out[k] = true
		}
		return out
	}()), ", "))
}

// pathValue: Feld a.b der Antwort.
func pathValue(v map[string]any, path string) any {
	var cur any = v
	for _, k := range strings.Split(path, ".") {
		mp, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mp[k]
	}
	return cur
}
