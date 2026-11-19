// Per-session instructions files: persistence.
//
// Follows the context_level_persist.go pattern: the field lives in the
// tool_data extras zone, outside the positional MarshalToolData /
// UnmarshalToolData signatures and without a SQL schema migration, so a
// binary that predates the key preserves it via MergeToolDataExtras. The keys
// are deleted at the empty value (unset), so clearing the field leaves a row
// shaped exactly like a pre-feature one.
//
// Two keys, written together: `instructions_files` is the list, and
// `instructions_file` is its first path, which is what the one-file builds of
// this feature read. A row therefore stays readable in both directions, and
// an older binary hands the session at least its first file.
package session

import "encoding/json"

const (
	toolDataInstructionsFileKey  = "instructions_file"
	toolDataInstructionsFilesKey = "instructions_files"
)

// WriteInstructionsFilesToToolData merges the session's instructions files
// into the given tool_data blob, removing both keys when the list is empty.
// Sibling keys are preserved.
func WriteInstructionsFilesToToolData(td json.RawMessage, paths []string) json.RawMessage {
	first := ""
	if len(paths) > 0 {
		first = paths[0]
	}
	td = writeToolDataString(td, toolDataInstructionsFileKey, first)
	return writeToolDataStrings(td, toolDataInstructionsFilesKey, paths)
}

// WriteInstructionsFileToToolData writes a single path, for the callers (and
// the tests) that still think in one file.
func WriteInstructionsFileToToolData(td json.RawMessage, path string) json.RawMessage {
	if path == "" {
		return WriteInstructionsFilesToToolData(td, nil)
	}
	return WriteInstructionsFilesToToolData(td, []string{path})
}

// ReadInstructionsFilesFromToolData extracts the list from the blob, in
// order. A legacy row that carries only the single key reads as a one element
// list. Returns nil (unset) for missing, malformed or legacy-empty rows.
func ReadInstructionsFilesFromToolData(td json.RawMessage) []string {
	if len(td) == 0 {
		return nil
	}
	var blob struct {
		InstructionsFile  string   `json:"instructions_file"`
		InstructionsFiles []string `json:"instructions_files"`
	}
	_ = json.Unmarshal(td, &blob)
	if len(blob.InstructionsFiles) > 0 {
		return blob.InstructionsFiles
	}
	if blob.InstructionsFile != "" {
		return []string{blob.InstructionsFile}
	}
	return nil
}

// ReadInstructionsFileFromToolData extracts the first path from the blob.
// Returns "" (unset) for missing, malformed, or legacy rows.
func ReadInstructionsFileFromToolData(td json.RawMessage) string {
	if paths := ReadInstructionsFilesFromToolData(td); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// instructionsFilesOf is what a persisted record carries: its list, or the
// pre-list single path when only that is there.
func instructionsFilesOf(list []string, single string) []string {
	if len(list) > 0 {
		return list
	}
	if single != "" {
		return []string{single}
	}
	return nil
}

// writeToolDataStrings is writeToolDataString for a list value: the key is
// deleted when the list is empty, so an unset field leaves no trace.
func writeToolDataStrings(td json.RawMessage, key string, values []string) json.RawMessage {
	m := map[string]json.RawMessage{}
	if len(td) > 0 {
		_ = json.Unmarshal(td, &m)
	}
	if len(values) == 0 {
		delete(m, key)
	} else {
		raw, err := json.Marshal(values)
		if err != nil {
			return td
		}
		m[key] = raw
	}
	out, err := json.Marshal(m)
	if err != nil {
		return td
	}
	return out
}
