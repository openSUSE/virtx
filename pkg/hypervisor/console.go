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
package hypervisor

import (
	"encoding/xml"
	"errors"
	"io"

	"libvirt.org/go/libvirt"

	"suse.com/virtx/pkg/logger"
)

type serial_stream struct {
	stream *libvirt.Stream
	conn   *libvirt.Connect
}

func (s serial_stream) Read(b []byte) (int, error)  { return s.stream.Recv(b) }
func (s serial_stream) Write(b []byte) (int, error) { return s.stream.Send(b) }
func (s serial_stream) Close() error {
	var err error
	s.stream.Finish()
	s.stream.Free()
	_, err = s.conn.Close()
	return err
}

type xmlGraphics struct {
	Type string `xml:"type,attr"`
	Port int `xml:"port,attr"`
}

type xmlDevicesGraphics struct {
	Graphics []xmlGraphics `xml:"graphics"`
}

type xmlDomainGraphics struct {
	Devices xmlDevicesGraphics `xml:"devices"`
}

func Get_vnc_port(uuid string) (int, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		xmlstr string
		xmldata xmlDomainGraphics
		grp xmlGraphics
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return 0, err
	}
	defer conn.Close()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return 0, err
	}
	defer domain.Free()
	xmlstr, err = domain.GetXMLDesc(0)
	if (err != nil) {
		return 0, err
	}
	err = xml.Unmarshal([]byte(xmlstr), &xmldata)
	if (err != nil) {
		return 0, err
	}
	for _, grp = range xmldata.Devices.Graphics {
		if (grp.Type == "vnc" && grp.Port > 0) {
			logger.Debug("Get_vnc_port: %s port %d", uuid, grp.Port)
			return grp.Port, nil
		}
	}
	return 0, errors.New("VNC port not found")
}

func Open_serial(uuid string) (io.ReadWriteCloser, error) {
	var (
		err error
		conn *libvirt.Connect
		domain *libvirt.Domain
		stream *libvirt.Stream
	)
	conn, err = libvirt.NewConnect(LIBVIRT_URI)
	if (err != nil) {
		return nil, err
	}
	defer func() {
		if (err != nil) {
			conn.Close()
		}
	}()
	domain, err = conn.LookupDomainByUUIDString(uuid)
	if (err != nil) {
		return nil, err
	}
	defer domain.Free()
	stream, err = conn.NewStream(0)
	if (err != nil) {
		return nil, err
	}
	err = domain.OpenConsole("", stream, 0)
	if (err != nil) {
		stream.Free()
		return nil, err
	}
	return serial_stream{stream, conn}, nil
}
