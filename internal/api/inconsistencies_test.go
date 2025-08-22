// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const (
	inconsistenciesTestConfigYAML = `
		availability_zones: [ az-one, az-two ]
		discovery:
			method: static
			static_config:
				domains:
					- { name: germany, id: uuid-for-germany }
					- { name: france,id: uuid-for-france }
				projects:
					uuid-for-germany:
						- { name: berlin, id: uuid-for-berlin, parent_id: uuid-for-germany }
						- { name: dresden, id: uuid-for-dresden, parent_id: uuid-for-berlin }
					uuid-for-france:
						- { name: paris, id: uuid-for-paris, parent_id: uuid-for-france}
		liquids:
			shared:
				area: testing
			unshared:
				area: testing
	`
)

func TestFullInconsistencyReport(t *testing.T) {
	t.Helper()
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-inconsistencies.sql"),
		test.WithConfig(inconsistenciesTestConfigYAML),
		test.WithMockLiquidClient("shared", test.DefaultLiquidServiceInfo()),
		test.WithMockLiquidClient("unshared", test.DefaultLiquidServiceInfo()),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-list.json"),
	}.Check(t, s.Handler)
}

func TestEmptyInconsistencyReport(t *testing.T) {
	t.Helper()
	s := test.NewSetup(t,
		test.WithConfig(inconsistenciesTestConfigYAML),
		test.WithMockLiquidClient("shared", test.DefaultLiquidServiceInfo()),
		test.WithMockLiquidClient("unshared", test.DefaultLiquidServiceInfo()),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-empty.json"),
	}.Check(t, s.Handler)
}
