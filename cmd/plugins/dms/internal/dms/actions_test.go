package dms

import (
	"slices"
	"testing"
)

// unavailableActions: je Zustand nur die möglichen Aktionen.
func TestUnavailableActions(t *testing.T) {
	has := func(l []any, a string) bool { return slices.Contains(l, any(a)) }
	in := unavailableActions(stInbox, wfWaiting, "", "2026-10-18")
	if has(in, "continue") || has(in, "file") || !has(in, "remove") || !has(in, "reopen") {
		t.Errorf("Eingang, wartend: %v", in)
	}
	if done := unavailableActions(stInbox, wfDone, "", "2026-10-18"); !has(done, "continue") || has(done, "startOver") {
		t.Errorf("Eingang, fertig: %v", done)
	}
	filed := unavailableActions(stFiled, wfDone, "2034-12-31", "2026-10-18")
	if !has(filed, "continue") || !has(filed, "file") || !has(filed, "remove") || has(filed, "addRef") || has(filed, "aiCreateLater") {
		t.Errorf("abgelegt, Frist läuft: %v", filed)
	}
	if expired := unavailableActions(stFiled, "", "2020-12-31", "2026-10-18"); has(expired, "remove") {
		t.Errorf("abgelegt, Frist abgelaufen: %v", expired)
	}
	if d := unavailableActions(stDiscarded, "", "", "2026-10-18"); has(d, "reopen") || !has(d, "addRef") {
		t.Errorf("verworfen: %v", d)
	}
}
