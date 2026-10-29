package procurement

import (
	"context"
	"fmt"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// --- „Auftrag erteilen …“ am Angebot -------------------------------------------------

// quoteLine: Hauptposition eines Angebots für die Auswahl.
type quoteLine struct {
	line                         int64
	pos, text, kind, kindName    string
	amount                       int64
	preselected, inTotal, hasAlt bool
	alternativeTo                int64
}

// quoteLines: Hauptpositionen (ohne nachgelagerte und abhängige Zeilen) mit Positionsart.
func (m *Module) quoteLines(ctx context.Context, cc, id string) ([]quoteLine, error) {
	res, err := m.db.Query(ctx, `SELECT i.line_no, i.supplier_pos, i.item_text, i.item_kind, k.name, i.amount, k.preselected, k.in_total, i.alternative_to
		FROM procurement__quote_item i LEFT JOIN procurement__item_kind k ON k.company_code = i.company_code AND k.code = i.item_kind
		WHERE i.company_code = ? AND i.quote_id = ? AND i.is_active = ? AND i.dependent_type IS NULL AND i.parent_line IS NULL ORDER BY i.line_no`, cc, id, true)
	if err != nil {
		return nil, err
	}
	out := make([]quoteLine, 0, len(res.Rows))
	for _, r := range res.Rows {
		l := quoteLine{line: toInt(r[0]), pos: crud.Str(r[1]), text: crud.Str(r[2]), kind: crud.Str(r[3]), kindName: crud.Str(r[4]), amount: toInt(r[5]),
			preselected: r[6] == nil || crud.AsBool(r[6]), inTotal: r[7] == nil || crud.AsBool(r[7]), alternativeTo: toInt(r[8])}
		out = append(out, l)
	}
	return out, nil
}

func (m *Module) orderFromQuoteFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	key, err := m.set.Entity(quoteObject).ParseID(req.ID)
	if err != nil {
		return st, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["quote_id"])
	q, err := m.db.Query(ctx, `SELECT supplier_id, currency FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, cc, id)
	if err != nil || len(q.Rows) == 0 {
		return st, err
	}
	dec := m.currencyDecimals(ctx, crud.Str(q.Rows[0][1]))
	// Entwürfe desselben Lieferanten (ein Auftrag über mehrere Angebote)
	drafts := []metamodel.Option{{Value: "", Label: "neuer Auftrag"}}
	if res, err := m.db.Query(ctx, `SELECT order_id, description FROM procurement__order WHERE company_code = ? AND supplier_id = ? AND status = ?
		AND NOT EXISTS (SELECT 1 FROM procurement__order_quote x WHERE x.company_code = procurement__order.company_code AND x.order_id = procurement__order.order_id AND x.quote_id = ?)
		ORDER BY order_id DESC`, cc, q.Rows[0][0], orderDraft, id); err == nil {
		for _, r := range res.Rows {
			drafts = append(drafts, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[0]) + " " + crud.Str(r[1])})
		}
	}
	st.Fields["ord_order"] = metamodel.FieldState{Options: drafts}
	toExisting := req.Values["ord_order"] != ""
	var types []metamodel.Option
	if res, err := m.db.Query(ctx, `SELECT code, name FROM procurement__order_type WHERE company_code = ? AND is_active = ? ORDER BY sort_order, code`, cc, true); err == nil {
		for _, r := range res.Rows {
			types = append(types, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[1])})
		}
	}
	hidden := false
	tf := metamodel.FieldState{Options: types}
	if toExisting {
		tf.Visible = &hidden
	} else if req.Values["ord_type"] == "" && len(types) > 0 {
		tf.Value = &types[0].Value
	}
	st.Fields["ord_type"] = tf
	lines, err := m.quoteLines(ctx, cc, id)
	if err != nil {
		return st, err
	}
	var opts []metamodel.Option
	var pre []string
	for _, l := range lines {
		label := strings.TrimSpace(l.pos + " " + l.text)
		if label == "" {
			label = fmt.Sprintf("Position %d", l.line)
		}
		label += " – " + formatDE(l.amount, dec)
		if l.kindName != "" {
			label += " (" + l.kindName + ")"
		}
		opts = append(opts, metamodel.Option{Value: fmt.Sprint(l.line), Label: label})
		if l.preselected {
			pre = append(pre, fmt.Sprint(l.line))
		}
	}
	lf := metamodel.FieldState{Options: opts}
	if len(opts) == 0 {
		lf.Visible = &hidden
	} else if req.Values["ord_lines"] == "" {
		v := strings.Join(pre, ",")
		lf.Value = &v
	}
	st.Fields["ord_lines"] = lf
	if len(opts) == 0 {
		st.Message = "Das Angebot hat keine Positionen – der Auftrag bekommt eine Position über den Betrag des Angebots."
	} else {
		st.Message = "Alternative oder Bedarfspositionen statt bzw. zusätzlich zu den vorausgewählten wählen. Der Auftrag entsteht als Entwurf: Texte, Kontakte und Positionen dort prüfen, dann „Auftrag erteilen …“."
	}
	return st, nil
}

// listValue: Mehrfachauswahl (Komma-Liste oder Liste).
func listValue(v any) []string {
	var parts []string
	switch x := v.(type) {
	case []any:
		for _, p := range x {
			parts = append(parts, crud.Str(p))
		}
	case []string:
		parts = x
	default:
		parts = strings.Split(crud.Str(v), ",")
	}
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// orderFromQuoteAction: Auftrag aus dem Angebot (neu oder zu einem Entwurf desselben
// Lieferanten); gewählte Positionen, Standard-Textbausteine; das Angebot gilt als angenommen.
func (m *Module) orderFromQuoteAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return sdk.Response{}, err
	}
	key, err := m.set.Entity(quoteObject).ParseID(in.ID)
	if err != nil {
		return sdk.Response{}, err
	}
	cc, quote := crud.Str(key["company_code"]), crud.Str(key["quote_id"])
	if err := requireWrite(ctx, orderObject, "create", cc); err != nil {
		return sdk.Response{}, err
	}
	plan := crud.Record{"planned_from": in.Data["planned_from"], "planned_to": in.Data["planned_to"]}
	if err := checkPlanned(plan); err != nil {
		return sdk.Response{}, err
	}
	q, err := m.set.Entity(quoteObject).Load(ctx, crud.Record{"company_code": cc, "quote_id": quote})
	if err != nil {
		return sdk.Response{}, err
	}
	if crud.Str(q["status"]) == quoteRejected {
		return sdk.Response{}, crud.Invalid("Angebot %s ist abgelehnt", quote)
	}
	lines, err := m.quoteLines(ctx, cc, quote)
	if err != nil {
		return sdk.Response{}, err
	}
	chosen := map[int64]bool{}
	for _, s := range listValue(in.Data["ord_lines"]) {
		chosen[toInt(s)] = true
	}
	if len(lines) > 0 && len(chosen) == 0 {
		return sdk.Response{}, crud.Invalid("Keine Position gewählt")
	}
	var orderID string
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if existing := strings.TrimSpace(crud.Str(in.Data["ord_order"])); existing != "" {
			o, err := m.requireDraft(ctx, cc, existing)
			if err != nil {
				return err
			}
			if o.Supplier != crud.Str(q["supplier_id"]) || o.Currency != crud.Str(q["currency"]) {
				return crud.Invalid("Auftrag %s ist für einen anderen Lieferanten oder eine andere Währung", existing)
			}
			orderID = existing
		} else {
			data := map[string]any{"company_code": cc, "order_type": in.Data["ord_type"], "supplier_id": q["supplier_id"],
				"description": q["description"], "order_date": crud.KeyDate(ctx), "object_type": q["object_type"], "object_id": q["object_id"],
				"cost_category": q["cost_category"], "currency": q["currency"], "net_amounts": crud.AsBool(q["net_amounts"]),
				"dependent_types": q["dependent_types"], "planned_from": firstNonNil(plan["planned_from"], q["planned_from"]),
				"planned_to": firstNonNil(plan["planned_to"], q["planned_to"])}
			if crud.Str(data["object_id"]) == "" {
				data["object_type"] = nil
			}
			resp, err := m.set.Entity(orderObject).Create(ctx, map[string]any{"data": data})
			if err != nil {
				return err
			}
			var rec struct {
				ID string `json:"order_id"`
			}
			if err := sdk.Decode(resp.Payload, &rec); err != nil {
				return err
			}
			orderID = rec.ID
			if err := m.copyDefaultBlocks(ctx, cc, orderID); err != nil {
				return err
			}
		}
		if _, err := m.db.Exec(ctx, `INSERT INTO procurement__order_quote (company_code, order_id, quote_id) VALUES (?, ?, ?)`, cc, orderID, quote); err != nil {
			return err
		}
		if err := m.copyQuoteItems(ctx, cc, quote, orderID, lines, chosen, q); err != nil {
			return err
		}
		if crud.Str(q["status"]) == quoteOpen {
			if _, err := m.db.Exec(ctx, `UPDATE procurement__quote SET status = ?, decided_at = ?, decided_by = ?, changed_at = ?,
				planned_from = COALESCE(?, planned_from), planned_to = COALESCE(?, planned_to) WHERE company_code = ? AND quote_id = ?`,
				quoteAccepted, now(), nilIfEmpty(sdk.CallFromContext(ctx).UserID), now(), plan["planned_from"], plan["planned_to"], cc, quote); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"order_id": orderID,
		"message": fmt.Sprintf("Auftrag %s (Entwurf) aus Angebot %s – Texte, Kontakte und Positionen dort prüfen, dann „Auftrag erteilen …“", orderID, quote)}}, nil
}

func firstNonNil(v ...any) any {
	for _, x := range v {
		if x != nil && crud.Str(x) != "" {
			return x
		}
	}
	return nil
}

// copyQuoteItems: gewählte Hauptpositionen des Angebots in den Auftrag (abhängige und
// nachgelagerte Zeilen rechnet der Auftrag selbst); ohne Positionen eine über den Betrag.
func (m *Module) copyQuoteItems(ctx context.Context, cc, quote, order string, lines []quoteLine, chosen map[int64]bool, q crud.Record) error {
	items := m.set.Entity(orderItemObject)
	if len(lines) == 0 {
		amount := toInt(m.text(ctx, `SELECT amount FROM procurement__quote WHERE company_code = ? AND quote_id = ?`, cc, quote))
		dec := m.currencyDecimals(ctx, crud.Str(q["currency"]))
		resp, err := items.Create(ctx, map[string]any{"data": map[string]any{"company_code": cc, "order_id": order,
			"item_text": firstNonEmpty(crud.Str(q["description"]), "Angebot "+quote), "unit_price": formatAmount(amount, dec), "cost_category": q["cost_category"]}})
		if err != nil {
			return err
		}
		return m.markOrigin(ctx, cc, order, resp, quote, 0)
	}
	res, err := m.db.Query(ctx, `SELECT line_no, supplier_pos, item_kind, item_text, quantity, unit, unit_price, amount, tax_code, cost_category,
		account_number, rounding_rule, scheme FROM procurement__quote_item WHERE company_code = ? AND quote_id = ? AND is_active = ?
		AND dependent_type IS NULL AND parent_line IS NULL ORDER BY line_no`, cc, quote, true)
	if err != nil {
		return err
	}
	dec := m.currencyDecimals(ctx, crud.Str(q["currency"]))
	for _, r := range res.Rows {
		line := toInt(r[0])
		if !chosen[line] {
			continue
		}
		data := map[string]any{"company_code": cc, "order_id": order, "supplier_pos": r[1], "item_kind": r[2], "item_text": r[3],
			"quantity": r[4], "unit": r[5], "unit_price": firstNonEmpty(crud.Str(r[6]), formatAmount(toInt(r[7]), dec)), "tax_code": r[8],
			"cost_category": r[9], "account_number": r[10], "rounding_rule": r[11], "scheme": r[12]}
		for k, v := range data {
			if v == nil || crud.Str(v) == "" {
				delete(data, k)
			}
		}
		resp, err := items.Create(ctx, map[string]any{"data": data})
		if err != nil {
			return fmt.Errorf("Position %d: %w", line, err)
		}
		if err := m.markOrigin(ctx, cc, order, resp, quote, line); err != nil {
			return err
		}
	}
	if !crud.AsBool(q["net_amounts"]) { // brutto ohne Rechnung der nachgelagerten Zeilen: Summe selbst
		total, err := m.docTotal(ctx, orderDoc, cc, order, 0)
		if err != nil {
			return err
		}
		_, err = m.db.Exec(ctx, `UPDATE procurement__order SET amount = ? WHERE company_code = ? AND order_id = ?`, total, cc, order)
		return err
	}
	return nil
}

// markOrigin: Herkunft einer übernommenen Position (Angebot, Position).
func (m *Module) markOrigin(ctx context.Context, cc, order string, resp sdk.Response, quote string, line int64) error {
	var rec struct {
		Line any `json:"line_no"`
	}
	if err := sdk.Decode(resp.Payload, &rec); err != nil {
		return err
	}
	_, err := m.db.Exec(ctx, `UPDATE procurement__order_item SET quote_id = ?, quote_line = ? WHERE company_code = ? AND order_id = ? AND line_no = ?`,
		quote, nilIfZero(line), cc, order, toInt(rec.Line))
	return err
}

func nilIfZero(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// copyDefaultBlocks: Standard-Textbausteine (Schreiben, nicht E-Mail) in einen neuen Auftrag.
func (m *Module) copyDefaultBlocks(ctx context.Context, cc, order string) error {
	res, err := m.db.Query(ctx, `SELECT code FROM procurement__text_block WHERE company_code = ? AND is_default = ? AND is_active = ? AND section <> ?
		ORDER BY sort_order, code`, cc, true, true, sectionMail)
	if err != nil {
		return err
	}
	var codes []string
	for _, r := range res.Rows {
		codes = append(codes, crud.Str(r[0]))
	}
	return m.insertBlocks(ctx, cc, order, codes)
}

// insertBlocks: Textbausteine als Texte des Auftrags (Reihenfolge = die des Bausteins).
func (m *Module) insertBlocks(ctx context.Context, cc, order string, codes []string) error {
	texts := m.set.Entity(orderTextObject)
	for _, code := range codes {
		res, err := m.db.Query(ctx, `SELECT section, text, sort_order FROM procurement__text_block WHERE company_code = ? AND code = ? AND is_active = ?`, cc, code, true)
		if err != nil {
			return err
		}
		if len(res.Rows) == 0 {
			return crud.Invalid("Textbaustein %s gibt es im Buchungskreis %s nicht", code, cc)
		}
		r := res.Rows[0]
		if crud.Str(r[0]) == sectionMail {
			return crud.Invalid("Textbaustein %s ist der Text einer E-Mail (an der Auftragsart)", code)
		}
		resp, err := texts.Create(ctx, map[string]any{"data": map[string]any{"company_code": cc, "order_id": order, "section": r[0], "text": r[1],
			"seq": toInt(r[2])}})
		if err != nil {
			return fmt.Errorf("Textbaustein %s: %w", code, err)
		}
		var rec struct {
			Line any `json:"line_no"`
		}
		if err := sdk.Decode(resp.Payload, &rec); err != nil {
			return err
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__order_text SET block = ? WHERE company_code = ? AND order_id = ? AND line_no = ?`,
			code, cc, order, toInt(rec.Line)); err != nil {
			return err
		}
	}
	return nil
}

// --- Aktionen am Auftrag ----------------------------------------------------------------

// orderAction: Auftrag einer Aktion mit Recht action (Object PurchaseOrder).
func (m *Module) orderAction(ctx context.Context, payload any, action string) (*orderRow, map[string]any, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(payload, &in); err != nil {
		return nil, nil, err
	}
	key, err := m.set.Entity(orderObject).ParseID(in.ID)
	if err != nil {
		return nil, nil, err
	}
	cc, id := crud.Str(key["company_code"]), crud.Str(key["order_id"])
	if ok, err := sdk.CheckAccess(ctx, orderObject, action, cc); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%w: keine Berechtigung für %s.%s im Buchungskreis %s", sdk.ErrPermissionDenied, orderObject, action, cc)
		}
		return nil, nil, err
	}
	o, err := m.loadOrder(ctx, cc, id)
	return o, in.Data, err
}

func (m *Module) addBlocksFormState(ctx context.Context, req metamodel.FormStateRequest) (metamodel.FormState, error) {
	st := metamodel.FormState{Fields: map[string]metamodel.FieldState{}}
	key, err := m.set.Entity(orderObject).ParseID(req.ID)
	if err != nil {
		return st, err
	}
	res, err := m.db.Query(ctx, `SELECT code, name, section FROM procurement__text_block WHERE company_code = ? AND is_active = ? AND section <> ?
		ORDER BY section, sort_order, code`, key["company_code"], true, sectionMail)
	if err != nil {
		return st, err
	}
	var opts []metamodel.Option
	for _, r := range res.Rows {
		opts = append(opts, metamodel.Option{Value: crud.Str(r[0]), Label: crud.Str(r[1]) + " (" + optionLabel(sectionOptions, crud.Str(r[2])) + ")"})
	}
	st.Fields["ord_blocks"] = metamodel.FieldState{Options: opts}
	if len(opts) == 0 {
		st.Message = "Keine Textbausteine (Einstellungen → Textbausteine (Aufträge))."
	}
	return st, nil
}

func (m *Module) orderAddBlocksAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, data, err := m.orderAction(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	if o.Status != orderDraft {
		return sdk.Response{}, crud.Invalid("Auftrag %s ist %s – nur im Entwurf änderbar", o.ID, optionLabel(orderStatusOptions, o.Status))
	}
	codes := listValue(data["ord_blocks"])
	if len(codes) == 0 {
		return sdk.Response{}, crud.Invalid("Keinen Textbaustein gewählt")
	}
	if err := m.db.InTx(ctx, nil, func(ctx context.Context) error { return m.insertBlocks(ctx, o.CC, o.ID, codes) }); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Auftrag %s: %d Textbausteine eingefügt", o.ID, len(codes))}}, nil
}

// orderStatusAction: Statuswechsel (from leer = jeder außer storniert).
func (m *Module) orderStatusAction(from, to string) func(context.Context, sdk.Request) (sdk.Response, error) {
	return func(ctx context.Context, req sdk.Request) (sdk.Response, error) {
		o, _, err := m.orderAction(ctx, req.Payload, "update")
		if err != nil {
			return sdk.Response{}, err
		}
		if (from != "" && o.Status != from) || o.Status == orderCancelled || o.Status == to {
			return sdk.Response{}, crud.Invalid("Auftrag %s ist %s", o.ID, optionLabel(orderStatusOptions, o.Status))
		}
		if _, err := m.db.Exec(ctx, `UPDATE procurement__order SET status = ?, changed_at = ? WHERE company_code = ? AND order_id = ?`, to, now(), o.CC, o.ID); err != nil {
			return sdk.Response{}, err
		}
		return sdk.Response{Payload: map[string]any{"message": "Auftrag " + o.ID + ": " + optionLabel(orderStatusOptions, to)}}, nil
	}
}

// orderPlanAction: Ausführungszeitraum – auch nach dem Erteilen (Liquiditätsvorschau).
func (m *Module) orderPlanAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	o, data, err := m.orderAction(ctx, req.Payload, "update")
	if err != nil {
		return sdk.Response{}, err
	}
	plan := crud.Record{"planned_from": data["planned_from"], "planned_to": data["planned_to"]}
	if err := checkPlanned(plan); err != nil {
		return sdk.Response{}, err
	}
	if _, err := m.db.Exec(ctx, `UPDATE procurement__order SET planned_from = ?, planned_to = ?, changed_at = ? WHERE company_code = ? AND order_id = ?`,
		plan["planned_from"], plan["planned_to"], now(), o.CC, o.ID); err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"message": "Auftrag " + o.ID + ": Ausführungszeitraum geändert"}}, nil
}
