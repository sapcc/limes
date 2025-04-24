/*******************************************************************************
*
* Copyright 2018 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

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
			method: --test-static
		services:
			- service_type: shared
				type: liquid
				params:
					area: testing
					liquid_service_type: %[1]s
			- service_type: unshared
				type: liquid
				params:
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
