// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesresources

import (
	"github.com/sapcc/go-api-declarations/internal/marshal"
	"github.com/sapcc/go-api-declarations/limes"
)

func (r ClusterAvailabilityZoneReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(r) }
func (r ClusterResourceReports) MarshalJSON() ([]byte, error)         { return marshal.MapAsList(r) }
func (s ClusterServiceReports) MarshalJSON() ([]byte, error)          { return marshal.MapAsList(s) }
func (r DomainResourceReports) MarshalJSON() ([]byte, error)          { return marshal.MapAsList(r) }
func (s DomainServiceReports) MarshalJSON() ([]byte, error)           { return marshal.MapAsList(s) }
func (r ProjectResourceReports) MarshalJSON() ([]byte, error)         { return marshal.MapAsList(r) }
func (s ProjectServiceReports) MarshalJSON() ([]byte, error)          { return marshal.MapAsList(s) }

func (r *ClusterAvailabilityZoneReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterAvailabilityZoneReport) limes.AvailabilityZone { return r.Name })
	*r = ClusterAvailabilityZoneReports(m)
	return err
}
func (r *ClusterResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterResourceReport) ResourceName { return r.Name })
	*r = ClusterResourceReports(m)
	return err
}
func (s *ClusterServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ClusterServiceReport) limes.ServiceType { return s.Type })
	*s = ClusterServiceReports(m)
	return err
}
func (r *DomainResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *DomainResourceReport) ResourceName { return r.Name })
	*r = DomainResourceReports(m)
	return err
}
func (s *DomainServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *DomainServiceReport) limes.ServiceType { return s.Type })
	*s = DomainServiceReports(m)
	return err
}
func (r *ProjectResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ProjectResourceReport) ResourceName { return r.Name })
	*r = ProjectResourceReports(m)
	return err
}
func (s *ProjectServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ProjectServiceReport) limes.ServiceType { return s.Type })
	*s = ProjectServiceReports(m)
	return err
}
