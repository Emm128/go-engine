package proxy

import (
	"context"
	"encoding/binary"
	"errors"
	"github.com/esrrhs/go-engine/src/common"
	"github.com/esrrhs/go-engine/src/conn"
	"github.com/esrrhs/go-engine/src/loggo"
	"github.com/golang/protobuf/proto"
	"golang.org/x/sync/errgroup"
	"io"
	"strconv"
	"sync"
	"time"
)

type Config struct {
	MaxMsgSize         int // 消息最大长度
	MainBuffer         int // 主通道buffer最大长度
	ConnBuffer         int // 每个conn buffer最大长度
	EstablishedTimeout int // 主通道登录超时
	PingInter          int // 主通道ping间隔
	PingTimeoutInter   int // 主通道ping超时间隔
	ConnTimeout        int // 每个conn的不活跃超时时间
	ConnectTimeout     int // 每个conn的连接超时
	Proto              string
	Key                string
	Encrypt            string
	Compress           int
}

func DefaultConfig() *Config {
	return &Config{
		MaxMsgSize:         1024 * 1024,
		MainBuffer:         1024 * 1024,
		ConnBuffer:         1024,
		EstablishedTimeout: 10,
		PingInter:          1,
		PingTimeoutInter:   5,
		ConnTimeout:        300,
		ConnectTimeout:     10,
		Proto:              "tcp",
	}
}

type ProxyConn struct {
	conn        conn.Conn
	established bool
	sendch      chan *ProxyFrame
	recvch      chan *ProxyFrame
	actived     int
	pinged      int
	id          string
	needclose   bool
}

func checkProxyFame(f *ProxyFrame) error {
	switch f.Type {
	case FRAME_TYPE_LOGIN:
		if f.LoginFrame == nil {
			return errors.New("LoginFrame nil")
		}
	case FRAME_TYPE_LOGINRSP:
		if f.LoginRspFrame == nil {
			return errors.New("LoginRspFrame nil")
		}
	case FRAME_TYPE_DATA:
		if f.DataFrame == nil {
			return errors.New("DataFrame nil")
		}
	case FRAME_TYPE_PING:
		if f.PingFrame == nil {
			return errors.New("PingFrame nil")
		}
	case FRAME_TYPE_PONG:
		if f.PongFrame == nil {
			return errors.New("PongFrame nil")
		}
	case FRAME_TYPE_OPEN:
		if f.OpenFrame == nil {
			return errors.New("OpenFrame nil")
		}
	case FRAME_TYPE_OPENRSP:
		if f.OpenRspFrame == nil {
			return errors.New("OpenRspFrame nil")
		}
	case FRAME_TYPE_CLOSE:
		if f.CloseFrame == nil {
			return errors.New("CloseFrame nil")
		}
	default:
		return errors.New("Type error")
	}

	return nil
}

func MarshalSrpFrame(f *ProxyFrame, compress int, encrpyt string) ([]byte, error) {

	err := checkProxyFame(f)
	if err != nil {
		return nil, err
	}

	if f.Type == FRAME_TYPE_DATA && compress > 0 && len(f.DataFrame.Data) > compress {
		newb := common.CompressData(f.DataFrame.Data)
		if len(newb) < len(f.DataFrame.Data) {
			f.DataFrame.Data = newb
			f.DataFrame.Compress = true
		}
	}

	if f.Type == FRAME_TYPE_DATA && encrpyt != "" {
		newb, err := common.Rc4(encrpyt, f.DataFrame.Data)
		if err != nil {
			return nil, err
		}
		f.DataFrame.Data = newb
	}

	mb, err := proto.Marshal(f)
	if err != nil {
		return nil, err
	}
	return mb, err
}

func UnmarshalSrpFrame(b []byte, encrpyt string) (*ProxyFrame, error) {

	f := &ProxyFrame{}
	err := proto.Unmarshal(b, f)
	if err != nil {
		return nil, err
	}

	err = checkProxyFame(f)
	if err != nil {
		return nil, err
	}

	if f.Type == FRAME_TYPE_DATA && encrpyt != "" {
		newb, err := common.Rc4(encrpyt, f.DataFrame.Data)
		if err != nil {
			return nil, err
		}
		f.DataFrame.Data = newb
	}

	if f.Type == FRAME_TYPE_DATA && f.DataFrame.Compress {
		newb, err := common.DeCompressData(f.DataFrame.Data)
		if err != nil {
			return nil, err
		}
		f.DataFrame.Data = newb
		f.DataFrame.Compress = false
	}

	return f, nil
}

func recvFrom(ctx context.Context, recvch chan<- *ProxyFrame, conn conn.Conn, maxmsgsize int, encrypt string) error {
	defer common.CrashLog()

	bs := make([]byte, 4)
	ds := make([]byte, maxmsgsize)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			_, err := io.ReadFull(conn, bs)
			if err != nil {
				loggo.Error("recvFrom ReadFull fail: %s %s", conn.Info(), err.Error())
				return err
			}

			len := binary.LittleEndian.Uint32(bs)
			if len > uint32(maxmsgsize) {
				loggo.Error("recvFrom len fail: %s %d", conn.Info(), len)
				return errors.New("msg len fail " + strconv.Itoa(int(len)))
			}

			_, err = io.ReadFull(conn, ds[0:len])
			if err != nil {
				loggo.Error("recvFrom ReadFull fail: %s %s", conn.Info(), err.Error())
				return err
			}

			f, err := UnmarshalSrpFrame(ds[0:len], encrypt)
			if err != nil {
				loggo.Error("recvFrom UnmarshalSrpFrame fail: %s %s", conn.Info(), err.Error())
				return err
			}

			recvch <- f
		}
	}
}

func sendTo(ctx context.Context, sendch <-chan *ProxyFrame, conn conn.Conn, compress int, maxmsgsize int, encrypt string) error {
	defer common.CrashLog()

	bs := make([]byte, 4)

	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-sendch:
			mb, err := MarshalSrpFrame(f, compress, encrypt)
			if err != nil {
				loggo.Error("sendTo MarshalSrpFrame fail: %s %s", conn.Info(), err.Error())
				return err
			}

			len := uint32(len(mb))
			if len > uint32(maxmsgsize) {
				loggo.Error("sendTo len fail: %s %d", conn.Info(), len)
				return errors.New("msg len fail " + strconv.Itoa(int(len)))
			}

			binary.LittleEndian.PutUint32(bs, len)
			_, err = conn.Write(bs)
			if err != nil {
				loggo.Error("sendTo Write fail: %s %s", conn.Info(), err.Error())
				return err
			}

			_, err = conn.Write(mb)
			if err != nil {
				loggo.Error("sendTo Write fail: %s %s", conn.Info(), err.Error())
				return err
			}
		}
	}
}

func checkPingActive(ctx context.Context, sendch chan<- *ProxyFrame, recvch <-chan *ProxyFrame, proxyconn *ProxyConn,
	estimeout int, pinginter int, pingintertimeout int) error {
	defer common.CrashLog()

	n := 0
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(time.Second):
		n++
		if !proxyconn.established {
			if n > estimeout {
				loggo.Error("checkPingActive established timeout %s", proxyconn.conn.Info())
				return errors.New("established timeout")
			}
		} else {
			break
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(pinginter) * time.Second):
			if proxyconn.pinged > pingintertimeout {
				loggo.Error("checkPingActive ping pong timeout %s", proxyconn.conn.Info())
				return errors.New("ping pong timeout")
			}

			f := &ProxyFrame{}
			f.Type = FRAME_TYPE_PING
			f.PingFrame = &PingFrame{}
			f.PingFrame.Time = time.Now().UnixNano()
			sendch <- f
			proxyconn.pinged++
			loggo.Info("ping %s", proxyconn.conn.Info())
		}
	}
}

func checkNeedClose(ctx context.Context, proxyconn *ProxyConn) error {
	defer common.CrashLog()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
			if proxyconn.needclose {
				loggo.Error("checkNeedClose needclose %s", proxyconn.conn.Info())
				return errors.New("needclose")
			}
		}
	}
}

func processPing(ctx context.Context, f *ProxyFrame, sendch chan<- *ProxyFrame, proxyconn *ProxyConn) {
	rf := &ProxyFrame{}
	rf.Type = FRAME_TYPE_PONG
	rf.PongFrame = &PongFrame{}
	rf.PongFrame.Time = f.PingFrame.Time
	sendch <- rf
}

func processPong(ctx context.Context, f *ProxyFrame, sendch chan<- *ProxyFrame, proxyconn *ProxyConn) {
	elapse := time.Duration(time.Now().UnixNano() - f.PongFrame.Time)
	proxyconn.pinged = 0
	loggo.Info("pong from %s %s", proxyconn.conn.Info(), elapse.String())
}

func checkSonnyActive(ctx context.Context, proxyconn *ProxyConn, estimeout int, timeout int) error {
	defer common.CrashLog()

	n := 0
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(time.Second):
		n++
		if !proxyconn.established {
			if n > estimeout {
				loggo.Error("checkSonnyActive established timeout %s %s", proxyconn.conn.Info())
				return errors.New("established timeout")
			}
		} else {
			break
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Duration(timeout) * time.Second):
			if proxyconn.actived == 0 {
				loggo.Error("checkSonnyActive timeout %s %s %s", proxyconn.conn.Info())
				return errors.New("conn timeout")
			}
			proxyconn.actived = 0
		}
	}
}

func copySonnyRecv(ctx context.Context, recvch <-chan *ProxyFrame, proxyConn *ProxyConn) error {
	defer common.CrashLog()

	for {
		select {
		case <-ctx.Done():
			return nil
		case f := <-recvch:
			proxyConn.sendch <- f
			proxyConn.actived++
		}
	}
}

func NewInputer(ctx context.Context, wg *errgroup.Group, proto string, addr string, clienttype CLIENT_TYPE, config *Config, father *ProxyConn) (*Inputer, error) {
	conn, err := conn.NewConn(proto)
	if conn == nil {
		return nil, err
	}

	listenconn, err := conn.Listen(addr)
	if err != nil {
		return nil, err
	}

	input := &Inputer{
		clienttype: clienttype,
		config:     config,
		proto:      proto,
		addr:       addr,
		father:     father,
		listenconn: listenconn,
	}

	wg.Go(func() error {
		return input.listen(ctx)
	})

	return input, nil
}

type Inputer struct {
	clienttype CLIENT_TYPE
	config     *Config
	proto      string
	addr       string
	father     *ProxyConn

	listenconn conn.Conn
	sonny      sync.Map
}

func (i *Inputer) Close() {
	i.listenconn.Close()
}

func (i *Inputer) processDataFrame(f *ProxyFrame) {
	id := f.DataFrame.Id
	v, ok := i.sonny.Load(id)
	if !ok {
		return
	}
	sonny := v.(*ProxyConn)
	sonny.sendch <- f
	sonny.actived++
}

func (i *Inputer) processOpenRspFrame(f *ProxyFrame) {
	id := f.OpenRspFrame.Id
	v, ok := i.sonny.Load(id)
	if !ok {
		return
	}
	sonny := v.(*ProxyConn)
	if f.OpenRspFrame.Ret {
		sonny.established = true
		loggo.Info("Inputer processOpenRspFrame ok %s %s %s", id, sonny.conn.Info())
	} else {
		sonny.needclose = true
		loggo.Info("Inputer processOpenRspFrame fail %s %s %s", id, sonny.conn.Info())
	}
}

func (i *Inputer) listen(ctx context.Context) error {

	defer common.CrashLog()

	loggo.Info("Inputer start listen %s", i.addr)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
			conn, err := i.listenconn.Accept()
			if err != nil {
				continue
			}
			proxyconn := &ProxyConn{conn: conn}
			go i.processProxyConn(ctx, proxyconn)
		}
	}
}

func (i *Inputer) processProxyConn(fctx context.Context, proxyConn *ProxyConn) {

	defer common.CrashLog()

	proxyConn.id = common.UniqueId()

	loggo.Info("Inputer processProxyConn start %s %s", proxyConn.id, proxyConn.conn.Info())

	_, loaded := i.sonny.LoadOrStore(proxyConn.id, proxyConn)
	if loaded {
		loggo.Error("Inputer processProxyConn LoadOrStore fail %s", proxyConn.id)
		proxyConn.conn.Close()
		return
	}

	sendch := make(chan *ProxyFrame, i.config.ConnBuffer)
	recvch := make(chan *ProxyFrame, i.config.ConnBuffer)

	proxyConn.sendch = sendch
	proxyConn.recvch = recvch

	wg, ctx := errgroup.WithContext(fctx)

	i.openConn(ctx, proxyConn)

	wg.Go(func() error {
		return recvFrom(ctx, recvch, proxyConn.conn, i.config.MaxMsgSize, i.config.Encrypt)
	})

	wg.Go(func() error {
		return sendTo(ctx, sendch, proxyConn.conn, i.config.Compress, i.config.MaxMsgSize, i.config.Encrypt)
	})

	wg.Go(func() error {
		return checkSonnyActive(ctx, proxyConn, i.config.EstablishedTimeout, i.config.ConnTimeout)
	})

	wg.Go(func() error {
		return checkNeedClose(ctx, proxyConn)
	})

	wg.Go(func() error {
		return copySonnyRecv(ctx, recvch, i.father)
	})

	wg.Wait()
	proxyConn.conn.Close()
	i.sonny.Delete(proxyConn.id)
	close(sendch)
	close(recvch)

	loggo.Info("Inputer processProxyConn end %s %s", proxyConn.id, proxyConn.conn.Info())
}

func (i *Inputer) openConn(ctx context.Context, proxyConn *ProxyConn) {
	f := &ProxyFrame{}
	f.Type = FRAME_TYPE_OPEN
	f.OpenFrame = &OpenConnFrame{}
	f.OpenFrame.Id = proxyConn.id

	i.father.sendch <- f
	loggo.Info("Inputer openConn %s", proxyConn.id)
}

func NewOutputer(ctx context.Context, wg *errgroup.Group, proto string, addr string, clienttype CLIENT_TYPE, config *Config, father *ProxyConn) (*Outputer, error) {
	conn, err := conn.NewConn(proto)
	if conn == nil {
		return nil, err
	}

	output := &Outputer{
		clienttype: clienttype,
		config:     config,
		conn:       conn,
		proto:      proto,
		addr:       addr,
		father:     father,
	}
	return output, nil
}

type Outputer struct {
	clienttype CLIENT_TYPE
	config     *Config
	proto      string
	addr       string
	father     *ProxyConn

	conn  conn.Conn
	sonny sync.Map
}

func (o *Outputer) Close() {
	o.conn.Close()
}

func (o *Outputer) processDataFrame(f *ProxyFrame) {
	id := f.DataFrame.Id
	v, ok := o.sonny.Load(id)
	if !ok {
		return
	}
	sonny := v.(*ProxyConn)
	sonny.sendch <- f
	sonny.actived++
}

func (o *Outputer) processOpenFrame(ctx context.Context, f *ProxyFrame) {

	id := f.OpenFrame.Id

	rf := &ProxyFrame{}
	rf.Type = FRAME_TYPE_OPENRSP
	rf.OpenRspFrame = &OpenConnRspFrame{}
	rf.OpenRspFrame.Id = id

	conn, err := o.conn.Dial(o.addr)
	if err != nil {
		rf.OpenRspFrame.Ret = false
		rf.OpenRspFrame.Msg = "Dial fail"
		o.father.sendch <- rf
		loggo.Error("Outputer processOpenFrame Dial fail %s %s", o.addr, err.Error())
		return
	}

	proxyconn := &ProxyConn{id: id, conn: conn, established: true}
	go o.processProxyConn(ctx, proxyconn)
}

func (o *Outputer) processProxyConn(fctx context.Context, proxyConn *ProxyConn) {
	defer common.CrashLog()

	loggo.Info("Outputer processProxyConn start %s %s", proxyConn.id, proxyConn.conn.Info())

	_, loaded := o.sonny.LoadOrStore(proxyConn.id, proxyConn)
	if loaded {
		loggo.Error("Outputer processProxyConn LoadOrStore fail %s", proxyConn.id)
		proxyConn.conn.Close()
		return
	}

	sendch := make(chan *ProxyFrame, o.config.ConnBuffer)
	recvch := make(chan *ProxyFrame, o.config.ConnBuffer)

	proxyConn.sendch = sendch
	proxyConn.recvch = recvch

	wg, ctx := errgroup.WithContext(fctx)

	wg.Go(func() error {
		return recvFrom(ctx, recvch, proxyConn.conn, o.config.MaxMsgSize, o.config.Encrypt)
	})

	wg.Go(func() error {
		return sendTo(ctx, sendch, proxyConn.conn, o.config.Compress, o.config.MaxMsgSize, o.config.Encrypt)
	})

	wg.Go(func() error {
		return checkSonnyActive(ctx, proxyConn, o.config.EstablishedTimeout, o.config.ConnTimeout)
	})

	wg.Go(func() error {
		return checkNeedClose(ctx, proxyConn)
	})

	wg.Go(func() error {
		return copySonnyRecv(ctx, recvch, o.father)
	})

	wg.Wait()
	proxyConn.conn.Close()
	o.sonny.Delete(proxyConn.id)
	close(sendch)
	close(recvch)

	loggo.Info("Inputer processProxyConn end %s %s", proxyConn.id, proxyConn.conn.Info())
}
