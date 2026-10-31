package asset

import (
	"testing"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
)

// TestAssetsPartsPersons: Kataloge beim ersten Objekt, sprechende IDs je
// Objektart, Bestandteile nach Katalog (ohne Kreis, ein Objekt je Zeitpunkt),
// exklusive Rollen.
func TestAssetsPartsPersons(t *testing.T) {
	e := setup(t)
	e.h.partners["BP1"], e.h.partners["BP2"] = "Abraham", "Isaak"
	car := e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "auto", "designation": "Kamelkarren", "valid_from": "2026-01-01"})
	if car["asset_id"] != "AU001" || car["status"] != "ACTIVE" || car["valid_to"] != "9999-12-31" {
		t.Fatalf("Auto: %v", car)
	}
	summer := e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "REIFEN", "designation": "Sommerreifen", "valid_from": "2026-01-01"})
	winter := e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "REIFEN", "designation": "Winterreifen", "valid_from": "2026-01-01"})
	if summer["asset_id"] != "RS001" || winter["asset_id"] != "RS002" {
		t.Fatalf("Reifen: %v %v", summer["asset_id"], winter["asset_id"])
	}
	_, err := e.call("Asset", "create", map[string]any{"data": map[string]any{"company_code": "1000", "asset_type": "REIFEN", "asset_id": "rs001", "designation": "x"}})
	expect(t, err, sdk.ErrAlreadyExists, "ID doppelt")
	_, err = e.call("Asset", "create", map[string]any{"data": map[string]any{"company_code": "1000", "asset_type": "RAKETE", "designation": "x"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannte Objektart")

	part := func(parent, child, from, to string) error {
		_, err := e.call("AssetPart", "create", map[string]any{"data": map[string]any{"company_code": "1000", "asset_id": parent, "part_id": child, "valid_from": from, "valid_to": to}})
		return err
	}
	if err := part("AU001", "RS002", "2026-01-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	if err := part("AU001", "RS001", "2026-04-01", "2026-10-31"); err != nil {
		t.Fatal(err)
	}
	expect(t, part("AU001", "AU001", "2026-01-01", ""), sdk.ErrInvalidArgument, "sich selbst")
	expect(t, part("RS001", "AU001", "2026-01-01", ""), sdk.ErrInvalidArgument, "Reifen erlaubt keine Bestandteile")
	// Geräte dürfen Geräte enthalten – aber kein Kreis.
	e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "GERAET", "designation": "Rechner", "valid_from": "2026-01-01"})
	e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "GERAET", "designation": "Bildschirm", "valid_from": "2026-01-01"})
	if err := part("GE001", "GE002", "2026-01-01", ""); err != nil {
		t.Fatal(err)
	}
	expect(t, part("GE002", "GE001", "2026-06-01", ""), sdk.ErrInvalidArgument, "Kreis")
	e.create("Asset", map[string]any{"company_code": "1000", "asset_type": "WOHNUNG", "designation": "Zelt in Hebron", "valid_from": "2026-01-01"})
	expect(t, part("WO001", "GE002", "2026-02-01", ""), sdk.ErrInvalidArgument, "schon Bestandteil")
	if parts := items(e.must("AssetPart", "list", map[string]any{"query": map[string]any{"company_code": "1000", "asset_id": "AU001", "includeHistory": "true"}})); len(parts) != 2 {
		t.Fatalf("Bestandteile: %v", parts)
	}

	person := func(role, bp, from string) error {
		_, err := e.call("AssetPerson", "create", map[string]any{"data": map[string]any{"company_code": "1000", "asset_id": "AU001", "role_code": role, "partner_id": bp, "valid_from": from}})
		return err
	}
	if err := person("HOLDER", "BP1", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	expect(t, person("HOLDER", "BP2", "2026-05-01"), sdk.ErrInvalidArgument, "Halter exklusiv")
	if err := person("DRIVER", "BP1", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	if err := person("DRIVER", "BP2", "2026-01-01"); err != nil {
		t.Fatal(err)
	}
	expect(t, person("DRIVER", "BP9", "2026-01-01"), sdk.ErrInvalidArgument, "unbekannte Person")
	got := e.must("AssetPerson", "get", map[string]any{"id": "1000|AU001|DRIVER|BP2|2026-01-01"})
	if labels, _ := got["_labels"].(map[string]any); labels["partner_id"] != "Isaak" || labels["role_code"] != "Fahrer" {
		t.Fatalf("Bezeichnungen: %v", got["_labels"])
	}
}

// TestAssetTypeCatalog: Kürzel eindeutig, Bestandteile nur bekannte Objektarten.
func TestAssetTypeCatalog(t *testing.T) {
	e := setup(t)
	e.must("AssetSetup", "setupCompany", map[string]any{"company": "1000"})
	_, err := e.call("AssetType", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "BOOT", "name": "Boot", "id_prefix": "au"}})
	expect(t, err, sdk.ErrInvalidArgument, "Kürzel doppelt")
	_, err = e.call("AssetType", "create", map[string]any{"data": map[string]any{"company_code": "1000", "code": "BOOT", "name": "Boot", "id_prefix": "BO", "part_types": "SEGEL"}})
	expect(t, err, sdk.ErrInvalidArgument, "unbekannter Bestandteil")
	b := e.create("AssetType", map[string]any{"company_code": "1000", "code": "boot", "name": "Boot", "id_prefix": "bo", "part_types": "geraet, boot"})
	if b["code"] != "BOOT" || b["id_prefix"] != "BO" || b["part_types"] != "GERAET,BOOT" {
		t.Fatalf("Objektart: %v", b)
	}
}
