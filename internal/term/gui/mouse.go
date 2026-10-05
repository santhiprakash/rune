// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package gui

import (
	"context"
	"math"
	"time"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	tterm "unstable.build/rune/internal/term"
	"unstable.build/rune/internal/term/gui/font"
)

type mouseState struct {
	left   bool
	right  bool
	middle bool
	x, y   int
}

type mouse struct {
	fontManager *font.Manager
	mouse       mouseManager
	now         func() time.Time // for testing

	state         mouseState
	width, height int

	// lastButtonAt is when the pipeline last emitted a button event. A
	// held, motionless drag produces no state change to dispatch on, so
	// its age past the repeat delay is what keeps edge auto-scroll
	// advancing while the pointer stays still.
	lastButtonAt time.Time

	// accumY is the accumulated fractional vertical wheel offset. Ebiten
	// reports wheel deltas in (possibly fractional) line units; high-resolution
	// devices such as trackpads emit many small deltas per physical gesture. We
	// accumulate them and emit one discrete wheel event per whole line crossed,
	// keeping the remainder for the next frame. This mirrors Alacritty's
	// accumulated_scroll approach and produces smooth, high-resolution scrolling
	// instead of one jump per frame.
	accumY     float64
	multiplier float64
}

type mouseManager interface {
	Wheel() (float64, float64)
	CursorPosition() (int, int)
	IsMouseButtonPressed(ebiten.MouseButton) bool
}

// defaultScrollMultiplier matches Alacritty's default scrolling.multiplier.
const defaultScrollMultiplier = 3

// maxWheelLinesPerFrame caps how many discrete wheel events a single frame can
// emit. Ebiten reports small per-frame deltas, so a frame that would cross more
// lines than this can only come from a pathological delta (e.g. a malfunctioning
// device or a non-finite value). Capping keeps the slice allocation bounded and
// avoids a makeslice panic without affecting normal scrolling.
const maxWheelLinesPerFrame = 1024

func newMouse(fontManager *font.Manager) *mouse {
	return &mouse{
		fontManager: fontManager,
		mouse:       ebitenInputManager{},
		now:         time.Now,
		multiplier:  defaultScrollMultiplier,
	}
}

// processMouse polls the mouse state and returns the events produced since the
// last call. A single frame may yield multiple events: button transitions
// produce at most one event, while wheel movement can emit several discrete
// wheel events depending on the accumulated scroll distance.
func (m *mouse) processMouse() []term.Event {
	state := m.state
	m.state.left = m.mouse.IsMouseButtonPressed(ebiten.MouseButtonLeft)
	m.state.right = m.mouse.IsMouseButtonPressed(ebiten.MouseButtonRight)
	m.state.middle = m.mouse.IsMouseButtonPressed(ebiten.MouseButtonMiddle)
	m.state.x, m.state.y = m.mouse.CursorPosition()
	_, wheelY := m.mouse.Wheel()

	if m.state == state && wheelY == 0 {
		return m.dragRepeatEvents()
	}

	pos := m.clampedCoordinates()
	// The cell coordinate alone loses where inside the cell the pointer
	// is; carry it so consumers can snap positions to the nearer cell edge.
	ctx := tterm.ContextWithSubCellFraction(context.Background(), m.subCellFraction(pos))

	// Wheel events are accumulated and emitted as discrete line events,
	// preserving the fractional remainder for the next frame.
	if wheelY != 0 {
		return m.wheelEvents(ctx, pos, wheelY)
	}

	ev := term.Event{Type: term.EventMouse, MouseX: pos.X, MouseY: pos.Y, Context: ctx}

	switch {
	case m.state.left:
		ev.Key = term.MouseLeft
	case m.state.right:
		ev.Key = term.MouseRight
	case m.state.middle:
		ev.Key = term.MouseMiddle
	case (state.left && !m.state.left) ||
		(state.right && !m.state.right) ||
		(state.middle && !m.state.middle):
		ev.Key = term.MouseRelease
	case m.state.x == state.x && m.state.y == state.y:
		return nil
	}
	if ev.Key != 0 {
		m.lastButtonAt = m.now()
	}
	return []term.Event{ev}
}

// dragRepeatInterval is the base cadence for held-button events a
// motionless drag synthesizes. Downstream auto-scroll only advances on
// events, so a pointer held still at a pane's edge stops scrolling the
// moment it stops moving; twenty repeats per second keeps it moving
// without flooding the dispatch pipeline.
const dragRepeatInterval = 50 * time.Millisecond

// maxDragOvershoot caps the pointer's overshoot past the window edge, in
// cells, that shortens the repeat cadence. Bounding it keeps the repeat
// rate finite no matter how far out the pointer is held.
const maxDragOvershoot = 8

// maxDragRepeatEvents bounds how many held-button events one frame can
// emit, the same bound the wheel path puts on pathological deltas: a
// long stall must not become an unbounded burst on the next frame.
const maxDragRepeatEvents = 16

// dragRepeatEvents synthesizes the held-button events a motionless drag
// still needs. The window clamp, and the window manager's drag clamp
// downstream, erase how far past a pane's edge the pointer is, but the
// overshoot past the window itself is still known here, so the repeat
// cadence is where that lost distance still reaches: holding further
// out scrolls faster, the pacing other toolkits give edge auto-scroll.
func (m *mouse) dragRepeatEvents() []term.Event {
	var key term.Key
	switch {
	case m.state.left:
		key = term.MouseLeft
	case m.state.right:
		key = term.MouseRight
	case m.state.middle:
		key = term.MouseMiddle
	default:
		return nil
	}
	owed := int(m.now().Sub(m.lastButtonAt) / m.dragRepeatInterval())
	if owed < 1 {
		return nil
	}
	if owed > maxDragRepeatEvents {
		owed = maxDragRepeatEvents
	}
	m.lastButtonAt = m.now()

	pos := m.clampedCoordinates()
	ctx := tterm.ContextWithSubCellFraction(context.Background(), m.subCellFraction(pos))
	events := make([]term.Event, owed)
	for i := range events {
		events[i] = term.Event{
			Type: term.EventMouse, Key: key,
			MouseX: pos.X, MouseY: pos.Y, Context: ctx,
		}
	}
	return events
}

// dragRepeatInterval returns the repeat cadence for a motionless drag,
// shortened by how far past the window's edge the pointer is held.
func (m *mouse) dragRepeatInterval() time.Duration {
	return dragRepeatInterval / time.Duration(min(m.dragOvershoot(), maxDragOvershoot)+1)
}

// dragOvershoot reports how many cells past the window's edge the
// pointer sits, on whichever axis reaches further.
func (m *mouse) dragOvershoot() int {
	raw := m.calculateCoordinates()
	return max(max(-raw.X, raw.X-m.width+1), max(-raw.Y, raw.Y-m.height+1), 0)
}

// wheelEvents accumulates the fractional wheel delta and returns one discrete
// wheel event per whole line crossed. The remainder below one line is retained
// for the next frame so high-resolution devices scroll smoothly instead of
// jumping a full line per frame.
func (m *mouse) wheelEvents(ctx context.Context, pos term.Coordinates, wheelY float64) []term.Event {
	// A non-finite delta would poison the accumulator permanently (NaN
	// propagates, Inf overflows the line count). Drop it and recover the
	// accumulator if a prior frame already poisoned it.
	if !isFinite(wheelY) {
		if !isFinite(m.accumY) {
			m.accumY = 0
		}
		return nil
	}

	m.accumY += wheelY * m.multiplier
	if !isFinite(m.accumY) {
		m.accumY = 0
		return nil
	}

	// Clamp before the int conversion: converting a float64 beyond the
	// int64 range is implementation-defined (amd64 yields MinInt64,
	// whose negation overflows back to MinInt64 and reaches makeslice
	// negative; arm64 saturates). Bounding the accumulator to the
	// per-frame line budget keeps the conversion in range everywhere.
	if m.accumY > maxWheelLinesPerFrame {
		m.accumY = maxWheelLinesPerFrame
	} else if m.accumY < -maxWheelLinesPerFrame {
		m.accumY = -maxWheelLinesPerFrame
	}
	lines := int(m.accumY)
	m.accumY -= float64(lines)
	if lines == 0 {
		return nil
	}

	key := term.MouseWheelUp
	if lines < 0 {
		key = term.MouseWheelDown
		lines = -lines
	}

	events := make([]term.Event, lines)
	for i := range events {
		events[i] = term.Event{
			Type:    term.EventMouse,
			Key:     key,
			MouseX:  pos.X,
			MouseY:  pos.Y,
			Context: ctx,
		}
	}
	return events
}

func isFinite(f float64) bool {
	return !math.IsNaN(f) && !math.IsInf(f, 0)
}

// clampedCoordinates returns the current cursor position in cell coordinates,
// clamped to the bounds of the window.
func (m *mouse) clampedCoordinates() term.Coordinates {
	return m.clamp(m.calculateCoordinates())
}

// clamp bounds a cell position to the window.
func (m *mouse) clamp(pos term.Coordinates) term.Coordinates {
	if pos.X >= m.width {
		pos.X = m.width - 1
	}
	if pos.Y >= m.height {
		pos.Y = m.height - 1
	}
	if pos.X < 0 {
		pos.X = 0
	}
	if pos.Y < 0 {
		pos.Y = 0
	}
	return pos
}

func (m *mouse) calculateCoordinates() (ret term.Coordinates) {
	return m.cellAt(float64(m.state.x), float64(m.state.y))
}

// subCellFraction reports where inside pos's cell the pointer sits. The
// pixel position is clamped to the window so a pointer past an edge reads
// as the edge cell's far side.
func (m *mouse) subCellFraction(pos term.Coordinates) tterm.SubCellFraction {
	pitch := m.fontManager.PixelX(1)
	px := min(max(float64(m.state.x), 0), pitch*float64(m.width)-1)
	return tterm.SubCellFraction{
		X: (px - m.fontManager.PixelX(pos.X)) / pitch,
	}
}

// cellAt converts a pixel position to unclamped cell coordinates.
func (m *mouse) cellAt(x, y float64) (ret term.Coordinates) {
	ret.X = int(m.fontManager.CellX(x))
	ret.Y = int(m.fontManager.CellY(y))
	return
}

func (m *mouse) resize(width, height int) {
	m.width = width
	m.height = height
}
