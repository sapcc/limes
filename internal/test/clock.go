// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package test

import "time"

// NoJitter replaces test.AddJitter in unit tests, to provide deterministic
// behavior.
func NoJitter(d time.Duration) time.Duration {
	return d
}
