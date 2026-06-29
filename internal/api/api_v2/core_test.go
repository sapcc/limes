// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/sapcc/go-bits/httptest"

	"github.com/sapcc/limes/internal/api/api_v2"
	"github.com/sapcc/limes/internal/test"
)

func TestRequestBodyParsing(t *testing.T) {
	// the actual test setup is kind of irrelevant; we just do a minimal setup based
	// on the commitment-create tests since we are using that endpoint for this test
	ctx := t.Context()
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	// overlong request body
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new",
		httptest.WithHeader("Content-Type", "application/json"),
		httptest.WithBody(strings.NewReader(
			fmt.Sprintf(`{"service_type":"%s"}`, strings.Repeat("a", 10000)),
		)),
	).ExpectText(t, http.StatusRequestEntityTooLarge, "request body too large\n")

	// request body that is not a JSON payload
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new",
		httptest.WithHeader("Content-Type", "application/json"),
		httptest.WithBody(strings.NewReader("I need more quota kthxbye")),
	).ExpectText(t, http.StatusBadRequest, "request body is not valid JSON: invalid character 'I' looking for beginning of value\n")

	// multiple JSON payloads in request body
	s.Handler.RespondTo(ctx, "POST /resources/v2/commitments/new",
		httptest.WithHeader("Content-Type", "application/json"),
		httptest.WithBody(strings.NewReader(`{"service_type":"first"}{"service_type":"second"}`)),
	).ExpectText(t, http.StatusBadRequest, "request body contains 25 unexpected bytes after the JSON payload\n")
}

func TestDomainNameSeparation(t *testing.T) {
	ctx := t.Context()
	s := test.NewSetup(t,
		test.WithConfig(commitmentCreateConfigJSON),
		test.WithAPIDomainNames(api_v2.DomainNames{
			V1: "limes.example.com",
			V2: "limitas.example.com",
		}),
		test.WithPersistedServiceInfo("first", test.DefaultLiquidServiceInfo("First")),
		test.WithInitialDiscovery,
		test.WithEmptyResourceRecordsAsNeeded,
	)

	for _, format := range []string{"http://%s", "http://%s:8080", "https://%s", "https://%s:4443"} {
		t.Run(fmt.Sprintf("format=%s", format), func(t *testing.T) {
			s.Handler.RespondTo(ctx, fmt.Sprintf(`GET %s/resources/v2/info`, fmt.Sprintf(format, "limes.example.com"))).
				ExpectText(t, http.StatusBadRequest, "endpoint /resources/v2/info cannot be accessed on limes.example.com\n")
			s.Handler.RespondTo(ctx, fmt.Sprintf(`GET %s/resources/v2/info`, fmt.Sprintf(format, "limitas.example.com"))).
				ExpectStatus(t, http.StatusOK)
		})
	}
}
