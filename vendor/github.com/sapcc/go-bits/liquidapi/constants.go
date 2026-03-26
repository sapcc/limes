// SPDX-FileCopyrightText: 2018 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package liquidapi

import (
	"regexp"
)

// The following list of regexes is derived from a hardcoded list of valid values
// for an image's "vmware_ostype" attribute in Nova/Glance. This list was copied from
// https://github.com/openstack/nova/blob/master/nova/virt/vmwareapi/constants.py
// and the comment over there says:
//
//	This list was extracted from a file on an installation of ESX 6.5. The
//	file can be found in /usr/lib/vmware/hostd/vimLocale/en/gos.vmsg
//	The contents of this list should be updated whenever there is a new
//	release of ESX.
//
// As the list is over 4 years old, new os_type versions have broken our
// OSTypeProber. Therefore, we replaced all obvious version numbers in the valid
// values with placeholders, so that this does not break as easily in the future.
// We do not recommend utilizing this regex list for anything else other than
// validating the "vmware_ostype" attribute on existing images.

var isValidVMwareOSTypeRegex = []*regexp.Regexp{
	regexp.MustCompile(`^almalinux_64Guest$`),
	regexp.MustCompile(`^amazonlinux(\d+)_64Guest$`),
	regexp.MustCompile(`^asianux(\d+)_64Guest$`),
	regexp.MustCompile(`^asianux(\d+)Guest$`),
	regexp.MustCompile(`^centos64Guest$`),
	regexp.MustCompile(`^centos(\d+)_64Guest$`),
	regexp.MustCompile(`^centos(\d+)Guest$`),
	regexp.MustCompile(`^centosGuest$`),
	regexp.MustCompile(`^coreos64Guest$`),
	regexp.MustCompile(`^crxPod1Guest$`),
	regexp.MustCompile(`^crxSys1Guest$`),
	regexp.MustCompile(`^darwin(\d+)_64Guest$`),
	regexp.MustCompile(`^darwin(\d+)Guest$`),
	regexp.MustCompile(`^darwin64Guest$`),
	regexp.MustCompile(`^darwinGuest$`),
	regexp.MustCompile(`^debian(\d+)_64Guest$`),
	regexp.MustCompile(`^debian(\d+)Guest$`),
	regexp.MustCompile(`^dosGuest$`),
	regexp.MustCompile(`^eComStation2Guest$`),
	regexp.MustCompile(`^eComStationGuest$`),
	regexp.MustCompile(`^fedora64Guest$`),
	regexp.MustCompile(`^fedoraGuest$`),
	regexp.MustCompile(`^freebsd(\d+)_64Guest$`),
	regexp.MustCompile(`^freebsd(\d+)Guest$`),
	regexp.MustCompile(`^freebsd64Guest$`),
	regexp.MustCompile(`^freebsdGuest$`),
	regexp.MustCompile(`^mandrakeGuest$`),
	regexp.MustCompile(`^mandriva64Guest$`),
	regexp.MustCompile(`^mandrivaGuest$`),
	regexp.MustCompile(`^netware(\d+)Guest$`),
	regexp.MustCompile(`^nld9Guest$`),
	regexp.MustCompile(`^oesGuest$`),
	regexp.MustCompile(`^openServer(\d+)Guest$`),
	regexp.MustCompile(`^opensuse64Guest$`),
	regexp.MustCompile(`^opensuseGuest$`),
	regexp.MustCompile(`^oracleLinux(\d+)_64Guest$`),
	regexp.MustCompile(`^oracleLinux64Guest$`),
	regexp.MustCompile(`^oracleLinux(\d+)Guest$`),
	regexp.MustCompile(`^oracleLinuxGuest$`),
	regexp.MustCompile(`^os2Guest$`),
	regexp.MustCompile(`^other(\d+)xLinux64Guest$`),
	regexp.MustCompile(`^other(\d+)xLinuxGuest$`),
	regexp.MustCompile(`^otherGuest64$`),
	regexp.MustCompile(`^otherGuest$`),
	regexp.MustCompile(`^otherLinux64Guest$`),
	regexp.MustCompile(`^otherLinuxGuest$`),
	regexp.MustCompile(`^redhatGuest$`),
	regexp.MustCompile(`^rhel(\d+)_64Guest$`),
	regexp.MustCompile(`^rhel(\d+)Guest$`),
	regexp.MustCompile(`^rockylinux_64Guest$`),
	regexp.MustCompile(`^sjdsGuest$`),
	regexp.MustCompile(`^sles(\d+)_64Guest$`),
	regexp.MustCompile(`^sles(\d+)Guest$`),
	regexp.MustCompile(`^sles64Guest$`),
	regexp.MustCompile(`^slesGuest$`),
	regexp.MustCompile(`^solaris(\d+)_64Guest$`),
	regexp.MustCompile(`^solaris(\d+)Guest$`),
	regexp.MustCompile(`^suse64Guest$`),
	regexp.MustCompile(`^suseGuest$`),
	regexp.MustCompile(`^turboLinux64Guest$`),
	regexp.MustCompile(`^turboLinuxGuest$`),
	regexp.MustCompile(`^ubuntu64Guest$`),
	regexp.MustCompile(`^ubuntuGuest$`),
	regexp.MustCompile(`^unixWare7Guest$`),
	regexp.MustCompile(`^vmkernel(\d+)Guest$`),
	regexp.MustCompile(`^vmkernelGuest$`),
	regexp.MustCompile(`^vmwarePhoton64Guest$`),
	regexp.MustCompile(`^win(\d+)AdvServGuest$`),
	regexp.MustCompile(`^win(\d+)ProGuest$`),
	regexp.MustCompile(`^win(\d+)ServGuest$`),
	regexp.MustCompile(`^win(\d+)Guest$`),
	regexp.MustCompile(`^windows(\d+)_64Guest$`),
	regexp.MustCompile(`^windows(\d+)srv_64Guest$`),
	regexp.MustCompile(`^windows(\d+)srvNext_64Guest$`),
	regexp.MustCompile(`^windows(\d+)Guest$`),
	regexp.MustCompile(`^windows(\d+)Server64Guest$`),
	regexp.MustCompile(`^winLonghorn64Guest$`),
	regexp.MustCompile(`^winLonghornGuest$`),
	regexp.MustCompile(`^winNetBusinessGuest$`),
	regexp.MustCompile(`^winNetDatacenter64Guest$`),
	regexp.MustCompile(`^winNetDatacenterGuest$`),
	regexp.MustCompile(`^winNetEnterprise64Guest$`),
	regexp.MustCompile(`^winNetEnterpriseGuest$`),
	regexp.MustCompile(`^winNetStandard64Guest$`),
	regexp.MustCompile(`^winNetStandardGuest$`),
	regexp.MustCompile(`^winNetWebGuest$`),
	regexp.MustCompile(`^winNTGuest$`),
	regexp.MustCompile(`^winVista64Guest$`),
	regexp.MustCompile(`^winVistaGuest$`),
	regexp.MustCompile(`^winXPHomeGuest$`),
	regexp.MustCompile(`^winXPPro64Guest$`),
	regexp.MustCompile(`^winXPProGuest$`),
}
