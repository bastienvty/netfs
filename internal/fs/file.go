package fs

import (
	"context"
	"fmt"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// https://github.com/aegistudio/enigma/blob/master/cmd/enigma/fuse_unix.go
// https://github.com/pachyderm/pachyderm/blob/master/src/server/pfs/fuse/files.go
// https://github.com/rclone/rclone/blob/b2f6aac754c5d46c66758db46ecb89aa85c3c113/cmd/mount2/file.go
// https://github.com/materials-commons/hydra/blob/main/pkg/mcfs/fs/mcfs/base_file_handle.go
// juicefs
// nanafs
// gocryptfs

type File struct {
	n *Node
}

var _ fs.FileHandle = (*File)(nil)

// var _ = (fs.FileGetattrer)((*File)(nil))
// var _ = (fs.FileSetattrer)((*File)(nil))
var _ = (fs.FileReader)((*File)(nil))

var _ = (fs.FileWriter)((*File)(nil))

var _ = (fs.FileFlusher)((*File)(nil))
var _ = (fs.FileReleaser)((*File)(nil))
var _ = (fs.FileFsyncer)((*File)(nil))

/*func newFile(meta meta.Meta, name string) (fh *File, errno syscall.Errno) {
	st := &syscall.Stat_t{}
	if err := syscall.Fstat(int(ino), st); err != nil {
		errno = fs.ToErrno(err)
		return
	}

	osFile := os.NewFile(uintptr(ino), name)

	fh = &File{}

	return fh, 0
}*/

/*func (f *NSFile) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	return 0
}*/

func (f *File) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	fmt.Println("READ")
	ino := f.n.StableAttr().Ino
	data, error := f.n.obj.Get(ino, nil, off)
	if error != nil {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(data), 0
}

func (f *File) Write(ctx context.Context, data []byte, off int64) (written uint32, errno syscall.Errno) {
	ino := f.n.StableAttr().Ino
	/*text := string(data)
	lines := strings.Split(text, "\n")
	if len(lines) > 2 {
		lines = lines[:len(lines)-2]
		text = strings.Join(lines, "\n") + "\n"
	}
	fmt.Println("TEXT:", text)
	newData := []byte(text)*/
	// decData, _ := f.n.enc.Decrypt(nil, data)
	err := f.n.meta.Write(ctx, ino, data, off)
	if err != 0 {
		return 0, err
	}
	// key := uuid.New().String()
	error := f.n.obj.Put(ino, nil, data)
	if error != nil {
		return 0, syscall.EIO
	}
	return uint32(len(data)), 0
}

func (f *File) Flush(ctx context.Context) syscall.Errno {
	return 0
}

func (f *File) Release(ctx context.Context) syscall.Errno {
	return 0
}

func (f *File) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0
}
