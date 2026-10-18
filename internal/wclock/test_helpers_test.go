package wclock

import "os"

var mkdirAll = func(path string) error { return os.MkdirAll(path, 0o700) }
var symlink = os.Symlink
