/*
 * Copyright (c) 2026 SUSE LLC
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
package virtx

import (
	"net/http"
	"os"
	"slices"
	"sync"

	"suse.com/virtx/pkg/hypervisor"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/reg"
)

/*
 * The def lock: per-VM lock on the definition of the VMs owned by this host, and on the state
 * that must stay consistent with it (domain, reg dir, storage).
 * An operation takes it iff it modifies or depends on the VM definition (domain, reg, storage)
 * in multiple steps, which libvirt does not serialize: create, update, delete, boot, migrate,
 * register, unregister.
 * Single libvirt calls on the domain (pause, resume, shutdown) are serialized by libvirt,
 * and reads need no lock.
 * The runstate must be checked with libvirt after acquiring the lock, the inventory can be stale.
 * For the same reason, ownership is checked with reg (the authority on ownership) after acquiring
 * the lock, except for create (which makes the registration), register and unregister.
 * Migrate holds the lock of the source host until reg.Move completes, and the destination host
 * fails the ownership check until then: register is a repair op, and must not be run on a VM
 * being migrated.
 */
var def_lock sync.Map /* uuid -> openapi.OperationCode of the operation holding the def lock */

/*
 * acquire the def lock of the VM for op, check that reg registers the VM on this host
 * (except for create, register, unregister), then check that the libvirt runstate is one of
 * states (no states: no check). On failure respond with an error and return false:
 * 409 Conflict naming the operation holding the lock, 409 if the VM is not registered on this
 * host, or 422 if the runstate is not allowed.
 */
func def_lock_acquire(w http.ResponseWriter, uuid string, op openapi.OperationCode, states ...openapi.Vmrunstate) bool {
	cur, held := def_lock.LoadOrStore(uuid, op)
	if (held) {
		http.Error(w, "VM busy: " + cur.(openapi.OperationCode).String() + " in progress", http.StatusConflict)
		return false
	}
	switch (op) {
	case openapi.OpVmCreate, openapi.OpVmRegister, openapi.OpVmUnregister:
		/* create makes the registration, register and unregister are repair ops */
	default:
		/* reg is the authority on ownership, the inventory can route to a non-owner during migration */
		err := reg.Access(machine.Uuid(), uuid)
		if (err != nil) {
			def_lock_release(uuid)
			if (os.IsNotExist(err)) {
				http.Error(w, "VM is not registered on this host", http.StatusConflict)
			} else {
				logger.Log("reg.Access failed: %s", err.Error())
				http.Error(w, "could not check VM registration", http.StatusInternalServerError)
			}
			return false
		}
	}
	if (len(states) == 0) {
		return true
	}
	state, err := hypervisor.Get_runstate(uuid)
	if (err != nil) {
		def_lock_release(uuid)
		logger.Log("hypervisor.Get_runstate failed: %s", err.Error())
		http.Error(w, "could not get VM runstate", http.StatusFailedDependency)
		return false
	}
	if (!slices.Contains(states, state)) {
		def_lock_release(uuid)
		http.Error(w, "VM runstate is " + state.String(), http.StatusUnprocessableEntity)
		return false
	}
	return true
}

func def_lock_release(uuid string) {
	def_lock.Delete(uuid)
}
