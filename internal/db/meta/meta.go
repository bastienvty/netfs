package meta

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/bastienvty/netsecfs/utils"
	_ "github.com/mattn/go-sqlite3"
	"github.com/pkg/errors"
	"xorm.io/xorm"
	"xorm.io/xorm/names"
)

var logger = utils.GetLogger("juicefs")

type setting struct {
	Name  string `xorm:"pk"`
	Value string `xorm:"varchar(4096) notnull"`
}

type edge struct {
	Id     int64  `xorm:"pk bigserial"`
	Parent Ino    `xorm:"unique(edge) notnull"`
	Name   []byte `xorm:"unique(edge) varbinary(255) notnull"`
	Inode  Ino    `xorm:"index notnull"`
	Type   uint8  `xorm:"notnull"`
}

type node struct {
	Inode     Ino    `xorm:"pk"`
	Type      uint8  `xorm:"notnull"`
	Mode      uint16 `xorm:"notnull"`
	Uid       uint32 `xorm:"notnull"`
	Gid       uint32 `xorm:"notnull"`
	Atime     int64  `xorm:"notnull"`
	Mtime     int64  `xorm:"notnull"`
	Ctime     int64  `xorm:"notnull"`
	Atimensec int16  `xorm:"notnull default 0"`
	Mtimensec int16  `xorm:"notnull default 0"`
	Ctimensec int16  `xorm:"notnull default 0"`
	Nlink     uint32 `xorm:"notnull"`
	Length    uint64 `xorm:"notnull"`
	Rdev      uint32
	Parent    Ino
}

type namedNode struct {
	node `xorm:"extends"`
	Name []byte `xorm:"varbinary(255)"`
}

type user struct {
	Id       uint32 `xorm:"pk autoincr"`
	Username string `xorm:"notnull unique"`
	Password string `xorm:"notnull"`
}

type dbMeta struct {
	sync.Mutex
	db   *xorm.Engine
	addr string
	fmt  *Format

	root       Ino
	dirParents map[Ino]Ino
	parentMu   sync.Mutex // protect dirParents
}

func (m *dbMeta) Name() string {
	return m.addr
}

func (m *dbMeta) Load() (*Format, error) {
	body, err := m.doLoad()
	if err == nil && len(body) == 0 {
		err = fmt.Errorf("database is not formatted, please run `juicefs format ...` first")
	}
	if err != nil {
		return nil, err
	}
	var format = new(Format)
	if err = json.Unmarshal(body, format); err != nil {
		return nil, fmt.Errorf("json: %s", err)
	}
	m.Lock()
	m.fmt = format
	m.Unlock()
	return format, nil
}

func (m *dbMeta) doLoad() (data []byte, err error) {
	err = m.roTxn(func(ses *xorm.Session) error {
		if ok, err := ses.IsTableExist(&setting{}); err != nil {
			return err
		} else if !ok {
			return nil
		}
		s := setting{Name: "format"}
		ok, err := ses.Get(&s)
		if err == nil && ok {
			data = []byte(s.Value)
		}
		return err
	})
	return
}

func (m *dbMeta) Init(format *Format) error {
	if err := m.db.Sync2(new(setting)); err != nil {
		return fmt.Errorf("create table setting, counter: %s", err)
	}
	if err := m.db.Sync2(new(edge)); err != nil {
		return fmt.Errorf("create table edge: %s", err)
	}
	if err := m.db.Sync2(new(node), new(user)); err != nil {
		return fmt.Errorf("create table node, user: %s", err)
	}

	var s = setting{Name: "format"}
	var ok bool
	err := m.roTxn(func(ses *xorm.Session) (err error) {
		ok, err = ses.Get(&s)
		return err
	})
	if err != nil {
		return err
	}

	if ok {
		var old Format
		err = json.Unmarshal([]byte(s.Value), &old)
		if err != nil {
			return fmt.Errorf("json: %s", err)
		}
		if err = format.update(&old); err != nil {
			return errors.Wrap(err, "update format")
		}
	}

	data, err := json.MarshalIndent(format, "", "")
	if err != nil {
		return fmt.Errorf("json: %s", err)
	}

	m.fmt = format
	now := time.Now()
	n := &node{
		Type:      TypeDirectory,
		Atime:     now.UnixNano() / 1e3,
		Mtime:     now.UnixNano() / 1e3,
		Ctime:     now.UnixNano() / 1e3,
		Atimensec: int16(now.UnixNano() % 1e3),
		Mtimensec: int16(now.UnixNano() % 1e3),
		Ctimensec: int16(now.UnixNano() % 1e3),
		Nlink:     2,
		Length:    4 << 10,
		Parent:    1,
	}
	return m.txn(func(s *xorm.Session) error {
		if ok {
			_, err = s.Update(&setting{"format", string(data)}, &setting{Name: "format"})
			return err
		} else {
			var set = &setting{"format", string(data)}
			if n, err := s.Insert(set); err != nil {
				return err
			} else if n == 0 {
				return fmt.Errorf("format is not inserted")
			}
		}

		n.Inode = 1
		n.Mode = 0777 // allow operations on root
		/*var cs = []counter{
			{"nextInode", 2}, // 1 is root
			{"nextChunk", 1},
			{"nextSession", 0},
			{"usedSpace", 0},
			{"totalInodes", 0},
			{"nextCleanupSlices", 0},
		}*/
		return mustInsert(s, n)
	})
}

func (m *dbMeta) Shutdown() error {
	return m.db.Close()
}

func (m *dbMeta) parseAttr(n *node, attr *Attr) {
	if attr == nil || n == nil {
		return
	}
	attr.Typ = n.Type
	attr.Mode = n.Mode
	attr.Uid = n.Uid
	attr.Gid = n.Gid
	attr.Atime = n.Atime / 1e6
	attr.Atimensec = uint32(n.Atime%1e6*1000) + uint32(n.Atimensec)
	attr.Mtime = n.Mtime / 1e6
	attr.Mtimensec = uint32(n.Mtime%1e6*1000) + uint32(n.Mtimensec)
	attr.Ctime = n.Ctime / 1e6
	attr.Ctimensec = uint32(n.Ctime%1e6*1000) + uint32(n.Ctimensec)
	attr.Nlink = n.Nlink
	attr.Length = n.Length
	attr.Rdev = n.Rdev
	attr.Parent = n.Parent
	attr.Full = true
}

func mustInsert(s *xorm.Session, beans ...interface{}) error {
	for start, end, size := 0, 0, len(beans); end < size; start = end {
		end = start + 200
		if end > size {
			end = size
		}
		if n, err := s.Insert(beans[start:end]...); err != nil {
			return err
		} else if d := end - start - int(n); d > 0 {
			return fmt.Errorf("%d records not inserted: %+v", d, beans[start:end])
		}
	}
	return nil
}

var errBusy error

func (m *dbMeta) shouldRetry(err error) bool {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "too many connections") || strings.Contains(msg, "too many clients") {
		logger.Warnf("transaction failed: %s, will retry it. please increase the max number of connections in your database, or use a connection pool.", msg)
		return true
	}
	return errors.Is(err, errBusy) || strings.Contains(msg, "database is locked")
}

func (m *dbMeta) txn(f func(s *xorm.Session) error, inodes ...Ino) error {
	start := time.Now()

	inodes = []Ino{1}
	var lastErr error
	for i := 0; i < 50; i++ {
		_, err := m.db.Transaction(func(s *xorm.Session) (interface{}, error) {
			return nil, f(s)
		})
		if eno, ok := err.(syscall.Errno); ok && eno == 0 {
			err = nil
		}
		if err != nil && m.shouldRetry(err) {
			logger.Debugf("Transaction failed, restart it (tried %d): %s", i+1, err)
			lastErr = err
			time.Sleep(time.Millisecond * time.Duration(i*i))
			continue
		} else if err == nil && i > 1 {
			logger.Warnf("Transaction succeeded after %d tries (%s), inodes: %v, last error: %s", i+1, time.Since(start), inodes, lastErr)
		}
		return err
	}
	logger.Warnf("Already tried 50 times, returning: %s", lastErr)
	return lastErr
}

func (m *dbMeta) roTxn(f func(s *xorm.Session) error) error {
	start := time.Now()
	s := m.db.NewSession()
	defer s.Close()

	var lastErr error
	for i := 0; i < 50; i++ {
		err := f(s)
		if eno, ok := err.(syscall.Errno); ok && eno == 0 {
			err = nil
		}
		_ = s.Rollback()
		if err != nil && m.shouldRetry(err) {
			logger.Debugf("Read transaction failed, restart it (tried %d): %s", i+1, err)
			lastErr = err
			time.Sleep(time.Millisecond * time.Duration(i*i))
			continue
		} else if err == nil && i > 1 {
			logger.Warnf("Read transaction succeeded after %d tries (%s), last error: %s", i+1, time.Since(start), lastErr)
		}
		return err
	}
	logger.Warnf("Already tried 50 times, returning: %s", lastErr)
	return lastErr
}

/*func (m *dbMeta) GetAttr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) (ch *fs.Inode, errno syscall.Errno) {
	return nil, syscall.ENOSYS
}*/

func (m *dbMeta) doLookup(ctx context.Context, parent Ino, name string, inode *Ino, attr *Attr) syscall.Errno {
	err := m.roTxn(func(s *xorm.Session) error {
		s = s.Table(&edge{})
		nn := namedNode{node: node{Parent: parent}, Name: []byte(name)}
		var exist bool
		var err error
		if attr != nil {
			s = s.Join("INNER", &node{}, "jfs_edge.inode=jfs_node.inode")
			exist, err = s.Select("jfs_node.*").Get(&nn)
		} else {
			exist, err = s.Select("*").Get(&nn)
		}
		if err != nil {
			return err
		}
		if !exist {
			return syscall.ENOENT
		}
		*inode = nn.Inode
		m.parseAttr(&nn.node, attr)
		// fmt.Println("LOOKUP", parent, name, inode, attr)
		return nil
	})
	if eno, ok := err.(syscall.Errno); ok {
		return eno
	}
	return 0
}

func newSQLMeta(driver, addr string) (Meta, error) {
	engine, err := xorm.NewEngine(driver, addr)
	if err != nil {
		return nil, fmt.Errorf("unable to use data source %s: %s", driver, err)
	}

	start := time.Now()
	if err = engine.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %s", err)
	}
	if time.Since(start) > time.Millisecond*5 {
		logger.Warnf("The latency to database is too high: %s", time.Since(start))
	}
	engine.DB().SetMaxIdleConns(runtime.GOMAXPROCS(-1) * 2)
	engine.DB().SetConnMaxIdleTime(time.Minute * 5)
	engine.SetTableMapper(names.NewPrefixMapper(engine.GetTableMapper(), "nsfs_"))
	m := &dbMeta{
		db:         engine,
		addr:       addr,
		root:       RootInode,
		dirParents: make(map[Ino]Ino),
	}
	return m, nil
}
