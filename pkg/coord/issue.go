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

/*
 * Issuing an execution instruction to the landing host.
 *
 * The carrier is the API the cluster already uses: a POST to the landing host's
 * own /vms/{uuid}/takeover, with the epoch and the mask deadline the instruction
 * was issued under. The design forbids exactly one thing - inventing a "write a
 * file and let the other side poll it" channel - and this is the opposite:
 * the same request a human would send, with the decision layer's two extra
 * facts attached.
 *
 * There is no authentication to carry, because the API has none: it is reachable
 * on the management network only. That is the existing convention, not a new
 * decision, and it is recorded in the plan for this slice as a boundary.
 */
package coord

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"suse.com/virtx/pkg/httpx"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/model"
	"suse.com/virtx/pkg/serfcomm"
)

/* CLIENT_API_PORT is the port virtxd serves the cluster API on. */
const CLIENT_API_PORT = 8080

/*
 * host_of strips the port off a serf membership address: Management_addr gives
 * the address serf itself gossips on, while httpx.Do_request appends the API
 * port itself.
 */
func host_of(addr string) string {
	var (
		i int
	)
	i = strings.LastIndex(addr, ":")
	if (i > 0) {
		return addr[:i]
	}
	return addr
}

/*
 * Issue_takeover asks the landing host to take the VM over, under the epoch and
 * mask deadline given, from the victim host named. It returns the refusal code
 * the landing host reported (empty when the instruction was accepted), so the
 * caller can put it in the audit line without parsing an error string.
 *
 * victim_uuid is the host the VM is being taken from. It travels with the
 * instruction because the landing cannot see the VM in its own view of the
 * cluster: the VM's host is dead, and a dead host publishes nothing, so no live
 * host has it in its inventory at all (that is the "404 unknown uuid" measured
 * on 2026-10-05). The landing resolves the VM from /vms/reg/<victim>/<uuid>
 * instead and checks that the victim is legitimate (pkg/virtx/placement_of).
 *
 * row is the decision table's row this instruction was issued under. It travels
 * with the instruction because it decides whether the landing's lease-side
 * re-checks bind at all (pkg/coord.Check_landing): the landing's lease evidence is
 * "only for the host-is-still-alive path; the Failed row does not do it", and
 * applying it to the Failed row produced the measured 2026-10-06 defect - a
 * running VM refused gate-held (it holds its leases), a powered-off VM accepted
 * and started (its leases are free).
 *
 * attempt is the tier's attempt number of the idempotency key
 * (epoch, vm_uuid, attempt): it travels with the instruction so that the
 * landing host can deduplicate a repeat inside one mask window. It is a
 * parameter and not part of a new request body on purpose - the
 * generated model is not touched.
 *
 * dry_run makes it a drill: the landing moves the registration and moves it
 * straight back, and starts nothing.
 */
func Issue_takeover(landing_uuid string, vm_uuid string, victim_uuid string, row string, epoch int64, mask_deadline_ms int64, attempt int, dry_run bool) (string, error) {
	var (
		addr, host, path, refusal string
		query url.Values
		resp *http.Response
		body []byte
		err error
	)
	addr, err = serfcomm.Management_addr(landing_uuid)
	if (err != nil) {
		return "", fmt.Errorf("no management address for host %s: %w", landing_uuid, err)
	}
	host = host_of(addr)
	path = fmt.Sprintf("/vms/%s/takeover", vm_uuid)
	/*
	 * The instruction's parameters are a query string, and they are built as
	 * one: handing "?epoch=..." in the path escaped the "?" into the path, the
	 * landing host routed it as a path and answered "404 page not found", so
	 * the instruction never arrived (measured 2026-10-05, first instructed
	 * takeover - see httpx.request_url). url.Values also escapes the values
	 * itself, so nothing is quoted by hand here.
	 */
	query = url.Values{}
	query.Set("victim", victim_uuid)
	query.Set("epoch", strconv.FormatInt(epoch, 10))
	query.Set("mask_deadline_ms", strconv.FormatInt(mask_deadline_ms, 10))
	query.Set("attempt", strconv.Itoa(attempt))
	if (row != "") {
		query.Set("row", row)
	}
	if (dry_run) {
		query.Set("dry_run", "1")
	}
	/*
	 * Host is the landing host itself: the receiving virtxd then runs the
	 * request locally instead of proxying it on.
	 */
	resp, err = httpx.Do_request_query(host, "POST", path, query, &openapi.VmRegisterOptions{ Host: landing_uuid })
	if (err != nil) {
		return "", err
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(resp.Body)
	refusal = strings.TrimSpace(string(body))
	/*
	 * Any success status is an acceptance, and the instruction has to be watched
	 * from here: the landing answers 202 when it starts the work, and 204 when
	 * there is nothing left to do because the registration already sits with it -
	 * the second half of a claim that was made before. Judging by "not 202" read
	 * the second as a refusal with an empty reason, the ladder spent its tiers on
	 * it and the take-over stopped with the VM still down (measured 2026-10-15:
	 * refused ... 204 No Content with refusal="" on attempts two and three).
	 */
	if (resp.StatusCode < 200 || resp.StatusCode > 299) {
		logger.Log("coord: landing %s refused %s: %s: %s", landing_uuid, vm_uuid, resp.Status, refusal)
		return refusal, fmt.Errorf("landing %s refused the instruction: %s: %s", landing_uuid, resp.Status, refusal)
	}
	logger.Debug("coord: landing %s accepted %s (epoch %d, mask %d, attempt %d, row %q, dry_run=%v)",
		landing_uuid, vm_uuid, epoch, mask_deadline_ms, attempt, row, dry_run)
	return "", nil
}
