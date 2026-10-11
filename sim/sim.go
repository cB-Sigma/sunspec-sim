// Package sim is a simulated SunSpec device for tests and demos: a Modbus
// TCP server whose holding registers hold a SunSpec map laid out register
// by register (the "SunS" marker at a base address, then models as ID, L
// and data, then the end model), as a real inverter publishes it
// (SunSpec Device Information Model Specification v1.2, section 6).
// Reads outside the map answer exception 2 (illegal data address), as
// devices do; writes are taken only on registers marked writable, and a
// hook can make the device keep a different value (an inverter that
// clamps a setpoint). Demo builds a ready-made inverter image.
package sim

import (
	"fmt"
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

// Device serves an image over Modbus TCP.
type Device struct {
	mu     sync.Mutex
	im     *Image
	units  map[uint8]bool
	keep   func(addr, v uint16) uint16
	writes []string
	addr   string
	srv    *mb.ModbusServer
}

// Start serves the image on listen ("host:port"; "" or port 0 picks a
// free port on 127.0.0.1 or the given host), answering on the units given
// (none: every unit; others get exception 11, gateway target failed to
// respond). The device takes the image over: change it afterwards only
// through Set. Close stops it.
func Start(im *Image, listen string, units ...uint8) (*Device, error) {
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return nil, err
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
	d := &Device{im: im, units: map[uint8]bool{}, addr: listen}
	for _, u := range units {
		d.units[u] = true
	}
	srv, err := mb.NewServer(&mb.ServerConfiguration{URL: "tcp://" + listen, Timeout: 5 * time.Second, MaxClients: 8}, d)
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

// URL is the device's Modbus TCP URL ("tcp://127.0.0.1:50211").
func (d *Device) URL() string { return "tcp://" + d.addr }

// Close stops the server and closes its connections.
func (d *Device) Close() error { return d.srv.Stop() }

// SetKeep sets a hook that gives the value the device keeps for a write
// to addr (nil: the value written).
func (d *Device) SetKeep(keep func(addr, v uint16) uint16) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keep = keep
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

// HandleHoldingRegisters serves function codes 3, 6 and 16 (it is the
// server's request handler).
func (d *Device) HandleHoldingRegisters(r *mb.HoldingRegistersRequest) ([]uint16, error) {
	if !d.unit(r.UnitId) {
		return nil, mb.ErrGWTargetFailedToRespond
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.IsWrite {
		for i := range r.Args {
			if !d.im.writable[r.Addr+uint16(i)] {
				return nil, mb.ErrIllegalDataAddress
			}
		}
		for i, v := range r.Args {
			a := r.Addr + uint16(i)
			if d.keep != nil {
				v = d.keep(a, v)
			}
			d.im.regs[a] = v
			d.writes = append(d.writes, fmt.Sprintf("%d=%d", a, r.Args[i]))
		}
		return nil, nil
	}
	out := make([]uint16, r.Quantity)
	for i := range out {
		v, ok := d.im.regs[r.Addr+uint16(i)]
		if !ok {
			return nil, mb.ErrIllegalDataAddress
		}
		out[i] = v
	}
	return out, nil
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
