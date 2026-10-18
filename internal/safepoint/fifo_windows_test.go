package safepoint

import "errors"

func makeTestFIFO(string) error { return errors.New("Windows 不提供 Unix FIFO") }
