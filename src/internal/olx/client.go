// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

// Package olx is a hand-written client for the public OLX.pl
// reverse-engineered HTTP/GraphQL surface. It targets two hosts:
//   - https://www.olx.pl  (REST under /api/v1, /api/v2; GraphQL gateway at /apigateway/graphql)
//   - https://jobs-api.olx.pl (employer profile GraphQL — optional)
//
// All endpoints are publicly readable and require no authentication.
// The HAR capture at ~/projects/har/olx/21www.olx.pl.har documents the
// shapes used here.
package olx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultBaseURL    = "https://www.olx.pl"
	defaultJobsAPIURL = "https://jobs-api.olx.pl"
	defaultUserAgent  = "olx-pp-cli/0.1 (+sales-intel; https://github.com/dskibickikono-lang/olx-pp-cli)"

	// Politeness defaults.
	defaultWWWPerSec    = 1.0 // requests/sec to www.olx.pl
	defaultJobsPerSec   = 0.5 // requests/sec to jobs-api.olx.pl
	defaultPhonesPerSec = 0.2 // requests/sec to /api/v1/offers/{id}/limited-phones/ — OLX's anti-abuse fires fast here
)

// ErrPhonesBlocked is returned by GetPhones once OLX's anti-abuse system
// has rejected a limited-phones request with the "suspicious activity"
// signal. Once tripped, every further GetPhones call returns this error
// immediately without hitting the network — repeated attempts only
// deepen the block and waste request budget.
var ErrPhonesBlocked = errors.New("olx: limited-phones endpoint blocked by anti-abuse system; suppressing further calls this session")

// HTTPError is the typed error do() returns for non-2xx final responses
// (after retries are exhausted on 429/5xx, or immediately for other 4xx).
// Callers that need to inspect the status code or body — most notably
// GetPhones, which has to detect OLX's "podejrzaną aktywność" 400 — can
// type-assert via errors.As.
type HTTPError struct {
	StatusCode int
	Host       string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%d from %s: %s", e.StatusCode, e.Host, strings.TrimSpace(e.Body))
}

// IsSuspiciousActivity reports whether the response is OLX's anti-abuse
// 400 with the "Nie możesz kontynuować, ponieważ wykryliśmy podejrzaną
// aktywność…" detail. Empirically this fires on the limited-phones
// endpoint after a few dozen rapid calls and is sticky for the session.
func (e *HTTPError) IsSuspiciousActivity() bool {
	if e == nil || e.StatusCode != http.StatusBadRequest {
		return false
	}
	return strings.Contains(e.Body, "podejrzan") // matches both "podejrzaną" and any spelling variant
}

// Client is a low-overhead, dependency-light OLX HTTP client.
type Client struct {
	HTTP       *http.Client
	BaseURL    string // www.olx.pl
	JobsAPIURL string // jobs-api.olx.pl
	UserAgent  string

	wwwLimiter    *rate.Limiter
	jobsLimiter   *rate.Limiter
	phonesLimiter *rate.Limiter

	// phonesBlocked is set to true the first time GetPhones sees the
	// "suspicious activity" 400. Sticky for the lifetime of the Client.
	phonesBlocked atomic.Bool
}

// Options configures a new Client. Zero-valued fields fall back to defaults.
type Options struct {
	HTTPClient   *http.Client
	BaseURL      string
	JobsAPIURL   string
	UserAgent    string
	WWWPerSec    float64
	JobsPerSec   float64
	PhonesPerSec float64
}

// New returns a configured Client. Defaults to a 30s HTTP timeout.
func New(opts Options) *Client {
	c := &Client{
		HTTP:       opts.HTTPClient,
		BaseURL:    opts.BaseURL,
		JobsAPIURL: opts.JobsAPIURL,
		UserAgent:  opts.UserAgent,
	}
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	if c.BaseURL == "" {
		c.BaseURL = defaultBaseURL
	}
	if c.JobsAPIURL == "" {
		c.JobsAPIURL = defaultJobsAPIURL
	}
	if c.UserAgent == "" {
		c.UserAgent = defaultUserAgent
	}
	www := opts.WWWPerSec
	if www <= 0 {
		www = defaultWWWPerSec
	}
	jobs := opts.JobsPerSec
	if jobs <= 0 {
		jobs = defaultJobsPerSec
	}
	phones := opts.PhonesPerSec
	if phones <= 0 {
		phones = defaultPhonesPerSec
	}
	c.wwwLimiter = rate.NewLimiter(rate.Limit(www), 1)
	c.jobsLimiter = rate.NewLimiter(rate.Limit(jobs), 1)
	c.phonesLimiter = rate.NewLimiter(rate.Limit(phones), 1)
	return c
}

// PhonesBlocked reports whether GetPhones has been blackballed by OLX's
// anti-abuse system during this Client's lifetime. Sync callers should
// gate phone-fetch calls on this to avoid wasted requests after the
// block trips.
func (c *Client) PhonesBlocked() bool {
	return c.phonesBlocked.Load()
}

// SearchParam is a key/value pair in the apigateway GraphQL searchParameters input.
type SearchParam struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ListByCategory invokes the apigateway ListingSearchQuery for one page of
// offers within a category. Returns the parsed listings plus a flag
// indicating whether there is likely a next page (true if len(items) == limit).
func (c *Client) ListByCategory(ctx context.Context, categoryID, offset, limit int) ([]OfferSummary, bool, error) {
	params := []SearchParam{
		{Key: "offset", Value: strconv.Itoa(offset)},
		{Key: "limit", Value: strconv.Itoa(limit)},
		{Key: "category_id", Value: strconv.Itoa(categoryID)},
		{Key: "suggest_filters", Value: "true"},
	}
	body, err := c.graphqlPOST(ctx, c.BaseURL+"/apigateway/graphql", map[string]any{
		"query":     listingSearchQuery,
		"variables": map[string]any{"searchParameters": params},
	}, c.wwwLimiter)
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		Data struct {
			ClientCompatibleListings struct {
				Typename string          `json:"__typename"`
				Data     []OfferSummary  `json:"data"`
				Metadata json.RawMessage `json:"metadata"`
			} `json:"clientCompatibleListings"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, false, fmt.Errorf("decode ListingSearchQuery: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, false, fmt.Errorf("ListingSearchQuery: %s", resp.Errors[0].Message)
	}
	listings := resp.Data.ClientCompatibleListings.Data
	hasMore := len(listings) >= limit && limit > 0
	return listings, hasMore, nil
}

// OtherSellerAds returns all ads from one seller via apigateway/graphql.
// Returns the list and a hasMore hint (true when len == limit).
func (c *Client) OtherSellerAds(ctx context.Context, sellerID string, offset, limit int) ([]OfferSummary, bool, error) {
	body, err := c.graphqlPOST(ctx, c.BaseURL+"/apigateway/graphql", map[string]any{
		"query": otherSellerAdsQuery,
		"variables": map[string]any{
			"sellerId": sellerID,
			"offset":   offset,
			"limit":    limit,
		},
	}, c.wwwLimiter)
	if err != nil {
		return nil, false, err
	}
	var resp struct {
		Data struct {
			GetOtherAdsOfUser struct {
				Typename string         `json:"__typename"`
				Offers   []OfferSummary `json:"offers"`
			} `json:"getOtherAdsOfUser"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, false, fmt.Errorf("decode OtherSellerAdsQuery: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, false, fmt.Errorf("OtherSellerAdsQuery: %s", resp.Errors[0].Message)
	}
	offers := resp.Data.GetOtherAdsOfUser.Offers
	return offers, len(offers) >= limit && limit > 0, nil
}

// GetOffer fetches the full offer detail from /api/v2/offers/{id}/.
func (c *Client) GetOffer(ctx context.Context, offerID string) (*OfferDetail, json.RawMessage, error) {
	u := c.BaseURL + "/api/v2/offers/" + url.PathEscape(offerID) + "/"
	raw, err := c.getJSON(ctx, u, c.wwwLimiter)
	if err != nil {
		return nil, nil, err
	}
	var env struct {
		Data OfferDetail `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, raw, fmt.Errorf("decode offer detail: %w", err)
	}
	return &env.Data, raw, nil
}

// GetPhones fetches the rate-limited phone numbers for an offer.
//
// Three call shapes:
//   - Block already tripped this session → returns (nil, nil, ErrPhonesBlocked)
//     without any network call.
//   - OLX returns the "suspicious activity" 400 → trips the sticky block
//     and returns (nil, nil, ErrPhonesBlocked) so the caller can decide
//     to stop attempting.
//   - Otherwise → returns the phones found (possibly empty when OLX
//     redacts the number for this caller) and the raw response body.
//
// Throttled by phonesLimiter (default 1 every 5s) — a much slower path
// than the general wwwLimiter, because empirically OLX's anti-abuse on
// /limited-phones/ trips far faster than on the listing/offer endpoints.
func (c *Client) GetPhones(ctx context.Context, offerID string) ([]string, json.RawMessage, error) {
	if c.phonesBlocked.Load() {
		return nil, nil, ErrPhonesBlocked
	}
	u := c.BaseURL + "/api/v1/offers/" + url.PathEscape(offerID) + "/limited-phones/"
	raw, err := c.getJSON(ctx, u, c.phonesLimiter)
	if err != nil {
		var herr *HTTPError
		if errors.As(err, &herr) && herr.IsSuspiciousActivity() {
			c.phonesBlocked.Store(true)
			return nil, nil, ErrPhonesBlocked
		}
		return nil, nil, err
	}
	var env struct {
		Data struct {
			Phones []string `json:"phones"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, raw, fmt.Errorf("decode phones: %w", err)
	}
	return env.Data.Phones, raw, nil
}

// GetUser fetches the seller profile.
func (c *Client) GetUser(ctx context.Context, userID string) (*UserProfile, json.RawMessage, error) {
	u := c.BaseURL + "/api/v1/users/" + url.PathEscape(userID) + "/"
	raw, err := c.getJSON(ctx, u, c.wwwLimiter)
	if err != nil {
		return nil, nil, err
	}
	var env struct {
		Data UserProfile `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, raw, fmt.Errorf("decode user: %w", err)
	}
	return &env.Data, raw, nil
}

// ResolveSlug resolves an OLX URL slug like "praca,produkcja,obsluga-produkcji"
// into its category_id and friendly query params.
func (c *Client) ResolveSlug(ctx context.Context, slugPath string) (int, map[string]string, error) {
	u := c.BaseURL + "/api/v1/friendly-links/query-params/" + slugPath + "/"
	raw, err := c.getJSON(ctx, u, c.wwwLimiter)
	if err != nil {
		return 0, nil, err
	}
	var env struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return 0, nil, fmt.Errorf("decode slug resolve: %w", err)
	}
	cid := 0
	if v, ok := env.Data["category_id"]; ok {
		cid, _ = strconv.Atoi(v)
	}
	return cid, env.Data, nil
}

// --- HTTP helpers --------------------------------------------------------

func (c *Client) getJSON(ctx context.Context, u string, limiter *rate.Limiter) (json.RawMessage, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "pl,en;q=0.8")
	return c.do(req)
}

func (c *Client) graphqlPOST(ctx context.Context, u string, payload map[string]any, limiter *rate.Limiter) (json.RawMessage, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "pl,en;q=0.8")
	return c.do(req)
}

// do executes the request with exponential backoff on 5xx and Retry-After
// on 429. Returns the raw response body on success (any 2xx).
func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	const maxAttempts = 4
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < maxAttempts; attempt++ {
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
		} else {
			body, rerr := io.ReadAll(resp.Body)
			resp.Body.Close()
			switch {
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				if rerr != nil {
					return nil, fmt.Errorf("read body: %w", rerr)
				}
				return json.RawMessage(body), nil
			case resp.StatusCode == http.StatusTooManyRequests:
				wait := parseRetryAfter(resp.Header.Get("Retry-After"))
				if wait == 0 {
					wait = backoff
				}
				lastErr = &HTTPError{StatusCode: resp.StatusCode, Host: req.URL.Host, Body: string(body)}
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(wait):
				}
				backoff *= 2
				continue
			case resp.StatusCode >= 500:
				lastErr = &HTTPError{StatusCode: resp.StatusCode, Host: req.URL.Host, Body: string(body)}
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(backoff):
				}
				backoff *= 2
				continue
			default:
				return nil, &HTTPError{StatusCode: resp.StatusCode, Host: req.URL.Host, Body: string(body)}
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
		lastErr = errors.New("request failed after retries")
	}
	return nil, lastErr
}

func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		return time.Until(t)
	}
	return 0
}
