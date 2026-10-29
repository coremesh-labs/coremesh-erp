package procurement

import (
	"context"
	"strings"

	"github.com/coremesh-labs/coremesh/pkg/sdk"
	"github.com/coremesh-labs/coremesh/pkg/sdk/crud"
	"github.com/coremesh-labs/coremesh/pkg/sdk/metamodel"
)

// Ablage im DMS (seit 0.15.0): am Angebot und am Auftrag wählbar. Zur Auswahl stehen
// die Ablagen der Dokumentarten (Vorschlags-Ablage und „Ablagen zur Auswahl“ in der
// Dokumentenverwaltung), die das DMS über DmsDocument.fileAccounts liefert. Das DMS legt
// Dokumente zum Angebot bzw. Auftrag dort ab (Feld file_account am Kategorieobjekt);
// leer = Ablagefindung über Kostenart bzw. Sachkonto.

// fileAccountOptions: Ablagen zur Auswahl und Vorschlag; ok = false ohne DMS.
func (m *Module) fileAccountOptions(ctx context.Context, cc, object string) (opts []metamodel.Option, def string, ok bool) {
	resp, err := m.services.Call(ctx, "DmsDocument", "fileAccounts", map[string]any{"company_code": cc, "object": object})
	if err != nil {
		return nil, "", false
	}
	var out struct {
		Options []metamodel.Option `json:"options"`
		Default string             `json:"default"`
	}
	if sdk.Decode(resp.Payload, &out) != nil {
		return nil, "", false
	}
	return out.Options, out.Default, true
}

// fileAccountState: Auswahl im Formular, beim Anlegen der Vorschlag.
func (m *Module) fileAccountState(ctx context.Context, st *metamodel.FormState, cc, object string, req metamodel.FormStateRequest) {
	opts, def, ok := m.fileAccountOptions(ctx, cc, object)
	if !ok {
		return
	}
	fs := metamodel.FieldState{Options: opts}
	if req.Mode == "create" && req.Values["file_account"] == "" && def != "" {
		fs.Value = &def
	}
	st.Fields["file_account"] = fs
}

// checkFileAccount: gewählte Ablage muss zur Auswahl gehören (ohne DMS keine Prüfung).
func (m *Module) checkFileAccount(ctx context.Context, rec crud.Record, object string) error {
	acc := strings.TrimSpace(crud.Str(rec["file_account"]))
	rec["file_account"] = nilIfEmpty(acc)
	if acc == "" {
		return nil
	}
	opts, _, ok := m.fileAccountOptions(ctx, crud.Str(rec["company_code"]), object)
	if !ok {
		return nil
	}
	var valid []string
	for _, o := range opts {
		if o.Value == acc {
			return nil
		}
		valid = append(valid, o.Value)
	}
	if len(valid) == 0 {
		return crud.Invalid("Ablage %s: keine Ablagen zur Auswahl – in der Dokumentenverwaltung an der Dokumentart „Ablagen zur Auswahl“ eintragen", acc)
	}
	return crud.Invalid("Ablage %s steht nicht zur Auswahl (%s)", acc, strings.Join(valid, ", "))
}
