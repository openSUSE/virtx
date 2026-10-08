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
package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"suse.com/virtx/pkg/model"
	. "suse.com/virtx/pkg/constants"
)

/* a request without a body is a request with empty options, not an error */
func Test_decode_request_body_without_a_body(t *testing.T) {
	var o openapi.HostListOptions
	r := httptest.NewRequest("GET", "/hosts", nil)
	vr, err := Decode_request_body(r, &o)
	if (err != nil) {
		t.Errorf("Decode_request_body: %v", err)
	}
	if (len(vr.body) != 0) {
		t.Errorf("body has %d bytes, expected 0", len(vr.body))
	}
}

/*
 * a chunked request has no content-length: it must be accepted, and the
 * body must still be bounded.
 */
func Test_decode_request_body_chunked(t *testing.T) {
	var o openapi.VmListOptions
	body := `{"filter":{"name":"x"}}`
	r := httptest.NewRequest("GET", "/vms", struct{ io.Reader }{ strings.NewReader(body) })
	if (r.ContentLength != -1) {
		t.Fatalf("ContentLength is %d, expected -1", r.ContentLength)
	}
	_, err := Decode_request_body(r, &o)
	if (err != nil) {
		t.Errorf("Decode_request_body: %v", err)
	}
	if (o.Filter.Name != "x") {
		t.Errorf("Filter.Name is %q, expected %q", o.Filter.Name, "x")
	}
}

/* a body of exactly the maximum length fits, one byte more does not */
func Test_decode_request_body_at_the_limit(t *testing.T) {
	var s string
	body := `"` + strings.Repeat("a", HTTP_MAX_BODY_LEN - 2) + `"`
	r := httptest.NewRequest("GET", "/hosts", strings.NewReader(body))
	_, err := Decode_request_body(r, &s)
	if (err != nil) {
		t.Errorf("a body of %d bytes was rejected: %v", len(body), err)
	}
	body = `"` + strings.Repeat("a", HTTP_MAX_BODY_LEN - 1) + `"`
	r = httptest.NewRequest("GET", "/hosts", strings.NewReader(body))
	_, err = Decode_request_body(r, &s)
	if (err == nil) {
		t.Errorf("a body of %d bytes was accepted", len(body))
	}
}

/* a response without a body is an error only when a result is expected */
func Test_decode_response_body_without_a_body(t *testing.T) {
	var o openapi.HostList
	r := &http.Response{
		StatusCode: 200,
		Status: "200 OK",
		Header: http.Header{},
		Body: io.NopCloser(strings.NewReader("")),
		ContentLength: 0,
	}
	_, err := Decode_response_body(r, &o)
	if (err == nil) {
		t.Errorf("a 200 response with a result and no body was accepted")
	}
	r = &http.Response{
		StatusCode: 404,
		Status: "404 Not Found",
		Header: http.Header{},
		Body: io.NopCloser(strings.NewReader("")),
		ContentLength: 0,
	}
	vr, err := Decode_response_body(r, &o)
	if (err != nil) {
		t.Errorf("a 404 response without a body: %v", err)
	}
	if (len(vr.Body) != 0) {
		t.Errorf("body has %d bytes, expected 0", len(vr.Body))
	}
}
