package virtx

import (
	"net/http"
	"encoding/json"
	"bytes"
	"path/filepath"
	"strings"
	"os"
	"os/exec"
	"fmt"
	"errors"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
)

/*
 * Return the index of the main disk, as per virtx convention.
 * The main disk is the first non-CDROM disk.
 */
func vm_find_main_disk(vmdef *openapi.Vmdef) (int, error) {
	var i, n int = 0, len(vmdef.Disks)
	for i = 0; i < n; i++ {
		if (vmdef.Disks[i].Device == openapi.DEVICE_DISK) {
			return i, nil
		}
	}
	return -1, errors.New("vm_get_main_disk: not found")
}

/* calculate the virtxml path from the main disk */
func vm_calc_virtxml_path(vmdef *openapi.Vmdef, main_disk int) string {
	var p string = vmdef.Disks[main_disk].Path
	p = strings.TrimSuffix(p, filepath.Ext(p)) + ".xml"
	return p
}

/* get disk driver type from path, or "" if not recognized */
func disk_get_driver_from_path(p string) string {
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

/* we use virt-install for now for simplicity while possible */

func vm_create(w http.ResponseWriter, r *http.Request) {
	service.m.RLock()
	defer service.m.RUnlock()
	var (
		err error
		ok bool
		o openapi.VmCreateOptions
		libvirt_uri string
		main_disk int
		host openapi.Host
	)
	err = json.NewDecoder(r.Body).Decode(&o)
	if (err != nil) {
		http.Error(w, "vm_create: Failed to decode JSON in Request Body", http.StatusBadRequest)
		return
	}
	if (o.Host != "") {
		host, ok = service.hosts[o.Host]
		if (!ok) {
			http.Error(w, "vm_create: could not find host", http.StatusUnprocessableEntity)
			return
		}
		libvirt_uri = "qemu+ssh://" + host.Def.Name + "/system"
	} else {
		host, ok = service.hosts[hypervisor.Uuid]
		if (!ok) {
			logger.Log("vm_create: could not find my own host in service")
			http.Error(w, "vm_create: invalid service state", http.StatusInternalServerError)
			return
		}
		libvirt_uri = "qemu:///system"
	}
	if (host.State != openapi.HOST_ACTIVE) {
		http.Error(w, "vm_create: host is not active", http.StatusUnprocessableEntity)
		return
	}

	/* Main validation of parameters supplied */

	if (o.Vmdef.Name == "" || len(o.Vmdef.Name) > VM_NAME_MAX) {
		http.Error(w, "vm_create: invalid Name", http.StatusBadRequest)
		return
	}
	if (o.Vmdef.Memory.Total < 1) {
		http.Error(w, "vm_create: invalid memory size", http.StatusBadRequest)
		return
	}
	if (o.Vmdef.Cpudef.Model == "") {
		http.Error(w, "vm_create: no Cpu model provided", http.StatusBadRequest)
		return
	}
	if (o.Vmdef.Cpudef.Sockets < 1 || o.Vmdef.Cpudef.Cores < 1 || o.Vmdef.Cpudef.Threads < 1) {
		http.Error(w, "vm_create: no Cpu topology provided", http.StatusBadRequest)
		return
	}
	if (o.Vmdef.Cpudef.Threads > 1) {
		http.Error(w, "vm_create: unsupported Cpu topology", http.StatusNotImplemented)
		return
	}
	if (o.Vmdef.Genid != "" && o.Vmdef.Genid != "auto" && len(o.Vmdef.Genid) != 36) {
		http.Error(w, "vm_create: invalid Genid", http.StatusBadRequest)
		return
	}
	if (len(o.Vmdef.Disks) < 1 || len(o.Vmdef.Disks) > DISKS_MAX) {
		http.Error(w, "vm_create: invalid Disks", http.StatusBadRequest)
		return
	}
	main_disk, err = vm_find_main_disk(&o.Vmdef)
	if (err != nil) {
		http.Error(w, "vm_create: no main Disk", http.StatusBadRequest)
		return
	}
	if (len(o.Vmdef.Nets) > NETS_MAX) {
		http.Error(w, "vm_create: invalid Nets", http.StatusBadRequest)
		return
	}

	/* virt-install: start building command line */

	var args []string
	args = append(args, "--print-xml", "--dry-run", "--noautoconsole", "--check", "all=on",
		"--virt-type", "kvm", "--os-variant", "detect=off,require=off")

	if (o.Vmdef.Firmware == openapi.FIRMWARE_UEFI) {
		args = append(args, "--machine", "q35")
	} else {
		args = append(args, "--machine", "pc")
	}
	args = append(args, "--name", o.Vmdef.Name)
	args = append(args, "--metadata", "title=" + o.Vmdef.Name)
	var virtxml string = vm_calc_virtxml_path(&o.Vmdef, main_disk)
	/* XXX we use the description field because there is seemingly no way to add custom metadata XXX */
	args = append(args, "--metadata", "description=" + virtxml)

	args = append(args, "--memory", fmt.Sprintf("%d", o.Vmdef.Memory.Total))
	var cpu_str string = o.Vmdef.Cpudef.Model
	if (cpu_str == "host-passthrough") {
		cpu_str += ",check=none,migratable=on"
	}
	cpu_str += fmt.Sprintf(",topology.sockets=%d,topology.cores=%d,topology.threads=%d",
		o.Vmdef.Cpudef.Sockets, o.Vmdef.Cpudef.Cores, o.Vmdef.Cpudef.Threads)

	args = append(args, "--cpu", cpu_str)

	if (o.Vmdef.Firmware == openapi.FIRMWARE_UEFI) {
		args = append(args, "--boot", "uefi")
	}
	if (o.Vmdef.Genid != "") {
		var genid_str string
		if (o.Vmdef.Genid == "auto") {
			genid_str = "genid_enable=yes"
		} else {
			genid_str = "genid=" + o.Vmdef.Genid
		}
		args = append(args, "--metadata", genid_str)
	}
	/* XXX add sysinfo for smbios.reflectHost ? XXX */
	/*
	if (sysinfo):
	args.extend(["--sysinfo", sysinfo])
	*/
	/* display, graphics (no sound atm) */
	args = append(args, "--graphics", "vnc")

	/* use virtio-vga for x86 and virtio-gpu-pci for AArch64 */
	var video string = "none"
	switch (host.Def.Cpuarch.Arch) {
	case "x86_64":
		video = "vga"
	case "aarch64":
		video = "virtio-gpu-pci"
	}
	args = append(args, "--video", "model.type=" + video)

	/* XXX no sound for now, does not seem needed on server XXX */
	/*
	   if (sound):
	   args.extend(["--sound", f"model={sound}"])
	*/
	/* XXX events XXX
	args.extend(["--events", "on_crash=restart"])
	*/

	/* XXX todo vlanid XXX */

	/* Disks and controllers */

	/* keep track of the index of the disk _per bus type_ */

	disk_count := make(map[string]int)
	var iothread_count int

	for _, disk := range o.Vmdef.Disks {
		if (disk.Size < 0) {
			http.Error(w, "vm_create: invalid Disk Size", http.StatusBadRequest)
			return
		}
		if (disk.Path == "" || !filepath.IsAbs(disk.Path)) {
			http.Error(w, "vm_create: invalid Disk Path", http.StatusBadRequest)
			return
		}
		var path string = filepath.Clean(disk.Path)
		path, err = filepath.EvalSymlinks(path)
		if (err != nil) {
			http.Error(w, "vm_create: invalid Disk Path", http.StatusBadRequest)
			return
		}
		var disk_driver string = disk_get_driver_from_path(path)
		if (path != disk.Path || !strings.HasPrefix(disk.Path, VMS_DIR) || disk_driver == "") {
			/* symlink shenanigans, or not starting with /vms/ or invalid ext : bail */
			http.Error(w, "vm_create: invalid Disk Path", http.StatusBadRequest)
			return
		}
		if (!disk.Device.IsValid()) {
			http.Error(w, "vm_create: invalid Disk Device", http.StatusBadRequest)
			return
		}
		if (!disk.Bus.IsValid()) {
			http.Error(w, "vm_create: invalid Disk Bus", http.StatusBadRequest)
			return
		}
		if (!disk.Createmode.IsValid()) {
			http.Error(w, "vm_create: invalid Disk Createmode", http.StatusBadRequest)
			return
		}
		/* keep track of how many disks require a specific bus type */

		/* XXX TODO: Handle Disk Creation, Size XXX */
		var (
			ctrl_type string
			ctrl_model string
			use_iothread bool
		)
		switch (disk.Bus) {
		case openapi.BUS_SCSI:
			ctrl_type = "scsi"
			ctrl_model = "auto"
		case openapi.BUS_VIRTIO_SCSI:
			ctrl_type = "scsi"
			ctrl_model = "virtio-scsi"
			use_iothread = true
		case openapi.BUS_SATA:
			ctrl_type = "sata"
			ctrl_model = ""
		case openapi.BUS_VIRTIO_BLK:
			ctrl_type = "virtio"
			ctrl_model = ""
			use_iothread = true
		}
		if (ctrl_type != "virtio") { /* no controller for virtio-blk */
			var controller string = "type=" + ctrl_type
			if (ctrl_model != "") {
				controller += ",model=" + ctrl_model
			}
			controller += fmt.Sprintf(",index=%d", disk_count[ctrl_type])
			if (use_iothread) {
				/* iothread index starts from 1! */
				iothread_count += 1
				controller += fmt.Sprintf(",driver.iothread=%d", iothread_count)
			}
			args = append(args, "--controller", controller)
		}
		var (
			diskstr string
			device string
		)
		device = disk.Device.String()
		diskstr = "device=" + device + ",driver.cache=none" + ",path=" + disk.Path +
			",target.bus=" + ctrl_type

		if (ctrl_type == "virtio") {
			/* && use_iothread, but it is implicit */
			iothread_count += 1
			diskstr += fmt.Sprintf(",driver.iothread=%d", iothread_count)
		} else {
			diskstr += fmt.Sprintf(",address.controller=%d", disk_count[ctrl_type])
		}
		diskstr += ",driver.type=" + disk_driver
		if (disk.Device == openapi.DEVICE_CDROM) {
			diskstr += ",readonly"
		}
		args = append(args, "--disk", diskstr)
		/* controller index per type starts from 0 */
		disk_count[ctrl_type] += 1
	}
	args = append(args, "--iothreads", fmt.Sprintf("%d", iothread_count))

	/* networks */
	for _, net := range o.Vmdef.Nets {
		//iothread_count += 1
		var netstr string = net.Nettype.String()
		if (net.Name != "") {
			netstr += "=" + net.Name
		}
		if (!net.Model.IsValid()) {
			http.Error(w, "vm_create: invalid Net model", http.StatusBadRequest)
			return
		}
		netstr += ",model=" + net.Model.String()
		if (net.Mac != "") {
			if (len(net.Mac) != MAC_LEN) {
				http.Error(w, "vm_create: invalid Mac", http.StatusBadRequest)
				return
			}
			netstr += ",mac=" + net.Mac
		}
		netstr += ",driver.queues=2"
		args = append(args, "--network", netstr)
	}

	/* OTHER COMMUNICATION CHANNELS */
	/* need 15SP5 or higher */
	//args = append(args, "--channel", "qemu-vdagent,source.clipboard.copypaste=on,target.type=virtio")

	/* REMOVE USELESS FEATURES */
	args = append(args, "--memballoon", "none")
	args = append(args, "--audio", "none")

	/* MISC DEVICES */
	args = append(args, "--rng", "/dev/urandom")

	/* Run virt-install and get the Xml */
	var (
		cmd *exec.Cmd
		stdoutput bytes.Buffer
		stderror bytes.Buffer
		uuid string
	)
	cmd = exec.Command("/usr/bin/virt-install", args...)
    cmd.Stdout = &stdoutput
    cmd.Stderr = &stderror

    err = cmd.Run()
	if (err != nil) {
		logger.Log("vm_create: virt-install failed: %s\n%s", err.Error(), stderror.String())
		http.Error(w, "vm_create: failed", http.StatusInternalServerError)
		return
	}
	/* Write the xml to disk, owner RW, group R, others - */
	err = os.WriteFile(virtxml, stdoutput.Bytes(), 0640)
	if (err != nil) {
		logger.Log("vm_create: os.WriteFile failed: %s", err.Error())
		http.Error(w, "vm_create: could not write virtxml", http.StatusInternalServerError)
		return
	}
	uuid, err = hypervisor.Define_domain(libvirt_uri, stdoutput.String())
	if (err != nil) {
		logger.Log("vm_create: hypervisor.Define_domain failed: %s", err.Error())
		http.Error(w, "vm_create: could not define VM", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	err = json.NewEncoder(&buf).Encode(&uuid)
	if (err != nil) {
		http.Error(w, "vm_create: Failed to encode JSON", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
