package incus

import (
	"context"
	"errors"
	"time"
)

// guestSystemdSocket is created by the guest's systemd after it has set up
// its signal handling, on the /run tmpfs of the current boot.
const guestSystemdSocket = "/run/systemd/private"

// awaitGuestSystemd waits, at most 30 s, until a running guest's systemd is
// up, so that a graceful stop sent next reaches it. Incus sends a graceful
// stop once: as SIGRTMIN+3 when the guest's init blocks that signal, as
// SIGPWR otherwise. Until systemd has set up its signals, the init of P's
// NixOS image is the stage-2 shell script or early systemd, which lose that
// request, and Incus gives up after its timeout. A wait that ends without
// systemd returns an error for the caller to report if its stop then fails.
func (b *Backend) awaitGuestSystemd(ctx context.Context, instance string) error {
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	api := &unixFileAPI{socket: b.config.UserSocket, project: b.config.Project, instance: instance}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, up, err := api.head(wait, guestSystemdSocket, 0)
		if err == nil && up {
			return nil
		}
		select {
		case <-wait.Done():
			return errors.Join(errors.New("guest systemd not up before the stop"), err)
		case <-ticker.C:
		}
	}
}
