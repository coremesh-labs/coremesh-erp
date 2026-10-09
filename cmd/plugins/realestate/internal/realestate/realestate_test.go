package realestate

import (
	"strings"
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/hook"
)

func unit(cc, building, usage, id string) map[string]any { return obj(kindUnit, cc, building, usage, id) }

func space(cc, building, usage, id string) map[string]any { return obj(kindSpace, cc, building, usage, id) }

func composite(cc, building, usage, id string) map[string]any {
	return obj(kindComposite, cc, building, usage, id)
}

// obj: Daten eines Mietobjekts der Art kind.
func obj(kind, cc, building, usage, id string) map[string]any {
	d := map[string]any{"kind": kind, "company_code": cc, "building_id": building, "designation": "Einheit", "usage_type": usage}
	if id != "" {
		d["object_id"] = id
	}
	return d
}

// TestMnemonicIDs: Gebäude = Wirtschaftseinheit + Nummer, Objekte = Gebäude +
// Kürzel der Nutzungsart + laufende Nummer; eingegebene IDs müssen passen.
func TestMnemonicIDs(t *testing.T) {
	e := setup(t)
	e.house()
	b := e.must("Building", "get", map[string]any{"id": "1000|LpzBrn1"})
	if b["street"] != "Brunnenstraße 1" || b["status"] != "ACTIVE" || b["valid_to"] != "9999-12-31" {
		t.Fatalf("Gebäude: %v", b)
	}
	for i, want := range []string{"LpzBrn1WG001", "LpzBrn1WG002"} {
		if u := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", "")); u["object_id"] != want || u["entity_id"] != "LpzBrn" {
			t.Fatalf("Einheit %d: %v", i, u)
		}
	}
	if u := e.create("RentObject", unit("1000", "LpzBrn1", "STELLPLATZ", "")); u["object_id"] != "LpzBrn1SP001" {
		t.Fatalf("Stellplatz: %v", u["object_id"])
	}
	e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", "LpzBrn1WG010"))
	if u := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", "")); u["object_id"] != "LpzBrn1WG011" {
		t.Fatalf("nach manueller ID: %v", u["object_id"])
	}
	_, err := e.call("RentObject", "create", map[string]any{"data": unit("1000", "LpzBrn1", "WOHNEN", "Hamburg1WG001")})
	expect(t, err, sdk.ErrInvalidArgument, "fremdes Gebäude als Präfix")
	_, err = e.call("RentObject", "create", map[string]any{"data": unit("1000", "LpzBrn1", "WOHNEN", "lpzbrn1wg001")})
	expect(t, err, sdk.ErrAlreadyExists, "doppelt (Groß-/Kleinschreibung)")
	_, err = e.call("RentObject", "create", map[string]any{"data": unit("1000", "LpzBrn1", "POOL", "")})
	expect(t, err, sdk.ErrInvalidArgument, "Nutzungsart nur für Pools")
	if b := e.create("Building", map[string]any{"company_code": "1000", "entity_id": "LpzBrn", "designation": "Hinterhaus"}); b["building_id"] != "LpzBrn2" {
		t.Fatalf("zweites Gebäude: %v", b["building_id"])
	}
	_, err = e.call("Building", "create", map[string]any{"data": map[string]any{"company_code": "1000", "entity_id": "LpzBrn", "building_id": "Foo1", "designation": "x"}})
	expect(t, err, sdk.ErrInvalidArgument, "Gebäude-ID ohne Präfix")
}

// TestOneMask: Alle Arten in einer Maske – Art ist Pflicht und fest, Felder,
// die zur Art nicht passen, werden geleert; die Liste filtert nach Art.
func TestOneMask(t *testing.T) {
	e := setup(t)
	e.house()
	u := unit("1000", "LpzBrn1", "WOHNEN", "")
	u["total_area"], u["floor"] = 99, "EG"
	w := e.create("RentObject", u)
	if w["kind"] != kindUnit || w["total_area"] != nil || w["floor"] != "EG" {
		t.Fatalf("Mieteinheit: %v", w)
	}
	e.create("RentObject", space("1000", "LpzBrn1", "LAGER", ""))
	if n := len(items(e.must("RentObject", "list", map[string]any{"query": map[string]any{"kind": kindUnit}}))); n != 1 {
		t.Fatalf("Filter Art: %d", n)
	}
	if n := len(items(e.must("RentObject", "list", map[string]any{"query": map[string]any{"building_id": "LpzBrn1"}}))); n != 2 {
		t.Fatalf("alle Arten: %d", n)
	}
	noKind := unit("1000", "LpzBrn1", "WOHNEN", "")
	delete(noKind, "kind")
	_, err := e.call("RentObject", "create", map[string]any{"data": noKind})
	expect(t, err, sdk.ErrInvalidArgument, "ohne Art")
	_, err = e.call("RentObject", "create", map[string]any{"data": obj("GARAGE", "1000", "LpzBrn1", "WOHNEN", "")})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Art")
	// Art ist fest.
	id := "1000|" + w["object_id"].(string)
	_, err = e.call("RentObject", "update", map[string]any{"id": id, "data": map[string]any{"kind": kindPool}})
	expect(t, err, sdk.ErrInvalidArgument, "Art ändern")
	if got := e.must("RentObject", "update", map[string]any{"id": id, "data": map[string]any{"designation": "Neu", "total_area": 5}}); got["total_area"] != nil {
		t.Fatalf("Pool-Feld an Mieteinheit: %v", got)
	}
}

// TestCatalogsPerCompany: Jeder Buchungskreis hat seine Kataloge.
func TestCatalogsPerCompany(t *testing.T) {
	e := setup(t)
	e.house() // legt die Kataloge für 1000 an
	if n := len(items(e.must("UsageType", "list", map[string]any{"query": map[string]any{"company_code": "2000"}}))); n != 0 {
		t.Fatalf("2000 ohne Kataloge: %d", n)
	}
	r := e.must(setupObject, "setupCompany", map[string]any{"company": "2000"})
	if r["created"].(int) == 0 {
		t.Fatalf("Setup: %v", r)
	}
	// 2000 nennt Wohnungen WE statt WG.
	e.must("UsageType", "update", map[string]any{"id": "2000|WOHNEN", "data": map[string]any{"id_prefix": "WE"}})
	e.create("BusinessEntity", map[string]any{"company_code": "2000", "entity_id": "HamAlt", "designation": "Hamburg Altona"})
	e.create("Building", map[string]any{"company_code": "2000", "entity_id": "HamAlt", "designation": "Haus 1"})
	if u := e.create("RentObject", unit("2000", "HamAlt1", "WOHNEN", "")); u["object_id"] != "HamAlt1WE001" {
		t.Fatalf("Kürzel je Buchungskreis: %v", u["object_id"])
	}
	_, err := e.call("UsageType", "create", map[string]any{"data": map[string]any{"company_code": "2000", "code": "X", "name": "x", "id_prefix": "WE"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kürzel doppelt")
	_, err = e.call("RentObject", "create", map[string]any{"data": map[string]any{"company_code": "1000", "building_id": "LpzBrn1",
		"designation": "x", "usage_type": "WOHNEN", "floor": "OG9"}})
	expect(t, err, sdk.ErrInvalidArgument, "Geschoss nicht im Katalog")
}

// TestCompositeUnits: Vertragsobjekt aus Wohnung, Stellplatz und Fläche; ein
// Objekt gehört zu einem Zeitpunkt zu höchstens einem Vertragsobjekt.
func TestCompositeUnits(t *testing.T) {
	e := setup(t)
	e.house()
	w := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	s := e.create("RentObject", unit("1000", "LpzBrn1", "STELLPLATZ", ""))["object_id"]
	f := e.create("RentObject", space("1000", "LpzBrn1", "KELLER", ""))["object_id"]
	c := e.create("RentObject", composite("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	if c != "LpzBrn1WG002" {
		t.Fatalf("Vertragsobjekt-ID: %v", c)
	}
	for _, o := range []any{w, s, f} {
		e.create("CompositeItem", map[string]any{"company_code": "1000", "composite_id": c, "object_id": o, "valid_from": "2026-01-01"})
	}
	c2 := e.create("RentObject", composite("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	_, err := e.call("CompositeItem", "create", map[string]any{"data": map[string]any{"company_code": "1000", "composite_id": c2, "object_id": w, "valid_from": "2026-06-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Wohnung doppelt vergeben")
	_, err = e.call("CompositeItem", "create", map[string]any{"data": map[string]any{"company_code": "1000", "composite_id": c2, "object_id": c, "valid_from": "2026-06-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Vertragsobjekt im Vertragsobjekt")
	// Zuordnung beenden, danach darf das zweite Vertragsobjekt die Wohnung haben.
	e.must("CompositeItem", "expire", map[string]any{"id": "1000|" + c.(string) + "|" + w.(string) + "|2026-01-01", "valid_to": "2026-05-31"})
	e.create("CompositeItem", map[string]any{"company_code": "1000", "composite_id": c2, "object_id": w, "valid_from": "2026-06-01"})
}

// TestMeasurementsAndPool: Bemessungen je Ebene, Standard-Maßeinheit, Pool-Prüfung.
func TestMeasurementsAndPool(t *testing.T) {
	e := setup(t)
	e.house()
	w := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	m := e.create("Measurement", map[string]any{"company_code": "1000", "object_id": w, "measurement_type": "WFL", "value": 72.5, "valid_from": "2026-01-01"})
	if m["unit"] != "M2" || m["object_level"] != "UNIT" {
		t.Fatalf("Bemessung: %v", m)
	}
	if m := e.create("Measurement", map[string]any{"company_code": "1000", "object_id": "LpzBrn", "measurement_type": "MEA", "value": 1000, "valid_from": "2026-01-01"}); m["object_level"] != "ENTITY" || m["unit"] != "TSD" {
		t.Fatalf("MEA: %v", m)
	}
	_, err := e.call("Measurement", "create", map[string]any{"data": map[string]any{"company_code": "1000", "object_id": "Gibtsnicht", "measurement_type": "WFL", "value": 1}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Objekt")

	pool := e.create("RentObject", map[string]any{"kind": "POOL", "company_code": "1000", "building_id": "LpzBrn1", "designation": "Hoffläche",
		"usage_type": "FREIFLAECHE", "area_type": "NFL", "total_area": 100})["object_id"]
	space := func() any {
		return e.create("RentObject", map[string]any{"kind": "SPACE", "company_code": "1000", "building_id": "LpzBrn1", "designation": "Teilfläche",
			"usage_type": "FREIFLAECHE", "pool_id": pool})["object_id"]
	}
	s1, s2 := space(), space()
	e.create("Measurement", map[string]any{"company_code": "1000", "object_id": s1, "measurement_type": "NFL", "value": 60, "valid_from": "2026-01-01"})
	_, err = e.call("Measurement", "create", map[string]any{"data": map[string]any{"company_code": "1000", "object_id": s2, "measurement_type": "NFL", "value": 50, "valid_from": "2026-01-01"}})
	expect(t, err, sdk.ErrInvalidArgument, "Pool überschritten")
	if err != nil && !strings.Contains(err.Error(), "110") {
		t.Fatalf("Meldung: %v", err)
	}
	e.create("Measurement", map[string]any{"company_code": "1000", "object_id": s2, "measurement_type": "NFL", "value": 40, "valid_from": "2026-01-01"})
	// Andere Bemessungsart zählt nicht; Gesamtfläche verkleinern scheitert.
	e.create("Measurement", map[string]any{"company_code": "1000", "object_id": s2, "measurement_type": "WFL", "value": 500, "valid_from": "2026-01-01"})
	_, err = e.call("RentObject", "update", map[string]any{"id": "1000|" + pool.(string), "data": map[string]any{"total_area": 90}})
	expect(t, err, sdk.ErrInvalidArgument, "Pool zu klein")
}

// TestVisibility: Daten nur im Buchungskreis bzw. Gebäude mit Leserecht.
func TestVisibility(t *testing.T) {
	e := setup(t)
	e.house()
	e.create("Building", map[string]any{"company_code": "1000", "entity_id": "LpzBrn", "designation": "Hinterhaus"})
	e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))
	e.create("RentObject", unit("1000", "LpzBrn2", "WOHNEN", ""))
	e.h.rules["RentObject.read"] = []sdk.GrantRule{{CompanyCodes: []string{"1000"}, Fields: map[string][]sdk.ValueRange{"building_id": {{Low: "LpzBrn2"}}}}}
	got := items(e.must("RentObject", "list", nil))
	if len(got) != 1 || got[0]["object_id"] != "LpzBrn2WG001" {
		t.Fatalf("nur Hinterhaus: %v", got)
	}
	_, err := e.call("RentObject", "get", map[string]any{"id": "1000|LpzBrn1WG001"})
	expect(t, err, sdk.ErrNotFound, "Vorderhaus unsichtbar")
	e.h.rules["RentObject.read"] = []sdk.GrantRule{{CompanyCodes: []string{"2000"}}}
	if n := len(items(e.must("BusinessEntity", "list", nil))); n != 0 {
		t.Fatalf("anderer Buchungskreis: %d", n)
	}
}

// TestPostingCheck: Hook ledger.posting/check – Mietobjekt im Buchungskreis und gültig.
func TestPostingCheck(t *testing.T) {
	e := setup(t)
	e.house()
	w := e.create("RentObject", map[string]any{"kind": kindUnit, "company_code": "1000", "building_id": "LpzBrn1", "designation": "W1", "usage_type": "WOHNEN",
		"valid_from": "2026-01-01", "valid_to": "2026-12-31"})["object_id"].(string)
	check := func(cc, date, obj string) []hook.Message {
		t.Helper()
		resp, err := e.p.Handle(e.ctx, sdk.Request{Object: postingCheckCallback, Action: hook.CallbackAction, Payload: map[string]any{
			"hook": "ledger.posting", "action": "check", "data": map[string]any{
				"request": map[string]any{"company_code": cc, "posting_date": date},
				"lines":   []any{map[string]any{"line": 1, "dims": map[string]any{"rent_object_id": obj}}, map[string]any{"line": 2}},
			}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp.Payload.(hook.Response).Messages
	}
	if msgs := check("1000", "2026-10-01", w); len(msgs) != 0 {
		t.Fatalf("gültig: %v", msgs)
	}
	if msgs := check("1000", "2027-01-05", w); len(msgs) != 1 || msgs[0].ID != "RE-002" {
		t.Fatalf("abgelaufen: %v", msgs)
	}
	if msgs := check("2000", "2026-10-01", w); len(msgs) != 1 || msgs[0].ID != "RE-001" || msgs[0].Field != "rent_object_id" {
		t.Fatalf("anderer Buchungskreis: %v", msgs)
	}
}

// TestFreeIDs: Einstellung id_prefix_required = false – übernommene Schlüssel
// ohne Präfix der übergeordneten Ebene; vorgeschlagene IDs bleiben hierarchisch.
func TestFreeIDs(t *testing.T) {
	e := setup(t)
	e.house()
	off := false
	e.m.settings.IDPrefixRequired = &off
	if b := e.create("Building", map[string]any{"company_code": "1000", "entity_id": "LpzBrn", "building_id": "ROAlt1", "designation": "Altbau"}); b["building_id"] != "ROAlt1" {
		t.Fatalf("freie Gebäude-ID: %v", b["building_id"])
	}
	if u := e.create("RentObject", unit("1000", "ROAlt1", "WOHNEN", "RUA101")); u["object_id"] != "RUA101" {
		t.Fatalf("freie Objekt-ID: %v", u["object_id"])
	}
	if u := e.create("RentObject", unit("1000", "ROAlt1", "WOHNEN", "")); u["object_id"] != "ROAlt1WG001" {
		t.Fatalf("Vorschlag: %v", u["object_id"])
	}
	_, err := e.call("RentObject", "create", map[string]any{"data": unit("1000", "ROAlt1", "WOHNEN", "rua101")})
	expect(t, err, sdk.ErrAlreadyExists, "eindeutig bleibt")
}

// TestBuildingArea: Bei einer Art mit Flächenprüfung (OWN) dürfen die
// Mieteinheiten zusammen nicht mehr Wohnfläche haben als das Gebäude; bei WEG
// (nur einzelne Wohnungen erfasst) wird nicht geprüft.
func TestBuildingArea(t *testing.T) {
	e := setup(t)
	e.house() // WEG
	w1 := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	w2 := e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))["object_id"]
	wfl := func(obj any, v float64) error {
		_, err := e.call("Measurement", "create", map[string]any{"data": map[string]any{"company_code": "1000", "object_id": obj,
			"measurement_type": "WFL", "value": v, "valid_from": "2026-01-01"}})
		return err
	}
	for _, err := range []error{wfl("LpzBrn1", 100), wfl(w1, 70)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := wfl(w2, 50); err != nil {
		t.Fatalf("WEG ohne Prüfung: %v", err)
	}
	e.must("BusinessEntity", "update", map[string]any{"id": "1000|LpzBrn", "data": map[string]any{"entity_type": "OWN"}})
	e.must("Measurement", "update", map[string]any{"id": "1000|" + w2.(string) + "|WFL|2026-01-01", "data": map[string]any{"value": 30}})
	_, err := e.call("Measurement", "update", map[string]any{"id": "1000|" + w2.(string) + "|WFL|2026-01-01", "data": map[string]any{"value": 31}})
	expect(t, err, sdk.ErrInvalidArgument, "Eigenbestand: Summe über Gebäude")
	_, err = e.call("Measurement", "update", map[string]any{"id": "1000|LpzBrn1|WFL|2026-01-01", "data": map[string]any{"value": 99}})
	expect(t, err, sdk.ErrInvalidArgument, "Gebäude kleiner als Summe")
	e.must("EntityType", "update", map[string]any{"id": "1000|OWN", "data": map[string]any{"area_check": false}})
	e.must("Measurement", "update", map[string]any{"id": "1000|" + w2.(string) + "|WFL|2026-01-01", "data": map[string]any{"value": 31}})
}
