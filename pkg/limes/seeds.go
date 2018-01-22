/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package limes

import (
	"fmt"
	"io/ioutil"
	"regexp"
	"strconv"
	"strings"

	yaml "gopkg.in/yaml.v2"
)

//QuotaSeeds contains the contents of the seed configuration file for a limes.Cluster.
type QuotaSeeds struct {
	//Indexed by domain name.
	Domains map[string]QuotaSeedValues
	//Indexed by domain name, then by project name.
	Projects map[string]map[string]QuotaSeedValues
}

//QuotaSeedValues contains the quota seed for a single domain or project. The
//outer key is the service type, the inner key is the resource name.
type QuotaSeedValues map[string]map[string]uint64

//NewQuotaSeeds parses the quota seed at `seedConfigPath`. The `cluster`
//argument is required because quota values need to be converted into the base
//unit of their resource, for which we need to access the
//QuotaPlugin.Resources(). Hence, `cluster.Init()` needs to have been called
//before this function is called.
func NewQuotaSeeds(cluster *Cluster, seedConfigPath string) (*QuotaSeeds, []error) {
	buf, err := ioutil.ReadFile(seedConfigPath)
	if err != nil {
		return nil, []error{err}
	}

	var data struct {
		//          dom/proj   srvType    resName
		Domains  map[string]map[string]map[string]string `yaml:"domains"`
		Projects map[string]map[string]map[string]string `yaml:"projects"`
	}
	err = yaml.Unmarshal(buf, &data)
	if err != nil {
		return nil, []error{err}
	}

	result := &QuotaSeeds{
		Domains:  make(map[string]QuotaSeedValues),
		Projects: make(map[string]map[string]QuotaSeedValues),
	}
	var errors []error

	//parse quota seed values for domains
	for domainName, domainData := range data.Domains {
		values, errs := compileQuotaSeedValues(cluster, domainData)
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("invalid seed values for domain %s: %s", domainName, err.Error()),
			)
		}
		result.Domains[domainName] = values
	}

	//parse quota seed values for projects
	for projectAndDomainName, projectData := range data.Projects {
		fields := strings.SplitN(projectAndDomainName, "/", 2)
		if len(fields) < 2 {
			errors = append(errors,
				fmt.Errorf("missing domain name for project %s", projectAndDomainName),
			)
			continue
		}
		domainName := fields[0]
		projectName := fields[1]

		values, errs := compileQuotaSeedValues(cluster, projectData)
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("invalid seed values for project %s: %s", projectAndDomainName, err.Error()),
			)
		}

		if _, exists := result.Projects[domainName]; !exists {
			result.Projects[domainName] = make(map[string]QuotaSeedValues)
		}
		result.Projects[domainName][projectName] = values
	}

	//do not attempt to validate if the parsing already caused errors (a
	//consistent, but invalid quota seed might look inconsistent because values
	//that don't parse were not initialized in `result`)
	if len(errors) > 0 {
		return result, errors
	}

	//validate that project quotas fit into domain quotas
	allDomainNames := make(map[string]bool)
	for domainName := range result.Domains {
		allDomainNames[domainName] = true
	}
	for domainName := range result.Projects {
		allDomainNames[domainName] = true
	}
	for domainName := range allDomainNames {
		errs := validateQuotaSeedValues(cluster, result.Domains[domainName], result.Projects[domainName])
		for _, err := range errs {
			errors = append(errors,
				fmt.Errorf("inconsistent seed values for domain %s: %s", domainName, err.Error()),
			)
		}
	}

	return result, errors
}

func compileQuotaSeedValues(cluster *Cluster, data map[string]map[string]string) (values QuotaSeedValues, errors []error) {
	values = make(QuotaSeedValues)

	for serviceType, serviceData := range data {
		if !cluster.HasService(serviceType) {
			errors = append(errors, fmt.Errorf("no such service: %s", serviceType))
			continue
		}
		values[serviceType] = make(map[string]uint64)

		for resourceName, quotaValueStr := range serviceData {
			quotaValue, err := parseQuotaValue(
				serviceType,
				cluster.InfoForResource(serviceType, resourceName),
				quotaValueStr,
			)
			if err != nil {
				errors = append(errors, err)
				continue
			}
			values[serviceType][resourceName] = quotaValue
		}
	}

	return values, errors
}

var measuredQuotaValueRx = regexp.MustCompile(`^\s*([0-9]+)\s*([A-Za-z]+)$`)

func parseQuotaValue(serviceType string, resource ResourceInfo, str string) (uint64, error) {
	//for countable resources, expect a number only
	if resource.Unit == UnitNone {
		value, err := strconv.ParseUint(strings.TrimSpace(str), 10, 64)
		if err != nil {
			err = fmt.Errorf("invalid value %q for %s/%s: %s",
				str, serviceType, resource.Name, err.Error())
		}
		return value, err
	}

	//for measured resources, expect a number plus unit
	fields := strings.Fields(str)
	if len(fields) != 2 {
		return 0, fmt.Errorf("value %q for %s/%s does not match expected format \"<number> <unit>\"",
			str, serviceType, resource.Name)
	}

	number, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q for %s/%s: %s",
			str, serviceType, resource.Name, err.Error())
	}
	value := ValueWithUnit{
		Value: number,
		//no need to validate unit string here; that will happen implicitly during .ConvertTo()
		Unit: Unit(fields[1]),
	}
	converted, err := value.ConvertTo(resource.Unit)
	return converted.Value, err
}

func validateQuotaSeedValues(cluster *Cluster, domainQuotas QuotaSeedValues, projectQuotas map[string]QuotaSeedValues) (errors []error) {
	projectQuotaSums := make(QuotaSeedValues)
	for _, projectValues := range projectQuotas {
		for serviceType, serviceValues := range projectValues {
			if _, exists := projectQuotaSums[serviceType]; !exists {
				projectQuotaSums[serviceType] = make(map[string]uint64)
			}
			for resourceName, quota := range serviceValues {
				projectQuotaSums[serviceType][resourceName] += quota
			}
		}
	}

	for serviceType, serviceSums := range projectQuotaSums {
		for resourceName, projectQuotaSum := range serviceSums {
			domainQuota := domainQuotas[serviceType][resourceName]
			if projectQuotaSum > domainQuota {
				unit := cluster.InfoForResource(serviceType, resourceName).Unit
				errors = append(errors, fmt.Errorf(
					"sum of project quotas (%s) for %s/%s exceeds domain quota (%s)",
					ValueWithUnit{projectQuotaSum, unit},
					serviceType, resourceName,
					ValueWithUnit{domainQuota, unit},
				))
			}
		}
	}

	return
}
