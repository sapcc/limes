// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"

	"github.com/sapcc/limes/internal/api"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
	"github.com/sapcc/limes/internal/test/oldassert"
)

func TestForbidClusterIDHeader(t *testing.T) {
	srvInfo := test.DefaultLiquidServiceInfo("Foo")
	s := test.NewSetup(t,
		test.WithConfig(string(must.Return(httptest.NewJQModifiableJSONString("{}", "TestForbidClusterIDHeader").
			ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
			ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
			ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
			MarshalJSON()))),
		test.WithAPIMiddleware(api.ForbidClusterIDHeader),
		test.WithMockLiquidClient("foo", srvInfo),
	)

	// requests without X-Limes-Cluster-Id are accepted
	_, respBody := oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)

	// cluster ID "current" is still allowed for backwards compatibility, produces identical output
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-Cluster-Id": "current"},
		ExpectStatus: http.StatusOK,
		ExpectBody:   oldassert.ByteData(bytes.TrimSpace(respBody)),
	}.Check(t, s.Handler)

	// same request with X-Limes-Cluster-Id is rejected
	oldassert.HTTPRequest{
		Method:       "GET",
		Path:         "/v1/clusters/current",
		Header:       map[string]string{"X-Limes-Cluster-Id": "unknown"},
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   oldassert.StringData("multi-cluster support is removed: the X-Limes-Cluster-Id header is not allowed anymore\n"),
	}.Check(t, s.Handler)
}
