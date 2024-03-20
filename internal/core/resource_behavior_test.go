/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package core

import "testing"

func TestOvercommitFactor(t *testing.T) {
	check := func(f OvercommitFactor, raw, effective uint64) {
		actualEffective := f.ApplyTo(raw)
		if actualEffective != effective {
			t.Errorf("expected (%g).ApplyTo(%d) = %d, but got %d", f, raw, effective, actualEffective)
		}
		actualRaw := f.ApplyInReverseTo(effective)
		if actualRaw != raw {
			t.Errorf("expected (%g).ApplyInReverseTo(%d) = %d, but got %d", f, effective, raw, actualRaw)
		}
	}

	check(0, 42, 42)
	check(1, 42, 42)
	check(1.2, 42, 50)

	// ApplyTo is pretty straightforward, but I'd like some more test coverage for ApplyInReverseTo
	for _, factor := range []OvercommitFactor{0, 1, 1.1, 1.2, 1.5, 2, 2.5, 3, 4} {
		for raw := uint64(0); raw <= 100; raw++ {
			check(factor, raw, factor.ApplyTo(raw))
		}
	}
}
