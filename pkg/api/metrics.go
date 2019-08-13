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

package api

import (
	"github.com/prometheus/client_golang/prometheus"
)

var lowPrivilegeRaiseMetricLabels = []string{"os_cluster", "service", "resource"}

var lowPrivilegeRaiseDomainSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_low_privilege_raise_domain_success",
		Help: "Counter for successful quota auto approval for some service/resource per domain.",
	}, lowPrivilegeRaiseMetricLabels)

var lowPrivilegeRaiseDomainFailureCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_low_privilege_raise_domain_failure",
		Help: "Counter for failed quota auto approval for some service/resource per domain.",
	}, lowPrivilegeRaiseMetricLabels)

var lowPrivilegeRaiseProjectSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_low_privilege_raise_project_success",
		Help: "Counter for successful quota auto approval for some service/resource per project.",
	}, lowPrivilegeRaiseMetricLabels)

var lowPrivilegeRaiseProjectFailureCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_low_privilege_raise_project_failure",
		Help: "Counter for failed quota auto approval for some service/resource per project.",
	}, lowPrivilegeRaiseMetricLabels)

var auditEventPublishSuccessCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_successful_auditevent_publish",
		Help: "Counter for successful audit event publish to RabbitMQ server.",
	},
	[]string{"os_cluster"})

var auditEventPublishFailedCounter = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "limes_failed_auditevent_publish",
		Help: "Counter for failed audit event publish to RabbitMQ server.",
	},
	[]string{"os_cluster"})

func init() {
	prometheus.MustRegister(lowPrivilegeRaiseDomainSuccessCounter)
	prometheus.MustRegister(lowPrivilegeRaiseDomainFailureCounter)
	prometheus.MustRegister(lowPrivilegeRaiseProjectSuccessCounter)
	prometheus.MustRegister(lowPrivilegeRaiseProjectFailureCounter)
	prometheus.MustRegister(auditEventPublishSuccessCounter)
	prometheus.MustRegister(auditEventPublishFailedCounter)
}
