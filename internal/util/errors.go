// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"errors"

	"github.com/gophercloud/gophercloud/v2"
)

// UnpackError is usually a no-op, but for some Gophercloud errors, it removes
// the outer layer that obscures the better error message hidden within.
func UnpackError(err error) error {
	var innerErr gophercloud.ErrUnexpectedResponseCode
	if errors.As(err, &innerErr) {
		return innerErr
	}
	return err
}
