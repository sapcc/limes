// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"fmt"
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
				liquid_service_type: %[1]s
			unshared:
				area: testing
				liquid_service_type: %[2]s
	`
)

func TestFullInconsistencyReport(t *testing.T) {
	_, liquidServiceType := test.NewMockLiquidClient(test.DefaultLiquidServiceInfo())
	_, liquidServiceType2 := test.NewMockLiquidClient(test.DefaultLiquidServiceInfo())
	t.Helper()
	s := test.NewSetup(t,
		test.WithDBFixtureFile("fixtures/start-data-inconsistencies.sql"),
		test.WithConfig(fmt.Sprintf(inconsistenciesTestConfigYAML, liquidServiceType, liquidServiceType2)),
		test.WithAPIHandler(NewV1API),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-list.json"),
	}.Check(t, s.Handler)
}

func TestEmptyInconsistencyReport(t *testing.T) {
	_, liquidServiceType := test.NewMockLiquidClient(test.DefaultLiquidServiceInfo())
	_, liquidServiceType2 := test.NewMockLiquidClient(test.DefaultLiquidServiceInfo())
	t.Helper()
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(inconsistenciesTestConfigYAML, liquidServiceType, liquidServiceType2)),
		test.WithAPIHandler(NewV1API),
	)

	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/inconsistencies",
		ExpectStatus: 200,
		ExpectBody:   assert.JSONFixtureFile("./fixtures/inconsistency-empty.json"),
	}.Check(t, s.Handler)
}
