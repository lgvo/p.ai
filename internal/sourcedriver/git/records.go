package git

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// A Git service runs in a process group of its own, which outlives a driver
// that dies abruptly. So that a driver started after such a death can prove
// what Settle reports, each service keeps a record in the storage while it
// runs: its ID, the driver that runs it, and the leader of its process
// group with the boot and the leader's start time, which tell the recorded
// process from a later one that reuses its process ID.

// serviceRecord is the durable record of one running service.
type serviceRecord struct {
	Service string `json:"service"`
	Owner   string `json:"owner"`  // the Driver that runs it
	Leader  int    `json:"leader"` // the process ID of Git, which leads its group
	Boot    string `json:"boot"`   // the kernel's boot ID when the leader started
	Start   uint64 `json:"start"`  // the leader's start time, in clock ticks since that boot

	path string
}

// recordsDirectory holds the records of running services.
func (d *Driver) recordsDirectory() string { return filepath.Join(d.storage, "git-services") }

// recordService records a service whose Git leads process group leader,
// before any client input reaches it, and returns the function that removes
// the record once Git has exited.
func (d *Driver) recordService(id string, leader int) (remove func(), err error) {
	boot, err := bootID()
	if err != nil {
		return nil, err
	}
	start, alive, err := leaderStart(leader)
	if err != nil {
		return nil, err
	}
	if !alive {
		return nil, errors.New("git exited before its service was recorded")
	}
	dir := d.recordsDirectory()
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if err := privateDirectory(dir); err != nil {
		return nil, err
	}
	data, err := json.Marshal(serviceRecord{Service: id, Owner: d.services.ownerToken(), Leader: leader, Boot: boot, Start: start})
	if err != nil {
		return nil, err
	}
	// A record at the same name names an earlier process with this ID,
	// which has ended since the ID is Git's now.
	path := filepath.Join(dir, strconv.Itoa(leader)+".json")
	temp, err := os.CreateTemp(dir, ".record-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(data)
	if err = errors.Join(err, temp.Sync(), temp.Close()); err != nil {
		return nil, err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return nil, err
	}
	return func() { os.Remove(path) }, nil
}

// serviceRecords reads the records of the service id, or of every service
// when id is empty. A records directory it cannot list or a record it
// cannot read is an error: it would hide a service that may still run.
func (d *Driver) serviceRecords(id string) ([]serviceRecord, error) {
	dir := d.recordsDirectory()
	if err := privateDirectory(dir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var records []serviceRecord
	for _, entry := range entries {
		// A name starting with a dot is a record still being written,
		// whose Git has read no input yet.
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // its service ended meanwhile
		} else if err != nil {
			return nil, err
		}
		var r serviceRecord
		if err := json.Unmarshal(data, &r); err != nil || r.Service == "" || r.Leader <= 1 || r.Boot == "" {
			return nil, fmt.Errorf("the service record %s is malformed", name)
		}
		if id == "" || r.Service == id {
			r.path = path
			records = append(records, r)
		}
	}
	return records, nil
}

// alive reports whether the record's leader is still the process it
// records and has not exited. A record of another boot names a process
// that ended with that boot.
func (r serviceRecord) alive() (bool, error) {
	boot, err := bootID()
	if err != nil || boot != r.Boot {
		return false, err
	}
	start, alive, err := leaderStart(r.Leader)
	return alive && start == r.Start, err
}

// end kills the recorded process group while its leader is the recorded
// process, and waits until the leader is gone. Between proving the leader
// alive and the kill, the leader may exit and its process ID be reused by
// a new group leader, which the kill would then reach; without pidfds,
// which the syscall package lacks, that window cannot be closed.
func (r serviceRecord) end() error {
	for limit := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		alive, err := r.alive()
		if err != nil || !alive {
			return err
		}
		if time.Now().After(limit) {
			return fmt.Errorf("the earlier service %s outlived its kill", r.Service)
		}
		_ = syscall.Kill(-r.Leader, syscall.SIGKILL)
	}
}

// endEarlierServices ends the services that earlier drivers over this
// storage left running, as the daemon kills a dead driver's process group
// before a restart, and removes their records. This driver's own services
// go on.
func (d *Driver) endEarlierServices() error {
	records, err := d.serviceRecords("")
	if err != nil {
		return err
	}
	owner := d.services.ownerToken()
	for _, r := range records {
		if r.Owner == owner {
			continue
		}
		if err := r.end(); err != nil {
			return err
		}
		if err := os.Remove(r.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// bootID reads the kernel's ID of the current boot.
func bootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if id == "" {
		return "", errors.New("the kernel's boot ID is empty")
	}
	return id, nil
}

// leaderStart reads a process's start time from /proc, and whether it is
// alive: neither gone nor exited and waiting to be reaped.
func leaderStart(pid int) (start uint64, alive bool, err error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	// The command name is in parentheses and may hold spaces; after it come
	// the state, and the start time 20 fields later.
	fields := strings.Fields(string(data[bytes.LastIndexByte(data, ')')+1:]))
	if len(fields) < 20 {
		return 0, false, fmt.Errorf("process %d's status is malformed", pid)
	}
	start, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("process %d's start time is malformed", pid)
	}
	return start, fields[0] != "Z" && fields[0] != "X", nil
}

// ownerToken names this driver in its services' records; it is made on
// first use.
func (s *services) ownerToken() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owner == "" {
		var token [16]byte
		_, _ = rand.Read(token[:])
		s.owner = hex.EncodeToString(token[:])
	}
	return s.owner
}
