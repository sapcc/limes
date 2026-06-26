// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package opts

import (
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	. "go.xyrillian.de/gg/option"
)

// structInfo holds information about a struct type that can be used with ParseQueryString() or BuildQueryString().
type structInfo struct {
	Options  map[string]optionInfo
	FlagSets map[string]flagSetInfo
}

// optionInfo holds information about an option field that can appear in a query string.
// It appears in type [structInfo].
type optionInfo struct {
	Index      []int          // argument for reflect.Value.FieldByIndex()
	TimeFormat Option[string] // only for time.Time-valued fields or pointers/options thereof
	Required   bool
}

// flagSetInfo holds information about a key that can appear in a query string, and which is backed by several boolean flags.
// It appears in type [structInfo].
type flagSetInfo struct {
	Indexes map[string][]int // key = option value (e.g. "detail" in "?with=detail"), value = argument for reflect.Value.FieldByIndex()
}

var (
	structInfoCache      = make(map[reflect.Type]structInfo)
	structInfoCacheMutex sync.Mutex
)

func getStructInfo(t reflect.Type) structInfo {
	// not using an RWMutex here to simplify the implementation
	// (this critical section is extremely fast most of the time,
	// so the mutex is not likely to be contended)
	structInfoCacheMutex.Lock()
	defer structInfoCacheMutex.Unlock()
	si, exists := structInfoCache[t]
	if !exists {
		si = buildStructInfo(t)
		structInfoCache[t] = si
	}
	return si
}

func panicf(msg string, args ...any) {
	panic(fmt.Sprintf(msg, args...))
}

func buildStructInfo(t reflect.Type) structInfo {
	si := structInfo{
		Options:  make(map[string]optionInfo),
		FlagSets: make(map[string]flagSetInfo),
	}

	if t.Kind() != reflect.Struct {
		panic("options type is not a struct")
	}
	for _, field := range reflect.VisibleFields(t) {
		// ignore embedded fields themselves and only consider the fields within embedded structs
		qTag := field.Tag.Get("q")
		if field.Anonymous {
			if qTag != "" {
				panicf(`expected embedded struct %q to have no "q:"-tag`, field.Name)
			}
			continue
		}

		// this check must come after ignoring embedded fields, because unexported
		// anonymous fields are fine and can indeed be traversed into
		if !field.IsExported() {
			panicf(`field %q is unexported and therefore cannot be set`, field.Name)
		}

		// parse "q:"-tag
		if qTag == "" {
			panicf(`expected %q to have a "q:"-tag`, field.Name)
		}
		key, maybeTimeFormat, maybeValue, required := parseQTag(qTag)

		// case 1: field is a flag set
		if value, ok := maybeValue.Unpack(); ok {
			if field.Type.Kind() != reflect.Bool {
				panicf(`field %q has "value:" option but is not a bool`, field.Name)
			}
			if required {
				panicf(`field %q cannot have both "value:" and "required" options`, field.Name)
			}
			if maybeTimeFormat.IsSome() {
				panicf(`field %q cannot have both "value:" and "format:" options`, field.Name)
			}
			if _, exists := si.FlagSets[key]; !exists {
				si.FlagSets[key] = flagSetInfo{Indexes: make(map[string][]int)}
			}
			if _, exists := si.FlagSets[key].Indexes[value]; exists {
				panicf(`value %q for key %q is declared on multiple fields`, value, key)
			}
			si.FlagSets[key].Indexes[value] = field.Index
			continue
		}

		// case 2: field is an option
		if typeNeedsTimeFormat(field.Type) && maybeTimeFormat.IsNone() {
			panicf(`time format is missing for field %q`, field.Name)
		}
		if _, exists := si.Options[key]; exists {
			panicf(`key %q is declared on multiple fields`, key)
		}
		err := checkFieldTypeAllowed(field.Type)
		if err != nil {
			panic(err.Error())
		}
		si.Options[key] = optionInfo{
			Index:      field.Index,
			TimeFormat: maybeTimeFormat,
			Required:   required,
		}
	}

	for key := range si.FlagSets {
		if _, exists := si.Options[key]; exists {
			panic(fmt.Sprintf(`key %q cannot be declared as both a regular field and a value-discriminant field`, key))
		}
	}

	return si
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	anyOptionType = reflect.TypeFor[interface{ IsSome() bool }]()
)

// checkFieldTypeAllowed validates if the given type is allowed as a field within an option struct.
func checkFieldTypeAllowed(t reflect.Type) error {
	// a single level of pointer indirection is allowed
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	switch {
	case isScalarFieldType(t):
		return nil
	case t.Kind() == reflect.Struct:
		if t == timeType {
			return nil
		}
		if t.Implements(anyOptionType) {
			// Option.UnwrapOr() returns T, so that's an easy way to get to the contained type
			if m, ok := t.MethodByName("UnwrapOr"); ok {
				payloadType := m.Type.Out(0)
				if isScalarFieldType(payloadType) || payloadType == timeType {
					return nil
				} else {
					zero := reflect.New(payloadType).Elem().Interface()
					return fmt.Errorf("option.Option[T] with structured payload T = %T is not supported", zero)
				}
			}
		}
		return errors.New("structs other than time.Time and option.Option[T] are not supported")
	case t.Kind() == reflect.Slice:
		elementType := t.Elem()
		if elementType == reflect.TypeFor[time.Time]() || isScalarFieldType(elementType) {
			return nil
		}
		zero := reflect.New(elementType).Elem().Interface()
		return fmt.Errorf("slices of type %T are not supported", zero)
	case t.Kind() == reflect.Map:
		if !isScalarFieldType(t.Key()) {
			zero := reflect.New(t.Key()).Elem().Interface()
			return fmt.Errorf("map keys of type %T are not supported", zero)
		}
		if !isScalarFieldType(t.Elem()) {
			zero := reflect.New(t.Elem()).Elem().Interface()
			return fmt.Errorf("map values of type %T are not supported", zero)
		}
		return nil
	default:
		return fmt.Errorf("fields of kind %s are not supported", t.Kind())
	}
}
