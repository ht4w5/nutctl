# NUT87 Driver

Open-source driver and configurator for the WEIKAV NUT87, an 87-key TKL keyboard with
a knob (USB and 2.4G). It exists because the vendor's web configurator is unusable on
Linux.

## Language

**Device**:
The physical keyboard being configured (a NUT87 today), or its dongle when that is
what stands on the wire.
_Avoid_: board, unit

**Model**:
A keyboard product (the NUT87 is ours) defined by its Device Identity, layout table
and capability set. New Models are data, not code.
_Avoid_: variant, board, device type

**Device Identity**:
The firmware-reported tuple (vendor id, product id, product name, manufacturer, product)
that determines which Model a connected device is. Models share USB product ids, so the
name fields are decisive.
_Avoid_: productName, VID/PID (when meaning identity)

**Connection**:
How a Device is reached: USB or 2.4G Dongle. The NUT87 has no Bluetooth.
_Avoid_: connectType, mode

**2.4G Dongle**:
The wireless receiver that speaks the protocol on the keyboard's behalf.
_Avoid_: receiver, adapter

**Key Slot**:
One addressable entry (0–127) of the Device's binding tables; a physical key or knob
gesture maps to exactly one.
_Avoid_: key id, value

**Key Action**:
What a Key Slot is bound to: a keyboard key, consumer key, mouse button, Macro,
Advanced Key, or Function.
_Avoid_: userKey, mapping

**Layer**:
A complete Key Slot table. The NUT87 has a Base layer and an Fn layer.
_Avoid_: profile, level

**Knob**:
The rotary encoder, with three gestures: clockwise, press, counter-clockwise.
_Avoid_: wheel, dial

**Macro**:
A stored sequence of key events with delays, bindable as a Key Action. 100 slots.
_Avoid_: shortcut

**Advanced Key**:
A composite Key Action: SOCD, MT (mod-tap), TGL (toggle), CB (combo).
_Avoid_: senior key

**Function**:
A firmware-defined behaviour bindable to a key (lighting controls, OS switch, lock Win),
identified by a 24-bit id.
_Avoid_: FUNC, hotkey

**Lighting Effect**:
The global lighting configuration: effect mode, primary/secondary colors, brightness 1–6,
speed 1–6, direction.
_Avoid_: LED effect, light mode

**Per-Key RGB**:
The 128-entry custom lighting table of per-slot colors.
_Avoid_: custom LED data, matrix

**Settings**:
The Device's persistent behaviour block: Report Rate, key delay, sleep, Fn switch, and
related switches.
_Avoid_: game mode (vendor term for the same block)

**State File**:
A plain file holding a complete snapshot of one Device's state (Layers, Lighting
Effect, Per-Key RGB, Settings, Macros), read or written only when the user explicitly
saves or loads it. The Device itself is the source of truth.
_Avoid_: preset, profile, config store

**Report Rate**:
The polling rate offered to the host: 1K, 2K, 4K or 8K. The NUT87 offers 1K/4K/8K.
_Avoid_: polling rate
