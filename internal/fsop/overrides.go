package fsop

import (
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// Modes holds the --chmod overrides: a subset of the rsync --chmod syntax
// with octal modes only.
type Modes struct {
	Dir, File       fs.FileMode
	HasDir, HasFile bool
}

// ParseModes parses a comma-separated list of "D<octal>" (directories),
// "F<octal>" (files), and "<octal>" (both) items, for example "D0700,F0600".
// Later items take precedence.
func ParseModes(s string) (Modes, error) {
	var m Modes
	if strings.TrimSpace(s) == "" {
		return m, nil
	}
	for item := range strings.SplitSeq(s, ",") {
		item = strings.TrimSpace(item)
		dir, file := true, true
		switch {
		case strings.HasPrefix(item, "D"):
			file, item = false, item[1:]
		case strings.HasPrefix(item, "F"):
			dir, item = false, item[1:]
		}
		if item == "" {
			return Modes{}, errors.New("chmod: empty mode")
		}
		v, err := strconv.ParseUint(item, 8, 32)
		if err != nil || v > 0o7777 {
			return Modes{}, fmt.Errorf("chmod: %q is not an octal mode between 0 and 7777", item)
		}
		mode := fromOctal(v)
		if dir {
			m.Dir, m.HasDir = mode, true
		}
		if file {
			m.File, m.HasFile = mode, true
		}
	}
	return m, nil
}

// String formats the overrides in the --chmod syntax.
func (m Modes) String() string {
	var parts []string
	if m.HasDir {
		parts = append(parts, fmt.Sprintf("D%04o", toOctal(m.Dir)))
	}
	if m.HasFile {
		parts = append(parts, fmt.Sprintf("F%04o", toOctal(m.File)))
	}
	return strings.Join(parts, ",")
}

func fromOctal(v uint64) fs.FileMode {
	m := fs.FileMode(v & 0o777)
	if v&0o4000 != 0 {
		m |= fs.ModeSetuid
	}
	if v&0o2000 != 0 {
		m |= fs.ModeSetgid
	}
	if v&0o1000 != 0 {
		m |= fs.ModeSticky
	}
	return m
}

func toOctal(m fs.FileMode) uint32 {
	v := uint32(m.Perm())
	if m&fs.ModeSetuid != 0 {
		v |= 0o4000
	}
	if m&fs.ModeSetgid != 0 {
		v |= 0o2000
	}
	if m&fs.ModeSticky != 0 {
		v |= 0o1000
	}
	return v
}

// Owner holds the --chown override.
type Owner struct {
	UID, GID int
	Set      bool
}

// ParseOwner parses "UID:GID" with numeric IDs, for example "1000:1000".
func ParseOwner(s string) (Owner, error) {
	if strings.TrimSpace(s) == "" {
		return Owner{}, nil
	}
	u, g, ok := strings.Cut(s, ":")
	uid, uerr := strconv.Atoi(u)
	gid, gerr := strconv.Atoi(g)
	if !ok || uerr != nil || gerr != nil || uid < 0 || gid < 0 {
		return Owner{}, fmt.Errorf("chown: %q is not UID:GID with numeric IDs", s)
	}
	return Owner{UID: uid, GID: gid, Set: true}, nil
}

// String formats the override in the --chown syntax.
func (o Owner) String() string {
	if !o.Set {
		return ""
	}
	return fmt.Sprintf("%d:%d", o.UID, o.GID)
}
