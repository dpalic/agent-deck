package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A fork and a cross-harness switch continue the job of the session they come
// from, so they carry its instructions files (#3206). A `launch` child is a
// different job and starts without them.

// instructionsInheritSource is a session of `tool` that really can be forked:
// each harness needs its own on-disk trace, seeded with the helpers the fork
// tests of that harness already use.
func instructionsInheritSource(t *testing.T, tool string) (*Instance, []string) {
	t.Helper()
	home := identityTestEnv(t)
	src := NewInstanceWithTool("src", t.TempDir(), tool)
	src.ID = "src01-1700000000"
	switch tool {
	case "claude":
		const sid = "11111111-2222-3333-4444-555555555555"
		src.ClaudeSessionID = sid
		seedClaudeJSONL(t, src, sid, 1, 0)
	case "codex":
		const sid = "22222222-3333-4444-5555-666666666666"
		codexHome := filepath.Join(home, ".codex")
		if err := os.MkdirAll(codexHome, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CODEX_HOME", codexHome)
		seedCodexRollout(t, codexHome, sid)
		src.CodexSessionID = sid
		src.CodexDetectedAt = time.Now()
	case "pi":
		// A remote Pi session is forkable without a local trace: the launch
		// command validates on the target side.
		src.SSHHost = "box"
	}
	role := instructionsFileFixture(t, "develop.md", "# Role: Develop\nyou write the code\n")
	rules := instructionsFileFixture(t, "rules.md", "# Rules\nbe brief\n")
	if _, _, err := SetField(src, FieldInstructionsFile, role+","+rules, nil); err != nil {
		t.Fatal(err)
	}
	return src, []string{role, rules}
}

func instructionsEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for idx := range want {
		if got[idx] != want[idx] {
			return false
		}
	}
	return true
}

// Every harness's fork path carries the files, in order.
func TestForkCarriesTheInstructionsFiles(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		fork       func(*Instance) (*Instance, string, error)
	}{
		{"claude", "claude", func(src *Instance) (*Instance, string, error) {
			return src.CreateForkedInstanceWithOptions("fork", "", nil)
		}},
		{"pi", "pi", func(src *Instance) (*Instance, string, error) {
			return src.CreateForkedPiInstanceWithOptions("fork", "", nil)
		}},
		{"codex", "codex", func(src *Instance) (*Instance, string, error) {
			return src.CreateForkedCodexInstanceWithOptions("fork", "", nil)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, want := instructionsInheritSource(t, tc.tool)

			forked, _, err := tc.fork(src)
			if err != nil {
				t.Fatalf("fork: %v", err)
			}
			if forked == nil {
				t.Fatal("no forked instance")
			}
			if !instructionsEqual(forked.InstructionsFiles, want) {
				t.Errorf("fork files %q, want %q", forked.InstructionsFiles, want)
			}
		})
	}
}

// The files are on the fork before its start command is baked, so its first
// spawn carries them and not only the spawn after a restart.
func TestForkCommandCarriesTheInstructionsFilesOnTheFirstStart(t *testing.T) {
	src, want := instructionsInheritSource(t, "claude")

	forked, cmd, err := src.CreateForkedInstanceWithOptions("fork", "", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !instructionsEqual(forked.InstructionsFiles, want) {
		t.Fatalf("fork files %q, want %q", forked.InstructionsFiles, want)
	}
	// The block the fork itself renders must name the role file: that is what
	// a spawn of the fork delivers.
	text, ok := forked.identityText()
	if !ok {
		t.Fatal("the fork has no context block")
	}
	if !strings.Contains(text, want[0]) {
		t.Errorf("the fork's block does not carry %s:\n%s", want[0], text)
	}
	if cmd == "" {
		t.Error("no fork command")
	}
}

// A cross-harness switch (claude to codex, for example) is the same session
// with a new harness, so the target receives the files too.
func TestCrossHarnessSwitchTargetCarriesTheInstructionsFiles(t *testing.T) {
	src, want := instructionsInheritSource(t, "claude")

	target := newCrossHarnessTarget(&FreshTargetLaunchPlan{
		Target: FreshTargetIdentity{
			InstanceID:  "tgt01-1700000001",
			Title:       src.Title,
			ProjectPath: src.ProjectPath,
			GroupPath:   src.GroupPath,
			Tool:        "codex",
		},
	}, src)

	if !instructionsEqual(target.InstructionsFiles, want) {
		t.Errorf("switch target files %q, want %q", target.InstructionsFiles, want)
	}
	if target.Tool != "codex" {
		t.Errorf("target tool %q, want codex", target.Tool)
	}
}

// A launch child has its own job: it starts without the parent's files.
func TestALaunchChildDoesNotInheritTheInstructionsFiles(t *testing.T) {
	src, _ := instructionsInheritSource(t, "claude")

	child := NewInstanceWithTool("child", src.ProjectPath, "claude")

	if len(child.InstructionsFiles) != 0 {
		t.Errorf("a fresh child carries %q, want none", child.InstructionsFiles)
	}
	if len(src.InstructionsFiles) != 2 {
		t.Error("the source lost its own files")
	}
}
