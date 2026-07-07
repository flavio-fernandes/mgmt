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

package resources

import (
	"context"
	"fmt"

	"github.com/purpleidea/mgmt/engine"
	"github.com/purpleidea/mgmt/engine/traits"
	"github.com/purpleidea/mgmt/util/aw9523"
	"github.com/purpleidea/mgmt/util/errwrap"
	"github.com/purpleidea/mgmt/util/mcp2221"
)

func init() {
	engine.RegisterResource("gpio", func() engine.Res { return &GPIORes{} })
}

// GPIORes is a resource that drives a single digital output pin high or low on
// an AW9523 GPIO expander, reached over a Microchip MCP2221 USB to I2C bridge.
// It is a first step towards general GPIO support; for now it only writes an
// output pin. The chip specific logic lives behind the aw9523 and mcp2221
// packages so that other backends can be added later.
type GPIORes struct {
	traits.Base // add the base methods without re-implementation

	init *engine.Init

	// Device is the path to the MCP2221 hidraw device. If empty, a sensible
	// default is used.
	Device string `lang:"device" yaml:"device"`

	// Address is the 7 bit I2C address of the AW9523 expander. If zero, the
	// chip's default address is used.
	Address int `lang:"address" yaml:"address"`

	// Pin is the GPIO pin number on the expander to drive, from 0 to 15.
	Pin int `lang:"pin" yaml:"pin"`

	// Value is the digital level to write to the pin: true for high, false
	// for low.
	Value bool `lang:"value" yaml:"value"`

	mcp *mcp2221.MCP2221
	aw  *aw9523.AW9523
}

// address returns the configured I2C address or the chip default if unset.
func (obj *GPIORes) address() byte {
	if obj.Address == 0 {
		return aw9523.DefaultAddress
	}
	return byte(obj.Address)
}

// device returns the configured hidraw device path or the default if unset.
func (obj *GPIORes) device() string {
	if obj.Device == "" {
		return mcp2221.DefaultDevice
	}
	return obj.Device
}

// Default returns some sensible defaults for this resource.
func (obj *GPIORes) Default() engine.Res {
	return &GPIORes{}
}

// Validate if the params passed in are valid data.
func (obj *GPIORes) Validate() error {
	if obj.Pin < 0 || obj.Pin >= aw9523.Pins {
		return fmt.Errorf("pin %d out of range (0..%d)", obj.Pin, aw9523.Pins-1)
	}
	if obj.Address < 0 || obj.Address > 0x7F {
		return fmt.Errorf("address 0x%02x out of range", obj.Address)
	}
	return nil
}

// Init runs some startup code for this resource. It opens the bridge, verifies
// and initializes the expander, and configures our pin as an output.
func (obj *GPIORes) Init(init *engine.Init) error {
	obj.init = init // save for later

	mcp, err := mcp2221.Open(obj.device())
	if err != nil {
		return errwrap.Wrapf(err, "could not open gpio bridge")
	}
	if err := mcp.SetSpeed(mcp2221.DefaultSpeed); err != nil {
		mcp.Close()
		return errwrap.Wrapf(err, "could not configure i2c bus")
	}

	aw := aw9523.New(mcp, obj.address())
	if err := aw.Init(); err != nil {
		mcp.Close()
		return errwrap.Wrapf(err, "could not initialize aw9523")
	}
	if err := aw.SetDirection(obj.Pin, true); err != nil { // output
		mcp.Close()
		return errwrap.Wrapf(err, "could not set pin direction")
	}

	obj.mcp = mcp
	obj.aw = aw
	return nil
}

// Cleanup is run by the engine to clean up after the resource is done.
func (obj *GPIORes) Cleanup() error {
	if obj.mcp != nil {
		return obj.mcp.Close()
	}
	return nil
}

// Watch is the primary listener for this resource and it outputs events. There
// is no external source of change for a pure output pin, so after signalling
// that we are ready we simply block until the engine shuts us down.
func (obj *GPIORes) Watch(ctx context.Context) error {
	if err := obj.init.Event(ctx); err != nil {
		return err
	}

	select {
	case <-ctx.Done(): // closed by the engine to signal shutdown
	}

	return ctx.Err()
}

// CheckApply reads the pin's current output state and, if it differs from the
// desired value and apply is true, writes the desired value.
func (obj *GPIORes) CheckApply(ctx context.Context, apply bool) (bool, error) {
	cur, err := obj.aw.GetOutput(obj.Pin)
	if err != nil {
		return false, errwrap.Wrapf(err, "could not read pin state")
	}
	obj.init.Logf("output of pin %d is %t", obj.Pin, cur)
	if cur == obj.Value {
		return true, nil // state is already correct
	}

	if !apply {
		return false, nil
	}

	obj.init.Logf("setting pin %d to %t", obj.Pin, obj.Value)
	if err := obj.aw.SetOutput(obj.Pin, obj.Value); err != nil {
		return false, errwrap.Wrapf(err, "could not write pin state")
	}
	return false, nil
}

// Cmp compares two resources and returns an error if they are not equivalent.
func (obj *GPIORes) Cmp(r engine.Res) error {
	// we can only compare GPIORes to others of the same resource kind
	res, ok := r.(*GPIORes)
	if !ok {
		return fmt.Errorf("not a %s", obj.Kind())
	}

	if obj.Device != res.Device {
		return fmt.Errorf("the Device differs")
	}
	if obj.Address != res.Address {
		return fmt.Errorf("the Address differs")
	}
	if obj.Pin != res.Pin {
		return fmt.Errorf("the Pin differs")
	}
	if obj.Value != res.Value {
		return fmt.Errorf("the Value differs")
	}

	return nil
}

// UnmarshalYAML is the custom unmarshal handler for this struct. It is primarily
// useful for setting the defaults.
func (obj *GPIORes) UnmarshalYAML(unmarshal func(interface{}) error) error {
	type rawRes GPIORes // indirection to avoid infinite recursion

	def := obj.Default()      // get the default
	res, ok := def.(*GPIORes) // put in the right format
	if !ok {
		return fmt.Errorf("could not convert to GPIORes")
	}
	raw := rawRes(*res) // convert; the defaults go here

	if err := unmarshal(&raw); err != nil {
		return err
	}

	*obj = GPIORes(raw) // restore from indirection with type conversion!
	return nil
}
