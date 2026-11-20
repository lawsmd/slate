// Package link lets another program on the same machine read and edit the
// documents open in slate editors, so that a person and a language model can
// write one together.
//
// A host registers each editor. The first registration opens a Unix socket
// in a directory only the user can reach and writes a file beside it that
// lists the process's editors. A client finds the editors through those
// files and sends one request per connection, as a line of JSON:
//
//	{"op":"list"}
//	{"op":"read","id":"4242-1"}
//	{"op":"edit","id":"4242-1","old":"teh","new":"the","by":"claude"}
//	{"op":"edit","id":"4242-1","own":true,"new":"draft 2","by":"claude","if_rev":7}
//	{"op":"watch","id":"4242-1"}
//
// The protocol can read and replace text and nothing else. It has no request
// that saves, sends or submits: what happens to the document stays with the
// person at the host's keyboard. An edit shows in the editor at once,
// highlighted, and one undo removes it.
//
// An edit with "own" replaces the text that writer's latest edit put in, as
// it stands now, so a writer can revise its draft after the person has
// changed it. An edit with "if_rev" is refused unless the document is still
// at that revision. A read returns the revision and each writer's text.
package link

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lawsmd/slate"
)

// Document is an editor as the link uses it. *slate.Editor is one.
type Document interface {
	Snapshot(ctx context.Context) (slate.Snapshot, error)
	Edit(ctx context.Context, edit slate.Edit, origin string) (uint64, error)
	Watch(fn func(slate.Event)) (stop func())
	Target() string
}

// Info describes an editor to a client that has to choose among several.
type Info struct {
	// Title says what the document is, such as "comment on PROJ-123".
	Title string `json:"title,omitempty"`
	// Meta holds whatever else the host knows, such as a ticket key or a
	// file path.
	Meta map[string]string `json:"meta,omitempty"`
}

// Entry is one editor in a listing.
type Entry struct {
	ID     string `json:"id"`
	Target string `json:"target"`
	Info
	PID  int    `json:"pid"`
	Exe  string `json:"exe"`                  // the host program
	Pane string `json:"herdr_pane,omitempty"` // the host's Herdr pane, when it runs in one

	address string
}

// listing is the file a host writes for clients to find it.
type listing struct {
	PID     int     `json:"pid"`
	Address string  `json:"address"`
	Editors []Entry `json:"editors"`
}

// dirEnv overrides where hosts and clients meet.
const dirEnv = "SLATE_LINK_DIR"

// offEnv set to 0 keeps a host's editors to itself.
const offEnv = "SLATE_LINK"

// Enabled reports whether this process may register editors. Setting
// SLATE_LINK=0 turns the link off.
func Enabled() bool { return os.Getenv(offEnv) != "0" }

// dir returns the directory of the sockets and listings. It is inside the
// user's own cache directory, which other users cannot open.
func dir() (string, error) {
	if d := os.Getenv(dirEnv); d != "" {
		return d, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "slate", "run"), nil
}

// server is this process's end of the link.
var server struct {
	mu   sync.Mutex
	ln   net.Listener
	dir  string
	next int
	docs map[string]*Registration
}

// Registration is one editor offered over the link.
type Registration struct {
	id   string
	doc  Document
	info Info
}

// Register offers doc to clients until the registration is closed.
func Register(doc Document, info Info) (*Registration, error) {
	if !Enabled() {
		return nil, errors.New("link: turned off by " + offEnv + "=0")
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.ln == nil {
		d, err := dir()
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
		address := filepath.Join(d, strconv.Itoa(os.Getpid())+".sock")
		os.Remove(address) // left by an earlier process with this ID
		ln, err := net.Listen("unix", address)
		if err != nil {
			return nil, fmt.Errorf("link: %w", err)
		}
		server.ln, server.dir, server.docs = ln, d, map[string]*Registration{}
		go serve(ln)
	}
	server.next++
	r := &Registration{id: fmt.Sprintf("%d-%d", os.Getpid(), server.next), doc: doc, info: info}
	server.docs[r.id] = r
	return r, publish()
}

// ID returns the name clients use for this editor.
func (r *Registration) ID() string { return r.id }

// Describe replaces the editor's description.
func (r *Registration) Describe(info Info) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	r.info = info
	return publish()
}

// Close withdraws the editor. The socket closes with the last one.
func (r *Registration) Close() error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.docs[r.id] != r {
		return nil
	}
	delete(server.docs, r.id)
	if len(server.docs) > 0 {
		return publish()
	}
	address := server.ln.Addr().String()
	err := server.ln.Close()
	server.ln = nil
	os.Remove(address)
	os.Remove(listingPath(server.dir, os.Getpid()))
	return err
}

func listingPath(dir string, pid int) string {
	return filepath.Join(dir, strconv.Itoa(pid)+".json")
}

// entries returns this process's editors in the order they registered. The
// caller holds server.mu.
func entries() []Entry {
	exe, _ := os.Executable()
	out := make([]Entry, 0, len(server.docs))
	for _, r := range server.docs {
		out = append(out, Entry{
			ID: r.id, Target: r.doc.Target(), Info: r.info,
			PID: os.Getpid(), Exe: filepath.Base(exe), Pane: os.Getenv("HERDR_PANE_ID"),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// publish writes this process's listing. The caller holds server.mu.
func publish() error {
	data, err := json.MarshalIndent(listing{
		PID: os.Getpid(), Address: server.ln.Addr().String(), Editors: entries(),
	}, "", "  ")
	if err != nil {
		return err
	}
	path := listingPath(server.dir, os.Getpid())
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// request is one line from a client.
type request struct {
	Op  string `json:"op"`
	ID  string `json:"id,omitempty"`
	Old   string `json:"old,omitempty"`
	New   string `json:"new,omitempty"`
	By    string `json:"by,omitempty"`
	Own   bool   `json:"own,omitempty"`
	IfRev uint64 `json:"if_rev,omitempty"`
}

// Change is one change of a document, as watch reports it.
type Change struct {
	Rev    uint64 `json:"rev"`
	Origin string `json:"origin"` // "user", "host", or the name an edit gave
}

// response is one line to a client. A watch gets one line per change.
type response struct {
	Error   string          `json:"error,omitempty"`
	Editors []Entry         `json:"editors,omitempty"`
	Doc     *slate.Snapshot `json:"doc,omitempty"`
	Rev     uint64          `json:"rev,omitempty"`
	Change  *Change         `json:"change,omitempty"`
}

func serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handle(conn)
	}
}

// requestTimeout bounds how long one read or edit may take in the editor.
const requestTimeout = 20 * time.Second

func handle(conn net.Conn) {
	defer conn.Close()
	var req request
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return
	}
	out := json.NewEncoder(conn)
	if err := json.Unmarshal(line, &req); err != nil {
		out.Encode(response{Error: "the request is not JSON: " + err.Error()})
		return
	}
	server.mu.Lock()
	r := server.docs[req.ID]
	all := entries()
	server.mu.Unlock()
	if req.Op == "list" {
		out.Encode(response{Editors: all})
		return
	}
	if r == nil {
		out.Encode(response{Error: fmt.Sprintf("this program has no editor %q", req.ID)})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	switch req.Op {
	case "read":
		doc, err := r.doc.Snapshot(ctx)
		if err != nil {
			out.Encode(response{Error: err.Error()})
			return
		}
		out.Encode(response{Doc: &doc})
	case "edit":
		by := req.By
		if by == "" {
			by = "link"
		}
		rev, err := r.doc.Edit(ctx, slate.Edit{Old: req.Old, New: req.New, Own: req.Own, IfRev: req.IfRev}, by)
		if err != nil {
			out.Encode(response{Error: err.Error()})
			return
		}
		out.Encode(response{Rev: rev})
	case "watch":
		changes := make(chan Change, 256)
		stop := r.doc.Watch(func(ev slate.Event) {
			if c, ok := ev.(slate.Changed); ok {
				select {
				case changes <- Change{Rev: c.Rev, Origin: c.Origin}:
				default: // the client is behind; it reads the document to catch up
				}
			}
		})
		defer stop()
		gone := make(chan struct{})
		go func() { // the client hangs up to end the watch
			conn.Read(make([]byte, 1))
			close(gone)
		}()
		for {
			select {
			case c := <-changes:
				if out.Encode(response{Change: &c}) != nil {
					return
				}
			case <-gone:
				return
			}
		}
	default:
		out.Encode(response{Error: fmt.Sprintf("unknown op %q; there are list, read, edit and watch", req.Op)})
	}
}

// List returns every editor registered on this machine by this user. It
// deletes the files of hosts that no longer answer.
func List() ([]Entry, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	files, _ := filepath.Glob(filepath.Join(d, "*.json"))
	var out []Entry
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var l listing
		if json.Unmarshal(data, &l) != nil {
			continue
		}
		// The host answers for itself: its file may be a moment old.
		resp, err := call(l.Address, request{Op: "list"}, nil)
		if err != nil {
			os.Remove(file) // the host has gone
			os.Remove(l.Address)
			continue
		}
		for _, e := range resp.Editors {
			e.address = l.Address
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Find returns the editor with that ID. An empty ID stands for the only
// editor there is.
func Find(id string) (Entry, error) {
	all, err := List()
	if err != nil {
		return Entry{}, err
	}
	if id == "" && len(all) == 1 {
		return all[0], nil
	}
	var names []string
	for _, e := range all {
		if e.ID == id {
			return e, nil
		}
		names = append(names, e.ID)
	}
	switch {
	case len(all) == 0:
		return Entry{}, errors.New("no slate editor is open")
	case id == "":
		return Entry{}, fmt.Errorf("several editors are open; name one of %s", strings.Join(names, ", "))
	}
	return Entry{}, fmt.Errorf("no editor %q; there are %s", id, strings.Join(names, ", "))
}

// call sends one request and returns the first response. With each set, it
// then passes every further response to each until the host hangs up or
// each returns false.
func call(address string, req request, each func(response) bool) (response, error) {
	conn, err := net.DialTimeout("unix", address, 2*time.Second)
	if err != nil {
		return response{}, err
	}
	defer conn.Close()
	if each == nil {
		conn.SetDeadline(time.Now().Add(requestTimeout + 5*time.Second))
	}
	line, _ := json.Marshal(req)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		return response{}, err
	}
	in := json.NewDecoder(conn)
	var first response
	if each == nil {
		if err := in.Decode(&first); err != nil {
			return response{}, err
		}
		if first.Error != "" {
			return response{}, errors.New(first.Error)
		}
		return first, nil
	}
	for {
		var r response
		if err := in.Decode(&r); err != nil {
			return first, nil // the host closed the editor or exited
		}
		if r.Error != "" {
			return response{}, errors.New(r.Error)
		}
		if !each(r) {
			return first, nil
		}
	}
}

// Read returns the document of the editor with that ID, its revision, the
// text the user has selected and each writer's text.
func Read(id string) (slate.Snapshot, error) {
	e, err := Find(id)
	if err != nil {
		return slate.Snapshot{}, err
	}
	resp, err := call(e.address, request{Op: "read", ID: e.ID}, nil)
	if err != nil {
		return slate.Snapshot{}, err
	}
	if resp.Doc == nil {
		return slate.Snapshot{}, errors.New("link: the host sent no document")
	}
	return *resp.Doc, nil
}

// Edit makes one edit, as slate.Edit describes, in the editor with that ID
// and returns the new revision. by names the writer to the host.
func Edit(id string, edit slate.Edit, by string) (uint64, error) {
	e, err := Find(id)
	if err != nil {
		return 0, err
	}
	resp, err := call(e.address, request{
		Op: "edit", ID: e.ID, Old: edit.Old, New: edit.New, By: by, Own: edit.Own, IfRev: edit.IfRev,
	}, nil)
	return resp.Rev, err
}

// Watch calls fn with each change of the document until fn returns false or
// the editor closes.
func Watch(id string, fn func(Change) bool) error {
	e, err := Find(id)
	if err != nil {
		return err
	}
	_, err = call(e.address, request{Op: "watch", ID: e.ID}, func(r response) bool {
		return r.Change == nil || fn(*r.Change)
	})
	return err
}
