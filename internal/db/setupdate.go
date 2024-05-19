/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package db

import (
	"fmt"
	"reflect"
	"slices"

	gorp "github.com/go-gorp/gorp/v3"
)

// SetUpdate describes an operation where we have an existing set of records (type R),
// and a set of records that we want to have, as identified by some key (type K).
// Records that we want to keep are updated, missing records are created,
// and existing records that we do not want to have are deleted.
//
// TODO This is not yet used in most of the places that could benefit from it.
type SetUpdate[R any, K comparable] struct {
	// All relevant records that currently exist in the DB.
	ExistingRecords []R
	// All keys for which we want to have a record in the DB.
	WantedKeys []K

	// KeyForRecord reads the key out of an existing record.
	// This does not need to be the primary key.
	// Whatever unique identifier the caller has available is fine.
	KeyForRecord func(R) K

	// Callback for creating a new record for a missing key.
	//
	// After this, the Update callback will also be called on the new record.
	// This avoids code duplication between the create and update callbacks.
	Create func(K) (R, error)
	// Callback for updating an existing record.
	Update func(*R) error
}

// Execute executes this SetUpdate.
// Returns the set of records that exist in the DB after this update.
func (u SetUpdate[R, K]) Execute(tx *gorp.Transaction) ([]R, error) {
	// create or update wanted records
	var result []R
	for _, k := range u.WantedKeys {
		idx := slices.IndexFunc(u.ExistingRecords, func(r R) bool { return u.KeyForRecord(r) == k })
		if idx == -1 {
			// we do not have this record -> create it
			r, err := u.Create(k)
			if err == nil {
				err = u.Update(&r)
			}
			if err != nil {
				return nil, fmt.Errorf("could not build new %T record with key %v: %w", r, k, err)
			}

			err = tx.Insert(&r)
			if err != nil {
				return nil, fmt.Errorf("could not insert %T record with key %v: %w", r, k, err)
			}
			result = append(result, r)
		} else {
			// we have this record -> update it
			r := u.ExistingRecords[idx]
			err := u.Update(&r)
			if err != nil {
				return nil, fmt.Errorf("could not build updated %T record with key %v: %w", r, k, err)
			}

			// only update in the DB if necessary
			if !reflect.DeepEqual(r, u.ExistingRecords[idx]) {
				_, err = tx.Update(&r)
				if err != nil {
					return nil, fmt.Errorf("could not update %T record with key %v: %w", r, k, err)
				}
			}
			result = append(result, r)
		}
	}

	// delete unwanted records
	for _, r := range u.ExistingRecords {
		k := u.KeyForRecord(r)
		if !slices.Contains(u.WantedKeys, k) {
			_, err := tx.Delete(&r)
			if err != nil {
				return nil, fmt.Errorf("could not delete %T record with key %v: %w", r, k, err)
			}
		}
	}

	return result, nil
}
