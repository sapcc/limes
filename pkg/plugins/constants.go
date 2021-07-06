/*******************************************************************************
*
* Copyright 2018 SAP SE
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

package plugins

//This is the list of all valid values for an image's "vmware_ostype" attribute
//in Nova/Glance. This list was copied from
//https://github.com/openstack/nova/blob/master/nova/virt/vmwareapi/constants.py
//and the comment over there says:
//
//      This list was extracted from a file on an installation of ESX 6.5. The
//      file can be found in /usr/lib/vmware/hostd/vimLocale/en/gos.vmsg
//      The contents of this list should be updated whenever there is a new
//      release of ESX.
var isValidVMwareOSType = map[string]bool{
	"asianux3_64Guest":        true,
	"asianux3Guest":           true,
	"asianux4_64Guest":        true,
	"asianux4Guest":           true,
	"asianux5_64Guest":        true,
	"asianux7_64Guest":        true,
	"centos64Guest":           true,
	"centosGuest":             true,
	"centos6Guest":            true,
	"centos6_64Guest":         true,
	"centos7_64Guest":         true,
	"centos7Guest":            true,
	"coreos64Guest":           true,
	"darwin10_64Guest":        true,
	"darwin10Guest":           true,
	"darwin11_64Guest":        true,
	"darwin11Guest":           true,
	"darwin12_64Guest":        true,
	"darwin13_64Guest":        true,
	"darwin14_64Guest":        true,
	"darwin15_64Guest":        true,
	"darwin16_64Guest":        true,
	"darwin64Guest":           true,
	"darwinGuest":             true,
	"debian4_64Guest":         true,
	"debian4Guest":            true,
	"debian5_64Guest":         true,
	"debian5Guest":            true,
	"debian6_64Guest":         true,
	"debian6Guest":            true,
	"debian7_64Guest":         true,
	"debian7Guest":            true,
	"debian8_64Guest":         true,
	"debian8Guest":            true,
	"debian9_64Guest":         true,
	"debian9Guest":            true,
	"debian10_64Guest":        true,
	"debian10Guest":           true,
	"dosGuest":                true,
	"eComStation2Guest":       true,
	"eComStationGuest":        true,
	"fedora64Guest":           true,
	"fedoraGuest":             true,
	"freebsd64Guest":          true,
	"freebsdGuest":            true,
	"genericLinuxGuest":       true,
	"mandrakeGuest":           true,
	"mandriva64Guest":         true,
	"mandrivaGuest":           true,
	"netware4Guest":           true,
	"netware5Guest":           true,
	"netware6Guest":           true,
	"nld9Guest":               true,
	"oesGuest":                true,
	"openServer5Guest":        true,
	"openServer6Guest":        true,
	"opensuse64Guest":         true,
	"opensuseGuest":           true,
	"oracleLinux64Guest":      true,
	"oracleLinuxGuest":        true,
	"oracleLinux6Guest":       true,
	"oracleLinux6_64Guest":    true,
	"oracleLinux7_64Guest":    true,
	"oracleLinux7Guest":       true,
	"os2Guest":                true,
	"other24xLinux64Guest":    true,
	"other24xLinuxGuest":      true,
	"other26xLinux64Guest":    true,
	"other26xLinuxGuest":      true,
	"other3xLinux64Guest":     true,
	"other3xLinuxGuest":       true,
	"otherGuest":              true,
	"otherGuest64":            true,
	"otherLinux64Guest":       true,
	"otherLinuxGuest":         true,
	"redhatGuest":             true,
	"rhel2Guest":              true,
	"rhel3_64Guest":           true,
	"rhel3Guest":              true,
	"rhel4_64Guest":           true,
	"rhel4Guest":              true,
	"rhel5_64Guest":           true,
	"rhel5Guest":              true,
	"rhel6_64Guest":           true,
	"rhel6Guest":              true,
	"rhel7_64Guest":           true,
	"rhel7Guest":              true,
	"sjdsGuest":               true,
	"sles10_64Guest":          true,
	"sles10Guest":             true,
	"sles11_64Guest":          true,
	"sles11Guest":             true,
	"sles12_64Guest":          true,
	"sles12Guest":             true,
	"sles64Guest":             true,
	"slesGuest":               true,
	"solaris10_64Guest":       true,
	"solaris10Guest":          true,
	"solaris11_64Guest":       true,
	"solaris6Guest":           true,
	"solaris7Guest":           true,
	"solaris8Guest":           true,
	"solaris9Guest":           true,
	"suse64Guest":             true,
	"suseGuest":               true,
	"turboLinux64Guest":       true,
	"turboLinuxGuest":         true,
	"ubuntu64Guest":           true,
	"ubuntuGuest":             true,
	"unixWare7Guest":          true,
	"vmkernel5Guest":          true,
	"vmkernel6Guest":          true,
	"vmkernel65Guest":         true,
	"vmkernelGuest":           true,
	"vmwarePhoton64Guest":     true,
	"win2000AdvServGuest":     true,
	"win2000ProGuest":         true,
	"win2000ServGuest":        true,
	"win31Guest":              true,
	"win95Guest":              true,
	"win98Guest":              true,
	"windows7_64Guest":        true,
	"windows7Guest":           true,
	"windows7Server64Guest":   true,
	"windows8_64Guest":        true,
	"windows8Guest":           true,
	"windows8Server64Guest":   true,
	"windows9_64Guest":        true,
	"windows9Guest":           true,
	"windows9Server64Guest":   true,
	"windowsHyperVGuest":      true,
	"winLonghorn64Guest":      true,
	"winLonghornGuest":        true,
	"winMeGuest":              true,
	"winNetBusinessGuest":     true,
	"winNetDatacenter64Guest": true,
	"winNetDatacenterGuest":   true,
	"winNetEnterprise64Guest": true,
	"winNetEnterpriseGuest":   true,
	"winNetStandard64Guest":   true,
	"winNetStandardGuest":     true,
	"winNetWebGuest":          true,
	"winNTGuest":              true,
	"winVista64Guest":         true,
	"winVistaGuest":           true,
	"winXPHomeGuest":          true,
	"winXPPro64Guest":         true,
	"winXPProGuest":           true,
}

//This is a list of all *stable* provisioning states of an Ironic node which will
//cause that node to not be considered when counting capacity.
//
//Reference: https://github.com/openstack/ironic/blob/master/ironic/common/states.py
var isAvailableProvisionState = map[string]bool{
	"enroll":     false,
	"manageable": false,
	"available":  true,
	"active":     true,
	"error":      true, //occurs during delete or rebuild, so node was active before
	"rescue":     true,
}
