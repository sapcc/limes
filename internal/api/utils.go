// SPDX-FileCopyrightText: 2023 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api

import (
	"time"

	"github.com/sapcc/go-api-declarations/limes"
)

func intoUnixEncodedTime(t time.Time) limes.UnixEncodedTime {
	return limes.UnixEncodedTime{Time: t}
}

func fromUnixEncodedTime(t limes.UnixEncodedTime) time.Time {
	return t.Time
}
