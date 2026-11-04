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
}
