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
	"strings"

	"github.com/sapcc/go-api-declarations/limes"
	"github.com/sapcc/go-api-declarations/liquid"
)

// TranslationRule appears in type ResourceBehavior.
//
// It provides a backwards compatibility mechanism to format subcapacities or
// subresources provided by a LIQUID implementation back into the old format
// that was generated by the respective CapacityPlugin or QuotaPlugin.
type TranslationRule struct {
	// If not nil, reports need to pass all `subcapacities` strings through this handler.
	TranslateSubcapacities func(string, limes.AvailabilityZone, liquid.ResourceName, liquid.ResourceInfo) (string, error)
	// If not nil, reports need to pass all `subresources` strings through this handler.
	TranslateSubresources func(string, limes.AvailabilityZone, liquid.ResourceName, liquid.ResourceInfo) (string, error)
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
	case "ironic-flavors":
		return TranslationRule{translateIronicSubcapacities, translateIronicSubresources}, nil
	case "nova-flavors":
		return TranslationRule{translateNovaSubcapacities, translateNovaSubresources}, nil
	default:
		return TranslationRule{}, fmt.Errorf("no such TranslationRule: %q", id)
	}
}

// IsEmpty returns whether this translation rule contains only nil members.
func (r TranslationRule) IsEmpty() bool {
	return r.TranslateSubcapacities == nil && r.TranslateSubresources == nil
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

func translateCinderOrManilaSubcapacities(input string, az limes.AvailabilityZone, _ liquid.ResourceName, _ liquid.ResourceInfo) (string, error) {
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

func translateCinderVolumeSubresources(input string, az limes.AvailabilityZone, _ liquid.ResourceName, _ liquid.ResourceInfo) (string, error) {
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

func translateCinderSnapshotSubresources(input string, az limes.AvailabilityZone, _ liquid.ResourceName, _ liquid.ResourceInfo) (string, error) {
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

func translateIronicSubcapacities(input string, az limes.AvailabilityZone, _ liquid.ResourceName, resInfo liquid.ResourceInfo) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	var resAttrs struct {
		Cores     uint64 `json:"cores"`
		MemoryMiB uint64 `json:"ram_mib"`
		DiskGiB   uint64 `json:"disk_gib"`
	}
	err := json.Unmarshal(resInfo.Attributes, &resAttrs)
	if err != nil {
		return "", fmt.Errorf("while parsing ResourceInfo.Attributes: %w", err)
	}

	type newFormat struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Attributes struct {
			ProvisionState       string  `json:"provision_state"`
			TargetProvisionState *string `json:"target_provision_state"`
			SerialNumber         string  `json:"serial_number"`
			InstanceID           *string `json:"instance_id"`
		} `json:"attributes"`
	}
	var inputs []newFormat
	err = json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ID                   string                 `json:"id"`
		Name                 string                 `json:"name"`
		ProvisionState       string                 `json:"provision_state,omitempty"`
		TargetProvisionState *string                `json:"target_provision_state,omitempty"`
		AvailabilityZone     limes.AvailabilityZone `json:"availability_zone"`
		RAM                  limes.ValueWithUnit    `json:"ram,omitempty"`
		Disk                 limes.ValueWithUnit    `json:"disk,omitempty"`
		Cores                uint64                 `json:"cores,omitempty"`
		SerialNumber         string                 `json:"serial,omitempty"`
		InstanceID           *string                `json:"instance_id,omitempty"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		out := oldFormat{
			ID:                   in.ID,
			Name:                 in.Name,
			ProvisionState:       in.Attributes.ProvisionState,
			TargetProvisionState: in.Attributes.TargetProvisionState,
			AvailabilityZone:     az,
			Cores:                resAttrs.Cores,
			SerialNumber:         in.Attributes.SerialNumber,
			InstanceID:           in.Attributes.InstanceID,
		}
		if resAttrs.MemoryMiB > 0 {
			out.RAM = limes.ValueWithUnit{Unit: limes.UnitMebibytes, Value: resAttrs.MemoryMiB}
		}
		if resAttrs.DiskGiB > 0 {
			out.Disk = limes.ValueWithUnit{Unit: limes.UnitGibibytes, Value: resAttrs.DiskGiB}
		}
		outputs[idx] = out
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}

func translateIronicSubresources(input string, az limes.AvailabilityZone, resName liquid.ResourceName, resInfo liquid.ResourceInfo) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	var resAttrs struct {
		Cores     uint64 `json:"cores"`
		MemoryMiB uint64 `json:"ram_mib"`
		DiskGiB   uint64 `json:"disk_gib"`
	}
	err := json.Unmarshal(resInfo.Attributes, &resAttrs)
	if err != nil {
		return "", fmt.Errorf("while parsing ResourceInfo.Attributes: %w", err)
	}

	type newFormat struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Attributes struct {
			Status   string            `json:"status"`
			Metadata map[string]string `json:"metadata"`
			Tags     []string          `json:"tags"`
			OSType   string            `json:"os_type"`
		} `json:"attributes"`
	}
	var inputs []newFormat
	err = json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ID             string                 `json:"id"`
		Name           string                 `json:"name"`
		Status         string                 `json:"status"`
		Metadata       map[string]string      `json:"metadata"`
		Tags           []string               `json:"tags"`
		AZ             limes.AvailabilityZone `json:"availability_zone"`
		HypervisorType string                 `json:"hypervisor,omitempty"`
		FlavorName     string                 `json:"flavor"`
		VCPUs          uint64                 `json:"vcpu"`
		MemoryMiB      limes.ValueWithUnit    `json:"ram"`
		DiskGiB        limes.ValueWithUnit    `json:"disk"`
		OSType         string                 `json:"os_type"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		out := oldFormat{
			ID:             in.ID,
			Name:           in.Name,
			Status:         in.Attributes.Status,
			Metadata:       in.Attributes.Metadata,
			Tags:           in.Attributes.Tags,
			AZ:             az,
			HypervisorType: "none", // baremetal is called "none" in this format
			FlavorName:     strings.TrimPrefix(string(resName), "instances_"),
			VCPUs:          resAttrs.Cores,
			MemoryMiB:      limes.ValueWithUnit{Unit: limes.UnitMebibytes, Value: resAttrs.MemoryMiB},
			DiskGiB:        limes.ValueWithUnit{Unit: limes.UnitGibibytes, Value: resAttrs.DiskGiB},
			OSType:         in.Attributes.OSType,
		}
		outputs[idx] = out
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}

func translateNovaSubcapacities(input string, az limes.AvailabilityZone, _ liquid.ResourceName, resInfo liquid.ResourceInfo) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	type newFormat struct {
		ID         string  `json:"id"`
		Name       string  `json:"name"`
		Capacity   uint64  `json:"capacity"`
		Usage      *uint64 `json:"usage"`
		Attributes struct {
			AggregateName string   `json:"aggregate_name"`
			Traits        []string `json:"traits"`
		} `json:"attributes"`
	}

	var inputs []newFormat
	err := json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ServiceHost      string                 `json:"service_host"`
		AvailabilityZone limes.AvailabilityZone `json:"az"`
		AggregateName    string                 `json:"aggregate"`
		Capacity         *uint64                `json:"capacity,omitempty"`
		Usage            *uint64                `json:"usage,omitempty"`
		Traits           []string               `json:"traits"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		out := oldFormat{
			ServiceHost:      in.Name,
			AvailabilityZone: az,
			AggregateName:    in.Attributes.AggregateName,
			Capacity:         &in.Capacity,
			Usage:            in.Usage,
			Traits:           in.Attributes.Traits,
		}
		outputs[idx] = out
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}

func translateNovaSubresources(input string, az limes.AvailabilityZone, resName liquid.ResourceName, resInfo liquid.ResourceInfo) (string, error) {
	if input == "" || input == "[]" {
		return input, nil
	}

	type newFormat struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Attributes struct {
			Status   string                  `json:"status"`
			Metadata map[string]string       `json:"metadata"`
			Tags     []string                `json:"tags"`
			AZ       liquid.AvailabilityZone `json:"availability_zone"`
			Flavor   struct {
				Name           string  `json:"name"`
				VCPUs          uint64  `json:"vcpu"`
				MemoryMiB      uint64  `json:"ram_mib"`
				DiskGiB        uint64  `json:"disk_gib"`
				VideoMemoryMiB *uint64 `json:"video_ram_mib"`
			} `json:"flavor"`
			OSType string `json:"os_type"`
		}
	}
	var inputs []newFormat
	err := json.Unmarshal([]byte(input), &inputs)
	if err != nil {
		return "", err
	}

	type oldFormat struct {
		ID             string                 `json:"id"`
		Name           string                 `json:"name"`
		Status         string                 `json:"status"`
		Metadata       map[string]string      `json:"metadata"`
		Tags           []string               `json:"tags"`
		AZ             limes.AvailabilityZone `json:"availability_zone"`
		HypervisorType string                 `json:"hypervisor,omitempty"`
		FlavorName     string                 `json:"flavor"`
		VCPUs          uint64                 `json:"vcpu"`
		MemoryMiB      limes.ValueWithUnit    `json:"ram"`
		DiskGiB        limes.ValueWithUnit    `json:"disk"`
		VideoMemoryMiB *limes.ValueWithUnit   `json:"video_ram,omitempty"`
		OSType         string                 `json:"os_type"`
	}
	outputs := make([]oldFormat, len(inputs))
	for idx, in := range inputs {
		out := oldFormat{
			ID:             in.ID,
			Name:           in.Name,
			Status:         in.Attributes.Status,
			Metadata:       in.Attributes.Metadata,
			Tags:           in.Attributes.Tags,
			AZ:             in.Attributes.AZ,
			HypervisorType: "vmware",
			FlavorName:     in.Attributes.Flavor.Name,
			VCPUs:          in.Attributes.Flavor.VCPUs,
			MemoryMiB:      limes.ValueWithUnit{Unit: limes.UnitMebibytes, Value: in.Attributes.Flavor.MemoryMiB},
			DiskGiB:        limes.ValueWithUnit{Unit: limes.UnitGibibytes, Value: in.Attributes.Flavor.DiskGiB},
			OSType:         in.Attributes.OSType,
		}
		if in.Attributes.Flavor.VideoMemoryMiB != nil {
			out.VideoMemoryMiB = &limes.ValueWithUnit{Unit: limes.UnitMebibytes, Value: *in.Attributes.Flavor.VideoMemoryMiB}
		}
		outputs[idx] = out
	}
	buf, err := json.Marshal(outputs)
	return string(buf), err
}
