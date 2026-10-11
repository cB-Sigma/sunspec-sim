// Package sim is a simulated SunSpec device for tests and demos: a Modbus
// server whose holding registers hold a SunSpec map laid out register by
// register (the "SunS" marker at a base address, then models as ID, L and
// data, then the end model), as a real inverter publishes it (SunSpec
// Device Information Model Specification v1.2, section 6). It speaks
// Modbus TCP, Modbus TCP over TLS with client certificates, and Modbus RTU
// framing over TCP or over any byte stream (a serial port the caller
// opens). Reads outside the map answer exception 2 (illegal data
// address), as devices do; writes are taken only on registers marked
// writable, and a hook can make the device keep a different value (an
// inverter that clamps a setpoint). Faults make it answer with an
// exception, late, or not at all. Build lays out any described model
// from named values, nested groups included; AllModels holds every model;
// DemoInverter builds a ready-made inverter.
package sim

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	mb "github.com/simonvetter/modbus"
)

// Image builds a register map. It is not safe for concurrent use; build
// it before Start.
type Image struct {
	// Base is the address of the marker.
	Base uint16
	regs map[uint16]uint16
	next uint16
	// Writable addresses.
	writable map[uint16]bool
}

// NewImage starts a map with the marker at base.
func NewImage(base uint16) *Image {
	im := &Image{Base: base, regs: map[uint16]uint16{}, writable: map[uint16]bool{}}
	im.regs[base], im.regs[base+1] = 0x5375, 0x6E53
	im.next = base + 2
	return im
}

// Model appends a model with id and its data registers, and returns the
// address of its first data register.
func (im *Image) Model(id uint16, data []uint16) uint16 {
	im.regs[im.next] = id
	im.regs[im.next+1] = uint16(len(data))
	start := im.next + 2
	for i, w := range data {
		im.regs[start+uint16(i)] = w
	}
	im.next = start + uint16(len(data))
	return start
}

// End appends the end model.
func (im *Image) End() {
	im.regs[im.next], im.regs[im.next+1] = 0xFFFF, 0
	im.next += 2
}

// Writable marks addresses as writable.
func (im *Image) Writable(addrs ...uint16) {
	for _, a := range addrs {
		im.writable[a] = true
	}
}

// Str encodes text into n registers, zero-padded.
func Str(s string, n int) []uint16 {
	out := make([]uint16, n)
	b := []byte(s)
	for i := range out {
		var hi, lo byte
		if 2*i < len(b) {
			hi = b[2*i]
		}
		if 2*i+1 < len(b) {
			lo = b[2*i+1]
		}
		out[i] = uint16(hi)<<8 | uint16(lo)
	}
	return out
}

// Filled returns n registers of v (0xFFFF or 0x8000 fill a model whose
// points are not implemented).
func Filled(n int, v uint16) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// Fault makes the device misbehave on the requests it matches.
type Fault struct {
	// From and To are the first and last address a request must touch
	// to match; both 0 match every address.
	From, To uint16
	// Op is "read", "write" or "" (both).
	Op string
	// Unit is the unit a request must address to match; 0 matches every
	// unit.
	Unit uint8
	// Exception is the Modbus exception code to answer with (1 illegal
	// function, 2 illegal data address, 3 illegal data value, 4 device
	// failure, 5 acknowledge, 6 busy, 8 memory parity, 10 gateway path
	// unavailable, 11 gateway target failed to respond); 0 answers
	// normally, after Delay.
	Exception uint8
	// Delay holds the answer back this long (past the client's timeout,
	// a timeout).
	Delay time.Duration
	// Silent never answers: the request is dropped (on Modbus TCP the
	// connection stays open with no reply until the device closes).
	Silent bool
	// Times is the number of matching requests the fault applies to; 0
	// keeps it until ClearFaults.
	Times int
}

// Options shapes a device's server.
type Options struct {
	// Listen is the address ("host:port"; "" or port 0 picks a free port
	// on 127.0.0.1 or the given host).
	Listen string
	// Units are the units it answers on; none: every unit. Others get
	// exception 11 on Modbus TCP and silence on RTU.
	Units []uint8
	// TLSCert, when set, serves Modbus TCP over TLS (Modbus/TCP
	// Security); TLSClientCAs are the CAs a client certificate must chain
	// to (required with TLSCert).
	TLSCert      *tls.Certificate
	TLSClientCAs *x509.CertPool
	// RTU serves Modbus RTU framing (with CRC) over TCP instead of Modbus
	// TCP, as a serial-to-Ethernet gateway does.
	RTU bool
}

// Device serves an image over Modbus.
type Device struct {
	mu     sync.Mutex
	im     *Image
	units  map[uint8]bool
	keep   func(addr, v uint16) uint16
	writes []string
	faults []*Fault
	addr   string
	scheme string
	srv    *mb.ModbusServer
	ln     net.Listener
	conns  map[net.Conn]bool
	done   chan struct{}
	once   sync.Once
}

// NewDevice makes a device for the image that serves nothing until
// ServeRTU (or use Start, StartOptions). The device takes the image over:
// change it afterwards only through Set.
func NewDevice(im *Image, units ...uint8) *Device {
	d := &Device{im: im, units: map[uint8]bool{}, done: make(chan struct{}), conns: map[net.Conn]bool{}}
	for _, u := range units {
		d.units[u] = true
	}
	return d
}

// Start serves the image over Modbus TCP on listen ("host:port"; "" or
// port 0 picks a free port on 127.0.0.1 or the given host), answering on
// the units given (none: every unit; others get exception 11, gateway
// target failed to respond). The device takes the image over: change it
// afterwards only through Set. Close stops it.
func Start(im *Image, listen string, units ...uint8) (*Device, error) {
	return StartOptions(im, Options{Listen: listen, Units: units})
}

// StartOptions serves the image as o says.
func StartOptions(im *Image, o Options) (*Device, error) {
	listen := o.Listen
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, err
	}
	d := NewDevice(im, o.Units...)
	if o.RTU {
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return nil, fmt.Errorf("sunspec sim: listening on %s: %w", listen, err)
		}
		d.ln, d.addr, d.scheme = ln, ln.Addr().String(), "rtuovertcp://"
		go d.acceptRTU()
		return d, nil
	}
	if port == "0" {
		// The server does not report the port it bound, so take a free
		// one first.
		l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
		if err != nil {
			return nil, err
		}
		listen = l.Addr().String()
		_ = l.Close()
	}
	d.addr, d.scheme = listen, "tcp://"
	conf := &mb.ServerConfiguration{URL: "tcp://" + listen, Timeout: 5 * time.Second, MaxClients: 8, Logger: log.New(io.Discard, "", 0)}
	if o.TLSCert != nil {
		if o.TLSClientCAs == nil {
			return nil, errors.New("sunspec sim: TLS needs TLSClientCAs for the client certificates")
		}
		d.scheme = "tcp+tls://"
		conf.URL, conf.TLSServerCert, conf.TLSClientCAs = "tcp+tls://"+listen, o.TLSCert, o.TLSClientCAs
	}
	srv, err := mb.NewServer(conf, d)
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, fmt.Errorf("sunspec sim: listening on %s: %w", listen, err)
	}
	d.srv = srv
	return d, nil
}

// Addr is the address the device listens on ("127.0.0.1:50211").
func (d *Device) Addr() string { return d.addr }

// URL is the device's URL ("tcp://127.0.0.1:50211", "tcp+tls://…",
// "rtuovertcp://…"), as package mbtcp takes it.
func (d *Device) URL() string { return d.scheme + d.addr }

// Close stops the server, closes its connections and releases requests
// held by a Silent or delaying fault.
func (d *Device) Close() error {
	var err error
	d.once.Do(func() {
		close(d.done)
		if d.srv != nil {
			err = d.srv.Stop()
		}
		if d.ln != nil {
			err = d.ln.Close()
			d.mu.Lock()
			for c := range d.conns {
				_ = c.Close()
			}
			d.mu.Unlock()
		}
	})
	return err
}

// SetKeep sets a hook that gives the value the device keeps for a write
// to addr (nil: the value written).
func (d *Device) SetKeep(keep func(addr, v uint16) uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keep = keep
}

// AddFault makes the device misbehave on the requests f matches; the
// first matching fault applies.
func (d *Device) AddFault(f Fault) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.faults = append(d.faults, &f)
}

// ClearFaults removes every fault.
func (d *Device) ClearFaults() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.faults = nil
}

// Get reads a register.
func (d *Device) Get(addr uint16) uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.im.regs[addr]
}

// Set writes registers (writable or not), as the device itself would.
func (d *Device) Set(addr uint16, words ...uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, w := range words {
		d.im.regs[addr+uint16(i)] = w
	}
}

func (d *Device) unit(id uint8) bool { return len(d.units) == 0 || d.units[id] }

// fault returns the fault for a request, counting it down.
func (d *Device) fault(unit uint8, write bool, addr, qty uint16) *Fault {
	d.mu.Lock()
	defer d.mu.Unlock()
	last := int(addr) + int(max(qty, 1)) - 1
	for i, f := range d.faults {
		if f.Unit != 0 && f.Unit != unit || f.Op == "read" && write || f.Op == "write" && !write {
			continue
		}
		if (f.From != 0 || f.To != 0) && (last < int(f.From) || int(addr) > int(f.To)) {
			continue
		}
		out := *f
		if f.Times > 0 {
			f.Times--
			if f.Times == 0 {
				d.faults = append(d.faults[:i:i], d.faults[i+1:]...)
			}
		}
		return &out
	}
	return nil
}

// errSilent marks a request the device does not answer.
var errSilent = errors.New("no answer")

var exceptions = map[uint8]error{1: mb.ErrIllegalFunction, 2: mb.ErrIllegalDataAddress, 3: mb.ErrIllegalDataValue,
	4: mb.ErrServerDeviceFailure, 5: mb.ErrAcknowledge, 6: mb.ErrServerDeviceBusy, 8: mb.ErrMemoryParityError,
	10: mb.ErrGWPathUnavailable, 11: mb.ErrGWTargetFailedToRespond}

var codes = map[error]uint8{}

func init() {
	for c, e := range exceptions {
		codes[e] = c
	}
}

// holding serves a holding register request: the registers read, or an
// exception error, or errSilent.
func (d *Device) holding(unit uint8, write bool, addr, qty uint16, args []uint16) ([]uint16, error) {
	if f := d.fault(unit, write, addr, qty); f != nil {
		if f.Silent {
			<-d.done
			return nil, errSilent
		}
		if f.Delay > 0 {
			select {
			case <-time.After(f.Delay):
			case <-d.done:
				return nil, errSilent
			}
		}
		if f.Exception != 0 {
			if e, ok := exceptions[f.Exception]; ok {
				return nil, e
			}
			return nil, mb.ErrServerDeviceFailure
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if write {
		for i := range args {
			if !d.im.writable[addr+uint16(i)] {
				return nil, mb.ErrIllegalDataAddress
			}
		}
		for i, v := range args {
			a := addr + uint16(i)
			if d.keep != nil {
				v = d.keep(a, v)
			}
			d.im.regs[a] = v
			d.writes = append(d.writes, fmt.Sprintf("%d=%d", a, args[i]))
		}
		return nil, nil
	}
	out := make([]uint16, qty)
	for i := range out {
		v, ok := d.im.regs[addr+uint16(i)]
		if !ok {
			return nil, mb.ErrIllegalDataAddress
		}
		out[i] = v
	}
	return out, nil
}

// HandleHoldingRegisters serves function codes 3, 6 and 16 (it is the
// Modbus TCP server's request handler).
func (d *Device) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	if !d.unit(r.UnitId) {
		return nil, mb.ErrGWTargetFailedToRespond
	}
	out, err := d.holding(r.UnitId, r.IsWrite, r.Addr, r.Quantity, r.Args)
	if err == errSilent {
		return nil, mb.ErrServerDeviceFailure // only once the device is closing
	}
	return out, err
}

// HandleInputRegisters answers illegal function: a SunSpec map is in
// holding registers only.
func (d *Device) HandleInputRegisters(*mb.InputRegistersRequest) ([]uint16, error) {
	return nil, mb.ErrIllegalFunction
}

// HandleCoils answers illegal function.
func (d *Device) HandleCoils(*mb.CoilsRequest) ([]bool, error) { return nil, mb.ErrIllegalFunction }

// HandleDiscreteInputs answers illegal function.
func (d *Device) HandleDiscreteInputs(*mb.DiscreteInputsRequest) ([]bool, error) {
	return nil, mb.ErrIllegalFunction
}

// WriteLog returns the writes so far, as "address=value" with the value
// written (before the SetKeep hook).
func (d *Device) WriteLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.writes...)
}

func (d *Device) acceptRTU() {
	for {
		c, err := d.ln.Accept()
		if err != nil {
			return
		}
		d.mu.Lock()
		d.conns[c] = true
		d.mu.Unlock()
		go func() {
			_ = d.ServeRTU(c)
			_ = c.Close()
			d.mu.Lock()
			delete(d.conns, c)
			d.mu.Unlock()
		}()
	}
}

// crc16 is the Modbus RTU check (polynomial 0xA001, reflected, from
// 0xFFFF), sent low byte first.
func crc16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, x := range b {
		crc ^= uint16(x)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// ServeRTU answers Modbus RTU frames read from rw (a serial port the
// caller opened, or a connection) until rw fails or the device closes:
// functions 3, 6 and 16 on the holding registers, exception 1 for
// functions 1 to 5 and 15 otherwise; a frame with a bad check, a unit the
// device does not answer, or a broadcast (unit 0) gets no reply.
func (d *Device) ServeRTU(rw io.ReadWriter) error {
	for {
		select {
		case <-d.done:
			return nil
		default:
		}
		frame, err := readRTU(rw)
		if err != nil {
			return err
		}
		if len(frame) < 4 || crc16(frame[:len(frame)-2]) != binary.LittleEndian.Uint16(frame[len(frame)-2:]) {
			continue
		}
		unit, fc := frame[0], frame[1]
		if unit == 0 || !d.unit(unit) {
			continue
		}
		resp := d.rtuAnswer(unit, fc, frame[2:len(frame)-2])
		if resp == nil {
			continue
		}
		resp = append([]byte{unit}, resp...)
		resp = binary.LittleEndian.AppendUint16(resp, crc16(resp))
		if _, err := rw.Write(resp); err != nil {
			return err
		}
	}
}

// readRTU reads one request frame, its length known from its function.
func readRTU(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	var rest []byte
	switch hdr[1] {
	case 1, 2, 3, 4, 5, 6:
		rest = make([]byte, 6)
		if _, err := io.ReadFull(r, rest); err != nil {
			return nil, err
		}
	case 15, 16:
		head := make([]byte, 5)
		if _, err := io.ReadFull(r, head); err != nil {
			return nil, err
		}
		tail := make([]byte, int(head[4])+2)
		if _, err := io.ReadFull(r, tail); err != nil {
			return nil, err
		}
		rest = append(head, tail...)
	default:
		return nil, fmt.Errorf("sunspec sim: RTU function %d cannot be framed", hdr[1])
	}
	return append(hdr, rest...), nil
}

// rtuAnswer is the PDU answering one request PDU (nil: no answer).
func (d *Device) rtuAnswer(unit, fc uint8, pdu []byte) []byte {
	exc := func(code uint8) []byte { return []byte{fc | 0x80, code} }
	if len(pdu) < 4 {
		return exc(3)
	}
	addr, qty := binary.BigEndian.Uint16(pdu), binary.BigEndian.Uint16(pdu[2:])
	var (
		out []uint16
		err error
	)
	switch fc {
	case 3:
		if qty < 1 || qty > 125 {
			return exc(3)
		}
		out, err = d.holding(unit, false, addr, qty, nil)
	case 6:
		_, err = d.holding(unit, true, addr, 1, []uint16{qty})
	case 16:
		if qty < 1 || qty > 123 || len(pdu) < 5 || int(pdu[4]) != 2*int(qty) || len(pdu) < 5+2*int(qty) {
			return exc(3)
		}
		args := make([]uint16, qty)
		for i := range args {
			args[i] = binary.BigEndian.Uint16(pdu[5+2*i:])
		}
		_, err = d.holding(unit, true, addr, qty, args)
	default:
		return exc(1)
	}
	switch {
	case err == errSilent:
		return nil
	case err != nil:
		code, ok := codes[err]
		if !ok {
			code = 4
		}
		return exc(code)
	case fc == 3:
		b := []byte{3, byte(2 * len(out))}
		for _, w := range out {
			b = binary.BigEndian.AppendUint16(b, w)
		}
		return b
	}
	return append([]byte{fc}, pdu[:4]...)
}
