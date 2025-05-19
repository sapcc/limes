// SPDX-FileCopyrightText: 2022 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package promquery

import (
	"github.com/sapcc/go-bits/errext"
)

// NoRowsError is returned by PrometheusClient.GetSingleValue()
// if there were no result values at all.
type NoRowsError struct {
	Query string
}

// Error implements the builtin/error interface.
func (e NoRowsError) Error() string {
	return "Prometheus query returned empty result: " + e.Query
}

// IsErrNoRows checks whether the given error is a NoRowsError.
func IsErrNoRows(err error) bool {
	return errext.IsOfType[NoRowsError](err)
}
