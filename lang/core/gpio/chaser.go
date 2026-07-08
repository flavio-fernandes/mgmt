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
	"sync"
	"time"

	"github.com/purpleidea/mgmt/lang/funcs"
	"github.com/purpleidea/mgmt/lang/interfaces"
	"github.com/purpleidea/mgmt/lang/types"
	"github.com/purpleidea/mgmt/util/aw9523"
	"github.com/purpleidea/mgmt/util/errwrap"
	"github.com/purpleidea/mgmt/util/mcp2221"
)

const (
	// ChaserFuncName is the name this function is registered as.
	ChaserFuncName = "chaser"

	// arg names...
	chaserArgNameForward  = "forward"
	chaserArgNameBackward = "backward"
	chaserArgNameCount    = "count"

	// chaserPollInterval is how often we re-read the two button pins to
	// notice a press. The MCP2221 has no interrupt line, so we poll. It has
	// to be short enough to feel responsive but not so short that we
	// saturate the USB bus with two I2C reads every tick.
	chaserPollInterval = 50 * time.Millisecond

	// chaserDebounceInterval is the minimum time between two counted button
	// presses. It rejects contact bounce so a single physical press only
	// advances the position once. It mirrors the debounce in the reference
	// button.py from junk/aw9523.md.
	chaserDebounceInterval = 50 * time.Millisecond
)

func init() {
	funcs.ModuleRegister(ModuleName, ChaserFuncName, func() interfaces.Func { return &ChaserFunc{} }) // must register the func and name
}

// chaserConfig is the set of pin/count parameters this function is driven with.
// It arrives from Call and is consumed by Stream.
type chaserConfig struct {
	forward  int
	backward int
	count    int
}

// ChaserFunc implements the classic one-of-N "chaser" position selector on top
// of an AW9523 GPIO expander reached over a Microchip MCP2221 USB to I2C
// bridge. It watches two momentary buttons and returns the index of the
// currently selected position, from 0 to count-1. A press of the forward button
// advances the index (wrapping count-1 -> 0) and a press of the backward button
// moves it backward (wrapping 0 -> count-1). It emits a new event whenever the
// index changes, so a caller can, for example, light exactly one of a row of
// LEDs.
//
// This is the stateful counterpart to the pin function: a purely declarative
// mcl expression cannot keep a running counter that reacts to button *edges*,
// so the small state machine (edge detection, debounce and the wrapping index)
// lives here in one place. Reading both buttons from a single goroutine also
// keeps all of the I2C traffic for the selector serialized and interrupt-free.
//
// The two button pins are configured as inputs (the chip's power-on default for
// a pin is actually an output). It never drives an output or touches any pin
// other than the two it was asked to watch; the LEDs themselves are driven by
// separate gpio resources.
//
// The buttons are expected to be wired active-low with external pull-ups, as
// described in junk/aw9523.md: a pin reads true when idle and false while
// pressed, so a press is a true -> false transition.
type ChaserFunc struct {
	interfaces.Textarea

	init *interfaces.Init

	mcp *mcp2221.MCP2221
	aw  *aw9523.AW9523

	input chan chaserConfig // stream of requested configs, from Call

	config *chaserConfig // the config we are currently acting on

	// state, mutated only by Stream, read by Call under mutex.
	mutex          sync.Mutex
	index          int   // the currently selected position, 0..count-1
	publishedIndex int   // the last index we emitted an event for
	lastFwd        *bool // last level seen on the forward button
	lastBwd        *bool // last level seen on the backward button
	lastPress      time.Time
	emitted        bool // have we published the current index at least once?
}

// String returns a simple name for this function. This is needed so this struct
// can satisfy the pgraph.Vertex interface.
func (obj *ChaserFunc) String() string {
	return ChaserFuncName
}

// ArgGen returns the Nth arg name for this function.
func (obj *ChaserFunc) ArgGen(index int) (string, error) {
	seq := []string{chaserArgNameForward, chaserArgNameBackward, chaserArgNameCount}
	if l := len(seq); index >= l {
		return "", fmt.Errorf("index %d exceeds arg length of %d", index, l)
	}
	return seq[index], nil
}

// Validate makes sure we've built our struct properly.
func (obj *ChaserFunc) Validate() error {
	return nil
}

// Info returns some static info about itself.
func (obj *ChaserFunc) Info() *interfaces.Info {
	return &interfaces.Info{
		Pure: false, // the index changes over time as buttons are pressed
		Memo: false,
		Fast: false,
		Spec: false,
		Sig:  types.NewType(fmt.Sprintf("func(%s int, %s int, %s int) int", chaserArgNameForward, chaserArgNameBackward, chaserArgNameCount)),
	}
}

// Init runs some startup code for this function. It opens the bridge and does a
// read-only check that the expander is present. It deliberately does not change
// any GPIO state.
func (obj *ChaserFunc) Init(init *interfaces.Init) error {
	obj.init = init
	obj.input = make(chan chaserConfig)

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
// two button pins and advances or retreats an internal index whenever it sees a
// press. It emits an event whenever that index changes. The config to watch
// arrives from Call over the input channel.
func (obj *ChaserFunc) Stream(ctx context.Context) error {
	ticker := time.NewTicker(chaserPollInterval)
	defer ticker.Stop()

	for {
		select {
		case config, ok := <-obj.input:
			if !ok {
				obj.input = nil // don't infinite loop back
				return fmt.Errorf("unexpected close")
			}
			if obj.config != nil && *obj.config == config {
				continue // nothing changed, keep polling
			}
			// A new (or first) config: reset our state so the next
			// poll republishes an initial index of 0, and read the
			// buttons right away rather than waiting a full tick.
			c := config
			obj.config = &c

			obj.mutex.Lock()
			obj.index = 0
			obj.lastFwd = nil
			obj.lastBwd = nil
			obj.lastPress = time.Time{}
			obj.emitted = false
			obj.mutex.Unlock()

			// The pins only sense the external level when they are
			// inputs; the chip defaults a pin to an output, which
			// would just read back its own latch and never change.
			// These are the only state changes we make, and only to
			// the two pins we were asked to watch. SetDirection is
			// idempotent.
			if err := obj.aw.SetDirection(config.forward, false); err != nil {
				return errwrap.Wrapf(err, "could not set forward pin to input")
			}
			if err := obj.aw.SetDirection(config.backward, false); err != nil {
				return errwrap.Wrapf(err, "could not set backward pin to input")
			}

		case <-ticker.C:
			// time to poll again

		case <-ctx.Done():
			return ctx.Err()
		}

		if obj.config == nil {
			continue // no config selected yet, nothing to poll
		}

		changed, err := obj.poll(*obj.config)
		if err != nil {
			return err
		}
		if !changed {
			continue // index unchanged, so no new event
		}

		if err := obj.init.Event(ctx); err != nil { // send event
			return err
		}
	}
}

// poll reads the two buttons once, updates the index on a fresh press, and
// reports whether the published index changed (which includes the very first
// poll, so the initial index of 0 always gets emitted).
func (obj *ChaserFunc) poll(config chaserConfig) (bool, error) {
	fwd, err := obj.aw.GetInput(config.forward)
	if err != nil {
		return false, errwrap.Wrapf(err, "could not read forward pin")
	}
	bwd, err := obj.aw.GetInput(config.backward)
	if err != nil {
		return false, errwrap.Wrapf(err, "could not read backward pin")
	}
	obj.mutex.Lock()
	defer obj.mutex.Unlock()

	if obj.init != nil && obj.init.Debug {
		obj.init.Logf("forward(%d)=%t backward(%d)=%t index=%d", config.forward, fwd, config.backward, bwd, obj.index)
	}

	// A press is a true -> false transition (active-low buttons). Only count
	// it once the debounce window since the last counted press has passed.
	pressed := func(last *bool, now bool) bool {
		return last != nil && *last && !now
	}
	debounceOK := time.Since(obj.lastPress) >= chaserDebounceInterval

	if debounceOK && pressed(obj.lastFwd, fwd) {
		obj.index = (obj.index + 1) % config.count
		obj.lastPress = time.Now()
	} else if debounceOK && pressed(obj.lastBwd, bwd) {
		obj.index = (obj.index - 1 + config.count) % config.count
		obj.lastPress = time.Now()
	}

	obj.lastFwd = &fwd
	obj.lastBwd = &bwd

	if obj.emitted && obj.index == obj.publishedIndex {
		return false, nil // nothing new to publish
	}
	obj.emitted = true
	obj.publishedIndex = obj.index
	return true, nil
}

// Call this function with the input args and return the value if it is possible
// to do so at this time.
func (obj *ChaserFunc) Call(ctx context.Context, args []types.Value) (types.Value, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("not enough args")
	}
	forward := int(args[0].Int())
	backward := int(args[1].Int())
	count := int(args[2].Int())

	if forward < 0 || forward >= aw9523.Pins {
		return nil, fmt.Errorf("forward pin %d out of range (0..%d)", forward, aw9523.Pins-1)
	}
	if backward < 0 || backward >= aw9523.Pins {
		return nil, fmt.Errorf("backward pin %d out of range (0..%d)", backward, aw9523.Pins-1)
	}
	if forward == backward {
		return nil, fmt.Errorf("forward and backward pins must differ")
	}
	if count < 1 {
		return nil, fmt.Errorf("count must be at least 1, got %d", count)
	}

	// Check before we send to a chan where we'd need Stream to be running.
	if obj.init == nil {
		return nil, funcs.ErrCantSpeculate
	}

	config := chaserConfig{forward: forward, backward: backward, count: count}

	// Tell the Stream which config we're watching now. This doesn't block
	// for long because Stream should always be ready to consume unless it's
	// closing down, in which case a ctx closure should come soon.
	select {
	case obj.input <- config:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	obj.mutex.Lock()
	index := obj.index
	obj.mutex.Unlock()

	return &types.IntValue{
		V: int64(index),
	}, nil
}

// Cleanup runs after this function was removed from the graph. It closes the
// bridge that Init opened.
func (obj *ChaserFunc) Cleanup(ctx context.Context) error {
	if obj.mcp != nil {
		return obj.mcp.Close()
	}
	return nil
}

// Done is a message from the engine to tell us that no more Call's are coming.
func (obj *ChaserFunc) Done() error {
	close(obj.input) // At this point we know obj.input won't be used.
	return nil
}
