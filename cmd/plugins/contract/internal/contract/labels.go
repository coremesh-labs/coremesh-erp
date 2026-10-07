package contract

import (
	"context"

	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
)

// withLabels: Felder mit Schlüsseln zeigen den Text – Kataloge des
// Buchungskreises, Partnerrollen, Geschäftspartner (Partnermodul), Sachkonten
// (Kontenfindung) und Objekte (Immobilienverwaltung).
func (m *Module) withLabels(e *crud.Entity) {
	catalogs := map[string]string{ // Feld → Tabelle (Spalte code)
		"contract_type":  "contract__contract_type",
		"condition_type": "contract__condition_type",
	}
	decorate := e.Decorate
	e.Decorate = func(ctx context.Context, rec crud.Record) error {
		if decorate != nil {
			if err := decorate(ctx, rec); err != nil {
				return err
			}
		}
		cc := crud.Str(rec["company_code"])
		l := labels(rec)
		for _, f := range e.Fields {
			v := crud.Str(rec[f.Key])
			if v == "" || f.Lookup == nil {
				continue
			}
			switch {
			case catalogs[f.Key] != "" && e.Table != catalogs[f.Key]:
				if t := m.text(ctx, "SELECT name FROM "+catalogs[f.Key]+" WHERE company_code = ? AND code = ?", cc, v); t != "" {
					l[f.Key] = t
				}
			case f.Key == "role_code" && e.Object != "ContractPartnerRole", f.Key == "main_role":
				if t := m.text(ctx, "SELECT name FROM contract__partner_role WHERE company_code = ? AND role_code = ?", cc, v); t != "" {
					l[f.Key] = t
				}
			case f.Lookup.Object == "BusinessPartner":
				if t := m.partnerName(ctx, v); t != "" {
					l[f.Key] = t
				}
			case f.Key == "account_number" && e.Object != "ContractAccount":
				if t := m.text(ctx, "SELECT account_name FROM contract__account WHERE company_code = ? AND contract_type = ? AND account_number = ?",
					cc, rec["contract_type"], v); t != "" {
					l[f.Key] = v + " " + t
				}
			case f.Key == "object_id" && e.Object == "ContractObject":
				if o, err := m.objectOf(ctx, cc, crud.Str(rec["object_type"]), v); err == nil && o.Designation != "" {
					l[f.Key] = v + " " + o.Designation
				}
			}
		}
		return nil
	}
}

// text: erster Wert einer Abfrage ("" bei Fehler oder ohne Treffer).
func (m *Module) text(ctx context.Context, sql string, args ...any) string {
	res, err := m.db.Query(ctx, sql, args...)
	if err != nil || len(res.Rows) == 0 {
		return ""
	}
	return crud.Str(res.Rows[0][0])
}
