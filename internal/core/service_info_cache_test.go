// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/majewsky/gg/options"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/assert"
	"github.com/sapcc/go-bits/easypg"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
)

const configJSON = `{
	"availability_zones": ["az-one", "az-two"],
	"discovery": {
		"method": "static"
	},
	"areas": { "shared": { "display_name": "Shared" }, "unshared": { "display_name": "Unshared" }},
	"liquids": {
		"shared": {"area": "shared"},
		"unshared": {"area": "unshared"}
	}
}`

func TestServiceInfoCache(t *testing.T) {
	serviceInfoFirst := test.DefaultLiquidServiceInfo("First")
	serviceInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create":    {DisplayName: "Object Creations", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:delete":    {DisplayName: "Object Deletions", Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"objects:update":    {DisplayName: "Object Updates", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:unlimited": {DisplayName: "Object Unlimited Operations", Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	serviceInfoFirst.Resources = map[liquid.ResourceName]liquid.ResourceInfo{}
	serviceInfoFirst.Categories = map[liquid.CategoryName]liquid.CategoryInfo{}
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", serviceInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
	)
	first := s.GetServiceID("first")
	secondCapacity := s.GetResourceID("second", "capacity")
	firstObjectsCreate := s.GetRateID("first", "objects:create")
	// by calling connect with a DB-URL, we register the service info listeners
	s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, func(serviceType db.ServiceType) (core.LiquidClient, error) { return nil, nil }, options.FromPointer(easypg.BuildDBURL(t)))
	t.Cleanup(func() { s.Cluster.ServiceInfoCache.Close() })

	sic := s.Cluster.ServiceInfoCache
	assert.Equal(t, len(sic.GetServices()), 2)
	assert.Equal(t, len(sic.GetResourcesForType("first")), 0)
	assert.Equal(t, len(sic.GetResourcesForType("second")), 2)
	assert.Equal(t, sic.GetResourceForTypeName("second", "capacity").Name, "capacity")
	assert.Equal(t, len(sic.GetRatesForType("second")), 0)
	assert.Equal(t, len(sic.GetCategories()), 1)

	// check service update
	assert.Equal(t, sic.GetServiceForType("first").DisplayName, "First")
	assert.Equal(t, sic.GetServiceForType("second").DisplayName, "Second")
	s.MustDBExec("UPDATE services SET display_name = 'Changed' WHERE id = $1", first)
	<-sic.OnInvalidate
	assert.Equal(t, sic.GetServiceForType("first").DisplayName, "Changed")
	assert.Equal(t, sic.GetServiceForType("second").DisplayName, "Second")

	// resource update
	assert.Equal(t, sic.GetResourceForTypeName("second", "capacity").DisplayName, "Capacity")
	assert.Equal(t, sic.GetResourceForTypeName("second", "things").DisplayName, "Things")
	s.MustDBExec("UPDATE resources r SET display_name = 'Changed' where id = $1", secondCapacity)
	<-sic.OnInvalidate
	assert.Equal(t, sic.GetResourceForTypeName("second", "capacity").DisplayName, "Changed")
	assert.Equal(t, sic.GetResourceForTypeName("second", "things").DisplayName, "Things")

	// check az_resource insert
	assert.Equal(t, len(sic.GetAZResourcesForTypeName("second", "capacity")), 5) // gives out total, any and unknown, too
	s.MustDBInsert(&db.AZResource{
		ResourceID:       secondCapacity,
		AvailabilityZone: "test",
		Path:             db.AZResourcePath{ServiceType: "second", ResourceName: "capacity", AvailabilityZone: "test"},
		RawCapacity:      123,
	})
	<-sic.OnInvalidate
	assert.Equal(t, len(sic.GetAZResourcesForTypeName("second", "capacity")), 6)

	// check rate deletion
	assert.Equal(t, len(sic.GetRatesForType("first")), 4)
	s.MustDBExec("DELETE FROM rates WHERE id = $1", firstObjectsCreate)
	<-sic.OnInvalidate
	assert.Equal(t, len(sic.GetRatesForType("first")), 3)
}
