/*******************************************************************************
*
* Copyright 2024 SAP SE
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

package core

import (
	"encoding/json"
	"fmt"

	"github.com/sapcc/go-api-declarations/limes"
)

// TranslationRule appears in type ResourceBehavior.
//
// It provides a backwards compatibility mechanism to format subcapacities or
// subresources provided by a LIQUID implementation back into the old format
// that was generated by the respective CapacityPlugin or QuotaPlugin.
type TranslationRule struct {
	// If not nil, reports need to pass all `subcapacities` strings through this handler.
	TranslateSubcapacities func(string, limes.AvailabilityZone) (string, error)
	// If not nil, reports need to pass all `subresources` strings through this handler.
	TranslateSubresources func(string, limes.AvailabilityZone) (string, error)
}

// NewTranslationRule returns the TranslationRule for the given ID, or an error if the ID is unknown.
func NewTranslationRule(id string) (TranslationRule, error) {
	switch id {
	case "":
		// the default is to not do any translation
		return TranslationRule{nil, nil}, nil
	case "cinder-volumes":
		return TranslationRule{nil, translateCinderVolumeSubresources}, nil
	case "cinder-snapshots":
		return TranslationRule{nil, translateCinderSnapshotSubresources}, nil
	case "cinder-manila-capacity":
		return TranslationRule{translateCinderOrManilaSubcapacities, nil}, nil
	default:
		return TranslationRule{}, fmt.Errorf("no such TranslationRule: %q", id)
	}
}

// UnmarshalYAML implements the yaml.Unmarshaler interface.
func (r *TranslationRule) UnmarshalYAML(unmarshal func(any) error) error {
	var id string
	err := unmarshal(&id)
	if err != nil {
		return err
	}

	*r, err = NewTranslationRule(id)
	return err
}

func translateCinderOrManilaSubcapacities(input string, az limes.AvailabilityZone) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	type newFormat struct {
		Name       string  `json:"name"`
		Capacity   uint64  `json:"capacity"`
		Usage      *uint64 `json:"usage"`
		Attributes struct {
			ExclusionReason string `json:"exclusion_reason"`
			RealCapacity    uint64 `json:"real_capacity"`
		} `json:"attributes"`
	}
	var inputs []newFormat
	err := json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		PoolName         string                 `json:"pool_name"`
		AvailabilityZone limes.AvailabilityZone `json:"az"`
		CapacityGiB      uint64                 `json:"capacity_gib"`
		UsageGiB         uint64                 `json:"usage_gib"`
		ExclusionReason  string                 `json:"exclusion_reason"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		if in.Usage == nil {
			return "", fmt.Errorf("no usage in subcapacity: %#v", in)
		}
		outputs[idx] = oldFormat{
			PoolName:         in.Name,
			AvailabilityZone: az,
			CapacityGiB:      in.Capacity,
			UsageGiB:         *in.Usage,
			ExclusionReason:  in.Attributes.ExclusionReason,
		}
		if in.Attributes.ExclusionReason != "" {
			outputs[idx].CapacityGiB = in.Attributes.RealCapacity
		}
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}

func translateCinderVolumeSubresources(input string, az limes.AvailabilityZone) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	type newFormat struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Attributes struct {
			SizeGiB uint64 `json:"size_gib"`
			Status  string `json:"status"`
		} `json:"attributes"`
	}
	var inputs []newFormat
	err := json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ID               string                 `json:"id"`
		Name             string                 `json:"name"`
		Status           string                 `json:"status"`
		Size             limes.ValueWithUnit    `json:"size"`
		AvailabilityZone limes.AvailabilityZone `json:"availability_zone"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		outputs[idx] = oldFormat{
			ID:               in.ID,
			Name:             in.Name,
			Status:           in.Attributes.Status,
			Size:             limes.ValueWithUnit{Value: in.Attributes.SizeGiB, Unit: limes.UnitGibibytes},
			AvailabilityZone: az,
		}
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}

func translateCinderSnapshotSubresources(input string, az limes.AvailabilityZone) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	type newFormat struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Attributes struct {
			SizeGiB  uint64 `json:"size_gib"`
			Status   string `json:"status"`
			VolumeID string `json:"volume_id"`
		} `json:"attributes"`
	}
	var inputs []newFormat
	err := json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ID       string              `json:"id"`
		Name     string              `json:"name"`
		Status   string              `json:"status"`
		Size     limes.ValueWithUnit `json:"size"`
		VolumeID string              `json:"volume_id"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		outputs[idx] = oldFormat{
			ID:       in.ID,
			Name:     in.Name,
			Status:   in.Attributes.Status,
			Size:     limes.ValueWithUnit{Value: in.Attributes.SizeGiB, Unit: limes.UnitGibibytes},
			VolumeID: in.Attributes.VolumeID,
		}
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}
