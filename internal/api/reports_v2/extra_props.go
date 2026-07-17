// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package reports_v2

import (
	"strings"

	"github.com/sapcc/limes/internal/apideclarations/apiv2/common"
)

// EvalClusterResourceExtraProps can validate common.ClusterResourceReportOpts and
// remove unchecked options tagged in the following style, named
// similarly to the opts query string param from the sql string:
// $with_[opt]{{[optional sql]}}
// If the option is checked (true), the [optional sql] is retained.
// If an unknown [opt] is found, the function panics.
func EvalClusterResourceExtraProps(sql string, opts common.ClusterResourceReportOpts) string {
	optSettings := map[string]bool{
		"commitment_stats": opts.WithCommitmentStats,
		"timing":           opts.WithTiming,
		"subcapacities":    opts.WithSubcapacities,
	}
	return handleProps(sql, optSettings)
}

// EvalDomainResourceExtraProps works like EvalClusterResourceExtraProps but with
// common.DomainResourceReportOpts.
func EvalDomainResourceExtraProps(sql string, opts common.DomainResourceReportOpts) string {
	optSettings := map[string]bool{
		"commitment_stats": opts.WithCommitmentStats,
	}
	return handleProps(sql, optSettings)
}

// EvalProjectResourceExtraProps works like EvalClusterResourceExtraProps but with
// common.ProjectResourceReportOpts.
func EvalProjectResourceExtraProps(sql string, opts common.ProjectResourceReportOpts) string {
	optSettings := map[string]bool{
		"constraints":      opts.WithUserSpecifiedConstraints,
		"commitment_stats": opts.WithCommitmentStats,
		"timing":           opts.WithTiming,
		"subresources":     opts.WithSubresources,
		"historical_usage": opts.WithHistoricalUsage,
	}
	return handleProps(sql, optSettings)
}

// handleProps is the generic, unexported function which takes an array of
// optStrings and does the replacement according to the chosen options.
// It panics when $with_[opt]{{...}} is found in the sql string, but not in the optSettings array.
// It counts nested {{ and }} to find the correct matching closing delimiter.
func handleProps(sql string, optSettings map[string]bool) string {
	for {
		idx := strings.Index(sql, "$with_")
		if idx == -1 {
			return sql
		}
		nameEnd := strings.Index(sql[idx:], "{{")
		optName := sql[idx+len("$with_") : idx+nameEnd]
		contentStart := idx + nameEnd + 2

		// find matching }} by counting nested braces
		depth, pos := 1, contentStart
		for pos < len(sql)-1 && depth > 0 {
			switch {
			case sql[pos] == '{' && sql[pos+1] == '{':
				depth++
				pos += 2
			case sql[pos] == '}' && sql[pos+1] == '}':
				depth--
				pos += 2
			default:
				pos++
			}
		}

		checked, ok := optSettings[optName]
		if !ok {
			panic("unknown $with_ option: " + optName)
		}
		if checked {
			sql = sql[:idx] + sql[contentStart:pos-2] + sql[pos:]
		} else {
			sql = sql[:idx] + sql[pos:]
		}
	}
}
