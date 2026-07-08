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
	"testing"
	"time"
)

func TestChaserAutoAdvance(t *testing.T) {
	config := chaserConfig{forward: 0, backward: 11, count: 7, idle: 10}
	t0 := time.Unix(1000, 0)

	// A press just happened at t0; simulate polling at a fixed cadence and
	// record the index each tick. We should hold at 3 for the whole idle
	// window, then step forward exactly once per second afterwards.
	obj := &ChaserFunc{index: 3, lastPress: t0}

	got := []int{}
	for s := 0; s <= 15; s++ {
		// poll a few times within each second to prove we still only
		// advance once per second, not once per poll.
		for _, ms := range []int{0, 300, 600, 900} {
			obj.autoAdvance(config, t0.Add(time.Duration(s)*time.Second+time.Duration(ms)*time.Millisecond))
		}
		got = append(got, obj.index)
	}

	// seconds 0..9 are still within the 10s idle window -> hold at 3.
	// second 10 is the first idle tick -> 4, then +1 each second, wrapping.
	want := []int{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 4, 5, 6, 0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("second %d: got index %d, want %d (full: %v)", i, got[i], want[i], got)
			break
		}
	}
}

func TestChaserAutoAdvanceDisabled(t *testing.T) {
	config := chaserConfig{forward: 0, backward: 11, count: 7, idle: 0} // disabled
	t0 := time.Unix(1000, 0)
	obj := &ChaserFunc{index: 3, lastPress: t0}
	for s := 0; s <= 60; s++ {
		obj.autoAdvance(config, t0.Add(time.Duration(s)*time.Second))
	}
	if obj.index != 3 {
		t.Errorf("idle=0 should never auto-advance, got index %d", obj.index)
	}
}

func TestChaserAutoAdvanceResetsOnPress(t *testing.T) {
	config := chaserConfig{forward: 0, backward: 11, count: 7, idle: 10}
	t0 := time.Unix(1000, 0)
	obj := &ChaserFunc{index: 3, lastPress: t0}

	// Idle long enough to auto-advance twice (t+10s -> 4, t+11s -> 5).
	obj.autoAdvance(config, t0.Add(10*time.Second))
	obj.autoAdvance(config, t0.Add(11*time.Second))
	if obj.index != 5 {
		t.Fatalf("expected index 5 after two auto-advances, got %d", obj.index)
	}

	// Simulate a real press at t+12s: the poll would set index and lastPress.
	obj.index = 6
	obj.lastPress = t0.Add(12 * time.Second)

	// For the next 10s there should be no auto-advance.
	obj.autoAdvance(config, t0.Add(20*time.Second))
	if obj.index != 6 {
		t.Errorf("auto-advance should be suppressed within idle window after a press, got %d", obj.index)
	}
	// 10s after the press it resumes.
	obj.autoAdvance(config, t0.Add(22*time.Second))
	if obj.index != 0 { // 6 -> wrap to 0
		t.Errorf("expected auto-advance to resume (6 -> 0), got %d", obj.index)
	}
}
