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

	"suse.com/virtx/pkg/model"

	"github.com/spf13/cobra"
)

var cmd = &cobra.Command{
	Use:   "virtx",
	Short: "manage VirtX VMs and Hosts",
	Long:  "manage VirtX VMs and Hosts by connecting to a VIRTX_API_SERVER",
}

func init() {
	cmd.PersistentFlags().StringVarP(&virtx.api_server, "api-server", "A", os.Getenv("VIRTX_API_SERVER"), "The VIRTX_API_SERVER to use. Defaults to the env variable.")
	cmd.PersistentFlags().BoolVarP(&virtx.debug, "debug", "D", false, "produce more verbose debug output")
	var cmd_list = &cobra.Command{
		Use:   "list",
		Short: "List resources and display them in table format",
	}
	var cmd_list_host = &cobra.Command{
		Use:   "host",
		Short: "List hosts in the cluster",
		Long:  "List all hosts in the cluster, or optionally applying filters (AND)",
		Run: func(cmd *cobra.Command, args []string) {
			if (virtx.ok) {
				if (virtx.result != nil) {
					host_list(virtx.result.(*openapi.HostList))
				}
			} else {
				host_list_req()
			}
		},
	}
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Name, "name", "n", "", "Filter by Host Name")
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Cpuarch.Arch, "arch", "a", "", "Filter by CPU Architecture (x86_64, aarch64)")
	cmd_list_host.Flags().StringVarP(&virtx.host_list_options.Filter.Cpuarch.Vendor, "vendor", "v", "", "Filter by CPU Vendor (Intel, AMD, ...)")
	cmd_list_host.Flags().Int16VarP((*int16)(unsafe.Pointer(&virtx.host_list_options.Filter.Hoststate)), "state", "s", 0, "Filter by Host State")
	cmd_list_host.Flags().Int32VarP(&virtx.host_list_options.Filter.Memoryavailable, "memory", "m", 0, "Filter by available normal memory")
	cmd_list_host.Flags().Int32VarP(&virtx.host_list_options.Filter.Hpavailable, "hp", "H", 0, "Filter by available HugePages memory")

	var cmd_list_vm = &cobra.Command{
		Use:   "vm",
		Short: "List VMs in the cluster",
		Long:  "List all VMs in the cluster, or optionally applying filters (AND)",
		Run: func(cmd *cobra.Command, args []string) {
			if (virtx.ok) {
				if (virtx.result != nil) {
					vm_list(virtx.result.(*openapi.VmList))
				}
			} else {
				vm_list_req()
			}
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
			if (virtx.ok) {
				if (virtx.result != nil) {
					host_get(virtx.result.(*openapi.Host))
				}
			} else {
				host_get_req(args[0])
			}
		},
	}
	cmd_get_host.Flags().BoolVarP(&virtx.stat_cpu, "stat-cpu", "C", false, "Show cpu statistics")
	cmd_get_host.Flags().BoolVarP(&virtx.stat_mem, "stat-mem", "M", false, "Show memory statistics")
	var cmd_get_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Fetch and show all details of the VM",
		Long:  "Fetch and show all details about the specified VM, identified by UUID",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			if (virtx.ok) {
				if (virtx.result != nil) {
					vm_get(virtx.result.(*openapi.Vm))
				}
			} else {
				vm_get_req(args[0])
			}
		},
	}
	cmd_get_vm.Flags().BoolVarP(&virtx.disk, "disk", "k", false, "Show VM disks")
	cmd_get_vm.Flags().BoolVarP(&virtx.net, "net", "n", false, "Show VM networks")
	cmd_get_vm.Flags().BoolVarP(&virtx.stat_disk, "stat-disk", "K", false, "Show disk statistics")
	cmd_get_vm.Flags().BoolVarP(&virtx.stat_net, "stat-net", "N", false, "Show network statistics")
	cmd_get_vm.Flags().BoolVarP(&virtx.stat_cpu, "stat-cpu", "C", false, "Show cpu statistics")
	cmd_get_vm.Flags().BoolVarP(&virtx.stat_mem, "stat-mem", "M", false, "Show memory statistics")

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
			if (virtx.result != nil) {
				vm_runstate_get(virtx.result.(*openapi.Vmruninfo))
			} else {
				vm_runstate_get_req(args[0])
			}
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
			if (virtx.ok) {
				if (virtx.result != nil) {
					vm_migrate_get(virtx.result.(*openapi.MigrationInfo))
				}
			} else {
				vm_migrate_get_req(args[0])
			}
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
			if (virtx.ok) {
				if (virtx.result != nil) {
					vm_create(virtx.result.(*string))
				}
			} else {
				vm_create_req(args[0])
			}
		},
	}
	cmd_create_vm.Flags().StringVarP(&virtx.vm_create_options.Host, "host", "h", "", "Create VM on the specified host")
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
			if (virtx.ok) {
				vm_update()
			} else {
				vm_update_req(args[0], args[1])
			}
		},
	}
	cmd_update_vm.Flags().BoolVarP(&virtx.vm_update_options.Deletestorage, "storage", "s", false, "Delete unused storage (NOT IMPLEMENTED)")

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
			if (virtx.ok) {
				vm_delete()
			} else {
				vm_delete_req(args[0])
			}
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
			if (virtx.ok) {
				vm_boot()
			} else {
				vm_boot_req(args[0])
			}
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
			if (virtx.ok) {
				vm_shutdown()
			} else {
				vm_shutdown_req(args[0])
			}
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
			if (virtx.ok) {
				vm_pause()
			} else {
				virtx.path = fmt.Sprintf("/vms/%s/runstate/pause", args[0])
				virtx.method = "POST"
				virtx.arg = nil
				virtx.result = nil
			}
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
			if (virtx.ok) {
				vm_resume()
			} else {
				virtx.path = fmt.Sprintf("/vms/%s/runstate/pause", args[0])
				virtx.method = "DELETE"
				virtx.arg = nil
				virtx.result = nil
			}
		},
	}
	var cmd_migrate = &cobra.Command{
		Use:   "migrate",
		Short: "Migrate a runnable resource",
	}
	var cmd_migrate_vm = &cobra.Command{
		Use:   "vm UUID",
		Short: "Migrate a VM",
		Long:  "Migrate a VM identified by UUID",
		Args:  cobra.MinimumNArgs(1), /* UUID and optionally HUUID */
		Run: func(cmd *cobra.Command, args []string) {
			if (virtx.ok) {
				vm_migrate()
			} else {
				if (virtx.live) {
					virtx.vm_migrate_options.MigrationType = openapi.MIGRATION_LIVE
				} else {
					virtx.vm_migrate_options.MigrationType = openapi.MIGRATION_COLD
				}
				virtx.path = fmt.Sprintf("/vms/%s/runstate/migrate", args[0])
				virtx.method = "POST"
				virtx.arg = &virtx.vm_migrate_options
				virtx.result = nil
			}
		},
	}
	cmd_migrate_vm.Flags().BoolVarP(&virtx.live, "live", "l", false, "if true, perform live migration")
	cmd_migrate_vm.Flags().StringVarP(&virtx.vm_migrate_options.Host, "host", "h", "", "a specific host to migrate to")
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
			if (virtx.ok) {
				vm_migrate_abort()
			} else {
				virtx.path = fmt.Sprintf("/vms/%s/runstate/migrate", args[0])
				virtx.method = "DELETE"
				virtx.arg = nil
				virtx.result = nil
			}
		},
	}
	var cmd_register = &cobra.Command{
		Use:   "register",
		Short: "Register a resource",
	}
	var cmd_register_vm = &cobra.Command{
		Use:   "vm --host HOST_UUID VM_UUID",
		Short: "Register a VM",
		Long:  "Register a VM identified by host and VM UUIDs if it desynced between virtx and libvirt",
		Args:  cobra.ExactArgs(1), /* UUID */
		Run: func(cmd *cobra.Command, args []string) {
			if (virtx.ok) {
				vm_register()
			} else {
				virtx.path = fmt.Sprintf("/vms/%s/register", args[0])
				virtx.method = "PUT"
				virtx.arg = &virtx.vm_register_options
				virtx.result = nil
			}
		},
	}
	cmd_register_vm.Flags().StringVarP(&virtx.vm_register_options.Host, "host", "h", "", "Register VM on the specified host")
	cmd_register_vm.MarkFlagRequired("host")

	/* XXX ugh. Cobra forces the existence of -h, --help if not overridden explicitly.
	 * This means that it's impossible to use -h for something else.
	 * So as a hack we just replace -h with -?, which overrides the standard entry and
	 * does not conflict with -h, --host.
	 */
	cobra.EnableCommandSorting = false
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
	cmd.AddCommand(cmd_register)
	cmd_register.AddCommand(cmd_register_vm)
}

func cmd_exec() error {
	var (
		err error
	)
	cmd.SilenceErrors = true
	err = cmd.Execute()
	if (err != nil) {
		return err
	}
	return nil
}
