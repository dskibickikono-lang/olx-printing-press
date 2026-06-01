// Copyright 2026 dskibickikono-lang. Licensed under Apache-2.0. See LICENSE.

package olx

// GraphQL queries are captured verbatim from the HAR. We intentionally
// keep the verbose ListingSearchQuery shape rather than persisted-query
// hashes because hashes rotate when OLX redeploys.

const listingSearchQuery = `query ListingSearchQuery($searchParameters: [SearchParameter!] = []) {
  clientCompatibleListings(searchParameters: $searchParameters) {
    __typename
    ... on ListingSuccess {
      __typename
      data {
        _nodeId
        id
        title
        description
        url
        created_time
        last_refresh_time
        omnibus_pushup_time
        valid_to_time
        business
        category {
          _nodeId
          id
          type
        }
        location {
          city {
            id
            name
            normalized_name
            _nodeId
          }
          district {
            id
            name
            normalized_name
            _nodeId
          }
          region {
            id
            name
            normalized_name
            _nodeId
          }
        }
        contact {
          name
          phone
          chat
          negotiation
          courier
        }
        user {
          _nodeId
          id
          uuid
          name
          company_name
          logo
          social_network_account_type
          seller_type
          other_ads_enabled
          created
          last_seen
          b2c_business_page
        }
        map {
          lat
          lon
          zoom
        }
      }
    }
    ... on ListingError {
      __typename
    }
  }
}`

const otherSellerAdsQuery = `query OtherSellerAdsQuery($sellerId: String, $offset: Int, $limit: Int) {
  getOtherAdsOfUser(sellerId: $sellerId, offset: $offset, limit: $limit) {
    __typename
    ... on OtherAdsList {
      __typename
      offers {
        _nodeId
        id
        title
        description
        url
        created_time
        last_refresh_time
        omnibus_pushup_time
        valid_to_time
        business
        category {
          _nodeId
          id
          type
        }
        location {
          region {
            id
            name
            normalized_name
          }
          city {
            id
            name
            normalized_name
          }
          district {
            id
            name
            normalized_name
          }
        }
        contact {
          name
          phone
          chat
          negotiation
          courier
        }
        user {
          _nodeId
          id
          uuid
          name
          company_name
        }
      }
    }
  }
}`
