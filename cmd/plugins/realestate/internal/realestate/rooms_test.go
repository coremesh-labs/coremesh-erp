package realestate

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestRoomBook (0.4.0): Räume mit laufender Nummer, Bauteile aus der Raumart.
func TestRoomBook(t *testing.T) {
	e := setup(t)
	e.house()
	e.create("RentObject", unit("1000", "LpzBrn1", "WOHNEN", ""))
	r := e.create("Room", map[string]any{"company_code": "1000", "object_id": "LpzBrn1WG001", "room_type": "BAD", "area": 6.5})
	if r["room_no"] != "01" || r["designation"] != "Bad" {
		t.Fatalf("Raum: %v", r)
	}
	r = e.create("Room", map[string]any{"company_code": "1000", "object_id": "LpzBrn1WG001", "room_type": "KIND", "designation": "Gretchens Zimmer"})
	if r["room_no"] != "02" || toFloat(r["sort_order"]) != 20 {
		t.Fatalf("zweiter Raum: %v", r)
	}
	comps := items(e.must("RoomComponent", "list", map[string]any{"query": map[string]any{"company_code": "1000", "object_id": "LpzBrn1WG001", "room_no": "01"}}))
	if len(comps) != 9 {
		t.Fatalf("Bauteile Bad: %d %v", len(comps), comps)
	}
	e.must("RoomComponent", "update", map[string]any{"id": "1000|LpzBrn1WG001|01|BODEN", "data": map[string]any{"description": "Fliesen grau 30×60"}})
	_, err := e.call("Room", "create", map[string]any{"data": map[string]any{"company_code": "1000", "object_id": "GIBTSNICHT", "room_type": "BAD"}})
	expect(t, err, sdk.ErrInvalidArgument, "Mietobjekt fehlt")
	_, err = e.call("RoomType", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "SAUNA", "name": "Sauna", "components": "BODEN,OFEN"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Bauteil")
	e.must("RoomComponent", "remove", map[string]any{"id": "1000|LpzBrn1WG001|01|BODEN"})
	if n := len(items(e.must("RoomComponent", "list", map[string]any{"query": map[string]any{"company_code": "1000", "object_id": "LpzBrn1WG001", "room_no": "01"}}))); n != 8 {
		t.Fatalf("nach Entfernen: %d", n)
	}
}

// TestRoomCodeMapping (0.5.0): einheitliche Kürzel der Raumarten, Kürzelschema eines
// Messdienstes und Abbildung seiner Kürzel; resolve löst Abbildung, Kürzel und Schlüssel auf.
func TestRoomCodeMapping(t *testing.T) {
	e := setup(t)
	e.house()
	rt := e.must("RoomType", "get", map[string]any{"id": "1000|ANBAUWC"})
	if rt["short"] != "AWC" || rt["name"] != "Anbau-WC" {
		t.Fatalf("Raumart Anbau-WC: %v", rt)
	}
	_, err := e.call("RoomType", "update", map[string]any{"id": "1000|KELLER", "data": map[string]any{"short": "ku"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kürzel doppelt")
	if tr := e.must("RoomType", "get", map[string]any{"id": "1000|TECHNIK"}); tr["short"] != "TR" || tr["din277"] != "TF" {
		t.Fatalf("Technikraum: %v", tr)
	}
	_, err = e.call("RoomType", "update", map[string]any{"id": "1000|KELLER", "data": map[string]any{"din277": "NUF9"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte DIN-277-Gruppe")
	if k := e.must("RoomType", "update", map[string]any{"id": "1000|KELLER", "data": map[string]any{"din277": "nuf4"}}); k["din277"] != "NUF4" {
		t.Fatalf("Keller DIN 277: %v", k)
	}
	e.create("CodeScheme", map[string]any{"company_code": "1000", "code": "WAGNER", "name": "Messdienst Wagner"})
	e.create("RoomTypeMap", map[string]any{"company_code": "1000", "scheme": "wagner", "ext_code": "K", "room_type": "KUECHE"})
	e.create("RoomTypeMap", map[string]any{"company_code": "1000", "scheme": "WAGNER", "ext_code": "Kl", "room_type": "kind", "designation": "Kinderzimmer klein"})
	_, err = e.call("RoomTypeMap", "create", map[string]any{"data": map[string]any{"company_code": "1000", "scheme": "WAGNER", "ext_code": "KL", "room_type": "KIND"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kürzel im Schema doppelt")
	_, err = e.call("RoomTypeMap", "create", map[string]any{"data": map[string]any{"company_code": "1000", "scheme": "MEPHISTO", "ext_code": "X", "room_type": "BAD"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekanntes Schema")
	_, err = e.call("RoomTypeMap", "create", map[string]any{"data": map[string]any{"company_code": "1000", "scheme": "WAGNER", "ext_code": "S", "room_type": "SAUNA"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Raumart")
	for _, c := range []struct{ scheme, code, rt, des, via string }{
		{"WAGNER", "k", "KUECHE", "Küche", "MAP"}, {"WAGNER", "KL", "KIND", "Kinderzimmer klein", "MAP"},
		{"WAGNER", "wz", "WOHNEN", "Wohnzimmer", "SHORT"}, {"", "AWC", "ANBAUWC", "Anbau-WC", "SHORT"}, {"", "kueche", "KUECHE", "Küche", "CODE"},
	} {
		r := e.must("RoomTypeMap", "resolve", map[string]any{"company_code": "1000", "scheme": c.scheme, "code": c.code})
		if r["room_type"] != c.rt || r["designation"] != c.des || r["via"] != c.via {
			t.Fatalf("resolve %s/%s: %v", c.scheme, c.code, r)
		}
	}
	_, err = e.call("RoomTypeMap", "resolve", map[string]any{"company_code": "1000", "scheme": "WAGNER", "code": "HY"})
	expect(t, err, sdk.ErrNotFound, "unbekanntes Kürzel")
}
