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
	str := fmt.Sprintf("%s %s %d", hostInfo.UUID, hostInfo.Hostname, newHost)
	return []byte(str), nil
}

func UnpackHostInfoEvent(payload []byte) (*hypervisor.HostInfo, int, error) {
	var uuidStr string
	var hostname string
	var newHost int
	if _, err := fmt.Sscanf(string(payload), "%s %s %d", &uuidStr, &hostname, &newHost); err != nil {
		return nil, 0, err
	}
	return &hypervisor.HostInfo{
		Hostname: hostname,
		UUID:     uuid.MustParse(uuidStr),
	}, newHost, nil
}

func PackGuestInfoEvent(guestInfo *hypervisor.GuestInfo, hostUUID uuid.UUID) ([]byte, error) {
	str := fmt.Sprintf("%s %s %s %d", hostUUID, guestInfo.UUID, guestInfo.Name, guestInfo.State)
	return []byte(str), nil
}

func UnpackGuestInfoEvent(payload []byte) (*hypervisor.GuestInfo, uuid.UUID, error) {
	var hostUuidStr string
	var uuidStr string
	var name string
	var state int
	if _, err := fmt.Sscanf(string(payload), "%s %s %s %d", &hostUuidStr, &uuidStr, &name, &state); err != nil {
		return nil, uuid.Nil, err
	}
	return &hypervisor.GuestInfo{
		Name:  name,
		UUID:  uuid.MustParse(uuidStr),
		State: state,
	}, uuid.MustParse(hostUuidStr), nil
}
