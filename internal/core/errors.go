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

package core

import "fmt"

// ErrorSet replaces the "error" return value in functions that can return
// multiple errors. It provides convenience functions for easily adding errors
// to the set.
type ErrorSet []error

// Add adds the given error to the set if it is non-nil.
func (errs *ErrorSet) Add(err error) {
	if err != nil {
		*errs = append(*errs, err)
	}
}

// Addf is a shorthand for errs.Add(fmt.Errorf(...)).
func (errs *ErrorSet) Addf(msg string, args ...any) {
	*errs = append(*errs, fmt.Errorf(msg, args...))
}

// Append adds all errors from the `other` ErrorSet to this one.
func (errs *ErrorSet) Append(other ErrorSet) {
	*errs = append(*errs, other...)
}

// IsEmpty returns true if no errors are in the set.
func (errs ErrorSet) IsEmpty() bool {
	return len(errs) == 0
}
