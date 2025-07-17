// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/sapcc/go-bits/assert"

	"github.com/sapcc/limes/internal/core"
)

func TestFilterDomains(t *testing.T) {
	cfg := core.DiscoveryConfiguration{
		IncludeDomainRx: "foo",
		ExcludeDomainRx: "2$",
	}

	input := []core.KeystoneDomain{
		{Name: "bar1"},
		{Name: "bar2"},
		{Name: "foo1"},
		{Name: "foo2"},
	}
	expected := []core.KeystoneDomain{
		{Name: "foo1"},
	}
	assert.DeepEqual(t, "filtered domains", cfg.FilterDomains(input), expected)
}
