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

// Package aw9523 is a driver for the AW9523(B) 16 pin I2C GPIO expander and LED
// driver. It talks to the chip over any I2C master that satisfies the I2C
// interface, such as the mcp2221 package's bridge.
package aw9523

import (
	"fmt"

	"github.com/purpleidea/mgmt/util/errwrap"
)

// DefaultAddress is the AW9523's default 7 bit I2C address. It can be strapped
// to one of 0x58..0x5b.
const DefaultAddress = 0x58

// chipID is the value the chip returns from its ID register; used to confirm we
// are really talking to an AW9523.
const chipID = 0x23

// Pins is the number of GPIO pins the chip exposes.
const Pins = 16

// AW9523 register addresses. The input, output and config registers are 16 bit
// (little endian): register 0 covers pins 0..7, the next covers pins 8..15.
const (
	regInput0 = 0x00 // read pin input levels
	regOutput = 0x02 // output latch
	regConfig = 0x04 // direction: bit set = input, clear = output
	regChipID = 0x10 // hardcoded chip id
	regGCR    = 0x11 // global control; bit 4 = port 0 push-pull
	regReset  = 0x7f // soft reset
)

// I2C is the minimal I2C master the AW9523 needs. The mcp2221.MCP2221 type
// satisfies it. Addresses are 7 bit.
type I2C interface {
	// Write performs an I2C write of data to the slave address.
	Write(addr byte, data []byte) error
	// WriteRead writes w then reads n bytes back using a repeated start.
	WriteRead(addr byte, w []byte, n int) ([]byte, error)
}

// AW9523 is a handle to a single expander chip on an I2C bus.
type AW9523 struct {
	bus  I2C
	addr byte
}

// New returns a handle for the chip at the given 7 bit address on the given bus.
// Pass DefaultAddress for the common case.
func New(bus I2C, addr byte) *AW9523 {
	return &AW9523{bus: bus, addr: addr}
}

func (obj *AW9523) readReg8(reg byte) (byte, error) {
	buf, err := obj.bus.WriteRead(obj.addr, []byte{reg}, 1)
	if err != nil {
		return 0, err
	}
	return buf[0], nil
}

func (obj *AW9523) writeReg8(reg, val byte) error {
	return obj.bus.Write(obj.addr, []byte{reg, val})
}

func (obj *AW9523) readReg16(reg byte) (uint16, error) {
	buf, err := obj.bus.WriteRead(obj.addr, []byte{reg}, 2)
	if err != nil {
		return 0, err
	}
	return uint16(buf[0]) | uint16(buf[1])<<8, nil
}

func (obj *AW9523) writeReg16(reg byte, val uint16) error {
	return obj.bus.Write(obj.addr, []byte{reg, byte(val & 0xFF), byte(val >> 8)})
}

// ChipID reads and returns the chip's ID register.
func (obj *AW9523) ChipID() (byte, error) {
	return obj.readReg8(regChipID)
}

// Reset performs a soft reset, returning the chip to its power-on defaults.
func (obj *AW9523) Reset() error {
	return obj.writeReg8(regReset, 0x00)
}

// Init confirms the chip is present, resets it, puts port 0 into push-pull mode
// and configures every pin as an input. This mirrors the Adafruit driver's
// power-on setup. Call it once before using the chip.
func (obj *AW9523) Init() error {
	id, err := obj.ChipID()
	if err != nil {
		return errwrap.Wrapf(err, "could not read chip id")
	}
	if id != chipID {
		return fmt.Errorf("unexpected chip id 0x%02x, expected 0x%02x", id, chipID)
	}
	if err := obj.Reset(); err != nil {
		return errwrap.Wrapf(err, "could not reset chip")
	}
	// Set port 0 to push-pull (GCR bit 4) without disturbing other bits.
	gcr, err := obj.readReg8(regGCR)
	if err != nil {
		return errwrap.Wrapf(err, "could not read gcr")
	}
	if err := obj.writeReg8(regGCR, gcr|(1<<4)); err != nil {
		return errwrap.Wrapf(err, "could not set push-pull mode")
	}
	// All pins as inputs (config bit set == input).
	if err := obj.writeReg16(regConfig, 0xFFFF); err != nil {
		return errwrap.Wrapf(err, "could not set directions")
	}
	return nil
}

// validatePin returns an error if pin is out of range.
func validatePin(pin int) error {
	if pin < 0 || pin >= Pins {
		return fmt.Errorf("pin %d out of range (0..%d)", pin, Pins-1)
	}
	return nil
}

// SetDirection configures a pin as an output (output == true) or input.
func (obj *AW9523) SetDirection(pin int, output bool) error {
	if err := validatePin(pin); err != nil {
		return err
	}
	cur, err := obj.readReg16(regConfig)
	if err != nil {
		return err
	}
	// A set config bit means input, so clear the bit to make it an output.
	next := cur
	if output {
		next &^= 1 << uint(pin)
	} else {
		next |= 1 << uint(pin)
	}
	if next == cur {
		return nil
	}
	return obj.writeReg16(regConfig, next)
}

// SetOutput drives an output pin high (value == true) or low.
func (obj *AW9523) SetOutput(pin int, value bool) error {
	if err := validatePin(pin); err != nil {
		return err
	}
	cur, err := obj.readReg16(regOutput)
	if err != nil {
		return err
	}
	next := cur
	if value {
		next |= 1 << uint(pin)
	} else {
		next &^= 1 << uint(pin)
	}
	if next == cur {
		return nil
	}
	return obj.writeReg16(regOutput, next)
}

// GetOutput returns the last value written to an output pin's latch.
func (obj *AW9523) GetOutput(pin int) (bool, error) {
	if err := validatePin(pin); err != nil {
		return false, err
	}
	cur, err := obj.readReg16(regOutput)
	if err != nil {
		return false, err
	}
	return cur&(1<<uint(pin)) != 0, nil
}

// GetInput returns the actual electrical level sensed at a pin.
func (obj *AW9523) GetInput(pin int) (bool, error) {
	if err := validatePin(pin); err != nil {
		return false, err
	}
	cur, err := obj.readReg16(regInput0)
	if err != nil {
		return false, err
	}
	return cur&(1<<uint(pin)) != 0, nil
}
