/*******************************************************************************
*
* Copyright 2020 SAP SE
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
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// Window is the size (in nanoseconds) of the time window that is considered
// when enforcing a rate limit. For example, a rate limit of "10 per second" has
// a limit of 10 and a window of 1 second.
//
// This type is very similar to time.Duration, but does not allow negative
// values and uses a different parsing logic that does not allow phrases with
// multiple units like time.Duration does (e.g. "2h30m").
type Window uint64

const (
	//WindowMilliseconds is a Window unit.
	WindowMilliseconds Window = 1000 * 1000
	//WindowSeconds is a Window unit.
	WindowSeconds Window = 1000 * WindowMilliseconds
	//WindowMinutes is a Window unit.
	WindowMinutes Window = 60 * WindowSeconds
	//WindowHours is a Window unit.
	WindowHours Window = 60 * WindowMinutes
)

var windowUnits = map[string]Window{
	"ms": WindowMilliseconds,
	"s":  WindowSeconds,
	"m":  WindowMinutes,
	"h":  WindowHours,
}

var windowFormatRx = regexp.MustCompile(`^\s*([0-9]+)\s*([A-Za-z]+)$`)

// MustParseWindow is like ParseWindow, but panics on error. This should only be used for compile-time constants.
func MustParseWindow(input string) Window {
	w, err := ParseWindow(input)
	if err != nil {
		panic(err.Error())
	}
	return w
}

// ParseWindow parses a string representation like "1s" or "5m".
func ParseWindow(input string) (Window, error) {
	if input == "" {
		return Window(0), nil
	}

	match := windowFormatRx.FindStringSubmatch(input)
	if match == nil {
		return 0, fmt.Errorf("invalid value %q: does not match expected format \"<number> <unit>\"", input)
	}
	number, err := strconv.ParseUint(match[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q: %s", input, err.Error())
	}
	multiplier, isValidUnit := windowUnits[match[2]]
	if !isValidUnit {
		return 0, fmt.Errorf("invalid value %q: unknown time unit %q", input, match[2])
	}
	return Window(number) * multiplier, nil
}

// String returns the optimal string representation for this window.
func (w Window) String() string {
	if w == 0 {
		return ""
	}

	//find the unit that yields the shortest exact representation
	shortest := ""
	for unit, multiplier := range windowUnits {
		if w%multiplier == 0 {
			repr := fmt.Sprintf("%d%s", uint64(w/multiplier), unit)
			if shortest == "" || len(shortest) > len(repr) {
				shortest = repr
			}
		}
	}
	return shortest
}

// MarshalJSON implements the json.Marshaler interface.
func (w Window) MarshalJSON() ([]byte, error) {
	repr := w.String()
	if repr == "" {
		return nil, fmt.Errorf("unrepresentable window size: %d ns", uint64(w))
	}
	return []byte(fmt.Sprintf("%q", repr)), nil
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (w *Window) UnmarshalJSON(buf []byte) error {
	var s string
	err := json.Unmarshal(buf, &s)
	if err != nil {
		return err
	}
	win, err := ParseWindow(s)
	if err == nil {
		*w = win
	}
	return err
}

// UnmarshalYAML implements the yaml.Unmarshaler interface. This method validates
// that windows in the config file are valid.
func (w *Window) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	err := unmarshal(&s)
	if err != nil {
		return err
	}
	win, err := ParseWindow(s)
	if err == nil {
		*w = win
	}
	return err
}
