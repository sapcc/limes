// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

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
		"first": {"area": "shared"},
		"second": {"area": "unshared"}
	}
}`

func TestServiceInfoSnapshotFilter(t *testing.T) {
	// "first" has area "shared", resources empty, rates: objects:create, objects:delete, objects:update, objects:unlimited
	// "second" has area "unshared", resources: capacity (category: foo_category), things (no category)
	serviceInfoFirst := test.DefaultLiquidServiceInfo("First")
	serviceInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create":    {DisplayName: "Object Creations", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:delete":    {DisplayName: "Object Deletions", Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
		"objects:update":    {DisplayName: "Object Updates", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:unlimited": {DisplayName: "Object Unlimited Operations", Unit: liquid.UnitKibibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	serviceInfoFirst.Resources = map[liquid.ResourceName]liquid.ResourceInfo{}
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", serviceInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
	)
	s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, func(serviceType db.ServiceType) (core.LiquidClient, error) { return nil, nil }, options.FromPointer(easypg.BuildDBURL(t)))
	t.Cleanup(func() { s.Cluster.SIC.Close() })

	sis := s.Cluster.SIC.GetSnapshot()

	// filter by ServiceType
	filtered := sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("second")})
	assert.Equal(t, len(filtered.GetServices()), 1)
	_, ok := filtered.GetServiceForType("second")
	assert.Equal(t, ok, true)
	_, ok = filtered.GetServiceForType("first")
	assert.Equal(t, ok, false)
	resources, ok := filtered.GetResourcesForType("second")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(resources), 2)
	_, ok = filtered.GetResourcesForType("first")
	assert.Equal(t, ok, false)
	_, ok = filtered.GetRatesForType("first")
	assert.Equal(t, ok, false)
	service, ok := filtered.GetFilteredService()
	assert.Equal(t, ok, true)
	assert.Equal(t, service.DisplayName, "Second")

	// filter by ServiceArea
	filtered = sis.Filter(core.ServiceInfoFilter{ServiceArea: Some("shared")})
	assert.Equal(t, len(filtered.GetServices()), 1)
	_, ok = filtered.GetServiceForType("first")
	assert.Equal(t, ok, true)
	_, ok = filtered.GetServiceForType("second")
	assert.Equal(t, ok, false)
	rates, ok := filtered.GetRatesForType("first")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(rates), 4)

	// filter by ResourceName
	filtered = sis.Filter(core.ServiceInfoFilter{ResourceName: Some[liquid.ResourceName]("capacity")})
	resources, ok = filtered.GetResourcesForType("second")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(resources), 1)
	_, ok = resources["capacity"]
	assert.Equal(t, ok, true)
	_, ok = resources["things"]
	assert.Equal(t, ok, false)
	_, ok = filtered.GetFilteredResource()
	assert.Equal(t, ok, false) // ServiceType not set
	filtered2 := sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("second"), ResourceName: Some[liquid.ResourceName]("capacity")})
	resource, ok := filtered2.GetFilteredResource()
	assert.Equal(t, ok, true)
	assert.Equal(t, resource.DisplayName, "Capacity")

	// filter by Category
	filtered = sis.Filter(core.ServiceInfoFilter{Category: Some[liquid.CategoryName]("foo_category")})
	resources, ok = filtered.GetResourcesForType("second")
	assert.Equal(t, ok, true)
	_, ok = resources["capacity"]
	assert.Equal(t, ok, true)
	_, ok = resources["things"]
	assert.Equal(t, ok, false)
	_, ok = filtered.GetServiceForType("first")
	assert.Equal(t, ok, false)

	// filter by RateName
	filtered = sis.Filter(core.ServiceInfoFilter{RateName: Some[liquid.RateName]("objects:create")})
	rates, ok = filtered.GetRatesForType("first")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(rates), 1)
	_, ok = rates["objects:create"]
	assert.Equal(t, ok, true)
	_, ok = filtered.GetFilteredRate()
	assert.Equal(t, ok, false) // ServiceType not set
	filtered2 = sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("first"), RateName: Some[liquid.RateName]("objects:create")})
	rate, ok := filtered2.GetFilteredRate()
	assert.Equal(t, ok, true)
	assert.Equal(t, rate.DisplayName, "Object Creations")

	// snapshot immutability: mutations on returned maps don't affect snapshot
	services := sis.GetServices()
	services["injected"] = db.Service{DisplayName: "Injected"}
	_, ok = sis.GetServiceForType("injected")
	assert.Equal(t, ok, false)
	resourcesClone := must.BeOKT(sis.GetResourcesForType("second"))(t)
	resourcesClone["injected"] = db.Resource{DisplayName: "Injected"}
	originalResources := must.BeOKT(sis.GetResourcesForType("second"))(t)
	_, ok = originalResources["injected"]
	assert.Equal(t, ok, false)

	// FilteredServiceInfoSnapshot immutability
	filtered = sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("second")})
	filteredServices := filtered.GetServices()
	filteredServices["injected"] = db.Service{DisplayName: "Injected"}
	_, ok = filtered.GetServiceForType("injected")
	assert.Equal(t, ok, false)

	// Filter does not affect original snapshot
	_ = sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("second")})
	assert.Equal(t, len(sis.GetServices()), 2)
}

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
	t.Cleanup(func() { s.Cluster.SIC.Close() })

	sis := s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, len(sis.GetServices()), 2)
	must.NotBeOKT(sis.GetResourcesForType("first"))(t)
	assert.Equal(t, len(must.BeOKT(sis.GetResourcesForType("second"))(t)), 2)
	assert.Equal(t, must.BeOKT(sis.GetResourceForPath(db.ResourcePath{ServiceType: "second", ResourceName: "capacity"}))(t).Name, "capacity")
	must.NotBeOKT(sis.GetRatesForType("second"))(t)
	assert.Equal(t, len(sis.GetCategories()), 1)

	// check service update
	assert.Equal(t, must.BeOKT(sis.GetServiceForType("first"))(t).DisplayName, "First")
	assert.Equal(t, must.BeOKT(sis.GetServiceForType("second"))(t).DisplayName, "Second")
	s.MustDBExec("UPDATE services SET display_name = 'Changed' WHERE id = $1", first)
	<-s.Cluster.SIC.OnInvalidate
	sis = s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, must.BeOKT(sis.GetServiceForType("first"))(t).DisplayName, "Changed")
	assert.Equal(t, must.BeOKT(sis.GetServiceForType("second"))(t).DisplayName, "Second")

	// resource update
	assert.Equal(t, must.BeOKT(sis.GetResourceForPath(db.ResourcePath{ServiceType: "second", ResourceName: "capacity"}))(t).DisplayName, "Capacity")
	assert.Equal(t, must.BeOKT(sis.GetResourceForPath(db.ResourcePath{ServiceType: "second", ResourceName: "things"}))(t).DisplayName, "Things")
	s.MustDBExec("UPDATE resources r SET display_name = 'Changed' where id = $1", secondCapacity)
	<-s.Cluster.SIC.OnInvalidate
	sis = s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, must.BeOKT(sis.GetResourceForPath(db.ResourcePath{ServiceType: "second", ResourceName: "capacity"}))(t).DisplayName, "Changed")
	assert.Equal(t, must.BeOKT(sis.GetResourceForPath(db.ResourcePath{ServiceType: "second", ResourceName: "things"}))(t).DisplayName, "Things")

	// check GetAZResourceForID
	secondCapacityAZOne := s.GetAZResourceID("second", "capacity", "az-one")
	azRes, ok := sis.GetAZResourceForID(secondCapacityAZOne)
	assert.Equal(t, ok, true)
	assert.Equal(t, azRes.Path.ServiceType, db.ServiceType("second"))
	assert.Equal(t, azRes.Path.ResourceName, liquid.ResourceName("capacity"))
	assert.Equal(t, azRes.Path.AvailabilityZone, limes.AvailabilityZone("az-one"))
	_, ok = sis.GetAZResourceForID(99999) // non-existent ID
	assert.Equal(t, ok, false)

	// check az_resource insert
	assert.Equal(t, len(must.BeOKT(sis.GetAZResourcesForPath(db.ResourcePath{ServiceType: "second", ResourceName: "capacity"}))(t)), 5) // gives out total, any and unknown, too
	s.MustDBInsert(&db.AZResource{
		ResourceID:       secondCapacity,
		AvailabilityZone: "test",
		Path:             db.AZResourcePath{ServiceType: "second", ResourceName: "capacity", AvailabilityZone: "test"},
		RawCapacity:      123,
	})
	<-s.Cluster.SIC.OnInvalidate
	sis = s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, len(must.BeOKT(sis.GetAZResourcesForPath(db.ResourcePath{ServiceType: "second", ResourceName: "capacity"}))(t)), 6)

	// check rate deletion
	assert.Equal(t, len(must.BeOKT(sis.GetRatesForType("first"))(t)), 4)
	s.MustDBExec("DELETE FROM rates WHERE id = $1", firstObjectsCreate)
	<-s.Cluster.SIC.OnInvalidate
	sis = s.Cluster.SIC.GetSnapshot()
	assert.Equal(t, len(must.BeOKT(sis.GetRatesForType("first"))(t)), 3)
}
