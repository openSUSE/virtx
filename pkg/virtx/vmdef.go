package virtx

import (
	"errors"
	"strings"
	"path/filepath"

	"suse.com/virtx/pkg/model"
)

/*
 * Return the index of the OS disk from a Vmdef, as per virtx convention.
 * The main OS disk is the first non-CDROM disk.
 */
func vmdef_find_os_disk(vmdef *openapi.Vmdef) int {
	var i, n int = 0, len(vmdef.Disks)
	for i = 0; i < n; i++ {
		if (vmdef.Disks[i].Device == openapi.DEVICE_DISK) {
			return i
		}
	}
	return -1
}

/* calculate the virtxml path from the os disk */
func vmdef_xml_path(vmdef *openapi.Vmdef) string {
	var os_disk int = vmdef_find_os_disk(vmdef)
	if (os_disk < 0) {
		return ""
	}
	var p string = vmdef.Disks[os_disk].Path
	p = strings.TrimSuffix(p, filepath.Ext(p)) + ".xml"
	return p
}

/*
 * Return the number of vcpus from a Vmdef
 */
func vmdef_get_vcpus(vmdef *openapi.Vmdef) uint {
	return uint(vmdef.Cpudef.Sockets * vmdef.Cpudef.Cores * vmdef.Cpudef.Threads);
}

/* get disk driver type from path, or "" if not recognized */
func disk_driver_from_path(p string) string {
	var (
		ext string
	)
	ext = filepath.Ext(p)
	switch (ext) {
	case ".qcow2":
		return "qcow2"
	case ".iso":
		fallthrough
	case ".raw":
		return "raw"
	}
	return ""
}

/* validate before generating the xml */
func vmdef_validate(vmdef *openapi.Vmdef) error {
	var err error
	if (vmdef.Name == "" || len(vmdef.Name) > VM_NAME_MAX) {
		return errors.New("invalid Name length")
	}
	if (vmdef.Memory.Total < 1) {
		return errors.New("invalid memory size")
	}
	if (vmdef.Cpudef.Model == "") {
		return errors.New("no cpu model provided")
	}
	if (vmdef.Cpudef.Sockets < 1 ||	vmdef.Cpudef.Cores < 1 || vmdef.Cpudef.Threads < 1) {
		return errors.New("no cpu topology provided")
	}
	if (vmdef.Cpudef.Threads > 1) {
		return errors.New("unsupported cpu topology")
	}
	if (vmdef.Genid != "" && vmdef.Genid != "auto" && len(vmdef.Genid) != 36) {
		return errors.New("invalid Genid")
	}
	if (len(vmdef.Disks) < 1 || len(vmdef.Disks) > DISKS_MAX) {
		return errors.New("invalid Disks")
	}
	if (vmdef_find_os_disk(vmdef) < 0) {
		return errors.New("no OS Disk")
	}
	if (len(vmdef.Nets) > NETS_MAX) {
		return errors.New("invalid Nets")
	}
	if (vmdef.Vlanid < 0 || vmdef.Vlanid > VLAN_MAX) {
		return errors.New("invalid Vlanid")
	}
	/* *** DISKS *** */
	for _, disk := range vmdef.Disks {
		if (disk.Size < 0) {
			return errors.New("invalid Disk Size")
		}
		if (disk.Path == "" || !filepath.IsAbs(disk.Path)) {
			return errors.New("invalid Disk Path")
		}
		var path string = filepath.Clean(disk.Path)
		path, err = filepath.EvalSymlinks(path)
		if (err != nil) {
			return errors.New("invalid Disk Path")
		}
		var disk_driver string = disk_driver_from_path(path)
		if (path != disk.Path || !strings.HasPrefix(disk.Path, VMS_DIR) || disk_driver == "") {
			/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
			return errors.New("invalid Disk Path")
		}
		if (!disk.Device.IsValid()) {
			return errors.New("invalid Disk Device")
		}
		if (!disk.Bus.IsValid()) {
			return errors.New("invalid Disk Bus")
		}
		if (!disk.Createmode.IsValid()) {
			return errors.New("invalid Disk Createmode")
		}
	}

	/* *** NETWORKS *** */
	for _, net := range vmdef.Nets {
		if (net.Mac != "" && len(net.Mac) != MAC_LEN) {
			return errors.New("invalid Mac")
		}
		if (!net.Model.IsValid()) {
			return errors.New("invalid Net model")
		}
	}
	/* *** CUSTOM FIELDS *** */
	for _, custom := range vmdef.Custom {
		if (custom.Name == "") {
			continue
		}
		if (!custom.IsAlnum()) {
			return errors.New("invalid Custom Field")
		}
	}
	return nil
}
