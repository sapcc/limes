// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"maps"
	"slices"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
	limesresources "github.com/sapcc/go-api-declarations/limes/resources"
	"github.com/sapcc/go-api-declarations/liquid"
	"github.com/sapcc/go-bits/errext"
	"github.com/sapcc/go-bits/regexpext"
	"go.xyrillian.de/gg/is"
	. "go.xyrillian.de/gg/option"

	resourcesv2 "github.com/sapcc/limes/internal/apideclarations/apiv2/resources"
)

// CommitmentBehavior describes how commitments work for a single resource.
//
// It appears in type ServiceConfiguration.
type CommitmentBehavior struct {
	// This ConfigSet is keyed on domain name, because commitment durations
	// (and thus committability) are allowed to differ per domain.
	//
	// If DurationsPerDomain.Pick() returns an empty slice, then commitments are entirely forbidden for that resource in the given domain.
	DurationsPerDomain regexpext.ConfigSet[string, []limesresources.CommitmentDuration] `json:"durations_per_domain"`

	MinConfirmDate  Option[time.Time]                                     `json:"min_confirm_date"`
	UntilPercent    Option[float64]                                       `json:"until_percent"`
	ConversionRules map[ConversionRuleIdentifier]CommitmentConversionRule `json:"conversion_rules"`
}

// Validate returns a list of all errors in this behavior configuration.
//
// The `path` argument denotes the location of this behavior in the
// configuration file, and will be used when generating error messages.
func (b CommitmentBehavior) Validate(path string, occupiedConversionIdentifiers []ConversionRuleIdentifier) (errs errext.ErrorSet, identifiers []ConversionRuleIdentifier) {
	if percent, ok := b.UntilPercent.Unpack(); ok {
		if percent < 0 {
			errs.Addf("invalid value: %s.until_percent may not be smaller than 0", path)
		}
		if percent > 100 {
			errs.Addf("invalid value: %s.until_percent may not be bigger than 100", path)
		}
	}
	for identifier := range b.ConversionRules {
		if slices.Contains(occupiedConversionIdentifiers, identifier) {
			errs.Addf("invalid value: %[1]s.conversion_rules[%[2]q] identifier must be restricted to a single serviceType, but %[2]q is already used by another serviceType", path, identifier)
		}
		identifiers = append(identifiers, identifier)
	}

	return errs, identifiers
}

// ScopedCommitmentBehavior is a CommitmentBehavior that applies only to a certain scope (usually a specific domain).
// It is created through the For... methods on type CommitmentBehavior.
type ScopedCommitmentBehavior struct {
	Durations       []limesresources.CommitmentDuration
	MinConfirmDate  Option[time.Time]
	UntilPercent    Option[float64]
	ConversionRules map[ConversionRuleIdentifier]CommitmentConversionRule
}

// ForDomain resolves Durations.Pick() using the provided domain name.
func (b CommitmentBehavior) ForDomain(domainName string) ScopedCommitmentBehavior {
	return ScopedCommitmentBehavior{
		Durations:       b.DurationsPerDomain.Pick(domainName).UnwrapOr(nil),
		MinConfirmDate:  b.MinConfirmDate,
		UntilPercent:    b.UntilPercent,
		ConversionRules: b.ConversionRules,
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
		Durations:       allDurations,
		MinConfirmDate:  b.MinConfirmDate,
		UntilPercent:    b.UntilPercent,
		ConversionRules: b.ConversionRules,
	}
}

// CanConfirmCommitmentsAt evaluates the MinConfirmDate field.
func (b ScopedCommitmentBehavior) CanConfirmCommitmentsAt(t time.Time) (errorMsg string) {
	canConfirm := b.MinConfirmDate.IsNoneOr(is.NotAfter(t))
	if canConfirm {
		return ""
	}
	return "this commitment needs a `confirm_by` timestamp at or after " + b.MinConfirmDate.UnwrapOr(time.Time{}).Format(time.RFC3339)
}

// ForAPI converts this behavior into its API representation.
func (b ScopedCommitmentBehavior) ForAPI(now time.Time) Option[limesresources.CommitmentConfiguration] {
	if v2Result, ok := b.ForV2API(now).Unpack(); ok {
		return Some(limesresources.CommitmentConfiguration{
			Durations:    v2Result.Durations,
			MinConfirmBy: v2Result.MinConfirmBy.AsPointer(),
		})
	}
	return None[limesresources.CommitmentConfiguration]()
}

// ForV2API converts this behavior into its v2-API representation.
func (b ScopedCommitmentBehavior) ForV2API(now time.Time) Option[resourcesv2.CommitmentConfiguration] {
	if len(b.Durations) == 0 {
		return None[resourcesv2.CommitmentConfiguration]()
	}
	result := resourcesv2.CommitmentConfiguration{
		Durations: b.Durations,
	}
	if date, ok := b.MinConfirmDate.Unpack(); ok && date.After(now) {
		result.MinConfirmBy = Some(limes.UnixEncodedTime{Time: date})
	}
	return Some(result)
}

// ConversionRuleIdentifier is an explicit type to increased readability of the code
type ConversionRuleIdentifier string

// CommitmentConversionRule describes how commitments for a resource may be converted
// into commitments for other resources with the same rule identifier.
type CommitmentConversionRule struct {
	Weight limes.Unit `json:"weight"`
	// if set, only allows this conversion rule as source of conversions
	OnlySource bool `json:"only_source"`
	// if set, this rule as target allows to round the amount down to the next integer, except when it would be 0
	AllowRounding bool `json:"allow_rounding"`
}

// CommitmentConversionRate describes the rate for converting commitments between two compatible resources.
type CommitmentConversionRate struct {
	FromAmount    uint64
	ToAmount      uint64
	AllowRounding bool
}

// GetConversionRateTo checks whether this resource can be converted into the given resource.
// If so, the conversion rate between the first two matching conversion rules (ordered by
// identifier alphabetically) is returned.
//
// The conversion rate satisfies the invariant:
//
//	fromAmount × sourceWeightFactor × sourceUnitFactor = toAmount × targetWeightFactor × targetUnitFactor
//
// To avoid overflow when multiplying large byte-based factors, the four factors
// are reduced pairwise (weight pair and unit pair) by their GCD before multiplying.
func (b ScopedCommitmentBehavior) GetConversionRateTo(other ScopedCommitmentBehavior, sourceUnit, targetUnit liquid.Unit) Option[CommitmentConversionRate] {
	sourceRules := b.ConversionRules
	if len(sourceRules) == 0 {
		return None[CommitmentConversionRate]()
	}
	targetRules := other.ConversionRules
	if len(targetRules) == 0 {
		return None[CommitmentConversionRate]()
	}

	sourceUnitBase, sourceUnitFactor := sourceUnit.Base()
	sourceUnitIsByte := sourceUnitBase == limes.UnitBytes
	targetUnitBase, targetUnitFactor := targetUnit.Base()
	targetUnitIsByte := targetUnitBase == limes.UnitBytes

outer:
	for _, sourceIdentifier := range slices.Sorted(maps.Keys(sourceRules)) {
		sourceRule := sourceRules[sourceIdentifier]
		for _, targetIdentifier := range slices.Sorted(maps.Keys(targetRules)) {
			targetRule := targetRules[targetIdentifier]
			if sourceIdentifier != targetIdentifier || targetRule.OnlySource {
				if sourceIdentifier <= targetIdentifier {
					continue outer
				}
				continue
			}
			sourceWeightUnitBase, sourceWeightFactor := sourceRule.Weight.Base()
			sourceWeightIsByte := sourceWeightUnitBase == limes.UnitBytes
			targetWeightUnitBase, targetWeightFactor := targetRule.Weight.Base()
			targetWeightIsByte := targetWeightUnitBase == limes.UnitBytes

			// we need to guard the units so that this works out:
			// okay: all piece units
			// okay: one of source is byte based, one of target is byte based
			// not okay: all other cases
			if sourceWeightIsByte && sourceUnitIsByte || targetWeightIsByte && targetUnitIsByte {
				continue
			}
			if (sourceWeightIsByte || sourceUnitIsByte) && !targetWeightIsByte && !targetUnitIsByte {
				continue
			}
			if (targetWeightIsByte || targetUnitIsByte) && !sourceWeightIsByte && !sourceUnitIsByte {
				continue
			}

			// we need: fromAmount × sourceWeightFactor × sourceUnitFactor = toAmount × targetWeightFactor × targetUnitFactor
			// therefore: fromAmount / toAmount = (targetWeightFactor × targetUnitFactor) / (sourceWeightFactor × sourceUnitFactor)
			// to reduce before multiplying (avoiding overflow), we cancel common factors weights and units separately
			weightDivisor := getGreatestCommonDivisor(sourceWeightFactor, targetWeightFactor)
			unitDivisor := getGreatestCommonDivisor(sourceUnitFactor, targetUnitFactor)
			fromAmount := (targetWeightFactor / weightDivisor) * (targetUnitFactor / unitDivisor)
			toAmount := (sourceWeightFactor / weightDivisor) * (sourceUnitFactor / unitDivisor)

			finalDivisor := getGreatestCommonDivisor(fromAmount, toAmount)
			fromAmount /= finalDivisor
			toAmount /= finalDivisor

			return Some(CommitmentConversionRate{
				FromAmount:    fromAmount,
				ToAmount:      toAmount,
				AllowRounding: targetRule.AllowRounding,
			})
		}
	}

	return None[CommitmentConversionRate]()
}

func getGreatestCommonDivisor(a, b uint64) uint64 {
	if b == 0 {
		return a
	}
	return getGreatestCommonDivisor(b, a%b)
}
