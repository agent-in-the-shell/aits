package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/agent-in-the-shell/aits/internal/shell"
	"github.com/agent-in-the-shell/aits/internal/store"
)

// schedWait bounds each wait between two checks. Timers stop while a Mac
// sleeps; a bounded wait finds the wall clock moved soon after it wakes.
const schedWait = 30 * time.Second

// SchedRequest is one job sched: it runs until ctx is done.
type SchedRequest struct {
	File    string   // the spec; relative to aits's working directory
	Environ []string // aits's environment: each job gets shell.Environ of it
	Kill    <-chan struct{}
	Report  func(Record) // each run, as it ends; called from one goroutine at a time
	Warn    func(string) // a spec that could not be reread, once per message
}

// Heartbeat is what a job sched last wrote about its file (#143).
type Heartbeat struct {
	File      string    `json:"file"`
	CheckedAt time.Time `json:"checked_at"` // the last minute it ran the jobs due by
	Next      []Due     `json:"next"`
	Error     string    `json:"error,omitempty"` // why the file could not be reread; the last good jobs still run
}

// Due is when a job runs next.
type Due struct {
	Name string    `json:"name"`
	At   time.Time `json:"at,omitzero"` // zero: never
}

// schedID names the files of the job sched of file: its heartbeat, its lock,
// its log and its LaunchAgent.
func schedID(file string) string {
	h := fnv.New64a()
	h.Write([]byte(file))
	return fmt.Sprintf("%016x", h.Sum64())
}

func heartbeatPath(dir, id string) string { return filepath.Join(dir, ".sched-"+id+".json") }

// SchedLog is the log a LaunchAgent of file writes, in dir.
func SchedLog(dir, file string) string { return filepath.Join(dir, "sched-"+schedID(file)+".log") }

// Sched runs the jobs r.File declares for this host when they are due, until
// ctx is done; then it waits for the runs it started, which ctx stops. A job
// due more than once since the last check, such as while the Mac slept or
// sched was down, runs once. The file is reread each minute. A non-nil error
// before anything ran means the file is invalid, or another sched holds it.
func Sched(ctx context.Context, r SchedRequest) error {
	file, err := filepath.Abs(r.File)
	if err != nil {
		return invalid("spec %q: %v", r.File, err)
	}
	dir, err := jobDir(r.Environ)
	if err != nil {
		return err
	}
	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("host name: %w", err)
	}
	host, _, _ = strings.Cut(host, ".")
	home, _ := lookup(r.Environ, "HOME")
	load := func() ([]string, []planned, error) {
		spec, err := readSpec(file)
		if err != nil {
			return nil, nil, err
		}
		return plan(spec, host, home)
	}
	path, jobs, err := load()
	if err != nil {
		return err
	}
	id := schedID(file)
	release, err := store.TryLock(filepath.Join(dir, ".sched-"+id+".lock"), true)
	if errors.Is(err, store.ErrLocked) {
		return fmt.Errorf("another job sched is running for %s", file)
	}
	if err != nil {
		return err
	}
	defer release()

	minute := func() time.Time { return time.Now().Round(0).Truncate(time.Minute) }
	hb := Heartbeat{File: file, CheckedAt: minute()}
	if prev, err := readHeartbeat(heartbeatPath(dir, id)); err == nil && prev.File == file && prev.CheckedAt.Before(hb.CheckedAt) {
		hb.CheckedAt = prev.CheckedAt // catch up what came due while no sched ran
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	run := func(j planned, path []string) {
		defer wg.Done()
		env := slices.DeleteFunc(slices.Clone(r.Environ), func(kv string) bool { return strings.HasPrefix(kv, "PATH=") })
		if path != nil {
			env = append(env, "PATH="+strings.Join(path, string(filepath.ListSeparator)))
		}
		timeout := j.timeout
		if timeout == 0 {
			timeout = shell.DefaultTimeout
		}
		rec, err := Run(ctx, Request{Name: j.name, Command: j.command, Timeout: timeout, Environ: env, Pass: j.env, OnAlert: j.onAlert, Kill: r.Kill})
		if err != nil {
			rec = Record{Name: j.name, Status: StatusStartFailed, Message: err.Error(), Alert: AlertNone}
		}
		mu.Lock()
		defer mu.Unlock()
		r.Report(rec)
	}
	for first := true; ; first = false {
		now := minute()
		if first || now.After(hb.CheckedAt) {
			if p, js, err := load(); err != nil {
				if msg := err.Error(); msg != hb.Error {
					hb.Error = msg
					mu.Lock()
					r.Warn(fmt.Sprintf("%s could not be reread; its last good jobs still run: %s", file, msg))
					mu.Unlock()
				}
			} else {
				path, jobs, hb.Error = p, js, ""
			}
			for _, j := range jobs {
				if at := j.cron.next(hb.CheckedAt.In(time.Local)); !at.IsZero() && !at.After(now) {
					wg.Add(1)
					go run(j, path)
				}
			}
			hb.CheckedAt, hb.Next = now, make([]Due, len(jobs))
			for i, j := range jobs {
				hb.Next[i] = Due{Name: j.name, At: j.cron.next(now.In(time.Local))}
			}
			if err := writeHeartbeat(heartbeatPath(dir, id), hb); err != nil {
				mu.Lock()
				r.Warn(fmt.Sprintf("heartbeat: %v", err))
				mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-time.After(min(time.Until(now.Add(time.Minute)), schedWait)):
		}
	}
}

func readHeartbeat(path string) (Heartbeat, error) {
	f, _, err := store.OpenRegular(path, os.O_RDONLY, 0)
	if err != nil {
		return Heartbeat{}, err
	}
	defer f.Close()
	var hb Heartbeat
	err = json.NewDecoder(f).Decode(&hb)
	return hb, err
}

func writeHeartbeat(path string, hb Heartbeat) error {
	b, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	return store.WriteFile(path, append(b, '\n'))
}

// Heartbeats returns what each job sched last wrote, by file. One that cannot
// be read is left out.
func Heartbeats(environ []string) ([]Heartbeat, error) {
	dir, err := jobDir(environ)
	if err != nil {
		return nil, err
	}
	paths, err := filepath.Glob(filepath.Join(dir, ".sched-*.json"))
	if err != nil {
		return nil, err
	}
	hbs := []Heartbeat{}
	for _, p := range paths {
		if hb, err := readHeartbeat(p); err == nil && heartbeatPath(dir, schedID(hb.File)) == p {
			hbs = append(hbs, hb)
		}
	}
	slices.SortFunc(hbs, func(a, b Heartbeat) int { return strings.Compare(a.File, b.File) })
	return hbs, nil
}
