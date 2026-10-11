package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Schritte neu nummerieren (Aktion am Workflow): in der bisherigen Reihenfolge
// 010, 020, 030 … (ab 100 Schritten vierstellig). Mit umgestellt werden
// Startschritt, Parameter und Optionen (Folgeschritte, undo_to) und Dokumente im
// Workflow (aktueller Schritt, Verlauf für „Zurück“, gemerkte KI-Ergebnisse).

func (m *Module) renumberWorkflowAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	if err := requireWrite(ctx, workflowObject, "update", ""); err != nil {
		return sdk.Response{}, err
	}
	n, err := m.renumberWorkflow(ctx, in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Workflow %s: %d Schritte neu nummeriert (10er-Schritte)", in.ID, n)}}, nil
}

// renumberMap: alte → neue Schrittnummern (Reihenfolge der bisherigen Nummern).
func renumberMap(steps []string) map[string]string {
	sorted := append([]string(nil), steps...)
	sort.Strings(sorted)
	width := 3
	if len(sorted)*10 > 999 {
		width = 4
	}
	out := map[string]string{}
	for i, s := range sorted {
		out[s] = fmt.Sprintf("%0*d", width, (i+1)*10)
	}
	return out
}

func (m *Module) renumberWorkflow(ctx context.Context, wf string) (int, error) {
	res, err := m.db.Query(ctx, `SELECT step FROM dms__step WHERE workflow = ?`, wf)
	if err != nil {
		return 0, err
	}
	if len(res.Rows) == 0 {
		return 0, crud.Invalid("Workflow %s hat keine Schritte", wf)
	}
	var steps []string
	for _, r := range res.Rows {
		steps = append(steps, crud.Str(r[0]))
	}
	mp := renumberMap(steps)
	newOf := func(s string) string {
		if n, ok := mp[s]; ok {
			return n
		}
		return s
	}
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		// zweistufig, damit keine neue Nummer mit einer alten zusammenstößt; der Schritt
		// wird kopiert, Parameter und Optionen (Fremdschlüssel) umgehängt, dann gelöscht
		for _, phase := range []func(old string) (string, string){
			func(old string) (string, string) { return old, "~" + newOf(old) },
			func(old string) (string, string) { return "~" + newOf(old), newOf(old) },
		} {
			for _, old := range steps {
				from, to := phase(old)
				if err := m.copyStep(ctx, wf, from, to); err != nil {
					return err
				}
				for _, q := range []string{
					`UPDATE dms__step_param SET step = ? WHERE workflow = ? AND step = ?`,
					`UPDATE dms__step_option SET step = ? WHERE workflow = ? AND step = ?`,
					`UPDATE dms__step_option SET next_step = ? WHERE workflow = ? AND next_step = ?`,
					`UPDATE dms__step_param SET value = ? WHERE workflow = ? AND param = 'undo_to' AND value = ?`,
					`UPDATE dms__workflow SET start_step = ? WHERE code = ? AND start_step = ?`,
					`UPDATE dms__document SET wf_step = ? WHERE wf_code = ? AND wf_step = ?`,
				} {
					if _, err := m.db.Exec(ctx, q, to, wf, from); err != nil {
						return err
					}
				}
				if _, err := m.db.Exec(ctx, `DELETE FROM dms__step WHERE workflow = ? AND step = ?`, wf, from); err != nil {
					return err
				}
			}
		}
		// Verlauf und KI-Ergebnisse der Dokumente im Workflow
		docs, err := m.db.Query(ctx, `SELECT id, wf_trail, wf_vars FROM dms__document WHERE wf_code = ?`, wf)
		if err != nil {
			return err
		}
		for _, r := range docs.Rows {
			var trail []trailEntry
			changed := false
			if s := crud.Str(r[1]); s != "" && json.Unmarshal([]byte(s), &trail) == nil {
				for i := range trail {
					if n := newOf(trail[i].Step); n != trail[i].Step {
						trail[i].Step, changed = n, true
					}
				}
			}
			vars := map[string]any{}
			if s := crud.Str(r[2]); s != "" && json.Unmarshal([]byte(s), &vars) == nil {
				for k, v := range vars {
					if step, ok := strings.CutPrefix(k, "ai."); ok && newOf(step) != step {
						delete(vars, k)
						vars["ai."+newOf(step)] = v
						changed = true
					}
				}
			}
			if !changed {
				continue
			}
			tb, _ := json.Marshal(trail)
			vb, _ := json.Marshal(vars)
			if _, err := m.db.Exec(ctx, `UPDATE dms__document SET wf_trail = ?, wf_vars = ? WHERE id = ?`, string(tb), string(vb), r[0]); err != nil {
				return err
			}
		}
		return nil
	})
	return len(steps), err
}

// copyStep legt den Schritt from als Schritt to an (alle Spalten).
func (m *Module) copyStep(ctx context.Context, wf, from, to string) error {
	res, err := m.db.Query(ctx, `SELECT * FROM dms__step WHERE workflow = ? AND step = ?`, wf, from)
	if err != nil {
		return err
	}
	if len(res.Rows) == 0 {
		return fmt.Errorf("Schritt %s fehlt", from)
	}
	vals := append([]any(nil), res.Rows[0]...)
	for i, c := range res.Columns {
		if c == "step" {
			vals[i] = to
		}
	}
	ph := strings.TrimSuffix(strings.Repeat("?, ", len(vals)), ", ")
	_, err = m.db.Exec(ctx, `INSERT INTO dms__step (`+strings.Join(res.Columns, ", ")+`) VALUES (`+ph+`)`, vals...)
	return err
}
