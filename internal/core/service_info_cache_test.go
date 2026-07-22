// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core_test

import (
	"testing"

	"github.com/gophercloud/gophercloud/v2"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/easypg"
	"github.com/sapcc/go-bits/httptest"
	"github.com/sapcc/go-bits/must"
	"go.xyrillian.de/gg/assert"
	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	"github.com/sapcc/limes/internal/core"
	"github.com/sapcc/limes/internal/db"
	"github.com/sapcc/limes/internal/test"
	"github.com/sapcc/limes/internal/test/common_fixtures"
)

var configJSON = string(must.Return(httptest.NewJQModifiableJSONString("{}", "configJSON").
	ModifyWithVariable(". * $ref", common_fixtures.AreaLiquidFirstSecond).
	ModifyWithVariable(".availability_zones = $ref", common_fixtures.AZsOneTwo).
	ModifyWithVariable(".discovery = $ref", common_fixtures.DiscoveryBerlinDresdenParis).
	MarshalJSON()))

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

	// filter by ServiceArea
	filtered = sis.Filter(core.ServiceInfoFilter{ServiceArea: Some("first")})
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

	// filter by Category matching the serviceType yields entries which have no explicit category
	filtered = sis.Filter(core.ServiceInfoFilter{Category: Some[liquid.CategoryName]("second")})
	resources, ok = filtered.GetResourcesForType("second")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(resources), 1)
	_, ok = resources["things"]
	assert.Equal(t, ok, true)
	_, ok = resources["capacity"]
	assert.Equal(t, ok, false)

	// filter by RateName
	filtered = sis.Filter(core.ServiceInfoFilter{RateName: Some[liquid.RateName]("objects:create")})
	rates, ok = filtered.GetRatesForType("first")
	assert.Equal(t, ok, true)
	assert.Equal(t, len(rates), 1)
	_, ok = rates["objects:create"]
	assert.Equal(t, ok, true)

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

func TestServiceInfoCacheGetByID(t *testing.T) {
	serviceInfoFirst := test.DefaultLiquidServiceInfo("First")
	serviceInfoFirst.Rates = map[liquid.RateName]liquid.RateInfo{
		"objects:create": {DisplayName: "Object Creations", Topology: liquid.FlatTopology, HasUsage: true},
		"objects:delete": {DisplayName: "Object Deletions", Unit: liquid.UnitMebibytes, Topology: liquid.FlatTopology, HasUsage: true},
	}
	serviceInfoFirst.Resources = map[liquid.ResourceName]liquid.ResourceInfo{}
	serviceInfoFirst.Categories = map[liquid.CategoryName]liquid.CategoryInfo{}
	s := test.NewSetup(t,
		test.WithConfig(configJSON),
		test.WithPersistedServiceInfo("first", serviceInfoFirst),
		test.WithPersistedServiceInfo("second", test.DefaultLiquidServiceInfo("Second")),
	)
	s.Cluster.Connect(s.Ctx, nil, gophercloud.EndpointOpts{}, func(serviceType db.ServiceType) (core.LiquidClient, error) { return nil, nil }, options.FromPointer(easypg.BuildDBURL(t)))
	t.Cleanup(func() { s.Cluster.SIC.Close() })

	sis := s.Cluster.SIC.GetSnapshot()

	// GetServiceForID
	firstServiceID := s.GetServiceID("first")
	svc, ok := sis.GetServiceForID(firstServiceID)
	assert.Equal(t, ok, true)
	assert.Equal(t, svc.Type, "first")
	assert.Equal(t, svc.DisplayName, "First")
	_, ok = sis.GetServiceForID(99999) // non-existent ID
	assert.Equal(t, ok, false)

	// GetResourceForID
	secondCapacityID := s.GetResourceID("second", "capacity")
	res, ok := sis.GetResourceForID(secondCapacityID)
	assert.Equal(t, ok, true)
	assert.Equal(t, res.Name, "capacity")
	assert.Equal(t, res.DisplayName, "Capacity")
	_, ok = sis.GetResourceForID(99999) // non-existent ID
	assert.Equal(t, ok, false)

	// GetAZResourceForID (already tested above but verify via index)
	secondCapacityAZOne := s.GetAZResourceID("second", "capacity", "az-one")
	azRes, ok := sis.GetAZResourceForID(secondCapacityAZOne)
	assert.Equal(t, ok, true)
	assert.Equal(t, azRes.Path.ServiceType, "second")
	assert.Equal(t, azRes.Path.ResourceName, "capacity")
	assert.Equal(t, azRes.Path.AvailabilityZone, "az-one")
	_, ok = sis.GetAZResourceForID(99999) // non-existent ID
	assert.Equal(t, ok, false)

	// GetRateForID
	firstObjectsCreateID := s.GetRateID("first", "objects:create")
	rate, ok := sis.GetRateForID(firstObjectsCreateID)
	assert.Equal(t, ok, true)
	assert.Equal(t, rate.Name, "objects:create")
	assert.Equal(t, rate.DisplayName, "Object Creations")
	_, ok = sis.GetRateForID(99999) // non-existent ID
	assert.Equal(t, ok, false)

	// Verify ID indexes are updated after invalidation
	s.MustDBExec("UPDATE services SET display_name = 'UpdatedFirst' WHERE id = $1", firstServiceID)
	<-s.Cluster.SIC.OnInvalidate
	sis = s.Cluster.SIC.GetSnapshot()
	svc, ok = sis.GetServiceForID(firstServiceID)
	assert.Equal(t, ok, true)
	assert.Equal(t, svc.DisplayName, "UpdatedFirst")

	// Verify FilteredServiceInfoSnapshot also delegates correctly
	filtered := sis.Filter(core.ServiceInfoFilter{ServiceType: Some[db.ServiceType]("second")})
	res, ok = filtered.GetResourceForID(secondCapacityID)
	assert.Equal(t, ok, true)
	assert.Equal(t, res.Name, "capacity")
	// rate from "first" service should NOT be accessible when filtered to "second"
	_, ok = filtered.GetRateForID(firstObjectsCreateID)
	assert.Equal(t, ok, false)
	// service "first" should NOT be accessible when filtered to "second"
	_, ok = filtered.GetServiceForID(firstServiceID)
	assert.Equal(t, ok, false)
}
