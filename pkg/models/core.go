/*******************************************************************************
*
* Copyright 2017 SAP SE
*
* Licensed under the Apache License, Version 2.0 (the "License");
* you may not use this file except in compliance with the License.
* You should have received a copy of the License along with this
* program. If not, you may obtain a copy of the License at
*
*     http://www.apache.org/licenses/LICENSE-2.0
*
* Unless required by applicable law or agreed to in writing, software
* distributed under the License is distributed on an "AS IS" BASIS,
* WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
* See the License for the specific language governing permissions and
* limitations under the License.
*
*******************************************************************************/

package models

import (
	"fmt"
	"strings"

	"github.com/sapcc/limes/pkg/limes"
)

//Table offers algorithms on Limes' database schema's tables.
type Table struct {
	Name       string
	AllFields  []string
	makeRecord func() Record
}

//Record contains generic functions on a table's records.
type Record interface {
	//Table returns the table containing this record.
	Table() *Table
	//ScanTargets returns a list of pointers to the fields of this record, for
	//use with db.Rows.Scan(), db.QueryRow() and so on. The order of fields is
	//like in Table().AllFields.
	ScanTargets() []interface{}
	//Delete deletes this record from the database.
	Delete() error
}

//WalkWhere finds rows matching the given WHERE clause and executes a callback for
//each of them.
func (t *Table) WalkWhere(whereClause string, args []interface{}, action func(record Record) error) error {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s",
		strings.Join(t.AllFields, ", "), t.Name, whereClause,
	)
	rows, err := limes.DB.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		record := t.makeRecord()
		err := rows.Scan(record.ScanTargets()...)
		if err != nil {
			return err
		}

		err = action(record)
		if err != nil {
			return err
		}
	}

	return rows.Err()
}
