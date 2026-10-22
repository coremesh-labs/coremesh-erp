package contract

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// TestClauseSetYAML: Herunterladen, im Editor ändern, wieder einlesen – geändert,
// neu, fehlend = deaktiviert; Fehler ändern nichts.
func TestClauseSetYAML(t *testing.T) {
	e := setup(t)
	e.basics()
	e.create(clauseSetObject, map[string]any{"company_code": "1000", "code": "WOHNUNG", "name": "Wohnung", "title": "Mietvertrag"})
	for _, c := range []map[string]any{
		{"clause_key": "MIETE", "paragraph": 10, "title": "Miete"},
		{"clause_key": "MIETE_HOEHE", "paragraph": 10, "subsection": 10, "text": "Die Miete beträgt {kondition:KM}.\n\nSie ist monatlich fällig.",
			"cond_source": condCondition, "cond_name": "KM", "rating": "CHECK"},
		{"clause_key": "ALT", "paragraph": 20, "title": "Alt", "text": "weg"},
	} {
		c["company_code"], c["set_code"] = "1000", "WOHNUNG"
		e.create(clauseObject, c)
	}
	r := e.must(clauseSetObject, "exportYaml", map[string]any{"id": "1000|WOHNUNG"})
	dl := r["download"].(metamodel.Content)
	raw, _ := base64.StdEncoding.DecodeString(dl.Content)
	y := string(raw)
	for _, want := range []string{"# Klauselset WOHNUNG", "key: MIETE_HOEHE", "text: |-\n      Die Miete beträgt {kondition:KM}.\n\n      Sie ist monatlich fällig.",
		"source: CONDITION", "rating: CHECK", "title: Mietvertrag"} {
		if !strings.Contains(y, want) {
			t.Fatalf("YAML ohne %q:\n%s", want, y)
		}
	}
	if dl.FileName != "klauselset-1000-wohnung.yaml" {
		t.Fatalf("Dateiname: %v", dl.FileName)
	}

	// Im Editor: Text ändern, Absatz anfügen, ALT entfernen.
	start := strings.Index(y, "  - key: ALT")
	y = y[:start] + "  - key: miete_faellig\n    paragraph: 10\n    subsection: 20\n    text: Bis zum 3. Werktag.\n"
	y = strings.Replace(y, "Sie ist monatlich fällig.", "Sie ist monatlich im Voraus fällig.", 1)
	r = e.must(clauseSetObject, "importYaml", map[string]any{"id": "1000|WOHNUNG", "data": map[string]any{clauseYAMLField: y}})
	if r["message"] != "Klauselset WOHNUNG eingelesen: 1 geändert, 1 neu, 1 unverändert, 1 deaktiviert (nicht in der Datei)" {
		t.Fatalf("Einlesen: %v", r)
	}
	res, _ := e.m.db.Query(e.ctx, `SELECT clause_key, text, is_active, cond_source, rating FROM contract__clause WHERE set_code = 'WOHNUNG' ORDER BY clause_key`)
	got := map[string][]any{}
	for _, row := range res.Rows {
		got[row[0].(string)] = row[1:]
	}
	if len(got) != 4 || !strings.HasSuffix(got["MIETE_HOEHE"][0].(string), "im Voraus fällig.") || got["MIETE_HOEHE"][2] != condCondition ||
		got["MIETE_HOEHE"][3] != "CHECK" || got["MIETE_FAELLIG"] == nil || toInt(got["ALT"][1]) != 0 {
		t.Fatalf("Klauseln: %v", got)
	}

	// Fehler (falsches Set, Absatz negativ) ändern nichts.
	if _, err := e.call(clauseSetObject, "importYaml", map[string]any{"id": "1000|WOHNUNG", "data": map[string]any{clauseYAMLField: "code: ANDERS\nclauses: [{key: X, paragraph: 1}]"}}); err == nil ||
		!strings.Contains(err.Error(), "ANDERS") {
		t.Fatalf("falsches Set: %v", err)
	}
	bad := strings.Replace(y, "subsection: 20", "subsection: -1", 1)
	bad = strings.Replace(bad, "im Voraus fällig.", "sofort fällig.", 1)
	if _, err := e.call(clauseSetObject, "importYaml", map[string]any{"id": "1000|WOHNUNG", "data": map[string]any{clauseYAMLField: bad}}); err == nil {
		t.Fatal("Absatz -1 angenommen")
	}
	res, _ = e.m.db.Query(e.ctx, `SELECT text FROM contract__clause WHERE clause_key = 'MIETE_HOEHE'`)
	if !strings.Contains(res.Rows[0][0].(string), "im Voraus") {
		t.Fatalf("Fehler hat geändert: %v", res.Rows[0][0])
	}
}
