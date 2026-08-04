// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import "testing"

// ReduceLogSpam can be set to true to disable some frequent INFO-level logs that tend to spam test output in an unhelpful manner.
// Set this to false if you need to actually see those logs within tests.
var ReduceLogSpam = testing.Testing()
