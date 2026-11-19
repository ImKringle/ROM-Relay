package web

import (
	"net/http"
	"strings"
	"testing"
)

// TestAnOverlongPasswordIsRefusedWithAMessageNotA500 pins the bcrypt 72-byte
// limit: validCredentials must reject what HashPassword cannot hash, or the
// user's chosen password dies as "could not secure the password".
func TestAnOverlongPasswordIsRefusedWithAMessageNotA500(t *testing.T) {
	f := newSaveVaultFixture(t)
	client := f.signInWithTempPassword(t, "misty")

	long := strings.Repeat("a", 73)
	res := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/account/password", map[string]string{"password": long, "confirm": long}, f.ui.csrf)
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a 73-char password must be a 400 with guidance, not %d", res.StatusCode)
	}
	if body := readBody(t, res); !strings.Contains(body, "1-72") {
		t.Fatal("the refusal must tell the user the real length limit")
	}

	// 72 exactly is bcrypt's maximum and must succeed.
	ok := strings.Repeat("b", 72)
	res2 := sendJSON(t, client, http.MethodPost, f.server.URL+"/api/account/password", map[string]string{"password": ok, "confirm": ok}, f.ui.csrf)
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusNoContent {
		t.Fatalf("a 72-char password is valid and must be accepted: got %d", res2.StatusCode)
	}
}
