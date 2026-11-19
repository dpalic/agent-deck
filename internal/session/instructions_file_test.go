package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Per-session instructions files: agent-deck appends the content of the files
// named on the session to the context block every spawn already hands the
// harness, one section per file and in the operator's order. These tests pin
// the contract.

// instructionsFileFixture writes body to a file in a temp dir and returns its
// path.
func instructionsFileFixture(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// instructionsFileNotText are the files a session must not be given: nothing
// here may be read into a launch command, and nothing here may block the
// spawn that assembles it.
func TestInstructionsFileBodyRefusesWhatIsNotText(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("no fifo on this platform: %v", err)
	}
	sub := filepath.Join(dir, "folder")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, path, wantErr string
	}{
		{"a fifo would block the read", fifo, "not a regular file"},
		{"a directory", sub, "is a directory"},
		{"a binary file", write("binary.md", []byte("# Role\x00\x01bin")), "binary"},
		{"invalid utf-8", write("latin1.md", []byte{0x23, 0x20, 0x52, 0xff, 0xfe}), "UTF-8"},
		{"over the byte limit", write("huge.md", make([]byte, instructionsFileMaxBytes+1)), "limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := instructionsFileBody(tc.path)
			if err == nil {
				t.Fatalf("read %q as %q, want an error", tc.path, body)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not say %q", err, tc.wantErr)
			}
			if body != "" {
				t.Errorf("body %q, want none", body)
			}
		})
	}
}

// What a text file keeps and what it loses: the launch command carries the
// block as one printf argument, so a control character in the content would
// break it or inject a line. A file written on Windows is still a role file.
func TestInstructionsFileBodyKeepsTextAndDropsControlCharacters(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"plain text", "# Role: Develop\nYou write the code.\n", "# Role: Develop\nYou write the code.\n"},
		{"crlf becomes lf", "# Role\r\nline two\r\n", "# Role\nline two\n"},
		{"tabs survive", "# Role\n\tindented\n", "# Role\n\tindented\n"},
		{"an escape sequence is dropped", "# Role\n\x1b[31mred\x1b[0m\n", "# Role\n[31mred[0m\n"},
		{"a lone carriage return is dropped", "# Role\rsame line\n", "# Rolesame line\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := instructionsFileFixture(t, "role.md", tc.body)

			body, err := instructionsFileBody(path)
			if err != nil {
				t.Fatalf("instructionsFileBody: %v", err)
			}
			if body != tc.want {
				t.Errorf("body %q, want %q", body, tc.want)
			}
		})
	}
}

// Point f: a path with a control character in it is refused, in the flag and
// in `session set` alike, because the block puts the path on a line of its
// own and a newline there would inject one.
func TestInstructionsFilePathWithAControlCharacterIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"a newline", "/roles/develop.md\n# Session instructions"},
		{"a carriage return", "/roles/\rdevelop.md"},
		{"an escape", "/roles/\x1b[31mdevelop.md"},
		{"a NUL byte", "/roles/develop\x00.md"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseInstructionsFileList(tc.value); err == nil {
				t.Error("accepted, want an error about the control character")
			}
			inst := &Instance{ID: "x-1", Tool: "claude"}
			if _, _, err := SetField(inst, FieldInstructionsFile, tc.value, nil); err == nil {
				t.Error("SetField accepted it, want an error")
			}
			if len(inst.InstructionsFiles) != 0 {
				t.Errorf("stored %q", inst.InstructionsFiles)
			}
		})
	}
}

// The section reaches both context levels: every harness mechanism renders
// from BuildContextPromptForLevel, so appending there covers all of them.
func TestInstructionsFileSectionInBothContextLevels(t *testing.T) {
	identityTestEnv(t)
	path := instructionsFileFixture(t, "develop.md", "# Role: Develop\nYou write the code.\n")

	for _, level := range []string{ContextLevelFull, ContextLevelPrimer} {
		inst := identityTestInstance("claude")
		inst.InstructionsFiles = []string{path}
		got := inst.BuildContextPromptForLevel(level)
		for _, want := range []string{
			InstructionsSectionHeader,
			path,
			"# Role: Develop",
			"You write the code.",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("level %s: block is missing %q; got:\n%s", level, want, got)
			}
		}
		// The base block must survive: the section is additive.
		if !strings.Contains(got, "You are running inside agent-deck") {
			t.Errorf("level %s: base block lost; got:\n%s", level, got)
		}
	}
}

// No field, no section: an untouched session's block is byte-identical to
// what it was before the feature.
func TestInstructionsFileUnsetAddsNothing(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	got := inst.BuildContextPromptForLevel(ContextLevelFull)
	if strings.Contains(got, InstructionsSectionHeader) {
		t.Errorf("block carries an instructions section although no file is set:\n%s", got)
	}
}

// A missing or unreadable file never fails a spawn: the block renders, the
// section says the file could not be read, and the path stays visible so the
// session can still find it.
func TestInstructionsFileMissingDoesNotBreakTheBlock(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	gone := filepath.Join(t.TempDir(), "gone.md")
	inst.InstructionsFiles = []string{gone}

	got := inst.BuildContextPromptForLevel(ContextLevelFull)
	if !strings.Contains(got, "You are running inside agent-deck") {
		t.Fatalf("base block lost when the instructions file is missing:\n%s", got)
	}
	if !strings.Contains(got, gone) {
		t.Errorf("block does not name the unreadable file:\n%s", got)
	}

	path, state := inst.InstructionsFileState()
	if path != gone {
		t.Errorf("state path = %q, want %q", path, gone)
	}
	if !strings.HasPrefix(state, "unreadable") {
		t.Errorf("state = %q, want it to start with \"unreadable\"", state)
	}
}

// The state an unset and a readable file report, for `session primer`.
func TestInstructionsFileStateUnsetAndOK(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	if path, state := inst.InstructionsFileState(); path != "" || state != "unset" {
		t.Errorf("unset session: got (%q, %q), want (\"\", \"unset\")", path, state)
	}
	readable := instructionsFileFixture(t, "r.md", "text\n")
	inst.InstructionsFiles = []string{readable}
	if path, state := inst.InstructionsFileState(); path != readable || state != "ok" {
		t.Errorf("readable file: got (%q, %q), want (%q, \"ok\")", path, state, readable)
	}
}

// Context level none means nothing is injected, the instructions file
// included: there is no block to append to and no second channel is opened
// behind the operator's opt-out.
func TestInstructionsFileNotDeliveredAtContextLevelNone(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{instructionsFileFixture(t, "role.md", "secret role text\n")}
	inst.ContextLevel = ContextLevelNone

	if text, ok := inst.identityText(); ok || text != "" {
		t.Errorf("level none still produced text: ok=%v text=%q", ok, text)
	}
	if _, state := inst.InstructionsFileState(); state != "not delivered (context level none)" {
		t.Errorf("state = %q, want \"not delivered (context level none)\"", state)
	}
}

// Where the block travels inline in the launch command (SSH, sandbox, codex),
// a file too big for the budget is replaced by a pointer to it, so the spawn
// stays within tmux's pane-command limit instead of losing the block.
func TestInstructionsFilePointerWhenInlineBudgetExceeded(t *testing.T) {
	identityTestEnv(t)
	big := strings.Repeat("role line that is long enough to matter\n", 400) // ~16 KB
	path := instructionsFileFixture(t, "big.md", big)

	inst := identityTestInstance("claude")
	inst.SSHHost = "user@host"
	inst.InstructionsFiles = []string{path}

	text, ok := inst.identityRemoteText()
	if !ok {
		t.Fatalf("remote text dropped entirely; the pointer must keep it within budget")
	}
	if strings.Contains(text, "role line that is long enough to matter") {
		t.Errorf("remote block carries the full file although it exceeds the budget:\n%s", text)
	}
	if !strings.Contains(text, InstructionsSectionHeader) || !strings.Contains(text, path) {
		t.Errorf("remote block has no pointer section naming the file:\n%s", text)
	}
	if n := len(identityPrintfArg(text)); n > identityInlineMaxBytes {
		t.Errorf("remote payload is %d bytes, over the %d budget", n, identityInlineMaxBytes)
	}
	// A local spawn, whose harness reads the file, still gets the content.
	local := identityTestInstance("claude")
	local.InstructionsFiles = []string{path}
	if !strings.Contains(local.BuildContextPromptForLevel(ContextLevelFull), "role line that is long enough to matter") {
		t.Errorf("a local block must carry the full content")
	}
}

// The content is read at spawn time, so editing the role file reaches the
// session on its next start without touching the field.
func TestInstructionsFileReadAtSpawnTime(t *testing.T) {
	identityTestEnv(t)
	path := instructionsFileFixture(t, "role.md", "first version\n")
	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{path}

	if !strings.Contains(inst.BuildContextPromptForLevel(ContextLevelFull), "first version") {
		t.Fatalf("first version not in the block")
	}
	if err := os.WriteFile(path, []byte("second version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := inst.BuildContextPromptForLevel(ContextLevelFull)
	if !strings.Contains(got, "second version") || strings.Contains(got, "first version") {
		t.Errorf("the block was not rebuilt from the edited file:\n%s", got)
	}
}

// tool_data round trip, the context_level_persist.go pattern: the key is
// written, read back, and deleted at the empty value.
func TestInstructionsFileToolDataRoundTrip(t *testing.T) {
	td := WriteInstructionsFileToToolData(json.RawMessage(`{"color":"#ff0000"}`), "/roles/develop.md")
	if got := ReadInstructionsFileFromToolData(td); got != "/roles/develop.md" {
		t.Errorf("read back %q, want /roles/develop.md", got)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["color"]; !ok {
		t.Errorf("sibling key lost: %s", td)
	}
	cleared := WriteInstructionsFileToToolData(td, "")
	// A fresh map: json.Unmarshal merges into a non-empty one.
	after := map[string]json.RawMessage{}
	if err := json.Unmarshal(cleared, &after); err != nil {
		t.Fatal(err)
	}
	if _, ok := after["instructions_file"]; ok {
		t.Errorf("empty value must delete the key: %s", cleared)
	}
	if _, ok := after["color"]; !ok {
		t.Errorf("clearing lost the sibling key: %s", cleared)
	}
	if got := ReadInstructionsFileFromToolData(nil); got != "" {
		t.Errorf("legacy row read %q, want \"\"", got)
	}
}

// SetField: the path is stored expanded and absolute, an empty value clears
// it, and a relative path is refused (it would resolve against whatever
// directory the next spawn ran from).
func TestSetFieldInstructionsFile(t *testing.T) {
	home := identityTestEnv(t)
	path := instructionsFileFixture(t, "role.md", "text\n")
	inst := identityTestInstance("claude")

	if _, _, err := SetField(inst, FieldInstructionsFile, path, nil); err != nil {
		t.Fatalf("set absolute path: %v", err)
	}
	if inst.InstructionsFileValue() != path {
		t.Errorf("stored %q, want %q", inst.InstructionsFileValue(), path)
	}
	if _, _, err := SetField(inst, FieldInstructionsFile, "roles/develop.md", nil); err == nil {
		t.Errorf("a relative path must be refused")
	}
	if err := os.MkdirAll(filepath.Join(home, "roles"), 0o755); err != nil {
		t.Fatal(err)
	}
	tilde := filepath.Join(home, "roles", "develop.md")
	if err := os.WriteFile(tilde, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SetField(inst, FieldInstructionsFile, "~/roles/develop.md", nil); err != nil {
		t.Fatalf("set ~ path: %v", err)
	}
	if inst.InstructionsFileValue() != tilde {
		t.Errorf("~ not expanded: stored %q, want %q", inst.InstructionsFileValue(), tilde)
	}
	if _, _, err := SetField(inst, FieldInstructionsFile, "", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if inst.InstructionsFileValue() != "" {
		t.Errorf("empty value did not clear the field: %q", inst.InstructionsFileValue())
	}
	if RestartPolicyFor(FieldInstructionsFile) != FieldRestartRequired {
		t.Errorf("the field is baked into the spawn command, so it needs a restart")
	}
	var listed bool
	for _, f := range ValidMutableFields {
		if f == FieldInstructionsFile {
			listed = true
		}
	}
	if !listed {
		t.Errorf("%q is missing from ValidMutableFields", FieldInstructionsFile)
	}
}

// Several files reach the block in the order they were given, each in its own
// section: the role first, the common rules after it.
func TestInstructionsFilesInGivenOrder(t *testing.T) {
	identityTestEnv(t)
	role := instructionsFileFixture(t, "develop.md", "# Role: Develop\nyour role\n")
	comms := instructionsFileFixture(t, "team-rules.md", "how to report\n")
	roles := instructionsFileFixture(t, "agent-deck-session-roles.md", "who does what\n")

	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{role, comms, roles}
	got := inst.BuildContextPromptForLevel(ContextLevelFull)

	if n := strings.Count(got, InstructionsSectionHeader); n != 3 {
		t.Errorf("block carries %d sections, want one per file (3):\n%s", n, got)
	}
	first, second, third := strings.Index(got, "your role"), strings.Index(got, "how to report"), strings.Index(got, "who does what")
	if first < 0 || second < 0 || third < 0 {
		t.Fatalf("a file's content is missing: %d %d %d\n%s", first, second, third, got)
	}
	if !(first < second && second < third) {
		t.Errorf("the files are not in the given order: %d %d %d", first, second, third)
	}
}

// The field is one value that REPLACES the whole list, every entry absolute,
// duplicates collapsed, and the separator makes a path that contains it
// impossible to name: the error says so.
func TestSetFieldInstructionsFileList(t *testing.T) {
	identityTestEnv(t)
	one := instructionsFileFixture(t, "one.md", "1\n")
	two := instructionsFileFixture(t, "two.md", "2\n")

	inst := identityTestInstance("claude")
	if _, _, err := SetField(inst, FieldInstructionsFile, one+","+two, nil); err != nil {
		t.Fatalf("set two files: %v", err)
	}
	if got := inst.InstructionsFilePaths(); len(got) != 2 || got[0] != one || got[1] != two {
		t.Errorf("stored %v, want [%s %s]", got, one, two)
	}
	// A second set replaces, it does not append.
	if _, _, err := SetField(inst, FieldInstructionsFile, two, nil); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if got := inst.InstructionsFilePaths(); len(got) != 1 || got[0] != two {
		t.Errorf("a set must replace the list, got %v", got)
	}
	// The same file twice is injected once.
	if _, _, err := SetField(inst, FieldInstructionsFile, one+","+one, nil); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if got := inst.InstructionsFilePaths(); len(got) != 1 {
		t.Errorf("duplicates must collapse, got %v", got)
	}
	// A path with a comma in it: the entries are not absolute, and the message
	// must name the reason instead of only "must be absolute".
	_, _, err := SetField(inst, FieldInstructionsFile, "/roles/develop,draft.md", nil)
	if err == nil {
		t.Fatalf("a path containing the separator must be refused")
	}
	if !strings.Contains(err.Error(), InstructionsFileSeparator) {
		t.Errorf("the error does not mention the separator: %v", err)
	}
}

// Where the block rides inline, the budget is spent in the given order: the
// role file is carried and the later, bigger file is named instead.
func TestInstructionsFilesBudgetSpentInOrder(t *testing.T) {
	identityTestEnv(t)
	role := instructionsFileFixture(t, "role.md", "# Role: Develop\nshort role text\n")
	big := instructionsFileFixture(t, "big.md", strings.Repeat("common rule that is long enough to matter\n", 400))

	inst := identityTestInstance("claude")
	inst.SSHHost = "user@host"
	inst.InstructionsFiles = []string{role, big}

	text, ok := inst.identityRemoteText()
	if !ok {
		t.Fatalf("remote text dropped entirely; the pointer must keep it within budget")
	}
	if !strings.Contains(text, "short role text") {
		t.Errorf("the first file must still be carried:\n%s", text)
	}
	if strings.Contains(text, "common rule that is long enough to matter") {
		t.Errorf("the big file must be named, not carried:\n%s", text)
	}
	if !strings.Contains(text, big) {
		t.Errorf("the pointer section does not name the big file:\n%s", text)
	}
	if n := len(identityPrintfArg(text)); n > identityInlineMaxBytes {
		t.Errorf("remote payload is %d bytes, over the %d budget", n, identityInlineMaxBytes)
	}
}

// tool_data: the list is written under `instructions_files`, its first path
// under the pre-list key, a legacy row with only that key reads as one file,
// and the empty list deletes both keys.
func TestInstructionsFilesToolDataRoundTrip(t *testing.T) {
	td := WriteInstructionsFilesToToolData(json.RawMessage(`{"color":"#ff0000"}`), []string{"/roles/develop.md", "/memory/standard.md"})
	got := ReadInstructionsFilesFromToolData(td)
	if len(got) != 2 || got[0] != "/roles/develop.md" || got[1] != "/memory/standard.md" {
		t.Errorf("read back %v, want both paths in order", got)
	}
	if first := ReadInstructionsFileFromToolData(td); first != "/roles/develop.md" {
		t.Errorf("the pre-list key reads %q, want the first path", first)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(td, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"instructions_file", "instructions_files", "color"} {
		if _, ok := m[key]; !ok {
			t.Errorf("key %q missing: %s", key, td)
		}
	}
	legacy := ReadInstructionsFilesFromToolData(json.RawMessage(`{"instructions_file":"/roles/plan.md"}`))
	if len(legacy) != 1 || legacy[0] != "/roles/plan.md" {
		t.Errorf("a legacy row read %v, want one element", legacy)
	}
	after := map[string]json.RawMessage{}
	if err := json.Unmarshal(WriteInstructionsFilesToToolData(td, nil), &after); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"instructions_file", "instructions_files"} {
		if _, ok := after[key]; ok {
			t.Errorf("the empty list must delete %q", key)
		}
	}
	if _, ok := after["color"]; !ok {
		t.Errorf("clearing lost the sibling key")
	}
}

// `session primer` reports a state per file; the two scalars stay the first
// file's, so a client that reads them sees no change.
func TestInstructionsFilesStatePerFile(t *testing.T) {
	identityTestEnv(t)
	good := instructionsFileFixture(t, "good.md", "text\n")
	gone := filepath.Join(t.TempDir(), "gone.md")

	inst := identityTestInstance("claude")
	inst.InstructionsFiles = []string{good, gone}

	reports := inst.InstructionsFilesState()
	if len(reports) != 2 {
		t.Fatalf("got %d reports, want 2: %v", len(reports), reports)
	}
	if reports[0].Path != good || reports[0].State != "ok" {
		t.Errorf("first report = %+v, want (%s, ok)", reports[0], good)
	}
	if reports[1].Path != gone || !strings.HasPrefix(reports[1].State, "unreadable") {
		t.Errorf("second report = %+v, want (%s, unreadable: ...)", reports[1], gone)
	}
	if path, state := inst.InstructionsFileState(); path != good || state != "ok" {
		t.Errorf("the scalars = (%q, %q), want the first file's", path, state)
	}
}
