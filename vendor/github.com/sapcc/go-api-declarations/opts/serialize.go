// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package opts

import (
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	. "go.xyrillian.de/gg/option"
)

// BuildQueryString is a function to be used by request methods with opts structs.
// It's inspired by [gophercloud.BuildQueryString] with partially stricter behavior.
// It accepts a tagged structure and expands it into a URL struct. Field names are
// converted into query parameters based on a "q" tag. For example:
//
//	type Something struct {
//	   Bar string `q:"x_bar"`
//	   Baz int    `q:"lorem_ipsum"`
//	}
//	instance := Something{
//	   Bar: "AAA",
//	   Baz: 1,
//	}
//
// will be converted into
//
//	?x_bar=AAA&lorem_ipsum=1
//
// On configuration errors (e.g. non-struct opts, opts with non-q-tagged fields)
// the function panics. On user errors (e.g. missing required field) an error
// is returned. On success, url.Values are returned according to the opts.
//
// This function understands and expects the same values for the "q" tag as [opts.ParseQueryString].
// See documentation over there for details.
//
// [gophercloud.BuildQueryString]: https://pkg.go.dev/github.com/gophercloud/gophercloud/v2#BuildQueryString
func BuildQueryString(opts any) (url.Values, error) {
	optsValue := reflect.ValueOf(opts)
	for optsValue.Kind() == reflect.Pointer {
		if optsValue.IsNil() {
			panic("opts is a nil pointer")
		}
		optsValue = optsValue.Elem()
	}
	si := getStructInfo(optsValue.Type())
	params := url.Values{}

	// serialize flagsets
	for key, fs := range si.FlagSets {
		for value, index := range fs.Indexes {
			if optsValue.FieldByIndex(index).Bool() {
				params.Add(key, value)
			}
		}
		slices.Sort(params[key])
	}

	// serialize options
	for key, opt := range si.Options {
		value := optsValue.FieldByIndex(opt.Index)
		if canBeSkipped(value, opt.Required) {
			continue
		}
		params[key] = serializeValue(value, opt.TimeFormat)
		if opt.Required && isOnlyEmptyStrings(params[key]) {
			// if the field is required, it cannot have no value (handles nil maps, slices, arrays)
			return url.Values{}, fmt.Errorf("required query parameter %q not set", key)
		}
	}
	return params, nil
}

// serializeValue converts a reflect.Value to its string representation for query parameters.
// Zero values are serialized, also - so they need to be taken care of separately, if that is not intentional.
func serializeValue(value reflect.Value, maybeTimeFormat Option[string]) []string {
	// dereference pointers
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}

	// only handle non-single-values here, rest is done by serializeSingleValue()
	switch value.Kind() {
	case reflect.Slice:
		values := make([]string, value.Len())
		for i := range value.Len() {
			values[i] = serializeSingleValue(value.Index(i), maybeTimeFormat)
		}
		return values
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			return []string{serializeSingleValue(value, maybeTimeFormat)}
		} else if m := value.MethodByName("AsPointer"); m.IsValid() {
			// Option[T] — unwrap via AsPointer
			results := m.Call(nil)
			if len(results) == 1 && results[0].Kind() == reflect.Pointer && !results[0].IsNil() {
				return []string{serializeSingleValue(results[0].Elem(), maybeTimeFormat)}
			} else {
				return nil
			}
		} else {
			// defense in depth: should have been rejected in checkFieldTypeAllowed()
			panic("structs other than time.Time and option.Option[T] are not supported")
		}
	case reflect.Map:
		keys := value.MapKeys()
		slices.SortFunc(keys, func(a, b reflect.Value) int {
			return strings.Compare(serializeSingleValue(a, maybeTimeFormat), serializeSingleValue(b, maybeTimeFormat))
		})
		result := make([]string, 0, value.Len())
		for _, k := range keys {
			result = append(result, serializeSingleValue(k, maybeTimeFormat)+":"+serializeSingleValue(value.MapIndex(k), maybeTimeFormat))
		}
		return result
	default:
		return []string{serializeSingleValue(value, maybeTimeFormat)}
	}
}

// serializeSingleValue converts a reflect.Value of a primitive type (e.g. int/string, but not slice/map) to its string representation for query parameters.
// Zero values are serialized, also - so they need to be taken care of separately, if that is not intentional.
func serializeSingleValue(v reflect.Value, timeFormat Option[string]) string {
	// Dereference pointers.
	for v.Kind() == reflect.Pointer {
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())
	case reflect.Struct:
		// handle time
		if v.Type() == reflect.TypeFor[time.Time]() {
			t := v.Interface().(time.Time)
			tf := timeFormat.UnwrapOrPanic("timeFormat should have been set")
			switch tf {
			case unixTimeFormat:
				return strconv.FormatInt(t.Unix(), 10)
			default:
				layout := nonUnixTimeFormats[tf]
				return t.Format(layout)
			}
		}
		// handle Option[T]: try to unwrap via AsPointer() method.
		if m := v.MethodByName("AsPointer"); m.IsValid() {
			results := m.Call(nil)
			if len(results) == 1 && results[0].Kind() == reflect.Pointer && !results[0].IsNil() {
				return serializeSingleValue(results[0].Elem(), timeFormat)
			}
		}
	}
	return fmt.Sprintf("%v", v.Interface())
}

// canBeSkipped checks if a value can be skipped for serialization into a query string.
// Required params are never skipped. Otherwise, isZero() of the value is checked.
// Special handling is applied to
// - structs (only time.Time and Option[T] are supported; others panic)
// - pointers (nil means skippable)
func canBeSkipped(v reflect.Value, required bool) bool {
	if required {
		return false
	}

	// check pointers
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return true
		}
		// Dereference the pointer and check the element.
		return canBeSkipped(v.Elem(), false)
	}

	// structs: only time.Time and Option[T] are supported
	if v.Kind() == reflect.Struct {
		if v.Type() == reflect.TypeFor[time.Time]() {
			return v.Interface().(time.Time).IsZero()
		}
		if _, isOption := v.Type().MethodByName("AsPointer"); isOption {
			type isZeroer interface{ IsZero() bool }
			return v.Interface().(isZeroer).IsZero()
		}
		// defense in depth: should have been rejected in checkFieldTypeAllowed()
		panic("structs other than time.Time and option.Option[T] are not supported")
	}

	return v.IsZero()
}
