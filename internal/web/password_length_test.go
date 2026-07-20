package web

import (
	"net/http"
	"net/url"
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
	form := url.Values{"password": {long}, "confirm": {long}}
	res, err := client.Post(f.server.URL+passwordChangePath, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a 73-char password must be a 400 with guidance, not %d", res.StatusCode)
	}
	if body := readBody(t, res); !strings.Contains(body, "1-72") {
		t.Fatal("the refusal must tell the user the real length limit")
	}

	// 72 exactly is bcrypt's maximum and must succeed.
	ok := strings.Repeat("b", 72)
	res2, err := client.Post(f.server.URL+passwordChangePath, "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"password": {ok}, "confirm": {ok}}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusSeeOther {
		t.Fatalf("a 72-char password is valid and must be accepted: got %d", res2.StatusCode)
	}
}
