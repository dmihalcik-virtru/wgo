package urlhandler

import (
	"context"
	"errors"
)

// fakeRunner records argvs and answers by argv[0]; plutil -extract answers
// come from extract keyed by the key name.
type fakeRunner struct {
	calls   [][]string
	out     map[string]string
	err     map[string]error
	extract map[string]string
	before  func(argv []string) // runs before answering, e.g. to leave files behind
}

func (f *fakeRunner) Output(_ context.Context, argv []string) (string, error) {
	f.calls = append(f.calls, argv)
	if f.before != nil {
		f.before(argv)
	}
	if argv[0] == "plutil" && len(argv) > 2 && argv[1] == "-extract" {
		v, ok := f.extract[argv[2]]
		if !ok {
			return "", errors.New("plutil: No value at that key path")
		}
		return v + "\n", nil
	}
	if e := f.err[argv[0]]; e != nil {
		return "", e
	}
	return f.out[argv[0]], nil
}
