package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultIssuer is the OpenAI auth origin used for device login and
	// token refresh.
	DefaultIssuer = "https://auth.openai.com"

	// DefaultClientID is the Codex OAuth client identifier used by the
	// public Codex-compatible device flow.
	DefaultClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

	// DefaultCodexBaseURL is the OpenAI Codex backend used with ChatGPT
	// subscription OAuth tokens.
	DefaultCodexBaseURL = "https://chatgpt.com/backend-api/codex"

	// AuthFileName is the file name used under .harness/auth for OpenAI
	// OAuth credentials.
	AuthFileName = "openai.json"

	// authModeChatGPT identifies ChatGPT/Codex OAuth credentials on disk.
	authModeChatGPT = "chatgpt"

	// authDirPerm is the private directory mode used for credential state.
	authDirPerm os.FileMode = 0o700

	// authFilePerm is the private file mode used for credential state.
	authFilePerm os.FileMode = 0o600

	// defaultPollTimeout is the maximum time a device login waits for the
	// browser authorization step.
	defaultPollTimeout = 15 * time.Minute

	// defaultPollInterval is the fallback polling interval when the server
	// does not provide one.
	defaultPollInterval = 2 * time.Second

	// refreshSkew is the amount of time before expiry that triggers a
	// refresh to avoid racing an in-flight model request.
	refreshSkew = 5 * time.Minute

	// refreshAgeLimit is the conservative refresh age for tokens without a
	// parseable JWT expiry.
	refreshAgeLimit = 8 * time.Hour
)

// ErrNotLoggedIn reports that no stored OpenAI OAuth credentials exist.
var ErrNotLoggedIn = errors.New("openai oauth credentials not found")

// Credentials is the JSON shape stored in the local OpenAI auth file.
type Credentials struct {
	// AuthMode records the credential family, currently "chatgpt".
	AuthMode string `json:"auth_mode,omitempty"`

	// Tokens stores bearer, refresh, and optional identity token data.
	Tokens TokenData `json:"tokens"`

	// LastRefresh records when Tokens were last obtained or refreshed.
	LastRefresh time.Time `json:"last_refresh,omitempty"`

	// Issuer records the auth origin used to refresh the credentials.
	Issuer string `json:"issuer,omitempty"`

	// ClientID records the OAuth client identifier for refresh requests.
	ClientID string `json:"client_id,omitempty"`

	// CodexBaseURL records the preferred backend for OAuth model calls.
	CodexBaseURL string `json:"codex_base_url,omitempty"`
}

// TokenData stores the token fields needed for Codex-style model access.
type TokenData struct {
	// IDToken is the raw JWT identity token returned by the auth server.
	IDToken string `json:"id_token,omitempty"`

	// AccessToken is the bearer token sent to the Codex backend.
	AccessToken string `json:"access_token"`

	// RefreshToken is exchanged for a new access token when needed.
	RefreshToken string `json:"refresh_token,omitempty"`

	// AccountID is the ChatGPT workspace/account identifier when present.
	AccountID string `json:"account_id,omitempty"`
}

// Options configures the device login and refresh HTTP endpoints.
type Options struct {
	// Issuer is the auth origin. Empty means DefaultIssuer.
	Issuer string

	// ClientID is the OAuth client id. Empty means DefaultClientID.
	ClientID string

	// CodexBaseURL is the preferred model backend for resulting tokens.
	CodexBaseURL string

	// HTTPClient performs requests. http.DefaultClient is used when nil.
	HTTPClient *http.Client

	// PollInterval overrides the device authorization polling interval.
	PollInterval time.Duration

	// PollTimeout overrides the device authorization timeout.
	PollTimeout time.Duration
}

// DeviceCode is the authorization challenge shown to the user.
type DeviceCode struct {
	// DeviceAuthID identifies the pending browser authorization.
	DeviceAuthID string

	// UserCode is entered by the user in the browser.
	UserCode string

	// VerificationURL is the browser page that accepts UserCode.
	VerificationURL string

	// PollInterval is the server-requested polling cadence.
	PollInterval time.Duration
}

// LoginProgress describes one user-visible device login state change.
type LoginProgress struct {
	// DeviceCode is populated when the user must visit the browser URL.
	DeviceCode DeviceCode

	// Message is a short status string safe to print in a terminal.
	Message string
}

// DefaultStorePath returns the OpenAI auth file path under root.
func DefaultStorePath(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("auth root must not be empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve auth root: %w", err)
	}

	return filepath.Join(absolute, ".harness", "auth", AuthFileName), nil
}

// Load reads stored credentials from path.
func Load(path string) (Credentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Credentials{}, ErrNotLoggedIn
		}

		return Credentials{}, fmt.Errorf("read openai auth: %w", err)
	}

	var creds Credentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return Credentials{}, fmt.Errorf("decode openai auth: %w", err)
	}
	creds.fillDefaults()
	if creds.Tokens.AccessToken == "" {
		return Credentials{}, fmt.Errorf("openai auth missing access " +
			"token")
	}

	return creds, nil
}

// Save writes credentials to path with private file permissions.
func Save(path string, creds Credentials) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path must not be empty")
	}
	creds.fillDefaults()
	if creds.Tokens.AccessToken == "" {
		return fmt.Errorf("openai auth missing access token")
	}
	if err := os.MkdirAll(filepath.Dir(path), authDirPerm); err != nil {
		return fmt.Errorf("create openai auth dir: %w", err)
	}

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("encode openai auth: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), "."+AuthFileName+".")
	if err != nil {
		return fmt.Errorf("create openai auth temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(authFilePerm); err != nil {
		tmp.Close()

		return fmt.Errorf("chmod openai auth temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()

		return fmt.Errorf("write openai auth temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close openai auth temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace openai auth file: %w", err)
	}
	if err := os.Chmod(path, authFilePerm); err != nil {
		return fmt.Errorf("chmod openai auth file: %w", err)
	}

	return nil
}

// Logout removes stored credentials and reports whether a file was removed.
func Logout(path string) (bool, error) {
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("remove openai auth: %w", err)
	}

	return true, nil
}

// AccessTokenFromEnv returns an externally supplied Codex access token.
func AccessTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("CODEX_ACCESS_TOKEN"))
}

// EnsureAccessToken loads credentials, refreshes them when needed, and saves
// any refreshed token data.
func EnsureAccessToken(ctx context.Context, path string,
	opts Options) (Credentials, error) {

	creds, err := Load(path)
	if err != nil {
		return Credentials{}, err
	}
	if !NeedsRefresh(creds, time.Now()) {
		return creds, nil
	}
	if creds.Tokens.RefreshToken == "" {
		return Credentials{}, fmt.Errorf("openai auth token expired " +
			"and has no refresh token")
	}
	refreshed, err := Refresh(ctx, creds, opts)
	if err != nil {
		return Credentials{}, err
	}
	if err := Save(path, refreshed); err != nil {
		return Credentials{}, err
	}

	return refreshed, nil
}

// NeedsRefresh reports whether credentials should be refreshed before use.
func NeedsRefresh(creds Credentials, now time.Time) bool {
	if expiresAt, ok := ParseJWTExpiration(creds.Tokens.AccessToken); ok {
		return !expiresAt.After(now.Add(refreshSkew))
	}
	if expiresAt, ok := ParseJWTExpiration(creds.Tokens.IDToken); ok {
		return !expiresAt.After(now.Add(refreshSkew))
	}
	if creds.LastRefresh.IsZero() {
		return false
	}

	return now.Sub(creds.LastRefresh) > refreshAgeLimit
}

// LoginDevice completes the device authorization flow and returns credentials.
func LoginDevice(ctx context.Context, opts Options,
	progress func(LoginProgress)) (Credentials, error) {

	device, err := RequestDeviceCode(ctx, opts)
	if err != nil {
		return Credentials{}, err
	}
	if progress != nil {
		progress(LoginProgress{DeviceCode: device})
	}
	auth, err := PollAuthorization(ctx, opts, device)
	if err != nil {
		return Credentials{}, err
	}
	if progress != nil {
		progress(LoginProgress{Message: "authorization approved"})
	}
	tokens, err := ExchangeAuthorization(ctx, opts, auth)
	if err != nil {
		return Credentials{}, err
	}
	creds := Credentials{
		AuthMode:     authModeChatGPT,
		Tokens:       tokens,
		LastRefresh:  time.Now().UTC(),
		Issuer:       optionIssuer(opts),
		ClientID:     optionClientID(opts),
		CodexBaseURL: optionCodexBaseURL(opts),
	}
	creds.fillDefaults()

	return creds, nil
}

// RequestDeviceCode starts the device authorization flow.
func RequestDeviceCode(ctx context.Context, opts Options) (DeviceCode, error) {
	body := struct {
		ClientID string `json:"client_id"`
	}{
		ClientID: optionClientID(opts),
	}
	data, err := json.Marshal(body)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("encode device code "+
			"request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		optionIssuer(opts)+"/api/accounts/deviceauth/usercode",
		bytes.NewReader(data),
	)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("create device code "+
			"request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	var response struct {
		DeviceAuthID    string          `json:"device_auth_id"`
		UserCode        string          `json:"user_code"`
		VerificationURL string          `json:"verification_uri"`
		Interval        json.RawMessage `json:"interval"`
	}
	if err := doJSON(opts, req, &response); err != nil {
		return DeviceCode{}, fmt.Errorf("request device code: %w", err)
	}
	if response.DeviceAuthID == "" || response.UserCode == "" {
		return DeviceCode{}, fmt.Errorf("device code response " +
			"missing id or user code")
	}
	if response.VerificationURL == "" {
		response.VerificationURL = optionIssuer(opts) + "/codex/device"
	}
	interval := optionPollInterval(opts)
	intervalSeconds, err := parseIntervalSeconds(response.Interval)
	if err != nil {
		return DeviceCode{}, fmt.Errorf("parse device poll "+
			"interval: %w", err)
	}
	if intervalSeconds > 0 {
		interval = time.Duration(intervalSeconds) * time.Second
	}

	return DeviceCode{
		DeviceAuthID:    response.DeviceAuthID,
		UserCode:        response.UserCode,
		VerificationURL: response.VerificationURL,
		PollInterval:    interval,
	}, nil
}

// parseIntervalSeconds accepts the numeric and string interval encodings seen
// from Codex-compatible device auth endpoints.
func parseIntervalSeconds(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0, nil
		}
		value, err := strconv.Atoi(text)
		if err != nil {
			return 0, fmt.Errorf("string interval %q: %w", text,
				err)
		}

		return value, nil
	}

	var number int
	if err := json.Unmarshal(raw, &number); err != nil {
		return 0, err
	}

	return number, nil
}

// PollAuthorization waits until the browser grants an authorization code.
func PollAuthorization(ctx context.Context, opts Options,
	device DeviceCode) (authorizationData, error) {

	timeout := optionPollTimeout(opts)
	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := device.PollInterval
	if interval <= 0 {
		interval = optionPollInterval(opts)
	}
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-pollCtx.Done():
			return authorizationData{}, fmt.Errorf("device "+
				"authorization timed out after %s", timeout)

		case <-timer.C:
			auth, pending, err := pollAuthorizationOnce(
				pollCtx, opts, device,
			)
			if err != nil {
				return authorizationData{}, err
			}
			if !pending {
				return auth, nil
			}
			timer.Reset(interval)
		}
	}
}

// ExchangeAuthorization trades an approved browser code for bearer tokens.
func ExchangeAuthorization(ctx context.Context, opts Options,
	auth authorizationData) (TokenData, error) {

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", optionClientID(opts))
	form.Set("code", auth.AuthorizationCode)
	form.Set("code_verifier", auth.CodeVerifier)
	form.Set("redirect_uri", optionIssuer(opts)+"/deviceauth/callback")

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, optionIssuer(opts)+"/oauth/token",
		strings.NewReader(
			form.Encode(),
		),
	)
	if err != nil {
		return TokenData{}, fmt.Errorf("create token exchange "+
			"request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var response tokenResponse
	if err := doJSON(opts, req, &response); err != nil {
		return TokenData{}, fmt.Errorf("exchange authorization: %w",
			err)
	}

	return response.tokenData(), nil
}

// Refresh exchanges a refresh token for updated bearer credentials.
func Refresh(ctx context.Context, creds Credentials,
	opts Options) (Credentials, error) {

	creds.fillDefaults()
	body := struct {
		ClientID     string `json:"client_id"`
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
	}{
		ClientID:     optionClientID(opts.withCredentials(creds)),
		GrantType:    "refresh_token",
		RefreshToken: creds.Tokens.RefreshToken,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Credentials{}, fmt.Errorf("encode refresh request: %w",
			err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		optionIssuer(opts.withCredentials(creds))+"/oauth/token",
		bytes.NewReader(data),
	)
	if err != nil {
		return Credentials{}, fmt.Errorf("create refresh request: %w",
			err)
	}
	req.Header.Set("Content-Type", "application/json")

	var response tokenResponse
	if err := doJSON(opts, req, &response); err != nil {
		return Credentials{}, fmt.Errorf("refresh openai auth: %w", err)
	}

	tokens := response.tokenData()
	if tokens.AccessToken == "" {
		tokens.AccessToken = creds.Tokens.AccessToken
	}
	if tokens.IDToken == "" {
		tokens.IDToken = creds.Tokens.IDToken
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = creds.Tokens.RefreshToken
	}
	if tokens.AccountID == "" {
		tokens.AccountID = creds.Tokens.AccountID
	}
	creds.Tokens = tokens
	creds.LastRefresh = time.Now().UTC()
	creds.fillDefaults()

	return creds, nil
}

// ParseJWTExpiration extracts the exp claim from a JWT without validating the
// signature.
func ParseJWTExpiration(token string) (time.Time, bool) {
	payload, ok := decodeJWTPayload(token)
	if !ok {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	if claims.Exp == 0 {
		return time.Time{}, false
	}

	return time.Unix(claims.Exp, 0).UTC(), true
}

// ParseChatGPTClaims extracts non-secret account hints from an id token.
func ParseChatGPTClaims(token string) (string, string) {
	payload, ok := decodeJWTPayload(token)
	if !ok {
		return "", ""
	}
	var claims struct {
		Email   string `json:"email"`
		Profile struct {
			Email string `json:"email"`
		} `json:"https://api.openai.com/profile"`
		Auth struct {
			AccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", ""
	}
	email := claims.Email
	if email == "" {
		email = claims.Profile.Email
	}

	return email, claims.Auth.AccountID
}

// authorizationData stores the browser-approved OAuth verifier material.
type authorizationData struct {
	// AuthorizationCode is exchanged for the final bearer token.
	AuthorizationCode string

	// CodeVerifier completes the PKCE-style token exchange.
	CodeVerifier string
}

// tokenResponse mirrors the token fields returned by the OAuth endpoint.
type tokenResponse struct {
	// AccessToken is the bearer token used for model requests.
	AccessToken string `json:"access_token"`

	// RefreshToken renews the bearer token.
	RefreshToken string `json:"refresh_token"`

	// IDToken is the raw identity JWT.
	IDToken string `json:"id_token"`
}

// tokenData converts an OAuth response into persisted token data.
func (r tokenResponse) tokenData() TokenData {
	_, accountID := ParseChatGPTClaims(r.IDToken)

	return TokenData{
		IDToken:      r.IDToken,
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		AccountID:    accountID,
	}
}

// fillDefaults completes omitted compatibility fields on credentials.
func (c *Credentials) fillDefaults() {
	if c.AuthMode == "" {
		c.AuthMode = authModeChatGPT
	}
	if c.Issuer == "" {
		c.Issuer = DefaultIssuer
	}
	if c.ClientID == "" {
		c.ClientID = DefaultClientID
	}
	if c.CodexBaseURL == "" {
		c.CodexBaseURL = DefaultCodexBaseURL
	}
	if c.Tokens.AccountID == "" {
		_, c.Tokens.AccountID = ParseChatGPTClaims(c.Tokens.IDToken)
	}
}

// withCredentials fills empty option values from stored credentials.
func (o Options) withCredentials(creds Credentials) Options {
	if o.Issuer == "" {
		o.Issuer = creds.Issuer
	}
	if o.ClientID == "" {
		o.ClientID = creds.ClientID
	}
	if o.CodexBaseURL == "" {
		o.CodexBaseURL = creds.CodexBaseURL
	}

	return o
}

// pollAuthorizationOnce performs one device authorization polling request.
func pollAuthorizationOnce(ctx context.Context, opts Options,
	device DeviceCode) (authorizationData, bool, error) {

	body := struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
	}{
		DeviceAuthID: device.DeviceAuthID,
		UserCode:     device.UserCode,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return authorizationData{}, false,
			fmt.Errorf("encode device poll request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost,
		optionIssuer(opts)+"/api/accounts/deviceauth/token",
		bytes.NewReader(data),
	)
	if err != nil {
		return authorizationData{}, false,
			fmt.Errorf("create device poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient(opts).Do(req)
	if err != nil {
		return authorizationData{}, false,
			fmt.Errorf("poll device authorization: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusForbidden ||
		resp.StatusCode == http.StatusNotFound {

		io.Copy(io.Discard, resp.Body)

		return authorizationData{}, true, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return authorizationData{}, false, readHTTPError(resp)
	}

	var response struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return authorizationData{}, false,
			fmt.Errorf("decode device authorization: %w", err)
	}
	if response.AuthorizationCode == "" || response.CodeVerifier == "" {
		return authorizationData{}, false,
			fmt.Errorf("device authorization missing code or " +
				"verifier")
	}

	return authorizationData{
		AuthorizationCode: response.AuthorizationCode,
		CodeVerifier:      response.CodeVerifier,
	}, false, nil
}

// doJSON sends req and decodes a successful JSON response into out.
func doJSON(opts Options, req *http.Request, out any) error {
	resp, err := httpClient(opts).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readHTTPError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}

// readHTTPError renders a bounded response body for endpoint diagnostics.
func readHTTPError(resp *http.Response) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return fmt.Errorf("http %s and read error body: %w",
			resp.Status, err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return fmt.Errorf("http %s", resp.Status)
	}

	return fmt.Errorf("http %s: %s", resp.Status, text)
}

// decodeJWTPayload decodes the payload segment of a JWT.
func decodeJWTPayload(token string) ([]byte, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}

	return payload, true
}

// httpClient returns the configured client or the package default.
func httpClient(opts Options) *http.Client {
	if opts.HTTPClient != nil {
		return opts.HTTPClient
	}

	return http.DefaultClient
}

// optionIssuer returns the normalized auth issuer.
func optionIssuer(opts Options) string {
	if opts.Issuer != "" {
		return strings.TrimRight(opts.Issuer, "/")
	}

	return DefaultIssuer
}

// optionClientID returns the configured OAuth client id.
func optionClientID(opts Options) string {
	if opts.ClientID != "" {
		return opts.ClientID
	}

	return DefaultClientID
}

// optionCodexBaseURL returns the normalized OAuth model backend.
func optionCodexBaseURL(opts Options) string {
	if opts.CodexBaseURL != "" {
		return strings.TrimRight(opts.CodexBaseURL, "/")
	}

	return DefaultCodexBaseURL
}

// optionPollInterval returns the device authorization polling cadence.
func optionPollInterval(opts Options) time.Duration {
	if opts.PollInterval > 0 {
		return opts.PollInterval
	}

	return defaultPollInterval
}

// optionPollTimeout returns the device authorization timeout.
func optionPollTimeout(opts Options) time.Duration {
	if opts.PollTimeout > 0 {
		return opts.PollTimeout
	}

	return defaultPollTimeout
}
