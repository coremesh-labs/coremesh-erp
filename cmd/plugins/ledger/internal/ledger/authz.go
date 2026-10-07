package ledger

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/camel/coremesh/pkg/sdk"
	"github.com/camel/coremesh/pkg/sdk/metamodel"
)

// Berechtigungen beim Buchen – bis auf Feldwerte (iam: Rolle → Berechtigung
// je Object.Action → Feldwerte). Die Objects deklarieren ihre
// Berechtigungsfelder im Metamodell; die Rollenpflege bietet sie aus dem
// Catalog an.
//
//	FiscalPeriod.post   in dieser Periode buchen (je Kontoart der Positionen)
//	                    Felder ledger, account_kind, fiscal_year, posting_period
//	                    z. B. posting_period 1..12 (Sonderperioden 13–16 nur für einige)
//	DocumentType.post   Belege dieser Belegart buchen
//	                    Feld code, z. B. KR, KG (Kreditorenrechnungen) oder SA
//
// Beide gelten zusätzlich zu JournalEntry.post im Buchungskreis und bei
// jedem Buchungsweg: Vorerfassung, PostingService der Fachmodule, Storno.

var (
	periodAuthorization = &metamodel.Authorization{
		Fields:  []string{"ledger", "account_kind", "fiscal_year", "posting_period"},
		Actions: []metamodel.AuthAction{{Name: "post", Label: "In der Periode buchen"}},
	}
	documentTypeAuthorization = &metamodel.Authorization{
		Fields:  []string{"code"},
		Actions: []metamodel.AuthAction{{Name: "post", Label: "Belege der Belegart buchen"}},
	}
)

func periodAttrs(cc, ledger, kind string, year, period int) sdk.Attrs {
	return sdk.Attrs{sdk.AttrCompanyCode: cc, "ledger": ledger, "account_kind": kind, "fiscal_year": strconv.Itoa(year), "posting_period": strconv.Itoa(period)}
}

// authorizePosting prüft Periode (je Kontoart der Positionen) und Belegart für
// den aufrufenden Benutzer.
func (m *Module) authorizePosting(ctx context.Context, cc, ledger string, year, period int, docType string, kinds []string) error {
	g, err := sdk.Grants(ctx, "FiscalPeriod", "post")
	if err != nil {
		return err
	}
	for _, kind := range kinds {
		if !g.Allows(periodAttrs(cc, ledger, kind, year, period)) {
			return fmt.Errorf("%w: keine Berechtigung, in Periode %d/%d (Ledger %s, Kontoart %s, Buchungskreis %s) zu buchen",
				sdk.ErrPermissionDenied, period, year, ledger, kind, cc)
		}
	}
	ok, err := sdk.Authorize(ctx, "DocumentType", "post", sdk.Attrs{sdk.AttrCompanyCode: cc, "code": docType})
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: keine Berechtigung, Belege der Belegart %s im Buchungskreis %s zu buchen", sdk.ErrPermissionDenied, docType, cc)
	}
	return nil
}

// allowedSpecialPeriods: Sonderperioden des Monats (Periodendefinition), in
// denen der Benutzer buchen darf.
func (m *Module) allowedSpecialPeriods(ctx context.Context, cc, ledger string, year int, month time.Month) ([]int, error) {
	g, err := sdk.Grants(ctx, "FiscalPeriod", "post")
	if err != nil {
		return nil, err
	}
	defs, err := m.periodDefs(ctx, cc)
	if err != nil {
		return nil, err
	}
	var out []int
	for _, d := range defs {
		if d.Special && d.Month == int(month) && g.Allows(periodAttrs(cc, ledger, "S", year, d.Period)) {
			out = append(out, d.Period)
		}
	}
	return out, nil
}
