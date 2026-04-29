//go:build !linux

// Stub for platforms without io_uring. Selecting MethodIOUring
// on non-Linux returns errProbeFailed, which NewEngine treats
// as the signal to fall back to MethodWorker — same behaviour
// a Linux host with io_uring sysctl-disabled produces.

package aio

import "errors"

var errProbeFailed = errors.New("aio: io_uring probe failed (non-Linux platform)")

// methodIOUring is the non-Linux stub. The Linux file declares
// the real type; defining it here would conflict.
type methodIOUring struct{}

func (m *methodIOUring) Name() string  { return MethodIOUring }
func (m *methodIOUring) Submit(*Op) *Handle {
	panic("aio: methodIOUring.Submit called on non-Linux build (probe should have failed)")
}
func (m *methodIOUring) Close() error { return nil }

func newMethodIOUring(*Engine, int) (*methodIOUring, error) {
	return nil, errProbeFailed
}
