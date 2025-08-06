/*
 * Copyright (c) 2024-2025 SUSE LLC
 *
 * This program is free software; you can redistribute it and/or
 * modify it under the terms of the GNU General Public License
 * as published by the Free Software Foundation; either version 2
 * of the License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, see
 * <https://www.gnu.org/licenses/>
 */
package main

import (
	"unsafe"
	"fmt"
	"os"
	"github.com/spf13/cobra"
)

var cmd = &cobra.Command{
	Use:   "virtx",
	Short: "manage VirtX VMs and Hosts",
	Long:  "manage VirtX VMs and Hosts by connecting to a VIRTX_API_SERVER",
	/*
	 * Uncomment the following line if virtx needs to do something
	 * other than displaying usage when run without arguments:
	 */
	// Run: func(cmd *cobra.Command, args []string) { },
}

func init() {
	cmd.Flags().StringVarP(&virtx.api_server, "api-server", "A", os.Getenv("VIRTX_API_SERVER"), "The VIRTX_API_SERVER to use. Defaults to the env variable.")
	var cmd_list = &cobra.Command{
		Use:   "list",
		Short: "List resources and display them in table format",
	}
	var cmd_list_host = &cobra.Command{
		Use:   "host",
		Short: "List hosts in the cluster",
		Long:  "List all hosts in the cluster, or optionally applying filters (AND)",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "list_host\n")
			os.Exit(0)
		},
	}
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Name, "name", "n", "", "Filter by Host Name")
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Cpuarch.Arch, "arch", "a", "", "Filter by CPU Architecture (x86_64, aarch64)")
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Cpuarch.Vendor, "vendor", "v", "", "Filter by CPU Vendor (Intel, AMD, ...)")
	cmd_list_host.Flags().Int16VarP((*int16)(unsafe.Pointer(&virtx.host_list_options.Filter.Hoststate)), "state", "s", 0, "Filter by Host State")
	cmd_list_host.Flags().Int32VarP(&virtx.host_list_options.Filter.Memoryavailable, "memory", "m", 0, "Filter by Memory Available")
	var cmd_list_vm = &cobra.Command{
		Use:   "vm",
		Short: "List VMs in the cluster",
		Long:  "List all VMs in the cluster, or optionally applying filters (AND)",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "list_vm\n")
			os.Exit(0)
		},
	}
	cmd_list_vm.Flags().StringVarP(&virtx.vm_list_options.Filter.Name, "name", "n", "", "Filter by VM Name")
	cmd_list_vm.Flags().StringVarP(&virtx.vm_list_options.Filter.Host, "host", "h", "", "Filter by Host UUID")
	cmd_list_vm.Flags().Int16VarP((*int16)(unsafe.Pointer(&virtx.vm_list_options.Filter.Runstate)), "state", "s", 0, "Filter by VM Runstate")
	cmd_list_vm.Flags().Int16VarP(&virtx.vm_list_options.Filter.Vlanid,	"vlanid", "v", 0, "Filter by VM Vlanid")
	cmd_list_vm.Flags().StringVarP(&virtx.vm_list_options.Filter.Custom.Name, "custom-name", "N", "", "Filter by VM Custom Field Name")
	cmd_list_vm.Flags().StringVarP(&virtx.vm_list_options.Filter.Custom.Value, "custom-value", "V", "", "Filter by VM Custom Field Value")
	var cmd_get = &cobra.Command{
		Use:   "get",
		Short: "Fetch and display all details about a resource",
	}
	var cmd_get_host = &cobra.Command{
		Use:   "host UUID",
		Short: "Show all details of the host",
		Long:  "Show all details about the specified host, identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "get_host %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_get_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Fetch and show all details of the VM",
		Long:  "Fetch and show all details about the specified VM, identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "get_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_get_runstate = &cobra.Command{
		Use:   "runstate",
		Short: "Show the runstate of the resource",
	}
	var cmd_get_runstate_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Show the runstate of the VM",
		Long:  "Show the runstate of the specified VM, identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "get_runstate_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_get_migrate = &cobra.Command{
		Use:   "migrate",
		Short: "Show the migration status of the resource",
	}
	var cmd_get_migrate_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Show the VM migration status",
		Long:  "Show the migration status of the specified VM, identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "get_migrate_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_create = &cobra.Command{
		Use:   "create",
		Short: "Create a new resource",
	}
	var cmd_create_vm = &cobra.Command{
		Use:   "vm FILENAME",
		Short: "Create a new VM",
		Long:  "Create a new VM from a JSON description in FILENAME",
		Args:  cobra.ExactArgs(1), /* FILENAME */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "create_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_update = &cobra.Command{
		Use:   "update",
		Short: "Update a resource",
	}
	var cmd_update_vm = &cobra.Command{
		Use:   "vm UUID FILENAME",
		Short: "Update a VM",
		Long:  "Update a VM identified by UUID by redefining it from FILENAME. Changes its UUID.",
		Args:  cobra.ExactArgs(2), /* UUID and FILENAME */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "update_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_delete = &cobra.Command{
		Use:   "delete",
		Short: "Delete a resource permanently",
	}
	var cmd_delete_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Delete a VM permanently",
		Long:  "Delete a VM identified by UUID permanently (use with care)",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "delete_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	cmd_delete_vm.Flags().BoolVarP(&virtx.vm_delete_options.Deletestorage, "storage", "s", false, "also delete managed storage")
	var cmd_boot = &cobra.Command{
		Use:   "boot",
		Short: "Startup a runnable resource",
	}
	var cmd_boot_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Start a VM",
		Long:  "Start a VM identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "boot_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_shutdown = &cobra.Command{
		Use:   "shutdown",
		Short: "Shutdown / Poweroff a runnable resource",
	}
	var cmd_shutdown_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Shutdown or Poweroff a VM",
		Long:  "Shutdown a VM guest gracefully with ACPI, or Poweroff with force",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			virtx.vm_shutdown_options.Force = int16(virtx.force)
			fmt.Fprintf(os.Stdout, "shutdown_vm %s force=%d\n", args[0], virtx.force)
			os.Exit(0)
		},
	}
	cmd_shutdown_vm.Flags().CountVarP(&virtx.force, "force", "f", "send the VM process a SIGTERM, or if repeated a SIGKILL")
	var cmd_pause = &cobra.Command{
		Use:   "pause",
		Short: "Pause a runnable resource",
	}
	var cmd_pause_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Pause a VM",
		Long:  "Pause a VM identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "pause_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_resume = &cobra.Command{
		Use:   "resume",
		Short: "Resume a runnable resource",
	}
	var cmd_resume_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Resume a VM",
		Long:  "Resume a VM identified by UUID in paused state",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "resume_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	var cmd_migrate = &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a runnable resource",
	}
	var cmd_migrate_vm = &cobra.Command{
		Use:   "vm UUID [HUUID]",
		Short: "Migrate a VM",
		Long:  "Migrate a VM identified by UUID to an automatically chosen or specified host",
		Args:  cobra.MinimumNArgs(1), /* UUID and optionally HUUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "migrate_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	cmd_migrate_vm.Flags().BoolVarP(&virtx.vm_migrate_options.Live, "live", "l", false, "if true, perform live migration, otherwise offline migration")
	var cmd_abort = &cobra.Command{
		Use:   "abort",
		Short: "Abort an ongoing operation",
	}
	var cmd_abort_migrate = &cobra.Command{
		Use:   "migrate",
		Short: "Abort an ongoing migration",
	}
	var cmd_abort_migrate_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Abort the live migration",
		Long:  "Abort the live migration of the VM identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintf(os.Stdout, "abort_migrate_vm %s\n", args[0])
			os.Exit(0)
		},
	}
	/*
     * XXX ugh. Cobra forces the existence of -h, --help if not overridden explicitly.
	 * This means that it's impossible to use -h for something else.
	 * So as a hack we just replace -h with -?, which overrides the standard entry and
	 * does not conflict with -h, --host.
	 */
	cmd.PersistentFlags().BoolP("help", "?", false, "")
	cmd.AddCommand(cmd_list)
	cmd_list.AddCommand(cmd_list_host)
	cmd_list.AddCommand(cmd_list_vm)
	cmd.AddCommand(cmd_get)
	cmd_get.AddCommand(cmd_get_host)
	cmd_get.AddCommand(cmd_get_vm)
	cmd_get.AddCommand(cmd_get_runstate)
	cmd_get_runstate.AddCommand(cmd_get_runstate_vm)
	cmd_get.AddCommand(cmd_get_migrate)
	cmd_get_migrate.AddCommand(cmd_get_migrate_vm)
	cmd.AddCommand(cmd_create)
	cmd_create.AddCommand(cmd_create_vm)
	cmd.AddCommand(cmd_update)
	cmd_update.AddCommand(cmd_update_vm)
	cmd.AddCommand(cmd_delete)
	cmd_delete.AddCommand(cmd_delete_vm)
	cmd.AddCommand(cmd_boot)
	cmd_boot.AddCommand(cmd_boot_vm)
	cmd.AddCommand(cmd_shutdown)
	cmd_shutdown.AddCommand(cmd_shutdown_vm)
	cmd.AddCommand(cmd_pause)
	cmd_pause.AddCommand(cmd_pause_vm)
	cmd.AddCommand(cmd_resume)
	cmd_resume.AddCommand(cmd_resume_vm)
	cmd.AddCommand(cmd_migrate)
	cmd_migrate.AddCommand(cmd_migrate_vm)
	cmd.AddCommand(cmd_abort)
	cmd_abort.AddCommand(cmd_abort_migrate)
	cmd_abort_migrate.AddCommand(cmd_abort_migrate_vm)
}

func cmd_exec() error {
	var (
		err error
	)
	err = cmd.Execute()
	if (err != nil) {
		return err
	}
	return nil
}
/*
	"version":  version
	"help":     help
	"list":     list
	"get":      get
	"create":   create
	"update":   update
	"delete":   delete
	"boot":     boot
	"shutdown": shutdown
	"pause":    pause
	"resume":   resume
	"migrate":  migrate
	"abort":    abort


var get_object = map[string]func(args ...string) {
	"host": action_get_host
	"vm": action_get_vm
	"migrate": action_get_migrate
	"runstate": action_get_runstate
}


const (
	ACTION_VERSION = iota
	ACTION_HELP
	ACTION_HOST_LIST
	ACTION_HOST_GET
	ACTION_VM_LIST
	ACTION_VM_GET
	ACTION_VM_CREATE
	ACTION_VM_UPDATE
	ACTION_VM_DELETE
	ACTION_VM_RUNSTATE_GET
	ACTION_VM_START
	ACTION_VM_SHUTDOWN
	ACTION_VM_POWEROFF
	ACTION_VM_PAUSE
	ACTION_VM_UNPAUSE
	ACTION_VM_MIGRATE_GET
	ACTION_VM_MIGRATE
	ACTION_VM_MIGRATE_CANCEL
    )
    func usage() {
	fmt.Println('
Usage: virtx ACTION ...

ACTIONs:

version             show the virtx version and exit
help                show this help text and exit

list host           list all hosts in the cluster (optionally with filters)
get host            show host details

list vm             list all VMs (optionally with filters)
get vm              retrieve VM details and show them

create vm           create a new VM from a json definition
update vm           update a VM in shutdown state with a new json definition
delete vm           remove completely a VM that is in shutdown state

get runstate vm     get the runstate of a VM
boot vm             boot (start) a VM
shutdown vm         shutdown a VM with various levels of force
pause vm            temporarily pause all VCPUs, stopping the VM
unpause vm          resume from a pause
get migrate vm      get the progress state of a VM migration process
migrate vm          start live migrating a VM to another host
abort migrate vm    interrupt the VM migration process
'

*/

