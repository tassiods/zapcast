// SPDX-FileCopyrightText: 2025 ZapCast contributors
// SPDX-License-Identifier: Apache-2.0

package server

// Configures the platform identifier WhatsApp shows in "Dispositivos
// vinculados" — set once at boot, only affects NEW pairings.

import (
	"runtime"

	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// selectDeviceProps maps a GOOS string to the PlatformType + human OS label
// advertised during QR pairing. Extracted so every branch is reachable from
// tests without recompiling per platform.
func selectDeviceProps(goos string) (waCompanionReg.DeviceProps_PlatformType, string) {
	switch goos {
	case "darwin":
		return waCompanionReg.DeviceProps_CATALINA, "macOS"
	case "windows":
		return waCompanionReg.DeviceProps_DESKTOP, "Windows"
	default:
		return waCompanionReg.DeviceProps_DESKTOP, "Linux"
	}
}

// configureDeviceProps writes the chosen PlatformType + Os string into the
// whatsmeow store. Idempotent — overwriting is fine because every pairing
// snapshots these values into the new session.
func configureDeviceProps() {
	ptype, osName := selectDeviceProps(runtime.GOOS)
	store.DeviceProps.PlatformType = ptype.Enum()
	store.DeviceProps.Os = proto.String("ZapCast — " + osName)
}
