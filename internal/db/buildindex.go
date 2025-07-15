// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

// BuildIndexOfDBResult executes an SQL query and returns a map (index) of the result.
// The key should be unique among the whole result set.
func BuildIndexOfDBResult[R any, K comparable](dbi Interface, keyFunc func(R) K, query string, args ...any) (result map[K]R, err error) {
	var resultArray []R
	_, err = dbi.Select(&resultArray, query, args...)
	if err != nil {
		return nil, err
	}
	result = make(map[K]R, len(resultArray))
	for _, item := range resultArray {
		result[keyFunc(item)] = item
	}
	return result, nil
}

// buildArrayIndexOfDBResult executes an SQL query and returns a map (index) of the result.
// The key should not be unique among the whole result set
func BuildArrayIndexOfDBResult[R any, K comparable](dbi Interface, keyFunc func(R) K, query string, args ...any) (result map[K][]R, err error) {
	var resultArray []R
	_, err = dbi.Select(&resultArray, query, args...)
	if err != nil {
		return nil, err
	}
	result = make(map[K][]R, len(resultArray))
	for _, item := range resultArray {
		key := keyFunc(item)
		result[key] = append(result[key], item)
	}
	return result, nil
}
