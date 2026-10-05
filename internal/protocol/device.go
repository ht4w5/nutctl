package protocol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ht4w5/nutctl/internal/hid"
)

// Transfer defaults (docs/protocol.md §2): 500 ms per attempt, 3 retries.
// Every command carries its own per-attempt timeout, quoted from the vendor
// bundle's transfer engine: 500 ms default, 1000 ms for SET_KEY, 2000 ms for
// SET_CUSTOM_LED_DATA and for every transfer on frameVersion-1 firmware.
const (
	DefaultTimeout          = 500 * time.Millisecond
	SetKeyTimeout           = 1000 * time.Millisecond
	SetCustomLEDDataTimeout = 2 * time.Second
	FrameVersion1Timeout    = 2 * time.Second
	DefaultMaxRetries       = 3
)

// Options tunes the transfer engine. The zero value uses the protocol
// defaults.
type Options struct {
	// Timeout overrides the per-attempt timeout of every transfer. Zero uses
	// the documented per-command timeouts above.
	Timeout time.Duration
	// MaxRetries is the total retries after the first attempt.
	MaxRetries int
}

// Device speaks the protocol to one keyboard over a Transport. One goroutine
// owns the wire: transfers are serialized, typed operations hide framing,
// chunking, response matching and retries.
type Device struct {
	t    hid.Transport
	opts Options

	xferMu sync.Mutex // serializes transfers
	waitMu sync.Mutex
	waiter *waiter // currently awaited response, if any

	notify chan []byte // input reports no transfer consumed (Notifications)

	frameVersion uint8 // from GET_DEVICE_INFO; selects the timeout
}

// Open starts a session on t. Close it when done.
func Open(t hid.Transport, opts Options) (*Device, error) {
	if t == nil {
		return nil, errors.New("protocol: nil transport")
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = DefaultMaxRetries
	}
	d := &Device{t: t, opts: opts, notify: make(chan []byte, notifyBuffer)}
	go d.readLoop()
	return d, nil
}

// Close ends the session and closes the underlying transport.
func (d *Device) Close() error {
	return d.t.Close()
}

// notifyBuffer is the unsolicited-report stream's capacity: `nutctl watch`
// drains it live, and when nobody is watching notify traffic is dropped
// rather than ever blocking the wire.
const notifyBuffer = 256

// Notifications delivers the input reports no transfer consumed: device
// notify traffic and anything else the Device pushes unsolicited
// (docs/protocol.md §2). The channel closes when the Device disappears. The
// stream is observability, never a transfer: reports are dropped when no
// reader keeps up.
func (d *Device) Notifications() <-chan []byte { return d.notify }

// readLoop owns reading input reports: matching responses go to the current
// waiter, everything else (garbage, notifications) goes to the Notifications
// stream — the transfer layer times out and retries on garbage, exactly like
// the vendor app, whose response waiter only accepts well-formed reports for
// the awaited command.
func (d *Device) readLoop() {
	defer close(d.notify)
	for raw := range d.t.Reports() {
		resp, err := ParseResponse(raw)
		if err == nil {
			d.waitMu.Lock()
			w := d.waiter
			d.waitMu.Unlock()
			if w != nil && w.matches(resp) {
				select {
				case w.ch <- raw:
				default:
				}
				continue
			}
		}
		select {
		case d.notify <- raw:
		default:
		}
	}
}

// waiter matches one awaited response.
type waiter struct {
	cmd  uint8
	addr *uint16 // non-nil: also match response addr
	ch   chan []byte
}

func (w *waiter) matches(resp Response) bool {
	return resp.Cmd == w.cmd && (w.addr == nil || resp.Addr == *w.addr)
}

func (d *Device) setWaiter(w *waiter) {
	d.waitMu.Lock()
	d.waiter = w
	d.waitMu.Unlock()
}

func (d *Device) clearWaiter(w *waiter) {
	d.waitMu.Lock()
	if d.waiter == w {
		d.waiter = nil
	}
	d.waitMu.Unlock()
}

// transferSpec describes one chunked request/response exchange (docs/protocol.md
// §2): the request is split into chunks of reportLen-header bytes, each chunk
// is answered by one response report, and the responses are reassembled by
// concatenating bytes 8.. of each report and truncating to contentSize.
type transferSpec struct {
	cmd         byte
	contentSize int
	addrStart   uint16
	data        []byte // request payload; nil for reads

	customHeader []byte
	otherHeader  []byte

	responseCmd        byte // 0 → same as cmd
	checkAddr          bool
	skipResponse       bool
	needLastPacketFlag bool

	timeout time.Duration // documented per-attempt timeout of this command
}

// do runs one transfer and returns the raw response reports (one per chunk).
func (d *Device) runTransfer(ctx context.Context, tr transferSpec) ([][]byte, error) {
	d.xferMu.Lock()
	defer d.xferMu.Unlock()

	reportLen := d.t.ReportLength()
	headerLen := HeaderSize
	if len(tr.customHeader) > 0 {
		headerLen = len(tr.customHeader)
	}
	chunkPayload := reportLen - headerLen
	if chunkPayload <= 0 {
		return nil, fmt.Errorf("protocol: report length %d leaves no payload capacity", reportLen)
	}
	if tr.contentSize <= 0 {
		return nil, fmt.Errorf("protocol: content size must be positive, got %d", tr.contentSize)
	}

	timeout := tr.timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if d.frameVersion == 1 && timeout < FrameVersion1Timeout {
		timeout = FrameVersion1Timeout
	}
	if d.opts.Timeout > 0 {
		timeout = d.opts.Timeout
	}

	var responses [][]byte
	nChunks := (tr.contentSize + chunkPayload - 1) / chunkPayload
	for i := 0; i < nChunks; i++ {
		chunkAddr := tr.addrStart + uint16(i*chunkPayload)
		remaining := tr.contentSize - i*chunkPayload
		length := chunkPayload
		if remaining < length {
			length = remaining
		}
		var payload []byte
		if tr.data != nil {
			lo := i * chunkPayload
			if lo < len(tr.data) {
				hi := min(lo+chunkPayload, len(tr.data))
				payload = tr.data[lo:hi]
			}
		}
		chunk, err := (Request{
			Cmd:          tr.cmd,
			Length:       uint8(length),
			Addr:         chunkAddr,
			Payload:      payload,
			OtherHeader:  tr.otherHeader,
			CustomHeader: tr.customHeader,
			LastPacket:   tr.needLastPacketFlag && i == nChunks-1,
		}).Marshal(reportLen)
		if err != nil {
			return nil, err
		}

		if tr.skipResponse {
			if err := d.t.SendReport(0, chunk); err != nil {
				return nil, fmt.Errorf("send command %#x: %w", tr.cmd, err)
			}
			continue
		}

		raw, err := d.roundTrip(ctx, tr, chunk, chunkAddr, timeout)
		if err != nil {
			return nil, err
		}
		responses = append(responses, raw)
	}
	return responses, nil
}

// roundTrip sends one chunk and awaits its response, retrying up to
// MaxRetries times on timeout. The waiter is registered before the send, so a
// fast response is never missed.
func (d *Device) roundTrip(ctx context.Context, tr transferSpec, chunk []byte, chunkAddr uint16, timeout time.Duration) ([]byte, error) {
	responseCmd := tr.responseCmd
	if responseCmd == 0 {
		responseCmd = tr.cmd
	}
	for attempt := 0; attempt <= d.opts.MaxRetries; attempt++ {
		w := &waiter{cmd: responseCmd, ch: make(chan []byte, 1)}
		if tr.checkAddr {
			addr := chunkAddr
			w.addr = &addr
		}
		d.setWaiter(w)

		timer := time.NewTimer(timeout)
		sendErr := d.t.SendReport(0, chunk)
		if sendErr != nil {
			d.clearWaiter(w)
			timer.Stop()
			return nil, fmt.Errorf("send command %#x (attempt %d/%d): %w",
				tr.cmd, attempt+1, d.opts.MaxRetries+1, sendErr)
		}

		var raw []byte
		select {
		case raw = <-w.ch:
		case <-timer.C:
		case <-ctx.Done():
			d.clearWaiter(w)
			timer.Stop()
			return nil, ctx.Err()
		}
		d.clearWaiter(w)
		timer.Stop()
		if raw != nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("no response to command %#x after %d retries (timeout %s per attempt)",
		tr.cmd, d.opts.MaxRetries, timeout)
}

// Info reads the Device Identity block (GET_DEVICE_INFO). The reported
// frameVersion selects the transfer timeout for later operations.
func (d *Device) Info(ctx context.Context) (DeviceInfo, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetDeviceInfo,
		contentSize:        DeviceInfoSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("GET_DEVICE_INFO: %w", err)
	}
	info, err := DecodeDeviceInfo(reassemble(responses, DeviceInfoSize))
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("GET_DEVICE_INFO: %w", err)
	}
	d.frameVersion = info.FrameVersion
	return info, nil
}

// Settings reads the Device's persistent behaviour block (GET_GAME_MODE).
func (d *Device) Settings(ctx context.Context) (Settings, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetGameMode,
		contentSize:        SettingsSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return Settings{}, fmt.Errorf("GET_GAME_MODE: %w", err)
	}
	settings, err := DecodeSettings(reassemble(responses, SettingsSize))
	if err != nil {
		return Settings{}, fmt.Errorf("GET_GAME_MODE: %w", err)
	}
	return settings, nil
}

// Keymap reads the Base Layer Key Action table (GET_KEY): 128 Key Slots.
func (d *Device) Keymap(ctx context.Context) (Keymap, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetKey,
		contentSize:        KeymapSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return Keymap{}, fmt.Errorf("GET_KEY: %w", err)
	}
	keymap, err := DecodeKeymap(reassemble(responses, KeymapSize))
	if err != nil {
		return Keymap{}, fmt.Errorf("GET_KEY: %w", err)
	}
	return keymap, nil
}

// FnKeymap reads the Fn Layer Key Action table (GET_FN_KEY): 128 Key Slots.
func (d *Device) FnKeymap(ctx context.Context) (Keymap, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetFnKey,
		contentSize:        KeymapSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return Keymap{}, fmt.Errorf("GET_FN_KEY: %w", err)
	}
	keymap, err := DecodeKeymap(reassemble(responses, KeymapSize))
	if err != nil {
		return Keymap{}, fmt.Errorf("GET_FN_KEY: %w", err)
	}
	return keymap, nil
}

// LightingEffect reads the Lighting Effect block (GET_LED_EFFECT): 16 bytes.
// The check code lands in CheckCodeOK; enforcing it is the self-check's job
// (internal/device), not the codec's.
func (d *Device) LightingEffect(ctx context.Context) (LightingEffect, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetLEDEffect,
		contentSize:        LEDEffectSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return LightingEffect{}, fmt.Errorf("GET_LED_EFFECT: %w", err)
	}
	effect, err := DecodeLightingEffect(reassemble(responses, LEDEffectSize))
	if err != nil {
		return LightingEffect{}, fmt.Errorf("GET_LED_EFFECT: %w", err)
	}
	return effect, nil
}

// PerKeyRGB reads the Per-Key RGB table (GET_CUSTOM_LED_DATA): 128 entries.
func (d *Device) PerKeyRGB(ctx context.Context) (PerKeyRGB, error) {
	responses, err := d.runTransfer(ctx, transferSpec{
		cmd:                CmdGetCustomLEDData,
		contentSize:        PerKeyRGBSize,
		needLastPacketFlag: true,
	})
	if err != nil {
		return PerKeyRGB{}, fmt.Errorf("GET_CUSTOM_LED_DATA: %w", err)
	}
	rgb, err := DecodePerKeyRGB(reassemble(responses, PerKeyRGBSize))
	if err != nil {
		return PerKeyRGB{}, fmt.Errorf("GET_CUSTOM_LED_DATA: %w", err)
	}
	return rgb, nil
}

// writeSpec is a batched write of one complete block (docs/protocol.md §4):
// the block goes out as one chunked transfer, each chunk answered by one ack.
func writeSpec(cmd byte, size int, data []byte, timeout time.Duration) transferSpec {
	return transferSpec{
		cmd:                cmd,
		contentSize:        size,
		data:               data,
		needLastPacketFlag: true,
		timeout:            timeout,
	}
}

// SetKeymap writes the Base Layer Key Action table (SET_KEY): one batched
// 512-byte transfer, 1000 ms per chunk as the vendor engine does.
func (d *Device) SetKeymap(ctx context.Context, km Keymap) error {
	if err := d.runTransferErr(ctx, "SET_KEY", writeSpec(CmdSetKey, KeymapSize, EncodeKeymap(km), SetKeyTimeout)); err != nil {
		return err
	}
	return nil
}

// SetFnKeymap writes the Fn Layer Key Action table (SET_FN_KEY): one batched
// 512-byte transfer.
func (d *Device) SetFnKeymap(ctx context.Context, km Keymap) error {
	return d.runTransferErr(ctx, "SET_FN_KEY", writeSpec(CmdSetFnKey, KeymapSize, EncodeKeymap(km), DefaultTimeout))
}

// SetLightingEffect writes the Lighting Effect block (SET_LED_EFFECT): one
// 16-byte transfer. The check code the block carries is the SET wire
// format's (docs/protocol.md §4), not the state read back.
func (d *Device) SetLightingEffect(ctx context.Context, e LightingEffect) error {
	return d.runTransferErr(ctx, "SET_LED_EFFECT", writeSpec(CmdSetLEDEffect, LEDEffectSize, EncodeLightingEffect(e), DefaultTimeout))
}

// SetPerKeyRGB writes the Per-Key RGB table (SET_CUSTOM_LED_DATA): one
// batched 512-byte transfer, 2000 ms per chunk as the vendor engine does.
func (d *Device) SetPerKeyRGB(ctx context.Context, rgb PerKeyRGB) error {
	return d.runTransferErr(ctx, "SET_CUSTOM_LED_DATA", writeSpec(CmdSetCustomLEDData, PerKeyRGBSize, EncodePerKeyRGB(rgb), SetCustomLEDDataTimeout))
}

// SetSettings writes the Settings block (SET_GAME_MODE): one 56-byte
// transfer.
func (d *Device) SetSettings(ctx context.Context, s Settings) error {
	return d.runTransferErr(ctx, "SET_GAME_MODE", writeSpec(CmdSetGameMode, SettingsSize, EncodeSettings(s), DefaultTimeout))
}

// runTransferErr runs one transfer and names the command in the failure the
// way the typed reads do ("SET_KEY: …").
func (d *Device) runTransferErr(ctx context.Context, name string, tr transferSpec) error {
	if _, err := d.runTransfer(ctx, tr); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// reassemble concatenates the data of each response report and truncates to
// size (docs/protocol.md §2).
func reassemble(responses [][]byte, size int) []byte {
	var out []byte
	for _, raw := range responses {
		resp, err := ParseResponse(raw)
		if err != nil {
			continue
		}
		out = append(out, resp.Data...)
	}
	if len(out) > size {
		out = out[:size]
	}
	return out
}
