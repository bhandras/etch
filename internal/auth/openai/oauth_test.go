package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLoginDeviceCompletesDeviceFlow verifies the Codex-compatible device
// endpoints are called in order and persisted credentials contain token data.
func TestLoginDeviceCompletesDeviceFlow(t *testing.T) {
	var sawUserCode bool
	var sawPoll bool
	var sawExchange bool
	idToken := testJWT(t, map[string]any{
		"exp": time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-test",
		},
	})
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/accounts/deviceauth/usercode":
				sawUserCode = true
				var body struct {
					ClientID string `json:"client_id"`
				}
				if err := json.NewDecoder(r.Body).Decode(
					&body,
				); err != nil {

					t.Fatal(err)
				}
				if body.ClientID != "test-client" {
					t.Fatalf("unexpected client id: %q",
						body.ClientID)
				}
				fmt.Fprint(
					w,
					`{"device_auth_id":"dev-1",`+
						`"user_code":"ABCD-EFGH",`+
						`"interval":"1"}`,
				)

			case "/api/accounts/deviceauth/token":
				sawPoll = true
				fmt.Fprint(
					w,
					`{"authorization_code":"code-1",`+
						`"code_verifier":"verifier-1"}`,
				)

			case "/oauth/token":
				sawExchange = true
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				assertFormValue(
					t, r.Form, "grant_type",
					"authorization_code",
				)
				assertFormValue(
					t, r.Form, "client_id", "test-client",
				)
				assertFormValue(t, r.Form, "code", "code-1")
				assertFormValue(
					t, r.Form, "code_verifier",
					"verifier-1",
				)
				fmt.Fprintf(
					w,
					`{"access_token":"access-1",`+
						`"refresh_token":"refresh-1",`+
						`"id_token":%q}`,
					idToken,
				)

			default:
				t.Fatalf("unexpected path: %q", r.URL.Path)
			}
		}),
	)
	defer server.Close()

	var progress []LoginProgress
	creds, err := LoginDevice(context.Background(), Options{
		Issuer:       server.URL,
		ClientID:     "test-client",
		PollInterval: time.Millisecond,
		PollTimeout:  time.Second,
	}, func(event LoginProgress) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawUserCode || !sawPoll || !sawExchange {
		t.Fatalf("missing device flow calls: user=%v poll=%v "+
			"exchange=%v", sawUserCode, sawPoll, sawExchange)
	}
	if len(progress) == 0 ||
		progress[0].DeviceCode.UserCode != "ABCD-EFGH" {

		t.Fatalf("missing device progress: %#v", progress)
	}
	if progress[0].DeviceCode.PollInterval != time.Second {
		t.Fatalf("unexpected poll interval: %v",
			progress[0].DeviceCode.PollInterval)
	}
	if creds.Tokens.AccessToken != "access-1" ||
		creds.Tokens.RefreshToken != "refresh-1" ||
		creds.Tokens.AccountID != "acct-test" {

		t.Fatalf("unexpected credentials: %#v", creds)
	}
}

// TestRefreshUpdatesStoredTokens verifies refresh requests use stored defaults
// and keep old token fields when the endpoint omits replacements.
func TestRefreshUpdatesStoredTokens(t *testing.T) {
	var sawRefresh bool
	server := httptest.NewServer(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/oauth/token" {
				t.Fatalf("unexpected path: %q", r.URL.Path)
			}
			sawRefresh = true
			var body struct {
				ClientID     string `json:"client_id"`
				GrantType    string `json:"grant_type"`
				RefreshToken string `json:"refresh_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(
				&body,
			); err != nil {

				t.Fatal(err)
			}
			if body.ClientID != "stored-client" ||
				body.GrantType != "refresh_token" ||
				body.RefreshToken != "old-refresh" {

				t.Fatalf("unexpected refresh body: %#v", body)
			}
			fmt.Fprint(w, `{"access_token":"new-access"}`)
		}),
	)
	defer server.Close()

	creds, err := Refresh(context.Background(), Credentials{
		Issuer:       server.URL,
		ClientID:     "stored-client",
		CodexBaseURL: "http://codex.local",
		Tokens: TokenData{
			AccessToken:  "old-access",
			RefreshToken: "old-refresh",
			IDToken:      "old-id",
			AccountID:    "old-account",
		},
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !sawRefresh {
		t.Fatal("refresh endpoint was not called")
	}
	if creds.Tokens.AccessToken != "new-access" ||
		creds.Tokens.RefreshToken != "old-refresh" ||
		creds.Tokens.IDToken != "old-id" ||
		creds.Tokens.AccountID != "old-account" {

		t.Fatalf("unexpected refreshed credentials: %#v", creds)
	}
}

// TestSaveLoadAndLogout verifies local auth storage round-trips through a
// private JSON file and can be removed without leaking token content.
func TestSaveLoadAndLogout(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".harness", "auth", AuthFileName)
	creds := Credentials{
		Tokens: TokenData{
			AccessToken: "access",
		},
	}
	if err := Save(path, creds); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != authFilePerm {
		t.Fatalf("unexpected file mode: %v", info.Mode().Perm())
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AuthMode != authModeChatGPT ||
		loaded.Tokens.AccessToken != "access" ||
		loaded.Issuer != DefaultIssuer ||
		loaded.CodexBaseURL != DefaultCodexBaseURL {

		t.Fatalf("unexpected loaded credentials: %#v", loaded)
	}

	removed, err := Logout(path)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected logout to remove a file")
	}
	if _, err := Load(path); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("expected not logged in after logout, got %v", err)
	}
}

// TestNeedsRefreshUsesJWTExpiry verifies token refresh decisions prefer the
// bearer token expiry when it can be decoded.
func TestNeedsRefreshUsesJWTExpiry(t *testing.T) {
	now := time.Now()
	fresh := Credentials{Tokens: TokenData{
		AccessToken: testJWT(t, map[string]any{
			"exp": now.Add(time.Hour).Unix(),
		}),
	}}
	if NeedsRefresh(fresh, now) {
		t.Fatal("fresh token should not need refresh")
	}

	expiring := Credentials{Tokens: TokenData{
		AccessToken: testJWT(t, map[string]any{
			"exp": now.Add(time.Minute).Unix(),
		}),
	}}
	if !NeedsRefresh(expiring, now) {
		t.Fatal("near-expiry token should need refresh")
	}
}

// assertFormValue fails the test if form lacks the expected value.
func assertFormValue(t *testing.T, form url.Values, key string, want string) {
	t.Helper()
	if got := form.Get(key); got != want {
		t.Fatalf("unexpected form %s: want %q got %q", key, want, got)
	}
}

// testJWT returns an unsigned JWT-like string with the provided payload.
func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "none"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}

	return base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(body) + ".sig"
}
