package contract

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Klauselset als YAML (seit 0.41.0): „Als YAML herunterladen“ liefert Kopf und alle
// Klauseln (auch inaktive) zum Bearbeiten im Editor; „Aus YAML einlesen …“ übernimmt
// die Datei als vollständigen Stand des Sets – Kopf und Klauseln ändern bzw. anlegen,
// Klauseln, die in der Datei fehlen, deaktivieren. Alles in einer Transaktion über die
// Prüfungen der Klauseln; ein Fehler ändert nichts.

const clauseYAMLField = "yaml_file"

type clauseSetYAML struct {
	Code          string       `yaml:"code"`
	Name          string       `yaml:"name"`
	ContractTypes string       `yaml:"contract_types,omitempty"`
	Title         string       `yaml:"title,omitempty"`
	Template      string       `yaml:"template,omitempty"`
	OwnLabel      string       `yaml:"own_label,omitempty"`
	OwnRoles      string       `yaml:"own_roles,omitempty"`
	PartnerLabel  string       `yaml:"partner_label,omitempty"`
	Clauses       []clauseYAML `yaml:"clauses"`
}

type clauseYAML struct {
	Key        string         `yaml:"key"`
	Paragraph  int64          `yaml:"paragraph"`
	Subsection int64          `yaml:"subsection"`
	SortOrder  int64          `yaml:"sort_order,omitempty"`
	Title      string         `yaml:"title,omitempty"`
	Text       string         `yaml:"text,omitempty"`
	Condition  *conditionYAML `yaml:"condition,omitempty"`
	Rating     string         `yaml:"rating,omitempty"`
	Note       string         `yaml:"note,omitempty"`
	Inactive   bool           `yaml:"inactive,omitempty"`
}

type conditionYAML struct {
	Source string `yaml:"source"`
	Name   string `yaml:"name"`
	Values string `yaml:"values,omitempty"`
	Negate bool   `yaml:"negate,omitempty"`
}

const clauseYAMLHead = `# Klauselset %s (Buchungskreis %s), Stand %s
#
# Bearbeiten und mit „Aus YAML einlesen …“ am Klauselset wieder einlesen. Die Datei ist
# der vollständige Stand: Klauseln, die fehlen, werden deaktiviert.
#   key         Schlüssel (A–Z, 0–9, _; für Verweise {ref:KEY}), nicht ändern
#   paragraph   Paragraph, subsection Absatz (0 = Überschrift bzw. allgemeiner Text);
#               gedruckt wird lückenlos (§ 1, Abs. 1 …)
#   sort_order  Reihenfolge im Absatz (Klauseln mit derselben Absatznummer; sonst nach key)
#   text        Markdown (**fett**, *kursiv*, Listen mit - / 1. / a), Leerzeile = Absatz);
#               Tabellenzeilen | Grundmiete | EUR {kondition:KM} |, Linie |---|---|;
#               Tabellenzeilen aufeinanderfolgender Klauseln eines Absatzes = eine Tabelle
#   condition   source: %s;
#               name, values (Komma-Liste; leer = vorhanden), negate: true = nicht erfüllt
#   rating      %s; note wird nicht gedruckt
#   inactive    true = Klausel ruht
`

func (m *Module) clauseSetActions() []crud.Action {
	return []crud.Action{
		{ActionConfig: metamodel.ActionConfig{Name: "exportYaml", Label: "Als YAML herunterladen", Record: true}, Handle: m.exportClausesYAMLAction},
		{ActionConfig: metamodel.ActionConfig{Name: "importYaml", Label: "Aus YAML einlesen …", Record: true, Fields: []string{clauseYAMLField},
			Confirm: "Die Datei ersetzt den Stand des Klauselsets; Klauseln, die darin fehlen, werden deaktiviert."}, Handle: m.importClausesYAMLAction},
	}
}

func (m *Module) clauseSetOfRequest(ctx context.Context, req sdk.Request) (*clauseSetRow, string, map[string]any, error) {
	var in struct {
		ID   string         `json:"id"`
		Data map[string]any `json:"data"`
	}
	if err := sdk.Decode(req.Payload, &in); err != nil {
		return nil, "", nil, err
	}
	key, err := m.set.Entity(clauseSetObject).ParseID(in.ID)
	if err != nil {
		return nil, "", nil, err
	}
	cc := crud.Str(key["company_code"])
	set, err := m.clauseSetOf(ctx, cc, crud.Str(key["code"]))
	return set, cc, in.Data, err
}

func (m *Module) exportClausesYAMLAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	set, cc, _, err := m.clauseSetOfRequest(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	if ok, err := sdk.CheckAccess(ctx, clauseObject, "list", cc); err != nil || !ok {
		return sdk.Response{}, fmt.Errorf("%w: Klauseln lesen", sdk.ErrPermissionDenied)
	}
	doc := clauseSetYAML{Code: set.Code, Name: set.Name, ContractTypes: set.Types, Title: set.Title, Template: set.Template,
		OwnLabel: set.OwnLabel, OwnRoles: set.OwnRoles, PartnerLabel: set.PartnerLabel, Clauses: []clauseYAML{}}
	res, err := m.db.Query(ctx, `SELECT clause_key, paragraph, subsection, title, text, cond_source, cond_name, cond_values, cond_negate, rating, note, is_active, sort_order
		FROM contract__clause WHERE company_code = ? AND set_code = ? ORDER BY paragraph, subsection, sort_order, clause_key`, cc, set.Code)
	if err != nil {
		return sdk.Response{}, err
	}
	for _, r := range res.Rows {
		c := clauseYAML{Key: crud.Str(r[0]), Paragraph: toInt(r[1]), Subsection: toInt(r[2]), Title: crud.Str(r[3]), Text: crud.Str(r[4]),
			Rating: crud.Str(r[9]), Note: crud.Str(r[10]), Inactive: !crud.AsBool(r[11]), SortOrder: toInt(r[12])}
		if src := crud.Str(r[5]); src != "" {
			c.Condition = &conditionYAML{Source: src, Name: crud.Str(r[6]), Values: crud.Str(r[7]), Negate: crud.AsBool(r[8])}
		}
		doc.Clauses = append(doc.Clauses, c)
	}
	raw, err := encodeClausesYAML(doc)
	if err != nil {
		return sdk.Response{}, err
	}
	head := fmt.Sprintf(clauseYAMLHead, set.Code, cc, time.Now().Format("2006-01-02 15:04"), optionList(condSourceOptions), optionList(ratingOptions))
	raw = append([]byte(head), raw...)
	return sdk.Response{Payload: map[string]any{"message": fmt.Sprintf("Klauselset %s: %d Klauseln", set.Code, len(doc.Clauses)),
		"download": metamodel.Content{Content: base64.StdEncoding.EncodeToString(raw), ContentType: "application/yaml",
			FileName: strings.ToLower("klauselset-" + cc + "-" + set.Code + ".yaml")}}}, nil
}

// encodeClausesYAML: Texte und Hinweise als Block (|), damit sie im Editor lesbar sind.
func encodeClausesYAML(doc clauseSetYAML) ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(doc); err != nil {
		return nil, err
	}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(n.Content); i += 2 {
				if k, v := n.Content[i], n.Content[i+1]; (k.Value == "text" || k.Value == "note") && v.Kind == yaml.ScalarNode {
					v.Style = yaml.LiteralStyle
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

// sameClause: Inhalt unverändert (Änderungsdatum bleibt).
func sameClause(a, b clauseYAML) bool {
	norm := func(c clauseYAML) clauseYAML {
		c.Text, c.Note, c.Inactive = strings.TrimRight(c.Text, "\n"), strings.TrimRight(c.Note, "\n"), false
		if c.Condition != nil {
			cc := *c.Condition
			c.Condition = &cc
		}
		return c
	}
	a, b = norm(a), norm(b)
	if (a.Condition == nil) != (b.Condition == nil) || (a.Condition != nil && *a.Condition != *b.Condition) {
		return false
	}
	a.Condition, b.Condition = nil, nil
	return a == b
}

// checkClauseYAML: die Prüfungen der Klausel vorab, damit eine fehlerhafte Datei gar
// nichts schreibt.
func checkClauseYAML(c clauseYAML) error {
	known := func(opts []metamodel.Option, v string) bool {
		return v == "" || slices.ContainsFunc(opts, func(o metamodel.Option) bool { return o.Value == v })
	}
	switch {
	case !clauseKeyRe.MatchString(c.Key):
		return fmt.Errorf("key: A–Z, 0–9, _ (höchstens 30 Zeichen)")
	case c.Paragraph < 1:
		return fmt.Errorf("paragraph: ganze Zahl ab 1")
	case c.Subsection < 0:
		return fmt.Errorf("subsection: ganze Zahl ab 0")
	case !known(ratingOptions, c.Rating):
		return fmt.Errorf("rating %q: %s", c.Rating, optionList(ratingOptions))
	case c.Condition != nil && (c.Condition.Source == "" || !known(condSourceOptions, c.Condition.Source)):
		return fmt.Errorf("condition.source %q: %s", c.Condition.Source, optionList(condSourceOptions))
	case c.Condition != nil && strings.TrimSpace(c.Condition.Name) == "":
		return fmt.Errorf("condition.name fehlt")
	}
	return nil
}

// optionList: „WERT (Bezeichnung), …“ für die Erläuterung im Kopf der Datei.
func optionList(opts []metamodel.Option) string {
	parts := make([]string, len(opts))
	for i, o := range opts {
		parts[i] = o.Value + " (" + o.Label + ")"
	}
	return strings.Join(parts, ", ")
}

func (m *Module) importClausesYAMLAction(ctx context.Context, req sdk.Request) (sdk.Response, error) {
	set, cc, data, err := m.clauseSetOfRequest(ctx, req)
	if err != nil {
		return sdk.Response{}, err
	}
	var doc clauseSetYAML
	dec := yaml.NewDecoder(strings.NewReader(crud.Str(data[clauseYAMLField])))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return sdk.Response{}, crud.Invalid("YAML-Datei: %v", err)
	}
	if code := strings.ToUpper(strings.TrimSpace(doc.Code)); code != "" && code != set.Code {
		return sdk.Response{}, crud.Invalid("Die Datei gehört zum Klauselset %s, nicht zu %s", code, set.Code)
	}
	if len(doc.Clauses) == 0 {
		return sdk.Response{}, crud.Invalid("Die Datei enthält keine Klauseln")
	}
	seen := map[string]bool{}
	for i, c := range doc.Clauses {
		k := strings.ToUpper(strings.TrimSpace(c.Key))
		if k == "" {
			return sdk.Response{}, crud.Invalid("Klausel Nr. %d: key fehlt", i+1)
		}
		if seen[k] {
			return sdk.Response{}, crud.Invalid("Klausel %s steht doppelt in der Datei", k)
		}
		seen[k] = true
		doc.Clauses[i].Key = k
		if err := checkClauseYAML(doc.Clauses[i]); err != nil {
			return sdk.Response{}, crud.Invalid("Klausel %s: %v", k, err)
		}
	}
	res, err := m.db.Query(ctx, `SELECT clause_key, is_active, paragraph, subsection, title, text, cond_source, cond_name, cond_values, cond_negate, rating, note, sort_order
		FROM contract__clause WHERE company_code = ? AND set_code = ?`, cc, set.Code)
	if err != nil {
		return sdk.Response{}, err
	}
	have := map[string]bool{}         // Schlüssel → aktiv
	stored := map[string]clauseYAML{} // Schlüssel → gespeicherter Stand (ohne inactive)
	for _, r := range res.Rows {
		k := crud.Str(r[0])
		have[k] = crud.AsBool(r[1])
		c := clauseYAML{Key: k, Paragraph: toInt(r[2]), Subsection: toInt(r[3]), Title: crud.Str(r[4]), Text: crud.Str(r[5]), Rating: crud.Str(r[10]), Note: crud.Str(r[11]), SortOrder: toInt(r[12])}
		if src := crud.Str(r[6]); src != "" {
			c.Condition = &conditionYAML{Source: src, Name: crud.Str(r[7]), Values: crud.Str(r[8]), Negate: crud.AsBool(r[9])}
		}
		stored[k] = c
	}
	setE, clE := m.set.Entity(clauseSetObject), m.set.Entity(clauseObject)
	id := func(k string) string { return cc + "|" + set.Code + "|" + k }
	var created, changed, deactivated int
	err = m.db.InTx(ctx, nil, func(ctx context.Context) error {
		if _, err := setE.Update(ctx, map[string]any{"id": cc + "|" + set.Code, "data": map[string]any{
			"name": firstNonEmpty(doc.Name, set.Name), "contract_types": doc.ContractTypes, "title": doc.Title, "template": doc.Template,
			"own_label": doc.OwnLabel, "own_roles": doc.OwnRoles, "partner_label": doc.PartnerLabel}}); err != nil {
			return fmt.Errorf("Kopf: %w", err)
		}
		for _, c := range doc.Clauses {
			rec := map[string]any{"paragraph": c.Paragraph, "subsection": c.Subsection, "sort_order": c.SortOrder, "title": c.Title, "text": strings.TrimRight(c.Text, "\n"),
				"cond_source": "", "cond_name": "", "cond_values": "", "cond_negate": false, "rating": c.Rating, "note": strings.TrimRight(c.Note, "\n")}
			if c.Condition != nil {
				rec["cond_source"], rec["cond_name"], rec["cond_values"], rec["cond_negate"] = c.Condition.Source, c.Condition.Name, c.Condition.Values, c.Condition.Negate
			}
			active, exists := have[c.Key]
			switch {
			case !exists:
				rec["company_code"], rec["set_code"], rec["clause_key"] = cc, set.Code, c.Key
				if _, err := clE.Create(ctx, map[string]any{"data": rec}); err != nil {
					return fmt.Errorf("Klausel %s: %w", c.Key, err)
				}
				created++
				active = true
			default:
				if !active && !c.Inactive {
					if _, err := clE.Reactivate(ctx, map[string]any{"id": id(c.Key)}); err != nil {
						return fmt.Errorf("Klausel %s: %w", c.Key, err)
					}
					active = true
				}
				if !sameClause(stored[c.Key], c) {
					if _, err := clE.Update(ctx, map[string]any{"id": id(c.Key), "data": rec}); err != nil {
						return fmt.Errorf("Klausel %s: %w", c.Key, err)
					}
					changed++
				}
			}
			if active && c.Inactive {
				if _, err := clE.Deactivate(ctx, map[string]any{"id": id(c.Key)}); err != nil {
					return fmt.Errorf("Klausel %s: %w", c.Key, err)
				}
			}
		}
		for k, active := range have {
			if active && !seen[k] {
				if _, err := clE.Deactivate(ctx, map[string]any{"id": id(k)}); err != nil {
					return fmt.Errorf("Klausel %s: %w", k, err)
				}
				deactivated++
			}
		}
		return nil
	})
	if err != nil {
		return sdk.Response{}, err
	}
	return sdk.Response{Payload: map[string]any{"id": cc + "|" + set.Code, "message": fmt.Sprintf(
		"Klauselset %s eingelesen: %d geändert, %d neu, %d unverändert, %d deaktiviert (nicht in der Datei)", set.Code, changed, created,
		len(doc.Clauses)-changed-created, deactivated)}}, nil
}
