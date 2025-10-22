// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/test"
)

func TestForbidClusterIDHeader(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo()
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [
						{"name": "germany", "id": "uuid-for-germany"},
						{"name": "france", "id": "uuid-for-france"}
					],
					"projects": {
						"uuid-for-germany": [
							{"name": "berlin", "id": "uuid-for-berlin", "parent_id": "uuid-for-germany"},
							{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-berlin"}
						],
						"uuid-for-france": [
							{"name": "paris", "id": "uuid-for-paris", "parent_id": "uuid-for-france"}
						]
					}
				}
			},
			"liquids": {
				"foo": {
					"area": "testing"
				}
			}
		}`),
		test.WithAPIMiddleware(api.ForbidClusterIDHeader),
		test.WithMockLiquidClient("foo", srvInfo),
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
