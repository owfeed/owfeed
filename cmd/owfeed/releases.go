package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"owfeed.org/owfeed/internal/arch"
	"owfeed.org/owfeed/internal/config"
)

// releases reports what owfeed believes about OpenWrt's published releases:
// which point release is newest on each line, and which package format that line
// takes.
//
// It exists to be compared against. owlab answers the same two questions from
// the same server and neither tool reads the other, which is deliberate — a
// shared library between them would be the code-level dependency both designs
// refuse. The cost of that choice is drift, and drift is only tolerable while it
// is observable, so there has to be a command that states owfeed's answer in a
// form a nightly job can diff against `owlab releases --all --json`.
//
// Everything else that knows this is either internal (arch.LatestPoint) or
// refuses to run outside a feed repository (`owfeed lock`), and a cross-check
// that has to be run from inside one of the repositories it checks is not much of
// a cross-check.
func (a *app) releases(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("owfeed releases", flag.ContinueOnError)
	fs.SetOutput(a.err)
	asJSON := fs.Bool("json", false, "write the answer as JSON")
	var lines multiFlag
	fs.Var(&lines, "line", "release line to report, e.g. 25.12. Repeatable. Default: every line upstream publishes")
	if err := fs.Parse(args); err != nil {
		return wrap(exitConfig, err)
	}

	if a.noNetwork {
		return fail(exitUpstream, "owfeed releases asks the download server what it publishes; "+
			"there is no cached answer to give under --no-network")
	}

	published, err := arch.Versions(ctx, upstreamHTTP)
	if err != nil {
		return wrapUpstream(err)
	}

	want := []string(lines)
	if len(want) == 0 {
		want = linesOf(published)
	}

	type lineDoc struct {
		Line   string `json:"line"`
		Point  string `json:"point"`
		Format string `json:"format"`
	}
	out := make([]lineDoc, 0, len(want))
	for _, line := range want {
		point, err := arch.LatestPointIn(line, published)
		if err != nil {
			// Reported, not fatal: a line the caller named that upstream has no
			// final release for is an answer, and it should not cost the answer
			// for the lines that do.
			fmt.Fprintf(a.err, "! %s: %v\n", line, err)
			continue
		}
		out = append(out, lineDoc{Line: line, Point: point, Format: FormatFor(line)})
	}

	if *asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Schema string    `json:"schema"`
			Source string    `json:"source"`
			Lines  []lineDoc `json:"lines"`
		}{"owfeed.releases/v1", arch.VersionsURL, out})
	}

	for _, l := range out {
		fmt.Fprintf(a.out, "%-10s %-10s %s\n", l.Line, l.Point, l.Format)
	}
	return nil
}

// FormatFor is the apk/opkg verdict for a release line.
//
// OpenWrt switched package managers in 25.12, and this is the one rule owfeed and
// owlab genuinely share. It is stated here as a function rather than left to the
// `format:` field in owfeed.yml because the field records what a feed was
// configured for, and a cross-check needs what upstream actually did.
func FormatFor(line string) string {
	if line == "snapshot" {
		return config.FormatAPK
	}
	major, minor, ok := splitLine(line)
	if !ok {
		return ""
	}
	if major > 25 || (major == 25 && minor >= 12) {
		return config.FormatAPK
	}
	return config.FormatIPK
}

func splitLine(line string) (int, int, bool) {
	a, b, ok := strings.Cut(line, ".")
	if !ok {
		return 0, 0, false
	}
	major, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(b)
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// linesOf reduces a list of point releases to the lines they belong to, newest
// first. Release candidates carry no line of their own — a feed pinned to an -rc
// is pinned to something that will stop existing.
func linesOf(published []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range published {
		major, minor, ok := splitPoint(v)
		if !ok {
			continue
		}
		line := fmt.Sprintf("%02d.%02d", major, minor)
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// splitPoint reports the line a final point release belongs to. Anything whose
// patch is not a bare number -- "25.12.0-rc1" -- is not a release and has no line.
func splitPoint(v string) (int, int, bool) {
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	if _, err := strconv.Atoi(parts[2]); err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// multiFlag collects a repeatable string flag.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, " ") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
