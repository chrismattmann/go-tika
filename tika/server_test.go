/*
Copyright 2017 Google Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package tika

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

func init() {
	// Overwrite the cmder to inject a dummy command. We simulate starting a server
	// by running the TestHelperProcess.
	cmder = func(context.Context, string, ...string) *exec.Cmd {
		c := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "sleep", "2")
		c.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
		return c
	}
}

func TestNewServerError(t *testing.T) {
	path, err := os.Executable() // Use the text executable path as a dummy jar.
	if err != nil {
		t.Skip("cannot find current test executable")
	}
	tests := []struct {
		name    string
		jar     string
		options []Option
	}{
		{name: "no jar path"},
		{
			name: "invalid jar path",
			jar:  "/invalid/jar/path.jar",
		},
		{
			name:    "invalid hostname",
			jar:     path,
			options: []Option{WithHostname("192.168.0.%31")},
		},
	}
	for _, test := range tests {
		if _, err := NewServer(test.jar, test.options...); err == nil {
			t.Errorf("NewServer(%s) got no error", test.name)
		}
	}
}

func TestStart(t *testing.T) {
	path, err := os.Executable() // Use the text executable path as a dummy jar.
	if err != nil {
		t.Skip("cannot find current test executable")
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "1.14")
	}))
	defer ts.Close()
	tsURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("error creating test server: %v", err)
	}
	tests := []struct {
		name    string
		options []Option
	}{
		{
			name: "basic config",
			options: []Option{
				WithHostname(tsURL.Hostname()),
				WithPort(tsURL.Port()),
			},
		},
	}
	for _, test := range tests {
		s, err := NewServer(path, test.options...)
		if err != nil {
			t.Errorf("NewServer(%s) got error: %v", test.name, err)
			continue
		}
		cancel, err := s.Start(context.Background())
		if err != nil {
			t.Errorf("Start(%s) got error: %v", test.name, err)
		}
		cancel()
	}
}

func bouncyServer(bounce int) *httptest.Server {
	bounced := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if bounced < bounce {
			bounced++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, "1.14")
	}))

}

func TestStartError(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Skip("cannot find current test executable")
	}
	ts := bouncyServer(4)
	defer ts.Close()
	tsURL, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("error creating test server: %v", err)
	}
	tests := []struct {
		name    string
		jar     string
		options []Option
	}{
		{
			name: "unresponsive server",
			jar:  path,
			options: []Option{
				WithHostname(tsURL.Hostname()),
				WithPort(tsURL.Port()),
				WithStartupTimeout(2 * time.Second),
			},
		},
	}
	for _, test := range tests {
		s, err := NewServer(test.jar, test.options...)
		if err != nil {
			t.Errorf("NewServer(%s) got error: %v", test.name, err)
			continue
		}
		if cancel, err := s.Start(context.Background()); err == nil {
			t.Errorf("s.Start(%s) got no error, want error", test.name)
			cancel()
		}
	}
}

func TestURL(t *testing.T) {
	tests := []string{"", "test"}
	for _, test := range tests {
		s := &Server{url: test}
		if got := s.URL(); got != test {
			t.Errorf("URL() = %q, want %q", got, test)
		}
	}
}

func TestWaitForStart(t *testing.T) {
	tests := []struct {
		name        string
		reqToBounce int
		wantError   bool
		timeout     time.Duration
	}{
		{"not bounced", 0, false, 5 * time.Second},
		{"bounced twice", 2, false, 5 * time.Second},
		{"bounced for too long", 4, true, 2 * time.Second},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ts := bouncyServer(test.reqToBounce)
			defer ts.Close()
			s := &Server{url: ts.URL, startupTimeout: test.timeout}
			got := s.waitForStart(context.Background())
			if test.wantError && got == nil {
				t.Errorf("waitForStart(%s) got no error, want error", test.name)
			}
			if test.wantError {
				ts.Close()
				return
			}
			if got != nil {
				t.Errorf("waitForStart(%s) got %v, want no error", test.name, got)
			}
		})
	}
}

// TestHelperProcess isn't a real test. It's used as a helper process
// for TestParameterRun.
// Adapted from os/exec/exec_test.go.
func TestHelperProcess(*testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if args[0] == "sleep" {
		l, err := strconv.Atoi(args[1])
		if err != nil {
			os.Exit(1)
		}
		time.Sleep(time.Duration(l) * time.Second)
	}
}

func TestValidateFileMD5(t *testing.T) {
	path, err := os.Executable()
	if err != nil {
		t.Skip("cannot find current test executable")
	}

	tests := []struct {
		path      string
		md5String string
		want      bool
	}{
		{
			"path_to_non_existent_file",
			"does not match",
			false,
		},
		{
			path,
			"does not match",
			false,
		},
	}
	for _, test := range tests {
		if got := validateFileMD5(test.path, test.md5String); got != test.want {
			t.Errorf("validateFileMD5(%q, %q) = %t, want %t", test.path, test.md5String, got, test.want)
		}
	}
}

func TestDownloadServerError(t *testing.T) {
	tests := []struct {
		version Version
		path    string
	}{
		{"1.0", ""},
	}
	for _, test := range tests {
		if err := DownloadServer(context.Background(), test.version, test.path); err == nil {
			t.Errorf("DownloadServer(%q, %q) got no error, want an error", test.version, test.path)
		}
	}
}
