// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package limesresources

import (
	"github.com/sapcc/go-api-declarations/internal/marshal"
	"github.com/sapcc/go-api-declarations/limes"
)

// MarshalJSON implements the json.Marshaler interface.
func (r ClusterAvailabilityZoneReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(r) }

// MarshalJSON implements the json.Marshaler interface.
func (r ClusterResourceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(r) }

// MarshalJSON implements the json.Marshaler interface.
func (s ClusterServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }

// MarshalJSON implements the json.Marshaler interface.
func (r DomainResourceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(r) }

// MarshalJSON implements the json.Marshaler interface.
func (s DomainServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }

// MarshalJSON implements the json.Marshaler interface.
func (r ProjectResourceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(r) }

// MarshalJSON implements the json.Marshaler interface.
func (s ProjectServiceReports) MarshalJSON() ([]byte, error) { return marshal.MapAsList(s) }

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *ClusterAvailabilityZoneReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterAvailabilityZoneReport) limes.AvailabilityZone { return r.Name })
	*r = ClusterAvailabilityZoneReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *ClusterResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ClusterResourceReport) ResourceName { return r.Name })
	*r = ClusterResourceReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (s *ClusterServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ClusterServiceReport) limes.ServiceType { return s.Type })
	*s = ClusterServiceReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *DomainResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *DomainResourceReport) ResourceName { return r.Name })
	*r = DomainResourceReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (s *DomainServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *DomainServiceReport) limes.ServiceType { return s.Type })
	*s = DomainServiceReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (r *ProjectResourceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(r *ProjectResourceReport) ResourceName { return r.Name })
	*r = ProjectResourceReports(m)
	return err
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (s *ProjectServiceReports) UnmarshalJSON(buf []byte) error {
	m, err := marshal.MapFromList(buf, func(s *ProjectServiceReport) limes.ServiceType { return s.Type })
	*s = ProjectServiceReports(m)
	return err
}
