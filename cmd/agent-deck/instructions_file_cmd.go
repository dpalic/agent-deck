package main

import (
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// instructionsFileFlag collects `add|launch --instructions-file`, which is
// repeatable and may also carry a separated list in one occurrence, keeping
// the operator's order: the files are injected in exactly that order.
type instructionsFileFlag struct {
	values []string
}

func (f *instructionsFileFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(f.values, session.InstructionsFileSeparator)
}

// Set validates every occurrence at once, so a wrong path fails before the
// session is created and with the same message `session set` gives.
func (f *instructionsFileFlag) Set(value string) error {
	if _, err := session.ParseInstructionsFileList(value); err != nil {
		return err
	}
	f.values = append(f.values, value)
	return nil
}

// IsRepeatableFlag marks this flag in the creation catalog
// (`add --capabilities --json` -> .commands.add[].repeatable), the way
// IsBoolFlag marks a boolean one. A client that passes several files must
// require that mark: a build without it takes one value only and would read a
// separated list as a single path.
func (f *instructionsFileFlag) IsRepeatableFlag() bool { return true }

// given reports whether the flag was used at all, so an unused flag never
// touches the field.
func (f *instructionsFileFlag) given() bool { return f != nil && len(f.values) > 0 }

// applyInstructionsFileFlag applies the flag to the new session through the
// same mutator `session set <id> instructions-file` uses, so the flag and the
// field accept exactly the same values and report the same error.
func applyInstructionsFileFlag(inst *session.Instance, flagValue *instructionsFileFlag) error {
	if inst == nil || !flagValue.given() {
		return nil
	}
	joined := strings.Join(flagValue.values, session.InstructionsFileSeparator)
	if strings.TrimSpace(joined) == "" {
		return nil
	}
	_, _, err := session.SetField(inst, session.FieldInstructionsFile, joined, nil)
	return err
}
