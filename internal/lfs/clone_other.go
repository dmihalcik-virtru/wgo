//go:build !darwin && !linux

package lfs

// clone is unsupported on platforms without a known reflink primitive;
// cloneOrCopy always falls back to a plain copy.
func clone(src, dst string) error {
	return errCloneUnsupported
}
