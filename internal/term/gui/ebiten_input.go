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
	ebiten "github.com/hajimehoshi/ebiten/v2"
)

var _ keysManager = ebitenInputManager{}
var _ mouseManager = ebitenInputManager{}

type ebitenInputManager struct {
}

func (e ebitenInputManager) AppendInputEvents(buf []ebiten.InputEvent) []ebiten.InputEvent {
	return ebiten.AppendInputEvents(buf)
}

func (e ebitenInputManager) KeyName(key ebiten.Key) string {
	return ebiten.KeyName(key)
}

func (e ebitenInputManager) Wheel() (float64, float64) {
	return ebiten.Wheel()
}

func (e ebitenInputManager) CursorPosition() (int, int) {
	return ebiten.CursorPosition()
}

func (e ebitenInputManager) IsMouseButtonPressed(button ebiten.MouseButton) bool {
	return ebiten.IsMouseButtonPressed(button)
}
