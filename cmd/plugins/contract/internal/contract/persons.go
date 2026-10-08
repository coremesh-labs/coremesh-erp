package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Personen im Mietobjekt je Vertrag (optional, Zeitscheiben) – nur nötig, wo
// ein Verteilerschlüssel nach Personen umlegt (Nebenkostenabrechnung).
const personsObject = "ContractPersons"

func (m *Module) persons() *crud.Entity {
	return &crud.Entity{
		Object: personsObject, Title: "Personen", Icon: "icon-users", Table: "contract__persons", Section: "Verträge",
		Keys: []string{"company_code", "contract_id", "valid_from"}, TimeSlice: true, Order: "company_code, contract_id, valid_from",
		Filters: []string{"company_code", "contract_id"},
		Fields: crud.WithTimeSlice(
			crud.Field{Key: "company_code", Label: "Buchungskreis", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: lookupCC},
			crud.Field{Key: "contract_id", Label: "Vertrag", Type: tText, Required: true, Listable: true, Immutable: true, Lookup: contractLookup},
			crud.Field{Key: "persons", Label: "Personen (Umlage nach Personen)", Type: tNum, Required: true, Listable: true},
			crud.Field{Key: "note", Label: "Bemerkung", Type: tText},
		),
		Access: &crud.Access{Object: "Contract", Records: true, CompanyCode: "company_code"},
		Validate: func(ctx context.Context, rec, _ crud.Record) error {
			c, err := m.contractOf(ctx, crud.Str(rec["company_code"]), crud.Str(rec["contract_id"]))
			if err != nil {
				return err
			}
			if err := within("Personen", crud.Str(rec["valid_from"]), crud.Str(rec["valid_to"]), c); err != nil {
				return err
			}
			if toInt(rec["persons"]) < 0 {
				return crud.Invalid("Personen: 0 oder mehr")
			}
			return nil
		},
	}
}

var personsSection = metamodel.SectionDefinition{Key: "personen", Title: "Personen", Collapsed: true,
	Relation: &metamodel.Relation{Object: personsObject, ForeignKey: "contract_id", Match: match(), Columns: []string{"persons", "valid_from", "valid_to", "note"}}}
