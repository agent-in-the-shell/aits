package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type applyResult struct {
	Host      string        `json:"host"`
	Added     []string      `json:"added"`
	Changed   []string      `json:"changed"`
	Removed   []string      `json:"removed"`
	Unchanged []string      `json:"unchanged"`
	Sched     *schedApplied `json:"sched"`
	Error     *errorBody    `json:"error"`
}

// cron is a fake crontab command on PATH: its table is a file next to it,
// and every install is counted, so a test never touches the user's crontab
// (P12).
type cron struct {
	bin, home, spec string
	aitsHome        string // one AITS_HOME for every apply, as a user has
}

func newCron(t *testing.T, table string) cron {
	t.Helper()
	c := cron{bin: t.TempDir(), home: t.TempDir(), aitsHome: t.TempDir()}
	c.spec = filepath.Join(t.TempDir(), "jobs.json")
	script := `#!/bin/sh
d=${0%/*}
case $1 in
-l) if [ -f "$d/table" ]; then /bin/cat "$d/table"; else echo "crontab: no crontab for you" >&2; exit 1; fi;;
-) [ ! -f "$d/fail" ] || { echo "crontab: refused" >&2; exit 1; }; /bin/cat > "$d/table"; echo >> "$d/installs";;
*) exit 2;;
esac
`
	if err := os.WriteFile(filepath.Join(c.bin, "crontab"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if table != "" {
		c.write(t, "table", table)
	}
	if err := os.MkdirAll(filepath.Join(c.home, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"bin/backup", "bin/notify"} {
		if err := os.WriteFile(filepath.Join(c.home, p), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func (c cron) write(t *testing.T, name, s string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(c.bin, name), []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (c cron) table(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(c.bin, "table"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func (c cron) installs(t *testing.T) int {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(c.bin, "installs"))
	return strings.Count(string(b), "\n")
}

// apply writes spec and runs "aits job apply SPEC --json [flags]".
func (c cron) apply(t *testing.T, spec string, flags ...string) (applyResult, output) {
	t.Helper()
	if err := os.WriteFile(c.spec, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	out := call{path: c.bin, env: []string{"HOME=" + c.home, "AITS_HOME=" + c.aitsHome}, args: append([]string{"job", "apply", c.spec, "--json"}, flags...)}.run(t)
	return decodeOne[applyResult](t, out.stdout), out
}

func shortHost(t *testing.T) string {
	t.Helper()
	h, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

func TestJobApplyInstallsBlock(t *testing.T) {
	c := newCron(t, "MAILTO=\"\"\n0 1 * * * /usr/bin/true\n")
	spec := `{"path": "~/bin:/usr/bin:/bin", "jobs": [
	  {"name": "backup", "schedule": "30  23 * * *", "timeout": "10m", "on_alert": "~/bin/notify", "env": ["TOKEN"],
	   "command": ["~/bin/backup", "it's here"]},
	  {"name": "prices", "schedule": "*/5 * * * *", "command": ["/bin/echo"]}]}`

	res, out := c.apply(t, spec, "--dry-run")
	if out.code != 0 || !slices.Equal(res.Added, []string{"backup", "prices"}) || res.Host != shortHost(t) {
		t.Fatalf("dry run: exit %d, %+v, stderr %q", out.code, res, out.stderr)
	}
	if c.installs(t) != 0 {
		t.Fatal("a dry run installed a crontab")
	}

	res, out = c.apply(t, spec)
	if out.code != 0 || !slices.Equal(res.Added, []string{"backup", "prices"}) || len(res.Unchanged) != 0 {
		t.Fatalf("exit %d, %+v, stderr %q", out.code, res, out.stderr)
	}
	path := c.home + "/bin:/usr/bin:/bin"
	want := fmt.Sprintf(`MAILTO=""
0 1 * * * /usr/bin/true
# BEGIN aits job apply %[1]s
30 23 * * * PATH=%[2]s %[3]s job run backup --timeout 10m --env TOKEN --on-alert %[4]s/bin/notify -- %[4]s/bin/backup 'it'\''s here'
*/5 * * * * PATH=%[2]s %[3]s job run prices -- /bin/echo
# END aits job apply %[1]s
`, c.spec, path, aitsBin, c.home)
	if got := c.table(t); got != want {
		t.Fatalf("crontab:\n%s\nwant:\n%s", got, want)
	}

	res, out = c.apply(t, spec)
	if out.code != 0 || !slices.Equal(res.Unchanged, []string{"backup", "prices"}) || len(res.Added) != 0 {
		t.Fatalf("again: exit %d, %+v", out.code, res)
	}
	if c.installs(t) != 1 {
		t.Errorf("%d installs; an apply with nothing to change installed again", c.installs(t))
	}
}

func TestJobApplyChangesRemovesAndFiltersHosts(t *testing.T) {
	c := newCron(t, "")
	_, out := c.apply(t, `{"jobs": [
	  {"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo", "1"]},
	  {"name": "b", "schedule": "0 * * * *", "command": ["/bin/echo"]},
	  {"name": "c", "schedule": "0 * * * *", "command": ["/bin/echo"]}]}`)
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	// b leaves, a changes, c moves to another host, whose missing paths this
	// host does not check, and d is for this host.
	res, out := c.apply(t, fmt.Sprintf(`{"jobs": [
	  {"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo", "2"]},
	  {"name": "c", "schedule": "0 * * * *", "hosts": ["not-%[1]s"], "command": ["/no/such/program"]},
	  {"name": "d", "schedule": "0 * * * *", "hosts": ["not-%[1]s", "%[1]s"], "command": ["/bin/echo"]}]}`, shortHost(t)))
	if out.code != 0 {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}
	if !slices.Equal(res.Added, []string{"d"}) || !slices.Equal(res.Changed, []string{"a"}) ||
		!slices.Equal(res.Removed, []string{"b", "c"}) || len(res.Unchanged) != 0 {
		t.Fatalf("%+v", res)
	}
	table := c.table(t)
	if !strings.Contains(table, "job run a -- /bin/echo 2\n") || strings.Contains(table, "job run b ") || !strings.Contains(table, "job run d ") {
		t.Fatalf("crontab:\n%s", table)
	}

	res, out = c.apply(t, `{"jobs": []}`)
	if out.code != 0 || !slices.Equal(res.Removed, []string{"a", "d"}) || c.table(t) != "" {
		t.Fatalf("exit %d, %+v, crontab %q", out.code, res, c.table(t))
	}
}

// TestJobApplyRefuses pins P6: invalid input is exit 2 and leaves the
// crontab as it was.
func TestJobApplyRefuses(t *testing.T) {
	job := func(fields string) string {
		return `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo"]` + fields + `}]}`
	}
	cases := map[string]struct{ spec, table, want string }{
		"unknown key":        {spec: `{"jobs": [], "job": []}`, want: `unknown field "job"`},
		"two values":         {spec: `{"jobs": []} {}`, want: "more than one JSON value"},
		"bad name":           {spec: `{"jobs": [{"name": "-a", "schedule": "0 * * * *", "command": ["/bin/echo"]}]}`, want: "job name"},
		"duplicate":          {spec: `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo"]}, {"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo"]}]}`, want: "more than once"},
		"short schedule":     {spec: `{"jobs": [{"name": "a", "schedule": "@daily", "command": ["/bin/echo"]}]}`, want: "5 cron fields"},
		"no command":         {spec: `{"jobs": [{"name": "a", "schedule": "0 * * * *"}]}`, want: "no command"},
		"relative command":   {spec: `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["echo"]}]}`, want: "not an absolute path"},
		"missing command":    {spec: `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/no/such/program"]}]}`, want: "no such file"},
		"percent":            {spec: `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/bin/date", "+%s"]}]}`, want: "line break"},
		"bad timeout":        {spec: job(`, "timeout": "soon"`), want: "positive duration"},
		"bad env":            {spec: job(`, "env": ["A=1"]`), want: "variable name"},
		"empty host":         {spec: job(`, "hosts": [""]`), want: "empty host"},
		"relative path":      {spec: `{"path": "bin", "jobs": []}`, want: "path entry"},
		"relative on_alert":  {spec: job(`, "on_alert": "notify"`), want: "on_alert"},
		"block without end":  {spec: job(""), table: "# BEGIN aits job apply SPEC\n", want: "without the line that closes it"},
		"block twice":        {spec: job(""), table: "# BEGIN aits job apply SPEC\n# END aits job apply SPEC\n# BEGIN aits job apply SPEC\n# END aits job apply SPEC\n", want: "more than once"},
		"hand-written block": {spec: job(""), table: "# BEGIN aits job apply SPEC\n0 * * * * /bin/echo\n# END aits job apply SPEC\n", want: "did not write"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c := newCron(t, "")
			table := strings.ReplaceAll(tc.table, "SPEC", c.spec) + "0 1 * * * /usr/bin/true\n"
			c.write(t, "table", table)
			res, out := c.apply(t, tc.spec)
			if out.code != 2 || res.Error == nil || res.Error.Code != "invalid_input" || !strings.Contains(res.Error.Message, tc.want) {
				t.Fatalf("exit %d, %+v, want invalid_input with %q", out.code, res.Error, tc.want)
			}
			if c.installs(t) != 0 || c.table(t) != table {
				t.Errorf("the crontab changed: %q", c.table(t))
			}
		})
	}
}

func TestJobApplyCrontabFails(t *testing.T) {
	c := newCron(t, "")
	c.write(t, "fail", "")
	_, out := c.apply(t, `{"jobs": [{"name": "a", "schedule": "0 * * * *", "command": ["/bin/echo"]}]}`)
	if out.code != 1 || !strings.Contains(out.stdout, `"internal"`) || !strings.Contains(out.stdout, "refused") {
		t.Fatalf("exit %d: %s", out.code, out.stdout)
	}

	out = call{path: t.TempDir(), args: []string{"job", "apply", c.spec, "--json"}}.run(t)
	if out.code != 1 || !strings.Contains(out.stdout, "crontab is not on PATH") {
		t.Fatalf("without crontab: exit %d: %s", out.code, out.stdout)
	}
}
