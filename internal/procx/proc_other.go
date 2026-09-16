//go:build !darwin && !linux

package procx

import "syscall"

func listAll() ([]Proc, error) { return nil, ErrUnsupported }

func readProc(int) (Proc, error) { return Proc{}, ErrUnsupported }

type osSignaler struct{}

func (osSignaler) signalVerified(int, syscall.Signal, func(Proc) error) error {
	return ErrUnsupported
}
