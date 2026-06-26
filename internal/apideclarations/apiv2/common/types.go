// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/sapcc/go-api-declarations/liquid"

	"github.com/sapcc/limes/internal/db"
)

// RefInService contains a pair of service type and resource or rate name.
// Within JSON documents, this appears as a string in the "service/resource" or "service/rate" format.
type RefInService[S, R ~string] struct {
	ServiceType S
	Name        R
}

// MarshalText implements the [encoding.TextUnmarshaler] interface.
func (r *RefInService[S, R]) MarshalText() ([]byte, error) {
	return fmt.Appendf(nil, "%s/%s", string(r.ServiceType), string(r.Name)), nil
}

// UnmarshalText implements the [encoding.TextUnmarshaler] interface.
func (r *RefInService[S, R]) UnmarshalText(text []byte) error {
	fields := strings.Split(string(text), "/")
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return fmt.Errorf(`expected a value of the form "%s/%s", but got %q`,
			reflect.TypeFor[S]().Name(),
			reflect.TypeFor[R]().Name(),
			string(text),
		)
	}

	*r = RefInService[S, R]{
		ServiceType: S(fields[0]),
		Name:        R(fields[1]),
	}
	return nil
}

// ResourceRef is a reference to a specific resource that fits into a single string,
// using the serialization format "service/resource".
type ResourceRef = RefInService[db.ServiceType, liquid.ResourceName]
