package dms

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Probelauf (Workflow → „Probelauf …“): Der Workflow läuft für Dokumente im
// Eingang von vorn, in einer Transaktion, die danach zurückgerollt wird –
// nichts bleibt geändert. Benutzerschritte übernehmen den Vorschlag bzw. den
// bisherigen Wert; ohne ihn endet der Lauf dort („wartet“). Die KI wird nicht
// gefragt (nur gespeicherte Antworten), Aktionen mit „Ändert Daten“ und Befehle,
// die die Datei ersetzen, laufen nicht.

type dryKey struct{}

// dryRun sammelt die Schritte eines Probelaufs.
type dryRun struct {
	lines [][]any
	doc   string
	n     int
	stop  string
}

func dryRunOf(ctx context.Context) *dryRun {
	d, _ := ctx.Value(dryKey{}).(*dryRun)
	return d
}

func (dr *dryRun) add(s *stepDef, outcome, value, info string) {
	dr.lines = append(dr.lines, []any{dr.doc, s.Step, s.Name, optionLabel(outcomeOptions, outcome), value, info})
}

// userStep: Benutzerschritt im Probelauf – Vorschlag bzw. bisheriger Wert, sonst Ende.
func (dr *dryRun) userStep(ctx context.Context, m *Module, d *docRow, s *stepDef) error {
	if dr.n++; dr.n > maxSteps {
		dr.stop = "Abbruch nach zu vielen Schritten"
		return nil
	}
	wait := func(why string) error {
		dr.stop = fmt.Sprintf("Schritt %s %s wartet: %s", s.Step, s.Name, why)
		dr.lines = append(dr.lines, []any{dr.doc, s.Step, s.Name, "wartet", "", why})
		return nil
	}
	key := s.targetKey()
	value, label, info := "", "", ""
	switch {
	case s.Kind == kForm:
		return wait("Maske – würde anlegen")
	case s.Kind == kInput && s.InputType == inYesNo:
		return wait("Ja/Nein")
	}
	if p := m.proposalFor(ctx, d, key); p != nil {
		value, label, info = p.Value, p.Label, "Probelauf: "+p.text()
	} else if v := m.targetValue(ctx, d, key); v != "" {
		value, info = v, "Probelauf: bisheriger Wert"
	} else if s.Kind == kInput && s.DefaultValue != "" {
		value, info = m.expand(ctx, d, s.DefaultValue), "Probelauf: Vorgabe"
	}
	if value == "" {
		return wait("kein Vorschlag")
	}
	if s.Kind == kSelect {
		if cs, err := m.choices(ctx, d, s, ""); err == nil && s.SourceKind != srcTree && !containsChoice(cs, value) {
			return wait(fmt.Sprintf("Vorschlag %q steht nicht zur Auswahl", value))
		}
	}
	if s.Target != "" {
		v, err := m.setTargetValue(ctx, d, key, value, label)
		if err != nil {
			return wait(err.Error())
		}
		value = v
	}
	if err := m.advanceTrail(ctx, d, s, positive(s), value, info, true); err != nil {
		return err
	}
	return m.runWorkflow(ctx, d.ID)
}

var errDryRun = errors.New("Probelauf")

// dryRunAction: Probelauf für ein Dokument bzw. die Dokumente im Eingang.
func (m *Module) dryRunAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string `json:"id"`
		Data struct {
			Document string `json:"dry_document"`
		} `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, workflowObject, "update", ""); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.startStep(ctx, in.ID); err != nil {
		return sdk.Response{}, err
	}
	var ids []string
	if id := strings.TrimSpace(in.Data.Document); id != "" {
		ids = []string{id}
	} else {
		res, err := m.db.Query(ctx, `SELECT id FROM dms__document WHERE status = ? AND wf_code = ? ORDER BY created_at LIMIT 20`, stInbox, in.ID)
		if err != nil {
			return sdk.Response{}, err
		}
		for _, r := range res.Rows {
			ids = append(ids, crud.Str(r[0]))
		}
	}
	if len(ids) == 0 {
		return sdk.Response{}, crud.Invalid("Keine Dokumente im Eingang mit Workflow %s – Dokument angeben", in.ID)
	}
	var rows [][]any
	var summary []string
	for _, id := range ids {
		lines, stop, err := m.dryRunDoc(ctx, in.ID, id)
		if err != nil {
			rows = append(rows, []any{id, "", "", "Fehler", "", err.Error()})
			summary = append(summary, id+": Fehler")
			continue
		}
		rows = append(rows, lines...)
		summary = append(summary, id+": "+firstOf(stop, "bis zum Ende"))
	}
	return sdk.Response{Payload: map[string]any{
		"message": fmt.Sprintf("Probelauf %s, %d Dokumente (nichts geändert) – %s", in.ID, len(ids), strings.Join(summary, "; ")),
		"table":   map[string]any{"columns": []string{"Dokument", "Schritt", "Bezeichnung", "Ergebnis", "Wert", "Hinweis"}, "rows": rows}}}, nil
}

// dryRunDoc: Probelauf eines Dokuments – die Transaktion wird zurückgerollt.
func (m *Module) dryRunDoc(ctx context.Context, wf, id string) ([][]any, string, error) {
	d, err := m.writableDoc(ctx, map[string]any{"id": id}, "update")
	if err != nil {
		return nil, "", err
	}
	if d.Status != stInbox {
		return nil, "", crud.Invalid("Dokument %s ist nicht im Eingang", id)
	}
	dr := &dryRun{doc: id}
	ctx = context.WithValue(ctx, dryKey{}, dr)
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if err := m.resetToInbox(ctx, d, wf); err != nil {
			return err
		}
		if err := m.startWorkflow(ctx, d.ID, wf); err != nil {
			return err
		}
		if dr.stop == "" {
			if e, err := m.loadDoc(ctx, d.ID); err == nil && e.WfStatus == wfFailed {
				dr.stop = "Workflow angehalten bei Schritt " + e.WfStep
			}
		}
		return errDryRun
	})
	if err != nil && !errors.Is(err, errDryRun) {
		return dr.lines, dr.stop, err
	}
	return dr.lines, dr.stop, nil
}
