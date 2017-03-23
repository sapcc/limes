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

package test

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gorp "gopkg.in/gorp.v2"

	"github.com/mattes/migrate/migrate"
	"github.com/sapcc/limes/pkg/db"

	//provides sqlite3 migration driver
	_ "github.com/mattes/migrate/driver/sqlite3"
	sqlite "github.com/mattn/go-sqlite3"
)

func init() {
	//provide SQL functions that the sqlite3 driver needs to consume Postgres queries successfully
	toTimestamp := func(i int64) int64 {
		return i
	}
	sql.Register("sqlite3-limes", &sqlite.SQLiteDriver{
		ConnectHook: func(conn *sqlite.SQLiteConn) error {
			//need to enable foreign-key support (so that stuff like "ON DELETE CASCADE" works)
			_, err := conn.Exec("PRAGMA foreign_keys = ON", []driver.Value{})
			if err != nil {
				return err
			}
			return conn.RegisterFunc("to_timestamp", toTimestamp, true)
		},
	})
}

//InitDatabase initializes DB in pkg/db with an empty in-memory SQLite
//database.
func InitDatabase(t *testing.T, migrationsPath string) {
	//wipe DB (might be left over from previous test runs)
	dbPath := "unittest.db"
	err := os.Remove(dbPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	//apply DB schema
	errs, ok := migrate.UpSync("sqlite3://"+dbPath, migrationsPath)
	if !ok {
		for _, err := range errs {
			t.Error(err)
		}
		t.FailNow()
	}

	//initialize DB connection
	sqliteDB, err := sql.Open("sqlite3-debug", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.DB = &gorp.DbMap{Db: sqliteDB, Dialect: gorp.SqliteDialect{}}
	db.InitGorp()
}

//ExecSQLFile loads a file containing SQL statements and executes them all.
//It implies that every SQL statement is on a single line.
func ExecSQLFile(t *testing.T, path string) {
	sqlBytes, err := ioutil.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	//split into single statements because db.DB.Exec() will just ignore everything after the first semicolon
	for _, line := range strings.Split(string(sqlBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		_, err = db.DB.Exec(line)
		if err != nil {
			t.Fatal(err)
		}
	}
}

//AssertDBContent makes a dump of the database contents (as a sequence of
//INSERT statements) and runs diff(1) against the given file, producing a test
//error if these two are different from each other.
func AssertDBContent(t *testing.T, fixtureFile string) {
	actualContent := getDBContent(t)

	fixturePath, _ := filepath.Abs(fixtureFile)
	cmd := exec.Command("diff", "-u", fixturePath, "-")
	cmd.Stdin = bytes.NewReader([]byte(actualContent))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	failOnErr(t, cmd.Run())
}

func getDBContent(t *testing.T) string {
	//list all tables
	var tableNames []string
	rows, err := db.DB.Query(`
		SELECT name FROM sqlite_master WHERE type='table'
		AND name != 'schema_migration' AND name NOT LIKE '%sqlite%'
	`)
	failOnErr(t, err)
	for rows.Next() {
		var name string
		failOnErr(t, rows.Scan(&name))
		tableNames = append(tableNames, name)
	}
	failOnErr(t, rows.Err())
	failOnErr(t, rows.Close())

	//foreach table, dump each entry as an INSERT statement
	var result string
	for _, tableName := range tableNames {
		rows, err := db.DB.Query(`SELECT * FROM ` + tableName)
		failOnErr(t, err)
		columnNames, err := rows.Columns()
		failOnErr(t, err)

		scanTarget := make([]interface{}, len(columnNames))
		for idx := range scanTarget {
			scanTarget[idx] = &sqlValueSerializer{}
		}
		formatStr := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);\n",
			tableName,
			strings.Join(columnNames, ", "),
			strings.Join(times(len(columnNames), "%#v"), ", "),
		)

		hadRows := false
		for rows.Next() {
			failOnErr(t, rows.Scan(scanTarget...))
			result += fmt.Sprintf(formatStr, scanTarget...)
			hadRows = true
		}

		failOnErr(t, rows.Err())
		failOnErr(t, rows.Close())
		if hadRows {
			result += "\n"
		}
	}

	return strings.TrimSuffix(result, "\n")
}

func failOnErr(t *testing.T, err error) {
	if err != nil {
		t.Fatal(err)
	}
}

func times(n int, s string) []string {
	result := make([]string, n)
	for idx := range result {
		result[idx] = s
	}
	return result
}

type sqlValueSerializer struct {
	Serialized string
}

func (s *sqlValueSerializer) Scan(src interface{}) error {
	switch val := src.(type) {
	case int64:
		s.Serialized = fmt.Sprintf("%#v", val)
	case float64:
		s.Serialized = fmt.Sprintf("%#v", val)
	case bool:
		s.Serialized = "FALSE"
		if val {
			s.Serialized = "TRUE"
		}
	case []byte:
		s.Serialized = fmt.Sprintf("'%s'", string(val))
		//SQLite apparently stores boolean values as C strings
		switch s.Serialized {
		case "'FALSE'":
			s.Serialized = "FALSE"
		case "'TRUE'":
			s.Serialized = "TRUE"
		}
	case string:
		s.Serialized = fmt.Sprintf("'%s'", val)
	case time.Time:
		s.Serialized = fmt.Sprintf("%#v", val.Unix())
	default:
		s.Serialized = "NULL"
	}
	return nil
}

func (s *sqlValueSerializer) GoString() string {
	return s.Serialized
}
