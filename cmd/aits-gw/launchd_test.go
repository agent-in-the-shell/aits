package main

import (
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// plistStrings returns the Label and ProgramArguments of a launchd plist,
// failing the test if the plist is not well-formed XML.
func plistStrings(t *testing.T, plist string) (label string, args []string) {
	t.Helper()
	var p struct {
		Dict struct {
			Items []struct {
				XMLName xml.Name
				Text    string   `xml:",chardata"`
				Strings []string `xml:"string"`
			} `xml:",any"`
		} `xml:"dict"`
	}
	if err := xml.Unmarshal([]byte(plist), &p); err != nil {
		t.Fatal(err)
	}
	items := p.Dict.Items
	for i := 0; i+1 < len(items); i++ {
		if items[i].XMLName.Local != "key" {
			continue
		}
		switch items[i].Text {
		case "Label":
			label = items[i+1].Text
		case "ProgramArguments":
			args = items[i+1].Strings
		}
	}
	return label, args
}

// TestLaunchdExample checks the agent in docs/gw-launchd.md (#171): it is a
// valid plist where plutil exists, its label is aits-gw, and serve really
// starts with its arguments, so the example cannot drift from the CLI.
func TestLaunchdExample(t *testing.T) {
	doc, err := os.ReadFile("../../docs/gw-launchd.md")
	if err != nil {
		t.Fatal(err)
	}
	_, plist, ok := strings.Cut(string(doc), "```xml\n")
	plist, _, ok2 := strings.Cut(plist, "```")
	if !ok || !ok2 {
		t.Fatal("docs/gw-launchd.md has no ```xml block")
	}
	if plutil, err := exec.LookPath("plutil"); err == nil {
		file := filepath.Join(t.TempDir(), "aits-gw.plist")
		if err := os.WriteFile(file, []byte(plist), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(plutil, "-lint", file).CombinedOutput(); err != nil {
			t.Errorf("plutil -lint: %v\n%s", err, out)
		}
	}
	inbox := t.TempDir()
	label, args := plistStrings(t, strings.ReplaceAll(plist, "/ABSOLUTE/PATH/TO/INBOX", inbox))
	if label != "aits-gw" {
		t.Errorf("Label = %q, want aits-gw", label)
	}
	if len(args) < 2 || filepath.Base(args[0]) != "aits-gw" || args[1] != "serve" {
		t.Fatalf("ProgramArguments = %q, want aits-gw serve ...", args)
	}
	h := newHome(t)
	if out := h.serve(args[2:]...).stop(t); out.code != 0 || !strings.Contains(out.stdout, `"status":"stopped"`) {
		t.Errorf("serve with the example's arguments: code=%d stdout=%q stderr=%q", out.code, out.stdout, out.stderr)
	}
}
