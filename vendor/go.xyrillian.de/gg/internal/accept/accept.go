// SPDX-FileCopyrightText: 2026 Stefan Majewsky <majewsky@gmx.net>
// SPDX-License-Identifier: Apache-2.0

package accept

import (
	"cmp"
	"mime"
	"regexp"
	"slices"
	"strconv"
	"strings"

	. "go.xyrillian.de/gg/option"
)

// Header contains a parsed set of "Accept" HTTP headers [RFC 9110, 12.5.1].
type Header struct {
	options []option
}

type option struct {
	MediaType string
	Params    map[string]string
	Weight    float64
}

var weightRx = regexp.MustCompile(`\s*;\s*q=([01](?:\.[0-9]{0,3})?)$`)

// ParseHeader parses a Set of "Accept" HTTP headers [RFC 9110, 12.5.1].
// If any part of the header is malformed, an empty Header struct is returned.
func ParseHeader(headers []string) Header {
	var (
		result Header
		none   Header // return in case of errors
	)
	for _, header := range headers {
		for _, section := range strings.Split(header, ",") {
			section = strings.TrimSpace(section)

			// remove weight from `section` while capturing the weight number in `weightStr`
			var weightStr string
			section = weightRx.ReplaceAllStringFunc(section, func(match string) string {
				_, weightStr, _ = strings.Cut(match, "=")
				return ""
			})

			mediaType, params, err := mime.ParseMediaType(section)
			if err != nil {
				return none
			}
			if _, ok := params["q"]; ok {
				// malformed q-value that was not caught by the regex
				return none
			}
			opt := option{mediaType, params, 1.0}
			if weightStr != "" {
				opt.Weight, err = strconv.ParseFloat(weightStr, 64)
				if err != nil {
					// defense in depth: unreachable because the regex match has
					// extremely constrained grammar for `weightStr`
					return none
				}
				if opt.Weight > 1.0 { // this boundary is easier to express here than in the regex
					return none
				}
			}

			result.options = append(result.options, opt)
		}
	}

	// sort options by descending weight to simplify lookups
	slices.SortFunc(result.options, func(lhs, rhs option) int {
		return cmp.Compare(rhs.Weight, lhs.Weight)
	})
	return result
}

// Negotiate picks from a list of supported media types according to the client's request.
// If h is empty, the first argument is returned (thus the first argument is the server's preference).
// If none of the arguments (the server's options) satisfy the client's request,
// None is returned and a 406 response shall be generated.
func (h Header) Negotiate(mediaTypes ...string) Option[string] {
	// parse all `mediaTypes` once
	// TODO: if we decide to turn this package into public API, change the API to allow precomputing this
	type offer struct {
		OriginalValue string
		MediaType     string
		Params        map[string]string
	}
	var offers []offer
	for _, mt := range mediaTypes {
		mediaType, params, err := mime.ParseMediaType(mt)
		if err == nil {
			offers = append(offers, offer{mt, mediaType, params})
		}
	}

	// we cannot choose from an empty set of options (this can only happen if the
	// caller gave us no or only malformed media types)
	if len(offers) == 0 {
		return None[string]()
	}

	// if nothing was offered, we default to our own preferred option
	if len(h.options) == 0 {
		return Some(offers[0].OriginalValue)
	}

	// NOTE: ParseHeader() sorts options by descending weight, so the first match wins.
	for _, opt := range h.options {
	MEDIATYPE:
		for _, offer := range offers {
			// can only consider offered media types that match on all requested parameters
			for k, v1 := range opt.Params {
				if v2, ok := offer.Params[k]; !ok || v1 != v2 {
					continue MEDIATYPE
				}
			}

			// check if offered media type matches requested media type or media type pattern
			if opt.MediaType == "*/*" {
				return Some(offer.OriginalValue)
			}
			if category, ok := strings.CutSuffix(opt.MediaType, "/*"); ok {
				if rest, ok := strings.CutPrefix(offer.MediaType, category); ok && strings.HasPrefix(rest, "/") {
					return Some(offer.OriginalValue)
				}
			} else if opt.MediaType == offer.MediaType {
				return Some(offer.OriginalValue)
			}
		}
	}

	return None[string]()
}
