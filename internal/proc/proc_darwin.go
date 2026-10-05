//go:build darwin

package proc

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// szomb is the p_stat value of a zombie: exited but not yet reaped.
const szomb = 5

type systemInspector struct{}

func (systemInspector) Lookup(pid int) (Info, error) {
	if pid <= 0 {
		return Info{}, ErrNotFound
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp == nil || int(kp.Proc.P_pid) != pid || kp.Proc.P_stat == szomb {
		return Info{}, ErrNotFound
	}
	comm := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	st := kp.Proc.P_starttime
	return Info{
		PID:   pid,
		PPID:  int(kp.Eproc.Ppid),
		Start: int64(st.Sec)*1_000_000 + int64(st.Usec),
		Name:  string(comm),
	}, nil
}

// Args reads kern.procargs2: a native-endian int32 argc, the executable path,
// NUL padding, then argc NUL-terminated arguments.
func (systemInspector) Args(pid int) ([]string, error) {
	buf, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	if len(buf) < 4 {
		return nil, ErrNotFound
	}
	argc := int(binary.NativeEndian.Uint32(buf[:4]))
	rest := buf[4:]
	// Skip the executable path and its padding.
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return nil, ErrNotFound
	}
	rest = rest[i:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc && len(rest) > 0 {
		j := bytes.IndexByte(rest, 0)
		if j < 0 {
			args = append(args, string(rest))
			break
		}
		args = append(args, string(rest[:j]))
		rest = rest[j+1:]
	}
	return args, nil
}
