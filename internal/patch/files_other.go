//go:build !unix

package patch

import (
	"errors"
	"io/fs"
)

// confinedFiles has no directory handles to confine writes with here, so
// it refuses every path.
type confinedFiles Targets

var errConfined = errors.New("confined patches need a Unix system")

func (confinedFiles) stat(string) (fs.FileInfo, error)            { return nil, errConfined }
func (confinedFiles) lstat(string) (fs.FileInfo, error)           { return nil, errConfined }
func (confinedFiles) readFile(string) ([]byte, error)             { return nil, errConfined }
func (confinedFiles) mkdirParents(string) error                   { return errConfined }
func (confinedFiles) writeFile(string, []byte, fs.FileMode) error { return errConfined }
func (confinedFiles) remove(string) error                         { return errConfined }
