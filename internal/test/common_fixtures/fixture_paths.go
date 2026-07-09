// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common_fixtures

import (
	_ "embed"

	"github.com/sapcc/go-bits/httptest"
)

var (
	//go:embed discovery_berlin_dresden_paris.json
	discoveryBerlinDresdenParis string
	// DiscoveryBerlinDresdenParis can be used as fixture for config.json in tests.
	DiscoveryBerlinDresdenParis httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(discoveryBerlinDresdenParis)

	//go:embed areas_first_second.json
	areasFirstSecond string
	// AreasFirstSecond can be used as fixture for config.json in tests.
	AreasFirstSecond httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(areasFirstSecond)

	//go:embed areas_shared_unshared.json
	areasSharedUnshared string
	// AreasSharedUnshared can be used as fixture for config.json in tests.
	AreasSharedUnshared httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(areasSharedUnshared)

	//go:embed area_liquid_dummy.json
	areaLiquidDummy string
	// AreaLiquidDummy can be used as fixture for config.json in tests.
	AreaLiquidDummy httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(areaLiquidDummy)

	//go:embed area_liquid_shared_unshared.json
	areaLiquidSharedUnshared string
	// AreaLiquidSharedUnshared can be used as fixture for config.json in tests.
	AreaLiquidSharedUnshared httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(areaLiquidSharedUnshared)

	//go:embed area_liquid_first_second.json
	areaLiquidFirstSecond string
	// AreaLiquidFirstSecond can be used as fixture for config.json in tests.
	AreaLiquidFirstSecond httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(areaLiquidFirstSecond)

	//go:embed azs_one_two.json
	azsOneTwo string
	// AZsOneTwo can be used as fixture for config.json in tests.
	AZsOneTwo httptest.JQUnmodifiedContent = httptest.JQUnmodifiedJSONString(azsOneTwo)
)
