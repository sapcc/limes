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

package db

import (
	"database/sql"
	"fmt"
	"strings"
)

//BuildSimpleWhereClause constructs a WHERE clause of the form "field1 = val1 AND
//field2 = val2 AND field3 IN (val3, val4)".
//
//If parameterOffset is not 0, start counting placeholders ("$1", "$2", etc.)
//after that offset.
func BuildSimpleWhereClause(fields map[string]interface{}, parameterOffset int) (queryFragment string, queryArgs []interface{}) {
	var (
		conditions []string
		args       []interface{}
	)
	for field, val := range fields {
		switch value := val.(type) {
		case []string:
			conditions = append(conditions, fmt.Sprintf("%s IN (%s)", field, makePlaceholderList(len(value), len(args)+1+parameterOffset)))
			for _, v := range value {
				args = append(args, v)
			}
		case []interface{}:
			conditions = append(conditions, fmt.Sprintf("%s IN (%s)", field, makePlaceholderList(len(value), len(args)+1+parameterOffset)))
			args = append(args, value...)
		default:
			conditions = append(conditions, fmt.Sprintf("%s = $%d", field, len(args)+1+parameterOffset))
			args = append(args, value)
		}
	}

	if len(conditions) == 0 {
		return "TRUE", nil
	}

	return strings.Join(conditions, " AND "), args
}

func makePlaceholderList(count, offset int) string {
	placeholders := make([]string, count)
	for idx := range placeholders {
		placeholders[idx] = fmt.Sprintf("$%d", offset+idx)
	}
	return strings.Join(placeholders, ",")
}

//ForeachRow calls dbi.Query() with the given query and args, then executes the
//given action one for every row in the result set. It then cleans up the
//result set, and it handles any errors that occur during all of this.
func ForeachRow(dbi Interface, query string, args []interface{}, action func(*sql.Rows) error) error {
	rows, err := dbi.Query(query, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		err = action(rows)
		if err != nil {
			rows.Close()
			return err
		}
	}
	err = rows.Err()
	if err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
