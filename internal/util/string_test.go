// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package util

import (
	"testing"

	"github.com/sapcc/go-bits/assert"
)

func TestTitleCase(t *testing.T) {
	tests := []struct {
		input  string
		output string
	}{
		// Empty and whitespace cases
		{"", ""},
		{"_", ""},
		{"__", ""},
		{"_ _", ""},

		// Single keyword cases
		{"hdd", "HDD"},
		{"iscsi", "ISCSI"},
		{"nfs", "NFS"},
		{"vmdk", "VMDK"},
		{"kvm", "KVM"},
		{"ssd", "SSD"},

		// Keyword with underscore prefix
		{"hdd_foo", "HDD Foo"},
		{"iscsi_foo", "ISCSI Foo"},
		{"nfs_bar", "NFS Bar"},
		{"vmdk_bar", "VMDK Bar"},
		{"kvm_baz", "KVM Baz"},
		{"ssd_baz", "SSD Baz"},

		// Keyword with underscore suffix
		{"foo_hdd", "Foo HDD"},
		{"foo_iscsi", "Foo ISCSI"},
		{"bar_nfs", "Bar NFS"},
		{"bar_vmdk", "Bar VMDK"},
		{"baz_kvm", "Baz KVM"},
		{"baz_ssd", "Baz SSD"},

		// Multiple underscores
		{"foo__bar", "Foo Bar"},

		// Single word (no underscores)
		{"simple", "Simple"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			assert.Equal(t, TitleCase(test.input), test.output)
		})
	}
}
