// Per-session instructions files.
//
// An operator can name one or more files per session (`add|launch
// --instructions-file`, repeatable and accepting a comma separated list, and
// `session set <id> instructions-file "<p1>,<p2>"`, which replaces the whole
// list). Their content is appended to the context block agent-deck already
// hands the harness at every start, restart, resume and fork
// (identity_injection.go), each file in a clearly marked section of its own
// and in the order the operator gave, so it reaches every harness of that
// file's table through the harness's own mechanism. Nothing is written into
// the project directory and no new environment variable is introduced.
//
// The files are read at spawn time, never cached: editing one reaches the
// session at its next start/restart without touching the field.
//
// Three shapes per file, in order of preference:
//
//	content     the file's text (every file-delivered harness, always)
//	pointer     one line naming the file, where the block rides inline in the
//	            launch command (SSH, sandbox, codex's -c override) and the
//	            content would exceed identityInlineMaxBytes. The budget is
//	            spent in the operator's order: the files that still fit are
//	            carried, every later one is named instead.
//	unreadable  one line naming the file and why it could not be read; a
//	            missing or unreadable file never fails a spawn
package session

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// InstructionsSectionHeader opens the section of every file in every shape,
// so a session (and `session primer`) can find them in the block.
const InstructionsSectionHeader = "# Session instructions"

// InstructionsFileSeparator separates the paths inside one CLI value, for the
// flag as well as for the field, following the house style of the other
// list-valued fields (mutators.go). A path that contains it cannot be named.
const InstructionsFileSeparator = ","

// instructionsFileMaxBytes caps what agent-deck inlines from one file at all.
// A role or brief this long belongs in the project, not in every prompt; above
// it the section names the file instead of carrying it.
const instructionsFileMaxBytes = 64 << 10

// instructionsMode picks the shape of the sections (see the file comment).
type instructionsMode int

const (
	instructionsContent instructionsMode = iota
	instructionsPointer
	// instructionsNone leaves the sections out altogether: the payload has
	// no room even for the pointer lines, and the identity block itself is
	// worth more than nothing.
	instructionsNone
)

// InstructionsFileReport is one file of a session and what became of it, as
// `session primer --json` reports it.
type InstructionsFileReport struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

// ParseInstructionsFileList turns one CLI value into the absolute paths it
// names, in the given order. An empty value (or one with only separators)
// means "unset". Every entry is expanded ($VAR, ~) and must be absolute: a
// relative path would be read against whatever directory the next spawn ran
// from, which is not the operator's. Duplicates are collapsed, the first
// position winning, so one file is never injected twice.
//
// The separator always separates files, so a path that contains one cannot be
// named at all; the error says that rather than only "must be absolute".
func ParseInstructionsFileList(value string) ([]string, error) {
	var paths []string
	seen := make(map[string]bool)
	for _, raw := range strings.Split(value, InstructionsFileSeparator) {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if bad, ok := firstControlChar(trimmed); ok {
			return nil, fmt.Errorf("instructions file %q contains the control character %q: the block puts the path on a line of its own, where a newline or an escape would inject text", trimmed, bad)
		}
		expanded := filepath.Clean(ExpandPath(trimmed))
		if !filepath.IsAbs(expanded) {
			return nil, fmt.Errorf("instructions file %q must be an absolute path (%q separates several files, so a path that contains one cannot be used)",
				trimmed, InstructionsFileSeparator)
		}
		if seen[expanded] {
			continue
		}
		seen[expanded] = true
		paths = append(paths, expanded)
	}
	return paths, nil
}

// firstControlChar is the first character of s that may not appear in a path
// this tool puts into a prompt or a launch command.
func firstControlChar(s string) (string, bool) {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return string(r), true
		}
	}
	return "", false
}

// InstructionsFilePaths are this session's files, in order, without the
// empty entries a hand-edited row could carry.
func (i *Instance) InstructionsFilePaths() []string {
	if i == nil {
		return nil
	}
	var paths []string
	for _, path := range i.InstructionsFiles {
		if trimmed := strings.TrimSpace(path); trimmed != "" {
			paths = append(paths, trimmed)
		}
	}
	return paths
}

// instructionsFileSet reports whether this session names any instructions file.
func (i *Instance) instructionsFileSet() bool {
	return len(i.InstructionsFilePaths()) > 0
}

// InstructionsFileValue is the list as one CLI value, which is what `session
// set <id> instructions-file` takes and what the TUI and the dialogs show.
func (i *Instance) InstructionsFileValue() string {
	return strings.Join(i.InstructionsFilePaths(), InstructionsFileSeparator)
}

// InstructionsFileFirst is the first file, for the pre-list key
// `instructions_file` in tool_data and in the JSON views.
func (i *Instance) InstructionsFileFirst() string {
	if paths := i.InstructionsFilePaths(); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// instructionsFileBody reads one file, capped at instructionsFileMaxBytes.
// The error is what the section and the state report; it is never fatal.
//
// Only a regular file is read. This runs while the spawn command is being
// assembled, so a FIFO or a character device would block every start,
// restart and `session primer` of that session instead of delivering
// anything, and its Stat size says nothing about what a read would return.
// The read is limited as well, because a regular file can grow between the
// Stat and the read.
func instructionsFileBody(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", path)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file (%s)", path, info.Mode().Type())
	}
	if info.Size() > instructionsFileMaxBytes {
		return "", fmt.Errorf("%s is %d bytes, over the %d byte limit", path, info.Size(), instructionsFileMaxBytes)
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	defer file.Close()
	// One byte over the cap is read on purpose: that is how a file which grew
	// since the Stat is noticed instead of going into every prompt.
	data, err := io.ReadAll(io.LimitReader(file, instructionsFileMaxBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > instructionsFileMaxBytes {
		return "", fmt.Errorf("%s grew past the %d byte limit while it was read", path, instructionsFileMaxBytes)
	}
	return instructionsFileText(data, path)
}

// instructionsFileText is the file's content as the block may carry it.
//
// The block travels as one argument of the launch command, and the quoting
// there assumes its text holds no control characters (identityPrintfArg): a
// NUL byte truncates the whole command, and an escape sequence or a stray
// carriage return mangles it. A binary or mis-encoded file is therefore
// refused rather than carried, and of the control characters only the two
// that belong in a text file survive. CRLF becomes LF instead of losing the
// carriage return alone, so a file written on Windows is delivered as it
// reads.
func instructionsFileText(data []byte, path string) (string, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("%s is binary (it holds a NUL byte)", path)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s is not valid UTF-8", path)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || r == 0x7f:
			return -1
		default:
			return r
		}
	}, text), nil
}

// instructionsOneSection renders the section of one file: the pointer line
// when `pointer` is set, otherwise its content (or why it could not be read).
func (i *Instance) instructionsOneSection(path string, pointer bool) string {
	if pointer {
		return fmt.Sprintf("\n%s\nRead the file %s and follow it. Its content is not inlined here: this session's harness receives its context inside the launch command, which has no room for it.\n",
			InstructionsSectionHeader, path)
	}
	body, err := instructionsFileBody(path)
	if err != nil {
		sessionLog.Warn("instructions_file_unreadable",
			slog.String("instance_id", i.ID),
			slog.String("path", path),
			slog.String("error", err.Error()))
		return fmt.Sprintf("\n%s\nagent-deck could not read it: %s. Read it yourself if it is there.\n",
			InstructionsSectionHeader, err.Error())
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return fmt.Sprintf("\n%s (from %s)\n%s", InstructionsSectionHeader, path, body)
}

// instructionsSections renders one section per file, in the operator's order,
// or "" when the session names none.
//
// `room` is how many bytes of the inline budget the rest of the block leaves,
// measured the way the launch command measures it (identityPrintfArg), and is
// only consulted in instructionsPointer mode: there the budget is spent in
// order, so the files that still fit are carried and every later one is named
// instead. A deterministic rule, and the first file (the session's role) is
// the one that survives.
func (i *Instance) instructionsSections(mode instructionsMode, room int) string {
	paths := i.InstructionsFilePaths()
	if len(paths) == 0 || mode == instructionsNone {
		return ""
	}
	var b strings.Builder
	named := false
	for _, path := range paths {
		section := i.instructionsOneSection(path, false)
		if mode == instructionsPointer {
			if !named && len(identityPrintfArg(section)) > room {
				// From here on the budget is spent: naming this file but
				// inlining a smaller one after it would make the result
				// depend on the sizes further down the list instead of on
				// the operator's order.
				named = true
			}
			if named {
				section = i.instructionsOneSection(path, true)
			}
		}
		room -= len(identityPrintfArg(section))
		b.WriteString(section)
	}
	return b.String()
}

// instructionsShapedPrompt is the context block for a spawn that carries it
// inside the launch command, in the largest shape that fits: the files'
// content, else every file named, else the block without them. `cost`
// measures the finished payload the way its caller sends it, which is the
// only measurement that counts. ok is false when not
// even the bare block fits; the caller decides what that means.
func (i *Instance) instructionsShapedPrompt(level string, cost func(string) int) (text string, shape instructionsMode, ok bool) {
	content := i.buildContextPrompt(level, instructionsContent)
	if cost(content) <= identityInlineMaxBytes {
		return content, instructionsContent, true
	}
	if i.instructionsFileSet() {
		if named := i.buildContextPrompt(level, instructionsPointer); cost(named) <= identityInlineMaxBytes {
			return named, instructionsPointer, true
		}
		base := i.buildContextPrompt(level, instructionsNone)
		return base, instructionsNone, cost(base) <= identityInlineMaxBytes
	}
	return content, instructionsContent, false
}

// instructionsShape is the shape this session's next spawn delivers, for the
// states `session primer` and `session show` report. A session whose block is
// written to a file always receives the content; only a payload that rides in
// the launch command can fall back. For a local codex the operator's own
// developer_instructions are not known here, so they are not counted: that
// spawn can still demote a file this reports as delivered.
func (i *Instance) instructionsShape() instructionsMode {
	if !i.instructionsFileSet() {
		return instructionsContent
	}
	level, _ := i.EffectiveContextLevel()
	switch {
	case i.identityRunsElsewhere():
		_, shape, _ := i.instructionsShapedPrompt(level, func(s string) int {
			return len(identityPrintfArg(s))
		})
		return shape
	case i.Tool == "codex":
		_, shape, _ := i.instructionsShapedPrompt(level, func(s string) int {
			return len("developer_instructions=" + identityTOMLString(s))
		})
		return shape
	}
	return instructionsContent
}

// inheritInstructionsFiles gives dst the files of src. A fork and a
// cross-harness switch continue the same job, so they carry the same
// instructions (#3206); a `launch` child has its own job
// and gets none. The slice is copied, so the two sessions never share it.
func inheritInstructionsFiles(dst, src *Instance) {
	if dst == nil || src == nil {
		return
	}
	if paths := src.InstructionsFilePaths(); len(paths) > 0 {
		dst.InstructionsFiles = append([]string(nil), paths...)
	}
}

// InstructionsFilesState is what `session primer` reports per file: its path
// and one of "ok", "not delivered (context level none)" or
// "unreadable: <reason>". Empty when the session names no file.
func (i *Instance) InstructionsFilesState() []InstructionsFileReport {
	paths := i.InstructionsFilePaths()
	if len(paths) == 0 {
		return nil
	}
	shape := i.instructionsShape()
	reports := make([]InstructionsFileReport, 0, len(paths))
	for _, path := range paths {
		state := "ok"
		switch {
		case !i.identityInjectionEnabled():
			state = "not delivered (context level none)"
		case shape == instructionsPointer:
			state = "named only (inline budget)"
		case shape == instructionsNone:
			state = "not delivered (inline budget)"
		default:
			if _, err := instructionsFileBody(path); err != nil {
				state = "unreadable: " + err.Error()
			}
		}
		reports = append(reports, InstructionsFileReport{Path: path, State: state})
	}
	return reports
}

// InstructionsFileState is the first file's path and state, kept for the
// pre-list `instructions_file` and `instructions_file_state` keys; "unset"
// when the session names none.
func (i *Instance) InstructionsFileState() (path, state string) {
	reports := i.InstructionsFilesState()
	if len(reports) == 0 {
		return "", "unset"
	}
	return reports[0].Path, reports[0].State
}
