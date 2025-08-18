// SPDX-FileCopyrightText: 2017 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sapcc/go-api-declarations/liquid"
)

// BuildSimpleWhereClause constructs a WHERE clause of the form "field1 = val1 AND
// field2 = val2 AND field3 IN (val3, val4)".
//
// If parameterOffset is not 0, start counting placeholders ("$1", "$2", etc.)
// after that offset.
func BuildSimpleWhereClause(fields map[string]any, parameterOffset int) (queryFragment string, queryArgs []any) {
	var (
		conditions []string
		args       []any
	)
	for field, val := range fields {
		switch value := val.(type) {
		case []string:
			if len(value) == 0 {
				// no admissible values for this field, so the entire condition must fail
				return "FALSE", nil
			}
			conditions = append(conditions, fmt.Sprintf("%s IN (%s)", field, makePlaceholderList(len(value), len(args)+1+parameterOffset)))
			for _, v := range value {
				args = append(args, v)
			}
		case []any:
			if len(value) == 0 {
				// no admissible values for this field, so the entire condition must fail
				return "FALSE", nil
			}
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

var (
	enumPlaceholderRx = regexp.MustCompile(`{{[a-zA-Z][a-zA-Z0-9\.]*}}`)
	enumPlaceholders  = map[string]string{
		"{{liquid.CommitmentStatusPlanned}}":    enumValueToSQLLiteral(liquid.CommitmentStatusPlanned),
		"{{liquid.CommitmentStatusPending}}":    enumValueToSQLLiteral(liquid.CommitmentStatusPending),
		"{{liquid.CommitmentStatusGuaranteed}}": enumValueToSQLLiteral(liquid.CommitmentStatusGuaranteed),
		"{{liquid.CommitmentStatusConfirmed}}":  enumValueToSQLLiteral(liquid.CommitmentStatusConfirmed),
		"{{liquid.CommitmentStatusSuperseded}}": enumValueToSQLLiteral(liquid.CommitmentStatusSuperseded),
		"{{liquid.CommitmentStatusExpired}}":    enumValueToSQLLiteral(liquid.CommitmentStatusExpired),
	}
)

// ExpandEnumPlaceholders takes an SQL query literal from the source code and
// replaces placeholders {{like.This}} with SQL string literals 'like-this'.
// The placeholder must refer to an enum variant one of the following types:
//   - liquid.CommitmentStatus
//
// Canonical usage looks like this:
//
//	var query = sqlext.SimplifyWhitespace(db.ExpandEnumPlaceholders(`
//		...
//	`))
func ExpandEnumPlaceholders(query string) string {
	return enumPlaceholderRx.ReplaceAllStringFunc(query, func(match string) string {
		expansion, exists := enumPlaceholders[match]
		if exists {
			return expansion
		} else {
			return match // do not replace unknown placeholders
		}
	})
}

func enumValueToSQLLiteral[S ~string](value S) string {
	return fmt.Sprintf("'%s'", strings.ReplaceAll(string(value), "'", "''"))
}
