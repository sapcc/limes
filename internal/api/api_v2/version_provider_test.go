// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"testing"

	"go.xyrillian.de/gg/assert"

	"github.com/sapcc/limes/internal/api/api_v2"
)

func TestParseDomainNames(t *testing.T) {
	t.Setenv("LIMES_API_DOMAIN_NAME_V1", "https://limes.example.com")
	t.Setenv("LIMES_API_DOMAIN_NAME_V2", "limitas.example.com")
	_, err := api_v2.CollectDomainNamesFromEnv()
	assert.ErrEqual(t, err, `invalid value for LIMES_API_DOMAIN_NAME_V1: expected a hostname, but got "https://limes.example.com"`)

	t.Setenv("LIMES_API_DOMAIN_NAME_V1", "limes.example.com")
	t.Setenv("LIMES_API_DOMAIN_NAME_V2", "limitas.example.com/v2")
	_, err = api_v2.CollectDomainNamesFromEnv()
	assert.ErrEqual(t, err, `invalid value for LIMES_API_DOMAIN_NAME_V2: expected a hostname, but got "limitas.example.com/v2"`)

	t.Setenv("LIMES_API_DOMAIN_NAME_V1", "limes.example.com")
	t.Setenv("LIMES_API_DOMAIN_NAME_V2", "limitas.example.com")
	names, err := api_v2.CollectDomainNamesFromEnv()
	if assert.ErrEqual(t, err, nil) {
		assert.Equal(t, names, api_v2.DomainNames{
			V1: "limes.example.com",
			V2: "limitas.example.com",
		})
	}
}
