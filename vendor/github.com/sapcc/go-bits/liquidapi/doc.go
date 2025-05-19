// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package liquidapi provides a runtime library for servers and clients implementing the LIQUID protocol:
// <https://pkg.go.dev/github.com/sapcc/go-api-declarations/liquid>
//
//   - func Run() provides a full-featured runtime that handles OpenStack credentials, authorization, and more.
//   - type Client is a specialized gophercloud.ServiceClient for use in Limes and limesctl.
//   - The other functions in this package contain various numeric algorithms that are useful for LIQUID implementations.
package liquidapi
