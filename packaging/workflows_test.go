package packaging

// The workflow files have no Go package of their own, and this one already runs
// in CI with a YAML parser to hand, so the repository-level assertions live
// here.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type workflow struct {
	Jobs map[string]struct {
		If    string `yaml:"if"`
		Steps []struct {
			Name string `yaml:"name"`
			ID   string `yaml:"id"`
			If   string `yaml:"if"`
			Uses string `yaml:"uses"`
			Run  string `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func workflowFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("../.github/workflows/*.yml")
	if err != nil {
		t.Fatal(err)
	}
	extra, err := filepath.Glob("*/.github/workflows/*.yml") // the tap and the bucket
	if err != nil {
		t.Fatal(err)
	}
	all := append(matches, extra...)
	if len(all) < 3 {
		t.Fatalf("only found %v; the glob is wrong", all)
	}
	return all
}

func loadWorkflow(t *testing.T, path string) workflow {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var w workflow
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatalf("%s does not parse: %v", path, err)
	}
	return w
}

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// A third-party action referenced by tag can be moved to different code without
// the reference changing. Every one of them is pinned to a commit; this is the
// check that keeps it that way, in this repository and in the two it ships.
func TestEveryActionIsPinnedToACommit(t *testing.T) {
	for _, path := range workflowFiles(t) {
		for jobName, job := range loadWorkflow(t, path).Jobs {
			for _, step := range job.Steps {
				if step.Uses == "" || strings.HasPrefix(step.Uses, "./") {
					continue
				}
				ref := step.Uses
				at := strings.LastIndex(ref, "@")
				if at < 0 {
					t.Errorf("%s: %s: %q has no version at all", path, jobName, ref)
					continue
				}
				if !sha40.MatchString(ref[at+1:]) {
					t.Errorf("%s: %s: %q is pinned to a tag, not a commit",
						path, jobName, ref)
				}
			}
		}
	}
}

// Publishing an apt repository whose packages are unsigned would hand out a
// .repo file with gpgcheck=1 that no machine can satisfy. Both halves are gated
// on the signing key being present, and neither gate may quietly disappear.
func TestThePackageRepositoryIsGatedOnTheSigningKey(t *testing.T) {
	w := loadWorkflow(t, "../.github/workflows/release.yml")

	build, ok := w.Jobs["build"]
	if !ok {
		t.Fatal("the release workflow has no build job")
	}
	var keyStep bool
	gated := map[string]bool{}
	for _, step := range build.Steps {
		if step.ID == "key" {
			keyStep = true
		}
		for _, guarded := range []string{
			"Collect the packages of recent releases",
			"Build the apt and dnf repository",
			"Upload the repository as a Pages artifact",
		} {
			if step.Name == guarded {
				gated[guarded] = strings.Contains(step.If, "steps.key.outputs.signing")
			}
		}
	}
	if !keyStep {
		t.Error("no step with id \"key\"; the gate has nothing to read")
	}
	for _, name := range []string{
		"Collect the packages of recent releases",
		"Build the apt and dnf repository",
		"Upload the repository as a Pages artifact",
	} {
		present, found := gated[name]
		if !found {
			t.Errorf("the step %q is gone", name)
			continue
		}
		if !present {
			t.Errorf("the step %q is no longer gated on the signing key", name)
		}
	}

	pages, ok := w.Jobs["pages"]
	if !ok {
		t.Fatal("the release workflow no longer has a pages job")
	}
	if !strings.Contains(pages.If, "needs.build.outputs.signing") {
		t.Errorf("the pages job is gated on %q, which does not mention the signing key",
			pages.If)
	}
}

// The scripts the workflow calls have to be there and be executable, or the
// release fails after the tag exists.
func TestReleaseWorkflowScriptsExist(t *testing.T) {
	b, err := os.ReadFile("../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, script := range []string{
		"packaging/build-packages.sh",
		"packaging/generate-manifests.sh",
		"packaging/build-repo.sh",
		"packaging/fetch-release-packages.sh",
	} {
		if !strings.Contains(body, script) {
			t.Errorf("the release workflow no longer calls %s", script)
		}
		info, err := os.Stat(filepath.Join("..", script))
		if err != nil {
			t.Errorf("%s: %v", script, err)
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable (mode %04o)", script, info.Mode().Perm())
		}
	}
}
