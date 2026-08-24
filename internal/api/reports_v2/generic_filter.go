// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"regexp"
	"slices"
	"strconv"
	"time"

	. "go.xyrillian.de/gg/option"
	"go.xyrillian.de/gg/options"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
	"github.com/sapcc/limes/internal/util"
)

// EvalCommitmentListOptsGenericFilters can validate common.CommitmentListOpts
// and replace generic filters with an arg position and return its value,
// or replace it with a no-op when the generic filters are not set.
// The expressions must be of the form "{{.* $[filter-field]}}".
func EvalCommitmentListOptsGenericFilters(opts common.CommitmentListOpts, originalQuery string, originalArgs ...any) (query string, args []any) {
	optSettings := map[string]Option[string]{
		"updated_after": options.Map(opts.UpdatedAfter, func(t time.Time) string { return t.Format(time.RFC3339Nano) }),
	}
	return handleGenericFilters(optSettings, originalQuery, originalArgs)
}

var genericFilterReplaceRx = regexp.MustCompile(`{{([^}]+?) \$(\S+?)}}`)

// handleGenericFilters is the generic, unexported function which takes an array of
// optStrings and does the replacement according to the given Option[string] value.
// It matches the lazily, so all replacements inside the curly braces should be performed before.
// Filter-fields which are not found in the optSettings are ignored silently.
func handleGenericFilters(optSettings map[string]Option[string], originalQuery string, originalArgs []any) (query string, args []any) {
	args = slices.Clone(originalArgs)
	query = genericFilterReplaceRx.ReplaceAllStringFunc(originalQuery, func(matchStr string) string {
		match := genericFilterReplaceRx.FindStringSubmatch(matchStr)

		optSetting, settingExists := optSettings[match[2]]
		if !settingExists {
			return matchStr
		}
		val, valueExists := optSetting.Unpack()
		if !valueExists {
			return util.SQLFilterNoop
		}
		args = append(args, val)
		return match[1] + " $" + strconv.Itoa(len(args))
	})
	return query, args
}
