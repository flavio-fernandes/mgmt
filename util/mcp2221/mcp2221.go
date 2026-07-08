// Mgmt
// Copyright (C) James Shubin and the project contributors
// Written by James Shubin <james@shubin.ca> and the project contributors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// Additional permission under GNU GPL version 3 section 7
//
// If you modify this program, or any covered work, by linking or combining it
// with embedded mcl code and modules (and that the embedded mcl code and
// modules which link with this program, contain a copy of their source code in
// the authoritative form) containing parts covered by the terms of any other
// license, the licensors of this program grant you additional permission to
// convey the resulting work. Furthermore, the licensors of this program grant
// the original author, James Shubin, additional permission to update this
// additional permission if he deems it necessary to achieve the goals of this
// additional permission.

// Package mcp2221 is a pure-golang (no cgo) driver for the Microchip MCP2221 and
// MCP2221A USB to I2C/UART bridge, spoken over a Linux hidraw device. It exposes
// the chip's I2C master so that higher level chip drivers (for example the
// aw9523 package) can talk to I2C peripherals hanging off the bridge.
//
// The chip is driven with 64 byte HID reports in each direction. The protocol
// implemented here follows Microchip's datasheet and matches the behaviour of
// Adafruit's Blinka reference driver.
package mcp2221

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/purpleidea/mgmt/util/errwrap"
)

// DefaultDevice is the usual hidraw path for a single attached MCP2221.
const DefaultDevice = "/dev/hidraw0"

// DefaultSpeed is the I2C bus speed we configure by default, in Hz.
const DefaultSpeed = 100000

// clock is the MCP2221 internal clock used to derive the I2C divider.
const clock = 12000000

// reportLen is the fixed size of an MCP2221 HID report in each direction.
const reportLen = 64

// retryMax is how many times we poll the chip before giving up on a transfer.
const retryMax = 50

// MCP2221 HID command bytes.
const (
	cmdStatus       = 0x10 // status / set parameters
	cmdI2CWrite     = 0x90 // I2C write data (with stop)
	cmdI2CRead      = 0x91 // I2C read data
	cmdI2CWriteNS   = 0x94 // I2C write data (no stop)
	cmdI2CReadRS    = 0x93 // I2C read data (repeated start)
	cmdI2CReadFetch = 0x40 // fetch the bytes read by a prior read command
)

// I2C engine states (from the status report) that we can never recover from.
func unrecoverable(state byte) bool {
	switch state {
	case 0x12, 0x23, 0x25, 0x44, 0x62:
		return true
	}
	return false
}

// MCP2221 represents an open connection to a single bridge device. It is safe
// for concurrent use; each I2C transaction is serialized with a mutex.
//
// A single physical bridge is a single shared bus: overlapping transactions
// from two independent handles would corrupt each other. To prevent this, Open
// returns one shared handle per device path, and all callers of that path share
// this struct (and therefore its mutex), so every transaction across the whole
// process is serialized onto the bus.
type MCP2221 struct {
	f   *os.File
	mu  sync.Mutex // serializes I2C transactions on this bus
	rmw sync.Mutex // serializes multi-transaction read-modify-writes

	device string // hidraw path; also the registry key
	refs   int    // number of open handles sharing us, guarded by registryMu
}

// registry holds the shared handle for each device path so that a resource and
// a function (or any two callers) end up on the same serialized bus.
var (
	registryMu sync.Mutex
	registry   = map[string]*MCP2221{}
)

// Open opens the bridge at the given hidraw device path, or returns the existing
// shared handle if that path is already open. Pass DefaultDevice for the common
// case of a single attached chip. Each successful Open must be paired with one
// Close.
func Open(device string) (*MCP2221, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	if obj, exists := registry[device]; exists {
		obj.refs++ // hand back the shared handle
		return obj, nil
	}

	f, err := os.OpenFile(device, os.O_RDWR, 0)
	if err != nil {
		return nil, errwrap.Wrapf(err, "could not open mcp2221 device")
	}
	obj := &MCP2221{f: f, device: device, refs: 1}
	registry[device] = obj
	return obj, nil
}

// Close releases this handle. The underlying device is only really closed once
// the last handle sharing it has been closed.
func (obj *MCP2221) Close() error {
	registryMu.Lock()
	defer registryMu.Unlock()

	if obj.refs <= 0 {
		return nil // already fully closed; ignore a double Close
	}
	obj.refs--
	if obj.refs > 0 {
		return nil // still in use by another handle
	}
	delete(registry, obj.device)
	return obj.f.Close()
}

// xfer writes a single HID report and returns the fixed-length response report.
// Per the hidraw convention the first byte written is the report ID, which is
// zero for the MCP2221 because it does not use numbered reports.
func (obj *MCP2221) xfer(report []byte) ([]byte, error) {
	if len(report) > reportLen {
		return nil, fmt.Errorf("report too long: %d", len(report))
	}
	buf := make([]byte, reportLen+1) // [0]=report id, [1:]=zero padded report
	copy(buf[1:], report)
	if _, err := obj.f.Write(buf); err != nil {
		return nil, errwrap.Wrapf(err, "hid write failed")
	}
	resp := make([]byte, reportLen)
	if _, err := obj.f.Read(resp); err != nil {
		return nil, errwrap.Wrapf(err, "hid read failed")
	}
	return resp, nil
}

// SetSpeed configures the I2C bus speed in Hz. It must be called once after Open
// before any I2C transactions.
func (obj *MCP2221) SetSpeed(speed int) error {
	obj.mu.Lock()
	defer obj.mu.Unlock()
	if speed <= 0 {
		return fmt.Errorf("invalid speed: %d", speed)
	}
	divider := byte(clock/speed - 3)
	_, err := obj.xfer([]byte{cmdStatus, 0x00, 0x00, 0x20, divider})
	return errwrap.Wrapf(err, "could not set i2c speed")
}

// i2cState returns the current I2C engine state byte from the status report.
func (obj *MCP2221) i2cState() (byte, error) {
	resp, err := obj.xfer([]byte{cmdStatus})
	if err != nil {
		return 0, err
	}
	if resp[1] != 0x00 {
		return 0, fmt.Errorf("could not get i2c status")
	}
	return resp[8], nil
}

// i2cCancel asks the engine to abort any in-progress transfer.
func (obj *MCP2221) i2cCancel() error {
	resp, err := obj.xfer([]byte{cmdStatus, 0x00, 0x10})
	if err != nil {
		return err
	}
	if resp[1] != 0x00 {
		return fmt.Errorf("could not cancel i2c transfer")
	}
	if resp[2] == 0x10 {
		time.Sleep(time.Millisecond) // bus release needs a moment
	}
	return nil
}

// i2cWrite performs a write transaction. cmd is cmdI2CWrite for a normal write
// with a stop, or cmdI2CWriteNS to hold the bus for a following repeated-start
// read. addr is the 7 bit slave address.
func (obj *MCP2221) i2cWrite(cmd, addr byte, buffer []byte) error {
	if st, err := obj.i2cState(); err != nil {
		return err
	} else if st != 0x00 {
		if err := obj.i2cCancel(); err != nil {
			return err
		}
	}

	length := len(buffer)
	start, retries := 0, 0
	for (length-start) > 0 || length == 0 {
		chunk := length - start
		if chunk > 60 { // max I2C data bytes per report
			chunk = 60
		}
		report := append([]byte{cmd, byte(length & 0xFF), byte((length >> 8) & 0xFF), addr << 1}, buffer[start:start+chunk]...)
		resp, err := obj.xfer(report)
		if err != nil {
			return err
		}
		if resp[1] != 0x00 { // engine busy or errored
			if unrecoverable(resp[2]) {
				return fmt.Errorf("unrecoverable i2c state 0x%02x", resp[2])
			}
			if retries++; retries >= retryMax {
				return fmt.Errorf("i2c write: max retries reached")
			}
			time.Sleep(time.Millisecond)
			continue // try this chunk again
		}
		for { // wait out any partial-data state before the next chunk
			st, err := obj.i2cState()
			if err != nil {
				return err
			}
			if st != 0x41 {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if length == 0 {
			break // zero-length write (used by bus scans)
		}
		start += chunk
		retries = 0
	}

	for i := 0; i < retryMax; i++ { // confirm the transfer really finished
		resp, err := obj.xfer([]byte{cmdStatus})
		if err != nil {
			return err
		}
		if resp[1] != 0x00 {
			return fmt.Errorf("could not get i2c status")
		}
		if resp[20]&0x40 != 0 {
			return fmt.Errorf("i2c slave address was NACK'd")
		}
		st := resp[8]
		if st == 0x00 || (st == 0x45 && cmd == cmdI2CWriteNS) {
			return nil // done (0x45 == "writing, no stop" is fine here)
		}
		if unrecoverable(st) {
			return fmt.Errorf("unrecoverable i2c state 0x%02x", st)
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("i2c write: max retries reached on status")
}

// i2cRead performs a read transaction into a buffer of len(buffer) bytes. cmd is
// cmdI2CRead for a plain read, or cmdI2CReadRS for a repeated-start read that
// follows a no-stop write.
func (obj *MCP2221) i2cRead(cmd, addr byte, buffer []byte) error {
	if st, err := obj.i2cState(); err != nil {
		return err
	} else if st != 0x45 && st != 0x00 { // 0x45 == mid write-no-stop
		if err := obj.i2cCancel(); err != nil {
			return err
		}
	}

	length := len(buffer)
	resp, err := obj.xfer([]byte{cmd, byte(length & 0xFF), byte((length >> 8) & 0xFF), (addr << 1) | 0x01})
	if err != nil {
		return err
	}
	if resp[1] != 0x00 {
		return fmt.Errorf("unrecoverable i2c read failure during setup")
	}

	start := 0
	for (length - start) > 0 {
		var got []byte
		for i := 0; i < retryMax; i++ {
			resp, err := obj.xfer([]byte{cmdI2CReadFetch})
			if err != nil {
				return err
			}
			if resp[1] == 0x41 { // partial data, not ready yet
				time.Sleep(time.Millisecond)
				continue
			}
			if resp[1] != 0x00 {
				return fmt.Errorf("unrecoverable i2c read failure")
			}
			if resp[2] == 0x25 {
				return fmt.Errorf("i2c slave address was NACK'd")
			}
			if resp[3] == 0x7F { // read error, retry
				time.Sleep(time.Millisecond)
				continue
			}
			if (resp[3] == 0x00 && resp[2] == 0x00) || resp[2] == 0x55 || resp[2] == 0x54 {
				got = resp // read complete or partial
				break
			}
		}
		if got == nil {
			return fmt.Errorf("i2c read: max retries reached")
		}
		chunk := length - start
		if chunk > 60 {
			chunk = 60
		}
		for i := 0; i < chunk; i++ {
			buffer[start+i] = got[4+i]
		}
		start += chunk
	}
	return nil
}

// Write performs an I2C write of data to the 7 bit slave address addr.
func (obj *MCP2221) Write(addr byte, data []byte) error {
	obj.mu.Lock()
	defer obj.mu.Unlock()
	return obj.i2cWrite(cmdI2CWrite, addr, data)
}

// Read reads n bytes from the 7 bit slave address addr.
func (obj *MCP2221) Read(addr byte, n int) ([]byte, error) {
	obj.mu.Lock()
	defer obj.mu.Unlock()
	buf := make([]byte, n)
	if err := obj.i2cRead(cmdI2CRead, addr, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Transaction runs f while holding an exclusive lock on the bus, so that a
// read-modify-write built out of several separate I2C operations is atomic with
// respect to other callers sharing this same physical bus. The per-operation
// mutex still serializes each individual Read/Write/WriteRead, so f may freely
// call them; this second lock only guarantees that no other caller's operations
// interleave in the middle of f. Without it, two callers each reading a shared
// register, flipping one bit, and writing it back could clobber each other.
func (obj *MCP2221) Transaction(f func() error) error {
	obj.rmw.Lock()
	defer obj.rmw.Unlock()
	return f()
}

// WriteRead writes w and then, using a repeated start, reads n bytes back from
// the same 7 bit slave address. This is the usual way to read a device register:
// write the register pointer, then read its contents.
func (obj *MCP2221) WriteRead(addr byte, w []byte, n int) ([]byte, error) {
	obj.mu.Lock()
	defer obj.mu.Unlock()
	if err := obj.i2cWrite(cmdI2CWriteNS, addr, w); err != nil { // hold the bus
		return nil, err
	}
	buf := make([]byte, n)
	if err := obj.i2cRead(cmdI2CReadRS, addr, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
