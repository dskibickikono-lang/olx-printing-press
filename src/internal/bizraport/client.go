// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// Package bizraport is a small client for the bizraport.pl company-registry
// API (https://api.bizraport.pl). It resolves Polish companies to registry
// data (NIP, KRS, REGON, address, legal form) for the `enrich` command.
//
// Authentication is by email + password passed as query-string parameters.
// Because credentials travel in the URL, this client NEVER includes the
// full URL (or its query) in errors or logs — HTTPError carries only the
// request path. Callers must keep the same discipline.
package bizraport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL   = "https://api.bizraport.pl"
	defaultUserAgent = "olx-pp-cli/0.1 (+sales-intel; bizraport-enrich)"
	// Conservative default; bizraport bills per returned row and documents
	// no hard rate limit, so politeness is on us.
	defaultPerSec = 1.0
)

// HTTPError is returned for non-2xx responses. It deliberately omits the
// query string (which holds email + password) — only the path is kept.
type HTTPError struct {
	StatusCode int
	Path       string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("bizraport %d from %s: %s", e.StatusCode, e.Path, strings.TrimSpace(e.Body))
}

// Options configures a Client. Zero-valued fields fall back to defaults.
type Options struct {
	HTTPClient *http.Client
	BaseURL    string
	Email      string
	Password   string
	UserAgent  string
	PerSec     float64
}

// Client talks to api.bizraport.pl.
type Client struct {
	HTTP      *http.Client
	BaseURL   string
	UserAgent string

	email    string
	password string
	limiter  *rate.Limiter
}

// New returns a configured Client. Defaults to a 30s HTTP timeout.
func New(opts Options) *Client {
	c := &Client{
		HTTP:      opts.HTTPClient,
		BaseURL:   opts.BaseURL,
		UserAgent: opts.UserAgent,
		email:     opts.Email,
		password:  opts.Password,
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.UserAgent == "" {
		c.UserAgent = defaultUserAgent
	}
	perSec := opts.PerSec
	if perSec <= 0 {
		perSec = defaultPerSec
	}
	c.limiter = rate.NewLimiter(rate.Limit(perSec), 1)
	return c
}

// HasCredentials reports whether email and password are both set.
func (c *Client) HasCredentials() bool {
	return c.email != "" && c.password != ""
}

// Informacje is the subset of `informacje_o_firmie` we map onto a company.
type Informacje struct {
	Nazwa            string
	NIP              string
	REGON            string
	FormaPrawna      string
	Ulica            string
	KodPocztowy      string
	Miejscowosc      string
	Wojewodztwo      string
	KapitalZakladowy string
	Email            string
	StronaWWW        string
}

// CompanyProfile is the parsed, flattened /api/dane response. Raw holds the
// verbatim payload for caching.
type CompanyProfile struct {
	KRS     string
	NIP     string
	KodPKD  string
	OpisPKD string
	Info    Informacje
	Raw     json.RawMessage
}

// GetByKRS fetches a full company profile by KRS number.
func (c *Client) GetByKRS(ctx context.Context, krs string) (*CompanyProfile, error) {
	return c.dane(ctx, "krs", krs)
}

// GetByNIP fetches a full company profile by NIP.
func (c *Client) GetByNIP(ctx context.Context, nip string) (*CompanyProfile, error) {
	return c.dane(ctx, "nip", nip)
}

func (c *Client) dane(ctx context.Context, key, val string) (*CompanyProfile, error) {
	raw, err := c.get(ctx, "/api/dane", url.Values{key: {val}})
	if err != nil {
		return nil, err
	}
	return ParseProfile(raw)
}

// ParseProfile flattens a raw /api/dane payload into a CompanyProfile.
// Exported so cached responses can be reused without a network call.
//
// The live API wraps results in {"data":[{...}]} and expresses
// informacje_o_firmie as an array of {nazwa_pola, wartosc} pairs.
func ParseProfile(raw json.RawMessage) (*CompanyProfile, error) {
	var wrapper struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, fmt.Errorf("decode /api/dane envelope: %w", err)
	}
	if len(wrapper.Data) == 0 {
		return nil, nil
	}
	item := wrapper.Data[0]

	var top struct {
		KRS           string          `json:"krs"`
		NIPRaw        json.RawMessage `json:"nip"`
		KodPKD        string          `json:"kod_pkd"`
		OpisPKD       string          `json:"opis_pkd"`
		InformacjeRaw json.RawMessage `json:"informacje_o_firmie"`
	}
	if err := json.Unmarshal(item, &top); err != nil {
		return nil, fmt.Errorf("decode /api/dane item: %w", err)
	}

	p := &CompanyProfile{
		KRS:     keepDigits(top.KRS),
		NIP:     rawToString(top.NIPRaw),
		KodPKD:  top.KodPKD,
		OpisPKD: top.OpisPKD,
		Raw:     raw,
	}
	p.Info = parseInformacje(top.InformacjeRaw)
	if p.NIP == "" {
		p.NIP = p.Info.NIP
	}
	return p, nil
}

// parseInformacje handles the array-of-pairs format used by the live API
// and falls back to a JSON-object (or string-encoded object) format.
func parseInformacje(raw json.RawMessage) Informacje {
	if len(raw) == 0 {
		return Informacje{}
	}
	var pairs []struct {
		NazwaPola string `json:"nazwa_pola"`
		Wartosc   string `json:"wartosc"`
	}
	if err := json.Unmarshal(raw, &pairs); err == nil {
		m := make(map[string]string, len(pairs))
		for _, p := range pairs {
			m[p.NazwaPola] = p.Wartosc
		}
		return Informacje{
			Nazwa:            m["nazwa"],
			NIP:              m["nip"],
			REGON:            m["regon"],
			FormaPrawna:      m["forma_prawna"],
			Ulica:            m["ulica"],
			KodPocztowy:      m["kod_pocztowy"],
			Miejscowosc:      m["miejscowosc"],
			Wojewodztwo:      m["wojewodztwo"],
			KapitalZakladowy: m["kapital_zakladowy"],
			Email:            m["email"],
			StronaWWW:        m["adres_strony_internetowej"],
		}
	}
	// Fallback: JSON object or string-encoded object (legacy / future API change).
	if m := parseMaybeStringJSON(raw); m != nil {
		return Informacje{
			Nazwa:            asString(m["nazwa"]),
			NIP:              asString(m["nip"]),
			REGON:            asString(m["regon"]),
			FormaPrawna:      asString(m["forma_prawna"]),
			Ulica:            asString(m["ulica"]),
			KodPocztowy:      asString(m["kod_pocztowy"]),
			Miejscowosc:      asString(m["miejscowosc"]),
			Wojewodztwo:      asString(m["wojewodztwo"]),
			KapitalZakladowy: asString(m["kapital_zakladowy"]),
			Email:            asString(m["email"]),
			StronaWWW:        asString(m["adres_strony_internetowej"]),
		}
	}
	return Informacje{}
}

// rawToString decodes a json.RawMessage that may be a string or number.
func rawToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return asString(v)
}

// Search resolves a free-text query (company name, NIP, KRS or REGON) to a
// list of KRS numbers. The bool reports whether results were truncated
// (>100k matches or a `limit` was applied). Two-step enrichment: feed these
// KRS numbers to GetByKRS.
func (c *Client) Search(ctx context.Context, q string) ([]string, bool, error) {
	raw, err := c.get(ctx, "/api/szukaj", url.Values{"q": {q}})
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		Data []struct {
			KRS string `json:"krs"`
		} `json:"data"`
		DaneUciete bool `json:"dane_uciete"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, false, fmt.Errorf("decode /api/szukaj: %w", err)
	}
	out := make([]string, 0, len(resp.Data))
	for _, d := range resp.Data {
		// KRS is a 10-digit number; keep only digits to shed any padding/
		// obfuscation characters from the response.
		if k := keepDigits(d.KRS); k != "" {
			out = append(out, k)
		}
	}
	return out, resp.DaneUciete, nil
}

// Usage is the /api/zuzycie monitoring response (month-to-date).
type Usage struct {
	Miesiac       string         `json:"miesiac"`
	Zuzycie       map[string]int `json:"zuzycie"`
	KosztNettoPLN float64        `json:"koszt_netto_pln"`
}

// Usage returns month-to-date request counts and net cost.
func (c *Client) Usage(ctx context.Context) (*Usage, error) {
	raw, err := c.get(ctx, "/api/zuzycie", nil)
	if err != nil {
		return nil, err
	}
	var u Usage
	if err := json.Unmarshal(raw, &u); err != nil {
		return nil, fmt.Errorf("decode /api/zuzycie: %w", err)
	}
	return &u, nil
}

// --- HTTP helpers --------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, params url.Values) (json.RawMessage, error) {
	if !c.HasCredentials() {
		return nil, errors.New("bizraport: missing credentials — set [bizraport] email/password in config or BIZRAPORT_EMAIL/BIZRAPORT_PASSWORD")
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	q := url.Values{}
	for k, vs := range params {
		q[k] = vs
	}
	q.Set("email", c.email)
	q.Set("password", c.password)
	u := c.BaseURL + path + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	return c.do(req, path)
}

// do executes the request with backoff on 429/5xx. The path argument is
// used in errors instead of req.URL so credentials never leak.
func (c *Client) do(req *http.Request, path string) (json.RawMessage, error) {
	const maxAttempts = 4
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := c.HTTP.Do(req)
		if err != nil {
			// Never surface err.Error() verbatim: url.Error embeds the full
			// URL (with credentials). Report the path only.
			lastErr = fmt.Errorf("bizraport request to %s failed", path)
		} else {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				if rerr != nil {
					return nil, fmt.Errorf("read body: %w", rerr)
				}
				return json.RawMessage(body), nil
			case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500:
				lastErr = &HTTPError{StatusCode: resp.StatusCode, Path: path, Body: string(body)}
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				continue
			default:
				return nil, &HTTPError{StatusCode: resp.StatusCode, Path: path, Body: string(body)}
			}
		}
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	if lastErr == nil {
		lastErr = errors.New("bizraport request failed after retries")
	}
	return nil, lastErr
}

// parseMaybeStringJSON unmarshals a field that is either a JSON string
// containing serialized JSON (the documented shape) or a real JSON object.
func parseMaybeStringJSON(raw json.RawMessage) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var m map[string]any
	if trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(raw, &inner); err != nil {
			return nil
		}
		if json.Unmarshal([]byte(inner), &m) != nil {
			return nil
		}
		return m
	}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		// JSON numbers decode to float64; render without trailing zeros.
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", t), "0"), ".")
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func keepDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
