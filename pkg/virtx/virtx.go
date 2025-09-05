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
package virtx

import (
	"net/http"
	"errors"
	"context"

	g_uuid "github.com/google/uuid"

	"suse.com/virtx/pkg/logger"
	"suse.com/virtx/pkg/httpx"
)

type Service struct {
	servemux *http.ServeMux
	server http.Server
}

var service Service

func Init() {
	/*
	 * configure UUID generation to not use the RandPool.
	 * This is a tradeoff. If we notice that uuid generation is a hot path,
	 * change this to .EnableRandPool()
	 */
	g_uuid.DisableRandPool()

	var servemux *http.ServeMux = http.NewServeMux()
	servemux.HandleFunc("POST /vms", vm_create)
	servemux.HandleFunc("GET /vms", vm_list)
	servemux.HandleFunc("PUT /vms/{uuid}", vm_update)
	servemux.HandleFunc("GET /vms/{uuid}", vm_get)
	servemux.HandleFunc("DELETE /vms/{uuid}", vm_delete)
	servemux.HandleFunc("GET /vms/{uuid}/runstate", vm_runstate_get)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/boot", vm_boot)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/boot", vm_shutdown)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/pause", vm_pause)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/pause", vm_resume)
	servemux.HandleFunc("POST /vms/{uuid}/runstate/migrate", vm_migrate)
	servemux.HandleFunc("GET /vms/{uuid}/runstate/migrate", vm_migrate_get)
	servemux.HandleFunc("DELETE /vms/{uuid}/runstate/migrate", vm_migrate_abort)
	servemux.HandleFunc("PUT /vms/{uuid}/register", vm_register)

	servemux.HandleFunc("GET /hosts", host_list)
	servemux.HandleFunc("GET /hosts/{uuid}", host_get)

	service = Service{
		servemux: servemux,
		server: http.Server{
			Addr: ":8080",
			Handler: servemux,
		},
	}
}

func New_uuid() string {
	var (
		g g_uuid.UUID
		err error
	)
	g, err = g_uuid.NewRandom()
	if (err != nil) {
		logger.Log("g_uuid.NewRandom() failed: %s", err.Error())
		return ""
	}
	return g.String()
}

func Shutdown(ctx context.Context) error {
	var err error
	err = service.server.Shutdown(ctx)
	/* Shutdown the client too (used for proxy) */
	httpx.Shutdown()
	return err
}

func Close() error {
	return service.server.Close()
}

func Start_listening() <-chan error {
	err_ch := make(chan error, 1)
	go func() {
		var err error = service.server.ListenAndServe()
		if (err != nil && !errors.Is(err, http.ErrServerClosed)) {
			err_ch <- err
			return
		}
		close(err_ch)
	}()
	return err_ch
}
