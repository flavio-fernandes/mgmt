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

package coregpio

import (
	"context"
	"fmt"
	"time"

	"github.com/purpleidea/mgmt/lang/funcs"
	"github.com/purpleidea/mgmt/lang/interfaces"
	"github.com/purpleidea/mgmt/lang/types"
	"github.com/purpleidea/mgmt/util/aw9523"
	"github.com/purpleidea/mgmt/util/errwrap"
	"github.com/purpleidea/mgmt/util/mcp2221"
)

const (
	// PinFuncName is the name this function is registered as.
	PinFuncName = "pin"

	// arg names...
	pinArgNamePin = "pin"

	// pinPollInterval is how often we re-read the pin to notice a change.
	// The MCP2221 does not give us an interrupt line, so we poll. It needs
	// to be short enough to catch a button press but not so short that we
	// saturate the USB bus.
	pinPollInterval = 100 * time.Millisecond
)

func init() {
	funcs.ModuleRegister(ModuleName, PinFuncName, func() interfaces.Func { return &PinFunc{} }) // must register the func and name
}

// PinFunc reads the digital level of a single input pin on an AW9523 GPIO
// expander, reached over a Microchip MCP2221 USB to I2C bridge. It returns true
// when the pin is high and false when it is low, and it emits a new event
// whenever that level changes, for example when a button or switch wired to the
// pin is pressed or released.
//
// It is read-only: it never changes any pin's direction or output. The pin must
// already be configured as an input, which is the chip's power-on default.
type PinFunc struct {
	interfaces.Textarea

	init *interfaces.Init

	mcp *mcp2221.MCP2221
	aw  *aw9523.AW9523

	input chan int // stream of requested pin numbers, from Call

	pin  *int  // the pin we are currently watching
	last *bool // the last level we emitted an event for
}

// String returns a simple name for this function. This is needed so this struct
// can satisfy the pgraph.Vertex interface.
func (obj *PinFunc) String() string {
	return PinFuncName
}

// ArgGen returns the Nth arg name for this function.
func (obj *PinFunc) ArgGen(index int) (string, error) {
	seq := []string{pinArgNamePin}
	if l := len(seq); index >= l {
		return "", fmt.Errorf("index %d exceeds arg length of %d", index, l)
	}
	return seq[index], nil
}

// Validate makes sure we've built our struct properly.
func (obj *PinFunc) Validate() error {
	return nil
}

// Info returns some static info about itself.
func (obj *PinFunc) Info() *interfaces.Info {
	return &interfaces.Info{
		Pure: false, // the pin level changes over time
		Memo: false,
		Fast: false,
		Spec: false,
		Sig:  types.NewType(fmt.Sprintf("func(%s int) bool", pinArgNamePin)),
	}
}

// Init runs some startup code for this function. It opens the bridge and does a
// read-only check that the expander is present. It deliberately does not change
// any GPIO state.
func (obj *PinFunc) Init(init *interfaces.Init) error {
	obj.init = init
	obj.input = make(chan int)

	mcp, err := mcp2221.Open(mcp2221.DefaultDevice)
	if err != nil {
		return errwrap.Wrapf(err, "could not open gpio bridge")
	}
	// Setting the bridge's I2C bus speed is transport configuration; it does
	// not touch any GPIO pin state on the expander.
	if err := mcp.SetSpeed(mcp2221.DefaultSpeed); err != nil {
		mcp.Close()
		return errwrap.Wrapf(err, "could not configure i2c bus")
	}

	aw := aw9523.New(mcp, aw9523.DefaultAddress)
	if err := aw.CheckID(); err != nil { // read-only presence check
		mcp.Close()
		return errwrap.Wrapf(err, "could not find aw9523")
	}

	obj.mcp = mcp
	obj.aw = aw
	return nil
}

// Stream returns the changing values that this func has over time. It polls the
// selected pin and emits an event whenever its level changes. The pin to watch
// arrives from Call over the input channel.
func (obj *PinFunc) Stream(ctx context.Context) error {
	ticker := time.NewTicker(pinPollInterval)
	defer ticker.Stop()

	for {
		select {
		case pin, ok := <-obj.input:
			if !ok {
				obj.input = nil // don't infinite loop back
				return fmt.Errorf("unexpected close")
			}
			if obj.pin != nil && *obj.pin == pin {
				continue // nothing changed, keep polling
			}
			// A new (or first) pin to watch: reset so the next read
			// always emits an initial event, and read it right away
			// rather than waiting a full tick.
			p := pin
			obj.pin = &p
			obj.last = nil

		case <-ticker.C:
			// time to poll again

		case <-ctx.Done():
			return ctx.Err()
		}

		if obj.pin == nil {
			continue // no pin selected yet, nothing to poll
		}

		value, err := obj.aw.GetInput(*obj.pin)
		if err != nil {
			return errwrap.Wrapf(err, "could not read gpio pin")
		}
		if obj.last != nil && *obj.last == value {
			continue // unchanged, so no new event
		}
		v := value
		obj.last = &v

		if err := obj.init.Event(ctx); err != nil { // send event
			return err
		}
	}
}

// Call this function with the input args and return the value if it is possible
// to do so at this time.
func (obj *PinFunc) Call(ctx context.Context, args []types.Value) (types.Value, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("not enough args")
	}
	pin := int(args[0].Int())
	if pin < 0 || pin >= aw9523.Pins {
		return nil, fmt.Errorf("pin %d out of range (0..%d)", pin, aw9523.Pins-1)
	}

	// Check before we send to a chan where we'd need Stream to be running.
	if obj.init == nil {
		return nil, funcs.ErrCantSpeculate
	}

	// Tell the Stream which pin we're watching now. This doesn't block for
	// long because Stream should always be ready to consume unless it's
	// closing down, in which case a ctx closure should come soon.
	select {
	case obj.input <- pin:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	value, err := obj.aw.GetInput(pin)
	if err != nil {
		return nil, errwrap.Wrapf(err, "could not read gpio pin")
	}
	return &types.BoolValue{
		V: value,
	}, nil
}

// Cleanup runs after this function was removed from the graph. It closes the
// bridge that Init opened.
func (obj *PinFunc) Cleanup(ctx context.Context) error {
	if obj.mcp != nil {
		return obj.mcp.Close()
	}
	return nil
}

// Done is a message from the engine to tell us that no more Call's are coming.
func (obj *PinFunc) Done() error {
	close(obj.input) // At this point we know obj.input won't be used.
	return nil
}
