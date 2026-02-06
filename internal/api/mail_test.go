// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

func TestRenderMailTemplate(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(`{
			"availability_zones": ["az-one", "az-two"],
			"discovery": {
				"method": "static",
				"static_config": {
					"domains": [
						{"name": "germany", "id": "uuid-for-germany"}
					],
					"projects": {
						"uuid-for-germany": [{"name": "dresden", "id": "uuid-for-dresden", "parent_id": "uuid-for-germany"}]
					}
				}
			},
			"liquids": {
				"shared": {"area": "shared"}
			}
		}`),
		test.WithMailTemplates,
	)

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/mail/render?template_type=confirmed_commitments",
		ExpectStatus: http.StatusForbidden,
	}.Check(t, s.Handler)
	s.TokenValidator.Enforcer.AllowView = true

	// expect error when template type is missing
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/mail/render",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("missing required parameter: template_type\n"),
	}.Check(t, s.Handler)

	// expect error for invalid template type
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/mail/render?template_type=unknown",
		ExpectStatus: http.StatusBadRequest,
		ExpectBody:   assert.StringData("invalid template type\n"),
	}.Check(t, s.Handler)

	// happy path
	assert.HTTPRequest{
		Method:       "GET",
		Path:         "/admin/mail/render?template_type=confirmed_commitments",
		ExpectStatus: http.StatusOK,
	}.Check(t, s.Handler)
}
