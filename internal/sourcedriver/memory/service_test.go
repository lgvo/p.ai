package memory

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/lgvo/p.ai/internal/sourcedriver"
	"github.com/lgvo/p.ai/internal/sourcedriver/conformance"
)

// Client returns a session's clone of the project's repository, which speaks
// the memory Git service's line protocol (service.go).
func (w *World) Client(project string) conformance.Client {
	return &client{w: w, project: project, objects: map[sourcedriver.OID]*object{}}
}

// client is a session's clone: objects of its own, which reach the World's
// repository only through a service.
type client struct {
	w       *World
	project string

	mu      sync.Mutex
	objects map[sourcedriver.OID]*object
}

var _ conformance.Client = (*client)(nil)

func (c *client) Commit(parents ...sourcedriver.OID) sourcedriver.OID {
	return c.write(&object{typ: commitObject, parents: slices.Clone(parents)})
}

func (c *client) Tree() sourcedriver.OID { return c.write(&object{typ: treeObject}) }

func (c *client) write(o *object) sourcedriver.OID {
	c.w.mu.Lock()
	id := sourcedriver.OID(c.w.newID("client " + string(o.typ)))
	c.w.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.objects[id] = o
	return id
}

func (c *client) Has(id sourcedriver.OID) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.objects[id] != nil
}

func (c *client) Advertised(serve conformance.Serve) (map[string]sourcedriver.OID, error) {
	var advertised map[string]sourcedriver.OID
	err := c.exchange(serve, "git-upload-pack", func(in *bufio.Reader, out io.Writer) error {
		var err error
		if advertised, err = readAdvertisement(in); err != nil {
			return err
		}
		if _, err := io.WriteString(out, "done\n"); err != nil {
			return err
		}
		_, err = c.readObjects(in)
		return err
	})
	return advertised, err
}

func (c *client) Fetch(serve conformance.Serve, name string) (sourcedriver.OID, error) {
	var tip sourcedriver.OID
	err := c.exchange(serve, "git-upload-pack", func(in *bufio.Reader, out io.Writer) error {
		advertised, err := readAdvertisement(in)
		if err != nil {
			return err
		}
		if tip = advertised["refs/heads/"+name]; tip == "" {
			return fmt.Errorf("refs/heads/%s is not advertised", name)
		}
		if _, err := io.WriteString(out, "want "+string(tip)+"\ndone\n"); err != nil {
			return err
		}
		objects, err := c.readObjects(in)
		if err != nil {
			return err
		}
		c.keep(objects)
		return nil
	})
	return tip, err
}

func (c *client) WantID(serve conformance.Serve, id sourcedriver.OID) error {
	return c.exchange(serve, "git-upload-pack", func(in *bufio.Reader, out io.Writer) error {
		if _, err := readAdvertisement(in); err != nil {
			return err
		}
		if _, err := io.WriteString(out, "want "+string(id)+"\ndone\n"); err != nil {
			return err
		}
		objects, err := c.readObjects(in)
		if err != nil {
			return err
		}
		if objects[id] == nil {
			return fmt.Errorf("the service did not send %s", id)
		}
		return nil
	})
}

func (c *client) Push(serve conformance.Serve, updates ...conformance.Update) error {
	return c.exchange(serve, "git-receive-pack", func(in *bufio.Reader, out io.Writer) error {
		advertised, err := readAdvertisement(in)
		if err != nil {
			return err
		}
		var request strings.Builder
		var news []sourcedriver.OID
		for _, u := range updates {
			fmt.Fprintf(&request, "update %s %s %s\n", orNone(advertised[u.Ref]), orNone(u.New), u.Ref)
			news = append(news, u.New)
		}
		for _, line := range c.objectLines(news) {
			request.WriteString(line)
		}
		request.WriteString("done\n")
		if _, err := io.WriteString(out, request.String()); err != nil {
			return err
		}
		answer, err := in.ReadString('\n')
		if err != nil {
			return err
		}
		if answer != "ok\n" {
			return fmt.Errorf("the push was refused: %s", strings.TrimSpace(answer))
		}
		return nil
	})
}

// exchange runs one command: it hands serve the service's end of the
// channel, runs talk on the client's end, and returns talk's error once
// serve returned and the channel is closed.
func (c *client) exchange(serve conformance.Serve, word string, talk func(*bufio.Reader, io.Writer) error) error {
	toService, fromClient := io.Pipe()
	toClient, fromService := io.Pipe()
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = serve(word+" '/"+c.project+"'", sourcedriver.Stream{In: toService, Out: fromService, Err: io.Discard})
		fromService.Close()
		toService.Close()
	}()
	err := talk(bufio.NewReader(toClient), fromClient)
	fromClient.Close()
	_, _ = io.Copy(io.Discard, toClient)
	<-served
	return err
}

// readAdvertisement reads the heads a service advertises, by ref.
func readAdvertisement(in *bufio.Reader) (map[string]sourcedriver.OID, error) {
	advertised := map[string]sourcedriver.OID{}
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(line)
		switch {
		case line == "end\n":
			return advertised, nil
		case len(fields) == 3 && fields[0] == "head":
			advertised[fields[2]] = sourcedriver.OID(fields[1])
		default:
			return nil, fmt.Errorf("unexpected advertisement %q", line)
		}
	}
}

// readObjects reads the objects an upload service sends, up to its end.
func (c *client) readObjects(in *bufio.Reader) (map[sourcedriver.OID]*object, error) {
	objects := map[sourcedriver.OID]*object{}
	for {
		line, err := in.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(line)
		switch {
		case line == "end\n":
			return objects, nil
		case len(fields) >= 2 && fields[0] == "commit":
			parents := make([]sourcedriver.OID, 0, len(fields)-2)
			for _, p := range fields[2:] {
				parents = append(parents, sourcedriver.OID(p))
			}
			objects[sourcedriver.OID(fields[1])] = &object{typ: commitObject, parents: parents}
		default:
			return nil, errors.New(strings.TrimSpace(line))
		}
	}
}

func (c *client) keep(objects map[sourcedriver.OID]*object) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, o := range objects {
		c.objects[id] = o
	}
}

// objectLines are the client's objects the ids reach, as a push sends them.
func (c *client) objectLines(ids []sourcedriver.OID) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen := map[sourcedriver.OID]bool{}
	var lines []string
	for stack := slices.Clone(ids); len(stack) > 0; {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		o := c.objects[id]
		if id == "" || seen[id] || o == nil {
			continue
		}
		seen[id] = true
		line := string(o.typ) + " " + string(id)
		for _, p := range o.parents {
			line += " " + string(p)
		}
		lines = append(lines, line+"\n")
		stack = append(stack, o.parents...)
	}
	return lines
}

// orNone writes an absent ID as the protocol's "-".
func orNone(id sourcedriver.OID) string {
	if id == "" {
		return "-"
	}
	return string(id)
}
