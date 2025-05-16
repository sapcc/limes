// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

// IDsToJSON embeds a list of string IDs in a data structure that serializes to
// JSON like [{"id":"first"},{"id":"second"}].
func IDsToJSON(ids []string) any {
	type id struct {
		ID string `json:"id"`
	}
	result := make([]id, len(ids))
	for idx, str := range ids {
		result[idx].ID = str
	}
	return result
}
