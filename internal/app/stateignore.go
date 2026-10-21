package app

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// stateIgnoreMarker starts the state directory's .ignore while uah owns
// it: uah rewrites the file only while its first line is this one.
const stateIgnoreMarker = "# Written by uah."

// stateIgnore is the state directory's .ignore: search tools that read it
// (rg, fd, ag), run in a workspace that holds the state directory such as
// ~, skip its sessions, runs, and logs, which hold what the agent did and
// grow, while the configuration, prompts, rules, and skills stay
// searchable.
const stateIgnore = stateIgnoreMarker + ` It keeps search tools (rg, fd, ag) out of uah's
# sessions, runs, and logs when a session's workspace holds this directory,
# such as ~. Remove the first line to keep your own version.
/sessions/
/runs/
/logs/
/sandbox/
/images/
/editor/
/models/
/uah.db*
/history.jsonl
`

// writeStateIgnore writes the state directory's .ignore when it has none,
// or one that uah wrote and that differs; a file without the marker is the
// user's and stays. It is best effort: a session runs without it.
func writeStateIgnore(stateDir string) error {
	path := filepath.Join(stateDir, ".ignore")
	old, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	case !bytes.HasPrefix(old, []byte(stateIgnoreMarker)) || string(old) == stateIgnore:
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(stateIgnore), 0o600)
}
