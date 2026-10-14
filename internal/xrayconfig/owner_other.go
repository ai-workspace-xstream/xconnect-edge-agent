//go:build !unix

package xrayconfig

import "io/fs"

func fileOwner(fs.FileInfo) (uid, gid int, ok bool) { return 0, 0, false }
