package incus

import (
	"encoding/json"
	"testing"
)

func TestParseDriverConfigReadsTheThreeIncusSettings(t *testing.T) {
	raw := json.RawMessage(`{"binary":"/run/current-system/sw/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"user-1000"}`)
	got, err := ParseDriverConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := DriverConfig{Binary: "/run/current-system/sw/bin/incus", UserSocket: "/var/lib/incus/unix.socket.user", Project: "user-1000"}
	if got != want {
		t.Fatalf("settings %+v, want %+v", got, want)
	}
}

func TestParseDriverConfigRefusesAnythingButTheExactSettings(t *testing.T) {
	for name, raw := range map[string]string{
		"not an object":       `["/bin/incus"]`,
		"null":                `null`,
		"unknown field":       `{"binary":"/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p","remote":"https://host"}`,
		"inexact field name":  `{"Binary":"/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p"}`,
		"duplicate field":     `{"binary":"/bin/incus","binary":"/tmp/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p"}`,
		"trailing data":       `{"binary":"/bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p"} {}`,
		"missing binary":      `{"user_socket":"/var/lib/incus/unix.socket.user","project":"p"}`,
		"missing user socket": `{"binary":"/bin/incus","project":"p"}`,
		"missing project":     `{"binary":"/bin/incus","user_socket":"/var/lib/incus/unix.socket.user"}`,
		"relative binary":     `{"binary":"bin/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p"}`,
		"unclean binary":      `{"binary":"/bin/../tmp/incus","user_socket":"/var/lib/incus/unix.socket.user","project":"p"}`,
		"relative socket":     `{"binary":"/bin/incus","user_socket":"unix.socket.user","project":"p"}`,
		"unclean socket":      `{"binary":"/bin/incus","user_socket":"/var/lib/incus//unix.socket.user","project":"p"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := ParseDriverConfig(json.RawMessage(raw)); err == nil {
				t.Fatalf("settings accepted: %+v", got)
			}
		})
	}
}
