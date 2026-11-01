package process

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/events"
)

// Zeitgeber: prüft Fristen und Wiedervorlagen (check_minutes) und meldet das
// Ereignisjournal nach dem Commit an den Event-Dispatcher (push_seconds) –
// als Object ProcessEvent, Action = Ereignis (z. B. OVERDUE), EntityID
// „Buchungskreis|Vorgang“, Data mit seq und den Daten des Ereignisses.
// check_minutes < 0 schaltet den Zeitgeber ab (Tests).

func (m *Module) startTimer() {
	if m.cfg.CheckMinutes < 0 {
		return
	}
	check := time.Duration(max(m.cfg.CheckMinutes, 0)) * time.Minute
	if check == 0 {
		check = 60 * time.Minute
	}
	push := time.Duration(max(m.cfg.PushSeconds, 0)) * time.Second
	if push == 0 {
		push = 15 * time.Second
	}
	m.stop = make(chan struct{})
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		first := time.NewTimer(30 * time.Second) // erster Lauf kurz nach dem Start
		checkT, pushT := time.NewTicker(check), time.NewTicker(push)
		defer first.Stop()
		defer checkT.Stop()
		defer pushT.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-first.C:
				m.runCheck()
			case <-checkT.C:
				m.runCheck()
			case <-pushT.C:
				if _, err := m.pushEvents(context.Background()); err != nil {
					m.log.Warn("Ereignisjournal nicht gemeldet", "error", err)
				}
			}
		}
	}()
}

func (m *Module) runCheck() {
	ctx := context.Background()
	if _, err := m.checkDue(ctx); err != nil {
		m.log.Warn("Fristenprüfung fehlgeschlagen", "error", err)
	}
	if _, err := m.pushEvents(ctx); err != nil {
		m.log.Warn("Ereignisjournal nicht gemeldet", "error", err)
	}
}

// dueResult: Ergebnis einer Fristenprüfung.
type dueResult struct {
	Due, Overdue, Escalated, Idle int
}

// checkDue: heute fällig (Stufe 1), überschritten (2), eskaliert (3, nach
// escalate_days der Art an „Eskalation an“ bzw. den Vertreter), ohne Bewegung
// (idle_days der Art) – je Vorgang und Stufe einmal: Hinweis und Ereignis.
func (m *Module) checkDue(ctx context.Context) (dueResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out dueResult
	today := m.today()
	res, err := m.db.Query(ctx, `SELECT p.company_code, p.process_id, p.subject, p.assignee, p.deputy, p.next_due, p.next_step, p.escalation,
			p.last_activity, t.escalate_days, t.escalate_to, t.idle_days
		FROM process__process p
		JOIN process__status s ON s.company_code = p.company_code AND s.code = p.status
		JOIN process__type t ON t.company_code = p.company_code AND t.code = p.process_type
		WHERE s.kind <> ? ORDER BY p.company_code, p.process_id`, kindClosed)
	if err != nil {
		return out, err
	}
	for _, r := range res.Rows {
		cc, id, subject, assignee, deputy := crud.Str(r[0]), crud.Str(r[1]), crud.Str(r[2]), crud.Str(r[3]), crud.Str(r[4])
		due, step, level := dateStr(r[5]), crud.Str(r[6]), toInt(r[7])
		what := subject
		if step != "" {
			what += " – " + step
		}
		err := m.db.InTx(ctx, nil, func(ctx context.Context) error {
			newLevel := level
			switch {
			case due == "":
			case r[9] != nil && crud.Str(r[9]) != "" && due < addDays(today, -toInt(r[9])) && level < levelEscalate:
				newLevel = levelEscalate
				to := crud.Str(r[10])
				if to == "" {
					to = deputy
				}
				text := fmt.Sprintf("Eskaliert: %s (Frist %s, verantwortlich %s)", what, due, orDash(assignee))
				for _, u := range uniq(to, assignee) {
					if err := m.notify(ctx, cc, u, id, noticeEscalated, text); err != nil {
						return err
					}
				}
				out.Escalated++
				if err := m.timerEvent(ctx, cc, id, "ESCALATED", due, step, to); err != nil {
					return err
				}
			case due < today && level < levelOverdue:
				newLevel = levelOverdue
				if err := m.notify(ctx, cc, assignee, id, noticeOverdue, fmt.Sprintf("Frist überschritten (%s): %s", due, what)); err != nil {
					return err
				}
				out.Overdue++
				if err := m.timerEvent(ctx, cc, id, "OVERDUE", due, step, assignee); err != nil {
					return err
				}
			case due == today && level < levelDue:
				newLevel = levelDue
				if err := m.notify(ctx, cc, assignee, id, noticeDue, "Heute fällig: "+what); err != nil {
					return err
				}
				out.Due++
				if err := m.timerEvent(ctx, cc, id, "DUE", due, step, assignee); err != nil {
					return err
				}
			}
			if newLevel != level {
				if _, err := m.db.Exec(ctx, "UPDATE process__process SET escalation = ? WHERE company_code = ? AND process_id = ?", newLevel, cc, id); err != nil {
					return err
				}
			}
			if r[11] == nil || crud.Str(r[11]) == "" {
				return nil
			}
			last := crud.Str(r[8])
			if last != "" && last[:min(10, len(last))] >= addDays(today, -toInt(r[11])) {
				return nil
			}
			open, err := m.one(ctx, "SELECT 1 FROM process__notice WHERE company_code = ? AND process_id = ? AND kind = ? AND is_open = ?", cc, id, noticeIdle, true)
			if err != nil || open != nil {
				return err
			}
			if err := m.notify(ctx, cc, assignee, id, noticeIdle, fmt.Sprintf("Ohne Bewegung seit %d Tagen: %s", toInt(r[11]), subject)); err != nil {
				return err
			}
			out.Idle++
			return m.timerEvent(ctx, cc, id, "IDLE", due, step, assignee)
		})
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

func (m *Module) timerEvent(ctx context.Context, cc, id, event, due, step, user string) error {
	return m.journal(ctx, cc, id, event, map[string]any{"next_due": due, "next_step": step, "notified": user})
}

func uniq(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if v != "" && !strings.Contains(","+strings.Join(out, ",")+",", ","+v+",") {
			out = append(out, v)
		}
	}
	return out
}

// pushEvents: noch nicht gemeldete Einträge des Journals an den Event-Dispatcher.
func (m *Module) pushEvents(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	res, err := m.db.Query(ctx, `SELECT seq, company_code, process_id, event, user_id, data, occurred_at FROM process__event
		WHERE pushed_at IS NULL ORDER BY seq LIMIT 500`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res.Rows {
		data := map[string]any{"seq": toInt(r[0]), "event": crud.Str(r[3]), "occurred_at": crud.Str(r[6]), "user_id": crud.Str(r[4])}
		if raw := crud.Str(r[5]); raw != "" {
			var extra map[string]any
			if sdk.Decode([]byte(raw), &extra) == nil {
				for k, v := range extra {
					data[k] = v
				}
			}
		}
		cc := crud.Str(r[1])
		if err := events.Push(ctx, m.services, events.Event{Object: eventObject, Action: crud.Str(r[3]), CompanyCode: cc,
			EntityID: cc + "|" + crud.Str(r[2]), Source: Name, UserID: crud.Str(r[4]), Data: data}); err != nil {
			return n, err
		}
		if _, err := m.db.Exec(ctx, "UPDATE process__event SET pushed_at = ? WHERE seq = ?", m.stamp(), r[0]); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// checkDueAction: ProcessSetup.checkDue (Konsole process:check-due).
func (m *Module) checkDueAction(ctx context.Context, _ sdk.Request) (sdk.Response, error) {
	r, err := m.checkDue(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	pushed, err := m.pushEvents(ctx)
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"due": r.Due, "overdue": r.Overdue, "escalated": r.Escalated, "idle": r.Idle, "pushed": pushed,
		"message": fmt.Sprintf("%d heute fällig, %d überschritten, %d eskaliert, %d ohne Bewegung; %d Ereignisse gemeldet",
			r.Due, r.Overdue, r.Escalated, r.Idle, pushed)}}, nil
}
