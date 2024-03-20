/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package core_test

// NOTE: This file must be in a separate package (not in `package core`) to prevent an import cycle.
import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/test"
)

const (
	testParseQuotaOverridesConfigYAML = `
		availability_zones: [ az-one ]
		discovery:
			method: --test-static
		services:
			- service_type: unittest
				type: --test-generic
	`
)

func TestParseQuotaOverrides(t *testing.T) {
	s := test.NewSetup(t,
		test.WithConfig(testParseQuotaOverridesConfigYAML),
	)

	// test successful parsing
	buf := []byte(`
		domain-one:
			project-one:
				unittest:
					things: 20
					capacity: 5 GiB
	`)
	buf = bytes.Replace(buf, []byte("\t"), []byte("  "), -1)
	result, errs := s.Cluster.ParseQuotaOverrides("overrides.yaml", buf)
	errStrings := strings.Split(errs.Join("\n"), "\n")

	assert.DeepEqual(t, "errors", errStrings, []string{""})
	assert.DeepEqual(t, "result", result, map[string]map[string]map[string]map[string]uint64{
		"domain-one": {
			"project-one": {
				"unittest": {
					"things":   20,
					"capacity": 5 << 30, // capacity is in unit "B"
				},
			},
		},
	})

	// test parsing errors
	buf = []byte(`
		domain-one:
			project1:
				unittest:
					things: [ 1, GiB ]
			project2:
				unittest:
					things: 50 GiB
			project3:
				unittest:
					unknown-resource: 10
			project4:
				unknown-service:
					items: 10
	`)
	buf = bytes.Replace(buf, []byte("\t"), []byte("  "), -1)
	_, errs = s.Cluster.ParseQuotaOverrides("overrides.yaml", buf)
	errStrings = strings.Split(errs.Join("\n"), "\n")
	sort.Strings(errStrings) // map iteration order is not deterministic

	assert.DeepEqual(t, "errors", errStrings, []string{
		`while parsing overrides.yaml: in value for unittest/things: expected string or number, but got []interface {}{1, "GiB"}`,
		`while parsing overrides.yaml: in value for unittest/things: strconv.ParseUint: parsing "50 GiB": invalid syntax`,
		`while parsing overrides.yaml: unittest/unknown-resource is not a valid resource`,
		`while parsing overrides.yaml: unknown-service/items is not a valid resource`,
	})
}
