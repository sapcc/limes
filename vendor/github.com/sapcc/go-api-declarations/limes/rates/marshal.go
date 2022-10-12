/*******************************************************************************
*
* Copyright 2022 SAP SE
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

package limesrates

import (
	"github.com/sapcc/go-api-declarations/internal/marshal"
)

func (r ClusterRateReports) MarshalJSON() ([]byte, error)    { return marshal.MapAsList(r) }
func (s ClusterServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }
func (r ProjectRateReports) MarshalJSON() ([]byte, error)    { return marshal.MapAsList(r) }
func (s ProjectServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }

func (r *ClusterRateReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterRateReport) string { return r.Name })
	*r = ClusterRateReports(m)
	return err
}
func (s *ClusterServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ClusterServiceReport) string { return s.Type })
	*s = ClusterServiceReports(m)
	return err
}
func (r *ProjectRateReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ProjectRateReport) string { return r.Name })
	*r = ProjectRateReports(m)
	return err
}
func (s *ProjectServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ProjectServiceReport) string { return s.Type })
	*s = ProjectServiceReports(m)
	return err
}
