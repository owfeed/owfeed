package main

import (
	"strings"
	"testing"
)

// The dry-run job indexes with --throwaway-key; the publish job never does, or an
// unrecorded rotation would publish the new key under the old keyring version.
func TestFeedWorkflowSkipsTheKeyComparisonOnlyInTheDryRun(t *testing.T) {
	jobs := workflowJobs(t, "feed.yml")
	for job, want := range map[string]bool{"check": true, "publish": false} {
		steps := jobs[job].Steps
		if len(steps) == 0 {
			t.Fatalf("feed.yml has no %s job", job)
		}
		indexes := 0
		for _, s := range steps {
			if !strings.Contains(s.Run, "owfeed index") {
				continue
			}
			indexes++
			if got := strings.Contains(s.Run, "--throwaway-key"); got != want {
				t.Errorf("%s job, step %q: --throwaway-key is %v, want %v", job, s.Name, got, want)
			}
		}
		if indexes == 0 {
			t.Errorf("the %s job runs no `owfeed index`", job)
		}
	}
}
