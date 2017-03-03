/*******************************************************************************
*
* Copyright 2017 Stefan Majewsky <majewsky@gmx.net>
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

package sqlproxy

import (
	"database/sql"
	"fmt"
	"os"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

var queries []string

func init() {
	sql.Register("sqlite3+nothing", &Driver{
		ProxiedDriverName: "sqlite3",
	})
	sql.Register("sqlite3+beforequery", &Driver{
		ProxiedDriverName: "sqlite3",
		BeforeQueryHook: func(query string, args []interface{}) {
			queries = append(queries, fmt.Sprintf("(%s) %#v", query, args))
		},
	})
}

var dsn = "file:test.db"

type TT struct {
	t *testing.T
}

func (tt TT) Must(err error) {
	if err != nil {
		tt.t.Fatal(err)
	}
}

func (tt TT) MustDB(db *sql.DB, err error) *sql.DB {
	tt.Must(err)
	return db
}

func (tt TT) MustResult(result sql.Result, err error) sql.Result {
	tt.Must(err)
	return result
}

func (tt TT) MustRows(rows *sql.Rows, err error) *sql.Rows {
	tt.Must(err)
	return rows
}

func (tt TT) CleanupDB() {
	err := os.Remove("test.db")
	if !os.IsNotExist(err) {
		tt.Must(err)
	}
}

func (tt TT) PrepareDB() {
	tt.CleanupDB()

	db := tt.MustDB(sql.Open("sqlite3", dsn))
	tt.MustResult(db.Exec(`CREATE TABLE knowledge (number INTEGER, thing TEXT)`))
	tt.MustResult(db.Exec(`INSERT INTO knowledge VALUES (23, 'conspiracy')`))
	tt.MustResult(db.Exec(`INSERT INTO knowledge VALUES (42, 'truth')`))
	tt.Must(db.Close())
}

func (tt TT) Unexpected(name string, expected, actual interface{}) {
	tt.t.Errorf("expected %s = %#v, got %s = %#v", name, expected, name, actual)
}

func (tt TT) ExpectRow(rows *sql.Rows, expectedNumber int, expectedThing string) {
	if !rows.Next() {
		tt.t.Fatalf("unexpected end of result set")
	}
	var (
		number int
		thing  string
	)
	tt.Must(rows.Scan(&number, &thing))
	if number != expectedNumber {
		tt.Unexpected("number", expectedNumber, number)
	}
	if thing != expectedThing {
		tt.Unexpected("thing", expectedThing, thing)
	}
}

////////////////////////////////////////////////////////////////////////////////

//Test_Basic verifies that SQL statements pass through the proxy.
func Test_Basic(t *testing.T) {
	tt := TT{t}
	tt.PrepareDB()
	db := tt.MustDB(sql.Open("sqlite3+nothing", dsn))

	tt.MustResult(db.Exec(`INSERT INTO knowledge VALUES (5, 'chaos')`)).RowsAffected()

	affected, err := tt.MustResult(db.Exec(`UPDATE knowledge SET thing = ? WHERE number = ?`, "douglas", "42")).RowsAffected()
	tt.Must(err)
	if affected != 1 {
		tt.Unexpected("affected", 1, affected)
	}

	tt.MustResult(db.Exec(`DELETE FROM knowledge WHERE thing = 'conspiracy'`))

	rows := tt.MustRows(db.Query(`SELECT * FROM knowledge ORDER BY number`))
	tt.ExpectRow(rows, 5, "chaos")
	tt.ExpectRow(rows, 42, "douglas")
	if rows.Next() {
		t.Fatalf("unexpected continuation of result set")
	}
	tt.Must(rows.Close())

	tt.Must(db.Close())
	tt.CleanupDB()
}

//Test_BeforeQueryHook tests that the BeforeQueryHook is being called.
func Test_BeforeQueryHook(t *testing.T) {
	tt := TT{t}
	queries = nil

	db := tt.MustDB(sql.Open("sqlite3+beforequery", ":memory:"))
	var x int

	tt.Must(db.QueryRow(`SELECT 42`).Scan(&x))
	if x != 42 {
		tt.Unexpected("x", 42, x)
	}

	tt.Must(db.QueryRow(`SELECT ?`, int16(23)).Scan(&x))
	if x != 23 {
		tt.Unexpected("x", 23, x)
	}

	var y string
	var z string
	tt.Must(db.QueryRow(`SELECT ?, ?`, "black", "magic").Scan(&y, &z))
	if y != "black" {
		tt.Unexpected("y", "black", y)
	}
	if z != "magic" {
		tt.Unexpected("z", "magic", z)
	}

	expectedQueries := []string{
		`(SELECT 42) []interface {}{}`,
		`(SELECT ?) []interface {}{23}`,
		`(SELECT ?, ?) []interface {}{"black", "magic"}`,
	}
	if !reflect.DeepEqual(queries, expectedQueries) {
		t.Errorf("not seeing the queries that I expected")
		for idx, query := range expectedQueries {
			t.Logf("expected %d = %s", idx, query)
		}
		for idx, query := range queries {
			t.Logf("actual %d = %s", idx, query)
		}
	}
}
