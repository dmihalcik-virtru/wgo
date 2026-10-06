//go:build !darwin && !linux

package proc

type systemInspector struct{}

func (systemInspector) Lookup(int) (Info, error) { return Info{}, ErrUnsupported }

func (systemInspector) Args(int) ([]string, error) { return nil, ErrUnsupported }
