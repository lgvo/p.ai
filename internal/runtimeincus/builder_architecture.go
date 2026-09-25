package runtimeincus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

// builderHostArchitecture reads only the Incus server's kernel architecture
// through the already selected local Unix socket. The CLI `query` command in
// pinned Incus cannot be combined with --project, so this one bounded GET is
// explicitly project-qualified and exposes no general query surface.
func (b *Backend) builderHostArchitecture(ctx context.Context) (string, error) {
	var zero string
	transport := &http.Transport{DialContext: func(call context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(call, "unix", b.config.UserSocket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("Incus server architecture redirect refused")
	}}
	u := url.URL{Scheme: "http", Host: "incus", Path: "/1.0"}
	query := u.Query()
	query.Set("project", b.config.Project)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return zero, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return zero, errors.New("Incus server architecture response refused")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10+1))
	if err != nil || len(raw) > 256<<10 {
		return zero, errors.Join(err, errors.New("Incus server architecture response exceeded bound"))
	}
	var data struct {
		Type       string `json:"type"`
		StatusCode int    `json:"status_code"`
		Metadata   struct {
			Environment struct {
				KernelArchitecture string `json:"kernel_architecture"`
			} `json:"environment"`
		} `json:"metadata"`
	}
	if json.Unmarshal(raw, &data) != nil || data.Type != "sync" || data.StatusCode != http.StatusOK ||
		(data.Metadata.Environment.KernelArchitecture != "x86_64" && data.Metadata.Environment.KernelArchitecture != "aarch64") {
		return zero, errors.New("Incus server kernel architecture unavailable")
	}
	return data.Metadata.Environment.KernelArchitecture, nil
}
