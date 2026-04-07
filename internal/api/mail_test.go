// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"testing"

	"github.com/majewsky/gg/jsonmatch"

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
			"areas": { "shared": { "display_name": "Shared" }},
			"liquids": {
				"shared": {"area": "shared"}
			},
			"mail_notifications": {
				"templates": {
					"confirmed_commitments": {
						"subject": "Confirmed Commitments",
						"body": "<!DOCTYPE html><html><body>Confirmed</body></html>"
					},
					"expiring_commitments": {
						"subject": "Expiring Commitments",
						"body": "<!DOCTYPE html><html><body>Expiring</body></html>"
					},
					"transferred_commitments": {
						"subject": "Transferred Commitments",
						"body": "<!DOCTYPE html><html><body>Transferred</body></html>"
					}
				}	
			}
		}`),
	)

	ctx := t.Context()

	// endpoint requires cluster show permissions
	s.TokenValidator.Enforcer.AllowView = false
	resp := s.Handler.RespondTo(ctx, "GET /admin/mail/render")
	resp.ExpectStatus(t, http.StatusForbidden)
	s.TokenValidator.Enforcer.AllowView = true

	// happy path - renders all templates as JSON
	resp = s.Handler.RespondTo(ctx, "GET /admin/mail/render")
	resp.ExpectJSON(t, http.StatusOK, jsonmatch.Object{
		"confirmed_commitments":   "<!DOCTYPE html><html><body>Confirmed</body></html>",
		"expiring_commitments":    "<!DOCTYPE html><html><body>Expiring</body></html>",
		"transferred_commitments": "<!DOCTYPE html><html><body>Transferred</body></html>",
	})
}

func TestRenderMailTemplateInvalidHTML(t *testing.T) {
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
			"areas": { "shared": { "display_name": "Shared" }},
			"liquids": {
				"shared": {"area": "shared"}
			},
			"mail_notifications": {
				"templates": {
					"confirmed_commitments": {
						"subject": "subject",
						"body": "<html>"
					},
					"expiring_commitments": {
						"subject": "subject",
						"body": "<!DOCTYPE html><html><body>Test</body></html>"
					},
					"transferred_commitments": {
						"subject": "subject",
						"body": "<!DOCTYPE html><html><body>Test</body></html>"
					}
				}	
			}
		}`),
	)

	ctx := t.Context()
	resp := s.Handler.RespondTo(ctx, "GET /admin/mail/render")
	resp.ExpectText(t, http.StatusInternalServerError, "template \"confirmed_commitments\" returned invalid HTML: XML syntax error on line 1: unexpected EOF\n")
}

func TestRenderMailTemplateOverEscaped(t *testing.T) {
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
			"areas": { "shared": { "display_name": "Shared" }},
			"liquids": {
				"shared": {"area": "shared"}
			},
			"mail_notifications": {
				"templates": {
					"confirmed_commitments": {
						"subject": "subject",
						"body": "\"\\u003chtml\\u003ebody\\u003c/html\\u003e\\n\""
					},
					"expiring_commitments": {
						"subject": "subject",
						"body": "<!DOCTYPE html><html><body>Test</body></html>"
					},
					"transferred_commitments": {
						"subject": "subject",
						"body": "<!DOCTYPE html><html><body>Test</body></html>"
					}
				}	
			}
		}`),
	)

	ctx := t.Context()
	resp := s.Handler.RespondTo(ctx, "GET /admin/mail/render")
	resp.ExpectText(t, http.StatusInternalServerError, "template \"confirmed_commitments\" was escaped multiple times\n")
}
