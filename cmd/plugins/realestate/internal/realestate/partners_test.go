package realestate

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

func (e *env) assign(obj, role, partner, from string, share any) (map[string]any, error) {
	d := map[string]any{"company_code": "1000", "object_id": obj, "role_code": role, "partner_id": partner, "valid_from": from}
	if share != nil {
		d["share"] = share
	}
	return e.call(objectPartnerObject, "create", map[string]any{"data": d})
}

func (e *env) partner(id, name string, roles ...string) {
	e.h.partners[id] = name
	for _, r := range roles {
		e.h.partnerRoles[id] = append(e.h.partnerRoles[id], partnerRoleSlice{r, "2000-01-01", "9999-12-31"})
	}
}

// TestPartnerAssignments: Rollen aus dem Partnermodul, je Buchungskreis
// aktiviert; Zuordnung mit Ebenen, Exklusivität, Anteilen und Vererbung.
func TestPartnerAssignments(t *testing.T) {
	e := setup(t)
	e.house() // aktiviert die vorgeschlagenen Rollen, die das Partnermodul kennt
	roles := items(e.must(partnerRoleObject, "list", map[string]any{"query": map[string]any{"company_code": "1000"}}))
	if len(roles) != 4 || roles[0]["role_code"] != "OWNER" || roles[0]["name"] != "Eigentümer" || roles[0]["with_share"] != true {
		t.Fatalf("Rollen: %v", roles)
	}
	_, err := e.call(partnerRoleObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "role_code": "GIBTSNICHT"}})
	expect(t, err, sdk.ErrInvalidArgument, "Rolle nicht im Partnermodul")
	_, err = e.call(partnerRoleObject, "create", map[string]any{"data": map[string]any{"company_code": "1000", "role_code": "TENANT", "levels": "UNIT, KELLER"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Ebene")

	w1 := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"].(string)
	w2 := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"].(string)
	e.partner("P1", "Hausmeister Müller", "JANITOR")
	e.partner("P2", "Hausdienst Schulz", "JANITOR")
	e.partner("O1", "Anna Eigner", "OWNER")
	e.partner("O2", "Bernd Eigner", "OWNER")
	e.partner("O3", "Carla Käufer", "OWNER")

	a, err := e.assign("LpzBrn1", "JANITOR", "P1", "2026-01-01", nil)
	if err != nil || a["object_level"] != levelBuilding {
		t.Fatalf("Hausmeister am Gebäude: %v %v", a, err)
	}
	if labels := a["_labels"].(map[string]any); labels["partner_id"] != "Hausmeister Müller" || labels["role_code"] != "Hausmeister" {
		t.Fatalf("Texte: %v", labels)
	}
	_, err = e.assign(w1, "JANITOR", "P1", "2026-01-01", nil)
	expect(t, err, sdk.ErrInvalidArgument, "Hausmeister an der Wohnung (Ebene)")
	_, err = e.assign("LpzBrn1", "JANITOR", "P2", "2026-06-01", nil)
	expect(t, err, sdk.ErrInvalidArgument, "zweiter Hausmeister (exklusiv)")
	_, err = e.assign("LpzBrn1", "JANITOR", "O1", "2026-01-01", nil)
	expect(t, err, sdk.ErrInvalidArgument, "Partner ohne Rolle")
	_, err = e.assign("LpzBrn1", "TENANT", "P1", "2026-01-01", nil)
	expect(t, err, sdk.ErrInvalidArgument, "Rolle nicht aktiviert")

	// Eigentümergemeinschaft an der Wirtschaftseinheit: Anteile ≤ 100 %.
	if _, err := e.assign("LpzBrn", "OWNER", "O1", "2026-01-01", 60); err != nil {
		t.Fatal(err)
	}
	_, err = e.assign("LpzBrn", "OWNER", "O2", "2026-03-01", 50)
	expect(t, err, sdk.ErrInvalidArgument, "Anteile über 100 %")
	if err == nil || !strings.Contains(err.Error(), "110") {
		t.Fatalf("Meldung: %v", err)
	}
	_, err = e.assign("LpzBrn", "OWNER", "O2", "2026-03-01", nil)
	expect(t, err, sdk.ErrInvalidArgument, "Eigentümer ohne Anteil")
	if _, err := e.assign("LpzBrn", "OWNER", "O2", "2026-03-01", 40); err != nil {
		t.Fatal(err)
	}
	// Wohnung 1 verkauft (Sondereigentum): eigener Eigentümer übersteuert.
	if _, err := e.assign(w1, "OWNER", "O3", "2026-01-01", 100); err != nil {
		t.Fatal(err)
	}

	effective := func(obj, date string) map[string][]EffectivePartner {
		t.Helper()
		ps, err := e.m.EffectivePartners(e.ctx, "1000", obj, date)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]EffectivePartner{}
		for _, p := range ps {
			out[p.Role] = append(out[p.Role], p)
		}
		return out
	}
	p := effective(w1, "2026-06-01")
	if len(p["OWNER"]) != 1 || p["OWNER"][0].Partner != "O3" || p["OWNER"][0].From != w1 {
		t.Fatalf("Eigentümer W1: %+v", p["OWNER"])
	}
	if len(p["JANITOR"]) != 1 || p["JANITOR"][0].From != "LpzBrn1" || p["JANITOR"][0].PartnerName != "Hausmeister Müller" {
		t.Fatalf("Hausmeister vom Gebäude: %+v", p["JANITOR"])
	}
	if p := effective(w2, "2026-06-01"); len(p["OWNER"]) != 2 || p["OWNER"][0].FromLevel != levelEntity {
		t.Fatalf("Eigentümer W2 von der Wirtschaftseinheit: %+v", p["OWNER"])
	}
	if p := effective(w2, "2026-02-01"); len(p["OWNER"]) != 1 {
		t.Fatalf("vor dem Eintritt von O2: %+v", p["OWNER"])
	}

	// Action in der Detailansicht bzw. Konsole.
	r := e.must("RentObject", "partners", map[string]any{"id": "1000|" + w2, "date": "2026-06-01"})
	if msg := r["message"].(string); !strings.Contains(msg, "Anna Eigner (60 %) – von Wirtschaftseinheit LpzBrn") || !strings.Contains(msg, "Hausmeister: Hausmeister Müller – von Gebäude LpzBrn1") {
		t.Fatalf("Meldung: %s", msg)
	}
	if r := e.must(setupObject, "partners", map[string]any{"company": "1000", "object": "LpzBrn1"}); len(r["partners"].([]EffectivePartner)) == 0 {
		t.Fatalf("Konsole: %v", r)
	}
}

// TestEvents: Mietobjekte melden sich unter RentObject mit Art und Gebäude;
// Bestandteile, Bemessungen und Partnerzuordnungen melden Änderungen ebenfalls.
func TestEvents(t *testing.T) {
	e := setup(t)
	e.house()
	w := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	c := e.create("RentObject", composite("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	e.create("CompositeItem", map[string]any{"company_code": "1000", "composite_id": c, "object_id": w, "valid_from": "2026-01-01"})
	e.create("Measurement", map[string]any{"company_code": "1000", "object_id": w, "measurement_type": "WFL", "value": 70, "valid_from": "2026-01-01"})
	e.partner("P1", "Hausmeister Müller", "JANITOR")
	if _, err := e.assign("LpzBrn1", "JANITOR", "P1", "2026-01-01", nil); err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, ev := range e.h.events {
		seen[ev["object"].(string)] = ev
	}
	ro := seen["RentObject"]
	if ro == nil || ro["company_code"] != "1000" {
		t.Fatalf("Events: %v", e.h.events)
	}
	if d := ro["data"].(map[string]any); d["kind"] != kindComposite || d["building_id"] != "LpzBrn1" || d["entity_id"] != "LpzBrn" {
		t.Fatalf("RentObject-Event: %v", d)
	}
	for _, o := range []string{"CompositeItem", "Measurement", objectPartnerObject, "Building", "BusinessEntity"} {
		if seen[o] == nil {
			t.Errorf("kein Event für %s", o)
		}
	}
	if d := seen[objectPartnerObject]["data"].(map[string]any); d["role_code"] != "JANITOR" || d["object_level"] != levelBuilding {
		t.Fatalf("Partner-Event: %v", d)
	}
}

// TestSetupDisplayRules: setup-company legt die Darstellungsregeln je Art an –
// nur fehlende, also beim zweiten Mal keine.
func TestSetupDisplayRules(t *testing.T) {
	e := setup(t)
	r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"})
	if r["display_rules"] != 4 || r["partner_roles"] != 4 {
		t.Fatalf("Setup: %v", r)
	}
	got := strings.Join(e.h.display, " ")
	for _, want := range []string{"DisplayRule:Mietobjekt: Pool", "cond:kind=POOL", "hidden:floor", "section:bestandteile", "section:flaechen"} {
		if !strings.Contains(got, want) {
			t.Errorf("fehlt %s in %s", want, got)
		}
	}
	if r := e.must(setupObject, "setupCompany", map[string]any{"company": "1000"}); r["display_rules"] != 0 || r["partner_roles"] != 0 {
		t.Fatalf("zweites Setup: %v", r)
	}
}
