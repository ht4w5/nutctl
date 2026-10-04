//go:build !linux

package hid

import "errors"

// errLinuxOnly is returned by every operation: v0 is Linux-only (ADR-0002).
var errLinuxOnly = errors.New("nutctl supports Linux only (ADR-0002)")

// NewEnumerator returns an enumerator that refuses to run off Linux.
func NewEnumerator() Enumerator { return unsupportedEnumerator{} }

type unsupportedEnumerator struct{}

func (unsupportedEnumerator) Enumerate() ([]Info, error) { return nil, errLinuxOnly }

func (unsupportedEnumerator) Open(Info) (Transport, error) { return nil, errLinuxOnly }
