// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package olx

import (
	"encoding/json"
	"strconv"
)

// OfferSummary is the row shape returned by apigateway ListingSearchQuery
// and OtherSellerAdsQuery. Fields are a subset of what OLX returns — the
// ones we actually use. The raw JSON is preserved by the caller (sync.go)
// for re-parsing later if we ever need more fields.
type OfferSummary struct {
	ID                FlexID   `json:"id"`
	NodeID            string   `json:"_nodeId"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	URL               string   `json:"url"`
	CreatedTime       string   `json:"created_time"`
	LastRefreshTime   string   `json:"last_refresh_time"`
	ValidToTime       string   `json:"valid_to_time"`
	OmnibusPushupTime string   `json:"omnibus_pushup_time"`
	Business          bool     `json:"business"`
	Category          Category `json:"category"`
	Location          Location `json:"location"`
	Contact           Contact  `json:"contact"`
	User              User     `json:"user"`
	Map               Map      `json:"map"`
}

// OfferDetail is the body of /api/v2/offers/{id}/ → "data". A superset of
// OfferSummary with description, params, photos, etc.
type OfferDetail struct {
	OfferSummary
	Params    []OfferParam      `json:"params"`
	KeyParams []string          `json:"key_params"`
	Status    string            `json:"status"`
	OfferType string            `json:"offer_type"`
	Promotion json.RawMessage   `json:"promotion"`
	Photos    []json.RawMessage `json:"photos"`
}

// OfferParam is one row from offer.params (key/value attribute pair).
type OfferParam struct {
	Key   string          `json:"key"`
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
	Type  string          `json:"type"`
}

// Category holds the offer's category id + type.
type Category struct {
	ID   FlexID `json:"id"`
	Type string `json:"type"`
}

// Location holds the offer location.
type Location struct {
	City     Place `json:"city"`
	Region   Place `json:"region"`
	District Place `json:"district"`
}

// Place is one city/region/district entry.
type Place struct {
	ID             FlexID `json:"id"`
	Name           string `json:"name"`
	NormalizedName string `json:"normalized_name"`
}

// Contact is the offer's contact preferences.
type Contact struct {
	Name        string `json:"name"`
	Phone       bool   `json:"phone"`
	Chat        bool   `json:"chat"`
	Negotiation bool   `json:"negotiation"`
	Courier     bool   `json:"courier"`
}

// User is the embedded seller summary on an offer.
type User struct {
	ID                        FlexID `json:"id"`
	UUID                      string `json:"uuid"`
	Name                      string `json:"name"`
	CompanyName               string `json:"company_name"`
	Logo                      string `json:"logo"`
	SocialNetworkAccountType  string `json:"social_network_account_type"`
	SellerType                string `json:"seller_type"`
	OtherAdsEnabled           bool   `json:"other_ads_enabled"`
	Created                   string `json:"created"`
	LastSeen                  string `json:"last_seen"`
	B2CBusinessPage           bool   `json:"b2c_business_page"`
	About                     string `json:"about"`
}

// UserProfile is the body of /api/v1/users/{id}/ → "data".
type UserProfile struct {
	User
}

// Map holds the offer's map coordinates.
type Map struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
	Zoom int    `json:"zoom"`
}

// FlexID accepts OLX ids that come back as either JSON numbers or strings.
// ListingSearchQuery returns numeric ids, jobs-api returns string ids,
// `_nodeId` is base64'd, etc. We always normalize to string at use sites.
type FlexID string

// UnmarshalJSON decodes either a JSON number or a JSON string into a FlexID.
func (f *FlexID) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexID(s)
		return nil
	}
	// Number — accept both ints and floats.
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*f = FlexID(string(n))
	return nil
}

// String returns the id as a string.
func (f FlexID) String() string { return string(f) }

// Int returns the id parsed as int, or 0 if not parseable.
func (f FlexID) Int() int {
	if f == "" {
		return 0
	}
	n, _ := strconv.Atoi(string(f))
	return n
}
