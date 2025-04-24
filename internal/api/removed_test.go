/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package api

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/httpapi"

	"github.com/sapcc/limes/internal/test"
)

func TestForbidClusterIDHeader(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	_, liquidServiceType := test.NewMockLiquidClient(srvInfo)
	s := test.NewSetup(t,
		test.WithConfig(fmt.Sprintf(`
			availability_zones: [ az-one, az-two ]
			discovery:
				method: --test-static
			services:
				- service_type: foo
					type: liquid
					params:
						area: testing
						liquid_service_type: %[1]s
		`, liquidServiceType)),
		test.WithAPIHandler(NewV1API,
			httpapi.WithGlobalMiddleware(ForbidClusterIDHeader),
		),
	)

	// requests without X-Limes-Cluster-Id are accepted
	_, respBody := assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)

	// cluster ID "current" is still allowed for backwards compatibility, produces identical output
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-Cluster-Id": "current"},
		ExpectStatus: http.StatusOK,
		ExpectBody:   assert.ByteData(bytes.TrimSpace(respBody)),
	}.Check(t, s.Handler)

	// same request with X-Limes-Cluster-Id is rejected
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-Cluster-Id": "unknown"},
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("multi-cluster support is removed: the X-Limes-Cluster-Id header is not allowed anymore\n"),
	}.Check(t, s.Handler)
}
