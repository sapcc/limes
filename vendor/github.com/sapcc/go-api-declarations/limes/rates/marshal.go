// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesrates

import (
	"github.com/sapcc/go-api-declarations/internal/marshal"
	"github.com/sapcc/go-api-declarations/limes"
)

func (r ClusterRateReports) MarshalJSON() ([]byte, error)    { return marshal.MapAsList(r) }
func (s ClusterServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }
func (r ProjectRateReports) MarshalJSON() ([]byte, error)    { return marshal.MapAsList(r) }
func (s ProjectServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }

func (r *ClusterRateReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterRateReport) RateName { return r.Name })
	*r = ClusterRateReports(m)
	return err
}
func (s *ClusterServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ClusterServiceReport) limes.ServiceType { return s.Type })
	*s = ClusterServiceReports(m)
	return err
}
func (r *ProjectRateReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ProjectRateReport) RateName { return r.Name })
	*r = ProjectRateReports(m)
	return err
}
func (s *ProjectServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ProjectServiceReport) limes.ServiceType { return s.Type })
	*s = ProjectServiceReports(m)
	return err
}
