package protocol

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ht4w5/nutctl/internal/hid"
)

// Transfer defaults (docs/protocol.md §2): 500 ms per attempt, 3 retries;
// firmware with frameVersion 1 answers slower and gets 2000 ms. SET_KEY gets
// 1000 ms — wired in when the write path lands.
const (
	DefaultTimeout       = 500 * time.Millisecond
	FrameVersion1Timeout = 2 * time.Second
	DefaultMaxRetries    = 3
)

// Options tunes the transfer engine. The zero value uses the protocol
// defaults.
type Options struct {
	Timeout    time.Duration // per attempt
	MaxRetries int           // total retries after the first attempt
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

	frameVersion uint8 // from GET_DEVICE_INFO; selects the timeout
}

// Open starts a session on t. Close it when done.
func Open(t hid.Transport, opts Options) (*Device, error) {
	if t == nil {
		return nil, errors.New("protocol: nil transport")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = DefaultMaxRetries
	}
	d := &Device{t: t, opts: opts}
	go d.readLoop()
	return d, nil
}

// Close ends the session and closes the underlying transport.
func (d *Device) Close() error {
	return d.t.Close()
}

// readLoop owns reading input reports: matching responses go to the current
// waiter, everything else (garbage, notifications) is dropped — the transfer
// layer times out and retries on garbage, exactly like the vendor app, whose
// response waiter only accepts well-formed reports for the awaited command.
func (d *Device) readLoop() {
	for raw := range d.t.Reports() {
		resp, err := ParseResponse(raw)
		if err != nil {
			continue
		}
		d.waitMu.Lock()
		w := d.waiter
		d.waitMu.Unlock()
		if w == nil || !w.matches(resp) {
			continue
		}
		select {
		case w.ch <- raw:
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

	timeout := d.opts.Timeout
	if d.frameVersion == 1 {
		timeout = FrameVersion1Timeout
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
