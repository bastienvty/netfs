package fs

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/bastienvty/netsecfs/internal/crypto"
	"github.com/bastienvty/netsecfs/internal/db/meta"
	"github.com/bastienvty/netsecfs/internal/db/object"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const (
	rootID        = 1
	maxName       = meta.MaxName
	fileBlockSize = 1 << 12          // 4k
	maxSize       = 1125899906842624 // 1TB
)

const (
	DirEntryTimeout = 1 * time.Second
	EntryTimeout    = 1 * time.Second
	AttrTimeout     = 1 * time.Second
)

type Ino = meta.Ino

type Node struct {
	fs.Inode

	inoMap map[string]Ino
	meta   meta.Meta
	obj    object.ObjectStorage
	enc    crypto.Crypto

	privKey *rsa.PrivateKey
	key     []byte
	userId  uint32
}

func NewRootNode(meta meta.Meta, obj object.ObjectStorage, privateKey *rsa.PrivateKey, key []byte, username string) *Node {
	var userId uint32
	ok := meta.GetUserId(username, &userId)
	if ok != nil {
		return nil
	}
	return &Node{
		inoMap:  make(map[string]Ino),
		meta:    meta,
		obj:     obj,
		enc:     &crypto.CryptoHelper{},
		privKey: privateKey,
		key:     key,
		userId:  userId,
	}
}

var _ = (fs.InodeEmbedder)((*Node)(nil))
var _ = (fs.NodeLookuper)((*Node)(nil))
var _ = (fs.NodeSetattrer)((*Node)(nil))
var _ = (fs.NodeGetattrer)((*Node)(nil))
var _ = (fs.NodeStatfser)((*Node)(nil))

var _ = (fs.NodeOpener)((*Node)(nil))
var _ = (fs.NodeCreater)((*Node)(nil))

var _ = (fs.NodeReaddirer)((*Node)(nil))
var _ = (fs.NodeMkdirer)((*Node)(nil))
var _ = (fs.NodeRmdirer)((*Node)(nil))

var _ = (fs.NodeUnlinker)((*Node)(nil))

func (n *Node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if len(name) > maxName {
		return nil, syscall.ENAMETOOLONG
	}
	var errno syscall.Errno
	var err error
	var attr = &meta.Attr{}
	parent := Ino(n.StableAttr().Ino)
	var key, keyDec []byte
	ino, ok := n.inoMap[name]
	if !ok {
		return nil, syscall.ENOENT
	}
	if parent == meta.SharedInode {
		errno = n.meta.GetSharedKey(ctx, n.userId, ino, &key)
	} else {
		errno = n.meta.GetKey(ctx, ino, &key)
	}
	if errno != 0 {
		return nil, errno
	}
	if parent == meta.SharedInode {
		keyDec, err = n.enc.DecryptRSA(n.privKey, key)
	} else {
		keyDec, err = n.enc.Decrypt(n.key, key)
	}
	if err != nil {
		return nil, syscall.EINVAL
	}
	errno = n.meta.Lookup(ctx, n.userId, parent, ino, attr)
	if errno != 0 {
		return nil, errno
	}
	ops := &Node{
		inoMap:  n.inoMap,
		meta:    n.meta,
		obj:     n.obj,
		enc:     n.enc,
		privKey: n.privKey,
		key:     keyDec,
		userId:  n.userId,
	}
	entry := &meta.Entry{Inode: ino, Attr: attr}
	attrToStat(entry.Inode, entry.Attr, &out.Attr)
	st := fs.StableAttr{
		Mode: attr.SMode(),
		Ino:  uint64(entry.Inode),
		// Gen:  1,
	}
	newNode := n.NewInode(ctx, ops, st)
	return newNode, 0
}

func attrToStat(inode Ino, attr *meta.Attr, out *fuse.Attr) {
	if inode == meta.RootInode {
		out.Uid = 0
		out.Gid = 0
	} else {
		out.Uid = uint32(os.Getuid())
		out.Gid = uint32(os.Getgid())
	}
	out.Ino = uint64(inode)
	out.Mode = attr.SMode()
	out.Nlink = attr.Nlink
	out.Atime = uint64(attr.Atime)
	out.Atimensec = attr.Atimensec
	out.Mtime = uint64(attr.Mtime)
	out.Mtimensec = attr.Mtimensec
	out.Ctime = uint64(attr.Ctime)
	out.Ctimensec = attr.Ctimensec

	var size, blocks uint64
	switch attr.Typ {
	case meta.TypeDirectory:
		fallthrough
	case meta.TypeFile:
		size = attr.Length
		blocks = (size + 511) / 512
	}
	out.Size = size
	out.Blocks = blocks
	out.Blksize = 4096
}

func (n *Node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) (errno syscall.Errno) {
	var err syscall.Errno
	var attr = &meta.Attr{}
	ino := Ino(n.StableAttr().Ino)
	err = n.meta.GetAttr(ctx, ino, attr)
	if err == 0 {
		entry := &meta.Entry{Inode: ino, Attr: attr}
		attrToStat(entry.Inode, entry.Attr, &out.Attr)
	}
	return err
}

func (n *Node) Statfs(ctx context.Context, out *fuse.StatfsOut) syscall.Errno {
	out.Blocks = uint64(maxSize) / fileBlockSize    // Total data blocks in file system.
	out.Bfree = uint64(maxSize-1e9) / fileBlockSize // Free blocks in file system.
	out.Bavail = out.Bfree                          // Free blocks in file system if you're not root.
	out.Files = 1e9                                 // Total files in file system.
	out.Ffree = 1e9                                 // Free files in file system.
	out.Bsize = fileBlockSize                       // Block size
	out.NameLen = 255                               // Maximum file name length?
	out.Frsize = fileBlockSize                      // Fragment size, smallest addressable data size in the file system.
	return 0
}

func (n *Node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	var err syscall.Errno
	var attr = &meta.Attr{}
	ino := Ino(n.StableAttr().Ino)
	err = n.meta.SetAttr(ctx, ino, in, attr)
	if err == 0 {
		entry := &meta.Entry{Inode: ino, Attr: attr}
		attrToStat(entry.Inode, entry.Attr, &out.Attr)
	}
	return err
}

func (n *Node) Open(ctx context.Context, flags uint32) (fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	fh = &File{
		n: n,
	}
	return fh, 0, 0
}

func (n *Node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (node *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	if len(name) > maxName {
		return nil, nil, 0, syscall.ENAMETOOLONG
	}
	if n.GetChild(name) != nil {
		return nil, nil, 0, syscall.EEXIST
	}
	attr := &meta.Attr{}
	parent := Ino(n.StableAttr().Ino)
	var ino Ino
	n.meta.GetNextInode(ctx, &ino)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, 0, fs.ToErrno(err)
	}
	cipher, ok := n.enc.Encrypt(key, []byte(name))
	if ok != nil {
		return nil, nil, 0, syscall.EINVAL
	}
	keyCipher, ok := n.enc.Encrypt(n.key, key)
	if ok != nil {
		return nil, nil, 0, syscall.EINVAL
	}
	err := n.meta.Mknod(ctx, parent, meta.TypeFile, mode, n.userId, &ino, cipher, keyCipher, attr)
	if err != 0 {
		return nil, nil, 0, err
	}
	_, exist := n.inoMap[name]
	if !exist {
		n.inoMap[name] = ino
	}
	entry := &meta.Entry{Inode: ino, Attr: attr}
	attrToStat(entry.Inode, entry.Attr, &out.Attr)
	ops := &Node{
		inoMap:  n.inoMap,
		meta:    n.meta,
		obj:     n.obj,
		enc:     n.enc,
		privKey: n.privKey,
		key:     key,
		userId:  n.userId,
	}
	st := fs.StableAttr{
		Mode: attr.SMode(),
		Ino:  uint64(entry.Inode),
		// Gen:  1,
	}
	n.NewInode(ctx, ops, st)

	fh = &File{
		n: ops,
	}

	return ops.EmbeddedInode(), fh, 0, 0
}

func (n *Node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	var attr meta.Attr
	var entries []*meta.Entry
	result := make([]fuse.DirEntry, 0)
	inode := Ino(n.StableAttr().Ino)
	if err := n.meta.GetAttr(ctx, inode, &attr); err != 0 {
		return nil, err
	}
	if inode == meta.RootInode {
		attr.Parent = meta.RootInode
	}
	entries = []*meta.Entry{
		{
			Inode: inode,
			Name:  []byte("."),
			Attr:  &meta.Attr{Typ: meta.TypeDirectory},
		},
	}
	entries = append(entries, &meta.Entry{
		Inode: attr.Parent,
		Name:  []byte(".."),
		Attr:  &meta.Attr{Typ: meta.TypeDirectory},
	})
	errno := n.meta.Readdir(ctx, inode, n.userId, &entries)
	if errno != 0 {
		return nil, errno
	}
	var de fuse.DirEntry
	var name, key []byte
	var ok error
	for _, e := range entries {
		if inode == meta.SharedInode {
			key, ok = n.enc.DecryptRSA(n.privKey, e.Key)
		} else {
			key, ok = n.enc.Decrypt(n.key, e.Key)
		}
		if ok != nil {
			return nil, syscall.EINVAL
		}
		name, ok = n.enc.Decrypt(key, e.Name)
		if ok != nil {
			return nil, syscall.EINVAL
		}
		if string(name) != "." && string(name) != ".." {
			if n.inoMap != nil {
				n.inoMap[string(name)] = e.Inode
			}
		}
		de.Ino = uint64(e.Inode)
		de.Name = string(name)
		de.Mode = e.Attr.SMode()
		result = append(result, de)
	}
	return fs.NewListDirStream(result), errno
}

func (n *Node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (node *fs.Inode, errno syscall.Errno) {
	if len(name) > maxName {
		return nil, syscall.ENAMETOOLONG
	}
	if n.GetChild(name) != nil {
		return nil, syscall.EEXIST
	}
	attr := &meta.Attr{}
	parent := Ino(n.StableAttr().Ino)
	var ino Ino
	n.meta.GetNextInode(ctx, &ino)
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fs.ToErrno(err)
	}
	cipher, ok := n.enc.Encrypt(key, []byte(name))
	if ok != nil {
		return nil, syscall.EINVAL
	}
	keyCipher, ok := n.enc.Encrypt(n.key, key)
	if ok != nil {
		return nil, syscall.EINVAL
	}
	err := n.meta.Mknod(ctx, parent, meta.TypeDirectory, mode, n.userId, &ino, cipher, keyCipher, attr)
	if err != 0 {
		return nil, err
	}
	entry := &meta.Entry{Inode: ino, Attr: attr}
	attrToStat(entry.Inode, entry.Attr, &out.Attr)
	ops := &Node{
		inoMap:  make(map[string]Ino),
		meta:    n.meta,
		obj:     n.obj,
		enc:     n.enc,
		privKey: n.privKey,
		key:     key,
		userId:  n.userId,
	}
	st := fs.StableAttr{
		Mode: attr.SMode(),
		Ino:  uint64(entry.Inode),
		// Gen:  1,
	}
	node = n.NewInode(ctx, ops, st)
	// n.AddChild(name, node, true)
	return node, 0
}

func (n *Node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if len(name) > maxName {
		return syscall.ENAMETOOLONG
	}
	if name == "shared" {
		return syscall.EPERM
	}
	if name == "." {
		return syscall.EINVAL
	}
	if name == ".." {
		return syscall.ENOTEMPTY
	}
	ino := n.inoMap[name]
	parent := Ino(n.StableAttr().Ino)
	// node := n.GetChild(name)
	err := n.meta.Rmdir(ctx, parent, ino)
	delete(n.inoMap, name)
	// seems to be done by default
	/*if err == 0 {
		n.RmChild(name)
	}*/
	return err
}

func (n *Node) Unlink(ctx context.Context, name string) syscall.Errno {
	if len(name) > maxName {
		return syscall.ENAMETOOLONG
	}
	child := n.GetChild(name)
	ino := n.inoMap[name]
	parent := Ino(n.StableAttr().Ino)
	err := n.meta.Unlink(ctx, parent, ino)
	delete(n.inoMap, name)
	if err != 0 {
		return err
	}
	errno := n.obj.Delete(child.StableAttr().Ino, "")
	return fs.ToErrno(errno)
}
