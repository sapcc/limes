// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"slices"
	"time"

	. "github.com/majewsky/gg/option"
	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
)

// CommitmentBehavior describes how commitments work for a single resource.
//
// It appears in type ServiceConfiguration.
type CommitmentBehavior struct {
	// This ConfigSet is keyed on domain name, because commitment durations
	// (and thus committability) are allowed to differ per domain.
	//
	// If DurationsPerDomain.Pick() returns an empty slice, then commitments are entirely forbidden for that resource in the given domain.
	DurationsPerDomain regexpext.ConfigSet[string, []limesresources.CommitmentDuration] `yaml:"durations_per_domain"`

	MinConfirmDate Option[time.Time]                `yaml:"min_confirm_date"`
	UntilPercent   Option[float64]                  `yaml:"until_percent"`
	ConversionRule Option[CommitmentConversionRule] `yaml:"conversion_rule"`
}

// Validate returns a list of all errors in this behavior configuration.
//
// The `path` argument denotes the location of this behavior in the
// configuration file, and will be used when generating error messages.
func (b CommitmentBehavior) Validate(path string, occupiedConversionIdentifiers []string) (errs errext.ErrorSet, identifier string) {
	if percent, ok := b.UntilPercent.Unpack(); ok {
		if percent < 0 {
			errs.Addf("invalid value: %s.until_percent may not be smaller than 0", path)
		}
		if percent > 100 {
			errs.Addf("invalid value: %s.until_percent may not be bigger than 100", path)
		}
	}
	if conversionRule, ok := b.ConversionRule.Unpack(); ok {
		identifier = conversionRule.Identifier
		if slices.Contains(occupiedConversionIdentifiers, conversionRule.Identifier) {
			errs.Addf("invalid value: %s.conversion_rule.identifier values must be restricted to a single serviceType, but %q is already used by another serviceType", path, conversionRule.Identifier)
		}
	}

	return errs, identifier
}

// ScopedCommitmentBehavior is a CommitmentBehavior that applies only to a certain scope (usually a specific domain).
// It is created through the For... methods on type CommitmentBehavior.
type ScopedCommitmentBehavior struct {
	Durations      []limesresources.CommitmentDuration
	MinConfirmDate Option[time.Time]
	UntilPercent   Option[float64]
	ConversionRule Option[CommitmentConversionRule]
}

// ForDomain resolves Durations.Pick() using the provided domain name.
func (b CommitmentBehavior) ForDomain(domainName string) ScopedCommitmentBehavior {
	return ScopedCommitmentBehavior{
		Durations:      b.DurationsPerDomain.Pick(domainName).UnwrapOr(nil),
		MinConfirmDate: b.MinConfirmDate,
		UntilPercent:   b.UntilPercent,
		ConversionRule: b.ConversionRule,
	}
}

// ForCluster merges the commitment behaviors for all domains together, thus reporting
// all durations that are allowed on at least one domain in no guaranteed order.
func (b CommitmentBehavior) ForCluster() ScopedCommitmentBehavior {
	// merge all `b.Durations[].Value` together
	var allDurations []limesresources.CommitmentDuration
	for _, entry := range b.DurationsPerDomain {
		if len(allDurations) == 0 {
			// optimization: avoid the loop below if possible
			allDurations = slices.Clone(entry.Value)
		} else {
			// merge without duplicates
			for _, duration := range entry.Value {
				if !slices.Contains(allDurations, duration) {
					allDurations = append(allDurations, duration)
				}
			}
		}
	}

	return ScopedCommitmentBehavior{
		Durations:      allDurations,
		MinConfirmDate: b.MinConfirmDate,
		UntilPercent:   b.UntilPercent,
		ConversionRule: b.ConversionRule,
	}
}

// CanConfirmCommitmentsAt evaluates the MinConfirmDate field.
func (b ScopedCommitmentBehavior) CanConfirmCommitmentsAt(t time.Time) (errorMsg string) {
	canConfirm := b.MinConfirmDate.IsNoneOr(func(input time.Time) bool { return input.Before(t) })
	if canConfirm {
		return ""
	}
	return "this commitment needs a `confirm_by` timestamp at or after " + b.MinConfirmDate.UnwrapOr(time.Time{}).Format(time.RFC3339)
}

// ForAPI converts this behavior into its API representation.
func (b ScopedCommitmentBehavior) ForAPI(now time.Time) Option[limesresources.CommitmentConfiguration] {
	if len(b.Durations) == 0 {
		return None[limesresources.CommitmentConfiguration]()
	}
	result := limesresources.CommitmentConfiguration{
		Durations: b.Durations,
	}
	if date, ok := b.MinConfirmDate.Unpack(); ok && date.After(now) {
		result.MinConfirmBy = &limes.UnixEncodedTime{Time: date}
	}
	return Some(result)
}

// CommitmentConversionRule describes how commitments for a resource may be converted
// into commitments for other resources with the same rule identifier.
type CommitmentConversionRule struct {
	Identifier string `yaml:"identifier"`
	Weight     uint64 `yaml:"weight"`
}

// CommitmentConversionRate describes the rate for converting commitments between two compatible resources.
type CommitmentConversionRate struct {
	FromAmount uint64
	ToAmount   uint64
}

// GetConversionRateTo checks whether this resource can be converted into the given resource.
// If so, the conversion rate is returned.
func (b ScopedCommitmentBehavior) GetConversionRateTo(other ScopedCommitmentBehavior) Option[CommitmentConversionRate] {
	sourceRule, ok := b.ConversionRule.Unpack()
	if !ok {
		return None[CommitmentConversionRate]()
	}
	targetRule, ok := other.ConversionRule.Unpack()
	if !ok {
		return None[CommitmentConversionRate]()
	}
	if sourceRule.Identifier != targetRule.Identifier {
		return None[CommitmentConversionRate]()
	}

	divisor := getGreatestCommonDivisor(sourceRule.Weight, targetRule.Weight)
	return Some(CommitmentConversionRate{
		FromAmount: targetRule.Weight / divisor,
		ToAmount:   sourceRule.Weight / divisor,
	})
}

func getGreatestCommonDivisor(a, b uint64) uint64 {
	if b == 0 {
		return a
	}
	return getGreatestCommonDivisor(b, a%b)
}
