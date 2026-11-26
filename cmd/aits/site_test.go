package main

import (
	"bytes"
	"flag"
	"fmt"
	"html"
	"html/template"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/agent-in-the-shell/aits/internal/cli"
)

// The website (site/, #174): index.html is written by hand; cli.html and
// principles.html are generated from docs/cli.md and docs/principles.md
// through site/layout.tmpl. "go test ./cmd/aits -run TestSitePages -update"
// rewrites them.

const repoURL = "https://github.com/agent-in-the-shell/aits"

// page is what site/layout.tmpl fills in.
type page struct {
	Page, Title, OGTitle, Description string
	Body                              template.HTML
}

// TestSitePages enforces P13 for the website: cli.html and principles.html
// are exactly what docs/cli.md and docs/principles.md render to, and
// TestCLIDocs keeps docs/cli.md equal to the help.
func TestSitePages(t *testing.T) {
	layout := template.Must(template.ParseFiles("../../site/layout.tmpl"))
	pages := map[string]page{
		"cli.html": {
			Page: "cli", Title: "CLI reference: aits", OGTitle: "aits CLI reference",
			Description: "Every aits command and flag, with result fields, statuses, errors and exit codes. Rendered from docs/cli.md, which is the help of the current build.",
			Body:        template.HTML(cliBody(t, readFile(t, "../../docs/cli.md"))),
		},
		"principles.html": {
			Page: "principles", Title: "Principles: aits", OGTitle: "aits principles",
			Description: "The design rules for aits: each states the rule, the failure it prevents, and the guard that enforces it. Rendered from docs/principles.md.",
			Body:        template.HTML(principlesBody(t, readFile(t, "../../docs/principles.md"))),
		},
	}
	for name, p := range pages {
		var b bytes.Buffer
		if err := layout.Execute(&b, p); err != nil {
			t.Fatal(err)
		}
		file := "../../site/" + name
		if *update {
			if err := os.WriteFile(file, b.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if string(readFile(t, file)) != b.String() {
			t.Errorf("site/%s is out of date; run: go test ./cmd/aits -run TestSitePages -update", name)
		}
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// helpSection is one "## aits ..." section of docs/cli.md.
type helpSection struct {
	path []string // without "aits"
	text string   // the help, without the code fence
}

func helpSections(t *testing.T, md []byte) []helpSection {
	t.Helper()
	var out []helpSection
	for _, part := range strings.Split(string(md), "\n## ")[1:] {
		title, rest, _ := strings.Cut(part, "\n")
		_, body, ok := strings.Cut(rest, "```text\n")
		body, _, ok2 := strings.Cut(body, "```")
		if !ok || !ok2 {
			t.Fatalf("docs/cli.md: section %q has no text block", title)
		}
		out = append(out, helpSection{path: strings.Fields(title)[1:], text: body})
	}
	return out
}

func anchor(path []string) string {
	if len(path) == 0 {
		return "aits"
	}
	return strings.Join(path, "-")
}

var (
	flagRow    = regexp.MustCompile(`^  (--[a-z-]+)(?: (\S+))?\s{2,}(.*)$`)
	defaultEnd = regexp.MustCompile(`\s*\(default ([^)]*)\)$`)
	cmdRow     = regexp.MustCompile(`^  ([a-z]+)\s{2,}(.*)$`)
	labelHead  = regexp.MustCompile(`^([A-Z][A-Za-z ()/-]{0,40}):$`)
	labelLead  = regexp.MustCompile(`^([A-Z][A-Za-z ()/-]{0,30}):( |$)`)
	flagWord   = regexp.MustCompile(`(^|[\s(,"])(--[a-z][a-z-]*)`)
)

// inline escapes s and marks the flags in it as code.
func inline(s string) string {
	return flagWord.ReplaceAllString(html.EscapeString(s), "$1<code>$2</code>")
}

// cliBody renders docs/cli.md: a hero, a table of contents and one section
// per command. Each help is split into its usage line, summary, flags or
// commands table, and the rest as paragraphs and preformatted blocks.
func cliBody(t *testing.T, md []byte) string {
	secs := helpSections(t, md)
	var b, toc, ticker strings.Builder
	group := ""
	for _, s := range secs {
		name := strings.Join(append([]string{"aits"}, s.path...), " ")
		if len(s.path) > 0 && s.path[0] != group {
			group = s.path[0]
			fmt.Fprintf(&toc, "      <li class=\"grp-l\">%s</li>\n", group)
		}
		label := strings.Join(s.path, " ")
		if label == "" {
			label = "aits"
		}
		fmt.Fprintf(&toc, "      <li><a href=\"#%s\">%s</a></li>\n", anchor(s.path), label)
		if len(s.path) > 1 || len(s.path) == 1 && (s.path[0] == "version" || s.path[0] == "help") {
			fmt.Fprintf(&ticker, "<b><i>$</i> %s</b>", name)
		}
	}
	fmt.Fprintf(&b, `<div class="doc-head" id="top" data-hero="help">
  <div class="wrap">
    <span class="num"><span class="hp" aria-hidden="true"><span class="c-p">$ </span>aits help</span>reference</span>
    <h1>CLI reference<span class="sq" aria-hidden="true"></span></h1>
    <p class="lede">Every command and flag in the current build. This page renders <a href="%[1]s/blob/main/docs/cli.md"><code>docs/cli.md</code></a>, which is generated from the help output of <code>aits</code> and checked against it in CI. The installed binary's help is the authority for its version: run <code>aits help COMMAND...</code> or <code>aits COMMAND --help</code>.</p>
    <p class="meta">pre-release: output shapes may change before 1.0.</p>
    <div class="ticker" aria-hidden="true"><span>%[2]s%[2]s</span></div>
  </div>
</div>

<div class="wrap doc">
  <nav class="toc" aria-label="Commands on this page">
    <h2>On this page</h2>
    <ol>
%[3]s    </ol>
  </nav>

  <div class="doc-body json">
`, repoURL, ticker.String(), toc.String())
	for i, s := range secs {
		b.WriteString(cliSection(s, i == 0))
	}
	b.WriteString("  </div>\n</div>")
	return b.String()
}

func cliSection(s helpSection, first bool) string {
	var b strings.Builder
	name := strings.Join(append([]string{"aits"}, s.path...), " ")
	style := ""
	if first {
		style = ` style="padding-top:0;border-top:0"`
	}
	title := html.EscapeString(name)
	if len(s.path) > 1 {
		parent := strings.Join(append([]string{"aits"}, s.path[:len(s.path)-1]...), " ")
		title = fmt.Sprintf(`<span class="par">%s</span> %s`, parent, s.path[len(s.path)-1])
	}
	fmt.Fprintf(&b, "\n  <section class=\"cmd\" id=\"%s\"%s>\n    <div class=\"cmd-h\"><h2>%s</h2></div>\n", anchor(s.path), style, title)
	blocks := strings.Split(strings.TrimRight(s.text, "\n"), "\n\n")
	usage := strings.TrimPrefix(blocks[0], "Usage: ")
	rest := strings.TrimPrefix(usage, name)
	fmt.Fprintf(&b, "    <p class=\"sum\">%s</p>\n<pre class=\"usage\">Usage: <span class=\"k\">%s</span>%s</pre>\n", inline(strings.ReplaceAll(blocks[1], "\n", " ")), html.EscapeString(name), html.EscapeString(rest))
	for _, blk := range blocks[2:] {
		lines := strings.Split(blk, "\n")
		switch lines[0] {
		case "Flags:":
			b.WriteString("    <h3>Flags</h3>\n    <table class=\"tbl\">\n      <thead><tr><th scope=\"col\">Flag</th><th scope=\"col\">Meaning</th><th scope=\"col\" class=\"c-dflt\">Default</th></tr></thead>\n      <tbody>\n")
			for _, l := range lines[1:] {
				m := flagRow.FindStringSubmatch(l)
				if m == nil {
					continue
				}
				desc, dflt := m[3], ""
				if d := defaultEnd.FindStringSubmatch(desc); d != nil {
					dflt, desc = d[1], strings.TrimSuffix(desc, d[0])
				}
				flag := "<code>" + m[1] + "</code>"
				if m[2] != "" {
					flag += " <span class=\"a\">" + html.EscapeString(m[2]) + "</span>"
				}
				fmt.Fprintf(&b, "        <tr><td>%s</td><td>%s</td><td class=\"c-dflt\">%s</td></tr>\n", flag, inline(desc), html.EscapeString(dflt))
			}
			b.WriteString("      </tbody>\n    </table>\n")
			continue
		case "Commands:":
			b.WriteString("    <h3>Commands</h3>\n    <table class=\"tbl\">\n      <thead><tr><th scope=\"col\">Command</th><th scope=\"col\">What it does</th></tr></thead>\n      <tbody>\n")
			for _, l := range lines[1:] {
				if m := cmdRow.FindStringSubmatch(l); m != nil {
					fmt.Fprintf(&b, "        <tr><td><a href=\"#%s\">%s</a></td><td>%s</td></tr>\n", anchor(append(slices.Clone(s.path), m[1])), m[1], inline(m[2]))
				}
			}
			b.WriteString("      </tbody>\n    </table>\n")
			continue
		}
		if m := labelHead.FindStringSubmatch(lines[0]); m != nil {
			fmt.Fprintf(&b, "    <h3>%s</h3>\n", html.EscapeString(m[1]))
			lines = lines[1:]
		}
		b.WriteString(prose(lines))
	}
	b.WriteString("  </section>\n")
	return b.String()
}

// prose renders lines: runs of lines indented less than two spaces become
// paragraphs (a short "Label:" lead in bold), runs of indented lines a
// preformatted block.
func prose(lines []string) string {
	var b strings.Builder
	for len(lines) > 0 {
		indented := strings.HasPrefix(lines[0], "  ")
		n := 1
		for n < len(lines) && strings.HasPrefix(lines[n], "  ") == indented {
			n++
		}
		run := lines[:n]
		lines = lines[n:]
		if indented {
			fmt.Fprintf(&b, "<pre class=\"out-block\">%s</pre>\n", html.EscapeString(dedent(run)))
			continue
		}
		for i := range run {
			run[i] = strings.TrimSpace(run[i])
		}
		text := strings.Join(run, " ")
		lead := ""
		if m := labelLead.FindStringSubmatch(text); m != nil {
			lead = "<b>" + html.EscapeString(m[1]) + ".</b> "
			text = strings.TrimSpace(text[len(m[0]):])
		}
		fmt.Fprintf(&b, "    <p>%s%s</p>\n", lead, inline(text))
	}
	return b.String()
}

// dedent removes the indentation every line shares.
func dedent(lines []string) string {
	cut := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if n := len(l) - len(strings.TrimLeft(l, " ")); cut < 0 || n < cut {
			cut = n
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= cut {
			out[i] = l[cut:]
		}
	}
	return strings.Join(out, "\n")
}

var (
	principleHead = regexp.MustCompile(`^## (P\d+) — (.+)$`)
	partLead      = regexp.MustCompile(`^\*\*(Rule|Prevents|Guard)\.\*\* ?`)
	mdLink        = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	mdCode        = regexp.MustCompile("`([^`]+)`")
	mdBold        = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdEm          = regexp.MustCompile(`\*([^*\s][^*]*)\*`)
)

// md renders the inline markdown docs/principles.md uses: code, links
// (relative ones point at the repository), bold, and *review-only* as its
// tag.
func md(s string) string {
	var codes []string
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		codes = append(codes, "<code>"+html.EscapeString(m[1:len(m)-1])+"</code>")
		return fmt.Sprintf("\x00%d\x00", len(codes)-1)
	})
	s = html.EscapeString(s)
	s = mdLink.ReplaceAllStringFunc(s, func(m string) string {
		p := mdLink.FindStringSubmatch(m)
		href := html.UnescapeString(p[2])
		if !strings.Contains(href, "://") {
			href = repoURL + "/blob/main/docs/" + href
		}
		return fmt.Sprintf(`<a href="%s">%s</a>`, html.EscapeString(href), p[1])
	})
	s = mdBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = strings.ReplaceAll(s, "*review-only*", `<span class="ro">review-only</span>`)
	s = mdEm.ReplaceAllString(s, "<em>$1</em>")
	for i, c := range codes {
		s = strings.Replace(s, fmt.Sprintf("\x00%d\x00", i), c, 1)
	}
	return s
}

// mdBlocks renders paragraphs and (nested) lists.
func mdBlocks(text string) string {
	var b strings.Builder
	for _, blk := range strings.Split(strings.TrimSpace(text), "\n\n") {
		lines := strings.Split(blk, "\n")
		if isItem(lines[0]) {
			b.WriteString(mdList(lines, 0))
		} else {
			fmt.Fprintf(&b, "<p>%s</p>", md(strings.Join(lines, " ")))
		}
	}
	return b.String()
}

var item = regexp.MustCompile(`^(\s*)(- |\d+\. )(.*)$`)

func isItem(l string) bool { return item.MatchString(l) }

// mdList renders the list items at indent in lines, with deeper ones nested.
func mdList(lines []string, indent int) string {
	tag := "ul"
	if m := item.FindStringSubmatch(lines[0]); m != nil && m[2] != "- " {
		tag = "ol"
	}
	var b strings.Builder
	b.WriteString("<" + tag + ">")
	for i := 0; i < len(lines); {
		m := item.FindStringSubmatch(lines[i])
		text := strings.TrimSpace(lines[i])
		if m != nil {
			text = m[3]
		}
		j := i + 1
		for j < len(lines) && !(isItem(lines[j]) && len(item.FindStringSubmatch(lines[j])[1]) <= indent) {
			if isItem(lines[j]) {
				break
			}
			text += " " + strings.TrimSpace(lines[j])
			j++
		}
		k := j
		for k < len(lines) && len(lines[k])-len(strings.TrimLeft(lines[k], " ")) > indent {
			k++
		}
		b.WriteString("<li>" + md(text))
		if k > j {
			b.WriteString(mdList(lines[j:k], indent+2))
		}
		b.WriteString("</li>")
		i = k
	}
	b.WriteString("</" + tag + ">")
	return b.String()
}

// principlesBody renders docs/principles.md: the introduction, a grid and
// table of contents, and one article per principle with its rule, what it
// prevents and its guard.
func principlesBody(t *testing.T, src []byte) string {
	parts := strings.Split(string(src), "\n## ")
	intro := strings.TrimSpace(strings.SplitN(parts[0], "\n", 2)[1])
	introParas := strings.Split(intro, "\n\n")
	type principle struct{ id, title, short, body string }
	var ps []principle
	for _, p := range parts[1:] {
		head, body, _ := strings.Cut("## "+p, "\n")
		m := principleHead.FindStringSubmatch(head)
		if m == nil {
			t.Fatalf("docs/principles.md: unexpected heading %q", head)
		}
		short, _, _ := strings.Cut(m[2], ", ")
		ps = append(ps, principle{m[1], m[2], short, body})
	}
	var grid, ticker, toc, arts strings.Builder
	for _, p := range ps {
		id := strings.ToLower(p.id)
		fmt.Fprintf(&grid, `<li><a href="#%s"><span class="pn">%s</span><span class="pt">%s</span></a></li>`, id, p.id, html.EscapeString(p.short))
		fmt.Fprintf(&ticker, `<b><i>%s</i> %s</b>`, p.id, html.EscapeString(p.short))
		fmt.Fprintf(&toc, "      <li><a href=\"#%s\">%s <span class=\"t t2\">%s</span></a></li>\n", id, p.id, html.EscapeString(p.short))
		fmt.Fprintf(&arts, "\n  <article class=\"prin\" id=\"%s\"><div class=\"big\">%s</div><div>\n    <h2>%s</h2>\n    <dl class=\"rpg\">\n", id, p.id, html.EscapeString(p.title))
		// Split the body at **Rule.**, **Prevents.** and **Guard.**.
		var label string
		var buf []string
		flush := func() {
			if label == "" {
				if strings.TrimSpace(strings.Join(buf, "\n")) != "" {
					t.Fatalf("docs/principles.md %s: text before **Rule.**", p.id)
				}
				return
			}
			cls := map[string]string{"Rule": "r", "Prevents": "p", "Guard": "g"}[label]
			fmt.Fprintf(&arts, "      <dt class=\"%s\">%s</dt><dd>%s</dd>\n", cls, label, mdBlocks(strings.Join(buf, "\n")))
		}
		for _, l := range strings.Split(p.body, "\n") {
			if m := partLead.FindStringSubmatch(l); m != nil {
				flush()
				label, buf = m[1], []string{l[len(m[0]):]}
				continue
			}
			buf = append(buf, l)
		}
		flush()
		arts.WriteString("    </dl>\n  </div></article>\n")
	}
	lede := md(strings.Join(strings.Fields(introParas[len(introParas)-1]), " "))
	meta := md(strings.Join(strings.Fields(introParas[0]), " "))
	n := len(ps)
	return fmt.Sprintf(`<div class="doc-head" id="top" data-hero="ci">
  <div class="wrap">
    <span class="num">docs/principles.md</span>
    <h1>Principles<span class="sq" aria-hidden="true"></span></h1>
    <p class="cicount" aria-hidden="true"><i class="cc-st"></i><b class="cc-n">%[1]d</b><span class="cc-of">/%[1]d</span> <span class="cc-l">rules, each with its guard</span></p>
    <p class="lede">%[2]s</p>
    <p class="meta">%[3]s This page renders <a href="%[7]s/blob/main/docs/principles.md"><code>docs/principles.md</code></a>.</p>
    <ol class="pgrid" aria-label="All principles">%[4]s</ol>
    <div class="ticker" aria-hidden="true"><span>%[5]s%[5]s</span></div>
  </div>
</div>

<div class="wrap doc">
  <nav class="toc" aria-label="Principles on this page">
    <h2>%[1]d rules</h2>
    <ol>
%[6]s    </ol>
  </nav>

  <div class="doc-body">
%[8]s
  <p class="more"><a href="%[7]s/blob/main/CONTRIBUTING.md">Contributing guide</a>: open an issue first, one pull request per issue, and every review checks the change against these rules.</p>
  </div>
</div>`, n, lede, meta, grid.String(), ticker.String(), toc.String(), repoURL, arts.String())
}

// TestSiteMatchesHelp is the drift test for the hand-written home page
// (#174), as TestSkill is for the skill: every command group and command in
// the help is on site/index.html, every group and command it names exists,
// each flag it shows a command with is one of that command's flags, and its
// list of shell submit statuses is the help's.
func TestSiteMatchesHelp(t *testing.T) {
	index := string(readFile(t, "../../site/index.html"))
	cliPage := string(readFile(t, "../../site/cli.html"))
	known := map[string]*cli.Command{}
	walk(cli.Root(), nil, func(path []string, c *cli.Command) {
		if len(path) > 0 {
			known[strings.Join(path, " ")] = c
		}
	})

	// Groups: the synopsis and the availability list name exactly the
	// groups of "aits help".
	var groups []string
	for _, c := range cli.Root().Children {
		if c.Setup == nil {
			groups = append(groups, c.Name)
		}
	}
	shown := matches(index, `<div class="gname">aits ([a-z]+)</div>`)
	if !slices.Equal(sorted(shown), sorted(groups)) {
		t.Errorf("index.html synopsis shows groups %q, the help has %q", shown, groups)
	}
	avail := matches(index, `<li(?: class="[^"]*")?><i></i>([a-z]+)(?: [a-z]+)? <em>`)
	for _, g := range avail {
		if !slices.Contains(groups, g) {
			t.Errorf("index.html lists %q as a command group; the help has no such group", g)
		}
	}
	if strings.Contains(index, "planned") {
		t.Error(`index.html says "planned": the site describes the current build only`)
	}

	// Commands: every leaf under a group is in the synopsis, and every
	// command the synopsis shows exists, with the flags it shows.
	for _, line := range matches(index, `(?s)<li><span class="c">(aits .*?)</li>`) {
		for _, seg := range regexp.MustCompile(`<span class="c">`).Split(line, -1) {
			text := html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(seg, ""))
			fields := strings.Fields(strings.TrimPrefix(text, "aits "))
			if len(fields) < 2 {
				continue
			}
			name := fields[0] + " " + fields[1]
			c := known[name]
			if c == nil {
				t.Errorf("index.html shows aits %s, which the help does not have", name)
				continue
			}
			flags := helpFlags(c)
			for _, f := range regexp.MustCompile(`--[a-z][a-z-]*`).FindAllString(text, -1) {
				if !slices.Contains(flags, f) {
					t.Errorf("index.html shows aits %s with %s, which is not one of its flags", name, f)
				}
			}
		}
	}
	for name, c := range known {
		if c.Setup == nil || !strings.Contains(name, " ") || name == "help" {
			continue
		}
		if !strings.Contains(index, `<span class="c">aits `+name+`</span>`) {
			t.Errorf("index.html synopsis is missing aits %s", name)
		}
	}

	// Statuses: the contract section lists shell submit's statuses.
	var want []string
	inStatuses := false
	for _, l := range strings.Split(call{args: []string{"shell", "submit", "--help"}}.run(t).stdout, "\n") {
		switch {
		case l == "Statuses:":
			inStatuses = true
		case inStatuses && l == "":
			inStatuses = false
		case inStatuses && strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   "):
			want = append(want, strings.Fields(l)[0])
		}
	}
	block := regexp.MustCompile(`<div class="statuses">(.*?)</div>`).FindStringSubmatch(index)
	if block == nil {
		t.Fatal(`index.html has no <div class="statuses">`)
	}
	if got := matches(block[1], `<code[^>]*>([a-z_]+)</code>`); !slices.Equal(got, want) {
		t.Errorf("index.html lists statuses %q, shell submit --help has %q", got, want)
	}

	// The generated reference has a section for every command and names
	// every flag.
	for name, c := range known {
		if !strings.Contains(cliPage, `id="`+strings.ReplaceAll(name, " ", "-")+`"`) {
			t.Errorf("cli.html has no section for aits %s", name)
		}
		for _, f := range helpFlags(c) {
			if !strings.Contains(cliPage, "<code>"+f+"</code>") {
				t.Errorf("cli.html does not name %s of aits %s", f, name)
			}
		}
	}
}

// helpFlags returns the flags c's help lists, such as "--json".
// Every command takes --json and --help; Setup only declares a leaf's own.
func helpFlags(c *cli.Command) []string {
	flags := []string{"--json", "--help"}
	if c.Setup != nil {
		fs := flag.NewFlagSet(c.Name, flag.ContinueOnError)
		c.Setup(fs)
		fs.VisitAll(func(f *flag.Flag) { flags = append(flags, "--"+f.Name) })
	}
	return flags
}

func matches(s, re string) []string {
	var out []string
	for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}
