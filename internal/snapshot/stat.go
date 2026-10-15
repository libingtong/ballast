package snapshot

import "io/fs"

// fileStat is the part of lstat the index stat cache cares about.
type fileStat struct {
	kind  fs.FileMode // fs.ModeSymlink, fs.ModeDir, 0 (regular) or other type bits
	exec  bool        // owner execute bit
	size  int64
	mtime tspec
	// ctime, dev and ino are only meaningful when full is true (POSIX
	// stat available); elsewhere only mtime and size are compared.
	ctime    tspec
	dev, ino uint32
	full     bool
}

type tspec struct{ sec, nsec uint32 }

// statOf extracts a fileStat from an lstat result.
func statOf(fi fs.FileInfo) fileStat {
	st := fileStat{
		kind: fi.Mode() & fs.ModeType,
		exec: fi.Mode().Perm()&0o100 != 0,
		size: fi.Size(),
	}
	if !fillSys(&st, fi) {
		t := fi.ModTime()
		st.mtime = tspec{uint32(t.Unix()), uint32(t.Nanosecond())}
	}
	return st
}
