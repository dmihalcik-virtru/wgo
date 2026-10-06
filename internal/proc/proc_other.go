//go:build !darwin && !linux

package proc

type systemInspector struct{}

func (systemInspector) Lookup(int) (Info, error) { return Info{}, ErrNotFound }

func (systemInspector) Args(int) ([]string, error) { return nil, ErrNotFound }
