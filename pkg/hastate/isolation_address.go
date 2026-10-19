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
 * <https://www.gnu.org/licenses/>.
 */
package hastate

import (
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"suse.com/virtx/pkg/inventory"
	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/machine"
	"suse.com/virtx/pkg/policy"
)

/*
 * judge_addresses is what this host pings to tell "my own network is broken"
 * apart from "that peer is unreachable": the addresses the cluster policy
 * configures, or, when it configures none, one address derived from this host's
 * own network configuration.
 *
 * The derived address matters because a cluster that has no policy yet must
 * still be able to tell those two apart, and because an operator who does not
 * care about the distinction should not have to write it down.
 */
func judge_addresses(p *policy.Policy) []string {
	var (
		addr string
		err error
	)
	if (p != nil && len(p.Isolation_addrs) != 0) {
		/* the policy configures them: there is nothing derived to report */
		derived_note = ""
		return append([]string(nil), p.Isolation_addrs...)
	}
	addr, err = derived_judge_address()
	if (err != nil) {
		note_derived("no judge address: " + err.Error())
		return nil
	}
	note_derived(fmt.Sprintf("%s configures no judge address, using the gateway of the management network: %s",
		policy.PATH, addr))
	return []string{ addr }
}

/*
 * derived_note is what was last said about the derived judge address. It is
 * only touched by the observation goroutine, and it is what keeps a state that
 * does not change from being announced every round.
 */
var derived_note string

/* note_derived reports the derived address, or why there is none, once. */
func note_derived(msg string) {
	if (msg == derived_note) {
		return
	}
	derived_note = msg
	logger.Log("hastate: %s", msg)
}

/*
 * derived_judge_address is the address a cluster without a policy uses: the
 * gateway of the interface that carries this host's management address.
 *
 * A gateway is not a cluster member, so a host that has been partitioned off
 * from its peers still reaches it as long as its own network is intact - which
 * is exactly the distinction the judge is there to make. The management address
 * is our own serf member address, which the inventory keeps.
 */
func derived_judge_address() (string, error) {
	var (
		hd inventory.Hostdata
		err error
		addr string
	)
	hd, err = inventory.Get_hostdata(machine.Uuid())
	if (err != nil) {
		return "", fmt.Errorf("this host has no management address yet: %s", err.Error())
	}
	addr = strings.TrimSpace(hd.Man_ip)
	if (addr == "") {
		return "", fmt.Errorf("this host has no management address yet")
	}
	return default_gateway(addr)
}

/* default_gateway is the gateway of the interface that owns one address. */
func default_gateway(addr string) (string, error) {
	var (
		iface string
		err error
	)
	iface, err = interface_of(addr)
	if (err != nil) {
		return "", err
	}
	return route_gateway(iface)
}

/* interface_of is the local interface that owns one IPv4 address. */
func interface_of(addr string) (string, error) {
	var (
		ip net.IP = net.ParseIP(addr)
		ifaces []net.Interface
		addrs []net.Addr
		n *net.IPNet
		ok bool
		err error
	)
	if (ip == nil) {
		return "", fmt.Errorf("%q is not an IP address", addr)
	}
	ifaces, err = net.Interfaces()
	if (err != nil) {
		return "", err
	}
	for _, i := range ifaces {
		addrs, err = i.Addrs()
		if (err != nil) {
			continue
		}
		for _, a := range addrs {
			n, ok = a.(*net.IPNet)
			if (!ok) {
				continue
			}
			if (n.IP.Equal(ip)) {
				return i.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no local interface carries %s", addr)
}

/*
 * route_gateway is the default gateway of one interface, read from the routing
 * table the kernel exports as /proc/net/route: the destination of a default
 * route is all zeroes, and its gateway is the address in network byte order,
 * printed as hexadecimal.
 */
func route_gateway(iface string) (string, error) {
	var (
		data []byte
		err error
		fields []string
	)
	data, err = os.ReadFile("/proc/net/route")
	if (err != nil) {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields = strings.Fields(line)
		if (len(fields) < 3) {
			continue
		}
		if (fields[0] != iface || fields[1] != "00000000") {
			continue
		}
		return hex_gateway(fields[2])
	}
	return "", fmt.Errorf("%s has no default route", iface)
}

/* hex_gateway turns the hexadecimal gateway of /proc/net/route into a string. */
func hex_gateway(hex string) (string, error) {
	var (
		n uint64
		b [4]byte
		err error
	)
	n, err = strconv.ParseUint(hex, 16, 32)
	if (err != nil) {
		return "", fmt.Errorf("%q is not a gateway address", hex)
	}
	binary.LittleEndian.PutUint32(b[:], uint32(n))
	return net.IPv4(b[0], b[1], b[2], b[3]).String(), nil
}
