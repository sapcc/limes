// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package api_v2

import "errors"

var (
	errNoSuchProjectResource  = errors.New("no such project resource")
	errNoSuchResource         = errors.New("no such resource")
	errNoSuchService          = errors.New("no such service")
	errDoesNotTrackQuota      = errors.New("resource does not track quota")
	errDoesNotHaveCommitments = errors.New("resource does not have commitments")
	errResourceForbidden      = errors.New("resource is not enabled in this project")
)
