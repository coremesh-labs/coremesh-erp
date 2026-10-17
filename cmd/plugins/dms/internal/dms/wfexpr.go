package dms

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// Platzhalter in Fragen, Vorgaben, Filtern, Parametern und Bedingungen:
//
//	{company_code} {file_name} {today} {partner} (Name und Nummer) {refs} (Bezüge)
//	{doc.<Feld>}            Feld des Dokuments (auch content_type, inbox, status …)
//	{ref.<Object>}          Schlüssel des Bezugs, {refid.<Object>} ohne Buchungskreis
//	{var.<Name>}            Variable, {var.<Name>_label} ihre Bezeichnung
//	{proposal.<Ziel>}       Vorschlag (z. B. {proposal.partner_id}, {proposal.ref:Contract})
//	{hint.<Schlüssel>}      Erkennungsregel (Muster, Wert aus einem Object)
//	{text}                  erkannter Text, {text.chars} seine Zeichen ohne Leerraum
//	{inbox.own_scans}       Eingangsverzeichnis mit eigenen Scans (true/false), {inbox.code}
//	{category.objects}      Objects der Dokumentkategorie (Komma-Liste), {category.partner}
//	                        true, wenn eines einen Geschäftspartner hat
//	{doctype.ref_kind}      Bezugsart der Dokumentart
//	{name_proposal}         Dokumentname nach dem Muster des Buchungskreises

var tmplRe = regexp.MustCompile(`\{([a-z_]+)(?:\.([A-Za-z0-9_:]+))?\}`)

// expand ersetzt die Platzhalter; unbekannte bleiben stehen.
func (m *Module) expand(ctx context.Context, d *docRow, t string) string {
	if !strings.Contains(t, "{") {
		return t
	}
	var doc map[string]string
	return tmplRe.ReplaceAllStringFunc(t, func(ph string) string {
		g := tmplRe.FindStringSubmatch(ph)
		switch g[1] {
		case "company_code":
			return d.CC
		case "today":
			return today()
		case "partner":
			if d.Partner == "" {
				return "–"
			}
			return m.partnerName(ctx, d.Partner) + " (" + d.Partner + ")"
		case "refs":
			return m.refsText(ctx, d)
		case "file_name":
			return d.FileName
		case "doc":
			if doc == nil {
				doc = m.docValues(ctx, d.ID)
			}
			return doc[g[2]]
		case "ref", "refid":
			refs, _ := m.activeRefs(ctx, d.ID)
			for _, r := range refs {
				if r.Object == g[2] {
					if g[1] == "refid" {
						return strings.TrimPrefix(r.Key, d.CC+"|")
					}
					return r.Key
				}
			}
			return ""
		case "text":
			if g[2] == "chars" {
				return strconv.Itoa(countChars(m.docText(ctx, d.ID)))
			}
			return m.docText(ctx, d.ID)
		case "hint":
			return m.docHints(ctx, d)[g[2]]
		case "var":
			return m.varText(ctx, d.ID, g[2])
		case "proposal":
			if p := m.proposal(ctx, d.ID, g[2]); p != nil {
				return p.Value
			}
			return ""
		case "inbox":
			switch g[2] {
			case "own_scans":
				return strconv.FormatBool(m.ownScans(ctx, d))
			case "code":
				return d.Inbox
			}
		case "category":
			objs, _ := m.docObjects(ctx, d)
			switch g[2] {
			case "objects":
				var out []string
				for _, o := range objs {
					out = append(out, o.Object)
				}
				return strings.Join(out, ",")
			case "partner":
				for _, o := range objs {
					if o.PartnerField != "" {
						return "true"
					}
				}
				return "false"
			}
		case "doctype":
			if g[2] == "ref_kind" {
				return m.text(ctx, `SELECT ref_kind FROM dms__doc_type WHERE code = ?`, d.DocType)
			}
		case "name_proposal":
			if n, err := m.proposeName(ctx, d); err == nil {
				return n
			}
			return ""
		}
		return ph
	})
}

// docValues: Felder des Dokuments als Text (Datum JJJJ-MM-TT).
func (m *Module) docValues(ctx context.Context, id string) map[string]string {
	out := map[string]string{}
	res, err := m.db.Query(ctx, `SELECT * FROM dms__document WHERE id = ?`, id)
	if err != nil || len(res.Rows) == 0 {
		return out
	}
	for i, c := range res.Columns {
		v := crud.Str(res.Rows[0][i])
		if c == "doc_date" {
			v = dateOf(res.Rows[0][i])
		}
		out[c] = v
	}
	return out
}

// --- Bedingungen -----------------------------------------------------------------------

// holds wertet eine Bedingung aus; why erklärt, warum sie nicht erfüllt ist.
func (m *Module) holds(ctx context.Context, d *docRow, c condition) (ok bool, why string) {
	l := strings.TrimSpace(m.expand(ctx, d, c.Left))
	r := strings.TrimSpace(m.expand(ctx, d, c.Right))
	ok = evalOp(c.Op, l, r)
	if ok {
		return true, ""
	}
	if c.Desc != "" {
		return false, m.expand(ctx, d, c.Desc)
	}
	return false, fmt.Sprintf("%s = %q, %s %s", c.Left, l, strings.ToLower(optionLabel(opOptions, c.Op)), r)
}

// allHold: alle Bedingungen erfüllt (sonst der erste Grund).
func (m *Module) allHold(ctx context.Context, d *docRow, cs []condition) (bool, string) {
	for _, c := range cs {
		if ok, why := m.holds(ctx, d, c); !ok {
			return false, why
		}
	}
	return true, ""
}

func evalOp(op, l, r string) bool {
	list := func(s string) []string { return splitList(s) }
	inList := func(v string, items []string) bool {
		for _, it := range items {
			if p, ok := strings.CutSuffix(it, "*"); ok && strings.HasPrefix(v, p) || strings.EqualFold(it, v) {
				return true
			}
		}
		return false
	}
	num := func(s string) (float64, bool) {
		f, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", "."), 64)
		return f, err == nil
	}
	switch op {
	case opEq:
		return strings.EqualFold(l, r)
	case opNe:
		return !strings.EqualFold(l, r)
	case opEmpty:
		return l == ""
	case opNotEmpty:
		return l != ""
	case opIn:
		return inList(l, list(r))
	case opNotIn:
		return !inList(l, list(r))
	case opHas:
		return inList(r, list(l))
	case opNotHas:
		return !inList(r, list(l))
	case opContains:
		return strings.Contains(strings.ToLower(l), strings.ToLower(r))
	case opGe, opLe:
		a, ok1 := num(l)
		b, ok2 := num(r)
		if !ok1 || !ok2 {
			return false
		}
		if op == opGe {
			return a >= b
		}
		return a <= b
	}
	return false
}

// --- Variablen und Vorschläge (dms__document.wf_vars, JSON) -------------------------------

func (m *Module) wfVars(ctx context.Context, docID string) map[string]any {
	out := map[string]any{}
	if s := m.text(ctx, `SELECT wf_vars FROM dms__document WHERE id = ?`, docID); s != "" {
		_ = json.Unmarshal([]byte(s), &out)
	}
	return out
}

func (m *Module) setWfVar(ctx context.Context, docID, key string, v any) error {
	vars := m.wfVars(ctx, docID)
	if v == nil {
		delete(vars, key)
	} else {
		vars[key] = v
	}
	b, err := json.Marshal(vars)
	if err != nil {
		return err
	}
	_, err = m.db.Exec(ctx, `UPDATE dms__document SET wf_vars = ? WHERE id = ?`, string(b), docID)
	return err
}

func (m *Module) varText(ctx context.Context, docID, name string) string {
	v := m.wfVars(ctx, docID)["var."+name]
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// proposalRec ist ein Vorschlag eines Programmschritts für ein Ziel.
type proposalRec struct {
	Value  string   `json:"value"`
	Label  string   `json:"label,omitempty"`
	Source string   `json:"source"` // Schritt bzw. Regel
	Score  int64    `json:"score,omitempty"`
	Why    []string `json:"why,omitempty"`
}

// text: „Vorschlag: Bezeichnung (IBAN, Name; 7 Punkte, Schritt 030)“.
func (p *proposalRec) text() string {
	var why []string
	if len(p.Why) > 0 {
		why = append(why, strings.Join(p.Why, ", "))
	}
	if p.Score > 0 {
		why = append(why, fmt.Sprintf("%d Punkte", p.Score))
	}
	if p.Source != "" {
		why = append(why, p.Source)
	}
	t := firstOf(p.Label, p.Value)
	if len(why) > 0 {
		t += " (" + strings.Join(why, "; ") + ")"
	}
	return "Vorschlag: " + t
}

func (m *Module) proposal(ctx context.Context, docID, key string) *proposalRec {
	raw, ok := m.wfVars(ctx, docID)["proposal."+key]
	if !ok {
		return nil
	}
	b, _ := json.Marshal(raw)
	var p proposalRec
	if json.Unmarshal(b, &p) != nil || p.Value == "" {
		return nil
	}
	return &p
}

func (m *Module) setProposal(ctx context.Context, docID, key string, p *proposalRec) error {
	if p == nil {
		return m.setWfVar(ctx, docID, "proposal."+key, nil)
	}
	return m.setWfVar(ctx, docID, "proposal."+key, p)
}

// proposalFor: Vorschlag für das Ziel – beim Bezug ohne festes Object (Auswahl der
// kaufmännischen Objekte) der erste Vorschlag eines Objects der Kategorie als Object:Schlüssel.
func (m *Module) proposalFor(ctx context.Context, d *docRow, key string) *proposalRec {
	if key != "ref" {
		return m.proposal(ctx, d.ID, key)
	}
	objs, _ := m.docObjects(ctx, d)
	for _, o := range objs {
		if p := m.proposal(ctx, d.ID, "ref:"+o.Object); p != nil {
			p.Value = o.Object + ":" + p.Value
			return p
		}
	}
	return nil
}
