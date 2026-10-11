// Copyright 2026 Abhinav Gautam
// SPDX-License-Identifier: Apache-2.0

// Package sim is a simulated SunSpec device for tests and demos, built on
// the sunspec module's device package (github.com/cB-Sigma/sunspec/device),
// which lays out and serves the register map: the "SunS" marker at a base
// address, then models as ID, L and data, then the end model, over Modbus
// TCP, Modbus TCP over TLS with client certificates, and RTU or ASCII
// framing over TCP or over any byte stream (a serial port the caller
// opens). Reads outside the map answer exception 2 (illegal data
// address); writes are taken only on registers marked writable, and a
// hook can make the device keep a different value (an inverter that
// clamps a setpoint). What the simulator adds: faults that make it answer
// with an exception, late, or not at all; an inverter whose output
// follows its DER settings and adopted curves against a simulated grid
// voltage and frequency (Follow); and DemoInverter, a ready-made
// inverter.
//
// Image, Options, Build, Example, NotImplemented, AllModels, Str and
// Filled are the device package's, kept here under their old names.
package sim

import (
	"sync"
	"time"

	"github.com/cB-Sigma/sunspec/device"
)

// Image builds a register map (device.Image). It is not safe for
// concurrent use; build it before Start.
type Image = device.Image

// Options shapes a device's server (device.Options).
type Options = device.Options

// NewImage starts a map with the marker at base.
func NewImage(base uint16) *Image { return device.NewImage(base) }

// Str encodes text into n registers, zero-padded.
func Str(s string, n int) []uint16 { return device.Str(s, n) }

// Filled returns n registers of v.
func Filled(n int, v uint16) []uint16 { return device.Filled(n, v) }

// Build lays out a described model's data registers from named raw
// values (device.Build): it returns the registers and the offsets of the
// points the definition marks writable.
func Build(id int, values map[string]any) ([]uint16, []int, error) { return device.Build(id, values) }

// Example gives made-up raw values for every point of a described model
// (device.Example).
func Example(id int) map[string]any { return device.Example(id) }

// NotImplemented returns a described model's data registers with every
// point at its type's "not implemented" value (device.NotImplemented).
func NotImplemented(id int, repeats ...int) []uint16 { return device.NotImplemented(id, repeats...) }

// AllModels builds an image at base holding one instance of every
// described model (device.AllModels).
func AllModels(base uint16) (*Image, map[int]uint16, error) { return device.AllModels(base) }

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

// Device serves an image over Modbus: a device.Device (Get, Set,
// SetKeep, WriteLog, ServeRTU, ServeASCII, Addr, URL, Close and the
// hooks) with faults. The faults are the device's SetBefore hook: calling
// SetBefore replaces them.
type Device struct {
	*device.Device
	mu     sync.Mutex
	faults []*Fault
}

func wrap(dd *device.Device) *Device {
	d := &Device{Device: dd}
	dd.SetBefore(d.before)
	return d
}

// NewDevice makes a device for the image that serves nothing until
// ServeRTU or ServeASCII (or use Start, StartOptions). The device takes
// the image over: change it afterwards only through Set.
func NewDevice(im *Image, units ...uint8) *Device { return wrap(device.NewDevice(im, units...)) }

// Start serves the image over Modbus TCP on listen ("host:port"; "" or
// port 0 picks a free port on 127.0.0.1 or the given host), answering on
// the units given (none: every unit; others get exception 11, gateway
// target failed to respond). Close stops it.
func Start(im *Image, listen string, units ...uint8) (*Device, error) {
	return StartOptions(im, Options{Listen: listen, Units: units})
}

// StartOptions serves the image as o says: Modbus TCP, TLS, or RTU or
// ASCII framing over TCP.
func StartOptions(im *Image, o Options) (*Device, error) {
	dd, err := device.StartOptions(im, o)
	if err != nil {
		return nil, err
	}
	return wrap(dd), nil
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

// fault returns the fault for a request, counting it down.
func (d *Device) fault(r device.Request) *Fault {
	d.mu.Lock()
	defer d.mu.Unlock()
	last := int(r.Addr) + int(max(r.Count, 1)) - 1
	for i, f := range d.faults {
		if f.Unit != 0 && f.Unit != r.Unit || f.Op == "read" && r.Write || f.Op == "write" && !r.Write {
			continue
		}
		if (f.From != 0 || f.To != 0) && (last < int(f.From) || int(r.Addr) > int(f.To)) {
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

// before applies the faults to a request (the device's SetBefore hook).
func (d *Device) before(r device.Request) error {
	f := d.fault(r)
	if f == nil {
		return nil
	}
	if f.Silent {
		<-d.Done()
		return device.ErrNoAnswer
	}
	if f.Delay > 0 {
		select {
		case <-time.After(f.Delay):
		case <-d.Done():
			return device.ErrNoAnswer
		}
	}
	if f.Exception != 0 {
		return device.Exception(f.Exception)
	}
	return nil
}
