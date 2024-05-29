package comm

import (
	"fmt"

	"github.com/google/uuid"

	"suse.com/inventory-service/pkg/hypervisor"
)

const (
	HostInfoEvent  string = "host-info"
	GuestInfoEvent string = "guest-info"
)

func PackHostInfoEvent(hostInfo *hypervisor.HostInfo, newHost int) ([]byte, error) {
	str := fmt.Sprintf(
		"%s %s %s %s %s %d",
		hostInfo.UUID, hostInfo.Hostname,
		hostInfo.Arch, hostInfo.Vendor, hostInfo.Model,
		newHost)
	return []byte(str), nil
}

func UnpackHostInfoEvent(payload []byte) (*hypervisor.HostInfo, int, error) {
	var (
		uuidStr  string
		hostname string
		arch     string
		vendor   string
		model    string
		newHost  int
	)
	if _, err := fmt.Sscanf(
		string(payload), "%s %s %s %s %s %d",
		&uuidStr, &hostname,
		&arch, &vendor, &model,
		&newHost,
	); err != nil {
		return nil, 0, err
	}
	return &hypervisor.HostInfo{
		Hostname: hostname,
		UUID:     uuid.MustParse(uuidStr),
		Arch:     arch,
		Vendor:   vendor,
		Model:    model,
	}, newHost, nil
}

func PackGuestInfoEvent(guestInfo *hypervisor.GuestInfo, hostUUID uuid.UUID) ([]byte, error) {
	str := fmt.Sprintf(
		"%s %s %s %d %d %d",
		hostUUID, guestInfo.UUID, guestInfo.Name,
		guestInfo.State, guestInfo.Memory, guestInfo.NrVirtCpu,
	)
	return []byte(str), nil
}

func UnpackGuestInfoEvent(payload []byte) (*hypervisor.GuestInfo, uuid.UUID, error) {
	var (
		hostUuidStr string
		uuidStr     string
		name        string
		state       int
		memory      uint64
		nrVirtCPU   uint
	)
	if _, err := fmt.Sscanf(
		string(payload), "%s %s %s %d %d %d",
		&hostUuidStr, &uuidStr, &name,
		&state, &memory, &nrVirtCPU,
	); err != nil {
		return nil, uuid.Nil, err
	}
	return &hypervisor.GuestInfo{
		Name:      name,
		UUID:      uuid.MustParse(uuidStr),
		State:     state,
		Memory:    memory,
		NrVirtCpu: nrVirtCPU,
	}, uuid.MustParse(hostUuidStr), nil
}
