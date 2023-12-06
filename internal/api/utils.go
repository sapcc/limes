/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package api

import (
	"time"

	"github.com/sapcc/go-api-declarations/limes"
)

func maybeUnixEncodedTime(t *time.Time) *limes.UnixEncodedTime {
	if t == nil {
		return nil
	}
	return &limes.UnixEncodedTime{Time: *t}
}

func maybeUnpackUnixEncodedTime(t *limes.UnixEncodedTime) *time.Time {
	if t == nil {
		return nil
	}
	return &t.Time
}

func unwrapOrDefault[T any](value *T, defaultValue T) T {
	if value == nil {
		return defaultValue
	}
	return *value
}
