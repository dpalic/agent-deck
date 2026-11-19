package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// How the per-session instructions files are delivered when the context block
// has to ride inside the launch command: which shape each file takes, what
// the session is told it got, and what happens when not even the short form
// fits. The budget is identityInlineMaxBytes (identity_injection.go).

// instructionsManyFiles returns n real files whose paths are long enough that
// their pointer sections alone cannot fit the inline budget, so the fallback
// is forced rather than simulated.
func instructionsManyFiles(t *testing.T, n int) []string {
	t.Helper()
	dir := t.TempDir()
	long := strings.Repeat("role-folder-with-a-very-long-name/", 4)
	paths := make([]string, 0, n)
	for idx := 0; idx < n; idx++ {
		deep := filepath.Join(dir, long, "level", string(rune('a'+idx%26)))
		if err := os.MkdirAll(deep, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(deep, "role-with-a-long-file-name.md")
		if err := os.WriteFile(path, []byte("# Role: many\nbody\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	return paths
}

// Point a: the fallback keeps the identity block. Before, a session whose
// pointer sections still overflowed lost the whole block, which it had before
// this feature existed at all.
func TestInstructionsFilesRemoteFallsBackToTheBaseBlock(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.SSHHost = "box"
	inst.InstructionsFiles = instructionsManyFiles(t, 40)

	text, ok := inst.identityRemoteText()

	if !ok {
		t.Fatal("the block was dropped; the base identity block must still be delivered")
	}
	if !strings.Contains(text, "# agent-deck session context") {
		t.Errorf("the base block is missing:\n%s", text)
	}
	if strings.Contains(text, InstructionsSectionHeader) {
		t.Errorf("no instructions section may be carried when none of them fits:\n%s", text)
	}
	if n := len(identityPrintfArg(text)); n > identityInlineMaxBytes {
		t.Errorf("the fallback itself is %d bytes, over the %d byte budget", n, identityInlineMaxBytes)
	}
}

// Point d: the budget is spent in the operator's order. Once one file is only
// named, every later one is named too, so the first file (the session's role)
// is the one that survives and the outcome never depends on the sizes further
// down the list.
func TestInstructionsFilesOnceNamedEveryLaterFileIsNamed(t *testing.T) {
	identityTestEnv(t)
	big := instructionsFileFixture(t, "role.md",
		"# Role: Develop\n"+strings.Repeat("the role text goes on and on. ", 400))
	small := instructionsFileFixture(t, "rules.md", "# Rules\nbe brief\n")
	inst := identityTestInstance("claude")
	inst.SSHHost = "box"
	inst.InstructionsFiles = []string{big, small}

	text, ok := inst.identityRemoteText()

	if !ok {
		t.Fatal("no block delivered")
	}
	for _, path := range []string{big, small} {
		if !strings.Contains(text, path) {
			t.Errorf("file %s is not named in the block:\n%s", path, text)
		}
	}
	if strings.Contains(text, "be brief") {
		t.Error("the second file was inlined although the first one was only named; in order means every later file is named too")
	}
}

// Point c: what the operator is told. A file that is only named must not be
// reported as delivered.
func TestInstructionsFilesStateSaysNamedOnly(t *testing.T) {
	identityTestEnv(t)
	// One file whose content does not fit, while its one pointer line does.
	big := instructionsFileFixture(t, "role.md",
		"# Role: Develop\n"+strings.Repeat("the role text goes on and on. ", 400))
	inst := identityTestInstance("claude")
	inst.SSHHost = "box"
	inst.InstructionsFiles = []string{big}

	reports := inst.InstructionsFilesState()

	if len(reports) != 1 {
		t.Fatalf("reports %+v, want one", reports)
	}
	if reports[0].State != "named only (inline budget)" {
		t.Errorf("state %q, want \"named only (inline budget)\"", reports[0].State)
	}
}

// Point c: and when not even the pointer lines fit, the session is told that
// instead of being told the files arrived.
func TestInstructionsFilesStateSaysNotDelivered(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.SSHHost = "box"
	inst.InstructionsFiles = instructionsManyFiles(t, 40)

	reports := inst.InstructionsFilesState()

	if len(reports) == 0 {
		t.Fatal("no reports")
	}
	for _, report := range reports {
		if report.State != "not delivered (inline budget)" {
			t.Errorf("%s: state %q, want \"not delivered (inline budget)\"", report.Path, report.State)
		}
	}
}

// Point c: a session that receives the files as content keeps saying so.
func TestInstructionsFilesStateStaysOKForALocalSession(t *testing.T) {
	identityTestEnv(t)
	path := instructionsFileFixture(t, "role.md", "# Role: Develop\nshort\n")
	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{path}

	reports := inst.InstructionsFilesState()

	if len(reports) != 1 || reports[0].State != "ok" {
		t.Errorf("reports %+v, want one \"ok\"", reports)
	}
}

// Point g: the reason already names the file, so the section must not repeat
// it.
func TestInstructionsFileUnreadableNamesThePathOnce(t *testing.T) {
	identityTestEnv(t)
	missing := filepath.Join(t.TempDir(), "gone.md")
	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{missing}

	section := inst.instructionsOneSection(missing, false)

	if got := strings.Count(section, missing); got != 1 {
		t.Errorf("the path appears %d times, want once:\n%s", got, section)
	}
}

// Point b: the local codex override is measured once, in the unit it is
// finally sent in. TOML escaping grows quotes and backslashes, so a value
// that is under the budget as a printf argument can be over it as TOML.
func TestCodexOverrideIsMeasuredAsTheValueItSends(t *testing.T) {
	identityTestEnv(t)
	heavy := "# Role: Develop\n" + strings.Repeat(`a "quoted" \ word `, 240)
	path := instructionsFileFixture(t, "role.md", heavy)
	inst := identityTestInstance("codex")
	inst.InstructionsFiles = []string{path}

	value, ok := inst.codexIdentityOverride(t.TempDir(), "")

	if !ok {
		t.Fatal("override not built")
	}
	if len(value) > identityInlineMaxBytes {
		t.Errorf("the value sent is %d bytes, over the %d byte budget; it must be measured as TOML, not as a printf argument",
			len(value), identityInlineMaxBytes)
	}
	if !strings.Contains(tomlRoundTrip(t, value), "# agent-deck session context") {
		t.Errorf("the base block is missing from the override:\n%s", value)
	}
}

// Point b: the operator's own developer_instructions count towards the same
// budget, because codex receives the prefix and the block as one value.
func TestCodexOverrideCountsTheOperatorPrefix(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	// Big enough to decide the outcome, small enough that the named form
	// still fits: that is what makes this a test of the prefix being counted.
	prefix := "# My own instructions\n" + strings.Repeat("keep this in mind, please. ", 40)
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"),
		[]byte("developer_instructions = "+tomlBasicString(prefix)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := instructionsFileFixture(t, "role.md",
		"# Role: Develop\n"+strings.Repeat("do the work. ", 600))
	inst := identityTestInstance("codex")
	inst.InstructionsFiles = []string{path}

	value, ok := inst.codexIdentityOverride(codexHome, "")

	if !ok {
		t.Fatal("override not built")
	}
	if len(value) > identityInlineMaxBytes {
		t.Errorf("the value sent is %d bytes, over the %d byte budget with the operator prefix counted",
			len(value), identityInlineMaxBytes)
	}
	got := tomlRoundTrip(t, value)
	if !strings.HasPrefix(got, prefix) {
		t.Error("the operator's own developer_instructions must still lead")
	}
	if strings.Contains(got, "do the work. do the work.") {
		t.Error("the file was inlined although the value does not fit; it must be named instead")
	}
}

// Point b: when even the named form does not fit, the base block is sent
// without any instructions section, as in point a.
func TestCodexOverrideFallsBackToTheBaseBlock(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("codex")
	inst.InstructionsFiles = instructionsManyFiles(t, 40)

	value, ok := inst.codexIdentityOverride(t.TempDir(), "")

	if !ok {
		t.Fatal("override not built")
	}
	if len(value) > identityInlineMaxBytes {
		t.Errorf("the fallback value is %d bytes, over the %d byte budget", len(value), identityInlineMaxBytes)
	}
	got := tomlRoundTrip(t, value)
	if !strings.Contains(got, "# agent-deck session context") {
		t.Errorf("the base block is missing:\n%s", got)
	}
	if strings.Contains(got, InstructionsSectionHeader) {
		t.Errorf("no instructions section fits, so none may be sent:\n%s", got)
	}
}
