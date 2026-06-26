// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package opts

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	. "go.xyrillian.de/gg/option"
)

var (
	// timeFormats lists all allowed formats supported by the time package, except "Unix" (see below).
	nonUnixTimeFormats = map[string]string{
		"RFC3339Nano": time.RFC3339Nano,
		"RFC3339":     time.RFC3339,
		"DateTime":    time.DateTime,
		"DateOnly":    time.DateOnly,
	}
	// unixFormat is an identifier for the time to be interpreted as unix-seconds since epoch.
	unixTimeFormat                = "Unix"
	supportedHumanReadableFormats = strings.Join(slices.Sorted(slices.Values(append(slices.Collect(maps.Keys(nonUnixTimeFormats)), unixTimeFormat))), ", ")
)

// parseQTag parses a q struct tag value into its key name, optional format, optional value discriminant, and required flag.
// The tag format is "key_name" or "key_name,format:FormatName,required,value:ValueName".
// Examples:
//
//	`q:"updated_at"`                      → key="updated_at", format=None, value=None, required=false
//	`q:"updated_at,format:Unix"`          → key="updated_at", format=Some("Unix"), value=None, required=false
//	`q:"updated_at,required"`             → key="updated_at", format=None, value=None, required=true
//	`q:"updated_at,format:Unix,required"` → key="updated_at", format=Some("Unix"), value=None, required=true
//	`q:"with,value:details"`              → key="with", format=None, value=Some("details"), required=false
func parseQTag(tag string) (key string, format, value Option[string], required bool) {
	key, options, hasOptions := strings.Cut(tag, ",")
	if hasOptions {
		for opt := range strings.SplitSeq(options, ",") {
			if after, found := strings.CutPrefix(opt, "format:"); found {
				// all known formats are currently for time
				if _, ok := nonUnixTimeFormats[after]; after != unixTimeFormat && !ok {
					panic(fmt.Sprintf("unsupported time format %q; accepted: %s", after, supportedHumanReadableFormats))
				}
				format = Some(after)
			} else if after, found := strings.CutPrefix(opt, "value:"); found {
				value = Some(after)
			} else if opt == "required" {
				required = true
			} else {
				panic("unrecognized option on tag " + tag)
			}
		}
	}
	return key, format, value, required
}

// typeNeedsTimeFormat reports whether t contains time.Time at any level
// of indirection (pointer, slice element, map value, or Option inner type).
func typeNeedsTimeFormat(t reflect.Type) bool {
	if t.Kind() == reflect.Map {
		return typeNeedsTimeFormat(t.Key()) || typeNeedsTimeFormat(t.Elem())
	}
	if slices.Contains([]reflect.Kind{reflect.Pointer, reflect.Slice, reflect.Array}, t.Kind()) {
		return typeNeedsTimeFormat(t.Elem())
	}
	if t == reflect.TypeFor[time.Time]() {
		return true
	}
	// Option[T]: detected by IsSome method, inner type via UnmarshalYAML contract
	if _, isOption := t.MethodByName("IsSome"); isOption {
		var innerType reflect.Type
		probe := func(dest any) error {
			// dest is **T → Elem() is *T → Elem() is T
			innerType = reflect.TypeOf(dest).Elem().Elem()
			return nil
		}
		type yamlUnmarshaler interface {
			UnmarshalYAML(func(any) error) error
		}
		err := reflect.New(t).Interface().(yamlUnmarshaler).UnmarshalYAML(probe)
		if err != nil {
			panic(fmt.Sprintf("failed to probe inner type of Option: %v", err))
		}
		return typeNeedsTimeFormat(innerType)
	}
	return false
}

func isScalarFieldType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Bool, reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
