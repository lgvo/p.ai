package control

import "testing"

func TestClientSocketSelection(t *testing.T) {
	for _, test := range []struct {
		name, env, explicit, want string
		invalid                   bool
	}{
		{name: "standard instance", want: "/var/lib/p/control.sock"},
		{name: "environment", env: "/tmp/owner/control.sock", want: "/tmp/owner/control.sock"},
		{name: "explicit wins", env: "/tmp/other.sock", explicit: "/tmp/chosen.sock", want: "/tmp/chosen.sock"},
		{name: "explicit wins over invalid environment", env: "relative.sock", explicit: "/tmp/chosen.sock", want: "/tmp/chosen.sock"},
		{name: "invalid environment does not fall back", env: "relative.sock", invalid: true},
		{name: "invalid explicit does not fall back", env: "/tmp/other.sock", explicit: "relative.sock", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("P_SOCKET", test.env)
			got, err := ClientSocket(test.explicit)
			if test.invalid {
				if err == nil || got != "" {
					t.Fatalf("invalid endpoint selected %q with error %v", got, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("selected %q, %v; want %q", got, err, test.want)
			}
		})
	}
}
