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
	"database/sql"
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
	//Save inserts this record into the database, or updates it if it already exists.
	Save(db DBInterface) error
	//Delete deletes this record from the database.
	Delete(db DBInterface) error
}

//Collection references a set of records by way of a still-to-be-executed SQL query.
type Collection struct {
	table      *Table
	conditions []string
	args       []interface{}
}

//Where selects a collection of records from the given table. Additional
//conditions can be added before the collection is actually queried.
func (t *Table) Where(condition string, args ...interface{}) *Collection {
	return &Collection{
		table:      t,
		conditions: []string{condition},
		args:       args,
	}
}

//Where adds additional SQL conditions to this Collection.
func (c *Collection) Where(condition string, args ...interface{}) *Collection {
	return &Collection{
		table:      c.table,
		conditions: append(c.conditions, condition),
		args:       append(c.args, args...),
	}
}

//DBInterface is an interface that both sql.DB and sql.Transaction implement.
type DBInterface interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
	Query(query string, args ...interface{}) (*sql.Rows, error)
	QueryRow(query string, args ...interface{}) *sql.Row
}

func (c *Collection) doQuery(db DBInterface) (*sql.Rows, error) {
	var where string
	if len(c.conditions) == 1 {
		where = c.conditions[0]
	} else {
		//join multiple SQL conditions into one as "(cond1) AND (cond2) AND (cond3)"
		conds := make([]string, len(c.conditions))
		for idx, condition := range c.conditions {
			conds[idx] = "(" + condition + ")"
		}
		where = strings.Join(conds, " AND ")
	}

	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s",
		strings.Join(c.table.AllFields, ", "), c.table.Name, where,
	)
	return db.Query(query, c.args...)
}

//Foreach materializes the Collection into Record instances and calls the
//callback once for each record.
func (c *Collection) Foreach(db DBInterface, action func(record Record) error) error {
	rows, err := c.doQuery(db)
	if err != nil {
		return err
	}
	defer func() {
		if rows != nil {
			err := rows.Close()
			if err != nil {
				//log this error since I might be returning some other error already
				limes.Log(limes.LogError, "sql.Rows.Close failed: %s", err.Error())
			}
		}
	}()

	//we need to materialize the records first before using the callback -- The
	//DBInterface might be a transaction, and the callback might do more SQL
	//queries using the same transaction, but the libpq driver does not support
	//multiple concurrent queries in a single transaction.
	//<https://github.com/lib/pq/issues/81>
	var records []Record
	for rows.Next() {
		record := c.table.makeRecord()
		err := rows.Scan(record.ScanTargets()...)
		if err != nil {
			return err
		}
		records = append(records, record)
	}
	err = rows.Err()
	if err != nil {
		return err
	}
	err = rows.Close()
	rows = nil //do not try to Close() twice
	if err != nil {
		return err
	}

	//now we can safely use the callback
	for _, record := range records {
		err := action(record)
		if err != nil {
			return err
		}
	}

	return nil
}
