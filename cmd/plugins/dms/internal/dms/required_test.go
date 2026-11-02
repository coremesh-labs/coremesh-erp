package dms

import (
	"testing"
	"time"
)

// TestRequiredDocs (0.33.0): Pflichtunterlagen je Objekt – vorhanden, abgelaufen,
// fehlt; Art der Wirtschaftseinheit grenzt ein; entfällt und Papier vom Benutzer.
func TestRequiredDocs(t *testing.T) {
	e := setup(t)
	e.company()
	if _, err := e.call(requiredObject, "create", map[string]any{"data": map[string]any{"company_code": "2000", "code": "GST", "name": "Grundsteuer",
		"file_accounts": "9.9"}}); err != nil {
		t.Fatalf("Anfang einer Ablage zulässig: %v", err)
	}
	if _, err := e.call(requiredObject, "create", map[string]any{"data": map[string]any{"company_code": "2000", "code": "X", "name": "x", "file_accounts": ""}}); err == nil {
		t.Fatal("ohne Ablage")
	}
	e.must(requiredObject, "update", map[string]any{"id": "2000|GST", "data": map[string]any{"file_accounts": "3.0", "doc_types": "notice"}})
	e.create(requiredObject, map[string]any{"company_code": "2000", "code": "WP", "name": "Wirtschaftsplan", "file_accounts": "5.2.3.2",
		"valid_months": 15, "entity_types": "sev"})
	e.create(requiredObject, map[string]any{"company_code": "2000", "code": "RE", "name": "Rechnungen", "file_accounts": "4.2.0", "valid_months": 0})

	old := time.Now().AddDate(-2, 0, 0).Format("2006-01-02")
	e.fileDoc("3.0.1", "NOTICE", "2026-02-01", "Building", "ROBro1")
	e.fileDoc("5.2.3.2", "STATEMENT", old, "Building", "ROBro1")
	r := e.must(reqStatusObj, "refresh", map[string]any{})
	if toInt(r["rows"]) != 5 { // ROBro1: GST, WP, RE; ROLeer (OWN): GST, RE
		t.Fatalf("Zeilen: %v", r)
	}
	st := func(prop, req string) map[string]any {
		t.Helper()
		return e.must(reqStatusObj, "get", map[string]any{"id": "2000|" + prop + "|" + req})
	}
	if s := st("ROBro1", "GST"); s["status"] != rqOK || toInt(s["doc_count"]) != 1 || s["last_date"] != "2026-02-01" || s["last_document"] == nil {
		t.Fatalf("Grundsteuer: %v", s)
	}
	if s := st("ROBro1", "WP"); s["status"] != rqExpired || s["valid_until"] == nil {
		t.Fatalf("Wirtschaftsplan: %v", s)
	}
	if s := st("ROBro1", "RE"); s["status"] != rqMissing {
		t.Fatalf("Rechnungen: %v", s)
	}
	if _, err := e.call(reqStatusObj, "get", map[string]any{"id": "2000|ROLeer|WP"}); err == nil {
		t.Fatal("Wirtschaftsplan nur bei SEV")
	}
	if s := st("ROLeer", "GST"); s["status"] != rqMissing {
		t.Fatalf("ohne Dokumente: %v", s)
	}
	// Benutzer: entfällt sofort, Papier beim nächsten Ermitteln
	e.must(reqStatusObj, "update", map[string]any{"id": "2000|ROLeer|RE", "data": map[string]any{"not_required": true, "note": "keine"}})
	if s := st("ROLeer", "RE"); s["status"] != rqNA {
		t.Fatalf("entfällt: %v", s)
	}
	e.must(reqStatusObj, "update", map[string]any{"id": "2000|ROBro1|WP", "data": map[string]any{"paper_date": time.Now().Format("2006-01-02")}})
	e.must(reqStatusObj, "refresh", map[string]any{})
	if s := st("ROBro1", "WP"); s["status"] != rqOK {
		t.Fatalf("Papier: %v", s)
	}
	if s := st("ROLeer", "RE"); s["status"] != rqNA || s["note"] != "keine" {
		t.Fatalf("entfällt bleibt: %v", s)
	}
	// Katalogeintrag aus: Zeilen ohne Benutzerangaben entfallen, andere bleiben „entfällt“
	e.must(requiredObject, "deactivate", map[string]any{"id": "2000|RE"})
	e.must(reqStatusObj, "refresh", map[string]any{})
	if _, err := e.call(reqStatusObj, "get", map[string]any{"id": "2000|ROBro1|RE"}); err == nil {
		t.Fatal("Zeile ohne Angaben bleibt")
	}
	if s := st("ROLeer", "RE"); s["status"] != rqNA {
		t.Fatalf("mit Vermerk: %v", s)
	}
}

func TestEvalRequired(t *testing.T) {
	day := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		date        string
		months, war int64
		want, until string
	}{
		{"", 12, 60, rqMissing, ""},
		{"2010-01-01", 0, 60, rqOK, ""},
		{"2026-01-01", 12, 60, rqDue, "2027-01-01"},
		{"2025-11-01", 12, 60, rqExpired, "2026-11-01"},
		{"2026-06-01", 12, 60, rqOK, "2027-06-01"},
	} {
		if s, u := evalRequired(c.date, c.months, c.war, day); s != c.want || u != c.until {
			t.Errorf("%v: %s %s", c, s, u)
		}
	}
}
