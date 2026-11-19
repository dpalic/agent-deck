package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Per-session instructions file on the CLI: a flag on `add` and `launch`, a
// `session set` field, visible in `session show --json` and in the text
// `session primer` prints.

// instructionsFileHome prepares a temp home with a project directory and a
// role file, and returns both paths.
func instructionsFileHome(t *testing.T, body string) (home, projectDir, rolePath string) {
	t.Helper()
	home = t.TempDir()
	projectDir = filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rolePath = filepath.Join(home, "develop.md")
	if err := os.WriteFile(rolePath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home, projectDir, rolePath
}

// instructionsAddSession runs `add --json` and returns the new session's id.
func instructionsAddSession(t *testing.T, home string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, args...)
	if code != 0 {
		t.Fatalf("agent-deck %s failed (exit %d)\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned an empty id; stdout: %s", stdout)
	}
	return resp.ID
}

// `agent-deck add --instructions-file <path>` persists the path, and
// `session primer --json` shows the file's content in the text the harness
// receives.
//
// Failure mode before the feature:
//
//	flag provided but not defined: -instructions-file
func TestAddInstructionsFileFlagAndPrimer(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, projectDir, rolePath := instructionsFileHome(t, "# Role: Develop\nYou write the code.\n")

	id := instructionsAddSession(t, home,
		"add", "-t", "if-add-test", "-c", "claude",
		"--instructions-file", rolePath,
		"--no-parent", "--json", projectDir,
	)

	if got := showInstructionsFile(t, home, id); got != rolePath {
		t.Errorf("session show --json .instructions_file = %q, want %q", got, rolePath)
	}

	stdout, stderr, code := runAgentDeck(t, home, "session", "primer", "--json", id)
	if code != 0 {
		t.Fatalf("session primer failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var primer struct {
		Text                  string `json:"text"`
		InstructionsFile      string `json:"instructions_file"`
		InstructionsFileState string `json:"instructions_file_state"`
	}
	if err := json.Unmarshal([]byte(stdout), &primer); err != nil {
		t.Fatalf("parse primer response: %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(primer.Text, "# Role: Develop") || !strings.Contains(primer.Text, "You write the code.") {
		t.Errorf("primer text does not carry the role file:\n%s", primer.Text)
	}
	if primer.InstructionsFile != rolePath {
		t.Errorf("primer .instructions_file = %q, want %q", primer.InstructionsFile, rolePath)
	}
	if primer.InstructionsFileState != "ok" {
		t.Errorf("primer .instructions_file_state = %q, want \"ok\"", primer.InstructionsFileState)
	}
}

// `session set <id> instructions-file <path>` sets it on an existing session
// and an empty value clears it.
func TestSessionSetInstructionsFile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, projectDir, rolePath := instructionsFileHome(t, "# Role: Review\n")

	id := instructionsAddSession(t, home,
		"add", "-t", "if-set-test", "-c", "claude", "--no-parent", "--json", projectDir,
	)

	stdout, stderr, code := runAgentDeck(t, home, "session", "set", "--json", id, "instructions-file", rolePath)
	if code != 0 {
		t.Fatalf("session set instructions-file failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got := showInstructionsFile(t, home, id); got != rolePath {
		t.Errorf("after set, session show --json .instructions_file = %q, want %q", got, rolePath)
	}

	if _, stderr, code = runAgentDeck(t, home, "session", "set", "--json", id, "instructions-file", ""); code != 0 {
		t.Fatalf("clearing instructions-file failed (exit %d)\nstderr: %s", code, stderr)
	}
	if got := showInstructionsFile(t, home, id); got != "" {
		t.Errorf("an empty value must clear the field, got %q", got)
	}
}

// showInstructionsFile reads .instructions_file from `session show --json`.
func showInstructionsFile(t *testing.T, home, id string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", "--json", id)
	if code != 0 {
		t.Fatalf("session show failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var show struct {
		InstructionsFile string `json:"instructions_file"`
	}
	if err := json.Unmarshal([]byte(stdout), &show); err != nil {
		t.Fatalf("parse show response: %v\nstdout: %s", err, stdout)
	}
	return show.InstructionsFile
}

// A missing file never fails the spawn or the primer: the session is created,
// and `session primer` says the file could not be read.
func TestInstructionsFileMissingIsReportedNotFatal(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, projectDir, _ := instructionsFileHome(t, "unused\n")
	gone := filepath.Join(home, "no-such-role.md")

	id := instructionsAddSession(t, home,
		"add", "-t", "if-missing-test", "-c", "claude",
		"--instructions-file", gone,
		"--no-parent", "--json", projectDir,
	)

	stdout, stderr, code := runAgentDeck(t, home, "session", "primer", "--json", id)
	if code != 0 {
		t.Fatalf("session primer failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var primer struct {
		Text                  string `json:"text"`
		InstructionsFileState string `json:"instructions_file_state"`
	}
	if err := json.Unmarshal([]byte(stdout), &primer); err != nil {
		t.Fatalf("parse primer response: %v\nstdout: %s", err, stdout)
	}
	if !strings.HasPrefix(primer.InstructionsFileState, "unreadable") {
		t.Errorf("primer .instructions_file_state = %q, want it to start with \"unreadable\"", primer.InstructionsFileState)
	}
	if !strings.Contains(primer.Text, "You are running inside agent-deck") {
		t.Errorf("the base block must still be there:\n%s", primer.Text)
	}
}

// The creation catalog `add --capabilities --json` reports the flag for both
// commands, so a caller can tell a build with the field from one without it.
func TestInstructionsFileInCreationCapabilities(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	stdout, stderr, code := runAgentDeck(t, home, "add", "--capabilities", "--json")
	if code != 0 {
		t.Fatalf("add --capabilities --json failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var catalog struct {
		Commands map[string][]struct {
			Name       string `json:"name"`
			TakesValue bool   `json:"takes_value"`
			Repeatable bool   `json:"repeatable"`
		} `json:"commands"`
	}
	if err := json.Unmarshal([]byte(stdout), &catalog); err != nil {
		t.Fatalf("parse catalog: %v\nstdout: %s", err, stdout)
	}
	for _, command := range []string{"add", "launch"} {
		var found bool
		for _, f := range catalog.Commands[command] {
			if f.Name == "instructions-file" {
				found = true
				if !f.TakesValue {
					t.Errorf("%s: instructions-file must take a value", command)
				}
				// The mark is how a client
				// tells this build from the one that takes a single file.
				if !f.Repeatable {
					t.Errorf("%s: instructions-file must be reported as repeatable", command)
				}
			}
		}
		if !found {
			t.Errorf("%s: the catalog does not report instructions-file", command)
		}
	}
}

// instructionsPrimerJSON is what `session primer --json` reports about the
// session's instructions files: the two pre-list scalars and the per-file list.
type instructionsPrimerJSON struct {
	Text                  string `json:"text"`
	InstructionsFile      string `json:"instructions_file"`
	InstructionsFileState string `json:"instructions_file_state"`
	InstructionsFiles     []struct {
		Path  string `json:"path"`
		State string `json:"state"`
	} `json:"instructions_files"`
}

// instructionsPrimer runs `session primer --json` and parses it.
func instructionsPrimer(t *testing.T, home, id string) instructionsPrimerJSON {
	t.Helper()
	var primer instructionsPrimerJSON
	stdout, stderr, code := runAgentDeck(t, home, "session", "primer", "--json", id)
	if code != 0 {
		t.Fatalf("session primer failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &primer); err != nil {
		t.Fatalf("parse primer response: %v\nstdout: %s", err, stdout)
	}
	return primer
}

// Several files, given as two occurrences of the flag and then as one comma
// separated value: both reach the session in order, `session primer` reports
// one state per file, and the block carries a section per file.
//
// Red before the change: the second occurrence of the flag replaced the first,
// and the primer had no `instructions_files` at all.
func TestAddSeveralInstructionsFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, projectDir, rolePath := instructionsFileHome(t, "# Role: Develop\nyour role\n")
	comms := filepath.Join(home, "team-rules.md")
	if err := os.WriteFile(comms, []byte("how to report\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	id := instructionsAddSession(t, home,
		"add", "-t", "if-several", "-c", "claude",
		"--instructions-file", rolePath,
		"--instructions-file", comms,
		"--no-parent", "--json", projectDir,
	)

	primer := instructionsPrimer(t, home, id)
	if len(primer.InstructionsFiles) != 2 {
		t.Fatalf("primer reports %d files, want 2: %+v", len(primer.InstructionsFiles), primer.InstructionsFiles)
	}
	if primer.InstructionsFiles[0].Path != rolePath || primer.InstructionsFiles[1].Path != comms {
		t.Errorf("order lost: %+v", primer.InstructionsFiles)
	}
	for _, report := range primer.InstructionsFiles {
		if report.State != "ok" {
			t.Errorf("file %s: state %q, want ok", report.Path, report.State)
		}
	}
	if primer.InstructionsFile != rolePath {
		t.Errorf("the scalar is %q, want the first file %q", primer.InstructionsFile, rolePath)
	}
	if !strings.Contains(primer.Text, "your role") || !strings.Contains(primer.Text, "how to report") {
		t.Errorf("the block does not carry both files:\n%s", primer.Text)
	}
	if got := showInstructionsFile(t, home, id); got != rolePath {
		t.Errorf("session show --json .instructions_file = %q, want the first file", got)
	}

	// One comma separated value on the existing session REPLACES the list.
	if _, stderr, code := runAgentDeck(t, home, "session", "set", "--json", id,
		"instructions-file", comms+","+rolePath); code != 0 {
		t.Fatalf("session set with a comma list failed (exit %d)\nstderr: %s", code, stderr)
	}
	primer = instructionsPrimer(t, home, id)
	if len(primer.InstructionsFiles) != 2 || primer.InstructionsFiles[0].Path != comms {
		t.Errorf("the list was not replaced in the given order: %+v", primer.InstructionsFiles)
	}
}

// A path that contains the separator cannot be named: the flag and the field
// both refuse it, and the message explains why (addition B).
func TestInstructionsFileWithACommaIsRefused(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, projectDir, _ := instructionsFileHome(t, "# Role: Develop\n")

	_, stderr, code := runAgentDeck(t, home, "add", "-t", "if-comma", "-c", "claude",
		"--instructions-file", "/roles/develop,draft.md", "--no-parent", "--json", projectDir)
	if code == 0 {
		t.Fatalf("the flag accepted a path with a comma")
	}
	if !strings.Contains(stderr, ",") {
		t.Errorf("the flag's message does not explain the separator: %s", stderr)
	}

	id := instructionsAddSession(t, home,
		"add", "-t", "if-comma-set", "-c", "claude", "--no-parent", "--json", projectDir)
	_, stderr, code = runAgentDeck(t, home, "session", "set", "--json", id,
		"instructions-file", "/roles/develop,draft.md")
	if code == 0 {
		t.Fatalf("the field accepted a path with a comma")
	}
	if got := showInstructionsFile(t, home, id); got != "" {
		t.Errorf("a refused value must not be stored, got %q", got)
	}
}
