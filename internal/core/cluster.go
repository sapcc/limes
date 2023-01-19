/*******************************************************************************
*
* Copyright 2017 SAP SE
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

package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"

	"github.com/gophercloud/gophercloud"
	"github.com/open-policy-agent/opa/rego"
	"github.com/sapcc/go-api-declarations/limes"
	limesrates "github.com/sapcc/go-api-declarations/limes/rates"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/logg"
	"github.com/sapcc/go-bits/must"
	"github.com/sapcc/go-bits/osext"
	yaml "gopkg.in/yaml.v2"

	"github.com/sapcc/limes/internal/util"
)

// Cluster contains all configuration and runtime information for the target
// cluster.
type Cluster struct {
	Auth              *AuthSession
	Config            ClusterConfiguration
	DiscoveryPlugin   DiscoveryPlugin
	QuotaPlugins      map[string]QuotaPlugin
	CapacityPlugins   map[string]CapacityPlugin
	Authoritative     bool
	QuotaConstraints  *QuotaConstraintSet
	LowPrivilegeRaise struct {
		LimitsForDomains  map[string]map[string]LowPrivilegeRaiseLimit
		LimitsForProjects map[string]map[string]LowPrivilegeRaiseLimit
	}
	OPA struct {
		ProjectQuotaQuery *rego.PreparedEvalQuery
		DomainQuotaQuery  *rego.PreparedEvalQuery
	}
}

// NewCluster creates a new Cluster instance with the given ID and
// configuration, and also initializes all quota and capacity plugins. Errors
// will be logged when some of the requested plugins cannot be found.
func NewCluster(config ClusterConfiguration) *Cluster {
	c := &Cluster{
		Config:          config,
		QuotaPlugins:    make(map[string]QuotaPlugin),
		CapacityPlugins: make(map[string]CapacityPlugin),
		Authoritative:   osext.GetenvBool("LIMES_AUTHORITATIVE"),
	}

	c.DiscoveryPlugin = DiscoveryPluginRegistry.Instantiate(config.Discovery.Method)
	if c.DiscoveryPlugin == nil {
		logg.Fatal("setup for discovery method %s failed: no suitable discovery plugin found", config.Discovery.Method)
	}

	for _, srv := range config.Services {
		plugin := QuotaPluginRegistry.Instantiate(srv.Type)
		if plugin == nil {
			logg.Error("skipping service %s: no suitable collector plugin found", srv.Type)
			continue
		}
		c.QuotaPlugins[srv.Type] = plugin
	}

	for _, capa := range config.Capacitors {
		plugin := CapacityPluginRegistry.Instantiate(capa.Type)
		if plugin == nil {
			logg.Error("skipping capacitor %s: no suitable capacity plugin found", capa.ID)
			continue
		}
		c.CapacityPlugins[capa.ID] = plugin
	}

	c.SetupOPA(os.Getenv("LIMES_OPA_DOMAIN_QUOTA_POLICY_PATH"), os.Getenv("LIMES_OPA_PROJECT_QUOTA_POLICY_PATH"))

	return c
}

func (c *Cluster) SetupOPA(domainQuotaPolicyPath, projectQuotaPolicyPath string) {
	if domainQuotaPolicyPath == "" {
		c.OPA.DomainQuotaQuery = nil
	} else {
		domainModule := must.Return(os.ReadFile(domainQuotaPolicyPath))
		query, err := rego.New(
			rego.Query("violations = data.limes.violations"),
			rego.Module("limes.rego", string(domainModule)),
		).PrepareForEval(context.Background())
		if err != nil {
			logg.Fatal("preparing OPA domain query failed: %s", err)
		}
		c.OPA.DomainQuotaQuery = &query
	}

	if projectQuotaPolicyPath == "" {
		c.OPA.ProjectQuotaQuery = nil
	} else {
		projectModule := must.Return(os.ReadFile(projectQuotaPolicyPath))
		projectQuery, err := rego.New(
			rego.Query("violations = data.limes.violations"),
			rego.Module("limes.rego", string(projectModule)),
		).PrepareForEval(context.Background())
		if err != nil {
			logg.Fatal("preparing OPA project query failed: %s", err)
		}
		c.OPA.ProjectQuotaQuery = &projectQuery
	}
}

// Connect calls Connect() on all AuthParameters for this Cluster, thus ensuring
// that all ProviderClient instances are available. It also calls Init() on all
// quota plugins.
//
// It also loads the QuotaConstraints for this cluster, if configured. The
// LowPrivilegeRaise.Limits fields are also initialized here. We also validate
// if Config.ResourceBehavior[].ScalesWith refers to existing resources.
//
// We cannot do any of this earlier because we only know all resources after
// calling Init() on all quota plugins.
func (c *Cluster) Connect() (err error) {
	c.Auth, err = AuthToOpenstack()
	if err != nil {
		return fmt.Errorf("failed to authenticate: %s", err.Error())
	}
	provider := c.Auth.ProviderClient
	eo := c.Auth.EndpointOpts

	//initialize discovery plugin
	err = yaml.UnmarshalStrict([]byte(c.Config.Discovery.Parameters), c.DiscoveryPlugin)
	if err != nil {
		return fmt.Errorf("failed to supply params to discovery method: %w", err)
	}
	err = c.DiscoveryPlugin.Init(provider, eo)
	if err != nil {
		return fmt.Errorf("failed to initialize discovery method: %w", util.UnpackError(err))
	}

	//initialize quota plugins
	for _, srv := range c.Config.Services {
		scrapeSubresources := map[string]bool{}
		for _, resName := range c.Config.Subresources[srv.Type] {
			scrapeSubresources[resName] = true
		}

		plugin := c.QuotaPlugins[srv.Type]
		err = yaml.UnmarshalStrict([]byte(srv.Parameters), plugin)
		if err != nil {
			return fmt.Errorf("failed to supply params to service %s: %w", srv.Type, err)
		}
		err := plugin.Init(provider, eo, scrapeSubresources)
		if err != nil {
			return fmt.Errorf("failed to initialize service %s: %w", srv.Type, util.UnpackError(err))
		}
	}

	//initialize capacity plugins
	scrapeSubcapacities := make(map[string]map[string]bool)
	for serviceType, resourceNames := range c.Config.Subcapacities {
		m := make(map[string]bool)
		for _, resourceName := range resourceNames {
			m[resourceName] = true
		}
		scrapeSubcapacities[serviceType] = m
	}
	for _, capa := range c.Config.Capacitors {
		plugin := c.CapacityPlugins[capa.ID]
		err = yaml.UnmarshalStrict([]byte(capa.Parameters), plugin)
		if err != nil {
			return fmt.Errorf("failed to supply params to capacitor %s: %w", capa.ID, err)
		}
		err := plugin.Init(provider, eo, scrapeSubcapacities)
		if err != nil {
			return fmt.Errorf("failed to initialize capacitor %s: %w", capa.ID, util.UnpackError(err))
		}
	}

	//load quota constraints
	constraintPath := os.Getenv("LIMES_CONSTRAINTS_PATH")
	if constraintPath != "" && c.QuotaConstraints == nil {
		var errs []error
		c.QuotaConstraints, errs = NewQuotaConstraints(c, constraintPath)
		if len(errs) > 0 {
			for _, err := range errs {
				logg.Error(err.Error())
			}
			return errors.New("cannot load quota constraints (see errors above)")
		}
	}

	//parse low-privilege raise limits
	c.LowPrivilegeRaise.LimitsForDomains, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForDomains, "domain")
	if err != nil {
		return fmt.Errorf("could not parse low-privilege raise limit: %s", err.Error())
	}
	c.LowPrivilegeRaise.LimitsForProjects, err = c.parseLowPrivilegeRaiseLimits(
		c.Config.LowPrivilegeRaise.Limits.ForProjects, "project")
	if err != nil {
		return fmt.Errorf("could not parse low-privilege raise limit: %s", err.Error())
	}

	//validate scaling relations
	for _, behavior := range c.Config.ResourceBehaviors {
		b := behavior.Compiled
		if b.ScalesWithResourceName == "" {
			continue
		}
		if !c.HasResource(b.ScalesWithServiceType, b.ScalesWithResourceName) {
			return fmt.Errorf(`resources matching "%s" scale with unknown resource "%s/%s"`,
				behavior.FullResourceName, b.ScalesWithServiceType, b.ScalesWithResourceName)
		}
	}

	return nil
}

var (
	percentOfClusterRx              = regexp.MustCompile(`^([0-9.]+)\s*% of cluster capacity$`)
	untilPercentOfClusterAssignedRx = regexp.MustCompile(`^until ([0-9.]+)\s*% of cluster capacity is assigned$`)
)

func (c Cluster) parseLowPrivilegeRaiseLimits(inputs map[string]map[string]string, scopeType string) (map[string]map[string]LowPrivilegeRaiseLimit, error) {
	result := make(map[string]map[string]LowPrivilegeRaiseLimit)
	for srvType, quotaPlugin := range c.QuotaPlugins {
		result[srvType] = make(map[string]LowPrivilegeRaiseLimit)
		for _, res := range quotaPlugin.Resources() {
			limit, exists := inputs[srvType][res.Name]
			if !exists {
				continue
			}

			match := percentOfClusterRx.FindStringSubmatch(limit)
			if match != nil {
				percent, err := strconv.ParseFloat(match[1], 64)
				if err != nil {
					return nil, err
				}
				if percent < 0 || percent > 100 {
					return nil, fmt.Errorf("value out of range: %s%%", match[1])
				}
				result[srvType][res.Name] = LowPrivilegeRaiseLimit{
					PercentOfClusterCapacity: percent,
				}
				continue
			}

			//the "until X% of cluster capacity is assigned" syntax is only allowed for domains
			if scopeType == "domain" {
				match := untilPercentOfClusterAssignedRx.FindStringSubmatch(limit)
				if match != nil {
					percent, err := strconv.ParseFloat(match[1], 64)
					if err != nil {
						return nil, err
					}
					if percent < 0 || percent > 100 {
						return nil, fmt.Errorf("value out of range: %s%%", match[1])
					}
					result[srvType][res.Name] = LowPrivilegeRaiseLimit{
						UntilPercentOfClusterCapacityAssigned: percent,
					}
					continue
				}
			}

			rawValue, err := res.Unit.Parse(limit)
			if err != nil {
				return nil, err
			}
			result[srvType][res.Name] = LowPrivilegeRaiseLimit{
				AbsoluteValue: rawValue,
			}
		}
	}
	return result, nil
}

// ProviderClient returns the gophercloud.ProviderClient for this cluster. This
// returns nil unless Connect() is called first. (This usually happens at
// program startup time for the current cluster.)
func (c *Cluster) ProviderClient() (*gophercloud.ProviderClient, gophercloud.EndpointOpts) {
	if c.Auth == nil {
		return nil, gophercloud.EndpointOpts{}
	}
	return c.Auth.ProviderClient, c.Auth.EndpointOpts
}

// ServiceTypesInAlphabeticalOrder can be used when service types need to be
// iterated over in a stable order (mostly to ensure deterministic behavior in unit tests).
func (c *Cluster) ServiceTypesInAlphabeticalOrder() []string {
	result := make([]string, 0, len(c.QuotaPlugins))
	for serviceType, quotaPlugin := range c.QuotaPlugins {
		if quotaPlugin != nil { //defense in depth (nil values should never be stored in the map anyway)
			result = append(result, serviceType)
		}
	}
	sort.Strings(result)
	return result
}

// HasService checks whether the given service is enabled in this cluster.
func (c *Cluster) HasService(serviceType string) bool {
	return c.QuotaPlugins[serviceType] != nil
}

// HasResource checks whether the given service is enabled in this cluster and
// whether it advertises the given resource.
func (c *Cluster) HasResource(serviceType, resourceName string) bool {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return false
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return true
		}
	}
	return false
}

// InfoForResource finds the plugin for the given serviceType and finds within that
// plugin the ResourceInfo for the given resourceName. If the service or
// resource does not exist, an empty ResourceInfo (with .Unit == UnitNone and
// .Category == "") is returned.
func (c *Cluster) InfoForResource(serviceType, resourceName string) limesresources.ResourceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limesresources.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
	}
	for _, res := range plugin.Resources() {
		if res.Name == resourceName {
			return res
		}
	}
	return limesresources.ResourceInfo{Name: resourceName, Unit: limes.UnitNone}
}

// InfoForService finds the plugin for the given serviceType and returns its
// ServiceInfo(), or an empty ServiceInfo (with .Area == "") when no such
// service exists in this cluster.
func (c *Cluster) InfoForService(serviceType string) limes.ServiceInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limes.ServiceInfo{Type: serviceType}
	}
	return plugin.ServiceInfo()
}

// GetServiceTypesForArea returns all service types that belong to the given area.
func (c *Cluster) GetServiceTypesForArea(area string) (serviceTypes []string) {
	for serviceType, plugin := range c.QuotaPlugins {
		if plugin.ServiceInfo().Area == area {
			serviceTypes = append(serviceTypes, serviceType)
		}
	}
	return
}

// BehaviorForResource returns the ResourceBehavior for the given resource in
// the given scope.
//
// `scopeName` should be empty for cluster resources, equal to the domain name
// for domain resources, or equal to `$DOMAIN_NAME/$PROJECT_NAME` for project
// resources.
func (c *Cluster) BehaviorForResource(serviceType, resourceName, scopeName string) ResourceBehavior {
	//default behavior
	result := ResourceBehavior{
		MaxBurstMultiplier: c.Config.Bursting.MaxMultiplier,
	}

	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, behaviorConfig := range c.Config.ResourceBehaviors {
		behavior := behaviorConfig.Compiled
		if !behavior.FullResourceNameRx.MatchString(fullName) {
			continue
		}
		if scopeName != "" && behavior.ScopeRx != nil && !behavior.ScopeRx.MatchString(scopeName) {
			continue
		}

		// merge `behavior` into `result`
		if result.MaxBurstMultiplier > behavior.MaxBurstMultiplier {
			result.MaxBurstMultiplier = behavior.MaxBurstMultiplier
		}
		if behavior.OvercommitFactor != 0 {
			result.OvercommitFactor = behavior.OvercommitFactor
		}
		if behavior.ScalingFactor != 0 {
			result.ScalesWithServiceType = behavior.ScalesWithServiceType
			result.ScalesWithResourceName = behavior.ScalesWithResourceName
			result.ScalingFactor = behavior.ScalingFactor
		}
		if result.MinNonZeroProjectQuota < behavior.MinNonZeroProjectQuota {
			result.MinNonZeroProjectQuota = behavior.MinNonZeroProjectQuota
		}
		if len(behavior.Annotations) > 0 && result.Annotations == nil {
			result.Annotations = make(map[string]interface{})
		}
		for k, v := range behavior.Annotations {
			result.Annotations[k] = v
		}
	}

	return result
}

// QuotaDistributionConfigForResource returns the QuotaDistributionConfiguration
// for the given resource.
func (c *Cluster) QuotaDistributionConfigForResource(serviceType, resourceName string) QuotaDistributionConfiguration {
	//check for specific behavior
	fullName := serviceType + "/" + resourceName
	for _, dmCfg := range c.Config.QuotaDistributionConfigs {
		if dmCfg.FullResourceNameRx.MatchString(fullName) {
			return *dmCfg
		}
	}

	//default behavior
	return QuotaDistributionConfiguration{Model: limesresources.HierarchicalQuotaDistribution}
}

// HasUsageForRate checks whether the given service is enabled in this cluster and
// whether it scrapes usage for the given rate.
func (c *Cluster) HasUsageForRate(serviceType, rateName string) bool {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return false
	}
	for _, rate := range plugin.Rates() {
		if rate.Name == rateName {
			return true
		}
	}
	return false
}

// InfoForRate finds the plugin for the given serviceType and finds within that
// plugin the RateInfo for the given rateName. If the service or rate does not
// exist, an empty RateInfo (with .Unit == UnitNone) is returned. Note that this
// only returns non-empty RateInfos for rates where a usage is reported. There
// may be rates that only have a limit, as defined in the ClusterConfiguration.
func (c *Cluster) InfoForRate(serviceType, rateName string) limesrates.RateInfo {
	plugin := c.QuotaPlugins[serviceType]
	if plugin == nil {
		return limesrates.RateInfo{Name: rateName, Unit: limes.UnitNone}
	}
	for _, rate := range plugin.Rates() {
		if rate.Name == rateName {
			return rate
		}
	}
	return limesrates.RateInfo{Name: rateName, Unit: limes.UnitNone}
}
