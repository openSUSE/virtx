package comm

import (
	"fmt"

	"github.com/google/uuid"
	"suse.com/virtXD/pkg/hypervisor"
)

const (
	HostInfoEvent  string = "host-info"
	GuestInfoEvent string = "guest-info"
)

func PackHostInfoEvent(hostInfo *hypervisor.HostInfo, newHost int) ([]byte, error) {
	str := fmt.Sprintf(
		"%d %s %s %s %s %s %d",
		hostInfo.Seq, hostInfo.UUID, hostInfo.Hostname,
		hostInfo.Arch, hostInfo.Vendor, hostInfo.Model,
		newHost)
	return []byte(str), nil
}

func UnpackHostInfoEvent(payload []byte) (*hypervisor.HostInfo, int, error) {
	var (
		seq      uint64
		uuidStr  string
		hostname string
		arch     string
		vendor   string
		model    string
		newHost  int
	)
	if _, err := fmt.Sscanf(
		string(payload), "%d %s %s %s %s %s %d",
		&seq, &uuidStr, &hostname,
		&arch, &vendor, &model,
		&newHost,
	); err != nil {
		return nil, 0, err
	}
	return &hypervisor.HostInfo{
		Seq:      seq,
		Hostname: hostname,
		UUID:     uuid.MustParse(uuidStr),
		Arch:     arch,
		Vendor:   vendor,
		Model:    model,
	}, newHost, nil
}

func PackGuestInfoEvent(guestInfo *hypervisor.GuestInfo, hostUUID uuid.UUID) ([]byte, error) {
	str := fmt.Sprintf(
		"%s %d %s %s %d %d %d",
		hostUUID, guestInfo.Seq, guestInfo.UUID, guestInfo.Name,
		guestInfo.State, guestInfo.Memory, guestInfo.NrVirtCpu,
	)
	return []byte(str), nil
}

func UnpackGuestInfoEvent(payload []byte) (*hypervisor.GuestInfo, uuid.UUID, error) {
	var (
		seq         uint64
		hostUuidStr string
		uuidStr     string
		name        string
		state       int
		memory      uint64
		nrVirtCPU   uint
	)
	if _, err := fmt.Sscanf(
		string(payload), "%s %d %s %s %d %d %d",
		&hostUuidStr, &seq, &uuidStr, &name,
		&state, &memory, &nrVirtCPU,
	); err != nil {
		return nil, uuid.Nil, err
	}
	return &hypervisor.GuestInfo{
		Seq:       seq,
		Name:      name,
		UUID:      uuid.MustParse(uuidStr),
		State:     state,
		Memory:    memory,
		NrVirtCpu: nrVirtCPU,
	}, uuid.MustParse(hostUuidStr), nil
}
