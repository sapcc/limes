/******************************************************************************
*
*  Copyright 2023 SAP SE
*
*  Licensed under the Apache License, Version 2.0 (the "License");
*  you may not use this file except in compliance with the License.
*  You may obtain a copy of the License at
*
*      http://www.apache.org/licenses/LICENSE-2.0
*
*  Unless required by applicable law or agreed to in writing, software
*  distributed under the License is distributed on an "AS IS" BASIS,
*  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
*  See the License for the specific language governing permissions and
*  limitations under the License.
*
******************************************************************************/

package limesresources

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sapcc/go-api-declarations/limes"
)

// Commitment is the API representation of an *existing* commitment as reported by Limes.
type Commitment struct {
	ID               int64                  `json:"id"`
	ServiceType      limes.ServiceType      `json:"service_type"`
	ResourceName     ResourceName           `json:"resource_name"`
	AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`
	Amount           uint64                 `json:"amount"`
	Unit             limes.Unit             `json:"unit,omitempty"`
	Duration         CommitmentDuration     `json:"duration"`
	CreatedAt        limes.UnixEncodedTime  `json:"created_at"`
	// CreatorUUID and CreatorName identify the user who created this commitment.
	// CreatorName is in the format `fmt.Sprintf("%s@%s", userName, userDomainName)`
	// and intended for informational displays only. API access should always use the UUID.
	CreatorUUID string `json:"creator_uuid,omitempty"`
	CreatorName string `json:"creator_name,omitempty"`
	// CanBeDeleted will be true if the commitment can be deleted by the same user
	// who saw this object in response to a GET query.
	CanBeDeleted bool `json:"can_be_deleted,omitempty"`
	// ConfirmBy is only filled if it was set in the CommitmentRequest.
	ConfirmBy *limes.UnixEncodedTime `json:"confirm_by,omitempty"`
	// ConfirmedAt is only filled after the commitment was confirmed.
	ConfirmedAt *limes.UnixEncodedTime `json:"confirmed_at,omitempty"`
	ExpiresAt   limes.UnixEncodedTime  `json:"expires_at,omitempty"`
	// TransferStatus and TransferToken are only filled while the commitment is marked for transfer.
	TransferStatus CommitmentTransferStatus `json:"transfer_status,omitempty"`
	TransferToken  *string                  `json:"transfer_token,omitempty"`
	// NotifyOnConfirm can only be set if ConfirmBy is filled.
	// Used to send a mail notification at commitment confirmation.
	NotifyOnConfirm bool `json:"notify_on_confirm,omitempty"`
}

// CommitmentRequest is the API representation of a *new* commitment as requested by a user.
type CommitmentRequest struct {
	ServiceType      limes.ServiceType      `json:"service_type"`
	ResourceName     ResourceName           `json:"resource_name"`
	AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`
	Amount           uint64                 `json:"amount"`
	Duration         CommitmentDuration     `json:"duration"`
	ConfirmBy        *limes.UnixEncodedTime `json:"confirm_by,omitempty"`
	NotifyOnConfirm  bool                   `json:"notify_on_confirm,omitempty"`
}

// CommitmentConversionRule is the API representation of how commitments can be converted into a different resource.
//
// The conversion rate is represented as an integer fraction:
// For example, "FromAmount = 2" of the source resource and "ToAmount = 3" of the target resource corresponds to a 2:3 conversion rate.
type CommitmentConversionRule struct {
	FromAmount     uint64            `json:"from"`
	ToAmount       uint64            `json:"to"`
	TargetService  limes.ServiceType `json:"target_service"`
	TargetResource ResourceName      `json:"target_resource"`
}

// CommitmentTransferStatus is an enum.
type CommitmentTransferStatus string

const (
	// CommitmentTransferStatusNone is the default transfer status,
	// meaning that the commitment is not marked for transfer.
	CommitmentTransferStatusNone CommitmentTransferStatus = ""

	// CommitmentTransferStatusPublic means that the commitment is marked for transfer,
	// and is visible as such to all other projects.
	CommitmentTransferStatusPublic CommitmentTransferStatus = "public"

	// CommitmentTransferStatusUnlisted means that the commitment is marked for transfer,
	// but the receiver needs to know the commitment's transfer token.
	CommitmentTransferStatusUnlisted CommitmentTransferStatus = "unlisted"
)

// CommitmentDuration is the parsed representation of a commitment duration.
//
// The behavior of this type is similar to time.Duration or limesrates.Window for short durations
// (which are commonly used in automated tests for convenience and clarity),
// but also allows large durations with calendar-compatible calculations
// (e.g. "1y" is actually one year and not just 365 days).
type CommitmentDuration struct {
	//NOTE: this does not use uint etc. because time.Time.AddDate() wants int
	Years  int
	Months int
	Days   int
	Short  time.Duration // represents durations of hours, minutes and seconds
}

var cdTokenRx = regexp.MustCompile(`^([0-9]*)\s*(second|minute|hour|day|month|year)s?$`)

// ParseCommitmentDuration parses the string representation of a CommitmentDuration.
// Acceptable inputs include "5 hours" and "1year,2 \t months,  3days".
func ParseCommitmentDuration(input string) (CommitmentDuration, error) {
	var result CommitmentDuration
	for _, field := range strings.Split(input, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		match := cdTokenRx.FindStringSubmatch(field)
		if match == nil {
			return CommitmentDuration{}, fmt.Errorf("could not parse CommitmentDuration %q: malformed field %q", input, field)
		}
		amount, err := strconv.Atoi(match[1])
		if err != nil {
			return CommitmentDuration{}, fmt.Errorf("could not parse CommitmentDuration %q: malformed field %q", input, field)
		}
		switch match[2] {
		case "second":
			result.Short += time.Duration(amount) * time.Second
		case "minute":
			result.Short += time.Duration(amount) * time.Minute
		case "hour":
			result.Short += time.Duration(amount) * time.Hour
		case "day":
			result.Days += amount
		case "month":
			result.Months += amount
		case "year":
			result.Years += amount
		}
	}

	if result.Years == 0 && result.Months == 0 && result.Days == 0 && result.Short == 0 {
		return CommitmentDuration{}, fmt.Errorf("could not parse CommitmentDuration %q: empty duration", input)
	}
	return result, nil
}

// String returns the canonical string representation of this duration.
func (d CommitmentDuration) String() string {
	var fields []string
	format := func(amount int, unit string) {
		switch amount {
		case 0:
			return
		case 1:
			fields = append(fields, "1 "+unit)
		default:
			fields = append(fields, fmt.Sprintf("%d %ss", amount, unit))
		}
	}

	format(d.Years, "year")
	format(d.Months, "month")
	format(d.Days, "day")
	duration := d.Short

	hours := duration / time.Hour
	duration -= hours * time.Hour //nolint:durationcheck // false positive
	format(int(hours), "hour")

	minutes := duration / time.Minute
	duration -= minutes * time.Minute //nolint:durationcheck // false positive
	format(int(minutes), "minute")

	format(int(duration/time.Second), "second")
	return strings.Join(fields, ", ")
}

// AddTo adds this duration to the given time.
func (d CommitmentDuration) AddTo(t time.Time) time.Time {
	return t.AddDate(d.Years, d.Months, d.Days).Add(d.Short)
}

// MarshalJSON implements the json.Marshaler interface.
func (d CommitmentDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// UnmarshalJSON implements the json.Unmarshaler interface.
func (d *CommitmentDuration) UnmarshalJSON(input []byte) error {
	var s string
	err := json.Unmarshal(input, &s)
	if err != nil {
		return err
	}
	*d, err = ParseCommitmentDuration(s)
	return err
}

// MarshalYAML implements the yaml.Marshaler interface.
func (d CommitmentDuration) MarshalYAML() (any, error) {
	return d.String(), nil
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (d *CommitmentDuration) UnmarshalYAML(unmarshal func(any) error) error {
	var input string
	err := unmarshal(&input)
	if err != nil {
		return err
	}
	*d, err = ParseCommitmentDuration(input)
	return err
}

// Scan implements the sql.Scanner interface.
func (d *CommitmentDuration) Scan(src any) (err error) {
	var srcString string
	switch src := src.(type) {
	case string:
		srcString = src
	case []byte:
		srcString = string(src)
	case nil:
		srcString = ""
	default:
		return fmt.Errorf("cannot scan value of type %T into type limesresources.CommitmentDuration", src)
	}

	*d, err = ParseCommitmentDuration(srcString)
	return err
}

// Value implements the sql/driver.Valuer interface.
func (d CommitmentDuration) Value() (driver.Value, error) {
	return driver.Value(d.String()), nil
}
