// Package deezer is a minimal Deezer gateway (gw-light) client: ARL login,
// favorites enumeration, track-URL resolution and BF_CBC_STRIPE decryption.
package deezer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sync"
)

const (
	gwURL     = "https://www.deezer.com/ajax/gw-light.php"
	mediaURL  = "https://media.deezer.com/v1/get_url"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) deeznt"
)

// Client is a logged-in Deezer session. It is safe for concurrent use.
type Client struct {
	http     *http.Client
	gwURL    string // overridable for tests
	mediaURL string // overridable for tests

	mu       sync.Mutex
	arl      string
	apiToken string

	// Populated after login.
	userID            int64
	licenseToken      string
	canStreamHQ       bool
	canStreamLossless bool
	country           string
	loggedIn          bool
}

// New creates a client for the given ARL cookie.
func New(arl string) (*Client, error) {
	return newClient(arl, nil, gwURL, mediaURL)
}

// NewWithBaseURLs creates a client with custom base URLs — used in tests to
// point at a local mock server.
func NewWithBaseURLs(arl, gwBaseURL, mediaBaseURL string) (*Client, error) {
	return newClient(arl, nil, gwBaseURL, mediaBaseURL)
}

func newClient(arl string, httpClient *http.Client, gw, media string) (*Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	h := httpClient
	if h == nil {
		h = &http.Client{Jar: jar}
	} else {
		h.Jar = jar
	}
	u, _ := url.Parse("https://www.deezer.com")
	jar.SetCookies(u, []*http.Cookie{{
		Name: "arl", Value: arl, Path: "/", Domain: ".deezer.com",
	}})
	c := &Client{
		http:     h,
		gwURL:    gw,
		mediaURL: media,
		arl:      arl,
	}
	return c, nil
}

// CanStreamLossless reports whether the account can fetch FLAC.
func (c *Client) CanStreamLossless() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.canStreamLossless }

// CanStreamHQ reports whether the account can fetch MP3_320.
func (c *Client) CanStreamHQ() bool { c.mu.Lock(); defer c.mu.Unlock(); return c.canStreamHQ }

// UserID returns the logged-in user id (0 before Login).
func (c *Client) UserID() int64 { c.mu.Lock(); defer c.mu.Unlock(); return c.userID }

// apiCall performs one gw-light method call. It bootstraps/refreshes the api
// token as needed and retries once on an invalid-token error.
func (c *Client) apiCall(ctx context.Context, method string, args any) (json.RawMessage, error) {
	return c.apiCallRetry(ctx, method, args, true)
}

func (c *Client) apiCallRetry(ctx context.Context, method string, args any, retry bool) (json.RawMessage, error) {
	c.mu.Lock()
	token := c.apiToken
	c.mu.Unlock()

	if token == "" && method != "deezer.getUserData" {
		if err := c.Login(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		token = c.apiToken
		c.mu.Unlock()
	}
	if method == "deezer.getUserData" {
		token = "null"
	}

	body, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}

	q := url.Values{}
	q.Set("api_version", "1.0")
	q.Set("input", "3")
	q.Set("method", method)
	q.Set("api_token", token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gwURL+"?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyErr(err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gw %s: http %d", method, resp.StatusCode)
	}

	var parsed struct {
		Error   json.RawMessage `json:"error"`
		Results json.RawMessage `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("gw %s: decode: %w", method, err)
	}

	if gwErr := parseGWError(parsed.Error); gwErr != "" {
		if retry && isInvalidTokenErr(gwErr) {
			c.mu.Lock()
			c.apiToken = ""
			c.mu.Unlock()
			if err := c.Login(ctx); err != nil {
				return nil, err
			}
			return c.apiCallRetry(ctx, method, args, false)
		}
		if isRateLimitErr(gwErr) {
			return nil, ErrRateLimited
		}
		return nil, fmt.Errorf("gw %s: %s", method, gwErr)
	}
	return parsed.Results, nil
}

// Login fetches user data and caches the api/license tokens and capabilities.
func (c *Client) Login(ctx context.Context) error {
	results, err := c.apiCallRetry(ctx, "deezer.getUserData", struct{}{}, false)
	if err != nil {
		return err
	}
	var ud userData
	if err := json.Unmarshal(results, &ud); err != nil {
		return fmt.Errorf("getUserData: %w", err)
	}
	if ud.User.UserID == 0 || ud.CheckForm == "" {
		return fmt.Errorf("login failed: invalid or expired ARL")
	}
	c.mu.Lock()
	c.apiToken = ud.CheckForm
	c.userID = ud.User.UserID
	c.licenseToken = ud.User.Options.LicenseToken
	c.canStreamHQ = ud.User.Options.WebHQ || ud.User.Options.MobileHQ
	c.canStreamLossless = ud.User.Options.WebLossless || ud.User.Options.MobileLossless
	c.country = ud.User.Options.LicenseCountry
	c.loggedIn = true
	c.mu.Unlock()
	return nil
}

func parseGWError(raw json.RawMessage) string {
	s := string(bytes.TrimSpace(raw))
	if s == "" || s == "[]" || s == "{}" || s == "null" {
		return ""
	}
	return s
}

func isInvalidTokenErr(s string) bool {
	return bytes.Contains([]byte(s), []byte("invalid api token")) ||
		bytes.Contains([]byte(s), []byte("Invalid CSRF token")) ||
		bytes.Contains([]byte(s), []byte("VALID_TOKEN_REQUIRED"))
}
